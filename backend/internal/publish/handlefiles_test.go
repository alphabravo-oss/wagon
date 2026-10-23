package publish

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
	"github.com/alphabravo-oss/wagon/backend/internal/hauls"
	"github.com/alphabravo-oss/wagon/backend/internal/sqlite"
)

// /h/{slug} must not serve a haul that is not currently published, even when
// publish auth is unset (open by default).
func TestHandleFilesRequiresPublished(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{DataDir: t.TempDir()}
	haulSvc := hauls.NewService(db.DB, cfg)

	haul, err := haulSvc.Create(context.Background(), "Secret Haul", "")
	if err != nil {
		t.Fatalf("create haul: %v", err)
	}
	// Put a downloadable artifact in the store so, absent the gate, /h/ would
	// serve content.
	const manifestHex = "2222222222222222222222222222222222222222222222222222222222222222"
	const contentHex = "3333333333333333333333333333333333333333333333333333333333333333"
	writePublishFixture(t, filepath.Join(haul.StoreDir, "index.json"), `{"manifests":[{"digest":"sha256:`+manifestHex+`"}]}`)
	writePublishFixture(t, filepath.Join(haul.StoreDir, "blobs", "sha256", manifestHex),
		`{"layers":[{"digest":"sha256:`+contentHex+`","size":6,"annotations":{"org.opencontainers.image.title":"notes.txt"}}]}`)
	writePublishFixture(t, filepath.Join(haul.StoreDir, "blobs", "sha256", contentHex), "abcdef")

	mgr := NewManager(cfg, db.DB, haulSvc)
	h := NewHandler(mgr, haulSvc)

	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.handleFiles(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}

	// Not published yet: listing and file download must both 404.
	if w := get("/h/" + haul.Slug + "/"); w.Code != http.StatusNotFound {
		t.Errorf("unpublished listing: status=%d body=%q, want 404", w.Code, w.Body.String())
	}
	if w := get("/h/" + haul.Slug + "/notes.txt"); w.Code != http.StatusNotFound || w.Body.String() == "abcdef" {
		t.Errorf("unpublished file: status=%d body=%q, want 404 without content", w.Code, w.Body.String())
	}

	// After publishing, the same requests succeed.
	if _, err := mgr.Publish(context.Background(), haul.ID, ""); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if w := get("/h/" + haul.Slug + "/"); w.Code != http.StatusOK {
		t.Errorf("published listing: status=%d body=%q, want 200", w.Code, w.Body.String())
	}
	if w := get("/h/" + haul.Slug + "/notes.txt"); w.Code != http.StatusOK || w.Body.String() != "abcdef" {
		t.Errorf("published file: status=%d body=%q, want 200 abcdef", w.Code, w.Body.String())
	}
}
