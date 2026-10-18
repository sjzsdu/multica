package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// migrationBatchSize bounds how many rows one scan page loads. Large enough
// that a few thousand attachments cost a handful of queries, small enough
// that the page itself stays cheap to hold.
const migrationBatchSize = 500

// migrationFailureCap bounds the failure list carried in memory and reported
// to the UI. A catastrophically misconfigured endpoint can fail every object;
// recording the first N with a dropped counter keeps the snapshot bounded
// while still telling the operator what went wrong.
const migrationFailureCap = 50

// StorageMigrator copies objects that still live on the local-disk backend up
// to the configured S3-compatible object store, repoints the referencing rows
// at the new URLs, and removes the local file — per object, in that order.
//
// Per-object ordering is the safety property: upload → verify → CAS the DB
// row → delete local. A crash or failure at any step leaves the local file
// intact and the row still resolving (to the local URL, or to the new object
// once the row was updated), so re-running the migration is always safe —
// rows already migrated no longer match the scan predicate.
//
// Process-local state: running/migrated/failed counts and the in-flight flag
// live in memory (mu-protected). Multi-replica deployments will have each
// replica see its own state; the Start() 409 refusal does NOT coordinate
// across replicas, so setups that need a single serial run MUST schedule
// this on one replica only (or accept that concurrent runs are idempotent
// but wasteful). POST and GET may also hit different replicas, so the UI
// may briefly show an empty state right after start until the next poll.
//
// No cancellation: Start() runs the migration goroutine with
// context.Background(). The run is single-pass and non-destructive on
// failure (local files stay), so the operator's only escape is letting it
// finish or restarting the process. This is deliberate — a mid-run cancel
// would leave the operator uncertain which rows had already been switched
// to the object store URL.
//
// Memory: copyOne buffers each attachment fully into memory before upload.
// Attachments on the order of a few hundred MB will hold that much memory
// briefly; the stream path would avoid the duplicate hold inside the SDK
// but still requires one copy of the bytes.
type StorageMigrator struct {
	Queries *db.Queries
	Local   *storage.LocalStorage
	Remote  storage.Storage
	Logger  *slog.Logger

	mu      sync.Mutex
	state   MigrationState
	running bool
}

// MigrationFailure records one object the migration could not move. The local
// copy of a failed object is always left in place.
type MigrationFailure struct {
	Kind  string `json:"kind"` // "attachment" | "avatar"
	ID    string `json:"id"`
	URL   string `json:"url"`
	Error string `json:"error"`
}

// MigrationState is the operator-visible snapshot of a migration run.
// Failures is capped at migrationFailureCap; DroppedFailures counts the rest.
type MigrationState struct {
	Running         bool               `json:"running"`
	Total           int64              `json:"total"`
	Migrated        int64              `json:"migrated"`
	Failed          int64              `json:"failed"`
	LocalCleaned    int64              `json:"local_cleaned"`
	Failures        []MigrationFailure `json:"failures"`
	DroppedFailures int64              `json:"dropped_failures"`
	StartedAt       *time.Time         `json:"started_at,omitempty"`
	FinishedAt      *time.Time         `json:"finished_at,omitempty"`
}

// PendingCounts is the live "how much is still on local disk" tally the UI
// banner keys off. Computed on read so it stays truthful while a run is in
// flight (rows leave the scan predicate as they are migrated).
type PendingCounts struct {
	Attachments int64 `json:"pending_attachments"`
	Avatars     int64 `json:"pending_avatars"`
}

// Snapshot returns the current run state for the status endpoint.
func (m *StorageMigrator) Snapshot() MigrationState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.state
	out.Running = m.running
	out.Failures = append([]MigrationFailure(nil), m.state.Failures...)
	return out
}

// Pending returns the number of rows still referencing local-disk objects.
func (m *StorageMigrator) Pending(ctx context.Context) (PendingCounts, error) {
	attachments, err := m.Queries.CountLocalStorageAttachments(ctx)
	if err != nil {
		return PendingCounts{}, fmt.Errorf("count local attachments: %w", err)
	}
	var avatars int64
	for _, table := range avatarTables() {
		n, err := table.count(ctx, m.Queries)
		if err != nil {
			return PendingCounts{}, fmt.Errorf("count local %s avatars: %w", table.name(), err)
		}
		avatars += n
	}
	return PendingCounts{Attachments: attachments, Avatars: avatars}, nil
}

