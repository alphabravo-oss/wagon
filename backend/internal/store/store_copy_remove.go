package store

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// CopyRequest represents the request to copy the store to a registry or directory
type CopyRequest struct {
	HaulID    int64  `json:"haulId,omitempty"`
	Target    string `json:"target"`
	Insecure  bool   `json:"insecure"`
	PlainHTTP bool   `json:"plainHttp"`
	Only      string `json:"only,omitempty"`
}

// RemoveRequest represents the request to remove artifacts from the store
type RemoveRequest struct {
	HaulID int64  `json:"haulId,omitempty"`
	Match  string `json:"match"`
	Force  bool   `json:"force"`
}

// Copy handles POST /api/store/copy
func (h *Handler) Copy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req CopyRequest
	if !decodeStoreRequest(w, r, &req) {
		return
	}

	if req.Target == "" {
		http.Error(w, "target is required", http.StatusBadRequest)
		return
	}

	// Validate target format
	if !strings.HasPrefix(req.Target, "registry://") && !strings.HasPrefix(req.Target, "dir://") {
		http.Error(w, "target must start with registry:// or dir://", http.StatusBadRequest)
		return
	}

	haul, storeArgs, err := h.resolveHaul(r.Context(), req.HaulID)
	if err != nil {
		http.Error(w, "Failed to resolve haul: "+err.Error(), http.StatusBadRequest)
		return
	}

	// A dir:// target is a folder hauler writes into as root, so it stays
	// inside the haul's folder.
	target := req.Target
	if dir, ok := strings.CutPrefix(target, "dir://"); ok {
		resolved, err := haulOutputDir(haul, "dir:// target", dir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		target = "dir://" + resolved
	}

	// Build args for hauler store copy command
	args := []string{"store", "copy", target}

	// Optional insecure flag
	if req.Insecure {
		args = append(args, "--insecure")
	}

	// Optional plain HTTP flag
	if req.PlainHTTP {
		args = append(args, "--plain-http")
	}

	// Optional only filter (sig, att)
	if req.Only != "" {
		args = append(args, "--only", req.Only)
	}

	// Scope to this haul's store.
	args = append(args, storeArgs...)

	job, err := h.JobRunner.CreateJob(r.Context(), "hauler", args, nil)
	if err != nil {
		log.Printf("Error creating copy job: %v", err)
		http.Error(w, "Failed to create copy job", http.StatusInternalServerError)
		return
	}
	h.tagJobHaul(r.Context(), job.ID, haul.ID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jobId":   job.ID,
		"message": "Copy job started",
		"target":  target,
		"haulId":  haul.ID,
	})
}

// Remove handles POST /api/store/remove
func (h *Handler) Remove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req RemoveRequest
	if !decodeStoreRequest(w, r, &req) {
		return
	}

	if req.Match == "" {
		http.Error(w, "match is required", http.StatusBadRequest)
		return
	}
	if err := rejectFlagLike("match", req.Match); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	haul, storeArgs, err := h.resolveHaul(r.Context(), req.HaulID)
	if err != nil {
		http.Error(w, "Failed to resolve haul: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Build args for hauler store remove command
	args := []string{"store", "remove", req.Match}

	// Optional force flag to bypass confirmation
	if req.Force {
		args = append(args, "--force")
	}

	// Scope to this haul's store.
	args = append(args, storeArgs...)

	job, err := h.JobRunner.CreateJob(r.Context(), "hauler", args, nil)
	if err != nil {
		log.Printf("Error creating remove job: %v", err)
		http.Error(w, "Failed to create remove job", http.StatusInternalServerError)
		return
	}
	h.tagJobHaul(r.Context(), job.ID, haul.ID)
	go h.rescanAfterJob(job.ID, haul)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jobId":   job.ID,
		"message": "Remove job started",
		"match":   req.Match,
		"force":   req.Force,
		"haulId":  haul.ID,
	})
}
