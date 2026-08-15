package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/authz"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/project"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// These tests are about the wiring: which routes exist, which principal each
// admits, and that adding projects changed nothing for operators. Domain
// behaviour lives in internal/project's own tests.

const (
	projTestWorkspace = "ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	projTestOther     = "ws_bbbbbbbb-0000-4000-8000-000000000002"
	projTestProject   = "prj_7c9e6679-7425-40de-944b-e07fc1f90ae7"
	projTestUser      = "9c1e6679-7425-40de-944b-e07fc1f90ae7"

	// A syntactically valid credential token. It authenticates only because
	// stubProjectAuth is wired to accept it; nothing here talks to a database.
	projTestToken = "lw_sk_abcdefghijklmnop_abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrst"
)

// stubProjectAuth accepts exactly one token and returns a principal a test
// controls.
type stubProjectAuth struct {
	workspace string
	scopes    []string
	calls     int
}

func (s *stubProjectAuth) AuthenticateCredential(_ context.Context, token string) (*auth.ProjectPrincipal, error) {
	s.calls++
	if token != projTestToken {
		return nil, nil
	}
	return &auth.ProjectPrincipal{
		ProjectID:    projTestProject,
		ProjectName:  "Billing worker",
		CredentialID: "key_9b2f4c1a-1111-4222-8333-444455556666",
		WorkspaceID:  s.workspace,
		Scopes:       s.scopes,
	}, nil
}

// stubProjectHandler builds a project handler whose repository never touches a
// database. Every route it serves is operator-only, so these tests only need
// the routes to exist and the gate to run.
func stubProjectHandler() *project.Handler {
	return project.NewHandler(project.NewService(&stubProjectRepo{}, &stubProjectWorkspaces{}, stubTxRunner{}, stubAuditWriter{}))
}

type stubProjectRepo struct{}

func (stubProjectRepo) CreateProject(context.Context, *project.Project) error { return nil }
func (stubProjectRepo) GetProject(context.Context, string) (*project.Project, error) {
	return nil, nil
}
func (stubProjectRepo) ListProjects(context.Context, string) ([]project.Project, error) {
	return nil, nil
}
func (stubProjectRepo) UpdateProjectName(context.Context, string, string, time.Time) (*project.Project, error) {
	return nil, nil
}
func (stubProjectRepo) ArchiveProject(context.Context, string, time.Time) (*project.Project, error) {
	return nil, nil
}
func (stubProjectRepo) CreateCredential(context.Context, *project.Credential) error { return nil }
func (stubProjectRepo) ListCredentials(context.Context, string) ([]project.Credential, error) {
	return nil, nil
}
func (stubProjectRepo) GetCredential(context.Context, string) (*project.Credential, error) {
	return nil, nil
}
func (stubProjectRepo) CountActiveCredentials(context.Context, string) (int, error) { return 0, nil }
func (stubProjectRepo) CountActiveCredentialsByProject(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}
func (stubProjectRepo) RevokeCredential(context.Context, string, string, time.Time) (*project.Credential, error) {
	return nil, nil
}
func (stubProjectRepo) FindByKeyPrefix(context.Context, string) (*project.Credential, *project.Project, error) {
	return nil, nil, nil
}
func (stubProjectRepo) TouchLastUsed(context.Context, string, time.Time) error { return nil }
func (r stubProjectRepo) WithTx(database.Tx) project.Repository                { return r }

type stubProjectWorkspaces struct{}

func (stubProjectWorkspaces) GetByID(context.Context, string) (*workspace.Workspace, error) {
	return nil, nil
}

// projectRouter builds a full /v1 surface with the project domain wired.
func projectRouter(t *testing.T, projectAuth auth.ProjectAuthenticator, identity *auth.Identity) *gin.Engine {
	t.Helper()
	r := newGin()

	provider := &fakeProvider{id: identity}
	if identity == nil {
		provider = &fakeProvider{err: auth.ErrInvalidToken}
	}

	SetupRouter(r, RouterDeps{
		User:              SetupUser(&gorm.DB{}),
		Provider:          provider,
		AdminChecker:      &fakeAdminChecker{allow: true},
		Workspace:         stubWorkspaceHandler(),
		Project:           stubProjectHandler(),
		ProjectAuth:       projectAuth,
		WorkspaceIdentity: stubWorkspaceIdentityHandler(t),
	})
	return r
}

