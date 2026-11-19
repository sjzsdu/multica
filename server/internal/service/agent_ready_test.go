package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/dispatch"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// fallbackResolveFixture owns the workspace/user an agent_ready resolver test
// runs in. Every runtime it creates is owned by that user, so private-runtime
// ownership never interferes with the verdict under test.
type fallbackResolveFixture struct {
	fx *testutil.Fixture
}

func newFallbackResolveFixture(t *testing.T) fallbackResolveFixture {
	t.Helper()
	pool := sharedTestPool(t)
	bootstrap := testutil.New(pool, "", "")
	suffix := time.Now().UnixNano()
	ownerID := bootstrap.User(t,
		fmt.Sprintf("fallback-owner-%d", suffix),
		fmt.Sprintf("fallback-owner-%d@example.com", suffix),
	)
	workspaceID := bootstrap.Workspace(t,
		fmt.Sprintf("fallback-resolve-%d", suffix),
		fmt.Sprintf("fallback-resolve-%d", suffix),
	)
	fx := testutil.New(pool, workspaceID, ownerID)
	fx.Member(t, workspaceID, ownerID, "owner")
	return fallbackResolveFixture{fx: fx}
}

// lookup builds the resolver's runtime read on the fixture pool.
func (ff fallbackResolveFixture) lookup() RuntimeLookup {
	return RuntimeLookup{Queries: db.New(ff.fx.Pool), Source: "agent_ready_test"}
}

// offlineRuntime inserts a runtime that is down with the daemon's structured
// reason code — the shape a deregister handler leaves behind.
func (ff fallbackResolveFixture) offlineRuntime(t *testing.T, name, code string) string {
	t.Helper()
	return ff.fx.Runtime(t, name, testutil.Cols{
		"status":   "offline",
		"metadata": testutil.Raw(fmt.Sprintf(`'{"offline_reason": {"code": %q}}'::jsonb`, code)),
	})
}

// agentWithChain loads the agent row the resolver consumes, with fallback
// chain and primary model already in place.
func (ff fallbackResolveFixture) agentWithChain(t *testing.T, name, primaryRuntimeID string, chain []FallbackRuntimeEntry, over ...testutil.Cols) db.Agent {
	t.Helper()
	cols := testutil.Cols{}
	if len(over) > 0 {
		cols = over[0]
	}
	if chain != nil {
		b, err := json.Marshal(chain)
		if err != nil {
			t.Fatalf("marshal fallback chain: %v", err)
		}
		cols["fallback_runtimes"] = string(b)
	}
	agentID := ff.fx.Agent(t, name, primaryRuntimeID, cols)
	agent, err := db.New(ff.fx.Pool).GetAgent(context.Background(), mustParseUUID(t, agentID))
	if err != nil {
		t.Fatalf("load agent: %v", err)
	}
	return agent
}

func textPtr(s string) *string { return &s }

// fallbackTestUUID builds a stable, distinct runtime/agent uuid for the
// pure-function TaskExecutionSettings test, which never touches the database.
func fallbackTestUUID(n byte) pgtype.UUID {
	var u pgtype.UUID
	_ = u.Scan(fmt.Sprintf("00000000-0000-0000-0000-%012d", n))
	return u
}

func mustParseUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	id, err := util.ParseUUID(s)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return id
}

