# Autopilots reference

Behavioral detail behind `multica-autopilots/SKILL.md`. Everything here is
user-observable behavior of the autopilot feature.

## CLI surface

The Multica CLI registers autopilot `list`, `get`, `create`, `update`,
`delete`, `trigger`, `runs`, `trigger-add`, `trigger-update`,
`trigger-delete`, and `trigger-rotate-url`, mapped to the `/api/autopilots`
routes, `/api/autopilots/{id}/trigger`, `/api/autopilots/{id}/runs`, and the
trigger subroutes. The public webhook ingress lives at
`/api/webhooks/autopilots/{token}` and needs no session.

`autopilot get` never prints webhook secrets in normal JSON output: the token,
path, and URL fields are nulled and replaced by `has_webhook_token` plus
`webhook_token_hint`. `--show-secrets` is an explicit JSON-only escape hatch
that prints a credential-exposure warning to stderr.

`GET /api/autopilots/cron-preview?expr=&tz=` returns `{next_runs}` — the next
3 occurrences as RFC3339 UTC — or 400 with the parser/timezone error. It is
compute-only (no autopilot is touched) and gated by workspace membership;
schedule editors use it instead of approximating the next run client-side.

## Dispatch

An autopilot has an execution mode: `create_issue` creates an issue and enqueues
its assigned agent; `run_only` creates a task directly for the resolved
executing agent without creating an issue. Squad-assigned autopilots resolve
the executable agent from the squad leader. Archived or runtime-unready agents
are blocked before any task is enqueued, so admission is the moment readiness
is checked.

Webhook deliveries are durable: a public delivery row is stored first, an
idempotent run is admitted synchronously (the public endpoint answers
`200 accepted|skipped` plus a `run_id`), and a worker resumes admitted runs
using the delivery binding so crash recovery cannot duplicate a run or task.
Queued deliveries are claimed with expiring database leases and paced per
trigger.

## Authorization

Editing, deleting, triggering, replaying deliveries, and managing triggers or
webhook secrets require the autopilot's creator, a workspace owner/admin, or an
explicit collaborator. Reads (list/get/runs/deliveries) stay open to any
workspace member, but the detail read redacts webhook token, path, and URL for
callers who lack write access, since the token alone can trigger the autopilot.
Creating a new autopilot is open to any member, who becomes its creator.

This autopilot-level View/Write layer is independent of, and ANDed with, the
agent-assignment access gate enforced again at dispatch time.

Explicit write grants ("collaborators") are stored per autopilot, deleted with
it, and managed through `POST /api/autopilots/{id}/collaborators` (body
`{user_id}`) and `DELETE /api/autopilots/{id}/collaborators/{userId}`. Both
are gated narrowly — creator or workspace owner/admin only — so a granted
collaborator keeps write/execute but cannot re-grant or revoke peers, which
prevents privilege escalation. The detail read embeds the collaborator list
and stamps two per-caller booleans: `can_write` (gates edit/run/trigger
controls) and the narrower `can_manage_access` (gates the "Manage access"
entry in the web/desktop UI).
