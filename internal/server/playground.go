package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/gin-gonic/gin"
)

// playgroundAssetDir is the on-disk location of the DEV-ONLY playground
// static assets, relative to the API process's working directory. The
// repo-root `web/` tree houses these because the user-facing organization
// keeps web assets out of the Go package tree (and Go's go:embed can't
// reach paths outside the embedding package).
//
// Containers should COPY this directory into /app/web/. The Makefile and
// `go run ./cmd/api` invocations are expected to run from the repo root.
const playgroundAssetDir = "web/dev"

// mountPlayground wires the DEV-ONLY developer-auth surface IFF
// cfg.DevPlaygroundEnabled is true. It exposes both the in-browser
// Keycloak login UI (/dev/auth) and a token-introspection endpoint
// (/auth/debug). Neither must ever be enabled in production.
//
// Surface:
//
//	GET  /dev/auth                — HTML shell
//	GET  /dev/auth/auth.js        — playground JS (PKCE flow)
//	GET  /dev/auth/styles.css     — playground stylesheet
//	GET  /dev/auth/config.json    — runtime config the JS fetches on load
//	GET  /auth/debug              — token-introspection endpoint (DEV-ONLY)
//
// All endpoints respond 404 when the env flag is false, which is the
// default. Production deployments simply don't set DEV_PLAYGROUND_ENABLED.
func mountPlayground(r *gin.Engine, cfg *config.Config, provider auth.AuthProvider) {
	if !cfg.DevPlaygroundEnabled {
		return
	}

	log.Warn("DEV_PLAYGROUND_ENABLED=true — mounting /dev/auth + /auth/debug (DEV-ONLY). Do not run this in production.")

	r.GET("/dev/auth", servePlaygroundFile("auth.html", "text/html; charset=utf-8"))
	r.GET("/dev/auth/auth.js", servePlaygroundFile("auth.js", "application/javascript; charset=utf-8"))
	r.GET("/dev/auth/styles.css", servePlaygroundFile("styles.css", "text/css; charset=utf-8"))

	// Config endpoint — what the JS needs to drive the PKCE flow. Values
	// come from the API's own runtime config so the playground always
	// stays in sync with whatever Keycloak the API is talking to.
	r.GET("/dev/auth/config.json", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"keycloakUrl": cfg.KeycloakURL,
			"realm":       cfg.KeycloakRealm,
			"clientId":    cfg.DevPlaygroundClientID,
			// apiBase is empty → JS calls same-origin (/me, /health).
			"apiBase":     "",
			"redirectUri": "http://localhost:" + cfg.Port + "/dev/auth",
		})
	})

	r.GET("/auth/debug", authDebugHandler(cfg, provider))
}

// authDebugHandler returns a gin handler implementing GET /auth/debug.
//
// Purpose: collapse the "is my token rejected and why?" debugging loop into
// a single curl. It accepts a token via either Authorization: Bearer or
// ?token=, decodes the payload locally to surface received azp/sub values
// (even if validation later fails), then runs the same provider validation
// the real middleware uses and reports the outcome.
//
// Response shape (always HTTP 200 — the endpoint is observational, not a
// gate; the `valid` field carries the answer):
//
//	{
//	  "issuer":          "<URL>/realms/<realm>",   // what this API expects
//	  "allowed_clients": ["a", "b"],               // azp whitelist
//	  "received_azp":    "<from token, unverified>",
//	  "received_sub":    "<from token, unverified>",
//	  "valid":           true | false,
//	  "reason":          "<provider error message when valid=false>"
//	}
//
// Security: the endpoint surfaces unverified claim values. That's
// intentional — debugging a rejected token requires showing what was in it.
// Because the route is gated by DEV_PLAYGROUND_ENABLED (off by default),
// and the only information exposed is what the caller already has (their
// own token + the API's expected issuer/clients), there is no
// confidentiality risk in dev.
func authDebugHandler(cfg *config.Config, provider auth.AuthProvider) gin.HandlerFunc {
	expectedIssuer := strings.TrimRight(cfg.KeycloakURL, "/") + "/realms/" + cfg.KeycloakRealm
	allowedClients := cfg.KeycloakAllowedClientIDs
	if len(allowedClients) == 0 {
		// Mirrors the keycloak provider's fallback so the debug output
		// matches what the validator actually uses.
		allowedClients = []string{cfg.KeycloakClientID}
	}

	return func(c *gin.Context) {
		// Stable response shape. Empty values mean "claim absent from token"
		// rather than "field missing from API contract" — keeps consumers'
		// jq/JSONPath expressions resilient across tokens with different
		// scope profiles.
		resp := gin.H{
			"issuer":          expectedIssuer,
			"allowed_clients": allowedClients,
			"received_azp":    "",
			"received_sub":    "",
			"exp":             "",
			"expired":         false,
			"iat":             "",
			"aud":             []string{},
			"email":           "",
			"roles":           []string{},
			"valid":           false,
			"reason":          "",
		}

		raw := bearerFromHeader(c.GetHeader("Authorization"))
		if raw == "" {
			raw = c.Query("token")
		}
		if raw == "" {
			resp["reason"] = "no token supplied (use Authorization: Bearer <jwt> or ?token=<jwt>)"
			c.JSON(http.StatusOK, resp)
			return
		}

		// 1. Decode the payload WITHOUT verifying — populate the surfaced
		//    claim fields even when the token is malformed or has been
		//    tampered with. These values are caller-controlled; the
		//    consumer knows that. Surfaced fields are restricted to claims
		//    a developer needs to debug auth failures (iss/azp/sub/aud/
		//    exp/iat/email/roles). Sensitive or signature-dependent claims
		//    (like nonces, secrets baked into custom mappers) are not
		//    surfaced — the consumer can decode the full token themselves
		//    if they need them.
		if claims, err := decodeJWTPayload(raw); err == nil {
			if v, ok := claims["azp"].(string); ok {
				resp["received_azp"] = v
			}
			if v, ok := claims["sub"].(string); ok {
				resp["received_sub"] = v
			}
			if v, ok := claims["email"].(string); ok {
				resp["email"] = v
			}
			if iso, _ := formatTimestampClaim(claims["iat"]); iso != "" {
				resp["iat"] = iso
			}
			if iso, isExpired := formatExpClaim(claims["exp"]); iso != "" {
				resp["exp"] = iso
				resp["expired"] = isExpired
			}
			resp["aud"] = normalizeAudClaim(claims["aud"])
			resp["roles"] = extractRolesFromClaims(claims, cfg.KeycloakClientID)
		}

		// 2. Run the real validation path. If it succeeds, valid=true and
		//    reason stays empty. If it fails, surface the provider's
		//    error verbatim so the developer sees exactly which check
		//    rejected the token (wrong iss, unknown azp, missing sub,
		//    expired, bad signature, ...).
		if _, err := provider.ValidateToken(c.Request.Context(), raw); err != nil {
			resp["reason"] = err.Error()
		} else {
			resp["valid"] = true
		}

		c.JSON(http.StatusOK, resp)
	}
}

