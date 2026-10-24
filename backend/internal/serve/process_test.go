package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMonitorProcessDrainsPipes(t *testing.T) {
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	e := newTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `printf '%200000s\nstdout-end\n' ''; printf '%200000s\nstderr-end\n' '' >&2`)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stderr.Close() }()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	pid := cmd.Process.Pid
	e.seedProcess(t, "registry", pid, 5000, "running", e.haul.ID)
	var rowID int64
	if err := e.db.QueryRow("SELECT id FROM serve_processes WHERE pid = ?", pid).Scan(&rowID); err != nil {
		t.Fatal(err)
	}
	proc := &managedProcess{Cmd: cmd, Process: cmd.Process, StartedAt: time.Now(), Logs: []string{}, rowID: rowID, done: make(chan struct{})}
	e.handler.processes[pid] = proc
	go e.handler.monitorProcess(proc, stdout, stderr)
	select {
	case <-proc.done:
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = stdout.Close()
		_ = stderr.Close()
		<-proc.done
		t.Fatal("monitor did not complete before timeout")
	}
	if !cmd.ProcessState.Success() {
		t.Fatalf("child failed: %v", cmd.ProcessState)
	}
	// Output is drained and discarded; only the exit line is recorded. A serve
	// that exits 0 without a Stop exited unexpectedly.
	const want = "exited unexpectedly (exit status 0): stdout-end"
	if len(proc.Logs) != 1 || proc.Logs[0] != "Process exited: "+want {
		t.Fatalf("unexpected exit logs: %q", proc.Logs)
	}
	if _, exists := e.handler.processes[pid]; exists {
		t.Fatal("exited process still registered")
	}
	if status, reason := rowStatus(t, e, pid); status != "crashed" || reason != want {
		t.Fatalf("unexpected final status %q, reason %q", status, reason)
	}
}

// shellCmd returns a /bin/sh command, skipping the test when sh is missing.
func shellCmd(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	return exec.Command("/bin/sh", "-c", script)
}

func rowStatus(t *testing.T, e *testEnv, pid int) (status, reason string) {
	t.Helper()
	if err := e.db.QueryRow("SELECT status, COALESCE(exit_reason, '') FROM serve_processes WHERE pid = ? ORDER BY id DESC LIMIT 1", pid).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	return status, reason
}

// A serve that dies at startup (hauler exits within milliseconds when its port
// is taken) must be reported as an error and recorded as crashed, never left
// "running". Before the fix the exit update could run before the INSERT.
func TestLaunch_ExitAtStartupIsReportedAndRecorded(t *testing.T) {
	e := newTestEnv(t)
	// Real hauler output: log lines (with color and a timestamp) on stdout,
	// cobra usage text on stderr.
	cmd := shellCmd(t, `printf '\033[90m2026-10-24 04:25:12\033[0m \033[32mINF\033[0m starting registry\n\033[90m2026-10-24 04:25:12\033[0m \033[31mERR\033[0m listen tcp :5000: bind: address already in use\n'; printf 'Usage:\n  -s, --store string   Set the directory\n' >&2; exit 1`)

	proc, err := e.handler.launch(cmd, "registry", 5000, e.haul.ID, `{}`)
	if err == nil {
		t.Fatalf("launch returned no error (proc %+v), want the startup failure", proc)
	}
	want := "exit status 1: ERR listen tcp :5000: bind: address already in use"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
	status, reason := rowStatus(t, e, cmd.Process.Pid)
	if status != "crashed" || reason != want {
		t.Fatalf("row = %q / %q, want crashed / %q", status, reason, want)
	}
	if e.handler.portInUse(5000) {
		t.Error("port still counted as in use after the process exited")
	}
}

