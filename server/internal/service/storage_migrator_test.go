package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/storage"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Storage migrator tests. These are real-integration tests: a temp LOCAL
// upload dir, a shared Postgres pool, and an in-memory remote store — the
// three surfaces a migration run touches.
//
// The scan predicate is instance-wide (`%/uploads/%`), so rows other test
// packages seed can be seen by a run started here. They are safe: their keys
// do not exist under this test's LOCAL_UPLOAD_DIR, so their read fails and
// the row is deliberately left untouched (failure path never writes). Only
// rows this test seeds through its own LocalStorage can match.

// fakeRemote is an in-memory object store implementing storage.Storage plus
// the optional streamUploader capability — both shipped backends implement
// UploadStream, so the migrator's type assertion picks the streamed path,
// which is what production S3 runs take.
type fakeRemote struct {
	mu       sync.Mutex
	objects  map[string][]byte
	streamed int
	buffered int
}

func newFakeRemote() *fakeRemote {
	return &fakeRemote{objects: map[string][]byte{}}
}

func (f *fakeRemote) Upload(_ context.Context, key string, data []byte, _, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buffered++
	f.objects[key] = append([]byte(nil), data...)
	return f.ObjectURL(key), nil
}

func (f *fakeRemote) UploadStream(_ context.Context, key string, data io.Reader, sizeBytes int64, _, _ string) (string, error) {
	payload, err := io.ReadAll(data)
	if err != nil {
		return "", err
	}
	if sizeBytes >= 0 && int64(len(payload)) != sizeBytes {
		return "", fmt.Errorf("stream size mismatch: got %d, want %d", len(payload), sizeBytes)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streamed++
	f.objects[key] = payload
	return f.ObjectURL(key), nil
}

func (f *fakeRemote) Delete(_ context.Context, key string) {
	_ = f.DeleteObject(context.Background(), key)
}
func (f *fakeRemote) DeleteKeys(_ context.Context, keys []string) {}
func (f *fakeRemote) DeleteObject(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	return nil
}
func (f *fakeRemote) KeyFromURL(rawURL string) string {
	return rawURL[len("https://cdn.test/"):]
}
func (f *fakeRemote) ObjectURL(key string) string { return "https://cdn.test/" + key }
func (f *fakeRemote) CdnDomain() string           { return "cdn.test" }
func (f *fakeRemote) GetReader(_ context.Context, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	if !ok {
		return nil, fmt.Errorf("object not found: %s", key)
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), nil
}

func (f *fakeRemote) get(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	return data, ok
}

// newTestMigrator builds a migrator over a fresh temp upload dir and an
// in-memory remote. Returns the pool for raw-SQL fixtures and the dir so
// tests can assert on files.
func newTestMigrator(t *testing.T) (*StorageMigrator, *fakeRemote, *storage.LocalStorage, *pgxpool.Pool, string) {
	t.Helper()
	pool := sharedTestPool(t)
	dir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", dir)
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "http://migrator.test")
	local := storage.NewLocalStorageFromEnv()
	if local == nil {
		t.Fatalf("local storage init failed for %s", dir)
	}
	remote := newFakeRemote()
	m := &StorageMigrator{
		Queries: db.New(pool),
		Local:   local,
		Remote:  remote,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return m, remote, local, pool, dir
}

// seedMigratorWorkspace creates a throwaway workspace + owner and cleans them
// up after the test.
func seedMigratorWorkspace(t *testing.T, pool *pgxpool.Pool) (workspaceID, userID string) {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	email := fmt.Sprintf("storage-migrator-%d@multica.test", suffix)
	slug := fmt.Sprintf("storage-migrator-%d", suffix)

	if err := pool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ('Storage Migrator', $1)
		RETURNING id
	`, email).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM "user" WHERE email = $1`, email)
	})
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, '', 'SMG')
		RETURNING id
	`, "Storage Migrator WS", slug).Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM workspace WHERE slug = $1`, slug)
	})
	return workspaceID, userID
}

func insertMigratorAttachment(t *testing.T, pool *pgxpool.Pool, workspaceID, uploaderID, url string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO attachment (workspace_id, uploader_type, uploader_id, filename, url, content_type, size_bytes)
		VALUES ($1, 'member', $2, 'mig.bin', $3, 'application/octet-stream', 8)
		RETURNING id
	`, workspaceID, uploaderID, url).Scan(&id); err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM attachment WHERE id = $1`, id)
	})
	return id
}

