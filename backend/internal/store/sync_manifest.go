package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/alphabravo-oss/wagon/backend/internal/safepath"
)

// syncAPIVersions are the apiVersions hauler store sync reads; it skips or
// rejects documents with any other.
var syncAPIVersions = map[string]bool{
	"content.hauler.cattle.io/v1":          true,
	"content.hauler.cattle.io/v1alpha1":    true,
	"collection.hauler.cattle.io/v1":       true,
	"collection.hauler.cattle.io/v1alpha1": true,
}

// syncDoc holds the fields of a hauler manifest document that name something
// hauler may read from disk. hauler decodes the same JSON with Go's
// encoding/json, so field names match the same way here (case-insensitively).
type syncDoc struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Files []struct {
			Path string `json:"path"`
			Name string `json:"name"`
		} `json:"files"`
		Images []struct {
			Key string `json:"key"`
		} `json:"images"`
		Charts []struct {
			Name    string `json:"name"`
			RepoURL string `json:"repoURL"`
		} `json:"charts"`
		ImageTxts []struct {
			Ref string `json:"ref"`
		} `json:"imageTxts"`
	} `json:"spec"`
}

// checkSyncManifest checks every path a hauler sync manifest makes hauler read
// from disk and returns the documents re-encoded as JSON, which hauler reads as
// YAML. hauler runs as root, so a local path must be a file or folder in
// <DataDir>/imports. Wagon hands hauler the re-encoded copy, so hauler reads
// exactly what was checked. The error names no host path.
func (h *Handler) checkSyncManifest(data []byte) ([]byte, error) {
	var out bytes.Buffer
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for n := 1; ; n++ {
		var v any
		if err := dec.Decode(&v); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("manifest is not valid YAML: %w", err)
		}
		if v == nil {
			continue
		}
		if err := uniqueFoldedKeys(v); err != nil {
			return nil, fmt.Errorf("manifest document %d: %w", n, err)
		}
		doc, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("manifest document %d cannot be read: %w", n, err)
		}
		if err := h.checkSyncDoc(doc); err != nil {
			return nil, fmt.Errorf("manifest document %d: %w", n, err)
		}
		out.WriteString("---\n")
		out.Write(doc)
		out.WriteByte('\n')
	}
	if out.Len() == 0 {
		return nil, errors.New("manifest has no documents")
	}
	return out.Bytes(), nil
}

// uniqueFoldedKeys rejects a mapping with two keys that differ only in case.
// encoding/json matches field names case-insensitively, so such keys would
// leave it unclear which value hauler uses. The fold matches encoding/json's.
func uniqueFoldedKeys(v any) error {
	switch v := v.(type) {
	case map[string]any:
		seen := make(map[string]bool, len(v))
		for k, child := range v {
			f := strings.ToUpper(strings.ToLower(k))
			if seen[f] {
				return fmt.Errorf("keys that differ only in case are not allowed (%q)", k)
			}
			seen[f] = true
			if err := uniqueFoldedKeys(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := uniqueFoldedKeys(child); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkSyncDoc checks one JSON-encoded manifest document by kind, the way
// hauler v1.3 reads it. Unknown kinds are refused, since hauler fails on them.
func (h *Handler) checkSyncDoc(doc []byte) error {
	var d syncDoc
	if err := json.Unmarshal(doc, &d); err != nil {
		return fmt.Errorf("cannot be read: %w", err)
	}
	if !syncAPIVersions[d.APIVersion] {
		return errors.New("apiVersion must be content.hauler.cattle.io or collection.hauler.cattle.io, v1 or v1alpha1")
	}
	switch d.Kind {
	case "Files":
		for i, f := range d.Spec.Files {
			if err := h.manifestSource(f.Path); err != nil {
				return fmt.Errorf("files[%d].path %w", i, err)
			}
			if f.Name != "" && !validFileName(f.Name) {
				return fmt.Errorf("files[%d].name must be a plain file name", i)
			}
		}
	case "Images":
		if err := h.manifestKey(d.Metadata.Annotations["hauler.dev/key"]); err != nil {
			return fmt.Errorf("annotation hauler.dev/key %w", err)
		}
		for i, img := range d.Spec.Images {
			if err := h.manifestKey(img.Key); err != nil {
				return fmt.Errorf("images[%d].key %w", i, err)
			}
		}
	case "Charts", "ThickCharts":
		for i, c := range d.Spec.Charts {
			if err := h.manifestChart(c.Name, c.RepoURL); err != nil {
				return fmt.Errorf("charts[%d] %w", i, err)
			}
		}
	case "ImageTxts":
		for i, it := range d.Spec.ImageTxts {
			if err := h.manifestSource(it.Ref); err != nil {
				return fmt.Errorf("imageTxts[%d].ref %w", i, err)
			}
		}
	default:
		return errors.New("kind must be Files, Images, Charts, ThickCharts or ImageTxts")
	}
	return nil
}

// manifestSource checks a source hauler's getters open (a Files path or an
// ImageTxts ref). They read filepath.Join(host, path) of the parsed URL from
// disk whenever it exists, whatever the scheme, so that must lie in imports.
// Only an http or https URL with a host whose local form does not exist is
// fetched from the network instead.
func (h *Handler) manifestSource(src string) error {
	u, err := url.Parse(src)
	if err != nil {
		return errors.New("is not a valid path or URL")
	}
	local := filepath.Join(u.Host, u.Path)
	if (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && !h.mayExist(local) {
		return nil
	}
	return h.manifestLocal(local)
}

// manifestChart checks a chart the way hauler builds the chart reference and
// helm locates it: helm loads the reference from disk whenever it exists.
func (h *Handler) manifestChart(name, repoURL string) error {
	ref := name
	if strings.HasPrefix(repoURL, "oci://") {
		ref = repoURL + "/" + name
	} else if _, err := url.ParseRequestURI(repoURL); err != nil {
		ref = repoURL + "/" + name
	}
	ref = strings.TrimSpace(ref)
	if !h.mayExist(ref) {
		return nil // fetched from the repository
	}
	if err := h.manifestLocal(ref); err != nil {
		return fmt.Errorf("names a local chart that %w", err)
	}
	return nil
}

// manifestKey checks a cosign public key path. hauler expands a leading ~ to
// root's home folder, so that form is refused.
func (h *Handler) manifestKey(key string) error {
	if key == "" {
		return nil
	}
	if strings.HasPrefix(key, "~") {
		return errors.New("must be a file in Wagon's imports folder (<data>/imports)")
	}
	return h.manifestLocal(key)
}

// manifestLocal checks a path hauler reads from disk. hauler runs in the data
// folder, so a relative path is taken from there. It must be an existing file
// or folder in <DataDir>/imports, with symlinks resolved.
func (h *Handler) manifestLocal(p string) error {
	dir := filepath.Join(h.Cfg.DataDir, "imports")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return errors.New("cannot be checked: the imports folder is unavailable")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(h.Cfg.DataDir, p)
	}
	resolved, err := safepath.Confine(dir, p)
	if err == nil {
		if _, serr := os.Stat(resolved); serr != nil {
			err = errors.New("not found")
		}
	}
	if err != nil {
		return fmt.Errorf("must be a file or folder in Wagon's imports folder (<data>/imports): %w", err)
	}
	return nil
}

// mayExist reports whether hauler, in the data folder, could find p on disk.
// Any error other than "does not exist" counts as found.
func (h *Handler) mayExist(p string) bool {
	if p == "" {
		return false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(h.Cfg.DataDir, p)
	}
	_, err := os.Stat(p)
	return !errors.Is(err, fs.ErrNotExist)
}
