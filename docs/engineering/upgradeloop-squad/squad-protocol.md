# Upgrade Loop squad protocol

The upgrade loop squad runs an unattended dependency-upgrade loop on a single
issue. A human provides the dependency and the target version once (the
issue), and from then on the squad plans the migration, executes it, and
verifies it until the verifier accepts — or the server stops the loop and
calls the human.

## Participants

| Role | Responsibility |
| --- | --- |
| Leader | Dispatch by comment, track rounds, stand down on escalation. Never does the work. |
| Planner | Release-notes research; maps breaking changes onto the repository; migration plan. No code. |
| Upgrader | Bumps manifest + lockfile, migrates call sites, keeps the build green. |
| Verifier | Independent full verification (build, typecheck, suite, diff review) and the only verdict. |

Agent names and ids are fixed by `setup.sh` (`upgradeloop-leader`,
`upgradeloop-planner`, `upgradeloop-upgrader`, `upgradeloop-verifier`) and
rendered into each agent's instructions at creation time.

All four are plain squad members; the leader is the squad's leader. The issue's
assignee is the squad (`assignee_type=squad`), so assigning the issue wakes the
leader with a squad-leader briefing.

## Flow

```
human files upgrade issue ──► leader
                                │ round 1
                                ▼
                  planner ──► upgrader ──► verifier
                                                │ verdict
                           ┌────────────────────┤
                           ▼                    ▼
                    accept → issue done    reject → next round: upgrader fixes
                           │               (round counted by the server)
                           └─ round cap exceeded → server notifies the human
```

Everything happens as comments on that one issue. A comment that mentions an
agent wakes that agent; a member's reply mentions the leader to wake the leader
back up. One comment dispatches exactly one agent.

## Code handoff

Every task runs in its own git worktree of the same repository on an
`agent/<role>/<task-id>`, so work only crosses tasks as commits:

- The planner commits nothing: its plan travels as the reply comment, which
  every later task can read on the issue.
- The upgrader commits before reporting done; uncommitted files are invisible
  to every other task.
- The verifier runs the checks by merging the upgrader's branch into its own
  task branch, and reviews the diff (`git diff
  HEAD...<agent/upgradeloop-upgrader/...>`), never its own edits — it never
  edits files at all.
- The upgrader merges earlier `agent/upgradeloop-upgrader/*` branches before
  migrating, so rounds accumulate on one line of work.
- On acceptance, when a remote is configured and an upgrader branch is ahead
  of the default branch, the leader pushes it, opens a PR, and puts the PR
  link in the final summary comment.

## Server-enforced verdict loop

The squad carries a pipeline policy; presence of the block enables the loop:

```
pipeline: { max_rounds: <N>, product_agent: <agent uuid> }
```

- Endpoint: `POST /api/issues/{id}/pipeline/verdict`
  body `{"verdict": "accept" | "reject", "reason": "<text>"}`
  (CLI: `multica issue verdict <issue-id> <accept|reject> --reason "..."`).
- Only the configured verdict agent (here: the verifier) may call it (403
  otherwise); the issue must be assigned to a squad of that agent (403); a
  missing pipeline policy is 409; a finished pipeline
  (`accepted`/`escalated`) is 409 on further verdicts.
- `reject` requires a non-empty reason without `mention://` links (400).
- `accept` closes the issue (status `done`) through the normal status update
  path, then records `pipeline_status=accepted`.
- `reject` increments the round and records `pipeline_status=rejected`. When the
  reject would exceed `max_rounds`, the server instead:
  1. records `pipeline_status=escalated`,
  2. posts a top-level comment quoting the reason, mentioning the leader to
     stand down and notifying the human owner,
  3. wakes the leader via that comment — and does **not** dispatch another round.

Round counting, escalation, and owner notification all happen server-side, so no
prompt can loop past `max_rounds`.

## Issue metadata contract

| Key | Values | Written by |
| --- | --- | --- |
| `pipeline_round` | integer, rework rounds consumed | server (on each reject) |
| `pipeline_status` | `rejected` \| `escalated` \| `accepted` | server (on each verdict) |

Agents read these (`multica issue metadata list <issue-id>`) but never write
them. The verdict response also returns `verdict`, `round`, `max_rounds`,
`escalated`, and the resulting `metadata`.

## Wake rules

- Issue assigned to the squad → leader task with the squad-leader briefing.
- Comment mentioning an agent → task for that agent (this is the only dispatch
  mechanism the squad uses).
- Server escalation comment → leader task (stand-down turn) + member mention
  notification for the human owner.
- A plain `reject` writes no comment; the verifier's reply to the leader is
  what wakes the leader for the next round. That reply is part of the
  protocol, not optional.
