package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/projectaccess"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/squadworkflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	workflowRetryInterval = time.Minute
	workflowRetryLease    = 2 * time.Minute
	workflowRetryDelay    = time.Minute
)

// WorkflowRetryWorker replays durable stage runs that could not promote at
// the original child-done request. The database lease makes this safe across
// multiple server instances; all actual child writes still pass through the
// workspace capacity lock in autoPromoteNextWorkflowStage.
type WorkflowRetryWorker struct {
	h *Handler
}

func NewWorkflowRetryWorker(h *Handler) *WorkflowRetryWorker {
	return &WorkflowRetryWorker{h: h}
}

func (w *WorkflowRetryWorker) Run(ctx context.Context) {
	if w == nil || w.h == nil || w.h.Queries == nil {
		return
	}
	ticker := time.NewTicker(workflowRetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.RunOnce(ctx); err != nil {
				slog.Warn("squad workflow retry failed", "error", err)
			}
		}
	}
}

func (w *WorkflowRetryWorker) RunOnce(ctx context.Context) error {
	recoveryErr := w.recoverRejectedPipelineRework(ctx)
	runErr := w.runNext(ctx)
	return errors.Join(recoveryErr, runErr)
}

// recoverRejectedPipelineRework covers a committed reject whose post-commit
// dispatch failed. It deliberately does not depend on the run being claimable:
// the original run may already be finalized while its rework task is absent.
func (w *WorkflowRetryWorker) recoverRejectedPipelineRework(ctx context.Context) error {
	candidates, err := w.h.Queries.ListRejectedPipelineReworkCandidates(ctx)
	if err != nil {
		return fmt.Errorf("list rejected pipeline rework candidates: %w", err)
	}
	var errs []error
	for _, run := range candidates {
		parent, err := w.h.Queries.GetIssue(ctx, run.ParentIssueID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("load rejected pipeline parent %s: %w", uuidToString(run.ParentIssueID), err))
			continue
		}
		policy := w.h.parentWorkflowPolicy(ctx, parent)
		metadata := parseIssueMetadata(parent.Metadata)
		if !policy.RequirePipelineVerdict || pipelineMetadataStatus(metadata) != pipelineStatusRejected {
			continue
		}
		status := issuestatus.Effective(ctx, w.h.Queries, parent.WorkspaceID, parent.Status)
		if status == issuestatus.Done || status == issuestatus.Cancelled {
			continue
		}
		round := pipelineMetadataRound(metadata)
		hasCurrentRun, err := w.hasCurrentRoundWorkflowRun(ctx, parent, round)
		if err != nil {
			errs = append(errs, fmt.Errorf("check rejected pipeline progress %s: %w", uuidToString(run.ParentIssueID), err))
			continue
		}
		if hasCurrentRun {
			continue
		}
		if err := w.dispatchWorkflowAction(ctx, run, parent, policy, "rework", round); err != nil {
			errs = append(errs, fmt.Errorf("recover rejected pipeline run %s: %w", uuidToString(run.ID), err))
		}
	}
	return errors.Join(errs...)
}

func (w *WorkflowRetryWorker) hasCurrentRoundWorkflowRun(ctx context.Context, parent db.Issue, round int) (bool, error) {
	runs, err := w.h.Queries.ListSquadWorkflowRunsByParent(ctx, db.ListSquadWorkflowRunsByParentParams{
		ParentIssueID: parent.ID, WorkspaceID: parent.WorkspaceID,
	})
	if err != nil {
		return false, err
	}
	return hasWorkflowRunForPipelineRound(runs, round), nil
}

