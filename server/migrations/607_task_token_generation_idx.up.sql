CREATE INDEX CONCURRENTLY IF NOT EXISTS task_token_task_generation_idx ON task_token (task_id, dispatched_at);
