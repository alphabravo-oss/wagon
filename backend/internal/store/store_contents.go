package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alphabravo-oss/wagon/backend/internal/hauls"
	"github.com/alphabravo-oss/wagon/backend/internal/jobrunner"
)

// StoreInfo represents the response from hauler store info
type StoreInfo struct {
	Images []ImageInfo `json:"images"`
	Charts []ChartInfo `json:"charts"`
	Files  []FileInfo  `json:"files"`
}

// ImageInfo represents information about a stored image
type ImageInfo struct {
	Name       string `json:"name,omitempty"`
	Digest     string `json:"digest,omitempty"`
	Size       int64  `json:"size,omitempty"`
	SourceHaul string `json:"sourceHaul,omitempty"`
}

// ChartInfo represents information about a stored chart
type ChartInfo struct {
	Name       string `json:"name,omitempty"`
	Version    string `json:"version,omitempty"`
	Digest     string `json:"digest,omitempty"`
	Size       int64  `json:"size,omitempty"`
	SourceHaul string `json:"sourceHaul,omitempty"`
}

// FileInfo represents information about a stored file
type FileInfo struct {
	Name       string `json:"name,omitempty"`
	Digest     string `json:"digest,omitempty"`
	Size       int64  `json:"size,omitempty"`
	SourceHaul string `json:"sourceHaul,omitempty"`
}

// StoreItem represents a single item from hauler store info raw output
type StoreItem struct {
	Reference string `json:"Reference"`
	Type      string `json:"Type"`
	Platform  string `json:"Platform"`
	Digest    string `json:"Digest"`
	Layers    int    `json:"Layers"`
	Size      int64  `json:"Size"`
}

