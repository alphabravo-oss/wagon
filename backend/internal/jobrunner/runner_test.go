package jobrunner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	f, err := os.CreateTemp("", "testdb-*.db")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Errorf("close f: %v", err)
		}
	}()
	name := f.Name()
	t.Cleanup(func() {
		if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove temp file: %v", err)
		}
	})

	db, err := sql.Open("sqlite", name)
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	// Mirror production Open: SQLite is single-writer. Without this cap, the
	// concurrent log-streaming/status goroutines started by Start race the
	// polling reads and yield transient "database is locked" errors, which made
	// TestGetLogsWithSince flaky (nil GetJob result -> panic) on CI runners.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})

	// Create schema
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
			result TEXT
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
	`)
	if err != nil {
		t.Fatalf("creating schema: %v", err)
	}

	return db
}

func TestCreateJob(t *testing.T) {
	db := setupTestDB(t)
	runner := New(db, t.TempDir())

	ctx := context.Background()
	job, err := runner.CreateJob(ctx, "echo", []string{"hello", "world"}, nil)
	if err != nil {
		t.Fatalf("CreateJob failed: %v", err)
	}

	if job.ID == 0 {
		t.Error("expected non-zero job ID")
	}
	if job.Command != "echo" {
		t.Errorf("expected command 'echo', got %q", job.Command)
	}
	if len(job.Args) != 2 || job.Args[0] != "hello" || job.Args[1] != "world" {
		t.Errorf("unexpected args: %v", job.Args)
	}
	if job.Status != StatusQueued {
		t.Errorf("expected status %q, got %q", StatusQueued, job.Status)
	}
}

func TestGetJob(t *testing.T) {
	db := setupTestDB(t)
	runner := New(db, t.TempDir())

	ctx := context.Background()
	created, err := runner.CreateJob(ctx, "test", []string{"arg1"}, nil)
	if err != nil {
		t.Fatalf("CreateJob failed: %v", err)
	}

	fetched, err := runner.GetJob(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}

	if fetched.ID != created.ID {
		t.Errorf("expected ID %d, got %d", created.ID, fetched.ID)
	}
	if fetched.Command != created.Command {
		t.Errorf("expected command %q, got %q", created.Command, fetched.Command)
	}
}

func TestJobExecutionAndLogCapture(t *testing.T) {
	db := setupTestDB(t)
	runner := New(db, t.TempDir())

	ctx := context.Background()

	// Find echo command path
	echoPath, err := exec.LookPath("echo")
	if err != nil {
		t.Skip("echo command not found")
	}

	job, err := runner.CreateJob(ctx, echoPath, []string{"hello", "world"}, nil)
	if err != nil {
		t.Fatalf("CreateJob failed: %v", err)
	}

	// Start the job
	if err := runner.Start(ctx, job.ID); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for job to complete
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var finalJob *Job
	for {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for job to complete")
		case <-ticker.C:
			finalJob, err = runner.GetJob(ctx, job.ID)
			if err != nil {
				t.Fatalf("GetJob failed: %v", err)
			}
			if finalJob.Status == StatusSucceeded || finalJob.Status == StatusFailed {
				goto done
			}
		}
	}
done:

	if finalJob.Status != StatusSucceeded {
		t.Errorf("expected status %q, got %q", StatusSucceeded, finalJob.Status)
	}
	if finalJob.ExitCode == nil || *finalJob.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %v", finalJob.ExitCode)
	}
	if finalJob.StartedAt == nil {
		t.Error("expected StartedAt to be set")
	}
	if finalJob.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}

	// Get logs
	logs, err := runner.GetLogs(ctx, job.ID, nil)
	if err != nil {
		t.Fatalf("GetLogs failed: %v", err)
	}

	if len(logs) == 0 {
		t.Fatal("expected at least one log entry")
	}

	// Check for expected output
	found := false
	for _, log := range logs {
		if log.Stream == "stdout" && strings.Contains(log.Content, "hello world") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find 'hello world' in stdout logs, got: %v", logs)
	}
}

func TestJobExecutionWithStderr(t *testing.T) {
	db := setupTestDB(t)
	runner := New(db, t.TempDir())

	ctx := context.Background()

	// Use a command that writes to stderr
	// On Linux, we can use 'sh' with a command that writes to stderr
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh command not found")
	}

	job, err := runner.CreateJob(ctx, shPath, []string{"-c", "echo error >&2"}, nil)
	if err != nil {
		t.Fatalf("CreateJob failed: %v", err)
	}

	if err := runner.Start(ctx, job.ID); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for completion
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var finalJob *Job
	for {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for job to complete")
		case <-ticker.C:
			finalJob, err = runner.GetJob(ctx, job.ID)
			if err != nil {
				t.Fatalf("GetJob failed: %v", err)
			}
			if finalJob.Status == StatusSucceeded || finalJob.Status == StatusFailed {
				goto done
			}
		}
	}
done:

	// Get logs
	logs, err := runner.GetLogs(ctx, job.ID, nil)
	if err != nil {
		t.Fatalf("GetLogs failed: %v", err)
	}

	// Check for stderr output
	foundStderr := false
	for _, log := range logs {
		if log.Stream == "stderr" && strings.Contains(log.Content, "error") {
			foundStderr = true
			break
		}
	}
	if !foundStderr {
		t.Errorf("expected to find stderr output, got: %v", logs)
	}
}

func TestJobExecutionFailure(t *testing.T) {
	db := setupTestDB(t)
	runner := New(db, t.TempDir())

	ctx := context.Background()

	// Use a command that will fail
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh command not found")
	}

	job, err := runner.CreateJob(ctx, shPath, []string{"-c", "exit 42"}, nil)
	if err != nil {
		t.Fatalf("CreateJob failed: %v", err)
	}

	if err := runner.Start(ctx, job.ID); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for completion
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var finalJob *Job
	for {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for job to complete")
		case <-ticker.C:
			finalJob, err = runner.GetJob(ctx, job.ID)
			if err != nil {
				t.Fatalf("GetJob failed: %v", err)
			}
			if finalJob.Status == StatusSucceeded || finalJob.Status == StatusFailed {
				goto done
			}
		}
	}
done:

	if finalJob.Status != StatusFailed {
		t.Errorf("expected status %q, got %q", StatusFailed, finalJob.Status)
	}
	if finalJob.ExitCode == nil || *finalJob.ExitCode != 42 {
		t.Errorf("expected exit code 42, got %v", finalJob.ExitCode)
	}
}

func TestListJobs(t *testing.T) {
	db := setupTestDB(t)
	runner := New(db, t.TempDir())

	ctx := context.Background()

	// Create some jobs
	_, _ = runner.CreateJob(ctx, "cmd1", nil, nil)
	_, _ = runner.CreateJob(ctx, "cmd2", nil, nil)
	job3, _ := runner.CreateJob(ctx, "cmd3", nil, nil)

	// List all jobs
	jobs, err := runner.ListJobs(ctx, nil)
	if err != nil {
		t.Fatalf("ListJobs failed: %v", err)
	}

	if len(jobs) != 3 {
		t.Errorf("expected 3 jobs, got %d", len(jobs))
	}

	// List by status
	s := StatusQueued
	queuedJobs, err := runner.ListJobs(ctx, &s)
	if err != nil {
		t.Fatalf("ListJobs with status failed: %v", err)
	}

	if len(queuedJobs) != 3 {
		t.Errorf("expected 3 queued jobs, got %d", len(queuedJobs))
	}

	// Verify IDs are unique
	ids := make(map[int64]bool)
	for _, j := range jobs {
		if ids[j.ID] {
			t.Errorf("duplicate job ID: %d", j.ID)
		}
		ids[j.ID] = true
	}

	// Verify job3 is in the list
	found := false
	for _, j := range jobs {
		if j.ID == job3.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("job3 not found in jobs list")
	}
}

func TestGetLogsWithSince(t *testing.T) {
	db := setupTestDB(t)
	runner := New(db, t.TempDir())

	ctx := context.Background()

	echoPath, err := exec.LookPath("echo")
	if err != nil {
		t.Skip("echo command not found")
	}

	job, err := runner.CreateJob(ctx, echoPath, []string{"test"}, nil)
	if err != nil {
		t.Fatalf("CreateJob failed: %v", err)
	}

	if err := runner.Start(ctx, job.ID); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for completion
	for i := 0; i < 50; i++ {
		finalJob, err := runner.GetJob(ctx, job.ID)
		if err != nil || finalJob == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if finalJob.Status == StatusSucceeded || finalJob.Status == StatusFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Get all logs
	allLogs, err := runner.GetLogs(ctx, job.ID, nil)
	if err != nil {
		t.Fatalf("GetLogs failed: %v", err)
	}

	if len(allLogs) == 0 {
		t.Fatal("expected at least one log entry")
	}

	// Get logs after the first timestamp
	since := allLogs[0].Timestamp
	partialLogs, err := runner.GetLogs(ctx, job.ID, &since)
	if err != nil {
		t.Fatalf("GetLogs with since failed: %v", err)
	}

	// Should have fewer logs (all except possibly ones at exactly the since timestamp)
	if len(partialLogs) > len(allLogs) {
		t.Errorf("expected partial logs (%d) <= all logs (%d)", len(partialLogs), len(allLogs))
	}
}

func TestStreamOutput(t *testing.T) {
	const longLineBytes = 200 * 1024
	capped := strings.Repeat("x", maxLogLineBytes)
	for _, tt := range []struct {
		name    string
		input   string
		readErr error
		want    []string
	}{
		{
			name:  "mixed lines",
			input: "short\n\n" + strings.Repeat("x", longLineBytes) + "\nwindows\r\n\r\nPASSWORD=secret\nfinal",
			want:  []string{"short", "", capped + fmt.Sprintf(" [truncated %d bytes]", longLineBytes-maxLogLineBytes), "windows", "", "PASSWORD=[REDACTED]", "final"},
		},
		{name: "empty"},
		{
			name:  "truncated URL password",
			input: capped[:maxLogLineBytes-len("https://user:secret")] + "https://user:secret@registry.example.com/x extra\n",
			want:  []string{capped[:maxLogLineBytes-len("https://user:secret")] + "https://[REDACTED]:" + fmt.Sprintf(" [truncated %d bytes]", len("@registry.example.com/x extra"))},
		},
		{
			name:  "truncated URL username",
			input: capped[:maxLogLineBytes-len("https://user")] + "https://user:secret@registry.example.com/x\r\n",
			want:  []string{capped[:maxLogLineBytes-len("https://user")] + "https://[REDACTED]" + fmt.Sprintf(" [truncated %d bytes]", len(":secret@registry.example.com/x"))},
		},
		{
			name:  "truncated docker auth",
			input: capped[:maxLogLineBytes-len(`"auth": "abc`)] + `"auth": "abcdef"}`,
			want:  []string{capped[:maxLogLineBytes-len(`"auth": "abc`)] + `"auth": "[REDACTED]` + fmt.Sprintf(" [truncated %d bytes]", len(`def"}`))},
		},
		{name: "full URL with port", input: "https://host:8080/path\n", want: []string{"https://host:8080/path"}},
		{name: "URL at cap", input: capped[:maxLogLineBytes-len("https://host:8080/path")] + "https://host:8080/path\r\n", want: []string{capped[:maxLogLineBytes-len("https://host:8080/path")] + "https://host:8080/path"}},
		{name: "empty line", input: "\n", want: []string{""}},
		{name: "final CR", input: "\r", want: []string{""}},
		{name: "at cap CRLF", input: capped + "\r\n", want: []string{capped}},
		{name: "at cap EOF", input: capped, want: []string{capped}},
		{name: "over cap CRLF", input: capped + "x\r\n", want: []string{capped + " [truncated 1 bytes]"}},
		{name: "over cap EOF", input: capped + "x", want: []string{capped + " [truncated 1 bytes]"}},
		{name: "split CRLF", input: strings.Repeat("x", 4095) + "\r\n", want: []string{strings.Repeat("x", 4095)}},
		{name: "retained CR", input: strings.Repeat("x", maxLogLineBytes-1) + "\rx\n", want: []string{strings.Repeat("x", maxLogLineBytes-1) + "\r [truncated 1 bytes]"}},
		{name: "read error", input: "short\npartial\r", readErr: errors.New("broken stream"), want: []string{"short", "partial", "[stream error: broken stream]"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := New(setupTestDB(t), t.TempDir())
			ctx := context.Background()
			job, err := runner.CreateJob(ctx, "test", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			var reader io.Reader = strings.NewReader(tt.input)
			if tt.readErr != nil {
				reader = io.MultiReader(reader, streamErrorReader{tt.readErr})
			}
			runner.streamOutput(ctx, job.ID, reader, "stdout")
			logs, err := runner.GetLogs(ctx, job.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(logs) != len(tt.want) {
				t.Fatalf("got %d lines, want %d", len(logs), len(tt.want))
			}
			for i, want := range tt.want {
				if logs[i].Stream != "stdout" || logs[i].Content != want {
					t.Errorf("line %d mismatch: got stream %q and %d bytes, want stdout and %d bytes", i, logs[i].Stream, len(logs[i].Content), len(want))
				}
			}
		})
	}
}

