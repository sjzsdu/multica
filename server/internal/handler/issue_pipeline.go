package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/squadworkflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Pipeline verdicts are the server-side acceptance gate of a pipeline squad.
// The product-role agent experiences the build and then posts accept or reject
// here; counting rounds and escalating are done in this handler so no prompt
// can talk its way past the cap and no failed agent can silently stop the loop.
//
// Round state lives in the issue's metadata KV map, next to the pipeline state
// agents already read (see issue_metadata.go for the storage rules):
//
//	pipeline_round    number   rework rounds started so far
//	pipeline_status   string   rejected | escalated | accepted
const (
	pipelineRoundMetadataKey  = "pipeline_round"
	pipelineStatusMetadataKey = "pipeline_status"
)

const (
	pipelineStatusRejected  = "rejected"
	pipelineStatusEscalated = "escalated"
	pipelineStatusAccepted  = "accepted"
)

// maxPipelineVerdictReasonRunes caps the reason echoed into server-authored
// content: the escalation comment and the activity row.
const maxPipelineVerdictReasonRunes = 2000

// errPipelineVerdictConflict marks a round whose status changed between the
// request's pre-checks and its write (a concurrent verdict, or an already
// escalated pipeline). Rendered as a 409.
var errPipelineVerdictConflict = errors.New("pipeline verdict already settled")

// pipelineMetadataRound reads the rework round counter; unset means 0.
func pipelineMetadataRound(meta map[string]any) int {
	value, ok := meta[pipelineRoundMetadataKey].(float64)
	if !ok || value < 0 {
		return 0
	}
	return int(value)
}

// pipelineMetadataStatus reads the pipeline status; unset means the pipeline
// has not recorded a verdict yet.
func pipelineMetadataStatus(meta map[string]any) string {
	status, _ := meta[pipelineStatusMetadataKey].(string)
	return status
}

