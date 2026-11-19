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
	decision, err := w.h.workflowDecisionForRun(ctx, run, parent, policy)
	if err != nil {
		return w.deferRun(ctx, run, err.Error(), "failed")
	}
	if !decision.StageClosed {
		return w.finishRun(ctx, run, "failed", "closed stage is no longer reproducible")
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
		ID: run.ID, LeaseToken: run.LeaseToken,
	}); err != nil {
		return fmt.Errorf("mark promoted workflow run: %w", err)
	}
	return nil
}

func (w *WorkflowRetryWorker) finishRun(ctx context.Context, run db.SquadWorkflowRun, state, message string) error {
	_, err := w.h.Queries.MarkClaimedSquadWorkflowRunBlocked(ctx, db.MarkClaimedSquadWorkflowRunBlockedParams{
		ID: run.ID, LeaseToken: run.LeaseToken, State: state,
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
	children, err := h.Queries.ListChildIssues(ctx, parent.ID)
	if err != nil {
		return squadworkflow.Decision{}, fmt.Errorf("list children for workflow retry: %w", err)
	}
	var completed db.Issue
	for _, child := range children {
		status := issuestatus.Effective(ctx, h.Queries, child.WorkspaceID, child.Status)
		if policy.IsCompletionStatus(status) && ((!child.Stage.Valid && run.ClosedStage == 0) || (child.Stage.Valid && child.Stage.Int32 == run.ClosedStage)) {
			completed = child
			break
		}
	}
	if !completed.ID.Valid {
		return squadworkflow.Decision{}, nil
	}
	decision, _ := h.evaluateWorkflowTransitionWithApprovals(ctx, policy, children, "todo", completed, parent)
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
	updated, err := qtx.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: current.ID, Status: "in_review", WorkspaceID: current.WorkspaceID})
	if err != nil {
		return false, fmt.Errorf("move parent to in_review: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit automatic parent finalization: %w", err)
	}
	response := issueToResponse(updated, h.getIssuePrefix(ctx, updated.WorkspaceID))
	h.fillStatusCategory(ctx, updated.WorkspaceID, &response)
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
	if !gateEvaluator.Evaluate(latestPolicy, h.workflowGateEvidenceWithQueriesForStage(ctx, qtx, latestPolicy, lockedParent, decision.ClosedStage)).Passed {
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

	for _, child := range promoted {
		response := issueToResponse(child, h.getIssuePrefix(ctx, child.WorkspaceID))
		h.fillStatusCategory(ctx, child.WorkspaceID, &response)
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
