// Package auth — provider-agnostic middleware.
//
// RequireAuth wraps any AuthProvider. The provider implementation chosen
// at process boot (Keycloak today, anything tomorrow) is invisible to
// every consumer of this middleware.
package auth

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// RequireAuth returns a Gin middleware that:
//  1. extracts the Bearer token from the Authorization header
//  2. validates it via the injected AuthProvider
//  3. stores the resulting *Identity in the gin context for handlers
//  4. emits an AuthEvent (token_validated, validation_failed, ...) for
//     every request so observability backends can subscribe without
//     touching middleware code
//
// On failure the request is aborted with 401 and a generic error message;
// the precise reason is captured in the AuthEvent.Reason field, not in
// the response body, to avoid leaking validation details to clients.
func RequireAuth(p AuthProvider) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		method := c.Request.Method

		raw, kind := extractBearer(c)
		if raw == "" {
			EmitEvent(AuthEvent{
				Kind:     kind,
				Reason:   "missing or malformed Authorization header",
				Path:     path,
				Method:   method,
				Duration: time.Since(start),
			})
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}

		id, err := p.ValidateToken(c.Request.Context(), raw)
		if err != nil {
			EmitEvent(AuthEvent{
				Kind:     EventValidationFailed,
				Reason:   err.Error(),
				Path:     path,
				Method:   method,
				Duration: time.Since(start),
			})
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}

		StoreIdentity(c, id)
		EmitEvent(AuthEvent{
			Kind:     EventTokenValidated,
			Subject:  id.Subject,
			Path:     path,
			Method:   method,
			Duration: time.Since(start),
		})
		c.Next()
	}
}

// RequireRole returns a Gin middleware that gates a route group on the
// caller possessing the given realm role. Must be mounted AFTER RequireAuth
// in the same group — it expects Identity in gin context.
//
// On denial: emits an AuthEvent{Kind: EventForbidden} so RBAC failures show
// up in observability alongside auth failures, then responds 403.
func RequireRole(role string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := IdentityFrom(c)
		if !ok {
			// Defensive: shouldn't happen if RequireAuth ran first, but if a
			// caller wires this directly we return 401 (not 403) — there's
			// no identity to check the role against.
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}
		if !id.HasRole(role) {
			EmitEvent(AuthEvent{
				Kind:    EventForbidden,
				Subject: id.Subject,
				Reason:  "missing role: " + role,
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

// RequireAnyRole is RequireRole's disjunction — caller must possess at
// least one of the listed roles.
func RequireAnyRole(roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := IdentityFrom(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}
		for _, r := range roles {
			if id.HasRole(r) {
				c.Next()
				return
			}
		}
		EmitEvent(AuthEvent{
			Kind:    EventForbidden,
			Subject: id.Subject,
			Reason:  "missing any of roles: " + strings.Join(roles, ","),
			Path:    c.Request.URL.Path,
			Method:  c.Request.Method,
		})
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		c.Abort()
	}
}

// extractBearer returns the raw token string and an event kind describing
// the input quality (missing header vs malformed header) when the token
// is unusable.
func extractBearer(c *gin.Context) (string, AuthEventKind) {
	h := c.GetHeader("Authorization")
	if h == "" {
		return "", EventMissingHeader
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", EventMalformedHeader
	}
	tok := strings.TrimSpace(strings.TrimPrefix(h, prefix))
	if tok == "" {
		return "", EventMalformedHeader
	}
	return tok, ""
}
