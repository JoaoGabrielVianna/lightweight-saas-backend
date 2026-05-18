package auth

import (
	"context"
	"errors"
)

// AuthProvider validates a bearer token and returns the principal identity.
// Implementations must be safe for concurrent use.
//
// The interface is deliberately minimal so business code never depends on a
// concrete provider (Keycloak, Auth0, Supabase, Clerk, custom). Adding methods
// here is a breaking change for every provider — push provider-specific
// capabilities into the concrete type instead.
type AuthProvider interface {
	ValidateToken(ctx context.Context, rawToken string) (*Identity, error)
}

// Sentinel errors. Concrete providers wrap these with context via fmt.Errorf
// so callers can errors.Is on the kind without depending on provider details.
var (
	ErrInvalidToken = errors.New("invalid token")
	ErrTokenExpired = errors.New("token expired")
	ErrMissingClaim = errors.New("missing required claim")
)
