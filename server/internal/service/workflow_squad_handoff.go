package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// EnqueueTaskForWorkflowSquadLeader preserves each stage handoff even when an
// older stage or another squad already holds the leader's pending-task slot.
func (s *TaskService) EnqueueTaskForWorkflowSquadLeader(ctx context.Context, issue db.Issue, leaderID, squadID pgtype.UUID, handoffNote string, actorUserID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueMentionTaskWithCommentPlan(ctx, issue, leaderID, pgtype.UUID{}, nil, true, squadID, false, handoffNote, actorUserID, pgtype.UUID{}, OriginDerived, false, true, "")
}

// EnqueueTaskForWorkflowAgent persists one server-driven workflow action. The
// dispatch key, rather than task liveness, defines replay identity.
func (s *TaskService) EnqueueTaskForWorkflowAgent(ctx context.Context, issue db.Issue, agentID, squadID pgtype.UUID, isLeader bool, dispatchKey, handoffNote string, actorUserID pgtype.UUID) (db.AgentTaskQueue, error) {
	if dispatchKey == "" {
		return db.AgentTaskQueue{}, errors.New("workflow dispatch key is required")
	}
	return s.enqueueMentionTaskWithCommentPlan(ctx, issue, agentID, pgtype.UUID{}, nil, isLeader, squadID, false, handoffNote, actorUserID, pgtype.UUID{}, OriginDerived, false, false, dispatchKey)
}

func (s *TaskService) createWorkflowSquadHandoffTask(ctx context.Context, issue db.Issue, params db.CreateAgentTaskParams) (db.AgentTaskQueue, error) {
	if s.TxStarter == nil {
		return db.AgentTaskQueue{}, errors.New("workflow squad handoff requires a transaction")
	}
	// Deferred rows can coexist with an occupied queued/dispatched slot. The
	// existing promoter releases them when that slot frees; never rewrite the
	// binding, role, comments or attribution of a task already offered to a daemon.
	params.WorkflowStep = pgtype.Text{String: issue.Status, Valid: true}
	params.WorkflowHandoffRevision = pgtype.Int8{Int64: issue.Revision, Valid: true}
	var task db.AgentTaskQueue
	err := s.runInTx(ctx, func(q *db.Queries) error {
		lockedAt, err := q.LockWorkflowSquadHandoff(ctx, issue.ID)
		if err != nil {
			return err
		}
		params.FireAt = lockedAt
		_, err = q.GetWorkflowSquadHandoffTask(ctx, db.GetWorkflowSquadHandoffTaskParams{
			IssueID: issue.ID, AgentID: params.AgentID, SquadID: params.SquadID,
			IssueRevision: issue.Revision,
		})
		if err == nil {
			return ErrDuplicatePendingTask
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		task, err = q.CreateAgentTask(ctx, params)
		return err
	})
	return task, err
}

func (s *TaskService) createWorkflowAgentDispatchTask(ctx context.Context, issue db.Issue, params db.CreateAgentTaskParams) (db.AgentTaskQueue, error) {
	if s.TxStarter == nil {
		return db.AgentTaskQueue{}, errors.New("workflow agent dispatch requires a transaction")
	}
	params.WorkflowStep = pgtype.Text{String: issue.Status, Valid: true}
	var task db.AgentTaskQueue
	err := s.runInTx(ctx, func(q *db.Queries) error {
		lockedAt, err := q.LockWorkflowSquadHandoff(ctx, issue.ID)
		if err != nil {
			return err
		}
		params.FireAt = lockedAt
		_, err = q.GetWorkflowAgentDispatchTask(ctx, db.GetWorkflowAgentDispatchTaskParams{
			IssueID: issue.ID, AgentID: params.AgentID, SquadID: params.SquadID,
			DispatchKey: params.WorkflowDispatchKey.String,
		})
		if err == nil {
			return ErrDuplicatePendingTask
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		task, err = q.CreateAgentTask(ctx, params)
		return err
	})
	return task, err
}
