-- Human/provider gate decisions are separate from the workflow run snapshot so
-- approvals remain auditable and can be revoked without rewriting history.
CREATE TABLE IF NOT EXISTS squad_workflow_approval (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    squad_id UUID NOT NULL,
    parent_issue_id UUID NOT NULL,
    closed_stage INTEGER NOT NULL CHECK (closed_stage >= 0),
    gate TEXT NOT NULL CHECK (gate IN ('review', 'human_approval')),
    approved BOOLEAN NOT NULL,
    approved_by_type TEXT NOT NULL,
    approved_by_id UUID NOT NULL,
    note TEXT,
    approved_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
