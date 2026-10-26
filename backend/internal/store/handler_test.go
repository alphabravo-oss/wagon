package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
	"github.com/alphabravo-oss/wagon/backend/internal/hauls"
	"github.com/alphabravo-oss/wagon/backend/internal/jobrunner"
	"github.com/alphabravo-oss/wagon/backend/internal/safefetch"
)

func setupTestHandler(t *testing.T) (*Handler, *sql.DB) {
	t.Helper()

	dataDir := t.TempDir()
	name := filepath.Join(dataDir, "test.db")

	// The test polls jobs while background goroutines write them; without a
	// busy timeout those writes can fail with SQLITE_BUSY and be dropped.
	db, err := sql.Open("sqlite", "file:"+name+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})

	// Create the subset of schema the store handler relies on.
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			command TEXT NOT NULL,
			args TEXT,
			env_overrides TEXT,
			status TEXT NOT NULL DEFAULT 'queued',
			exit_code INTEGER,
			started_at DATETIME,
			completed_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			result TEXT,
			haul_id INTEGER
		);

		CREATE TABLE IF NOT EXISTS job_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			job_id INTEGER NOT NULL,
			stream TEXT NOT NULL,
			content TEXT NOT NULL,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE
		);

		CREATE INDEX IF NOT EXISTS idx_job_logs_job_id ON job_logs(job_id, timestamp);

		CREATE TABLE IF NOT EXISTS hauls (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			slug TEXT NOT NULL UNIQUE,
			description TEXT,
			store_dir TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS store_contents (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			haul_id INTEGER,
			content_type TEXT NOT NULL,
			name TEXT NOT NULL,
			digest TEXT,
			source_haul TEXT,
			loaded_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(haul_id, content_type, name, digest)
		);
	`)
	if err != nil {
		t.Fatalf("creating schema: %v", err)
	}

	cfg := &config.Config{
		HaulerTempDir: filepath.Join(dataDir, "tmp"),
		DataDir:       dataDir,
	}

	runner := jobrunner.New(db, dataDir)
	haulSvc := hauls.NewService(db, cfg)
	if _, err := haulSvc.EnsureDefault(context.Background()); err != nil {
		t.Fatalf("ensuring default haul: %v", err)
	}
	handler := NewHandler(runner, cfg, haulSvc)
	// Tests never reach the network: names do not resolve unless a test swaps
	// in its own Fetcher.
	handler.Fetcher = safefetch.New(safefetch.Options{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return nil, errors.New("no DNS in tests")
		},
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("no network in tests")
		},
	})
	// Add File url jobs download in the background; let them end before the
	// temp folder and database go away.
	t.Cleanup(func() { waitJobsDone(t, db) })

	return handler, db
}

// waitJobsDone waits until no job is running.
func waitJobsDone(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM jobs WHERE status = 'running'").Scan(&n); err != nil || n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("jobs still running after 10s")
}

// defaultStoreDir returns the store directory of the default haul created by
// setupTestHandler, used to assert the trailing "--store <dir>" args.
func defaultStoreDir(t *testing.T, h *Handler) string {
	t.Helper()
	haul, err := h.Hauls.EnsureDefault(context.Background())
	if err != nil {
		t.Fatalf("resolving default haul: %v", err)
	}
	return haul.StoreDir
}

func TestPositionalArgs_RejectLeadingDash(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		body     string
	}{
		{"add-image", "add-image", `{"imageRef":"--help"}`},
		{"add-chart", "add-chart", `{"name":"-v"}`},
		{"add-file-path", "add-file", `{"filePath":"--store=/etc"}`},
		{"add-file-url", "add-file", `{"url":"-x"}`},
		{"remove", "remove", `{"match":"--force"}`},
		{"extract", "extract", `{"artifactRef":"-o"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, db := setupTestHandler(t)
			mux := http.NewServeMux()
			handler.RegisterRoutes(mux)
			r := httptest.NewRequest(http.MethodPost, "/api/store/"+tt.endpoint, strings.NewReader(tt.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			mux.ServeHTTP(w, r)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
			}
			if !strings.Contains(w.Body.String(), "must not start with '-'") {
				t.Errorf("expected leading-dash error, got: %s", w.Body.String())
			}
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&count); err != nil {
				t.Fatalf("counting jobs: %v", err)
			}
			if count != 0 {
				t.Errorf("expected no jobs, got %d", count)
			}
		})
	}
}

