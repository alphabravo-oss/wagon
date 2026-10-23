package hauls

import (
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
)

// Handler exposes the haul resource over HTTP.
type Handler struct {
	svc *Service
}

// NewHandler creates a new haul HTTP handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Summary augments a Haul with aggregate counts for list/detail views.
type Summary struct {
	Haul
	ImageCount   int   `json:"imageCount"`
	ChartCount   int   `json:"chartCount"`
	FileCount    int   `json:"fileCount"`
	ArchiveCount int   `json:"archiveCount"`
	ArchiveBytes int64 `json:"archiveBytes"`
}

// Archive describes a built .tar.zst file belonging to a haul.
type Archive struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// RegisterRoutes wires the haul endpoints into the mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/hauls", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			h.List(w, r)
		case http.MethodPost:
			h.Create(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/hauls/", h.routeByID)
}

// routeByID dispatches /api/hauls/{id} and /api/hauls/{id}/archives[/{file}].
func (h *Handler) routeByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/hauls/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}

	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "Invalid haul id", http.StatusBadRequest)
		return
	}

	// /api/hauls/{id}/archives and /api/hauls/{id}/archives/{file}
	if len(parts) >= 2 && parts[1] == "archives" {
		if len(parts) >= 3 && parts[2] != "" {
			h.handleArchiveFile(w, r, id, parts[2])
			return
		}
		if r.Method == http.MethodGet {
			h.ListArchives(w, r, id)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// /api/hauls/{id}
	switch r.Method {
	case http.MethodGet:
		h.Get(w, r, id)
	case http.MethodPatch, http.MethodPut:
		h.Update(w, r, id)
	case http.MethodDelete:
		h.Delete(w, r, id)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// List returns all hauls with aggregate summaries.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	hauls, err := h.svc.List(r.Context())
	if err != nil {
		http.Error(w, "Failed to list hauls: "+err.Error(), http.StatusInternalServerError)
		return
	}
	summaries := make([]Summary, 0, len(hauls))
	for i := range hauls {
		summaries = append(summaries, h.summarize(r, &hauls[i]))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"hauls": summaries})
}

// Get returns a single haul with its summary.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request, id int64) {
	haul, err := h.svc.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "Haul not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, h.summarize(r, haul))
}

type haulRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// Create makes a new haul.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req haulRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Name == nil || strings.TrimSpace(*req.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	desc := ""
	if req.Description != nil {
		desc = *req.Description
	}
	haul, err := h.svc.Create(r.Context(), *req.Name, desc)
	if err != nil {
		http.Error(w, "Failed to create haul: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, h.summarize(r, haul))
}

// Update renames or re-describes a haul.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request, id int64) {
	var req haulRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	haul, err := h.svc.Update(r.Context(), id, req.Name, req.Description)
	if err != nil {
		http.Error(w, "Failed to update haul: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, h.summarize(r, haul))
}

// Delete removes a haul and all of its content.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request, id int64) {
	if err := h.svc.Delete(r.Context(), id); err != nil {
		http.Error(w, "Failed to delete haul: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"message": "Haul deleted"})
}

// ListArchives returns the built archives for a haul.
func (h *Handler) ListArchives(w http.ResponseWriter, r *http.Request, id int64) {
	haul, err := h.svc.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "Haul not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"archives": listArchives(haul)})
}

// handleArchiveFile serves (GET) or deletes (DELETE) a single archive file.
func (h *Handler) handleArchiveFile(w http.ResponseWriter, r *http.Request, id int64, filename string) {
	haul, err := h.svc.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "Haul not found", http.StatusNotFound)
		return
	}
	if !safeArchiveName(filename) {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}
	path := filepath.Join(haul.ArchivesDir(), filename)

	switch r.Method {
	case http.MethodDelete:
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				http.Error(w, "File not found", http.StatusNotFound)
				return
			}
			http.Error(w, "Failed to delete archive", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"message": "Archive deleted", "filename": filename})
	case http.MethodGet:
		info, err := os.Stat(path)
		if err != nil {
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
		f, err := os.Open(path)
		if err != nil {
			http.Error(w, "Error opening file", http.StatusInternalServerError)
			return
		}
		defer func() { _ = f.Close() }() // read-only, nothing to flush
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		if _, err := io.Copy(w, f); err != nil {
			log.Printf("Error serving archive %s: %v", path, err)
		}
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// summarize computes aggregate counts for a haul.
func (h *Handler) summarize(r *http.Request, haul *Haul) Summary {
	s := Summary{Haul: *haul}

	rows, err := h.svc.db.QueryContext(r.Context(),
		`SELECT content_type, COUNT(1) FROM store_contents WHERE haul_id = ? GROUP BY content_type`, haul.ID)
	if err == nil {
		defer func() { _ = rows.Close() }() // close error not actionable; rows are fully read or abandoned
		for rows.Next() {
			var ct string
			var count int
			if err := rows.Scan(&ct, &count); err != nil {
				continue
			}
			switch ct {
			case "image":
				s.ImageCount = count
			case "chart":
				s.ChartCount = count
			case "file":
				s.FileCount = count
			}
		}
	}

	for _, a := range listArchives(haul) {
		s.ArchiveCount++
		s.ArchiveBytes += a.Size
	}
	return s
}

// listArchives enumerates .tar.zst files in a haul's archives directory.
func listArchives(haul *Haul) []Archive {
	entries, err := os.ReadDir(haul.ArchivesDir())
	if err != nil {
		return []Archive{}
	}
	archives := make([]Archive, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".tar.zst") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		archives = append(archives, Archive{Name: e.Name(), Size: info.Size(), Modified: info.ModTime()})
	}
	// Newest first.
	for i := 0; i < len(archives); i++ {
		for j := i + 1; j < len(archives); j++ {
			if archives[i].Modified.Before(archives[j].Modified) {
				archives[i], archives[j] = archives[j], archives[i]
			}
		}
	}
	return archives
}

// safeArchiveName rejects path traversal and enforces the .tar.zst extension.
func safeArchiveName(name string) bool {
	if strings.Contains(name, "..") || strings.ContainsAny(name, "/\\") {
		return false
	}
	return strings.HasSuffix(strings.ToLower(name), ".tar.zst")
}