// TestStorageMigratorMovesRowsThenCleansLocal covers the happy path end to
// end: upload → verify → CAS row → delete local, for both an attachment and
// an avatar, then proves a second run is a no-op (re-running is the recovery
// story after a crash).
func TestStorageMigratorMovesRowsThenCleansLocal(t *testing.T) {
	ctx := context.Background()
	m, remote, local, pool, dir := newTestMigrator(t)
	workspaceID, userID := seedMigratorWorkspace(t, pool)

	// Seed through LocalStorage itself so the stored URL round-trips through
	// KeyFromURL exactly the way real uploads do.
	attKey := "workspaces/" + workspaceID + "/attachments/happy.bin"
	attPayload := []byte("attachment payload")
	attURL, err := local.Upload(ctx, attKey, attPayload, "application/octet-stream", "happy.bin")
	if err != nil {
		t.Fatalf("seed local attachment: %v", err)
	}
	attachmentID := insertMigratorAttachment(t, pool, workspaceID, userID, attURL)

	avatarKey := "workspaces/" + workspaceID + "/avatar.png"
	avatarPayload := []byte("avatar payload")
	avatarURL, err := local.Upload(ctx, avatarKey, avatarPayload, "image/png", "avatar.png")
	if err != nil {
		t.Fatalf("seed local avatar: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workspace SET avatar_url = $1 WHERE id = $2`, avatarURL, workspaceID); err != nil {
		t.Fatalf("set workspace avatar: %v", err)
	}

	m.run(ctx)

	snap := m.Snapshot()
	if snap.Running {
		t.Error("run should have finished synchronously")
	}
	if snap.Migrated != 2 {
		t.Errorf("Migrated = %d, want 2 (attachment + avatar)", snap.Migrated)
	}
	if snap.LocalCleaned != 2 {
		t.Errorf("LocalCleaned = %d, want 2", snap.LocalCleaned)
	}
	if snap.FinishedAt == nil {
		t.Error("FinishedAt not recorded")
	}
	// Production S3 implements UploadStream, so the migrator's capability
	// assertion must route both objects through the streamed path.
	if remote.streamed != 2 || remote.buffered != 0 {
		t.Errorf("upload path: streamed=%d buffered=%d, want streamed=2 buffered=0", remote.streamed, remote.buffered)
	}

	var gotAttURL string
	if err := pool.QueryRow(ctx, `SELECT url FROM attachment WHERE id = $1`, attachmentID).Scan(&gotAttURL); err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	if want := remote.ObjectURL(attKey); gotAttURL != want {
		t.Errorf("attachment url = %q, want %q", gotAttURL, want)
	}

	var gotAvatarURL *string
	if err := pool.QueryRow(ctx, `SELECT avatar_url FROM workspace WHERE id = $1`, workspaceID).Scan(&gotAvatarURL); err != nil {
		t.Fatalf("read workspace avatar: %v", err)
	}
	if gotAvatarURL == nil || *gotAvatarURL != remote.ObjectURL(avatarKey) {
		t.Errorf("workspace avatar_url = %v, want %q", gotAvatarURL, remote.ObjectURL(avatarKey))
	}

	for key, want := range map[string][]byte{attKey: attPayload, avatarKey: avatarPayload} {
		got, ok := remote.get(key)
		if !ok {
			t.Errorf("remote object %q not uploaded", key)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("remote object %q = %q, want %q", key, got, want)
		}
		if _, err := os.Stat(filepath.Join(dir, key)); !os.IsNotExist(err) {
			t.Errorf("local file %q still exists (stat err: %v)", key, err)
		}
	}
}

// TestStorageMigratorLeavesRowWhenLocalFileMissing covers the failure path:
// the object is gone from disk (a manual delete, a half-finished previous
// run). The row must keep its old URL — pointing at a file that no longer
// resolves is still the truth, and rewriting it to a remote URL whose object
// was never uploaded would turn a local miss into a permanent remote 404.
func TestStorageMigratorLeavesRowWhenLocalFileMissing(t *testing.T) {
	ctx := context.Background()
	m, remote, local, pool, _ := newTestMigrator(t)
	workspaceID, userID := seedMigratorWorkspace(t, pool)

	ghostKey := "workspaces/" + workspaceID + "/attachments/ghost.bin"
	ghostURL, err := local.Upload(ctx, ghostKey, []byte("ephemeral"), "application/octet-stream", "ghost.bin")
	if err != nil {
		t.Fatalf("seed local ghost: %v", err)
	}
	attachmentID := insertMigratorAttachment(t, pool, workspaceID, userID, ghostURL)
	if err := local.DeleteObject(ctx, ghostKey); err != nil {
		t.Fatalf("delete local ghost: %v", err)
	}

	m.run(ctx)

	snap := m.Snapshot()
	if snap.Migrated != 0 {
		t.Errorf("Migrated = %d, want 0", snap.Migrated)
	}
	if snap.Failed < 1 {
		t.Errorf("Failed = %d, want >= 1", snap.Failed)
	}
	if _, ok := remote.get(ghostKey); ok {
		t.Error("ghost object must not be uploaded")
	}

	var gotURL string
	if err := pool.QueryRow(ctx, `SELECT url FROM attachment WHERE id = $1`, attachmentID).Scan(&gotURL); err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	if gotURL != ghostURL {
		t.Errorf("attachment url rewritten to %q, want unchanged %q", gotURL, ghostURL)
	}
}
