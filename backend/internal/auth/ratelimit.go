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

// loginLimiter is a small rate limiter built on the standard library only.
// Keys are per IPv4 address or per IPv6 /64: each bucket refills at a steady
// rate and starts with a small burst so a legitimate user who fat-fingers their
// password a couple of times is not blocked, while a brute-force loop is.
//
// It is intended to guard the login endpoint only. Do NOT wrap registry or
// content endpoints with it — that would throttle legitimate pulls.
type loginLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*tokenBucket
	overflow  *tokenBucket
	lastEvict time.Time

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
func newLoginLimiter() *loginLimiter {
	rpm := loginRatePerMinute()
	return &loginLimiter{
		buckets:    make(map[string]*tokenBucket),
		refillRate: float64(rpm) / 60.0,
		capacity:   float64(rpm),
		ttl:        10 * time.Minute,
	}
}

// allow reports whether a request for the given key may proceed, and how long the
// caller should wait before retrying. It refills the bucket based on elapsed
// time, consumes one token on success, and opportunistically evicts stale
// entries so the map cannot grow unbounded.
func (l *loginLimiter) allow(ip string) (bool, time.Duration) {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastEvict) >= time.Minute || (l.buckets[ip] == nil && len(l.buckets) >= maxLoginBuckets) {
		l.evictStaleLocked(now)
	}

	b := l.buckets[ip]
	if b == nil && len(l.buckets) >= maxLoginBuckets {
		// Keep the shared overflow bucket outside the capped per-key map.
		if l.overflow == nil {
			l.overflow = &tokenBucket{tokens: l.capacity, lastSeen: now}
		}
		b = l.overflow
	}
	if b == nil {
		b = &tokenBucket{tokens: l.capacity, lastSeen: now}
		l.buckets[ip] = b
	} else {
		elapsed := now.Sub(b.lastSeen).Seconds()
		b.tokens += elapsed * l.refillRate
		if b.tokens > l.capacity {
			b.tokens = l.capacity
		}
	}
	b.lastSeen = now

	if b.tokens >= 1 {
		b.tokens -= 1
		return true, 0
	}

	// Not enough tokens: compute how long until one token is available.
	needed := 1 - b.tokens
	wait := time.Duration(needed / l.refillRate * float64(time.Second))
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
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

// Middleware limits logins per IPv4 address or IPv6 /64. When the limit is
// exceeded it responds 429 with a Retry-After header instead of calling next.
func (l *loginLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, retryAfter := l.allow(rateLimitKey(clientIP(r)))
		if !ok {
			secs := int(retryAfter.Seconds())
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			http.Error(w, "Too many login attempts", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