func (w *WorkflowRetryWorker) resumeRejectedPipelineRun(ctx context.Context, run db.SquadWorkflowRun, parent db.Issue, round int) (db.Issue, bool, error) {
	if w.h.TxStarter == nil {
		return parent, false, errors.New("pipeline resume requires a transaction starter")
	}
	tx, err := w.h.TxStarter.Begin(ctx)
	if err != nil {
		return parent, false, fmt.Errorf("begin rejected pipeline resume: %w", err)
	}
	defer tx.Rollback(ctx)
	q := w.h.Queries.WithTx(tx)
	if _, err := q.LockWorkspaceForWorkflowCapacity(ctx, parent.WorkspaceID); err != nil {
		return parent, false, fmt.Errorf("lock workspace for rejected pipeline resume: %w", err)
	}
	current, err := q.LockIssueForDescriptionUpdate(ctx, db.LockIssueForDescriptionUpdateParams{
		ID: parent.ID, WorkspaceID: parent.WorkspaceID,
	})
	if err != nil {
		return parent, false, fmt.Errorf("lock parent for rejected pipeline resume: %w", err)
	}
	latestRun, err := q.GetSquadWorkflowRun(ctx, db.GetSquadWorkflowRunParams{ID: run.ID, WorkspaceID: run.WorkspaceID})
	if err != nil {
		return current, false, fmt.Errorf("reload workflow run for rejected pipeline resume: %w", err)
	}
	metadata := parseIssueMetadata(current.Metadata)
	policy := w.h.parentWorkflowPolicyWithQueries(ctx, q, current)
	status := issuestatus.Effective(ctx, q, current.WorkspaceID, current.Status)
	pipelineStatus := pipelineMetadataStatus(metadata)
	if !policy.RequirePipelineVerdict || pipelineMetadataRound(metadata) != round ||
		!workflowRunMatchesPipelineRound(latestRun, round) || status == issuestatus.Done || status == issuestatus.Cancelled ||
		(pipelineStatus != pipelineStatusRejected && pipelineStatus != "") {
		return current, false, nil
	}
	if pipelineStatus == pipelineStatusRejected {
		updated, err := q.DeleteIssueMetadataKey(ctx, db.DeleteIssueMetadataKeyParams{
			ID: current.ID, WorkspaceID: current.WorkspaceID, Key: pipelineStatusMetadataKey,
		})
		if err != nil {
			return current, false, fmt.Errorf("clear rejected pipeline status: %w", err)
		}
		current.Metadata = updated.Metadata
		current.Revision = updated.Revision
	}
	if err := tx.Commit(ctx); err != nil {
		return current, false, fmt.Errorf("commit rejected pipeline resume: %w", err)
	}
	if pipelineStatus == pipelineStatusRejected {
		w.h.publishPipelineMetadataChanged(current, "system", "", parseIssueMetadata(current.Metadata), current.Revision)
	}
	return current, true, nil
}

