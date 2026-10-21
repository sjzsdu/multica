package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/dispatch"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// AgentAvailability is what a readiness check concluded, in the vocabulary the
// callers actually branch on.
//
// The distinction that matters is not "ready or not" but whether WAITING is a
// plan. A sleeping laptop comes back on its own, so queued work is right to
// wait for it; an agent bound to nothing, or bound to a machine whose CLI
// cannot run, will never pick that work up until a human acts. dispatch's
// reason codes already draw that line — this type is the server-side half so
// every admission path draws it the same way.
type AgentAvailability int

const (
	// AgentAvailable: the agent can take work now.
	AgentAvailable AgentAvailability = iota
	// AgentWaitable: not runnable right now, but nothing is broken — the
	// machine is offline and work queued for it runs when it returns.
	AgentWaitable
	// AgentBlocked: nothing will claim this agent's work until someone
	// intervenes. Callers must refuse the trigger and say why.
	AgentBlocked
)

// AgentVerdict is a readiness decision plus everything a caller needs to act on
// it: the wire reason code, and — when the daemon reported one — the command
// that repairs the runtime.
type AgentVerdict struct {
	Availability AgentAvailability
	// Reason is the dispatch code for a non-available verdict, empty otherwise.
	Reason dispatch.ReasonCode
	// Repair is the fix the daemon reported for an unusable runtime: the npm
	// package that owns the broken entry point and the command that reinstalls
	// it. Absent for every other verdict, and for a runtime whose daemon is too
	// old to report one.
	Repair *RuntimeRepair
	// Detail is the daemon's own description, for logs and for the record left
	// on a blocked non-interactive trigger. Never parsed.
	Detail string
}

// Ready reports whether the agent can take work right now.
func (v AgentVerdict) Ready() bool { return v.Availability == AgentAvailable }

// Blocked reports whether waiting is futile and the caller must refuse the
// trigger rather than queue it.
func (v AgentVerdict) Blocked() bool { return v.Availability == AgentBlocked }

// RuntimeRepair mirrors the daemon's agent.ExecFormatRepair over the wire.
type RuntimeRepair struct {
	Package string `json:"package,omitempty"`
	Command string `json:"command,omitempty"`
	// Shell is the interpreter Command is written for, as reported by the
	// machine that must run it ("bash", "powershell"). Rendered as the code
	// block's language so a Windows user is not handed POSIX syntax in a bash
	// fence. Empty from a daemon too old to report it.
	Shell string `json:"shell,omitempty"`
}

// runtimeOfflineReason is the daemon's structured explanation, stored on the
// runtime row's metadata by the deregister handler.
type runtimeOfflineReason struct {
	Code   string         `json:"code"`
	Detail string         `json:"detail"`
	Repair *RuntimeRepair `json:"repair"`
	// Installing: the daemon has an automatic install in flight, so this offline
	// runtime is expected to come back on its own and work may queue.
	Installing bool `json:"installing"`
}

// runtimeOfflineCodeNotExecutable is the daemon's code for "the OS refuses to
// execute this agent CLI" (daemon.RuntimeOfflineCodeNotExecutable). Compared as
// a string because the server must not import the daemon package.
const runtimeOfflineCodeNotExecutable = "not_executable"

// runtimeOfflineCodeDshProfile is the daemon's code for "this runtime's DSH
// runtime profile is not installed" (daemon.RuntimeOfflineCodeDshProfile). Same
// string-comparison rule as above, and the same MUL-6164 semantics: with no
// install configured the wait never resolves, so the trigger is refused.
// runtimeOfflineReason.Installing is the exception — a daemon that is installing
// the profile right now resolves it by itself.
const runtimeOfflineCodeDshProfile = "dsh_profile"

// RuntimeBlockedNeedsNotice reports whether a blocked verdict's reason is one a
// human has to repair on the runtime's machine, and therefore one that must
// leave a durable explanation on the issue (MUL-6164).
//
// A predicate rather than an inline comparison because three admission paths
// ask it — the refused @mention, the refused assignment, and the refused
// assign-on-create — and a code added to one of them but not the others is a
// trigger that vanishes with no trace on exactly the surfaces that have no
// response for the user to read.
func RuntimeBlockedNeedsNotice(code dispatch.ReasonCode) bool {
	return code == dispatch.ReasonRuntimeUnusable ||
		code == dispatch.ReasonRuntimeProfileMissing ||
		code == dispatch.ReasonRuntimeAccessDenied
}

