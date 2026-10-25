package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
	"github.com/alphabravo-oss/wagon/backend/internal/jobrunner"
	"github.com/alphabravo-oss/wagon/backend/internal/sqlite"
)

// newTestHandler builds a Handler backed by a real jobrunner.Runner with a
// migrated on-disk SQLite database in a temp directory.
func newTestHandler(t *testing.T, cfg *config.Config) (*Handler, *jobrunner.Runner) {
	t.Helper()

	db, err := sqlite.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runner := jobrunner.New(db.DB, t.TempDir())
	return NewHandler(runner, cfg), runner
}

// waitForJobSettled polls until the job leaves the queued/running state so the
// background Start goroutine (which execs the missing hauler binary and fails)
// finishes writing before the test's DB is closed. This keeps the async work
// from racing test cleanup; it does not assert job success.
func waitForJobSettled(t *testing.T, runner *jobrunner.Runner, jobID int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := runner.GetJob(context.Background(), jobID)
		if err == nil && job.Status != jobrunner.StatusQueued && job.Status != jobrunner.StatusRunning {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Not fatal: the job row still exists regardless of the exec outcome.
}

func TestLoginCreatesJob(t *testing.T) {
	h, runner := newTestHandler(t, &config.Config{DockerAuthPath: "/data/.docker/config.json"})

	body, _ := json.Marshal(LoginRequest{
		Registry: "registry.example.com",
		Username: "alice",
		Password: "s3cret",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/registry/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.Login(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d (body: %s)", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v (body: %s)", err, rec.Body.String())
	}

	rawID, ok := resp["jobId"]
	if !ok {
		t.Fatalf("response missing jobId: %v", resp)
	}
	jobID := int64(rawID.(float64))
	if jobID == 0 {
		t.Fatalf("expected non-zero jobId, got %v", rawID)
	}
	if resp["registry"] != "registry.example.com" {
		t.Errorf("expected registry echoed back, got %v", resp["registry"])
	}
	if resp["username"] != "alice" {
		t.Errorf("expected username echoed back, got %v", resp["username"])
	}

	// A job row must exist and reference the hauler login command.
	job, err := runner.GetJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("GetJob(%d) failed: %v", jobID, err)
	}
	if job.Command != "hauler" {
		t.Errorf("expected command %q, got %q", "hauler", job.Command)
	}
	wantArgs := []string{"login", "registry.example.com", "-u", "alice", "--password-stdin"}
	if strings.Join(job.Args, " ") != strings.Join(wantArgs, " ") {
		t.Errorf("args %q, want %q", job.Args, wantArgs)
	}
	if len(job.EnvOverrides) != 0 {
		t.Errorf("expected no env overrides, got keys %v", len(job.EnvOverrides))
	}
	if resp["message"] != loginSavedMessage || !strings.Contains(loginSavedMessage, "without checking") {
		t.Errorf("message %q should say the login was saved, not verified", resp["message"])
	}

	jobs, err := runner.ListJobs(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListJobs failed: %v", err)
	}
	if len(jobs) != 1 {
		t.Errorf("expected exactly 1 job, got %d", len(jobs))
	}

	waitForJobSettled(t, runner, jobID)
}

func TestLoginEmptyBodyIsBadRequest(t *testing.T) {
	h, runner := newTestHandler(t, &config.Config{})

	req := httptest.NewRequest(http.MethodPost, "/api/registry/login", bytes.NewReader(nil))
	rec := httptest.NewRecorder()

	h.Login(rec, req)

	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("expected 4xx for empty body, got %d", rec.Code)
	}

	jobs, err := runner.ListJobs(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListJobs failed: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("expected no jobs created on invalid request, got %d", len(jobs))
	}
}

func TestLoginMissingRegistryIsBadRequest(t *testing.T) {
	h, runner := newTestHandler(t, &config.Config{})

	// Valid JSON but missing the required registry field.
	body, _ := json.Marshal(LoginRequest{Username: "alice", Password: "s3cret"})
	req := httptest.NewRequest(http.MethodPost, "/api/registry/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.Login(rec, req)

	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("expected 4xx for missing registry, got %d", rec.Code)
	}

	jobs, err := runner.ListJobs(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListJobs failed: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("expected no jobs created, got %d", len(jobs))
	}
}

func TestLogoutEnqueuesJob(t *testing.T) {
	h, runner := newTestHandler(t, &config.Config{})

	body, _ := json.Marshal(LogoutRequest{Registry: "registry.example.com"})
	req := httptest.NewRequest(http.MethodPost, "/api/registry/logout", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.Logout(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d (body: %s)", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v (body: %s)", err, rec.Body.String())
	}
	rawID, ok := resp["jobId"]
	if !ok {
		t.Fatalf("response missing jobId: %v", resp)
	}
	jobID := int64(rawID.(float64))
	if jobID == 0 {
		t.Fatalf("expected non-zero jobId, got %v", rawID)
	}
	if resp["registry"] != "registry.example.com" {
		t.Errorf("expected registry echoed back, got %v", resp["registry"])
	}

	job, err := runner.GetJob(context.Background(), jobID)
	if err != nil {
		t.Fatalf("GetJob(%d) failed: %v", jobID, err)
	}
	if job.Command != "hauler" {
		t.Errorf("expected command %q, got %q", "hauler", job.Command)
	}
	if len(job.Args) != 2 || job.Args[0] != "logout" || job.Args[1] != "registry.example.com" {
		t.Errorf("unexpected args: %v", job.Args)
	}

	waitForJobSettled(t, runner, jobID)
}

