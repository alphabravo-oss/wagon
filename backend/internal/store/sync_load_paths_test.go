package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alphabravo-oss/wagon/backend/internal/jobrunner"
)

// card69Fixture plants what the card 69 repro used: a root-only file outside
// the data folder, a file-shaped manifest in the haul's archives folder (what
// Import leaves there), a symlink and a folder in imports that point outside,
// and an allowed file in imports.
type card69Fixture struct {
	h                              *Handler
	dataDir, imports, archives     string
	outside, outsideDir, inImports string
}

func newCard69Fixture(t *testing.T) (*card69Fixture, func() int) {
	t.Helper()
	h, db := setupTestHandler(t)
	f := &card69Fixture{h: h, dataDir: h.Cfg.DataDir}
	f.imports = filepath.Join(f.dataDir, "imports")
	f.archives = filepath.Join(filepath.Dir(defaultStoreDir(t, h)), "archives")
	f.outsideDir = t.TempDir()
	f.outside = filepath.Join(f.outsideDir, "host_canary.txt")
	f.inImports = filepath.Join(f.imports, "ok.txt")
	for _, d := range []string{filepath.Join(f.imports, "sub"), f.archives, filepath.Join(f.outsideDir, "chart")} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for p, body := range map[string]string{
		f.outside:   "CARD69-CANARY",
		f.inImports: "ok",
		filepath.Join(f.archives, "evil.tar.zst"): filesManifest(f.outside),
		filepath.Join(f.archives, "good.tar.zst"): "archive",
	} {
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(f.outside, filepath.Join(f.imports, "link-out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.outsideDir, filepath.Join(f.imports, "dir-out")); err != nil {
		t.Fatal(err)
	}
	return f, func() int { return jobCount(t, db) }
}

func filesManifest(paths ...string) string {
	m := "apiVersion: content.hauler.cattle.io/v1\nkind: Files\nmetadata:\n  name: card69\nspec:\n  files:\n"
	for _, p := range paths {
		m += "    - path: " + p + "\n"
	}
	return m
}

// Load used absolute filenames as-is, so any readable hauler archive on the
// host could be loaded into the store and served.
func TestLoad_PathOutsideAllowedFoldersRejected(t *testing.T) {
	probe, _ := newCard69Fixture(t)
	for i := range card69LoadInputs(probe) {
		f, jobs := newCard69Fixture(t)
		p := card69LoadInputs(f)[i]
		storeFile := filepath.Join(defaultStoreDir(t, f.h), "oci-layout")
		writeStoreFixture(t, storeFile, "{}")
		w := postStore(f.h, "load", mustJSON(t, map[string]any{"filenames": []string{p}, "clear": true}))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "filenames[0] must be") {
			t.Errorf("%q: status %d body %q, want 400", p, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), f.dataDir) {
			t.Errorf("%q: 400 body names the data folder: %q", p, w.Body.String())
		}
		if n := jobs(); n != 0 {
			t.Errorf("%q: expected no jobs, got %d", p, n)
		}
		// A refused load must not have cleared the store first.
		if _, err := os.Stat(storeFile); err != nil {
			t.Errorf("%q: store was cleared before the filename was refused", p)
		}
	}
}

func card69LoadInputs(f *card69Fixture) []string {
	return []string{
		f.outside,
		"/etc/passwd",
		filepath.Join(f.dataDir, "hauls", "other", "archives", "x.tar.zst"),
		filepath.Join(f.archives, "good.tar.zst"), // a path, not a plain name
		"../hauls/default/archives/good.tar.zst",
		"link-out",
		filepath.Join(f.imports, "link-out"),
		"missing.tar.zst",
		"..",
		"",
	}
}

func TestLoad_ArchiveOrImportAccepted(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"plain name in archives", "good.tar.zst", "archives/good.tar.zst"},
		{"absolute path in imports", "IMPORTS/ok.txt", "imports/ok.txt"},
		{"relative path in imports", "./ok.txt", "imports/ok.txt"},
	} {
		f, _ := newCard69Fixture(t)
		db := f.h.JobRunner.DB()
		input := strings.Replace(tc.input, "IMPORTS", f.imports, 1)
		w := postStore(f.h, "load", mustJSON(t, map[string]any{"filenames": []string{input}}))
		if w.Code != 202 {
			t.Fatalf("%s: status %d body %q", tc.name, w.Code, w.Body.String())
		}
		got := argAfter(onlyJobArgs(t, f.h, db), "-f")
		if !strings.HasSuffix(got, "/"+tc.want) || !filepath.IsAbs(got) {
			t.Errorf("%s: hauler got -f %q, want .../%s", tc.name, got, tc.want)
		}
	}
}