// Start launches a migration run in the background. Reports alreadyRunning so
// the handler can answer 409 instead of stacking a second run on the first.
// The run is idempotent, so a lost race costs nothing but a duplicate scan.
func (m *StorageMigrator) Start() (alreadyRunning bool) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return true
	}
	m.running = true
	now := time.Now()
	m.state = MigrationState{Running: true, StartedAt: &now}
	m.mu.Unlock()

	go m.run(context.Background())
	return false
}

func (m *StorageMigrator) log() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

func (m *StorageMigrator) run(ctx context.Context) {
	defer func() {
		m.mu.Lock()
		m.running = false
		now := time.Now()
		m.state.Running = false
		m.state.FinishedAt = &now
		m.mu.Unlock()
	}()

	log := m.log()
	if err := m.migrateAttachments(ctx, log); err != nil {
		// A scan-level failure (DB unreachable) aborts that pass; per-object
		// failures were already recorded and did not reach here.
		log.Error("storage migration: attachment scan aborted", "error", err)
		m.recordFailure(MigrationFailure{Kind: "attachment", Error: err.Error()})
	}
	for _, table := range avatarTables() {
		if err := m.migrateAvatarPage(ctx, table, log); err != nil {
			log.Error("storage migration: avatar scan aborted", "table", table.name(), "error", err)
			m.recordFailure(MigrationFailure{Kind: "avatar", Error: err.Error()})
		}
	}

	snap := m.Snapshot()
	log.Info("storage migration finished",
		"migrated", snap.Migrated, "failed", snap.Failed, "local_cleaned", snap.LocalCleaned)
}

