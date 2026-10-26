// Package safefetch downloads a user-supplied http(s) URL to a local file
// without letting the URL reach Wagon's own host or internal network.
//
// Every connection, including each redirect hop, is checked against the
// resolved IP address: the address is checked before dialing and again in the
// net.Dialer Control hook on the socket actually being connected, so a DNS
// answer cannot change between the check and the dial.
package safefetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// AllowPrivateByDefault decides whether private addresses (RFC 1918, CGNAT
// 100.64.0.0/10 and IPv6 ULA fc00::/7) are reachable without listing them in
// HAULER_UI_FETCH_ALLOW_CIDRS. m j owns this choice (panel question); change
// it only here.
const AllowPrivateByDefault = false

const (
	// DefaultMaxBytes caps a download when HAULER_UI_FETCH_MAX_BYTES is unset.
	DefaultMaxBytes int64 = 2 << 30
	// DefaultTimeout bounds a whole download, redirects included, when
	// HAULER_UI_FETCH_TIMEOUT is unset.
	DefaultTimeout = 30 * time.Minute

	maxRedirects = 10
	dialTimeout  = 30 * time.Second
)

// alwaysBlocked can never be allowed: Wagon's own host, cloud metadata and
// addresses that are not a single remote host.
var alwaysBlocked = prefixes(
	"0.0.0.0/8",      // "this network"; Linux connects 0.x to the local host
	"127.0.0.0/8",    // loopback
	"169.254.0.0/16", // link-local, including 169.254.169.254 metadata
	"224.0.0.0/4",    // multicast
	"240.0.0.0/4",    // reserved, including 255.255.255.255 broadcast
	"::/128",         // unspecified
	"::1/128",        // loopback
	"fe80::/10",      // link-local
	"ff00::/8",       // multicast
)

// private is blocked unless AllowPrivateByDefault is set or a CIDR in the
// allowlist covers the address.
var private = prefixes(
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10", // CGNAT, also Tailscale
	"fc00::/7",      // ULA
)

// nat64 embeds an IPv4 address in its last 32 bits; that address is checked too.
var nat64 = netip.MustParsePrefix("64:ff9b::/96")

func prefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

