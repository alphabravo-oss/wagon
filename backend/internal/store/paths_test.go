package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func postStore(h *Handler, endpoint, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodPost, "/api/store/"+endpoint, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func jobCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// onlyJobArgs returns the args of the single job the request created.
func onlyJobArgs(t *testing.T, h *Handler, db *sql.DB) []string {
	t.Helper()
	var id int64
	if err := db.QueryRow("SELECT id FROM jobs").Scan(&id); err != nil {
		t.Fatalf("expected one job: %v", err)
	}
	job, err := h.JobRunner.GetJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return job.Args
}

func realDir(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// expectRefused checks a 400 with wantMsg that names no host path, and that no
// job was created.
func expectRefused(t *testing.T, w *httptest.ResponseRecorder, db *sql.DB, dataDir, input, wantMsg string) {
	t.Helper()
	body := w.Body.String()
	if w.Code != http.StatusBadRequest || !strings.Contains(body, wantMsg) {
		t.Errorf("%q: status %d body %q, want 400 %q", input, w.Code, body, wantMsg)
	}
	if strings.Contains(body, dataDir) || strings.Contains(body, realDir(t, dataDir)) {
		t.Errorf("%q: 400 body names the data folder: %q", input, body)
	}
	if n := jobCount(t, db); n != 0 {
		t.Errorf("%q: expected no jobs, got %d", input, n)
	}
}

// filePath is read by hauler as root, so only files in <DataDir>/imports are
// accepted; anything else, including a symlink out of imports, is a 400.
func TestAddFile_FilePathOutsideImportsRejected(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "host_secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		outside,
		"/etc/passwd",
		"../test.db",
		"../hauls/default/store/oci-layout",
		"link-out",
		"sub",
		"missing.txt",
		".",
	} {
		h, db := setupTestHandler(t)
		imports := filepath.Join(h.Cfg.DataDir, "imports")
		if err := os.MkdirAll(filepath.Join(imports, "sub"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(imports, "link-out")); err != nil {
			t.Fatal(err)
		}
		w := postStore(h, "add-file", mustJSON(t, map[string]string{"filePath": p}))
		expectRefused(t, w, db, h.Cfg.DataDir, p, "filePath must be a file in Wagon's imports folder")
	}
}

func TestAddFile_FilePathInImportsAccepted(t *testing.T) {
	for _, rel := range []bool{true, false} {
		h, db := setupTestHandler(t)
		imports := filepath.Join(h.Cfg.DataDir, "imports")
		if err := os.MkdirAll(imports, 0755); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(imports, "config.yaml")
		if err := os.WriteFile(file, []byte("a: 1"), 0644); err != nil {
			t.Fatal(err)
		}
		p := file
		if rel {
			p = "config.yaml"
		}
		w := postStore(h, "add-file", mustJSON(t, map[string]string{"filePath": p}))
		if w.Code != http.StatusAccepted {
			t.Fatalf("%q: status %d body %q", p, w.Code, w.Body.String())
		}
		if args := onlyJobArgs(t, h, db); args[3] != realDir(t, file) {
			t.Errorf("%q: hauler got %q, want %q", p, args[3], realDir(t, file))
		}
	}
}

// hauler reads a url without a scheme, or with file://, as a local path, so
// only http and https URLs with a host are accepted.
func TestAddFile_URLNotRemoteRejected(t *testing.T) {
	for _, u := range []string{
		"/etc/host_secret.txt",
		"etc/passwd",
		"../test.db",
		"file:///etc/host_secret.txt",
		"FILE:///etc/host_secret.txt",
		"file://localhost/etc/passwd",
		"http:///etc/passwd",
		"https://",
		"http://:80/f",
		"http:/etc/passwd",
		"ftp://example.com/f",
		"s3://bucket/f",
		"git::https://example.com/repo",
		"oci://example.com/f",
		"example.com/f",
	} {
		h, db := setupTestHandler(t)
		w := postStore(h, "add-file", mustJSON(t, map[string]string{"url": u}))
		expectRefused(t, w, db, h.Cfg.DataDir, u, "url must be an http:// or https:// URL with a host")
	}
}

func TestAddFile_URLRemoteAccepted(t *testing.T) {
	for _, u := range []string{"https://example.com/f.txt", "http://10.0.0.5:8080/a/b?x=1", "HTTPS://example.com/f"} {
		h, db := setupTestHandler(t)
		w := postStore(h, "add-file", mustJSON(t, map[string]string{"url": u}))
		if w.Code != http.StatusAccepted {
			t.Fatalf("%q: status %d body %q", u, w.Code, w.Body.String())
		}
		if args := onlyJobArgs(t, h, db); args[3] != u {
			t.Errorf("%q: hauler got %q", u, args[3])
		}
	}
}

// The add-file name becomes a file name when hauler serves or extracts it.
func TestAddFile_NameWithPathPartsRejected(t *testing.T) {
	for _, name := range []string{"../../etc/cron.d/x", "a/b", `a\b`, "..", "."} {
		h, db := setupTestHandler(t)
		w := postStore(h, "add-file", mustJSON(t, map[string]string{"url": "https://example.com/f", "name": name}))
		expectRefused(t, w, db, h.Cfg.DataDir, name, "name must be a plain file name")
	}

	for _, name := range []string{"config-1.yaml", "v1..2.tgz", "..hidden"} {
		h, _ := setupTestHandler(t)
		w := postStore(h, "add-file", mustJSON(t, map[string]string{"url": "https://example.com/f", "name": name}))
		if w.Code != http.StatusAccepted {
			t.Errorf("%q: status %d body %q", name, w.Code, w.Body.String())
		}
	}
}

// outsideHaulDirs are folders hauler must never write into for h's default
// haul, given as absolute paths or relative to the haul's folder. It plants
// <haul>/link-out, a symlink to a folder outside the haul.
func outsideHaulDirs(t *testing.T, h *Handler) []string {
	t.Helper()
	haulDir := filepath.Dir(defaultStoreDir(t, h))
	if err := os.Symlink(t.TempDir(), filepath.Join(haulDir, "link-out")); err != nil {
		t.Fatal(err)
	}
	return []string{
		"/etc/cron.d",
		"/tmp/writetarget",
		h.Cfg.DataDir,
		filepath.Join(h.Cfg.DataDir, "hauls", "other"),
		"../other",
		"..",
		".",
		"",
		"store",
		"store/blobs",
		"archives",
		"archives/x",
		"registry",
		"registry/docker",
		"link-out",
		"link-out/x",
	}
}

func TestExtract_OutputDirOutsideHaulRejected(t *testing.T) {
	probe, _ := setupTestHandler(t)
	for i := range outsideHaulDirs(t, probe) {
		h, db := setupTestHandler(t)
		dir := outsideHaulDirs(t, h)[i]
		if dir == "" {
			continue // empty means the default, covered below
		}
		w := postStore(h, "extract", mustJSON(t, map[string]string{"artifactRef": "hauler/f:latest", "outputDir": dir}))
		expectRefused(t, w, db, h.Cfg.DataDir, dir, "outputDir must be a folder inside the haul's folder")
	}
}

func TestExtract_OutputDirInsideHaul(t *testing.T) {
	for _, tc := range []struct{ outputDir, want string }{
		{"", "extracted"},
		{"out/files", "out/files"},
	} {
		h, db := setupTestHandler(t)
		haulDir := realDir(t, filepath.Dir(defaultStoreDir(t, h)))
		if tc.outputDir == "out/files" {
			tc.outputDir = filepath.Join(haulDir, tc.outputDir)
		}
		w := postStore(h, "extract", mustJSON(t, map[string]string{"artifactRef": "hauler/f:latest", "outputDir": tc.outputDir}))
		if w.Code != http.StatusAccepted {
			t.Fatalf("%q: status %d body %q", tc.outputDir, w.Code, w.Body.String())
		}
		want := filepath.Join(haulDir, tc.want)
		if got := argAfter(onlyJobArgs(t, h, db), "--output"); got != want {
			t.Errorf("%q: --output %q, want %q", tc.outputDir, got, want)
		}
	}
}

func TestCopy_DirTargetOutsideHaulRejected(t *testing.T) {
	probe, _ := setupTestHandler(t)
	for i := range outsideHaulDirs(t, probe) {
		h, db := setupTestHandler(t)
		dir := outsideHaulDirs(t, h)[i]
		w := postStore(h, "copy", mustJSON(t, map[string]string{"target": "dir://" + dir}))
		expectRefused(t, w, db, h.Cfg.DataDir, "dir://"+dir, "dir:// target must be a folder inside the haul's folder")
	}
}

// Bodies over the cap get 413 and create no job.
func TestStore_BodyTooLarge(t *testing.T) {
	big := `{"haulId":0,"x":"` + strings.Repeat("a", maxStoreBody) + `"}`
	for _, ep := range []string{"add-image", "add-chart", "add-file", "sync", "save", "load", "extract", "copy", "remove"} {
		h, db := setupTestHandler(t)
		w := postStore(h, ep, big)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: status %d, want 413", ep, w.Code)
		}
		if n := jobCount(t, db); n != 0 {
			t.Errorf("%s: expected no jobs, got %d", ep, n)
		}
	}
}

// argAfter returns the value after flag in args, or "" if flag is absent.
func argAfter(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}
