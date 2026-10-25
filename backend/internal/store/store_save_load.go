package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alphabravo-oss/wagon/backend/internal/jobrunner"
)

// SaveRequest represents the request to save the store to an archive
type SaveRequest struct {
	HaulID     int64  `json:"haulId,omitempty"`
	Filename   string `json:"filename,omitempty"`
	Platform   string `json:"platform,omitempty"`
	Containerd string `json:"containerd,omitempty"`
}

// ExtractRequest represents the request to extract an artifact from the store
type ExtractRequest struct {
	HaulID      int64  `json:"haulId,omitempty"`
	ArtifactRef string `json:"artifactRef"`
	OutputDir   string `json:"outputDir,omitempty"`
}

// LoadRequest represents the request to load archives into the store
type LoadRequest struct {
	HaulID    int64    `json:"haulId,omitempty"`
	Filenames []string `json:"filenames,omitempty"`
	Clear     bool     `json:"clear"`
}

// Save handles POST /api/store/save
func (h *Handler) Save(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req SaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	haul, storeArgs, err := h.resolveHaul(r.Context(), req.HaulID)
	if err != nil {
		http.Error(w, "Failed to resolve haul: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Default filename if not provided. Archives are written into the haul's
	// own archives directory so they stay associated with the haul.
	filename := strings.TrimSpace(req.Filename)
	if filename == "" {
		filename = haul.Slug + ".tar.zst"
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".tar.zst") {
		filename += ".tar.zst"
	}
	// Reject path traversal in user-supplied filenames.
	if strings.Contains(filename, "..") || strings.ContainsAny(filename, "/\\") {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	if err := os.MkdirAll(haul.ArchivesDir(), 0755); err != nil {
		http.Error(w, "Failed to prepare archives directory: "+err.Error(), http.StatusInternalServerError)
		return
	}
	archivePath := filepath.Join(haul.ArchivesDir(), filename)

	// Build args for hauler store save command
	args := []string{"store", "save", "--filename", archivePath}

	// Optional platform
	if req.Platform != "" {
		args = append(args, "--platform", req.Platform)
	}

	// Optional containerd target
	if req.Containerd != "" {
		args = append(args, "--containerd", req.Containerd)
	}

	// Scope to this haul's store.
	args = append(args, storeArgs...)

	job, err := h.JobRunner.CreateJob(r.Context(), "hauler", args, nil)
	if err != nil {
		log.Printf("Error creating save job: %v", err)
		http.Error(w, "Failed to create save job", http.StatusInternalServerError)
		return
	}
	h.tagJobHaul(r.Context(), job.ID, haul.ID)

	// Track the archive path and download URL once the job succeeds.
	go h.trackSaveResult(job.ID, haul.ID, archivePath, filename)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jobId":    job.ID,
		"message":  "Save job started",
		"filename": filename,
		"haulId":   haul.ID,
	})
}

// trackSaveResult waits for a save job to finish and records the resulting
// archive path and download URL on the job. Uses a background context because
// it outlives the originating HTTP request.
func (h *Handler) trackSaveResult(jobID, haulID int64, archivePath, filename string) {
	ctx := context.Background()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		job, err := h.JobRunner.GetJob(ctx, jobID)
		if err != nil {
			return
		}
		switch job.Status {
		case jobrunner.StatusSucceeded:
			if _, err := os.Stat(archivePath); err == nil {
				result := map[string]interface{}{
					"archivePath": archivePath,
					"filename":    filename,
					"downloadUrl": fmt.Sprintf("/api/hauls/%d/archives/%s", haulID, filename),
				}
				resultJSON, _ := json.Marshal(result)
				_ = h.JobRunner.UpdateResult(ctx, jobID, string(resultJSON))
			}
			return
		case jobrunner.StatusFailed:
			return
		}
	}
}