// ParseCIDRs parses a list of CIDRs for Options.Allow.
func ParseCIDRs(cidrs []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", c, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// ErrBlocked is returned (wrapped) when a connection targets a blocked address.
var ErrBlocked = errors.New("address is not allowed")

// Options configures a Fetcher. Zero values take the defaults.
type Options struct {
	// Allow lists private CIDRs that may be reached. It cannot unblock
	// loopback, link-local, unspecified, multicast or reserved addresses.
	Allow    []netip.Prefix
	MaxBytes int64
	Timeout  time.Duration

	// Lookup resolves a host name; nil uses the system resolver. Tests set it.
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	// Dial connects to an ip:port that has already passed the policy; nil uses
	// a net.Dialer whose Control hook checks the address again. Tests set it to
	// reach an httptest server under a fake public address.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// Fetcher downloads http(s) URLs under the address policy.
type Fetcher struct {
	allow    []netip.Prefix
	maxBytes int64
	timeout  time.Duration

	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	dial   func(ctx context.Context, network, address string) (net.Conn, error)
}

// New returns a Fetcher. By default it resolves with the system resolver and
// dials through a net.Dialer whose Control hook enforces the policy.
func New(opts Options) *Fetcher {
	f := &Fetcher{
		allow:    opts.Allow,
		maxBytes: opts.MaxBytes,
		timeout:  opts.Timeout,
	}
	if f.maxBytes <= 0 {
		f.maxBytes = DefaultMaxBytes
	}
	if f.timeout <= 0 {
		f.timeout = DefaultTimeout
	}
	f.lookup = opts.Lookup
	if f.lookup == nil {
		f.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	f.dial = opts.Dial
	if f.dial == nil {
		d := &net.Dialer{Timeout: dialTimeout, Control: f.control}
		f.dial = d.DialContext
	}
	return f
}

// check reports whether ip may be connected to.
func (f *Fetcher) check(ip netip.Addr) error {
	ip = ip.WithZone("").Unmap()
	if nat64.Contains(ip) {
		b := ip.As16()
		if err := f.check(netip.AddrFrom4([4]byte(b[12:]))); err != nil {
			return err
		}
	}
	for _, p := range alwaysBlocked {
		if p.Contains(ip) {
			return fmt.Errorf("%w: %s is a loopback, link-local, multicast or reserved address", ErrBlocked, ip)
		}
	}
	for _, p := range private {
		if !p.Contains(ip) {
			continue
		}
		if AllowPrivateByDefault {
			return nil
		}
		for _, a := range f.allow {
			if a.Contains(ip) {
				return nil
			}
		}
		return fmt.Errorf("%w: %s is a private address; list its range in HAULER_UI_FETCH_ALLOW_CIDRS to allow it", ErrBlocked, ip)
	}
	return nil
}

// CheckHost resolves host and checks every address it resolves to. It is for
// hosts Wagon hands to hauler rather than connecting to itself; hauler
// resolves the name again when the job runs, so a DNS answer can still change
// in between.
func (f *Fetcher) CheckHost(ctx context.Context, host string) error {
	if ip, err := netip.ParseAddr(host); err == nil {
		return f.check(ip)
	}
	ips, err := f.lookup(ctx, host)
	if err != nil {
		// The resolver's error names Wagon's DNS server; callers show this
		// message to the user, so leave it out.
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return fmt.Errorf("cannot resolve %s: no such host", host)
		}
		return fmt.Errorf("cannot resolve %s", host)
	}
	if len(ips) == 0 {
		return fmt.Errorf("cannot resolve %s: no addresses", host)
	}
	for _, ip := range ips {
		if err := f.check(ip); err != nil {
			return err
		}
	}
	return nil
}

// control is the net.Dialer Control hook. address is the resolved ip:port
// of the socket about to connect.
func (f *Fetcher) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: cannot parse %q", ErrBlocked, address)
	}
	return f.check(ap.Addr())
}

// dialContext resolves the host, drops blocked addresses and dials the first
// allowed one that answers. It is used for every connection the client opens.
func (f *Fetcher) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else if ips, err = f.lookup(ctx, host); err != nil {
		return nil, fmt.Errorf("cannot resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("cannot resolve %s: no addresses", host)
	}
	var firstErr error
	for _, ip := range ips {
		if err := f.check(ip); err != nil {
			firstErr = cmpErr(firstErr, err)
			continue
		}
		conn, err := f.dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		firstErr = cmpErr(firstErr, err)
	}
	return nil, firstErr
}

func cmpErr(first, next error) error {
	if first != nil {
		return first
	}
	return next
}

func (f *Fetcher) client() *http.Client {
	tr := &http.Transport{
		// No proxy: a proxy would connect to the target for us and bypass the
		// address check.
		Proxy:                 nil,
		DialContext:           f.dialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: 5 * time.Minute,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   f.timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to a %q URL is not allowed", req.URL.Scheme)
			}
			return nil
		},
	}
}

// Download fetches rawURL into dst, which it truncates first. It returns the
// number of bytes written. On error dst may hold a partial body; the caller
// removes it.
func (f *Fetcher) Download(ctx context.Context, rawURL, dst string) (int64, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return 0, errors.New("url must be an http:// or https:// URL with a host")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, err
	}
	client := f.client()
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, fmt.Errorf("server returned %s", resp.Status)
	}
	if resp.ContentLength > f.maxBytes {
		return 0, fmt.Errorf("file is %d bytes, over the %d byte limit (HAULER_UI_FETCH_MAX_BYTES)", resp.ContentLength, f.maxBytes)
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(resp.Body, f.maxBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, err
	}
	if n > f.maxBytes {
		return n, fmt.Errorf("file is over the %d byte limit (HAULER_UI_FETCH_MAX_BYTES)", f.maxBytes)
	}
	return n, nil
}
