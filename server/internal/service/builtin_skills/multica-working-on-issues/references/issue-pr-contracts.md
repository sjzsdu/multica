# Issue PR contracts

Full behavior behind the PR sections of `multica-working-on-issues/SKILL.md`.
Everything here is user-observable behavior of the PR-linking feature.

## The two webhook scans

The GitHub integration runs two separate scans over an incoming pull request.
They are not the same gate and they read different fields. Both run only when
the workspace auto-link setting is enabled.

### Scan 1 — link (title OR body OR branch)

Every `PREFIX-NUMBER` key (case-insensitive, e.g. `MUL-2759`) that resolves to
an issue in the workspace, appearing in the PR's **title, body, or head branch
name**, writes an issue ↔ PR link row. This is what
`multica issue pull-requests` reads back.

**Reference-only links.** The link row carries a reference-only flag. A
linked key qualifies as a full link when it appears in the title or branch, or
in the body after a closing keyword; a key matched ONLY by a bare body mention
is written reference-only. Reference-only rows are excluded from the PR list
(the CLI and the issue's right-side PR panel) **and** from the auto-advance
gate — an open body-only mention must not silently block the issue from
reaching `done` while being invisible in the list. The row still exists for
edit-time close-intent tracking. The flag follows the same preserve-close-intent
terminal gate as close intent.

### Scan 2 — close intent (title OR body only, keyword-adjacent)

Only a key **immediately after** a closing keyword (`Closes` / `Fixes` /
`Resolves`, optional `:` then whitespace) sets the link row's close-intent
flag — the gate that auto-advances the issue to `done` when the PR merges.

- `Closes MUL-1` closes; `Fix login MUL-1` does not — adjacency is what counts.
- Branch names are deliberately excluded from this scan: a branch like
  `mul-1/fix-login` links but must never declare close intent.

Net: a bare title prefix (`MUL-2759: ...`) or a branch ref links only (shown in
the PR list); `Closes MUL-2759` links **and** records close intent; a bare body
mention with no title/branch ref and no closing keyword links as
reference-only and is hidden from the PR list.

## Reading the PR list

```bash
multica issue pull-requests <issue-id> --output json
```

Only `--output` is accepted; the default table shows number, state, title, and
URL. The JSON body is `{"pull_requests": [...]}` and each element exposes:

- `provider` — `github`, `forgejo`, `gitea`, or `gitlab`;
- `number`, `html_url`, `title`;
- `state` — the folded lifecycle enum (below);
- `merged_at`, `closed_at`;
- `mergeable_state` — mirrors GitHub; the UI surfaces `clean` / `dirty`, other
  values round-trip as unknown;
- `snapshot_available` — for GitHub, true only when the snapshot feature is
  enabled and the snapshot head matches the current PR head;
- `mergeable` (conflict-only verdict) and `merge_state_status` (the complete
  merge gate; "ready" requires `clean`);
- `checks_rollup` plus run-level `checks_total` / `checks_passed` /
  `checks_failed` / `checks_running`, and `failed_check_names`;
- `checks_conclusion` — coarse `passed` / `failed` / `pending` or `null`.
  GitHub derives it only from an available current-head snapshot; self-hosted
  providers derive it from webhook commit statuses.

There is **no** standalone draft or merged boolean in the response. The PR
lifecycle is a single folded string computed when the webhook upserts the row:

```
merged   → if the PR is merged
closed   → else if the PR state is closed
draft    → else if the PR is a draft
open     → otherwise
```

So "is it merged?" is `state == "merged"` (or `merged_at != null`); "is it a
draft?" is `state == "draft"`; combine with `checks_conclusion` for CI status.
Only when `snapshot_available` is true does `checks_rollup: null` mean "no
checks"; when false, the snapshot feature is disabled, has not fetched yet, or
matches an old head.

If the command returns no linked PRs after a PR was opened, the link scanner
saw no routable key in title/body/branch, or the only match was a bare body
mention (hidden as reference-only).

## Default handoff for code-changing work

When an issue run changes code in a checked-out GitHub repo, the default
handoff is to open or update a PR before posting the final Multica issue
comment, unless the user explicitly asked for a local-only change or no PR.
This is a default, not an unconditional command: if no code changed, say no PR
is needed; if PR creation is blocked by auth, failing tests, or missing remote
state, report that blocker instead of pretending the run is complete.

Put a routable issue key in the PR title, body, or branch so the webhook can
link the PR back to the issue. If the PR should close the issue on merge, put
the key immediately after a closing keyword in the title or body. In the final
issue comment, include the PR URL when a PR exists; if the task deliberately
produced no PR, say that explicitly.
