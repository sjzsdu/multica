package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/squadworkflow"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type SquadWorkflowApprovalResponse struct {
	ID             string `json:"id"`
	ClosedStage    int32  `json:"closed_stage"`
	Gate           string `json:"gate"`
	Approved       bool   `json:"approved"`
	ApprovedByType string `json:"approved_by_type"`
	ApprovedByID   string `json:"approved_by_id"`
	Note           string `json:"note,omitempty"`
	PipelineRound  int32  `json:"pipeline_round"`
	ApprovedAt     string `json:"approved_at"`
}

type SquadWorkflowReviewResponse struct {
	SquadWorkflowApprovalResponse
	Round     int            `json:"round"`
	MaxRounds int            `json:"max_rounds"`
	Escalated bool           `json:"escalated"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type issueWorkflowReviewWrite struct {
	approval      db.SquadWorkflowApproval
	reject        pipelineRejectWrite
	activity      db.ActivityLog
	activityData  []byte
	reviewedRound int
}

type SquadWorkflowRunResponse struct {
	ID            string                          `json:"id"`
	SquadID       string                          `json:"squad_id"`
	ParentIssueID string                          `json:"parent_issue_id"`
	ClosedStage   int32                           `json:"closed_stage"`
	NextStage     int32                           `json:"next_stage"`
	State         string                          `json:"state"`
	GateSnapshot  json.RawMessage                 `json:"gate_snapshot"`
	AttemptCount  int32                           `json:"attempt_count"`
	NextAttemptAt string                          `json:"next_attempt_at"`
	LastError     string                          `json:"last_error,omitempty"`
	CreatedAt     string                          `json:"created_at"`
	UpdatedAt     string                          `json:"updated_at"`
	Approvals     []SquadWorkflowApprovalResponse `json:"approvals"`
}

func squadWorkflowApprovalResponse(row db.SquadWorkflowApproval) SquadWorkflowApprovalResponse {
	return SquadWorkflowApprovalResponse{
		ID: uuidToString(row.ID), ClosedStage: row.ClosedStage, Gate: row.Gate,
		Approved: row.Approved, ApprovedByType: row.ApprovedByType,
		ApprovedByID: uuidToString(row.ApprovedByID), Note: row.Note.String,
		PipelineRound: row.PipelineRound, ApprovedAt: timestampToString(row.ApprovedAt),
	}
}

func squadWorkflowRunResponse(row db.SquadWorkflowRun, approvals []db.SquadWorkflowApproval) SquadWorkflowRunResponse {
	snapshot := json.RawMessage(row.GateSnapshot)
	if len(snapshot) == 0 {
		snapshot = json.RawMessage(`{}`)
	}
	result := SquadWorkflowRunResponse{
		ID: uuidToString(row.ID), SquadID: uuidToString(row.SquadID),
		ParentIssueID: uuidToString(row.ParentIssueID), ClosedStage: row.ClosedStage,
		NextStage: row.NextStage, State: row.State, GateSnapshot: snapshot,
		AttemptCount: row.AttemptCount, NextAttemptAt: timestampToString(row.NextAttemptAt),
		LastError: row.LastError.String, CreatedAt: timestampToString(row.CreatedAt),
		UpdatedAt: timestampToString(row.UpdatedAt), Approvals: []SquadWorkflowApprovalResponse{},
	}
	for _, approval := range approvals {
		result.Approvals = append(result.Approvals, squadWorkflowApprovalResponse(approval))
	}
	return result
}

func (h *Handler) ListIssueWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	runs, err := h.Queries.ListSquadWorkflowRunsByParent(r.Context(), db.ListSquadWorkflowRunsByParentParams{ParentIssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workflow runs")
		return
	}
	approvals, err := h.Queries.ListSquadWorkflowApprovalsByIssue(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workflow approvals")
		return
	}
	byStage := make(map[int32][]db.SquadWorkflowApproval)
	for _, approval := range approvals {
		byStage[approval.ClosedStage] = append(byStage[approval.ClosedStage], approval)
	}
	response := make([]SquadWorkflowRunResponse, 0, len(runs))
	for _, run := range runs {
		response = append(response, squadWorkflowRunResponse(run, byStage[run.ClosedStage]))
	}
	writeJSON(w, http.StatusOK, response)
}

// RecordIssueWorkflowReview stores evidence only from the configured reviewer.
// Opted-in reviews also settle rejection through the product verdict's shared
// capped-rework transaction, so neither actor can advance the same round twice.
func (h *Handler) RecordIssueWorkflowReview(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	runID, err := util.ParseUUID(chi.URLParam(r, "runId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workflow run id")
		return
	}
	run, err := h.Queries.GetSquadWorkflowRun(r.Context(), db.GetSquadWorkflowRunParams{ID: runID, WorkspaceID: issue.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && run.ParentIssueID != issue.ID {
		writeError(w, http.StatusNotFound, "workflow run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load workflow run")
		return
	}
	var req struct {
		Approved *bool  `json:"approved"`
		Note     string `json:"note"`
		Round    *int   `json:"round"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Approved == nil {
		writeError(w, http.StatusBadRequest, "approved is required")
		return
	}
	note := strings.TrimSpace(sanitizeNullBytes(req.Note))
	if !*req.Approved && note == "" {
		writeError(w, http.StatusBadRequest, "note is required when rejecting a review")
		return
	}
	if strings.Contains(note, "mention://") {
		writeError(w, http.StatusBadRequest, "note cannot contain mention links")
		return
	}
	if runes := []rune(note); len(runes) > maxPipelineVerdictReasonRunes {
		note = string(runes[:maxPipelineVerdictReasonRunes])
	}
	auth, ok := h.authorizeSquadAgentTask(w, r, issue, "record a review")
	if !ok {
		return
	}
	if auth.squad.ID != run.SquadID {
		writeError(w, http.StatusNotFound, "workflow run not found")
		return
	}
	if auth.policy.ReviewAgentID == "" {
		writeError(w, http.StatusConflict, "this squad has no independent reviewer configured")
		return
	}
	if auth.actorID != auth.policy.ReviewAgentID {
		writeError(w, http.StatusForbidden, "only the configured independent reviewer can record a review")
		return
	}

	result, err := wakeupWrite(h, r, func(q *db.Queries) (issueWorkflowReviewWrite, error) {
		var result issueWorkflowReviewWrite
		current, err := q.LockIssueForDescriptionUpdate(r.Context(), db.LockIssueForDescriptionUpdateParams{
			ID: issue.ID, WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return result, err
		}
		currentMetadata := parseIssueMetadata(current.Metadata)
		if auth.policy.RequirePipelineVerdict {
			runs, err := q.ListSquadWorkflowRunsByParent(r.Context(), db.ListSquadWorkflowRunsByParentParams{
				ParentIssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
			})
			if err != nil {
				return result, err
			}
			status := issuestatus.Effective(r.Context(), q, issue.WorkspaceID, current.Status)
			if len(runs) == 0 || runs[0].ID != run.ID || runs[0].NextStage != 0 ||
				status == issuestatus.Done || status == issuestatus.Cancelled ||
				pipelineMetadataStatus(currentMetadata) == pipelineStatusEscalated ||
				pipelineMetadataStatus(currentMetadata) == pipelineStatusAccepted ||
				!pipelineRoundMatches(auth.policy, currentMetadata, req.Round) {
				return result, errPipelineVerdictConflict
			}
		}

		upsertApproval := func(reviewedRound int) error {
			result.reviewedRound = reviewedRound
			result.approval, err = q.UpsertSquadWorkflowApproval(r.Context(), db.UpsertSquadWorkflowApprovalParams{
				WorkspaceID: issue.WorkspaceID, SquadID: run.SquadID, ParentIssueID: issue.ID,
				ClosedStage: run.ClosedStage, Gate: string(squadworkflow.GateReview), Approved: *req.Approved,
				ApprovedByType: "agent", ApprovedByID: parseUUID(auth.actorID),
				Note: pgtype.Text{String: note, Valid: note != ""}, PipelineRound: int32(reviewedRound),
			})
			return err
		}
		if !*req.Approved && auth.policy.RequirePipelineVerdict {
			result.reject, err = h.writePipelineReject(
				r.Context(), q, current, auth.task, auth.squad, auth.policy,
				auth.actorType, auth.actorID, note, req.Round, upsertApproval,
			)
			if err != nil {
				return issueWorkflowReviewWrite{}, err
			}
		} else {
			if err := upsertApproval(pipelineMetadataRound(currentMetadata)); err != nil {
				return issueWorkflowReviewWrite{}, err
			}
			if err := q.WakeSquadWorkflowRun(r.Context(), run.ID); err != nil {
				return issueWorkflowReviewWrite{}, err
			}
		}

		round := result.reviewedRound
		maxRounds := 0
		if auth.policy.Pipeline != nil {
			maxRounds = auth.policy.Pipeline.MaxRounds
		}
		if !*req.Approved && auth.policy.RequirePipelineVerdict {
			round = result.reject.Round
		}
		result.activityData, err = json.Marshal(map[string]any{
			"squad_id":       uuidToString(auth.squad.ID),
			"task_id":        uuidToString(auth.task.ID),
			"run_id":         uuidToString(run.ID),
			"approved":       *req.Approved,
			"note":           note,
			"pipeline_round": result.reviewedRound,
			"round":          round,
			"max_rounds":     maxRounds,
			"escalated":      result.reject.Escalated,
		})
		if err != nil {
			return issueWorkflowReviewWrite{}, err
		}
		result.activity, err = q.CreateActivity(r.Context(), db.CreateActivityParams{
			ID:          dbid.NewV7(),
			WorkspaceID: issue.WorkspaceID,
			IssueID:     issue.ID,
			ActorType:   pgtype.Text{String: "agent", Valid: true},
			ActorID:     auth.task.AgentID,
			Action:      "pipeline_review",
			Details:     result.activityData,
		})
		if err != nil {
			return issueWorkflowReviewWrite{}, err
		}
		return result, nil
	})
	if errors.Is(err, errPipelineVerdictConflict) {
		writeError(w, http.StatusConflict, "workflow run or pipeline round is stale, or the pipeline is already settled")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "issue not found")
		return
	}
	if err != nil {
		slog.Error("workflow review failed", "issue_id", uuidToString(issue.ID), "run_id", uuidToString(run.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save workflow review")
		return
	}

	if !*req.Approved && auth.policy.RequirePipelineVerdict {
		h.publishPipelineMetadataChanged(issue, auth.actorType, auth.actorID, result.reject.Metadata, result.reject.Revision)
		if result.reject.Escalated {
			h.announcePipelineEscalation(r, issue, auth.actorType, auth.actorID, result.reject.Comment, result.reject.CommentIssueRevision)
		}
	}
	h.publish(protocol.EventActivityCreated, uuidToString(issue.WorkspaceID), "agent", auth.actorID, map[string]any{
		"issue_id": uuidToString(issue.ID),
		"entry": map[string]any{
			"type":       "activity",
			"id":         uuidToString(result.activity.ID),
			"actor_type": "agent",
			"actor_id":   auth.actorID,
			"action":     result.activity.Action,
			"details":    json.RawMessage(result.activityData),
			"created_at": timestampToString(result.activity.CreatedAt),
		},
	})
	if !*req.Approved && auth.policy.RequirePipelineVerdict && !result.reject.Escalated {
		if err := h.dispatchPipelineRework(r.Context(), issue, auth.policy, result.reject.Round, note); err != nil {
			slog.Error("pipeline rework dispatch failed after review commit",
				"issue_id", uuidToString(issue.ID), "round", result.reject.Round, "error", err)
			writeError(w, http.StatusServiceUnavailable, "review committed, but automatic rework dispatch failed; the workflow worker will retry")
			return
		}
	}

	maxRounds := 0
	if auth.policy.Pipeline != nil {
		maxRounds = auth.policy.Pipeline.MaxRounds
	}
	response := SquadWorkflowReviewResponse{
		SquadWorkflowApprovalResponse: squadWorkflowApprovalResponse(result.approval),
		Round:                         result.reviewedRound, MaxRounds: maxRounds, Escalated: result.reject.Escalated,
	}
	if !*req.Approved && auth.policy.RequirePipelineVerdict {
		response.Round = result.reject.Round
		response.Metadata = result.reject.Metadata
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (h *Handler) UpsertIssueWorkflowApproval(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	memberID, err := util.ParseUUID(userID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid user identity")
		return
	}
	runID, err := util.ParseUUID(chi.URLParam(r, "runId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workflow run id")
		return
	}
	run, err := h.Queries.GetSquadWorkflowRun(r.Context(), db.GetSquadWorkflowRunParams{ID: runID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "workflow run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load workflow run")
		return
	}
	if run.ParentIssueID != issue.ID {
		writeError(w, http.StatusNotFound, "workflow run not found")
		return
	}
	var req struct {
		Gate     string `json:"gate"`
		Approved *bool  `json:"approved"`
		Note     string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Approved == nil {
		writeError(w, http.StatusBadRequest, "gate and approved are required")
		return
	}
	if req.Gate != string(squadworkflow.GateReview) && req.Gate != string(squadworkflow.GateHumanApproval) {
		writeError(w, http.StatusBadRequest, "gate must be review or human_approval")
		return
	}
	squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{ID: run.SquadID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusNotFound, "squad not found")
		return
	}
	policy := h.loadSquadWorkflowPolicy(r.Context(), squad)
	if !allowsHumanWorkflowApproval(policy, req.Gate) {
		writeError(w, http.StatusConflict, "review approval is owned by the configured independent reviewer")
		return
	}
	approval, err := h.Queries.UpsertSquadWorkflowApproval(r.Context(), db.UpsertSquadWorkflowApprovalParams{
		WorkspaceID: issue.WorkspaceID, SquadID: run.SquadID, ParentIssueID: issue.ID,
		ClosedStage: run.ClosedStage, Gate: req.Gate, Approved: *req.Approved,
		ApprovedByType: "member", ApprovedByID: memberID,
		Note:          pgtype.Text{String: req.Note, Valid: req.Note != ""},
		PipelineRound: int32(pipelineMetadataRound(parseIssueMetadata(issue.Metadata))),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save workflow approval")
		return
	}
	_ = h.Queries.WakeSquadWorkflowRun(r.Context(), run.ID)
	// The approval is durable and the leased worker will pick up the woken run;
	// this request must not claim an unrelated run from the global queue.
	writeJSON(w, http.StatusAccepted, squadWorkflowApprovalResponse(approval))
}