func TestResolveRuntimeWithFallback_PrimaryAvailable(t *testing.T) {
	ff := newFallbackResolveFixture(t)
	ctx := context.Background()

	primary := ff.fx.Runtime(t, "primary")
	// A live fallback must NOT be consulted when the primary is fine.
	_ = ff.fx.Runtime(t, "unused-fallback")
	agent := ff.agentWithChain(t, "agent", primary, []FallbackRuntimeEntry{
		{RuntimeID: "00000000-0000-0000-0000-00000000000f", Model: "unused"},
	}, testutil.Cols{"model": "primary-model"})

	resolved, err := ResolveRuntimeWithFallback(ctx, ff.lookup(), agent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.FallbackFrom != "primary" {
		t.Errorf("FallbackFrom = %q, want primary", resolved.FallbackFrom)
	}
	if resolved.RuntimeID != agent.RuntimeID {
		t.Errorf("RuntimeID = %s, want primary %s", util.UUIDToString(resolved.RuntimeID), util.UUIDToString(agent.RuntimeID))
	}
	if !resolved.Model.Valid || resolved.Model.String != "primary-model" {
		t.Errorf("Model = %+v, want primary-model", resolved.Model)
	}
	if !resolved.Verdict.Ready() {
		t.Errorf("verdict = %+v, want Available", resolved.Verdict)
	}
}

func TestResolveRuntimeWithFallback_FallbackHit(t *testing.T) {
	ff := newFallbackResolveFixture(t)
	ctx := context.Background()

	primary := ff.offlineRuntime(t, "primary", runtimeOfflineCodeNotExecutable)
	fallback1 := ff.fx.Runtime(t, "fallback-1")
	agent := ff.agentWithChain(t, "agent", primary, []FallbackRuntimeEntry{
		{RuntimeID: fallback1, Model: "fallback-model", ThinkingLevel: textPtr("low")},
	})

	resolved, err := ResolveRuntimeWithFallback(ctx, ff.lookup(), agent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.FallbackFrom != "fallback_1" {
		t.Errorf("FallbackFrom = %q, want fallback_1", resolved.FallbackFrom)
	}
	if want := mustParseUUID(t, fallback1); resolved.RuntimeID != want {
		t.Errorf("RuntimeID = %s, want fallback runtime %s", util.UUIDToString(resolved.RuntimeID), util.UUIDToString(want))
	}
	if !resolved.Model.Valid || resolved.Model.String != "fallback-model" {
		t.Errorf("Model = %+v, want fallback-model", resolved.Model)
	}
	if !resolved.ThinkingLevel.Valid || resolved.ThinkingLevel.String != "low" {
		t.Errorf("ThinkingLevel = %+v, want low", resolved.ThinkingLevel)
	}
	if !resolved.Verdict.Ready() {
		t.Errorf("verdict = %+v, want Available", resolved.Verdict)
	}
}

func TestResolveRuntimeWithFallback_FallbackIndexIsOneBased(t *testing.T) {
	ff := newFallbackResolveFixture(t)
	ctx := context.Background()

	primary := ff.offlineRuntime(t, "primary", runtimeOfflineCodeNotExecutable)
	broken := ff.offlineRuntime(t, "fallback-broken", runtimeOfflineCodeNotExecutable)
	live := ff.fx.Runtime(t, "fallback-live")
	agent := ff.agentWithChain(t, "agent", primary, []FallbackRuntimeEntry{
		{RuntimeID: broken},
		{RuntimeID: live, Model: "second"},
	})

	resolved, err := ResolveRuntimeWithFallback(ctx, ff.lookup(), agent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.FallbackFrom != "fallback_2" {
		t.Errorf("FallbackFrom = %q, want fallback_2 (1-indexed chain)", resolved.FallbackFrom)
	}
	if want := mustParseUUID(t, live); resolved.RuntimeID != want {
		t.Errorf("RuntimeID = %s, want %s", util.UUIDToString(resolved.RuntimeID), util.UUIDToString(want))
	}
}

func TestResolveRuntimeWithFallback_AllBlockedReturnsPrimaryVerdict(t *testing.T) {
	ff := newFallbackResolveFixture(t)
	ctx := context.Background()

	primary := ff.offlineRuntime(t, "primary", runtimeOfflineCodeNotExecutable)
	broken := ff.offlineRuntime(t, "fallback-broken", runtimeOfflineCodeDshProfile)
	agent := ff.agentWithChain(t, "agent", primary, []FallbackRuntimeEntry{{RuntimeID: broken}})

	resolved, err := ResolveRuntimeWithFallback(ctx, ff.lookup(), agent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// The refusal must name the agent's own runtime and its actual problem,
	// not the last fallback's failure.
	if !resolved.Verdict.Blocked() {
		t.Fatalf("verdict = %+v, want Blocked", resolved.Verdict)
	}
	if resolved.Verdict.Reason != dispatch.ReasonRuntimeUnusable {
		t.Errorf("Reason = %q, want the primary's %q", resolved.Verdict.Reason, dispatch.ReasonRuntimeUnusable)
	}
	if resolved.RuntimeID != agent.RuntimeID {
		t.Errorf("RuntimeID = %s, want primary (verdict identity)", util.UUIDToString(resolved.RuntimeID))
	}
}

func TestResolveRuntimeWithFallback_WaitableShortCircuits(t *testing.T) {
	ff := newFallbackResolveFixture(t)
	ctx := context.Background()

	// Offline with NO structured reason = waitable: the machine is just asleep
	// and comes back on its own, so the chain must not move to the live
	// fallback below.
	primary := ff.fx.Runtime(t, "primary", testutil.Cols{"status": "offline"})
	live := ff.fx.Runtime(t, "fallback-live")
	agent := ff.agentWithChain(t, "agent", primary, []FallbackRuntimeEntry{
		{RuntimeID: live, Model: "would-have-won"},
	})

	resolved, err := ResolveRuntimeWithFallback(ctx, ff.lookup(), agent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Verdict.Availability != AgentWaitable {
		t.Errorf("Availability = %v, want Waitable", resolved.Verdict.Availability)
	}
	if resolved.FallbackFrom != "primary" {
		t.Errorf("FallbackFrom = %q, want primary (chain must not advance past Waitable)", resolved.FallbackFrom)
	}
}

func TestResolveRuntimeWithFallback_SystemTierUsedLast(t *testing.T) {
	ff := newFallbackResolveFixture(t)
	ctx := context.Background()

	primary := ff.offlineRuntime(t, "primary", runtimeOfflineCodeNotExecutable)
	broken := ff.offlineRuntime(t, "fallback-broken", runtimeOfflineCodeNotExecutable)
	systemRuntime := ff.fx.Runtime(t, "system-fallback")
	agent := ff.agentWithChain(t, "agent", primary, []FallbackRuntimeEntry{{RuntimeID: broken}})

	settings, err := json.Marshal(systemConfigSettings{
		FallbackRuntime: &FallbackRuntimeEntry{RuntimeID: systemRuntime, Model: "system-model"},
	})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	ff.fx.Exec(t, "UPDATE system_config SET settings = $1 WHERE id = 1", string(settings))
	// Restore the empty default so unrelated tests never see this row.
	ff.fx.Cleanup(t, "UPDATE system_config SET settings = '{}'::jsonb WHERE id = 1")

	resolved, err := ResolveRuntimeWithFallback(ctx, ff.lookup(), agent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.FallbackFrom != "system_default" {
		t.Errorf("FallbackFrom = %q, want system_default", resolved.FallbackFrom)
	}
	if want := mustParseUUID(t, systemRuntime); resolved.RuntimeID != want {
		t.Errorf("RuntimeID = %s, want system runtime %s", util.UUIDToString(resolved.RuntimeID), util.UUIDToString(want))
	}
	if !resolved.Model.Valid || resolved.Model.String != "system-model" {
		t.Errorf("Model = %+v, want system-model", resolved.Model)
	}
}

func TestResolveRuntimeWithFallback_UnboundAgentDoesNotFallBack(t *testing.T) {
	ff := newFallbackResolveFixture(t)
	ctx := context.Background()

	live := ff.fx.Runtime(t, "fallback-live")
	agent := ff.agentWithChain(t, "agent", "", []FallbackRuntimeEntry{
		{RuntimeID: live, Model: "would-have-won"},
	})

	resolved, err := ResolveRuntimeWithFallback(ctx, ff.lookup(), agent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !resolved.Verdict.Blocked() {
		t.Fatalf("verdict = %+v, want Blocked", resolved.Verdict)
	}
	if resolved.RuntimeID.Valid {
		t.Errorf("RuntimeID = %s, want unset (MUL-5559: no binding, no fallback)", util.UUIDToString(resolved.RuntimeID))
	}
}

func TestResolveRuntimeWithFallback_PrimaryDBErrorReturnsError(t *testing.T) {
	// A nil Queries handle makes the primary runtime read fail. The error must
	// come back untouched — callers split on it (autopilot fails open, enqueue
	// fails closed) — and it must not be folded into a Blocked verdict.
	ff := newFallbackResolveFixture(t)
	ctx := context.Background()

	primary := ff.fx.Runtime(t, "primary")
	agent := ff.agentWithChain(t, "agent", primary, nil)

	lookup := RuntimeLookup{Queries: db.New(scanErrDBTX{err: context.DeadlineExceeded}), Source: "agent_ready_test"}
	resolved, err := ResolveRuntimeWithFallback(ctx, lookup, agent)
	if err == nil {
		t.Fatalf("want error from primary read failure, got resolved=%+v", resolved)
	}
	if resolved.FallbackFrom != "primary" {
		t.Errorf("FallbackFrom = %q, want primary", resolved.FallbackFrom)
	}
}

func TestTaskExecutionSettings(t *testing.T) {
	primary := fallbackTestUUID(1)
	fallback := fallbackTestUUID(2)
	stray := fallbackTestUUID(3)

	chain, err := json.Marshal([]FallbackRuntimeEntry{
		{RuntimeID: util.UUIDToString(fallback), Model: "fb-model", ThinkingLevel: textPtr("high"), ServiceTier: textPtr("flex")},
		{RuntimeID: util.UUIDToString(stray), Model: "stray-model"},
	})
	if err != nil {
		t.Fatalf("marshal chain: %v", err)
	}
	agent := db.Agent{
		RuntimeID:        primary,
		Model:            pgtype.Text{String: "primary-model", Valid: true},
		ThinkingLevel:    pgtype.Text{String: "low", Valid: true},
		ServiceTier:      pgtype.Text{String: "scale", Valid: true},
		FallbackRuntimes: chain,
	}

	// Primary-pinned task: the agent's own settings.
	model, thinking, tier := TaskExecutionSettings(agent, db.AgentTaskQueue{RuntimeID: primary})
	if model.String != "primary-model" || thinking.String != "low" || tier.String != "scale" {
		t.Errorf("primary task settings = (%q, %q, %q), want agent's own", model.String, thinking.String, tier.String)
	}

	// Fallback-pinned task: the matching entry's settings.
	model, thinking, tier = TaskExecutionSettings(agent, db.AgentTaskQueue{RuntimeID: fallback})
	if model.String != "fb-model" || thinking.String != "high" || tier.String != "flex" {
		t.Errorf("fallback task settings = (%q, %q, %q), want entry's", model.String, thinking.String, tier.String)
	}

	// A runtime no chain entry names (chain edited after enqueue): empty
	// settings so the daemon uses the runtime's own defaults.
	model, thinking, tier = TaskExecutionSettings(agent, db.AgentTaskQueue{RuntimeID: fallbackTestUUID(4)})
	if model.Valid || thinking.Valid || tier.Valid {
		t.Errorf("unmatched task settings = (%+v, %+v, %+v), want empty", model, thinking, tier)
	}

	// Unset task runtime: the agent's own settings.
	model, _, _ = TaskExecutionSettings(agent, db.AgentTaskQueue{})
	if model.String != "primary-model" {
		t.Errorf("unset task runtime settings = %q, want primary-model", model.String)
	}
}
