-- Durable coordination record for one closed stage barrier. Relationships are
-- intentionally application-owned: workflow records must survive issue/squad
-- cleanup long enough for retries and diagnostics, and the repository forbids
-- database foreign keys for these domain relationships.
CREATE TABLE squad_workflow_run (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    squad_id UUID NOT NULL,
    parent_issue_id UUID NOT NULL,
    closed_stage INTEGER NOT NULL DEFAULT 0 CHECK (closed_stage >= 0),
    next_stage INTEGER NOT NULL DEFAULT 0 CHECK (next_stage >= 0),
    state TEXT NOT NULL DEFAULT 'blocked'
        CHECK (state IN ('blocked', 'pending', 'awaiting_manual', 'promoted', 'ready_for_finalization', 'failed')),
    gate_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(gate_snapshot) = 'object'),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_token UUID,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