func keyRequest(r *gin.Engine, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not the /v1 envelope: %s", w.Body.String())
	}
	return body.Error.Code
}

// ---------------------------------------------------------------------------
// Mounting
// ---------------------------------------------------------------------------

// TestSetupRouter_ProjectRoutesAbsentWithoutTheDependency — a deployment that
// does not wire the project domain has no project surface at all: 404, not 403,
// so a probe cannot confirm the feature would exist with different config.
func TestSetupRouter_ProjectRoutesAbsentWithoutTheDependency(t *testing.T) {
	r := newGin()
	SetupRouter(r, RouterDeps{
		User:      SetupUser(&gorm.DB{}),
		Provider:  &fakeProvider{id: adminIdentity("op")},
		Workspace: stubWorkspaceHandler(),
	})

	w := v1Request(r, http.MethodGet, "/v1/workspaces/"+projTestWorkspace+"/projects", true)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when the project domain is not wired", w.Code)
	}
}

func TestSetupRouter_AllProjectRoutesRegistered(t *testing.T) {
	r := projectRouter(t, nil, adminIdentity("op"))

	want := []string{
		"GET /v1/project-scopes",
		"GET /v1/workspaces/:workspace_id/projects",
		"POST /v1/workspaces/:workspace_id/projects",
		"GET /v1/workspaces/:workspace_id/projects/:project_id",
		"PATCH /v1/workspaces/:workspace_id/projects/:project_id",
		"POST /v1/workspaces/:workspace_id/projects/:project_id/archive",
		"GET /v1/workspaces/:workspace_id/projects/:project_id/credentials",
		"POST /v1/workspaces/:workspace_id/projects/:project_id/credentials",
		"POST /v1/workspaces/:workspace_id/projects/:project_id/credentials/:credential_id/revoke",
	}

	got := map[string]bool{}
	for _, route := range r.Routes() {
		got[route.Method+" "+route.Path] = true
	}
	sort.Strings(want)
	for _, route := range want {
		if !got[route] {
			t.Errorf("route not registered: %s", route)
		}
	}

	// The two absent-by-design routes. A DELETE would leave dangling `prj_`
	// references in audit history; an endpoint returning a secret cannot exist
	// because the secret is not stored.
	for _, forbidden := range []string{
		"DELETE /v1/workspaces/:workspace_id/projects/:project_id",
		"GET /v1/workspaces/:workspace_id/projects/:project_id/credentials/:credential_id",
	} {
		if got[forbidden] {
			t.Errorf("route %s exists and must not", forbidden)
		}
	}
}

// TestSetupRouter_EveryV1RouteIsClassified is the boot-time guarantee in test
// form. The router panics on an unclassified route, so simply constructing it
// with every surface wired is the assertion.
func TestSetupRouter_EveryV1RouteIsClassified(t *testing.T) {
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("building the router panicked, which means a mounted /v1 route has no "+
				"authorization classification: %v", rec)
		}
	}()
	projectRouter(t, nil, adminIdentity("op"))
}

// ---------------------------------------------------------------------------
// Project credentials on the identity surface
// ---------------------------------------------------------------------------

func TestProjectCredential_ReachesItsOwnWorkspace(t *testing.T) {
	pa := &stubProjectAuth{workspace: projTestWorkspace, scopes: []string{"users:read"}}
	r := projectRouter(t, pa, nil)

	w := keyRequest(r, http.MethodGet, "/v1/workspaces/"+projTestWorkspace+"/users", projTestToken)

	// The stub identity handler answers 200; what matters is that the request
	// was not stopped by authentication or authorization.
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Fatalf("status = %d (%s), want the request to pass the gates", w.Code, w.Body.String())
	}
	if pa.calls != 1 {
		t.Errorf("project authenticator calls = %d, want 1", pa.calls)
	}
}