// RecordPipelineVerdict is POST /api/issues/{id}/pipeline/verdict.
//
// Authority comes from the TASK row rather than the issue's assignee: the
// caller must be the agent this run was enqueued for, running on this very
// issue, inside a squad whose workflow policy opts into a pipeline. Check ORDER
// mirrors RecordSquadLeaderEvaluation — everything above the ownership gate may
// not quote task-derived ids, because GetAgentTaskInWorkspace is only
// workspace-scoped while the task itself is a global row.
//
// accept delegates the status change to UpdateIssue so the pipeline closing an
// issue obeys exactly the guards a human flipping it to done obeys; reject
// counts the round here and escalates to a human once a further round would
// exceed MaxRounds.
func (h *Handler) RecordPipelineVerdict(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	var req struct {
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	verdict := strings.ToLower(strings.TrimSpace(req.Verdict))
	if verdict != "accept" && verdict != "reject" {
		writeError(w, http.StatusBadRequest, "verdict must be 'accept' or 'reject'")
		return
	}
	reason := strings.TrimSpace(sanitizeNullBytes(req.Reason))
	if verdict == "reject" && reason == "" {
		writeError(w, http.StatusBadRequest, "reason is required when rejecting")
		return
	}
	// On escalation the reason is echoed into a comment THIS handler authors,
	// and that comment's mentions are what notify the human and stand the
	// leader down. A caller-supplied mention link would dispatch work nobody
	// named, so the reason may not carry one.
	if strings.Contains(reason, "mention://") {
		writeError(w, http.StatusBadRequest, "reason cannot contain mention links")
		return
	}
	if runes := []rune(reason); len(runes) > maxPipelineVerdictReasonRunes {
		reason = string(runes[:maxPipelineVerdictReasonRunes])
	}

	// Ownership: the caller must be the agent this task was enqueued for.
	// A task id that does not resolve inside this workspace is refused
	// generically, before any task-derived id is echoed back.
	taskUUID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task id")
	if !ok {
		return
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{
		ID:          taskUUID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || !task.IssueID.Valid {
		writeError(w, http.StatusBadRequest, "task does not belong to issue")
		return
	}
	userID := requestUserID(r)
	workspaceID := uuidToString(issue.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if actorType != "agent" || !task.AgentID.Valid || actorID != uuidToString(task.AgentID) {
		writeError(w, http.StatusForbidden, "only the agent running this task can record a verdict")
		return
	}
	// Past the ownership gate, naming the task's own issue is safe — and useful:
	// an agent woken on one issue that verdicts on another gets told where.
	if uuidToString(task.IssueID) != uuidToString(issue.ID) {
		writeError(w, http.StatusBadRequest,
			"task does not belong to issue; record the verdict on issue "+uuidToString(task.IssueID)+" (the issue this task is running on)")
		return
	}

	// The pipeline runs on ONE squad-assigned issue; verdicts anywhere else are
	// ordinary issue edits done through the normal endpoints.
	if issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		writeError(w, http.StatusConflict, "issue is not assigned to a squad")
		return
	}
	squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "squad not found")
		return
	}
	isMember, err := h.Queries.IsSquadMember(r.Context(), db.IsSquadMemberParams{
		SquadID:    squad.ID,
		MemberType: "agent",
		MemberID:   parseUUID(actorID),
	})
	if err != nil {
		slog.Error("pipeline verdict: squad membership lookup failed",
			"squad_id", uuidToString(squad.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load squad membership")
		return
	}
	if !isMember && actorID != uuidToString(squad.LeaderID) {
		writeError(w, http.StatusForbidden, "only members of this squad can record a verdict")
		return
	}
	policy := h.loadSquadWorkflowPolicy(r.Context(), squad)
	if policy.Pipeline == nil {
		writeError(w, http.StatusConflict, "this squad has no pipeline configured")
		return
	}
	if product := policy.Pipeline.ProductAgentID; product != "" && product != actorID {
		writeError(w, http.StatusForbidden, "only this pipeline's product agent can record a verdict")
		return
	}

	// A pipeline verdict describes work in flight. A terminal issue has no
	// round to count and no status to move, so both verdicts stop here.
	if status := issuestatus.Effective(r.Context(), h.Queries, issue.WorkspaceID, issue.Status); status == issuestatus.Done || status == issuestatus.Cancelled {
		writeError(w, http.StatusConflict, "issue is already "+status)
		return
	}
	if verdict == "accept" && pipelineMetadataStatus(parseIssueMetadata(issue.Metadata)) == pipelineStatusAccepted {
		writeError(w, http.StatusConflict, "pipeline already accepted this issue")
		return
	}

	if verdict == "reject" {
		h.recordPipelineReject(w, r, issue, task, squad, policy, actorType, actorID, reason)
		return
	}
	h.recordPipelineAccept(w, r, issue, task, squad, policy, actorType, actorID, reason)
}

// recordPipelineAccept closes the issue through the ordinary update path and
// then stamps the acceptance in metadata. Status first: the response must never
// claim acceptance for an update the guards refused.
func (h *Handler) recordPipelineAccept(w http.ResponseWriter, r *http.Request, issue db.Issue, task db.AgentTaskQueue, squad db.Squad, policy squadworkflow.Policy, actorType, actorID, reason string) {
	if !h.delegateIssueStatusUpdate(w, r, `{"status":"done"}`) {
		return
	}
	metadata, revision, err := h.writePipelineVerdictMetadata(r, issue, nil, pipelineStatusAccepted)
	if err != nil {
		h.writePipelineMetadataError(w, err, "accept")
		return
	}
	h.publishPipelineMetadataChanged(issue, actorType, actorID, metadata, revision)
	round := pipelineMetadataRound(metadata)
	h.recordPipelineActivity(r, issue, task, squad, policy, actorID, "accept", reason, round, false)
	writeJSON(w, http.StatusOK, map[string]any{
		"verdict":    "accept",
		"round":      round,
		"max_rounds": policy.Pipeline.MaxRounds,
		"metadata":   metadata,
	})
}

// recordPipelineReject counts the round and, when a further round would exceed
// the cap, escalates. Round, status and — on escalation — the notification
// comment commit in ONE transaction: a retry after a lost response recomputes
// from committed state instead of starting a round that never happened, and an
// escalation can neither be recorded without its comment nor commented without
// its record.
func (h *Handler) recordPipelineReject(w http.ResponseWriter, r *http.Request, issue db.Issue, task db.AgentTaskQueue, squad db.Squad, policy squadworkflow.Policy, actorType, actorID, reason string) {
	var round int
	var escalated bool
	var metadata map[string]any
	var revision int64
	var comment db.Comment
	var commentIssueRevision int64

	_, err := wakeupWrite(h, r, func(q *db.Queries) (struct{}, error) {
		var none struct{}
		fresh, err := q.GetIssueMetadataInWorkspace(r.Context(), db.GetIssueMetadataInWorkspaceParams{
			ID:          issue.ID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return none, err
		}
		// Re-read inside the write: a concurrent verdict may have settled this
		// round while the request was being authorized.
		switch pipelineMetadataStatus(parseIssueMetadata(fresh.Metadata)) {
		case pipelineStatusEscalated, pipelineStatusAccepted:
			return none, errPipelineVerdictConflict
		}

		round = pipelineMetadataRound(parseIssueMetadata(fresh.Metadata)) + 1
		escalated = round > policy.Pipeline.MaxRounds
		status := pipelineStatusRejected
		if escalated {
			status = pipelineStatusEscalated
		}
		if err := setPipelineMetadataKey(r.Context(), q, issue, pipelineRoundMetadataKey, round); err != nil {
			return none, err
		}
		if err := setPipelineMetadataKey(r.Context(), q, issue, pipelineStatusMetadataKey, status); err != nil {
			return none, err
		}
		if escalated {
			created, err := q.CreateComment(r.Context(), db.CreateCommentParams{
				ID:           dbid.NewV7(),
				IssueID:      issue.ID,
				WorkspaceID:  issue.WorkspaceID,
				AuthorType:   actorType,
				AuthorID:     parseUUID(actorID),
				Content:      h.pipelineEscalationContent(r.Context(), issue, task, squad, reason, round, policy.Pipeline.MaxRounds),
				Type:         "comment",
				SourceTaskID: task.ID,
			})
			if err != nil {
				return none, err
			}
			comment = created.Comment()
			commentIssueRevision = created.IssueRevision
		}
		final, err := q.GetIssueMetadataInWorkspace(r.Context(), db.GetIssueMetadataInWorkspaceParams{
			ID:          issue.ID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return none, err
		}
		metadata = parseIssueMetadata(final.Metadata)
		revision = final.Revision
		return none, nil
	})
	switch {
	case errors.Is(err, errPipelineVerdictConflict):
		writeError(w, http.StatusConflict, "pipeline already escalated; a human must decide the next step")
		return
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "issue not found")
		return
	case err != nil:
		slog.Error("pipeline reject failed", "issue_id", uuidToString(issue.ID), "round", round, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to record verdict")
		return
	}

	h.publishPipelineMetadataChanged(issue, actorType, actorID, metadata, revision)
	if escalated {
		h.announcePipelineEscalation(r, issue, actorType, actorID, comment, commentIssueRevision)
	}
	h.recordPipelineActivity(r, issue, task, squad, policy, actorID, "reject", reason, round, escalated)
	writeJSON(w, http.StatusOK, map[string]any{
		"verdict":    "reject",
		"round":      round,
		"max_rounds": policy.Pipeline.MaxRounds,
		"escalated":  escalated,
		"metadata":   metadata,
	})
}

// setPipelineMetadataKey writes one primitive verdict key. A no-op (the value is
// already stored) returns no rows, which is not an error: the caller only needs
// the committed snapshot, and it re-reads that separately.
func setPipelineMetadataKey(ctx context.Context, q *db.Queries, issue db.Issue, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode pipeline metadata %s: %w", key, err)
	}
	_, err = q.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Key:         key,
		Value:       encoded,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// writePipelineVerdictMetadata persists a single-key verdict update (accept)
// and returns the committed snapshot.
func (h *Handler) writePipelineVerdictMetadata(r *http.Request, issue db.Issue, round *int, status string) (map[string]any, int64, error) {
	var metadata map[string]any
	var revision int64
	_, err := wakeupWrite(h, r, func(q *db.Queries) (struct{}, error) {
		var none struct{}
		if round != nil {
			if err := setPipelineMetadataKey(r.Context(), q, issue, pipelineRoundMetadataKey, *round); err != nil {
				return none, err
			}
		}
		if err := setPipelineMetadataKey(r.Context(), q, issue, pipelineStatusMetadataKey, status); err != nil {
			return none, err
		}
		final, err := q.GetIssueMetadataInWorkspace(r.Context(), db.GetIssueMetadataInWorkspaceParams{
			ID:          issue.ID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return none, err
		}
		metadata = parseIssueMetadata(final.Metadata)
		revision = final.Revision
		return none, nil
	})
	return metadata, revision, err
}

func (h *Handler) writePipelineMetadataError(w http.ResponseWriter, err error, op string) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "issue not found")
		return
	}
	slog.Error("pipeline verdict metadata write failed", "operation", op, "error", err)
	writeError(w, http.StatusInternalServerError, "failed to record verdict")
}

// delegateIssueStatusUpdate runs the public UpdateIssue handler against a
// synthetic body and a recording ResponseWriter, and reports whether the update
// landed. Delegating (rather than issuing the status UPDATE directly) is the
// point: the status guard, wake-ups, activity, events and sub-issue rules all
// run exactly as they do when a member sets the status, and a refusal reaches
// the caller with its original status and body.
func (h *Handler) delegateIssueStatusUpdate(w http.ResponseWriter, r *http.Request, body string) bool {
	sub := r.Clone(r.Context())
	sub.Body = io.NopCloser(strings.NewReader(body))
	sub.ContentLength = int64(len(body))

	recorder := &pipelineUpdateRecorder{header: make(http.Header)}
	h.UpdateIssue(recorder, sub)
	if recorder.statusCode() < 400 {
		return true
	}
	for key, values := range recorder.header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(recorder.statusCode())
	_, _ = w.Write(recorder.body.Bytes())
	return false
}

// pipelineUpdateRecorder captures what a delegated handler wrote so its refusal
// can be relayed verbatim. http.ResponseWriter is the whole contract because
// that is all UpdateIssue writes through.
type pipelineUpdateRecorder struct {
	header      http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

func (rec *pipelineUpdateRecorder) Header() http.Header { return rec.header }

func (rec *pipelineUpdateRecorder) Write(b []byte) (int, error) {
	if !rec.wroteHeader {
		rec.status = http.StatusOK
		rec.wroteHeader = true
	}
	return rec.body.Write(b)
}

func (rec *pipelineUpdateRecorder) WriteHeader(status int) {
	if rec.wroteHeader {
		return
	}
	rec.status = status
	rec.wroteHeader = true
}

func (rec *pipelineUpdateRecorder) statusCode() int {
	if !rec.wroteHeader {
		return http.StatusOK
	}
	return rec.status
}

// announcePipelineEscalation publishes the escalation comment and runs the
// ordinary comment triggers, which is what wakes the squad leader: its next
// turn reads pipeline_status=escalated and stands down with a summary instead
// of dispatching another round. The member mention in the same comment is what
// notifies the human. Both happen only after the comment committed.
func (h *Handler) announcePipelineEscalation(r *http.Request, issue db.Issue, actorType, actorID string, comment db.Comment, issueRevision int64) {
	resp := commentToResponse(comment, nil, nil)
	resp.IssueRevision = issueRevision
	workspaceID := uuidToString(issue.WorkspaceID)
	h.publish(protocol.EventCommentCreated, workspaceID, actorType, actorID, map[string]any{
		"comment":             resp,
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
		"issue_revision":      issueRevision,
	})
	originatorUserID := h.invokeOriginatorFromRequest(r, actorType, actorID)
	outcomes := h.triggerTasksForComment(r.Context(), issue, comment, nil, actorType, actorID, originatorUserID, nil, nil)
	for _, outcome := range outcomes {
		slog.Info("pipeline escalation trigger",
			"issue_id", uuidToString(issue.ID), "comment_id", uuidToString(comment.ID),
			"target_type", outcome.TargetType, "target_id", outcome.TargetID,
			"status", string(outcome.Status), "reason_code", string(outcome.ReasonCode))
	}
}

// pipelineEscalationContent is the server-authored top-level comment that stops
// the loop: it names the round that exhausted the cap, repeats the reason, asks
// the leader to stand down and pings the human who owns the decision. Mention
// labels are stripped of the brackets that would break out of the link syntax.
func (h *Handler) pipelineEscalationContent(ctx context.Context, issue db.Issue, task db.AgentTaskQueue, squad db.Squad, reason string, round, maxRounds int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Pipeline escalated after %d of %d rework rounds.**\n\n", round, maxRounds)
	fmt.Fprintf(&b, "Last rejection reason: %s\n", reason)

	if squad.LeaderID.Valid {
		if leader, err := h.Queries.GetAgent(ctx, squad.LeaderID); err == nil {
			fmt.Fprintf(&b, "\n[@%s](mention://agent/%s) stand down: summarize the current state for the owner instead of dispatching another round.\n",
				mentionLabel(leader.Name), uuidToString(leader.ID))
		}
	}
	ownerID := ""
	if task.OriginatorUserID.Valid {
		ownerID = uuidToString(task.OriginatorUserID)
	} else if issue.CreatorType == "member" && issue.CreatorID.Valid {
		ownerID = uuidToString(issue.CreatorID)
	}
	if ownerID != "" {
		fmt.Fprintf(&b, "\n[@owner](mention://member/%s) this pipeline needs a human decision.", ownerID)
	}
	return b.String()
}

// mentionLabel keeps a name from breaking out of the [@label](mention://...)
// link it is embedded in.
func mentionLabel(name string) string {
	cleaned := strings.NewReplacer("[", "", "]", "").Replace(strings.TrimSpace(name))
	if cleaned == "" {
		return "lead"
	}
	return cleaned
}

func (h *Handler) publishPipelineMetadataChanged(issue db.Issue, actorType, actorID string, metadata map[string]any, revision int64) {
	h.publish(protocol.EventIssueMetadataChanged, uuidToString(issue.WorkspaceID), actorType, actorID, map[string]any{
		"issue_id":       uuidToString(issue.ID),
		"metadata":       metadata,
		"issue_revision": revision,
	})
}

// recordPipelineActivity logs every verdict, mirroring the squad-leader
// evaluation row: actor_id carries the task's agent so activity lookups keyed on
// task.agent_id find it.
func (h *Handler) recordPipelineActivity(r *http.Request, issue db.Issue, task db.AgentTaskQueue, squad db.Squad, policy squadworkflow.Policy, actorID, verdict, reason string, round int, escalated bool) {
	details, err := json.Marshal(map[string]any{
		"squad_id":   uuidToString(squad.ID),
		"task_id":    uuidToString(task.ID),
		"verdict":    verdict,
		"reason":     reason,
		"round":      round,
		"max_rounds": policy.Pipeline.MaxRounds,
		"escalated":  escalated,
	})
	if err != nil {
		details = []byte("{}")
	}
	activity, err := h.Queries.CreateActivity(r.Context(), db.CreateActivityParams{
		ID:          dbid.NewV7(),
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
		ActorType:   pgtype.Text{String: "agent", Valid: true},
		ActorID:     task.AgentID,
		Action:      "pipeline_verdict",
		Details:     details,
	})
	if err != nil {
		// The verdict itself already committed; a failed activity row must not
		// make the caller retry a settled round.
		slog.Error("pipeline verdict activity write failed",
			"issue_id", uuidToString(issue.ID), "verdict", verdict, "error", err)
		return
	}
	h.publish(protocol.EventActivityCreated, uuidToString(issue.WorkspaceID), "agent", actorID, map[string]any{
		"issue_id": uuidToString(issue.ID),
		"entry": map[string]any{
			"type":       "activity",
			"id":         uuidToString(activity.ID),
			"actor_type": "agent",
			"actor_id":   actorID,
			"action":     activity.Action,
			"details":    json.RawMessage(details),
			"created_at": timestampToString(activity.CreatedAt),
		},
	})
}
