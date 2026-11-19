# Issue workflow contracts

Full behavior behind the metadata, properties, status, ownership, and stage
sections of `multica-working-on-issues/SKILL.md`. Everything here is
user-observable behavior.

## Status side effects (enqueue contracts)

A status change is not cosmetic — the server enqueues or skips agent work
based on it. A workspace may define custom statuses beyond the built-ins; each
inherits its category's behavior in full, and each built-in key below is also
the name of its category, so read these bullets as category rules. Two writes
are literal-key exceptions, not category rules: the failed-task rollback
writes the literal `todo` key, and a merged PR with close intent writes the
literal `done` key. When custom statuses exist, the runtime brief lists this
workspace's catalog (it rides the claim payload).

- **Create time**: an agent-assigned issue with a non-backlog status enqueues
  immediately; `--status backlog` parks it with the assignee set but no
  trigger. The same contract holds for batch updates.
- **`backlog`** parks an agent-assigned issue. Moving `backlog → todo` (or any
  non-done/non-cancelled status) enqueues the assigned agent then.
- **`in_progress` / `in_review`** are agent-managed CLI mutations, not task
  lifecycle side effects: starting or completing a task never writes issue
  status behind the scenes. The runtime brief asks agents to write the state
  the issue is in whenever their work changes it — not from the trigger type,
  not gated on being the assignee, and including mid-turn writes: a turn that
  advances the issue's own ask sets `in_progress` as soon as that is known; a
  blocker is recorded when hit; the turn must not exit stale (delivered the
  issue's own ask → `in_review`; work continues beyond the turn →
  `in_progress`; stuck → `blocked`). A turn that produces none of the issue's
  own deliverable writes nothing at any point. The activity kind never decides:
  research, design, planning, and review all count as work exactly when they
  are what the issue asks for. Questions, discussion, and acknowledgements
  never move the status. Squad leaders: dispatching members is not delivery —
  a dispatch turn leaves the parent `in_progress`.
- **`done`** on a child issue posts a system comment on its parent. A PR that
  carries close intent advances the issue to `done` itself on merge — no
  manual flip needed.
- **`cancelled`** is a terminal, user-driven decision. Like `done` it enqueues
  no new agent work, but it does **not** stop tasks already in flight — a run
  in progress keeps going. To stop a running task, cancel the task itself.
  Only deleting the issue cancels its in-flight tasks.
- **Failed issue-triggered tasks** may roll an issue from `in_progress` back to
  `todo` when no active task or retry remains — the main server-owned status
  write on the agent-run path.

## Claim ownership without duplicating a run

Assigning an active issue to an agent normally starts a run. When the work is
already underway and the write only records ownership or progress, pass
`--no-start` on every command in that flow — suppressing the assignment alone
does not suppress a later status update:

```bash
multica issue assign <issue-id> --to-id <agent-id> --no-start
multica issue update <issue-id> --assignee-id <agent-id> --no-start
multica issue status <issue-id> in_progress --no-start
```

Before self-assigning, check the target issue's comment history for an existing
claim. The server also suppresses a trusted self-assignment when the exact
`(issue, agent)` pair already has a non-terminal task, but deliberately keeps
same-agent handoffs to a fresh issue starting runs: cross-issue serial chains
and triage batches rely on that.

Claim responses also expose a bounded, workspace-scoped snapshot of the same
agent's other dispatched/running/waiting issue tasks (queued tasks are
excluded), and daemon prompts point at the target's comment history and the
concrete sibling run-messages commands.

## Who else is running right now

Nothing about concurrent runs is pushed into your prompt: the answer changes
while a turn is running, and most turns never need it. Ask the server on the
turns that do — before opening a PR against code a sibling issue also touches:

```bash
multica issue runs <issue-id> --active --output json     # in-flight runs on this issue
multica issue runs <issue-id> --siblings --output json   # ...and across the sub-issue family
```

`--active` drops execution history and returns only queued / dispatched /
running / waiting_local_directory runs. `--siblings` widens the same read to
the issue's family — its parent (or itself, when parentless) plus every child
of that parent — labeling each row with the issue it belongs to, which is how
you find another agent already working on a sibling sub-issue before opening a
second PR against the same code.

The family read returns a compact row — task, issue, agent, status, started —
not the full execution-log record; follow a task id with
`multica issue run-messages` for detail. Rows come back running-first, newest
first within a status, and the family read is capped at 20; when the cap
truncates, the CLI prints a warning on stderr — read it. Without that warning
a short list means "nobody else is there"; with it, the list proves nothing
about the runs it did not return.

Both are advisory reads: nothing reserves an issue or serializes anything. A
run you see may finish a second later, and one you don't see may start a second
later. Coordinate through the issue's comments — the reads tell you whom to
coordinate with.

## Sub-issues and stages

On an agent-assigned issue, create status decides whether the assignee fires
immediately: a non-backlog status (e.g. `todo`) enqueues the agent at create
time; `backlog` sets the assignee without triggering. Creating every step of a
serial chain as `todo` enqueues the whole chain at once — park later steps as
`backlog` and promote one at a time with `multica issue status <child-id> todo`.

