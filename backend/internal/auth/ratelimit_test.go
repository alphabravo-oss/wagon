package auth

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestRateLimitKey(t *testing.T) {
	for _, tt := range []struct {
		ip, want string
	}{
		{"203.0.113.7", "203.0.113.7"},
		{"::ffff:192.0.2.1", "192.0.2.1"},
		{"2001:db8:1:2::1", "2001:db8:1:2::/64"},
		{"2001:db8:1:2:abcd:1234:5678:ffff", "2001:db8:1:2::/64"},
		{"2001:db8:1:3::1", "2001:db8:1:3::/64"},
		{"fe80::1%eth0", "fe80::/64"},
		{"odd-remote-address", "odd-remote-address"},
	} {
		t.Run(tt.ip, func(t *testing.T) {
			if got := rateLimitKey(tt.ip); got != tt.want {
				t.Fatalf("rateLimitKey(%q) = %q, want %q", tt.ip, got, tt.want)
			}
		})
	}
}

func TestLimiterMiddlewareSharesBuckets(t *testing.T) {
	t.Setenv("HAULER_UI_TRUST_PROXY", "")
	t.Setenv("HAULER_UI_LOGIN_RATE", "3")
	for _, tt := range []struct {
		name, first, shared, independent string
	}{
		{"IPv6 /64", "2001:db8:1:2::1", "2001:db8:1:2::ffff", "2001:db8:1:3::1"},
		{"mapped IPv4", "::ffff:192.0.2.1", "192.0.2.1", "192.0.2.2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := newLoginLimiter()
			l.refillRate = 0
			handler := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			check := func(ip string, want int) {
				t.Helper()
				r := httptest.NewRequest(http.MethodPost, "/login", nil)
				r.RemoteAddr = net.JoinHostPort(ip, "5555")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("client %s: status = %d, want %d", ip, w.Code, want)
				}
			}
			for i := 0; i < 3; i++ {
				check(tt.first, http.StatusNoContent)
			}
			check(tt.shared, http.StatusTooManyRequests)
			if len(l.buckets) != 1 {
				t.Fatalf("shared bucket count = %d, want 1", len(l.buckets))
			}
			check(tt.independent, http.StatusNoContent)
		})
	}
}

func TestLimiterMiddlewareIPv6Flood(t *testing.T) {
	t.Setenv("HAULER_UI_TRUST_PROXY", "")
	t.Setenv("HAULER_UI_LOGIN_RATE", "5")
	l := newLoginLimiter()
	l.refillRate = 0
	handler := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for i := 0; i < 20000; i++ {
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		r.RemoteAddr = "[2001:db8:1:2::" + strconv.FormatInt(int64(i), 16) + "]:5555"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		want := http.StatusTooManyRequests
		if i < 5 {
			want = http.StatusNoContent
		}
		if w.Code != want {
			t.Fatalf("request %d: status = %d, want %d", i, w.Code, want)
		}
	}
	if len(l.buckets) != 1 || l.buckets["2001:db8:1:2::/64"] == nil {
		t.Fatalf("IPv6 flood should use exactly one /64 bucket, got %d buckets", len(l.buckets))
	}
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = "203.0.113.7:5555"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("fresh IPv4 client: status = %d, want %d", w.Code, http.StatusNoContent)
	}
	if len(l.buckets) != 2 || l.buckets["203.0.113.7"] == nil {
		t.Fatal("fresh IPv4 client should have its own bucket")
	}
	if l.overflow != nil {
		t.Fatal("IPv6 flood should not create an overflow bucket")
	}
}