// Sync passed filenames to hauler verbatim, so a manifest planted anywhere
// (here by Import, in the haul's archives folder) could name host files.
func TestSync_FilenamesOutsideImportsRejected(t *testing.T) {
	probe, _ := newCard69Fixture(t)
	for i := range card69SyncFilenames(probe) {
		f, jobs := newCard69Fixture(t)
		p := card69SyncFilenames(f)[i]
		body := map[string]any{"filenames": []string{p}}
		if p == defaultSyncFile {
			body = map[string]any{} // hauler-manifest.yaml, not in imports
		}
		w := postStore(f.h, "sync", mustJSON(t, body))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "filenames[0] must be a file in Wagon's imports folder") {
			t.Errorf("%q: status %d body %q, want 400", p, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), f.dataDir) {
			t.Errorf("%q: 400 body names the data folder: %q", p, w.Body.String())
		}
		if n := jobs(); n != 0 {
			t.Errorf("%q: expected no jobs, got %d", p, n)
		}
	}
}

func card69SyncFilenames(f *card69Fixture) []string {
	return []string{
		filepath.Join(f.archives, "evil.tar.zst"),
		"../hauls/default/archives/evil.tar.zst",
		f.outside,
		"/etc/passwd",
		"link-out",
		"dir-out",
		"",
		"https://example.com/hauler-manifest.yaml",
		defaultSyncFile,
	}
}

// defaultSyncFile stands for a sync request without filenames.
const defaultSyncFile = "(default)"

// The manifest itself names what hauler reads. Every local path in it must be
// in imports, the way hauler resolves it (relative to the data folder, URL
// decoded, symlinks followed); anything else is a 400 before a job exists.
func TestSync_ManifestHostPathsRejected(t *testing.T) {
	probe, _ := newCard69Fixture(t)
	for i := range card69BadManifests(probe) {
		f, jobs := newCard69Fixture(t)
		tc := card69BadManifests(f)[i]
		for _, viaFile := range []bool{false, true} {
			body := map[string]any{"manifestYaml": tc.yaml}
			if viaFile {
				if err := os.WriteFile(filepath.Join(f.imports, "m.yaml"), []byte(tc.yaml), 0644); err != nil {
					t.Fatal(err)
				}
				body = map[string]any{"filenames": []string{"m.yaml"}}
			}
			w := postStore(f.h, "sync", mustJSON(t, body))
			if w.Code != 400 || !strings.Contains(w.Body.String(), tc.want) {
				t.Errorf("%s (file=%v): status %d body %q, want 400 %q", tc.name, viaFile, w.Code, w.Body.String(), tc.want)
			}
			if strings.Contains(w.Body.String(), f.dataDir) || strings.Contains(w.Body.String(), f.outsideDir) {
				t.Errorf("%s: 400 body names a host path: %q", tc.name, w.Body.String())
			}
		}
		if n := jobs(); n != 0 {
			t.Errorf("%s: expected no jobs, got %d", tc.name, n)
		}
	}
}

type badManifest struct{ name, yaml, want string }