func (w *WorkflowRetryWorker) runNext(ctx context.Context) error {
	leaseUntil := pgtype.Timestamptz{Time: time.Now().UTC().Add(workflowRetryLease), Valid: true}
	run, err := w.h.Queries.ClaimNextDueSquadWorkflowRun(ctx, leaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("claim squad workflow run: %w", err)
	}

	parent, err := w.h.Queries.GetIssue(ctx, run.ParentIssueID)
	if err != nil {
		return w.deferRun(ctx, run, "load parent issue: "+err.Error(), "failed")
	}
	policy := w.h.parentWorkflowPolicy(ctx, parent)
	if !policy.AutoPromoteStage {
		return w.finishRun(ctx, run, "awaiting_manual", "automatic promotion disabled")
	}
	if policy.RequirePipelineVerdict {
		metadata := parseIssueMetadata(parent.Metadata)
		status := issuestatus.Effective(ctx, w.h.Queries, parent.WorkspaceID, parent.Status)
		if status == issuestatus.Done || status == issuestatus.Cancelled {
			return w.finishRun(ctx, run, "finalized", "parent issue is terminal")
		}
		switch pipelineMetadataStatus(metadata) {
		case pipelineStatusEscalated:
			return w.finishRun(ctx, run, "awaiting_manual", "pipeline escalated; awaiting human takeover")
		case pipelineStatusAccepted:
			return w.finishRun(ctx, run, "finalized", "pipeline accepted")
		case pipelineStatusRejected:
			round := pipelineMetadataRound(metadata)
			if workflowRunMatchesPipelineRound(run, round) {
				var resumed bool
				parent, resumed, err = w.resumeRejectedPipelineRun(ctx, run, parent, round)
				if err != nil {
					return w.deferRun(ctx, run, err.Error(), "failed")
				}
				if !resumed {
					return w.finishRun(ctx, run, "failed", "rejected pipeline round changed before resume")
				}
				break
			}
			hasCurrentRun, err := w.hasCurrentRoundWorkflowRun(ctx, parent, round)
			if err != nil {
				return w.deferRun(ctx, run, err.Error(), "failed")
			}
			if hasCurrentRun {
				return w.finishRun(ctx, run, "finalized", "superseded by a current-round workflow run")
			}
			if err := w.dispatchWorkflowAction(ctx, run, parent, policy, "rework", round); err != nil {
				return w.deferRun(ctx, run, err.Error(), "failed")
			}
			return w.finishRun(ctx, run, "finalized", "dispatched pipeline rework")
		}
	}
	decision, err := w.h.workflowDecisionForRun(ctx, run, parent, policy)
	if err != nil {
		return w.deferRun(ctx, run, err.Error(), "failed")
	}
	if !decision.StageClosed {
		return w.finishRun(ctx, run, "failed", "closed stage is no longer reproducible")
	}
	if policy.RequirePipelineVerdict {
		round := pipelineMetadataRound(parseIssueMetadata(parent.Metadata))
		if workflowGateMissing(decision.Gates.Missing, squadworkflow.GateTests) {
			if err := w.dispatchWorkflowAction(ctx, run, parent, policy, "stabilize", round); err != nil {
				return w.deferRun(ctx, run, err.Error(), "failed")
			}
			return w.deferRun(ctx, run, "dispatched delivery stabilization", "blocked")
		}
		if workflowGateMissing(decision.Gates.Missing, squadworkflow.GateReview) {
			if err := w.dispatchWorkflowAction(ctx, run, parent, policy, "review", round); err != nil {
				return w.deferRun(ctx, run, err.Error(), "failed")
			}
			return w.deferRun(ctx, run, "dispatched independent review", "blocked")
		}
		if workflowGateMissing(decision.Gates.Missing, squadworkflow.GateMerge) {
			if err := w.dispatchWorkflowAction(ctx, run, parent, policy, "integrate", round); err != nil {
				return w.deferRun(ctx, run, err.Error(), "failed")
			}
			return w.deferRun(ctx, run, "dispatched delivery integration", "blocked")
		}
		if decision.CanFinalize && policy.AutoFinalizeParent {
			finalized, err := w.h.autoFinalizeParent(ctx, parent, policy, decision)
			if err != nil {
				return w.deferRun(ctx, run, err.Error(), "failed")
			}
			if !finalized {
				return w.deferRun(ctx, run, "automatic parent finalization did not complete", "failed")
			}
			if err := w.dispatchWorkflowAction(ctx, run, parent, policy, "accept", round); err != nil {
				return w.deferRun(ctx, run, err.Error(), "failed")
			}
			return w.deferRun(ctx, run, "awaiting product verdict", "blocked")
		}
	}
	if decision.CanFinalize {
		if policy.AutoFinalizeParent {
			finalized, err := w.h.autoFinalizeParent(ctx, parent, policy, decision)
			if err != nil {
				return w.deferRun(ctx, run, err.Error(), "failed")
			}
			if finalized {
				return w.finishRun(ctx, run, "finalized", "parent moved to in_review")
			}
		}
		return w.finishRun(ctx, run, "ready_for_finalization", "all configured gates passed")
	}
	if !decision.CanPromote {
		return w.deferRun(ctx, run, "waiting for gates: "+strings.Join(gateNames(decision.Gates.Missing), ", "), "blocked")
	}

	promoted, err := w.h.autoPromoteNextWorkflowStage(ctx, parent, policy, decision)
	if err != nil {
		return w.deferRun(ctx, run, err.Error(), "failed")
	}
	if len(promoted) == 0 {
		// The most common reason is that max_active_children is currently full.
		// Keep the run pending so a later retry can observe a newly freed slot.
		return w.deferRun(ctx, run, "no child was promoted; capacity or provider state may have changed", "pending")
	}
	if _, err := w.h.Queries.MarkClaimedSquadWorkflowRunPromoted(ctx, db.MarkClaimedSquadWorkflowRunPromotedParams{
		ID: run.ID, LeaseToken: run.LeaseToken, GateSnapshot: run.GateSnapshot,
	}); err != nil {
		return fmt.Errorf("mark promoted workflow run: %w", err)
	}
	return nil
}

