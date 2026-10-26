package store

import (
	"os"
	"path/filepath"
	"testing"
)

// callerFileFields are the request fields naming a file hauler reads as root,
// with the folder each must be in and the 400 message for anything else.
var callerFileFields = []struct {
	name, endpoint, folder, wantMsg string
	body                            func(p string) map[string]any
}{
	{"load filenames", "load", "imports", "filenames must be files in the haul's archives folder or in Wagon's imports folder",
		func(p string) map[string]any { return map[string]any{"filenames": []string{p}} }},
	{"sync filenames", "sync", "imports", "filenames must be a file in Wagon's imports folder",
		func(p string) map[string]any { return map[string]any{"filenames": []string{p}} }},
	{"sync key", "sync", "certs", "key must be a file in Wagon's certs folder",
		func(p string) map[string]any { return map[string]any{"manifestYaml": "kind: Images", "key": p} }},
	{"add-image key", "add-image", "certs", "key must be a file in Wagon's certs folder",
		func(p string) map[string]any { return map[string]any{"imageRef": "nginx:1", "key": p} }},
	{"add-chart keyFile", "add-chart", "certs", "keyFile must be a file in Wagon's certs folder",
		func(p string) map[string]any { return map[string]any{"name": "nginx", "keyFile": p} }},
	{"add-chart certFile", "add-chart", "certs", "certFile must be a file in Wagon's certs folder",
		func(p string) map[string]any { return map[string]any{"name": "nginx", "certFile": p} }},
	{"add-chart caFile", "add-chart", "certs", "caFile must be a file in Wagon's certs folder",
		func(p string) map[string]any { return map[string]any{"name": "nginx", "caFile": p} }},
}

// Every caller-chosen file outside its folder, including a symlink out of it,
// is a 400 before any job is created.
func TestCallerFiles_OutsideFolderRejected(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "host_secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, f := range callerFileFields {
		for _, p := range []string{
			outside,
			"/etc/passwd",
			"../test.db",
			"../../test.db",
			"../hauls/default/store/oci-layout",
			"link-out",
			"sub",
			"missing.pem",
		} {
			h, db := setupTestHandler(t)
			// Plant a sub folder and a symlink out in every folder a field may use.
			for _, dir := range []string{
				filepath.Join(h.Cfg.DataDir, f.folder),
				filepath.Join(filepath.Dir(defaultStoreDir(t, h)), "archives"),
			} {
				if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(dir, "link-out")); err != nil {
					t.Fatal(err)
				}
			}
			w := postStore(h, f.endpoint, mustJSON(t, f.body(p)))
			expectRefused(t, w, db, h.Cfg.DataDir, f.name+" "+p, f.wantMsg)
		}
	}
}

// A file in the field's folder is accepted, by name or by absolute path, and
// hauler gets the resolved path.
func TestCallerFiles_InFolderAccepted(t *testing.T) {
	for _, f := range callerFileFields {
		for _, rel := range []bool{true, false} {
			h, db := setupTestHandler(t)
			dir := filepath.Join(h.Cfg.DataDir, f.folder)
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "a.tar.zst")
			if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
			p := file
			if rel {
				p = "a.tar.zst"
			}
			w := postStore(h, f.endpoint, mustJSON(t, f.body(p)))
			if w.Code != 202 {
				t.Fatalf("%s %q: status %d body %q", f.name, p, w.Code, w.Body.String())
			}
			if !containsArg(onlyJobArgs(t, h, db), realDir(t, file)) {
				t.Errorf("%s %q: hauler args %q lack %q", f.name, p, onlyJobArgs(t, h, db), realDir(t, file))
			}
		}
	}
}

// Load takes bare names from the haul's archives folder, as Haul Detail sends them.
func TestLoad_ArchiveInHaulFolderAccepted(t *testing.T) {
	h, db := setupTestHandler(t)
	archives := filepath.Join(filepath.Dir(defaultStoreDir(t, h)), "archives")
	if err := os.MkdirAll(archives, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(archives, "default.tar.zst")
	if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	w := postStore(h, "load", `{"filenames":["default.tar.zst"]}`)
	if w.Code != 202 {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}
	if got := argAfter(onlyJobArgs(t, h, db), "-f"); got != realDir(t, file) {
		t.Errorf("-f %q, want %q", got, realDir(t, file))
	}
}

// A refused Load with clear set leaves the store alone.
func TestLoad_RejectedFilenameDoesNotClear(t *testing.T) {
	h, _ := setupTestHandler(t)
	marker := filepath.Join(defaultStoreDir(t, h), "marker")
	if err := os.MkdirAll(filepath.Dir(marker), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	w := postStore(h, "load", `{"filenames":["/etc/passwd"],"clear":true}`)
	if w.Code != 400 {
		t.Fatalf("status %d body %q, want 400", w.Code, w.Body.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("store was cleared by a refused load: %v", err)
	}
}

// Sync with no filenames and no manifest reads hauler-manifest.yaml, which
// must now be in the imports folder.
func TestSync_DefaultManifestFromImports(t *testing.T) {
	h, db := setupTestHandler(t)
	w := postStore(h, "sync", `{}`)
	expectRefused(t, w, db, h.Cfg.DataDir, "default", "filenames must be a file in Wagon's imports folder")

	imports := filepath.Join(h.Cfg.DataDir, "imports")
	file := filepath.Join(imports, "hauler-manifest.yaml")
	if err := os.WriteFile(file, []byte("kind: Images"), 0644); err != nil {
		t.Fatal(err)
	}
	if w := postStore(h, "sync", `{}`); w.Code != 202 {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}
	if got := argAfter(onlyJobArgs(t, h, db), "-f"); got != realDir(t, file) {
		t.Errorf("-f %q, want %q", got, realDir(t, file))
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
