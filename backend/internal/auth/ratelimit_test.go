package auth

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

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
// spent and that distinct IPs get independent buckets.
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
