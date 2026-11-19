# Upgrade Loop squad preset

An unattended dependency-upgrade squad: a human files an upgrade issue once,
and three agents run the loop — the planner maps the upstream breaking
changes onto the repository, the upgrader bumps and migrates, and the
verifier runs the full build/typecheck/test suite, reviews the diff, and
records the verdict. A leader coordinates by comment; the server counts
rework rounds (the cap is low by default — an upgrade that needs many attempts
should reach a human, not grind) and escalates when it is hit.

```
human files upgrade issue → leader ⇄ planner ⇄ upgrader ⇄ verifier
                                  │
                  accept → issue done │ reject → next round (server-counted)
                                  └─ round cap exceeded → server notifies the human
```

## Layout

| File | Purpose |
| --- | --- |
| `setup.sh` | Creates the four agents, the squad with its pipeline policy, and wires members + instructions. |
| `instructions/leader.md` | Dispatch/round/stand-down protocol for the coordinating agent. |
| `instructions/planner.md` | Release-notes research and the migration plan (no code). |
| `instructions/upgrader.md` | Bump, migrate, keep the build green. |
| `instructions/verifier.md` | Full verification and the only verdict authority. |
| `squad-protocol.md` | Human-facing protocol spec: endpoints, metadata contract, wake rules. |
| `issue-template.md` | Issue description template for an upgrade. |

## Prerequisites

- A running Multica server and a logged-in `multica` CLI with workspace context
  (`--workspace-id`, `MULTICA_WORKSPACE_ID`, or `multica config set
  workspace_id <id>`).
- At least one runtime in the workspace (`multica runtime list`).
- Permission to create agents and squads in that workspace.

## Quick start

```bash
cd docs/engineering/upgradeloop-squad
./setup.sh                      # picks the first listed runtime, 2 rounds
RUNTIME_ID=<uuid> CODE_RUNTIME_ID=<uuid> MAX_ROUNDS=3 ./setup.sh
```

`RUNTIME_ID` hosts the leader, planner, and verifier, `CODE_RUNTIME_ID` the
upgrader (defaults to `RUNTIME_ID`). The script pins a known-good model
pairing (`MODEL_COORD` for the leader/planner/verifier, `MODEL_CODE` for the
upgrader); pass either as empty to accept the server default:

```bash
MODEL_COORD="" MODEL_CODE="" ./setup.sh
```

The script prints the squad and agent ids. It is a one-shot provisioner:
re-running it creates a second set of agents.

File an upgrade (run from this directory so `--description-file` resolves):

```bash
multica issue create \
  --title "Upgrade <dependency> <from> → <to>" \
  --description-file issue-template.md \
  --assignee-id <squad-id>
```

Assigning the issue to the squad wakes the leader; from there the loop is
comment-driven and unattended. Read the issue's comments
(`multica issue comment list <issue-id>`) to follow along.

To run the loop on a schedule, point a Multica autopilot at the issue-create
command above (check first whether the dependency has a newer version, so an
idle week opens no issue).

## Round semantics

- One round = plan (round 1, or again when the rejection points at a research
  gap) → bump/migrate → full verification.
- The verifier records the verdict:
  `multica issue verdict <issue-id> accept|reject --reason "..."`.
- A reject increments `pipeline_round` on the issue and starts the next round
  with the rejection reason.
- The reject that would exceed `max_rounds` is not dispatched: the server posts
  an escalation comment, notifies the human owner, marks
  `pipeline_status=escalated`, and wakes the leader only to stand down. A
  planner answer of INFEASIBLE reaches the human the same way, via the
  leader's same-blocker notification instead of a verdict.
- `accept` closes the issue (`done`) through the normal status path and marks
  `pipeline_status=accepted`.
- On acceptance, if the repository has a remote and an upgrader branch has
  commits, the leader pushes it and opens a PR; the final summary comment
  carries the PR link.

Round counting, verdict authorization (verifier only), and escalation are
enforced server-side; see `squad-protocol.md` and
`server/internal/handler/issue_pipeline.go`.

## Tuning

```bash
# Change the round cap (the pipeline block is replaced as a whole, so restate
# the verdict agent):
multica squad update <squad-id> --pipeline-max-rounds 3 --pipeline-product-agent <agent-id>

# Turn the loop off (removes the policy block):
multica squad update <squad-id> --pipeline-max-rounds 0
```

Instructions are plain agent instructions — edit them with
`multica agent update <agent-id> --instructions "$(cat instructions/<role>.md)"`
(re-run `setup.sh`'s id substitution first if you edit the templates in place).

## Teardown

```bash
multica squad delete <squad-id>
multica agent update <agent-id> --status archived   # for each of the four agents
```

## Development

The preset ships with the server-side verdict endpoint:

- Handler: `server/internal/handler/issue_pipeline.go`
- Policy: `server/internal/squadworkflow/policy.go`
- CLI: `server/cmd/multica/cmd_issue_pipeline.go` (`issue verdict`),
  `server/cmd/multica/cmd_squad.go` (`--pipeline-*` flags)
- Tests: `server/internal/handler/issue_pipeline_test.go`,
  `server/cmd/multica/cmd_issue_pipeline_test.go`
