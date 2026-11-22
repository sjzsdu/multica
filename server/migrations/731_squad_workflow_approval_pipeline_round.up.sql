ALTER TABLE squad_workflow_approval
    ADD COLUMN pipeline_round INTEGER NOT NULL DEFAULT 0 CHECK (pipeline_round >= 0);
