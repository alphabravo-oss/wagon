package safefetch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// publicIP stands in for an internet host; tests route it to an httptest server.
const publicIP = "93.184.216.34"

// fakeNet resolves names from hosts and connects the IPs in routes to httptest
// servers. Its dialer runs the real Control hook on the address it was asked
// for, so a blocked address still fails there.
type fakeNet struct {
	hosts  map[string]string
	routes map[string]*httptest.Server
}

func (n *fakeNet) fetcher(t *testing.T, opts Options) *Fetcher {
	t.Helper()
	var f *Fetcher
	opts.Lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		ip, ok := n.hosts[host]
		if !ok {
			return nil, fmt.Errorf("no such host %q", host)
		}
		return []netip.Addr{netip.MustParseAddr(ip)}, nil
	}
	opts.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if err := f.control(network, address, nil); err != nil {
			return nil, err
		}
		host, _, _ := net.SplitHostPort(address)
		srv, ok := n.routes[host]
		if !ok {
			return nil, fmt.Errorf("no route to %s in test", address)
		}
		var d net.Dialer
		return d.DialContext(ctx, network, srv.Listener.Addr().String())
	}
	f = New(opts)
	return f
}

func serve(t *testing.T, body string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func download(t *testing.T, f *Fetcher, rawURL string) (string, error) {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "dl")
	_, err := f.Download(context.Background(), rawURL, dst)
	b, _ := os.ReadFile(dst)
	return string(b), err
}

func TestDownload_PublicURL(t *testing.T) {
	remote := serve(t, "REMOTE-BODY", nil)
	n := &fakeNet{hosts: map[string]string{"files.example": publicIP}, routes: map[string]*httptest.Server{publicIP: remote}}
	got, err := download(t, n.fetcher(t, Options{}), "http://files.example/dist/f.txt")
	if err != nil || got != "REMOTE-BODY" {
		t.Fatalf("got %q, %v; want the remote body", got, err)
	}
}

// hauler opened filepath.Join(host, path) on disk for these URLs. Wagon must
// fetch them over HTTP or fail, and never read a local file.
func TestDownload_LocalLookingURLsNeverReadLocalFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".docker"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"app.db", ".docker/config.json", "x/etc/host_secret.txt"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte("LOCAL-SECRET"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	var paths []string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RawPath+"|"+r.URL.Path)
		_, _ = w.Write([]byte("REMOTE-BODY"))
	}))
	t.Cleanup(remote.Close)
	n := &fakeNet{
		hosts:  map[string]string{"x": publicIP, "example.com": publicIP},
		routes: map[string]*httptest.Server{publicIP: remote},
	}
	f := n.fetcher(t, Options{})

	// A resolvable host: the body comes from the server, not the disk.
	for _, u := range []string{
		"http://x/../../etc/host_secret.txt",
		"http://x/etc/host_secret.txt",
		"https://example.com/../../../../../../etc/passwd",
		"http://example.com/..%2f..%2fetc%2fpasswd",
	} {
		u = strings.Replace(u, "https://", "http://", 1) // the fake server speaks plain HTTP
		got, err := download(t, f, u)
		if err != nil || got != "REMOTE-BODY" {
			t.Errorf("%s: got %q, %v; want the remote body", u, got, err)
		}
	}
	if len(paths) != 4 {
		t.Errorf("server saw %d requests (%q), want 4", len(paths), paths)
	}

	// Hosts that only exist as local files: the download fails, nothing is read.
	for _, u := range []string{"http://.docker", "http://.docker/config.json", "http://app.db", "https://app.db/"} {
		got, err := download(t, f, u)
		if err == nil || !strings.Contains(err.Error(), "cannot resolve") {
			t.Errorf("%s: err %v, want a resolve failure", u, err)
		}
		if got != "" {
			t.Errorf("%s: wrote %q, want nothing", u, got)
		}
	}
}

