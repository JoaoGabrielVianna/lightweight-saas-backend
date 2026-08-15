package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// stubProvider is an AuthProvider whose behaviour a test controls, plus a
// record of whether it was consulted at all — which is how the discrimination
// property is proven rather than assumed.
type stubProvider struct {
	id     *Identity
	err    error
	calls  int
	tokens []string
}

func (s *stubProvider) ValidateToken(_ context.Context, raw string) (*Identity, error) {
	s.calls++
	s.tokens = append(s.tokens, raw)
	if s.err != nil {
		return nil, s.err
	}
	return s.id, nil
}

type stubProjects struct {
	principal *ProjectPrincipal
	err       error
	calls     int
	tokens    []string
}

func (s *stubProjects) AuthenticateCredential(_ context.Context, token string) (*ProjectPrincipal, error) {
	s.calls++
	s.tokens = append(s.tokens, token)
	return s.principal, s.err
}

func do(r *gin.Engine, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ─── Discrimination ─────────────────────────────────────────────────────────

// TestAuthenticate_ProjectKeyNeverReachesTheJWTParser is the property that
// makes the prefix test a discriminator rather than a fallback chain. Handing
// attacker-controlled input to a second parser "just in case" is how a cheap
// check becomes an expensive one and a timing signal.
func TestAuthenticate_ProjectKeyNeverReachesTheJWTParser(t *testing.T) {
	provider := &stubProvider{id: &Identity{Subject: "op"}}
	projects := &stubProjects{principal: &ProjectPrincipal{
		ProjectID: "prj_1", CredentialID: "key_1", WorkspaceID: "ws_1", Scopes: []string{"users:read"},
	}}

	var captured *Principal
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AuthenticatePrincipal(PrincipalConfig{Provider: provider, Projects: projects}))
	r.GET("/x", func(c *gin.Context) {
		captured, _ = PrincipalFrom(c)
		c.Status(http.StatusOK)
	})

	if w := do(r, "Bearer lw_sk_abcdefghijklmnop_"+
		"abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrst"); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	if provider.calls != 0 {
		t.Errorf("the OIDC provider was handed a project key (%d calls, %v)", provider.calls, provider.tokens)
	}
	if projects.calls != 1 {
		t.Errorf("project authenticator calls = %d, want 1", projects.calls)
	}
	if captured == nil || !captured.IsProject() {
		t.Fatal("no project principal was stored")
	}
}

// TestAuthenticate_JWTNeverReachesTheProjectAuthenticator is the mirror.
func TestAuthenticate_JWTNeverReachesTheProjectAuthenticator(t *testing.T) {
	provider := &stubProvider{id: &Identity{Subject: "op", Roles: []string{"admin"}}}
	projects := &stubProjects{}

	gin.SetMode(gin.TestMode)
	var captured *Principal
	r := gin.New()
	r.Use(AuthenticatePrincipal(PrincipalConfig{Provider: provider, Projects: projects}))
	r.GET("/x", func(c *gin.Context) {
		captured, _ = PrincipalFrom(c)
		c.Status(http.StatusOK)
	})

	if w := do(r, "Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJvcCJ9.sig"); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	if projects.calls != 0 {
		t.Errorf("the project authenticator was handed a JWT (%d calls)", projects.calls)
	}
	if provider.calls != 1 {
		t.Errorf("provider calls = %d, want 1", provider.calls)
	}
	if captured == nil || !captured.IsOperator() {
		t.Fatal("no operator principal was stored")
	}
}

// TestAuthenticate_ProjectDoesNotProduceAnIdentity is what keeps a machine out
// of every operator-shaped code path in the system. identity.Service's
// self-protection guards, the admin gates and the legacy audit actor all ask
// IdentityFrom; if a project answered, they would silently apply.
func TestAuthenticate_ProjectDoesNotProduceAnIdentity(t *testing.T) {
	projects := &stubProjects{principal: &ProjectPrincipal{
		ProjectID: "prj_1", CredentialID: "key_1", WorkspaceID: "ws_1",
	}}

	gin.SetMode(gin.TestMode)
	var hasIdentity bool
	r := gin.New()
	r.Use(AuthenticatePrincipal(PrincipalConfig{Provider: &stubProvider{}, Projects: projects}))
	r.GET("/x", func(c *gin.Context) {
		_, hasIdentity = IdentityFrom(c)
		c.Status(http.StatusOK)
	})

	do(r, "Bearer lw_sk_abcdefghijklmnop_abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrst")

	if hasIdentity {
		t.Fatal("a project credential produced an auth.Identity; every operator-shaped check " +
			"downstream would then treat it as a human")
	}
}

