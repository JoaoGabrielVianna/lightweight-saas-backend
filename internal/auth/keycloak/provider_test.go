package keycloak

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
)

const testKid = "test-key-1"

// newTestStack stands up an httptest server that returns a single-key JWKS for
// an ephemeral RSA keypair, then builds a Provider pointed at it.
func newTestStack(t *testing.T) (*Provider, *rsa.PrivateKey, string) {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}

	jwks := map[string]any{
		"keys": []map[string]any{rsaPublicJWK(&priv.PublicKey, testKid)},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		URL:              "http://example.test/auth",
		Realm:            "saas",
		ClientID:         "saas-backend",
		JWKSURL:          srv.URL,
		AllowedClientIDs: []string{"saas-backend", "saas-dev-playground"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	p, err := NewProvider(ctx, cfg, JWKSOptions{RefreshInterval: time.Hour})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return p, priv, cfg.Issuer()
}

func rsaPublicJWK(pub *rsa.PublicKey, kid string) map[string]any {
	eBytes := big.NewInt(int64(pub.E)).Bytes()
	return map[string]any{
		"kty": "RSA",
		"alg": "RS256",
		"use": "sig",
		"kid": kid,
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(eBytes),
	}
}

func signRS256(t *testing.T, priv *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func baseClaims(issuer string) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":                issuer,
		"azp":                "saas-backend",
		"sub":                "abc-123-uuid",
		"email":              "user@test.com",
		"preferred_username": "user",
		"exp":                now.Add(time.Hour).Unix(),
		"iat":                now.Unix(),
		"realm_access":       map[string]any{"roles": []any{"user", "offline_access"}},
		"resource_access": map[string]any{
			"saas-backend": map[string]any{"roles": []any{"client-role-a"}},
		},
	}
}

func TestValidateToken_Valid(t *testing.T) {
	p, priv, issuer := newTestStack(t)
	raw := signRS256(t, priv, testKid, baseClaims(issuer))

	id, err := p.ValidateToken(context.Background(), raw)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	if id.Subject != "abc-123-uuid" {
		t.Errorf("sub: got %q", id.Subject)
	}
	if id.Email != "user@test.com" {
		t.Errorf("email: got %q", id.Email)
	}
	if id.Username != "user" {
		t.Errorf("username: got %q", id.Username)
	}
	wantRoles := map[string]bool{"user": true, "offline_access": true, "client-role-a": true}
	for r := range wantRoles {
		if !id.HasRole(r) {
			t.Errorf("expected role %q, missing", r)
		}
	}
	if id.HasRole("admin") {
		t.Errorf("HasRole(admin) unexpectedly true")
	}
	if id.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt not parsed")
	}
	if id.Raw == nil {
		t.Errorf("Raw claims not populated")
	}
}

func TestValidateToken_Expired(t *testing.T) {
	p, priv, issuer := newTestStack(t)
	c := baseClaims(issuer)
	c["exp"] = time.Now().Add(-time.Minute).Unix()
	c["iat"] = time.Now().Add(-time.Hour).Unix()
	raw := signRS256(t, priv, testKid, c)

	_, err := p.ValidateToken(context.Background(), raw)
	if !errors.Is(err, auth.ErrTokenExpired) {
		t.Fatalf("expected ErrTokenExpired, got %v", err)
	}
}