// AgentReadiness reports whether an agent can accept new work right now, and
// what the caller should do when it cannot.
//
// The lookup carries the connection to read on plus the source label the
// runtime read is attributed to (MUL-6884), so each admission path stays
// distinguishable in multica_agent_runtime_lookup_total.
//
// err is non-nil only on DB lookup failure for the runtime row. Callers that
// treat a transient DB error as "do not skip" (the autopilot admission gate)
// should swallow it; callers that need a hard yes/no (the squad-leader
// pre-enqueue check in the handler) should fail closed.
//
// This is the single source of truth shared by:
//   - service.shouldSkipDispatch (autopilot admission gate)
//   - service.dispatchRunOnly    (squad-leader runtime check, MUL-2429)
//   - handler.isSquadLeaderReady (issue-assign / comment-trigger path)
//   - the direct-agent trigger paths, which consult it for the BLOCKED verdict
//     only: an offline machine still queues, because that wait ends by itself.
//
// Keeping these aligned matters because the paths can otherwise drift — e.g.
// one starts allowing "starting" runtimes while another doesn't, and the bug
// only surfaces when a user assigns the same agent through two different entry
// points. Touch this function, all of them move together.
func AgentReadiness(ctx context.Context, lookup RuntimeLookup, agent db.Agent) (AgentVerdict, error) {
	if agent.ArchivedAt.Valid {
		return AgentVerdict{
			Availability: AgentBlocked,
			Reason:       dispatch.ReasonTargetUnavailable,
			Detail:       "agent is archived",
		}, nil
	}
	if !agent.RuntimeID.Valid {
		return AgentVerdict{
			Availability: AgentBlocked,
			Reason:       dispatch.ReasonAgentRuntimeRequired,
			Detail:       "agent has no runtime bound",
		}, nil
	}
	rt, err := lookup.Get(ctx, agent.RuntimeID)
	if err != nil {
		return AgentVerdict{}, err
	}
	return runtimeVerdict(rt, agent), nil
}

// runtimeVerdict combines runtime health with the ownership binding that
// determines whether this agent can execute there.
func runtimeVerdict(rt db.AgentRuntime, agent db.Agent) AgentVerdict {
	if rt.Visibility == "private" && rt.OwnerID.Valid && (!agent.OwnerID.Valid || agent.OwnerID != rt.OwnerID) {
		return AgentVerdict{
			Availability: AgentBlocked,
			Reason:       dispatch.ReasonRuntimeAccessDenied,
			Detail:       "agent owner does not match private runtime owner",
		}
	}
	if rt.Status == "online" {
		return AgentVerdict{Availability: AgentAvailable}
	}
	// Offline with a reason the daemon says a human must repair: refuse rather
	// than queue, and carry the repair so the caller can show it.
	if reason, ok := parseRuntimeOfflineReason(rt.Metadata); ok {
		switch {
		case reason.Code == runtimeOfflineCodeNotExecutable:
			return AgentVerdict{
				Availability: AgentBlocked,
				Reason:       dispatch.ReasonRuntimeUnusable,
				Repair:       reason.Repair,
				Detail:       reason.Detail,
			}
		// A missing runtime profile is the same shape of finding — the machine
		// is reachable and its CLI cannot serve work — but its own reason code,
		// because the CLI is not the broken part and "reinstall the CLI" copy
		// would send the user to the wrong repair. The one exception is an
		// install the daemon is running right now: that wait DOES end by
		// itself, so the work queues instead of being refused.
		case reason.Code == runtimeOfflineCodeDshProfile && !installClaimHolds(reason, rt):
			return AgentVerdict{
				Availability: AgentBlocked,
				Reason:       dispatch.ReasonRuntimeProfileMissing,
				Repair:       reason.Repair,
				Detail:       reason.Detail,
			}
		}
	}
	return AgentVerdict{
		Availability: AgentWaitable,
		Reason:       dispatch.ReasonRuntimeOffline,
		Detail:       "agent runtime is " + rt.Status,
	}
}

