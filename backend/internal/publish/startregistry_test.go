package publish

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
	"github.com/alphabravo-oss/wagon/backend/internal/hauls"
	"github.com/alphabravo-oss/wagon/backend/internal/sqlite"
)

// fakeHauler puts a "hauler" script first in PATH for the test.
func fakeHauler(t *testing.T, script string) {
	t.Helper()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hauler"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A haul created before Wagon wrote index.json has a store hauler's registry
// serve cannot open (card 64). Starting its registry must add an empty
// index.json and keep the registry running; an existing index.json is kept.
func TestStartRegistry_StoreWithoutIndex(t *testing.T) {
	const emptyIndex = `{"schemaVersion":2,"manifests":[]}`
	const ownIndex = `{"schemaVersion":2,"manifests":[{"digest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"}]}`
	for _, tc := range []struct {
		name  string
		index string // "" = no index.json, as an empty haul from before PR 88
		want  string
	}{
		{name: "missing index", index: "", want: emptyIndex},
		{name: "existing index", index: ownIndex, want: ownIndex},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runs := filepath.Join(t.TempDir(), "runs.log")
			// Like hauler v1.3.2: exit 1 at once when index.json is missing.
			fakeHauler(t, `while [ $# -gt 0 ]; do [ "$1" = --store ] && store=$2; shift; done
if [ ! -f "$store/index.json" ]; then echo missing >> '`+runs+`'; exit 1; fi
echo running >> '`+runs+`'
exec sleep 30`)

			db, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatalf("open db: %v", err)
			}
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Errorf("close database: %v", err)
				}
			})
			cfg := &config.Config{DataDir: t.TempDir()}
			haulSvc := hauls.NewService(db.DB, cfg)
			haul, err := haulSvc.Create(context.Background(), "Empty Haul", "")
			if err != nil {
				t.Fatalf("create haul: %v", err)
			}
			indexPath := filepath.Join(haul.StoreDir, "index.json")
			if tc.index == "" {
				if err := os.Remove(indexPath); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			} else {
				writePublishFixture(t, indexPath, tc.index)
			}

			mgr := NewManager(cfg, db.DB, haulSvc)
			t.Cleanup(mgr.StopAll)
			pub, err := mgr.Publish(context.Background(), haul.ID, "")
			if err != nil {
				t.Fatalf("publish: %v", err)
			}
			p, err := mgr.startRegistry(haul, pub.Hostname)
			if err != nil {
				t.Fatalf("startRegistry: %v", err)
			}

			var first string
			for deadline := time.Now().Add(5 * time.Second); first == "" && time.Now().Before(deadline); {
				if b, err := os.ReadFile(runs); err == nil {
					first, _, _ = strings.Cut(string(b), "\n")
				}
				if first == "" {
					time.Sleep(20 * time.Millisecond)
				}
			}
			if first != "running" {
				t.Fatalf("first hauler run = %q, want running (hauler found no index.json)", first)
			}
			time.Sleep(200 * time.Millisecond)
			mgr.mu.Lock()
			live := mgr.byHaul[haul.ID]
			mgr.mu.Unlock()
			if live != p {
				t.Errorf("registry for haul %d is no longer running", haul.ID)
			}
			got, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatalf("read index.json: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("index.json = %s, want %s", got, tc.want)
			}
		})
	}
}
