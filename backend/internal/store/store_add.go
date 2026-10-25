package store

import (
	"encoding/json"
	"log"
	"net/http"
)

// AddImageRequest represents the request to add an image to the store
type AddImageRequest struct {
	HaulID                      int64  `json:"haulId,omitempty"`
	ImageRef                    string `json:"imageRef"`
	Platform                    string `json:"platform,omitempty"`
	Key                         string `json:"key,omitempty"`
	CertificateIdentity         string `json:"certificateIdentity,omitempty"`
	CertificateIdentityRegexp   string `json:"certificateIdentityRegexp,omitempty"`
	CertificateOidcIssuer       string `json:"certificateOidcIssuer,omitempty"`
	CertificateOidcIssuerRegexp string `json:"certificateOidcIssuerRegexp,omitempty"`
	CertificateGithubWorkflow   string `json:"certificateGithubWorkflow,omitempty"`
	Rewrite                     string `json:"rewrite,omitempty"`
	UseTlogVerify               bool   `json:"useTlogVerify"`
}

// AddImage handles POST /api/store/add-image
func (h *Handler) AddImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req AddImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.ImageRef == "" {
		http.Error(w, "imageRef is required", http.StatusBadRequest)
		return
	}

	haul, storeArgs, err := h.resolveHaul(r.Context(), req.HaulID)
	if err != nil {
		http.Error(w, "Failed to resolve haul: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Build args for hauler store add image command
	args := []string{"store", "add", "image", req.ImageRef}

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

	// Optional rewrite path
	if req.Rewrite != "" {
		args = append(args, "--rewrite", req.Rewrite)
	}

	// Optional tlog verify
	if req.UseTlogVerify {
		args = append(args, "--use-tlog-verify")
	}

	// Scope to this haul's store.
	args = append(args, storeArgs...)

	// Create a job for the add image operation
	job, err := h.JobRunner.CreateJob(r.Context(), "hauler", args, nil)
	if err != nil {
		log.Printf("Error creating add image job: %v", err)
		http.Error(w, "Failed to create add image job", http.StatusInternalServerError)
		return
	}
	h.tagJobHaul(r.Context(), job.ID, haul.ID)
	go h.trackAfterJob(job.ID, haul)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jobId":    job.ID,
		"message":  "Add image job started",
		"imageRef": req.ImageRef,
		"haulId":   haul.ID,
	})
}

// AddChartRequest represents the request to add a chart to the store.
// It has no username/password: hauler ignores them on `store add chart` and
// they would be visible in the process argv.
type AddChartRequest struct {
	HaulID                int64  `json:"haulId,omitempty"`
	Name                  string `json:"name"`
	RepoURL               string `json:"repoUrl,omitempty"`
	Version               string `json:"version,omitempty"`
	KeyFile               string `json:"keyFile,omitempty"`
	CertFile              string `json:"certFile,omitempty"`
	CAFile                string `json:"caFile,omitempty"`
	InsecureSkipTLSVerify bool   `json:"insecureSkipTlsVerify"`
	PlainHTTP             bool   `json:"plainHttp"`
	Verify                bool   `json:"verify"`
	AddDependencies       bool   `json:"addDependencies"`
	AddImages             bool   `json:"addImages"`
}

// AddChart handles POST /api/store/add-chart
func (h *Handler) AddChart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req AddChartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	haul, storeArgs, err := h.resolveHaul(r.Context(), req.HaulID)
	if err != nil {
		http.Error(w, "Failed to resolve haul: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Build args for hauler store add chart command
	args := []string{"store", "add", "chart", req.Name}

	// Optional repo URL
	if req.RepoURL != "" {
		args = append(args, "--repo", req.RepoURL)
	}

	// Optional version
	if req.Version != "" {
		args = append(args, "--version", req.Version)
	}

	// Optional TLS files
	if req.KeyFile != "" {
		args = append(args, "--key-file", req.KeyFile)
	}
	if req.CertFile != "" {
		args = append(args, "--cert-file", req.CertFile)
	}
	if req.CAFile != "" {
		args = append(args, "--ca-file", req.CAFile)
	}

	// TLS options
	if req.InsecureSkipTLSVerify {
		args = append(args, "--insecure-skip-tls-verify")
	}
	if req.PlainHTTP {
		args = append(args, "--plain-http")
	}

	// Verify option
	if req.Verify {
		args = append(args, "--verify")
	}

	// Capability-driven options
	if req.AddDependencies {
		args = append(args, "--add-dependencies")
	}
	if req.AddImages {
		args = append(args, "--add-images")
	}

	// Scope to this haul's store.
	args = append(args, storeArgs...)

	// Create a job for the add chart operation
	job, err := h.JobRunner.CreateJob(r.Context(), "hauler", args, nil)
	if err != nil {
		log.Printf("Error creating add chart job: %v", err)
		http.Error(w, "Failed to create add chart job", http.StatusInternalServerError)
		return
	}
	h.tagJobHaul(r.Context(), job.ID, haul.ID)
	go h.trackAfterJob(job.ID, haul)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jobId":   job.ID,
		"message": "Add chart job started",
		"name":    req.Name,
		"haulId":  haul.ID,
	})
}

// AddFileRequest represents the request to add a file to the store
type AddFileRequest struct {
	HaulID   int64  `json:"haulId,omitempty"`
	FilePath string `json:"filePath,omitempty"`
	URL      string `json:"url,omitempty"`
	Name     string `json:"name,omitempty"`
}

// AddFile handles POST /api/store/add-file
func (h *Handler) AddFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req AddFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate that either filePath or URL is provided (mutually exclusive)
	if req.FilePath == "" && req.URL == "" {
		http.Error(w, "Either filePath or url is required", http.StatusBadRequest)
		return
	}
	if req.FilePath != "" && req.URL != "" {
		http.Error(w, "Please provide either filePath or url, not both", http.StatusBadRequest)
		return
	}

	haul, storeArgs, err := h.resolveHaul(r.Context(), req.HaulID)
	if err != nil {
		http.Error(w, "Failed to resolve haul: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Determine the file source
	fileSource := req.FilePath
	if fileSource == "" {
		fileSource = req.URL
	}

	// Build args for hauler store add file command
	args := []string{"store", "add", "file", fileSource}

	// Optional name rewrite
	if req.Name != "" {
		args = append(args, "--name", req.Name)
	}

	// Scope to this haul's store.
	args = append(args, storeArgs...)

	// Create a job for the add file operation
	job, err := h.JobRunner.CreateJob(r.Context(), "hauler", args, nil)
	if err != nil {
		log.Printf("Error creating add file job: %v", err)
		http.Error(w, "Failed to create add file job", http.StatusInternalServerError)
		return
	}
	h.tagJobHaul(r.Context(), job.ID, haul.ID)
	go h.trackAfterJob(job.ID, haul)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jobId":   job.ID,
		"message": "Add file job started",
		"file":    fileSource,
		"haulId":  haul.ID,
	})
}
