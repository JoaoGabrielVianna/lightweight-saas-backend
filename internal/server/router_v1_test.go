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

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auditlog"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/connection"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// stubWorkspaceHandler builds a workspace handler over a repository that never
// touches a database.
//
// These tests are about the middleware chain and route table, not about
// workspace behaviour (that lives in internal/workspace's own tests). What
// matters is that a request either reaches the handler or is stopped by a
// gate, which is observable from the status code alone.
func stubWorkspaceHandler() *workspace.Handler {
	return workspace.NewHandler(workspace.NewService(&stubWorkspaceRepo{}, stubTxRunner{}, stubAuditWriter{}))
}

type stubWorkspaceRepo struct{}

func (s *stubWorkspaceRepo) Create(_ context.Context, _ *workspace.Workspace) error { return nil }
func (s *stubWorkspaceRepo) GetByID(_ context.Context, _ string) (*workspace.Workspace, error) {
	return nil, nil
}
func (s *stubWorkspaceRepo) List(_ context.Context, _ workspace.StatusFilter) ([]workspace.Workspace, error) {
	return nil, nil
}
func (s *stubWorkspaceRepo) UpdateName(_ context.Context, _, _ string, _ time.Time) (*workspace.Workspace, error) {
	return nil, nil
}
func (s *stubWorkspaceRepo) WithTx(database.Tx) workspace.Repository { return s }

func (s *stubWorkspaceRepo) Archive(_ context.Context, _ string, _ time.Time) (*workspace.Workspace, error) {
	return nil, nil
}

// adminIdentity returns a token identity carrying the realm admin role.
func adminIdentity(subject string) *auth.Identity {
	return &auth.Identity{Subject: subject, Roles: []string{"admin"}, ExpiresAt: time.Now().Add(time.Hour)}
}

// v1Request issues a request against /v1 and returns the recorder.
func v1Request(r *gin.Engine, method, path string, withToken bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(""))
	if withToken {
		req.Header.Set("Authorization", "Bearer t")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ---------------------------------------------------------------------------
// Mounting
// ---------------------------------------------------------------------------

// TestSetupRouter_V1AbsentWithoutOption pins that /v1 is opt-in. A deployment
// that does not wire the workspace domain has no /v1 surface at all — 404,
// not 401, so an unauthenticated probe cannot confirm the feature exists.
func TestSetupRouter_V1AbsentWithoutOption(t *testing.T) {
	r := newGin()
	provider := &fakeProvider{id: adminIdentity("s1")}
	SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider})

	if w := v1Request(r, http.MethodGet, "/v1/workspaces", true); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when the workspace option is not passed", w.Code)
	}
}

// TestSetupRouter_WithWorkspacesNilIsNoOp — passing a nil handler must behave
// exactly like passing no option, not panic.
func TestSetupRouter_WithWorkspacesNilIsNoOp(t *testing.T) {
	r := newGin()
	provider := &fakeProvider{id: adminIdentity("s1")}
	SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider})

	if w := v1Request(r, http.MethodGet, "/v1/workspaces", true); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a nil workspace handler", w.Code)
	}
}

// TestSetupRouter_AllV1RoutesRegistered pins the route table. A route silently
// missing from the group would be a 404 that looks like a client mistake.
func TestSetupRouter_AllV1RoutesRegistered(t *testing.T) {
	r := newGin()
	provider := &fakeProvider{id: adminIdentity("s1")}
	SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider, Workspace: stubWorkspaceHandler()})

	want := []string{
		"GET /v1/workspaces",
		"POST /v1/workspaces",
		"GET /v1/workspaces/:workspace_id",
		"PATCH /v1/workspaces/:workspace_id",
		"POST /v1/workspaces/:workspace_id/archive",
	}

	got := map[string]bool{}
	for _, route := range r.Routes() {
		got[route.Method+" "+route.Path] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("route %q is not registered", w)
		}
	}

	// And nothing else under /v1 — no accidental extra surface.
	var v1Routes []string
	for _, route := range r.Routes() {
		if strings.HasPrefix(route.Path, "/v1") {
			v1Routes = append(v1Routes, route.Method+" "+route.Path)
		}
	}
	if len(v1Routes) != len(want) {
		sort.Strings(v1Routes)
		t.Errorf("/v1 has %d routes, want exactly %d: %v", len(v1Routes), len(want), v1Routes)
	}
}

