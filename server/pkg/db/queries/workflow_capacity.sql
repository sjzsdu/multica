-- name: LockWorkspaceForWorkflowCapacity :one
-- Serializes workflow-capacity checks with issue creation and promotion.
SELECT id
FROM workspace
WHERE id = $1
FOR UPDATE;
