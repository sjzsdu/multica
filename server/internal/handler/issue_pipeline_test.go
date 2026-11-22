package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// The verdict endpoint is the pipeline's server-side gate: it is the only
// place that decides how many rework rounds a squad gets and the only place
// that closes the issue. These tests cover the gates (whose run may verdict,
// on which issue, under which policy) and the round arithmetic, including the
// escalation that has to reach both the leader and the owner.

// pipelineFixture is one squad-assigned issue plus the product agent whose
// task authorizes verdicts on it.
type pipelineFixture struct {
	issueID  string
	squadID  string
	leaderID string
	agentID  string
	taskID   string
}

// newPipelineFixture wires a squad, its workflow policy, the issue the
// pipeline iterates on, and a queued product-agent task bound to that issue.
// maxRounds<=0 skips the policy row so callers can test the unconfigured case;
// with a policy, any squad member may verdict unless a test names one.
func newPipelineFixture(t *testing.T, maxRounds int) pipelineFixture {
	t.Helper()
	suffix := strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())

	leaderID := createHandlerTestAgent(t, "pipeline-leader-"+suffix, nil)
	agentID := createHandlerTestAgent(t, "pipeline-product-"+suffix, nil)
	squadID := dbfx.Squad(t, "Pipeline Squad "+suffix, leaderID)
	dbfx.SquadMember(t, squadID, "agent", leaderID, testutil.Cols{"role": "leader"})
	dbfx.SquadMember(t, squadID, "agent", agentID, testutil.Cols{"role": "member"})

	if maxRounds > 0 {
		config := fmt.Sprintf(`{"pipeline":{"max_rounds":%d}}`, maxRounds)
		dbfx.InsertNoID(t, "squad_workflow_policy", testutil.Cols{
			"squad_id":     squadID,
			"workspace_id": testWorkspaceID,
			"config":       config,
		}, "squad_id = $1", squadID)
	}

	issueID := dbfx.Issue(t, "Pipeline issue "+suffix, testutil.Cols{
		"assignee_type": "squad",
		"assignee_id":   squadID,
		"status":        "in_progress",
	})
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":            issueID,
		"runtime_id":          handlerTestRuntimeID(t),
		"originator_user_id":  testUserID,
		"accountable_user_id": testUserID,
	})

	// These rows are created BY the verdict calls under test; no cascade
	// exists to remove them with the issue (repo rule), so they go explicitly
	// and, being registered last, first.
	dbfx.Cleanup(t, "DELETE FROM comment WHERE issue_id = $1", issueID)
	dbfx.Cleanup(t, "DELETE FROM activity_log WHERE issue_id = $1", issueID)
	dbfx.Cleanup(t, "DELETE FROM agent_task_queue WHERE issue_id = $1", issueID)

	return pipelineFixture{issueID: issueID, squadID: squadID, leaderID: leaderID, agentID: agentID, taskID: taskID}
}

func callPipelineVerdict(t *testing.T, f pipelineFixture, agentID, taskID string, body map[string]any) *testutil.Response {
	t.Helper()
	req := newRequest("POST", "/api/issues/"+f.issueID+"/pipeline/verdict", body)
	if agentID != "" {
		req.Header.Set("X-Agent-ID", agentID)
	}
	if taskID != "" {
		req.Header.Set("X-Task-ID", taskID)
	}
	return testutil.Call(t, testHandler.RecordPipelineVerdict, withURLParam(req, "id", f.issueID))
}

func pipelineMetadata(t *testing.T, issueID string) map[string]any {
	t.Helper()
	var raw string
	dbfx.QueryRow(t, `SELECT metadata::text FROM issue WHERE id = $1`, issueID).Scan(&raw)
	meta := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatalf("decode issue metadata %s: %v", raw, err)
	}
	return meta
}

func pipelineIssueStatus(t *testing.T, issueID string) string {
	t.Helper()
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status)
	return status
}

func TestRecordPipelineVerdict_OptInRequiresCurrentRound(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineFixture(t, 2)
	dbfx.Exec(t, `
		UPDATE squad_workflow_policy
		SET config = jsonb_build_object(
			'require_pipeline_verdict', true,
			'human_approval_required', false,
			'pipeline', jsonb_build_object('max_rounds', 2, 'product_agent_id', $2::text)
		)
		WHERE squad_id = $1
	`, f.squadID, f.agentID)

	callPipelineVerdict(t, f, f.agentID, f.taskID, map[string]any{"verdict": "accept"}).
		Want(http.StatusConflict)
	callPipelineVerdict(t, f, f.agentID, f.taskID, map[string]any{"verdict": "accept", "round": 1}).
		Want(http.StatusConflict)
	if got := pipelineIssueStatus(t, f.issueID); got != "in_progress" {
		t.Fatalf("issue status = %q after stale verdicts, want in_progress", got)
	}
	// A current round alone is not acceptance evidence: the final run must have
	// closed and moved the parent through product review first.
	callPipelineVerdict(t, f, f.agentID, f.taskID, map[string]any{"verdict": "accept", "round": 0}).
		Want(http.StatusConflict)

	dbfx.Insert(t, "squad_workflow_run", testutil.Cols{
		"workspace_id": testWorkspaceID, "squad_id": f.squadID, "parent_issue_id": f.issueID,
		"closed_stage": 3, "state": "blocked",
	})
	createStagedChild(t, f.issueID, 3, "done")
	dbfx.Exec(t, `UPDATE issue SET status = 'in_review' WHERE id = $1`, f.issueID)
	callPipelineVerdict(t, f, f.agentID, f.taskID, map[string]any{"verdict": "accept", "round": 0}).
		Want(http.StatusOK)
}