// ---------------------------------------------------------------------------
// The protection chain — identical to /admin/*
// ---------------------------------------------------------------------------

// TestSetupRouter_V1RequiresAuth — no token, no access, on every route.
func TestSetupRouter_V1RequiresAuth(t *testing.T) {
	r := newGin()
	provider := &fakeProvider{err: auth.ErrInvalidToken}
	SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider, Workspace: stubWorkspaceHandler()})

	routes := [][2]string{
		{http.MethodGet, "/v1/workspaces"},
		{http.MethodPost, "/v1/workspaces"},
		{http.MethodGet, "/v1/workspaces/ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301"},
		{http.MethodPatch, "/v1/workspaces/ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301"},
		{http.MethodPost, "/v1/workspaces/ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301/archive"},
	}

	for _, route := range routes {
		t.Run(route[0]+" "+route[1], func(t *testing.T) {
			if w := v1Request(r, route[0], route[1], false); w.Code != http.StatusUnauthorized {
				t.Errorf("without a token: status = %d, want 401", w.Code)
			}
		})
	}
}

// TestSetupRouter_V1RequiresAdminRole — a valid token without the realm admin
// role is denied, and the expensive live check never runs.
func TestSetupRouter_V1RequiresAdminRole(t *testing.T) {
	r := newGin()
	provider := &fakeProvider{id: &auth.Identity{
		Subject: "viewer-1", Roles: []string{"viewer"}, ExpiresAt: time.Now().Add(time.Hour),
	}}
	checker := &fakeAdminChecker{allow: true}
	SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider, AdminChecker: checker, Workspace: stubWorkspaceHandler()})

	w := v1Request(r, http.MethodGet, "/v1/workspaces", true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (RequireRole denial)", w.Code)
	}
	if checker.calls != 0 {
		t.Errorf("RequireLiveAdmin ran after a RequireRole denial (%d calls); the cheap gate must short-circuit", checker.calls)
	}
}

// TestSetupRouter_V1EnforcesLiveAdmin — this is the GAP-1 property. A token
// whose claim says admin but whose role has since been revoked must be denied,
// which is the entire reason RequireLiveAdmin exists on /admin/*.
func TestSetupRouter_V1EnforcesLiveAdmin(t *testing.T) {
	r := newGin()
	provider := &fakeProvider{id: adminIdentity("stale-admin")}
	checker := &fakeAdminChecker{allow: false}
	SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider, AdminChecker: checker, Workspace: stubWorkspaceHandler()})

	w := v1Request(r, http.MethodGet, "/v1/workspaces", true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (live-admin denial)", w.Code)
	}
	if checker.calls != 1 || checker.lastSubject != "stale-admin" {
		t.Errorf("checker calls=%d subject=%q, want 1 call for stale-admin", checker.calls, checker.lastSubject)
	}
}

// TestSetupRouter_V1ReachesHandlerWhenFullyAuthorized closes the chain: a live
// admin gets through to the handler.
func TestSetupRouter_V1ReachesHandlerWhenFullyAuthorized(t *testing.T) {
	r := newGin()
	provider := &fakeProvider{id: adminIdentity("real-admin")}
	checker := &fakeAdminChecker{allow: true}
	SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider, AdminChecker: checker, Workspace: stubWorkspaceHandler()})

	w := v1Request(r, http.MethodGet, "/v1/workspaces", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the chain should have passed", w.Code)
	}
	if checker.calls != 1 {
		t.Errorf("live-admin checker calls = %d, want 1", checker.calls)
	}
}

// TestSetupRouter_V1SkipsLiveCheckWhenCheckerNil mirrors the admin group's
// conditional exactly: no identity provider means nothing to ask.
func TestSetupRouter_V1SkipsLiveCheckWhenCheckerNil(t *testing.T) {
	r := newGin()
	provider := &fakeProvider{id: adminIdentity("s1")}
	SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider, Workspace: stubWorkspaceHandler()})

	if w := v1Request(r, http.MethodGet, "/v1/workspaces", true); w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 with no live-admin checker wired", w.Code)
	}
}

