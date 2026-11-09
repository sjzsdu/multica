# Runtimes and repos reference

Behavioral detail behind `multica-runtimes-and-repos/SKILL.md`. Everything
here is user-observable behavior of the runtime and checkout features.

## Runtime CLI

The Multica CLI registers `runtime list`, `usage`, `activity`, `update`, and
`delete`.

- `runtime list` reads `/api/runtimes` and prints `id`, `name`, `runtime_mode`,
  `provider`, `status`, and `last_seen_at`;
- `runtime update` posts to `/api/runtimes/{runtime-id}/update`; with `--wait`
  it polls update status. Initiating an update requires runtime-owner or
  workspace-owner/admin access. Status polling additionally permits that
  request's own initiator, so an in-flight poll survives an admin-role change;
- `runtime delete` deletes `/api/runtimes/{runtime-id}`. With `--cascade`, it
  first reads the active-agents conflict payload and posts those ids to the
  unbind-and-delete endpoint; an older archive-agents-and-delete path still
  routes to the same handler for installed clients. Both delete paths run the
  runtime teardown: user agents are unbound (kept, with `runtime_id` cleared),
  their task history is detached so deleting the runtime cannot cascade it
  away, active tasks are cancelled for user-confirmed deletion, and only
  system-kind agents are hard-deleted.

## Repo checkout

`multica repo checkout <url> [--ref]` works only inside a task. It requires
`MULTICA_DAEMON_PORT` plus the injected task-scoped `MULTICA_TOKEN`, which is
sent as a localhost bearer credential along with the workspace, workdir, ref,
agent name, task id, and the daemon-managed checkout mode; the command prints
the checked-out path.

Each provider execution is registered in an in-memory checkout registry keyed
by its task token and removed when execution ends. The daemon's checkout
endpoint authenticates against that registry, requires the requested workspace,
task, and canonical workdir to belong to the active task, and derives the
branch's agent name from the registry instead of trusting the request. It then
resolves the checkout ref: the request's `ref` wins; otherwise it falls back to
the current task's project repository default ref, and forwards the validated
isolated-checkout mode.

### Checkout modes

Linux and Windows Codex tasks get an isolated checkout: a local clone of the
bare cache with task-local Git metadata and the real repository as `origin`.
Windows needs this because inside its native sandbox a linked worktree's
external gitdir stays read-only and `git add` / `git commit` fail from within
the checkout; the layout is chosen per platform, not per sandbox policy, so it
does not depend on a task's resolved policy. On Windows the clone uses
`--no-hardlinks` so the checkout's objects are private copies rather than
filesystem links that share one file and security descriptor with the cache.
Every other runtime keeps the linked-worktree path.

When the bare cache is a partial clone, the isolated checkout must have
`remote.origin.promisor` / `partialclonefilter` restored before its first
checkout: a local clone neither inherits them nor errors on the missing objects
it leaves behind, and the checkout would otherwise succeed with an empty
working tree. The linked-worktree path shares the cache's own config and needs
no such repair.

## The task boundary

The daemon claims tasks, prepares workdirs, launches provider CLIs, and
reports completion. It validates the task-scoped credential, exports
`MULTICA_TASK_CONFIG_ROOT`, and keeps that daemon-owned variable ahead of
custom environment assembly so agents cannot override it.

The per-task config directory is private (mode `0700`) and created/restored
per task; it is not a copy of the daemon owner's Multica profile. The CLI
resolves Multica profiles below `MULTICA_TASK_CONFIG_ROOT` when present while
leaving ordinary home-directory resolution unchanged outside tasks.

CLI commands enforce the task boundary: API calls require task
authentication, task-local config commands fail closed without their root,
auth status hides credential material, and human/local profile or daemon
commands reject strong managed-task identity (agent/task id environment, the
task config root, or the workdir marker). `MULTICA_DAEMON_PORT` alone is a
weak fail-closed signal — older host/container setups may leave it in the
startup environment — so guarded login paths explicitly resolve and update the
human profile instead.

The two read-only daemon diagnostics are scoped rather than rejected: with
strong task identity, status health takes the injected daemon port (never a
profile-derived one, which would report an unrelated daemon), and disk usage
takes the daemon's task-workspaces root (never a home-derived default), rejects
flags that widen the scan past this daemon, and skips the columns that reach
for owner profile state.

Task, project, and repository context is injected into agent workdirs as
runtime config, which is what makes `MULTICA_TASK_ID`-scoped behavior work.