func workflowGateMissing(missing []squadworkflow.Gate, gate squadworkflow.Gate) bool {
	for _, candidate := range missing {
		if candidate == gate {
			return true
		}
	}
	return false
}

func (w *WorkflowRetryWorker) dispatchWorkflowAction(ctx context.Context, run db.SquadWorkflowRun, parent db.Issue, policy squadworkflow.Policy, action string, round int) error {
	return w.dispatchWorkflowActionWithReason(ctx, run, parent, policy, action, round, "")
}

func (w *WorkflowRetryWorker) dispatchWorkflowActionWithReason(ctx context.Context, run db.SquadWorkflowRun, parent db.Issue, policy squadworkflow.Policy, action string, round int, reason string) error {
	if w.h.TaskService == nil {
		return errors.New("workflow dispatch requires a task service")
	}
	squad, err := w.h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: run.SquadID, WorkspaceID: run.WorkspaceID})
	if err != nil {
		return fmt.Errorf("load squad for workflow dispatch: %w", err)
	}
	agentID := squad.LeaderID
	isLeader := true
	switch action {
	case "stabilize", "integrate", "rework":
	case "review":
		agentID = parseUUID(policy.ReviewAgentID)
		isLeader = false
	case "accept":
		agentID = parseUUID(policy.Pipeline.ProductAgentID)
		isLeader = agentID == squad.LeaderID
	default:
		return fmt.Errorf("unknown workflow dispatch action %q", action)
	}
	if !agentID.Valid {
		return fmt.Errorf("workflow dispatch %s has no agent", action)
	}
	if !isLeader {
		member, err := w.h.Queries.IsSquadMember(ctx, db.IsSquadMemberParams{SquadID: squad.ID, MemberType: "agent", MemberID: agentID})
		if err != nil {
			return fmt.Errorf("check workflow dispatch membership: %w", err)
		}
		if !member {
			return fmt.Errorf("workflow dispatch %s agent is not a squad member", action)
		}
	}
	actorUserID := pgtype.UUID{}
	if parent.CreatorType == "member" {
		actorUserID = parent.CreatorID
	}
	dispatchKey := fmt.Sprintf("%s:%s:%d", action, uuidToString(run.ID), round)
	note := fmt.Sprintf("Workflow %s for run %s, round %d. Revalidate the durable evidence and record the result through the workflow API.", action, uuidToString(run.ID), round)
	if action == "rework" {
		if reason == "" {
			reason = "Read the latest pipeline rejection activity for the rejection reason."
		}
		note = fmt.Sprintf("Pipeline rework for run %s, round %d. Rejection reason: %s\nReopen and correct the relevant staged children, close the final stage again, and revalidate CI, independent review, merge, deployment, and product acceptance for this round.", uuidToString(run.ID), round, reason)
	}
	if action == "review" {
		pullRequests, err := w.h.Queries.ListPullRequestsByIssue(ctx, parent.ID)
		if err != nil {
			return fmt.Errorf("load review pull request evidence: %w", err)
		}
		evidence := make([]string, 0, len(pullRequests))
		for _, pr := range pullRequests {
			evidence = append(evidence, fmt.Sprintf("- %s | head SHA: %s | CI rollup: %s", pr.HtmlUrl, pr.HeadSha, pr.ChecksRollupState.String))
		}
		if len(evidence) == 0 {
			evidence = append(evidence, "- No linked GitHub PR evidence was found.")
		}
		runID := uuidToString(run.ID)
		issueID := uuidToString(parent.ID)
		note = fmt.Sprintf("Independent review for run %s, round %d, parent issue %s.\nAcceptance criteria: read the stage 1 product-definition child issue description and comments.\nPR / head SHA / CI evidence:\n%s\nDeployment evidence: read the stage 4 deployment pre-validation child issue description and comments, including environment URL, validated version, health checks, and rollback notes.\nRevalidate the current durable evidence. Record exactly one current-round result with:\nmultica issue review %s approve --run %s --round %d\nor:\nmultica issue review %s reject --run %s --round %d --reason \"<evidence-backed reason>\"\nPASS/FAIL comments are explanatory only and do not satisfy the review gate.", runID, round, issueID, strings.Join(evidence, "\n"), issueID, runID, round, issueID, runID, round)
	}
	_, err = w.h.TaskService.EnqueueTaskForWorkflowAgent(ctx, parent, agentID, squad.ID, isLeader, dispatchKey, note, actorUserID)
	if errors.Is(err, service.ErrDuplicatePendingTask) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("dispatch workflow %s: %w", action, err)
	}
	return nil
}