// TestProjectCredential_WrongWorkspaceIsRefused is the central property, at the
// router level: the request must not reach the identity handler, which is where
// the resolver, the connection and the provider live.
func TestProjectCredential_WrongWorkspaceIsRefused(t *testing.T) {
	pa := &stubProjectAuth{workspace: projTestWorkspace, scopes: []string{"users:read"}}
	handler := stubWorkspaceIdentityHandler(t)
	r := newGin()
	SetupRouter(r, RouterDeps{
		User:              SetupUser(&gorm.DB{}),
		Provider:          &fakeProvider{err: auth.ErrInvalidToken},
		Workspace:         stubWorkspaceHandler(),
		Project:           stubProjectHandler(),
		ProjectAuth:       pa,
		WorkspaceIdentity: handler,
	})

	w := keyRequest(r, http.MethodGet, "/v1/workspaces/"+projTestOther+"/users", projTestToken)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if got := errorCode(t, w); got != "workspace_mismatch" {
		t.Errorf("code = %q, want workspace_mismatch", got)
	}
}

func TestProjectCredential_MissingScopeIsRefused(t *testing.T) {
	pa := &stubProjectAuth{workspace: projTestWorkspace, scopes: []string{"users:read"}}
	r := projectRouter(t, pa, nil)

	w := keyRequest(r, http.MethodPost, "/v1/workspaces/"+projTestWorkspace+"/invitations", projTestToken)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if got := errorCode(t, w); got != "insufficient_scope" {
		t.Errorf("code = %q, want insufficient_scope", got)
	}
}

// TestProjectCredential_CannotManageTheControlPlane sweeps every control-plane
// route with a credential holding EVERY scope. Table-driven over the routes so
// a new control-plane route without a decision fails here.
func TestProjectCredential_CannotManageTheControlPlane(t *testing.T) {
	pa := &stubProjectAuth{workspace: projTestWorkspace, scopes: authz.ScopeStrings(authz.AllScopes())}
	r := projectRouter(t, pa, nil)

	ws := "/v1/workspaces/" + projTestWorkspace
	routes := [][2]string{
		// Workspace management
		{http.MethodGet, "/v1/workspaces"},
		{http.MethodPost, "/v1/workspaces"},
		{http.MethodGet, ws},
		{http.MethodPatch, ws},
		{http.MethodPost, ws + "/archive"},
		// Project management — a credential that could mint credentials would
		// make revocation meaningless.
		{http.MethodGet, ws + "/projects"},
		{http.MethodPost, ws + "/projects"},
		{http.MethodGet, ws + "/projects/" + projTestProject},
		{http.MethodPatch, ws + "/projects/" + projTestProject},
		{http.MethodPost, ws + "/projects/" + projTestProject + "/archive"},
		{http.MethodGet, ws + "/projects/" + projTestProject + "/credentials"},
		{http.MethodPost, ws + "/projects/" + projTestProject + "/credentials"},
		{http.MethodGet, "/v1/project-scopes"},
		// The direct password set.
		{http.MethodPut, ws + "/users/" + projTestUser + "/password"},
	}

	for _, route := range routes {
		t.Run(route[0]+" "+route[1], func(t *testing.T) {
			w := keyRequest(r, route[0], route[1], projTestToken)

			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", w.Code, w.Body.String())
			}
			if got := errorCode(t, w); got != "operator_only" {
				t.Errorf("code = %q, want operator_only", got)
			}
		})
	}
}

