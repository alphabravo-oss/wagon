package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/alphabravo-oss/wagon/backend/internal/auth"
	"github.com/alphabravo-oss/wagon/backend/internal/config"
	"github.com/alphabravo-oss/wagon/backend/internal/hauler"
	"github.com/alphabravo-oss/wagon/backend/internal/hauls"
	"github.com/alphabravo-oss/wagon/backend/internal/jobrunner"
	"github.com/alphabravo-oss/wagon/backend/internal/manifests"
	"github.com/alphabravo-oss/wagon/backend/internal/obs"
	"github.com/alphabravo-oss/wagon/backend/internal/publish"
	"github.com/alphabravo-oss/wagon/backend/internal/registry"
	"github.com/alphabravo-oss/wagon/backend/internal/serve"
	"github.com/alphabravo-oss/wagon/backend/internal/settings"
	"github.com/alphabravo-oss/wagon/backend/internal/sqlite"
	"github.com/alphabravo-oss/wagon/backend/internal/store"
)

// maxConcurrentJobs caps how many jobs run at once so a burst of queued
// operations does not exhaust memory/disk on a single-instance deployment.
func maxConcurrentJobs() int {
	if v := os.Getenv("HAULER_UI_MAX_CONCURRENT_JOBS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 2
}

// startJobProcessor starts a background goroutine that processes queued jobs,
// honoring a concurrency limit. Each tick it also refreshes the job and
// published-haul gauges for the metrics endpoint.
func startJobProcessor(runner *jobrunner.Runner, pm *publish.Manager, stopCh <-chan struct{}) {
	ctx := context.Background()
	limit := maxConcurrentJobs()

	go func() {
		log.Printf("Job processor goroutine started (max concurrent: %d)", limit)
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-stopCh:
				log.Println("Job processor stopped")
				return
			case <-ticker.C:
				jobs, err := runner.ListJobs(ctx, nil)
				if err != nil {
					log.Printf("Error listing jobs: %v", err)
					continue
				}

				// Count current job states for the concurrency limit and metrics.
				running, queued := 0, 0
				for _, job := range jobs {
					switch job.Status {
					case jobrunner.StatusRunning:
						running++
					case jobrunner.StatusQueued:
						queued++
					}
				}
				obs.SetJobs(queued, running)
				obs.SetPublishedHauls(len(pm.List(ctx)))

				for _, job := range jobs {
					if running >= limit {
						break
					}
					if job.Status == jobrunner.StatusQueued {
						log.Printf("Starting queued job #%d: %s %v", job.ID, job.Command, jobrunner.RedactArgs(job.Args))
						if err := runner.Start(ctx, job.ID); err != nil {
							log.Printf("Error starting job #%d: %v", job.ID, err)
						} else {
							running++
						}
					}
				}
			}
		}
	}()
	log.Println("Job processor started")
}

// cleanupOnBoot resets state left over from a previous run: jobs stuck in
// "running" (the process died mid-job) are marked failed, and stale serve
// process rows (dead PIDs) are marked stopped so the UI reflects reality.
func cleanupOnBoot(db *sql.DB) {
	if res, err := db.Exec(`UPDATE jobs SET status = 'failed', completed_at = CURRENT_TIMESTAMP WHERE status = 'running'`); err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("Boot cleanup: marked %d interrupted job(s) as failed", n)
		}
	}
	if res, err := db.Exec(`UPDATE serve_processes SET status = 'stopped', stopped_at = CURRENT_TIMESTAMP WHERE status = 'running'`); err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("Boot cleanup: marked %d stale serve process(es) as stopped", n)
		}
	}
}

