---
name: multica-working-on-issues
description: "Use when acting on a Multica issue beyond what the brief covers: PR linking vs close intent, reading a linked PR's real state, metadata keys, status-change side effects, sub-issue todo vs backlog."
user-invocable: false
allowed-tools: Bash(multica *), Bash(git *), Bash(gh *)
---

# Working on Multica issues

Product contracts the runtime brief does not fully encode: PR linking vs close
intent, reading linked-PR state, metadata keys, status side effects, and
sub-issue enqueue behavior.

For building mention links, load the platform skill's mentions reference
instead — not this skill.

## PR linking and close intent are two distinct contracts

The GitHub webhook runs two separate scans over an incoming PR. They are not
the same gate and they read different fields.

**Linking** scans the PR **title, body, OR branch** for a routable issue key
(`PREFIX-NUMBER`, e.g. `MUL-2759`). Each match writes an issue ↔ PR link row.
This is the link that `multica issue pull-requests` reads back — but a key that
appears **only** as a bare mention in the body links yet stays hidden from that
list (reference-only, below).

```text
MUL-2759: add built-in issue working skill        # title prefix → links, shown
agent/matt/mul-2759-working-on-issues             # branch ref   → links, shown
```

**Close intent** is stricter and is a separate scan over **title or body only —
never the branch**. It fires only for a key placed immediately after a closing
keyword (`Closes` / `Fixes` / `Resolves`, optional `:` then whitespace). That
adjacency sets the link row's close-intent flag, the gate that auto-advances
the issue to `done` when the PR merges.

```text
Closes MUL-2759                                    # links AND records close intent
Fixes MUL-2759
Resolves MUL-2759
Fix login MUL-2759                                 # links only — keyword not adjacent
```

**Reference-only links (hidden from the PR list).** A key that appears **only**
as a bare mention in the body — no closing keyword, and not in the title or
branch — still writes a link row, but the row is flagged reference-only and is
**excluded from `multica issue pull-requests`** (and the issue's right-side PR
list in the UI) and from the auto-advance gate. This keeps passing mentions
like `Related MUL-2759` from surfacing an unrelated PR as if it were working on
that issue. To make a PR show up, put the key in the title, the branch, or
after a closing keyword in the body.

### Default for code-changing issue work

When an issue run changes code in a checked-out GitHub repo, the default handoff
is to open or update a PR before posting the final Multica issue comment, unless
the user explicitly asked for a local-only change or no PR. If no code changed,
say no PR is needed; if PR creation is blocked by auth, failing tests, or
missing remote state, report that blocker instead of pretending the run is
complete. Include the PR URL in the final issue comment when a PR exists, and
say explicitly when the task deliberately produced none.

## Reading a linked PR's real state

When a step depends on PR state, query Multica's link table — do not infer it
from branch names, GitHub search, memory, or stale metadata:

```bash
multica issue pull-requests <issue-id> --output json
```

Returns `{"pull_requests": [...]}`. Each element exposes `number`, `html_url`,
`title`, a single folded lifecycle `state` enum (`merged` > `closed` > `draft` >
`open` — there is no separate draft or merged boolean), `merged_at`, `provider`
(`github`, `forgejo`, `gitea`, `gitlab`), `mergeable_state`, a GitHub API
snapshot group (`snapshot_available`, `mergeable`, `merge_state_status`,
`checks_rollup`, check counts, `failed_check_names`, `snapshot_fetched_at`,
`snapshot_stale`), and a coarse `checks_conclusion` (`passed` / `failed` /
`pending` / `null`). "Is it merged?" is `state == "merged"`; "still a draft?"
is `state == "draft"`; coarse CI status is `checks_conclusion`. Only when
`snapshot_available` is true does `checks_rollup: null` mean "no checks".
If the command returns nothing after a PR was opened, the link scanner saw no
routable key in title/body/branch, or the only match was a bare body mention
(hidden as reference-only).

## Metadata, properties, status, runs

- **Metadata** is a free-form durable KV bag: `multica issue metadata set
  <issue-id> --key <key> --value <value>` (JSON-sniffed; `--type
  string|number|bool` forces a type) and `... delete --key <stale-key>`. Never
  store secrets in it; runtime bookkeeping and single-run notes belong in the
  result comment.
- **Custom properties** are the typed, validated, user-visible sibling. Read
  the catalog first (`multica property list`, `multica issue property list
  <issue-id>`), set by name (`multica issue property set <issue-id> --name
  Environment --value staging`), and prefer them for workflow state a human
  should see and filter by. Agents cannot create or edit property definitions —
  propose a missing one in a comment.
- **Status changes have server side effects.** `backlog` parks an
  agent-assigned issue (assignee set, no task); moving out of `backlog` enqueues
  then. `in_progress` / `in_review` are agent-managed mutations written
  whenever the work changes the issue's state — including mid-turn — never
  from the trigger type or the run lifecycle; a turn that produces none of the
  issue's own deliverable writes nothing. `done` on a child posts a system
  comment on the parent, and a merged PR with close intent advances the issue
  itself. `cancelled` is terminal and user-driven but does **not** stop tasks
  already in flight — cancel the task to stop it. A failed issue-triggered task
  may roll the issue back to `todo` when nothing active remains. Custom
  statuses inherit their category's behavior in full.
- **Claim ownership without duplicating a run**: pass `--no-start` on every
  command in a records-only flow (`issue assign`, `issue update`, `issue
  status`). A trusted self-assignment is suppressed only when that exact
  `(issue, agent)` pair already has a non-terminal task; same-agent handoffs to
  a fresh issue still start runs.
- **Who else is running**: ask the server on the turns that need it —
  `multica issue runs <issue-id> --active --output json` for in-flight runs on
  this issue, `--siblings` to widen across the sub-issue family (capped, with a
  stderr warning when truncated). Both are advisory reads: coordinate through
  the issue's comments.

## Sub-issues and stages

On an agent-assigned issue, create status decides whether the assignee fires
immediately: a non-backlog status enqueues at create time; `backlog` parks it.
`--stage <N>` (N ≥ 1) groups sub-issues under a parent into ordered stages; the
parent assignee is woken **once, when the lowest unfinished stage closes** (every
sub-issue in it terminal). An unstaged sibling set is one implicit stage, woken
when the last sub-issue finishes. Advancement is agent-driven: the server only
detects the closed barrier and wakes the parent assignee, who decides whether to
promote the next stage's `backlog` sub-issues to `todo`.

```bash
multica issue create --title "Research A" --parent <id> --assignee <agent> --stage 1 --status todo
multica issue create --title "Build"      --parent <id> --assignee <agent> --stage 2 --status backlog
multica issue children <parent-id>             # sub-issues grouped by stage, per-stage done counts
multica issue status <stage-2-child-id> todo   # promote when its deps are met
```

Read each sub-issue's description before promoting and only promote items whose
stated dependencies are met; if a description conflicts with the parent's
breakdown, leave it `backlog` and comment to confirm first. A custom status
counts as done here when its category is `done` or `cancelled` — read
`status_category` rather than matching status names.

## References

`references/issue-pr-contracts.md` — the two webhook scans, reference-only
links, response shape, and merge/draft/CI semantics in full.
`references/issue-workflow-contracts.md` — status enqueue contracts, claim
ownership, sibling-run awareness, stages and barrier wakes, metadata, and
custom properties in full.
