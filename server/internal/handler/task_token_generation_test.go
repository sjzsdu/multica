package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/auth"
)

// gen1/gen2 are arbitrary, ordered dispatch generations for one task. The
// absolute values do not matter; only t1 <= t2 does.
const (
	taskTokenGen1 = "2026-01-01T00:00:00Z"
	taskTokenGen2 = "2026-01-01T00:00:10Z"
)

func skipUnlessDB(t *testing.T) {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
}

// newTaskTokenFixture creates a running task with its own issue, bound to a
// fresh agent/runtime, and returns the ids needed to mint tokens for it.
func newTaskTokenFixture(t *testing.T, number int, dispatchedAt string) (taskID, agentID, runtimeID, issueID string) {
	t.Helper()
	ctx := context.Background()

	runtimeID = createClaimReclaimRuntime(t, ctx, fmt.Sprintf("task-token-%d", number))
	agentID, issueID = createClaimReclaimAgentAndIssue(t, ctx, runtimeID, fmt.Sprintf("task-token-%d", number))

	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, started_at, dispatched_at)
		VALUES ($1, $2, $3, 'running', 0, now(), $4::timestamptz)
		RETURNING id
	`, agentID, runtimeID, issueID, dispatchedAt).Scan(&taskID); err != nil {
		t.Fatalf("setup: create task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	return taskID, agentID, runtimeID, issueID
}

// insertTaskToken mints a token row at the given generation and returns its
// hash. Tokens are never logged or asserted by value elsewhere.
func insertTaskToken(t *testing.T, taskID, agentID, dispatchedAt string) string {
	t.Helper()
	rawToken := fmt.Sprintf("mat_test_%s_%s", taskID, dispatchedAt)
	hash := auth.HashToken(rawToken)
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO task_token (token_hash, task_id, agent_id, workspace_id, user_id, expires_at, dispatched_at)
		VALUES ($1, $2, $3, $4, $5, now() + interval '24 hours', $6::timestamptz)
	`, hash, taskID, agentID, testWorkspaceID, testUserID, dispatchedAt); err != nil {
		t.Fatalf("setup: insert task token: %v", err)
	}
	return hash
}

func isTokenRevoked(t *testing.T, hash string) bool {
	t.Helper()
	var revoked bool
	if err := testPool.QueryRow(context.Background(),
		`SELECT revoked_at IS NOT NULL FROM task_token WHERE token_hash = $1`, hash).Scan(&revoked); err != nil {
		t.Fatalf("read token revocation: %v", err)
	}
	return revoked
}

