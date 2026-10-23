package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/alphabravo-oss/wagon/backend/internal/config"
)

var pbkdf2Iterations = 600000
var fatalf = log.Fatalf

const (
	sessionCookieName = "haulerci_session"
	sessionDuration   = 24 * time.Hour
	tokenLength       = 32
)

// Manager handles authentication operations
type Manager struct {
	db       *sql.DB
	password string
	enabled  bool
}

// NewManager creates a new auth manager
func NewManager(db *sql.DB, cfg *config.Config) *Manager {
	m := &Manager{
		db:       db,
		password: cfg.UIPassword,
		enabled:  cfg.UIPassword != "",
	}
	if m.enabled {
		if err := m.revokeSessionsOnPasswordChange(); err != nil {
			log.Printf("Error checking UI password change: %v", err)
			// The failed transaction has rolled back before this fallback runs.
			if _, err := m.db.Exec(`DELETE FROM sessions`); err != nil {
				fatalf("Refusing to start: unable to revoke UI sessions as a precaution: %v", err)
				return nil
			}
			log.Print("UI sessions revoked as a precaution after a password change check failed")
		}
	}
	return m
}

func (m *Manager) revokeSessionsOnPasswordChange() error {
	tx, err := m.db.Begin()
	if err != nil {
		return fmt.Errorf("beginning auth metadata transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // best effort; committed transactions are already closed

	var saltHex string
	err = tx.QueryRow(`SELECT v FROM auth_meta WHERE k = 'password_salt'`).Scan(&saltHex)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reading password salt: %w", err)
	}
	salt, err := hex.DecodeString(saltHex)
	if err != nil || len(salt) != 16 {
		salt = make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return fmt.Errorf("generating password salt: %w", err)
		}
		saltHex = hex.EncodeToString(salt)
		if _, err := tx.Exec(`INSERT INTO auth_meta (k, v) VALUES ('password_salt', ?)
			ON CONFLICT(k) DO UPDATE SET v = excluded.v`, saltHex); err != nil {
			return fmt.Errorf("storing password salt: %w", err)
		}
	}
	fingerprint, err := pbkdf2.Key(sha256.New, m.password, salt, pbkdf2Iterations, sha256.Size)
	if err != nil {
		return fmt.Errorf("deriving password fingerprint: %w", err)
	}
	fingerprintHex := hex.EncodeToString(fingerprint)
	var storedFingerprint string
	err = tx.QueryRow(`SELECT v FROM auth_meta WHERE k = 'password_fp'`).Scan(&storedFingerprint)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reading password fingerprint: %w", err)
	}
	if err == nil && storedFingerprint == fingerprintHex {
		return nil
	}
	if _, err := tx.Exec(`DELETE FROM sessions`); err != nil {
		return fmt.Errorf("deleting sessions after UI password change: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO auth_meta (k, v) VALUES ('password_fp', ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v`, fingerprintHex); err != nil {
		return fmt.Errorf("storing password fingerprint: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing auth metadata: %w", err)
	}
	log.Print("UI sessions revoked because the UI password changed or was first configured")
	return nil
}

// IsEnabled returns whether authentication is enabled
func (m *Manager) IsEnabled() bool {
	return m.enabled
}

// VerifyPassword checks if the provided password matches the configured password
func (m *Manager) VerifyPassword(password string) bool {
	if !m.enabled {
		return true // No auth configured, allow access
	}
	// Constant-time compare to avoid leaking timing information
	return subtle.ConstantTimeCompare([]byte(password), []byte(m.password)) == 1
}

// CreateSession creates a new session token and returns it
func (m *Manager) CreateSession() (string, time.Time, error) {
	token, err := generateToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generating token: %w", err)
	}

	expiresAt := time.Now().Add(sessionDuration)

	_, err = m.db.Exec(
		`INSERT INTO sessions (token, expires_at) VALUES (?, ?)`,
		token, expiresAt,
	)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("inserting session: %w", err)
	}

	// Clean up expired sessions
	go m.cleanupExpiredSessions()

	return token, expiresAt, nil
}

// ValidateSession checks if a session token is valid
func (m *Manager) ValidateSession(token string) bool {
	if !m.enabled {
		return true
	}

	var expiresAt time.Time
	err := m.db.QueryRow(
		`SELECT expires_at FROM sessions WHERE token = ? AND expires_at > CURRENT_TIMESTAMP`,
		token,
	).Scan(&expiresAt)

	return err == nil
}

// DeleteSession removes a session token
func (m *Manager) DeleteSession(token string) error {
	_, err := m.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// cleanupExpiredSessions removes expired sessions from the database
func (m *Manager) cleanupExpiredSessions() {
	_, err := m.db.Exec(`DELETE FROM sessions WHERE expires_at < CURRENT_TIMESTAMP`)
	if err != nil {
		log.Printf("Error cleaning up expired sessions: %v", err)
	}
}

// generateToken generates a secure random token
func generateToken() (string, error) {
	b := make([]byte, tokenLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// extractTokenFromRequest extracts the session token from the request cookie
func extractTokenFromRequest(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// Middleware returns an HTTP middleware that checks for valid sessions
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If auth is not enabled, pass through
		if !m.enabled {
			next.ServeHTTP(w, r)
			return
		}

		// Allow login endpoint without authentication
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Check for session token
		token := extractTokenFromRequest(r)
		if token == "" || !m.ValidateSession(token) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// isPublicPath returns true if the path should be accessible without authentication
func isPublicPath(path string) bool {
	publicPaths := []string{
		"/api/auth/login",
		"/healthz",
		"/readyz",
		"/metrics",
		"/api/config",
		"/h/", // published haul files are served to air-gap consumers without a UI session
	}

	for _, p := range publicPaths {
		if strings.HasPrefix(path, p) {
			return true
		}
	}

	// Also allow static assets and the login page itself
	if strings.HasPrefix(path, "/assets/") {
		return true
	}

	return false
}

// isSecureRequest reports whether the request arrived over TLS, either directly
// or via a proxy that set X-Forwarded-Proto.
func isSecureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// SetSessionCookie sets the session cookie on the response
func SetSessionCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Expires:  expiresAt,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearSessionCookie clears the session cookie from the response
func ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Expires:  time.Unix(0, 0),
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})
}
