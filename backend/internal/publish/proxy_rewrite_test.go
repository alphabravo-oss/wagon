package publish

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"reflect"
	"testing"
)

// legacyDirector is the Director the registry proxy used before the move to
// Rewrite. It is kept here only to prove the forwarding behavior is unchanged.
func legacyDirector(r *http.Request) {
	if target, ok := r.Context().Value(targetKey).(string); ok {
		r.URL.Scheme = "http"
		r.URL.Host = target
	}
}

type seenRequest struct {
	Host       string
	RequestURI string
	Header     http.Header
}

func TestRegistryProxyRewriteMatchesLegacyDirector(t *testing.T) {
	var seen []seenRequest
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, seenRequest{Host: r.Host, RequestURI: r.RequestURI, Header: r.Header.Clone()})
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	target := backend.Listener.Addr().String()

	m := &Manager{}
	//lint:ignore SA1019 deliberate comparison against the deprecated Director behavior
	legacy := &httputil.ReverseProxy{Director: legacyDirector} //nolint:staticcheck // SA1019: deliberate comparison against the deprecated Director behavior
	current := &httputil.ReverseProxy{Rewrite: m.rewrite}

	cases := []struct {
		name   string
		method string
		target string
		remote string
		header http.Header
	}{
		{name: "plain manifest pull", method: http.MethodGet, target: "http://haul-a.example.com/v2/library/nginx/manifests/latest"},
		{name: "host with port and blob", method: http.MethodHead, target: "https://haul-a.example.com:5443/v2/ns/img/blobs/sha256:abc"},
		{name: "escaped path and query", method: http.MethodGet, target: "http://h/v2/a%2Fb/tags/list?n=10&last=x%20y"},
		{name: "semicolon query", method: http.MethodGet, target: "http://h/v2/_catalog?n=5;x=1&a=b"},
		{name: "client X-Forwarded-For single", method: http.MethodGet, target: "http://h/v2/", header: http.Header{"X-Forwarded-For": {"10.0.0.1"}}},
		{name: "client X-Forwarded-For multiple", method: http.MethodGet, target: "http://h/v2/", header: http.Header{"X-Forwarded-For": {"10.0.0.1", "10.0.0.2, 10.0.0.3"}}},
		{name: "client forwarding headers", method: http.MethodGet, target: "http://h/v2/", header: http.Header{
			"Forwarded":         {"for=10.0.0.9;proto=https"},
			"X-Forwarded-Host":  {"outer.example.com"},
			"X-Forwarded-Proto": {"https"},
		}},
		{name: "forwarding headers marked hop-by-hop", method: http.MethodGet, target: "http://h/v2/", header: http.Header{
			"Connection":        {"X-Forwarded-Proto, x-forwarded-for"},
			"X-Forwarded-For":   {"10.0.0.1"},
			"X-Forwarded-Proto": {"https"},
			"X-Forwarded-Host":  {"kept.example.com"},
		}},
		{name: "auth, range and user agent", method: http.MethodGet, target: "http://h/v2/x/blobs/sha256:1", header: http.Header{
			"Authorization": {"Basic dXNlcjpwYXNz"},
			"Range":         {"bytes=0-99"},
			"User-Agent":    {"containerd/2.0"},
			"Te":            {"trailers"},
		}},
		{name: "remote addr without port", method: http.MethodGet, target: "http://h/v2/", remote: "192.0.2.10", header: http.Header{"X-Forwarded-For": {"10.0.0.1"}}},
		{name: "push upload", method: http.MethodPatch, target: "http://h/v2/ns/img/blobs/uploads/uuid?_state=abc%3D%3D"},
	}

	run := func(p *httputil.ReverseProxy, method, rawURL, remote string, h http.Header) seenRequest {
		t.Helper()
		seen = nil
		req := httptest.NewRequest(method, rawURL, nil)
		if remote != "" {
			req.RemoteAddr = remote
		}
		for k, v := range h {
			req.Header[k] = append([]string(nil), v...)
		}
		req = req.WithContext(context.WithValue(req.Context(), targetKey, target))
		rr := httptest.NewRecorder()
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || len(seen) != 1 {
			t.Fatalf("%s %s: status %d, backend saw %d requests", method, rawURL, rr.Code, len(seen))
		}
		return seen[0]
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := run(legacy, tc.method, tc.target, tc.remote, tc.header)
			got := run(current, tc.method, tc.target, tc.remote, tc.header)
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("forwarded request differs\nDirector: %+v\nRewrite:  %+v", want, got)
			}
			u, _ := url.Parse(tc.target)
			if got.Host != u.Host {
				t.Fatalf("Host = %q, want inbound %q", got.Host, u.Host)
			}
		})
	}
}
