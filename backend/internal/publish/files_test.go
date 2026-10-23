package publish

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writePublishFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// dg returns a canonical sha256 digest derived from a readable label.
func dg(label string) string {
	sum := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// blobFile returns the blob path for a canonical digest, failing the test otherwise.
func blobFile(t *testing.T, dir, digest string) string {
	t.Helper()
	p, err := blobPath(dir, digest)
	if err != nil {
		t.Fatalf("blobPath(%q): %v", digest, err)
	}
	return p
}

func TestPublishedFiles(t *testing.T) {
	dir := t.TempDir()
	writePublishFixture(t, filepath.Join(dir, "index.json"), `{"manifests":[{"digest":"`+dg("missing")+`"},{"digest":"`+dg("invalid")+`"},{"digest":"`+dg("manifest")+`"}]}`)
	writePublishFixture(t, blobFile(t, dir, dg("invalid")), `{`)
	writePublishFixture(t, blobFile(t, dir, dg("manifest")), `{"layers":[
 {"digest":"`+dg("untitled")+`"},
 {"digest":"`+dg("content")+`","size":6,"annotations":{"org.opencontainers.image.title":"notes.txt"}},
 {"digest":"`+dg("duplicate")+`","size":99,"annotations":{"org.opencontainers.image.title":"notes.txt"}},
 {"digest":"`+dg("absent")+`","size":10,"annotations":{"org.opencontainers.image.title":"missing.tgz"}}]}`)
	writePublishFixture(t, blobFile(t, dir, dg("content")), "abcdef")
	want := []FileEntry{{Name: "notes.txt", Size: 6, Digest: dg("content")}, {Name: "missing.tgz", Size: 10, Digest: dg("absent")}}
	got, err := listFiles(dir)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("files=%#v err=%v", got, err)
	}
	digest, size, ok := findFileLayer(dir, "notes.txt")
	if !ok || digest != dg("content") || size != 6 {
		t.Fatalf("layer=%q %d %v", digest, size, ok)
	}
	w := httptest.NewRecorder()
	serveFileList(w, dir)
	var listing struct {
		Files []FileEntry `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/json" || !reflect.DeepEqual(listing.Files, want) {
		t.Fatalf("listing=%s", w.Body)
	}
	for _, tc := range []struct {
		name, rangeHeader string
		status            int
		body              string
	}{
		{"notes.txt", "", 200, "abcdef"}, {"notes.txt", "bytes=1-3", 206, "bcd"},
		{"unknown", "", 404, "file not found\n"}, {"missing.tgz", "", 404, "blob not found\n"},
		{"../secret", "", 400, "invalid name\n"}, {"a/b", "", 400, "invalid name\n"}, {`a\b`, "", 400, "invalid name\n"},
	} {
		t.Run(tc.name+tc.rangeHeader, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("Range", tc.rangeHeader)
			w := httptest.NewRecorder()
			serveFile(w, r, dir, tc.name)
			if w.Code != tc.status || w.Body.String() != tc.body {
				t.Fatalf("response=%d %q", w.Code, w.Body.String())
			}
			if tc.status == 200 || tc.status == 206 {
				if w.Header().Get("Content-Disposition") != `attachment; filename="notes.txt"` {
					t.Fatal(w.Header())
				}
			}
			if tc.status == 206 && w.Header().Get("Content-Range") != "bytes 1-3/6" {
				t.Fatal(w.Header())
			}
		})
	}
}

func TestFileIndexErrorsAndEmptyListing(t *testing.T) {
	for _, tc := range []string{"missing", "malformed", "directory"} {
		t.Run(tc, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "index.json")
			if tc == "malformed" {
				writePublishFixture(t, path, `{`)
			}
			if tc == "directory" {
				if err := os.Mkdir(path, 0755); err != nil {
					t.Fatal(err)
				}
			}
			w := httptest.NewRecorder()
			serveFileList(w, dir)
			if tc == "missing" {
				if w.Code != 200 || w.Body.String() != "{\"files\":[]}\n" {
					t.Fatalf("response=%d %s", w.Code, w.Body)
				}
			} else if w.Code != 500 {
				t.Fatalf("status=%d", w.Code)
			}
			if _, _, ok := findFileLayer(dir, "unknown"); ok {
				t.Fatal("unexpected file")
			}
		})
	}
}
