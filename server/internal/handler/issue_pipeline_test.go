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
