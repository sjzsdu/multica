package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// systemFallbackHandler returns a handler whose queries run on a transaction
// the test rolls back. system_config is a shared singleton row, so writing it
// through the pool from this package would race the service package's chain
// tests; an uncommitted write stays invisible to them.
func systemFallbackHandler(t *testing.T) *Handler {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("handler test DB not configured")
	}
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	h := *testHandler
	h.Queries = db.New(tx)
	return &h
}

func TestSystemFallbackRuntimeWriteAndRead(t *testing.T) {
	h := systemFallbackHandler(t)
	runtimeID := createProviderRuntime(t, "claude")

	var got systemFallbackRuntimePayload
	testutil.Call(t, h.UpdateSystemFallbackRuntime, newRequest(http.MethodPut, "/api/system-fallback-runtime", map[string]any{
		"fallback_runtime": map[string]any{
			"runtime_id": runtimeID,
			"model":      "claude-sonnet-4",
		},
	})).Want(http.StatusOK).JSON(&got)
	if got.FallbackRuntime == nil {
		t.Fatal("write response omitted the stored entry")
	}
	if got.FallbackRuntime.RuntimeID != runtimeID || got.FallbackRuntime.Model != "claude-sonnet-4" {
		t.Fatalf("write response = %+v", got.FallbackRuntime)
	}

	// The read endpoint serves exactly what the write stored, so the settings
	// page and service.SystemFallbackRuntime agree on the chain's last tier.
	testutil.Call(t, h.GetSystemFallbackRuntime, newRequest(http.MethodGet, "/api/system-fallback-runtime", nil)).
		Want(http.StatusOK).JSON(&got)
	if got.FallbackRuntime == nil || got.FallbackRuntime.RuntimeID != runtimeID || got.FallbackRuntime.Model != "claude-sonnet-4" {
		t.Fatalf("read after write = %+v", got.FallbackRuntime)
	}

	// Null clears the tier.
	testutil.Call(t, h.UpdateSystemFallbackRuntime, newRequest(http.MethodPut, "/api/system-fallback-runtime", map[string]any{
		"fallback_runtime": nil,
	})).Want(http.StatusOK).JSON(&got)
	if got.FallbackRuntime != nil {
		t.Fatalf("clear response = %+v, want null", got.FallbackRuntime)
	}
	testutil.Call(t, h.GetSystemFallbackRuntime, newRequest(http.MethodGet, "/api/system-fallback-runtime", nil)).
		Want(http.StatusOK).JSON(&got)
	if got.FallbackRuntime != nil {
		t.Fatalf("read after clear = %+v, want null", got.FallbackRuntime)
	}
}

func TestSystemFallbackRuntimeValidationAndPermissions(t *testing.T) {
	h := systemFallbackHandler(t)

	// A runtime that is not in this workspace is rejected at write time.
	testutil.Call(t, h.UpdateSystemFallbackRuntime, newRequest(http.MethodPut, "/api/system-fallback-runtime", map[string]any{
		"fallback_runtime": map[string]any{"runtime_id": "550e8400-e29b-41d4-a716-446655440000"},
	})).Want(http.StatusBadRequest)

	// A malformed body is rejected before any lookup.
	testutil.Call(t, h.UpdateSystemFallbackRuntime, newRequest(http.MethodPut, "/api/system-fallback-runtime", map[string]any{
		"fallback_runtime": "not-an-object",
	})).Want(http.StatusBadRequest)

	// Plain members can read the global tier (the settings page shows it
	// read-only) but cannot change it.
	memberID := dbfx.User(t, "System fallback member", "system-fallback-member@multica.test")
	dbfx.Member(t, testWorkspaceID, memberID, "member")
	testutil.Call(t, h.GetSystemFallbackRuntime, newRequestAs(memberID, http.MethodGet, "/api/system-fallback-runtime", nil)).
		Want(http.StatusOK)
	testutil.Call(t, h.UpdateSystemFallbackRuntime, newRequestAs(memberID, http.MethodPut, "/api/system-fallback-runtime", map[string]any{
		"fallback_runtime": nil,
	})).Want(http.StatusForbidden)
}
