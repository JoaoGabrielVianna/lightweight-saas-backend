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
//
// # Three middlewares, one bucket implementation
//
//	RateLimitPerIP         /admin/*  per client IP, legacy 429 body
//	RateLimitEdge          /v1       per client IP, /v1 envelope, releases
//	                                 the token for machine callers
//	RateLimitPerCredential /v1       per project credential, after auth
//
// /admin/* and /v1 do not share a middleware because they do not share an error
// contract: /admin/* answers `{"error":"rate limit exceeded"}` and must keep
// doing so, while /v1 answers the envelope with a code and a request id that
// every other /v1 error also carries.
package server

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/authz"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
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

// rateLimiter is the token-bucket state, keyed by an opaque string. Locking is
// via the manager's single mutex; we don't shard because the map is short-lived
// and the per-request critical section is microseconds.
//
// The key is deliberately untyped: the edge limiter keys by client IP and the
// principal limiter keys by `key_<uuid>`, and neither needs a different bucket
// implementation to do so. TD-027 (per-process buckets) is a swap of this type,
// not a redesign, for the same reason.
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

// allow reports whether the request should proceed, and how many whole tokens
// are left in the bucket afterwards. Refills lazily on each call (no per-key
// timer) so the cost of an idle key is one map miss + one struct alloc.
//
// The remaining count is returned rather than exposed through a second lookup
// so a caller emitting RateLimit-Remaining reads the value the decision was
// actually made from, under the same lock, instead of a value that another
// request may already have changed.
func (l *rateLimiter) allow(key string) (ok bool, remaining int) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b, exists := l.buckets[key]
	if !exists {
		b = &ipBucket{tokens: l.burst, last: now, lastSeen: now}
		l.buckets[key] = b
	} else {
		// Refill based on elapsed time since last touch. Cap at burst so an
		// idle key doesn't accumulate an unbounded credit.
		b.tokens += now.Sub(b.last).Seconds() * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
	}
	b.last = now
	b.lastSeen = now

	if b.tokens < 1 {
		return false, 0
	}
	b.tokens--
	return true, int(b.tokens)
}

