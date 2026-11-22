-- name: UpsertSquadWorkflowRun :one
-- A new closure updates the snapshot even while a worker owns the lease. Keep
-- lease scheduling intact; claimed finishes below fence stale snapshots.
INSERT INTO squad_workflow_run (
    workspace_id, squad_id, parent_issue_id, closed_stage, next_stage,
    state, gate_snapshot, next_attempt_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (parent_issue_id, closed_stage) DO UPDATE SET
    workspace_id = EXCLUDED.workspace_id,
    squad_id = EXCLUDED.squad_id,
    next_stage = EXCLUDED.next_stage,
    gate_snapshot = EXCLUDED.gate_snapshot,
    state = CASE WHEN squad_workflow_run.lease_token IS NOT NULL
        THEN squad_workflow_run.state ELSE EXCLUDED.state END,
    next_attempt_at = CASE WHEN squad_workflow_run.lease_token IS NOT NULL
        THEN squad_workflow_run.next_attempt_at ELSE EXCLUDED.next_attempt_at END,
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
WHERE id = $1 AND lease_token = $2 AND gate_snapshot = $3
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
WHERE id = $1 AND lease_token = $2 AND gate_snapshot = $6
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

-- name: ListRejectedPipelineReworkCandidates :many
-- Find a bounded set of rejected opt-in parents whose latest run has no durable
-- rework dispatch. The worker rechecks current state before enqueueing; the
-- dispatch admission lock remains the concurrency authority.
SELECT run.id, run.workspace_id, run.squad_id, run.parent_issue_id,
       run.closed_stage, run.next_stage, run.state, run.gate_snapshot,
       run.attempt_count, run.next_attempt_at, run.lease_token,
       run.last_error, run.created_at, run.updated_at
FROM issue AS parent
JOIN squad_workflow_policy AS policy
  ON policy.workspace_id = parent.workspace_id
 AND policy.squad_id = parent.assignee_id
CROSS JOIN LATERAL (
    SELECT candidate.id, candidate.workspace_id, candidate.squad_id,
           candidate.parent_issue_id, candidate.closed_stage, candidate.next_stage,
           candidate.state, candidate.gate_snapshot, candidate.attempt_count,
           candidate.next_attempt_at, candidate.lease_token, candidate.last_error,
           candidate.created_at, candidate.updated_at
    FROM squad_workflow_run AS candidate
    WHERE candidate.parent_issue_id = parent.id
      AND candidate.workspace_id = parent.workspace_id
    ORDER BY candidate.closed_stage DESC, candidate.created_at DESC, candidate.id DESC
    LIMIT 1
) AS run
WHERE parent.parent_issue_id IS NULL
  AND parent.assignee_type = 'squad'
  AND parent.metadata->>'pipeline_status' = 'rejected'
  AND policy.config->>'require_pipeline_verdict' = 'true'
  AND NOT EXISTS (
      SELECT 1
      FROM agent_task_queue AS task
      WHERE task.issue_id = parent.id
        AND task.squad_id = run.squad_id
        AND task.context->>'workflow_dispatch_key' =
            'rework:' || run.id::text || ':' || COALESCE(parent.metadata->>'pipeline_round', '0')
  )
ORDER BY parent.updated_at, run.id
LIMIT 16;