func TestDownload_LoopbackBlocked(t *testing.T) {
	var hits atomic.Int32
	internal := serve(t, "INTERNAL", &hits)
	f := New(Options{}) // real resolver and dialer
	for _, u := range []string{
		internal.URL + "/secret",
		strings.Replace(internal.URL, "127.0.0.1", "[::ffff:127.0.0.1]", 1),
		strings.Replace(internal.URL, "127.0.0.1", "0.0.0.0", 1),
		strings.Replace(internal.URL, "127.0.0.1", "localhost", 1),
	} {
		got, err := download(t, f, u)
		if !errors.Is(err, ErrBlocked) || got != "" {
			t.Errorf("%s: got %q, %v; want ErrBlocked", u, got, err)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("internal server got %d requests, want 0", hits.Load())
	}
}

// The Control hook alone refuses a blocked socket, without the pre-dial check.
func TestControlHookBlocksLoopback(t *testing.T) {
	var hits atomic.Int32
	internal := serve(t, "INTERNAL", &hits)
	f := New(Options{})
	conn, err := f.dial(context.Background(), "tcp", internal.Listener.Addr().String())
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("dial loopback: %v, want ErrBlocked", err)
	}
}

func TestDownload_RedirectToInternalBlocked(t *testing.T) {
	var hits atomic.Int32
	internal := serve(t, "INTERNAL", &hits)
	for _, target := range []string{
		internal.URL + "/secret",
		"http://169.254.169.254/latest/meta-data/",
		"http://metadata.internal/computeMetadata/v1/",
		"http://10.1.2.3/",
		"http://[fd00::1]/",
	} {
		redirector := httptest.NewServer(http.RedirectHandler(target, http.StatusFound))
		t.Cleanup(redirector.Close)
		n := &fakeNet{
			hosts:  map[string]string{"files.example": publicIP, "metadata.internal": "169.254.169.254"},
			routes: map[string]*httptest.Server{publicIP: redirector, "127.0.0.1": internal, "169.254.169.254": internal},
		}
		got, err := download(t, n.fetcher(t, Options{}), "http://files.example/f.txt")
		if !errors.Is(err, ErrBlocked) || got != "" {
			t.Errorf("redirect to %s: got %q, %v; want ErrBlocked", target, got, err)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("internal server got %d requests, want 0", hits.Load())
	}
}

func TestDownload_AllowlistedPrivateCIDR(t *testing.T) {
	nas := serve(t, "NAS-BODY", nil)
	n := &fakeNet{hosts: map[string]string{"nas.lan": "10.20.30.40"}, routes: map[string]*httptest.Server{"10.20.30.40": nas}}

	if _, err := download(t, n.fetcher(t, Options{}), "http://nas.lan/f.txt"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("without allowlist: %v, want ErrBlocked", err)
	}

	allow, err := ParseCIDRs([]string{"10.20.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := download(t, n.fetcher(t, Options{Allow: allow}), "http://nas.lan/f.txt")
	if err != nil || got != "NAS-BODY" {
		t.Fatalf("with allowlist: got %q, %v", got, err)
	}
}

func TestDownload_SizeCap(t *testing.T) {
	withLength := serve(t, "0123456789ABCDEF", nil)
	chunked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("01234567"))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("89ABCDEF"))
	}))
	t.Cleanup(chunked.Close)
	for name, srv := range map[string]*httptest.Server{"content-length": withLength, "chunked": chunked} {
		n := &fakeNet{hosts: map[string]string{"files.example": publicIP}, routes: map[string]*httptest.Server{publicIP: srv}}
		f := n.fetcher(t, Options{MaxBytes: 10})
		if _, err := download(t, f, "http://files.example/f"); err == nil || !strings.Contains(err.Error(), "limit") {
			t.Errorf("%s: err %v, want the size limit", name, err)
		}
		f = n.fetcher(t, Options{MaxBytes: 16})
		if got, err := download(t, f, "http://files.example/f"); err != nil || got != "0123456789ABCDEF" {
			t.Errorf("%s at the limit: got %q, %v", name, got, err)
		}
	}
}

