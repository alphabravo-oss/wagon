package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
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
	proc := &managedProcess{Cmd: cmd, Process: cmd.Process, StartedAt: time.Now(), Logs: []string{}}
	e.handler.processes[pid] = proc
	e.seedProcess(t, "registry", pid, 5000, "running", e.haul.ID)
	var rowID int64
	if err := e.db.QueryRow("SELECT id FROM serve_processes WHERE pid = ?", pid).Scan(&rowID); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go e.handler.monitorProcess(rowID, pid, cmd, stdout, stderr, done)
	select {
	case <-done:
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = stdout.Close()
		_ = stderr.Close()
		<-done
		t.Fatal("monitor did not complete before timeout")
	}
	if !cmd.ProcessState.Success() {
		t.Fatalf("child failed: %v", cmd.ProcessState)
	}
	// Output is drained and discarded; only the exit line is recorded, as before.
	if len(proc.Logs) != 1 || proc.Logs[0] != "Process exited cleanly" {
		t.Fatalf("unexpected exit logs: %q", proc.Logs)
	}
	if _, exists := e.handler.processes[pid]; exists {
		t.Fatal("exited process still registered")
	}
	var status, reason string
	if err := e.db.QueryRow("SELECT status, exit_reason FROM serve_processes WHERE pid = ?", pid).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "stopped" || reason != "" {
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
	if err := e.db.QueryRow("SELECT status, COALESCE(exit_reason, '') FROM serve_processes WHERE pid = ?", pid).Scan(&status, &reason); err != nil {
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
