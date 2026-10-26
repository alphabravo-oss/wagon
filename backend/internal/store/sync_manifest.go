package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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
// hauler may read from disk or fetch from the network. hauler decodes the same
// JSON with Go's encoding/json, so field names match the same way here
// (case-insensitively).
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
			Name string `json:"name"`
			Key  string `json:"key"`
		} `json:"images"`
		Charts []struct {
			Name        string `json:"name"`
			RepoURL     string `json:"repoURL"`
			ExtraImages []struct {
				Ref string `json:"ref"`
			} `json:"extraImages"`
		} `json:"charts"`
		ImageTxts []struct {
			Ref string `json:"ref"`
		} `json:"imageTxts"`
	} `json:"spec"`
}

// checkSyncManifest checks every path a hauler sync manifest makes hauler read
// from disk and returns the documents re-encoded as JSON, which hauler reads as
// YAML, with unquoted numbers kept as written. hauler runs as root, so a local
// path must be a file or folder in <DataDir>/imports. Wagon hands hauler the
// re-encoded copy, so hauler reads exactly what was checked. The error names
// no host path.
func (h *Handler) checkSyncManifest(ctx context.Context, data []byte) ([]byte, error) {
	var out bytes.Buffer
	mark := "\x00wagon-number-" + rand.Text() + "\x00"
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for n := 1; ; n++ {
		var node yaml.Node
		var v any
		err := dec.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		} else if err == nil {
			keepNumberText(&node, mark)
			err = node.Decode(&v)
		}
		if err != nil {
			return nil, fmt.Errorf("manifest is not valid YAML: %w", err)
		}
		if v == nil {
			continue
		}
		v = numbersFromMarks(v, mark)
		if err := uniqueFoldedKeys(v); err != nil {
			return nil, fmt.Errorf("manifest document %d: %w", n, err)
		}
		doc, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("manifest document %d cannot be read: %w", n, err)
		}
		if err := h.checkSyncDoc(ctx, doc); err != nil {
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

// keepNumberText makes each plain int or float value in n decode as mark
// followed by its text, for numbersFromMarks to turn into a json.Number as
// written. yaml.v3 alone decodes 1.10 as the float 1.1, and hauler would then
// sync version "1.1". Mapping keys and anchored scalars (which an alias could
// reuse as a key) are left as they are, as are numbers JSON cannot spell.
func keepNumberText(n *yaml.Node, mark string) {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			keepNumberText(c, mark)
		}
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			keepNumberText(n.Content[i], mark)
		}
	case yaml.ScalarNode:
		tag := n.ShortTag()
		if (tag == "!!int" || tag == "!!float") && n.Anchor == "" && n.Style&yaml.TaggedStyle == 0 && isJSONNumber(n.Value) {
			n.Tag, n.Value = "!!str", mark+n.Value
		}
	}
}

// isJSONNumber reports whether s is a JSON number literal.
func isJSONNumber(s string) bool {
	return s != "" && (s[0] == '-' || (s[0] >= '0' && s[0] <= '9')) && json.Valid([]byte(s))
}

