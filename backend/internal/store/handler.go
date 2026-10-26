package store

import (
	"log"
	"net/http"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
	"github.com/alphabravo-oss/wagon/backend/internal/hauls"
	"github.com/alphabravo-oss/wagon/backend/internal/jobrunner"
	"github.com/alphabravo-oss/wagon/backend/internal/safefetch"
)

// Handler handles HTTP requests for store operations
type Handler struct {
	JobRunner *jobrunner.Runner
	Cfg       *config.Config
	Hauls     *hauls.Service
	// Fetcher downloads Add File urls; hauler only ever gets the local copy.
	Fetcher *safefetch.Fetcher
}

// NewHandler creates a new store handler
func NewHandler(jobRunner *jobrunner.Runner, cfg *config.Config, haulSvc *hauls.Service) *Handler {
	allow, err := safefetch.ParseCIDRs(cfg.FetchAllowCIDRs)
	if err != nil {
		// config.Validate refuses to start with a bad list; this is a fallback.
		log.Printf("Warning: ignoring HAULER_UI_FETCH_ALLOW_CIDRS: %v", err)
		allow = nil
	}
	return &Handler{
		JobRunner: jobRunner,
		Cfg:       cfg,
		Hauls:     haulSvc,
		Fetcher: safefetch.New(safefetch.Options{
			Allow:    allow,
			MaxBytes: cfg.FetchMaxBytes,
			Timeout:  cfg.FetchTimeout,
		}),
	}
}

// RegisterRoutes registers the store routes with the given mux. Operations are
// scoped to a haul via a "haulId" field in the request body (or "?haul=" query
// for reads); when omitted they fall back to the default haul.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/store/info", h.GetInfo)
	mux.HandleFunc("/api/store/add-image", h.AddImage)
	mux.HandleFunc("/api/store/add-chart", h.AddChart)
	mux.HandleFunc("/api/store/add-file", h.AddFile)
	mux.HandleFunc("/api/store/sync", h.Sync)
	mux.HandleFunc("/api/store/save", h.Save)
	mux.HandleFunc("/api/store/load", h.Load)
	mux.HandleFunc("/api/store/extract", h.Extract)
	mux.HandleFunc("/api/store/copy", h.Copy)
	mux.HandleFunc("/api/store/remove", h.Remove)
	mux.HandleFunc("/api/store/rescan", h.Rescan)
	mux.HandleFunc("/api/store/import", h.Import)
}
