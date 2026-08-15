package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"gorm.io/gorm"
)

// Request-id consistency across the whole /v1 chain.
//
// A correlation id is only worth having if it is on EVERY answer. One layer
// that refuses without it produces exactly the support conversation the id
// exists to prevent — "it failed at 14:03" against a log with nothing to match.
//
// The layers that can refuse a /v1 request, in the order they run:
//
//	requestid              (cannot refuse; it is what the others depend on)
//	RateLimitEdge          429
//	AuthenticatePrincipal  401, 503
//	RateLimitPerCredential 429
//	Authorize              403, 503
//	resolver / handler     4xx, 5xx
//
// The edge limiter is the one that was wrong before this slice: it answered
// with the legacy body, which has no request_id field at all, from a position
// where requestid.Middleware had already run. The id existed and was thrown
// away.

// v1ErrorLayer is one refusal, and which middleware produces it.
type v1ErrorLayer struct {
	layer  string
	build  func(t *testing.T) (*httptest.ResponseRecorder, string)
	status int
	code   string
}

// TestV1_EveryRefusingLayerCarriesTheRequestID walks each layer.
//
// Walking rather than spot-checking, because a correlation id added by hand to
// five middlewares is one a sixth will be missing — and the sixth is the one
// added under time pressure to fix an incident.
func TestV1_EveryRefusingLayerCarriesTheRequestID(t *testing.T) {
	layers := []v1ErrorLayer{
		{
			layer: "AuthenticatePrincipal — no credential",
			build: func(t *testing.T) (*httptest.ResponseRecorder, string) {
				r := rlRouter(t, RateLimitSettings{}, newMultiKeyAuth())
				return rlSend(r, "", "198.51.100.10", rlUsersPath(projTestWorkspace)), ""
			},
			status: http.StatusUnauthorized, code: "credential_invalid",
		},
		{
			layer: "AuthenticatePrincipal — unknown credential",
			build: func(t *testing.T) (*httptest.ResponseRecorder, string) {
				r := rlRouter(t, RateLimitSettings{}, newMultiKeyAuth())
				return rlSend(r, rlToken("nobody"), "198.51.100.11", rlUsersPath(projTestWorkspace)), ""
			},
			status: http.StatusUnauthorized, code: "credential_invalid",
		},
		{
			layer: "AuthenticatePrincipal — authenticator unavailable",
			build: func(t *testing.T) (*httptest.ResponseRecorder, string) {
				r := rlRouter(t, RateLimitSettings{}, &brokenProjectAuth{})
				return rlSend(r, rlToken("anykey"), "198.51.100.12", rlUsersPath(projTestWorkspace)), ""
			},
			status: http.StatusServiceUnavailable, code: "authorization_unavailable",
		},
		{
			layer: "RateLimitEdge — anonymous flood",
			build: func(t *testing.T) (*httptest.ResponseRecorder, string) {
				r := rlRouter(t, RateLimitSettings{EdgeRPS: 1}, newMultiKeyAuth())
				_, throttled := countUntilThrottled(r, "", "198.51.100.13", rlUsersPath(projTestWorkspace), 60)
				if throttled == nil {
					t.Fatal("the edge limiter never refused; the layer was not exercised")
				}
				return throttled, ""
			},
			status: http.StatusTooManyRequests, code: "rate_limit_exceeded",
		},
		{
			layer: "RateLimitPerCredential — credential over quota",
			build: func(t *testing.T) (*httptest.ResponseRecorder, string) {
				pa := newMultiKeyAuth(rlKey{
					token: rlToken("alpha"), credentialID: "key_alpha",
					workspace: projTestWorkspace, scopes: []string{"users:read"},
				})
				r := rlRouter(t, RateLimitSettings{EdgeRPS: 100, CredentialRPS: 1}, pa)
				_, throttled := countUntilThrottled(r, rlToken("alpha"), "198.51.100.14",
					rlUsersPath(projTestWorkspace), 60)
				if throttled == nil {
					t.Fatal("the credential limiter never refused; the layer was not exercised")
				}
				return throttled, ""
			},
			status: http.StatusTooManyRequests, code: "rate_limit_exceeded",
		},
		{
			layer: "Authorize — workspace mismatch",
			build: func(t *testing.T) (*httptest.ResponseRecorder, string) {
				pa := newMultiKeyAuth(rlKey{
					token: rlToken("alpha"), credentialID: "key_alpha",
					workspace: projTestWorkspace, scopes: []string{"users:read"},
				})
				r := rlRouter(t, RateLimitSettings{}, pa)
				return rlSend(r, rlToken("alpha"), "198.51.100.15", rlUsersPath(projTestOther)), ""
			},
			status: http.StatusForbidden, code: "workspace_mismatch",
		},
		{
			layer: "Authorize — insufficient scope",
			build: func(t *testing.T) (*httptest.ResponseRecorder, string) {
				pa := newMultiKeyAuth(rlKey{
					token: rlToken("alpha"), credentialID: "key_alpha",
					workspace: projTestWorkspace, scopes: []string{"roles:read"},
				})
				r := rlRouter(t, RateLimitSettings{}, pa)
				return rlSend(r, rlToken("alpha"), "198.51.100.16", rlUsersPath(projTestWorkspace)), ""
			},
			status: http.StatusForbidden, code: "insufficient_scope",
		},
		{
			layer: "Authorize — operator-only route",
			build: func(t *testing.T) (*httptest.ResponseRecorder, string) {
				pa := newMultiKeyAuth(rlKey{
					token: rlToken("alpha"), credentialID: "key_alpha",
					workspace: projTestWorkspace, scopes: []string{"users:read"},
				})
				r := rlRouter(t, RateLimitSettings{}, pa)
				return rlSend(r, rlToken("alpha"), "198.51.100.17", "/v1/workspaces"), ""
			},
			status: http.StatusForbidden, code: "operator_only",
		},
		{
			layer: "resolver — workspace has no active connection",
			build: func(t *testing.T) (*httptest.ResponseRecorder, string) {
				pa := newMultiKeyAuth(rlKey{
					token: rlToken("alpha"), credentialID: "key_alpha",
					workspace: projTestWorkspace, scopes: []string{"users:read"},
				})
				r := rlRouter(t, RateLimitSettings{}, pa)
				return rlSend(r, rlToken("alpha"), "198.51.100.18", rlUsersPath(projTestWorkspace)), ""
			},
			// The stub runtime has no connection wired, so this is the
			// resolver's refusal rather than the authorization chain's — which
			// is exactly the layer being checked.
			status: 0, code: "",
		},
	}

	for _, l := range layers {
		t.Run(l.layer, func(t *testing.T) {
			w, _ := l.build(t)

			if l.status != 0 && w.Code != l.status {
				t.Errorf("status = %d, want %d (body %s)", w.Code, l.status, w.Body.String())
			}
			if w.Code < 400 {
				t.Fatalf("layer did not refuse: status %d", w.Code)
			}

			var body struct {
				Error struct {
					Code      string `json:"code"`
					RequestID string `json:"request_id"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("response is not the /v1 envelope: %s", w.Body.String())
			}
			if l.code != "" && body.Error.Code != l.code {
				t.Errorf("error.code = %q, want %q", body.Error.Code, l.code)
			}
			if body.Error.Code == "" {
				t.Error("error.code is empty; an SDK has nothing to branch on")
			}
			if body.Error.RequestID == "" {
				t.Fatal("error.request_id is empty; this refusal cannot be correlated with a log line")
			}
			header := w.Header().Get(requestid.Header)
			if header == "" {
				t.Fatal("X-Request-Id header is absent")
			}
			if header != body.Error.RequestID {
				t.Errorf("X-Request-Id %q != envelope request_id %q — "+
					"an id that appears twice with two values is worse than none",
					header, body.Error.RequestID)
			}
		})
	}
}

// brokenProjectAuth is an authenticator that cannot decide. Its contract answer
// is 503, not 401: telling a correctly configured backend its credential is
// invalid during a database outage sends an operator rotating working keys.
type brokenProjectAuth struct{}

func (brokenProjectAuth) AuthenticateCredential(_ context.Context, _ string) (*auth.ProjectPrincipal, error) {
	return nil, errUnavailable{}
}

type errUnavailable struct{}

func (errUnavailable) Error() string { return "database unreachable" }

// TestV1_InboundRequestIDSurvivesTheWholeChain — a correlation id assigned
// upstream (a gateway, a calling service) must reach the error body, or the
// caller's id and this API's id describe the same request under two names.
func TestV1_InboundRequestIDSurvivesTheWholeChain(t *testing.T) {
	r := rlRouter(t, RateLimitSettings{}, newMultiKeyAuth())

	const upstream = "trace-abc123"
	req := httptest.NewRequest(http.MethodGet, rlUsersPath(projTestWorkspace), nil)
	req.Header.Set(requestid.Header, upstream)
	req.RemoteAddr = "198.51.100.20:1"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get(requestid.Header); got != upstream {
		t.Errorf("X-Request-Id = %q, want the caller's %q", got, upstream)
	}
	var body struct {
		Error struct {
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Error.RequestID != upstream {
		t.Errorf("envelope request_id = %q, want the caller's %q", body.Error.RequestID, upstream)
	}
}

// TestV1_SuccessAlsoCarriesTheRequestID — reads emit no audit event, so the
// echoed header is the ONLY correlation handle a successful M2M read has. It
// has to be there.
func TestV1_SuccessAlsoCarriesTheRequestID(t *testing.T) {
	r := newGin()
	SetupRouter(r, RouterDeps{
		User:      SetupUser(&gorm.DB{}),
		Provider:  &fakeProvider{id: adminIdentity("op")},
		Workspace: stubWorkspaceHandler(),
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)
	req.Header.Set("Authorization", "Bearer t")
	req.RemoteAddr = "198.51.100.30:1"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if w.Header().Get(requestid.Header) == "" {
		t.Error("a successful /v1 response has no X-Request-Id; the request cannot be correlated")
	}
}

// TestV1_NoSecretMaterialInAnyResponse walks the same layers again, looking for
// the values that must never come back out.
//
// A response is the widest possible disclosure surface: it crosses every proxy,
// CDN and browser devtools panel between here and the caller. The rule is not
// "do not log the key" but "the key never leaves the request it arrived on".
func TestV1_NoSecretMaterialInAnyResponse(t *testing.T) {
	const (
		token        = "lw_sk_zzzzsecretzzzzz_qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"
		credentialID = "key_11112222-3333-4444-5555-666677778888"
	)
	pa := newMultiKeyAuth(rlKey{
		token: token, credentialID: credentialID,
		workspace: projTestWorkspace, scopes: []string{"users:read"},
	})
	r := rlRouter(t, RateLimitSettings{EdgeRPS: 100, CredentialRPS: 100}, pa)

	// The lookup segment is not a secret, but it identifies the credential and
	// is enough to correlate a leaked log line to a live key, so it is held to
	// the same rule.
	lookup := "zzzzsecretzzzzz"
	forbidden := []string{token, lookup, credentialID}

	cases := []struct {
		name string
		send func() *httptest.ResponseRecorder
	}{
		{"authorized read", func() *httptest.ResponseRecorder {
			return rlSend(r, token, "198.51.100.40", rlUsersPath(projTestWorkspace))
		}},
		{"workspace mismatch", func() *httptest.ResponseRecorder {
			return rlSend(r, token, "198.51.100.41", rlUsersPath(projTestOther))
		}},
		{"operator-only route", func() *httptest.ResponseRecorder {
			return rlSend(r, token, "198.51.100.42", "/v1/workspaces")
		}},
		{"rejected credential", func() *httptest.ResponseRecorder {
			return rlSend(r, token+"x", "198.51.100.43", rlUsersPath(projTestWorkspace))
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := c.send()

			for _, secret := range forbidden {
				if strings.Contains(w.Body.String(), secret) {
					t.Errorf("response body contains %q", secret)
				}
				for name, values := range w.Header() {
					for _, v := range values {
						if strings.Contains(v, secret) {
							t.Errorf("response header %s contains %q", name, secret)
						}
					}
				}
			}
		})
	}
}
