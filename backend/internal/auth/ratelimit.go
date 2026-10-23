package auth

import (
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxLoginBuckets = 10000
const ipv6Prefix48Multiplier = 10

// loginLimiter is a small rate limiter built on the standard library only.
// Keys are per IPv4 address or per IPv6 /64: each bucket refills at a steady
// rate and starts with a small burst so a legitimate user who fat-fingers their
// password a couple of times is not blocked, while a brute-force loop is.
// IPv6 also has a coarser /48 limit to bound bucket creation across /64s.
//
// It is intended to guard the login endpoint only. Do NOT wrap registry or
// content endpoints with it — that would throttle legitimate pulls.
type loginLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*tokenBucket
	overflow  *tokenBucket
	lastEvict time.Time
	prefix48  *loginLimiter

	// refillRate is tokens added per second.
	refillRate float64
	// capacity is the maximum tokens a bucket can hold (the burst size).
	capacity float64
	// ttl is how long an idle bucket is kept before it is eligible for eviction.
	ttl time.Duration
}

type tokenBucket struct {
	tokens   float64
	lastSeen time.Time
}

// loginRatePerMinute reads HAULER_UI_LOGIN_RATE (attempts per minute per key).
// Keys are per IPv4 address or per IPv6 /64.
// Defaults to 5, and clamps to at least 1 so the endpoint never fully locks out.
func loginRatePerMinute() int {
	if v := os.Getenv("HAULER_UI_LOGIN_RATE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 5
}

// newLoginLimiter constructs a limiter allowing ~ratePerMinute attempts per
// minute per IPv4 address or IPv6 /64, with a burst equal to the per-minute rate.
// IPv6 /48s share a separate limit at ten times that rate and burst.
func newLoginLimiter() *loginLimiter {
	rpm := loginRatePerMinute()
	return &loginLimiter{
		buckets:    make(map[string]*tokenBucket),
		refillRate: float64(rpm) / 60.0,
		capacity:   float64(rpm),
		ttl:        10 * time.Minute,
		prefix48: &loginLimiter{
			buckets:    make(map[string]*tokenBucket),
			refillRate: float64(rpm) / 60.0 * ipv6Prefix48Multiplier,
			capacity:   float64(rpm) * ipv6Prefix48Multiplier,
			ttl:        10 * time.Minute,
		},
	}
}

// allow reports whether a request for the given key may proceed, and how long the
// caller should wait before retrying. It refills the bucket based on elapsed
// time, consumes one token on success, and opportunistically evicts stale
// entries so the map cannot grow unbounded.
func (l *loginLimiter) allow(key string) (bool, time.Duration) {
	ok, retryAfter, _ := l.charge(key)
	return ok, retryAfter
}

// peek reports whether the key's existing bucket has a token after refill,
// without changing limiter state. It skips charge()'s expired-entry clearing,
// so for unknown keys (including when the map is full) it defers to charge().
func (l *loginLimiter) peek(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.buckets[key]
	if b == nil {
		return true, 0
	}
	tokens := l.refilledTokens(b, time.Now())
	return tokens >= 1, l.tokenWait(tokens)
}

func (l *loginLimiter) refilledTokens(b *tokenBucket, now time.Time) float64 {
	if b == nil {
		return l.capacity
	}
	tokens := b.tokens + now.Sub(b.lastSeen).Seconds()*l.refillRate
	if tokens > l.capacity {
		tokens = l.capacity
	}
	return tokens
}

func (l *loginLimiter) tokenWait(tokens float64) time.Duration {
	if tokens >= 1 {
		return 0
	}
	needed := 1 - tokens
	wait := time.Duration(needed / l.refillRate * float64(time.Second))
	if wait < time.Second {
		wait = time.Second
	}
	return wait
}

// charge is allow that also returns the bucket it charged (its own bucket, a
// new one, or the shared overflow bucket), so a refund can credit that exact
// bucket instead of looking the key up again.
func (l *loginLimiter) charge(key string) (bool, time.Duration, *tokenBucket) {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastEvict) >= time.Minute || (l.buckets[key] == nil && len(l.buckets) >= maxLoginBuckets) {
		l.evictStaleLocked(now)
	}

	b := l.buckets[key]
	if b == nil && len(l.buckets) >= maxLoginBuckets {
		// Keep the shared overflow bucket outside the capped per-key map.
		if l.overflow == nil {
			l.overflow = &tokenBucket{tokens: l.capacity, lastSeen: now}
		}
		b = l.overflow
	}
	if b == nil {
		b = &tokenBucket{tokens: l.capacity, lastSeen: now}
		l.buckets[key] = b
	} else {
		b.tokens = l.refilledTokens(b, now)
	}
	b.lastSeen = now

	if b.tokens >= 1 {
		b.tokens -= 1
		return true, 0, b
	}

	return false, l.tokenWait(b.tokens), b
}

