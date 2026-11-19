package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// systemFallbackRuntimePayload is the wire shape shared by the read and write
// endpoints. A null fallback_runtime means the global tier is off.
type systemFallbackRuntimePayload struct {
	FallbackRuntime *service.FallbackRuntimeEntry `json:"fallback_runtime"`
}

// GetSystemFallbackRuntime returns the operator-pinned last-resort runtime.
// Every workspace member may read it — the settings page renders it read-only
// for members — while only owners and admins may write.
func (h *Handler) GetSystemFallbackRuntime(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	writeJSON(w, http.StatusOK, systemFallbackRuntimePayload{
		FallbackRuntime: service.SystemFallbackRuntime(r.Context(), h.Queries),
	})
}

// UpdateSystemFallbackRuntime replaces the global fallback, or clears it when
// the body carries null. Owners and admins only: the value applies to every
// workspace on the instance, so it is gated at the same level as the other
// workspace settings that affect everyone.
func (h *Handler) UpdateSystemFallbackRuntime(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin")
	if !ok {
		return
	}

	var in systemFallbackRuntimePayload
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid system fallback body")
		return
	}

	entry := in.FallbackRuntime
	if entry != nil {
		normalized, ok := h.validateSystemFallbackRuntime(w, r, workspaceID, member.UserID, *entry)
		if !ok {
			return
		}
		entry = &normalized
	}

	stored, err := h.writeSystemFallbackRuntime(r.Context(), entry)
	if err != nil {
		slog.Warn("update system fallback runtime failed", "error", err, "workspace_id", workspaceID)
		writeError(w, http.StatusInternalServerError, "could not save system fallback runtime")
		return
	}
	writeJSON(w, http.StatusOK, systemFallbackRuntimePayload{FallbackRuntime: stored})
}

// validateSystemFallbackRuntime checks one entry the way the agent-level chain
// is checked at write time: the runtime must exist in this workspace and be
// usable by the editor. Model ids are per-runtime and discovered from the
// daemon, so — exactly like an agent's primary model — the server stores the
// chosen id as-is and the shared model picker guarantees it belongs to that
// runtime. A blank model means "the runtime's own default".
func (h *Handler) validateSystemFallbackRuntime(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID string,
	actorID pgtype.UUID,
	entry service.FallbackRuntimeEntry,
) (service.FallbackRuntimeEntry, bool) {
	runtimeID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(entry.RuntimeID), "runtime_id")
	if !ok {
		return service.FallbackRuntimeEntry{}, false
	}
	rt, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
		ID:          runtimeID,
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "runtime not found in this workspace")
			return service.FallbackRuntimeEntry{}, false
		}
		slog.Warn("load system fallback runtime failed", "error", err, "workspace_id", workspaceID)
		writeError(w, http.StatusInternalServerError, "could not load runtime")
		return service.FallbackRuntimeEntry{}, false
	}
	if !service.RuntimeAllowsUser(rt, actorID) {
		writeError(w, http.StatusBadRequest, "runtime is not available to this user")
		return service.FallbackRuntimeEntry{}, false
	}
	return service.FallbackRuntimeEntry{
		RuntimeID:     uuidToString(runtimeID),
		Model:         strings.TrimSpace(entry.Model),
		ThinkingLevel: entry.ThinkingLevel,
		ServiceTier:   entry.ServiceTier,
	}, true
}

// writeSystemFallbackRuntime stores the entry (or removes the key) and parses
// the row back, so the response always matches what the read path will hand
// out. Only the settings.fallback_runtime key is touched.
func (h *Handler) writeSystemFallbackRuntime(
	ctx context.Context,
	entry *service.FallbackRuntimeEntry,
) (*service.FallbackRuntimeEntry, error) {
	var (
		row db.SystemConfig
		err error
	)
	if entry == nil {
		row, err = h.Queries.ClearSystemFallbackRuntime(ctx)
	} else {
		var raw []byte
		raw, err = json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		row, err = h.Queries.SetSystemFallbackRuntime(ctx, raw)
	}
	if err != nil {
		return nil, err
	}
	var settings systemFallbackRuntimePayload
	if err := json.Unmarshal(row.Settings, &settings); err != nil {
		return nil, err
	}
	return settings.FallbackRuntime, nil
}