func (w *WorkflowRetryWorker) finishRun(ctx context.Context, run db.SquadWorkflowRun, state, message string) error {
	_, err := w.h.Queries.MarkClaimedSquadWorkflowRunBlocked(ctx, db.MarkClaimedSquadWorkflowRunBlockedParams{
		ID: run.ID, LeaseToken: run.LeaseToken, State: state, GateSnapshot: run.GateSnapshot,
		NextAttemptAt: pgtype.Timestamptz{Time: time.Now().UTC().Add(workflowRetryDelay), Valid: true},
		LastError:     pgtype.Text{String: message, Valid: message != ""},
	})
	return err
}

func (w *WorkflowRetryWorker) deferRun(ctx context.Context, run db.SquadWorkflowRun, message, state string) error {
	if err := w.finishRun(ctx, run, state, message); err != nil {
		return fmt.Errorf("defer workflow run: %w", err)
	}
	return nil
}

func (h *Handler) workflowDecisionForRun(ctx context.Context, run db.SquadWorkflowRun, parent db.Issue, policy squadworkflow.Policy) (squadworkflow.Decision, error) {
	return h.workflowDecisionForRunWithQueries(ctx, h.Queries, run, parent, policy)
}

func (h *Handler) workflowDecisionForRunWithQueries(ctx context.Context, q *db.Queries, run db.SquadWorkflowRun, parent db.Issue, policy squadworkflow.Policy) (squadworkflow.Decision, error) {
	children, err := q.ListChildIssues(ctx, parent.ID)
	if err != nil {
		return squadworkflow.Decision{}, fmt.Errorf("list children for workflow retry: %w", err)
	}
	var completed db.Issue
	for _, child := range children {
		status := issuestatus.Effective(ctx, q, child.WorkspaceID, child.Status)
		if policy.IsCompletionStatus(status) && ((!child.Stage.Valid && run.ClosedStage == 0) || (child.Stage.Valid && child.Stage.Int32 == run.ClosedStage)) {
			completed = child
			break
		}
	}
	if !completed.ID.Valid {
		return squadworkflow.Decision{}, nil
	}
	decision, _ := h.evaluateWorkflowTransitionWithApprovalsAndQueries(ctx, q, policy, children, "todo", completed, parent)
	return decision, nil
}

func gateNames(gates []squadworkflow.Gate) []string {
	names := make([]string, 0, len(gates))
	for _, gate := range gates {
		names = append(names, string(gate))
	}
	return names
}

// parentWorkflowPolicy resolves the squad workflow policy for a parent issue.
// Ported from the deleted issue_child_done.go; kept because the retry worker
// still needs to read the policy to decide whether to auto-promote or
// auto-finalize. Returns DefaultPolicy when the parent is not squad-assigned.
func (h *Handler) parentWorkflowPolicy(ctx context.Context, parent db.Issue) squadworkflow.Policy {
	return h.parentWorkflowPolicyWithQueries(ctx, h.Queries, parent)
}

func (h *Handler) parentWorkflowPolicyWithQueries(ctx context.Context, q *db.Queries, parent db.Issue) squadworkflow.Policy {
	if !parent.AssigneeType.Valid || parent.AssigneeType.String != "squad" || !parent.AssigneeID.Valid {
		return squadworkflow.DefaultPolicy()
	}
	squad, err := q.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID: parent.AssigneeID, WorkspaceID: parent.WorkspaceID,
	})
	if err != nil {
		return squadworkflow.DefaultPolicy()
	}
	return h.loadSquadWorkflowPolicyWithQueries(ctx, q, squad)
}

