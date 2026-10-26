package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/alphabravo-oss/wagon/backend/internal/hauls"
	"github.com/alphabravo-oss/wagon/backend/internal/jobrunner"
	"github.com/alphabravo-oss/wagon/backend/internal/safepath"
)

// maxStoreBody caps a JSON store request body. Sync's pasted manifest is the
// largest field.
const maxStoreBody = 1 << 20

// decodeStoreRequest reads a capped JSON body into v. It writes 413 for an
// oversized body and 400 for invalid JSON, and reports whether v is usable.
func decodeStoreRequest(w http.ResponseWriter, r *http.Request, v any) bool {
	_, ok := readStoreRequest(w, r, v)
	return ok
}

// readStoreRequest is decodeStoreRequest that also returns the raw body.
func readStoreRequest(w http.ResponseWriter, r *http.Request, v any) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxStoreBody))
	if err == nil {
		err = json.NewDecoder(bytes.NewReader(body)).Decode(v)
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, fmt.Sprintf("Request body is larger than %d bytes", maxStoreBody), http.StatusRequestEntityTooLarge)
			return nil, false
		}
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

// hasField reports whether the JSON object in body has the key name, matched
// case-insensitively as encoding/json matches struct fields, whatever its value.
func hasField(body []byte, name string) bool {
	var fields map[string]json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&fields); err != nil {
		return false
	}
	for key := range fields {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

// rejectFlagLike prevents hauler's flag parser from reading a positional user value as a flag.
func rejectFlagLike(field, value string) error {
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("%s must not start with '-'", field)
	}
	return nil
}

// importFile confines a local file the caller names in field (add-file
// filePath, sync and load filenames) to an existing regular file in
// <DataDir>/imports, the one folder Wagon reads local files from. hauler runs
// as root, so any other path would be an arbitrary read. The error names no
// host path.
func (h *Handler) importFile(field, p string) (string, error) {
	dir := filepath.Join(h.Cfg.DataDir, "imports")
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("Error creating imports directory: %v", err)
		return "", errors.New("imports folder is unavailable")
	}
	resolved, err := safepath.ConfineFile(dir, p)
	if err == nil && resolved == "" {
		err = errors.New("empty")
	}
	if err != nil {
		return "", fmt.Errorf("%s must be a file in Wagon's imports folder (<data>/imports): %w", field, err)
	}
	return resolved, nil
}

// certsFile confines a key, cert or CA file the caller names in a request
// field (add-image and sync key, add-chart keyFile, certFile and caFile) to an
// existing regular file in <DataDir>/certs, the folder Serve also reads its TLS
// files from. A relative path is taken from <DataDir>/certs, so "registry.pub"
// is <DataDir>/certs/registry.pub. A key named inside a sync manifest is
// different: hauler takes it from the data folder (see manifestKey). hauler
// runs as root, so any other path would be an arbitrary read. The error names
// field but no host path.
func (h *Handler) certsFile(field, p string) (string, error) {
	resolved, err := safepath.ConfineFile(filepath.Join(h.Cfg.DataDir, "certs"), p)
	if err != nil {
		return "", fmt.Errorf("%s must be a file in Wagon's certs folder (<data>/certs): %w", field, err)
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

// syncManifestPattern names the checked manifest copies Sync hands hauler in
// the temp folder. Boot cleanup removes any left by a stop mid-job.
const syncManifestPattern = "sync-manifest-*.yaml"

// RemoveStaleManifests deletes checked Sync manifest copies in tempDir that a
// run which stopped before its jobs ended left behind. A copy a queued job
// names is kept, since the dispatcher still runs that job; the next boot
// removes it.
func RemoveStaleManifests(ctx context.Context, runner *jobrunner.Runner, tempDir string) {
	queued := jobrunner.StatusQueued
	jobs, err := runner.ListJobs(ctx, &queued)
	if err != nil {
		log.Printf("Boot cleanup: cannot list queued jobs, keeping sync manifests: %v", err)
		return
	}
	keep := map[string]bool{}
	for _, job := range jobs {
		for _, a := range job.Args {
			keep[a] = true
		}
	}
	matches, _ := filepath.Glob(filepath.Join(tempDir, syncManifestPattern))
	for _, m := range matches {
		if keep[m] {
			continue
		}
		if err := os.Remove(m); err == nil {
			log.Printf("Boot cleanup: removed stale sync manifest %s", filepath.Base(m))
		}
	}
}

// writeTempManifest writes manifest YAML content to a new, uniquely named
// file in the temp folder and returns its path. The caller removes it.
func (h *Handler) writeTempManifest(yamlContent string) (string, error) {
	if err := os.MkdirAll(h.Cfg.HaulerTempDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}
	f, err := os.CreateTemp(h.Cfg.HaulerTempDir, syncManifestPattern)
	if err != nil {
		return "", fmt.Errorf("failed to create temp manifest: %w", err)
	}
	if _, err := copyAndClose(f, strings.NewReader(yamlContent)); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("failed to write temp manifest: %w", err)
	}
	return f.Name(), nil
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
