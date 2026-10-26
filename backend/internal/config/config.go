package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alphabravo-oss/wagon/backend/internal/safefetch"
)

// Config holds the application configuration
type Config struct {
	// HaulerDir is the base directory for hauler data (default: /data)
	HaulerDir string

	// HaulerStoreDir is where hauler stores content (default: /data/store)
	HaulerStoreDir string

	// HaulerTempDir is for temporary files (default: /data/tmp)
	HaulerTempDir string

	// DockerAuthPath is where registry credentials are stored (default: /data/.docker/config.json)
	DockerAuthPath string

	// DatabasePath is the SQLite database file path (default: /data/app.db)
	DatabasePath string

	// DataDir is the base data directory (same as HaulerDir for downloads)
	DataDir string

	// UIPassword is the shared password for UI/API access. It is required:
	// Validate fails when it is empty unless InsecureNoAuth is set.
	UIPassword string

	// InsecureNoAuth is an explicit, off-by-default opt-out that allows running
	// with no UI password (HAULER_UI_INSECURE_NO_AUTH=true). Anyone who can reach
	// the UI port then has full control of Wagon. Not in ToMap.
	InsecureNoAuth bool

	// TrustedOrigins lists extra browser origins (scheme://host[:port]) allowed
	// to make state-changing requests, for reverse-proxy setups that rewrite the
	// Host header (HAULER_UI_TRUSTED_ORIGINS, comma-separated). Same-origin
	// requests are always allowed.
	TrustedOrigins []string

	// PublishAuthUser is the optional HTTP Basic auth username for published
	// registry/file endpoints (default: empty, no auth). Secret; not in ToMap.
	PublishAuthUser string

	// PublishAuthPassword is the optional HTTP Basic auth password for published
	// registry/file endpoints (default: empty, no auth). Secret; not in ToMap.
	PublishAuthPassword string

	// FetchAllowCIDRs lists private ranges (RFC 1918, CGNAT, ULA) an Add File
	// url may reach (HAULER_UI_FETCH_ALLOW_CIDRS, comma-separated). Loopback,
	// link-local and metadata addresses stay blocked whatever it says.
	FetchAllowCIDRs []string

	// FetchMaxBytes caps an Add File url download (HAULER_UI_FETCH_MAX_BYTES,
	// default 2 GiB; zero also means the default). Load sets -1 when the value
	// does not parse, and Validate rejects it.
	FetchMaxBytes int64

	// FetchTimeout bounds an Add File url download, redirects included
	// (HAULER_UI_FETCH_TIMEOUT, a Go duration such as 30m; default 30m, and
	// zero also means the default). Load sets -1 when the value does not parse.
	FetchTimeout time.Duration
}

// Load returns the application configuration from environment variables
// or defaults if not set.
func Load() *Config {
	haulerDir := getEnv("HAULER_DIR", "/data")
	homeDir := getEnv("HOME", haulerDir)
	dockerConfig := getEnv("DOCKER_CONFIG", filepath.Join(homeDir, ".docker"))

	return &Config{
		HaulerDir:      haulerDir,
		HaulerStoreDir: getEnv("HAULER_STORE_DIR", filepath.Join(haulerDir, "store")),
		HaulerTempDir:  getEnv("HAULER_TEMP_DIR", filepath.Join(haulerDir, "tmp")),
		DockerAuthPath: filepath.Join(dockerConfig, "config.json"),
		DatabasePath:   getEnv("DATABASE_PATH", filepath.Join(haulerDir, "app.db")),
		DataDir:        haulerDir,
		UIPassword:     getEnv("HAULER_UI_PASSWORD", ""),
		InsecureNoAuth: strings.EqualFold(strings.TrimSpace(os.Getenv("HAULER_UI_INSECURE_NO_AUTH")), "true"),
		TrustedOrigins: splitList(os.Getenv("HAULER_UI_TRUSTED_ORIGINS")),

		PublishAuthUser:     getEnv("HAULER_UI_PUBLISH_USER", ""),
		PublishAuthPassword: getEnv("HAULER_UI_PUBLISH_PASSWORD", ""),

		FetchAllowCIDRs: splitList(os.Getenv("HAULER_UI_FETCH_ALLOW_CIDRS")),
		FetchMaxBytes:   parsePositiveInt(getEnv("HAULER_UI_FETCH_MAX_BYTES", strconv.FormatInt(safefetch.DefaultMaxBytes, 10))),
		FetchTimeout:    parsePositiveDuration(getEnv("HAULER_UI_FETCH_TIMEOUT", safefetch.DefaultTimeout.String())),
	}
}

func parsePositiveInt(v string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n <= 0 {
		return -1
	}
	return n
}

func parsePositiveDuration(v string) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil || d <= 0 {
		return -1
	}
	return d
}

// ErrNoPassword is returned by Validate when no UI password is configured and
// the insecure opt-out is not set.
var ErrNoPassword = errors.New("HAULER_UI_PASSWORD is not set: refusing to start without authentication. " +
	"Set HAULER_UI_PASSWORD to a strong password (see deploy/.env.example). " +
	"To knowingly run with NO authentication (anyone who can reach the port gets full control), set HAULER_UI_INSECURE_NO_AUTH=true")

// Validate checks that the configuration is safe to serve. Authentication is
// mandatory: an empty UI password is an error unless InsecureNoAuth is set.
func (c *Config) Validate() error {
	if c.UIPassword == "" && !c.InsecureNoAuth {
		return ErrNoPassword
	}
	if _, err := safefetch.ParseCIDRs(c.FetchAllowCIDRs); err != nil {
		return fmt.Errorf("HAULER_UI_FETCH_ALLOW_CIDRS: %w", err)
	}
	if c.FetchMaxBytes < 0 {
		return errors.New("HAULER_UI_FETCH_MAX_BYTES must be a positive number of bytes")
	}
	if c.FetchTimeout < 0 {
		return errors.New("HAULER_UI_FETCH_TIMEOUT must be a positive Go duration, such as 30m")
	}
	return nil
}

// splitList splits a comma-separated value, trimming blanks.
func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// getEnv returns the environment variable value or the fallback if not set
func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// ToMap returns a map representation of the config for JSON serialization
func (c *Config) ToMap() map[string]string {
	return map[string]string{
		"haulerDir":       c.HaulerDir,
		"haulerStoreDir":  c.HaulerStoreDir,
		"haulerTempDir":   c.HaulerTempDir,
		"dockerAuthPath":  c.DockerAuthPath,
		"databasePath":    c.DatabasePath,
		"haulerDirEnv":    "HAULER_DIR",
		"haulerStoreEnv":  "HAULER_STORE_DIR",
		"haulerTempEnv":   "HAULER_TEMP_DIR",
		"dockerConfigEnv": "DOCKER_CONFIG",
		"databasePathEnv": "DATABASE_PATH",
		"authEnabled":     boolToString(c.UIPassword != ""),
	}
}

// boolToString converts a bool to "true" or "false" string
func boolToString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
