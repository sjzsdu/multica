-- name: GetSystemConfig :one
-- Fetch the singleton system-level config row. Always returns one row
-- (id = 1); the migration guarantees it exists. Used to read the global
-- fallback runtime an operator has pinned as the last-resort runtime when
-- every agent-level fallback is blocked.
SELECT * FROM system_config WHERE id = 1;

-- name: SetSystemFallbackRuntime :one
-- Write settings.fallback_runtime on the singleton row, leaving every other
-- key untouched. The caller validates the runtime first; this query only
-- owns the one key so future settings are never clobbered by a read-modify-
-- write of the whole document.
UPDATE system_config
SET settings = jsonb_set(settings, '{fallback_runtime}', $1::jsonb, true),
    updated_at = now()
WHERE id = 1
RETURNING *;

-- name: ClearSystemFallbackRuntime :one
-- Remove settings.fallback_runtime, turning the global fallback off.
UPDATE system_config
SET settings = settings - 'fallback_runtime',
    updated_at = now()
WHERE id = 1
RETURNING *;
