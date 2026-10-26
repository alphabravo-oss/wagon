package store

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/alphabravo-oss/wagon/backend/internal/safefetch"
)

// testDNSServer must never appear in a response.
const testDNSServer = "192.0.2.53:53"

// hostPolicyHandler returns a handler whose fetcher allows 10.20.0.0/16 and
// resolves a few fixed names; any other name is public.
func hostPolicyHandler(t *testing.T) (*Handler, *sql.DB) {
	t.Helper()
	h, db := setupTestHandler(t)
	names := map[string][]string{
		"localhost":        {"127.0.0.1", "::1"},
		"internal.example": {"10.1.2.3"},
		"mirror.example":   {"10.20.0.7"},
		"mixed.example":    {"93.184.216.34", "10.1.2.3"},
	}
	h.Fetcher = safefetch.New(safefetch.Options{
		Allow: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")},
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			switch host {
			case "unresolvable.example":
				return nil, &net.DNSError{Err: "no such host", Name: host, Server: testDNSServer, IsNotFound: true}
			case "dnsdown.example":
				return nil, &net.DNSError{Err: "dial udp " + testDNSServer + ": connect: network is unreachable", Name: host, Server: testDNSServer}
			}
			ips, ok := names[host]
			if !ok {
				ips = []string{"93.184.216.34"}
			}
			out := make([]netip.Addr, len(ips))
			for i, ip := range ips {
				out[i] = netip.MustParseAddr(ip)
			}
			return out, nil
		},
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("no network in tests")
		},
	})
	return h, db
}

// hauler, not Wagon, connects to image registries and chart repositories, so
// their hosts are checked against the Add File address policy up front.
func TestStoreHosts_BlockedRefused(t *testing.T) {
	const blocked = "address is not allowed"
	for _, tc := range []struct {
		endpoint string
		body     map[string]string
		wantMsg  string
	}{
		{"add-image", map[string]string{"imageRef": "127.0.0.1:5000/app:1"}, blocked},
		{"add-image", map[string]string{"imageRef": "localhost:5000/app"}, blocked},
		{"add-image", map[string]string{"imageRef": "169.254.169.254/latest/meta-data"}, blocked},
		{"add-image", map[string]string{"imageRef": "[::1]:5000/app"}, blocked},
		{"add-image", map[string]string{"imageRef": "[::ffff:127.0.0.1]:5000/app"}, blocked},
		{"add-image", map[string]string{"imageRef": "10.0.0.5:5000/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}, blocked},
		{"add-image", map[string]string{"imageRef": "internal.example/app:1"}, blocked},
		{"add-image", map[string]string{"imageRef": "mixed.example/app:1"}, blocked},
		{"add-image", map[string]string{"imageRef": "unresolvable.example/app"}, "cannot resolve unresolvable.example: no such host"},
		{"add-image", map[string]string{"imageRef": "dnsdown.example/app"}, "cannot resolve dnsdown.example"},
		{"add-image", map[string]string{"imageRef": "user@127.0.0.1/app"}, "not a valid registry"},
		{"add-chart", map[string]string{"name": "app", "repoUrl": "http://169.254.169.254/charts"}, blocked},
		{"add-chart", map[string]string{"name": "app", "repoUrl": "oci://10.0.0.5:5000/charts"}, blocked},
		{"add-chart", map[string]string{"name": "app", "repoUrl": "https://internal.example/charts"}, blocked},
		{"add-chart", map[string]string{"name": "app", "repoUrl": "oci://[::1]/charts"}, blocked},
		{"add-chart", map[string]string{"name": "app", "repoUrl": "https://user:pw@localhost/charts"}, blocked},
		{"add-chart", map[string]string{"name": "app", "repoUrl": "oci://public.example/%zz"}, "repoUrl: not a valid URL"},
		{"sync", map[string]string{"registry": "127.0.0.1:5000"}, blocked},
		{"sync", map[string]string{"registry": "internal.example"}, blocked},
		{"sync", map[string]string{"registry": "user@public.example"}, "not a valid registry"},
		{"sync", map[string]string{"products": "rancher=v2.10.1", "productRegistry": "169.254.169.254"}, blocked},
		{"sync", map[string]string{"products": "rancher=v2.10.1", "productRegistry": "localhost:5000/mirror"}, blocked},
	} {
		h, db := hostPolicyHandler(t)
		body := mustJSON(t, tc.body)
		w := postStore(h, tc.endpoint, body)
		expectRefused(t, w, db, h.Cfg.DataDir, tc.endpoint+" "+body, tc.wantMsg)
		if strings.Contains(w.Body.String(), "192.0.2.53") {
			t.Errorf("%s %s: 400 body names the DNS server: %q", tc.endpoint, body, w.Body.String())
		}
	}
}

const syncManifest = `apiVersion: content.hauler.cattle.io/v1
kind: Images
metadata:
  name: app
spec:
  images:
    - name: nginx:1.27
`

func TestStoreHosts_AllowedAccepted(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		body     map[string]string
	}{
		{"add-image", map[string]string{"imageRef": "nginx:1.27"}},
		{"add-image", map[string]string{"imageRef": "library/nginx@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		{"add-image", map[string]string{"imageRef": "ghcr.io/org/app:1"}},
		{"add-image", map[string]string{"imageRef": "mirror.example:5000/app:1"}},
		{"add-image", map[string]string{"imageRef": "10.20.0.5:5000/app:1"}},
		{"add-chart", map[string]string{"name": "app"}},
		{"add-chart", map[string]string{"name": "loki", "repoUrl": "grafana"}},
		{"add-chart", map[string]string{"name": "app", "repoUrl": "https://charts.example.com"}},
		{"add-chart", map[string]string{"name": "app", "repoUrl": "oci://10.20.0.5:5000/charts"}},
		{"sync", map[string]string{"manifestYaml": syncManifest}},
		{"sync", map[string]string{"manifestYaml": syncManifest, "registry": "mirror.example:5000"}},
		{"sync", map[string]string{"manifestYaml": syncManifest, "products": "rancher=v2.10.1", "productRegistry": "myorg"}},
	} {
		h, db := hostPolicyHandler(t)
		w := postStore(h, tc.endpoint, mustJSON(t, tc.body))
		if w.Code != http.StatusAccepted {
			t.Errorf("%s %v: status %d body %q, want 202", tc.endpoint, tc.body, w.Code, w.Body.String())
			continue
		}
		if n := jobCount(t, db); n != 1 {
			t.Errorf("%s %v: %d jobs, want 1", tc.endpoint, tc.body, n)
		}
	}
}
