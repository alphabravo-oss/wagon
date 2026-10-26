package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// imagesWithKey is an Images manifest whose image names key as its cosign key,
// or, with annotation set, names it in the hauler.dev/key annotation instead.
func imagesWithKey(key string, annotation bool) string {
	m := "apiVersion: content.hauler.cattle.io/v1\nkind: Images\nmetadata:\n  name: i\n"
	if annotation {
		m += "  annotations:\n    hauler.dev/key: " + key + "\n"
	}
	m += "spec:\n  images:\n    - name: docker.io/library/busybox:1\n"
	if !annotation {
		m += "      key: " + key + "\n"
	}
	return m
}

// newCard111Fixture adds <data>/certs to the card 69 fixture, with a key file,
// a folder, and a symlink that points outside.
func newCard111Fixture(t *testing.T) (*card69Fixture, string, func() int) {
	t.Helper()
	f, jobs := newCard69Fixture(t)
	certs := filepath.Join(f.dataDir, "certs")
	if err := os.MkdirAll(filepath.Join(certs, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certs, "cosign.pub"), []byte("key"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.outside, filepath.Join(certs, "link-out")); err != nil {
		t.Fatal(err)
	}
	return f, certs, jobs
}

// A manifest's cosign key may be a file in certs, where the request's key
// lives, as well as in imports. hauler only verifies with it and returns
// nothing of its content.
func TestSync_ManifestKeyInCertsAccepted(t *testing.T) {
	for _, key := range []string{"CERTS/cosign.pub", "certs/cosign.pub", "IMPORTS/ok.txt", "imports/ok.txt"} {
		for _, annotation := range []bool{false, true} {
			f, certs, _ := newCard111Fixture(t)
			k := strings.NewReplacer("CERTS", certs, "IMPORTS", f.imports).Replace(key)
			w := postStore(f.h, "sync", mustJSON(t, map[string]any{"manifestYaml": imagesWithKey(k, annotation)}))
			if w.Code != 202 {
				t.Errorf("key %s (annotation=%v): status %d body %q, want 202", key, annotation, w.Code, w.Body.String())
			}
		}
	}
}

// A key must be an existing regular file in imports or certs. Anything else,
// including a folder, is a 400 before a job exists, and the error names no
// host path.
func TestSync_ManifestKeyNotAFileRejected(t *testing.T) {
	const want = "must be a file in Wagon's imports folder (<data>/imports) or certs folder (<data>/certs)"
	for _, key := range []string{
		"IMPORTS/sub", "CERTS/sub", "CERTS", "certs", "CERTS/missing.pub", "CERTS/link-out",
		"CERTS/../test.db", "OUTSIDE", "/etc/passwd", "~/cosign.pub",
	} {
		for _, annotation := range []bool{false, true} {
			f, certs, jobs := newCard111Fixture(t)
			k := strings.NewReplacer("CERTS", certs, "IMPORTS", f.imports, "OUTSIDE", f.outside).Replace(key)
			w := postStore(f.h, "sync", mustJSON(t, map[string]any{"manifestYaml": imagesWithKey(k, annotation)}))
			if w.Code != 400 || !strings.Contains(w.Body.String(), want) {
				t.Errorf("key %s (annotation=%v): status %d body %q, want 400 %q", key, annotation, w.Code, w.Body.String(), want)
			}
			if strings.Contains(w.Body.String(), f.dataDir) || strings.Contains(w.Body.String(), f.outsideDir) {
				t.Errorf("key %s: 400 body names a host path: %q", key, w.Body.String())
			}
			if n := jobs(); n != 0 {
				t.Errorf("key %s: expected no jobs, got %d", key, n)
			}
		}
	}
}

// certs holds Serve's TLS private key, so a Files path there stays a 400:
// hauler would copy the file into the store, where users can read it back.
func TestSync_ManifestFilesInCertsRejected(t *testing.T) {
	for _, p := range []string{"CERTS/cosign.pub", "certs/cosign.pub"} {
		f, certs, jobs := newCard111Fixture(t)
		yaml := filesManifest(strings.ReplaceAll(p, "CERTS", certs))
		w := postStore(f.h, "sync", mustJSON(t, map[string]any{"manifestYaml": yaml}))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "files[0].path must be a file or folder in Wagon's imports folder") {
			t.Errorf("%s: status %d body %q, want 400", p, w.Code, w.Body.String())
		}
		if n := jobs(); n != 0 {
			t.Errorf("%s: expected no jobs, got %d", p, n)
		}
	}
}
