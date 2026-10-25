package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/alphabravo-oss/wagon/backend/internal/hauls"
	"github.com/alphabravo-oss/wagon/backend/internal/httpjson"
	"github.com/alphabravo-oss/wagon/backend/internal/safepath"
)

// maxStoreBody caps a JSON store request body. Sync's pasted manifest is the
// largest field.
const maxStoreBody = 1 << 20

// decodeStoreRequest reads a capped JSON body into v. It writes 413 for an
// oversized body and 400 for invalid JSON, and reports whether v is usable.
func decodeStoreRequest(w http.ResponseWriter, r *http.Request, v any) bool {
	return httpjson.Decode(w, r, v, maxStoreBody)
}

// rejectFlagLike prevents hauler's flag parser from reading a positional user value as a flag.
func rejectFlagLike(field, value string) error {
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("%s must not start with '-'", field)
	}
	return nil
}

// importFile confines an add-file filePath to an existing regular file in
// <DataDir>/imports, the one folder Wagon reads local files from. hauler runs
// as root, so any other path would be an arbitrary read. The error names no
// host path.
func (h *Handler) importFile(p string) (string, error) {
	dir := filepath.Join(h.Cfg.DataDir, "imports")
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("Error creating imports directory: %v", err)
		return "", errors.New("imports folder is unavailable")
	}
	resolved, err := safepath.ConfineFile(dir, p)
	if err != nil {
		return "", fmt.Errorf("filePath must be a file in Wagon's imports folder (<data>/imports): %w", err)
	}
	return resolved, nil
}

// remoteFileURL checks that an add-file url is an http or https URL with a
// host. Wagon downloads it itself (see fetchTempFile); hauler never sees it.
func remoteFileURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, errors.New("url must be an http:// or https:// URL with a host; put local files in Wagon's imports folder and use filePath")
	}
	return u, nil
}

// validFileName is the add-file name rule: a single path element, so hauler
// serves and extracts it inside its output folder.
func validFileName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\")
}

// fetchTempPrefix names the files url downloads are written to in the imports
// folder. Boot cleanup removes any left by a crash.
const fetchTempPrefix = ".wagon-fetch-"

// fetchTempFile creates an empty file in <DataDir>/imports for a url download.
// The error names no host path.
func (h *Handler) fetchTempFile() (string, error) {
	dir := filepath.Join(h.Cfg.DataDir, "imports")
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("Error creating imports directory: %v", err)
		return "", errors.New("imports folder is unavailable")
	}
	f, err := os.CreateTemp(dir, fetchTempPrefix+"*")
	if err != nil {
		log.Printf("Error creating download file: %v", err)
		return "", errors.New("cannot create a file for the download in the imports folder")
	}
	_ = f.Close()
	return f.Name(), nil
}

// RemoveStaleDownloads deletes url download files left in <dataDir>/imports
// by a run that stopped mid-download.
func RemoveStaleDownloads(dataDir string) {
	matches, _ := filepath.Glob(filepath.Join(dataDir, "imports", fetchTempPrefix+"*"))
	for _, m := range matches {
		if err := os.Remove(m); err == nil {
			log.Printf("Boot cleanup: removed stale download %s", filepath.Base(m))
		}
	}
}

// haulOutputDir confines a folder hauler writes into (extract output, dir://
// copy target) to the haul's own folder, outside the store, archives and
// registry folders Wagon manages there. Relative paths are taken from the
// haul's folder. The error names no host path.
func haulOutputDir(haul *hauls.Haul, field, p string) (string, error) {
	haulDir := filepath.Dir(haul.StoreDir)
	excluded := []string{haul.StoreDir, haul.ArchivesDir(), filepath.Join(haulDir, "registry")}
	resolved, err := safepath.ConfineOutside(haulDir, excluded, p)
	if err != nil {
		return "", fmt.Errorf("%s must be a folder inside the haul's folder, outside its store, archives and registry folders: %w", field, err)
	}
	return resolved, nil
}

// resolveHaul returns the haul a request targets. When haulID is 0 it falls back
// to the default haul. It also returns the "--store <dir>" args that scope a
// hauler command to that haul's isolated store directory.
func (h *Handler) resolveHaul(ctx context.Context, haulID int64) (*hauls.Haul, []string, error) {
	var (
		haul *hauls.Haul
		err  error
	)
	if haulID > 0 {
		haul, err = h.Hauls.Get(ctx, haulID)
	} else {
		haul, err = h.Hauls.EnsureDefault(ctx)
	}
	if err != nil {
		return nil, nil, err
	}
	return haul, []string{"--store", haul.StoreDir}, nil
}

// tagJobHaul records which haul a job operated on, so job history can be filtered.
func (h *Handler) tagJobHaul(ctx context.Context, jobID, haulID int64) {
	if _, err := h.JobRunner.DB().ExecContext(ctx, `UPDATE jobs SET haul_id = ? WHERE id = ?`, haulID, jobID); err != nil {
		log.Printf("Warning: failed to tag job %d with haul %d: %v", jobID, haulID, err)
	}
}

// writeTempManifest writes manifest YAML content to a temporary file and returns the path
func (h *Handler) writeTempManifest(yamlContent string) (string, error) {
	// Ensure temp directory exists
	if err := os.MkdirAll(h.Cfg.HaulerTempDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}

	// Create a temporary file with a predictable name for sync operations
	tempFile := filepath.Join(h.Cfg.HaulerTempDir, fmt.Sprintf("sync-manifest-%d.yaml", makeTimestamp()))
	if err := os.WriteFile(tempFile, []byte(yamlContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write temp manifest: %w", err)
	}

	return tempFile, nil
}

// makeTimestamp returns a unique timestamp-based identifier
func makeTimestamp() int64 {
	return int64(float64(1000000))
}

// clearStore removes a haul's store directory and recreates the OCI layout structure
func (h *Handler) clearStore(storeDir string) error {
	// Remove the store directory
	if err := os.RemoveAll(storeDir); err != nil {
		return fmt.Errorf("removing store directory: %w", err)
	}

	// Recreate the OCI layout structure
	blobsDir := filepath.Join(storeDir, "blobs", "sha256")
	if err := os.MkdirAll(blobsDir, 0755); err != nil {
		return fmt.Errorf("creating blobs directory: %w", err)
	}

	// Create minimal oci-layout
	ociLayout := []byte(`{"imageLayoutVersion": "1.0.0"}`)
	ociLayoutPath := filepath.Join(storeDir, "oci-layout")
	if err := os.WriteFile(ociLayoutPath, ociLayout, 0644); err != nil {
		return fmt.Errorf("creating oci-layout: %w", err)
	}

	log.Printf("Store cleared and recreated at %s", storeDir)
	return nil
}