func (m *StorageMigrator) migrateAttachments(ctx context.Context, log *slog.Logger) error {
	// A zero pgtype.UUID{} would encode as SQL NULL and make `id > NULL`
	// match nothing — the cursor must be the VALID all-zero UUID instead.
	afterID := pgtype.UUID{Valid: true}
	for {
		rows, err := m.Queries.ListLocalStorageAttachments(ctx, db.ListLocalStorageAttachmentsParams{
			AfterID:    afterID,
			BatchLimit: migrationBatchSize,
		})
		if err != nil {
			return fmt.Errorf("list local attachments: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			id := util.UUIDToString(row.ID)
			key := m.Local.KeyFromURL(row.Url)
			if err := m.copyOne(ctx, key, row.Filename, row.ContentType); err != nil {
				m.recordFailure(MigrationFailure{Kind: "attachment", ID: id, URL: row.Url, Error: err.Error()})
				log.Warn("storage migration: attachment not migrated", "id", id, "key", key, "error", err)
				continue
			}
			updated, err := m.Queries.UpdateAttachmentURLIfUnchanged(ctx, db.UpdateAttachmentURLIfUnchangedParams{
				ID:     row.ID,
				OldUrl: row.Url,
				NewUrl: m.Remote.ObjectURL(key),
			})
			if err != nil {
				m.recordFailure(MigrationFailure{Kind: "attachment", ID: id, URL: row.Url, Error: fmt.Sprintf("update row: %v", err)})
				log.Warn("storage migration: attachment row update failed", "id", id, "error", err)
				continue
			}
			if updated == 0 {
				// The row stopped pointing at this local URL mid-run (a
				// concurrent delete or re-upload). Leave the local file:
				// conservative, and the object is unreachable either way.
				log.Info("storage migration: attachment row changed during migration, keeping local file", "id", id)
				continue
			}
			m.countMigrated()
			if err := m.Local.DeleteObject(ctx, key); err != nil {
				log.Warn("storage migration: local cleanup failed", "id", id, "key", key, "error", err)
				continue
			}
			m.countLocalCleaned()
		}
		afterID = rows[len(rows)-1].ID
	}
}

// copyOne moves one object from local disk to the remote store under the same
// key, then verifies the object is readable back. The local file is NOT
// touched here — the caller CAS-updates the row and deletes local only after
// this succeeds.
func (m *StorageMigrator) copyOne(ctx context.Context, key, filename, contentType string) error {
	if key == "" {
		return fmt.Errorf("empty storage key")
	}
	// Read the whole object into memory, then pick the upload path. The local
	// file sits on the same host and attachments are bounded, so buffering is
	// the simple correct choice; the stream path below avoids holding a large
	// object twice (here and inside the SDK) when the backend supports it.
	reader, err := m.Local.GetReader(ctx, key)
	if err != nil {
		return fmt.Errorf("read local object: %w", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read local object: %w", err)
	}

	if streamer, ok := m.Remote.(streamUploader); ok {
		if _, err := streamer.UploadStream(ctx, key, bytes.NewReader(data), int64(len(data)), contentType, filename); err != nil {
			return fmt.Errorf("upload to object store: %w", err)
		}
	} else {
		if _, err := m.Remote.Upload(ctx, key, data, contentType, filename); err != nil {
			return fmt.Errorf("upload to object store: %w", err)
		}
	}

	// Existence check: a PutObject that returned success but left no readable
	// object would otherwise surface later as a 404 on the first download,
	// long after the local copy was deleted.
	verify, err := m.Remote.GetReader(ctx, key)
	if err != nil {
		return fmt.Errorf("verify uploaded object: %w", err)
	}
	verify.Close()
	return nil
}

// streamUploader is the optional streaming-upload capability. Both shipped
// backends (S3, local) implement it; a custom backend that does not simply
// takes the buffered Upload path in copyOne.
type streamUploader interface {
	UploadStream(ctx context.Context, key string, data io.Reader, sizeBytes int64, contentType string, filename string) (string, error)
}

// avatarObjectMeta derives the content type and download filename for an
// avatar object. The bytes decide the content type (the sniffer handles
// mislabelled extensions); the extension is the fallback for anything the
// sniffer reads as generic octet-stream or text. The key basename is the
// filename.
func (m *StorageMigrator) avatarObjectMeta(ctx context.Context, key string) (contentType, filename string, err error) {
	reader, err := m.Local.GetReader(ctx, key)
	if err != nil {
		return "", "", fmt.Errorf("read local avatar: %w", err)
	}
	defer reader.Close()
	buf := make([]byte, 512)
	n, readErr := io.ReadFull(reader, buf)
	if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
		return "", "", fmt.Errorf("sniff local avatar: %w", readErr)
	}
	contentType = http.DetectContentType(buf[:n])
	if contentType == "application/octet-stream" || isTextualDetection(contentType) {
		if byExt := mime.TypeByExtension(path.Ext(key)); byExt != "" {
			contentType = byExt
		}
	}
	return contentType, path.Base(key), nil
}

// isTextualDetection reports whether the content sniffer produced a generic
// text type — avatars are images, so a text detection means the sniff read
// nothing useful and the extension should decide instead.
func isTextualDetection(contentType string) bool {
	return len(contentType) >= len("text/") && contentType[:len("text/")] == "text/"
}

// avatarTable abstracts one avatar-bearing table so the four of them (user,
// workspace, agent, squad) share a single scan/migrate loop. The sqlc queries
// differ only by table, and this keeps that difference in one place instead of
// four copies of the batch logic.
type avatarTable interface {
	name() string
	count(ctx context.Context, q *db.Queries) (int64, error)
	// list returns one page of (id, url) rows still on local disk, after the
	// given cursor, plus the cursor to pass to the next page.
	list(ctx context.Context, q *db.Queries, after pgtype.UUID) (ids []pgtype.UUID, urls []string, next pgtype.UUID, err error)
	update(ctx context.Context, q *db.Queries, id pgtype.UUID, oldURL, newURL string) (int64, error)
}

func avatarTables() []avatarTable {
	return []avatarTable{userAvatarTable{}, workspaceAvatarTable{}, agentAvatarTable{}, squadAvatarTable{}}
}

// migrateAvatarPage drives one table through the same copy → CAS → delete
// sequence the attachment path uses.
func (m *StorageMigrator) migrateAvatarPage(ctx context.Context, table avatarTable, log *slog.Logger) error {
	afterID := pgtype.UUID{Valid: true}
	for {
		ids, urls, next, err := table.list(ctx, m.Queries, afterID)
		if err != nil {
			return fmt.Errorf("list local %s avatars: %w", table.name(), err)
		}
		if len(ids) == 0 {
			return nil
		}
		for i := range ids {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			id := util.UUIDToString(ids[i])
			url := urls[i]
			key := m.Local.KeyFromURL(url)
			contentType, filename, metaErr := m.avatarObjectMeta(ctx, key)
			if metaErr != nil {
				m.recordFailure(MigrationFailure{Kind: "avatar", ID: id, URL: url, Error: metaErr.Error()})
				log.Warn("storage migration: avatar not migrated", "table", table.name(), "id", id, "key", key, "error", metaErr)
				continue
			}
			if err := m.copyOne(ctx, key, filename, contentType); err != nil {
				m.recordFailure(MigrationFailure{Kind: "avatar", ID: id, URL: url, Error: err.Error()})
				log.Warn("storage migration: avatar not migrated", "table", table.name(), "id", id, "key", key, "error", err)
				continue
			}
			updated, err := table.update(ctx, m.Queries, ids[i], url, m.Remote.ObjectURL(key))
			if err != nil {
				m.recordFailure(MigrationFailure{Kind: "avatar", ID: id, URL: url, Error: fmt.Sprintf("update row: %v", err)})
				log.Warn("storage migration: avatar row update failed", "table", table.name(), "id", id, "error", err)
				continue
			}
			if updated == 0 {
				log.Info("storage migration: avatar row changed during migration, keeping local file", "table", table.name(), "id", id)
				continue
			}
			m.countMigrated()
			if err := m.Local.DeleteObject(ctx, key); err != nil {
				log.Warn("storage migration: local cleanup failed", "table", table.name(), "id", id, "key", key, "error", err)
				continue
			}
			m.countLocalCleaned()
		}
		afterID = next
	}
}

func (m *StorageMigrator) recordFailure(f MigrationFailure) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Failed++
	if len(m.state.Failures) < migrationFailureCap {
		m.state.Failures = append(m.state.Failures, f)
	} else {
		m.state.DroppedFailures++
	}
}

