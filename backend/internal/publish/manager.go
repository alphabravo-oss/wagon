// Package publish exposes multiple hauls through Wagon's single front door:
// a host-routed reverse proxy for per-haul registries and path-routed file
// serving straight from each haul's store.
//
// Registries are started lazily: publishing a haul only records intent. The
// backing "hauler serve registry" subprocess is spawned on the first registry
// request for that haul's host and reaped after an idle period. This bounds the
// number of live subprocesses to the working set of hauls actually being pulled,
// rather than the total number of published hauls.
package publish

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
	"github.com/alphabravo-oss/wagon/backend/internal/hauls"
)

// published is a live, exposed haul: an internal readonly registry process plus
// the virtual hostname that routes to it.
type published struct {
	HaulID    int64
	Slug      string
	Hostname  string
	Port      int // internal registry port (127.0.0.1:Port)
	StartedAt time.Time
	lastUsed  time.Time // guarded by Manager.mu; drives idle reaping
	stopping  bool      // intentional stop (reap/unpublish); guarded by Manager.mu
	cmd       *exec.Cmd
}

// Manager owns the set of published hauls and their internal registry processes.
type Manager struct {
	cfg   *config.Config
	db    *sql.DB
	hauls *hauls.Service

	mu           sync.Mutex
	byHaul       map[int64]*published    // LIVE registry subprocesses only
	desired      map[int64]string        // haulID -> hostname that should stay published
	desiredHost  map[string]int64        // hostname -> haulID (reverse of desired)
	starting     map[int64]chan struct{} // in-flight lazy starts (single-flight)
	shuttingDown bool
	proxy        *httputil.ReverseProxy
	tls          *tlsState

	idleTimeout time.Duration
}

// NewManager creates a publish manager and starts the idle-registry reaper.
func NewManager(cfg *config.Config, db *sql.DB, haulSvc *hauls.Service) *Manager {
	m := &Manager{
		cfg:         cfg,
		db:          db,
		hauls:       haulSvc,
		byHaul:      make(map[int64]*published),
		desired:     make(map[int64]string),
		desiredHost: make(map[string]int64),
		starting:    make(map[int64]chan struct{}),
		idleTimeout: registryIdleTimeout(),
	}
	// Single reverse proxy whose Director resolves the target per request from
	// the incoming Host header.
	m.proxy = &httputil.ReverseProxy{
		Director:      m.director,
		FlushInterval: 250 * time.Millisecond, // stream blobs, don't buffer
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("registry proxy error for host %q: %v", r.Host, err)
			http.Error(w, "registry unavailable", http.StatusBadGateway)
		},
	}
	m.bootstrapTLS()
	go m.reapLoop()
	return m
}

// hostHaulKey is used to pass the resolved target through the request context.
type ctxKey string

const targetKey ctxKey = "publishTarget"

// director rewrites a proxied request to the internal registry resolved from Host.
func (m *Manager) director(r *http.Request) {
	if target, ok := r.Context().Value(targetKey).(string); ok {
		r.URL.Scheme = "http"
		r.URL.Host = target
	}
}

// registryDomain is the optional base domain for subdomain routing.
func (m *Manager) registryDomain() string {
	return os.Getenv("HAULER_UI_REGISTRY_DOMAIN")
}

// hostnameFor derives the virtual host for a haul: an explicit override, else
// "<slug>.<domain>" when a base domain is configured, else the bare slug.
func (m *Manager) hostnameFor(slug, override string) string {
	if override != "" {
		return strings.ToLower(override)
	}
	if d := m.registryDomain(); d != "" {
		return strings.ToLower(slug + "." + d)
	}
	return strings.ToLower(slug)
}

// registryIdleTimeout is how long a registry subprocess may sit idle (no
// requests) before it is reaped. Configurable via HAULER_UI_REGISTRY_IDLE.
func registryIdleTimeout() time.Duration {
	if v := os.Getenv("HAULER_UI_REGISTRY_IDLE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 5 * time.Minute
}

// freePort asks the OS for an unused TCP port.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitReady blocks until 127.0.0.1:port accepts a connection or the timeout
// elapses, so the first proxied request doesn't race the subprocess's bind.
func waitReady(port int, timeout time.Duration) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("registry at %s not ready within %s", addr, timeout)
}

