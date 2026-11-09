# Projects and resources reference

Behavioral detail behind `multica-projects-and-resources/SKILL.md`.
Everything here is user-observable behavior of projects and their resources.

## CLI surface

The Multica CLI registers project `list`, `get`, `create`, `update`, `delete`,
and `status`, plus `project resource list/add/update/remove`.

- `project create --repo` attaches a `github_repo` resource during creation;
- `project create` / `project update` accept `--start-date` / `--due-date`
  (calendar days, `YYYY-MM-DD`); on update, an empty value clears the date,
  mirroring the issue date flags;
- `project resource add` supports shortcuts for `github_repo` (`--url`,
  non-JSON `--ref` for the checkout ref, `--default-branch-hint`) and
  `local_directory` (`--local-path`, `--daemon-id`, `--ref-label`,
  `--execution-mode`), or a generic JSON `--ref '<json>'`;
- `project resource update` merges shortcut edits with the existing resource
  ref so a partial edit does not clobber required fields; non-JSON `--ref`
  updates the checkout ref;
- resources are exposed over `/api/projects` and
  `/api/projects/{projectId}/resources`, and written into
  `.multica/project/resources.json` for agent workdirs.

## local_directory execution modes

`local_directory.execution_mode` is validated on save. `in_place` (the
default) runs the agent in the user's directory, one task at a time; `worktree`
gives each task its own git worktree so tasks run concurrently.

Saving `worktree` is gated on the daemon having advertised its local-worktree
capability, checked against the daemon's most recently seen runtime row at
save time — not capable means HTTP 422 with `daemon_version_unsupported`. The
authoritative gate is at claim time: the capability is read off the claim
request itself, and a task claimed by a non-capable daemon is cancelled with an
explicit reason. The minimum CLI version shown to users is informational only;
nothing gates on it.

### Worktree branch and delivery rules (worktree mode)

The branch a worktree task delivers on is chosen per conversation (the issue,
else the chat session), task-scoped only when there is no conversation or a
sibling task already holds the conversation's branch. Continuing one requires
proving ownership: the branch record must carry this workspace/agent/conversation
AND its checkpoint (the record commit's second parent) must still be an
ancestor of the branch tip, so a same-named branch that was deleted and
recreated, or force-moved, is refused. An unowned branch pushes the task onto a
`<branch>-<fingerprint>` branch instead.

The checkpoint is always a commit the branch could not sit at without this
conversation's work: every branch this mode creates gets a baseline commit of
its own (empty when the user's tree was clean), and every later turn that
replays user edits commits its own baseline too. Delivery verification
requires the delivered tip to be its own branch's and to still contain the
commit that turn started from — which is later than any earlier checkpoint
once the user has committed on the branch themselves. A turn whose replay
conflicted has no baseline until the agent resolves, so it only advances the
recorded snapshot when the branch moved past where that turn found it. The tip
is recorded before the worktree is removed; a task whose delivery cannot be
verified fails and keeps its worktree.

The user's directory is captured as one commit including untracked files and
recorded under a per-repo local-state ref once the branch carries it. A
follow-up turn replays only the difference; an unmergeable difference is left
in the worktree for the agent and announced in the prompt, and delivery is
refused while any path is still unmerged. Orphaned state refs are pruned on
the next prepare in that repo.

## Ref flow into checkouts

The `github_repo` resource's checkout ref is lifted onto the claim response,
stored per task, and used as the default checkout ref when a checkout request
does not pass one explicitly.

## Project description as durable context

A project's `description` is injected as durable context for every task in the
project: the claim response carries it, the daemon threads it into the brief's
`## Project Context` section and into `.multica/project/resources.json` as
`project_description`. Use it for project-wide rules that should apply to every
task in the project.

## Referring to a project in a comment

`[Label](mention://project/<uuid>)` is a render-only link. The mention parser
does not include `project` in its type group, so the backend never parses it
and it can enqueue nothing — deliberately weaker than `agent` / `squad`
mentions. Presentation differs by client: web and desktop render a chip in the
rich-content renderer and the editor's mention view, while mobile has no chip —
it keeps default enriched-link styling and only routes the tap. Unfurling a
bare in-app project/issue URL is web/desktop only, via the editor's
workspace-entity link parser, which requires same origin, no query/fragment,
the current workspace slug, and an id that addresses one entity — a UUID or an
issue identifier (`MUL-123`), the form the copy-link action produces, falling
back to the plain link when it resolves to nothing. On mobile a bare URL still
goes to the system browser. The agent-facing line lives in the runtime brief's
Mentions section.
