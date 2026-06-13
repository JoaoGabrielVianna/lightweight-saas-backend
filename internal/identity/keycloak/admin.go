// Package keycloak implements identity.IdentityProvider against Keycloak's
// REST Admin API.
package keycloak

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
)

// AdminConfig configures the HTTP client that talks to Keycloak's Admin
// REST API on behalf of the service account.
type AdminConfig struct {
	// BaseURL is the URL the API uses to REACH Keycloak (e.g.
	// http://keycloak:8080 inside docker). Distinct from any host-facing
	// URL used for `iss` matching.
	BaseURL string
	// Realm is the Keycloak realm name (e.g. "saas").
	Realm string
	// ClientID + ClientSecret identify the service-account client. This
	// client must have realm-management roles granted to its
	// service-account user.
	ClientID     string
	ClientSecret string
	// HTTPClient overrides the default HTTP client. Defaults to one with
	// a 10s timeout — admin operations are user-driven, not request-path.
	HTTPClient *http.Client
}

// AdminClient is a low-level HTTP client for Keycloak's /admin/realms/<realm>
// surface. It caches the service-account token between calls and refreshes
// transparently on expiry or 401. Concurrent-safe.
type AdminClient struct {
	cfg        AdminConfig
	httpClient *http.Client
	tokenURL   string
	adminBase  string
	// token is read on the request-path. Refresh uses Store(nil) to
	// invalidate after a 401, then the next acquireToken refills it.
	token atomic.Pointer[cachedToken]
}

type cachedToken struct {
	value     string
	expiresAt time.Time
}

// NewAdminClient validates the config and constructs an idle client. No
// network I/O happens here; the first admin call triggers token acquisition.
func NewAdminClient(cfg AdminConfig) (*AdminClient, error) {
	if cfg.BaseURL == "" || cfg.Realm == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, identity.ErrNotConfigured
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	return &AdminClient{
		cfg:        cfg,
		httpClient: hc,
		tokenURL:   base + "/realms/" + cfg.Realm + "/protocol/openid-connect/token",
		adminBase:  base + "/admin/realms/" + cfg.Realm,
	}, nil
}

// tokenRefreshSkew is the safety margin: we consider a cached token "about
// to expire" this far before its actual exp so we don't accidentally use a
// just-expired token mid-request.
const tokenRefreshSkew = 10 * time.Second

// acquireToken returns a cached service-account token if it's still fresh,
// otherwise mints a new one via client_credentials grant.
func (c *AdminClient) acquireToken(ctx context.Context) (string, error) {
	if t := c.token.Load(); t != nil && time.Until(t.expiresAt) > tokenRefreshSkew {
		return t.value, nil
	}

	body := url.Values{}
	body.Set("grant_type", "client_credentials")
	body.Set("client_id", c.cfg.ClientID)
	body.Set("client_secret", c.cfg.ClientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: token endpoint: %v", identity.ErrAdminAPIUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("%w: token endpoint HTTP %d: %s", identity.ErrAdminAPIUnavailable, resp.StatusCode, raw)
	}

	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("%w: decode token response: %v", identity.ErrAdminAPIUnavailable, err)
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("%w: empty access_token in response", identity.ErrAdminAPIUnavailable)
	}

	c.token.Store(&cachedToken{
		value:     payload.AccessToken,
		expiresAt: time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second),
	})
	return payload.AccessToken, nil
}

// doCreate is the variant of doJSON used for resource-creation calls where
// Keycloak signals the new resource id via the Location response header
// (e.g. POST /admin/realms/<realm>/users → Location: /admin/realms/<realm>/users/<uuid>).
//
// Returns the trailing path segment of the Location header (i.e. the new
// resource's id). The 201 body itself is empty in every Keycloak create
// flow we use; callers wanting the full representation should follow up
// with a GET using the returned id.
//
// All non-201 statuses are mapped to identity sentinels identically to
// doJSON. Empty Location header on a 201 returns ErrAdminAPIUnavailable —
// that's a Keycloak contract violation, not a caller problem.
func (c *AdminClient) doCreate(ctx context.Context, relPath string, body any) (string, error) {
	return c.doCreateOnce(ctx, relPath, body, true)
}