func TestRecordPipelineVerdict_OptInAcceptRequiresCurrentRoundGates(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineReviewFixture(t)
	enableBlackboxWorkerPolicy(t, f)
	createStagedChild(t, f.issueID, 3, "done")
	dbfx.Exec(t, `UPDATE issue SET status = 'in_review' WHERE id = $1`, f.issueID)

	// A closed final stage is not enough: CI, merge, and the independent
	// review must all be persisted for the current round before acceptance.
	callPipelineVerdict(t, f.pipelineFixture, f.agentID, f.taskID, map[string]any{"verdict": "accept", "round": 0}).
		Want(http.StatusConflict)
	linkWorkflowPullRequest(t, f, "merged")
	callPipelineVerdict(t, f.pipelineFixture, f.agentID, f.taskID, map[string]any{"verdict": "accept", "round": 0}).
		Want(http.StatusConflict)
	approveWorkflowReview(t, f)
	callPipelineVerdict(t, f.pipelineFixture, f.agentID, f.taskID, map[string]any{"verdict": "accept", "round": 0}).
		Want(http.StatusOK)
}

func TestRecordPipelineVerdict_AcceptClosesIssue(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineFixture(t, 2)

	// A reason is optional for accept: the reason exists to justify rework.
	var out map[string]any
	callPipelineVerdict(t, f, f.agentID, f.taskID, map[string]any{"verdict": "accept"}).
		Want(http.StatusOK).JSON(&out)
	if out["verdict"] != "accept" {
		t.Fatalf("verdict = %v, want accept", out["verdict"])
	}
	if got := pipelineIssueStatus(t, f.issueID); got != "done" {
		t.Fatalf("issue status = %q, want done — the pipeline closes the issue itself", got)
	}
	if got := pipelineMetadata(t, f.issueID)["pipeline_status"]; got != "accepted" {
		t.Fatalf("pipeline_status = %v, want accepted", got)
	}
	if n := dbfx.Count(t,
		`SELECT count(*) FROM activity_log WHERE issue_id = $1 AND action = 'pipeline_verdict'`,
		f.issueID); n != 1 {
		t.Fatalf("pipeline_verdict activity rows = %d, want 1", n)
	}

	// The issue is terminal now, so a second verdict cannot reopen counting.
	callPipelineVerdict(t, f, f.agentID, f.taskID, map[string]any{"verdict": "accept"}).
		Want(http.StatusConflict)
}

