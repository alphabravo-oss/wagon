package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

// SyncRequest represents the request to sync the store from manifests
type SyncRequest struct {
	HaulID                      int64    `json:"haulId,omitempty"`
	ManifestYaml                string   `json:"manifestYaml,omitempty"`
	Filenames                   []string `json:"filenames,omitempty"`
	Platform                    string   `json:"platform,omitempty"`
	Key                         string   `json:"key,omitempty"`
	CertificateIdentity         string   `json:"certificateIdentity,omitempty"`
	CertificateIdentityRegexp   string   `json:"certificateIdentityRegexp,omitempty"`
	CertificateOidcIssuer       string   `json:"certificateOidcIssuer,omitempty"`
	CertificateOidcIssuerRegexp string   `json:"certificateOidcIssuerRegexp,omitempty"`
	CertificateGithubWorkflow   string   `json:"certificateGithubWorkflow,omitempty"`
	Registry                    string   `json:"registry,omitempty"`
	Products                    string   `json:"products,omitempty"`
	ProductRegistry             string   `json:"productRegistry,omitempty"`
	Rewrite                     string   `json:"rewrite,omitempty"`
	UseTlogVerify               bool     `json:"useTlogVerify"`
}

// Sync handles POST /api/store/sync
func (h *Handler) Sync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req SyncRequest
	if !decodeStoreRequest(w, r, &req) {
		return
	}

	haul, storeArgs, err := h.resolveHaul(r.Context(), req.HaulID)
	if err != nil {
		http.Error(w, "Failed to resolve haul: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Build args for hauler store sync command
	args := []string{"store", "sync"}

	// hauler reads each manifest, and every path in it, as root. Wagon checks
	// them and hands hauler one checked copy, removed once the job ends.
	manifest, filenames, err := h.syncManifest(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tempFile, err := h.writeTempManifest(string(manifest))
	if err != nil {
		log.Printf("Error writing temp manifest: %v", err)
		http.Error(w, "Failed to create temp manifest file", http.StatusInternalServerError)
		return
	}
	args = append(args, "-f", tempFile)

	// Optional platform
	if req.Platform != "" {
		args = append(args, "--platform", req.Platform)
	}

	// Optional key for signature verification
	if req.Key != "" {
		args = append(args, "--key", req.Key)
	}

	// Keyless options
	if req.CertificateIdentity != "" {
		args = append(args, "--certificate-identity", req.CertificateIdentity)
	}
	if req.CertificateIdentityRegexp != "" {
		args = append(args, "--certificate-identity-regexp", req.CertificateIdentityRegexp)
	}
	if req.CertificateOidcIssuer != "" {
		args = append(args, "--certificate-oidc-issuer", req.CertificateOidcIssuer)
	}
	if req.CertificateOidcIssuerRegexp != "" {
		args = append(args, "--certificate-oidc-issuer-regexp", req.CertificateOidcIssuerRegexp)
	}
	if req.CertificateGithubWorkflow != "" {
		args = append(args, "--certificate-github-workflow-repository", req.CertificateGithubWorkflow)
	}

	// Optional registry override
	if req.Registry != "" {
		args = append(args, "--registry", req.Registry)
	}

	// Products
	if req.Products != "" {
		args = append(args, "--products", req.Products)
	}

	// Product registry
	if req.ProductRegistry != "" {
		args = append(args, "--product-registry", req.ProductRegistry)
	}

	// Optional rewrite path (experimental)
	if req.Rewrite != "" {
		args = append(args, "--rewrite", req.Rewrite)
	}

	// Optional tlog verify
	if req.UseTlogVerify {
		args = append(args, "--use-tlog-verify")
	}

	// Scope to this haul's store.
	args = append(args, storeArgs...)

	// Create a job for the sync operation
	job, err := h.JobRunner.CreateJob(r.Context(), "hauler", args, nil)
	if err != nil {
		removeTempManifest(tempFile)
		log.Printf("Error creating sync job: %v", err)
		http.Error(w, "Failed to create sync job", http.StatusInternalServerError)
		return
	}
	h.tagJobHaul(r.Context(), job.ID, haul.ID)
	go func() {
		h.trackAfterJob(job.ID, haul)
		removeTempManifest(tempFile)
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jobId":     job.ID,
		"message":   "Sync job started",
		"filenames": filenames,
		"haulId":    haul.ID,
	})
}

// syncManifest returns the checked manifest for a sync request, from
// manifestYaml or else from filenames (default hauler-manifest.yaml), which
// must be files in <DataDir>/imports. It also returns the filenames to report.
// The error names no host path.
func (h *Handler) syncManifest(req SyncRequest) ([]byte, []string, error) {
	if req.ManifestYaml != "" {
		manifest, err := h.checkSyncManifest([]byte(req.ManifestYaml))
		return manifest, []string{}, err
	}
	filenames := req.Filenames
	if len(filenames) == 0 {
		filenames = []string{"hauler-manifest.yaml"}
	}
	var manifest []byte
	for i, f := range filenames {
		field := fmt.Sprintf("filenames[%d]", i)
		p, err := h.importFile(field, f)
		if err != nil {
			return nil, nil, err
		}
		data, err := readManifestFile(p)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", field, err)
		}
		checked, err := h.checkSyncManifest(data)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", field, err)
		}
		manifest = append(manifest, checked...)
	}
	return manifest, filenames, nil
}

// readManifestFile reads a manifest file of at most maxStoreBody bytes, the
// same cap as a pasted manifest.
func readManifestFile(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, errors.New("cannot be read")
	}
	defer func() { _ = f.Close() }() // read-only, nothing to flush
	data, err := io.ReadAll(io.LimitReader(f, maxStoreBody+1))
	if err != nil {
		return nil, errors.New("cannot be read")
	}
	if len(data) > maxStoreBody {
		return nil, fmt.Errorf("manifest is larger than %d bytes", maxStoreBody)
	}
	return data, nil
}

// removeTempManifest deletes a checked manifest copy once hauler is done.
func removeTempManifest(p string) {
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		log.Printf("Error removing temp manifest: %v", err)
	}
}