func card69BadManifests(f *card69Fixture) []badManifest {
	const notImports = "must be a file or folder in Wagon's imports folder"
	images := func(meta, key string) string {
		return "apiVersion: content.hauler.cattle.io/v1\nkind: Images\nmetadata:\n  name: i\n" + meta +
			"spec:\n  images:\n    - name: docker.io/library/busybox:1\n      key: " + key + "\n"
	}
	charts := func(kind, name, repo string) string {
		return "apiVersion: content.hauler.cattle.io/v1\nkind: " + kind + "\nmetadata:\n  name: c\nspec:\n  charts:\n" +
			"    - name: " + name + "\n      repoURL: " + repo + "\n"
	}
	return []badManifest{
		{"absolute host file", filesManifest(f.outside), "files[0].path " + notImports},
		{"etc", filesManifest("/etc/passwd"), notImports},
		{"host folder", filesManifest(f.outsideDir), notImports},
		{"relative from data folder", filesManifest("test.db"), notImports},
		{"relative escape", filesManifest("imports/../test.db"), notImports},
		{"symlink out of imports", filesManifest(filepath.Join(f.imports, "link-out")), notImports},
		{"relative symlink", filesManifest("imports/link-out"), notImports},
		{"symlinked folder", filesManifest(filepath.Join(f.imports, "dir-out", "host_canary.txt")), notImports},
		{"url-encoded escape", filesManifest(f.imports + "/%2e%2e/test.db"), notImports},
		{"file url", filesManifest("file://" + f.outside), notImports},
		{"http url naming a local file", filesManifest("http://test.db"), notImports},
		{"http url with no host", filesManifest("http:///etc/passwd"), notImports},
		{"second entry", filesManifest(f.inImports, f.outside), "files[1].path"},
		{"v1alpha1", strings.Replace(filesManifest(f.outside), "/v1\n", "/v1alpha1\n", 1), notImports},
		{"collection group", strings.Replace(filesManifest(f.outside), "content.", "collection.", 1), notImports},
		{"second document", filesManifest(f.inImports) + "---\n" + filesManifest(f.outside), "manifest document 2"},
		{"upper-case key", strings.Replace(filesManifest(f.outside), "path:", "PATH:", 1), notImports},
		{"keys differing in case", filesManifest(f.inImports) + "      Path: " + f.outside + "\n", "differ only in case"},
		{"name with folders", filesManifest(f.inImports) + "      name: ../../etc/cron.d/x\n", "files[0].name must be a plain file name"},
		{"image key", images("", f.outside), "images[0].key " + notImports},
		{"image key in home", images("", "~/cosign.pub"), "images[0].key"},
		{"key annotation", images("  annotations:\n    hauler.dev/key: "+f.outside+"\n", "\"\""), "annotation hauler.dev/key"},
		{"local chart", charts("Charts", "chart", rel(f.dataDir, f.outsideDir)), "charts[0] names a local chart that " + notImports},
		{"chart path with no repo", charts("Charts", filepath.Join(f.outsideDir, "chart"), "\"\""), "charts[0] names a local chart"},
		{"chart name path with repo url", charts("Charts", filepath.Join(f.outsideDir, "chart"), "https://charts.example"), "charts[0] names a local chart"},
		{"thick chart", charts("ThickCharts", "chart", rel(f.dataDir, f.outsideDir)), "charts[0] names a local chart"},
		{"image txt", "apiVersion: collection.hauler.cattle.io/v1\nkind: ImageTxts\nmetadata:\n  name: t\nspec:\n  imageTxts:\n    - ref: " + f.outside + "\n", "imageTxts[0].ref " + notImports},
		{"unknown kind", "apiVersion: content.hauler.cattle.io/v1\nkind: Driver\nspec:\n  type: x\n", "kind must be"},
		{"unknown apiVersion", strings.Replace(filesManifest(f.outside), "content.hauler.cattle.io", "example.com", 1), "apiVersion must be"},
		{"not yaml", "kind: [\n", "not valid YAML"},
		{"empty", "---\n", "no documents"},
	}
}

