package serve

import (
	"context"
	"os/exec"
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
	done := make(chan struct{})
	go func() { e.handler.monitorProcess(pid, cmd, stdout, stderr); close(done) }()
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
