-- name: CreateTaskToken :one
INSERT INTO task_token (token_hash, task_id, agent_id, workspace_id, user_id, expires_at, dispatched_at, id)
VALUES ($1, $2, $3, $4, $5, $6, $7, COALESCE(sqlc.narg('id')::uuid, gen_random_uuid()))
RETURNING *;

-- name: GetTaskTokenByHash :one
SELECT * FROM task_token
WHERE token_hash = $1 AND expires_at > now() AND revoked_at IS NULL;

-- name: GetTaskTokenByHashForRevocationCheck :one
-- Used only after GetTaskTokenByHash misses, to tell a revoked token from an
-- unknown or expired one. The expiry filter keeps expired tokens in the
-- "invalid token" bucket rather than reporting them as revoked.
SELECT * FROM task_token
WHERE token_hash = $1 AND expires_at > now();

-- name: DeleteTaskTokensByTask :many
-- Revocation is a soft delete: rows stay for diagnostics and are pruned by
-- DeleteExpiredTaskTokens. This task-wide form is reserved for cancellation,
-- where the agent process must never call back again.
UPDATE task_token SET revoked_at = now()
WHERE task_id = $1 AND revoked_at IS NULL
RETURNING *;

-- name: DeleteTaskTokensByTaskGeneration :many
-- Revoke only tokens minted at or before the given dispatch generation.
-- A stale reclaim mints a NEWER token for the same task; a completion from
-- the older delivery must not revoke it.
UPDATE task_token SET revoked_at = now()
WHERE task_id = $1 AND dispatched_at <= $2 AND revoked_at IS NULL
RETURNING *;

-- name: DeleteExpiredTaskTokens :exec
DELETE FROM task_token
WHERE expires_at <= now() OR revoked_at < now() - interval '7 days';