// runtimeInstallClaimWindow is how long an `installing` claim on a runtime row
// is honoured before the row is read as a plain missing profile.
//
// The claim is what makes this one offline runtime worth queueing behind, so a
// claim that outlives its install recreates exactly the failure the structured
// reason exists to prevent — work queued behind something that stopped. The
// daemon withdraws the claim when an install gives up, but a withdraw cannot be
// guaranteed: the verdict is built before the runtime ids the withdraw needs are
// known, the corrective Deregister is best-effort and runs on an already-
// cancelled context during shutdown, and a daemon restarted mid-install does not
// track the row at all.
//
// So the claim is bounded rather than guaranteed. SetAgentRuntimeOfflineWithReason
// stamps updated_at, and the install it describes is itself bounded by the
// daemon's provisioning timeout; a window comfortably longer than that covers a
// real install while capping every way the claim can be left behind. The
// withdraw stays as the fast path — this is the floor under it.
const runtimeInstallClaimWindow = 5 * time.Minute

// installClaimHolds reports whether a runtime row's in-flight-install claim is
// still worth queueing behind.
//
// A row with no usable timestamp cannot be bounded, so the claim is not honoured:
// the cost of refusing during a real install is one retry the notice already
// asks for, and the cost of honouring a stale claim is work that queues until it
// expires.
func installClaimHolds(reason runtimeOfflineReason, rt db.AgentRuntime) bool {
	if !reason.Installing || !rt.UpdatedAt.Valid {
		return false
	}
	return time.Since(rt.UpdatedAt.Time) < runtimeInstallClaimWindow
}

// parseRuntimeOfflineReason reads the daemon's explanation off a runtime row.
// A row with no reason, or metadata this server cannot parse, simply has no
// explanation — never a different verdict.
func parseRuntimeOfflineReason(metadata []byte) (runtimeOfflineReason, bool) {
	if len(metadata) == 0 {
		return runtimeOfflineReason{}, false
	}
	var envelope struct {
		OfflineReason *runtimeOfflineReason `json:"offline_reason"`
	}
	if err := json.Unmarshal(metadata, &envelope); err != nil || envelope.OfflineReason == nil {
		return runtimeOfflineReason{}, false
	}
	return *envelope.OfflineReason, true
}

// RuntimeUnusableNotice is the durable explanation left on an issue when a
// trigger is refused because the target's runtime cannot serve work and only a
// human on that machine can change it.
//
// It lives here, next to the verdict, because two layers write it: the handler
// for a refused @mention and the service for a refused assignment. One text,
// one place to fix it. It names the repair command when the daemon reported
// one and stays useful when it did not — a natively installed CLI has no
// postinstall to re-run, and inventing a command would send the user somewhere
// that does not exist.
//
// The text is chosen by the verdict's reason, not by whether a repair command
// happened to be present. The two causes have opposite repairs: an unrunnable
// CLI is reinstalled, while a CLI missing its runtime profile is working
// perfectly and reinstalling it changes nothing. One text for both told DSH
// users to reinstall a CLI that was never the problem.
func RuntimeUnusableNotice(agentName string, verdict AgentVerdict) string {
	name := agentName
	if name == "" {
		name = "The assigned agent"
	}
	if verdict.Reason == dispatch.ReasonRuntimeAccessDenied {
		return fmt.Sprintf(
			"%s cannot run on this private runtime, so this trigger was not queued. Make the runtime's machine public, or rebind/copy the agent to a runtime its owner can use.",
			name,
		)
	}
	if verdict.Reason == dispatch.ReasonRuntimeProfileMissing {
		return runtimeProfileMissingNotice(name)
	}
	if verdict.Repair != nil && verdict.Repair.Command != "" {
		return fmt.Sprintf(
			"%s could not start: its CLI is installed but cannot be executed on that machine, so this trigger was not queued.\n\n"+
				"Usually the package's postinstall was blocked (npm 12 allowScripts, pnpm 10 approve-builds, `--ignore-scripts`, `--omit=optional`) and the bin entry is still a placeholder. On that machine, run:\n\n"+
				"```%s\n%s\n```\n\n"+
				"The runtime comes back on its own within a couple of minutes; trigger the agent again after that.",
			name, repairFenceLanguage(verdict.Repair.Shell), verdict.Repair.Command,
		)
	}
	return fmt.Sprintf(
		"%s could not start: its CLI is installed but cannot be executed on that machine, so this trigger was not queued. "+
			"Reinstall the agent CLI on that machine with install scripts enabled; the runtime comes back on its own within a couple of minutes.",
		name,
	)
}