// GetInfo handles GET /api/store/info
// Runs "hauler store info -o json" and returns parsed store contents
func (h *Handler) GetInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Resolve which haul's store to inspect.
	haulID, _ := strconv.ParseInt(r.URL.Query().Get("haul"), 10, 64)
	haul, _, err := h.resolveHaul(ctx, haulID)
	if err != nil {
		http.Error(w, "Failed to resolve haul: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Build args for hauler store info command with JSON output, scoped to the haul.
	args := []string{"store", "info", "-o", "json", "--store", haul.StoreDir}

	// Run hauler store info command directly
	cmd := exec.CommandContext(ctx, "hauler", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Error running store info: %v, output: %s", err, string(output))
		http.Error(w, "Failed to get store info: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Fetch source_haul data from database
	type SourceInfo struct {
		Name       string
		SourceHaul string
	}
	// Map by digest (primary) and by name (fallback)
	digestSourceMap := make(map[string]string)
	nameSourceMap := make(map[string]string)

	db := h.JobRunner.DB()
	rows, err := db.QueryContext(ctx, `SELECT name, digest, source_haul FROM store_contents WHERE haul_id = ?`, haul.ID)
	if err == nil {
		defer func() { _ = rows.Close() }() // read-only query; nothing to flush
		for rows.Next() {
			var s SourceInfo
			var digest sql.NullString
			if err := rows.Scan(&s.Name, &digest, &s.SourceHaul); err == nil {
				nameSourceMap[s.Name] = s.SourceHaul
				if digest.Valid {
					digestSourceMap[digest.String] = s.SourceHaul
				}
			}
		}
	}

	// Parse the array format from hauler store info
	var items []StoreItem
	storeInfo := StoreInfo{
		Images: []ImageInfo{},
		Charts: []ChartInfo{},
		Files:  []FileInfo{},
	}

	// Handle empty store (returns "null")
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "null" || trimmed == "" {
		// Empty store, keep default empty slices
	} else if err := json.Unmarshal(output, &items); err != nil {
		log.Printf("Error parsing store info JSON: %v, output: %s", err, string(output))
		http.Error(w, "Failed to parse store info: "+err.Error(), http.StatusInternalServerError)
		return
	} else {
		// Group items by type
		for _, item := range items {
			// Look up source_haul from database
			// Try digest first (most reliable), then exact name, then normalized name variations
			sourceHaul := digestSourceMap[item.Digest]
			if sourceHaul == "" {
				sourceHaul = nameSourceMap[item.Reference]
			}
			// For images, try matching without registry prefix as fallback
			if sourceHaul == "" {
				normalizedName := item.Reference
				// Strip common registry prefixes
				for _, prefix := range []string{"index.docker.io/", "docker.io/"} {
					if strings.HasPrefix(normalizedName, prefix) {
						normalizedName = strings.TrimPrefix(normalizedName, prefix)
						break
					}
				}
				sourceHaul = nameSourceMap[normalizedName]
				if sourceHaul == "" {
					// Try with library/ prefix for docker hub images
					sourceHaul = nameSourceMap["library/"+normalizedName]
				}
			}

			switch strings.ToLower(item.Type) {
			case "image":
				storeInfo.Images = append(storeInfo.Images, ImageInfo{
					Name:       item.Reference,
					Digest:     item.Digest,
					Size:       item.Size,
					SourceHaul: sourceHaul,
				})
			case "chart":
				// Extract version from reference (format: hauler/chart:version)
				name := item.Reference
				version := ""
				if parts := strings.Split(name, ":"); len(parts) >= 2 {
					name = strings.Join(parts[:len(parts)-1], ":")
					version = parts[len(parts)-1]
				}
				storeInfo.Charts = append(storeInfo.Charts, ChartInfo{
					Name:       name,
					Version:    version,
					Digest:     item.Digest,
					Size:       item.Size,
					SourceHaul: sourceHaul,
				})
			case "file":
				storeInfo.Files = append(storeInfo.Files, FileInfo{
					Name:       item.Reference,
					Digest:     item.Digest,
					Size:       item.Size,
					SourceHaul: sourceHaul,
				})
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(storeInfo)
}

// storeItem is a single artifact discovered in a haul's store index.
type storeItem struct {
	ContentType string
	Name        string
	Digest      string
}

// readStoreItems parses a haul store's index.json into a list of artifacts,
// deriving each item's name from its OCI annotations (the index manifests have
// no top-level name field). Returns an empty slice for a fresh/empty store.
func readStoreItems(storeDir string) ([]storeItem, error) {
	indexData, err := os.ReadFile(filepath.Join(storeDir, "index.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // fresh store, nothing tracked yet
		}
		return nil, fmt.Errorf("reading index.json: %w", err)
	}

	var index struct {
		Manifests []struct {
			Digest      string                 `json:"digest"`
			Annotations map[string]interface{} `json:"annotations"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(indexData, &index); err != nil {
		return nil, fmt.Errorf("parsing index.json: %w", err)
	}

	items := make([]storeItem, 0, len(index.Manifests))
	for _, m := range index.Manifests {
		// Prefer io.containerd.image.name (full reference) over the short ref name.
		var name string
		if m.Annotations != nil {
			if n, ok := m.Annotations["io.containerd.image.name"].(string); ok {
				name = n
			} else if n, ok := m.Annotations["org.opencontainers.image.ref.name"].(string); ok {
				name = n
			}
		}
		if name == "" {
			continue
		}

		contentType := "file"
		if strings.Contains(name, ":") {
			contentType = "image"
		} else if strings.HasSuffix(name, ".tgz") || strings.HasSuffix(name, ".tar.gz") {
			contentType = "chart"
		}
		items = append(items, storeItem{ContentType: contentType, Name: name, Digest: m.Digest})
	}
	return items, nil
}

// trackStoreContents records the artifacts currently in a haul's store. Existing
// rows are preserved (INSERT OR IGNORE) so provenance from prior loads is not
// clobbered by later direct adds; sourceArchive is recorded for newly seen items.
func (h *Handler) trackStoreContents(ctx context.Context, haul *hauls.Haul, sourceArchive string) error {
	items, err := readStoreItems(haul.StoreDir)
	if err != nil {
		return err
	}

	var source interface{}
	if sourceArchive != "" {
		source = sourceArchive
	}

	db := h.JobRunner.DB()
	for _, it := range items {
		if _, err := db.ExecContext(ctx, `
			INSERT OR IGNORE INTO store_contents (haul_id, content_type, name, digest, source_haul)
			VALUES (?, ?, ?, ?, ?)
		`, haul.ID, it.ContentType, it.Name, it.Digest, source); err != nil {
			log.Printf("Error inserting store content %s: %v", it.Name, err)
		}
	}
	log.Printf("Tracked %d items into haul %d (source=%q)", len(items), haul.ID, sourceArchive)
	return nil
}

// trackAfterJob waits for a store-modifying job to finish, then records the
// haul's current contents so summary counts and the contents view stay accurate.
func (h *Handler) trackAfterJob(jobID int64, haul *hauls.Haul) {
	ctx := context.Background()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		j, err := h.JobRunner.GetJob(ctx, jobID)
		if err != nil {
			return
		}
		switch j.Status {
		case jobrunner.StatusSucceeded:
			if err := h.trackStoreContents(ctx, haul, ""); err != nil {
				log.Printf("Warning: failed to track contents for haul %d: %v", haul.ID, err)
			}
			return
		case jobrunner.StatusFailed:
			return
		}
	}
}

// rescanAfterJob waits for a job (e.g. remove) to finish, then fully rebuilds the
// haul's tracked contents so deletions are reflected in the counts.
func (h *Handler) rescanAfterJob(jobID int64, haul *hauls.Haul) {
	ctx := context.Background()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		j, err := h.JobRunner.GetJob(ctx, jobID)
		if err != nil {
			return
		}
		switch j.Status {
		case jobrunner.StatusSucceeded:
			if _, err := h.rescanStore(ctx, haul); err != nil {
				log.Printf("Warning: failed to rescan haul %d: %v", haul.ID, err)
			}
			return
		case jobrunner.StatusFailed:
			return
		}
	}
}

// rescanStore rebuilds a haul's store_contents rows from scratch (used after
// removals and by the manual Rescan endpoint). Source provenance is reset.
func (h *Handler) rescanStore(ctx context.Context, haul *hauls.Haul) (int, error) {
	items, err := readStoreItems(haul.StoreDir)
	if err != nil {
		return 0, err
	}

	db := h.JobRunner.DB()
	if _, err := db.ExecContext(ctx, "DELETE FROM store_contents WHERE haul_id = ?", haul.ID); err != nil {
		return 0, fmt.Errorf("clearing store_contents: %w", err)
	}

	count := 0
	for _, it := range items {
		if _, err := db.ExecContext(ctx, `
			INSERT OR IGNORE INTO store_contents (haul_id, content_type, name, digest, source_haul, loaded_at)
			VALUES (?, ?, ?, ?, NULL, datetime('now'))
		`, haul.ID, it.ContentType, it.Name, it.Digest); err != nil {
			log.Printf("Error inserting store content %s: %v", it.Name, err)
		} else {
			count++
		}
	}
	log.Printf("Rescan complete for haul %d: tracked %d items", haul.ID, count)
	return count, nil
}

// Rescan handles POST /api/store/rescan
func (h *Handler) Rescan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	haulID, _ := strconv.ParseInt(r.URL.Query().Get("haul"), 10, 64)
	haul, _, err := h.resolveHaul(r.Context(), haulID)
	if err != nil {
		http.Error(w, "Failed to resolve haul: "+err.Error(), http.StatusBadRequest)
		return
	}

	count, err := h.rescanStore(r.Context(), haul)
	if err != nil {
		log.Printf("Error rescanning store: %v", err)
		http.Error(w, "Failed to rescan store: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"itemsFound": count,
		"message":    fmt.Sprintf("Store rescanned, tracked %d items", count),
	})
}
