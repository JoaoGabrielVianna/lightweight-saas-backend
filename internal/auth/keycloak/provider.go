package keycloak

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
)

// Provider implements auth.AuthProvider against a Keycloak realm using JWKS.
// Token signatures are verified with the realm's published keys; claims are
// then checked for issuer and authorized party.
type Provider struct {
	cfg            Config
	keyfunc        keyfunc.Keyfunc
	issuer         string
	clientID       string              // primary client id — used for role lookup in resource_access.<clientID>
	allowedClients map[string]struct{} // whitelist of acceptable "azp" claim values
}

// NewProvider builds a Provider, performing a blocking initial JWKS fetch so
// the application fails fast if Keycloak is unreachable or misconfigured.
func NewProvider(ctx context.Context, cfg Config, opts JWKSOptions) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	kf, err := newJWKS(ctx, cfg.resolvedJWKSURL(), opts)
	if err != nil {
		return nil, err
	}
	return &Provider{
		cfg:            cfg,
		keyfunc:        kf,
		issuer:         cfg.Issuer(),
		clientID:       cfg.ClientID,
		allowedClients: cfg.allowedClientSet(),
	}, nil
}

// newProviderWithKeyfunc is a test seam: skips network I/O by accepting an
// already-constructed keyfunc.
func newProviderWithKeyfunc(cfg Config, kf keyfunc.Keyfunc) *Provider {
	return &Provider{
		cfg:            cfg,
		keyfunc:        kf,
		issuer:         cfg.Issuer(),
		clientID:       cfg.ClientID,
		allowedClients: cfg.allowedClientSet(),
	}
}

// allowedAlgs restricts accepted signing algorithms to asymmetric families.
// Symmetric algorithms (HS*) are intentionally excluded: with HS* an attacker
// who learns the verification key can mint tokens, and Keycloak realms always
// publish asymmetric keys via JWKS.
var allowedAlgs = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512"}

// ValidateToken parses a bearer token, verifies its signature against the
// JWKS, checks expiry/issuer/azp, and returns the canonical Identity.
//
// Error wrapping: auth.ErrInvalidToken / auth.ErrTokenExpired /
// auth.ErrMissingClaim sentinels are wrapped so callers can errors.Is.
func (p *Provider) ValidateToken(_ context.Context, raw string) (*auth.Identity, error) {
	parsed, err := jwt.Parse(
		raw,
		p.keyfunc.Keyfunc,
		jwt.WithValidMethods(allowedAlgs),
		jwt.WithIssuer(p.issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, fmt.Errorf("%w: %v", auth.ErrTokenExpired, err)
		}
		return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
	}
	if !parsed.Valid {
		return nil, auth.ErrInvalidToken
	}

	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("%w: unexpected claim shape", auth.ErrInvalidToken)
	}

	// Keycloak sets "azp" (authorized party) to the client that requested the
	// token. When present, it must be in the configured allowed-client set.
	// A missing azp is allowed per OIDC core §2 (optional when there's a
	// single audience equal to client_id) — we do NOT silently accept
	// arbitrary client tokens here. The set is built from the explicit
	// AllowedClientIDs list, falling back to {ClientID} for backward
	// compatibility with single-client deployments.
	if azp, _ := claims["azp"].(string); azp != "" {
		if _, ok := p.allowedClients[azp]; !ok {
			return nil, fmt.Errorf("%w: azp %q is not in the allowed-client set", auth.ErrInvalidToken, azp)
		}
	}

	return identityFromClaims(claims, p.clientID)
}

// identityFromClaims projects a Keycloak claim set onto auth.Identity.
// Roles are flattened from realm_access.roles and resource_access.<client>.roles.
func identityFromClaims(c jwt.MapClaims, clientID string) (*auth.Identity, error) {
	sub, _ := c["sub"].(string)
	if sub == "" {
		return nil, fmt.Errorf("%w: sub", auth.ErrMissingClaim)
	}

	email, _ := c["email"].(string)
	username, _ := c["preferred_username"].(string)

	roles := extractRoles(c, clientID)

	var exp time.Time
	if e, ok := c["exp"].(float64); ok {
		exp = time.Unix(int64(e), 0)
	}

	return &auth.Identity{
		Subject:   sub,
		Email:     email,
		Username:  username,
		Roles:     roles,
		ExpiresAt: exp,
		Raw:       map[string]any(c),
	}, nil
}

func extractRoles(c jwt.MapClaims, clientID string) []string {
	var roles []string

	if ra, ok := c["realm_access"].(map[string]any); ok {
		roles = appendStringSlice(roles, ra["roles"])
	}
	if res, ok := c["resource_access"].(map[string]any); ok {
		if client, ok := res[clientID].(map[string]any); ok {
			roles = appendStringSlice(roles, client["roles"])
		}
	}
	return roles
}

func appendStringSlice(dst []string, v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return dst
	}
	for _, item := range arr {
		if s, ok := item.(string); ok {
			dst = append(dst, s)
		}
	}
	return dst
}
