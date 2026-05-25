// Package server — per-IP rate-limit middleware.
//
// Closes Finding F1 (SECURITY_VALIDATION_v0.3.md §3 / FINAL_SECURITY.md): no
// per-IP throttling at the API tier. Pre-fix, 100+ req/s were served against
// /admin/* and /me with no 429 / backpressure — a DoS surface.
//
// Implementation: a simple in-process token-bucket per-IP, no external
// dependencies. The bucket leaks at `rate` tokens/sec and bursts up to
// `burst`. Per-bucket state is kept under a sync.Mutex on a single map; a
// background sweep reaps stale buckets so the map doesn't grow unbounded
// behind a load balancer with a wide IP range.
//
// Scope: mount per-route-group, not globally — production deployments will
// likely front the API with an LB-level limiter for non-auth surfaces, but
// the admin tier should always have a self-defending floor.
package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// rateLimitDefaultRate is the steady-state allowed request rate per IP
// (requests/second) when the caller does not configure one. Generous enough
// for any human admin's click-rate, tight enough to stop a script.
const rateLimitDefaultRate = 10.0

// rateLimitDefaultBurst is the maximum burst size for a single IP. Lets a
// page with several concurrent fetches (e.g. /admin/users + /admin/roles +
// /auth/debug on Overview load) succeed in parallel before throttling.
const rateLimitDefaultBurst = 20

// rateLimitSweepInterval is how often the sweeper culls stale buckets. Set
// to a few minutes so it doesn't compete with hot path under load.
const rateLimitSweepInterval = 5 * time.Minute

// rateLimitStaleAfter is how long a bucket may sit idle before the sweeper
// removes it. The bucket is recreated on demand on the next request from
// that IP — the only cost of culling is one map miss per restart per IP.
const rateLimitStaleAfter = 10 * time.Minute

// rateLimiter is the per-IP token-bucket state. Locking is per-bucket via
// the manager's mutex; we don't shard because the map is short-lived and
// per-request critical section is microseconds.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*ipBucket
	rate    float64 // tokens added per second
	burst   float64 // bucket capacity
}

type ipBucket struct {
	tokens   float64
	last     time.Time
	lastSeen time.Time
}

// newRateLimiter constructs a limiter and starts its sweeper goroutine. The
// sweeper terminates when the process does; this is a long-lived,
// process-scoped object (one per route group that mounts it).
func newRateLimiter(rate float64, burst int) *rateLimiter {
	if rate <= 0 {
		rate = rateLimitDefaultRate
	}
	if burst <= 0 {
		burst = rateLimitDefaultBurst
	}
	l := &rateLimiter{
		buckets: make(map[string]*ipBucket),
		rate:    rate,
		burst:   float64(burst),
	}
	go l.sweepLoop()
	return l
}

// allow returns true if the request should proceed, false if it should be
// rejected with 429. Refills the bucket lazily on each call (no per-IP
// timer) so the cost of an idle IP is one map miss + one struct alloc.
func (l *rateLimiter) allow(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[ip]
	if !ok {
		l.buckets[ip] = &ipBucket{tokens: l.burst - 1, last: now, lastSeen: now}
		return true
	}

	// Refill based on elapsed time since last touch. Cap at burst so an idle
	// IP doesn't accumulate an unbounded credit.
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	b.lastSeen = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *rateLimiter) sweepLoop() {
	t := time.NewTicker(rateLimitSweepInterval)
	defer t.Stop()
	for range t.C {
		cutoff := time.Now().Add(-rateLimitStaleAfter)
		l.mu.Lock()
		for ip, b := range l.buckets {
			if b.lastSeen.Before(cutoff) {
				delete(l.buckets, ip)
			}
		}
		l.mu.Unlock()
	}
}

// clientIP extracts the request's originating IP. Honors X-Forwarded-For's
// leftmost entry (proxied deployments) and falls back to RemoteAddr. Strips
// the port off RemoteAddr so the bucket key is the IP, not "ip:ephemeral".
func clientIP(c *gin.Context) string {
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		// XFF is "client, proxy1, proxy2" — leftmost is the original client.
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := c.GetHeader("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	if host, _, err := net.SplitHostPort(c.Request.RemoteAddr); err == nil {
		return host
	}
	return c.Request.RemoteAddr
}

// RateLimitPerIP returns a Gin middleware that throttles each client IP to
// `rate` requests/sec with a burst of `burst`. Over-limit requests get
// 429 + a structured JSON body so the SPA's error toaster can render a
// useful message instead of a generic failure.
//
// rate <= 0 or burst <= 0 falls back to module defaults so callers don't
// have to compute them.
func RateLimitPerIP(rate float64, burst int) gin.HandlerFunc {
	l := newRateLimiter(rate, burst)
	return func(c *gin.Context) {
		ip := clientIP(c)
		if !l.allow(ip) {
			// Retry-After is a hint, not a guarantee — the bucket refills
			// continuously so the floor is roughly 1/rate seconds.
			c.Header("Retry-After", "1")
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "rate limit exceeded",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}
