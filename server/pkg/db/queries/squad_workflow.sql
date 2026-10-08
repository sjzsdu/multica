-- name: GetSquadWorkflowPolicy :one
SELECT squad_id, workspace_id, config, created_at, updated_at
FROM squad_workflow_policy
WHERE squad_id = $1 AND workspace_id = $2;

-- name: ListSquadWorkflowPolicies :many
SELECT squad_id, workspace_id, config, created_at, updated_at
FROM squad_workflow_policy
WHERE workspace_id = $1;

-- name: UpsertSquadWorkflowPolicy :one
INSERT INTO squad_workflow_policy (squad_id, workspace_id, config)
VALUES ($1, $2, $3)
ON CONFLICT (squad_id) DO UPDATE SET
    workspace_id = EXCLUDED.workspace_id,
    config = EXCLUDED.config,
    updated_at = now()
RETURNING squad_id, workspace_id, config, created_at, updated_at;

-- name: GetIssueCombinedPullRequestCloseAggregate :one
-- Returns close-intent merge completion verdict for an issue. The issue is
-- considered merged under the close-intent rule when every linked PR is
-- merged AND at least one linked PR has close_intent=true. Rows outside the
-- linked set are ignored. MUL-7726 follow-up for squad workflow gate.
SELECT
    COUNT(*)::bigint AS linked_total,
    COUNT(*) FILTER (WHERE pr.state = 'merged')::bigint AS linked_merged,
    COUNT(*) FILTER (WHERE pr.state = 'merged' AND ipr.close_intent)::bigint AS merged_with_close_intent
FROM issue_pull_request ipr
JOIN github_pull_request pr ON pr.id = ipr.pull_request_id
WHERE ipr.issue_id = sqlc.arg('issue_id');