func TestLimiterBoundsBucketsAndSharesOverflow(t *testing.T) {
	t.Setenv("HAULER_UI_LOGIN_RATE", "5")
	l := newLoginLimiter()
	l.refillRate = 0 // Make the overflow capacity deterministic.
	for i := 0; i < maxLoginBuckets+500; i++ {
		ok, _ := l.allow(strconv.Itoa(i))
		wantAllowed := i < maxLoginBuckets+5
		if ok != wantAllowed {
			t.Fatalf("key %d: allowed=%v, want %v", i, ok, wantAllowed)
		}
		if len(l.buckets) > maxLoginBuckets {
			t.Fatalf("bucket count exceeded cap: %d", len(l.buckets))
		}
	}
	if len(l.buckets) != maxLoginBuckets {
		t.Fatalf("bucket count = %d, want %d", len(l.buckets), maxLoginBuckets)
	}
	for i := 0; i < 4; i++ {
		if ok, _ := l.allow("0"); !ok {
			t.Fatal("overflow exhausted an existing key's bucket")
		}
	}
	if ok, _ := l.allow("0"); ok {
		t.Fatal("existing key should be limited after five requests")
	}

	// A new key at the cap triggers eviction even before a minute passes.
	l.buckets["1"].lastSeen = time.Now().Add(-l.ttl - time.Second)
	l.lastEvict = time.Now()
	if ok, _ := l.allow("new-key"); !ok {
		t.Fatal("new key should use space freed by eviction")
	}
	if l.buckets["1"] != nil || l.buckets["new-key"] == nil || len(l.buckets) != maxLoginBuckets {
		t.Fatal("eviction at the cap should replace the stale bucket")
	}
}

func TestLimiterRefund(t *testing.T) {
	l := &loginLimiter{
		buckets:  map[string]*tokenBucket{"known": {tokens: 4.5}},
		capacity: 5,
	}
	l.refund("unknown")
	if len(l.buckets) != 1 || l.overflow != nil {
		t.Fatal("refund of an unknown key should not create a bucket")
	}
	l.overflow = &tokenBucket{tokens: 2}
	l.refund("known")
	if l.buckets["known"].tokens != 5 || l.overflow.tokens != 2 {
		t.Fatal("refund should cap the existing bucket at capacity and leave overflow unchanged")
	}
	l.refund("unknown")
	if l.overflow.tokens != 3 || len(l.buckets) != 1 {
		t.Fatal("refund of an absent key should restore one overflow token without creating a bucket")
	}
	l.overflow.tokens = 5
	l.refund("unknown")
	if l.overflow.tokens != 5 {
		t.Fatal("overflow refund should be capped at capacity")
	}
}

