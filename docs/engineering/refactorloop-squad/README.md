# Refactor Loop squad preset

An unattended refactoring squad: a human states the refactoring direction
once (as an issue), and four agents run the loop — the architect mines
refactor points against an architecture checklist and defines the target
design, the refactorer applies the dispatched slice incrementally
(characterization tests first, full suite green), the reviewer guards
behavior preservation and principle application, and the architect records
the verdict. The server counts rework rounds and escalates to the human when
the cap is hit, so no prompt has to be trusted with stopping.

```
human files refactor issue → leader ⇄ architect ⇄ refactorer ⇄ reviewer
                                  │
                  accept → issue done │ reject → next round (server-counted)
                                  └─ round cap exceeded → server notifies the human
```

Refactoring here is behavior-preserving by definition: if a "refactor"
needs a behavior change, the squad reports it instead of smuggling it in.

## Layout

| File | Purpose |
| --- | --- |
| `setup.sh` | Creates the four agents, the squad with its pipeline policy, and wires members + instructions. |
| `instructions/leader.md` | Dispatch/round/stand-down protocol for the coordinating agent. |
| `instructions/architect.md` | Mine refactor points, define the target design, and record the only verdict. |
| `instructions/refactorer.md` | Incremental, behavior-preserving implementation. |
| `instructions/reviewer.md` | Independent review: behavior preservation, principle application, scope. |
| `squad-protocol.md` | Human-facing protocol spec: endpoints, metadata contract, wake rules. |
| `issue-template.md` | Issue description template for a refactoring direction. |

## Prerequisites

- A running Multica server and a logged-in `multica` CLI with workspace context
  (`--workspace-id`, `MULTICA_WORKSPACE_ID`, or `multica config set
  workspace_id <id>`).
- At least one runtime in the workspace (`multica runtime list`).
- Permission to create agents and squads in that workspace.

## Quick start

```bash
cd docs/engineering/refactorloop-squad
./setup.sh                      # picks the first listed runtime, 8 rounds
RUNTIME_ID=<uuid> CODE_RUNTIME_ID=<uuid> MAX_ROUNDS=5 ./setup.sh
```

`RUNTIME_ID` hosts the leader, architect, and reviewer, `CODE_RUNTIME_ID` the
refactorer (defaults to `RUNTIME_ID`). The script pins a known-good model
pairing (`MODEL_COORD` for the leader/architect/reviewer, `MODEL_CODE` for the
refactorer); pass either as empty to accept the server default:

```bash
MODEL_COORD="" MODEL_CODE="" ./setup.sh
```

The script prints the squad and agent ids. It is a one-shot provisioner:
re-running it creates a second set of agents.

Start a refactoring (run from this directory so `--description-file`
resolves):

```bash
multica issue create \
  --title "<refactoring direction title>" \
  --description-file issue-template.md \
  --assignee-id <squad-id>
```

Assigning the issue to the squad wakes the leader; from there the loop is
comment-driven and unattended. Read the issue's comments
(`multica issue comment list <issue-id>`) to follow along.

## Round semantics

- One round = mine (round 1 only) → refactor the dispatched slice → review →
  architect verify.
- For a system-level direction (layer separation, module split, interface
  extraction), the round-1 mine must produce a ranked roadmap of N ≥ 2
  points, not a single slice. The leader dispatches one point per round
  across rounds; the architect does not accept until every point is applied,
  descoped with a reason, or sent back to the human as a behavior-change
  request. Bump `max_rounds` above the default when the direction is
  system-level — 8 rounds is the floor, not a ceiling.
- The architect records the verdict:
  `multica issue verdict <issue-id> accept|reject --reason "..."`.
- A reject increments `pipeline_round` on the issue and starts the next round
  with the rejection reason.
- The reject that would exceed `max_rounds` is not dispatched: the server posts
  an escalation comment, notifies the human owner, marks
  `pipeline_status=escalated`, and wakes the leader only to stand down.
- `accept` closes the issue (`done`) through the normal status path and marks
  `pipeline_status=accepted`.
- On acceptance, if the repository has a remote and a refactorer branch has
  commits, the leader pushes it and opens a PR; the final summary comment
  carries the PR link.

Round counting, verdict authorization (architect only), and escalation are
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
