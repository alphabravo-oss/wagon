package auth

import (
	"net/http"
	"strings"
	"testing"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
)

// newShellTestServer wires the auth middleware the way main.go does: the SPA
// shell at "/", its icons, the auth routes, and a protected API route.
func newShellTestServer(t *testing.T) (http.Handler, *Handler) {
	t.Helper()
	t.Setenv("HAULER_UI_LOGIN_RATE", "100")
	m := NewManager(openAuthTestDB(t), &config.Config{UIPassword: "password-A"})
	h := NewHandler(m)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	mux.HandleFunc("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><div id=\"root\"></div>"))
	})
	for _, icon := range []string{"/favicon.svg", "/wagon-logo.svg"} {
		mux.HandleFunc(icon, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte("<svg/>"))
		})
	}
	return m.Middleware(mux), h
}

func TestSPAShellLoadsWithoutSession(t *testing.T) {
	srv, _ := newShellTestServer(t)
	w := authRequest(t, srv.ServeHTTP, http.MethodGet, "/", "", nil, http.StatusOK)
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("GET / Content-Type = %q, want text/html", ct)
	}
	authRequest(t, srv.ServeHTTP, http.MethodHead, "/", "", nil, http.StatusOK)
	for _, icon := range []string{"/favicon.svg", "/wagon-logo.svg"} {
		authRequest(t, srv.ServeHTTP, http.MethodGet, icon, "", nil, http.StatusOK)
	}
}

func TestSPAShellDoesNotWidenAuth(t *testing.T) {
	srv, _ := newShellTestServer(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/jobs"},
		{http.MethodGet, "/api/auth/validate"},
		{http.MethodGet, "/api/hauls"},
		{http.MethodGet, "/index.html"},
		{http.MethodGet, "/private"},
		{http.MethodGet, "/favicon.svgx"},
		{http.MethodGet, "/wagon-logo.svg/x"},
		{http.MethodPost, "/"},
		{http.MethodPut, "/"},
		{http.MethodDelete, "/favicon.svg"},
		// Method matching is exact and case-sensitive: "get" is not GET.
		{"get", "/"},
		// Path matching is exact: no cleaning, no case folding.
		{http.MethodGet, "//"},
		{http.MethodGet, "/./"},
		{http.MethodGet, "/FAVICON.SVG"},
		// Only GET and HEAD are public, so OPTIONS and TRACE stay protected.
		{http.MethodOptions, "/"},
		{http.MethodOptions, "/favicon.svg"},
		{http.MethodOptions, "/wagon-logo.svg"},
		{http.MethodTrace, "/"},
		{http.MethodTrace, "/favicon.svg"},
		{http.MethodTrace, "/wagon-logo.svg"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			authRequest(t, srv.ServeHTTP, tc.method, tc.path, "", nil, http.StatusUnauthorized)
		})
	}
}

func TestLoginCookieUnlocksAPI(t *testing.T) {
	srv, _ := newShellTestServer(t)
	authRequest(t, srv.ServeHTTP, http.MethodGet, "/api/jobs", "", nil, http.StatusUnauthorized)
	authRequest(t, srv.ServeHTTP, http.MethodPost, "/api/auth/login", `{"password":"wrong"}`, nil, http.StatusUnauthorized)
	w := authRequest(t, srv.ServeHTTP, http.MethodPost, "/api/auth/login", `{"password":"password-A"}`, nil, http.StatusOK)
	cookie := responseSessionCookie(t, w)
	w = authRequest(t, srv.ServeHTTP, http.MethodGet, "/api/jobs", "", cookie, http.StatusOK)
	if got := strings.TrimSpace(w.Body.String()); got != "[]" {
		t.Fatalf("/api/jobs body = %q", got)
	}
}
