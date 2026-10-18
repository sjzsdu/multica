-- name: UpsertSquadWorkflowRun :one
INSERT INTO squad_workflow_run (
    workspace_id, squad_id, parent_issue_id, closed_stage, next_stage,
    state, gate_snapshot, next_attempt_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (parent_issue_id, closed_stage) DO UPDATE SET
    workspace_id = EXCLUDED.workspace_id,
    squad_id = EXCLUDED.squad_id,
    next_stage = EXCLUDED.next_stage,
    gate_snapshot = EXCLUDED.gate_snapshot,
    state = CASE
        WHEN squad_workflow_run.state IN ('promoted', 'ready_for_finalization')
            THEN squad_workflow_run.state
        ELSE EXCLUDED.state
    END,
    next_attempt_at = EXCLUDED.next_attempt_at,
    lease_token = NULL,
    last_error = NULL,
    updated_at = now()
RETURNING id, workspace_id, squad_id, parent_issue_id, closed_stage,
          next_stage, state, gate_snapshot, attempt_count, next_attempt_at,
          lease_token, last_error, created_at, updated_at;

-- name: MarkSquadWorkflowRunPromoted :one
UPDATE squad_workflow_run
SET state = 'promoted',
    attempt_count = attempt_count + 1,
    next_attempt_at = now(),
    lease_token = NULL,
    last_error = NULL,
    updated_at = now()
WHERE id = $1
RETURNING id, workspace_id, squad_id, parent_issue_id, closed_stage,
          next_stage, state, gate_snapshot, attempt_count, next_attempt_at,
          lease_token, last_error, created_at, updated_at;

-- name: MarkSquadWorkflowRunBlocked :one
UPDATE squad_workflow_run
SET state = $2,
    attempt_count = attempt_count + 1,
    next_attempt_at = $3,
    lease_token = NULL,
    last_error = $4,
    updated_at = now()
WHERE id = $1
RETURNING id, workspace_id, squad_id, parent_issue_id, closed_stage,
          next_stage, state, gate_snapshot, attempt_count, next_attempt_at,
          lease_token, last_error, created_at, updated_at;

-- name: ClaimNextDueSquadWorkflowRun :one
WITH due AS (
    SELECT id
    FROM squad_workflow_run
    WHERE state IN ('blocked', 'pending', 'failed')
      AND next_attempt_at <= now()
    ORDER BY next_attempt_at, created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE squad_workflow_run AS run
SET lease_token = gen_random_uuid(),
    next_attempt_at = $1,
    updated_at = now()
FROM due
WHERE run.id = due.id
RETURNING run.id, run.workspace_id, run.squad_id, run.parent_issue_id,
          run.closed_stage, run.next_stage, run.state, run.gate_snapshot,
          run.attempt_count, run.next_attempt_at, run.lease_token,
          run.last_error, run.created_at, run.updated_at;

-- name: MarkClaimedSquadWorkflowRunPromoted :one
UPDATE squad_workflow_run
SET state = 'promoted',
    attempt_count = attempt_count + 1,
    next_attempt_at = now(),
    lease_token = NULL,
    last_error = NULL,
    updated_at = now()
WHERE id = $1 AND lease_token = $2
RETURNING id, workspace_id, squad_id, parent_issue_id, closed_stage,
          next_stage, state, gate_snapshot, attempt_count, next_attempt_at,
          lease_token, last_error, created_at, updated_at;

-- name: MarkClaimedSquadWorkflowRunBlocked :one
UPDATE squad_workflow_run
SET state = $3,
    attempt_count = attempt_count + 1,
    next_attempt_at = $4,
    lease_token = NULL,
    last_error = $5,
    updated_at = now()
WHERE id = $1 AND lease_token = $2
RETURNING id, workspace_id, squad_id, parent_issue_id, closed_stage,
          next_stage, state, gate_snapshot, attempt_count, next_attempt_at,
          lease_token, last_error, created_at, updated_at;

-- name: GetSquadWorkflowRun :one
SELECT id, workspace_id, squad_id, parent_issue_id, closed_stage, next_stage,
       state, gate_snapshot, attempt_count, next_attempt_at, lease_token,
       last_error, created_at, updated_at
FROM squad_workflow_run
WHERE id = $1 AND workspace_id = $2;

-- name: ListSquadWorkflowRunsByParent :many
SELECT id, workspace_id, squad_id, parent_issue_id, closed_stage, next_stage,
       state, gate_snapshot, attempt_count, next_attempt_at, lease_token,
       last_error, created_at, updated_at
FROM squad_workflow_run
WHERE parent_issue_id = $1 AND workspace_id = $2
ORDER BY closed_stage DESC, created_at DESC;

-- name: WakeSquadWorkflowRun :exec
UPDATE squad_workflow_run
SET next_attempt_at = now(), lease_token = NULL, updated_at = now()
WHERE id = $1 AND state IN ('blocked', 'pending', 'failed');