func (m *StorageMigrator) countMigrated() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Migrated++
}

func (m *StorageMigrator) countLocalCleaned() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.LocalCleaned++
}

type userAvatarTable struct{}

func (userAvatarTable) name() string { return "user" }
func (userAvatarTable) count(ctx context.Context, q *db.Queries) (int64, error) {
	return q.CountLocalStorageUserAvatars(ctx)
}
func (userAvatarTable) list(ctx context.Context, q *db.Queries, after pgtype.UUID) ([]pgtype.UUID, []string, pgtype.UUID, error) {
	rows, err := q.ListLocalStorageUserAvatars(ctx, db.ListLocalStorageUserAvatarsParams{AfterID: after, BatchLimit: migrationBatchSize})
	if err != nil {
		return nil, nil, after, err
	}
	ids := make([]pgtype.UUID, 0, len(rows))
	urls := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		urls = append(urls, row.AvatarUrl.String)
	}
	var next pgtype.UUID
	if len(ids) > 0 {
		next = ids[len(ids)-1]
	}
	return ids, urls, next, nil
}
func (userAvatarTable) update(ctx context.Context, q *db.Queries, id pgtype.UUID, oldURL, newURL string) (int64, error) {
	return q.UpdateUserAvatarURLIfUnchanged(ctx, db.UpdateUserAvatarURLIfUnchangedParams{
		ID: id, OldUrl: pgtype.Text{String: oldURL, Valid: true}, NewUrl: pgtype.Text{String: newURL, Valid: true},
	})
}

type workspaceAvatarTable struct{}