// A serve that exits later (after the startup grace) flips to crashed.
func TestLaunch_LaterExitMarksCrashed(t *testing.T) {
	old := startupGrace
	startupGrace = 50 * time.Millisecond
	t.Cleanup(func() { startupGrace = old })

	e := newTestEnv(t)
	cmd := shellCmd(t, `sleep 0.5; echo 'fatal: store vanished' >&2; exit 3`)
	proc, err := e.handler.launch(cmd, "fileserver", 5001, e.haul.ID, `{}`)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if status, _ := rowStatus(t, e, proc.Process.Pid); status != "running" {
		t.Fatalf("status during run = %q, want running", status)
	}
	select {
	case <-proc.done:
	case <-time.After(10 * time.Second):
		t.Fatal("process did not exit")
	}
	status, reason := rowStatus(t, e, proc.Process.Pid)
	if status != "crashed" || reason != "exit status 3: fatal: store vanished" {
		t.Fatalf("row = %q / %q, want crashed / exit status 3: fatal: store vanished", status, reason)
	}
}

// Stopping a serve keeps it "stopped", not "crashed", even though the process
// exits with a signal.
func TestLaunch_StopKeepsStopped(t *testing.T) {
	old := startupGrace
	startupGrace = 50 * time.Millisecond
	t.Cleanup(func() { startupGrace = old })

	e := newTestEnv(t)
	cmd := shellCmd(t, `exec sleep 30`)
	proc, err := e.handler.launch(cmd, "registry", 5002, e.haul.ID, `{}`)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	pid := proc.Process.Pid
	rr := httptest.NewRecorder()
	e.handler.StopRegistry(rr, httptest.NewRequest(http.MethodDelete, "/api/serve/registry/"+strconv.Itoa(pid), nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("stop status = %d: %s", rr.Code, rr.Body.String())
	}
	select {
	case <-proc.done:
	case <-time.After(10 * time.Second):
		_ = proc.Process.Kill()
		t.Fatal("process did not exit after stop")
	}
	if status, reason := rowStatus(t, e, pid); status != "stopped" || reason != "" {
		t.Fatalf("row = %q / %q, want stopped / empty", status, reason)
	}
}

// Published registries share serve_processes but belong to the Publishing
// page; the Serve page lists only manual serves.
func TestQueryProcesses_ExcludesPublished(t *testing.T) {
	e := newTestEnv(t)
	e.seedProcess(t, "registry", 301, 5002, "running", e.haul.ID)
	if _, err := e.db.Exec(`INSERT INTO serve_processes (serve_type, pid, port, args, status, haul_id, role, hostname)
		VALUES ('registry', 302, 49200, '{}', 'running', ?, 'published', 'demo')`, e.haul.ID); err != nil {
		t.Fatal(err)
	}
	procs, err := e.handler.queryProcesses("registry", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 1 || procs[0]["pid"] != 301 {
		t.Fatalf("processes = %v, want only the manual pid 301", procs)
	}
}

func shortGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old := startupGrace
	startupGrace = d
	t.Cleanup(func() { startupGrace = old })
}

func waitDone(t *testing.T, proc *managedProcess) {
	t.Helper()
	select {
	case <-proc.done:
	case <-time.After(10 * time.Second):
		_ = proc.Process.Kill()
		t.Fatal("process did not exit")
	}
}

// Wagon shutdown (StopAll) records its serves as stopped, not crashed, even
// though they exit with "signal: terminated".
func TestStopAll_RecordsStopped(t *testing.T) {
	shortGrace(t, 50*time.Millisecond)
	e := newTestEnv(t)
	reg, err := e.handler.launch(shellCmd(t, `exec sleep 30`), "registry", 5002, e.haul.ID, `{}`)
	if err != nil {
		t.Fatalf("launch registry: %v", err)
	}
	fs, err := e.handler.launch(shellCmd(t, `exec sleep 30`), "fileserver", 5001, e.haul.ID, `{}`)
	if err != nil {
		t.Fatalf("launch fileserver: %v", err)
	}
	e.handler.StopAll()
	for _, p := range []*managedProcess{reg, fs} {
		waitDone(t, p)
		if status, reason := rowStatus(t, e, p.Process.Pid); status != "stopped" || reason != "" {
			t.Fatalf("pid %d row = %q / %q, want stopped / empty", p.Process.Pid, status, reason)
		}
	}
}

// A user Stop that lands during the startup grace is not a startup failure:
// launch returns the process (status "stopped") and the handler gets no error.
func TestLaunch_StopDuringGraceReturnsCleanly(t *testing.T) {
	shortGrace(t, 5*time.Second)
	e := newTestEnv(t)
	type result struct {
		proc *managedProcess
		err  error
	}
	res := make(chan result, 1)
	cmd := shellCmd(t, `exec sleep 30`)
	go func() {
		p, err := e.handler.launch(cmd, "fileserver", 5001, e.haul.ID, `{}`)
		res <- result{p, err}
	}()

	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for pid == 0 && time.Now().Before(deadline) {
		e.handler.mu.RLock()
		for p := range e.handler.processes {
			pid = p
		}
		e.handler.mu.RUnlock()
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("process never registered")
	}
	rr := httptest.NewRecorder()
	e.handler.StopFileserver(rr, httptest.NewRequest(http.MethodDelete, "/api/serve/fileserver/"+strconv.Itoa(pid), nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("stop status = %d: %s", rr.Code, rr.Body.String())
	}

	var r result
	select {
	case r = <-res:
	case <-time.After(4 * time.Second):
		t.Fatal("launch did not return promptly after the stop")
	}
	if r.err != nil {
		t.Fatalf("launch returned %v after a user stop, want no error", r.err)
	}
	if got := r.proc.status(); got != "stopped" {
		t.Fatalf("reported status = %q, want stopped", got)
	}
	if status, reason := rowStatus(t, e, pid); status != "stopped" || reason != "" {
		t.Fatalf("row = %q / %q, want stopped / empty", status, reason)
	}
}

// A serve that exits 0 during the grace did not start; that is an error with a
// clear reason, not "running".
func TestLaunch_ExitZeroAtStartupIsAnError(t *testing.T) {
	e := newTestEnv(t)
	cmd := shellCmd(t, `exit 0`)
	if _, err := e.handler.launch(cmd, "registry", 5002, e.haul.ID, `{}`); err == nil ||
		err.Error() != "exited unexpectedly (exit status 0)" {
		t.Fatalf("launch error = %v, want exited unexpectedly (exit status 0)", err)
	}
	if status, _ := rowStatus(t, e, cmd.Process.Pid); status != "crashed" {
		t.Fatalf("row status = %q, want crashed", status)
	}
}

// Stop updates this run's row by id; an older row that happens to carry the
// same pid keeps its own history.
func TestStop_UpdatesRowByIDNotPID(t *testing.T) {
	shortGrace(t, 50*time.Millisecond)
	e := newTestEnv(t)
	proc, err := e.handler.launch(shellCmd(t, `exec sleep 30`), "registry", 5002, e.haul.ID, `{}`)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	pid := proc.Process.Pid
	res, err := e.db.Exec(`INSERT INTO serve_processes (serve_type, pid, port, args, status, haul_id, exit_reason)
		VALUES ('registry', ?, 5003, '{}', 'crashed', ?, 'old run')`, pid, e.haul.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldID, _ := res.LastInsertId()

	rr := httptest.NewRecorder()
	e.handler.StopRegistry(rr, httptest.NewRequest(http.MethodDelete, "/api/serve/registry/"+strconv.Itoa(pid), nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("stop status = %d: %s", rr.Code, rr.Body.String())
	}
	waitDone(t, proc)

	var status, reason string
	for _, c := range []struct {
		id                 int64
		wantStatus, wantRe string
	}{{proc.rowID, "stopped", ""}, {oldID, "crashed", "old run"}} {
		if err := e.db.QueryRow(`SELECT status, COALESCE(exit_reason, '') FROM serve_processes WHERE id = ?`, c.id).Scan(&status, &reason); err != nil {
			t.Fatal(err)
		}
		if status != c.wantStatus || reason != c.wantRe {
			t.Fatalf("row %d = %q / %q, want %q / %q", c.id, status, reason, c.wantStatus, c.wantRe)
		}
	}
}

// Control characters other than tab never reach the recorded reason.
func TestExitLine_StripsControlCharacters(t *testing.T) {
	var tail outputTail
	_, _ = tail.Write([]byte("\x1b[2K\x1b[31mERR\x1b[0m bad\x07 port\r\n"))
	if got := exitLine(&tail); got != "ERR bad port" {
		t.Fatalf("exitLine = %q, want %q", got, "ERR bad port")
	}
}

// A start that succeeds answers 201 with the JSON body the Serve page reads.
func TestServe_StartReturns201Body(t *testing.T) {
	shortGrace(t, 50*time.Millisecond)
	fakeHauler(t, `exec sleep 30`)
	for _, tc := range []struct {
		name    string
		path    string
		port    int
		message string
	}{
		{"registry", "/api/serve/registry", 5092, "Registry serve started"},
		{"fileserver", "/api/serve/fileserver", 5091, "Fileserver serve started"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			serve := e.handler.ServeRegistry
			if tc.name == "fileserver" {
				serve = e.handler.ServeFileserver
			}
			rr := httptest.NewRecorder()
			body := `{"port":` + strconv.Itoa(tc.port) + `}`
			serve(rr, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body)))
			t.Cleanup(e.handler.StopAll)
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d (%s), want 201", rr.Code, rr.Body.String())
			}
			if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var got struct {
				PID       int    `json:"pid"`
				Port      int    `json:"port"`
				Status    string `json:"status"`
				StartedAt string `json:"startedAt"`
				Message   string `json:"message"`
				HaulID    int64  `json:"haulId"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode body %q: %v", rr.Body.String(), err)
			}
			if got.PID <= 0 || got.Port != tc.port || got.Status != "running" || got.Message != tc.message || got.HaulID != e.haul.ID {
				t.Fatalf("body = %+v, want pid>0 port %d running %q haul %d", got, tc.port, tc.message, e.haul.ID)
			}
			if _, err := time.Parse(time.RFC3339, got.StartedAt); err != nil {
				t.Errorf("startedAt %q is not RFC3339: %v", got.StartedAt, err)
			}
			if status, _ := rowStatus(t, e, got.PID); status != "running" {
				t.Errorf("row status = %q, want running", status)
			}
		})
	}
}

// If the stop can't be recorded, the API must not claim success: it answers
// 500, the process keeps running and the row still reads running.
func TestStopRegistry_DBErrorReturns500AndKeepsRunning(t *testing.T) {
	shortGrace(t, 50*time.Millisecond)
	e := newTestEnv(t)
	proc, err := e.handler.launch(shellCmd(t, `exec sleep 30`), "registry", 5002, e.haul.ID, `{}`)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	pid := proc.Process.Pid
	if _, err := e.db.Exec(`ALTER TABLE serve_processes RENAME TO serve_processes_hidden`); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	e.handler.StopRegistry(rr, httptest.NewRequest(http.MethodDelete, "/api/serve/registry/"+strconv.Itoa(pid), nil))
	if _, err := e.db.Exec(`ALTER TABLE serve_processes_hidden RENAME TO serve_processes`); err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("stop status = %d (%s), want 500", rr.Code, rr.Body.String())
	}
	select {
	case <-proc.done:
		t.Fatal("process exited although the stop was not recorded")
	case <-time.After(200 * time.Millisecond):
	}
	if proc.status() != "running" {
		t.Errorf("proc.status() = %q, want running", proc.status())
	}
	if status, _ := rowStatus(t, e, pid); status != "running" {
		t.Fatalf("row status = %q, want running", status)
	}
	// A retry once the database works stops it normally.
	rr = httptest.NewRecorder()
	e.handler.StopRegistry(rr, httptest.NewRequest(http.MethodDelete, "/api/serve/registry/"+strconv.Itoa(pid), nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("retry stop status = %d (%s), want 200", rr.Code, rr.Body.String())
	}
	waitDone(t, proc)
	if status, reason := rowStatus(t, e, pid); status != "stopped" || reason != "" {
		t.Fatalf("row = %q / %q, want stopped / empty", status, reason)
	}
}
