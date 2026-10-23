package hauls

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
	"github.com/alphabravo-oss/wagon/backend/internal/sqlite"
)

// newTestService builds a Service backed by a freshly-migrated on-disk SQLite
// database and a config whose DataDir points at an isolated temp directory.
func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})
	cfg := &config.Config{DataDir: t.TempDir()}
	return NewService(db.DB, cfg)
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"spaces to dashes", "My Cool Haul", "my-cool-haul"},
		{"lowercasing", "UPPER", "upper"},
		{"strip invalid chars", "haul!@#name", "haul-name"},
		{"mixed", "Foo/Bar Baz", "foo-bar-baz"},
		{"leading trailing junk", "  --Hello--  ", "hello"},
		{"empty becomes haul", "", "haul"},
		{"only invalid becomes haul", "!!!", "haul"},
		{"already slug", "already-slug", "already-slug"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := slugify(tt.in); got != tt.want {
				t.Errorf("slugify(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCreate(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	h, err := s.Create(ctx, "My Haul", "a description")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if h.ID == 0 {
		t.Errorf("expected non-zero ID, got %d", h.ID)
	}
	if h.Name != "My Haul" {
		t.Errorf("Name = %q, want %q", h.Name, "My Haul")
	}
	if h.Slug != "my-haul" {
		t.Errorf("Slug = %q, want %q", h.Slug, "my-haul")
	}
	if h.Description != "a description" {
		t.Errorf("Description = %q, want %q", h.Description, "a description")
	}
	if h.StoreDir == "" {
		t.Fatal("expected non-empty StoreDir")
	}

	// Store dir must exist on disk with an initialized OCI layout.
	if fi, err := os.Stat(h.StoreDir); err != nil || !fi.IsDir() {
		t.Fatalf("StoreDir %q should be a directory: err=%v", h.StoreDir, err)
	}
	ociLayout := filepath.Join(h.StoreDir, "oci-layout")
	if _, err := os.Stat(ociLayout); err != nil {
		t.Errorf("expected oci-layout file at %q: %v", ociLayout, err)
	}
	blobs := filepath.Join(h.StoreDir, "blobs", "sha256")
	if fi, err := os.Stat(blobs); err != nil || !fi.IsDir() {
		t.Errorf("expected blobs/sha256 dir at %q: err=%v", blobs, err)
	}

	// A DB row must exist and be retrievable.
	got, err := s.Get(ctx, h.ID)
	if err != nil {
		t.Fatalf("Get after Create: %v", err)
	}
	if got.ID != h.ID || got.Slug != h.Slug {
		t.Errorf("Get returned %+v, want id=%d slug=%q", got, h.ID, h.Slug)
	}
}

func TestCreateRequiresName(t *testing.T) {
	s := newTestService(t)
	if _, err := s.Create(context.Background(), "   ", ""); err == nil {
		t.Error("expected error for blank name, got nil")
	}
}

func TestSlugUniqueness(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	// The hauls.name column is UNIQUE, so two distinct display names that
	// slugify to the same base ("same name" and "Same!Name" -> "same-name")
	// are what exercise the -2 suffix collision path.
	h1, err := s.Create(ctx, "same name", "")
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	h2, err := s.Create(ctx, "Same!Name", "")
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}

	if h1.Slug != "same-name" {
		t.Errorf("first slug = %q, want %q", h1.Slug, "same-name")
	}
	if h2.Slug != "same-name-2" {
		t.Errorf("second slug = %q, want %q", h2.Slug, "same-name-2")
	}
	if h1.Slug == h2.Slug {
		t.Errorf("slugs must be distinct, both = %q", h1.Slug)
	}
	if h1.StoreDir == h2.StoreDir {
		t.Errorf("store dirs must be isolated, both = %q", h1.StoreDir)
	}
}

func TestGetAndGetBySlug(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	created, err := s.Create(ctx, "Round Trip", "desc")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	byID, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if byID.ID != created.ID || byID.Name != created.Name || byID.StoreDir != created.StoreDir {
		t.Errorf("Get = %+v, want %+v", byID, created)
	}

	bySlug, err := s.GetBySlug(ctx, created.Slug)
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if bySlug.ID != created.ID || bySlug.Slug != created.Slug {
		t.Errorf("GetBySlug = %+v, want id=%d slug=%q", bySlug, created.ID, created.Slug)
	}
}

func TestDelete(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	h, err := s.Create(ctx, "To Delete", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	haulDir := filepath.Dir(h.StoreDir)
	if _, err := os.Stat(haulDir); err != nil {
		t.Fatalf("haul dir should exist before delete: %v", err)
	}

	if err := s.Delete(ctx, h.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Directory tree must be gone from disk.
	if _, err := os.Stat(haulDir); !os.IsNotExist(err) {
		t.Errorf("haul dir %q should be removed, stat err = %v", haulDir, err)
	}
	// DB row must be gone.
	if _, err := s.Get(ctx, h.ID); err == nil {
		t.Error("Get after Delete should error, got nil")
	}
}

func TestEnsureDefault(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	first, err := s.EnsureDefault(ctx)
	if err != nil {
		t.Fatalf("EnsureDefault (create): %v", err)
	}
	if first.Name != "Default" {
		t.Errorf("expected Default haul, got name %q", first.Name)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly 1 haul after EnsureDefault, got %d", len(list))
	}

	second, err := s.EnsureDefault(ctx)
	if err != nil {
		t.Fatalf("EnsureDefault (existing): %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("second EnsureDefault returned id %d, want existing id %d", second.ID, first.ID)
	}

	list, err = s.List(ctx)
	if err != nil {
		t.Fatalf("List after second EnsureDefault: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("EnsureDefault should not create a second haul, got %d hauls", len(list))
	}
}
