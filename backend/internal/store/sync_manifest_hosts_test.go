package store

import (
	"context"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/alphabravo-oss/wagon/backend/internal/safefetch"
)

// hostsManifest returns a one-document manifest of the given kind and spec.
func hostsManifest(apiVersion, kind, metadata, spec string) string {
	return "apiVersion: " + apiVersion + "\nkind: " + kind + "\nmetadata:\n  name: t\n" + metadata + "spec:\n" + spec
}

func chartsManifest(kind, chart string) string {
	return hostsManifest("content.hauler.cattle.io/v1", kind, "", "  charts:\n    - "+chart+"\n")
}

// splitChart is a chart whose repoURL and name hauler joins and helm trims
// into a URL neither field spells. Both are quoted, so YAML keeps the spaces.
func splitChart(kind, repoURL, name string) string {
	return chartsManifest(kind, "name: "+strconv.Quote(name)+"\n      repoURL: "+strconv.Quote(repoURL))
}

func imagesManifest(annotations, image string) string {
	return hostsManifest("content.hauler.cattle.io/v1", "Images", annotations, "  images:\n    - name: "+image+"\n")
}

// A Sync manifest makes hauler contact every registry, chart repository and
// URL in it, so each host is checked like a direct request's before a job or
// the temp manifest copy is made.
func TestSyncManifestHosts_BlockedRefused(t *testing.T) {
	const blocked = "address is not allowed"
	for _, tc := range []struct {
		manifest string
		wantMsg  string
	}{
		{chartsManifest("Charts", "name: x\n      repoURL: oci://127.0.0.1/x"), "charts[0].repoURL: " + blocked},
		{chartsManifest("Charts", "name: x\n      repoURL: http://169.254.169.254/x"), "charts[0].repoURL: " + blocked},
		{chartsManifest("Charts", "name: x\n      repoURL: https://internal.example/charts"), "charts[0].repoURL: " + blocked},
		{chartsManifest("Charts", "name: x\n      repoURL: oci://:5000/charts"), "charts[0].repoURL: not a valid URL"},
		{chartsManifest("Charts", "name: oci://localhost:5000/charts/x"), "charts[0].name: " + blocked},
		{chartsManifest("ThickCharts", "name: x\n      repoURL: oci://[::1]:5000/charts"), "charts[0].repoURL: " + blocked},
		{chartsManifest("ThickCharts", "name: x\n      repoURL: https://charts.example.com\n      extraImages:\n        - ref: internal.example/app:1"), "charts[0].extraImages[0].ref: " + blocked},
		// hauler joins repoURL + "/" + name and helm trims the result
		// (review 111): " oci:" + "/127.0.0.1:5000/charts/x" pulls
		// oci://127.0.0.1:5000/charts/x.
		{splitChart("Charts", " oci:", "/127.0.0.1:5000/charts/x"), "charts[0].repoURL must not start or end with a space"},
		{splitChart("Charts", "\toci:", "/127.0.0.1/x"), "charts[0].repoURL must not start or end with a space"},
		{splitChart("Charts", "\u00a0oci:", "/127.0.0.1/x"), "charts[0].repoURL must not start or end with a space"},
		{splitChart("Charts", " https:", "/169.254.169.254/x.tgz"), "charts[0].repoURL must not start or end with a space"},
		{splitChart("Charts", " http:", "/localhost:8080/x.tgz"), "charts[0].repoURL must not start or end with a space"},
		{splitChart("ThickCharts", " oci:", "/[::1]:5000/x"), "charts[0].repoURL must not start or end with a space"},
		{splitChart("Charts", " oci:/", "localhost:5000/x"), "charts[0].repoURL must not start or end with a space"},
		{splitChart("Charts", "\thttp:/", "169.254.169.254/latest/meta-data/x.tgz"), "charts[0].repoURL must not start or end with a space"},
		{splitChart("Charts", "charts", "x\n"), "charts[0].name must not start or end with a space"},
		{splitChart("Charts", "charts", "x\x7f"), "charts[0].name must not start or end with a space or contain a control character"},
		// repoURL is checked whether or not it contains "://", like Add Chart's.
		{splitChart("Charts", "charts%zz", "x"), "charts[0].repoURL: not a valid URL"},
		{imagesManifest("", "127.0.0.1:5000/img"), "images[0].name: " + blocked},
		{imagesManifest("", "localhost:5000/img"), "images[0].name: " + blocked},
		{imagesManifest("", "mixed.example/app:1"), "images[0].name: " + blocked},
		{imagesManifest("  annotations:\n    hauler.dev/registry: internal.example:5000\n", "app:1"), "annotation hauler.dev/registry: " + blocked},
		{hostsManifest("content.hauler.cattle.io/v1", "Files", "", "  files:\n    - path: http://169.254.169.254/latest/meta-data\n"), "files[0].path: " + blocked},
		{hostsManifest("content.hauler.cattle.io/v1", "Files", "", "  files:\n    - path: https://internal.example/k3s\n"), "files[0].path: " + blocked},
		{hostsManifest("collection.hauler.cattle.io/v1", "ImageTxts", "", "  imageTxts:\n    - ref: http://localhost:8080/images.txt\n"), "imageTxts[0].ref: " + blocked},
	} {
		h, db := hostPolicyHandler(t)
		body := mustJSON(t, map[string]string{"manifestYaml": tc.manifest})
		w := postStore(h, "sync", body)
		expectRefused(t, w, db, h.Cfg.DataDir, tc.manifest, tc.wantMsg)
		expectNoSyncResidue(t, h, tc.manifest, w.Body.String())
	}
}

