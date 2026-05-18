package auth

import (
	"time"

	"github.com/gin-gonic/gin"
)

// Identity is the provider-agnostic view of an authenticated principal.
// All AuthProvider implementations populate this from their native claim shape.
type Identity struct {
	Subject   string         // canonical subject id (Keycloak/OIDC "sub")
	Email     string         // RFC 5322 email if available
	Username  string         // "preferred_username" or equivalent
	Roles     []string       // flattened role list (realm + client roles for Keycloak)
	ExpiresAt time.Time      // token expiry
	Raw       map[string]any // full claim set for callers needing extra fields
}

// HasRole returns true when the identity carries the given role.
func (i *Identity) HasRole(role string) bool {
	if i == nil {
		return false
	}
	for _, r := range i.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// identityGinKey is the gin context key under which middleware stores the
// validated *Identity. Kept unexported so handlers must go through
// IdentityFrom — no string-typed key lookups scattered across the codebase.
const identityGinKey = "auth.identity"

// StoreIdentity is called by middleware after successful token validation.
func StoreIdentity(c *gin.Context, id *Identity) {
	c.Set(identityGinKey, id)
}

// IdentityFrom returns the validated identity for the current request.
// The bool is false when no middleware ran or validation failed before storing.
func IdentityFrom(c *gin.Context) (*Identity, bool) {
	v, ok := c.Get(identityGinKey)
	if !ok {
		return nil, false
	}
	id, ok := v.(*Identity)
	return id, ok
}