func (c *AdminClient) doCreateOnce(ctx context.Context, relPath string, body any, allowRetry bool) (string, error) {
	token, err := c.acquireToken(ctx)
	if err != nil {
		return "", err
	}

	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return "", fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.adminBase+relPath, reqBody)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", identity.ErrAdminAPIUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized && allowRetry {
		c.token.Store(nil)
		return c.doCreateOnce(ctx, relPath, body, false)
	}

	switch {
	case resp.StatusCode == http.StatusCreated:
		// expected
	case resp.StatusCode == http.StatusConflict:
		return "", identity.ErrConflict
	case resp.StatusCode == http.StatusBadRequest:
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("%w: %s", identity.ErrBadRequest, raw)
	case resp.StatusCode == http.StatusNotFound:
		return "", identity.ErrNotFound
	case resp.StatusCode == http.StatusForbidden:
		return "", identity.ErrForbidden
	case resp.StatusCode >= 500:
		return "", fmt.Errorf("%w: upstream HTTP %d", identity.ErrAdminAPIUnavailable, resp.StatusCode)
	default:
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("admin api create: HTTP %d: %s", resp.StatusCode, raw)
	}

	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("%w: 201 without Location header on POST %s", identity.ErrAdminAPIUnavailable, relPath)
	}
	// Trailing path segment is the new id.
	id := loc
	if i := strings.LastIndex(loc, "/"); i >= 0 && i < len(loc)-1 {
		id = loc[i+1:]
	}
	return id, nil
}

// doText fires a request with a plain-text body. Used for Keycloak's
// localization API which expects Content-Type: text/plain for individual key
// writes (PUT /localization/{locale}/{key}).
func (c *AdminClient) doText(ctx context.Context, method, relPath string, value string) error {
	return c.doTextOnce(ctx, method, relPath, value, true)
}

func (c *AdminClient) doTextOnce(ctx context.Context, method, relPath string, value string, allowRetry bool) error {
	token, err := c.acquireToken(ctx)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, method, c.adminBase+relPath, strings.NewReader(value))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "text/plain")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", identity.ErrAdminAPIUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized && allowRetry {
		c.token.Store(nil)
		return c.doTextOnce(ctx, method, relPath, value, false)
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return identity.ErrNotFound
	case resp.StatusCode == http.StatusForbidden:
		return identity.ErrForbidden
	case resp.StatusCode >= 500:
		return fmt.Errorf("%w: upstream HTTP %d", identity.ErrAdminAPIUnavailable, resp.StatusCode)
	case resp.StatusCode >= 400:
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("admin api: HTTP %d: %s", resp.StatusCode, raw)
	}
	return nil
}

// doJSON is the workhorse for every admin REST call: mint/reuse a token,
// fire the request, transparently retry once on 401 (covers cases where
// Keycloak rotated keys or invalidated the service-account session), then
// map HTTP status to identity sentinel errors.
func (c *AdminClient) doJSON(ctx context.Context, method, relPath string, query url.Values, body, out any) error {
	return c.doOnce(ctx, method, relPath, query, body, out, true)
}

func (c *AdminClient) doOnce(ctx context.Context, method, relPath string, query url.Values, body, out any, allowRetry bool) error {
	token, err := c.acquireToken(ctx)
	if err != nil {
		return err
	}

	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	u := c.adminBase + relPath
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", identity.ErrAdminAPIUnavailable, err)
	}
	defer resp.Body.Close()

	// On 401 the cached token is no longer valid (key rotation, session
	// terminated, etc.). Drop it and retry exactly once with a fresh one.
	if resp.StatusCode == http.StatusUnauthorized && allowRetry {
		c.token.Store(nil)
		return c.doOnce(ctx, method, relPath, query, body, out, false)
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return identity.ErrNotFound
	case resp.StatusCode == http.StatusForbidden:
		return identity.ErrForbidden
	case resp.StatusCode == http.StatusBadRequest:
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%w: %s", identity.ErrBadRequest, raw)
	case resp.StatusCode == http.StatusConflict:
		return identity.ErrConflict
	case resp.StatusCode >= 500:
		return fmt.Errorf("%w: upstream HTTP %d", identity.ErrAdminAPIUnavailable, resp.StatusCode)
	case resp.StatusCode >= 400:
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("admin api: HTTP %d: %s", resp.StatusCode, raw)
	}

	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%w: decode response: %v", identity.ErrAdminAPIUnavailable, err)
	}
	return nil
}