// formatTimestampClaim renders a Unix-seconds JWT timestamp claim as RFC3339
// UTC. Returns "" when the claim is absent or not a number — encoding/json
// surfaces JWT numbers as float64.
func formatTimestampClaim(v any) (string, time.Time) {
	f, ok := v.(float64)
	if !ok {
		return "", time.Time{}
	}
	t := time.Unix(int64(f), 0).UTC()
	return t.Format(time.RFC3339), t
}

// formatExpClaim is formatTimestampClaim plus an "expired" check against the
// process clock. Uses strict time.Now().After — matches the validator's own
// behavior (no leeway is configured anywhere in this codebase).
func formatExpClaim(v any) (string, bool) {
	iso, t := formatTimestampClaim(v)
	if iso == "" {
		return "", false
	}
	return iso, time.Now().After(t)
}

// normalizeAudClaim coerces the "aud" claim into a string array. Per
// RFC 7519 §4.1.3 the value may be either a single string or an array of
// strings; debug consumers always get an array (possibly empty) so a single
// jq path works regardless of how the token was minted.
func normalizeAudClaim(v any) []string {
	switch x := v.(type) {
	case string:
		if x == "" {
			return []string{}
		}
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return []string{}
}

// extractRolesFromClaims unions roles from realm_access.roles and from
// resource_access.<primaryClient>.roles (deduped, order-preserving).
// Mirrors the keycloak provider's identity.Roles construction so debug
// output exactly matches what the API would see post-validation. When the
// token carries neither claim, returns an empty array — never nil — so the
// JSON shape is stable for downstream tooling.
func extractRolesFromClaims(c map[string]any, primaryClient string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	add := func(role string) {
		if role == "" {
			return
		}
		if _, dup := seen[role]; dup {
			return
		}
		seen[role] = struct{}{}
		out = append(out, role)
	}

	if ra, ok := c["realm_access"].(map[string]any); ok {
		if rs, ok := ra["roles"].([]any); ok {
			for _, r := range rs {
				if s, ok := r.(string); ok {
					add(s)
				}
			}
		}
	}
	if res, ok := c["resource_access"].(map[string]any); ok {
		if client, ok := res[primaryClient].(map[string]any); ok {
			if rs, ok := client["roles"].([]any); ok {
				for _, r := range rs {
					if s, ok := r.(string); ok {
						add(s)
					}
				}
			}
		}
	}
	return out
}

// bearerFromHeader extracts a Bearer token from an Authorization header
// value. Returns "" when the header is missing or doesn't carry a Bearer
// scheme. Tolerant of casing and trailing whitespace.
func bearerFromHeader(h string) string {
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) && !strings.HasPrefix(h, "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// decodeJWTPayload base64url-decodes the second segment of a JWT and
// unmarshals it into a generic claim map. No signature check; this is
// for human-facing introspection of token contents only.
func decodeJWTPayload(raw string) (map[string]any, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a JWT (expected 3 dot-separated segments)")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some clients pad the payload — try standard URL-safe decoding
		// as a fallback before giving up.
		if data, err = base64.URLEncoding.DecodeString(parts[1]); err != nil {
			return nil, err
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(data, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// servePlaygroundFile returns a gin handler that serves a single file from
// the playground asset directory with the supplied Content-Type. We don't
// use gin's StaticFile because we want explicit MIME handling and a
// single source of truth for the asset directory.
func servePlaygroundFile(name, contentType string) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := filepath.Join(playgroundAssetDir, name)
		c.Header("Content-Type", contentType)
		// Prevent caching during dev so edits show up on refresh.
		c.Header("Cache-Control", "no-store")
		c.File(path)
	}
}
