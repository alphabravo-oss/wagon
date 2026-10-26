package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alphabravo-oss/wagon/backend/internal/jobrunner"
)

// hauler syncs each product by pulling <productRegistry>/hauler/<name>-manifest.yaml
// and running it like a -f manifest, without Wagon checking it. A request that
// still sends products or productRegistry, with any value and any key casing
// encoding/json would have matched, must be refused before a job exists.
func TestSync_ProductOptionsRejected(t *testing.T) {
	for _, body := range []string{
		`{"products":"rancher=v2.10.1"}`,
		`{"productRegistry":"registry.attacker.example"}`,
		`{"products":"evil=1","productRegistry":"registry.attacker.example"}`,
		`{"PRODUCTS":"rancher=v2.10.1"}`,
		`{"ProductRegistry":"registry.attacker.example"}`,
		`{"productregiſtry":"registry.attacker.example"}`, // long s folds to s in encoding/json
		`{"products":""}`,
		`{"productRegistry":null}`,
	} {
		t.Run(body, func(t *testing.T) {
			f, jobs := newCard69Fixture(t)
			manifest := mustJSON(t, filesManifest(f.inImports))
			body := strings.Replace(body, "{", `{"manifestYaml":`+manifest+",", 1)
			w := postStore(f.h, "sync", body)
			if w.Code != 400 || !strings.Contains(w.Body.String(), "products and productRegistry are not supported") {
				t.Errorf("status %d body %q, want 400", w.Code, w.Body.String())
			}
			if n := jobs(); n != 0 {
				t.Errorf("expected no jobs, got %d", n)
			}
		})
	}
}

// yaml.v3 decodes an unquoted 1.10 as the float 1.1, so re-encoding through it
// changed the chart version hauler synced. The checked copy keeps every
// unquoted number exactly as written.
func TestSync_ManifestKeepsNumberText(t *testing.T) {
	f, _ := newCard69Fixture(t)
	manifest := "apiVersion: content.hauler.cattle.io/v1\nkind: Charts\nmetadata:\n  name: c\nspec:\n  charts:\n" +
		"    - name: rancher\n      repoURL: https://releases.rancher.com/server-charts/stable\n      version: 1.10\n" +
		"    - name: big\n      repoURL: https://charts.example\n      version: 12345678901234567890\n" +
		"    - name: exp\n      repoURL: https://charts.example\n      version: 1e3\n" +
		"    - name: quoted\n      repoURL: https://charts.example\n      version: \"2.50\"\n"
	w := postStore(f.h, "sync", mustJSON(t, map[string]any{"manifestYaml": manifest}))
	if w.Code != 202 {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}
	copyPath := argAfter(onlyJobArgs(t, f.h, f.h.JobRunner.DB()), "-f")
	got, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"version":1.10`, `"version":12345678901234567890`, `"version":1e3`, `"version":"2.50"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("checked copy %s, want it to contain %s", got, want)
		}
	}
}

// A run that stops before its sync jobs end leaves their checked manifest
// copies in the temp folder. Boot removes them, except one a queued job still
// names: the dispatcher runs that job after the restart.
func TestRemoveStaleManifests(t *testing.T) {
	h, _ := setupTestHandler(t)
	dir := h.Cfg.HaulerTempDir
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "sync-manifest-1.yaml")
	ofFailedJob := filepath.Join(dir, "sync-manifest-2.yaml")
	ofQueuedJob := filepath.Join(dir, "sync-manifest-3.yaml")
	other := filepath.Join(dir, "other.yaml")
	for _, p := range []string{stale, ofFailedJob, ofQueuedJob, other} {
		if err := os.WriteFile(p, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	failed, err := h.JobRunner.CreateJob(ctx, "hauler", []string{"store", "sync", "-f", ofFailedJob}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.JobRunner.DB().Exec(`UPDATE jobs SET status = ? WHERE id = ?`, jobrunner.StatusFailed, failed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.JobRunner.CreateJob(ctx, "hauler", []string{"store", "sync", "-f", ofQueuedJob}, nil); err != nil {
		t.Fatal(err)
	}

	RemoveStaleManifests(ctx, h.JobRunner, dir)

	for p, wantKept := range map[string]bool{stale: false, ofFailedJob: false, ofQueuedJob: true, other: true} {
		_, err := os.Stat(p)
		if kept := err == nil; kept != wantKept {
			t.Errorf("%s: kept = %v, want %v", filepath.Base(p), kept, wantKept)
		}
	}
}
