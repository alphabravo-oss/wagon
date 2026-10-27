package jobrunner

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
)

const dummySecret = "DUMMY-not-a-real-secret-123"

func newSecretJob(t *testing.T) (*Handler, int64) {
	t.Helper()
	r := New(setupTestDB(t), t.TempDir())
	job, err := r.CreateJob(context.Background(), "hauler",
		[]string{"store", "add", "chart", "x", "--username", "u", "--password", dummySecret,
			"--token=" + dummySecret, "oci://u:" + dummySecret + "@registry.example/c"},
		map[string]string{"HAULER_REGISTRY_PASSWORD": dummySecret})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	return NewHandler(r, &config.Config{}), job.ID
}

func TestGetJobRedactsSecrets(t *testing.T) {
	h, id := newSecretJob(t)

	for _, tc := range []struct {
		name string
		fn   http.HandlerFunc
		path string
	}{
		{"get", h.GetJob, "/api/jobs/" + strconv.FormatInt(id, 10)},
		{"list", h.ListJobs, "/api/jobs"},
	} {
		w := httptest.NewRecorder()
		tc.fn(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", tc.name, w.Code)
		}
		body := w.Body.String()
		if strings.Contains(body, dummySecret) {
			t.Errorf("%s: response leaks secret: %s", tc.name, body)
		}
		if !strings.Contains(body, "HAULER_REGISTRY_PASSWORD") || !strings.Contains(body, RedactedValue) {
			t.Errorf("%s: expected redacted env key in response: %s", tc.name, body)
		}
	}
}

func TestStreamNoWildcardCORSAndRedacted(t *testing.T) {
	h, id := newSecretJob(t)
	srv := httptest.NewServer(http.HandlerFunc(h.StreamJobLogs))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/jobs/"+strconv.FormatInt(id, 10)+"/stream", nil)
	req.Header.Set("Origin", "http://other.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Logf("close response body: %v", err)
		}
	}()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want none", got)
	}

	sc := bufio.NewScanner(resp.Body)
	var stateEvent bool
	for sc.Scan() {
		line := sc.Text()
		if line == "event: state" {
			stateEvent = true
			continue
		}
		if stateEvent && strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if strings.Contains(data, dummySecret) {
				t.Errorf("state event leaks secret: %s", data)
			}
			var j Job
			if err := json.Unmarshal([]byte(data), &j); err != nil {
				t.Fatalf("decoding state: %v", err)
			}
			if j.EnvOverrides["HAULER_REGISTRY_PASSWORD"] != RedactedValue {
				t.Errorf("env override not redacted: %v", j.EnvOverrides)
			}
			return
		}
	}
	t.Fatal("no state event received")
}

func TestRedactArgs(t *testing.T) {
	in := []string{"login", "--password", "s1", "--password=s2", "--token", "s3", "--username", "bob", "https://bob:s4@h/x", "plain"}
	got := strings.Join(RedactArgs(in), " ")
	for _, s := range []string{"s1", "s2", "s3", "s4"} {
		if strings.Contains(got, s) {
			t.Errorf("RedactArgs leaked %q: %s", s, got)
		}
	}
	for _, keep := range []string{"login", "--username", "bob", "plain", "--password"} {
		if !strings.Contains(got, keep) {
			t.Errorf("RedactArgs dropped %q: %s", keep, got)
		}
	}
	if in[2] != "s1" {
		t.Error("RedactArgs mutated its input")
	}
}

// Cleanup must kill a stuck job's command, not only mark the job failed.
func TestCleanupStaleJobKillsProcess(t *testing.T) {
	r := New(setupTestDB(t), t.TempDir())
	pidFile := filepath.Join(t.TempDir(), "pid")
	job, err := r.CreateJob(context.Background(), "/bin/sh",
		[]string{"-c", "echo $$ > " + pidFile + "; exec sleep 30"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for pid == 0 && time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(b), "\n") {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("job command did not start")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	h := NewHandler(r, &config.Config{})
	w := httptest.NewRecorder()
	h.CleanupStaleJob(w, httptest.NewRequest(http.MethodPost, "/api/jobs/"+strconv.FormatInt(job.ID, 10)+"/cleanup", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}

	// Killed and reaped: signal 0 fails once the process is gone.
	deadline = time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatal("job process still running after cleanup")
		}
		time.Sleep(10 * time.Millisecond)
	}
	end, err := r.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if end.Status != StatusFailed {
		t.Errorf("status %q, want %q", end.Status, StatusFailed)
	}
}

// Cleanup during a prepared job's prepare step (an Add File download) must
// stop it, and the command must never start afterwards, even when prepare
// finishes without an error just as cleanup lands.
func TestCleanupStaleJobStopsPreparedJob(t *testing.T) {
	r := New(setupTestDB(t), t.TempDir())
	marker := filepath.Join(t.TempDir(), "ran")
	preparing := make(chan struct{})
	cleaned := make(chan struct{})
	job, err := r.CreatePreparedJob(context.Background(), "/bin/sh", []string{"-c", "touch " + marker},
		func(ctx context.Context, _ func(string)) error {
			close(preparing)
			<-ctx.Done()
			return nil
		},
		func() { close(cleaned) })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-preparing:
	case <-time.After(3 * time.Second):
		t.Fatal("prepare did not start")
	}

	h := NewHandler(r, &config.Config{})
	w := httptest.NewRecorder()
	h.CleanupStaleJob(w, httptest.NewRequest(http.MethodPost, "/api/jobs/"+strconv.FormatInt(job.ID, 10)+"/cleanup", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}

	select {
	case <-cleaned:
	case <-time.After(3 * time.Second):
		t.Fatal("prepared job did not end after cleanup")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("command ran after cleanup (stat err %v)", err)
	}
	end, err := r.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if end.Status != StatusFailed {
		t.Errorf("status %q, want %q", end.Status, StatusFailed)
	}
}
