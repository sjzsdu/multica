ALTER TABLE squad_workflow_run
    DROP CONSTRAINT IF EXISTS squad_workflow_run_state_check,
    ADD CONSTRAINT squad_workflow_run_state_check
        CHECK (state IN ('blocked', 'pending', 'awaiting_manual', 'promoted', 'ready_for_finalization', 'failed'));