func TestValidateToken_WrongIssuer(t *testing.T) {
	p, priv, _ := newTestStack(t)
	c := baseClaims("http://evil.local/realms/saas")
	raw := signRS256(t, priv, testKid, c)

	_, err := p.ValidateToken(context.Background(), raw)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestValidateToken_WrongAzp(t *testing.T) {
	p, priv, issuer := newTestStack(t)
	c := baseClaims(issuer)
	c["azp"] = "other-client"
	raw := signRS256(t, priv, testKid, c)

	_, err := p.ValidateToken(context.Background(), raw)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
	if !strings.Contains(err.Error(), "azp") {
		t.Errorf("expected azp mention in error, got %v", err)
	}
}

func TestValidateToken_Tampered(t *testing.T) {
	p, priv, issuer := newTestStack(t)
	raw := signRS256(t, priv, testKid, baseClaims(issuer))

	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("expected JWT with 3 segments")
	}
	// Mutate the payload segment so the signature no longer matches.
	payload := parts[1]
	if len(payload) == 0 {
		t.Fatalf("empty payload")
	}
	mutated := "X" + payload[1:]
	if mutated == payload {
		mutated = "Y" + payload[1:]
	}
	tampered := parts[0] + "." + mutated + "." + parts[2]

	_, err := p.ValidateToken(context.Background(), tampered)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestValidateToken_MissingSub(t *testing.T) {
	p, priv, issuer := newTestStack(t)
	c := baseClaims(issuer)
	delete(c, "sub")
	raw := signRS256(t, priv, testKid, c)

	_, err := p.ValidateToken(context.Background(), raw)
	if !errors.Is(err, auth.ErrMissingClaim) {
		t.Fatalf("expected ErrMissingClaim, got %v", err)
	}
}

func TestValidateToken_RejectsHS256(t *testing.T) {
	p, _, issuer := newTestStack(t)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, baseClaims(issuer))
	tok.Header["kid"] = testKid
	raw, err := tok.SignedString([]byte("any-secret"))
	if err != nil {
		t.Fatalf("sign hs256: %v", err)
	}

	_, err = p.ValidateToken(context.Background(), raw)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken for HS256, got %v", err)
	}
}

func TestConfig_Validate(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"full", Config{URL: "http://kc", Realm: "saas", ClientID: "c", JWKSURL: "http://j"}, true},
		{"derive jwks", Config{URL: "http://kc", Realm: "saas", ClientID: "c"}, true},
		{"missing url", Config{Realm: "saas", ClientID: "c", JWKSURL: "http://j"}, false},
		{"missing realm", Config{URL: "http://kc", ClientID: "c", JWKSURL: "http://j"}, false},
		{"missing client", Config{URL: "http://kc", Realm: "saas", JWKSURL: "http://j"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.ok && err != nil {
				t.Errorf("expected nil, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

func TestConfig_Issuer(t *testing.T) {
	c := Config{URL: "http://kc:8080/", Realm: "saas"}
	if got := c.Issuer(); got != "http://kc:8080/realms/saas" {
		t.Errorf("issuer: got %q", got)
	}
}

func TestValidateToken_AcceptsAdditionalAllowedClient(t *testing.T) {
	// The test stack is built with AllowedClientIDs containing both
	// "saas-backend" (primary) and "saas-dev-playground" (secondary).
	// Tokens minted by the playground client must validate successfully.
	p, priv, issuer := newTestStack(t)
	c := baseClaims(issuer)
	c["azp"] = "saas-dev-playground"
	raw := signRS256(t, priv, testKid, c)

	id, err := p.ValidateToken(context.Background(), raw)
	if err != nil {
		t.Fatalf("expected playground-client token to validate, got %v", err)
	}
	if id.Subject != "abc-123-uuid" {
		t.Errorf("identity sub: %q", id.Subject)
	}
}

func TestValidateToken_RejectsTokenFromClientOutsideAllowedSet(t *testing.T) {
	p, priv, issuer := newTestStack(t)
	c := baseClaims(issuer)
	c["azp"] = "evil-client" // not in the allowed list
	raw := signRS256(t, priv, testKid, c)

	_, err := p.ValidateToken(context.Background(), raw)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
	if !strings.Contains(err.Error(), "evil-client") {
		t.Errorf("error should name the rejected azp value, got %v", err)
	}
}

func TestConfig_AllowedClientSet_FallsBackToClientIDWhenListEmpty(t *testing.T) {
	c := Config{ClientID: "primary"}
	set := c.allowedClientSet()
	if _, ok := set["primary"]; !ok {
		t.Errorf("primary client should be in fallback set, got %v", set)
	}
	if len(set) != 1 {
		t.Errorf("fallback set must be exactly {ClientID}, got %v", set)
	}
}

func TestConfig_AllowedClientSet_FiltersBlankEntries(t *testing.T) {
	// A stray comma in the env var like "a,,b" must not let blank-azp tokens
	// through. (Empty azp is allowed via the OIDC-spec branch in
	// ValidateToken, but only because azp is missing — not because the set
	// happens to contain "".)
	c := Config{
		ClientID:         "a",
		AllowedClientIDs: []string{"a", "", "b"},
	}
	set := c.allowedClientSet()
	if _, ok := set[""]; ok {
		t.Errorf("blank entries must not enter the allowed set, got %v", set)
	}
	if len(set) != 2 {
		t.Errorf("expected exactly {a,b}, got %v", set)
	}
}
