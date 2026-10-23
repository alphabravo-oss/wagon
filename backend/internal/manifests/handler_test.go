package manifests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
	"github.com/alphabravo-oss/wagon/backend/internal/hauls"
	"github.com/alphabravo-oss/wagon/backend/internal/sqlite"
)

// setup creates a migrated in-file SQLite DB, a haul service backed by a temp
// data dir, and a wired-up manifests handler with routes registered.
func setup(t *testing.T) (*Handler, *hauls.Service, http.Handler) {
	t.Helper()

	dir := t.TempDir()
	db, err := sqlite.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})

	cfg := &config.Config{DataDir: filepath.Join(dir, "data")}
	haulSvc := hauls.NewService(db.DB, cfg)

	h := NewHandler(db.DB, haulSvc)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, haulSvc, mux
}

// doJSON issues a request through the mux and decodes a JSON body into out (if
// non-nil). It returns the recorder for status/body assertions.
func doJSON(t *testing.T, mux http.Handler, method, url string, body any, out any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, url, rdr)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if out != nil && rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("decode response (%s %s, status %d): %v; body=%s", method, url, rec.Code, err, rec.Body.String())
		}
	}
	return rec
}

// TestManifestCRUDLifecycle covers create -> list -> get -> update -> delete
// for a manifest scoped to an explicit haul.
func TestManifestCRUDLifecycle(t *testing.T) {
	_, haulSvc, mux := setup(t)

	haul, err := haulSvc.Create(context.Background(), "Alpha", "alpha haul")
	if err != nil {
		t.Fatalf("create haul: %v", err)
	}

	// Create a manifest scoped to the haul.
	createReq := CreateManifestRequest{
		HaulID:      haul.ID,
		Name:        "my-manifest",
		Description: "first",
		YAMLContent: "kind: ImageConfig",
		Tags:        []string{"a", "b"},
	}
	var created Manifest
	rec := doJSON(t, mux, http.MethodPost, "/api/manifests", createReq, &created)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if created.ID == 0 {
		t.Fatalf("create: expected non-zero id")
	}
	if created.HaulID != haul.ID {
		t.Errorf("create: expected haulId %d, got %d", haul.ID, created.HaulID)
	}
	if created.YAMLContent != "kind: ImageConfig" {
		t.Errorf("create: unexpected yaml %q", created.YAMLContent)
	}
	if len(created.Tags) != 2 {
		t.Errorf("create: expected 2 tags, got %v", created.Tags)
	}

	// List returns the created manifest.
	var listed []Manifest
	rec = doJSON(t, mux, http.MethodGet, fmt.Sprintf("/api/manifests?haul=%d", haul.ID), nil, &listed)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", rec.Code)
	}
	if len(listed) != 1 {
		t.Fatalf("list: expected 1 manifest, got %d", len(listed))
	}
	if listed[0].ID != created.ID || listed[0].Name != "my-manifest" {
		t.Errorf("list: unexpected manifest %+v", listed[0])
	}

	// Get returns the created content.
	var got Manifest
	rec = doJSON(t, mux, http.MethodGet, fmt.Sprintf("/api/manifests/%d", created.ID), nil, &got)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d", rec.Code)
	}
	if got.YAMLContent != "kind: ImageConfig" || got.Description != "first" {
		t.Errorf("get: unexpected manifest %+v", got)
	}

	// Update changes name, description, and content.
	updateReq := UpdateManifestRequest{
		Name:        "renamed-manifest",
		Description: "second",
		YAMLContent: "kind: ChartConfig",
		Tags:        []string{"c"},
	}
	var updated Manifest
	rec = doJSON(t, mux, http.MethodPut, fmt.Sprintf("/api/manifests/%d", created.ID), updateReq, &updated)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if updated.Name != "renamed-manifest" || updated.YAMLContent != "kind: ChartConfig" || updated.Description != "second" {
		t.Errorf("update: change not applied: %+v", updated)
	}

	// Get reflects the update.
	var afterUpdate Manifest
	doJSON(t, mux, http.MethodGet, fmt.Sprintf("/api/manifests/%d", created.ID), nil, &afterUpdate)
	if afterUpdate.YAMLContent != "kind: ChartConfig" || afterUpdate.Name != "renamed-manifest" {
		t.Errorf("get-after-update: stale data %+v", afterUpdate)
	}

	// Delete removes the manifest.
	rec = doJSON(t, mux, http.MethodDelete, fmt.Sprintf("/api/manifests/%d", created.ID), nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d", rec.Code)
	}

	// Subsequent Get returns 404.
	rec = doJSON(t, mux, http.MethodGet, fmt.Sprintf("/api/manifests/%d", created.ID), nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("get-after-delete: expected 404, got %d", rec.Code)
	}

	// Subsequent List is empty for the haul.
	var listedAfter []Manifest
	doJSON(t, mux, http.MethodGet, fmt.Sprintf("/api/manifests?haul=%d", haul.ID), nil, &listedAfter)
	if len(listedAfter) != 0 {
		t.Errorf("list-after-delete: expected 0 manifests, got %d", len(listedAfter))
	}
}