// autoFinalizeParent advances a completed squad parent to in_review when the
// policy opts in and all gates pass. Ported from the deleted
// issue_child_done.go. The worker calls this after a stage closes; the
// transaction serializes against concurrent child promotions via the
// workspace workflow-capacity lock.
func (h *Handler) autoFinalizeParent(ctx context.Context, parent db.Issue, policy squadworkflow.Policy, decision squadworkflow.Decision) (bool, error) {
	if !policy.AutoFinalizeParent || !decision.CanFinalize || h.TxStarter == nil {
		return false, nil
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin automatic parent finalization: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.LockWorkspaceForWorkflowCapacity(ctx, parent.WorkspaceID); err != nil {
		return false, fmt.Errorf("lock workspace for automatic parent finalization: %w", err)
	}
	current, err := qtx.LockIssueForDescriptionUpdate(ctx, db.LockIssueForDescriptionUpdateParams{ID: parent.ID, WorkspaceID: parent.WorkspaceID})
	if err != nil {
		return false, fmt.Errorf("lock parent for automatic finalization: %w", err)
	}
	status := issuestatus.Effective(ctx, qtx, current.WorkspaceID, current.Status)
	if status == "backlog" || status == "done" || status == "cancelled" {
		return false, nil
	}
	latestPolicy := h.parentWorkflowPolicyWithQueries(ctx, qtx, current)
	if !latestPolicy.AutoFinalizeParent {
		return false, nil
	}
	latestChildren, err := qtx.ListChildIssues(ctx, current.ID)
	if err != nil {
		return false, fmt.Errorf("list children for automatic finalization: %w", err)
	}
	isComplete := func(child db.Issue) bool {
		return latestPolicy.IsCompletionStatus(issuestatus.Effective(ctx, qtx, child.WorkspaceID, child.Status))
	}
	if siblingsAreStaged(latestChildren) {
		for _, child := range latestChildren {
			if child.Stage.Valid && !isComplete(child) {
				return false, nil
			}
		}
	} else {
		for _, child := range latestChildren {
			if !isComplete(child) {
				return false, nil
			}
		}
	}
	gates := h.WorkflowCoordinator.Gates
	if gates == nil {
		gates = squadworkflow.PolicyGateEvaluator{}
	}
	if !gates.Evaluate(latestPolicy, h.workflowGateEvidenceWithQueriesForStage(ctx, qtx, latestPolicy, current, decision.ClosedStage)).Passed {
		return false, nil
	}
	if latestPolicy.RequirePipelineVerdict && status == "in_review" {
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit automatic parent finalization: %w", err)
		}
		return true, nil
	}
	updated, err := qtx.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: current.ID, Status: "in_review", WorkspaceID: current.WorkspaceID})
	if err != nil {
		return false, fmt.Errorf("move parent to in_review: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit automatic parent finalization: %w", err)
	}
	response := issueToResponse(updated, h.getIssuePrefix(ctx, updated.WorkspaceID))
	h.fillStatusCategory(ctx, updated.WorkspaceID, projectaccess.Hidden{}, &response)
	h.publish(protocol.EventIssueUpdated, uuidToString(updated.WorkspaceID), "system", "", map[string]any{
		"issue": response, "status_changed": true, "prev_status": current.Status,
	})
	return true, nil
}

