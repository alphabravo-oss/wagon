package serve

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
	"github.com/alphabravo-oss/wagon/backend/internal/hauls"
)

// Default ports for manual serves. They avoid the UI (8080) and Publishing's
// registry front door (5000), which run in the same container.
const (
	DefaultRegistryPort   = 5002
	DefaultFileserverPort = 5001
)

// Handler handles HTTP requests for serve operations
type Handler struct {
	cfg         *config.Config
	db          *sql.DB
	hauls       *hauls.Service
	processes   map[int]*managedProcess
	certManager *CertManager
	mu          sync.RWMutex
}

type managedProcess struct {
	Cmd       *exec.Cmd
	Process   *os.Process
	StartedAt time.Time
	Logs      []string
	LogMu     sync.Mutex

	// done is closed after the process exits and its row is updated;
	// exitReason is set (under LogMu) just before.
	done       chan struct{}
	exitReason string
}

// NewHandler creates a new serve handler
func NewHandler(cfg *config.Config, db *sql.DB, haulSvc *hauls.Service) *Handler {
	return &Handler{
		cfg:         cfg,
		db:          db,
		hauls:       haulSvc,
		processes:   make(map[int]*managedProcess),
		certManager: NewCertManager(cfg.DataDir),
	}
}

// StopAll terminates every managed ad-hoc serve process (used on graceful
// shutdown) so no hauler children are orphaned.
func (h *Handler) StopAll() {
	h.mu.RLock()
	procs := make([]*managedProcess, 0, len(h.processes))
	for _, p := range h.processes {
		procs = append(procs, p)
	}
	h.mu.RUnlock()

	for _, p := range procs {
		if p.Process != nil {
			_ = p.Process.Signal(syscall.SIGTERM)
		}
	}
	if len(procs) > 0 {
		log.Printf("serve: sent SIGTERM to %d serve process(es)", len(procs))
	}
}

// resolveHaul returns the haul to serve, defaulting to the default haul.
func (h *Handler) resolveHaul(ctx context.Context, haulID int64) (*hauls.Haul, error) {
	if haulID > 0 {
		return h.hauls.Get(ctx, haulID)
	}
	return h.hauls.EnsureDefault(ctx)
}

// portInUse reports whether a serve process is already running on the port.
func (h *Handler) portInUse(port int) bool {
	var count int
	_ = h.db.QueryRow(`SELECT COUNT(1) FROM serve_processes WHERE port = ? AND status = 'running'`, port).Scan(&count)
	return count > 0
}

// ServeRegistryRequest represents the request to start a registry serve
type ServeRegistryRequest struct {
	HaulID     int64  `json:"haulId,omitempty"`
	Port       int    `json:"port,omitempty"`
	Readonly   bool   `json:"readonly,omitempty"`
	TLSCert    string `json:"tlsCert,omitempty"`
	TLSKey     string `json:"tlsKey,omitempty"`
	AutoTLS    bool   `json:"autoTls,omitempty"`
	Directory  string `json:"directory,omitempty"`
	ConfigFile string `json:"configFile,omitempty"`
}

// ServeFileserverRequest represents the request to start a fileserver serve
type ServeFileserverRequest struct {
	HaulID    int64  `json:"haulId,omitempty"`
	Port      int    `json:"port,omitempty"`
	Timeout   int    `json:"timeout,omitempty"`
	TLSCert   string `json:"tlsCert,omitempty"`
	TLSKey    string `json:"tlsKey,omitempty"`
	AutoTLS   bool   `json:"autoTls,omitempty"`
	Directory string `json:"directory,omitempty"`
}

