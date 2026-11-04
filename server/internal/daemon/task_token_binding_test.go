package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// C5: two concurrently active tasks must never share the MULTICA_TOKEN they
// inject into their children. taskMulticaEnvironment is pure, so this pins the
// per-task token plumbing at the boundary where an accidental config-root or
// env reuse would show up.
func TestTaskMulticaEnvironment_TokensArePerTask(t *testing.T) {
	taskA := Task{ID: "task-a", AgentID: "agent-a", WorkspaceID: "ws-1", AuthToken: "mat_a"}
	taskB := Task{ID: "task-b", AgentID: "agent-b", WorkspaceID: "ws-2", AuthToken: "mat_b"}

	envA := taskMulticaEnvironment(taskA, "claude", "agent-a", taskA.AuthToken, "/cfg-a", "/ws-a", "https://api", 1, 1, "/tmp/a")
	envB := taskMulticaEnvironment(taskB, "claude", "agent-b", taskB.AuthToken, "/cfg-b", "/ws-b", "https://api", 1, 2, "/tmp/b")

	if envA["MULTICA_TOKEN"] != "mat_a" {
		t.Fatalf("task A MULTICA_TOKEN = %q, want mat_a", envA["MULTICA_TOKEN"])
	}
	if envB["MULTICA_TOKEN"] != "mat_b" {
		t.Fatalf("task B MULTICA_TOKEN = %q, want mat_b", envB["MULTICA_TOKEN"])
	}
	if envA["MULTICA_TASK_ID"] != taskA.ID || envB["MULTICA_TASK_ID"] != taskB.ID {
		t.Fatalf("task ids crossed: A=%q B=%q", envA["MULTICA_TASK_ID"], envB["MULTICA_TASK_ID"])
	}
}

// C5: claim -> whoami identifies the exact task; a token bound to a different
// task must be rejected before spawn, and it is a permanent (non-retryable)
// failure.
func TestVerifyTaskTokenBinding(t *testing.T) {
	tests := []struct {
		name      string
		boundTask string
		wantErr   bool
		mismatch  bool
	}{
		{name: "matching task", boundTask: "task-b"},
		{name: "foreign task", boundTask: "task-a", wantErr: true, mismatch: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != "Bearer mat_b" {
					t.Errorf("whoami Authorization = %q, want the task token", got)
				}
				_ = json.NewEncoder(w).Encode(TaskTokenWhoami{
					TaskID:      tc.boundTask,
					AgentID:     "agent-b",
					WorkspaceID: "ws-1",
				})
			}))
			defer srv.Close()

			client := NewClient(srv.URL)
			d := &Daemon{client: client}
			err := d.verifyTaskTokenBinding(context.Background(), Task{ID: "task-b", AuthToken: "mat_b"})
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected binding error, got nil")
				}
				if tc.mismatch && !errors.Is(err, errTaskTokenBindingMismatch) {
					t.Fatalf("expected mismatch sentinel, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("verifyTaskTokenBinding: %v", err)
			}
		})
	}
}

// C5 backstop: the same fingerprint cannot be registered to two live tasks.
func TestRegisterTaskTokenOwner_RejectsReusedFingerprint(t *testing.T) {
	d := &Daemon{}
	first := Task{ID: "task-a", AuthToken: "mat_reused"}
	if err := d.registerTaskTokenOwner(first); err != nil {
		t.Fatalf("register first: %v", err)
	}
	defer d.unregisterTaskTokenOwner(first)

	second := Task{ID: "task-b", AuthToken: "mat_reused"}
	if err := d.registerTaskTokenOwner(second); err == nil {
		t.Fatal("expected reused token fingerprint to be rejected")
	}

	// Once the first task reaches a terminal state its registration is released.
	d.unregisterTaskTokenOwner(first)
	if err := d.registerTaskTokenOwner(second); err != nil {
		t.Fatalf("register after release: %v", err)
	}
	d.unregisterTaskTokenOwner(second)
}
