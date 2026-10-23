package jobrunner

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
