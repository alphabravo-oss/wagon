package auth

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
)

func authRequest(t *testing.T, handler http.HandlerFunc, method, path, body string, cookie *http.Cookie, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: status = %d, want %d; body = %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}

func assertAuthJSON(t *testing.T, w *httptest.ResponseRecorder, want map[string]any) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("JSON %s = %v, want %v", key, got[key], value)
		}
	}
}

func responseSessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "haulerci_session" {
			return cookie
		}
	}
	t.Fatal("missing haulerci_session cookie")
	return nil
}

func loginSession(t *testing.T, h *Handler, password string) *http.Cookie {
	t.Helper()
	body, err := json.Marshal(LoginRequest{Password: password})
	if err != nil {
		t.Fatal(err)
	}
	w := authRequest(t, h.Login, http.MethodPost, "/api/auth/login", string(body), nil, http.StatusOK)
	assertAuthJSON(t, w, map[string]any{"success": true})
	cookie := responseSessionCookie(t, w)
	if cookie.Value == "" || !h.manager.ValidateSession(cookie.Value) {
		t.Fatal("login must create a valid non-empty session")
	}
	return cookie
}

func TestHandlerLogin(t *testing.T) {
	m := NewManager(openAuthTestDB(t), &config.Config{UIPassword: "password-A"})
	h := NewHandler(m)
	authRequest(t, h.Login, http.MethodGet, "/api/auth/login", "", nil, http.StatusMethodNotAllowed)
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"bad JSON", `{`, http.StatusBadRequest},
		{"wrong password", `{"password":"wrong"}`, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := authRequest(t, h.Login, http.MethodPost, "/api/auth/login", tc.body, nil, tc.status)
			assertAuthJSON(t, w, map[string]any{"success": false})
			if len(w.Result().Cookies()) != 0 {
				t.Fatal("failed login must not set a cookie")
			}
		})
	}
	for _, transport := range []string{"HTTP", "forwarded HTTPS", "TLS"} {
		t.Run(transport, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"password-A"}`))
			if transport == "forwarded HTTPS" {
				r.Header.Set("X-Forwarded-Proto", "https")
			}
			if transport == "TLS" {
				r.TLS = &tls.ConnectionState{}
			}
			w := httptest.NewRecorder()
			h.Login(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			assertAuthJSON(t, w, map[string]any{"success": true})
			var body LoginResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if _, err := time.Parse(time.RFC3339, body.ExpiresAt); err != nil {
				t.Fatalf("invalid expiresAt: %v", err)
			}
			cookie := responseSessionCookie(t, w)
			if cookie.Value == "" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Secure != (transport != "HTTP") {
				t.Fatalf("unexpected session cookie: %+v", cookie)
			}
			if !m.ValidateSession(cookie.Value) {
				t.Fatal("login session must validate")
			}
		})
	}
}

func TestHandlerValidate(t *testing.T) {
	db := openAuthTestDB(t)
	m := NewManager(db, &config.Config{UIPassword: "password-A"})
	h := NewHandler(m)
	valid := loginSession(t, h, "password-A")
	expired := loginSession(t, h, "password-A")
	if _, err := db.Exec(`UPDATE sessions SET expires_at = ? WHERE token = ?`, time.Now().Add(-48*time.Hour), expired.Value); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
		valid  bool
	}{
		{"valid", valid, true},
		{"unknown", &http.Cookie{Name: "haulerci_session", Value: "unknown-token"}, false},
		{"missing", nil, false},
		{"expired", expired, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := ""
			if tc.cookie != nil {
				token = tc.cookie.Value
			}
			if got := m.ValidateSession(token); got != tc.valid {
				t.Errorf("ValidateSession = %v, want %v", got, tc.valid)
			}
			w := authRequest(t, h.Validate, http.MethodGet, "/api/auth/validate", "", tc.cookie, http.StatusOK)
			assertAuthJSON(t, w, map[string]any{"authenticated": tc.valid, "authEnabled": true})
		})
	}
	authRequest(t, h.Validate, http.MethodPost, "/api/auth/validate", "", valid, http.StatusMethodNotAllowed)
}

func TestHandlerLogout(t *testing.T) {
	m := NewManager(openAuthTestDB(t), &config.Config{UIPassword: "password-A"})
	h := NewHandler(m)
	cookie := loginSession(t, h, "password-A")
	authRequest(t, h.Logout, http.MethodGet, "/api/auth/logout", "", cookie, http.StatusMethodNotAllowed)
	if !m.ValidateSession(cookie.Value) {
		t.Fatal("wrong method must preserve session")
	}
	for _, session := range []*http.Cookie{cookie, nil} {
		w := authRequest(t, h.Logout, http.MethodPost, "/api/auth/logout", "", session, http.StatusOK)
		assertAuthJSON(t, w, map[string]any{"success": true})
		cleared := responseSessionCookie(t, w)
		if cleared.Value != "" || cleared.Expires.IsZero() || !cleared.Expires.Before(time.Now()) {
			t.Fatalf("cookie not cleared: %+v", cleared)
		}
	}
	if m.ValidateSession(cookie.Value) {
		t.Fatal("logged-out session must no longer validate")
	}
}

func TestHandlerPasswordChangeRevokesSession(t *testing.T) {
	db := openAuthTestDB(t)
	h := NewHandler(NewManager(db, &config.Config{UIPassword: "password-A"}))
	cookie := loginSession(t, h, "password-A")
	m := NewManager(db, &config.Config{UIPassword: "password-B"})
	h = NewHandler(m)
	if m.ValidateSession(cookie.Value) {
		t.Fatal("password change must invalidate old session")
	}
	w := authRequest(t, h.Validate, http.MethodGet, "/api/auth/validate", "", cookie, http.StatusOK)
	assertAuthJSON(t, w, map[string]any{"authenticated": false, "authEnabled": true})
	w = authRequest(t, h.Login, http.MethodPost, "/api/auth/login", `{"password":"password-A"}`, nil, http.StatusUnauthorized)
	assertAuthJSON(t, w, map[string]any{"success": false})
	loginSession(t, h, "password-B")
}

func TestManagerMiddleware(t *testing.T) {
	db := openAuthTestDB(t)
	m := NewManager(db, &config.Config{UIPassword: "password-A"})
	valid := loginSession(t, NewHandler(m), "password-A")
	expired := loginSession(t, NewHandler(m), "password-A")
	if _, err := db.Exec(`UPDATE sessions SET expires_at = ? WHERE token = ?`, time.Now().Add(-48*time.Hour), expired.Value); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path   string
		cookie *http.Cookie
		status int
	}{
		{"/api/hauls", nil, 401},
		{"/api/hauls", valid, 204},
		{"/api/hauls", expired, 401},
		{"/api/hauls", &http.Cookie{Name: "haulerci_session", Value: "unknown-token"}, 401},
		{"/api/authx", nil, 401},
		{"/hx/", nil, 401},
	}
	for _, path := range []string{"/api/auth/login", "/healthz", "/readyz", "/metrics", "/api/config", "/h/some-haul/file", "/assets/app.js"} {
		cases = append(cases, struct {
			path   string
			cookie *http.Cookie
			status int
		}{path, nil, 204})
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			called := false
			h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			}))
			authRequest(t, h.ServeHTTP, http.MethodGet, tc.path, "", tc.cookie, tc.status)
			if called != (tc.status == http.StatusNoContent) {
				t.Fatalf("next called = %v", called)
			}
		})
	}
}

func TestManagerDisabledAuthAndVerifyPassword(t *testing.T) {
	db := openAuthTestDB(t)
	m := NewManager(db, &config.Config{})
	if m.IsEnabled() {
		t.Fatal("empty password must disable auth")
	}
	h := NewHandler(m)
	w := authRequest(t, h.Login, http.MethodPost, "/api/auth/login", `{"password":"anything"}`, nil, http.StatusOK)
	assertAuthJSON(t, w, map[string]any{"success": true})
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("disabled auth must not set cookies")
	}
	w = authRequest(t, h.Validate, http.MethodGet, "/api/auth/validate", "", nil, http.StatusOK)
	assertAuthJSON(t, w, map[string]any{"authenticated": true, "authEnabled": false})
	next := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, path := range []string{"/api/hauls", "/private", "/api/auth/login"} {
		authRequest(t, next.ServeHTTP, http.MethodPost, path, "", nil, http.StatusNoContent)
	}
	enabled := NewManager(db, &config.Config{UIPassword: "password-A"})
	for _, password := range []string{"", "anything", "password-A", "Password-A", "password-A "} {
		if !m.VerifyPassword(password) {
			t.Errorf("disabled auth rejected %q", password)
		}
		if got := enabled.VerifyPassword(password); got != (password == "password-A") {
			t.Errorf("VerifyPassword(%q) = %v", password, got)
		}
	}
}

func TestHandlerRegisterRoutes(t *testing.T) {
	t.Setenv("HAULER_UI_LOGIN_RATE", "3")
	t.Setenv("HAULER_UI_TRUST_PROXY", "")
	h := NewHandler(NewManager(openAuthTestDB(t), &config.Config{UIPassword: "password-A"}))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	authRequest(t, mux.ServeHTTP, http.MethodGet, "/api/auth/login", "", nil, http.StatusMethodNotAllowed)
	for i := 0; i < 5; i++ {
		want := http.StatusUnauthorized
		if i >= 3 {
			want = http.StatusTooManyRequests
		}
		w := authRequest(t, mux.ServeHTTP, http.MethodPost, "/api/auth/login", `{"password":"wrong"}`, nil, want)
		if want == http.StatusTooManyRequests {
			seconds, err := strconv.Atoi(w.Header().Get("Retry-After"))
			if err != nil || seconds <= 0 {
				t.Fatalf("invalid Retry-After: %q", w.Header().Get("Retry-After"))
			}
		} else {
			assertAuthJSON(t, w, map[string]any{"success": false})
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"wrong"}`))
	r.RemoteAddr = "198.51.100.2:23456"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("different IP status = %d, want 401", w.Code)
	}
	w = authRequest(t, mux.ServeHTTP, http.MethodGet, "/api/auth/validate", "", nil, http.StatusOK)
	assertAuthJSON(t, w, map[string]any{"authenticated": false, "authEnabled": true})
	w = authRequest(t, mux.ServeHTTP, http.MethodPost, "/api/auth/logout", "", nil, http.StatusOK)
	assertAuthJSON(t, w, map[string]any{"success": true})
}

func TestRequestGuardAdditionalCases(t *testing.T) {
	h := RequestGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), []string{"HTTPS://PROXY.EXAMPLE/"})
	for _, tc := range []struct {
		name, method, origin, fetchSite string
		status                          int
	}{
		{"HEAD cross-origin", http.MethodHead, "https://evil.example", "cross-site", 204},
		{"OPTIONS cross-origin", http.MethodOptions, "https://evil.example", "cross-site", 204},
		{"fetch none", http.MethodPost, "", "none", 204},
		{"malformed origin", http.MethodPost, "://invalid", "", 403},
		{"trusted origin without fetch metadata", http.MethodPost, "https://proxy.example", "", 204},
		{"host case insensitive", http.MethodPost, "http://EXAMPLE.COM", "", 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://example.com/api/hauls", nil)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
		})
	}
}