func TestLimiterDefaultCapacityAndRefill(t *testing.T) {
	t.Setenv("HAULER_UI_LOGIN_RATE", "")
	l := newLoginLimiter()
	for i := 0; i < 5; i++ {
		if ok, _ := l.allow("ip-a"); !ok {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
	}
	if ok, wait := l.allow("ip-a"); ok || wait <= 0 {
		t.Fatalf("sixth attempt should be limited: ok=%v, wait=%v", ok, wait)
	}
	if ok, _ := l.allow("ip-b"); !ok {
		t.Fatal("distinct keys should have independent buckets")
	}
	l.buckets["ip-a"].lastSeen = time.Now().Add(-13 * time.Second)
	if ok, _ := l.allow("ip-a"); !ok {
		t.Fatal("bucket should refill one token after 13 seconds")
	}
	if ok, _ := l.allow("ip-a"); ok {
		t.Fatal("refill should not restore the entire burst")
	}
}

func TestLimiterEvictionInterval(t *testing.T) {
	t.Setenv("HAULER_UI_LOGIN_RATE", "5")
	l := newLoginLimiter()
	l.allow("stale")
	l.buckets["stale"].lastSeen = time.Now().Add(-l.ttl - time.Second)
	lastEvict := time.Now()
	l.lastEvict = lastEvict
	l.allow("active")
	if l.buckets["stale"] == nil || !l.lastEvict.Equal(lastEvict) {
		t.Fatal("eviction should not run again within a minute below the cap")
	}
	l.lastEvict = time.Now().Add(-time.Minute - time.Second)
	l.allow("active")
	if l.buckets["stale"] != nil {
		t.Fatal("eviction should remove buckets idle longer than ttl")
	}
	if l.buckets["active"] == nil || !l.lastEvict.After(lastEvict) {
		t.Fatal("eviction should retain active buckets and update lastEvict")
	}
}

// TestClientIP_IgnoresXFFByDefault is the security-critical case: with no
// trusted proxy configured, a spoofed X-Forwarded-For must NOT change the key,
// otherwise an attacker rotates the header to get a fresh bucket per request.
func TestClientIP_IgnoresXFFByDefault(t *testing.T) {
	t.Setenv("HAULER_UI_TRUST_PROXY", "")
	r := &http.Request{Header: http.Header{}, RemoteAddr: "203.0.113.7:5555"}
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := clientIP(r); got != "203.0.113.7" {
		t.Fatalf("spoofed XFF changed the key: got %q, want the RemoteAddr host 203.0.113.7", got)
	}
}

// TestClientIP_TrustProxyUsesRightmostHop verifies that when a proxy is trusted
// we take the rightmost (proxy-appended, unspoofable) hop, not the leftmost.
func TestClientIP_TrustProxyUsesRightmostHop(t *testing.T) {
	t.Setenv("HAULER_UI_TRUST_PROXY", "true")
	r := &http.Request{Header: http.Header{}, RemoteAddr: "10.0.0.1:5555"}
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 198.51.100.9") // 1.2.3.4 is client-supplied/spoofable
	if got := clientIP(r); got != "198.51.100.9" {
		t.Fatalf("want rightmost trusted hop 198.51.100.9, got %q", got)
	}
}

// TestLimiterBlocksAfterCapacity confirms the bucket blocks once its burst is
// spent and that distinct keys get independent buckets.
func TestLimiterBlocksAfterCapacity(t *testing.T) {
	l := &loginLimiter{
		buckets:    make(map[string]*tokenBucket),
		refillRate: 0, // no refill during the test so capacity is a hard ceiling
		capacity:   3,
		ttl:        time.Hour,
	}
	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("ip-a"); !ok {
			t.Fatalf("attempt %d for ip-a should be allowed within capacity", i+1)
		}
	}
	if ok, wait := l.allow("ip-a"); ok || wait <= 0 {
		t.Fatalf("ip-a should be blocked after capacity spent (ok=%v, wait=%v)", ok, wait)
	}
	// A different IP must not be affected by ip-a exhausting its bucket.
	if ok, _ := l.allow("ip-b"); !ok {
		t.Fatal("ip-b should have its own fresh bucket")
	}
}

