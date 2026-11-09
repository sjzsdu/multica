package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/squadworkflow"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type SquadWorkflowApprovalResponse struct {
	ID             string `json:"id"`
	ClosedStage    int32  `json:"closed_stage"`
	Gate           string `json:"gate"`
	Approved       bool   `json:"approved"`
	ApprovedByType string `json:"approved_by_type"`
	ApprovedByID   string `json:"approved_by_id"`
	Note           string `json:"note,omitempty"`
	ApprovedAt     string `json:"approved_at"`
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
		ApprovedAt: timestampToString(row.ApprovedAt),
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
	approval, err := h.Queries.UpsertSquadWorkflowApproval(r.Context(), db.UpsertSquadWorkflowApprovalParams{
		WorkspaceID: issue.WorkspaceID, SquadID: run.SquadID, ParentIssueID: issue.ID,
		ClosedStage: run.ClosedStage, Gate: req.Gate, Approved: *req.Approved,
		ApprovedByType: "member", ApprovedByID: memberID,
		Note: pgtype.Text{String: req.Note, Valid: req.Note != ""},
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
