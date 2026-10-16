package handler

import (
	"context"
	"errors"
	"log/slog"
	"strings"

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
	snapshots := make([]squadworkflow.Child, 0, len(children))
	for _, child := range children {
		snapshots = append(snapshots, squadworkflow.Child{
			ID:     uuidToString(child.ID),
			Stage:  child.Stage.Int32,
			Staged: child.Stage.Valid,
			Status: issuestatus.Effective(ctx, h.Queries, child.WorkspaceID, child.Status),
		})
	}
	return h.WorkflowCoordinator.EvaluateTransition(policy, snapshots, squadworkflow.Transition{
		ChildID:        uuidToString(completed.ID),
		PreviousStatus: issuestatus.Effective(ctx, h.Queries, completed.WorkspaceID, prevStatus),
		CurrentStatus:  issuestatus.Effective(ctx, h.Queries, completed.WorkspaceID, completed.Status),
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
	evidence := h.workflowGateEvidence(ctx, policy, parent)
	decision := h.evaluateWorkflowTransition(ctx, policy, children, prevStatus, completed, evidence)
	if !decision.StageClosed {
		return decision, evidence
	}
	evidence = h.workflowGateEvidenceWithQueriesForStage(ctx, h.Queries, policy, parent, decision.ClosedStage)
	decision = h.evaluateWorkflowTransition(ctx, policy, children, prevStatus, completed, evidence)
	return decision, evidence
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
	for _, approval := range approvals {
		if !approval.Approved {
			continue
		}
		switch approval.Gate {
		case string(squadworkflow.GateReview):
			evidence.ReviewApproved = true
		case string(squadworkflow.GateHumanApproval):
			evidence.HumanApproved = true
		}
	}
	return evidence
}
