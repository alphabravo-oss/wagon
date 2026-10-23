package publish

import (
	"crypto/subtle"
	"net/http"
)

// requireAuth optionally guards published endpoints with HTTP Basic auth.
//
// When no credentials are configured (both user and password empty) the handler
// is returned unchanged so airgap deployments stay open by default. When
// credentials are set, requests must present a matching Basic auth header;
// otherwise a 401 with a WWW-Authenticate challenge is returned.
func (m *Manager) requireAuth(next http.Handler) http.Handler {
	user := m.cfg.PublishAuthUser
	pass := m.cfg.PublishAuthPassword

	// Open by default: no credentials configured.
	if user == "" && pass == "" {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, ok := r.BasicAuth()
		// Compute both comparisons before combining so we don't short-circuit
		// and leak timing information about which field matched.
		userMatch := subtle.ConstantTimeCompare([]byte(gotUser), []byte(user)) == 1
		passMatch := subtle.ConstantTimeCompare([]byte(gotPass), []byte(pass)) == 1
		if !ok || !userMatch || !passMatch {
			w.Header().Set("WWW-Authenticate", `Basic realm="hauler"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
