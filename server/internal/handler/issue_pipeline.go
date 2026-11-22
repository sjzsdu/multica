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

var (
	errPipelineCompletionRequired = errors.New("opt-in pipeline issues require product acceptance; a human may complete only after escalation")
	errPipelineMetadataProtected  = errors.New("only a human member can change opt-in pipeline metadata")
)

type pipelineAcceptanceContextKey struct{}

type pipelineAcceptanceWrite struct {
	issueID       pgtype.UUID
	expectedRound int
}

func pipelineAcceptanceForIssue(ctx context.Context, issueID pgtype.UUID) (pipelineAcceptanceWrite, bool) {
	acceptance, ok := ctx.Value(pipelineAcceptanceContextKey{}).(pipelineAcceptanceWrite)
	return acceptance, ok && acceptance.issueID == issueID
}

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

func pipelineRoundMatches(policy squadworkflow.Policy, metadata map[string]any, requested *int) bool {
	return !policy.RequirePipelineVerdict || requested != nil && *requested == pipelineMetadataRound(metadata)
}

// pipelinePolicyForParentIssue resolves only top-level squad work. Squad
// children keep their ordinary completion and metadata behavior even when the
// parent's policy opts into product verdicts.
func (h *Handler) pipelinePolicyForParentIssue(ctx context.Context, q *db.Queries, issue db.Issue) (squadworkflow.Policy, bool, error) {
	if issue.ParentIssueID.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		return squadworkflow.DefaultPolicy(), false, nil
	}
	squad, err := q.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID: issue.AssigneeID, WorkspaceID: issue.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return squadworkflow.DefaultPolicy(), false, nil
	}
	if err != nil {
		return squadworkflow.DefaultPolicy(), false, err
	}
	policy := h.loadSquadWorkflowPolicyWithQueries(ctx, q, squad)
	return policy, policy.Pipeline != nil, nil
}

func (h *Handler) checkPipelineCompletion(ctx context.Context, q *db.Queries, r *http.Request, issue db.Issue, targetStatus string) error {
	if targetStatus == "" || issuestatus.Effective(ctx, q, issue.WorkspaceID, targetStatus) != issuestatus.Done {
		return nil
	}
	if _, ok := pipelineAcceptanceForIssue(ctx, issue.ID); ok {
		return nil
	}
	policy, pipelineParent, err := h.pipelinePolicyForParentIssue(ctx, q, issue)
	if err != nil {
		return fmt.Errorf("load pipeline completion policy: %w", err)
	}
	if !pipelineParent || !policy.RequirePipelineVerdict {
		return nil
	}
	actorType, _ := h.resolveActor(r, requestUserID(r), uuidToString(issue.WorkspaceID))
	if actorType == "member" && !isMachineCredentialActor(r) &&
		pipelineMetadataStatus(parseIssueMetadata(issue.Metadata)) == pipelineStatusEscalated {
		return nil
	}
	return errPipelineCompletionRequired
}

func (h *Handler) checkPipelineMetadataMutation(r *http.Request, issue db.Issue, key string) error {
	if key != pipelineRoundMetadataKey && key != pipelineStatusMetadataKey {
		return nil
	}
	policy, pipelineParent, err := h.pipelinePolicyForParentIssue(r.Context(), h.Queries, issue)
	if err != nil {
		return fmt.Errorf("load pipeline metadata policy: %w", err)
	}
	if !pipelineParent || !policy.RequirePipelineVerdict {
		return nil
	}
	actorType, _ := h.resolveActor(r, requestUserID(r), uuidToString(issue.WorkspaceID))
	if actorType == "member" && !isMachineCredentialActor(r) {
		return nil
	}
	return errPipelineMetadataProtected
}

type squadTaskAuthorization struct {
	task      db.AgentTaskQueue
	squad     db.Squad
	policy    squadworkflow.Policy
	actorType string
	actorID   string
}

