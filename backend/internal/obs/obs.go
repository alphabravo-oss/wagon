// Package obs provides observability for the Wagon server: structured
// logging setup, an HTTP instrumentation middleware, and a dependency-free
// Prometheus-format metrics endpoint.
//
// Metrics are exposed in the Prometheus text exposition format by hand rather
// than via prometheus/client_golang, keeping the binary dependency-free and
// fully airgap-friendly. If Go-runtime metrics are later wanted, swapping in the
// client library is a localized change to this package.
package obs

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Setup configures the default structured logger from the environment:
//   - HAULER_UI_LOG_FORMAT: "json" (default) or "text"
//   - HAULER_UI_LOG_LEVEL:  debug | info (default) | warn | error
//
// SetDefault also routes the standard library log package through this handler,
// so existing log.Printf calls become structured records automatically.
func Setup() {
	level := parseLevel(os.Getenv("HAULER_UI_LOG_LEVEL"))
	opts := &slog.HandlerOptions{Level: level}

	var h slog.Handler
	if strings.EqualFold(os.Getenv("HAULER_UI_LOG_FORMAT"), "text") {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(h))
	slog.SetLogLoggerLevel(level) // level for the bridged stdlib log package
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// --- metrics -----------------------------------------------------------------

// registry holds the process-wide metric state. Labels are kept low-cardinality
// (HTTP method + status code, job status) so the series set stays bounded.
var registry = struct {
	mu sync.Mutex

	reqTotal map[string]int64   // "method|code" -> count
	durSum   map[string]float64 // "method"      -> total seconds
	durCount map[string]int64   // "method"      -> observations
	jobs     map[string]int     // status        -> current count
	hauls    int                // currently published hauls
}{
	reqTotal: map[string]int64{},
	durSum:   map[string]float64{},
	durCount: map[string]int64{},
	jobs:     map[string]int{},
}

func observe(method string, code int, dur time.Duration) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.reqTotal[method+"|"+strconv.Itoa(code)]++
	registry.durSum[method] += dur.Seconds()
	registry.durCount[method]++
}

// SetJobs records the current number of queued and running jobs.
func SetJobs(queued, running int) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.jobs["queued"] = queued
	registry.jobs["running"] = running
}

// SetPublishedHauls records how many hauls are currently published.
func SetPublishedHauls(n int) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.hauls = n
}

// MetricsHandler serves the current metrics in Prometheus text exposition format.
func MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		registry.mu.Lock()
		defer registry.mu.Unlock()

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		var writeErr error
		write := func(format string, args ...any) {
			if writeErr != nil {
				return // client gone; skip further writes
			}
			_, writeErr = fmt.Fprintf(w, format, args...)
		}

		write("# HELP haulerui_http_requests_total Total HTTP requests handled.\n")
		write("# TYPE haulerui_http_requests_total counter\n")
		for _, k := range sortedKeys(registry.reqTotal) {
			method, code, _ := strings.Cut(k, "|")
			write("haulerui_http_requests_total{method=%q,code=%q} %d\n", method, code, registry.reqTotal[k])
		}

		write("# HELP haulerui_http_request_duration_seconds Request duration in seconds.\n")
		write("# TYPE haulerui_http_request_duration_seconds summary\n")
		for _, method := range sortedKeysF(registry.durSum) {
			write("haulerui_http_request_duration_seconds_sum{method=%q} %g\n", method, registry.durSum[method])
			write("haulerui_http_request_duration_seconds_count{method=%q} %d\n", method, registry.durCount[method])
		}

		write("# HELP haulerui_jobs Current jobs by status.\n")
		write("# TYPE haulerui_jobs gauge\n")
		for _, status := range sortedKeysI(registry.jobs) {
			write("haulerui_jobs{status=%q} %d\n", status, registry.jobs[status])
		}

		write("# HELP haulerui_published_hauls Number of currently published hauls.\n")
		write("# TYPE haulerui_published_hauls gauge\n")
		write("haulerui_published_hauls %d\n", registry.hauls)
	})
}

func sortedKeys(m map[string]int64) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
func sortedKeysF(m map[string]float64) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
func sortedKeysI(m map[string]int) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// --- instrumentation middleware ----------------------------------------------

// statusRecorder captures the response status code while transparently
// forwarding Flush so streaming responses (SSE job logs) keep working.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

// Flush forwards to the underlying ResponseWriter so SSE streaming is preserved.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Instrument records request metrics and emits a structured access log for each
// request. Only the URL path (never the raw query string, which can carry
// credentials) is logged.
func Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		dur := time.Since(start)
		observe(r.Method, rec.status, dur)
		slog.Info("http_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", dur.Milliseconds(),
			"remote", remoteHost(r),
		)
	})
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
