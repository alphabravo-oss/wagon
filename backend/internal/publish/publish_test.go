package publish

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
)

// okHandler is a trivial next handler that writes 200.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func TestRequireAuth_OpenWhenNoCreds(t *testing.T) {
	m := &Manager{cfg: &config.Config{PublishAuthUser: "", PublishAuthPassword: ""}}

	// The wrapped handler must be returned unchanged (pass-through).
	next := okHandler()
	got := m.requireAuth(next)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	rr := httptest.NewRecorder()
	got.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("open mode: expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != "ok" {
		t.Fatalf("open mode: expected next handler body %q, got %q", "ok", rr.Body.String())
	}
}

func TestRequireAuth_WithCreds(t *testing.T) {
	const user, pass = "admin", "s3cret"
	m := &Manager{cfg: &config.Config{PublishAuthUser: user, PublishAuthPassword: pass}}
	handler := m.requireAuth(okHandler())

	t.Run("no auth header -> 401 with challenge", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rr.Code)
		}
		if got := rr.Header().Get("WWW-Authenticate"); got == "" {
			t.Fatalf("expected WWW-Authenticate header, got empty")
		}
	})

	t.Run("wrong creds -> 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		req.SetBasicAuth("admin", "wrong")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for wrong creds, got %d", rr.Code)
		}
	})

	t.Run("wrong user -> 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		req.SetBasicAuth("nope", pass)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for wrong user, got %d", rr.Code)
		}
	})

	t.Run("correct creds -> 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		req.SetBasicAuth(user, pass)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200 for correct creds, got %d", rr.Code)
		}
		if rr.Body.String() != "ok" {
			t.Fatalf("expected next handler body %q, got %q", "ok", rr.Body.String())
		}
	})
}

func TestHostnameFor(t *testing.T) {
	m := &Manager{cfg: &config.Config{}}

	t.Run("override wins and is lowercased", func(t *testing.T) {
		t.Setenv("HAULER_UI_REGISTRY_DOMAIN", "example.com")
		if got := m.hostnameFor("myslug", "Custom.Host"); got != "custom.host" {
			t.Fatalf("expected override to win (lowercased), got %q", got)
		}
	})

	t.Run("domain set -> slug.domain", func(t *testing.T) {
		t.Setenv("HAULER_UI_REGISTRY_DOMAIN", "example.com")
		if got := m.hostnameFor("MySlug", ""); got != "myslug.example.com" {
			t.Fatalf("expected %q, got %q", "myslug.example.com", got)
		}
	})

	t.Run("no domain, no override -> bare lowercased slug", func(t *testing.T) {
		t.Setenv("HAULER_UI_REGISTRY_DOMAIN", "")
		if got := m.hostnameFor("MySlug", ""); got != "myslug" {
			t.Fatalf("expected bare lowercased slug %q, got %q", "myslug", got)
		}
	})
}

func TestResolveHost(t *testing.T) {
	// resolveHost matches against the desired (published) host set, whether or
	// not the backing registry is currently running.
	m := &Manager{
		desiredHost: map[string]int64{"demo.example.com": 1},
	}

	t.Run("strips port suffix and matches", func(t *testing.T) {
		id, ok := m.resolveHost("demo.example.com:8443")
		if !ok || id != 1 {
			t.Fatalf("expected haul 1 to resolve, got id=%d ok=%v", id, ok)
		}
	})

	t.Run("case-insensitive match", func(t *testing.T) {
		id, ok := m.resolveHost("DEMO.Example.COM")
		if !ok || id != 1 {
			t.Fatalf("expected case-insensitive match to haul 1, got id=%d ok=%v", id, ok)
		}
	})

	t.Run("exact host without port matches", func(t *testing.T) {
		if _, ok := m.resolveHost("demo.example.com"); !ok {
			t.Fatalf("expected exact host to match")
		}
	})

	t.Run("unknown host -> ok=false", func(t *testing.T) {
		if id, ok := m.resolveHost("unknown.example.com"); ok || id != 0 {
			t.Fatalf("expected unknown host to not resolve, got ok=%v id=%d", ok, id)
		}
	})
}

func TestFreePort(t *testing.T) {
	port, err := freePort()
	if err != nil {
		t.Fatalf("freePort returned error: %v", err)
	}
	if port <= 0 {
		t.Fatalf("expected a positive port, got %d", port)
	}
}

func TestPublishRejectsMalformedBody(t *testing.T) {
	h := &Handler{} // the body is validated before the manager is used
	r := httptest.NewRequest(http.MethodPost, "/api/publish/1", strings.NewReader("hostname=x"))
	w := httptest.NewRecorder()
	h.handleByID(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed publish body: status = %d, want 400", w.Code)
	}
}