// Extract handles POST /api/store/extract
func (h *Handler) Extract(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ExtractRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.ArtifactRef == "" {
		http.Error(w, "artifactRef is required", http.StatusBadRequest)
		return
	}
	if err := rejectFlagLike("artifactRef", req.ArtifactRef); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	haul, storeArgs, err := h.resolveHaul(r.Context(), req.HaulID)
	if err != nil {
		http.Error(w, "Failed to resolve haul: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Build args for hauler store extract command
	args := []string{"store", "extract", req.ArtifactRef}

	// Optional output directory
	if req.OutputDir != "" {
		args = append(args, "--output", req.OutputDir)
	}

	// Scope to this haul's store.
	args = append(args, storeArgs...)

	job, err := h.JobRunner.CreateJob(r.Context(), "hauler", args, nil)
	if err != nil {
		log.Printf("Error creating extract job: %v", err)
		http.Error(w, "Failed to create extract job", http.StatusInternalServerError)
		return
	}
	h.tagJobHaul(r.Context(), job.ID, haul.ID)

	// Record the output directory on success.
	go h.trackExtractResult(job.ID, req.OutputDir)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jobId":       job.ID,
		"message":     "Extract job started",
		"artifactRef": req.ArtifactRef,
		"outputDir":   req.OutputDir,
		"haulId":      haul.ID,
	})
}

// trackExtractResult records the output directory once an extract job succeeds.
func (h *Handler) trackExtractResult(jobID int64, outputDir string) {
	ctx := context.Background()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		job, err := h.JobRunner.GetJob(ctx, jobID)
		if err != nil {
			return
		}
		switch job.Status {
		case jobrunner.StatusSucceeded:
			resultOutputDir := outputDir
			if resultOutputDir == "" {
				resultOutputDir = "."
			}
			resultJSON, _ := json.Marshal(map[string]interface{}{"outputDir": resultOutputDir})
			_ = h.JobRunner.UpdateResult(ctx, jobID, string(resultJSON))
			return
		case jobrunner.StatusFailed:
			return
		}
	}
}