`--stage <N>` (N ≥ 1) groups sub-issues under the same parent into ordered
stages. The parent assignee is woken **once, when a whole stage finishes** —
i.e. every sub-issue in the lowest unfinished stage has reached a status in the
terminal categories (`done` / `cancelled`; a custom status counts via its
category, exposed as `status_category` on each child — read that rather than
matching status names). A completion that does not close a stage is silent: no
comment, no wake. A sibling set with **no** stages is one implicit stage, so
the parent is woken once when the last sub-issue finishes — not on every child.

Advancement is agent-driven: the server only detects the closed barrier and
wakes the parent assignee, who then decides whether to promote the next
stage's `backlog` sub-issues to `todo`. A workspace whose squad workflow policy
enables auto-promotion gets the next stage promoted automatically after the
workflow gates pass, subject to the configured cap on active children;
otherwise promotion stays the woken agent's decision. Closed barriers are
persisted as workflow runs retried under a database lease, and the issue's
workflow-run history (including recorded human approvals) is available over
the API. The barrier never auto-marks the parent `done`.

```bash
# Stage 1 runs now; later stages parked until promoted
multica issue create --title "Research A" --parent <id> --assignee <agent> --stage 1 --status todo
multica issue create --title "Research B" --parent <id> --assignee <agent> --stage 1 --status todo
multica issue create --title "Build"      --parent <id> --assignee <agent> --stage 2 --status backlog
multica issue create --title "Ship"       --parent <id> --assignee <agent> --stage 3 --status backlog
```

When a stage closes, the wake comment carries a per-stage summary and the next
stage. Inspect the layout, then promote:

```bash
multica issue children <parent-id>             # sub-issues grouped by stage, per-stage done counts
multica issue status <stage-2-child-id> todo   # promote when its deps are met
```

Read each sub-issue's description before promoting and only promote items whose
stated dependencies are met; if a description conflicts with the parent's
breakdown, leave it `backlog` and comment to confirm first.

## Metadata: durable custom state

Metadata is a free-form KV bag of durable issue state. Reading metadata is
safe. Writing a key is a state mutation tied to an explicit task requirement
to record that state for later readers or runs. Keys are whatever your
workflow needs — the platform curates no vocabulary; pick short snake_case
names and reuse them consistently within your workspace.

Never store secrets, tokens, or API keys in metadata.
Not metadata: logs or summaries; runtime bookkeeping such as timestamps,
attempt counts, or agent IDs; or other single-run details such as files
touched and investigation notes — those belong in the result comment.

```bash
multica issue metadata set <issue-id> --key <key> --value <value>
multica issue metadata delete <issue-id> --key <stale-key>
```

`--value` is JSON-parsed by default (bool/number are sniffed); pass
`--type string|number|bool` to force a type.

## Custom properties: typed workflow state

Workspaces may define custom issue properties (Severity, Environment, QA
Status, Reviewer, ...). Properties are the typed, user-visible sibling of
metadata: values are validated against the definition (select options, date
format, http(s) URL, member reference), visible in the issue sidebar, and
addressed by name.

- Read what exists before writing: `multica property list` shows the catalog;
  `multica issue property list <issue-id>` shows values set on the issue.
- Set values by property name and option name — the CLI translates to ids:

```bash
multica issue property set <issue-id> --name Environment --value staging
multica issue property set <issue-id> --name Platforms --value "iOS,Android"
multica issue property set <issue-id> --name Reviewer --value Bohan
multica issue property unset <issue-id> --name Environment
```

- A validation error lists the legal options — fix the value and retry.
- `actor` / `multi_actor` properties (Reviewer, Escalation contact, ...) hold
  workspace members only. `--value` takes a member name, email, UUID, short id,
  or an explicit `member:<uuid>`; `multi_actor` takes a comma-separated list
  (duplicates dropped, order kept, max 20). Actor references are checked for
  workspace membership only.
- Definitions may include an optional catalog icon for visual identification;
  it does not change the property's type or value validation.
- Agents cannot create or edit property definitions (owner/admin humans only).
  If a needed property does not exist, propose it in a comment instead.
- Property vs metadata: if the value is workflow state a human should see and
  filter by, and a definition exists, prefer the property. Metadata stays the
  free-form bag for durable custom issue state.
- `issue list` filters and sorts by property with the same name addressing:

```bash
multica issue list --property "Impact=High" --property "Impact=Medium" --output json
multica issue list --property "QA Status=__none__" --status in_review --output json
multica issue list --sort property:Impact --direction desc --output json
```

- `--property` takes one `Name=Value` per flag. Repeating the same property
  matches ANY of its values; different properties must ALL match. Values are
  option names or ids (select types), `true`/`false` (checkbox), a member
  name/email/id (actor types), or the value itself for text, url, number, and
  date (`YYYY-MM-DD`). The reserved value `__none__` matches issues where the
  property is unset (works for every type; not index-backed, so use it for
  targeted audits rather than as a default listing filter). Only `=` is
  supported today; the `>=`, `<=` and `!=` spellings are reserved for
  comparison filters and are rejected.
- `--sort property:<name-or-id>` orders select properties by option order — an
  ordinal scale (Low < Medium < High) sorts by meaning — and number/date/text
  url by value; issues without the property sort last either way. Archived
  properties and types without an order (multi_select, checkbox, actor kinds)
  are rejected up front.
