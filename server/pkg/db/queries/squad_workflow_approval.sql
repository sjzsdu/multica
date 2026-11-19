-- name: UpsertSquadWorkflowApproval :one
INSERT INTO squad_workflow_approval (
    workspace_id, squad_id, parent_issue_id, closed_stage, gate, approved,
    approved_by_type, approved_by_id, note
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (parent_issue_id, closed_stage, gate) DO UPDATE SET
    workspace_id = EXCLUDED.workspace_id,
    squad_id = EXCLUDED.squad_id,
    approved = EXCLUDED.approved,
    approved_by_type = EXCLUDED.approved_by_type,
    approved_by_id = EXCLUDED.approved_by_id,
    note = EXCLUDED.note,
    approved_at = now(),
    updated_at = now()
RETURNING id, workspace_id, squad_id, parent_issue_id, closed_stage, gate,
          approved, approved_by_type, approved_by_id, note, approved_at,
          created_at, updated_at;

-- name: ListSquadWorkflowApprovalsByIssue :many
SELECT id, workspace_id, squad_id, parent_issue_id, closed_stage, gate,
       approved, approved_by_type, approved_by_id, note, approved_at,
       created_at, updated_at
FROM squad_workflow_approval
WHERE parent_issue_id = $1
ORDER BY closed_stage DESC, gate ASC;

-- name: ListSquadWorkflowApprovalsByRun :many
SELECT id, workspace_id, squad_id, parent_issue_id, closed_stage, gate,
       approved, approved_by_type, approved_by_id, note, approved_at,
       created_at, updated_at
FROM squad_workflow_approval
WHERE parent_issue_id = $1 AND closed_stage = $2
ORDER BY gate ASC;