// TestProjectCredential_ConnectionRoutesAreOperatorOnly needs the connection
// surface wired, so it builds its own router.
func TestProjectCredential_ConnectionRoutesAreOperatorOnly(t *testing.T) {
	pa := &stubProjectAuth{workspace: projTestWorkspace, scopes: authz.ScopeStrings(authz.AllScopes())}

	r := newGin()
	SetupRouter(r, RouterDeps{
		User:        SetupUser(&gorm.DB{}),
		Provider:    &fakeProvider{err: auth.ErrInvalidToken},
		Workspace:   stubWorkspaceHandler(),
		Connection:  stubConnectionHandler(t),
		Project:     stubProjectHandler(),
		ProjectAuth: pa,
	})

	ws := "/v1/workspaces/" + projTestWorkspace
	conn := ws + "/connections/conn_7c9e6679-7425-40de-944b-e07fc1f90ae7"
	for _, route := range [][2]string{
		{http.MethodGet, ws + "/connections"},
		{http.MethodPost, ws + "/connections"},
		{http.MethodGet, conn},
		{http.MethodPatch, conn},
		{http.MethodDelete, conn},
		{http.MethodPost, conn + "/verify"},
		{http.MethodPost, conn + "/activate"},
		{http.MethodPost, conn + "/retire"},
	} {
		t.Run(route[0]+" "+route[1], func(t *testing.T) {
			w := keyRequest(r, route[0], route[1], projTestToken)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", w.Code, w.Body.String())
			}
			if got := errorCode(t, w); got != "operator_only" {
				t.Errorf("code = %q, want operator_only", got)
			}
		})
	}
}