func TestIPv6Prefix48Key(t *testing.T) {
	for _, tt := range []struct {
		ip, want string
		ok       bool
	}{
		{"203.0.113.7", "", false},
		{"::ffff:192.0.2.1", "", false},
		{"2001:db8:42:1::1", "2001:db8:42::/48", true},
		{"2001:db8:42:ffff::1", "2001:db8:42::/48", true},
		{"2001:db8:43:1::1", "2001:db8:43::/48", true},
		{"fe80::1%eth0", "fe80::/48", true},
		{"odd-remote-address", "", false},
	} {
		t.Run(tt.ip, func(t *testing.T) {
			if got, ok := ipv6Prefix48Key(tt.ip); got != tt.want || ok != tt.ok {
				t.Fatalf("ipv6Prefix48Key(%q) = (%q, %v), want (%q, %v)", tt.ip, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestLimiterMiddlewareIPv6Prefix48Flood(t *testing.T) {
	t.Setenv("HAULER_UI_TRUST_PROXY", "")
	t.Setenv("HAULER_UI_LOGIN_RATE", "5")
	l := newLoginLimiter()
	if l.prefix48.capacity != 50 || l.prefix48.refillRate != l.refillRate*10 || l.prefix48.ttl != l.ttl {
		t.Fatal("/48 limiter should have ten times the capacity and refill rate, and the same TTL")
	}
	l.refillRate = 0
	l.prefix48.refillRate = 0
	handler := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := func(ip string) int {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		r.RemoteAddr = net.JoinHostPort(ip, "5555")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code == http.StatusTooManyRequests && w.Header().Get("Retry-After") == "" {
			t.Fatal("429 without Retry-After")
		}
		return w.Code
	}
	allowed := 0
	for i := 0; i < 20000; i++ {
		status := request("2001:db8:42:" + strconv.FormatInt(int64(i), 16) + "::1")
		switch status {
		case http.StatusNoContent:
			allowed++
		case http.StatusTooManyRequests:
		default:
			t.Fatalf("request %d: unexpected status %d", i, status)
		}
	}
	if allowed != 50 || len(l.buckets) != 50 || l.overflow != nil || len(l.prefix48.buckets) != 1 {
		t.Fatalf("flood: allowed=%d, /64 buckets=%d, overflow=%v, /48 buckets=%d", allowed, len(l.buckets), l.overflow, len(l.prefix48.buckets))
	}
	for _, ip := range []string{"203.0.113.9", "2001:db8:43:1::1"} {
		if status := request(ip); status != http.StatusNoContent {
			t.Fatalf("fresh client %s: status = %d", ip, status)
		}
		if l.buckets[rateLimitKey(ip)] == nil {
			t.Fatalf("fresh client %s should get its own bucket", ip)
		}
	}
}

func TestLimiterMiddlewarePrefix48NotDrainedByDeniedRequests(t *testing.T) {
	t.Setenv("HAULER_UI_TRUST_PROXY", "")
	t.Setenv("HAULER_UI_LOGIN_RATE", "5")
	l := newLoginLimiter()
	l.refillRate = 0
	l.prefix48.refillRate = 0
	handler := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := func(ip string) int {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		r.RemoteAddr = net.JoinHostPort(ip, "5555")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code == http.StatusTooManyRequests && w.Header().Get("Retry-After") == "" {
			t.Fatal("429 without Retry-After")
		}
		return w.Code
	}
	allowed, denied := 0, 0
	for i := 0; i < 60; i++ {
		switch status := request("2001:db8:42:1::1"); status {
		case http.StatusNoContent:
			allowed++
		case http.StatusTooManyRequests:
			denied++
		default:
			t.Fatalf("request %d: unexpected status %d", i, status)
		}
	}
	if allowed != 5 || denied != 55 {
		t.Fatalf("allowed=%d, denied=%d, want 5 and 55", allowed, denied)
	}
	b := l.prefix48.buckets["2001:db8:42::/48"]
	if b == nil || b.tokens != 45 {
		t.Fatalf("/48 bucket = %+v, want 45 tokens", b)
	}
	if status := request("2001:db8:42:2::1"); status != http.StatusNoContent {
		t.Fatalf("neighbour: status = %d, want %d", status, http.StatusNoContent)
	}
	if b.tokens != 44 {
		t.Fatalf("/48 tokens = %v, want 44", b.tokens)
	}
}

func TestLimiterMiddlewarePrefix48PreservesPerKeyLimits(t *testing.T) {
	t.Setenv("HAULER_UI_TRUST_PROXY", "")
	t.Setenv("HAULER_UI_LOGIN_RATE", "5")
	for _, tt := range []struct {
		name, ip, other string
		prefixBuckets   int
	}{
		{"IPv6", "2001:db8:42:1::1", "2001:db8:42:2::1", 1},
		{"IPv4", "203.0.113.9", "203.0.113.10", 0},
		{"mapped IPv4", "::ffff:203.0.113.9", "::ffff:203.0.113.10", 0},
		{"nil prefix limiter", "2001:db8:42:1::1", "2001:db8:42:2::1", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := newLoginLimiter()
			l.refillRate = 0
			l.prefix48.refillRate = 0
			if tt.name == "nil prefix limiter" {
				l.prefix48 = nil
			}
			handler := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			for i := 0; i < 7; i++ {
				ip, want := tt.ip, http.StatusNoContent
				switch i {
				case 5:
					want = http.StatusTooManyRequests
				case 6:
					ip = tt.other
				}
				r := httptest.NewRequest(http.MethodPost, "/login", nil)
				r.RemoteAddr = net.JoinHostPort(ip, "5555")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("request %d: status = %d, want %d", i, w.Code, want)
				}
			}
			if l.prefix48 != nil && len(l.prefix48.buckets) != tt.prefixBuckets {
				t.Fatalf("/48 bucket count = %d, want %d", len(l.prefix48.buckets), tt.prefixBuckets)
			}
		})
	}
}