// Publish records a haul as desired (published) but does NOT start its registry;
// the subprocess is spawned lazily on the first pull. Returns a descriptor for
// the API response (Port is 0 until the registry is actually running).
func (m *Manager) Publish(ctx context.Context, haulID int64, hostnameOverride string) (*published, error) {
	haul, err := m.hauls.Get(ctx, haulID)
	if err != nil {
		return nil, fmt.Errorf("resolving haul: %w", err)
	}
	hostname := m.hostnameFor(haul.Slug, hostnameOverride)

	m.mu.Lock()
	// Drop a stale hostname->haul mapping if the hostname is changing.
	if old, ok := m.desired[haulID]; ok && old != hostname {
		delete(m.desiredHost, old)
	}
	m.desired[haulID] = hostname
	m.desiredHost[hostname] = haulID
	live := m.byHaul[haulID]
	m.mu.Unlock()

	m.persistDesired(haulID, hostname, live)

	if live != nil {
		return live, nil
	}
	return &published{HaulID: haul.ID, Slug: haul.Slug, Hostname: hostname}, nil
}

// persistDesired writes/updates the serve_processes row for a published haul.
// An idle (not-yet-running) haul is recorded with a null pid and port 0.
func (m *Manager) persistDesired(haulID int64, hostname string, live *published) {
	_, _ = m.db.Exec(`DELETE FROM serve_processes WHERE haul_id = ? AND role = 'published'`, haulID)
	if live != nil {
		_, _ = m.db.Exec(`
			INSERT INTO serve_processes (serve_type, pid, port, args, status, haul_id, role, hostname)
			VALUES ('registry', ?, ?, '{}', 'running', ?, 'published', ?)`,
			live.cmd.Process.Pid, live.Port, haulID, hostname)
		return
	}
	_, _ = m.db.Exec(`
		INSERT INTO serve_processes (serve_type, pid, port, args, status, haul_id, role, hostname)
		VALUES ('registry', NULL, 0, '{}', 'idle', ?, 'published', ?)`,
		haulID, hostname)
}

// ensureRunning returns the live registry for a desired haul, starting it (and
// waiting for readiness) on demand. Concurrent callers for the same haul are
// single-flighted so only one subprocess is spawned.
func (m *Manager) ensureRunning(ctx context.Context, haulID int64) (*published, error) {
	for {
		m.mu.Lock()
		if m.shuttingDown {
			m.mu.Unlock()
			return nil, fmt.Errorf("shutting down")
		}
		if p, ok := m.byHaul[haulID]; ok {
			p.lastUsed = time.Now()
			m.mu.Unlock()
			return p, nil
		}
		hostname, desired := m.desired[haulID]
		if !desired {
			m.mu.Unlock()
			return nil, fmt.Errorf("haul %d is not published", haulID)
		}
		if ch, ok := m.starting[haulID]; ok {
			// Another goroutine is starting it; wait, then re-evaluate.
			m.mu.Unlock()
			<-ch
			continue
		}
		ch := make(chan struct{})
		m.starting[haulID] = ch
		m.mu.Unlock()

		p, err := m.startAndWait(ctx, haulID, hostname)

		m.mu.Lock()
		delete(m.starting, haulID)
		close(ch)
		m.mu.Unlock()
		return p, err
	}
}

// startAndWait spawns the registry subprocess and blocks until it is accepting
// connections, so the triggering request can be proxied immediately.
func (m *Manager) startAndWait(ctx context.Context, haulID int64, hostname string) (*published, error) {
	haul, err := m.hauls.Get(ctx, haulID)
	if err != nil {
		return nil, fmt.Errorf("resolving haul: %w", err)
	}
	p, err := m.startRegistry(haul, hostname)
	if err != nil {
		return nil, err
	}
	if err := waitReady(p.Port, 5*time.Second); err != nil {
		m.mu.Lock()
		if cur, ok := m.byHaul[haulID]; ok && cur == p {
			p.stopping = true
			delete(m.byHaul, haulID)
		}
		m.mu.Unlock()
		if p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
		}
		return nil, err
	}
	return p, nil
}