func main() {
	// Configure structured logging first so every subsequent log line (including
	// those from the stdlib log package) is emitted as structured records.
	obs.Setup()

	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}
	if cfg.InsecureNoAuth && cfg.UIPassword == "" {
		slog.Warn("INSECURE: HAULER_UI_INSECURE_NO_AUTH=true and no HAULER_UI_PASSWORD: authentication is DISABLED. " +
			"Anyone who can reach this port has full control of Wagon. Do not expose it beyond localhost.")
	}

	// Initialize SQLite database
	db, err := sqlite.Open(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("Failed to close database: %v", err)
		}
	}()
	log.Printf("Database initialized: %s", cfg.DatabasePath)

	// Reset state left over from a previous run before anything starts.
	cleanupOnBoot(db.DB)

	// Initialize job runner
	jobRunner := jobrunner.New(db.DB, cfg.DataDir)
	jobHandler := jobrunner.NewHandler(jobRunner, cfg)

	// The background job processor is started later, once the publish manager
	// exists, so it can also report published-haul metrics.
	stopCh := make(chan struct{})
	defer close(stopCh)

	// Initialize hauler detector
	haulerBinary := getEnv("HAULER_BINARY", "hauler")
	haulerDetector := hauler.New(haulerBinary)
	haulerHandler := hauler.NewHandler(haulerDetector)

	// Initialize registry handler
	registryHandler := registry.NewHandler(jobRunner, cfg)

	// Initialize haul service and ensure a default haul exists on first boot
	haulService := hauls.NewService(db.DB, cfg)
	if _, err := haulService.EnsureDefault(context.Background()); err != nil {
		log.Printf("Warning: failed to ensure default haul: %v", err)
	}
	haulsHandler := hauls.NewHandler(haulService)

	// Initialize store handler
	storeHandler := store.NewHandler(jobRunner, cfg, haulService)

	// Initialize manifests handler
	manifestsHandler := manifests.NewHandler(db.DB, haulService)

	// Initialize serve handler
	serveHandler := serve.NewHandler(cfg, db.DB, haulService)

	// Initialize publish manager (host-routed registries + path-routed files)
	publishManager := publish.NewManager(cfg, db.DB, haulService)
	publishHandler := publish.NewHandler(publishManager, haulService)
	publishManager.RestoreOnBoot(context.Background())

	// Now that the publish manager exists, start the background job processor.
	startJobProcessor(jobRunner, publishManager, stopCh)

	// Initialize settings handler
	settingsHandler := settings.NewHandler(db.DB)

	// Initialize auth manager and handler
	authManager := auth.NewManager(db.DB, cfg)
	authHandler := auth.NewHandler(authManager)

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", healthzHandler)
	mux.HandleFunc("/readyz", readyzHandler(db.DB, haulerBinary))
	mux.Handle("/metrics", obs.MetricsHandler())
	mux.HandleFunc("/api/config", configHandler(cfg))

	// Auth endpoints (public)
	authHandler.RegisterRoutes(mux)

	// Hauler capabilities endpoints
	haulerHandler.RegisterRoutes(mux)

	// Registry endpoints
	registryHandler.RegisterRoutes(mux)

	// Haul endpoints
	haulsHandler.RegisterRoutes(mux)

	// Store endpoints
	storeHandler.RegisterRoutes(mux)

	// Manifests endpoints
	manifestsHandler.RegisterRoutes(mux)

	// Serve endpoints
	serveHandler.RegisterRoutes(mux)

	// Publish endpoints (routes table, publish/unpublish) and /h/ file serving
	publishHandler.RegisterRoutes(mux)

	// Settings endpoints
	settingsHandler.RegisterRoutes(mux)

	// Job API endpoints
	registerJobRoutes(mux, jobHandler)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, "./web/index.html")
	})

	// Serve static files from web build directory
	fs := http.FileServer(http.Dir("./web"))
	mux.Handle("/assets/", fs)
	mux.HandleFunc("/wagon-logo.svg", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./web/wagon-logo.svg")
	})
	mux.HandleFunc("/favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./web/favicon.svg")
	})

	// Wrap mux with the cross-origin/content-type guard and auth middleware,
	// then instrument the whole chain so metrics and access logs capture every
	// request (including rejections).
	handler := obs.Instrument(secureHandler(mux, authManager, cfg))

	port := getEnv("PORT", "8080")
	server := &http.Server{
		Addr:        ":" + port,
		Handler:     handler,
		ReadTimeout: 5 * time.Second,
	}

	// Start the single host-routed registry proxy listener for published hauls.
	// Runs unauthenticated (registry clients use their own auth), on its own port.
	registryAddr := ":" + getEnv("HAULER_UI_REGISTRY_PORT", "5000")
	go publishManager.StartRegistryListener(registryAddr)

	// Run the UI/API server until a termination signal arrives.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("Server starting on :%s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutdown signal received; stopping...")

	// Stop spawned hauler children so they are not orphaned, then drain HTTP.
	publishManager.StopAll()
	serveHandler.StopAll()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}
	log.Println("Shutdown complete")
}

// registerJobRoutes registers the job API. Jobs are created only by the typed
// endpoints (store, registry, ...); there is deliberately no generic
// "run this command" route.
func registerJobRoutes(mux *http.ServeMux, jobHandler *jobrunner.Handler) {
	mux.HandleFunc("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			jobHandler.ListJobs(w, r)
		case http.MethodDelete:
			jobHandler.DeleteAllJobs(w, r)
		default:
			w.Header().Set("Allow", "GET, DELETE")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/jobs/", func(w http.ResponseWriter, r *http.Request) {
		// Check if this is a logs, stream, or cleanup request
		if len(r.URL.Path) > len("/api/jobs/") {
			suffix := r.URL.Path[len("/api/jobs/"):]
			if len(suffix) > 0 {
				// Look for /logs, /stream, or /cleanup suffix
				for i, c := range suffix {
					if c == '/' {
						sub := suffix[i:]
						if sub == "/logs" {
							jobHandler.GetJobLogs(w, r)
							return
						}
						if sub == "/stream" {
							jobHandler.StreamJobLogs(w, r)
							return
						}
						if sub == "/cleanup" && r.Method == http.MethodPost {
							jobHandler.CleanupStaleJob(w, r)
							return
						}
					}
				}
				// No special suffix, treat as get job
				jobHandler.GetJob(w, r)
				return
			}
		}
		http.NotFound(w, r)
	})
}

// secureHandler applies the cross-origin/content-type guard, then auth.
func secureHandler(mux http.Handler, am *auth.Manager, cfg *config.Config) http.Handler {
	return auth.RequestGuard(am.Middleware(mux), cfg.TrustedOrigins)
}

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
	})
}

// readyzHandler is a readiness probe (distinct from the /healthz liveness probe).
// It returns 200 only when the app can actually serve requests: the database is
// reachable and the hauler binary resolves on PATH. On failure it returns 503 so
// orchestrators stop routing traffic until the dependencies recover.
func readyzHandler(db *sql.DB, haulerBinary string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dbOK := db != nil && db.Ping() == nil

		_, lookErr := exec.LookPath(haulerBinary)
		haulerOK := lookErr == nil

		w.Header().Set("Content-Type", "application/json")
		if dbOK && haulerOK {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status": "ready",
			})
			return
		}

		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "not ready",
			"db":     dbOK,
			"hauler": haulerOK,
		})
	}
}

func configHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(cfg.ToMap())
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