func TestSync_ManifestImportsAndRemoteAccepted(t *testing.T) {
	for _, tc := range []struct{ name, yaml string }{
		{"file in imports", filesManifest("IMPORTS/ok.txt")},
		{"relative file in imports", filesManifest("imports/ok.txt")},
		{"folder in imports", filesManifest("IMPORTS/sub")},
		{"local chart in imports", "apiVersion: content.hauler.cattle.io/v1\nkind: Charts\nmetadata:\n  name: c\nspec:\n  charts:\n    - name: sub\n      repoURL: imports\n"},
		{"remote file", filesManifest("https://example.com/k3s")},
		{"images", "apiVersion: content.hauler.cattle.io/v1\nkind: Images\nmetadata:\n  name: i\nspec:\n  images:\n    - name: docker.io/library/busybox:1\n"},
		{"remote charts", "apiVersion: content.hauler.cattle.io/v1\nkind: Charts\nmetadata:\n  name: c\nspec:\n  charts:\n" +
			"    - name: rancher\n      repoURL: https://releases.rancher.com/server-charts/stable\n" +
			"    - name: podinfo\n      repoURL: oci://ghcr.io/stefanprodan/charts\n"},
		{"remote image txt", "apiVersion: collection.hauler.cattle.io/v1\nkind: ImageTxts\nmetadata:\n  name: t\nspec:\n  imageTxts:\n    - ref: https://example.com/images.txt\n"},
	} {
		f, _ := newCard69Fixture(t)
		yaml := strings.ReplaceAll(tc.yaml, "IMPORTS", f.imports)
		w := postStore(f.h, "sync", mustJSON(t, map[string]any{"manifestYaml": yaml}))
		if w.Code != 202 {
			t.Errorf("%s: status %d body %q", tc.name, w.Code, w.Body.String())
		}
	}
}

// hauler gets one checked copy of the manifest under a unique name, and the
// copy still exists when the job runs. It used to be removed when the HTTP
// handler returned, before the queued job started, and every copy had the
// same name.
func TestSync_ManifestCopyOutlivesHandlerAndIsUnique(t *testing.T) {
	bin, out := t.TempDir(), t.TempDir()
	script := "#!/bin/sh\ncp \"$4\" \"$FAKE_HAULER_OUT/got\"\n"
	if err := os.WriteFile(filepath.Join(bin, "hauler"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_HAULER_OUT", out)

	f, _ := newCard69Fixture(t)
	db := f.h.JobRunner.DB()
	for _, body := range []map[string]any{
		{"manifestYaml": filesManifest(f.inImports)},
		{"manifestYaml": filesManifest(f.inImports)},
	} {
		if w := postStore(f.h, "sync", mustJSON(t, body)); w.Code != 202 {
			t.Fatalf("status %d body %q", w.Code, w.Body.String())
		}
	}
	rows, err := db.Query("SELECT id FROM jobs ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	var paths []string
	for _, id := range ids {
		job, err := f.h.JobRunner.GetJob(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, argAfter(job.Args, "-f"))
	}
	if len(paths) != 2 || paths[0] == paths[1] {
		t.Fatalf("manifest copies %q, want two different files", paths)
	}

	// The handler has returned; start the first job as the dispatcher would.
	if err := f.h.JobRunner.Start(context.Background(), ids[0]); err != nil {
		t.Fatal(err)
	}
	waitJobsDone(t, db)
	job, err := f.h.JobRunner.GetJob(context.Background(), ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobrunner.StatusSucceeded {
		t.Fatalf("job %s, want succeeded: hauler could not read the manifest copy", job.Status)
	}
	got, _ := os.ReadFile(filepath.Join(out, "got"))
	if !strings.Contains(string(got), `"path":"`+f.inImports+`"`) {
		t.Errorf("hauler read %q, want the checked JSON copy", got)
	}
	// Removed once the job has ended (trackAfterJob polls every 500ms).
	waitGone(t, paths[0])
}

func waitGone(t *testing.T, p string) {
	t.Helper()
	for range 40 {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("manifest copy %s left behind after the job ended", filepath.Base(p))
}

// rel is target relative to base, the form hauler resolves from its working
// folder (the data folder).
func rel(base, target string) string {
	r, err := filepath.Rel(base, target)
	if err != nil {
		panic(err)
	}
	return r
}