// autoPromoteNextWorkflowStage advances the next staged backlog group after
// the coordinator proves the closed barrier and all gates are satisfied.
// Ported from the deleted issue_child_done.go. Serialized against concurrent
// child-done notifications by the workspace workflow-capacity lock.
func (h *Handler) autoPromoteNextWorkflowStage(ctx context.Context, parent db.Issue, policy squadworkflow.Policy, decision squadworkflow.Decision) ([]db.Issue, error) {
	if !policy.AutoPromoteStage || !decision.CanPromote || decision.NextStage <= 0 || h.TxStarter == nil {
		return nil, nil
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin automatic stage promotion: %w", err)
	}
	defer tx.Rollback(ctx)

	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.LockWorkspaceForWorkflowCapacity(ctx, parent.WorkspaceID); err != nil {
		return nil, fmt.Errorf("lock workspace for automatic stage promotion: %w", err)
	}
	lockedParent, err := qtx.LockIssueForDescriptionUpdate(ctx, db.LockIssueForDescriptionUpdateParams{
		ID: parent.ID, WorkspaceID: parent.WorkspaceID,
	})
	if err != nil {
		return nil, fmt.Errorf("lock parent for automatic stage promotion: %w", err)
	}
	parentStatus := issuestatus.Effective(ctx, qtx, lockedParent.WorkspaceID, lockedParent.Status)
	if parentStatus == "backlog" || parentStatus == "done" || parentStatus == "cancelled" {
		return nil, nil
	}
	latestPolicy := h.parentWorkflowPolicyWithQueries(ctx, qtx, lockedParent)
	if !latestPolicy.AutoPromoteStage {
		return nil, nil
	}
	gateEvaluator := h.WorkflowCoordinator.Gates
	if gateEvaluator == nil {
		gateEvaluator = squadworkflow.PolicyGateEvaluator{}
	}
	gates := evaluateWorkflowPromotionGates(gateEvaluator, latestPolicy, h.workflowGateEvidenceWithQueriesForStage(ctx, qtx, latestPolicy, lockedParent, decision.ClosedStage), decision.NextStage)
	if !gates.Passed {
		return nil, nil
	}

	latestChildren, err := qtx.ListChildIssues(ctx, lockedParent.ID)
	if err != nil {
		return nil, fmt.Errorf("list children for automatic stage promotion: %w", err)
	}
	promoted := make([]db.Issue, 0)
	for _, child := range latestChildren {
		if !child.Stage.Valid || child.Stage.Int32 != decision.NextStage {
			continue
		}
		if issuestatus.Effective(ctx, qtx, child.WorkspaceID, child.Status) != "backlog" {
			continue
		}
		if err := service.CheckActiveChildCapacity(ctx, qtx, lockedParent, "todo", child.ID); err != nil {
			if errors.Is(err, service.ErrActiveChildrenLimitExceeded) {
				break
			}
			return nil, fmt.Errorf("check automatic stage capacity: %w", err)
		}
		updated, err := qtx.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
			ID: child.ID, Status: "todo", WorkspaceID: child.WorkspaceID,
		})
		if err != nil {
			return nil, fmt.Errorf("promote child %s to todo: %w", uuidToString(child.ID), err)
		}
		promoted = append(promoted, updated)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit automatic stage promotion: %w", err)
	}

	fillStatus := h.newStatusCategoryFiller(ctx, parent.WorkspaceID, projectaccess.Hidden{})
	for _, child := range promoted {
		response := issueToResponse(child, h.getIssuePrefix(ctx, child.WorkspaceID))
		fillStatus(&response)
		h.publish(protocol.EventIssueUpdated, uuidToString(child.WorkspaceID), "system", "", map[string]any{
			"issue":          response,
			"status_changed": true,
			"prev_status":    "backlog",
		})
		if h.IssueService == nil {
			continue
		}
		if trigger, ok := h.IssueService.WillEnqueueRun(ctx, service.IssueTriggerInput{
			Issue: child, PrevStatus: "backlog", StatusChanged: true,
		}, service.IssueTriggerProbe{}); ok {
			h.dispatchIssueRun(ctx, child, trigger, "system", "", "")
		}
	}
	return promoted, nil
}

func evaluateWorkflowPromotionGates(gateEvaluator squadworkflow.GateEvaluator, policy squadworkflow.Policy, evidence squadworkflow.GateEvidence, nextStage int32) squadworkflow.GateResult {
	if policy.DefersFinalGates(nextStage) {
		return squadworkflow.GateResult{Passed: true}
	}
	return gateEvaluator.Evaluate(policy, evidence)
}

// siblingsAreStaged reports whether any child in the set carries an explicit
// stage. A set with no stages is treated as a single implicit stage.
func siblingsAreStaged(children []db.Issue) bool {
	for _, c := range children {
		if c.Stage.Valid {
			return true
		}
	}
	return false
}
