-- Storage-migration queries: find rows still pointing at the local-disk
-- backend after an operator switched the deployment to S3-compatible object
-- storage, and retarget them once the object has been copied.
--
-- LocalStorage objects live at `/uploads/<key>` (or `<LOCAL_UPLOAD_BASE_URL>/uploads/<key>`),
-- and LocalStorage keys are always `workspaces/…` or `users/…`, so the
-- `/uploads/` marker in a URL never occurs on an object-store URL — it is a
-- safe predicate for "still on local disk".

-- name: CountLocalStorageAttachments :one
SELECT COUNT(*) FROM attachment WHERE url LIKE '%/uploads/%';

-- name: ListLocalStorageAttachments :many
SELECT id, url, content_type, filename FROM attachment
WHERE url LIKE '%/uploads/%' AND id > sqlc.arg(after_id)::uuid
ORDER BY id ASC
LIMIT sqlc.arg(batch_limit)::bigint;

-- name: UpdateAttachmentURLIfUnchanged :execrows
-- The compare-and-set on the old URL keeps a concurrent re-upload or delete
-- from being overwritten by a stale migration write.
UPDATE attachment SET url = sqlc.arg(new_url)
WHERE id = sqlc.arg(id) AND url = sqlc.arg(old_url);

-- name: CountLocalStorageUserAvatars :one
SELECT COUNT(*) FROM "user" WHERE avatar_url LIKE '%/uploads/%';

-- name: ListLocalStorageUserAvatars :many
SELECT id, avatar_url FROM "user"
WHERE avatar_url LIKE '%/uploads/%' AND id > sqlc.arg(after_id)::uuid
ORDER BY id ASC
LIMIT sqlc.arg(batch_limit)::bigint;

-- name: UpdateUserAvatarURLIfUnchanged :execrows
UPDATE "user" SET avatar_url = sqlc.arg(new_url)
WHERE id = sqlc.arg(id) AND avatar_url = sqlc.arg(old_url);

-- name: CountLocalStorageWorkspaceAvatars :one
SELECT COUNT(*) FROM workspace WHERE avatar_url LIKE '%/uploads/%';

-- name: ListLocalStorageWorkspaceAvatars :many
SELECT id, avatar_url FROM workspace
WHERE avatar_url LIKE '%/uploads/%' AND id > sqlc.arg(after_id)::uuid
ORDER BY id ASC
LIMIT sqlc.arg(batch_limit)::bigint;

-- name: UpdateWorkspaceAvatarURLIfUnchanged :execrows
UPDATE workspace SET avatar_url = sqlc.arg(new_url)
WHERE id = sqlc.arg(id) AND avatar_url = sqlc.arg(old_url);

-- name: CountLocalStorageAgentAvatars :one
SELECT COUNT(*) FROM agent WHERE avatar_url LIKE '%/uploads/%';

-- name: ListLocalStorageAgentAvatars :many
SELECT id, avatar_url FROM agent
WHERE avatar_url LIKE '%/uploads/%' AND id > sqlc.arg(after_id)::uuid
ORDER BY id ASC
LIMIT sqlc.arg(batch_limit)::bigint;

-- name: UpdateAgentAvatarURLIfUnchanged :execrows
UPDATE agent SET avatar_url = sqlc.arg(new_url)
WHERE id = sqlc.arg(id) AND avatar_url = sqlc.arg(old_url);

-- name: CountLocalStorageSquadAvatars :one
SELECT COUNT(*) FROM squad WHERE avatar_url LIKE '%/uploads/%';

-- name: ListLocalStorageSquadAvatars :many
SELECT id, avatar_url FROM squad
WHERE avatar_url LIKE '%/uploads/%' AND id > sqlc.arg(after_id)::uuid
ORDER BY id ASC
LIMIT sqlc.arg(batch_limit)::bigint;

-- name: UpdateSquadAvatarURLIfUnchanged :execrows
UPDATE squad SET avatar_url = sqlc.arg(new_url)
WHERE id = sqlc.arg(id) AND avatar_url = sqlc.arg(old_url);
