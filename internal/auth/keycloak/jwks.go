package keycloak

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/MicahParks/jwkset"
	"github.com/MicahParks/keyfunc/v3"
	"golang.org/x/time/rate"
)

// JWKSOptions tunes the JWKS client used by the provider.
// All fields are optional; zero values fall back to sensible defaults.
type JWKSOptions struct {
	// RefreshInterval is the fixed JWKS poll interval.
	// Default: 1 hour.
	RefreshInterval time.Duration
	// HTTPClient lets callers inject an instrumented client (timeouts,
	// tracing, retry middleware). Default: http.Client{Timeout: 10s}.
	HTTPClient *http.Client
}

// newJWKS constructs a keyfunc backed by a Keycloak JWKS endpoint with three
// layers of cache behavior:
//
//  1. Blocking initial fetch — surfaces configuration / network errors at
//     startup, not at first request.
//  2. Scheduled refresh every RefreshInterval (default 1h) — picks up routine
//     key rotation.
//  3. On-demand refresh when a token presents an unknown "kid" — picks up
//     emergency key rotation immediately. Rate-limited (1 fetch / 30s, burst
//     of 2) so a flood of bogus kids can't turn the API into a JWKS DDoS
//     source.
func newJWKS(ctx context.Context, jwksURL string, opts JWKSOptions) (keyfunc.Keyfunc, error) {
	if opts.RefreshInterval == 0 {
		opts.RefreshInterval = time.Hour
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}

	storage, err := jwkset.NewStorageFromHTTP(jwksURL, jwkset.HTTPClientStorageOptions{
		Client:                    opts.HTTPClient,
		Ctx:                       ctx,
		RefreshInterval:           opts.RefreshInterval,
		NoErrorReturnFirstHTTPReq: false, // fail fast on startup
	})
	if err != nil {
		return nil, fmt.Errorf("jwks initial fetch (%s): %w", jwksURL, err)
	}

	// Multi-URL wrapper. RefreshUnknownKID lives here, not on the per-URL
	// storage options.
	httpClient, err := jwkset.NewHTTPClient(jwkset.HTTPClientOptions{
		HTTPURLs:          map[string]jwkset.Storage{jwksURL: storage},
		RateLimitWaitMax:  time.Minute,
		RefreshUnknownKID: rate.NewLimiter(rate.Every(30*time.Second), 2),
	})
	if err != nil {
		return nil, fmt.Errorf("jwks http client wrap: %w", err)
	}

	kf, err := keyfunc.New(keyfunc.Options{Storage: httpClient})
	if err != nil {
		return nil, fmt.Errorf("jwks keyfunc init: %w", err)
	}
	return kf, nil
}