// Load handles POST /api/store/load
func (h *Handler) Load(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req LoadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	haul, storeArgs, err := h.resolveHaul(ctx, req.HaulID)
	if err != nil {
		http.Error(w, "Failed to resolve haul: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Clear this haul's store if requested.
	if req.Clear {
		if err := h.clearStore(haul.StoreDir); err != nil {
			log.Printf("Error clearing store: %v", err)
			http.Error(w, "Failed to clear store: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := h.JobRunner.DB().ExecContext(ctx, `DELETE FROM store_contents WHERE haul_id = ?`, haul.ID); err != nil {
			log.Printf("Warning: failed to clear tracked contents for haul %d: %v", haul.ID, err)
		}
	}

	// Determine archives to load. Bare filenames are resolved against the haul's
	// archives directory; absolute paths are used as-is.
	filenames := req.Filenames
	if len(filenames) == 0 {
		http.Error(w, "at least one filename is required", http.StatusBadRequest)
		return
	}
	resolved := make([]string, 0, len(filenames))
	for _, f := range filenames {
		if filepath.IsAbs(f) {
			resolved = append(resolved, f)
		} else {
			resolved = append(resolved, filepath.Join(haul.ArchivesDir(), f))
		}
	}

	// Build args for hauler store load command
	args := []string{"store", "load"}
	for _, f := range resolved {
		args = append(args, "-f", f)
	}
	args = append(args, storeArgs...)

	job, err := h.JobRunner.CreateJob(ctx, "hauler", args, nil)
	if err != nil {
		log.Printf("Error creating load job: %v", err)
		http.Error(w, "Failed to create load job", http.StatusInternalServerError)
		return
	}
	h.tagJobHaul(ctx, job.ID, haul.ID)

	// After the load completes, track what landed in the store for this haul.
	jobID := job.ID
	go func() {
		bgCtx := context.Background()
		for {
			time.Sleep(500 * time.Millisecond)
			j, err := h.JobRunner.GetJob(bgCtx, jobID)
			if err != nil {
				log.Printf("Error getting job status %d: %v", jobID, err)
				return
			}
			if j.Status == jobrunner.StatusSucceeded {
				for _, f := range filenames {
					if err := h.trackStoreContents(bgCtx, haul, filepath.Base(f)); err != nil {
						log.Printf("Warning: failed to track contents for %s: %v", f, err)
					}
				}
				return
			}
			if j.Status == jobrunner.StatusFailed {
				log.Printf("Load job %d failed, skipping tracking", jobID)
				return
			}
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jobId":     job.ID,
		"message":   "Load job started",
		"filenames": filenames,
		"cleared":   req.Clear,
		"haulId":    haul.ID,
	})
}

// Import handles POST /api/store/import. It accepts a .tar.zst upload, saves it
// into the target haul's archives directory, and kicks off a load so the
// archive's contents land in that haul's isolated store.
//
// Form fields: file (the archive), haulId (target haul), clear ("true" to wipe
// the haul's store before loading).
func (h *Handler) Import(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse multipart form (max 100GB)
	if err := r.ParseMultipartForm(100 << 30); err != nil {
		log.Printf("Error parsing multipart form: %v", err)
		http.Error(w, "Failed to parse form data", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	haulID, _ := strconv.ParseInt(r.FormValue("haulId"), 10, 64)
	haul, storeArgs, err := h.resolveHaul(ctx, haulID)
	if err != nil {
		http.Error(w, "Failed to resolve haul: "+err.Error(), http.StatusBadRequest)
		return
	}
	clear := r.FormValue("clear") == "true"

	// Get the file from form
	file, header, err := r.FormFile("file")
	if err != nil {
		log.Printf("Error getting file from form: %v", err)
		http.Error(w, "No file provided or error reading file", http.StatusBadRequest)
		return
	}
	defer func() { _ = file.Close() }() // read-only, nothing to flush

	filename := header.Filename
	if !strings.HasSuffix(strings.ToLower(filename), ".tar.zst") {
		http.Error(w, "Only .tar.zst files are allowed", http.StatusBadRequest)
		return
	}
	if strings.Contains(filename, "..") || strings.ContainsAny(filename, "/\\") {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	if err := os.MkdirAll(haul.ArchivesDir(), 0755); err != nil {
		log.Printf("Error creating archives directory: %v", err)
		http.Error(w, "Failed to create archives directory", http.StatusInternalServerError)
		return
	}

	destinationPath := filepath.Join(haul.ArchivesDir(), filename)
	destFile, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		log.Printf("Error creating destination file: %v", err)
		http.Error(w, "Failed to create file", http.StatusInternalServerError)
		return
	}

	written, err := copyAndClose(destFile, file)
	if err != nil {
		log.Printf("Error copying file: %v", err)
		if err := os.Remove(destinationPath); err != nil && !os.IsNotExist(err) {
			log.Printf("Error removing cleanup file: %v", err)
		}
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}
	log.Printf("Imported archive into haul %d: %s (%d bytes)", haul.ID, filename, written)

	// Optionally clear the haul's store before loading.
	if clear {
		if err := h.clearStore(haul.StoreDir); err != nil {
			http.Error(w, "Failed to clear store: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := h.JobRunner.DB().ExecContext(ctx, `DELETE FROM store_contents WHERE haul_id = ?`, haul.ID); err != nil {
			log.Printf("Warning: failed to clear tracked contents for haul %d: %v", haul.ID, err)
		}
	}

	// Kick off a load of the freshly uploaded archive into the haul's store.
	args := []string{"store", "load", "-f", destinationPath}
	args = append(args, storeArgs...)
	job, err := h.JobRunner.CreateJob(ctx, "hauler", args, nil)
	if err != nil {
		log.Printf("Error creating load job: %v", err)
		http.Error(w, "Failed to create load job", http.StatusInternalServerError)
		return
	}
	h.tagJobHaul(ctx, job.ID, haul.ID)

	jobID := job.ID
	go func() {
		bgCtx := context.Background()
		for {
			time.Sleep(500 * time.Millisecond)
			j, err := h.JobRunner.GetJob(bgCtx, jobID)
			if err != nil {
				return
			}
			if j.Status == jobrunner.StatusSucceeded {
				if err := h.trackStoreContents(bgCtx, haul, filename); err != nil {
					log.Printf("Warning: failed to track contents for %s: %v", filename, err)
				}
				return
			}
			if j.Status == jobrunner.StatusFailed {
				return
			}
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"message":  "Archive imported, load started",
		"filename": filename,
		"size":     written,
		"jobId":    job.ID,
		"haulId":   haul.ID,
	})
}

// copyAndClose copies src into dst and closes dst, returning the Close error
// when the copy itself succeeded so an incompletely flushed upload is treated
// as a failed save rather than handed to a load job.
func copyAndClose(dst io.WriteCloser, src io.Reader) (int64, error) {
	written, err := io.Copy(dst, src)
	if err != nil {
		_ = dst.Close() // already failing; the copy error is the one to report
		return written, err
	}
	if err := dst.Close(); err != nil {
		return written, fmt.Errorf("close: %w", err)
	}
	return written, nil
}
