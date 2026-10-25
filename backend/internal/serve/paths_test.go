package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// argvHauler installs a fake hauler that writes its argv, one per line, to
// a file and exits 1, and returns a func that reads that argv back ("" if
// hauler never ran).
func argvHauler(t *testing.T) func() string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "argv")
	fakeHauler(t, `printf '%s\n' "$@" > '`+out+`'; exit 1`)
	return func() string {
		b, err := os.ReadFile(out)
		if os.IsNotExist(err) {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
}

type serveEndpoint struct {
	name  string
	path  string
	serve func(h *Handler, w http.ResponseWriter, r *http.Request)
}

var serveEndpoints = []serveEndpoint{
	{"registry", "/api/serve/registry", (*Handler).ServeRegistry},
	{"fileserver", "/api/serve/fileserver", (*Handler).ServeFileserver},
}

func post(e *testEnv, ep serveEndpoint, body string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	ep.serve(e.handler, rr, httptest.NewRequest(http.MethodPost, ep.path, strings.NewReader(body)))
	return rr
}

func jsonBody(t *testing.T, fields map[string]any) string {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// argAfter returns the argv value after flag, or "" if flag is absent.
func argAfter(argv, flag string) string {
	args := strings.Split(strings.TrimSpace(argv), "\n")
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A directory outside the haul's folder (or inside its store) is refused with
// 400 before hauler runs, on both endpoints.
func TestServe_DirectoryOutsideHaulRejected(t *testing.T) {
	for _, ep := range serveEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			argv := argvHauler(t)
			e := newTestEnv(t)
			haulDir := filepath.Dir(e.haul.StoreDir)
			outside := t.TempDir()

			// Symlinks inside the haul folder that point out of it.
			if err := os.Symlink(outside, filepath.Join(haulDir, "escape")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(outside, "missing"), filepath.Join(haulDir, "dangling")); err != nil {
				t.Fatal(err)
			}

			for _, dir := range []string{
				"/etc",
				"/",
				outside,
				e.cfg.DataDir,
				filepath.Join(e.cfg.DataDir, "certs"),
				"../../..",
				"../other-haul/store",
				haulDir,
				".",
				e.haul.StoreDir,
				filepath.Join(e.haul.StoreDir, "blobs"),
				"store",
				"escape",
				"escape/new",
				filepath.Join(haulDir, "escape", "new"),
				"dangling",
				"dangling/new",
				filepath.Join(haulDir, "x", "..", "..", "..", "etc"),
			} {
				rr := post(e, ep, jsonBody(t, map[string]any{"directory": dir}))
				if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "directory must be a folder inside") {
					t.Errorf("directory %q: status %d body %q, want 400 directory error", dir, rr.Code, rr.Body.String())
				}
			}
			if got := argv(); got != "" {
				t.Fatalf("hauler ran with %q; it must not run for a rejected directory", got)
			}
			assertNoProcessesStarted(t, e)
		})
	}
}

// A directory inside the haul folder reaches hauler as the resolved absolute path.
func TestServe_DirectoryInsideHaulAccepted(t *testing.T) {
	for _, ep := range serveEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			e := newTestEnv(t)
			haulDir := realPath(t, filepath.Dir(e.haul.StoreDir))
			for in, want := range map[string]string{
				"files":                                filepath.Join(haulDir, "files"),
				"files/nested/":                        filepath.Join(haulDir, "files", "nested"),
				filepath.Join(haulDir, "served"):       filepath.Join(haulDir, "served"),
				"archives":                             filepath.Join(haulDir, "archives"),
				filepath.Join(haulDir, "a", "..", "b"): filepath.Join(haulDir, "b"),
			} {
				argv := argvHauler(t)
				post(e, ep, jsonBody(t, map[string]any{"directory": in}))
				if got := argAfter(argv(), "--directory"); got != want {
					t.Errorf("directory %q: hauler got --directory %q, want %q", in, got, want)
				}
			}
		})
	}
}

// With no directory, the registry still gets its per-haul default and the
// fileserver gets no --directory flag (hauler's own default).
func TestServe_DefaultDirectoryUnchanged(t *testing.T) {
	e := newTestEnv(t)
	argv := argvHauler(t)
	post(e, serveEndpoints[0], `{}`)
	if got, want := argAfter(argv(), "--directory"), filepath.Join(filepath.Dir(e.haul.StoreDir), "registry"); got != want {
		t.Errorf("registry --directory = %q, want %q", got, want)
	}

	argv = argvHauler(t)
	post(e, serveEndpoints[1], `{}`)
	if got := argv(); got == "" || strings.Contains(got, "--directory") {
		t.Errorf("fileserver argv = %q, want hauler to run without --directory", got)
	}
}