// loopbackRegistryConfig renders a distribution registry config that is
// equivalent to hauler's built-in default but binds the HTTP listener to
// loopback only. hauler 1.3.2's "store serve registry" has no bind-address flag
// and builds its config in code binding ":<port>" (all interfaces); passing
// --config overrides that so the internal registry is reachable only via the
// authenticated proxy on 127.0.0.1.
func loopbackRegistryConfig(registryDir string, port int) string {
	return fmt.Sprintf(`version: 0.1
log:
  level: info
storage:
  cache:
    blobdescriptor: inmemory
  filesystem:
    rootdirectory: %q
  maintenance:
    readonly:
      enabled: true
http:
  addr: 127.0.0.1:%d
  headers:
    X-Content-Type-Options: [nosniff]
validation:
  manifests:
    urls:
      allow:
      - .+
`, registryDir, port)
}

// writeRegistryConfig writes a loopback-only registry config file for a haul and
// returns its path.
func (m *Manager) writeRegistryConfig(haulID int64, registryDir string, port int) (string, error) {
	if err := os.MkdirAll(registryDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(registryDir, "registry-config.yaml")
	if err := os.WriteFile(path, []byte(loopbackRegistryConfig(registryDir, port)), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// startRegistry launches one internal registry process for a haul and begins
// monitoring it. The haul must already be recorded in m.desired.
func (m *Manager) startRegistry(haul *hauls.Haul, hostname string) (*published, error) {
	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("allocating port: %w", err)
	}

	registryDir := filepath.Join(filepath.Dir(haul.StoreDir), "registry")
	// Bind the internal registry to loopback only via a config file: the child
	// must be reachable solely through the authenticated proxy, never directly
	// on a public interface.
	configPath, err := m.writeRegistryConfig(haul.ID, registryDir, port)
	if err != nil {
		return nil, fmt.Errorf("writing registry config: %w", err)
	}
	args := []string{
		"store", "serve", "registry",
		"--readonly",
		"--port", fmt.Sprintf("%d", port),
		"--store", haul.StoreDir,
		"--directory", registryDir,
		"--config", configPath,
	}
	cmd := exec.Command("hauler", args...)
	cmd.Dir = m.cfg.DataDir
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting internal registry: %w", err)
	}

	now := time.Now()
	p := &published{
		HaulID:    haul.ID,
		Slug:      haul.Slug,
		Hostname:  hostname,
		Port:      port,
		StartedAt: now,
		lastUsed:  now,
		cmd:       cmd,
	}

	m.mu.Lock()
	m.byHaul[haul.ID] = p
	m.mu.Unlock()

	// Persist so we can surface state in the routes table.
	_, _ = m.db.Exec(`DELETE FROM serve_processes WHERE haul_id = ? AND role = 'published'`, haul.ID)
	if _, err := m.db.Exec(`
		INSERT INTO serve_processes (serve_type, pid, port, args, status, haul_id, role, hostname)
		VALUES ('registry', ?, ?, '{}', 'running', ?, 'published', ?)
	`, cmd.Process.Pid, port, haul.ID, hostname); err != nil {
		log.Printf("publish: failed to persist record for haul %d: %v", haul.ID, err)
	}

	go m.monitor(p)
	log.Printf("publish: started registry for haul %d (%s) at host %q -> 127.0.0.1:%d", haul.ID, haul.Slug, hostname, port)
	return p, nil
}

// monitor reaps the internal registry process. If it exited intentionally (idle
// reap, unpublish, or shutdown) it is not restarted; an unexpected crash of a
// still-in-use haul is restarted with backoff.
func (m *Manager) monitor(p *published) {
	_ = p.cmd.Wait()

	m.mu.Lock()
	if cur, ok := m.byHaul[p.HaulID]; ok && cur == p {
		delete(m.byHaul, p.HaulID)
	}
	intentional := p.stopping
	_, stillDesired := m.desired[p.HaulID]
	shuttingDown := m.shuttingDown
	m.mu.Unlock()

	if shuttingDown || intentional || !stillDesired {
		// Reaped-but-still-published hauls remain available for lazy restart, so
		// mark them idle rather than stopped.
		status := "stopped"
		if intentional && stillDesired && !shuttingDown {
			status = "idle"
		}
		_, _ = m.db.Exec(`UPDATE serve_processes SET status = ?, pid = NULL, stopped_at = CURRENT_TIMESTAMP WHERE haul_id = ? AND role = 'published'`, status, p.HaulID)
		return
	}

	// Unexpected exit while still in use: attempt to restart with backoff.
	log.Printf("publish: registry for haul %d (%s) exited unexpectedly; restarting", p.HaulID, p.Slug)
	for attempt := 1; attempt <= 3; attempt++ {
		time.Sleep(time.Duration(attempt) * time.Second)

		m.mu.Lock()
		hostname, want := m.desired[p.HaulID]
		down := m.shuttingDown
		m.mu.Unlock()
		if down || !want {
			return // unpublished or shutting down during backoff
		}

		haul, err := m.hauls.Get(context.Background(), p.HaulID)
		if err != nil {
			continue
		}
		if _, err := m.startRegistry(haul, hostname); err == nil {
			return
		}
		log.Printf("publish: restart attempt %d for haul %d failed", attempt, p.HaulID)
	}
	log.Printf("publish: giving up restarting haul %d after 3 attempts", p.HaulID)
	_, _ = m.db.Exec(`UPDATE serve_processes SET status = 'stopped', stopped_at = CURRENT_TIMESTAMP WHERE haul_id = ? AND role = 'published'`, p.HaulID)
}

// reapLoop periodically stops registry subprocesses that have been idle longer
// than the configured timeout. It exits once the manager is shutting down.
func (m *Manager) reapLoop() {
	interval := m.idleTimeout / 2
	if interval < 15*time.Second {
		interval = 15 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		if m.shuttingDown {
			m.mu.Unlock()
			return
		}
		now := time.Now()
		var idle []*published
		for _, p := range m.byHaul {
			if now.Sub(p.lastUsed) > m.idleTimeout {
				p.stopping = true
				delete(m.byHaul, p.HaulID)
				idle = append(idle, p)
			}
		}
		m.mu.Unlock()

		for _, p := range idle {
			if p.cmd != nil && p.cmd.Process != nil {
				_ = p.cmd.Process.Signal(syscall.SIGTERM)
			}
			log.Printf("publish: reaped idle registry for haul %d (%s)", p.HaulID, p.Slug)
		}
	}
}

// Unpublish removes a haul from the desired set and stops its registry if live.
func (m *Manager) Unpublish(ctx context.Context, haulID int64) error {
	m.mu.Lock()
	if hn, ok := m.desired[haulID]; ok {
		delete(m.desiredHost, hn)
	}
	delete(m.desired, haulID)
	p, ok := m.byHaul[haulID]
	if ok {
		p.stopping = true
		delete(m.byHaul, haulID)
	}
	m.mu.Unlock()

	if ok && p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
	}
	_, err := m.db.ExecContext(ctx, `DELETE FROM serve_processes WHERE haul_id = ? AND role = 'published'`, haulID)
	return err
}

