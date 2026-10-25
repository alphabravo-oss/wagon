package store

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// Hauler ignores --username/--password on `store add chart`, and anything in
// argv is readable by other local processes. Credentials sent by an old client
// must be dropped before the job is built.
func TestAddChartNeverPutsCredentialsInArgv(t *testing.T) {
	handler, _ := setupTestHandler(t)

	const user = "chart-user-not-real"
	const secret = "DUMMY-chart-password-not-real"
	body := `{"name":"mychart","repoUrl":"https://charts.example.com","version":"1.2.3",` +
		`"username":"` + user + `","password":"` + secret + `"}`

	r := httptest.NewRequest(http.MethodPost, "/api/store/add-chart", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.AddChart(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d: %s", http.StatusAccepted, w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	jobID, ok := resp["jobId"].(float64)
	if !ok || jobID == 0 {
		t.Fatalf("expected non-zero jobId in response: %v", resp)
	}

	// Runner.GetJob returns the unredacted args that are passed to exec.
	job, err := handler.JobRunner.GetJob(context.Background(), int64(jobID))
	if err != nil {
		t.Fatalf("getting job: %v", err)
	}

	want := []string{"store", "add", "chart", "mychart", "--repo", "https://charts.example.com",
		"--version", "1.2.3", "--store", defaultStoreDir(t, handler)}
	if !slices.Equal(job.Args, want) {
		t.Errorf("args = %v, want %v", job.Args, want)
	}
	for _, a := range job.Args {
		if strings.HasPrefix(a, "--username") || strings.HasPrefix(a, "--password") ||
			strings.Contains(a, user) || strings.Contains(a, secret) {
			t.Errorf("credential in argv: %q (args %v)", a, job.Args)
		}
	}
	if len(job.EnvOverrides) != 0 {
		t.Errorf("expected no env overrides, got keys for %d entries", len(job.EnvOverrides))
	}
}
