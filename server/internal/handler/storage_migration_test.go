package handler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/storage"
)

// Storage-migration endpoint tests: permission gate, unavailable reporting,
// start/409 flow, and live status while a run is in flight.

// blockingRemote is an in-memory object store whose Upload blocks until the
// test releases it, so a run can be held mid-flight deterministically.
type blockingRemote struct {
	mu      sync.Mutex
	objects map[string][]byte
	entered chan struct{}
	gate    chan struct{}
	once    sync.Once
	release sync.Once
}

func newBlockingRemote() *blockingRemote {
	return &blockingRemote{
		objects: map[string][]byte{},
		entered: make(chan struct{}),
		gate:    make(chan struct{}),
	}
}

func (r *blockingRemote) unblock() { r.release.Do(func() { close(r.gate) }) }

func (r *blockingRemote) Upload(_ context.Context, key string, data []byte, _, _ string) (string, error) {
	r.once.Do(func() { close(r.entered) })
	<-r.gate
	r.mu.Lock()
	defer r.mu.Unlock()
	r.objects[key] = append([]byte(nil), data...)
	return r.ObjectURL(key), nil
}

func (r *blockingRemote) Delete(_ context.Context, _ string)             {}
func (r *blockingRemote) DeleteKeys(_ context.Context, _ []string)       {}
func (r *blockingRemote) DeleteObject(_ context.Context, _ string) error { return nil }
func (r *blockingRemote) KeyFromURL(string) string                       { return "" }
func (r *blockingRemote) ObjectURL(key string) string                    { return "http://blocking.test/" + key }
func (r *blockingRemote) CdnDomain() string                              { return "blocking.test" }
func (r *blockingRemote) GetReader(_ context.Context, key string) (io.ReadCloser, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, ok := r.objects[key]
	if !ok {
		return nil, fmt.Errorf("object not found: %s", key)
	}
	return io.NopCloser(strings.NewReader(string(data))), nil
}

// storageMigrationMemberFixture adds a plain 'member' to the test workspace
// and returns their user id, cleaning up after the test.
func storageMigrationMemberFixture(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	email := fmt.Sprintf("storage-migration-member-%d@multica.test", time.Now().UnixNano())
	var userID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ('Storage Migration Member', $1)
		RETURNING id
	`, email).Scan(&userID); err != nil {
		t.Fatalf("create member user: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, email)
	})
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'member')
	`, testWorkspaceID, userID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	return userID
}