func TestAuthenticate_OperatorStillProducesAnIdentity(t *testing.T) {
	// The console and /admin/* depend on this, and RequireRole/RequireLiveAdmin
	// read it directly.
	provider := &stubProvider{id: &Identity{Subject: "op-1", Email: "op@example.test"}}

	gin.SetMode(gin.TestMode)
	var id *Identity
	r := gin.New()
	r.Use(AuthenticatePrincipal(PrincipalConfig{Provider: provider}))
	r.GET("/x", func(c *gin.Context) {
		id, _ = IdentityFrom(c)
		c.Status(http.StatusOK)
	})

	do(r, "Bearer some.jwt.value")

	if id == nil || id.Subject != "op-1" {
		t.Fatal("the operator identity was not stored")
	}
}

// ─── Failure responses ──────────────────────────────────────────────────────

// TestAuthenticate_EveryFailureIsTheSamePublicAnswer is the
// no-enumeration property at the HTTP layer.
func TestAuthenticate_EveryFailureIsTheSamePublicAnswer(t *testing.T) {
	const goodKey = "Bearer lw_sk_abcdefghijklmnop_abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrst"

	cases := map[string]struct {
		cfg    PrincipalConfig
		header string
	}{
		"no header": {
			PrincipalConfig{Provider: &stubProvider{}, Projects: &stubProjects{}}, "",
		},
		"not bearer": {
			PrincipalConfig{Provider: &stubProvider{}, Projects: &stubProjects{}}, "Basic abc",
		},
		"invalid operator token": {
			PrincipalConfig{Provider: &stubProvider{err: ErrInvalidToken}, Projects: &stubProjects{}},
			"Bearer some.jwt.value",
		},
		"rejected project credential": {
			PrincipalConfig{Provider: &stubProvider{}, Projects: &stubProjects{}}, goodKey,
		},
		"no project surface wired": {
			PrincipalConfig{Provider: &stubProvider{}}, goodKey,
		},
	}

	var first string
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(AuthenticatePrincipal(tc.cfg))
			r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

			w := do(r, tc.header)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
			if hdr := w.Header().Get("WWW-Authenticate"); hdr != `Bearer error="invalid_token"` {
				t.Errorf("WWW-Authenticate = %q", hdr)
			}
			// Bodies must be identical apart from the request id, which is
			// empty here because requestid middleware is not mounted.
			if first == "" {
				first = w.Body.String()
			} else if w.Body.String() != first {
				t.Errorf("body differs from the other failures:\n  %s\n  %s", w.Body.String(), first)
			}
		})
	}
}

// TestAuthenticate_InfrastructureFailureIs503 separates "your credential is
// bad" from "we could not check". The first sends an operator to rotate keys;
// only the second is true during an outage.
func TestAuthenticate_InfrastructureFailureIs503(t *testing.T) {
	projects := &stubProjects{err: errors.New("database unreachable")}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AuthenticatePrincipal(PrincipalConfig{Provider: &stubProvider{}, Projects: projects}))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := do(r, "Bearer lw_sk_abcdefghijklmnop_abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrst")

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestAuthenticate_UsesTheV1Envelope(t *testing.T) {
	// /v1 promises one error shape for every failure. Reusing RequireAuth here
	// would have produced the legacy {"error":"unauthorized"} instead.
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AuthenticatePrincipal(PrincipalConfig{Provider: &stubProvider{err: ErrInvalidToken}}))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := do(r, "Bearer bad.token.value")
	body := w.Body.String()

	for _, want := range []string{`"error"`, `"code"`, `"credential_invalid"`, `"message"`, `"request_id"`} {
		if !contains(body, want) {
			t.Errorf("body %s is missing %s", body, want)
		}
	}
}

// ─── Principal shape ────────────────────────────────────────────────────────

func TestPrincipal_UnionIsDiscriminated(t *testing.T) {
	op := NewOperatorPrincipal(&Identity{Subject: "s"})
	if !op.IsOperator() || op.IsProject() {
		t.Error("operator principal misreports its type")
	}
	if op.Project != nil {
		t.Error("operator principal carries a project")
	}

	pr := NewProjectPrincipal(&ProjectPrincipal{ProjectID: "prj_1"})
	if !pr.IsProject() || pr.IsOperator() {
		t.Error("project principal misreports its type")
	}
	if pr.Operator != nil {
		t.Error("project principal carries an operator identity")
	}

	var nilP *Principal
	if nilP.IsOperator() || nilP.IsProject() {
		t.Error("a nil principal must be neither")
	}
}

func TestPrincipalFrom_AbsentIsFalse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if _, ok := PrincipalFrom(c); ok {
		t.Error("PrincipalFrom reported a principal on a bare context")
	}
}