// refund credits exactly the bucket charge() charged. If that bucket was
// evicted meanwhile, the credit is harmlessly lost. Middleware
// uses it so a request denied by the /64 limit does not spend the /48 budget.
func (l *loginLimiter) refund(b *tokenBucket) {
	if b == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	b.tokens++
	if b.tokens > l.capacity {
		b.tokens = l.capacity
	}
}

// evictStaleLocked removes buckets idle longer than ttl. Caller must hold l.mu.
func (l *loginLimiter) evictStaleLocked(now time.Time) {
	l.lastEvict = now
	for ip, b := range l.buckets {
		if now.Sub(b.lastSeen) > l.ttl {
			delete(l.buckets, ip)
		}
	}
}

// trustProxy reports whether X-Forwarded-For should be trusted. Off by default
// because the header is client-supplied and trivially spoofable when the app is
// directly exposed.
func trustProxy() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HAULER_UI_TRUST_PROXY"))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// clientIP determines the client address for a request.
//
// By default it uses RemoteAddr (the real TCP peer), which a client cannot
// spoof. Only when HAULER_UI_TRUST_PROXY is set do we honor X-Forwarded-For, and
// then we take the RIGHTMOST hop: a trusted proxy appends the address it
// observed, so the last entry is the one the client cannot forge (the leftmost
// entries are attacker-controlled). ponytail: assumes a single trusted proxy; a
// multi-proxy chain would need a configured trusted-hop count.
func clientIP(r *http.Request) string {
	if trustProxy() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			last := strings.TrimSpace(parts[len(parts)-1])
			if last != "" {
				return last
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimitKey keys clients by IPv4 address or IPv6 /64.
func rateLimitKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.WithZone("").Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, _ := addr.Prefix(64)
	return prefix.String()
}

// ipv6Prefix48Key groups native IPv6 clients by /48, excluding mapped IPv4.
func ipv6Prefix48Key(ip string) (string, bool) {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return "", false
	}
	addr = addr.WithZone("").Unmap()
	if !addr.Is6() {
		return "", false
	}
	prefix, _ := addr.Prefix(48)
	return prefix.String(), true
}

// Middleware limits logins per IPv4 address or IPv6 /64 and /48, returning 429
// with Retry-After on denial. Already-empty per-key buckets leave /48 untouched.
func (l *loginLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		key := rateLimitKey(ip)
		// Advisory only: charge and refund below remain authoritative.
		ok, retryAfter := l.peek(key)
		if !ok {
			tooManyLoginAttempts(w, retryAfter)
			return
		}
		key48, isIPv6 := ipv6Prefix48Key(ip)
		checkPrefix48 := isIPv6 && l.prefix48 != nil
		var b48 *tokenBucket
		if checkPrefix48 {
			ok, retryAfter, b48 = l.prefix48.charge(key48)
		}
		if ok {
			ok, retryAfter = l.allow(key)
			if !ok && checkPrefix48 {
				// Denied /64 requests must not drain the shared /48 bucket.
				l.prefix48.refund(b48)
			}
		}
		if !ok {
			tooManyLoginAttempts(w, retryAfter)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tooManyLoginAttempts writes a 429 with a Retry-After of at least one second.
func tooManyLoginAttempts(w http.ResponseWriter, retryAfter time.Duration) {
	secs := int(retryAfter.Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	http.Error(w, "Too many login attempts", http.StatusTooManyRequests)
}
