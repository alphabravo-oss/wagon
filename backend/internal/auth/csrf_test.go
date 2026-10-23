package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := RequestGuard(ok, []string{"https://wagon.proxy.example"})

	const host = "wagon.local:8080"
	json := "application/json"
	cases := []struct {
		name, method, path, body, ctype string
		hdr                             map[string]string
		want                            int
	}{
		{"GET cross-site is allowed (safe)", "GET", "/api/jobs", "", "", map[string]string{"Origin": "http://evil.example"}, 204},
		{"POST foreign Origin", "POST", "/api/store/add-image", `{}`, json, map[string]string{"Origin": "http://evil.example"}, 403},
		{"POST foreign Origin text/plain", "POST", "/api/store/add-image", `{}`, "text/plain", map[string]string{"Origin": "http://evil.example"}, 403},
		{"POST Sec-Fetch-Site cross-site", "POST", "/api/publish/1", `{}`, json, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "http://evil.example"}, 403},
		{"POST Sec-Fetch-Site same-site", "POST", "/api/publish/1", `{}`, json, map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "http://a." + host}, 403},
		{"DELETE foreign Origin", "DELETE", "/api/jobs", "", "", map[string]string{"Origin": "http://evil.example"}, 403},
		{"POST null Origin", "POST", "/api/auth/login", `{}`, json, map[string]string{"Origin": "null"}, 403},
		{"POST same Origin json", "POST", "/api/store/add-image", `{}`, json, map[string]string{"Origin": "http://" + host}, 204},
		{"POST Sec-Fetch-Site same-origin", "POST", "/api/publish/1", `{}`, json, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://" + host}, 204},
		{"POST trusted proxy origin", "POST", "/api/publish/1", `{}`, json, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://wagon.proxy.example"}, 204},
		{"POST no Origin (non-browser) json", "POST", "/api/auth/login", `{}`, json, nil, 204},
		{"POST same origin text/plain -> 415", "POST", "/api/store/add-image", `{}`, "text/plain", map[string]string{"Origin": "http://" + host}, 415},
		{"POST json with charset", "POST", "/api/store/add-image", `{}`, "application/json; charset=utf-8", nil, 204},
		{"POST no body no ctype", "POST", "/api/auth/logout", "", "", map[string]string{"Origin": "http://" + host}, 204},
		{"POST form to JSON route -> 415", "POST", "/api/store/add-image", "a=b", "application/x-www-form-urlencoded", nil, 415},
		{"import multipart without header", "POST", "/api/store/import", "--x--", "multipart/form-data; boundary=x", map[string]string{"Origin": "http://" + host}, 403},
		{"import multipart with header", "POST", "/api/store/import", "--x--", "multipart/form-data; boundary=x", map[string]string{"Origin": "http://" + host, CSRFHeader: "XMLHttpRequest"}, 204},
		{"multipart to other route -> 415", "POST", "/api/store/add-image", "--x--", "multipart/form-data; boundary=x", map[string]string{CSRFHeader: "XMLHttpRequest"}, 415},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Host = host
			if tc.ctype != "" {
				r.Header.Set("Content-Type", tc.ctype)
			}
			for k, v := range tc.hdr {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Errorf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}
