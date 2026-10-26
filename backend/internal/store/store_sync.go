package store

import (
	"encoding/json"
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

	// Build file list: either from provided filenames or temp manifest from YAML
	var filenames []string
	var tempFiles []string
	defer func() {
		// Clean up temporary files after job starts
		for _, f := range tempFiles {
			if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
				log.Printf("Error removing cleanup file: %v", err)
			}
		}
	}()

	if req.ManifestYaml != "" {
		// Write manifest YAML to temp file
		tempFile, err := h.writeTempManifest(req.ManifestYaml)
		if err != nil {
			log.Printf("Error writing temp manifest: %v", err)
			http.Error(w, "Failed to create temp manifest file", http.StatusInternalServerError)
			return
		}
		tempFiles = append(tempFiles, tempFile)
		filenames = append(filenames, tempFile)
	} else if len(req.Filenames) > 0 {
		filenames = req.Filenames
	} else {
		// Default to hauler-manifest.yaml as per hauler CLI
		filenames = []string{"hauler-manifest.yaml"}
	}

	// Add each file with -f flag
	for _, f := range filenames {
		args = append(args, "-f", f)
	}

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
		log.Printf("Error creating sync job: %v", err)
		http.Error(w, "Failed to create sync job", http.StatusInternalServerError)
		return
	}
	h.tagJobHaul(r.Context(), job.ID, haul.ID)
	go h.trackAfterJob(job.ID, haul)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jobId":     job.ID,
		"message":   "Sync job started",
		"filenames": filenames,
		"haulId":    haul.ID,
	})
}
