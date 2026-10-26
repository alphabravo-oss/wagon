package registry

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
	"github.com/alphabravo-oss/wagon/backend/internal/jobrunner"
)

// Handler handles HTTP requests for registry operations
type Handler struct {
	jobRunner *jobrunner.Runner
	cfg       *config.Config
}

// NewHandler creates a new registry handler
func NewHandler(jobRunner *jobrunner.Runner, cfg *config.Config) *Handler {
	return &Handler{
		jobRunner: jobRunner,
		cfg:       cfg,
	}
}

// LoginRequest represents the request to login to a registry
type LoginRequest struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// LogoutRequest represents the request to logout from a registry
type LogoutRequest struct {
	Registry string `json:"registry"`
}

// loginSavedMessage is the login response message. hauler login saves the
// credentials without checking them with the registry, so a wrong password
// only shows up when a pull or push fails.
const loginSavedMessage = "Login job started. hauler saves the credentials without checking them with the registry; a wrong password shows up as a failed pull or push."

// Login handles POST /api/registry/login
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if req.Registry == "" {
		http.Error(w, "registry is required", http.StatusBadRequest)
		return
	}
	if strings.HasPrefix(req.Registry, "-") {
		// hauler would read it as a flag, not the registry positional.
		http.Error(w, "registry must not start with '-'", http.StatusBadRequest)
		return
	}

	if req.Username == "" {
		http.Error(w, "username is required", http.StatusBadRequest)
		return
	}

	if req.Password == "" {
		http.Error(w, "password is required", http.StatusBadRequest)
		return
	}

	// The password goes to hauler on stdin only, never in argv (visible in
	// /proc/<pid>/cmdline), env or the job row. hauler 1.3.2 reads no
	// username/password env vars.
	args := []string{"login", req.Registry, "-u", req.Username, "--password-stdin"}
	job, err := h.jobRunner.CreateStdinJob(r.Context(), "hauler", args, []byte(req.Password))
	if err != nil {
		log.Printf("Error creating login job: %v", err)
		http.Error(w, "Failed to create login job", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jobId":    job.ID,
		"message":  loginSavedMessage,
		"registry": req.Registry,
		"username": req.Username,
	})
}

// Logout handles POST /api/registry/logout
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req LogoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	if req.Registry == "" {
		http.Error(w, "registry is required", http.StatusBadRequest)
		return
	}
	if strings.HasPrefix(req.Registry, "-") {
		// hauler would read it as a flag, not the registry positional.
		http.Error(w, "registry must not start with '-'", http.StatusBadRequest)
		return
	}

	// Build args for hauler logout command
	args := []string{"logout", req.Registry}

	// Create a job for the logout operation
	job, err := h.jobRunner.CreateJob(r.Context(), "hauler", args, nil)
	if err != nil {
		log.Printf("Error creating logout job: %v", err)
		http.Error(w, "Failed to create logout job", http.StatusInternalServerError)
		return
	}

	// Start the job in background
	go func() {
		if err := h.jobRunner.Start(r.Context(), job.ID); err != nil {
			log.Printf("Error starting logout job %d: %v", job.ID, err)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jobId":    job.ID,
		"message":  "Logout job started",
		"registry": req.Registry,
	})
}

// Info handles GET /api/registry/info
// Returns information about configured registries
func (h *Handler) Info(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get the config handler to return registry info
	info := map[string]interface{}{
		"dockerAuthPath":     h.cfg.DockerAuthPath,
		"homeDir":            h.cfg.HaulerDir,
		"credentialsMessage": "Credentials are stored by hauler in the Docker config.json file",
	}

	// Parse the docker auth path to show user-friendly location
	displayPath := h.cfg.DockerAuthPath
	if strings.Contains(displayPath, "/data/.docker/config.json") {
		displayPath = "~/.docker/config.json (mapped to /data/.docker/config.json in container)"
	}

	info["displayPath"] = displayPath

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}

// RegisterRoutes registers the registry routes with the given mux
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/registry/login", h.Login)
	mux.HandleFunc("/api/registry/logout", h.Logout)
	mux.HandleFunc("/api/registry/info", h.Info)
}
