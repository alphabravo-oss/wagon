package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alphabravo-oss/wagon/backend/internal/auth"
	"github.com/alphabravo-oss/wagon/backend/internal/config"
	"github.com/alphabravo-oss/wagon/backend/internal/jobrunner"
	"github.com/alphabravo-oss/wagon/backend/internal/sqlite"
)

// newTestServer wires the real job routes behind the production middleware
// chain (secureHandler) with a UI password configured.
func newTestServer(t *testing.T) (*httptest.Server, *auth.Manager, *jobrunner.Runner) {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cfg := &config.Config{DataDir: dir, UIPassword: "test-password"}
	runner := jobrunner.New(db.DB, dir)
	am := auth.NewManager(db.DB, cfg)

	mux := http.NewServeMux()
	auth.NewHandler(am).RegisterRoutes(mux)
	registerJobRoutes(mux, jobrunner.NewHandler(runner, cfg))

	srv := httptest.NewServer(secureHandler(mux, am, cfg))
	t.Cleanup(srv.Close)
	return srv, am, runner
}

func do(t *testing.T, method, url, body string, hdr map[string]string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestDefaultConfigRefusesToStartWithoutPassword(t *testing.T) {
	cfg := &config.Config{} // what an empty HAULER_UI_PASSWORD produces
	if err := cfg.Validate(); err == nil {
		t.Fatal("empty password without explicit insecure opt-out must be rejected")
	}
}

func TestJobCreationRequiresAuthAndIsNotGeneric(t *testing.T) {
	srv, am, _ := newTestServer(t)
	jobBody := `{"command":"true","args":[]}`
	jsonHdr := map[string]string{"Content-Type": "application/json"}

	if resp := do(t, "POST", srv.URL+"/api/jobs", jobBody, jsonHdr); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated POST /api/jobs = %d, want 401", resp.StatusCode)
	}
	if resp := do(t, "GET", srv.URL+"/api/jobs", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET /api/jobs = %d, want 401", resp.StatusCode)
	}

	token, _, err := am.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	authed := map[string]string{"Content-Type": "application/json", "Cookie": "haulerci_session=" + token}
	if resp := do(t, "POST", srv.URL+"/api/jobs", jobBody, authed); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("authenticated POST /api/jobs = %d, want 405 (no generic command route)", resp.StatusCode)
	}
	if resp := do(t, "GET", srv.URL+"/api/jobs", "", authed); resp.StatusCode != http.StatusOK {
		t.Errorf("authenticated GET /api/jobs = %d, want 200", resp.StatusCode)
	}
}

func TestCrossOriginStateChangeRejected(t *testing.T) {
	srv, am, _ := newTestServer(t)
	token, _, _ := am.CreateSession()
	cookie := "haulerci_session=" + token

	// Even with a valid session, a foreign Origin cannot change state.
	resp := do(t, "DELETE", srv.URL+"/api/jobs", "", map[string]string{"Cookie": cookie, "Origin": "http://evil.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin DELETE /api/jobs = %d, want 403", resp.StatusCode)
	}
	resp = do(t, "POST", srv.URL+"/api/auth/login", `{"password":"test-password"}`,
		map[string]string{"Content-Type": "text/plain", "Origin": "http://evil.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin text/plain login = %d, want 403", resp.StatusCode)
	}
	// Same-origin login still works.
	resp = do(t, "POST", srv.URL+"/api/auth/login", `{"password":"test-password"}`,
		map[string]string{"Content-Type": "application/json", "Origin": srv.URL})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("same-origin login = %d, want 200", resp.StatusCode)
	}
}

func TestNoWildcardCORS(t *testing.T) {
	srv, am, runner := newTestServer(t)
	token, _, _ := am.CreateSession()
	job, err := runner.CreateJob(context.Background(), "true", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	hdr := map[string]string{"Cookie": "haulerci_session=" + token, "Origin": "http://evil.example"}
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/jobs/" + strconv.FormatInt(job.ID, 10) + "/stream"},
		{"OPTIONS", "/api/auth/login"},
		{"GET", "/api/jobs"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		req, _ := http.NewRequestWithContext(ctx, tc.method, srv.URL+tc.path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s %s: Access-Control-Allow-Origin = %q, want none", tc.method, tc.path, got)
		}
		resp.Body.Close()
		cancel()
	}
}