// tlsCert and tlsKey must be regular files in DataDir/certs.
func TestServe_TLSPathsOutsideCertsRejected(t *testing.T) {
	for _, ep := range serveEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			argv := argvHauler(t)
			e := newTestEnv(t)
			certs := filepath.Join(e.cfg.DataDir, "certs")
			if err := os.MkdirAll(filepath.Join(certs, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "outside.pem")
			if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(certs, "link.pem")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(certs, "ok.pem"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}

			for _, field := range []string{"tlsCert", "tlsKey"} {
				for _, p := range []string{
					"/etc/passwd",
					outside,
					"../app.db",
					"link.pem",
					"missing.pem",
					"sub",
					certs,
					filepath.Join(e.cfg.DataDir, "t.db"),
				} {
					fields := map[string]any{"tlsCert": "ok.pem", "tlsKey": "ok.pem"}
					fields[field] = p
					rr := post(e, ep, jsonBody(t, fields))
					if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), field+" must be a file in") {
						t.Errorf("%s %q: status %d body %q, want 400 %s error", field, p, rr.Code, rr.Body.String(), field)
					}
				}
			}
			if got := argv(); got != "" {
				t.Fatalf("hauler ran with %q; it must not run for a rejected TLS path", got)
			}
			assertNoProcessesStarted(t, e)
		})
	}
}

func TestServe_TLSPathsInCertsAccepted(t *testing.T) {
	for _, ep := range serveEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			e := newTestEnv(t)
			certs := filepath.Join(e.cfg.DataDir, "certs")
			if err := os.MkdirAll(certs, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"my.crt", "my.key"} {
				if err := os.WriteFile(filepath.Join(certs, name), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			argv := argvHauler(t)
			post(e, ep, jsonBody(t, map[string]any{"tlsCert": "my.crt", "tlsKey": filepath.Join(certs, "my.key")}))
			got := argv()
			if c, want := argAfter(got, "--tls-cert"), filepath.Join(realPath(t, certs), "my.crt"); c != want {
				t.Errorf("--tls-cert = %q, want %q", c, want)
			}
			if k, want := argAfter(got, "--tls-key"), filepath.Join(realPath(t, certs), "my.key"); k != want {
				t.Errorf("--tls-key = %q, want %q", k, want)
			}
		})
	}
}

// Auto TLS ignores tlsCert and tlsKey, so stray values there are not an error.
func TestServe_AutoTLSIgnoresTLSPaths(t *testing.T) {
	e := newTestEnv(t)
	argv := argvHauler(t)
	post(e, serveEndpoints[1], jsonBody(t, map[string]any{"autoTls": true, "tlsCert": "/etc/passwd"}))
	if c := argAfter(argv(), "--tls-cert"); c != filepath.Join(e.cfg.DataDir, "certs", "fileserver.crt") {
		t.Errorf("--tls-cert = %q, want the generated fileserver cert", c)
	}
}

// Oversized bodies are refused with 413 before hauler runs.
func TestServe_BodyTooLarge(t *testing.T) {
	for _, ep := range serveEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			argv := argvHauler(t)
			e := newTestEnv(t)
			body := `{"directory":"files","haulId":0,"pad":"` + strings.Repeat("a", 1<<20) + `"}`
			rr := post(e, ep, body)
			if rr.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("status %d body %q, want 413", rr.Code, rr.Body.String())
			}
			if got := argv(); got != "" {
				t.Fatalf("hauler ran with %q", got)
			}
			assertNoProcessesStarted(t, e)
		})
	}
}

// The 64 KiB cap is exact: a body one byte over gets 413, and a body at the cap
// is still decoded and checked like any other request.
func TestServe_BodyCapBoundary(t *testing.T) {
	pad := func(prefix string, size int) string {
		return prefix + `,"pad":"` + strings.Repeat("a", size-len(prefix)-len(`,"pad":""}`)) + `"}`
	}
	for _, ep := range serveEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			argv := argvHauler(t)
			e := newTestEnv(t)
			if rr := post(e, ep, pad(`{"directory":"/etc"`, maxServeBody+1)); rr.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("cap+1: status %d body %q, want 413", rr.Code, rr.Body.String())
			}
			if rr := post(e, ep, pad(`{"directory":"/etc"`, maxServeBody)); rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "directory must be a folder inside") {
				t.Errorf("at cap: status %d body %q, want 400 directory error", rr.Code, rr.Body.String())
			}
			if got := argv(); got != "" {
				t.Fatalf("hauler ran with %q", got)
			}
			assertNoProcessesStarted(t, e)
		})
	}
}

// configFile is refused the same way under the body cap: 400 for a body at the
// cap, 413 once the body is over it. hauler never runs either way.
func TestServeRegistry_ConfigFileUnderBodyCap(t *testing.T) {
	ep := serveEndpoints[0]
	argv := argvHauler(t)
	e := newTestEnv(t)
	body := func(size int) string {
		prefix := `{"configFile":"/tmp/writable.yaml"`
		return prefix + `,"pad":"` + strings.Repeat("a", size-len(prefix)-len(`,"pad":""}`)) + `"}`
	}
	if rr := post(e, ep, body(maxServeBody)); rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "configFile is no longer supported") {
		t.Errorf("at cap: status %d body %q, want 400 configFile error", rr.Code, rr.Body.String())
	}
	if rr := post(e, ep, body(maxServeBody+1)); rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("cap+1: status %d body %q, want 413", rr.Code, rr.Body.String())
	}
	if got := argv(); got != "" {
		t.Fatalf("hauler ran with %q", got)
	}
	assertNoProcessesStarted(t, e)
}
