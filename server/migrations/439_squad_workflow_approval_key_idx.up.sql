CREATE UNIQUE INDEX CONCURRENTLY squad_workflow_approval_key_uidx
ON squad_workflow_approval (parent_issue_id, closed_stage, gate);