// hauler may fetch an http(s) Files path or ImageTxts ref even when
// filepath.Join(host, path) exists on disk: it picks the file or http getter
// in random order. So the host is checked then too (review 111).
func TestSyncManifestHosts_ExistingJoinRefused(t *testing.T) {
	for _, tc := range []struct {
		manifest string
		wantMsg  string
	}{
		{hostsManifest("content.hauler.cattle.io/v1", "Files", "", "  files:\n    - path: http://169.254.169.254/../imports/x\n"), "files[0].path: address is not allowed"},
		{hostsManifest("content.hauler.cattle.io/v1alpha1", "ImageTxts", "", "  imageTxts:\n    - ref: http://127.0.0.1:8080/../imports/images.txt\n"), "imageTxts[0].ref: address is not allowed"},
		{hostsManifest("content.hauler.cattle.io/v1", "Files", "", "  files:\n    - path: http://[::1]/../imports/x\n"), "files[0].path: address is not allowed"},
	} {
		h, db := hostPolicyHandler(t)
		imports := filepath.Join(h.Cfg.DataDir, "imports")
		if err := os.MkdirAll(imports, 0755); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"x", "images.txt"} {
			if err := os.WriteFile(filepath.Join(imports, f), []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		w := postStore(h, "sync", mustJSON(t, map[string]string{"manifestYaml": tc.manifest}))
		expectRefused(t, w, db, h.Cfg.DataDir, tc.manifest, tc.wantMsg)
		expectNoSyncResidue(t, h, tc.manifest, w.Body.String())
	}
}

// A host named many times in one Sync request is looked up once.
func TestSyncManifestHosts_HostLookedUpOnce(t *testing.T) {
	h, db := hostPolicyHandler(t)
	lookups := map[string]int{}
	h.Fetcher = safefetch.New(safefetch.Options{
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			lookups[host]++
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		},
	})
	manifest := hostsManifest("content.hauler.cattle.io/v1", "Images", "", "  images:\n    - name: ghcr.io/org/a:1\n    - name: ghcr.io/org/b:1\n") +
		"---\n" + chartsManifest("Charts", "name: app\n      repoURL: oci://ghcr.io/org/charts")
	body := mustJSON(t, map[string]string{"manifestYaml": manifest, "registry": "ghcr.io"})
	if w := postStore(h, "sync", body); w.Code != http.StatusAccepted {
		t.Fatalf("status %d body %q, want 202", w.Code, w.Body.String())
	}
	if n := jobCount(t, db); n != 1 {
		t.Errorf("%d jobs, want 1", n)
	}
	if lookups["ghcr.io"] != 1 || len(lookups) != 1 {
		t.Errorf("lookups %v, want ghcr.io once", lookups)
	}
}

// The same checks apply to a manifest synced from the imports folder.
func TestSyncManifestHosts_ImportsFileRefused(t *testing.T) {
	h, db := hostPolicyHandler(t)
	imports := filepath.Join(h.Cfg.DataDir, "imports")
	if err := os.MkdirAll(imports, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := imagesManifest("", "localhost:5000/img")
	if err := os.WriteFile(filepath.Join(imports, "hosts.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	w := postStore(h, "sync", mustJSON(t, map[string]any{"filenames": []string{"hosts.yaml"}}))
	expectRefused(t, w, db, h.Cfg.DataDir, manifest, "filenames[0]: manifest document 1: images[0].name: address is not allowed")
	expectNoSyncResidue(t, h, manifest, w.Body.String())
}

// expectNoSyncResidue checks that a refused Sync left no temp manifest and
// that its body names neither an address a host resolved to nor the DNS server.
func expectNoSyncResidue(t *testing.T, h *Handler, manifest, body string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(h.Cfg.HaulerTempDir, syncManifestPattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("%q: temp manifest left behind: %v", manifest, left)
	}
	if strings.Contains(body, "192.0.2.53") {
		t.Errorf("%q: 400 body names the DNS server: %q", manifest, body)
	}
	// The addresses hostPolicyHandler resolves names to.
	for _, ip := range []string{"127.0.0.1", "::1", "10.1.2.3"} {
		if !strings.Contains(manifest, ip) && strings.Contains(body, ip) {
			t.Errorf("%q: 400 body names resolved address %s: %q", manifest, ip, body)
		}
	}
}

func TestSyncManifestHosts_AllowedAccepted(t *testing.T) {
	for _, manifest := range []string{
		imagesManifest("", "nginx:1.27"),
		imagesManifest("", "ghcr.io/org/app:1"),
		imagesManifest("", "mirror.example:5000/app:1"),
		imagesManifest("  annotations:\n    hauler.dev/registry: mirror.example:5000\n", "app:1"),
		chartsManifest("Charts", "name: app\n      repoURL: https://charts.example.com"),
		chartsManifest("Charts", "name: app\n      repoURL: oci://ghcr.io/org/charts"),
		chartsManifest("Charts", "name: app\n      repoURL: oci://10.20.0.5:5000/charts"),
		chartsManifest("ThickCharts", "name: app\n      repoURL: https://charts.example.com\n      extraImages:\n        - ref: ghcr.io/org/extra:1"),
		hostsManifest("content.hauler.cattle.io/v1", "Files", "", "  files:\n    - path: https://example.com/k3s\n"),
		hostsManifest("collection.hauler.cattle.io/v1", "ImageTxts", "", "  imageTxts:\n    - ref: https://example.com/images.txt\n"),
	} {
		h, db := hostPolicyHandler(t)
		w := postStore(h, "sync", mustJSON(t, map[string]string{"manifestYaml": manifest}))
		if w.Code != http.StatusAccepted {
			t.Errorf("%q: status %d body %q, want 202", manifest, w.Code, w.Body.String())
			continue
		}
		if n := jobCount(t, db); n != 1 {
			t.Errorf("%q: %d jobs, want 1", manifest, n)
		}
	}
}
