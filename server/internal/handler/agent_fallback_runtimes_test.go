package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fallbackEntry is the wire shape of one agent-level fallback chain entry.
type fallbackEntry struct {
	RuntimeID     string  `json:"runtime_id"`
	Model         string  `json:"model,omitempty"`
	ThinkingLevel *string `json:"thinking_level,omitempty"`
	ServiceTier   *string `json:"service_tier,omitempty"`
}

// createFallbackTestAgent posts a CreateAgent request and fails the test on
// any status other than want.
func createFallbackTestAgent(t *testing.T, name string, chain []fallbackEntry, want int) map[string]any {
	t.Helper()
	body := map[string]any{
		"name":                 name,
		"runtime_id":           testRuntimeID,
		"visibility":           "private",
		"max_concurrent_tasks": 1,
	}
	if chain != nil {
		body["fallback_runtimes"] = chain
	}
	w := httptest.NewRecorder()
	testHandler.CreateAgent(w, newRequest(http.MethodPost, "/api/agents", body))
	if w.Code != want {
		t.Fatalf("CreateAgent(%s): expected %d, got %d: %s", name, want, w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	return resp
}

func TestAgentFallbackRuntimesValidation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	extra := createProviderRuntime(t, "fallback")

	t.Cleanup(func() {
		testPool.Exec(ctx,
			`DELETE FROM agent WHERE workspace_id = $1 AND name LIKE 'fallback-test-%'`,
			testWorkspaceID,
		)
	})

	t.Run("valid chain is stored", func(t *testing.T) {
		resp := createFallbackTestAgent(t, "fallback-test-valid", []fallbackEntry{
			{RuntimeID: extra, Model: "fb-model", ThinkingLevel: strPtr("low")},
		}, http.StatusCreated)
		stored, _ := resp["fallback_runtimes"].([]any)
		if len(stored) != 1 {
			t.Fatalf("fallback_runtimes = %v, want one entry", resp["fallback_runtimes"])
		}
		entry, _ := stored[0].(map[string]any)
		if entry["runtime_id"] != extra || entry["model"] != "fb-model" {
			t.Errorf("stored entry = %v, want runtime %s with model fb-model", entry, extra)
		}
	})

	t.Run("unknown runtime is rejected", func(t *testing.T) {
		createFallbackTestAgent(t, "fallback-test-unknown", []fallbackEntry{
			{RuntimeID: "00000000-0000-0000-0000-0000000000aa"},
		}, http.StatusBadRequest)
	})

	t.Run("duplicate runtime entries are rejected", func(t *testing.T) {
		createFallbackTestAgent(t, "fallback-test-duplicate", []fallbackEntry{
			{RuntimeID: extra, Model: "first"},
			{RuntimeID: extra, Model: "second"},
		}, http.StatusBadRequest)
	})

	t.Run("fallback restating the primary runtime is rejected", func(t *testing.T) {
		createFallbackTestAgent(t, "fallback-test-primary", []fallbackEntry{
			{RuntimeID: testRuntimeID, Model: "same-as-primary"},
		}, http.StatusBadRequest)
	})

	t.Run("runtime from another workspace is rejected", func(t *testing.T) {
		// A valid UUID that exists nowhere in this workspace.
		createFallbackTestAgent(t, "fallback-test-foreign", []fallbackEntry{
			{RuntimeID: "11111111-1111-1111-1111-111111111111"},
		}, http.StatusBadRequest)
	})

	t.Run("update rejects chain naming the runtime the update installs", func(t *testing.T) {
		resp := createFallbackTestAgent(t, "fallback-test-update", []fallbackEntry{
			{RuntimeID: extra, Model: "fb-model"},
		}, http.StatusCreated)
		agentID, _ := resp["id"].(string)
		if agentID == "" {
			t.Fatalf("missing agent id in response: %v", resp)
		}

		// Switch the primary to `extra` while the (unchanged) chain still names
		// `extra`: the effective chain would contain the primary, so refuse.
		w := httptest.NewRecorder()
		testHandler.UpdateAgent(w, withURLParams(newRequest(http.MethodPatch, "/api/agents/"+agentID, map[string]any{
			"runtime_id": extra,
		}), "id", agentID))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("UpdateAgent switching primary onto chained runtime: expected 400, got %d: %s", w.Code, w.Body.String())
		}

		// A wholesale replacement that drops the offending entry is fine.
		w = httptest.NewRecorder()
		testHandler.UpdateAgent(w, withURLParams(newRequest(http.MethodPatch, "/api/agents/"+agentID, map[string]any{
			"runtime_id":        extra,
			"fallback_runtimes": []fallbackEntry{},
		}), "id", agentID))
		if w.Code != http.StatusOK {
			t.Fatalf("UpdateAgent with emptied chain: expected 200, got %d: %s", w.Code, w.Body.String())
		}
	})
}
