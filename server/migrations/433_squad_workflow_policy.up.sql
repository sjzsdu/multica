CREATE TABLE squad_workflow_policy (
    squad_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    config JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(config) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO squad_workflow_policy (squad_id, workspace_id)
SELECT id, workspace_id FROM squad;
