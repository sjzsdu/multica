# Squad contracts

Behavioral detail behind `multica-squads/SKILL.md`. Everything here is
user-observable behavior of the squad feature: object model, leader briefing,
leader evaluation recording, enqueue gates, workflow policy, and access rules.

## Object model

The `squad` record stores `name`, `description`, `leader_id`, `creator_id`,
archive metadata (`archived_at` / `archived_by`), and `instructions`.
`squad_member` stores `member_type` (constrained to `agent` or `member`),
`member_id`, and `role`. Issue `assignee_type` supports `squad`.

Field semantics:

- `name` — display name, unique per workspace.
- `description` — human-facing display text; no proven runtime prompt consumer.
- `instructions` — squad-level instructions added to the squad LEADER briefing.
  They are never injected into every squad member automatically.
- `leader_id` — the agent that squad-routed work runs through.
- `creator_id` — creator of the squad.
- `archived_at` / `archived_by` — archive metadata; archived squads are
  rejected by assignment and autopilot routing paths.
- `avatar_url` — optional squad avatar URL.
- `member_count` / `member_preview` — list-response aggregates.
- member `member_type` — `agent` or `member`; `role` — roster label. A
  non-empty role appears in the leader briefing roster; it creates no
  scheduling, permission, or routing behavior of its own.

## Create and update

- create requires `leader_id`;
- the leader must be a workspace agent on both create and update;
- an archived leader is NOT rejected at save time — the lookup only checks the
  agent exists in the workspace. An archived leader fails closed later, at
  routing/dispatch: the readiness gate on every leader enqueue, assignment
  validation, and autopilot admission all reject it before any task is
  enqueued;
- on create the backend adds the leader as a squad member with role `leader`;
- updating `leader_id` adds the new leader as a member with role `leader` when
  it is not already a member.

## Leader briefing

Squad leader tasks append a briefing to the leader agent's instructions. The
briefing contains:

- the Squad Operating Protocol;
- the Squad Roster;
- the squad `instructions` section, only when `instructions` is non-empty.

The protocol's "own the parent issue status" responsibility is present
exactly when the issue's assignee points at this squad; on other leader paths
(for example a squad `@`-mention on an issue owned by a plain agent) the
briefing instead carries an explicit "do not change this issue's status"
instruction. Injection is keyed off the task being a leader task, which is
broader than the authority it grants on purpose. A quick-create leader task
has no issue yet and receives no status grant.

When a claim's defensive gate withholds the briefing (no squad stamped on the
task, squad hard-deleted, leader swapped after enqueue), the claim response
clears the leader-task flag too, so the run degrades to an ordinary agent
turn. The daemon derives the leader role from the claim fields, never from the
briefing text, and every claim response advertises that those fields are
authoritative; older servers without that capability make the daemon fall back
to inferring leadership from the briefing header. The field is claim-only and
never rendered into a prompt.

Roster rows carry member name, member type, mention markdown, and non-empty
role. Agent members also list their assigned workspace skills (`skills: a, b`,
or `no skills assigned` when the agent has none) so the leader can delegate by
capability instead of guessing from the role label; human members carry no
skills segment; builtin platform skills are not listed. Archived agent members
are skipped from the roster.

## Leader evaluation recording (`multica squad activity`)

- authority is the TASK row, not the issue's assignee: the task must be this
  issue's, must be flagged as a leader task, and must carry a valid squad id.
  The target issue may be assigned to anyone;
- the task is loaded workspace-scoped, and the rejection message names the
  issue the task actually runs on;
- two live authorization checks apply: the caller must be the task's agent,
  and must be the squad's current leader. The second check exists because the
  leader-task flag on the row is enqueue-time intent — when the leader was
  swapped before the claim, the claim downgrades the run to an ordinary agent
  turn while the row keeps its flag, and without the live check such a
  downgraded run could write a leader verdict and suppress its own comment;
- a leader agent running a NON-leader task is rejected: it is not running as
  the leader;
- the recorded activity is attributed to the task's agent, which is what the
  `no_action` comment-suppression lookup matches on;
- the `no_action` comment prohibition is conditional on this write succeeding,
  so the injected instructions tell leaders to fall back to one short comment
  when the call errors — and only when the turn has not already commented.

## Issue assignment

- `assignee_type = "squad"` routes work to `squad.leader_id`; it never enqueues
  every member;
- assignment while the issue is in `backlog` does not immediately enqueue;
  moving out of `backlog` can enqueue the leader;
- changing the assignee cancels the issue's existing tasks before enqueueing
  the new assignee path;
