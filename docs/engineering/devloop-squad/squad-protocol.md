# Dev Loop squad protocol

The dev loop squad runs an unattended feature-iteration loop on a single issue.
A human provides the iteration direction once (the issue), and from then on the
squad explores, implements, reviews, and verifies until the product agent
accepts — or the server stops the loop and calls the human.

## Participants

| Role | Responsibility |
| --- | --- |
| Leader | Dispatch by comment, track rounds, stand down on escalation. Never does the work. |
| Product / QA | Explores the product (browser + static review), verifies, records the only verdict. |
| Developer | Implements the dispatched scope. |
| Reviewer | Independent code review; `VERDICT: APPROVE` / `VERDICT: CHANGES REQUESTED`. |

Agent names and ids are fixed by `setup.sh` (`devloop-leader`,
`devloop-product`, `devloop-developer`, `devloop-reviewer`) and rendered into
each agent's instructions at creation time.

All four are plain squad members; the leader is the squad's leader. The issue's
assignee is the squad (`assignee_type=squad`), so assigning the issue wakes the
leader with a squad-leader briefing.

## Flow

```
human files issue (direction) ──► leader
                                   │ round 1
                                   ▼
                     product.explore ──► developer ──► reviewer
                                                          │ approve
                     ┌────────────────────────────────────┘
                     ▼
               product.verify ── verdict ──► accept  → issue done, loop ends
                     ▲                        reject  → next round: developer fixes
                     │                               (round counted by the server)
                     └── reviewer CHANGES REQUESTED → developer fixes (≤2 passes/round)
```

Everything happens as comments on that one issue. A comment that mentions an
agent wakes that agent; a member's reply mentions the leader to wake the leader
back up. One comment dispatches exactly one agent.

## Code handoff

Every task runs in its own git worktree of the same repository on an
`agent/<role>/<task-id>` branch, so work only crosses tasks as commits:

- The developer commits before reporting done; uncommitted files are invisible
  to every other task.
- The reviewer reviews the developer branch's diff (`git diff
  HEAD...<agent/devloop-developer/...>`), never its own working tree.
- The developer merges earlier `agent/devloop-developer/*` branches before
  implementing, so rounds accumulate on one line of work.
- On acceptance, when a remote is configured and a developer branch is ahead of
  the default branch, the leader pushes it, opens a PR, and puts the PR link in
  the final summary comment.

## Server-enforced verdict loop

The squad carries a pipeline policy; presence of the block enables the loop:

```
pipeline: { max_rounds: <N>, product_agent: <agent uuid> }
```

- Endpoint: `POST /api/issues/{id}/pipeline/verdict`
  body `{"verdict": "accept" | "reject", "reason": "<text>"}`
  (CLI: `multica issue verdict <issue-id> <accept|reject> --reason "..."`).
- Only the configured product agent may call it (403 otherwise); the issue must
  be assigned to a squad of that agent (403); a missing pipeline policy is 409;
  a finished pipeline (`accepted`/`escalated`) is 409 on further verdicts.
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
- A plain `reject` writes no comment; the product agent's reply to the leader is
  what wakes the leader for the next round. That reply is part of the protocol,
  not optional.