// runtimeProfileMissingNotice explains a runtime whose CLI runs but whose
// Multica runtime profile is absent.
//
// No fenced command, deliberately. The install is `dsh plugin --profile multica
// add <bundle>`, and <bundle> is the operator's own choice of package,
// directory or tarball — Multica's bridge is not on a public registry yet
// (multica#6936). Rendering that line in a code block presents a placeholder as
// something to copy and run, which is the shape of instruction people paste
// verbatim and then report as broken. Naming the two ways to supply a real
// bundle, and pointing at the docs that list them, is the honest version.
func runtimeProfileMissingNotice(name string) string {
	return fmt.Sprintf(
		"%s could not start: the DeepSeek Harness CLI is installed on that machine, but the `multica` runtime profile it needs is not, so this trigger was not queued.\n\n"+
			"The profile supplies the protocol Multica drives — the CLI itself is fine, and reinstalling it changes nothing. On that machine, either add the Multica DSH runtime bundle to the profile with `dsh plugin --profile multica add`, or set `MULTICA_DSH_PROFILE_BUNDLE` for the daemon so it installs the bundle itself. See the agent runtime install docs for the bundle to use.\n\n"+
			"The runtime registers on its own within a couple of minutes after that; trigger the agent again then.",
		name,
	)
}

// repairFenceLanguage labels the code block with the shell the command was
// written for. A daemon too old to report one predates the Windows rendering
// entirely, so its command is POSIX by construction.
func repairFenceLanguage(shell string) string {
	if shell == "powershell" {
		return "powershell"
	}
	return "bash"
}

// FallbackRuntimeEntry is one ordered entry in an agent's fallback chain.
// Each entry names a runtime + model pair — model ids are per-runtime
// (RebindAgentBuilderRuntime resets model on runtime switch), so an entry
// must carry both. Optional thinking_level / service_tier override the
// runtime's own default for that tier.
type FallbackRuntimeEntry struct {
	RuntimeID     string  `json:"runtime_id"`
	Model         string  `json:"model,omitempty"`
	ThinkingLevel *string `json:"thinking_level,omitempty"`
	ServiceTier   *string `json:"service_tier,omitempty"`
}

// ResolvedRuntime is what ResolveRuntimeWithFallback picks for a trigger.
// Every field is carried through to the task row: RuntimeID + Model (and
// optional ThinkingLevel / ServiceTier) become the agent's execution
// configuration for this run, while FallbackFrom is the audit trail that
// explains which chain entry won.
type ResolvedRuntime struct {
	RuntimeID     pgtype.UUID
	Model         pgtype.Text
	ThinkingLevel pgtype.Text
	ServiceTier   pgtype.Text
	// FallbackFrom labels which level of the chain produced this runtime.
	// Values: "primary", "fallback_<N>" (1-indexed into the agent's list),
	// or "system_default" for the singleton system_config row's entry.
	// Empty when no runtime resolved (all entries blocked).
	FallbackFrom string
	// Verdict is the readiness verdict for the picked runtime. The caller
	// branches on Verdict.Availability: Available means "go ahead and enqueue",
	// Waitable means "the runtime is offline, queue it — it'll come back",
	// Blocked means "nothing in the chain can run this right now, refuse".
	Verdict AgentVerdict
}