// StopAll terminates every live registry process (used on graceful shutdown). It
// marks the manager as shutting down so monitors and the reaper do not restart.
func (m *Manager) StopAll() {
	m.mu.Lock()
	m.shuttingDown = true
	procs := make([]*published, 0, len(m.byHaul))
	for _, p := range m.byHaul {
		p.stopping = true
		procs = append(procs, p)
	}
	m.mu.Unlock()

	for _, p := range procs {
		if p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
		}
	}
	if len(procs) > 0 {
		log.Printf("publish: sent SIGTERM to %d internal registry process(es)", len(procs))
	}
}

// Route describes one published haul for the routes table.
type Route struct {
	HaulID    int64  `json:"haulId"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	Hostname  string `json:"hostname"`
	Port      int    `json:"port"`
	Status    string `json:"status"` // "active" (registry running) or "published" (idle)
	StartedAt string `json:"startedAt"`
}

// List returns one route per published (desired) haul, whether or not its
// registry is currently running.
func (m *Manager) List(ctx context.Context) []Route {
	type entry struct {
		id       int64
		hostname string
		live     *published
	}
	m.mu.Lock()
	entries := make([]entry, 0, len(m.desired))
	for id, hostname := range m.desired {
		entries = append(entries, entry{id: id, hostname: hostname, live: m.byHaul[id]})
	}
	m.mu.Unlock()

	routes := make([]Route, 0, len(entries))
	for _, e := range entries {
		name, slug := "", ""
		if h, err := m.hauls.Get(ctx, e.id); err == nil {
			name, slug = h.Name, h.Slug
		}
		route := Route{HaulID: e.id, Slug: slug, Name: name, Hostname: e.hostname, Status: "published"}
		if e.live != nil {
			route.Status = "active"
			route.Port = e.live.Port
			route.StartedAt = e.live.StartedAt.Format(time.RFC3339)
		}
		routes = append(routes, route)
	}
	return routes
}

// resolveHost maps an incoming Host header to a published haul id.
func (m *Manager) resolveHost(host string) (int64, bool) {
	host = strings.ToLower(host)
	if i := strings.IndexByte(host, ':'); i != -1 {
		host = host[:i] // strip port
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.desiredHost[host]
	return id, ok
}

// RegistryProxyHandler host-routes registry traffic to the matching haul's
// internal registry, starting it on demand. Mount on the dedicated listener.
func (m *Manager) RegistryProxyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		haulID, ok := m.resolveHost(r.Host)
		if !ok {
			http.Error(w, fmt.Sprintf("no published haul for host %q", r.Host), http.StatusNotFound)
			return
		}
		p, err := m.ensureRunning(r.Context(), haulID)
		if err != nil {
			log.Printf("publish: could not start registry for host %q: %v", r.Host, err)
			http.Error(w, "registry unavailable", http.StatusBadGateway)
			return
		}
		target := fmt.Sprintf("127.0.0.1:%d", p.Port)
		ctx := context.WithValue(r.Context(), targetKey, target)
		m.proxy.ServeHTTP(w, r.WithContext(ctx))
	})
}

// StartRegistryListener binds the single host-routed registry port and serves
// the proxy. Serves HTTPS using the configured (or self-signed) certificate so
// clusters can pull over TLS; only falls back to plain HTTP if no certificate
// could be prepared at all. Runs until the process exits.
func (m *Manager) StartRegistryListener(addr string) {
	if m.cfg.PublishAuthUser == "" && m.cfg.PublishAuthPassword == "" {
		log.Printf("WARNING: published registry/file endpoints are UNAUTHENTICATED (open); set HAULER_UI_PUBLISH_USER and HAULER_UI_PUBLISH_PASSWORD to require HTTP Basic auth")
	} else {
		log.Printf("published registry/file endpoints: HTTP Basic auth enforced")
	}
	srv := &http.Server{Addr: addr, Handler: m.requireAuth(m.RegistryProxyHandler())}
	if m.hasTLS() {
		srv.TLSConfig = &tls.Config{GetCertificate: m.getCertificate}
		log.Printf("registry proxy listening on %s (host-routed, TLS: %s)", addr, m.TLSStatus().Source)
		if err := srv.ListenAndServeTLS("", ""); err != nil {
			log.Printf("registry proxy listener stopped: %v", err)
		}
		return
	}
	log.Printf("registry proxy listening on %s (host-routed, plaintext)", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("registry proxy listener stopped: %v", err)
	}
}

// RestoreOnBoot restores the desired (published) set from the database without
// starting any registries; each is spawned lazily on first pull.
func (m *Manager) RestoreOnBoot(ctx context.Context) {
	rows, err := m.db.QueryContext(ctx, `SELECT DISTINCT haul_id, hostname FROM serve_processes WHERE role = 'published'`)
	if err != nil {
		log.Printf("publish restore: query failed: %v", err)
		return
	}
	type want struct {
		haulID   int64
		hostname sql.NullString
	}
	var wants []want
	for rows.Next() {
		var wnt want
		if err := rows.Scan(&wnt.haulID, &wnt.hostname); err == nil {
			wants = append(wants, wnt)
		}
	}
	rows.Close()

	restored := 0
	for _, wnt := range wants {
		if wnt.hostname.String == "" {
			continue
		}
		m.mu.Lock()
		m.desired[wnt.haulID] = wnt.hostname.String
		m.desiredHost[wnt.hostname.String] = wnt.haulID
		m.mu.Unlock()
		// Reset the row to idle; the registry starts on first pull.
		_, _ = m.db.ExecContext(ctx, `UPDATE serve_processes SET status = 'idle', pid = NULL WHERE haul_id = ? AND role = 'published'`, wnt.haulID)
		restored++
	}
	if restored > 0 {
		log.Printf("publish: restored %d published haul(s) (lazy start on first pull)", restored)
	}
}