// TestSetupRouter_V1EmitsRequestID pins that the correlation header is present
// on the /v1 surface.
func TestSetupRouter_V1EmitsRequestID(t *testing.T) {
	r := newGin()
	provider := &fakeProvider{id: adminIdentity("s1")}
	SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider, Workspace: stubWorkspaceHandler()})

	w := v1Request(r, http.MethodGet, "/v1/workspaces", true)
	if w.Header().Get(requestid.Header) == "" {
		t.Error("/v1 response carries no X-Request-Id header")
	}
}

// ---------------------------------------------------------------------------
// /admin/* compatibility
// ---------------------------------------------------------------------------

// TestAdminSurfaceUnchangedByV1 is the compatibility guarantee this slice is
// held to: mounting /v1 must not alter /admin/* in any observable way.
//
// It builds the router twice from the same inputs — once without the option,
// once with it — and compares the full /admin route table plus the response
// (status, headers, body) of a representative request.
func TestAdminSurfaceUnchangedByV1(t *testing.T) {
	stubURL := keycloakStub(t)
	cfg := &config.Config{
		KeycloakURL:               stubURL,
		KeycloakRealm:             "saas",
		KeycloakClientID:          "saas-api",
		KeycloakAdminClientID:     "kc-admin",
		KeycloakAdminClientSecret: "shh",
		AdminLiveCheckTTLSeconds:  5,
	}

	build := func(withV1 bool) *gin.Engine {
		identityHandler, _, _, err := SetupIdentity(cfg)
		if err != nil {
			t.Fatalf("SetupIdentity: %v", err)
		}
		r := newGin()
		provider := &fakeProvider{id: adminIdentity("admin-1")}
		checker := &fakeAdminChecker{allow: true}

		deps := RouterDeps{
			User:         SetupUser(&gorm.DB{}),
			Identity:     identityHandler,
			Provider:     provider,
			AdminChecker: checker,
		}
		if withV1 {
			deps.Workspace = stubWorkspaceHandler()
		}
		SetupRouter(r, deps)
		return r
	}

	adminRoutes := func(r *gin.Engine) []string {
		var out []string
		for _, route := range r.Routes() {
			if strings.HasPrefix(route.Path, "/admin") {
				out = append(out, route.Method+" "+route.Path)
			}
		}
		sort.Strings(out)
		return out
	}

	without, with := build(false), build(true)

	before, after := adminRoutes(without), adminRoutes(with)
	if len(before) == 0 {
		t.Fatal("no /admin routes registered — the fixture is wrong, not the code")
	}
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("/admin route table changed when /v1 was mounted:\nbefore:\n%s\nafter:\n%s",
			strings.Join(before, "\n"), strings.Join(after, "\n"))
	}

	// A representative /admin request must produce an identical response.
	call := func(r *gin.Engine) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
		req.Header.Set("Authorization", "Bearer t")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	a, b := call(without), call(with)

	if a.Code != b.Code {
		t.Errorf("/admin/users status changed: %d → %d", a.Code, b.Code)
	}
	if a.Body.String() != b.Body.String() {
		t.Errorf("/admin/users body changed:\n%s\n→\n%s", a.Body.String(), b.Body.String())
	}

	// Specifically: the request-id middleware must NOT have leaked onto
	// /admin, which is why it is mounted on the /v1 group rather than
	// globally.
	if b.Header().Get(requestid.Header) != "" {
		t.Errorf("/admin/users grew an %s header; the middleware must stay scoped to /v1", requestid.Header)
	}
	if len(a.Header()) != len(b.Header()) {
		t.Errorf("/admin/users header count changed: %v → %v", a.Header(), b.Header())
	}
}

