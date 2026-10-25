package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/alphabravo-oss/wagon/backend/internal/hauls"
	"github.com/alphabravo-oss/wagon/backend/internal/safepath"
)

// maxServeBody caps a serve request body. The requests are a few short fields.
const maxServeBody = 64 << 10

// decodeServeRequest reads a capped JSON body into v. It writes 413 for an
// oversized body and 400 for invalid JSON, and reports whether v is usable.
func decodeServeRequest(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxServeBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, fmt.Sprintf("Request body is larger than %d bytes", maxServeBody), http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return false
	}
	return true
}

// servePaths checks the caller-supplied paths of a serve request before hauler,
// which runs as root, sees them. directory must lie inside the haul's folder and
// outside its store; tlsCert and tlsKey must be files in Wagon's certs folder.
// Relative paths are taken from those folders. Empty values stay empty. The
// returned paths have symlinks resolved, so hauler opens exactly what was checked.
// Errors name no host paths; they go back to the caller as a 400.
func (h *Handler) servePaths(haul *hauls.Haul, directory, tlsCert, tlsKey string) (dir, cert, key string, err error) {
	if directory != "" {
		if dir, err = safepath.ConfineOutside(filepath.Dir(haul.StoreDir), haul.StoreDir, directory); err != nil {
			return "", "", "", fmt.Errorf("directory must be a folder inside the haul's folder, outside its store: %w", err)
		}
	}
	certsDir := h.certManager.certsDir
	if cert, err = safepath.ConfineFile(certsDir, tlsCert); err != nil {
		return "", "", "", fmt.Errorf("tlsCert must be a file in Wagon's certs folder: %w", err)
	}
	if key, err = safepath.ConfineFile(certsDir, tlsKey); err != nil {
		return "", "", "", fmt.Errorf("tlsKey must be a file in Wagon's certs folder: %w", err)
	}
	return dir, cert, key, nil
}