// A symlink at dst is refused, not followed (review 88, finding 5).
func TestDownload_SymlinkDstNotFollowed(t *testing.T) {
	remote := serve(t, "REMOTE-BODY", nil)
	n := &fakeNet{hosts: map[string]string{"files.example": publicIP}, routes: map[string]*httptest.Server{publicIP: remote}}
	dir := t.TempDir()
	target, dst := filepath.Join(dir, "target"), filepath.Join(dir, "dl")
	if err := os.WriteFile(target, []byte("KEEP"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := n.fetcher(t, Options{}).Download(context.Background(), "http://files.example/f", dst); err == nil {
		t.Error("download through a symlink succeeded, want an error")
	}
	if b, _ := os.ReadFile(target); string(b) != "KEEP" {
		t.Errorf("symlink target now %q, want it untouched", b)
	}
}

func TestDownload_RejectsNonHTTPAndErrors(t *testing.T) {
	notFound := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(notFound.Close)
	n := &fakeNet{hosts: map[string]string{"files.example": publicIP}, routes: map[string]*httptest.Server{publicIP: notFound}}
	f := n.fetcher(t, Options{})
	if _, err := download(t, f, "http://files.example/missing"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404: err %v", err)
	}
	for _, u := range []string{"file:///etc/passwd", "/etc/passwd", "ftp://files.example/f", "http:///etc/passwd"} {
		if _, err := download(t, f, u); err == nil {
			t.Errorf("%s: no error", u)
		}
	}
}

func TestCheck(t *testing.T) {
	allowAll, _ := ParseCIDRs([]string{"0.0.0.0/0", "::/0"})
	blockedAlways := []string{
		"127.0.0.1", "127.8.9.10", "::1", "::ffff:127.0.0.1", "0.0.0.0", "0.1.2.3", "::",
		"169.254.169.254", "169.254.0.1", "fe80::1", "fe80::1%eth0",
		"224.0.0.1", "239.255.255.250", "ff02::1", "255.255.255.255", "240.0.0.1",
		"64:ff9b::a9fe:a9fe", // NAT64 form of 169.254.169.254
	}
	blockedPrivate := []string{
		"10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1", "100.64.0.1", "100.65.189.113",
		"100.127.255.255", "fc00::1", "fd12:3456::1", "::ffff:10.0.0.1", "64:ff9b::a00:1",
	}
	public := []string{"93.184.216.34", "8.8.8.8", "172.32.0.1", "100.128.0.1", "2606:4700::1111", "192.0.2.1"}

	plain := New(Options{})
	open := New(Options{Allow: allowAll})
	for _, s := range blockedAlways {
		ip := netip.MustParseAddr(s)
		if plain.check(ip) == nil || open.check(ip) == nil {
			t.Errorf("%s: allowed, want always blocked", s)
		}
	}
	for _, s := range blockedPrivate {
		ip := netip.MustParseAddr(s)
		if plain.check(ip) == nil {
			t.Errorf("%s: allowed by default, want blocked", s)
		}
		if err := open.check(ip); err != nil {
			t.Errorf("%s: blocked with an allowlist: %v", s, err)
		}
	}
	for _, s := range public {
		if err := plain.check(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s: %v, want allowed", s, err)
		}
	}
}

func TestParseCIDRs(t *testing.T) {
	if _, err := ParseCIDRs([]string{"10.0.0.0/8", " fd00::/8 "}); err != nil {
		t.Errorf("valid list: %v", err)
	}
	for _, bad := range []string{"10.0.0.0", "nas.lan", "10.0.0.0/33"} {
		if _, err := ParseCIDRs([]string{bad}); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// CheckHost runs on the request path, so a resolver that never answers must
// not hold the request open.
func TestCheckHost_LookupHasDeadline(t *testing.T) {
	var deadline time.Time
	var ok bool
	f := New(Options{Lookup: func(ctx context.Context, _ string) ([]netip.Addr, error) {
		deadline, ok = ctx.Deadline()
		return []netip.Addr{netip.MustParseAddr(publicIP)}, nil
	}})
	start := time.Now()
	if err := f.CheckHost(context.Background(), "example.com"); err != nil {
		t.Fatalf("CheckHost: %v", err)
	}
	if !ok {
		t.Fatal("lookup context has no deadline")
	}
	if d := deadline.Sub(start); d <= 0 || d > 30*time.Second {
		t.Errorf("lookup deadline %v after the call, want at most 30s", d)
	}
}

// The addresses a name resolves to stay out of the error, which callers show
// to the user; otherwise the check answers DNS queries for internal names.
func TestCheckHost_BlockedNameHidesAddress(t *testing.T) {
	f := New(Options{Lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr(publicIP), netip.MustParseAddr("10.1.2.3")}, nil
	}})
	err := f.CheckHost(context.Background(), "internal.example")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("CheckHost = %v, want ErrBlocked", err)
	}
	if msg := err.Error(); strings.Contains(msg, "10.1.2.3") || !strings.Contains(msg, "internal.example") {
		t.Errorf("CheckHost error %q: want the host name and not the address", msg)
	}
}
