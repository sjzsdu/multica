package handler

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/squadworkflow"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func enableBlackboxWorkerPolicy(t *testing.T, f pipelineReviewFixture) {
	t.Helper()
	dbfx.Exec(t, `
		UPDATE squad_workflow_policy
		SET config = jsonb_build_object(
			'mode', 'delivery',
			'max_active_children', 1,
			'child_completion_signal', 'review_ready',
			'auto_promote_stage', true,
			'auto_finalize_parent', true,
			'require_tests', true,
			'require_review', true,
			'require_merge', true,
			'merge_mode', 'integrator',
			'human_approval_required', false,
			'final_gates_only', true,
			'review_agent_id', $2::text,
			'require_pipeline_verdict', true,
			'pipeline', jsonb_build_object('max_rounds', 3, 'product_agent_id', $3::text)
		)
		WHERE squad_id = $1
	`, f.squadID, f.reviewerID, f.agentID)
}

func countWorkflowDispatches(t *testing.T, f pipelineReviewFixture, action, agentID string) int {
	t.Helper()
	return dbfx.Count(t, `
		SELECT count(*)
		FROM agent_task_queue
		WHERE issue_id = $1
		  AND agent_id = $2
		  AND context->>'workflow_dispatch_key' = $3
	`, f.issueID, agentID, action+":"+f.runID+":0")
}

func linkWorkflowPullRequest(t *testing.T, f pipelineReviewFixture, state string) {
	t.Helper()
	now := time.Now()
	var mergedAt any
	if state == "merged" {
		mergedAt = now
	}
	prID := dbfx.Insert(t, "github_pull_request", testutil.Cols{
		"workspace_id": testWorkspaceID, "installation_id": 0,
		"repo_owner": "acme", "repo_name": f.runID, "pr_number": 1,
		"title": "blackbox delivery", "state": state, "html_url": "https://github.test/" + f.runID,
		"head_sha": "head-" + f.runID, "checks_rollup_state": "success", "pr_created_at": now, "pr_updated_at": now,
		"merged_at": mergedAt,
	})
	dbfx.InsertNoID(t, "issue_pull_request", testutil.Cols{"issue_id": f.issueID, "pull_request_id": prID},
		"pull_request_id = $1", prID)
}

func approveWorkflowReview(t *testing.T, f pipelineReviewFixture) {
	t.Helper()
	dbfx.Insert(t, "squad_workflow_approval", testutil.Cols{
		"workspace_id": testWorkspaceID, "squad_id": f.squadID, "parent_issue_id": f.issueID,
		"closed_stage": 3, "gate": "review", "approved": true,
		"approved_by_type": "agent", "approved_by_id": f.reviewerID, "pipeline_round": 0,
	})
}

func wakeWorkflowRunForTest(t *testing.T, runID string) {
	t.Helper()
	dbfx.Exec(t, `UPDATE squad_workflow_run SET next_attempt_at = '-infinity', lease_token = NULL WHERE id = $1`, runID)
}