func completeTaskAt(t *testing.T, taskID, dispatchedAt string) {
	t.Helper()
	body := map[string]any{"output": "done"}
	if dispatchedAt != "" {
		body["dispatched_at"] = dispatchedAt
	}
	req := newOwnerDaemonRequest("POST", "/api/daemon/tasks/"+taskID+"/complete", body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("taskId", taskID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	testHandler.CompleteTask(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("CompleteTask(%s): expected 200, got %d: %s", dispatchedAt, w.Code, w.Body.String())
	}
}

// B1: whoami exposes only the token binding, and refuses non-task credentials.
func TestTaskTokenWhoami(t *testing.T) {
	taskID := "01a1a190-7911-7c00-8000-000000000001"
	agentID := "01a1a190-7911-7c00-8000-000000000002"
	workspaceID := testWorkspaceID

	req := httptest.NewRequest(http.MethodGet, "/api/task-token/whoami", nil)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Task-ID", taskID)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Workspace-ID", workspaceID)
	w := httptest.NewRecorder()
	(&Handler{}).TaskTokenWhoami(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("whoami status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode whoami: %v", err)
	}
	if got["task_id"] != taskID || got["agent_id"] != agentID || got["workspace_id"] != workspaceID {
		t.Fatalf("whoami = %+v", got)
	}
	if len(got) != 3 {
		t.Fatalf("whoami must expose only the binding, got %+v", got)
	}

	forbidden := httptest.NewRequest(http.MethodGet, "/api/task-token/whoami", nil)
	forbidden.Header.Set("X-Actor-Source", "member")
	fw := httptest.NewRecorder()
	(&Handler{}).TaskTokenWhoami(fw, forbidden)
	if fw.Code != http.StatusForbidden {
		t.Fatalf("non-task credential status = %d, want 403", fw.Code)
	}
}

// C1: a completion from the older generation revokes only that generation; the
// newer stale-reclaim token keeps authenticating. A later callback carrying the
// newer generation revokes it too, even though the task already transitioned.
func TestCompleteTask_RevokesOnlyCallbackGeneration(t *testing.T) {
	skipUnlessDB(t)

	taskID, agentID, _, _ := newTaskTokenFixture(t, 9101, taskTokenGen2)
	hash1 := insertTaskToken(t, taskID, agentID, taskTokenGen1)
	hash2 := insertTaskToken(t, taskID, agentID, taskTokenGen2)

	completeTaskAt(t, taskID, taskTokenGen1)
	if !isTokenRevoked(t, hash1) {
		t.Fatal("generation 1 token should be revoked by its own completion")
	}
	if isTokenRevoked(t, hash2) {
		t.Fatal("generation 2 token must survive generation 1 completing")
	}

	// Replayed completion from the newer delivery still revokes its generation.
	completeTaskAt(t, taskID, taskTokenGen2)
	if !isTokenRevoked(t, hash2) {
		t.Fatal("generation 2 token should be revoked by generation 2's completion")
	}
}

// C2: two different tasks running concurrently — A completing must not revoke
// B's token (the incident this fix addresses).
func TestCompleteTask_DoesNotRevokeAnotherTasksToken(t *testing.T) {
	skipUnlessDB(t)

	taskA, agentA, _, _ := newTaskTokenFixture(t, 9102, taskTokenGen1)
	taskB, agentB, _, _ := newTaskTokenFixture(t, 9103, taskTokenGen1)
	hashA := insertTaskToken(t, taskA, agentA, taskTokenGen1)
	hashB := insertTaskToken(t, taskB, agentB, taskTokenGen1)

	completeTaskAt(t, taskA, taskTokenGen1)

	if !isTokenRevoked(t, hashA) {
		t.Fatal("task A token should be revoked")
	}
	if isTokenRevoked(t, hashB) {
		t.Fatal("task B token must survive task A completing")
	}
}

// C3: cancellation is terminal and revokes every generation of the task.
func TestCancelTask_RevokesAllGenerations(t *testing.T) {
	skipUnlessDB(t)

	taskID, agentID, _, _ := newTaskTokenFixture(t, 9104, taskTokenGen2)
	hash1 := insertTaskToken(t, taskID, agentID, taskTokenGen1)
	hash2 := insertTaskToken(t, taskID, agentID, taskTokenGen2)

	if _, err := testHandler.TaskService.CancelTask(context.Background(), parseUUID(taskID)); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	if !isTokenRevoked(t, hash1) || !isTokenRevoked(t, hash2) {
		t.Fatal("cancel must revoke every generation of the task's tokens")
	}
}

// C6: an older daemon omitting dispatched_at keeps the legacy task-wide revoke
// and its WARN, even though the daemon itself has no generation to report.
func TestCompleteTask_LegacyCallbackBroadRevokes(t *testing.T) {
	skipUnlessDB(t)

	taskID, agentID, _, _ := newTaskTokenFixture(t, 9105, taskTokenGen2)
	hash1 := insertTaskToken(t, taskID, agentID, taskTokenGen1)
	hash2 := insertTaskToken(t, taskID, agentID, taskTokenGen2)

	completeTaskAt(t, taskID, "")

	if !isTokenRevoked(t, hash1) || !isTokenRevoked(t, hash2) {
		t.Fatal("legacy completion without dispatched_at must keep broad revocation")
	}
}