func TestLogoutEmptyBodyIsBadRequest(t *testing.T) {
	h, runner := newTestHandler(t, &config.Config{})

	req := httptest.NewRequest(http.MethodPost, "/api/registry/logout", bytes.NewReader(nil))
	rec := httptest.NewRecorder()

	h.Logout(rec, req)

	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("expected 4xx for empty body, got %d", rec.Code)
	}

	jobs, err := runner.ListJobs(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListJobs failed: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("expected no jobs created, got %d", len(jobs))
	}
}

func TestInfoReturnsDockerAuthPathAndFriendlyDisplayPath(t *testing.T) {
	cfg := &config.Config{
		DockerAuthPath: "/data/.docker/config.json",
		HaulerDir:      "/data",
	}
	h, _ := newTestHandler(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/registry/info", nil)
	rec := httptest.NewRecorder()

	h.Info(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	var info map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decoding response: %v (body: %s)", err, rec.Body.String())
	}

	if info["dockerAuthPath"] != "/data/.docker/config.json" {
		t.Errorf("expected dockerAuthPath %q, got %v", "/data/.docker/config.json", info["dockerAuthPath"])
	}
	if info["homeDir"] != "/data" {
		t.Errorf("expected homeDir %q, got %v", "/data", info["homeDir"])
	}
	display, ok := info["displayPath"].(string)
	if !ok {
		t.Fatalf("expected displayPath string, got %v", info["displayPath"])
	}
	if !strings.Contains(display, "~/.docker/config.json") || !strings.Contains(display, "/data/.docker/config.json") {
		t.Errorf("expected friendly mapped displayPath, got %q", display)
	}
}

func TestInfoDisplayPathUnmappedWhenNotContainerPath(t *testing.T) {
	cfg := &config.Config{DockerAuthPath: "/home/user/.docker/config.json"}
	h, _ := newTestHandler(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/registry/info", nil)
	rec := httptest.NewRecorder()

	h.Info(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var info map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	// When the path is not the container-mapped one, displayPath equals the raw path.
	if info["displayPath"] != "/home/user/.docker/config.json" {
		t.Errorf("expected unmapped displayPath, got %v", info["displayPath"])
	}
}

// Login hands hauler the password on stdin only: a fake hauler on PATH records
// its argv, env and stdin, and the password must show up in stdin and nowhere
// else, including the job row and job log.
func TestLoginSendsPasswordOnStdinOnly(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + bin + "/argv\n" +
		"env > " + bin + "/env\n" +
		"cat > " + bin + "/stdin\n" +
		"echo fake hauler login done\n"
	if err := os.WriteFile(filepath.Join(bin, "hauler"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	h, runner := newTestHandler(t, &config.Config{})
	const password = "card61-not-a-real-password"
	body := `{"registry":"registry.example.com","username":"alice","password":"` + password + `"}`
	rec := httptest.NewRecorder()
	h.Login(rec, httptest.NewRequest(http.MethodPost, "/api/registry/login", strings.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	var resp struct {
		JobID int64 `json:"jobId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	waitForJobSettled(t, runner, resp.JobID)
	job, err := runner.GetJob(context.Background(), resp.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobrunner.StatusSucceeded {
		t.Fatalf("job %s", job.Status)
	}

	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(bin, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := read("stdin"); got != password {
		t.Errorf("hauler stdin is not the password (%d bytes)", len(got))
	}
	if got, want := read("argv"), "login\nregistry.example.com\n-u\nalice\n--password-stdin\n"; got != want {
		t.Errorf("argv %q, want %q", got, want)
	}
	if strings.Contains(read("argv"), password) || strings.Contains(read("env"), password) {
		t.Error("password reached hauler's argv or env")
	}
	logs, err := runner.GetLogs(context.Background(), resp.JobID, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range logs {
		if strings.Contains(l.Content, password) {
			t.Error("password in job log")
		}
	}
	if raw, _ := json.Marshal(job); strings.Contains(string(raw), password) {
		t.Error("password stored with the job")
	}
}

// A registry that starts with '-' would be read by hauler as a flag.
func TestLoginRejectsLeadingDashRegistry(t *testing.T) {
	h, runner := newTestHandler(t, &config.Config{})
	rec := httptest.NewRecorder()
	body := `{"registry":"--password=x","username":"u","password":"p"}`
	h.Login(rec, httptest.NewRequest(http.MethodPost, "/api/registry/login", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "must not start with '-'") {
		t.Errorf("status %d body %q, want 400 leading-dash error", rec.Code, rec.Body.String())
	}
	if jobs, _ := runner.ListJobs(context.Background(), nil); len(jobs) != 0 {
		t.Errorf("expected no jobs, got %d", len(jobs))
	}
}
