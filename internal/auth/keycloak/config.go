package keycloak

import (
	"errors"
	"strings"
)

// Config holds the runtime parameters needed to validate Keycloak-issued
// tokens. ClientSecret is unused by token validation (kept for future
// Admin-API calls); JWKSURL is derived from URL + Realm when empty.
//
// AllowedClientIDs is the whitelist of acceptable token "azp" claim values.
// When empty, falls back to just {ClientID} — backward-compatible with
// single-client deployments. When set, must contain ClientID (the primary)
// plus any auxiliary clients permitted to mint tokens for this API
// (e.g. an in-browser dev playground using its own public PKCE client).
type Config struct {
	URL              string   // base URL, e.g. http://keycloak:8080
	Realm            string   // realm name, e.g. saas
	ClientID         string   // primary client id (used for role lookup in resource_access)
	ClientSecret     string   // optional; reserved for Admin API access
	JWKSURL          string   // optional override; derived when empty
	AllowedClientIDs []string // optional whitelist of accepted azp values; defaults to {ClientID}
}

// allowedClientSet returns the effective set of accepted azp values:
//   - the explicit AllowedClientIDs list when non-empty
//   - otherwise the single-element set {ClientID}
//
// Empty strings inside AllowedClientIDs are filtered out so a stray comma
// in an env-var list (e.g. "a,,b") can't accidentally accept tokens with
// an empty azp claim.
func (c Config) allowedClientSet() map[string]struct{} {
	set := map[string]struct{}{}
	for _, id := range c.AllowedClientIDs {
		if id != "" {
			set[id] = struct{}{}
		}
	}
	if len(set) == 0 && c.ClientID != "" {
		set[c.ClientID] = struct{}{}
	}
	return set
}

// Issuer returns the expected "iss" claim value for tokens minted by this
// realm. Built per Keycloak's standard URL layout.
func (c Config) Issuer() string {
	return strings.TrimRight(c.URL, "/") + "/realms/" + c.Realm
}

// resolvedJWKSURL returns JWKSURL when set, otherwise derives the standard
// OIDC certs endpoint from URL + Realm.
func (c Config) resolvedJWKSURL() string {
	if c.JWKSURL != "" {
		return c.JWKSURL
	}
	return c.Issuer() + "/protocol/openid-connect/certs"
}

// Validate enforces the fields required to validate inbound tokens.
// ClientSecret is intentionally not required.
func (c Config) Validate() error {
	missing := []string{}
	if c.URL == "" {
		missing = append(missing, "URL")
	}
	if c.Realm == "" {
		missing = append(missing, "Realm")
	}
	if c.ClientID == "" {
		missing = append(missing, "ClientID")
	}
	if c.URL == "" && c.JWKSURL == "" {
		missing = append(missing, "JWKSURL")
	}
	if len(missing) > 0 {
		return errors.New("keycloak config missing required fields: " + strings.Join(missing, ", "))
	}
	return nil
}