func (workspaceAvatarTable) name() string { return "workspace" }
func (workspaceAvatarTable) count(ctx context.Context, q *db.Queries) (int64, error) {
	return q.CountLocalStorageWorkspaceAvatars(ctx)
}
func (workspaceAvatarTable) list(ctx context.Context, q *db.Queries, after pgtype.UUID) ([]pgtype.UUID, []string, pgtype.UUID, error) {
	rows, err := q.ListLocalStorageWorkspaceAvatars(ctx, db.ListLocalStorageWorkspaceAvatarsParams{AfterID: after, BatchLimit: migrationBatchSize})
	if err != nil {
		return nil, nil, after, err
	}
	ids := make([]pgtype.UUID, 0, len(rows))
	urls := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		urls = append(urls, row.AvatarUrl.String)
	}
	var next pgtype.UUID
	if len(ids) > 0 {
		next = ids[len(ids)-1]
	}
	return ids, urls, next, nil
}
func (workspaceAvatarTable) update(ctx context.Context, q *db.Queries, id pgtype.UUID, oldURL, newURL string) (int64, error) {
	return q.UpdateWorkspaceAvatarURLIfUnchanged(ctx, db.UpdateWorkspaceAvatarURLIfUnchangedParams{
		ID: id, OldUrl: pgtype.Text{String: oldURL, Valid: true}, NewUrl: pgtype.Text{String: newURL, Valid: true},
	})
}

type agentAvatarTable struct{}

func (agentAvatarTable) name() string { return "agent" }
func (agentAvatarTable) count(ctx context.Context, q *db.Queries) (int64, error) {
	return q.CountLocalStorageAgentAvatars(ctx)
}
func (agentAvatarTable) list(ctx context.Context, q *db.Queries, after pgtype.UUID) ([]pgtype.UUID, []string, pgtype.UUID, error) {
	rows, err := q.ListLocalStorageAgentAvatars(ctx, db.ListLocalStorageAgentAvatarsParams{AfterID: after, BatchLimit: migrationBatchSize})
	if err != nil {
		return nil, nil, after, err
	}
	ids := make([]pgtype.UUID, 0, len(rows))
	urls := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		urls = append(urls, row.AvatarUrl.String)
	}
	var next pgtype.UUID
	if len(ids) > 0 {
		next = ids[len(ids)-1]
	}
	return ids, urls, next, nil
}
func (agentAvatarTable) update(ctx context.Context, q *db.Queries, id pgtype.UUID, oldURL, newURL string) (int64, error) {
	return q.UpdateAgentAvatarURLIfUnchanged(ctx, db.UpdateAgentAvatarURLIfUnchangedParams{
		ID: id, OldUrl: pgtype.Text{String: oldURL, Valid: true}, NewUrl: pgtype.Text{String: newURL, Valid: true},
	})
}

type squadAvatarTable struct{}

func (squadAvatarTable) name() string { return "squad" }
func (squadAvatarTable) count(ctx context.Context, q *db.Queries) (int64, error) {
	return q.CountLocalStorageSquadAvatars(ctx)
}
func (squadAvatarTable) list(ctx context.Context, q *db.Queries, after pgtype.UUID) ([]pgtype.UUID, []string, pgtype.UUID, error) {
	rows, err := q.ListLocalStorageSquadAvatars(ctx, db.ListLocalStorageSquadAvatarsParams{AfterID: after, BatchLimit: migrationBatchSize})
	if err != nil {
		return nil, nil, after, err
	}
	ids := make([]pgtype.UUID, 0, len(rows))
	urls := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		urls = append(urls, row.AvatarUrl.String)
	}
	var next pgtype.UUID
	if len(ids) > 0 {
		next = ids[len(ids)-1]
	}
	return ids, urls, next, nil
}
func (squadAvatarTable) update(ctx context.Context, q *db.Queries, id pgtype.UUID, oldURL, newURL string) (int64, error) {
	return q.UpdateSquadAvatarURLIfUnchanged(ctx, db.UpdateSquadAvatarURLIfUnchangedParams{
		ID: id, OldUrl: pgtype.Text{String: oldURL, Valid: true}, NewUrl: pgtype.Text{String: newURL, Valid: true},
	})
}
