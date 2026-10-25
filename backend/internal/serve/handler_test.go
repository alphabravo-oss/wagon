package serve

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
	"github.com/alphabravo-oss/wagon/backend/internal/hauls"
	"github.com/alphabravo-oss/wagon/backend/internal/sqlite"
)

// testEnv bundles the pieces a handler test needs: a migrated DB, a haul
// service, a config rooted at a temp dir, the handler under test, and the
// default haul created so resolveHaul has something to return.
type testEnv struct {
	cfg     *config.Config
	db      *sqlite.DB
	svc     *hauls.Service
	handler *Handler
	haul    *hauls.Haul
}

// newTestEnv builds a fully-migrated, isolated environment. Everything lives
// under t.TempDir() so no real /data is touched and nothing needs cleanup.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}

	db, err := sqlite.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	svc := hauls.NewService(db.DB, cfg)
	haul, err := svc.EnsureDefault(context.Background())
	if err != nil {
		t.Fatalf("ensure default haul: %v", err)
	}

	h := NewHandler(cfg, db.DB, svc)

	return &testEnv{cfg: cfg, db: db, svc: svc, handler: h, haul: haul}
}

// seedProcess inserts a serve_processes row directly, bypassing the exec path
// entirely. Returns the row's pid.
func (e *testEnv) seedProcess(t *testing.T, serveType string, pid, port int, status string, haulID int64) {
	t.Helper()
	_, err := e.db.Exec(`
		INSERT INTO serve_processes (serve_type, pid, port, args, status, haul_id)
		VALUES (?, ?, ?, ?, ?, ?)`,
		serveType, pid, port, `{"port":`+itoa(port)+`}`, status, haulID)
	if err != nil {
		t.Fatalf("seed process: %v", err)
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// --- resolveHaul ---------------------------------------------------------

func TestResolveHaul_DefaultWhenIDNonPositive(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()

	for _, id := range []int64{0, -1} {
		got, err := e.handler.resolveHaul(ctx, id)
		if err != nil {
			t.Fatalf("resolveHaul(%d) error: %v", id, err)
		}
		if got.ID != e.haul.ID {
			t.Errorf("resolveHaul(%d) = haul %d, want default haul %d", id, got.ID, e.haul.ID)
		}
	}
}

func TestResolveHaul_SpecificByID(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()

	// Create a second haul so "specific by id" is meaningfully different from
	// the default (first) haul.
	second, err := e.svc.Create(ctx, "Second", "another workspace")
	if err != nil {
		t.Fatalf("create second haul: %v", err)
	}

	got, err := e.handler.resolveHaul(ctx, second.ID)
	if err != nil {
		t.Fatalf("resolveHaul(%d) error: %v", second.ID, err)
	}
	if got.ID != second.ID {
		t.Errorf("resolveHaul(%d) = haul %d, want %d", second.ID, got.ID, second.ID)
	}
}

func TestResolveHaul_UnknownIDErrors(t *testing.T) {
	e := newTestEnv(t)

	if _, err := e.handler.resolveHaul(context.Background(), 999999); err == nil {
		t.Fatal("resolveHaul(unknown id) = nil error, want error")
	}
}

// --- portInUse -----------------------------------------------------------

// NOTE ON IMPLEMENTATION: the real portInUse does NOT probe the network. It
// queries serve_processes for a row with the given port and status='running'.
// The task described a network-bind-based check, but the code under test is
// DB-backed, so these assertions exercise the actual logic. We still bind a
// real listener to obtain a genuinely-free port number and demonstrate that a
// bound-but-unrecorded port reports false (proving the check is DB-based, not
// network-based).
func TestPortInUse_DBBacked(t *testing.T) {
	e := newTestEnv(t)

	// Obtain a free port via :0, then close it so it's definitely free.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	freePort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	// No DB row -> not in use (even though nothing else is recorded).
	if e.handler.portInUse(freePort) {
		t.Errorf("portInUse(%d) = true for a free/unrecorded port, want false", freePort)
	}

	// A running row makes the port "in use".
	e.seedProcess(t, "registry", 4242, 5000, "running", e.haul.ID)
	if !e.handler.portInUse(5000) {
		t.Error("portInUse(5000) = false with a running row, want true")
	}

	// A stopped row does NOT count as in use.
	e.seedProcess(t, "registry", 4243, 6001, "stopped", e.haul.ID)
	if e.handler.portInUse(6001) {
		t.Error("portInUse(6001) = true with only a stopped row, want false")
	}
}

// TestPortInUse_BoundListenerIgnored documents that a live network listener on
// a port has no effect on portInUse, since the check is DB-based.
func TestPortInUse_BoundListenerIgnored(t *testing.T) {
	e := newTestEnv(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() {
		if err := ln.Close(); err != nil {
			t.Errorf("close ln: %v", err)
		}
	}()
	boundPort := ln.Addr().(*net.TCPAddr).Port

	if e.handler.portInUse(boundPort) {
		t.Errorf("portInUse(%d) = true for a bound-but-unrecorded port; check is DB-based, want false", boundPort)
	}
}

// --- queryProcesses ------------------------------------------------------

func TestQueryProcesses_FiltersByTypeAndHaul(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()

	other, err := e.svc.Create(ctx, "Other", "")
	if err != nil {
		t.Fatalf("create other haul: %v", err)
	}

	// registry rows: two on default haul (one running, one stopped), one on other haul.
	e.seedProcess(t, "registry", 100, 5000, "running", e.haul.ID)
	e.seedProcess(t, "registry", 101, 5001, "stopped", e.haul.ID)
	e.seedProcess(t, "registry", 102, 5002, "running", other.ID)
	// fileserver rows: one on each haul.
	e.seedProcess(t, "fileserver", 200, 8080, "running", e.haul.ID)
	e.seedProcess(t, "fileserver", 201, 8081, "running", other.ID)

	// Filter by type only.
	reg, err := e.handler.queryProcesses("registry", "")
	if err != nil {
		t.Fatalf("queryProcesses(registry): %v", err)
	}
	if len(reg) != 3 {
		t.Fatalf("registry count = %d, want 3", len(reg))
	}
	for _, p := range reg {
		if p["serveType"] != "registry" {
			t.Errorf("got serveType %v in registry results", p["serveType"])
		}
	}

	fs, err := e.handler.queryProcesses("fileserver", "")
	if err != nil {
		t.Fatalf("queryProcesses(fileserver): %v", err)
	}
	if len(fs) != 2 {
		t.Fatalf("fileserver count = %d, want 2", len(fs))
	}

	// Filter by type + haul.
	regDefault, err := e.handler.queryProcesses("registry", itoa(int(e.haul.ID)))
	if err != nil {
		t.Fatalf("queryProcesses(registry, default haul): %v", err)
	}
	if len(regDefault) != 2 {
		t.Fatalf("registry rows for default haul = %d, want 2", len(regDefault))
	}
	for _, p := range regDefault {
		if hid, ok := p["haulId"].(int64); !ok || hid != e.haul.ID {
			t.Errorf("registry row not scoped to default haul: haulId=%v", p["haulId"])
		}
	}

	regOther, err := e.handler.queryProcesses("registry", itoa(int(other.ID)))
	if err != nil {
		t.Fatalf("queryProcesses(registry, other haul): %v", err)
	}
	if len(regOther) != 1 {
		t.Fatalf("registry rows for other haul = %d, want 1", len(regOther))
	}
	if regOther[0]["pid"] != 102 {
		t.Errorf("other-haul registry pid = %v, want 102", regOther[0]["pid"])
	}

	// A non-numeric haul filter is silently ignored (treated as no filter).
	regBadFilter, err := e.handler.queryProcesses("registry", "not-a-number")
	if err != nil {
		t.Fatalf("queryProcesses(registry, bad filter): %v", err)
	}
	if len(regBadFilter) != 3 {
		t.Errorf("registry with non-numeric filter = %d, want 3 (filter ignored)", len(regBadFilter))
	}
}

// --- ServeRegistry / ServeFileserver validation (no exec) ----------------

func TestServeRegistry_InvalidJSON(t *testing.T) {
	e := newTestEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/api/serve/registry", strings.NewReader("{not json"))
	rr := httptest.NewRecorder()
	e.handler.ServeRegistry(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	assertNoProcessesStarted(t, e)
}

func TestServeRegistry_UnknownHaulID(t *testing.T) {
	e := newTestEnv(t)

	// A valid body referencing a haul that doesn't exist fails at resolveHaul,
	// before any exec. Returns 400.
	body := `{"haulId": 999999}`
	req := httptest.NewRequest(http.MethodPost, "/api/serve/registry", strings.NewReader(body))
	rr := httptest.NewRecorder()
	e.handler.ServeRegistry(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "resolve haul") {
		t.Errorf("body = %q, want it to mention haul resolution failure", rr.Body.String())
	}
	assertNoProcessesStarted(t, e)
}

func TestServeRegistry_PortConflict(t *testing.T) {
	e := newTestEnv(t)

	// Record a running process on the default registry port (5002). A valid
	// empty-body request resolves the default haul, defaults to port 5002,
	// sees the conflict, and returns 409 -- all before reaching exec.
	e.seedProcess(t, "registry", 4321, DefaultRegistryPort, "running", e.haul.ID)

	req := httptest.NewRequest(http.MethodPost, "/api/serve/registry", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	e.handler.ServeRegistry(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
	// Only the seeded row should exist; no new process was started.
	if n := countProcesses(t, e); n != 1 {
		t.Errorf("process rows = %d, want 1 (no new process started)", n)
	}
}

func TestServeRegistry_WrongMethod(t *testing.T) {
	e := newTestEnv(t)
	req := httptest.NewRequest(http.MethodPut, "/api/serve/registry", nil)
	rr := httptest.NewRecorder()
	e.handler.ServeRegistry(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

func TestServeFileserver_InvalidJSON(t *testing.T) {
	e := newTestEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/api/serve/fileserver", strings.NewReader("::bad::"))
	rr := httptest.NewRecorder()
	e.handler.ServeFileserver(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	assertNoProcessesStarted(t, e)
}

func TestServeFileserver_UnknownHaulID(t *testing.T) {
	e := newTestEnv(t)

	body := `{"haulId": 424242}`
	req := httptest.NewRequest(http.MethodPost, "/api/serve/fileserver", strings.NewReader(body))
	rr := httptest.NewRecorder()
	e.handler.ServeFileserver(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	assertNoProcessesStarted(t, e)
}

func TestServeFileserver_PortConflict(t *testing.T) {
	e := newTestEnv(t)

	// Default fileserver port is 5001.
	e.seedProcess(t, "fileserver", 7777, DefaultFileserverPort, "running", e.haul.ID)

	req := httptest.NewRequest(http.MethodPost, "/api/serve/fileserver", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	e.handler.ServeFileserver(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
	if n := countProcesses(t, e); n != 1 {
		t.Errorf("process rows = %d, want 1 (no new process started)", n)
	}
}

// --- StopRegistry / StopFileserver error paths ---------------------------

func TestStopRegistry_NonexistentPID(t *testing.T) {
	e := newTestEnv(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/serve/registry/54321", nil)
	rr := httptest.NewRecorder()
	e.handler.StopRegistry(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestStopRegistry_AlreadyStopped(t *testing.T) {
	e := newTestEnv(t)

	// A stopped DB row (not in the in-memory map) yields 410 Gone.
	e.seedProcess(t, "registry", 9001, 5000, "stopped", e.haul.ID)

	req := httptest.NewRequest(http.MethodDelete, "/api/serve/registry/9001", nil)
	rr := httptest.NewRecorder()
	e.handler.StopRegistry(rr, req)

	if rr.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", rr.Code)
	}
}

func TestStopRegistry_InvalidPID(t *testing.T) {
	e := newTestEnv(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/serve/registry/abc", nil)
	rr := httptest.NewRecorder()
	e.handler.StopRegistry(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestStopFileserver_NonexistentPID(t *testing.T) {
	e := newTestEnv(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/serve/fileserver/54321", nil)
	rr := httptest.NewRecorder()
	e.handler.StopFileserver(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestStopFileserver_AlreadyStopped(t *testing.T) {
	e := newTestEnv(t)

	e.seedProcess(t, "fileserver", 9002, 8080, "stopped", e.haul.ID)

	req := httptest.NewRequest(http.MethodDelete, "/api/serve/fileserver/9002", nil)
	rr := httptest.NewRecorder()
	e.handler.StopFileserver(rr, req)

	if rr.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", rr.Code)
	}
}

// --- GetRegistryStatus (DB-backed) ---------------------------------------

func TestGetRegistryStatus_FromDB(t *testing.T) {
	e := newTestEnv(t)

	e.seedProcess(t, "registry", 3131, 5005, "stopped", e.haul.ID)

	req := httptest.NewRequest(http.MethodGet, "/api/serve/registry/3131", nil)
	rr := httptest.NewRecorder()
	e.handler.GetRegistryStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v (%s)", err, rr.Body.String())
	}
	if got["serveType"] != "registry" {
		t.Errorf("serveType = %v, want registry", got["serveType"])
	}
	if got["status"] != "stopped" {
		t.Errorf("status = %v, want stopped", got["status"])
	}
	if got["port"].(float64) != 5005 {
		t.Errorf("port = %v, want 5005", got["port"])
	}
	if got["pid"].(float64) != 3131 {
		t.Errorf("pid = %v, want 3131", got["pid"])
	}
}

func TestGetRegistryStatus_NotFound(t *testing.T) {
	e := newTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/serve/registry/8888", nil)
	rr := httptest.NewRecorder()
	e.handler.GetRegistryStatus(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

// --- ListRegistryProcesses / ListFileserverProcesses (DB-backed) ---------

func TestListRegistryProcesses_ReturnsSeededRows(t *testing.T) {
	e := newTestEnv(t)

	e.seedProcess(t, "registry", 111, 5000, "running", e.haul.ID)
	e.seedProcess(t, "registry", 112, 5001, "stopped", e.haul.ID)
	e.seedProcess(t, "fileserver", 222, 8080, "running", e.haul.ID) // must NOT appear

	req := httptest.NewRequest(http.MethodGet, "/api/serve/registry", nil)
	rr := httptest.NewRecorder()
	e.handler.ListRegistryProcesses(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var list []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode body: %v (%s)", err, rr.Body.String())
	}
	if len(list) != 2 {
		t.Fatalf("len = %d, want 2 registry rows", len(list))
	}
	for _, p := range list {
		if p["serveType"] != "registry" {
			t.Errorf("unexpected serveType %v in list", p["serveType"])
		}
	}
}

func TestListRegistryProcesses_FilteredByHaul(t *testing.T) {
	e := newTestEnv(t)
	other, err := e.svc.Create(context.Background(), "Other", "")
	if err != nil {
		t.Fatalf("create other haul: %v", err)
	}

	e.seedProcess(t, "registry", 111, 5000, "running", e.haul.ID)
	e.seedProcess(t, "registry", 112, 5001, "running", other.ID)

	req := httptest.NewRequest(http.MethodGet, "/api/serve/registry?haul="+itoa(int(other.ID)), nil)
	rr := httptest.NewRecorder()
	e.handler.ListRegistryProcesses(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var list []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d, want 1 row for other haul", len(list))
	}
	if list[0]["pid"].(float64) != 112 {
		t.Errorf("pid = %v, want 112", list[0]["pid"])
	}
}

func TestListFileserverProcesses_ReturnsSeededRows(t *testing.T) {
	e := newTestEnv(t)

	e.seedProcess(t, "fileserver", 301, 8080, "running", e.haul.ID)
	e.seedProcess(t, "fileserver", 302, 8081, "stopped", e.haul.ID)
	e.seedProcess(t, "registry", 400, 5000, "running", e.haul.ID) // must NOT appear

	req := httptest.NewRequest(http.MethodGet, "/api/serve/fileserver", nil)
	rr := httptest.NewRecorder()
	e.handler.ListFileserverProcesses(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var list []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode body: %v (%s)", err, rr.Body.String())
	}
	if len(list) != 2 {
		t.Fatalf("len = %d, want 2 fileserver rows", len(list))
	}
	for _, p := range list {
		if p["serveType"] != "fileserver" {
			t.Errorf("unexpected serveType %v in list", p["serveType"])
		}
	}
}

func TestListFileserverProcesses_Empty(t *testing.T) {
	e := newTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/serve/fileserver", nil)
	rr := httptest.NewRecorder()
	e.handler.ListFileserverProcesses(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	// Handler encodes an initialized (non-nil) slice, so the body is "[]".
	if got := strings.TrimSpace(rr.Body.String()); got != "[]" {
		t.Errorf("body = %q, want []", got)
	}
}

// --- helpers -------------------------------------------------------------

// assertNoProcessesStarted verifies neither the in-memory map nor the DB has a
// process, i.e. the exec path was never reached.
func assertNoProcessesStarted(t *testing.T, e *testEnv) {
	t.Helper()
	e.handler.mu.RLock()
	n := len(e.handler.processes)
	e.handler.mu.RUnlock()
	if n != 0 {
		t.Errorf("in-memory processes = %d, want 0 (exec path must not be reached)", n)
	}
	if c := countProcesses(t, e); c != 0 {
		t.Errorf("serve_processes rows = %d, want 0 (exec path must not be reached)", c)
	}
}

func countProcesses(t *testing.T, e *testEnv) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(1) FROM serve_processes`).Scan(&n); err != nil {
		t.Fatalf("count processes: %v", err)
	}
	return n
}

// fakeHauler puts a "hauler" script first in PATH for the test.
func fakeHauler(t *testing.T, script string) {
	t.Helper()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hauler"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// When hauler cannot bind its port it exits at once; the start request must
// fail with 500 and the reason, and the row must read crashed.
func TestServeRegistry_BindFailureReturns500(t *testing.T) {
	fakeHauler(t, `printf '\033[31mERR\033[0m listen tcp :5002: bind: address already in use\n'; exit 1`)
	e := newTestEnv(t)

	rr := httptest.NewRecorder()
	e.handler.ServeRegistry(rr, httptest.NewRequest(http.MethodPost, "/api/serve/registry", strings.NewReader(`{}`)))

	const reason = "exit status 1: ERR listen tcp :5002: bind: address already in use"
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d (%s), want 500", rr.Code, rr.Body.String())
	}
	if want := "Registry serve on port 5002 failed: " + reason; strings.TrimSpace(rr.Body.String()) != want {
		t.Fatalf("body = %q, want %q", rr.Body.String(), want)
	}
	var status, got string
	if err := e.db.QueryRow(`SELECT status, COALESCE(exit_reason, '') FROM serve_processes`).Scan(&status, &got); err != nil {
		t.Fatal(err)
	}
	if status != "crashed" || got != reason {
		t.Fatalf("row = %q / %q, want crashed / %q", status, got, reason)
	}
}

func TestServeFileserver_BindFailureReturns500(t *testing.T) {
	fakeHauler(t, `echo 'ERR listen tcp :5001: bind: address already in use'; exit 1`)
	e := newTestEnv(t)

	rr := httptest.NewRecorder()
	e.handler.ServeFileserver(rr, httptest.NewRequest(http.MethodPost, "/api/serve/fileserver", strings.NewReader(`{}`)))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d (%s), want 500", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "bind: address already in use") {
		t.Fatalf("body = %q, want the bind error", rr.Body.String())
	}
}

// A registry started without a "readonly" field must be read-only: hauler only
// accepts pushes when the caller asks for readonly=false explicitly.
func TestServeRegistry_ReadonlyDefaultsToTrue(t *testing.T) {
	shortGrace(t, 50*time.Millisecond)
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"port":5093}`, "--readonly"},
		{`{"port":5093,"readonly":true}`, "--readonly"},
		{`{"port":5093,"readonly":false}`, "--readonly=false"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			argsFile := filepath.Join(t.TempDir(), "args")
			fakeHauler(t, `printf '%s\n' "$@" > '`+argsFile+`'; exec sleep 30`)
			e := newTestEnv(t)

			rr := httptest.NewRecorder()
			e.handler.ServeRegistry(rr, httptest.NewRequest(http.MethodPost, "/api/serve/registry", strings.NewReader(tc.body)))
			t.Cleanup(e.handler.StopAll)
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d (%s), want 201", rr.Code, rr.Body.String())
			}
			raw, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			var readonly []string
			for _, a := range strings.Fields(string(raw)) {
				if strings.HasPrefix(a, "--readonly") {
					readonly = append(readonly, a)
				}
			}
			if len(readonly) != 1 || readonly[0] != tc.want {
				t.Fatalf("readonly flags = %q, want [%q]", readonly, tc.want)
			}
		})
	}
}

// A haul created before Wagon wrote index.json has a store hauler's registry
// serve cannot open. Starting a registry on it must work and serve an empty
// catalog, not fail with 500.
func TestServeRegistry_EmptyStoreWithoutIndexStarts(t *testing.T) {
	shortGrace(t, 50*time.Millisecond)
	// Like hauler v1.3.2: the registry exits at once when index.json is missing.
	fakeHauler(t, `while [ $# -gt 0 ]; do [ "$1" = --store ] && store=$2; shift; done
[ -f "$store/index.json" ] || { echo "ERR function execution failed: signed image index: stat $store/index.json: no such file or directory"; exit 1; }
exec sleep 30`)
	e := newTestEnv(t)
	if err := os.Remove(filepath.Join(e.haul.StoreDir, "index.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	e.handler.ServeRegistry(rr, httptest.NewRequest(http.MethodPost, "/api/serve/registry", strings.NewReader(`{"port":5094}`)))
	t.Cleanup(e.handler.StopAll)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s), want 201", rr.Code, rr.Body.String())
	}
}
