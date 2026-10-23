package obs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// resetMetrics gives the calling test an empty metrics registry and restores
// the previous state afterwards, so exact-count assertions hold under
// go test -count=N and regardless of what other tests in the package observed.
func resetMetrics(t *testing.T) {
	t.Helper()
	registry.mu.Lock()
	prevReq, prevSum, prevCount := registry.reqTotal, registry.durSum, registry.durCount
	prevJobs, prevHauls := registry.jobs, registry.hauls
	registry.reqTotal = map[string]int64{}
	registry.durSum = map[string]float64{}
	registry.durCount = map[string]int64{}
	registry.jobs = map[string]int{}
	registry.hauls = 0
	registry.mu.Unlock()

	t.Cleanup(func() {
		registry.mu.Lock()
		defer registry.mu.Unlock()
		registry.reqTotal, registry.durSum, registry.durCount = prevReq, prevSum, prevCount
		registry.jobs, registry.hauls = prevJobs, prevHauls
	})
}

func TestMetricsExposition(t *testing.T) {
	resetMetrics(t)
	observe("GET", 200, 150*time.Millisecond)
	observe("GET", 200, 50*time.Millisecond)
	observe("POST", 500, 10*time.Millisecond)
	SetJobs(3, 1)
	SetPublishedHauls(2)

	rr := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	body := rr.Body.String()

	for _, want := range []string{
		`haulerui_http_requests_total{method="GET",code="200"} 2`,
		`haulerui_http_requests_total{method="POST",code="500"} 1`,
		`haulerui_http_request_duration_seconds_count{method="GET"} 2`,
		`haulerui_jobs{status="queued"} 3`,
		`haulerui_jobs{status="running"} 1`,
		`haulerui_published_hauls 2`,
		"# TYPE haulerui_http_requests_total counter",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q\n---\n%s", want, body)
		}
	}
}

// flushRecorder records whether Flush reached the underlying writer.
type flushRecorder struct {
	http.ResponseWriter
	flushed bool
}

func (f *flushRecorder) Flush() { f.flushed = true }

// TestInstrumentPreservesFlush guards against the middleware's ResponseWriter
// wrapper swallowing Flush, which would break SSE job-log streaming.
func TestInstrumentPreservesFlush(t *testing.T) {
	fr := &flushRecorder{ResponseWriter: httptest.NewRecorder()}
	h := Instrument(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(202)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("wrapped ResponseWriter does not implement http.Flusher")
		}
		flusher.Flush()
	}))
	h.ServeHTTP(fr, httptest.NewRequest("GET", "/api/jobs/1/stream", nil))
	if !fr.flushed {
		t.Fatal("Flush did not reach the underlying ResponseWriter")
	}
}