// TestManifestPerHaulScoping asserts the key isolation guarantee: a manifest
// created under haul A must not appear when listing haul B's manifests.
func TestManifestPerHaulScoping(t *testing.T) {
	_, haulSvc, mux := setup(t)

	haulA, err := haulSvc.Create(context.Background(), "Haul A", "")
	if err != nil {
		t.Fatalf("create haul A: %v", err)
	}
	haulB, err := haulSvc.Create(context.Background(), "Haul B", "")
	if err != nil {
		t.Fatalf("create haul B: %v", err)
	}
	if haulA.ID == haulB.ID {
		t.Fatalf("expected distinct haul ids, both %d", haulA.ID)
	}

	// Create a manifest under haul A.
	var inA Manifest
	rec := doJSON(t, mux, http.MethodPost, "/api/manifests", CreateManifestRequest{
		HaulID:      haulA.ID,
		Name:        "only-in-a",
		YAMLContent: "kind: ImageConfig",
	}, &inA)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create in A: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Create a distinct manifest under haul B to prove B's list is independently populated.
	var inB Manifest
	rec = doJSON(t, mux, http.MethodPost, "/api/manifests", CreateManifestRequest{
		HaulID:      haulB.ID,
		Name:        "only-in-b",
		YAMLContent: "kind: ChartConfig",
	}, &inB)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create in B: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Listing haul A returns only A's manifest.
	var listA []Manifest
	doJSON(t, mux, http.MethodGet, fmt.Sprintf("/api/manifests?haul=%d", haulA.ID), nil, &listA)
	if len(listA) != 1 {
		t.Fatalf("list A: expected 1 manifest, got %d (%+v)", len(listA), listA)
	}
	if listA[0].Name != "only-in-a" || listA[0].HaulID != haulA.ID {
		t.Errorf("list A: unexpected manifest %+v", listA[0])
	}

	// Listing haul B returns only B's manifest — A's must NOT leak in.
	var listB []Manifest
	doJSON(t, mux, http.MethodGet, fmt.Sprintf("/api/manifests?haul=%d", haulB.ID), nil, &listB)
	if len(listB) != 1 {
		t.Fatalf("list B: expected 1 manifest, got %d (%+v)", len(listB), listB)
	}
	if listB[0].Name != "only-in-b" || listB[0].HaulID != haulB.ID {
		t.Errorf("list B: unexpected manifest %+v", listB[0])
	}
	for _, m := range listB {
		if m.ID == inA.ID {
			t.Errorf("isolation violation: manifest from haul A (id=%d) appeared in haul B's list", inA.ID)
		}
	}

	// Same-name manifests are allowed across different hauls (scoping of the
	// uniqueness constraint), reinforcing that hauls are isolated namespaces.
	rec = doJSON(t, mux, http.MethodPost, "/api/manifests", CreateManifestRequest{
		HaulID:      haulB.ID,
		Name:        "only-in-a", // same name as A's manifest, but under B
		YAMLContent: "kind: ImageConfig",
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Errorf("cross-haul same-name create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}
