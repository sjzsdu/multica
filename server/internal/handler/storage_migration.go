package handler

import (
	"net/http"
)

// Storage migration API (local disk → configured object store).
//
// A run is instance-wide by nature — it scans every workspace's attachment
// and avatar rows — so both endpoints require workspace owner/admin: on a
// self-hosted deployment that role is the operator, and the entry point is
// the workspace settings page where the pending-files banner lives.
//
//	GET  /api/storage-migration  pending tally + current/last run snapshot
//	POST /api/storage-migration  start a run (409 when one is active)

// GetStorageMigrationStatus reports how many objects still live on local disk
// plus the current/last run state, so the settings banner can decide whether
// to appear and show progress while a run is in flight.
//
// Pending counts (5 full-table COUNTs across attachment + 4 avatar tables) are
// skipped while a run is active — the running UI only consumes the per-run
// state counters, and rows leave the scan predicate naturally as they are
// migrated, so re-scanning every poll brings no additional signal at the
// cost of a scan over the largest table in the DB.
func (h *Handler) GetStorageMigrationStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireStorageMigrationRole(w, r); !ok {
		return
	}
	if h.StorageMigrator == nil {
		// S3 is not configured (or the local upload dir failed to open), so
		// there is nowhere to migrate to. Not an error: the UI just hides
		// the banner.
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	snapshot := h.StorageMigrator.Snapshot()
	body := map[string]any{
		"available": true,
		"state":     snapshot,
	}
	if !snapshot.Running {
		pending, err := h.StorageMigrator.Pending(r.Context())
		if err != nil {
			writeErrorCode(w, http.StatusInternalServerError, "storage_migration_unavailable", "storage migration status unavailable")
			return
		}
		body["pending"] = pending
	}
	writeJSON(w, http.StatusOK, body)
}

// StartStorageMigration kicks off a background migration run. Re-running is
// always safe (rows already migrated leave the scan predicate), so the only
// refusal besides permissions is a run already in progress.
func (h *Handler) StartStorageMigration(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireStorageMigrationRole(w, r); !ok {
		return
	}
	if h.StorageMigrator == nil {
		writeFeatureDisabled(w, "storage_migration_not_configured", "storage migration is not configured")
		return
	}
	if h.StorageMigrator.Start() {
		writeErrorCode(w, http.StatusConflict, "storage_migration_running", "storage migration is already running")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

// requireStorageMigrationRole resolves the workspace on the request and gates
// it to owner/admin before either migration endpoint runs. The workspace ID
// comes from the X-Workspace-ID header via the shared resolver, not from a
// path parameter — these routes are flat under /api like the issue-status
// catalog.
func (h *Handler) requireStorageMigrationRole(w http.ResponseWriter, r *http.Request) (string, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id"); !ok {
		return "", false
	}
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return "", false
	}
	return workspaceID, true
}