// TestStorageMigrationGatingAndUnavailable covers the permission gate for
// both endpoints and the "no migrator wired" behaviour: GET still succeeds
// with available=false so the UI can hide the banner, POST refuses with a
// stable feature code.
func TestStorageMigrationGatingAndUnavailable(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	// Workspace owner sees the feature as unavailable (no S3 in the unit
	// suite), not as an error.
	w := httptest.NewRecorder()
	testHandler.GetStorageMigrationStatus(w, newRequest("GET", "/api/storage-migration", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("owner GET status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"available":false`) {
		t.Errorf("owner GET body = %s, want available:false", w.Body.String())
	}

	// A plain member is refused on both endpoints, before availability.
	memberID := storageMigrationMemberFixture(t)
	w = httptest.NewRecorder()
	testHandler.GetStorageMigrationStatus(w, newRequestAs(memberID, "GET", "/api/storage-migration", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("member GET status = %d, want 403", w.Code)
	}
	w = httptest.NewRecorder()
	testHandler.StartStorageMigration(w, newRequestAs(memberID, "POST", "/api/storage-migration", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("member POST status = %d, want 403", w.Code)
	}

	// Owner POST without a configured object store: feature-disabled code,
	// not a generic error, so the UI can tell "nothing to migrate" from a
	// transient failure.
	w = httptest.NewRecorder()
	testHandler.StartStorageMigration(w, newRequest("POST", "/api/storage-migration", nil))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "storage_migration_not_configured") {
		t.Errorf("unconfigured POST status = %d body = %s, want 403 storage_migration_not_configured", w.Code, w.Body.String())
	}
}

// TestStorageMigrationStartStatusAndConcurrentRefusal wires a real migrator
// with a blocking remote and walks the full operator flow: 202 on start, 409
// while the run is in flight, progress visible on GET, clean finish.
func TestStorageMigrationStartStatusAndConcurrentRefusal(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	dir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", dir)
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "http://handler-migrator.test")
	local := storage.NewLocalStorageFromEnv()
	if local == nil {
		t.Fatal("local storage init failed")
	}
	remote := newBlockingRemote()
	t.Cleanup(remote.unblock) // release on early failure so the run goroutine cannot leak

	migrator := &service.StorageMigrator{
		Queries: testHandler.Queries,
		Local:   local,
		Remote:  remote,
		Logger:  slog.Default(),
	}
	testHandler.StorageMigrator = migrator
	t.Cleanup(func() { testHandler.StorageMigrator = nil })

	// Seed one attachment on local disk. Rows other tests leave under
	// /uploads/ cannot be migrated from this test's LOCAL_UPLOAD_DIR — their
	// read fails and they stay untouched — so this run owns its effects.
	ctx := context.Background()
	key := "workspaces/" + testWorkspaceID + "/attachments/start-flow.bin"
	payload := []byte("start flow payload")
	attachmentURL, err := local.Upload(ctx, key, payload, "application/octet-stream", "start-flow.bin")
	if err != nil {
		t.Fatalf("seed local attachment: %v", err)
	}
	var attachmentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO attachment (workspace_id, uploader_type, uploader_id, filename, url, content_type, size_bytes)
		VALUES ($1, 'member', $2, 'start-flow.bin', $3, 'application/octet-stream', $4)
		RETURNING id
	`, testWorkspaceID, testUserID, attachmentURL, len(payload)).Scan(&attachmentID); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM attachment WHERE id = $1`, attachmentID)
	})

	// Start: accepted once.
	w := httptest.NewRecorder()
	testHandler.StartStorageMigration(w, newRequest("POST", "/api/storage-migration", nil))
	if w.Code != http.StatusAccepted {
		t.Fatalf("start status = %d, want 202 (body: %s)", w.Code, w.Body.String())
	}

	// The run must now be in flight long enough for the second start to be
	// refused with 409 rather than stacking a second run.
	select {
	case <-remote.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("migration run never reached the upload")
	}

	w = httptest.NewRecorder()
	testHandler.StartStorageMigration(w, newRequest("POST", "/api/storage-migration", nil))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "storage_migration_running") {
		t.Errorf("second start status = %d body = %s, want 409 storage_migration_running", w.Code, w.Body.String())
	}

	// Status while running: available and running.
	w = httptest.NewRecorder()
	testHandler.GetStorageMigrationStatus(w, newRequest("GET", "/api/storage-migration", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"available":true`) || !strings.Contains(w.Body.String(), `"running":true`) {
		t.Errorf("in-flight GET status = %d body = %s, want 200 available+running", w.Code, w.Body.String())
	}

	remote.unblock()

	deadline := time.Now().Add(10 * time.Second)
	for migrator.Snapshot().FinishedAt == nil {
		if time.Now().After(deadline) {
			t.Fatal("migration run did not finish after the remote was released")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Final state: the seeded attachment now points at the object store and
	// the local file is gone.
	w = httptest.NewRecorder()
	testHandler.GetStorageMigrationStatus(w, newRequest("GET", "/api/storage-migration", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `"running":false`) {
		t.Errorf("final GET status = %d body = %s, want 200 running:false", w.Code, body)
	}

	var gotURL string
	if err := testPool.QueryRow(ctx, `SELECT url FROM attachment WHERE id = $1`, attachmentID).Scan(&gotURL); err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	if want := remote.ObjectURL(key); gotURL != want {
		t.Errorf("attachment url = %q, want %q", gotURL, want)
	}
	if _, err := local.GetReader(ctx, key); err == nil {
		t.Error("local file still readable after migration")
	}
}