// TestSetupRouter_V1DoesNotShadowExistingSurfaces guards against the /v1 group
// claiming a path another surface owns.
func TestSetupRouter_V1DoesNotShadowExistingSurfaces(t *testing.T) {
	r := newGin()
	provider := &fakeProvider{id: adminIdentity("s1")}
	SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider, Workspace: stubWorkspaceHandler()})

	for _, route := range r.Routes() {
		if strings.HasPrefix(route.Path, "/v1") {
			continue
		}
		if strings.Contains(route.Path, "workspace") {
			t.Errorf("route %s %s mentions workspaces outside /v1", route.Method, route.Path)
		}
	}

	// /me is the pre-existing private route. It must still be registered, and
	// must not have acquired the /v1 chain.
	//
	// Checked via the route table and an unauthenticated request rather than
	// an authenticated one: with a valid token the handler would run against
	// the zero-value *gorm.DB these router tests use, which panics inside
	// GORM. What is under test here is routing, not the user handler.
	var hasMe bool
	for _, route := range r.Routes() {
		if route.Method == http.MethodGet && route.Path == "/me" {
			hasMe = true
		}
	}
	if !hasMe {
		t.Error("/me disappeared after mounting /v1")
	}

	w := v1Request(r, http.MethodGet, "/me", false)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("/me without a token = %d, want 401 — its gating must be unchanged", w.Code)
	}
	if w.Header().Get(requestid.Header) != "" {
		t.Error("/me grew an X-Request-Id header; the middleware must stay scoped to /v1")
	}
}

// TestSetupRouter_V1ErrorEnvelopeShape pins that a /v1 error reaching the
// client through the real router carries the documented envelope, request id
// included — the handler tests cover the codes, this covers the wiring.
func TestSetupRouter_V1ErrorEnvelopeShape(t *testing.T) {
	r := newGin()
	provider := &fakeProvider{id: adminIdentity("s1")}
	SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider, Workspace: stubWorkspaceHandler()})

	w := v1Request(r, http.MethodGet, "/v1/workspaces?status=bogus", true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}

	var body struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON %q: %v", w.Body.String(), err)
	}
	if body.Error.Code != "invalid_status_filter" {
		t.Errorf("code = %q, want invalid_status_filter", body.Error.Code)
	}
	if body.Error.RequestID == "" || body.Error.RequestID != w.Header().Get(requestid.Header) {
		t.Errorf("request_id %q does not match the %s header %q",
			body.Error.RequestID, requestid.Header, w.Header().Get(requestid.Header))
	}
}

// ---------------------------------------------------------------------------
// /v1 connections
// ---------------------------------------------------------------------------

// stubConnectionHandler builds a connection handler over collaborators that
// never touch a database or a provider. These tests are about the route table
// and the middleware chain; connection behaviour lives in internal/connection.
func stubConnectionHandler(t *testing.T) *connection.Handler {
	t.Helper()

	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyring, err := secrets.NewSingleVersionKeyring(1, key)
	if err != nil {
		t.Fatalf("new keyring: %v", err)
	}

	svc := connection.NewService(&stubConnectionRepo{}, &stubWorkspaceStore{}, keyring, &stubVerifier{}, stubTxRunner{}, stubAuditWriter{})
	if svc == nil {
		t.Fatal("connection.NewService returned nil with every collaborator present")
	}
	return connection.NewHandler(svc)
}

type stubConnectionRepo struct{}

func (s *stubConnectionRepo) Create(context.Context, *connection.Connection, secrets.Sealed) error {
	return nil
}
func (s *stubConnectionRepo) GetByID(context.Context, string) (*connection.Connection, error) {
	return nil, nil
}
func (s *stubConnectionRepo) List(context.Context, string, connection.StatusFilter) ([]connection.Connection, error) {
	return nil, nil
}
func (s *stubConnectionRepo) OpenSecret(context.Context, string) (*secrets.Sealed, error) {
	return nil, nil
}
func (s *stubConnectionRepo) UpdateConfig(context.Context, string, connection.ConfigPatch, *secrets.Sealed, time.Time) (*connection.Connection, error) {
	return nil, nil
}
func (s *stubConnectionRepo) SaveVerification(context.Context, string, connection.VerifyReport) (*connection.Connection, error) {
	return nil, nil
}
func (s *stubConnectionRepo) Activate(context.Context, string, string, time.Time) (*connection.Connection, error) {
	return nil, nil
}
func (s *stubConnectionRepo) Retire(context.Context, string, time.Time) (*connection.Connection, error) {
	return nil, nil
}
func (s *stubConnectionRepo) WithTx(database.Tx) connection.Repository { return s }

func (s *stubConnectionRepo) Delete(context.Context, string) error { return nil }