func TestPositionalArgs_AllowInnerDash(t *testing.T) {
	handler, _ := setupTestHandler(t)
	r := httptest.NewRequest(http.MethodPost, "/api/store/add-image", strings.NewReader(`{"imageRef":"docker.io/library/my-app:1-alpine"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.AddImage(w, r)

	if w.Code == http.StatusBadRequest {
		t.Errorf("unexpected status %d: %s", w.Code, w.Body.String())
	}
}

func TestCopyHandler_InvalidMethod(t *testing.T) {
	handler, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/store/copy", nil)
	w := httptest.NewRecorder()

	handler.Copy(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}
}

func TestCopyHandler_MissingTarget(t *testing.T) {
	handler, _ := setupTestHandler(t)

	req := CopyRequest{}
	body, _ := json.Marshal(req)

	r := httptest.NewRequest(http.MethodPost, "/api/store/copy", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.Copy(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestCopyHandler_InvalidTargetFormat(t *testing.T) {
	handler, _ := setupTestHandler(t)

	req := CopyRequest{Target: "invalid-target"}
	body, _ := json.Marshal(req)

	r := httptest.NewRequest(http.MethodPost, "/api/store/copy", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.Copy(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}

	respBody := w.Body.String()
	if !strings.Contains(respBody, "must start with registry:// or dir://") {
		t.Errorf("expected error message about target format, got: %s", respBody)
	}
}

func TestCopyHandler_ValidRegistryTarget(t *testing.T) {
	handler, db := setupTestHandler(t)

	req := CopyRequest{
		Target:    "registry://docker.io/my-org",
		Insecure:  true,
		PlainHTTP: false,
		Only:      "sig",
	}
	body, _ := json.Marshal(req)

	r := httptest.NewRequest(http.MethodPost, "/api/store/copy", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.Copy(w, r)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected status %d, got %d: %s", http.StatusAccepted, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	jobID, ok := resp["jobId"].(float64)
	if !ok || jobID == 0 {
		t.Error("expected non-zero jobId in response")
	}

	// Verify job was created with correct args
	ctx := context.Background()
	job, err := handler.JobRunner.GetJob(ctx, int64(jobID))
	if err != nil {
		t.Fatalf("getting job: %v", err)
	}

	if job.Command != "hauler" {
		t.Errorf("expected command 'hauler', got %q", job.Command)
	}

	expectedArgs := []string{"store", "copy", "registry://docker.io/my-org", "--insecure", "--only", "sig", "--store", defaultStoreDir(t, handler)}
	if len(job.Args) != len(expectedArgs) {
		t.Errorf("expected %d args, got %d (%v)", len(expectedArgs), len(job.Args), job.Args)
	} else {
		for i, arg := range expectedArgs {
			if job.Args[i] != arg {
				t.Errorf("arg %d: expected %q, got %q", i, arg, job.Args[i])
			}
		}
	}

	// Clean up job
	if _, err := db.Exec("DELETE FROM jobs WHERE id = ?", job.ID); err != nil {
		t.Errorf("clean up job: %v", err)
	}
}

func TestCopyHandler_ValidDirTarget(t *testing.T) {
	handler, db := setupTestHandler(t)

	req := CopyRequest{
		Target: "dir://export",
	}
	body, _ := json.Marshal(req)

	r := httptest.NewRequest(http.MethodPost, "/api/store/copy", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.Copy(w, r)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected status %d, got %d: %s", http.StatusAccepted, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	jobID, ok := resp["jobId"].(float64)
	if !ok || jobID == 0 {
		t.Error("expected non-zero jobId in response")
	}

	// Verify job was created with correct args (no insecure/plain-http for dir)
	ctx := context.Background()
	job, err := handler.JobRunner.GetJob(ctx, int64(jobID))
	if err != nil {
		t.Fatalf("getting job: %v", err)
	}

	// A relative dir:// target is taken from the haul's folder.
	export := filepath.Join(realDir(t, filepath.Dir(defaultStoreDir(t, handler))), "export")
	expectedArgs := []string{"store", "copy", "dir://" + export, "--store", defaultStoreDir(t, handler)}
	if len(job.Args) != len(expectedArgs) {
		t.Errorf("expected %d args, got %d (%v)", len(expectedArgs), len(job.Args), job.Args)
	} else {
		for i, arg := range expectedArgs {
			if job.Args[i] != arg {
				t.Errorf("arg %d: expected %q, got %q", i, arg, job.Args[i])
			}
		}
	}

	// Clean up job
	if _, err := db.Exec("DELETE FROM jobs WHERE id = ?", job.ID); err != nil {
		t.Errorf("clean up job: %v", err)
	}
}

func TestCopyHandler_PlainHTTPFlag(t *testing.T) {
	handler, db := setupTestHandler(t)

	req := CopyRequest{
		Target:    "registry://localhost:5000/my-repo",
		PlainHTTP: true,
	}
	body, _ := json.Marshal(req)

	r := httptest.NewRequest(http.MethodPost, "/api/store/copy", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.Copy(w, r)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected status %d, got %d: %s", http.StatusAccepted, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	jobID, _ := resp["jobId"].(float64)

	// Verify --plain-http flag was added
	ctx := context.Background()
	job, err := handler.JobRunner.GetJob(ctx, int64(jobID))
	if err != nil {
		t.Fatalf("getting job: %v", err)
	}

	hasPlainHTTP := false
	for _, arg := range job.Args {
		if arg == "--plain-http" {
			hasPlainHTTP = true
			break
		}
	}
	if !hasPlainHTTP {
		t.Error("expected --plain-http flag in args")
	}

	// Clean up job
	if _, err := db.Exec("DELETE FROM jobs WHERE id = ?", job.ID); err != nil {
		t.Errorf("clean up job: %v", err)
	}
}

func TestCopyHandler_OnlyAttestations(t *testing.T) {
	handler, db := setupTestHandler(t)

	req := CopyRequest{
		Target: "registry://docker.io/my-org",
		Only:   "att",
	}
	body, _ := json.Marshal(req)

	r := httptest.NewRequest(http.MethodPost, "/api/store/copy", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.Copy(w, r)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected status %d, got %d: %s", http.StatusAccepted, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	jobID, _ := resp["jobId"].(float64)

	// Verify --only att flag was added
	ctx := context.Background()
	job, err := handler.JobRunner.GetJob(ctx, int64(jobID))
	if err != nil {
		t.Fatalf("getting job: %v", err)
	}

	hasOnlyAtt := false
	for i, arg := range job.Args {
		if arg == "--only" && i+1 < len(job.Args) && job.Args[i+1] == "att" {
			hasOnlyAtt = true
			break
		}
	}
	if !hasOnlyAtt {
		t.Error("expected --only att flag in args")
	}

	// Clean up job
	if _, err := db.Exec("DELETE FROM jobs WHERE id = ?", job.ID); err != nil {
		t.Errorf("clean up job: %v", err)
	}
}