// TestProjectCredential_NeverAcceptedOnAdminSurface — /admin/* runs RequireAuth
// alone and has no notion of principals. A project key there must be rejected
// as an invalid bearer token, never authenticated.
func TestProjectCredential_NeverAcceptedOnAdminSurface(t *testing.T) {
	pa := &stubProjectAuth{workspace: projTestWorkspace, scopes: authz.ScopeStrings(authz.AllScopes())}

	identityHandler, adminChecker, _, err := SetupIdentity(legacyCfg(t))
	if err != nil {
		t.Fatalf("setup identity: %v", err)
	}

	r := newGin()
	SetupRouter(r, RouterDeps{
		User:         SetupUser(&gorm.DB{}),
		Identity:     identityHandler,
		Provider:     &fakeProvider{err: auth.ErrInvalidToken},
		AdminChecker: adminChecker,
		Workspace:    stubWorkspaceHandler(),
		Project:      stubProjectHandler(),
		ProjectAuth:  pa,
	})

	w := keyRequest(r, http.MethodGet, "/admin/users", projTestToken)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — /admin/* must never accept a project credential", w.Code)
	}
	if pa.calls != 0 {
		t.Errorf("the project authenticator was consulted for an /admin/* request (%d calls)", pa.calls)
	}
	// And the legacy body shape is preserved byte-for-byte.
	if got := strings.TrimSpace(w.Body.String()); got != `{"error":"unauthorized"}` {
		t.Errorf("body = %s, want the legacy /admin envelope", got)
	}
}

// ---------------------------------------------------------------------------
// Operator regression
// ---------------------------------------------------------------------------

// TestOperator_UnaffectedByProjectWiring — the console's path must be
// unchanged. Same routes, same chain, same outcomes.
func TestOperator_UnaffectedByProjectWiring(t *testing.T) {
	r := projectRouter(t, &stubProjectAuth{workspace: projTestWorkspace}, adminIdentity("op"))

	ws := "/v1/workspaces/" + projTestWorkspace
	for _, route := range [][2]string{
		{http.MethodGet, "/v1/workspaces"},
		{http.MethodGet, ws + "/projects"},
		{http.MethodGet, "/v1/project-scopes"},
		{http.MethodGet, ws + "/users"},
		{http.MethodPut, ws + "/users/" + projTestUser + "/password"},
	} {
		t.Run(route[0]+" "+route[1], func(t *testing.T) {
			w := v1Request(r, route[0], route[1], true)
			if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
				t.Errorf("status = %d — an operator was denied a route they could always reach: %s",
					w.Code, w.Body.String())
			}
		})
	}
}

func TestOperator_StillNeedsTheAdminRoleAfterTheSplit(t *testing.T) {
	// The authorization rules moved into authz.Authorize; they must not have
	// been softened on the way.
	r := projectRouter(t, nil, &auth.Identity{Subject: "viewer", Roles: []string{"viewer"}})

	w := v1Request(r, http.MethodGet, "/v1/workspaces", true)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a token without the admin role", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Rate limiting
// ---------------------------------------------------------------------------

// TestRateLimitPerCredential_UsesTheV1Envelope — the first programmatic client
// of this API will be an SDK, and two error shapes for one status on one
// endpoint is what makes a client library ugly forever.
func TestRateLimitPerCredential_UsesTheV1Envelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		auth.StorePrincipal(c, auth.NewProjectPrincipal(&auth.ProjectPrincipal{
			ProjectID: projTestProject, CredentialID: "key_1", WorkspaceID: projTestWorkspace,
		}))
		c.Next()
	})
	// rate 1/s burst 1: the second request in the same instant is refused.
	r.Use(RateLimitPerCredential(1, 1))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	first := httptest.NewRecorder()
	r.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/x", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", first.Code)
	}

	second := httptest.NewRecorder()
	r.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/x", nil))

	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", second.Code)
	}
	if got := errorCode(t, second); got != "rate_limit_exceeded" {
		t.Errorf("code = %q, want rate_limit_exceeded", got)
	}
	if second.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After header on a 429")
	}
	if second.Header().Get("RateLimit-Limit") == "" {
		t.Error("no RateLimit-Limit header on a 429")
	}
}

// TestRateLimitPerCredential_KeyedByCredentialNotProject — revoking the key
// that is flooding must fix the flood. Keyed by project, a runaway deployment
// would keep throttling its well-behaved siblings.
func TestRateLimitPerCredential_KeyedByCredentialNotProject(t *testing.T) {
	gin.SetMode(gin.TestMode)

	request := func(credentialID string, mw gin.HandlerFunc) int {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			auth.StorePrincipal(c, auth.NewProjectPrincipal(&auth.ProjectPrincipal{
				ProjectID: projTestProject, CredentialID: credentialID, WorkspaceID: projTestWorkspace,
			}))
			c.Next()
		})
		r.Use(mw)
		r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		return w.Code
	}

	mw := RateLimitPerCredential(1, 1)
	if code := request("key_a", mw); code != http.StatusOK {
		t.Fatalf("key_a first request = %d, want 200", code)
	}
	if code := request("key_a", mw); code != http.StatusTooManyRequests {
		t.Fatalf("key_a second request = %d, want 429", code)
	}
	// Same project, different credential: unaffected.
	if code := request("key_b", mw); code != http.StatusOK {
		t.Errorf("key_b = %d, want 200 — the bucket must be per credential", code)
	}
}

// TestRateLimitPerCredential_IgnoresOperators — operators keep the per-IP limit
// they have always had; adding a per-operator bucket would change console
// behaviour to solve a problem nobody has.
func TestRateLimitPerCredential_IgnoresOperators(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		auth.StorePrincipal(c, auth.NewOperatorPrincipal(adminIdentity("op")))
		c.Next()
	})
	r.Use(RateLimitPerCredential(1, 1))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("operator request %d = %d, want 200", i, w.Code)
		}
	}
}

// ─── Transactional stubs (Slice 15) ─────────────────────────────────────────
//
// These router tests are about MOUNTING and AUTHORIZATION, not persistence.
// They need a Service that constructs, not one that commits: stubTxRunner runs
// the callback with no transaction and stubAuditWriter accepts every event.
//
// The atomicity these stand in for is proven in internal/*/…_integration_test.go
// against a real database. A router test that appeared to prove it would be the
// worst of both.
type stubTxRunner struct{}

func (stubTxRunner) InTx(_ context.Context, fn func(tx database.Tx) error) error { return fn(nil) }

type stubAuditWriter struct{}

func (stubAuditWriter) RecordTx(context.Context, database.Tx, audit.Event) error { return nil }