// stubWorkspaceStore reports every workspace as present and active, so a
// request reaches the connection layer instead of stopping at the lookup.
type stubWorkspaceStore struct{}

func (s *stubWorkspaceStore) GetByID(_ context.Context, id string) (*workspace.Workspace, error) {
	return &workspace.Workspace{ID: id, Slug: "s", Name: "n", Status: workspace.StatusActive}, nil
}

type stubVerifier struct{}

func (s *stubVerifier) Verify(context.Context, connection.VerifyTarget) connection.VerifyReport {
	return connection.VerifyReport{}
}

const (
	testWorkspacePath  = "/v1/workspaces/ws_5a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d"
	testConnectionPath = testWorkspacePath + "/connections/conn_3f2504e0-4f89-41d3-9a0c-0305e82c3301"
)

// TestSetupRouter_ConnectionRoutesAbsentWithoutOption pins that the connection
// surface is opt-in. An installation with no SECRETS_MASTER_KEY has no
// connection routes at all — 404, not 401, so an unauthenticated probe cannot
// confirm the feature would exist with different config.
func TestSetupRouter_ConnectionRoutesAbsentWithoutOption(t *testing.T) {
	r := newGin()
	provider := &fakeProvider{id: adminIdentity("s1")}
	SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider, Workspace: stubWorkspaceHandler()})

	for _, path := range []string{
		testWorkspacePath + "/connections",
		testConnectionPath,
	} {
		if w := v1Request(r, http.MethodGet, path, true); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 without the connections option", path, w.Code)
		}
	}
}

func TestSetupRouter_WithConnectionsNilIsNoOp(t *testing.T) {
	r := newGin()
	provider := &fakeProvider{id: adminIdentity("s1")}
	SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider, Workspace: stubWorkspaceHandler()})

	if w := v1Request(r, http.MethodGet, testWorkspacePath+"/connections", true); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a nil connection handler", w.Code)
	}
}

// TestSetupRouter_ConnectionRoutesRequireWorkspaceHandler documents the nesting:
// connections live under the workspace surface, so they are not mounted without
// it. That keeps /v1/workspaces/... from existing in half-form.
func TestSetupRouter_ConnectionRoutesRequireWorkspaceHandler(t *testing.T) {
	r := newGin()
	provider := &fakeProvider{id: adminIdentity("s1")}
	SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider, Connection: stubConnectionHandler(t)})

	if w := v1Request(r, http.MethodGet, testWorkspacePath+"/connections", true); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 — connections are nested under the workspace surface", w.Code)
	}
}

// TestSetupRouter_AllConnectionRoutesRegistered pins the route table.
func TestSetupRouter_AllConnectionRoutesRegistered(t *testing.T) {
	r := newGin()
	provider := &fakeProvider{id: adminIdentity("s1")}
	SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider, Workspace: stubWorkspaceHandler(), Connection: stubConnectionHandler(t)})

	want := []string{
		"GET /v1/workspaces/:workspace_id/connections",
		"POST /v1/workspaces/:workspace_id/connections",
		"GET /v1/workspaces/:workspace_id/connections/:connection_id",
		"PATCH /v1/workspaces/:workspace_id/connections/:connection_id",
		"DELETE /v1/workspaces/:workspace_id/connections/:connection_id",
		"POST /v1/workspaces/:workspace_id/connections/:connection_id/verify",
		"POST /v1/workspaces/:workspace_id/connections/:connection_id/activate",
		"POST /v1/workspaces/:workspace_id/connections/:connection_id/retire",
	}

	got := map[string]bool{}
	for _, route := range r.Routes() {
		got[route.Method+" "+route.Path] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("route %q is not registered", w)
		}
	}
}

