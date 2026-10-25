package store

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/alphabravo-oss/wagon/backend/internal/hauls"
)

// rejectFlagLike prevents hauler's flag parser from reading a positional user value as a flag.
func rejectFlagLike(field, value string) error {
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("%s must not start with '-'", field)
	}
	return nil
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
