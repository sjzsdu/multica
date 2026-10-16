---
name: multica-squads
description: "Use when creating, inspecting, updating, assigning to, or debugging a Multica squad, including how leader routing picks who runs."
user-invocable: false
allowed-tools: Bash(multica *)
---

# Multica Squads

## Quick start

If debugging why a squad did or did not run, inspect first:

```bash
multica issue get <issue-id> --output json
multica squad get <squad-id> --output json
multica squad member list <squad-id> --output json
multica issue comment list <issue-id> --roots-only --summary --output json
multica issue comment list <issue-id> --thread <thread-id> --tail 30 --output json
```

The two comment reads are a sequence: scan the roots first, then open the
threads that look relevant — mention triggers, failure reasons, and user
instructions usually live in the replies, which the roots scan never returns.

Do not assign, comment, mention, update, delete, or record squad activity just
to test. These can mutate workspace state or trigger agent runs.

## Core model

A squad is a workspace routing and coordination object. It is not an agent and
does not run work by itself: squad-routed work runs through the squad's
`leader_id` agent.

- assigning an issue to a squad routes to the leader;
- mentioning a squad routes to the leader;
- squad-assigned autopilot resolves to the leader;
- squad members are not fanned out;
- squad `instructions` are leader briefing content, not member prompts.

## CLI

```bash
multica squad list --output json
multica squad get <squad-id> --output json
multica squad create --name <name> --leader <agent-name-or-id> --output json
multica squad update <squad-id> --instructions "<leader coordination policy>" --output json
multica squad delete <squad-id>
multica squad member list <squad-id> --output json
multica squad member add <squad-id> --member-id <id> --type agent|member --role <role> --output json
multica squad member remove <squad-id> --member-id <id> --type agent|member
multica squad member set-role <squad-id> --member-id <id> --member-type agent|member --role <role> --output json
multica squad activity <issue-id> action|no_action|failed --reason "<why>" --output json
```

Creating a squad requires `leader_id` (a workspace agent); the leader is added
as a member with role `leader` when missing, on create and on every leader
change. An archived leader is accepted at save time but fails closed later at
routing/dispatch: assignment, autopilot admission, and the comment/mention
readiness gate all reject it before any task is enqueued.

`activity` is a write: it records the leader's evaluation decision on an issue.
Use it only when acting as the squad leader after evaluating a trigger, and
only against the issue your current turn is running on — the target does NOT
need to be assigned to your squad; the server checks your task row
(`is_leader_task` plus a stamped `squad_id`), not the issue's assignee. A
leader woken by a stage barrier runs on the PARENT issue, so record against
the parent, not the child you just read. If the call fails, post one short
comment with the outcome instead — and only when this turn has not already
commented: on the `action` path your delegation comment is already that record.

## Leader briefing and fields

For squad leader tasks, Multica appends a leader briefing to the leader agent
instructions: the Squad Operating Protocol, the Squad Roster, and the squad
`instructions` when non-empty. Roster rows carry name, type, mention markdown,
and non-empty role; agent members also list their assigned workspace skills so
the leader can delegate by capability, while humans carry no skills segment.
Owning the parent issue's status is granted only when the issue's assignee
points at THIS squad; other leader paths get an explicit "do not change this
issue's status" instead.

Field semantics: `instructions` is leader-facing briefing content, never
injected into every member; `description` is display metadata with no proven
runtime prompt impact; member `role` is roster context, not scheduling or
permissions; `archived_at` / `archived_by` mark archived squads, which every
routing path rejects. The full field list, briefing construction, and the
leader-evaluation recording rules live in `references/squad-contracts.md`.

## Assignment, comments, mentions

Assign with `assignee_type = "squad"`, `assignee_id = <squad-id>`; the server
routes to `leader_id`, never the whole roster.

- assignment while status is `backlog` does not start work; moving out of
  `backlog` can trigger the leader;
- changing the assignee cancels existing tasks for the issue first;
- the leader's first dispatch turn moves the parent to `in_progress` and keeps
  it there while members work; the parent moves to `in_review` only when a
  later re-trigger confirms the overall goal is met;
- completing a leader task does not itself change issue status.

If an issue is assigned to a squad, a new comment can wake the leader — that
is leader routing, not member fan-out. The mention form is:

```md
[@Squad Name](mention://squad/<squad-id>)
```

which resolves the squad, reads `leader_id`, and enqueues one leader task
using the current comment as the trigger.

## Autopilots

Squad-assigned autopilots resolve their executable agent from
`squad.leader_id`; admission and readiness checks run against the leader, and
archived squads skip dispatch. A `create_issue` autopilot's created issue keeps
`assignee_type = "squad"` while the leader executes; `run_only` creates the
task directly for the leader.

## Handling complaints or product gaps

When the user says squad behavior is wrong, classify first: expected current
behavior, configuration issue, product limitation, or actual bug — then
explain it; if it is correct but product-wise bad, say so and propose a scoped
change. Do not silently change squad routing, fan-out, briefing, autopilot, or
comment-trigger behavior without confirmation.

## Side effects

These can trigger agent work or mutate durable state: creating, updating, or
deleting a squad; changing `leader_id`; adding/removing members; changing
member roles; assigning an issue to a squad; moving a squad-assigned issue out
of backlog; commenting on or mentioning a squad; squad-assigned autopilots;
recording squad activity; deleting or archiving a squad. Do not perform
side-effecting actions as tests unless the user explicitly authorizes them.

## Common wrong assumptions

- A squad is not an agent; work routes to `leader_id`, not every member.
- `instructions` are leader briefing content, not member prompts; `role` is
  roster context, not scheduling.
- Backlog assignment does not immediately start work.
- First leader dispatch is not parent completion — the parent stays
  `in_progress` until a later re-trigger confirms the goal; the server never
  auto-flips parent status.
- The leader briefing does NOT imply status authority: an `@`-mentioned squad
  on someone else's issue is a guest — delegation yes, `issue status` no.

## References

`references/squad-contracts.md` — full field model, briefing construction,
leader-evaluation recording, enqueue and access gates, stage-barrier parent
wake, and workflow policy.