func TestRecordPipelineVerdict_RejectCountsRoundsThenEscalates(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	const maxRounds = 2
	f := newPipelineFixture(t, maxRounds)

	for round := 1; round <= maxRounds; round++ {
		var out map[string]any
		callPipelineVerdict(t, f, f.agentID, f.taskID, map[string]any{
			"verdict": "reject",
			"reason":  fmt.Sprintf("round %d still broken", round),
		}).Want(http.StatusOK).JSON(&out)

		if got := out["round"]; got != float64(round) {
			t.Fatalf("round %v, want %d", got, round)
		}
		if out["escalated"] != false {
			t.Fatalf("round %d escalated early: %v", round, out["escalated"])
		}
		if got := pipelineMetadata(t, f.issueID)["pipeline_status"]; got != "rejected" {
			t.Fatalf("pipeline_status after round %d = %v, want rejected", round, got)
		}
		// Rework is dispatched by the leader, not by this endpoint: a reject
		// inside the cap must stay a round count and leave the loop running.
		if n := dbfx.Count(t,
			`SELECT count(*) FROM comment WHERE issue_id = $1 AND author_type = 'agent'`,
			f.issueID); n != 0 {
			t.Fatalf("round %d wrote %d agent comments, want 0", round, n)
		}
	}

	// The reject that would start round maxRounds+1 is not dispatched; the
	// server stops the loop instead.
	var out map[string]any
	callPipelineVerdict(t, f, f.agentID, f.taskID, map[string]any{
		"verdict": "reject",
		"reason":  "still broken after the cap",
	}).Want(http.StatusOK).JSON(&out)

	if out["escalated"] != true {
		t.Fatalf("escalated = %v, want true", out["escalated"])
	}
	if got := out["round"]; got != float64(maxRounds+1) {
		t.Fatalf("round %v, want %d", got, maxRounds+1)
	}
	if got := pipelineMetadata(t, f.issueID)["pipeline_status"]; got != "escalated" {
		t.Fatalf("pipeline_status = %v, want escalated", got)
	}
	if got := pipelineIssueStatus(t, f.issueID); got != "in_progress" {
		t.Fatalf("issue status = %q after escalation, want it left for a human to decide", got)
	}

	// The escalation is one comment carrying both audiences: the leader it
	// stands down and the owner it notifies.
	var content string
	dbfx.QueryRow(t,
		`SELECT content FROM comment WHERE issue_id = $1 AND author_type = 'agent' ORDER BY created_at DESC LIMIT 1`,
		f.issueID).Scan(&content)
	if !strings.Contains(content, "mention://agent/"+f.leaderID) {
		t.Fatalf("escalation comment does not mention the leader:\n%s", content)
	}
	if !strings.Contains(content, "mention://member/") {
		t.Fatalf("escalation comment does not mention the owner:\n%s", content)
	}

	// ...and the mention is what wakes the leader to stand down.
	if n := dbfx.Count(t,
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`,
		f.issueID, f.leaderID); n == 0 {
		t.Fatalf("escalation did not enqueue the squad leader")
	}

	// A pipeline that has escalated is a human decision now: further rejects
	// neither count a round nor re-notify anyone.
	callPipelineVerdict(t, f, f.agentID, f.taskID, map[string]any{
		"verdict": "reject",
		"reason":  "one more try",
	}).Want(http.StatusConflict)
	if got := pipelineMetadata(t, f.issueID)[pipelineRoundMetadataKey]; got != float64(maxRounds+1) {
		t.Fatalf("pipeline_round = %v after a post-escalation reject, want %d", got, maxRounds+1)
	}
}

func TestRecordPipelineVerdict_OptInRejectDispatchesReworkAfterCommit(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineReviewFixture(t)
	const reason = "the onboarding flow still drops the workspace name"
	var out map[string]any
	callPipelineVerdict(t, f.pipelineFixture, f.agentID, f.taskID, map[string]any{
		"verdict": "reject", "reason": reason, "round": 0,
	}).Want(http.StatusOK).JSON(&out)
	if out["round"] != float64(1) || out["escalated"] != false {
		t.Fatalf("reject response = %#v, want non-escalated round 1", out)
	}
	dispatchKey := "rework:" + f.runID + ":1"
	var handoff string
	dbfx.QueryRow(t, `
		SELECT handoff_note FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 AND context->>'workflow_dispatch_key' = $3
	`, f.issueID, f.leaderID, dispatchKey).Scan(&handoff)
	for _, want := range []string{f.runID, "round 1", reason, "revalidate"} {
		if !strings.Contains(handoff, want) {
			t.Fatalf("rework handoff %q does not contain %q", handoff, want)
		}
	}
}

func TestRecordPipelineVerdict_OptInRejectDispatchFailureIsRecoverable(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineReviewFixture(t)
	dbfx.Exec(t, `
		UPDATE squad_workflow_run
		SET state = 'finalized', next_attempt_at = now() + interval '1 hour', lease_token = NULL
		WHERE id = $1
	`, f.runID)
	failingHandler := *testHandler
	failingHandler.TaskService = nil
	req := newRequest("POST", "/api/issues/"+f.issueID+"/pipeline/verdict", map[string]any{
		"verdict": "reject", "reason": "temporary dispatch failure", "round": 0,
	})
	req.Header.Set("X-Agent-ID", f.agentID)
	req.Header.Set("X-Task-ID", f.taskID)
	testutil.Call(t, failingHandler.RecordPipelineVerdict, withURLParam(req, "id", f.issueID)).
		Want(http.StatusServiceUnavailable)
	if got := pipelineMetadata(t, f.issueID)[pipelineRoundMetadataKey]; got != float64(1) {
		t.Fatalf("committed pipeline_round = %v, want 1", got)
	}
	dispatchKey := "rework:" + f.runID + ":1"
	countDispatch := func() int {
		return dbfx.Count(t, `
			SELECT count(*) FROM agent_task_queue
			WHERE issue_id = $1 AND context->>'workflow_dispatch_key' = $2
		`, f.issueID, dispatchKey)
	}
	if got := countDispatch(); got != 0 {
		t.Fatalf("failed dispatch persisted %d tasks, want 0", got)
	}

	if err := NewWorkflowRetryWorker(testHandler).RunOnce(t.Context()); err != nil {
		t.Fatalf("recover rejected dispatch: %v", err)
	}
	if got := countDispatch(); got != 1 {
		t.Fatalf("recovered dispatches = %d, want 1", got)
	}
	callPipelineVerdict(t, f.pipelineFixture, f.agentID, f.taskID, map[string]any{
		"verdict": "reject", "reason": "stale retry", "round": 0,
	}).Want(http.StatusConflict)
	if got := pipelineMetadata(t, f.issueID)[pipelineRoundMetadataKey]; got != float64(1) {
		t.Fatalf("stale retry changed pipeline_round to %v", got)
	}
	if got := countDispatch(); got != 1 {
		t.Fatalf("stale retry produced %d dispatches, want 1", got)
	}
}

func TestRecordPipelineVerdict_RejectRequiresReasonAndMentionFreeText(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineFixture(t, 2)

	callPipelineVerdict(t, f, f.agentID, f.taskID, map[string]any{"verdict": "reject"}).
		Want(http.StatusBadRequest)

	// A reason that carries a mention would dispatch work through a comment
	// this handler authors, from a caller who never named the target.
	resp := callPipelineVerdict(t, f, f.agentID, f.taskID, map[string]any{
		"verdict": "reject",
		"reason":  "[@Nobody](mention://agent/00000000-0000-0000-0000-000000000001) please fix",
	}).Want(http.StatusBadRequest)
	var body struct {
		Error string `json:"error"`
	}
	resp.JSON(&body)
	if !strings.Contains(body.Error, "mention") {
		t.Fatalf("error = %q, want it to name the mention restriction", body.Error)
	}

	callPipelineVerdict(t, f, f.agentID, f.taskID, map[string]any{"verdict": "deploy"}).
		Want(http.StatusBadRequest)
}

func TestRecordPipelineVerdict_RequiresSquadPipelinePolicy(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineFixture(t, 0)
	dbfx.Exec(t, `DELETE FROM squad_workflow_policy WHERE squad_id = $1`, f.squadID)

	resp := callPipelineVerdict(t, f, f.agentID, f.taskID, map[string]any{"verdict": "accept"}).
		Want(http.StatusConflict)
	var body struct {
		Error string `json:"error"`
	}
	resp.JSON(&body)
	if !strings.Contains(body.Error, "no pipeline configured") {
		t.Fatalf("error = %q, want it to say the squad has no pipeline", body.Error)
	}
	if got := pipelineIssueStatus(t, f.issueID); got != "in_progress" {
		t.Fatalf("issue status = %q, want it untouched by a refused verdict", got)
	}
}

func TestRecordPipelineVerdict_RestrictsToProductAgent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineFixture(t, 2)
	// Name the leader as this pipeline's product role: a squad member whose
	// only qualification is leading the squad.
	dbfx.Exec(t, `
		UPDATE squad_workflow_policy
		SET config = config || jsonb_build_object('pipeline', jsonb_build_object('max_rounds', 2, 'product_agent_id', $2::text))
		WHERE squad_id = $1
	`, f.squadID, f.leaderID)

	callPipelineVerdict(t, f, f.agentID, f.taskID, map[string]any{"verdict": "accept"}).
		Want(http.StatusForbidden)
	if got := pipelineIssueStatus(t, f.issueID); got != "in_progress" {
		t.Fatalf("issue status = %q, want it untouched by a refused verdict", got)
	}
}

type pipelineReviewFixture struct {
	pipelineFixture
	reviewerID     string
	reviewerTaskID string
	runID          string
}

func newPipelineReviewFixture(t *testing.T) pipelineReviewFixture {
	t.Helper()
	f := newPipelineFixture(t, 3)
	reviewerID := createHandlerTestAgent(t, "pipeline-reviewer-"+strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()), nil)
	dbfx.SquadMember(t, f.squadID, "agent", reviewerID, testutil.Cols{"role": "member"})
	reviewerTaskID := dbfx.Task(t, reviewerID, testutil.Cols{
		"issue_id":   f.issueID,
		"runtime_id": handlerTestRuntimeID(t),
	})
	dbfx.Exec(t, `
		UPDATE squad_workflow_policy
		SET config = jsonb_build_object(
			'require_review', true,
			'review_agent_id', $2::text,
			'require_pipeline_verdict', true,
			'pipeline', jsonb_build_object('max_rounds', 3, 'product_agent_id', $3::text)
		)
		WHERE squad_id = $1
	`, f.squadID, reviewerID, f.agentID)
	runID := dbfx.Insert(t, "squad_workflow_run", testutil.Cols{
		"workspace_id":    testWorkspaceID,
		"squad_id":        f.squadID,
		"parent_issue_id": f.issueID,
		"closed_stage":    3,
		"state":           "blocked",
	})
	dbfx.Cleanup(t, "DELETE FROM squad_workflow_run WHERE parent_issue_id = $1", f.issueID)
	dbfx.Cleanup(t, "DELETE FROM squad_workflow_approval WHERE parent_issue_id = $1", f.issueID)
	return pipelineReviewFixture{
		pipelineFixture: f, reviewerID: reviewerID, reviewerTaskID: reviewerTaskID, runID: runID,
	}
}

func callWorkflowReview(t *testing.T, f pipelineReviewFixture, agentID, taskID string, body map[string]any) *testutil.Response {
	t.Helper()
	req := newRequest("POST", "/api/issues/"+f.issueID+"/workflow-runs/"+f.runID+"/review", body)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	req = withURLParams(req, "id", f.issueID, "runId", f.runID)
	return testutil.Call(t, testHandler.RecordIssueWorkflowReview, req)
}

func TestRecordIssueWorkflowReview_PersistsCurrentRoundIndependentApproval(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineReviewFixture(t)
	var out map[string]any
	callWorkflowReview(t, f, f.reviewerID, f.reviewerTaskID, map[string]any{
		"approved": true, "note": "CI, diff and deployment evidence verified", "round": 0,
	}).Want(http.StatusAccepted).JSON(&out)
	if out["approved_by_type"] != "agent" || out["approved_by_id"] != f.reviewerID {
		t.Fatalf("approval actor = %#v, want configured reviewer %s", out, f.reviewerID)
	}
	if out["pipeline_round"] != float64(0) || out["round"] != float64(0) || out["max_rounds"] != float64(3) || out["escalated"] != false {
		t.Fatalf("approval response = %#v, want current round and pipeline cap", out)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM activity_log WHERE issue_id = $1 AND action = 'pipeline_review'`, f.issueID); n != 1 {
		t.Fatalf("pipeline_review activity rows = %d, want 1", n)
	}
}

