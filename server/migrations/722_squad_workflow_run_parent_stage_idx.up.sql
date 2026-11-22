CREATE UNIQUE INDEX CONCURRENTLY squad_workflow_run_parent_stage_uidx
ON squad_workflow_run (parent_issue_id, closed_stage);