// numbersFromMarks replaces each string keepNumberText marked with a
// json.Number holding the original text.
func numbersFromMarks(v any, mark string) any {
	switch v := v.(type) {
	case string:
		if num, ok := strings.CutPrefix(v, mark); ok {
			return json.Number(num)
		}
	case map[string]any:
		for k, child := range v {
			v[k] = numbersFromMarks(child, mark)
		}
	case []any:
		for i, child := range v {
			v[i] = numbersFromMarks(child, mark)
		}
	}
	return v
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
// Every registry, chart repository and URL hauler would contact is checked
// against the same address policy as a direct request (store_hosts.go).
func (h *Handler) checkSyncDoc(ctx context.Context, doc []byte) error {
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
			if err := h.manifestSource(ctx, fmt.Sprintf("files[%d].path", i), f.Path); err != nil {
				return err
			}
			if f.Name != "" && !validFileName(f.Name) {
				return fmt.Errorf("files[%d].name must be a plain file name", i)
			}
		}
	case "Images":
		if err := h.manifestKey(d.Metadata.Annotations["hauler.dev/key"]); err != nil {
			return fmt.Errorf("annotation hauler.dev/key %w", err)
		}
		// hauler pulls an image without a registry from this one.
		if err := h.checkRegistry(ctx, "annotation hauler.dev/registry", d.Metadata.Annotations["hauler.dev/registry"]); err != nil {
			return err
		}
		for i, img := range d.Spec.Images {
			if err := h.manifestKey(img.Key); err != nil {
				return fmt.Errorf("images[%d].key %w", i, err)
			}
			if err := h.checkRegistry(ctx, fmt.Sprintf("images[%d].name", i), imageRegistry(img.Name)); err != nil {
				return err
			}
		}
	case "Charts", "ThickCharts":
		for i, c := range d.Spec.Charts {
			if err := h.manifestChart(c.Name, c.RepoURL); err != nil {
				return fmt.Errorf("charts[%d] %w", i, err)
			}
			if err := h.manifestChartURL(ctx, fmt.Sprintf("charts[%d].name", i), c.Name); err != nil {
				return err
			}
			if err := h.manifestChartURL(ctx, fmt.Sprintf("charts[%d].repoURL", i), c.RepoURL); err != nil {
				return err
			}
			for j, img := range c.ExtraImages {
				if err := h.checkRegistry(ctx, fmt.Sprintf("charts[%d].extraImages[%d].ref", i, j), imageRegistry(img.Ref)); err != nil {
					return err
				}
			}
		}
	case "ImageTxts":
		for i, it := range d.Spec.ImageTxts {
			if err := h.manifestSource(ctx, fmt.Sprintf("imageTxts[%d].ref", i), it.Ref); err != nil {
				return err
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
// fetched from the network instead, and only when that local form cannot name
// something in or above the data folder: jobs queue, so it could appear
// before hauler runs. The host of a fetched URL is checked like a direct
// request's.
func (h *Handler) manifestSource(ctx context.Context, field, src string) error {
	u, err := url.Parse(src)
	if err != nil {
		return fmt.Errorf("%s is not a valid path or URL", field)
	}
	local := filepath.Join(u.Host, u.Path)
	if (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && !h.mayExist(local) {
		if h.dataFolderEntry(local) {
			return fmt.Errorf("%s is a URL hauler would read from disk if its host and path named a file by the time the job runs; use a real host name", field)
		}
		return h.checkRepoURL(ctx, field, src)
	}
	if err := h.manifestLocal(local); err != nil {
		return fmt.Errorf("%s %w", field, err)
	}
	return nil
}

// dataFolderNames are entries Wagon makes in the data folder, some only later
// (certs on the first TLS serve). The configured store, temp, database and
// registry login paths are added when they lie in the data folder.
var dataFolderNames = []string{"app.db", "certs", "hauls", "imports", "registry-tls", "store", "tmp"}

// dataFolderEntry reports whether the first element of the relative path p
// could name something in the data folder when hauler runs: it starts with "."
// (.docker, or .. out of the folder), names an entry that exists now, or names
// one Wagon makes later. Names match case-insensitively.
func (h *Handler) dataFolderEntry(p string) bool {
	first, _, _ := strings.Cut(p, string(filepath.Separator))
	if strings.HasPrefix(first, ".") {
		return true
	}
	names := slices.Clone(dataFolderNames)
	dataDir := filepath.Clean(h.Cfg.DataDir)
	for _, c := range []string{h.Cfg.HaulerStoreDir, h.Cfg.HaulerTempDir, h.Cfg.DatabasePath, h.Cfg.DockerAuthPath} {
		if c = filepath.Clean(c); filepath.IsAbs(c) && c != dataDir && safepath.Within(dataDir, c) {
			rel, _ := filepath.Rel(dataDir, c)
			top, _, _ := strings.Cut(rel, string(filepath.Separator))
			names = append(names, top)
		}
	}
	for _, db := range []string{"app.db", filepath.Base(h.Cfg.DatabasePath)} {
		names = append(names, db+"-wal", db+"-shm", db+"-journal")
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return true
	}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	for _, n := range names {
		if strings.EqualFold(first, n) {
			return true
		}
	}
	return false
}

// manifestChart checks a chart the way hauler builds the chart reference and
// helm locates it: helm loads the reference from disk whenever it exists. A
// repoURL must be an http, https or oci:// URL or a relative path; a file URL
// or an absolute path would point helm at the host's disk.
func (h *Handler) manifestChart(name, repoURL string) error {
	errRepo := errors.New("repoURL must be an http, https or oci:// URL")
	if repo := strings.TrimSpace(repoURL); filepath.IsAbs(repo) || strings.HasPrefix(strings.ToLower(repo), "file:") {
		return errRepo
	}
	ref := name
	if strings.HasPrefix(repoURL, "oci://") {
		ref = repoURL + "/" + name
	} else if u, err := url.ParseRequestURI(repoURL); err != nil {
		ref = repoURL + "/" + name
	} else if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "oci" {
		return errRepo
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

// manifestChartURL checks the host of a chart name or repoURL that is a URL
// (helm pulls a name that is a URL directly). A relative path has no host;
// manifestChart confines it to imports.
func (h *Handler) manifestChartURL(ctx context.Context, field, v string) error {
	if !strings.Contains(v, "://") {
		return nil
	}
	return h.checkRepoURL(ctx, field, v)
}

// manifestKey checks a cosign public key path. It must be an existing regular
// file in <DataDir>/imports or <DataDir>/certs, where the request's key goes;
// hauler only verifies with it. hauler runs in the data folder, so a relative
// path is taken from there. hauler expands a leading ~ to root's home folder,
// so that form is refused.
func (h *Handler) manifestKey(key string) error {
	if key == "" {
		return nil
	}
	if !strings.HasPrefix(key, "~") {
		p := key
		if !filepath.IsAbs(p) {
			p = filepath.Join(h.Cfg.DataDir, p)
		}
		for _, dir := range []string{"imports", "certs"} {
			if _, err := safepath.ConfineFile(filepath.Join(h.Cfg.DataDir, dir), p); err == nil {
				return nil
			}
		}
	}
	return errors.New("must be a file in Wagon's imports folder (<data>/imports) or certs folder (<data>/certs)")
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