func TestRecordIssueWorkflowReview_EnforcesRoundReviewerAndRejectionReason(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineReviewFixture(t)
	callWorkflowReview(t, f, f.reviewerID, f.reviewerTaskID, map[string]any{"approved": true}).
		Want(http.StatusConflict)
	callWorkflowReview(t, f, f.reviewerID, f.reviewerTaskID, map[string]any{"approved": true, "round": 1}).
		Want(http.StatusConflict)
	callWorkflowReview(t, f, f.agentID, f.taskID, map[string]any{"approved": true, "round": 0}).
		Want(http.StatusForbidden)
	callWorkflowReview(t, f, f.reviewerID, f.reviewerTaskID, map[string]any{"approved": false, "round": 0}).
		Want(http.StatusBadRequest)
	callWorkflowReview(t, f, f.reviewerID, f.reviewerTaskID, map[string]any{
		"approved": false, "round": 0, "note": "[@agent](mention://agent/00000000-0000-0000-0000-000000000001)",
	}).Want(http.StatusBadRequest)
	if n := dbfx.Count(t, `SELECT count(*) FROM squad_workflow_approval WHERE parent_issue_id = $1`, f.issueID); n != 0 {
		t.Fatalf("refused reviews persisted %d approvals, want 0", n)
	}
}

