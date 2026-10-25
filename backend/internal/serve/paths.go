package serve

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/alphabravo-oss/wagon/backend/internal/hauls"
)

// maxServeBody caps a serve request body. The requests are a few short fields.
const maxServeBody = 64 << 10

// decodeServeRequest reads a capped JSON body into v and returns the bytes read,
// so callers can also check which fields were sent. It writes 413 for an
// oversized body and 400 for invalid JSON, and reports whether v is usable.
func decodeServeRequest(w http.ResponseWriter, r *http.Request, v any) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxServeBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, fmt.Sprintf("Request body is larger than %d bytes", maxServeBody), http.StatusRequestEntityTooLarge)
			return nil, false
		}
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return nil, false
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(v); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

// servePaths checks the caller-supplied paths of a serve request before hauler,
// which runs as root, sees them. directory must lie inside the haul's folder and
// outside its store; tlsCert and tlsKey must be files in Wagon's certs folder.
// Relative paths are taken from those folders. Empty values stay empty. The
// returned paths have symlinks resolved, so hauler opens exactly what was checked.
func (h *Handler) servePaths(haul *hauls.Haul, directory, tlsCert, tlsKey string) (dir, cert, key string, err error) {
	if directory != "" {
		haulDir := filepath.Dir(haul.StoreDir)
		dir, err = confine(haulDir, directory)
		if err == nil {
			if store, serr := filepath.EvalSymlinks(haul.StoreDir); serr != nil || within(store, dir) {
				err = errors.New("in the haul's store")
			}
		}
		if err != nil {
			return "", "", "", fmt.Errorf("directory must be a folder inside %s, outside its store: %w", haulDir, err)
		}
	}
	certsDir := h.certManager.certsDir
	if cert, err = confineFile(certsDir, tlsCert); err != nil {
		return "", "", "", fmt.Errorf("tlsCert must be a file in %s: %w", certsDir, err)
	}
	if key, err = confineFile(certsDir, tlsKey); err != nil {
		return "", "", "", fmt.Errorf("tlsKey must be a file in %s: %w", certsDir, err)
	}
	return dir, cert, key, nil
}

// confineFile is confine for an existing regular file; "" stays "".
func confineFile(root, p string) (string, error) {
	if p == "" {
		return "", nil
	}
	resolved, err := confine(root, p)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", errors.New("not found")
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	return resolved, nil
}

// confine resolves p (relative to root when not absolute) with symlinks
// evaluated and returns it only when it lies strictly below root. Parts of p
// that do not exist yet are allowed, since hauler creates them, but a dangling
// symlink is not: hauler would follow it.
func confine(root, p string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", root, err)
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(realRoot, p)
	}
	resolved, err := resolveExisting(filepath.Clean(p))
	if err != nil {
		return "", err
	}
	if resolved == realRoot || !within(realRoot, resolved) {
		return "", errors.New("outside the allowed folder")
	}
	return resolved, nil
}

// resolveExisting evaluates symlinks in the longest existing prefix of an
// absolute, clean path and appends the missing rest unchanged.
func resolveExisting(p string) (string, error) {
	rest := ""
	for {
		real, err := filepath.EvalSymlinks(p)
		if err == nil {
			return filepath.Join(real, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", errors.New("cannot be resolved")
		}
		if _, lerr := os.Lstat(p); lerr == nil {
			return "", errors.New("contains a dangling symlink")
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", errors.New("cannot be resolved")
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// within reports whether p is root or below it. Both must be clean and absolute.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
