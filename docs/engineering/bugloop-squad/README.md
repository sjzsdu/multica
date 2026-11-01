# Bug Loop squad preset

An unattended bug-fix squad: a human files a bug report once, and three
agents run the loop — the reproducer turns the report into a test that fails
because of the bug, the fixer fixes the root cause, and the reviewer verifies
the fix (repro test fail → pass, relevant suite, diff review) and records the
verdict. A leader coordinates by comment; the server counts rework rounds and
escalates to the human when the cap is hit, so no prompt has to be trusted
with stopping.

```
human files bug issue → leader ⇄ reproducer ⇄ fixer ⇄ reviewer
                              │
              accept → issue done │ reject → next round (server-counted)
                              └─ round cap exceeded → server notifies the human
```

## Layout

| File | Purpose |
| --- | --- |
| `setup.sh` | Creates the four agents, the squad with its pipeline policy, and wires members + instructions. |
| `instructions/leader.md` | Dispatch/round/stand-down protocol for the coordinating agent. |
| `instructions/reproducer.md` | Reproduce and commit the failing test. |
| `instructions/fixer.md` | Minimal root-cause fix on top of the reproduction. |
| `instructions/reviewer.md` | Independent verification and the only verdict authority. |
| `squad-protocol.md` | Human-facing protocol spec: endpoints, metadata contract, wake rules. |
| `issue-template.md` | Issue description template for a bug report. |

## Prerequisites

- A running Multica server and a logged-in `multica` CLI with workspace context
  (`--workspace-id`, `MULTICA_WORKSPACE_ID`, or `multica config set
  workspace_id <id>`).
- At least one runtime in the workspace (`multica runtime list`).
- Permission to create agents and squads in that workspace.

## Quick start

```bash
cd docs/engineering/bugloop-squad
./setup.sh                      # picks the first listed runtime, 3 rounds
RUNTIME_ID=<uuid> CODE_RUNTIME_ID=<uuid> MAX_ROUNDS=5 ./setup.sh
```

`RUNTIME_ID` hosts the leader and reviewer, `CODE_RUNTIME_ID` the reproducer
and fixer (defaults to `RUNTIME_ID`). The script pins a known-good model
pairing (`MODEL_COORD` for the leader/reviewer, `MODEL_CODE` for the
reproducer/fixer); pass either as empty to accept the server default:

```bash
MODEL_COORD="" MODEL_CODE="" ./setup.sh
```

The script prints the squad and agent ids. It is a one-shot provisioner:
re-running it creates a second set of agents.

File a bug (run from this directory so `--description-file` resolves):

```bash
multica issue create \
  --title "<bug title>" \
  --description-file issue-template.md \
  --assignee-id <squad-id>
```

Assigning the issue to the squad wakes the leader; from there the loop is
comment-driven and unattended. Read the issue's comments
(`multica issue comment list <issue-id>`) to follow along.

## Round semantics

- One round = reproduce (round 1, or again when the rejection says the
  reproduction itself is wrong) → fix → verify.
- The reviewer records the verdict:
  `multica issue verdict <issue-id> accept|reject --reason "..."`.
- A reject increments `pipeline_round` on the issue and starts the next round
  with the rejection reason.
- The reject that would exceed `max_rounds` is not dispatched: the server posts
  an escalation comment, notifies the human owner, marks
  `pipeline_status=escalated`, and wakes the leader only to stand down.
- `accept` closes the issue (`done`) through the normal status path and marks
  `pipeline_status=accepted`.
- On acceptance, if the repository has a remote and a fixer branch has
  commits, the leader pushes it and opens a PR; the final summary comment
  carries the PR link.

Round counting, verdict authorization (reviewer only), and escalation are
enforced server-side; see `squad-protocol.md` and
`server/internal/handler/issue_pipeline.go`.

## Tuning

```bash
# Change the round cap (the pipeline block is replaced as a whole, so restate
# the verdict agent):
multica squad update <squad-id> --pipeline-max-rounds 5 --pipeline-product-agent <agent-id>

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
