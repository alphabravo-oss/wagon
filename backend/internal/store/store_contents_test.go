package store

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeStoreFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestReadStoreItemsAnnotations(t *testing.T) {
	for _, tc := range []struct {
		name, index string
		want        []storeItem
		bad         bool
	}{
		{name: "missing"},
		{name: "empty", index: `{"manifests":[]}`, want: []storeItem{}},
		{name: "invalid", index: `{`, bad: true},
		{name: "annotations", index: `{"manifests":[
   {"digest":"sha256:a","annotations":{"io.containerd.image.name":"registry/app:v1","org.opencontainers.image.ref.name":"ignored"}},
   {"digest":"sha256:b","annotations":{"org.opencontainers.image.ref.name":"chart.tgz"}},
   {"digest":"sha256:c","annotations":{"io.containerd.image.name":42,"org.opencontainers.image.ref.name":"other.tar.gz"}},
   {"digest":"sha256:d","annotations":{"org.opencontainers.image.ref.name":"readme.txt"}},
   {"digest":"sha256:e"},{"annotations":{"org.opencontainers.image.ref.name":42}}]}`,
			want: []storeItem{{"image", "registry/app:v1", "sha256:a"}, {"chart", "chart.tgz", "sha256:b"}, {"chart", "other.tar.gz", "sha256:c"}, {"file", "readme.txt", "sha256:d"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.index != "" {
				writeStoreFixture(t, filepath.Join(dir, "index.json"), tc.index)
			}
			got, err := readStoreItems(dir)
			if (err != nil) != tc.bad {
				t.Fatalf("error = %v", err)
			}
			if !tc.bad && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("items = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestStoreTrackingAndRescan(t *testing.T) {
	h, db := setupTestHandler(t)
	ctx := context.Background()
	haul, args, err := h.resolveHaul(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, []string{"--store", haul.StoreDir}) {
		t.Fatal(args)
	}
	writeStoreFixture(t, filepath.Join(haul.StoreDir, "index.json"), `{"manifests":[{"digest":"sha256:a","annotations":{"io.containerd.image.name":"app:v1"}}]}`)
	for _, source := range []string{"original.tar.zst", "replacement.tar.zst", ""} {
		if err := h.trackStoreContents(ctx, haul, source); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var source string
	if err := db.QueryRow("SELECT count(*), source_haul FROM store_contents WHERE haul_id = ?", haul.ID).Scan(&count, &source); err != nil {
		t.Fatal(err)
	}
	if count != 1 || source != "original.tar.zst" {
		t.Fatalf("count=%d source=%q", count, source)
	}
	writeStoreFixture(t, filepath.Join(haul.StoreDir, "index.json"), `{"manifests":[{"digest":"sha256:b","annotations":{"org.opencontainers.image.ref.name":"new.txt"}}]}`)
	w := httptest.NewRecorder()
	h.Rescan(w, httptest.NewRequest("POST", "/api/store/rescan", nil))
	var response struct {
		ItemsFound int `json:"itemsFound"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || response.ItemsFound != 1 {
		t.Fatalf("response: %d %s", w.Code, w.Body)
	}
	var name string
	var reset bool
	if err := db.QueryRow("SELECT name, source_haul IS NULL FROM store_contents WHERE haul_id = ?", haul.ID).Scan(&name, &reset); err != nil {
		t.Fatal(err)
	}
	if name != "new.txt" || !reset {
		t.Fatalf("name=%q reset=%v", name, reset)
	}
	writeStoreFixture(t, filepath.Join(haul.StoreDir, "index.json"), `{`)
	w = httptest.NewRecorder()
	h.Rescan(w, httptest.NewRequest("POST", "/api/store/rescan", nil))
	if w.Code != 500 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestGetInfoFakeHauler(t *testing.T) {
	h, db := setupTestHandler(t)
	dir := t.TempDir()
	// GetInfo uses exec.CommandContext("hauler", ...), resolved through PATH.
	script := `#!/bin/sh
[ "$1" = store ] && [ "$2" = info ] && [ "$3" = -o ] && [ "$4" = json ] && [ "$5" = --store ] && [ "$6" = "$EXPECTED_STORE" ] || exit 9
printf '%s' "$STORE_OUTPUT"
exit "$STORE_EXIT"
`
	if err := os.WriteFile(filepath.Join(dir, "hauler"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("EXPECTED_STORE", defaultStoreDir(t, h))
	haul, _, err := h.resolveHaul(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range [][3]string{{"digest-name", "sha256:a", "digest-source"}, {"library/busybox:v1", "sha256:old", "name-source"}, {"chart:1.2", "sha256:c", "chart-source"}} {
		if _, err := db.Exec("INSERT INTO store_contents (haul_id,content_type,name,digest,source_haul) VALUES (?, 'image', ?, ?, ?)", haul.ID, row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, output, exit string
		status             int
	}{
		{"grouped", `[{"Reference":"app:v1","Type":"IMAGE","Digest":"sha256:a","Size":10},{"Reference":"docker.io/busybox:v1","Type":"image"},{"Reference":"chart:1.2","Type":"chart","Size":20},{"Reference":"notes","Type":"file","Size":30},{"Reference":"ignored","Type":"unknown"}]`, "0", 200},
		{"null", "null", "0", 200}, {"blank", "  ", "0", 200}, {"invalid", "broken", "0", 500}, {"failure", "failed", "1", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("STORE_OUTPUT", tc.output)
			t.Setenv("STORE_EXIT", tc.exit)
			w := httptest.NewRecorder()
			h.GetInfo(w, httptest.NewRequest("GET", "/api/store/info", nil))
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if tc.status != 200 {
				return
			}
			var got StoreInfo
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if tc.name == "grouped" {
				want := StoreInfo{Images: []ImageInfo{{Name: "app:v1", Digest: "sha256:a", Size: 10, SourceHaul: "digest-source"}, {Name: "docker.io/busybox:v1", SourceHaul: "name-source"}}, Charts: []ChartInfo{{Name: "chart", Version: "1.2", Size: 20, SourceHaul: "chart-source"}}, Files: []FileInfo{{Name: "notes", Size: 30}}}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("got %#v want %#v", got, want)
				}
			} else if got.Images == nil || got.Charts == nil || got.Files == nil || len(got.Images)+len(got.Charts)+len(got.Files) != 0 {
				t.Fatalf("expected empty arrays: %#v", got)
			}
		})
	}
}

func TestStoreRoutesRejectInvalidRequests(t *testing.T) {
	h, _ := setupTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	for _, route := range []string{"info", "add-image", "add-chart", "add-file", "sync", "save", "load", "extract", "copy", "remove", "rescan", "import"} {
		t.Run(route, func(t *testing.T) {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("PUT", "/api/store/"+route, nil))
			if w.Code != 405 {
				t.Fatalf("status=%d", w.Code)
			}
			if route == "info" || route == "rescan" {
				return
			}
			w = httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/store/"+route, strings.NewReader("{")))
			if w.Code != 400 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
	for _, route := range []string{"info?haul=999", "rescan?haul=999"} {
		method := "GET"
		if strings.HasPrefix(route, "rescan") {
			method = "POST"
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, "/api/store/"+route, nil))
		if w.Code != 400 {
			t.Fatalf("status=%d", w.Code)
		}
	}
}

func TestStoreFilesystemHelpers(t *testing.T) {
	h, _ := setupTestHandler(t)
	path, err := h.writeTempManifest("kind: Manifest\n")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "kind: Manifest\n" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	dir := defaultStoreDir(t, h)
	writeStoreFixture(t, filepath.Join(dir, "old"), "stale")
	if err := h.clearStore(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old")); !os.IsNotExist(err) {
		t.Fatalf("old file: %v", err)
	}
	if info, err := os.Stat(filepath.Join(dir, "blobs", "sha256")); err != nil || !info.IsDir() {
		t.Fatalf("blobs directory: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "oci-layout"))
	if err != nil {
		t.Fatal(err)
	}
	var layout map[string]string
	if err := json.Unmarshal(data, &layout); err != nil {
		t.Fatal(err)
	}
	if layout["imageLayoutVersion"] != "1.0.0" {
		t.Fatal(layout)
	}
	h.Cfg.HaulerTempDir = path
	if _, err := h.writeTempManifest("bad"); err == nil {
		t.Fatal("expected non-directory error")
	}
}