// TestSetupRouter_ConnectionRoutesCarryTheAdminChain is the security property:
// the connection surface holds provider credentials, so it must be gated
// exactly as /admin/* is — no weaker, and not by a second copy of the rule.
func TestSetupRouter_ConnectionRoutesCarryTheAdminChain(t *testing.T) {
	routes := [][2]string{
		{http.MethodGet, testWorkspacePath + "/connections"},
		{http.MethodPost, testWorkspacePath + "/connections"},
		{http.MethodGet, testConnectionPath},
		{http.MethodPatch, testConnectionPath},
		{http.MethodDelete, testConnectionPath},
		{http.MethodPost, testConnectionPath + "/verify"},
		{http.MethodPost, testConnectionPath + "/activate"},
		{http.MethodPost, testConnectionPath + "/retire"},
	}

	t.Run("no token is 401 on every route", func(t *testing.T) {
		r := newGin()
		provider := &fakeProvider{err: auth.ErrInvalidToken}
		SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider, Workspace: stubWorkspaceHandler(), Connection: stubConnectionHandler(t)})

		for _, route := range routes {
			if w := v1Request(r, route[0], route[1], false); w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s without a token = %d, want 401", route[0], route[1], w.Code)
			}
		}
	})

	t.Run("non-admin is 403 on every route", func(t *testing.T) {
		r := newGin()
		provider := &fakeProvider{id: &auth.Identity{
			Subject: "viewer", Roles: []string{"viewer"}, ExpiresAt: time.Now().Add(time.Hour),
		}}
		checker := &fakeAdminChecker{allow: true}
		SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider, AdminChecker: checker, Workspace: stubWorkspaceHandler(), Connection: stubConnectionHandler(t)})

		for _, route := range routes {
			if w := v1Request(r, route[0], route[1], true); w.Code != http.StatusForbidden {
				t.Errorf("%s %s as a non-admin = %d, want 403", route[0], route[1], w.Code)
			}
		}
		if checker.calls != 0 {
			t.Errorf("the live-admin check ran %d times after a RequireRole denial", checker.calls)
		}
	})

	t.Run("revoked admin is 403 (GAP-1)", func(t *testing.T) {
		r := newGin()
		provider := &fakeProvider{id: adminIdentity("stale-admin")}
		checker := &fakeAdminChecker{allow: false}
		SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider, AdminChecker: checker, Workspace: stubWorkspaceHandler(), Connection: stubConnectionHandler(t)})

		w := v1Request(r, http.MethodPost, testConnectionPath+"/activate", true)
		if w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403 for a revoked admin", w.Code)
		}
		if checker.calls != 1 {
			t.Errorf("live-admin checker calls = %d, want 1", checker.calls)
		}
	})

	t.Run("live admin reaches the handler", func(t *testing.T) {
		r := newGin()
		provider := &fakeProvider{id: adminIdentity("real-admin")}
		checker := &fakeAdminChecker{allow: true}
		SetupRouter(r, RouterDeps{User: SetupUser(&gorm.DB{}), Provider: provider, AdminChecker: checker, Workspace: stubWorkspaceHandler(), Connection: stubConnectionHandler(t)})

		// The stub repository reports no such connection, so a 404 from the
		// handler proves the whole chain passed and the handler ran.
		w := v1Request(r, http.MethodGet, testConnectionPath, true)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 from the handler (the chain should have passed)", w.Code)
		}
		if !strings.Contains(w.Body.String(), "connection_not_found") {
			t.Errorf("body = %s, want the connection error envelope", w.Body.String())
		}
		if w.Header().Get(requestid.Header) == "" {
			t.Error("connection responses must carry a request id")
		}
	})
}