// ServeRegistry handles POST /api/serve/registry
func (h *Handler) ServeRegistry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ServeRegistryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// Resolve which haul's store to serve.
	haul, err := h.resolveHaul(r.Context(), req.HaulID)
	if err != nil {
		http.Error(w, "Failed to resolve haul: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Default to 5002: Publishing's registry front door already listens on 5000.
	port := req.Port
	if port == 0 {
		port = DefaultRegistryPort
	}

	// Prevent port collisions across concurrently served hauls.
	if h.portInUse(port) {
		http.Error(w, fmt.Sprintf("Port %d is already in use by another serve process", port), http.StatusConflict)
		return
	}

	// Build args for hauler store serve registry command, scoped to the haul.
	args := []string{"store", "serve", "registry", "--port", strconv.Itoa(port), "--store", haul.StoreDir}

	// Give each haul its own registry backend directory so multiple registries
	// can run side by side without clobbering each other.
	if req.Directory == "" {
		req.Directory = filepath.Join(filepath.Dir(haul.StoreDir), "registry")
	}

	// Readonly flag (hauler default is true)
	if req.Readonly {
		args = append(args, "--readonly")
	} else {
		args = append(args, "--readonly=false")
	}

	// Handle TLS configuration
	if req.AutoTLS {
		serveType := "registry"
		certPath, keyPath, err := h.certManager.GetOrGenerateCert(serveType)
		if err != nil {
			log.Printf("Error generating TLS certificate: %v", err)
			http.Error(w, fmt.Sprintf("Failed to generate certificate: %v", err), http.StatusInternalServerError)
			return
		}
		args = append(args, "--tls-cert", certPath, "--tls-key", keyPath)
	} else {
		// Optional TLS cert
		if req.TLSCert != "" {
			args = append(args, "--tls-cert", req.TLSCert)
		}

		// Optional TLS key
		if req.TLSKey != "" {
			args = append(args, "--tls-key", req.TLSKey)
		}
	}

	// Optional directory
	if req.Directory != "" {
		args = append(args, "--directory", req.Directory)
	}

	// Optional config file
	if req.ConfigFile != "" {
		args = append(args, "--config", req.ConfigFile)
	}

	// Start the process
	cmd := exec.Command("hauler", args...)
	cmd.Dir = h.cfg.DataDir
	argsJSON, _ := json.Marshal(req)
	proc, err := h.launch(cmd, "registry", port, haul.ID, string(argsJSON))
	if err != nil {
		http.Error(w, fmt.Sprintf("Registry serve on port %d failed: %v", port, err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"pid":       proc.Process.Pid,
		"port":      port,
		"status":    "running",
		"startedAt": proc.StartedAt.Format(time.RFC3339),
		"message":   "Registry serve started",
		"haulId":    haul.ID,
	})
}

// startupGrace is how long launch waits for a new serve process to fail before
// reporting it as running. hauler exits within milliseconds when its port is
// taken or its flags are wrong.
var startupGrace = 2 * time.Second

// launch starts cmd, records it, and waits up to startupGrace so a process that
// exits at once is reported as an error instead of as running. The database row
// is written before the monitor starts, so the monitor's exit update can never
// run first and leave a dead process marked "running".
func (h *Handler) launch(cmd *exec.Cmd, serveType string, port int, haulID int64, argsJSON string) (*managedProcess, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("create stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	pid := cmd.Process.Pid

	res, err := h.db.Exec(`
		INSERT INTO serve_processes (serve_type, pid, port, args, status, haul_id)
		VALUES (?, ?, ?, ?, 'running', ?)
	`, serveType, pid, port, argsJSON, haulID)
	var rowID int64
	if err == nil {
		rowID, err = res.LastInsertId()
	}
	if err != nil {
		log.Printf("Error storing serve process in database: %v", err)
	}

	proc := &managedProcess{
		Cmd:       cmd,
		Process:   cmd.Process,
		StartedAt: time.Now(),
		Logs:      []string{},
		done:      make(chan struct{}),
	}
	h.mu.Lock()
	h.processes[pid] = proc
	h.mu.Unlock()

	go h.monitorProcess(rowID, pid, cmd, stdout, stderr, proc.done)

	select {
	case <-proc.done:
		if proc.exitReason == "" {
			return nil, errors.New("exited immediately")
		}
		return nil, errors.New(proc.exitReason)
	case <-time.After(startupGrace):
		return proc, nil
	}
}

// monitorProcess waits for a serve process to exit and records the outcome. A
// clean exit is "stopped"; any other exit is "crashed" with the error and the
// last line hauler printed (for example "bind: address already in use"). A row
// already marked "stopped" by a Stop request keeps that status.
func (h *Handler) monitorProcess(rowID int64, pid int, cmd *exec.Cmd, stdout, stderr io.ReadCloser, done chan struct{}) {
	// Close pipes when done
	defer func() { _ = stdout.Close() }() // pipes already closed by Wait
	defer func() { _ = stderr.Close() }() // pipes already closed by Wait

	// One tail per stream: hauler logs (including ERR lines) go to stdout,
	// while cobra prints usage text to stderr.
	var outTail, errTail outputTail
	var drains sync.WaitGroup
	for _, s := range []struct {
		r    io.Reader
		tail *outputTail
	}{{stdout, &outTail}, {stderr, &errTail}} {
		drains.Add(1)
		go func() {
			defer drains.Done()
			// Keep only the last bytes so the child never blocks on a full pipe.
			_, _ = io.Copy(s.tail, s.r)
		}()
	}
	// Wait closes the pipes, so finish reading both streams first.
	drains.Wait()

	// Wait for process to complete
	err := cmd.Wait()

	status, exitReason := "stopped", ""
	if err != nil {
		status, exitReason = "crashed", err.Error()
		if line := exitLine(&outTail, &errTail); line != "" {
			exitReason += ": " + line
		}
	}

	// Capture final status
	h.mu.Lock()
	managedProc, exists := h.processes[pid]
	if exists {
		managedProc.LogMu.Lock()
		if err != nil {
			managedProc.Logs = append(managedProc.Logs, "Process exited: "+exitReason)
		} else {
			managedProc.Logs = append(managedProc.Logs, "Process exited cleanly")
		}
		managedProc.exitReason = exitReason
		managedProc.LogMu.Unlock()
		delete(h.processes, pid)
	}
	h.mu.Unlock()

	_, dbErr := h.db.Exec(`
		UPDATE serve_processes
		SET status = ?, stopped_at = CURRENT_TIMESTAMP, exit_reason = ?
		WHERE id = ? AND status = 'running'
	`, status, exitReason, rowID)
	if dbErr != nil {
		log.Printf("Error updating serve process in database: %v", dbErr)
	}
	if done != nil {
		close(done)
	}
}

// outputTail keeps the last tailSize bytes written to it.
type outputTail struct {
	mu  sync.Mutex
	buf []byte
}

const tailSize = 4096

func (t *outputTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > tailSize {
		t.buf = t.buf[len(t.buf)-tailSize:]
	}
	return len(p), nil
}

var (
	ansiEscape   = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	logTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}\S*\s+`)
	errorMarker  = regexp.MustCompile(`(?i)\b(err|error|fatal|ftl)\b`)
)

// lines returns the tail's non-empty lines without color codes or timestamps.
func (t *outputTail) lines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for _, line := range strings.Split(ansiEscape.ReplaceAllString(string(t.buf), ""), "\n") {
		if line = logTimestamp.ReplaceAllString(strings.TrimSpace(line), ""); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// exitLine picks the line that best explains an exit: the last error line in
// either stream, else the last line of output.
func exitLine(tails ...*outputTail) string {
	var last string
	for _, t := range tails {
		lines := t.lines()
		for i := len(lines) - 1; i >= 0; i-- {
			if errorMarker.MatchString(lines[i]) {
				return clip(lines[i])
			}
		}
		if last == "" && len(lines) > 0 {
			last = lines[len(lines)-1]
		}
	}
	return clip(last)
}

func clip(line string) string {
	if len(line) > 300 {
		return line[len(line)-300:]
	}
	return line
}

// StopRegistry handles DELETE /api/serve/registry/:pid
func (h *Handler) StopRegistry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract PID from path
	// Path format: /api/serve/registry/:pid
	prefix := "/api/serve/registry/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	pidStr := r.URL.Path[len(prefix):]
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		http.Error(w, "Invalid PID", http.StatusBadRequest)
		return
	}

	h.mu.RLock()
	managedProc, exists := h.processes[pid]
	h.mu.RUnlock()

	if !exists {
		// Check database for historical record
		var status string
		row := h.db.QueryRow("SELECT status FROM serve_processes WHERE pid = ?", pid)
		_ = row.Scan(&status)
		if status == "stopped" {
			http.Error(w, "Process already stopped", http.StatusGone)
			return
		}
		http.Error(w, "Process not found", http.StatusNotFound)
		return
	}

	// Send SIGTERM for graceful shutdown
	if err := managedProc.Process.Signal(syscall.SIGTERM); err != nil {
		log.Printf("Error sending SIGTERM to process %d: %v", pid, err)
		http.Error(w, fmt.Sprintf("Failed to stop process: %v", err), http.StatusInternalServerError)
		return
	}

	// Update database immediately
	_, _ = h.db.Exec(`
		UPDATE serve_processes
		SET status = ?, stopped_at = CURRENT_TIMESTAMP
		WHERE pid = ?
	`, "stopped", pid)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"pid":     pid,
		"status":  "stopped",
		"message": "Registry serve stopped",
	})
}

// GetRegistryStatus handles GET /api/serve/registry/:pid
func (h *Handler) GetRegistryStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract PID from path
	prefix := "/api/serve/registry/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	pidStr := r.URL.Path[len(prefix):]
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		http.Error(w, "Invalid PID", http.StatusBadRequest)
		return
	}

	// Check in-memory map first
	h.mu.RLock()
	managedProc, inMemory := h.processes[pid]
	if inMemory {
		managedProc.LogMu.Lock()
		logs := make([]string, len(managedProc.Logs))
		copy(logs, managedProc.Logs)
		managedProc.LogMu.Unlock()
		h.mu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"pid":       pid,
			"status":    "running",
			"startedAt": managedProc.StartedAt.Format(time.RFC3339),
			"logs":      logs,
		})
		return
	}
	h.mu.RUnlock()

	// Check database for historical record
	var serveType string
	var port int
	var argsJSON string
	var status string
	var startedAt, stoppedAt sql.NullString
	var exitReason sql.NullString

	row := h.db.QueryRow(`
		SELECT serve_type, port, args, status, started_at, stopped_at, exit_reason
		FROM serve_processes
		WHERE pid = ?
		ORDER BY started_at DESC
		LIMIT 1
	`, pid)

	err = row.Scan(&serveType, &port, &argsJSON, &status, &startedAt, &stoppedAt, &exitReason)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Process not found", http.StatusNotFound)
		} else {
			http.Error(w, "Database error", http.StatusInternalServerError)
		}
		return
	}

	response := map[string]interface{}{
		"pid":        pid,
		"serveType":  serveType,
		"port":       port,
		"status":     status,
		"startedAt":  startedAt.String,
		"stoppedAt":  stoppedAt.String,
		"exitReason": exitReason.String,
	}

	if argsJSON != "" {
		var args map[string]interface{}
		_ = json.Unmarshal([]byte(argsJSON), &args)
		response["args"] = args
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// ListRegistryProcesses handles GET /api/serve/registry
func (h *Handler) ListRegistryProcesses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	processes, err := h.queryProcesses("registry", r.URL.Query().Get("haul"))
	if err != nil {
		http.Error(w, "Query error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(processes)
}

// queryProcesses returns serve processes of a given type, optionally filtered by
// haul id, including the haul each process is bound to.
func (h *Handler) queryProcesses(serveType, haulFilter string) ([]map[string]interface{}, error) {
	query := `
		SELECT id, serve_type, pid, port, args, status, started_at, stopped_at, exit_reason, haul_id
		FROM serve_processes
		WHERE serve_type = ? AND COALESCE(role, 'manual') = 'manual'`
	queryArgs := []interface{}{serveType}
	if haulFilter != "" {
		if haulID, err := strconv.ParseInt(haulFilter, 10, 64); err == nil {
			query += ` AND haul_id = ?`
			queryArgs = append(queryArgs, haulID)
		}
	}
	query += ` ORDER BY started_at DESC`

	rows, err := h.db.Query(query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // close error not actionable; rows are fully read or abandoned

	processes := []map[string]interface{}{}
	for rows.Next() {
		var id, pid, port int
		var st, argsJSON, status string
		var startedAt, stoppedAt, exitReason sql.NullString
		var haulID sql.NullInt64

		if err := rows.Scan(&id, &st, &pid, &port, &argsJSON, &status, &startedAt, &stoppedAt, &exitReason, &haulID); err != nil {
			continue
		}

		proc := map[string]interface{}{
			"id":         id,
			"serveType":  st,
			"pid":        pid,
			"port":       port,
			"status":     status,
			"startedAt":  startedAt.String,
			"stoppedAt":  stoppedAt.String,
			"exitReason": exitReason.String,
		}
		if haulID.Valid {
			proc["haulId"] = haulID.Int64
		}
		if argsJSON != "" {
			var args map[string]interface{}
			_ = json.Unmarshal([]byte(argsJSON), &args)
			proc["args"] = args
		}
		processes = append(processes, proc)
	}
	return processes, rows.Err()
}

// ServeFileserver handles POST /api/serve/fileserver
func (h *Handler) ServeFileserver(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ServeFileserverRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// Resolve which haul's store to serve.
	haul, err := h.resolveHaul(r.Context(), req.HaulID)
	if err != nil {
		http.Error(w, "Failed to resolve haul: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Default to 5001: the UI already listens on 8080.
	port := req.Port
	if port == 0 {
		port = DefaultFileserverPort
	}

	// Prevent port collisions across concurrently served hauls.
	if h.portInUse(port) {
		http.Error(w, fmt.Sprintf("Port %d is already in use by another serve process", port), http.StatusConflict)
		return
	}

	// Build args for hauler store serve fileserver command, scoped to the haul.
	args := []string{"store", "serve", "fileserver", "--port", strconv.Itoa(port), "--store", haul.StoreDir}

	// Optional timeout
	if req.Timeout > 0 {
		args = append(args, "--timeout", strconv.Itoa(req.Timeout))
	}

	// Handle TLS configuration
	if req.AutoTLS {
		serveType := "fileserver"
		certPath, keyPath, err := h.certManager.GetOrGenerateCert(serveType)
		if err != nil {
			log.Printf("Error generating TLS certificate: %v", err)
			http.Error(w, fmt.Sprintf("Failed to generate certificate: %v", err), http.StatusInternalServerError)
			return
		}
		args = append(args, "--tls-cert", certPath, "--tls-key", keyPath)
	} else {
		// Optional TLS cert
		if req.TLSCert != "" {
			args = append(args, "--tls-cert", req.TLSCert)
		}

		// Optional TLS key
		if req.TLSKey != "" {
			args = append(args, "--tls-key", req.TLSKey)
		}
	}

	// Optional directory
	if req.Directory != "" {
		args = append(args, "--directory", req.Directory)
	}

	// Start the process
	cmd := exec.Command("hauler", args...)
	cmd.Dir = h.cfg.DataDir
	argsJSON, _ := json.Marshal(req)
	proc, err := h.launch(cmd, "fileserver", port, haul.ID, string(argsJSON))
	if err != nil {
		http.Error(w, fmt.Sprintf("Fileserver serve on port %d failed: %v", port, err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"pid":       proc.Process.Pid,
		"port":      port,
		"status":    "running",
		"startedAt": proc.StartedAt.Format(time.RFC3339),
		"message":   "Fileserver serve started",
		"haulId":    haul.ID,
	})
}

// StopFileserver handles DELETE /api/serve/fileserver/:pid
func (h *Handler) StopFileserver(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract PID from path
	// Path format: /api/serve/fileserver/:pid
	prefix := "/api/serve/fileserver/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	pidStr := r.URL.Path[len(prefix):]
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		http.Error(w, "Invalid PID", http.StatusBadRequest)
		return
	}

	h.mu.RLock()
	managedProc, exists := h.processes[pid]
	h.mu.RUnlock()

	if !exists {
		// Check database for historical record
		var status string
		row := h.db.QueryRow("SELECT status FROM serve_processes WHERE pid = ?", pid)
		_ = row.Scan(&status)
		if status == "stopped" {
			http.Error(w, "Process already stopped", http.StatusGone)
			return
		}
		http.Error(w, "Process not found", http.StatusNotFound)
		return
	}

	// Send SIGTERM for graceful shutdown
	if err := managedProc.Process.Signal(syscall.SIGTERM); err != nil {
		log.Printf("Error sending SIGTERM to process %d: %v", pid, err)
		http.Error(w, fmt.Sprintf("Failed to stop process: %v", err), http.StatusInternalServerError)
		return
	}

	// Update database immediately
	_, _ = h.db.Exec(`
		UPDATE serve_processes
		SET status = ?, stopped_at = CURRENT_TIMESTAMP
		WHERE pid = ?
	`, "stopped", pid)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"pid":     pid,
		"status":  "stopped",
		"message": "Fileserver serve stopped",
	})
}

// GetFileserverStatus handles GET /api/serve/fileserver/:pid
func (h *Handler) GetFileserverStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract PID from path
	prefix := "/api/serve/fileserver/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	pidStr := r.URL.Path[len(prefix):]
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		http.Error(w, "Invalid PID", http.StatusBadRequest)
		return
	}

	// Check in-memory map first
	h.mu.RLock()
	managedProc, inMemory := h.processes[pid]
	if inMemory {
		managedProc.LogMu.Lock()
		logs := make([]string, len(managedProc.Logs))
		copy(logs, managedProc.Logs)
		managedProc.LogMu.Unlock()
		h.mu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"pid":       pid,
			"status":    "running",
			"startedAt": managedProc.StartedAt.Format(time.RFC3339),
			"logs":      logs,
		})
		return
	}
	h.mu.RUnlock()

	// Check database for historical record
	var serveType string
	var port int
	var argsJSON string
	var status string
	var startedAt, stoppedAt sql.NullString
	var exitReason sql.NullString

	row := h.db.QueryRow(`
		SELECT serve_type, port, args, status, started_at, stopped_at, exit_reason
		FROM serve_processes
		WHERE pid = ?
		ORDER BY started_at DESC
		LIMIT 1
	`, pid)

	err = row.Scan(&serveType, &port, &argsJSON, &status, &startedAt, &stoppedAt, &exitReason)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Process not found", http.StatusNotFound)
		} else {
			http.Error(w, "Database error", http.StatusInternalServerError)
		}
		return
	}

	response := map[string]interface{}{
		"pid":        pid,
		"serveType":  serveType,
		"port":       port,
		"status":     status,
		"startedAt":  startedAt.String,
		"stoppedAt":  stoppedAt.String,
		"exitReason": exitReason.String,
	}

	if argsJSON != "" {
		var args map[string]interface{}
		_ = json.Unmarshal([]byte(argsJSON), &args)
		response["args"] = args
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// ListFileserverProcesses handles GET /api/serve/fileserver
func (h *Handler) ListFileserverProcesses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	processes, err := h.queryProcesses("fileserver", r.URL.Query().Get("haul"))
	if err != nil {
		http.Error(w, "Query error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(processes)
}

// RegisterRoutes registers the serve routes with the given mux
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/serve/registry", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			h.ServeRegistry(w, r)
		case http.MethodGet:
			h.ListRegistryProcesses(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/serve/registry/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.GetRegistryStatus(w, r)
		case http.MethodDelete:
			h.StopRegistry(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/api/serve/fileserver", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			h.ServeFileserver(w, r)
		case http.MethodGet:
			h.ListFileserverProcesses(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/serve/fileserver/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.GetFileserverStatus(w, r)
		case http.MethodDelete:
			h.StopFileserver(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}
