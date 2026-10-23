package auth

import (
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// importPath is the only route that accepts a multipart (form) upload. It must
// carry the custom CSRFHeader, which a cross-site form cannot set.
const importPath = "/api/store/import"

// CSRFHeader is the custom request header required on multipart uploads.
const CSRFHeader = "X-Requested-With"

// RequestGuard rejects cross-site state-changing requests. It applies to every
// non-safe method (anything but GET, HEAD, OPTIONS):
//
//   - Sec-Fetch-Site, when present, must be "same-origin" or "none".
//   - Otherwise Origin, when present, must match the request Host or one of
//     trustedOrigins. Requests with neither header are not from a browser and
//     carry no ambient credentials, so they pass this check (auth still applies).
//   - A request with a body must be application/json, except the multipart
//     import route, which must also send the CSRFHeader. Other bodies get 415.
func RequestGuard(next http.Handler, trustedOrigins []string) http.Handler {
	trusted := make(map[string]bool, len(trustedOrigins))
	for _, o := range trustedOrigins {
		trusted[strings.ToLower(strings.TrimRight(o, "/"))] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		if !sameOrigin(r, trusted) {
			http.Error(w, "Forbidden: cross-origin request", http.StatusForbidden)
			return
		}

		if hasBody(r) {
			mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			switch {
			case r.URL.Path == importPath && mt == "multipart/form-data":
				if r.Header.Get(CSRFHeader) == "" {
					http.Error(w, "Forbidden: missing "+CSRFHeader+" header", http.StatusForbidden)
					return
				}
			case mt == "application/json":
			default:
				http.Error(w, "Unsupported Media Type: use application/json", http.StatusUnsupportedMediaType)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request, trusted map[string]bool) bool {
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" {
		if sfs == "same-origin" || sfs == "none" {
			return true
		}
		// Browsers that send Sec-Fetch-Site also send Origin; honour an
		// explicitly trusted origin (reverse proxy) before rejecting.
		return trusted[strings.ToLower(r.Header.Get("Origin"))]
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if trusted[strings.ToLower(origin)] {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func hasBody(r *http.Request) bool {
	return r.ContentLength != 0 || len(r.TransferEncoding) > 0
}
