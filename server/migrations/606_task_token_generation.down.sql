ALTER TABLE task_token
    DROP COLUMN IF EXISTS revoked_at,
    DROP COLUMN IF EXISTS dispatched_at;
