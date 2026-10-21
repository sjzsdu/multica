-- Agent runtime fallback: each agent can carry an ordered list of
-- (runtime_id + model) pairs to try when its primary runtime is blocked.
-- Triggered only on AgentBlocked verdicts (provider quota, auth failure,
-- model not found, CLI not executable, ...). Waitable verdicts (runtime
-- offline) still queue and wait — falling back would waste resources on a
-- second machine when the first will recover by itself.
ALTER TABLE agent ADD COLUMN fallback_runtimes JSONB NOT NULL DEFAULT '[]';

-- System-level singleton config: one row (id = 1) holding global settings
-- that have no natural owner workspace. Used here for the last-resort
-- fallback runtime an operator can pin so every agent in every workspace
-- has somewhere to go when both their primary and agent-level fallbacks
-- are blocked. Keyed by id rather than a text slug so the row is created
-- with its own CREATE TABLE and no ON CONFLICT upsert gymnastics; the
-- check constraint keeps it a true singleton.
CREATE TABLE system_config (
    id INT PRIMARY KEY CHECK (id = 1),
    settings JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO system_config (id) VALUES (1) ON CONFLICT DO NOTHING;