// TestAdminSurfaceUnchangedByConnections re-runs the compatibility guarantee
// with BOTH /v1 options mounted.
func TestAdminSurfaceUnchangedByConnections(t *testing.T) {
	stubURL := keycloakStub(t)
	cfg := &config.Config{
		KeycloakURL:               stubURL,
		KeycloakRealm:             "saas",
		KeycloakClientID:          "saas-api",
		KeycloakAdminClientID:     "kc-admin",
		KeycloakAdminClientSecret: "shh",
		AdminLiveCheckTTLSeconds:  5,
	}

	build := func(v1 func(*RouterDeps)) *gin.Engine {
		identityHandler, _, _, err := SetupIdentity(cfg)
		if err != nil {
			t.Fatalf("SetupIdentity: %v", err)
		}
		r := newGin()
		provider := &fakeProvider{id: adminIdentity("admin-1")}
		checker := &fakeAdminChecker{allow: true}
		deps := RouterDeps{
			User:         SetupUser(&gorm.DB{}),
			Identity:     identityHandler,
			Provider:     provider,
			AdminChecker: checker,
		}
		if v1 != nil {
			v1(&deps)
		}
		SetupRouter(r, deps)
		return r
	}

	adminRoutes := func(r *gin.Engine) []string {
		var out []string
		for _, route := range r.Routes() {
			if strings.HasPrefix(route.Path, "/admin") {
				out = append(out, route.Method+" "+route.Path)
			}
		}
		sort.Strings(out)
		return out
	}

	bare := build(nil)
	full := build(func(d *RouterDeps) {
		d.Workspace = stubWorkspaceHandler()
		d.Connection = stubConnectionHandler(t)
	})

	before, after := adminRoutes(bare), adminRoutes(full)
	if len(before) == 0 {
		t.Fatal("no /admin routes registered — the fixture is wrong")
	}
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("/admin route table changed:\nbefore:\n%s\nafter:\n%s",
			strings.Join(before, "\n"), strings.Join(after, "\n"))
	}

	call := func(r *gin.Engine) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
		req.Header.Set("Authorization", "Bearer t")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	a, b := call(bare), call(full)

	if a.Code != b.Code || a.Body.String() != b.Body.String() {
		t.Errorf("/admin/users response changed: %d/%s → %d/%s",
			a.Code, a.Body.String(), b.Code, b.Body.String())
	}
	if b.Header().Get(requestid.Header) != "" {
		t.Error("/admin/users grew an X-Request-Id header")
	}
}

// TestSetupConnection_DisabledWithoutMasterKey pins the wiring signal.
func TestSetupConnection_DisabledWithoutMasterKey(t *testing.T) {
	handler, err := SetupConnection(&gorm.DB{}, &config.Config{}, nil)
	if err != nil {
		t.Fatalf("SetupConnection without a key returned an error: %v", err)
	}
	if handler != nil {
		t.Error("no master key must yield no connection handler — credentials cannot be stored unsealed")
	}
}

// TestSetupConnection_RejectsUnusableMasterKey — a configured but malformed key
// is an operator mistake worth failing the boot for, not a silent disable.
func TestSetupConnection_RejectsUnusableMasterKey(t *testing.T) {
	for name, key := range map[string]string{
		"not base64": "!!!!",
		"too short":  "c2hvcnQ=",
		"16 bytes":   "AAAAAAAAAAAAAAAAAAAAAA==",
	} {
		t.Run(name, func(t *testing.T) {
			handler, err := SetupConnection(&gorm.DB{}, &config.Config{SecretsMasterKey: key}, nil)
			if err == nil {
				t.Fatal("expected an error for an unusable master key")
			}
			if handler != nil {
				t.Error("a handler was returned alongside the error")
			}
			if strings.Contains(err.Error(), key) {
				t.Errorf("the error echoes the key material: %v", err)
			}
		})
	}
}

// TestSetupConnection_BuildsHandlerWithValidKey closes the wiring path.
func TestSetupConnection_BuildsHandlerWithValidKey(t *testing.T) {
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// A recorder is required now: without a durable audit writer the connection
	// service cannot guarantee its mutations are atomic with their audit rows,
	// so SetupConnection deliberately yields no handler. Passing one here is
	// what makes this test about the KEY, which is its subject.
	handler, err := SetupConnection(&gorm.DB{}, &config.Config{SecretsMasterKey: secrets.EncodeKey(key)},
		auditlog.NewRecorder(&stubAuditStore{}))
	if err != nil {
		t.Fatalf("SetupConnection: %v", err)
	}
	if handler == nil {
		t.Error("a valid master key must yield a handler")
	}
}

// stubAuditStore is a durable-audit store that accepts everything. These
// wiring tests are about which handlers get built, not about what is recorded.
type stubAuditStore struct{}

func (stubAuditStore) Record(context.Context, auditlog.Record) error { return nil }
func (stubAuditStore) List(context.Context, auditlog.Query) (auditlog.Page, error) {
	return auditlog.Page{}, nil
}
func (stubAuditStore) DeleteOlderThan(context.Context, time.Time) (int64, error) { return 0, nil }
func (s stubAuditStore) WithTx(database.Tx) auditlog.Store                       { return s }
