// Package auth — live admin authorization.
//
// RequireRole gates on the JWT claim set, which is stale-by-design: once a
// token is signed with `admin` in realm_access.roles, it carries that
// privilege until `exp` regardless of subsequent server-side revocation.
// That window is `accessTokenLifespan` long — up to an hour in the default
// realm — and is the carrier wave for GAP-1 (see docs/SECURITY_GAPS.md §D).
//
// RequireLiveAdmin closes that window by consulting an injected AdminChecker
// (which the wiring layer backs with a live Keycloak lookup) on every admin
// request, with a short-lived in-process cache to bound the per-request cost.
//
// Layering: this file imports nothing from internal/identity — the interface
// here is the seam, the concrete implementation is built in internal/server.
// That keeps the dependency direction identity → auth (handlers already do
// auth.IdentityFrom) and avoids a cycle.
package auth

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// AdminChecker reports whether the given subject (Keycloak `sub`) currently
// has the admin role server-side. Implementations must be safe for concurrent
// use. The interface is deliberately one-method so the wiring layer can adapt
// any identity backend without dragging the auth package into that domain.
type AdminChecker interface {
	IsAdmin(ctx context.Context, subject string) (bool, error)
}

// AdminCheckerFunc adapts a plain function to the AdminChecker interface.
type AdminCheckerFunc func(ctx context.Context, subject string) (bool, error)

// IsAdmin satisfies AdminChecker.
func (f AdminCheckerFunc) IsAdmin(ctx context.Context, subject string) (bool, error) {
	return f(ctx, subject)
}

// AdminInvalidator drops cached admin-status entries — called by handlers
// that mutate role membership (assign/unassign role, delete user, disable
// user). The default no-op implementation lets the auth package compile
// without a cache wired up; the cached implementation is opt-in.
type AdminInvalidator interface {
	Invalidate(subject string)
	InvalidateAll()
}

// NoopAdminInvalidator drops all calls. Used when identity is wired without
// the cache (tests, future no-cache deployments).
type NoopAdminInvalidator struct{}

func (NoopAdminInvalidator) Invalidate(string) {}
func (NoopAdminInvalidator) InvalidateAll()    {}

// DefaultAdminTTL is the cache lifetime for a positive/negative live admin
// lookup. 30s is short enough that an out-of-band revocation (e.g. operator
// deletes the user in Keycloak directly without going through /admin/*)
// closes within a minute, but long enough that a bursting admin client
// doesn't hammer Keycloak. Mutations going through this API invalidate
// immediately, so this only bounds the out-of-band path.
const DefaultAdminTTL = 30 * time.Second

// CachedAdminChecker wraps an upstream AdminChecker with a per-subject TTL
// cache. Concurrent-safe.
//
// Both positive and negative results are cached for the same TTL. Negative
// caching matters here for two reasons:
//
//   - it stops a JWT with admin-in-claim but no live admin (the GAP-1 token)
//     from triggering one Keycloak call per request;
//   - it bounds the worst case if a normal user ever reaches this middleware
//     (the upstream RequireRole check should short-circuit them, but the
//     middleware is defense-in-depth).
type CachedAdminChecker struct {
	upstream AdminChecker
	ttl      time.Duration
	now      func() time.Time // injectable for tests

	mu      sync.RWMutex
	entries map[string]adminCacheEntry
}

type adminCacheEntry struct {
	isAdmin   bool
	expiresAt time.Time
}

// NewCachedAdminChecker wraps upstream with a TTL cache. A zero or negative
// ttl defaults to DefaultAdminTTL — passing 0 from config shouldn't disable
// caching by accident.
func NewCachedAdminChecker(upstream AdminChecker, ttl time.Duration) *CachedAdminChecker {
	if ttl <= 0 {
		ttl = DefaultAdminTTL
	}
	return &CachedAdminChecker{
		upstream: upstream,
		ttl:      ttl,
		now:      time.Now,
		entries:  make(map[string]adminCacheEntry),
	}
}

// IsAdmin returns the cached answer when fresh; otherwise consults the
// upstream and stores the result.
func (c *CachedAdminChecker) IsAdmin(ctx context.Context, subject string) (bool, error) {
	if subject == "" {
		return false, nil
	}

	c.mu.RLock()
	e, ok := c.entries[subject]
	c.mu.RUnlock()
	if ok && c.now().Before(e.expiresAt) {
		return e.isAdmin, nil
	}

	isAdmin, err := c.upstream.IsAdmin(ctx, subject)
	if err != nil {
		// Don't cache errors — next request retries the upstream. We DO NOT
		// fall back to the JWT claim here: the whole point of this layer is
		// to override the claim, so on upstream failure we MUST fail closed.
		return false, err
	}

	c.mu.Lock()
	c.entries[subject] = adminCacheEntry{
		isAdmin:   isAdmin,
		expiresAt: c.now().Add(c.ttl),
	}
	c.mu.Unlock()
	return isAdmin, nil
}

// Invalidate drops the cached entry for subject. Safe to call for a subject
// that was never cached.
func (c *CachedAdminChecker) Invalidate(subject string) {
	if subject == "" {
		return
	}
	c.mu.Lock()
	delete(c.entries, subject)
	c.mu.Unlock()
}

// InvalidateAll flushes the entire cache. Used when a realm-wide event
// (e.g. role deletion) could invalidate many entries at once.
func (c *CachedAdminChecker) InvalidateAll() {
	c.mu.Lock()
	c.entries = make(map[string]adminCacheEntry)
	c.mu.Unlock()
}

// RequireLiveAdmin returns a Gin middleware that consults the AdminChecker
// for the caller's live admin status. Must be mounted AFTER RequireAuth (it
// reads Identity from the context).
//
// Behavior:
//   - no identity in context  → 401 (defensive; should not happen)
//   - empty subject           → 401
//   - checker error           → 503 (fail closed; admin verbs must not run
//     with stale authorization on infra failure)
//   - isAdmin == false        → 403 (the GAP-1 path: JWT-stale but server
//     says no)
//   - isAdmin == true         → pass
//
// Emits EventForbidden on denial so observability sees the stale-token
// rejections distinctly from JWT-claim denials (the Reason field carries
// the marker "live admin check denied").
func RequireLiveAdmin(checker AdminChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := IdentityFrom(c)
		if !ok || id == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}
		if id.Subject == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}

		isAdmin, err := checker.IsAdmin(c.Request.Context(), id.Subject)
		if err != nil {
			EmitEvent(AuthEvent{
				Kind:    EventForbidden,
				Subject: id.Subject,
				Reason:  "live admin check failed: " + err.Error(),
				Path:    c.Request.URL.Path,
				Method:  c.Request.Method,
			})
			// 503 (not 502/500) — the caller's request was well-formed, the
			// authorization backend is temporarily unable to answer. Fail
			// closed: we never let an admin verb through on a guess.
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "authorization check unavailable"})
			c.Abort()
			return
		}
		if !isAdmin {
			EmitEvent(AuthEvent{
				Kind:    EventForbidden,
				Subject: id.Subject,
				Reason:  "live admin check denied: token role no longer present server-side",
				Path:    c.Request.URL.Path,
				Method:  c.Request.Method,
			})
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			c.Abort()
			return
		}
		c.Next()
	}
}