// ResolveRuntimeWithFallback walks the agent's runtime selection chain in
// priority order and returns the first entry whose AgentVerdict is NOT
// Blocked. The chain is:
//
//  1. agent.runtime_id + agent.model (primary)
//  2. agent.fallback_runtimes[0..N-1]
//  3. system_config.settings.fallback_runtime (global singleton)
//
// Waitable verdicts (runtime offline) short-circuit the chain: falling back
// to another machine while the first is just asleep wastes a second
// execution slot and creates a split-brain "which machine owns this run".
// Only Blocked verdicts — runtime CLI broken, profile missing, provider
// quota exhausted — justify moving to the next entry. The same logic keeps
// an unbound agent from falling back at all: the chain is stale by
// construction once the primary binding is gone (the validator requires one
// at write time), and reviving it would undo MUL-5559's refusal.
//
// err is non-nil only for a DB failure on the PRIMARY runtime read. It is
// returned instead of being folded into a Blocked verdict because callers
// intentionally split on it — the autopilot admission gate fails open on a
// transient read error, the enqueue paths fail closed — and a folded
// verdict would silently flip every one of those behaviours during an
// outage. Failures while probing later chain entries stay folded (Blocked,
// move on): the trigger was already doomed by the primary's verdict.
func ResolveRuntimeWithFallback(
	ctx context.Context,
	lookup RuntimeLookup,
	agent db.Agent,
) (ResolvedRuntime, error) {
	// 1. Primary — always tried first, on any verdict.
	primary := ResolvedRuntime{
		RuntimeID:     agent.RuntimeID,
		Model:         agent.Model,
		ThinkingLevel: agent.ThinkingLevel,
		ServiceTier:   agent.ServiceTier,
		FallbackFrom:  "primary",
	}
	if !agent.RuntimeID.Valid {
		primary.Verdict = AgentVerdict{
			Availability: AgentBlocked,
			Reason:       dispatch.ReasonAgentRuntimeRequired,
			Detail:       "agent has no runtime bound",
		}
		return primary, nil
	}
	v, err := AgentReadiness(ctx, lookup, agent)
	if err != nil {
		return primary, err
	}
	primary.Verdict = v
	if primary.Verdict.Availability != AgentBlocked {
		return primary, nil
	}

	// 2. Agent-level fallback chain. Only Blocked verdicts advance us.
	fallbackEntries := ParseFallbackRuntimes(agent.FallbackRuntimes)
	for i, entry := range fallbackEntries {
		resolved := tryFallbackEntry(ctx, lookup, entry, agent)
		if resolved.Verdict.Availability != AgentBlocked {
			resolved.FallbackFrom = fmt.Sprintf("fallback_%d", i+1)
			return resolved, nil
		}
	}

	// 3. System-level singleton fallback.
	if system := SystemFallbackRuntime(ctx, lookup.Queries); system != nil {
		resolved := tryFallbackEntry(ctx, lookup, *system, agent)
		if resolved.Verdict.Availability != AgentBlocked {
			resolved.FallbackFrom = "system_default"
			return resolved, nil
		}
	}

	// Every entry in the chain was Blocked. Return the primary verdict so
	// the caller's refusal message still names the agent's intended runtime
	// and its actual problem, not whatever the last fallback chain failure
	// happened to surface.
	return primary, nil
}

// systemConfigSettings is the JSON shape of system_config.settings. Fields
// are additive: unknown keys are ignored, so a future setting never breaks
// the fallback read.
type systemConfigSettings struct {
	FallbackRuntime *FallbackRuntimeEntry `json:"fallback_runtime"`
}

// SystemFallbackRuntime reads the operator-pinned last-resort runtime off
// the singleton system_config row. Any failure — no query handle, read
// error, malformed settings — yields nil, which simply removes the chain's
// third tier: a broken global config must never block a trigger that the
// agent's own chain could still serve.
//
// There is deliberately no write API yet (no instance-level admin surface
// exists); operators set the row directly in SQL until the settings API
// lands.
func SystemFallbackRuntime(ctx context.Context, q *db.Queries) *FallbackRuntimeEntry {
	if q == nil {
		return nil
	}
	row, err := q.GetSystemConfig(ctx)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Debug("system fallback: read system_config failed", "error", err)
		}
		return nil
	}
	var settings systemConfigSettings
	if err := json.Unmarshal(row.Settings, &settings); err != nil {
		slog.Debug("system fallback: unmarshal system_config.settings failed", "error", err)
		return nil
	}
	return settings.FallbackRuntime
}

