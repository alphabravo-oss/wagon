package publish

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A manifest whose layer digest is not a canonical sha256 digest must not be
// able to read a file outside the store's blob directory.
func TestNonCanonicalDigestCannotEscapeStore(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, "store")
	const canary = "CANARY-outside-the-store"
	if err := os.WriteFile(filepath.Join(root, "outside.txt"), []byte(canary), 0600); err != nil {
		t.Fatal(err)
	}

	const manifestHex = "1111111111111111111111111111111111111111111111111111111111111111"
	writePublishFixture(t, filepath.Join(store, "index.json"), `{"manifests":[{"digest":"sha256:`+manifestHex+`"}]}`)
	writePublishFixture(t, filepath.Join(store, "blobs", "sha256", manifestHex), `{"layers":[
 {"digest":"sha256:../../../outside.txt","size":24,"annotations":{"org.opencontainers.image.title":"evil"}},
 {"digest":"SHA256:`+strings.Repeat("A", 64)+`","size":1,"annotations":{"org.opencontainers.image.title":"upper"}},
 {"digest":"sha512:`+strings.Repeat("a", 128)+`","size":1,"annotations":{"org.opencontainers.image.title":"other-algo"}}]}`)

	for _, name := range []string{"evil", "upper", "other-algo"} {
		w := httptest.NewRecorder()
		serveFile(w, httptest.NewRequest("GET", "/h/x/"+name, nil), store, name)
		if w.Code != 404 || strings.Contains(w.Body.String(), canary) {
			t.Errorf("%s: status=%d body=%q, want 404 without outside content", name, w.Code, w.Body.String())
		}
	}

	entries, err := listFiles(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("listing includes entries with invalid digests: %#v", entries)
	}

	for _, d := range []string{"sha256:../../x", "sha256:" + strings.Repeat("a", 63), "sha256:" + strings.Repeat("a", 64) + "/..", "content", ""} {
		if _, err := blobPath(store, d); err == nil {
			t.Errorf("blobPath accepted non-canonical digest %q", d)
		}
	}
}
