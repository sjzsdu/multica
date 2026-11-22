package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/squadworkflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// loadSquadWorkflowPolicy resolves the policy as a domain value. Missing rows
// intentionally mean the historical manual workflow, which keeps new server
// versions compatible with squads created by older versions.
func (h *Handler) loadSquadWorkflowPolicy(ctx context.Context, squad db.Squad) squadworkflow.Policy {
	return h.loadSquadWorkflowPolicyWithQueries(ctx, h.Queries, squad)
}

func (h *Handler) loadSquadWorkflowPolicyWithQueries(ctx context.Context, q *db.Queries, squad db.Squad) squadworkflow.Policy {
	row, err := q.GetSquadWorkflowPolicy(ctx, db.GetSquadWorkflowPolicyParams{
		SquadID: squad.ID, WorkspaceID: squad.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return squadworkflow.DefaultPolicy()
	}
	if err != nil {
		slog.Warn("load squad workflow policy failed", "squad_id", uuidToString(squad.ID), "error", err)
		return squadworkflow.DefaultPolicy()
	}
	policy, err := squadworkflow.Parse(row.Config)
	if err != nil {
		slog.Warn("invalid squad workflow policy; using default", "squad_id", uuidToString(squad.ID), "error", err)
		return squadworkflow.DefaultPolicy()
	}
	return policy
}

func (h *Handler) workflowPoliciesBySquad(ctx context.Context, workspaceID pgtype.UUID) map[string]squadworkflow.Policy {
	rows, err := h.Queries.ListSquadWorkflowPolicies(ctx, workspaceID)
	if err != nil {
		slog.Warn("list squad workflow policies failed", "workspace_id", uuidToString(workspaceID), "error", err)
		return nil
	}
	policies := make(map[string]squadworkflow.Policy, len(rows))
	for _, row := range rows {
		policy, err := squadworkflow.Parse(row.Config)
		if err != nil {
			slog.Warn("invalid squad workflow policy; using default", "squad_id", uuidToString(row.SquadID), "error", err)
			policy = squadworkflow.DefaultPolicy()
		}
		policies[uuidToString(row.SquadID)] = policy
	}
	return policies
}

// evaluateWorkflowTransition adapts persistence rows into the domain
// coordinator. Status-category resolution stays at this boundary so the
// coordinator remains independent of SQL and custom-status storage.
func (h *Handler) evaluateWorkflowTransition(
	ctx context.Context,
	policy squadworkflow.Policy,
	children []db.Issue,
	prevStatus string,
	completed db.Issue,
	evidence squadworkflow.GateEvidence,
) squadworkflow.Decision {
	return h.evaluateWorkflowTransitionWithQueries(ctx, h.Queries, policy, children, prevStatus, completed, evidence)
}

func (h *Handler) evaluateWorkflowTransitionWithQueries(
	ctx context.Context,
	q *db.Queries,
	policy squadworkflow.Policy,
	children []db.Issue,
	prevStatus string,
	completed db.Issue,
	evidence squadworkflow.GateEvidence,
) squadworkflow.Decision {
	snapshots := make([]squadworkflow.Child, 0, len(children))
	for _, child := range children {
		snapshots = append(snapshots, squadworkflow.Child{
			ID:     uuidToString(child.ID),
			Stage:  child.Stage.Int32,
			Staged: child.Stage.Valid,
			Status: issuestatus.Effective(ctx, q, child.WorkspaceID, child.Status),
		})
	}
	return h.WorkflowCoordinator.EvaluateTransition(policy, snapshots, squadworkflow.Transition{
		ChildID:        uuidToString(completed.ID),
		PreviousStatus: issuestatus.Effective(ctx, q, completed.WorkspaceID, prevStatus),
		CurrentStatus:  issuestatus.Effective(ctx, q, completed.WorkspaceID, completed.Status),
	}, evidence)
}

func (h *Handler) evaluateWorkflowTransitionWithApprovals(
	ctx context.Context,
	policy squadworkflow.Policy,
	children []db.Issue,
	prevStatus string,
	completed db.Issue,
	parent db.Issue,
) (squadworkflow.Decision, squadworkflow.GateEvidence) {
	return h.evaluateWorkflowTransitionWithApprovalsAndQueries(ctx, h.Queries, policy, children, prevStatus, completed, parent)
}

func (h *Handler) evaluateWorkflowTransitionWithApprovalsAndQueries(
	ctx context.Context,
	q *db.Queries,
	policy squadworkflow.Policy,
	children []db.Issue,
	prevStatus string,
	completed db.Issue,
	parent db.Issue,
) (squadworkflow.Decision, squadworkflow.GateEvidence) {
	evidence := h.workflowGateEvidenceWithQueries(ctx, q, policy, parent)
	decision := h.evaluateWorkflowTransitionWithQueries(ctx, q, policy, children, prevStatus, completed, evidence)
	if !decision.StageClosed {
		return decision, evidence
	}
	evidence = h.workflowGateEvidenceWithQueriesForStage(ctx, q, policy, parent, decision.ClosedStage)
	decision = h.evaluateWorkflowTransitionWithQueries(ctx, q, policy, children, prevStatus, completed, evidence)
	return decision, evidence
}

type squadWorkflowRunGateSnapshot struct {
	PipelineRound *int32               `json:"pipeline_round"`
	MissingGates  []squadworkflow.Gate `json:"missing_gates,omitempty"`
}

func workflowRunMatchesPipelineRound(run db.SquadWorkflowRun, round int) bool {
	if len(run.GateSnapshot) == 0 {
		return false
	}
	var snapshot squadWorkflowRunGateSnapshot
	if err := json.Unmarshal(run.GateSnapshot, &snapshot); err != nil {
		return false
	}
	return snapshot.PipelineRound != nil && int(*snapshot.PipelineRound) == round
}

func hasWorkflowRunForPipelineRound(runs []db.SquadWorkflowRun, round int) bool {
	for _, run := range runs {
		if workflowRunMatchesPipelineRound(run, round) {
			return true
		}
	}
	return false
}

// recordWorkflowRunForCompletedChild persists a genuine opted-in stage closure
// in the same transaction as the child status write. The round in the snapshot
// lets the worker distinguish rework progress from the rejected run that
// originally requested it.
func (h *Handler) recordWorkflowRunForCompletedChild(ctx context.Context, q *db.Queries, previous, completed db.Issue) error {
	if !completed.ParentIssueID.Valid || previous.Status == completed.Status {
		return nil
	}
	parent, err := q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID: completed.ParentIssueID, WorkspaceID: completed.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load parent for workflow run: %w", err)
	}
	policy := h.parentWorkflowPolicyWithQueries(ctx, q, parent)
	if !policy.RequirePipelineVerdict {
		return nil
	}
	children, err := q.ListChildIssues(ctx, parent.ID)
	if err != nil {
		return fmt.Errorf("list children for workflow run: %w", err)
	}
	decision, _ := h.evaluateWorkflowTransitionWithApprovalsAndQueries(ctx, q, policy, children, previous.Status, completed, parent)
	if !decision.StageClosed {
		return nil
	}
	round := int32(pipelineMetadataRound(parseIssueMetadata(parent.Metadata)))
	snapshot, err := json.Marshal(squadWorkflowRunGateSnapshot{
		PipelineRound: &round,
		MissingGates:  decision.Gates.Missing,
	})
	if err != nil {
		return fmt.Errorf("encode workflow run gate snapshot: %w", err)
	}
	_, err = q.UpsertSquadWorkflowRun(ctx, db.UpsertSquadWorkflowRunParams{
		WorkspaceID: parent.WorkspaceID, SquadID: parent.AssigneeID, ParentIssueID: parent.ID,
		ClosedStage: decision.ClosedStage, NextStage: decision.NextStage, State: "pending",
		GateSnapshot: snapshot, NextAttemptAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		return fmt.Errorf("upsert workflow run: %w", err)
	}
	return nil
}

// workflowGateEvidence adapts already-mirrored provider state into the domain
// gate contract. Review approval intentionally remains false until a provider
// exposes a reviewed-by signal; an open or stale snapshot is never treated as
// proof. Missing provider data therefore fails closed for required gates.
func (h *Handler) workflowGateEvidence(ctx context.Context, policy squadworkflow.Policy, issue db.Issue) squadworkflow.GateEvidence {
	return h.workflowGateEvidenceWithQueries(ctx, h.Queries, policy, issue)
}

func (h *Handler) workflowGateEvidenceWithQueries(ctx context.Context, q *db.Queries, policy squadworkflow.Policy, issue db.Issue) squadworkflow.GateEvidence {
	evidence := squadworkflow.GateEvidence{}
	if !policy.RequireTests && !policy.RequireMerge {
		return evidence
	}
	githubPRs, err := q.ListPullRequestsByIssue(ctx, issue.ID)
	if err == nil && len(githubPRs) > 0 {
		evidence.TestsPassed = true
		allMerged := true
		for _, pr := range githubPRs {
			checks := strings.ToLower(strings.TrimSpace(pr.ChecksRollupState.String))
			if !pr.ChecksRollupState.Valid || checks != "success" || pr.ChecksFailed > 0 || pr.ChecksRunning > 0 {
				evidence.TestsPassed = false
			}
			if pr.State != "merged" || !pr.MergedAt.Valid {
				allMerged = false
			}
		}
		if policy.MergeMode != squadworkflow.MergeModeCloseIntent {
			evidence.MergeCompleted = allMerged
		}
	}
	if policy.MergeMode == squadworkflow.MergeModeCloseIntent {
		agg, err := q.GetIssueCombinedPullRequestCloseAggregate(ctx, issue.ID)
		if err == nil && agg.LinkedTotal > 0 && agg.LinkedTotal == agg.LinkedMerged && agg.MergedWithCloseIntent > 0 {
			evidence.MergeCompleted = true
		}
	}
	return evidence
}

func (h *Handler) workflowGateEvidenceWithQueriesForStage(ctx context.Context, q *db.Queries, policy squadworkflow.Policy, issue db.Issue, closedStage int32) squadworkflow.GateEvidence {
	evidence := h.workflowGateEvidenceWithQueries(ctx, q, policy, issue)
	approvals, err := q.ListSquadWorkflowApprovalsByRun(ctx, db.ListSquadWorkflowApprovalsByRunParams{ParentIssueID: issue.ID, ClosedStage: closedStage})
	if err != nil {
		return evidence
	}
	return applyWorkflowApprovalEvidence(evidence, policy, approvals, pipelineMetadataRound(parseIssueMetadata(issue.Metadata)))
}

func allowsHumanWorkflowApproval(policy squadworkflow.Policy, gate string) bool {
	return gate != string(squadworkflow.GateReview) || policy.ReviewAgentID == ""
}

func applyWorkflowApprovalEvidence(evidence squadworkflow.GateEvidence, policy squadworkflow.Policy, approvals []db.SquadWorkflowApproval, currentRound int) squadworkflow.GateEvidence {
	for _, approval := range approvals {
		if !approval.Approved || (policy.RequirePipelineVerdict && int(approval.PipelineRound) != currentRound) {
			continue
		}
		switch approval.Gate {
		case string(squadworkflow.GateReview):
			if policy.ReviewAgentID != "" && (approval.ApprovedByType != "agent" || uuidToString(approval.ApprovedByID) != policy.ReviewAgentID) {
				continue
			}
			evidence.ReviewApproved = true
		case string(squadworkflow.GateHumanApproval):
			evidence.HumanApproved = true
		}
	}
	return evidence
}