// refund returns one token to a bucket, capped at burst.
//
// It exists for the reserve-then-release pattern the edge limiter uses: the
// token has to be taken BEFORE the caller is known, because the whole point of
// the edge limiter is to run before authentication, and it can only be decided
// afterwards whether this request was the kind the edge limiter is meant to
// meter. See RateLimitEdge.
//
// A refund for a key whose bucket has already been swept is a no-op rather than
// a resurrection: the bucket would be recreated full on the next request
// anyway, so crediting a token to a fresh bucket would be indistinguishable and
// creating one here would let a refund allocate a map entry for an IP that is
// no longer sending traffic.
func (l *rateLimiter) refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, exists := l.buckets[key]
	if !exists {
		return
	}
	b.tokens++
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
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
		if ok, _ := l.allow(clientIP(c)); !ok {
			// Retry-After is a hint, not a guarantee — the bucket refills
			// continuously so the floor is roughly 1/rate seconds.
			c.Header("Retry-After", "1")
			// Legacy body shape, byte-for-byte. This middleware is mounted on
			// /admin/* ONLY, and /admin/* must stay unchanged. /v1 uses
			// RateLimitEdge, which answers in the /v1 envelope.
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "rate limit exceeded",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

// ─── The /v1 pair ───────────────────────────────────────────────────────────
//
// /v1 has two rate-limiting problems, and Slice 7 solved them with two buckets
// that competed instead of composing ([TD-026]).
//
//	edge       10 req/s per IP,       before authentication
//	credential 20 req/s per key_…,    after authentication
//
// A backend calls from one address, so its effective allowance was
// min(10, 20) = 10. The per-credential limit — the number PROJECTS.md publishes
// as the machine contract — could not be reached, which made the published
// contract untrue rather than merely conservative.
//
// # Why the fix is not a bigger number
//
// Raising the edge limit to sit above the credential limit trades away exactly
// the protection the edge limiter exists for: it runs before authentication
// precisely so an anonymous flood cannot buy a credential lookup or a JWT
// signature verification per request. Every request the raise admits is a
// request an unauthenticated attacker also gets.
//
// The two limits were never in competition conceptually. They meter different
// traffic:
//
//	edge       — traffic whose caller is unknown or unproven
//	credential — traffic from a known, attributable, revocable machine
//
// They collided only because the edge bucket was charged for BOTH, having no
// way to tell them apart at the point where it runs.
//
// # Reserve, then release
//
// So the edge limiter still charges every request up front — it must, since it
// runs before anyone is identified — and RELEASES the token once the request
// turns out to have been the second kind. The release happens after the chain
// returns, when the principal is known:
//
//	unauthenticated / invalid credential  → charged      (unchanged)
//	operator                              → charged      (unchanged)
//	project credential                    → released     (new)
//
// Anonymous-flood protection is therefore bit-for-bit what it was: the edge
// numbers did not move, and every request that fails authentication still
// costs a token. The console's behaviour did not move either — an operator is
// still metered at the edge exactly as before, because changing that would be
// a silent change to a working surface to solve a problem nobody reported.
//
// What did move is that a project credential's own bucket is now the only
// thing standing between it and the handler, so the published number is the
// enforced number.
//
// # What a valid credential can still cost
//
// A credential over its own limit is refused by the principal limiter, but its
// request was released at the edge, so a runaway backend can drive one indexed
// SELECT plus one SHA-256 per request with no ceiling above the credential
// bucket. That is bounded, attributable and instantly revocable, unlike the
// anonymous case, and it is recorded as [TD-028] rather than pre-solved with a
// negative cache nobody has needed yet.
//
// [TD-026]: docs/TECH_DEBT.md#td-026
// [TD-028]: docs/TECH_DEBT.md#td-028

// RateLimitEdge is the /v1 anonymous-protection limiter.
//
// Same token bucket, same key (client IP) and same defaults as RateLimitPerIP,
// which /admin/* keeps using. It differs in exactly two ways, and both are
// required by /v1 rather than nice to have:
//
//  1. It answers in the /v1 error envelope, with the request id. The legacy
//     `{"error":"rate limit exceeded"}` body has no `error.code` and no
//     `request_id`, so a 429 was the one /v1 response an SDK could not parse
//     with the same decoder as every other error.
//  2. It releases the token for a request that authenticated as a project
//     credential — the [TD-026] fix described above.
//
// The two could not be added to RateLimitPerIP itself without changing
// /admin/*, which must stay byte-compatible.
func RateLimitEdge(rate float64, burst int) gin.HandlerFunc {
	l := newRateLimiter(rate, burst)
	return func(c *gin.Context) {
		ip := clientIP(c)
		if ok, _ := l.allow(ip); !ok {
			c.Header("Retry-After", "1")
			// Deliberately NO RateLimit-* headers here. They would publish the
			// anonymous-protection tuning to an unauthenticated caller, which
			// is the one caller with no business knowing it. The principal
			// limiter publishes the quota that belongs to the caller instead.
			abortRateLimited(c)
			return
		}

		c.Next()

		// Release. Read AFTER the chain because that is the earliest point the
		// caller is known — AuthenticatePrincipal runs downstream of here.
		if p, ok := auth.PrincipalFrom(c); ok && p.IsProject() {
			l.refund(ip)
		}
	}
}

// credentialRateLimitRate is the steady-state allowance for one project
// credential, in requests per second.
//
// Higher than the per-IP default because the caller is a backend, not a human:
// a synchronisation pass or a batch of webhooks legitimately bursts in a way no
// console click-rate does. Low enough that one misbehaving deployment cannot
// saturate the process.
const credentialRateLimitRate = 20.0

// credentialRateLimitBurst is the burst allowance for one project credential.
const credentialRateLimitBurst = 40

// RateLimitPerCredential throttles each PROJECT CREDENTIAL independently.
//
// Mounted AFTER authentication, because the bucket key only exists once the
// caller is known. RateLimitEdge stays in front of authentication, where it
// protects the cost of authenticating; this one is the whole of the allowance a
// machine consumer actually has.
//
// # Why the credential and not the project
//
// Revoking the key that is flooding must fix the flood immediately. Keyed by
// project, a runaway deployment would keep throttling its well-behaved siblings
// until every one of the project's keys was revoked.
//
// # Why the credential and not the IP
//
// A credential's allowance follows the credential, not the address it is used
// from. A backend that scales to three instances, or moves between them, does
// not get three times the quota, and does not lose its history by moving.
//
// # Operators are deliberately not bucketed here
//
// They keep the per-IP limit they have always had. Adding a per-operator bucket
// would change existing console behaviour to solve a problem nobody has: the
// console is human-paced and already covered at the edge.
//
// # Known limitation
//
// The bucket is per process. Two replicas therefore permit twice this rate.
// That is acceptable for the single-process self-hosted deployment this targets
// and is documented rather than solved: solving it means a shared store, which
// is a dependency this slice does not take.
func RateLimitPerCredential(rate float64, burst int) gin.HandlerFunc {
	if rate <= 0 {
		rate = credentialRateLimitRate
	}
	if burst <= 0 {
		burst = credentialRateLimitBurst
	}
	l := newRateLimiter(rate, burst)

	// RateLimit-Limit advertises the SUSTAINED rate, not the burst capacity.
	//
	// It used to advertise the burst (40 while the sustained rate was 20), which
	// is the one number a client must not pace itself by: a client that read 40
	// as its quota would be refused half its traffic for following the header.
	// Advertising the sustained rate is both truthful over any window longer
	// than the burst and the same number PROJECTS.md publishes, so the header
	// and the documentation cannot disagree.
	//
	// Burst above it is a deliberate under-promise: allowing more than is
	// advertised is safe, advertising more than is allowed is not.
	quota := int(rate)
	limitStr := strconv.Itoa(quota)

	return func(c *gin.Context) {
		p, ok := auth.PrincipalFrom(c)
		if !ok || p == nil || !p.IsProject() {
			c.Next()
			return
		}

		allowed, remaining := l.allow(p.Project.CredentialID)
		c.Header("RateLimit-Limit", limitStr)
		if !allowed {
			c.Header("Retry-After", "1")
			c.Header("RateLimit-Remaining", "0")
			abortRateLimited(c)
			return
		}
		// Capped at the advertised quota so Remaining is never greater than
		// Limit, which would make the pair unreadable.
		if remaining > quota {
			remaining = quota
		}
		c.Header("RateLimit-Remaining", strconv.Itoa(remaining))
		c.Next()
	}
}

// abortRateLimited writes the single /v1 rate-limit refusal.
//
// One shape for both limiters: the caller is a program, /v1 promises one error
// envelope, and a 429 that needed its own decoder would be the one response an
// SDK could not treat like the rest. Which limiter refused is deliberately NOT
// distinguishable — that is internal tuning, and the client's correct reaction
// (back off, retry) is identical either way.
func abortRateLimited(c *gin.Context) {
	c.AbortWithStatusJSON(authz.ErrRateLimited.Status, authz.ErrorResponse{
		Error: authz.ErrorBody{
			Code:      authz.ErrRateLimited.Code,
			Message:   authz.ErrRateLimited.Message,
			RequestID: requestid.FromContext(c),
		},
	})
}