// TaskExecutionSettings returns the model / thinking_level / service_tier a
// claimed task runs with.
//
// A task pinned to the agent's primary runtime runs the agent's own
// settings. A fallback-dispatched task (task.runtime_id different from
// agent.runtime_id) runs the settings recorded on the matching fallback
// chain entry, because model ids are per-runtime and the primary's model
// need not exist on the machine that actually claimed the work. Matching by
// runtime_id is unambiguous: verdicts are a function of the runtime row
// alone, so the first chain entry naming a given runtime always beats any
// later one, and the write-time validator rejects duplicate entries and
// fallback == primary. When nothing matches — the chain was edited between
// enqueue and claim — the settings come back empty and the daemon uses the
// runtime's own defaults.
func TaskExecutionSettings(agent db.Agent, task db.AgentTaskQueue) (pgtype.Text, pgtype.Text, pgtype.Text) {
	if !task.RuntimeID.Valid || task.RuntimeID == agent.RuntimeID {
		return agent.Model, agent.ThinkingLevel, agent.ServiceTier
	}
	for _, entry := range ParseFallbackRuntimes(agent.FallbackRuntimes) {
		var rtID pgtype.UUID
		if rtID.Scan(entry.RuntimeID) != nil || rtID != task.RuntimeID {
			continue
		}
		return entryText(entry.Model), entryOptionalText(entry.ThinkingLevel), entryOptionalText(entry.ServiceTier)
	}
	return pgtype.Text{}, pgtype.Text{}, pgtype.Text{}
}

// entryText converts a chain-entry string field into the pgtype.Text the
// task payload carries; empty means "not set, use the runtime's own default".
func entryText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

func entryOptionalText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return entryText(*s)
}

// ParseFallbackRuntimes decodes agent.fallback_runtimes JSONB into typed
// entries. Invalid JSON or an empty array both yield an empty slice — the
// next chain level simply doesn't exist. Per-runtime ownership checks
// still happen inside tryFallbackEntry so ownership drift across a
// previously-valid fallback list surfaces the same way as any other
// blocked entry.
func ParseFallbackRuntimes(raw []byte) []FallbackRuntimeEntry {
	if len(raw) == 0 {
		return nil
	}
	var entries []FallbackRuntimeEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil
	}
	return entries
}

// tryFallbackEntry constructs a transient agent-shaped value for one
// fallback entry and runs AgentReadiness on it. The "transient agent" is
// just the fields AgentReadiness reads: RuntimeID plus the agent's own
// ArchivedAt + OwnerID (for the private-runtime ownership check). We
// don't persist this agent — it exists only for the verdict.
//
// If the fallback entry itself is malformed (no runtime_id, unparseable
// UUID) we return a Blocked verdict so the caller moves on to the next
// chain entry rather than erroring.
func tryFallbackEntry(
	ctx context.Context,
	lookup RuntimeLookup,
	entry FallbackRuntimeEntry,
	agent db.Agent,
) ResolvedRuntime {
	var rtID pgtype.UUID
	if entry.RuntimeID == "" {
		return ResolvedRuntime{
			Verdict: AgentVerdict{
				Availability: AgentBlocked,
				Reason:       dispatch.ReasonAgentRuntimeRequired,
				Detail:       "fallback entry has no runtime_id",
			},
		}
	}
	if err := rtID.Scan(entry.RuntimeID); err != nil {
		return ResolvedRuntime{
			Verdict: AgentVerdict{
				Availability: AgentBlocked,
				Reason:       dispatch.ReasonAgentRuntimeRequired,
				Detail:       fmt.Sprintf("fallback entry has invalid runtime_id: %v", err),
			},
		}
	}

	// Build a transient agent-shaped value. AgentReadiness reads:
	//   agent.ArchivedAt — archived agents can't run
	//   agent.RuntimeID — the runtime to check
	//   agent.OwnerID   — private runtime ownership
	// Plus it looks up AgentRuntime via RuntimeID and checks its status/metadata.
	probeAgent := agent
	probeAgent.RuntimeID = rtID

	v, err := AgentReadiness(ctx, lookup, probeAgent)
	if err != nil {
		return ResolvedRuntime{
			RuntimeID: rtID,
			Model:     entryText(entry.Model),
			Verdict: AgentVerdict{
				Availability: AgentBlocked,
				Reason:       dispatch.ReasonInternalError,
				Detail:       fmt.Sprintf("db error reading fallback runtime: %v", err),
			},
		}
	}
	return ResolvedRuntime{
		RuntimeID:     rtID,
		Model:         entryText(entry.Model),
		ThinkingLevel: entryOptionalText(entry.ThinkingLevel),
		ServiceTier:   entryOptionalText(entry.ServiceTier),
		Verdict:       v,
	}
}