func TestRecordIssueWorkflowReview_RejectCountsRoundAndDispatchesRework(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineReviewFixture(t)
	const reason = "the deployment preview does not match the acceptance criteria"
	var out map[string]any
	callWorkflowReview(t, f, f.reviewerID, f.reviewerTaskID, map[string]any{
		"approved": false, "note": reason, "round": 0,
	}).Want(http.StatusAccepted).JSON(&out)
	if out["pipeline_round"] != float64(0) || out["round"] != float64(1) || out["escalated"] != false {
		t.Fatalf("reject response = %#v, want reviewed round 0 and resulting round 1", out)
	}
	metadata := pipelineMetadata(t, f.issueID)
	if metadata[pipelineRoundMetadataKey] != float64(1) || metadata[pipelineStatusMetadataKey] != pipelineStatusRejected {
		t.Fatalf("metadata after review reject = %#v, want rejected round 1", metadata)
	}
	if n := dbfx.Count(t, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND context->>'workflow_dispatch_key' = $2
	`, f.issueID, "rework:"+f.runID+":1"); n != 1 {
		t.Fatalf("rework dispatches = %d, want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM activity_log WHERE issue_id = $1 AND action = 'pipeline_review'`, f.issueID); n != 1 {
		t.Fatalf("pipeline_review activity rows = %d, want 1", n)
	}

	// A retry of the old task's request is stale, not another rejection.
	callWorkflowReview(t, f, f.reviewerID, f.reviewerTaskID, map[string]any{
		"approved": false, "note": reason, "round": 0,
	}).Want(http.StatusConflict)
	if got := pipelineMetadata(t, f.issueID)[pipelineRoundMetadataKey]; got != float64(1) {
		t.Fatalf("pipeline_round after stale retry = %v, want 1", got)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM activity_log WHERE issue_id = $1 AND action = 'pipeline_review'`, f.issueID); n != 1 {
		t.Fatalf("pipeline_review activities after stale retry = %d, want 1", n)
	}
}

func TestRecordIssueWorkflowReview_RejectEscalatesAtSharedCap(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineReviewFixture(t)
	dbfx.Exec(t, `UPDATE issue SET metadata = $2 WHERE id = $1`, f.issueID,
		`{"pipeline_round":3,"pipeline_status":"rejected"}`)
	var out map[string]any
	callWorkflowReview(t, f, f.reviewerID, f.reviewerTaskID, map[string]any{
		"approved": false, "note": "third rework still fails the release gate", "round": 3,
	}).Want(http.StatusAccepted).JSON(&out)
	if out["round"] != float64(4) || out["escalated"] != true {
		t.Fatalf("reject response = %#v, want escalation at round 4", out)
	}
	if got := pipelineMetadata(t, f.issueID)[pipelineStatusMetadataKey]; got != pipelineStatusEscalated {
		t.Fatalf("pipeline_status = %v, want escalated", got)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1`, f.issueID); n != 1 {
		t.Fatalf("escalation comments = %d, want 1", n)
	}
	if n := dbfx.Count(t, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND context->>'workflow_dispatch_key' = $2
	`, f.issueID, "rework:"+f.runID+":4"); n != 0 {
		t.Fatalf("post-escalation rework dispatches = %d, want 0", n)
	}
	callWorkflowReview(t, f, f.reviewerID, f.reviewerTaskID, map[string]any{
		"approved": false, "note": "retry after escalation", "round": 4,
	}).Want(http.StatusConflict)
}

func TestRecordIssueWorkflowReview_RejectsOldRun(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineReviewFixture(t)
	dbfx.Insert(t, "squad_workflow_run", testutil.Cols{
		"workspace_id": testWorkspaceID, "squad_id": f.squadID, "parent_issue_id": f.issueID,
		"closed_stage": 4, "state": "blocked",
	})
	callWorkflowReview(t, f, f.reviewerID, f.reviewerTaskID, map[string]any{
		"approved": true, "round": 0,
	}).Want(http.StatusConflict)
	if n := dbfx.Count(t, `SELECT count(*) FROM squad_workflow_approval WHERE parent_issue_id = $1`, f.issueID); n != 0 {
		t.Fatalf("old-run review persisted %d approvals, want 0", n)
	}
}

func TestRecordIssueWorkflowReview_RejectsSettledParentAndMismatchedTaskOrRun(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	for _, status := range []string{"done", "cancelled"} {
		t.Run("terminal "+status, func(t *testing.T) {
			f := newPipelineReviewFixture(t)
			dbfx.Exec(t, `UPDATE issue SET status = $2 WHERE id = $1`, f.issueID, status)
			callWorkflowReview(t, f, f.reviewerID, f.reviewerTaskID, map[string]any{
				"approved": true, "round": 0,
			}).Want(http.StatusConflict)
		})
	}

	t.Run("accepted", func(t *testing.T) {
		f := newPipelineReviewFixture(t)
		dbfx.Exec(t, `UPDATE issue SET metadata = $2 WHERE id = $1`, f.issueID,
			`{"pipeline_round":0,"pipeline_status":"accepted"}`)
		callWorkflowReview(t, f, f.reviewerID, f.reviewerTaskID, map[string]any{
			"approved": true, "round": 0,
		}).Want(http.StatusConflict)
	})

	t.Run("task bound to another issue", func(t *testing.T) {
		f := newPipelineReviewFixture(t)
		otherIssueID := dbfx.Issue(t, "Review other task issue", testutil.Cols{
			"assignee_type": "squad",
			"assignee_id":   f.squadID,
		})
		otherTaskID := dbfx.Task(t, f.reviewerID, testutil.Cols{
			"issue_id":   otherIssueID,
			"runtime_id": handlerTestRuntimeID(t),
		})
		callWorkflowReview(t, f, f.reviewerID, otherTaskID, map[string]any{
			"approved": true, "round": 0,
		}).Want(http.StatusBadRequest)
	})

	t.Run("run belongs to another issue", func(t *testing.T) {
		f := newPipelineReviewFixture(t)
		otherIssueID := dbfx.Issue(t, "Review other run issue", testutil.Cols{
			"assignee_type": "squad",
			"assignee_id":   f.squadID,
		})
		otherRunID := dbfx.Insert(t, "squad_workflow_run", testutil.Cols{
			"workspace_id": testWorkspaceID, "squad_id": f.squadID, "parent_issue_id": otherIssueID,
			"closed_stage": 3, "state": "blocked",
		})
		foreignRun := f
		foreignRun.runID = otherRunID
		callWorkflowReview(t, foreignRun, f.reviewerID, f.reviewerTaskID, map[string]any{
			"approved": true, "round": 0,
		}).Want(http.StatusNotFound)
	})
}

func enableOptInPipeline(t *testing.T, f pipelineFixture) {
	t.Helper()
	dbfx.Exec(t, `
		UPDATE squad_workflow_policy
		SET config = jsonb_build_object(
			'require_pipeline_verdict', true,
			'human_approval_required', false,
			'pipeline', jsonb_build_object('max_rounds', 2, 'product_agent_id', $2::text)
		)
		WHERE squad_id = $1
	`, f.squadID, f.agentID)
}

func withPipelineActor(req *http.Request, f pipelineFixture, actor string) *http.Request {
	switch actor {
	case "agent", "task_token":
		req.Header.Set("X-Agent-ID", f.agentID)
		req.Header.Set("X-Task-ID", f.taskID)
		if actor == "task_token" {
			req.Header.Set("X-Actor-Source", "task_token")
		}
	case "cloud_pat":
		req.Header.Set("X-Actor-Source", "cloud_pat")
	}
	return req
}

func callPipelineStatusUpdate(t *testing.T, f pipelineFixture, issueID, status, actor string) *testutil.Response {
	t.Helper()
	req := newRequest(http.MethodPatch, "/api/issues/"+issueID, map[string]any{"status": status})
	req = withPipelineActor(req, f, actor)
	return testutil.Call(t, testHandler.UpdateIssue, withURLParam(req, "id", issueID))
}

func callPipelineMetadataMutation(t *testing.T, f pipelineFixture, method, issueID, key, actor string, value any) *testutil.Response {
	t.Helper()
	var body any
	if method == http.MethodPut {
		body = map[string]any{"value": value}
	}
	req := newRequest(method, "/api/issues/"+issueID+"/metadata/"+key, body)
	req = withPipelineActor(req, f, actor)
	req = withURLParams(req, "id", issueID, "key", key)
	if method == http.MethodPut {
		return testutil.Call(t, testHandler.SetIssueMetadataKey, req)
	}
	return testutil.Call(t, testHandler.DeleteIssueMetadataKey, req)
}

func TestRecordPipelineVerdict_AcceptRollsBackStatusWhenMetadataWriteFails(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineFixture(t, 2)
	enableOptInPipeline(t, f)
	dbfx.Insert(t, "squad_workflow_run", testutil.Cols{
		"workspace_id": testWorkspaceID, "squad_id": f.squadID, "parent_issue_id": f.issueID,
		"closed_stage": 3, "state": "blocked",
	})
	createStagedChild(t, f.issueID, 3, "done")
	// Leave room for the fixture itself, but not for pipeline_status=accepted.
	// The existing DB CHECK therefore faults the metadata write without any
	// production-only failure hook.
	dbfx.Exec(t, `
		UPDATE issue
		SET status = 'in_review',
		    metadata = jsonb_build_object('pipeline_round', 0, 'blob', repeat('x', 8120))
		WHERE id = $1
	`, f.issueID)

	callPipelineVerdict(t, f, f.agentID, f.taskID, map[string]any{"verdict": "accept", "round": 0}).
		Want(http.StatusInternalServerError)
	if got := pipelineIssueStatus(t, f.issueID); got != "in_review" {
		t.Fatalf("issue status = %q after accepted metadata failed, want in_review", got)
	}
	if got := pipelineMetadata(t, f.issueID)[pipelineStatusMetadataKey]; got != nil {
		t.Fatalf("pipeline_status = %v after rollback, want no accepted value", got)
	}
}

func TestPipelineCompletionGuard_RejectsMachineSingleUpdates(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	for _, actor := range []string{"agent", "task_token", "cloud_pat"} {
		t.Run(actor, func(t *testing.T) {
			f := newPipelineFixture(t, 2)
			enableOptInPipeline(t, f)
			callPipelineStatusUpdate(t, f, f.issueID, "done", actor).Want(http.StatusForbidden)
			if got := pipelineIssueStatus(t, f.issueID); got != "in_progress" {
				t.Fatalf("issue status = %q, want in_progress", got)
			}
		})
	}
}

func TestPipelineCompletionGuard_RejectsMachineCustomDoneStatus(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	createTestCustomStatus(t, "pipeline_phase4_done", "done")
	f := newPipelineFixture(t, 2)
	enableOptInPipeline(t, f)

	callPipelineStatusUpdate(t, f, f.issueID, "pipeline_phase4_done", "agent").Want(http.StatusForbidden)
	if got := pipelineIssueStatus(t, f.issueID); got != "in_progress" {
		t.Fatalf("issue status = %q, want in_progress", got)
	}
}

func TestPipelineCompletionGuard_RejectsMachineBatchUpdate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineFixture(t, 2)
	enableOptInPipeline(t, f)
	req := newRequest(http.MethodPost, "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{f.issueID},
		"updates":   map[string]any{"status": "done"},
	})
	req = withPipelineActor(req, f, "agent")
	var out struct {
		Updated int                `json:"updated"`
		Results []batchIssueResult `json:"results"`
	}
	testutil.Call(t, testHandler.BatchUpdateIssues, req).Want(http.StatusOK).JSON(&out)
	if out.Updated != 0 || len(out.Results) != 1 || out.Results[0].Outcome != batchOutcomeSkipped {
		t.Fatalf("batch response = %#v, want one skipped item and no updates", out)
	}
	if got := pipelineIssueStatus(t, f.issueID); got != "in_progress" {
		t.Fatalf("issue status = %q, want in_progress", got)
	}
}

func TestPipelineCompletionGuard_EscalatedMachinesCannotTakeOver(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	for _, actor := range []string{"agent", "task_token", "cloud_pat"} {
		t.Run(actor, func(t *testing.T) {
			f := newPipelineFixture(t, 2)
			enableOptInPipeline(t, f)
			dbfx.Exec(t, `UPDATE issue SET metadata = $2 WHERE id = $1`, f.issueID,
				`{"pipeline_round":2,"pipeline_status":"escalated"}`)

			callPipelineStatusUpdate(t, f, f.issueID, "done", actor).Want(http.StatusForbidden)
			if got := pipelineIssueStatus(t, f.issueID); got != "in_progress" {
				t.Fatalf("issue status = %q, want in_progress", got)
			}
			if got := pipelineMetadata(t, f.issueID)[pipelineStatusMetadataKey]; got != pipelineStatusEscalated {
				t.Fatalf("pipeline_status = %v, want escalated", got)
			}
		})
	}
}

func TestPipelineCompletionGuard_HumanRequiresEscalation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineFixture(t, 2)
	enableOptInPipeline(t, f)

	callPipelineStatusUpdate(t, f, f.issueID, "done", "human").Want(http.StatusForbidden)
	if got := pipelineIssueStatus(t, f.issueID); got != "in_progress" {
		t.Fatalf("issue status = %q, want in_progress", got)
	}
}

func TestPipelineCompletionGuard_HumanCanCompleteEscalatedIssue(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineFixture(t, 2)
	enableOptInPipeline(t, f)
	dbfx.Exec(t, `UPDATE issue SET metadata = $2 WHERE id = $1`, f.issueID,
		`{"pipeline_round":2,"pipeline_status":"escalated"}`)

	callPipelineStatusUpdate(t, f, f.issueID, "done", "human").Want(http.StatusOK)
	if got := pipelineIssueStatus(t, f.issueID); got != "done" {
		t.Fatalf("issue status = %q, want done", got)
	}
	if got := pipelineMetadata(t, f.issueID)[pipelineStatusMetadataKey]; got != pipelineStatusEscalated {
		t.Fatalf("pipeline_status = %v after human takeover, want escalated", got)
	}
}

func TestPipelineCompletionGuard_LegacyAndChildRemainUnchanged(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	t.Run("legacy pipeline", func(t *testing.T) {
		f := newPipelineFixture(t, 2)
		callPipelineStatusUpdate(t, f, f.issueID, "done", "agent").Want(http.StatusOK)
	})
	t.Run("child issue", func(t *testing.T) {
		f := newPipelineFixture(t, 2)
		enableOptInPipeline(t, f)
		childID := dbfx.Issue(t, "Pipeline child completion control", testutil.Cols{
			"parent_issue_id": f.issueID,
			"assignee_type":   "squad",
			"assignee_id":     f.squadID,
			"status":          "in_progress",
		})
		dbfx.Cleanup(t, "DELETE FROM activity_log WHERE issue_id = $1", childID)
		callPipelineStatusUpdate(t, f, childID, "done", "agent").Want(http.StatusOK)
	})
	t.Run("ordinary issue", func(t *testing.T) {
		f := newPipelineFixture(t, 0)
		ordinaryID := createMetadataTestIssue(t, "Ordinary pipeline completion control")
		callPipelineStatusUpdate(t, f, ordinaryID, "done", "agent").Want(http.StatusOK)
	})
}

func TestPipelineMetadataGuard_OptInRejectsMachinesAndAllowsHuman(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineFixture(t, 2)
	enableOptInPipeline(t, f)
	dbfx.Exec(t, `UPDATE issue SET metadata = $2 WHERE id = $1`, f.issueID,
		`{"pipeline_round":1,"pipeline_status":"rejected"}`)

	for _, actor := range []string{"agent", "task_token", "cloud_pat"} {
		for _, key := range []string{pipelineRoundMetadataKey, pipelineStatusMetadataKey} {
			t.Run(actor+" set "+key, func(t *testing.T) {
				callPipelineMetadataMutation(t, f, http.MethodPut, f.issueID, key, actor, "forged").
					Want(http.StatusForbidden)
			})
			t.Run(actor+" delete "+key, func(t *testing.T) {
				callPipelineMetadataMutation(t, f, http.MethodDelete, f.issueID, key, actor, nil).
					Want(http.StatusForbidden)
			})
		}
	}
	metadata := pipelineMetadata(t, f.issueID)
	if metadata[pipelineRoundMetadataKey] != float64(1) || metadata[pipelineStatusMetadataKey] != pipelineStatusRejected {
		t.Fatalf("machine calls changed protected metadata: %#v", metadata)
	}
	callPipelineMetadataMutation(t, f, http.MethodPut, f.issueID, "pr_number", "agent", 482).
		Want(http.StatusOK)
	callPipelineMetadataMutation(t, f, http.MethodDelete, f.issueID, "pr_number", "agent", nil).
		Want(http.StatusOK)

	callPipelineMetadataMutation(t, f, http.MethodPut, f.issueID, pipelineRoundMetadataKey, "human", 2).
		Want(http.StatusOK)
	callPipelineMetadataMutation(t, f, http.MethodPut, f.issueID, pipelineStatusMetadataKey, "human", pipelineStatusEscalated).
		Want(http.StatusOK)
	callPipelineMetadataMutation(t, f, http.MethodDelete, f.issueID, pipelineRoundMetadataKey, "human", nil).
		Want(http.StatusOK)
	callPipelineMetadataMutation(t, f, http.MethodDelete, f.issueID, pipelineStatusMetadataKey, "human", nil).
		Want(http.StatusOK)
}

func TestPipelineMetadataGuard_NonOptInRemainsUnchanged(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	t.Run("legacy pipeline agent", func(t *testing.T) {
		f := newPipelineFixture(t, 2)
		callPipelineMetadataMutation(t, f, http.MethodPut, f.issueID, pipelineStatusMetadataKey, "agent", "waiting").
			Want(http.StatusOK)
		callPipelineMetadataMutation(t, f, http.MethodDelete, f.issueID, pipelineStatusMetadataKey, "agent", nil).
			Want(http.StatusOK)
	})
	t.Run("ordinary issue machine credential", func(t *testing.T) {
		f := newPipelineFixture(t, 0)
		ordinaryID := createMetadataTestIssue(t, "Ordinary pipeline metadata control")
		callPipelineMetadataMutation(t, f, http.MethodPut, ordinaryID, pipelineStatusMetadataKey, "cloud_pat", "waiting").
			Want(http.StatusOK)
		callPipelineMetadataMutation(t, f, http.MethodDelete, ordinaryID, pipelineStatusMetadataKey, "cloud_pat", nil).
			Want(http.StatusOK)
	})
	t.Run("opt-in squad child agent", func(t *testing.T) {
		f := newPipelineFixture(t, 2)
		enableOptInPipeline(t, f)
		childID := dbfx.Issue(t, "Pipeline child metadata control", testutil.Cols{
			"parent_issue_id": f.issueID,
			"assignee_type":   "squad",
			"assignee_id":     f.squadID,
		})
		callPipelineMetadataMutation(t, f, http.MethodPut, childID, pipelineStatusMetadataKey, "agent", "waiting").
			Want(http.StatusOK)
		callPipelineMetadataMutation(t, f, http.MethodDelete, childID, pipelineStatusMetadataKey, "agent", nil).
			Want(http.StatusOK)
	})
}

func TestRecordPipelineVerdict_GatesOnTaskAndSquad(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newPipelineFixture(t, 2)

	t.Run("no task id", func(t *testing.T) {
		callPipelineVerdict(t, f, f.agentID, "", map[string]any{"verdict": "accept"}).
			Want(http.StatusBadRequest)
	})

	t.Run("task bound to another issue", func(t *testing.T) {
		otherIssueID := dbfx.Issue(t, "Pipeline other issue", testutil.Cols{
			"assignee_type": "squad",
			"assignee_id":   f.squadID,
		})
		otherTaskID := dbfx.Task(t, f.agentID, testutil.Cols{
			"issue_id":   otherIssueID,
			"runtime_id": handlerTestRuntimeID(t),
		})
		resp := callPipelineVerdict(t, f, f.agentID, otherTaskID, map[string]any{"verdict": "accept"}).
			Want(http.StatusBadRequest)
		var body struct {
			Error string `json:"error"`
		}
		resp.JSON(&body)
		if !strings.Contains(body.Error, otherIssueID) {
			t.Fatalf("error = %q, want it to name issue %s", body.Error, otherIssueID)
		}
	})

	t.Run("agent outside the squad", func(t *testing.T) {
		outsiderID := createHandlerTestAgent(t, "pipeline-outsider", nil)
		outsiderTaskID := dbfx.Task(t, outsiderID, testutil.Cols{
			"issue_id":   f.issueID,
			"runtime_id": handlerTestRuntimeID(t),
		})
		resp := callPipelineVerdict(t, f, outsiderID, outsiderTaskID, map[string]any{"verdict": "accept"}).
			Want(http.StatusForbidden)
		var body struct {
			Error string `json:"error"`
		}
		resp.JSON(&body)
		if !strings.Contains(body.Error, "members of this squad") {
			t.Fatalf("error = %q, want it to say squad membership", body.Error)
		}
	})

	t.Run("member without a task", func(t *testing.T) {
		callPipelineVerdict(t, f, "", "", map[string]any{"verdict": "accept"}).
			Want(http.StatusBadRequest)
	})

	t.Run("squad-assigned issue is untouched by every refusal", func(t *testing.T) {
		if got := pipelineIssueStatus(t, f.issueID); got != "in_progress" {
			t.Fatalf("issue status = %q, want in_progress", got)
		}
	})
}
