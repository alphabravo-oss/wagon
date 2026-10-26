package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// imageTxtsManifest is an ImageTxts manifest with one ref.
func imageTxtsManifest(ref string) string {
	return "apiVersion: collection.hauler.cattle.io/v1\nkind: ImageTxts\nmetadata:\n  name: t\nspec:\n  imageTxts:\n    - ref: " + ref + "\n"
}

// hauler reads an http(s) source's local form (host joined with path, from
// the data folder) whenever it exists when the job runs, and jobs queue. So a
// URL whose local form could name something in or above the data folder is a
// 400 even while the target does not exist yet: certs/<type>.key appears on
// the first TLS serve and .docker/config.json on the first registry login.
func TestSync_ManifestURLLocalFormRefused(t *testing.T) {
	const want = "is a URL hauler would read from disk"
	for _, src := range []string{
		"https://certs/registry.key",
		"https://CERTS/registry.key",
		"https://.docker/config.json",
		"https://../../etc/later-file",
		"https://example.com/%2e%2e/%2e%2e/etc/later-file",
		"https://example.com/../certs/registry.key",
		"http://hauls/x/late.txt",
		"http://imports/later.txt",
		"http://tmp/x",
		"http://store/x",
		"http://registry-tls/tls.key",
		"http://app.db/x",
		"http://mirror/later.txt",
		"http://wagon.sqlite/x",
		"http://wagon.sqlite-wal/x",
		"http://creds/config.json",
	} {
		for _, imageTxts := range []bool{false, true} {
			f, jobs := newCard69Fixture(t)
			f.h.Cfg.DatabasePath = filepath.Join(f.dataDir, "wagon.sqlite")
			f.h.Cfg.DockerAuthPath = filepath.Join(f.dataDir, "creds", "config.json")
			if err := os.Mkdir(filepath.Join(f.dataDir, "mirror"), 0755); err != nil {
				t.Fatal(err)
			}
			for _, absent := range []string{"certs", ".docker", "creds", "wagon.sqlite", "registry-tls"} {
				if _, err := os.Lstat(filepath.Join(f.dataDir, absent)); err == nil {
					t.Fatalf("fixture already has %s", absent)
				}
			}
			m, field := filesManifest(src), "files[0].path "
			if imageTxts {
				m, field = imageTxtsManifest(src), "imageTxts[0].ref "
			}
			w := postStore(f.h, "sync", mustJSON(t, map[string]any{"manifestYaml": m}))
			if w.Code != 400 || !strings.Contains(w.Body.String(), field+want) {
				t.Errorf("%s (imageTxts=%v): status %d body %q, want 400 %q", src, imageTxts, w.Code, w.Body.String(), field+want)
			}
			if strings.Contains(w.Body.String(), f.dataDir) {
				t.Errorf("%s: 400 body names a host path: %q", src, w.Body.String())
			}
			if n := jobs(); n != 0 {
				t.Errorf("%s: expected no jobs, got %d", src, n)
			}
		}
	}
}

// A URL whose host only starts like a data-folder entry is still fetched.
func TestSync_ManifestURLLookalikeHostAccepted(t *testing.T) {
	for _, src := range []string{"https://certs.example.com/registry.key", "https://store-mirror.example/x.txt"} {
		for _, m := range []string{filesManifest(src), imageTxtsManifest(src)} {
			f, _ := newCard69Fixture(t)
			if w := postStore(f.h, "sync", mustJSON(t, map[string]any{"manifestYaml": m})); w.Code != 202 {
				t.Errorf("%s: status %d body %q, want 202", src, w.Code, w.Body.String())
			}
		}
	}
}

// A chart repoURL must be http, https or oci. A file URL or an absolute path
// would point helm at the host's disk.
func TestSync_ManifestChartRepoURLScheme(t *testing.T) {
	const want = "charts[0] repoURL must be an http, https or oci:// URL"
	for _, repo := range []string{"file:///etc", "FILE:///etc", "File://localhost/etc", "\" file:///etc\"", "/etc", "\" /etc\"", "ftp://example.com/charts"} {
		for _, kind := range []string{"Charts", "ThickCharts"} {
			f, jobs := newCard69Fixture(t)
			m := "apiVersion: content.hauler.cattle.io/v1\nkind: " + kind + "\nmetadata:\n  name: c\nspec:\n  charts:\n" +
				"    - name: passwd\n      repoURL: " + repo + "\n"
			w := postStore(f.h, "sync", mustJSON(t, map[string]any{"manifestYaml": m}))
			if w.Code != 400 || !strings.Contains(w.Body.String(), want) {
				t.Errorf("%s %s: status %d body %q, want 400 %q", kind, repo, w.Code, w.Body.String(), want)
			}
			if n := jobs(); n != 0 {
				t.Errorf("%s %s: expected no jobs, got %d", kind, repo, n)
			}
		}
	}
	for _, repo := range []string{"HTTPS://charts.example.com", "http://charts.example.com", "oci://ghcr.io/x"} {
		f, _ := newCard69Fixture(t)
		m := "apiVersion: content.hauler.cattle.io/v1\nkind: Charts\nmetadata:\n  name: c\nspec:\n  charts:\n" +
			"    - name: podinfo\n      repoURL: " + repo + "\n"
		if w := postStore(f.h, "sync", mustJSON(t, map[string]any{"manifestYaml": m})); w.Code != 202 {
			t.Errorf("%s: status %d body %q, want 202", repo, w.Code, w.Body.String())
		}
	}
}