func TestWorkflowRunUpsertPreservesActiveLeaseAndFencesStaleClaim(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineReviewFixture(t)
	dbfx.Exec(t, `UPDATE squad_workflow_run SET next_attempt_at = '-infinity' WHERE id = $1`, f.runID)
	leaseUntil := pgtype.Timestamptz{Time: time.Now().UTC().Add(time.Minute), Valid: true}
	claimed, err := testHandler.Queries.ClaimNextDueSquadWorkflowRun(t.Context(), leaseUntil)
	if err != nil {
		t.Fatalf("claim workflow run: %v", err)
	}
	if uuidToString(claimed.ID) != f.runID {
		t.Fatalf("claimed run = %s, want %s", uuidToString(claimed.ID), f.runID)
	}

	upserted, err := testHandler.Queries.UpsertSquadWorkflowRun(t.Context(), db.UpsertSquadWorkflowRunParams{
		WorkspaceID: claimed.WorkspaceID, SquadID: claimed.SquadID, ParentIssueID: claimed.ParentIssueID,
		ClosedStage: claimed.ClosedStage, NextStage: claimed.NextStage, State: "pending",
		GateSnapshot:  []byte(`{"pipeline_round":1}`),
		NextAttemptAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		t.Fatalf("upsert claimed workflow run: %v", err)
	}
	if upserted.LeaseToken != claimed.LeaseToken {
		t.Fatalf("lease after upsert = %v, want active lease %v", upserted.LeaseToken, claimed.LeaseToken)
	}
	if upserted.State != claimed.State {
		t.Fatalf("state after upsert = %q, want claimed state %q", upserted.State, claimed.State)
	}
	if !upserted.NextAttemptAt.Time.Equal(claimed.NextAttemptAt.Time) {
		t.Fatalf("next attempt after upsert = %s, want lease deadline %s", upserted.NextAttemptAt.Time, claimed.NextAttemptAt.Time)
	}

	_, err = testHandler.Queries.MarkClaimedSquadWorkflowRunBlocked(t.Context(), db.MarkClaimedSquadWorkflowRunBlockedParams{
		ID: claimed.ID, LeaseToken: claimed.LeaseToken, State: "finalized", GateSnapshot: claimed.GateSnapshot,
		NextAttemptAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale claim finish error = %v, want no rows", err)
	}
	current, err := testHandler.Queries.GetSquadWorkflowRun(t.Context(), db.GetSquadWorkflowRunParams{
		ID: claimed.ID, WorkspaceID: claimed.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("reload workflow run: %v", err)
	}
	if !workflowRunMatchesPipelineRound(current, 1) || current.State != claimed.State || current.LeaseToken != claimed.LeaseToken {
		t.Fatalf("run after stale finish = state %q, snapshot %s, lease %v", current.State, current.GateSnapshot, current.LeaseToken)
	}
}

func TestWorkflowRetryWorkerDispatchesStabilization(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineReviewFixture(t)
	enableBlackboxWorkerPolicy(t, f)
	createStagedChild(t, f.issueID, 3, "done")
	wakeWorkflowRunForTest(t, f.runID)

	if err := NewWorkflowRetryWorker(testHandler).RunOnce(t.Context()); err != nil {
		t.Fatalf("run workflow worker: %v", err)
	}
	if got := countWorkflowDispatches(t, f, "stabilize", f.leaderID); got != 1 {
		t.Fatalf("stabilization dispatches = %d, want 1", got)
	}
	var leaderTask bool
	dbfx.QueryRow(t, `
		SELECT is_leader_task FROM agent_task_queue
		WHERE issue_id = $1 AND context->>'workflow_dispatch_key' = $2
	`, f.issueID, "stabilize:"+f.runID+":0").Scan(&leaderTask)
	if !leaderTask {
		t.Fatal("stabilization must be dispatched as a squad leader task")
	}
}

func TestWorkflowRetryWorkerDispatchesIndependentReviewOnceAcrossCompletedReplay(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineReviewFixture(t)
	enableBlackboxWorkerPolicy(t, f)
	createStagedChild(t, f.issueID, 3, "done")
	linkWorkflowPullRequest(t, f, "open")
	wakeWorkflowRunForTest(t, f.runID)
	worker := NewWorkflowRetryWorker(testHandler)

	if err := worker.RunOnce(t.Context()); err != nil {
		t.Fatalf("run workflow worker: %v", err)
	}
	if got := countWorkflowDispatches(t, f, "review", f.reviewerID); got != 1 {
		t.Fatalf("review dispatches = %d, want 1", got)
	}
	var handoffNote string
	dbfx.QueryRow(t, `
		SELECT COALESCE(handoff_note, '') FROM agent_task_queue
		WHERE issue_id = $1 AND context->>'workflow_dispatch_key' = $2
	`, f.issueID, "review:"+f.runID+":0").Scan(&handoffNote)
	for _, want := range []string{
		"run " + f.runID + ", round 0",
		"stage 1 product-definition child issue",
		"https://github.test/" + f.runID,
		"head SHA: head-" + f.runID,
		"CI rollup: success",
		"stage 4 deployment pre-validation child issue",
		"multica issue review " + f.issueID + " approve --run " + f.runID + " --round 0",
		"multica issue review " + f.issueID + " reject --run " + f.runID + " --round 0",
	} {
		if !strings.Contains(handoffNote, want) {
			t.Errorf("review handoff note missing %q:\n%s", want, handoffNote)
		}
	}
	dbfx.Exec(t, `
		UPDATE agent_task_queue SET status = 'completed', completed_at = now()
		WHERE issue_id = $1 AND context->>'workflow_dispatch_key' = $2
	`, f.issueID, "review:"+f.runID+":0")
	wakeWorkflowRunForTest(t, f.runID)
	if err := worker.RunOnce(t.Context()); err != nil {
		t.Fatalf("replay workflow worker: %v", err)
	}
	if got := countWorkflowDispatches(t, f, "review", f.reviewerID); got != 1 {
		t.Fatalf("review dispatches after completed replay = %d, want 1", got)
	}
	if got := countWorkflowDispatches(t, f, "stabilize", f.leaderID); got != 0 {
		t.Fatalf("green CI incorrectly dispatched stabilization %d times", got)
	}
}

func TestWorkflowRetryWorkerDispatchesIntegrationAfterApprovedReview(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineReviewFixture(t)
	enableBlackboxWorkerPolicy(t, f)
	createStagedChild(t, f.issueID, 3, "done")
	linkWorkflowPullRequest(t, f, "open")
	approveWorkflowReview(t, f)
	wakeWorkflowRunForTest(t, f.runID)

	if err := NewWorkflowRetryWorker(testHandler).RunOnce(t.Context()); err != nil {
		t.Fatalf("run workflow worker: %v", err)
	}
	if got := countWorkflowDispatches(t, f, "integrate", f.leaderID); got != 1 {
		t.Fatalf("integration dispatches = %d, want 1", got)
	}
	if got := countWorkflowDispatches(t, f, "review", f.reviewerID); got != 0 {
		t.Fatalf("approved review incorrectly dispatched another reviewer %d times", got)
	}
}

func TestWorkflowRetryWorkerFinalizesAndDispatchesProductAcceptanceOnce(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineReviewFixture(t)
	enableBlackboxWorkerPolicy(t, f)
	createStagedChild(t, f.issueID, 3, "done")
	linkWorkflowPullRequest(t, f, "merged")
	approveWorkflowReview(t, f)
	wakeWorkflowRunForTest(t, f.runID)
	worker := NewWorkflowRetryWorker(testHandler)

	if err := worker.RunOnce(t.Context()); err != nil {
		t.Fatalf("run workflow worker: %v", err)
	}
	if got := pipelineIssueStatus(t, f.issueID); got != "in_review" {
		t.Fatalf("parent status = %q, want in_review", got)
	}
	if got := countWorkflowDispatches(t, f, "accept", f.agentID); got != 1 {
		t.Fatalf("acceptance dispatches = %d, want 1", got)
	}
	var runState string
	dbfx.QueryRow(t, `SELECT state FROM squad_workflow_run WHERE id = $1`, f.runID).Scan(&runState)
	if runState != "blocked" {
		t.Fatalf("run state = %q while awaiting verdict, want blocked", runState)
	}

	dbfx.Exec(t, `
		UPDATE agent_task_queue SET status = 'completed', completed_at = now()
		WHERE issue_id = $1 AND context->>'workflow_dispatch_key' = $2
	`, f.issueID, "accept:"+f.runID+":0")
	wakeWorkflowRunForTest(t, f.runID)
	if err := worker.RunOnce(t.Context()); err != nil {
		t.Fatalf("replay workflow worker: %v", err)
	}
	if got := countWorkflowDispatches(t, f, "accept", f.agentID); got != 1 {
		t.Fatalf("acceptance dispatches after completed replay = %d, want 1", got)
	}
	if got := countWorkflowDispatches(t, f, "integrate", f.leaderID); got != 0 {
		t.Fatalf("merged delivery incorrectly dispatched integration %d times", got)
	}
}

func TestWorkflowRetryWorkerStopsDispatchingAfterPipelineSettles(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	tests := []struct {
		name         string
		status       string
		pipeline     string
		wantRunState string
	}{
		{name: "escalated", status: "in_progress", pipeline: pipelineStatusEscalated, wantRunState: "awaiting_manual"},
		{name: "accepted", status: "in_review", pipeline: pipelineStatusAccepted, wantRunState: "finalized"},
		{name: "done", status: "done", wantRunState: "finalized"},
		{name: "cancelled", status: "cancelled", wantRunState: "finalized"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPipelineReviewFixture(t)
			enableBlackboxWorkerPolicy(t, f)
			createStagedChild(t, f.issueID, 3, "done")
			dbfx.Exec(t, `
				UPDATE issue
				SET status = $2, metadata = metadata || jsonb_build_object('pipeline_status', $3::text)
				WHERE id = $1
			`, f.issueID, tt.status, tt.pipeline)
			wakeWorkflowRunForTest(t, f.runID)

			if err := NewWorkflowRetryWorker(testHandler).RunOnce(t.Context()); err != nil {
				t.Fatalf("run workflow worker: %v", err)
			}
			if got := dbfx.Count(t, `
				SELECT count(*) FROM agent_task_queue
				WHERE issue_id = $1 AND context ? 'workflow_dispatch_key'
			`, f.issueID); got != 0 {
				t.Fatalf("settled pipeline dispatches = %d, want 0", got)
			}
			var runState string
			dbfx.QueryRow(t, `SELECT state FROM squad_workflow_run WHERE id = $1`, f.runID).Scan(&runState)
			if runState != tt.wantRunState {
				t.Fatalf("run state = %q, want %q", runState, tt.wantRunState)
			}
		})
	}
}

func TestWorkflowRetryWorkerDispatchesReworkForRejectedRound(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineReviewFixture(t)
	enableBlackboxWorkerPolicy(t, f)
	createStagedChild(t, f.issueID, 3, "done")
	dbfx.Exec(t, `
		UPDATE issue
		SET metadata = metadata || '{"pipeline_round":1,"pipeline_status":"rejected"}'::jsonb
		WHERE id = $1
	`, f.issueID)
	wakeWorkflowRunForTest(t, f.runID)

	if err := NewWorkflowRetryWorker(testHandler).RunOnce(t.Context()); err != nil {
		t.Fatalf("run workflow worker: %v", err)
	}
	if got := dbfx.Count(t, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 AND context->>'workflow_dispatch_key' = $3
	`, f.issueID, f.leaderID, "rework:"+f.runID+":1"); got != 1 {
		t.Fatalf("rework dispatches = %d, want 1", got)
	}
	for _, action := range []string{"stabilize", "review", "integrate", "accept"} {
		if got := dbfx.Count(t, `
			SELECT count(*) FROM agent_task_queue
			WHERE issue_id = $1 AND context->>'workflow_dispatch_key' LIKE $2
		`, f.issueID, action+":%"); got != 0 {
			t.Fatalf("rejected round dispatched %s %d times, want 0", action, got)
		}
	}
}

func TestWorkflowRetryWorkerRecoversRejectedReworkAfterRunFinalized(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineReviewFixture(t)
	enableBlackboxWorkerPolicy(t, f)
	dbfx.Exec(t, `
		UPDATE issue
		SET metadata = metadata || '{"pipeline_round":1,"pipeline_status":"rejected"}'::jsonb
		WHERE id = $1
	`, f.issueID)
	dbfx.Exec(t, `
		UPDATE squad_workflow_run
		SET state = 'finalized', next_attempt_at = now() + interval '1 hour', lease_token = NULL
		WHERE id = $1
	`, f.runID)
	worker := NewWorkflowRetryWorker(testHandler)

	if err := worker.RunOnce(t.Context()); err != nil {
		t.Fatalf("recover finalized rejected run: %v", err)
	}
	dispatchKey := "rework:" + f.runID + ":1"
	if got := dbfx.Count(t, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 AND context->>'workflow_dispatch_key' = $3
	`, f.issueID, f.leaderID, dispatchKey); got != 1 {
		t.Fatalf("recovered rework dispatches = %d, want 1", got)
	}

	dbfx.Exec(t, `
		UPDATE agent_task_queue SET status = 'completed', completed_at = now()
		WHERE issue_id = $1 AND context->>'workflow_dispatch_key' = $2
	`, f.issueID, dispatchKey)
	if err := worker.RunOnce(t.Context()); err != nil {
		t.Fatalf("replay rejected recovery: %v", err)
	}
	if got := dbfx.Count(t, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND context->>'workflow_dispatch_key' = $2
	`, f.issueID, dispatchKey); got != 1 {
		t.Fatalf("rework dispatches after completed replay = %d, want 1", got)
	}
}

func TestWorkflowBlackboxReworkStartsANewReviewAndAcceptanceRound(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineReviewFixture(t)
	enableBlackboxWorkerPolicy(t, f)
	child := createStagedChild(t, f.issueID, 3, "done")
	linkWorkflowPullRequest(t, f, "merged")
	callWorkflowReview(t, f, f.reviewerID, f.reviewerTaskID, map[string]any{
		"approved": false, "note": "rework the deployment evidence", "round": 0,
	}).Want(http.StatusAccepted)

	callPipelineStatusUpdate(t, f.pipelineFixture, child.ID, "in_progress", "human").Want(http.StatusOK)
	callPipelineStatusUpdate(t, f.pipelineFixture, child.ID, "done", "human").Want(http.StatusOK)
	wakeWorkflowRunForTest(t, f.runID)
	worker := NewWorkflowRetryWorker(testHandler)
	if err := worker.RunOnce(t.Context()); err != nil {
		t.Fatalf("run reworked workflow worker: %v", err)
	}
	metadata := pipelineMetadata(t, f.issueID)
	if got := metadata[pipelineStatusMetadataKey]; got != nil {
		t.Fatalf("pipeline_status after a new stage closure = %v, want cleared", got)
	}
	if got := dbfx.Count(t, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND context->>'workflow_dispatch_key' = $2
	`, f.issueID, "review:"+f.runID+":1"); got != 1 {
		t.Fatalf("round 1 review dispatches = %d, want 1", got)
	}

	callWorkflowReview(t, f, f.reviewerID, f.reviewerTaskID, map[string]any{
		"approved": true, "note": "current deployment evidence passes", "round": 1,
	}).Want(http.StatusAccepted)
	wakeWorkflowRunForTest(t, f.runID)
	if err := worker.RunOnce(t.Context()); err != nil {
		t.Fatalf("run approved workflow worker: %v", err)
	}
	if got := pipelineIssueStatus(t, f.issueID); got != "in_review" {
		t.Fatalf("parent status = %q, want in_review", got)
	}
	if got := dbfx.Count(t, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND context->>'workflow_dispatch_key' = $2
	`, f.issueID, "accept:"+f.runID+":1"); got != 1 {
		t.Fatalf("round 1 acceptance dispatches = %d, want 1", got)
	}

	callPipelineVerdict(t, f.pipelineFixture, f.agentID, f.taskID, map[string]any{
		"verdict": "accept", "round": 1,
	}).Want(http.StatusOK)
	if got := pipelineIssueStatus(t, f.issueID); got != "done" {
		t.Fatalf("parent status after acceptance = %q, want done", got)
	}
	if got := pipelineMetadata(t, f.issueID)[pipelineStatusMetadataKey]; got != pipelineStatusAccepted {
		t.Fatalf("pipeline_status after acceptance = %v, want accepted", got)
	}
}

func TestWorkflowBlackboxReworkCanResumeFromIntermediateStage(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineReviewFixture(t)
	enableBlackboxWorkerPolicy(t, f)
	createStagedChild(t, f.issueID, 3, "done")
	callWorkflowReview(t, f, f.reviewerID, f.reviewerTaskID, map[string]any{
		"approved": false, "note": "rework the product definition", "round": 0,
	}).Want(http.StatusAccepted)

	first := createStagedChild(t, f.issueID, 1, "in_progress")
	second := createStagedChild(t, f.issueID, 2, "backlog")
	callPipelineStatusUpdate(t, f.pipelineFixture, first.ID, "done", "human").Want(http.StatusOK)
	dbfx.Exec(t, `
		UPDATE squad_workflow_run SET next_attempt_at = '-infinity', lease_token = NULL
		WHERE parent_issue_id = $1
	`, f.issueID)
	worker := NewWorkflowRetryWorker(testHandler)
	// The old final run may be claimed first; it must yield to the new
	// current-round intermediate run instead of dispatching stale rework.
	for i := 0; i < 2; i++ {
		if err := worker.RunOnce(t.Context()); err != nil {
			t.Fatalf("run intermediate rework worker: %v", err)
		}
	}
	if got := pipelineMetadata(t, f.issueID)[pipelineStatusMetadataKey]; got != nil {
		t.Fatalf("pipeline_status after intermediate closure = %v, want cleared", got)
	}
	if got := pipelineIssueStatus(t, second.ID); got != "todo" {
		t.Fatalf("next intermediate child status = %q, want todo", got)
	}
	if got := dbfx.Count(t, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND context->>'workflow_dispatch_key' = $2
	`, f.issueID, "rework:"+f.runID+":1"); got != 1 {
		t.Fatalf("rework dispatches after intermediate resume = %d, want 1", got)
	}
}

func TestWorkflowApprovalEvidenceUsesCurrentRoundAndIndependentReviewer(t *testing.T) {
	reviewerID := parseUUID("11111111-1111-1111-1111-111111111111")
	otherID := parseUUID("22222222-2222-2222-2222-222222222222")
	policy := squadworkflow.Policy{
		RequireReview: true, RequirePipelineVerdict: true,
		ReviewAgentID: uuidToString(reviewerID),
	}
	approval := db.SquadWorkflowApproval{
		Gate: "review", Approved: true, ApprovedByType: "agent",
		ApprovedByID: reviewerID, PipelineRound: 2,
	}
	if got := applyWorkflowApprovalEvidence(squadworkflow.GateEvidence{}, policy, []db.SquadWorkflowApproval{approval}, 2); !got.ReviewApproved {
		t.Fatal("current-round approval from configured reviewer must satisfy review gate")
	}
	if got := applyWorkflowApprovalEvidence(squadworkflow.GateEvidence{}, policy, []db.SquadWorkflowApproval{approval}, 3); got.ReviewApproved {
		t.Fatal("previous-round approval must not satisfy a new pipeline round")
	}
	approval.ApprovedByID = otherID
	if got := applyWorkflowApprovalEvidence(squadworkflow.GateEvidence{}, policy, []db.SquadWorkflowApproval{approval}, 2); got.ReviewApproved {
		t.Fatal("another agent must not satisfy the independent review gate")
	}
}

func TestWorkflowApprovalEvidenceLegacyIgnoresPipelineRound(t *testing.T) {
	approval := db.SquadWorkflowApproval{Gate: "review", Approved: true, ApprovedByType: "member", PipelineRound: 7}
	got := applyWorkflowApprovalEvidence(squadworkflow.GateEvidence{}, squadworkflow.Policy{}, []db.SquadWorkflowApproval{approval}, 2)
	if !got.ReviewApproved {
		t.Fatal("legacy policy must keep accepting existing approval evidence")
	}
}

func TestHumanWorkflowApprovalCannotOverwriteFixedReviewer(t *testing.T) {
	policy := squadworkflow.Policy{ReviewAgentID: "11111111-1111-1111-1111-111111111111"}
	if allowsHumanWorkflowApproval(policy, "review") {
		t.Fatal("human review must not overwrite independent reviewer evidence")
	}
	if !allowsHumanWorkflowApproval(policy, "human_approval") || !allowsHumanWorkflowApproval(squadworkflow.Policy{}, "review") {
		t.Fatal("human approval and legacy review must remain available")
	}
}

func TestWorkflowPromotionGateEvaluationDefersOnlyIntermediateStages(t *testing.T) {
	policy := squadworkflow.Policy{RequireTests: true, FinalGatesOnly: true}
	if got := evaluateWorkflowPromotionGates(squadworkflow.PolicyGateEvaluator{}, policy, squadworkflow.GateEvidence{}, 1); !got.Passed {
		t.Fatal("intermediate promotion must not require final delivery evidence")
	}
	if got := evaluateWorkflowPromotionGates(squadworkflow.PolicyGateEvaluator{}, policy, squadworkflow.GateEvidence{}, 0); got.Passed {
		t.Fatal("finalization must still require delivery evidence")
	}
	if got := evaluateWorkflowPromotionGates(squadworkflow.PolicyGateEvaluator{}, squadworkflow.Policy{RequireTests: true}, squadworkflow.GateEvidence{}, 1); got.Passed {
		t.Fatal("legacy promotion must keep enforcing gates at every stage")
	}
}

// TestSiblingsAreStaged covers the helper used by autoFinalizeParent to decide
// whether staged siblings exist (at least one child has a non-null Stage).
func TestSiblingsAreStaged(t *testing.T) {
	tests := []struct {
		name     string
		children []db.Issue
		want     bool
	}{
		{"empty slice", []db.Issue{}, false},
		{"all children have nil stage", []db.Issue{
			{Stage: pgtype.Int4{Valid: false}},
			{Stage: pgtype.Int4{Valid: false}},
		}, false},
		{"first child has staged", []db.Issue{
			{Stage: pgtype.Int4{Int32: 0, Valid: true}},
			{Stage: pgtype.Int4{Valid: false}},
		}, true},
		{"second child has staged", []db.Issue{
			{Stage: pgtype.Int4{Valid: false}},
			{Stage: pgtype.Int4{Int32: 1, Valid: true}},
		}, true},
		{"all children have staged", []db.Issue{
			{Stage: pgtype.Int4{Int32: 0, Valid: true}},
			{Stage: pgtype.Int4{Int32: 0, Valid: true}},
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := siblingsAreStaged(tt.children)
			if got != tt.want {
				t.Errorf("siblingsAreStaged() = %v, want %v", got, tt.want)
			}
		})
	}
}