// authorizeSquadAgentTask binds the agent credential, task, issue and squad
// before either review or verdict code can use task-derived identifiers.
func (h *Handler) authorizeSquadAgentTask(w http.ResponseWriter, r *http.Request, issue db.Issue, action string) (squadTaskAuthorization, bool) {
	var auth squadTaskAuthorization
	taskUUID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task id")
	if !ok {
		return auth, false
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{
		ID: taskUUID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || !task.IssueID.Valid {
		writeError(w, http.StatusBadRequest, "task does not belong to issue")
		return auth, false
	}
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	if actorType != "agent" || !task.AgentID.Valid || actorID != uuidToString(task.AgentID) {
		writeError(w, http.StatusForbidden, "only the agent running this task can "+action)
		return auth, false
	}
	if uuidToString(task.IssueID) != uuidToString(issue.ID) {
		writeError(w, http.StatusBadRequest,
			"task does not belong to issue; "+action+" on issue "+uuidToString(task.IssueID)+" (the issue this task is running on)")
		return auth, false
	}
	if issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		writeError(w, http.StatusConflict, "issue is not assigned to a squad")
		return auth, false
	}
	squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{
		ID: issue.AssigneeID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "squad not found")
		return auth, false
	}
	isMember, err := h.Queries.IsSquadMember(r.Context(), db.IsSquadMemberParams{
		SquadID: squad.ID, MemberType: "agent", MemberID: parseUUID(actorID),
	})
	if err != nil {
		slog.Error("squad task authorization: membership lookup failed", "squad_id", uuidToString(squad.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load squad membership")
		return auth, false
	}
	if !isMember && actorID != uuidToString(squad.LeaderID) {
		writeError(w, http.StatusForbidden, "only members of this squad can "+action)
		return auth, false
	}
	return squadTaskAuthorization{
		task: task, squad: squad, policy: h.loadSquadWorkflowPolicy(r.Context(), squad),
		actorType: actorType, actorID: actorID,
	}, true
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
		Round   *int   `json:"round"`
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

	auth, ok := h.authorizeSquadAgentTask(w, r, issue, "record a verdict")
	if !ok {
		return
	}
	task, squad, policy := auth.task, auth.squad, auth.policy
	actorType, actorID := auth.actorType, auth.actorID
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
	if !pipelineRoundMatches(policy, parseIssueMetadata(issue.Metadata), req.Round) {
		writeError(w, http.StatusConflict, "pipeline round is missing or stale; reload the issue and retry")
		return
	}

	if verdict == "reject" {
		h.recordPipelineReject(w, r, issue, task, squad, policy, actorType, actorID, reason, req.Round)
		return
	}
	h.recordPipelineAccept(w, r, issue, task, squad, policy, actorType, actorID, reason)
}

// recordPipelineAccept delegates one internal update carrying server-only
// acceptance authority. UpdateIssue commits done and accepted in its atomic
// transaction, then keeps all of its ordinary post-commit side effects.
func (h *Handler) recordPipelineAccept(w http.ResponseWriter, r *http.Request, issue db.Issue, task db.AgentTaskQueue, squad db.Squad, policy squadworkflow.Policy, actorType, actorID, reason string) {
	if policy.RequirePipelineVerdict {
		err := h.validatePipelineAcceptanceReadiness(r.Context(), h.Queries, issue, policy)
		switch {
		case errors.Is(err, errPipelineVerdictConflict):
			writeError(w, http.StatusConflict, "pipeline acceptance requires in_review, a reproducible final run, and all workflow gates")
			return
		case err != nil:
			slog.Error("pipeline acceptance readiness failed", "issue_id", uuidToString(issue.ID), "error", err)
			writeError(w, http.StatusInternalServerError, "failed to validate pipeline acceptance")
			return
		}
	}
	updated, ok := h.delegatePipelineAcceptance(w, r, pipelineAcceptanceWrite{
		issueID:       issue.ID,
		expectedRound: pipelineMetadataRound(parseIssueMetadata(issue.Metadata)),
	})
	if !ok {
		return
	}
	metadata := updated.Metadata
	h.publishPipelineMetadataChanged(issue, actorType, actorID, metadata, updated.Revision)
	round := pipelineMetadataRound(metadata)
	h.recordPipelineActivity(r, issue, task, squad, policy, actorID, "accept", reason, round, false)
	writeJSON(w, http.StatusOK, map[string]any{
		"verdict":    "accept",
		"round":      round,
		"max_rounds": policy.Pipeline.MaxRounds,
		"metadata":   metadata,
	})
}

// validatePipelineAcceptanceReadiness recomputes the latest run instead of
// trusting its stored state or gate snapshot. The same check runs again with
// the issue locked in the acceptance transaction.
func (h *Handler) validatePipelineAcceptanceReadiness(ctx context.Context, q *db.Queries, issue db.Issue, policy squadworkflow.Policy) error {
	if issuestatus.Effective(ctx, q, issue.WorkspaceID, issue.Status) != "in_review" ||
		pipelineMetadataStatus(parseIssueMetadata(issue.Metadata)) == pipelineStatusEscalated {
		return errPipelineVerdictConflict
	}
	runs, err := q.ListSquadWorkflowRunsByParent(ctx, db.ListSquadWorkflowRunsByParentParams{
		ParentIssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("list workflow runs for pipeline acceptance: %w", err)
	}
	if len(runs) == 0 {
		return errPipelineVerdictConflict
	}
	decision, err := h.workflowDecisionForRunWithQueries(ctx, q, runs[0], issue, policy)
	if err != nil {
		return err
	}
	if !decision.StageClosed || !decision.CanFinalize {
		return errPipelineVerdictConflict
	}
	return nil
}

// recordPipelineReject counts the round and, when a further round would exceed
// the cap, escalates. Round, status and — on escalation — the notification
// comment commit in ONE transaction: a retry after a lost response recomputes
// from committed state instead of starting a round that never happened, and an
// escalation can neither be recorded without its comment nor commented without
// its record.
func (h *Handler) recordPipelineReject(w http.ResponseWriter, r *http.Request, issue db.Issue, task db.AgentTaskQueue, squad db.Squad, policy squadworkflow.Policy, actorType, actorID, reason string, expectedRound *int) {
	result, err := wakeupWrite(h, r, func(q *db.Queries) (pipelineRejectWrite, error) {
		current, err := q.LockIssueForDescriptionUpdate(r.Context(), db.LockIssueForDescriptionUpdateParams{
			ID: issue.ID, WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return pipelineRejectWrite{}, err
		}
		return h.writePipelineReject(r.Context(), q, current, task, squad, policy, actorType, actorID, reason, expectedRound, nil)
	})
	switch {
	case errors.Is(err, errPipelineVerdictConflict):
		writeError(w, http.StatusConflict, "pipeline already escalated; a human must decide the next step")
		return
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "issue not found")
		return
	case err != nil:
		slog.Error("pipeline reject failed", "issue_id", uuidToString(issue.ID), "round", result.Round, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to record verdict")
		return
	}

	h.publishPipelineMetadataChanged(issue, actorType, actorID, result.Metadata, result.Revision)
	if result.Escalated {
		h.announcePipelineEscalation(r, issue, actorType, actorID, result.Comment, result.CommentIssueRevision)
	}
	h.recordPipelineActivity(r, issue, task, squad, policy, actorID, "reject", reason, result.Round, result.Escalated)
	if policy.RequirePipelineVerdict && !result.Escalated {
		if err := h.dispatchPipelineRework(r.Context(), issue, policy, result.Round, reason); err != nil {
			slog.Error("pipeline rework dispatch failed after verdict commit",
				"issue_id", uuidToString(issue.ID), "round", result.Round, "error", err)
			writeError(w, http.StatusServiceUnavailable, "verdict committed, but automatic rework dispatch failed; the workflow worker will retry")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"verdict":    "reject",
		"round":      result.Round,
		"max_rounds": policy.Pipeline.MaxRounds,
		"escalated":  result.Escalated,
		"metadata":   result.Metadata,
	})
}

type pipelineRejectWrite struct {
	Round                int
	Escalated            bool
	Metadata             map[string]any
	Revision             int64
	Comment              db.Comment
	CommentIssueRevision int64
}

// writePipelineReject is the single capped-rework transaction used by both the
// product verdict and independent review. beforeReject persists evidence for
// the round being rejected before the counter advances.
func (h *Handler) writePipelineReject(
	ctx context.Context,
	q *db.Queries,
	issue db.Issue,
	task db.AgentTaskQueue,
	squad db.Squad,
	policy squadworkflow.Policy,
	actorType, actorID, reason string,
	expectedRound *int,
	beforeReject func(reviewedRound int) error,
) (pipelineRejectWrite, error) {
	result := pipelineRejectWrite{}
	currentMetadata := parseIssueMetadata(issue.Metadata)
	switch pipelineMetadataStatus(currentMetadata) {
	case pipelineStatusEscalated, pipelineStatusAccepted:
		return result, errPipelineVerdictConflict
	}
	status := issuestatus.Effective(ctx, q, issue.WorkspaceID, issue.Status)
	if status == issuestatus.Done || status == issuestatus.Cancelled || !pipelineRoundMatches(policy, currentMetadata, expectedRound) {
		return result, errPipelineVerdictConflict
	}
	reviewedRound := pipelineMetadataRound(currentMetadata)
	if beforeReject != nil {
		if err := beforeReject(reviewedRound); err != nil {
			return result, err
		}
	}

	result.Round = reviewedRound + 1
	result.Escalated = result.Round > policy.Pipeline.MaxRounds
	pipelineStatus := pipelineStatusRejected
	if result.Escalated {
		pipelineStatus = pipelineStatusEscalated
	}
	if err := setPipelineMetadataKey(ctx, q, issue, pipelineRoundMetadataKey, result.Round); err != nil {
		return pipelineRejectWrite{}, err
	}
	if err := setPipelineMetadataKey(ctx, q, issue, pipelineStatusMetadataKey, pipelineStatus); err != nil {
		return pipelineRejectWrite{}, err
	}
	if result.Escalated {
		created, err := q.CreateComment(ctx, db.CreateCommentParams{
			ID:           dbid.NewV7(),
			IssueID:      issue.ID,
			WorkspaceID:  issue.WorkspaceID,
			AuthorType:   actorType,
			AuthorID:     parseUUID(actorID),
			Content:      h.pipelineEscalationContent(ctx, issue, task, squad, reason, result.Round, policy.Pipeline.MaxRounds),
			Type:         "comment",
			SourceTaskID: task.ID,
		})
		if err != nil {
			return pipelineRejectWrite{}, err
		}
		result.Comment = created.Comment()
		result.CommentIssueRevision = created.IssueRevision
	}
	final, err := q.GetIssueMetadataInWorkspace(ctx, db.GetIssueMetadataInWorkspaceParams{
		ID: issue.ID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return pipelineRejectWrite{}, err
	}
	result.Metadata = parseIssueMetadata(final.Metadata)
	result.Revision = final.Revision
	return result, nil
}

// dispatchPipelineRework runs after the rejected round has committed. The
// durable recovery scan uses the same dispatch key if this post-commit call
// fails, so a lost response cannot lose the rework or start another round.
func (h *Handler) dispatchPipelineRework(ctx context.Context, issue db.Issue, policy squadworkflow.Policy, round int, reason string) error {
	runs, err := h.Queries.ListSquadWorkflowRunsByParent(ctx, db.ListSquadWorkflowRunsByParentParams{
		ParentIssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("list workflow runs for pipeline rework: %w", err)
	}
	if len(runs) == 0 {
		return errors.New("pipeline rework requires a workflow run")
	}
	return NewWorkflowRetryWorker(h).dispatchWorkflowActionWithReason(ctx, runs[0], issue, policy, "rework", round, reason)
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

// delegatePipelineAcceptance runs UpdateIssue with an unexported context value.
// No request header or JSON field can manufacture this authority.
func (h *Handler) delegatePipelineAcceptance(w http.ResponseWriter, r *http.Request, acceptance pipelineAcceptanceWrite) (IssueResponse, bool) {
	ctx := context.WithValue(r.Context(), pipelineAcceptanceContextKey{}, acceptance)
	sub := r.Clone(ctx)
	const body = `{"status":"done"}`
	sub.Body = io.NopCloser(strings.NewReader(body))
	sub.ContentLength = int64(len(body))

	recorder := &pipelineUpdateRecorder{header: make(http.Header)}
	h.UpdateIssue(recorder, sub)
	if recorder.statusCode() >= 400 {
		for key, values := range recorder.header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(recorder.statusCode())
		_, _ = w.Write(recorder.body.Bytes())
		return IssueResponse{}, false
	}
	var updated IssueResponse
	if err := json.Unmarshal(recorder.body.Bytes(), &updated); err != nil {
		slog.Error("decode pipeline acceptance update", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to record verdict")
		return IssueResponse{}, false
	}
	return updated, true
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
	outcomes := h.triggerTasksForComment(r.Context(), issue, comment, nil, actorType, actorID, originatorUserID, nil, commentSteerRequest{})
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