func TestRedactTruncatedTail(t *testing.T) {
	for _, tt := range []struct {
		name  string
		input string
		want  string
	}{
		{"password", "prefix https://user:secret", "prefix https://[REDACTED]:"},
		{"empty password", "https://user:", "https://[REDACTED]:"},
		{"username", "https://user", "https://[REDACTED]"},
		{"auth", `{"auth": "abc`, `{"auth": "[REDACTED]`},
		{"empty auth", `"auth": "`, `"auth": "[REDACTED]`},
		{"auth whitespace", "\"auth\":\t\"abc", "\"auth\":\t\"[REDACTED]"},
		{"plain tail", "ordinary log text", "ordinary log text"},
		{"terminated URL", "https://[REDACTED]:@registry.example.com/x", "https://[REDACTED]:@registry.example.com/x"},
		{"terminated auth", `"auth": "[REDACTED]"`, `"auth": "[REDACTED]"`},
		{"URL before tail", "https://host/path more text", "https://host/path more text"},
		{"empty", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactTruncatedTail(tt.input); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

type streamErrorReader struct{ err error }

func (r streamErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestJobExecutionWithLongLine(t *testing.T) {
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	runner := New(setupTestDB(t), t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	job, err := runner.CreateJob(ctx, "/bin/sh", []string{"-c", "printf '%0204800d\\nnormal\\n' 0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for long-output job to succeed")
		case <-ticker.C:
			current, err := runner.GetJob(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Status == StatusFailed {
				t.Fatal("job failed")
			}
			if current.Status != StatusSucceeded {
				continue
			}
			logs, err := runner.GetLogs(ctx, job.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Repeat("0", maxLogLineBytes) + fmt.Sprintf(" [truncated %d bytes]", 204800-maxLogLineBytes)
			if len(logs) != 2 || logs[0].Content != want || logs[1].Content != "normal" {
				t.Fatal("expected truncated output followed by normal line")
			}
			return
		}
	}
}

func TestCreatePreparedJob(t *testing.T) {
	db := setupTestDB(t)
	runner := New(db, t.TempDir())
	ctx := context.Background()
	echoPath, err := exec.LookPath("echo")
	if err != nil {
		t.Skip("echo command not found")
	}

	for _, tc := range []struct {
		name       string
		prepareErr error
		wantStatus JobStatus
		wantLog    string
	}{
		{"prepare ok", nil, StatusSucceeded, "ran-command"},
		{"prepare fails", errors.New("download failed: blocked"), StatusFailed, "download failed: blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cleaned := make(chan struct{})
			job, err := runner.CreatePreparedJob(ctx, echoPath, []string{"ran-command"},
				func(_ context.Context, logf func(string)) error {
					logf("preparing")
					return tc.prepareErr
				},
				func() { close(cleaned) })
			if err != nil {
				t.Fatalf("CreatePreparedJob: %v", err)
			}
			if job.Status != StatusRunning {
				t.Errorf("new job status %s, want running so the job processor skips it", job.Status)
			}
			select {
			case <-cleaned:
			case <-time.After(5 * time.Second):
				t.Fatal("cleanup did not run")
			}
			got, err := runner.GetJob(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("status %s, want %s", got.Status, tc.wantStatus)
			}
			logs, _ := runner.GetLogs(ctx, job.ID, nil)
			var all []string
			for _, l := range logs {
				all = append(all, l.Content)
			}
			if joined := strings.Join(all, "\n"); !strings.Contains(joined, "preparing") || !strings.Contains(joined, tc.wantLog) {
				t.Errorf("logs %q, want %q and %q", joined, "preparing", tc.wantLog)
			}
		})
	}
}