- private-leader access is checked at assign time and again at enqueue time;
- archived squad and archived leader are rejected at assign time;
- pending-task dedup prevents a second leader enqueue while one is queued;
- parent status is agent-managed: the leader's first dispatch turn moves the
  parent to `in_progress` and keeps it there while members work; the parent
  moves to `in_review` only when a later re-trigger confirms the overall goal
  is met. Completing a leader task does not itself write issue status, and
  starting or completing a task never writes issue status behind the scenes;
- status names are category rules: a workspace may define custom statuses, and
  each inherits its category's behavior in full. When custom statuses exist,
  the runtime brief lists the workspace catalog. Two writes are literal-key
  exceptions: failed-task rollback writes `todo`, and a merged PR with close
  intent writes `done`.

## Comment and mention triggers

- a new comment on a squad-assigned issue can wake the squad leader — leader
  routing, never member fan-out. The same computation backs the trigger
  preview endpoint;
- an explicit `mention://squad/<id>` resolves the squad and adds a leader
  trigger using the comment as the trigger comment;
- leader tasks are enqueued as leader tasks, which is what carries the briefing;
- leader self-trigger loops are guarded: a comment that would re-trigger the
  same leader while its own task is the latest one is skipped, and an explicit
  mention of a member is not redirected into a leader wake.

## Autopilots

- a squad-assigned autopilot resolves its executable agent from
  `squad.leader_id`;
- save-time validation rejects an archived squad or archived leader, and
  dispatch re-resolves and re-checks readiness;
- an archived squad fails closed and dispatch is skipped;
- `create_issue` keeps the created issue assigned to the squad while the
  resolved leader executes; `run_only` creates the task directly for the
  resolved leader.

## Child completion and stage barriers

- when a child issue closes a stage barrier and the parent is assigned to a
  squad, the parent squad leader is woken — leader-only, one task, no member
  fan-out;
- the wake has no self-trigger guard: a same-squad or shared-leader child still
  wakes the parent leader, because the wake is a serial handoff onto the PARENT
  and is the only carrier of the stage-barrier "advance / wrap up" instruction.
  Re-triggering is bounded by the existing pending-task dedup for the parent
  issue and agent;
- child completion does NOT re-check whether the child's completer may invoke
  the leader: the parent was already permission-checked at squad-assign time,
  so waking its own leader is a coordination handoff, not a fresh invocation.
  Any future invocation gate must cover agent-assigned and squad-assigned
  parents together or pipelines strand after stage one;
- the barrier never auto-advances the parent to a terminal status. The system
  comment asks the leader to continue or — when the overall goal is met — to
  run `multica issue status <parent-id> in_review`. `done` stays a human or
  integration owned write.

## Squad workflow policy

- the workflow policy is stored separately from leader instructions and is
  resolved per squad. The default is manual promotion with terminal-only
  completion signals; staged or delivery-oriented squads may use
  `review_ready` to wake on the normal `in_review` hand-off;
- completion and stage decisions run through a persistence-neutral
  coordinator; the handler adapts stored rows into domain snapshots, and a
  gate evaluator is the extension point for CI, review, merge, and
  human-approval adapters;
- `max_active_children` is enforced during child creation and
  backlog-to-active promotion while holding the workspace transaction lock;
  re-parenting an already-active child is guarded the same way;
- `auto_promote_stage` promotes the next stage's `backlog` sub-issues to
  `todo` only after the coordinator gates pass, subject to
  `max_active_children`; otherwise promotion is the woken agent's decision;
- `auto_finalize_parent` independently moves a fully gated parent to
  `in_review`; the server never auto-marks the parent `done`;
- closed barriers are persisted as workflow runs and retried under a database
  lease by a retry worker; a stale worker whose lease was replaced can no
  longer update the run;
- the issue's workflow-run history is exposed over the API, and a
  workflow-run approvals endpoint records human review or final-approval
  decisions and wakes the durable retry run.

## Private leader access (invocation gate)

This is the trigger gate, distinct from the view gate on private agents:

- enqueueing a squad leader loads the leader and applies the agent invocation
  rules;
- the rules judge by the effective invoking user: a member actor is itself; an
  agent or system actor resolves to the top-of-chain human originator, which
  may be empty;
- the agent owner may always invoke their own agent;
- a private leader is deny-by-default: no admin bypass and no agent-to-agent
  bypass — only the owner branch passes;
- a `public_to` leader consults its invocation allow-list: a `workspace`
  target admits any workspace member and workspace-internal agent/system
  principals even with no resolved human; a `member` target requires the
  resolved human to match; `team` targets are inert in V1;
- the squad assign and promote paths deny the enqueue when the actor cannot
  invoke the leader (member authors are their own originator; agent-authored
  triggers resolve to no human, so a private leader does not wake from them);
- the child-done wake deliberately does NOT use this gate (see above).
