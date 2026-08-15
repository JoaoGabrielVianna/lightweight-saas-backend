package server

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/connection"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identityruntime"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Routing-level tests for the workspace-scoped identity surface. What the
// resolver does once reached is internal/identityruntime's business; what
// matters here is that these routes are mounted only when configured, carry the
// same gates as every other /v1 route, and change nothing about /admin/*.

// stubWorkspaceIdentityHandler builds the handler over stores that never touch
// a database. The stores answer "no such workspace", which is enough to reach
// the handler and observe a stable response.
func stubWorkspaceIdentityHandler(t *testing.T) *identityruntime.Handler {
	t.Helper()

	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyring, err := secrets.NewSingleVersionKeyring(1, key)
	if err != nil {
		t.Fatalf("new keyring: %v", err)
	}

	resolver := identityruntime.NewResolver(
		&stubRuntimeWorkspaces{}, &stubRuntimeConnections{}, keyring, identityruntime.Options{})
	if resolver == nil {
		t.Fatal("identityruntime.NewResolver returned nil with every collaborator present")
	}
	return identityruntime.NewHandler(resolver)
}

type stubRuntimeWorkspaces struct{}

func (s *stubRuntimeWorkspaces) GetByID(context.Context, string) (*workspace.Workspace, error) {
	return nil, nil
}

type stubRuntimeConnections struct{}

func (s *stubRuntimeConnections) GetActiveByWorkspace(context.Context, string) (*connection.Connection, error) {
	return nil, nil
}
func (s *stubRuntimeConnections) OpenSecret(context.Context, string) (*secrets.Sealed, error) {
	return nil, nil
}

// workspaceIdentityRoutes is the set this surface adds.
var workspaceIdentityRoutes = []string{
	"GET /v1/workspaces/:workspace_id/users",
	"GET /v1/workspaces/:workspace_id/roles",
	"POST /v1/workspaces/:workspace_id/roles",
}

// ---------------------------------------------------------------------------
// Mounting
// ---------------------------------------------------------------------------

// TestSetupRouter_WorkspaceIdentityAbsentWithoutTheDependency pins that these
// routes are opt-in. A deployment with no master key has no connections to
// route through, and must answer 404 rather than a 503 that confirms the
// feature exists with different configuration.
func TestSetupRouter_WorkspaceIdentityAbsentWithoutTheDependency(t *testing.T) {
	r := newGin()
	SetupRouter(r, RouterDeps{
		User:      SetupUser(&gorm.DB{}),
		Provider:  &fakeProvider{id: adminIdentity("s1")},
		Workspace: stubWorkspaceHandler(),
	})

	w := v1Request(r, http.MethodGet, "/v1/workspaces/ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301/users", true)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when WorkspaceIdentity is not wired", w.Code)
	}
}

// TestSetupRouter_WorkspaceIdentityNilIsNoOp — an explicit nil must behave
// exactly like an absent field, not panic.
func TestSetupRouter_WorkspaceIdentityNilIsNoOp(t *testing.T) {
	r := newGin()
	SetupRouter(r, RouterDeps{
		User:              SetupUser(&gorm.DB{}),
		Provider:          &fakeProvider{id: adminIdentity("s1")},
		Workspace:         stubWorkspaceHandler(),
		WorkspaceIdentity: nil,
	})

	w := v1Request(r, http.MethodGet, "/v1/workspaces/ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301/users", true)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestSetupRouter_WorkspaceIdentityRequiresTheWorkspaceSurface — these routes
// live inside the /v1 group, and /v1 exists only when the workspace domain is
// wired. Wiring the runtime alone must not conjure a partial /v1.
func TestSetupRouter_WorkspaceIdentityRequiresTheWorkspaceSurface(t *testing.T) {
	r := newGin()
	SetupRouter(r, RouterDeps{
		User:              SetupUser(&gorm.DB{}),
		Provider:          &fakeProvider{id: adminIdentity("s1")},
		WorkspaceIdentity: stubWorkspaceIdentityHandler(t),
	})

	if len(r.Routes()) != 1 {
		t.Errorf("routes = %v, want only GET /me", routeTable(r))
	}
}

// TestSetupRouter_AllWorkspaceIdentityRoutesRegistered enumerates the surface.
func TestSetupRouter_AllWorkspaceIdentityRoutesRegistered(t *testing.T) {
	r := newGin()
	SetupRouter(r, RouterDeps{
		User:              SetupUser(&gorm.DB{}),
		Provider:          &fakeProvider{id: adminIdentity("s1")},
		Workspace:         stubWorkspaceHandler(),
		WorkspaceIdentity: stubWorkspaceIdentityHandler(t),
	})

	got := map[string]bool{}
	for _, route := range r.Routes() {
		got[route.Method+" "+route.Path] = true
	}
	for _, want := range workspaceIdentityRoutes {
		if !got[want] {
			t.Errorf("route %q not registered", want)
		}
	}
}

// ---------------------------------------------------------------------------
// Gates
// ---------------------------------------------------------------------------

// TestSetupRouter_WorkspaceIdentityCarriesTheAdminChain walks the same chain
// assertion the connection routes get.
//
// These routes reach a Keycloak realm and can create realm roles in it, so a
// gap in the chain here is not a missing 403 — it is an unauthenticated caller
// writing to an identity provider.
func TestSetupRouter_WorkspaceIdentityCarriesTheAdminChain(t *testing.T) {
	const path = "/v1/workspaces/ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301/users"

	build := func(provider *fakeProvider, checker *fakeAdminChecker) *gin.Engine {
		r := newGin()
		deps := RouterDeps{
			User:              SetupUser(&gorm.DB{}),
			Provider:          provider,
			Workspace:         stubWorkspaceHandler(),
			WorkspaceIdentity: stubWorkspaceIdentityHandler(t),
		}
		if checker != nil {
			deps.AdminChecker = checker
		}
		SetupRouter(r, deps)
		return r
	}

	t.Run("no token is 401", func(t *testing.T) {
		r := build(&fakeProvider{id: adminIdentity("s1")}, nil)
		if w := v1Request(r, http.MethodGet, path, false); w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", w.Code)
		}
	})

	t.Run("token without the admin role is 403", func(t *testing.T) {
		r := build(&fakeProvider{id: &auth.Identity{
			Subject:   "s1",
			Roles:     []string{"user"},
			ExpiresAt: time.Now().Add(time.Hour),
		}}, nil)
		if w := v1Request(r, http.MethodGet, path, true); w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", w.Code)
		}
	})

	t.Run("live admin check denies is 403", func(t *testing.T) {
		r := build(&fakeProvider{id: adminIdentity("s1")}, &fakeAdminChecker{allow: false})
		if w := v1Request(r, http.MethodGet, path, true); w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", w.Code)
		}
	})

	t.Run("fully authorized reaches the handler", func(t *testing.T) {
		r := build(&fakeProvider{id: adminIdentity("s1")}, &fakeAdminChecker{allow: true})
		w := v1Request(r, http.MethodGet, path, true)
		// The stub stores report no such workspace, so the handler answers
		// 404 workspace_not_found — which is proof the request got past every
		// gate and into the resolver.
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 from the resolver (body %s)", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "workspace_not_found") {
			t.Errorf("body = %s, want the workspace_not_found envelope", w.Body.String())
		}
	})

	t.Run("responses carry a request id", func(t *testing.T) {
		r := build(&fakeProvider{id: adminIdentity("s1")}, &fakeAdminChecker{allow: true})
		w := v1Request(r, http.MethodGet, path, true)
		if w.Header().Get(requestid.Header) == "" {
			t.Error("no X-Request-Id — the /v1 group's middleware is not applied to these routes")
		}
	})
}

// ---------------------------------------------------------------------------
// Legacy compatibility (Phase 11)
// ---------------------------------------------------------------------------

// TestAdminSurfaceUnchangedByWorkspaceIdentity is the regression this slice
// most needs.
//
// /admin/* is the surface the existing frontend talks to, and it must keep
// using the process-level Keycloak provider whether or not the workspace-scoped
// runtime is wired. The assertion is threefold: the same route table, the same
// response body for a representative call, and no new X-Request-Id header —
// the last one because requestid.Middleware is mounted on /v1 only, and
// mounting it globally would change every /admin response.
func TestAdminSurfaceUnchangedByWorkspaceIdentity(t *testing.T) {
	stubURL := keycloakStub(t)
	cfg := &config.Config{
		KeycloakURL:               stubURL,
		KeycloakRealm:             "saas",
		KeycloakClientID:          "saas-api",
		KeycloakAdminClientID:     "kc-admin",
		KeycloakAdminClientSecret: "shh",
		AdminLiveCheckTTLSeconds:  5,
	}

	build := func(withRuntime bool) *gin.Engine {
		identityHandler, _, _, err := SetupIdentity(cfg)
		if err != nil {
			t.Fatalf("SetupIdentity: %v", err)
		}
		r := newGin()
		deps := RouterDeps{
			User:         SetupUser(&gorm.DB{}),
			Identity:     identityHandler,
			Provider:     &fakeProvider{id: adminIdentity("admin-1")},
			AdminChecker: &fakeAdminChecker{allow: true},
			Workspace:    stubWorkspaceHandler(),
			Connection:   stubConnectionHandler(t),
		}
		if withRuntime {
			deps.WorkspaceIdentity = stubWorkspaceIdentityHandler(t)
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
		t.Errorf("/admin route table changed when the workspace runtime was mounted:\nbefore:\n%s\nafter:\n%s",
			strings.Join(before, "\n"), strings.Join(after, "\n"))
	}

	a := v1Request(without, http.MethodGet, "/admin/users", true)
	b := v1Request(with, http.MethodGet, "/admin/users", true)
	if a.Code != b.Code || a.Body.String() != b.Body.String() {
		t.Errorf("/admin/users response changed: %d/%s → %d/%s",
			a.Code, a.Body.String(), b.Code, b.Body.String())
	}
	if b.Header().Get(requestid.Header) != "" {
		t.Error("/admin/users grew an X-Request-Id header")
	}
}

// TestWorkspaceIdentityDoesNotShadowTheAdminUsersRoute pins that the two user
// surfaces are genuinely separate paths reaching separate providers.
//
// /admin/users answers from the process-level Keycloak configuration and
// /v1/workspaces/{id}/users answers from a resolved connection. Here the
// runtime's stores are empty, so the /v1 route reports workspace_not_found
// while /admin/users still serves — which could not happen if one had
// displaced the other.
func TestWorkspaceIdentityDoesNotShadowTheAdminUsersRoute(t *testing.T) {
	stubURL := keycloakStub(t)
	identityHandler, _, _, err := SetupIdentity(&config.Config{
		KeycloakURL:               stubURL,
		KeycloakRealm:             "saas",
		KeycloakAdminClientID:     "kc-admin",
		KeycloakAdminClientSecret: "shh",
		AdminLiveCheckTTLSeconds:  5,
	})
	if err != nil {
		t.Fatalf("SetupIdentity: %v", err)
	}

	r := newGin()
	SetupRouter(r, RouterDeps{
		User:              SetupUser(&gorm.DB{}),
		Identity:          identityHandler,
		Provider:          &fakeProvider{id: adminIdentity("admin-1")},
		AdminChecker:      &fakeAdminChecker{allow: true},
		Workspace:         stubWorkspaceHandler(),
		WorkspaceIdentity: stubWorkspaceIdentityHandler(t),
	})

	admin := v1Request(r, http.MethodGet, "/admin/users", true)
	if admin.Code != http.StatusOK {
		t.Errorf("/admin/users = %d, want 200 — it must keep using the process-level provider", admin.Code)
	}

	scoped := v1Request(r, http.MethodGet, "/v1/workspaces/ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301/users", true)
	if scoped.Code != http.StatusNotFound {
		t.Errorf("/v1/.../users = %d, want 404 from the resolver", scoped.Code)
	}
	if !strings.Contains(scoped.Body.String(), "workspace_not_found") {
		t.Errorf("/v1/.../users body = %s, want the resolver's envelope", scoped.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Wiring
// ---------------------------------------------------------------------------

// TestSetupWorkspaceIdentity_DisabledWithoutMasterKey — no key, no connections,
// no runtime. Same signal SetupConnection uses.
func TestSetupWorkspaceIdentity_DisabledWithoutMasterKey(t *testing.T) {
	handler, err := SetupWorkspaceIdentity(&gorm.DB{}, &config.Config{})
	if err != nil {
		t.Fatalf("SetupWorkspaceIdentity without a key returned an error: %v", err)
	}
	if handler != nil {
		t.Error("no master key must yield no workspace-identity handler")
	}
}

// TestSetupWorkspaceIdentity_RejectsUnusableMasterKey — a key that IS set but
// cannot be decoded is an operator mistake worth refusing to boot on, not a
// reason to silently drop the routes.
func TestSetupWorkspaceIdentity_RejectsUnusableMasterKey(t *testing.T) {
	handler, err := SetupWorkspaceIdentity(&gorm.DB{}, &config.Config{SecretsMasterKey: "not-base64!!"})
	if err == nil {
		t.Fatal("an unusable master key was accepted")
	}
	if handler != nil {
		t.Error("handler returned alongside an error")
	}
	if strings.Contains(err.Error(), "not-base64") {
		t.Errorf("the error echoed the key material: %v", err)
	}
}

// TestSetupWorkspaceIdentity_BuildsHandlerWithValidKey.
func TestSetupWorkspaceIdentity_BuildsHandlerWithValidKey(t *testing.T) {
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	handler, err := SetupWorkspaceIdentity(&gorm.DB{}, &config.Config{
		SecretsMasterKey: secrets.EncodeKey(key),
	})
	if err != nil {
		t.Fatalf("SetupWorkspaceIdentity: %v", err)
	}
	if handler == nil {
		t.Error("a valid master key must yield a handler")
	}
}

// TestSetupWorkspaceIdentity_IgnoresTheProcessKeycloakConfiguration is the
// Phase 9 decision, asserted rather than only documented.
//
// The workspace runtime takes its provider coordinates exclusively from
// persisted Connections; legacy /admin/* takes its exclusively from the
// environment. Keeping the two authorities disjoint is what lets both exist in
// this slice without a precedence rule — so a deployment with a full Keycloak
// environment and no master key gets NO workspace-scoped surface, rather than
// one silently backed by the env configuration.
func TestSetupWorkspaceIdentity_IgnoresTheProcessKeycloakConfiguration(t *testing.T) {
	handler, err := SetupWorkspaceIdentity(&gorm.DB{}, &config.Config{
		KeycloakURL:               "http://keycloak.internal",
		KeycloakRealm:             "saas",
		KeycloakAdminClientID:     "kc-admin",
		KeycloakAdminClientSecret: "shh",
	})
	if err != nil {
		t.Fatalf("SetupWorkspaceIdentity: %v", err)
	}
	if handler != nil {
		t.Error("a fully-configured Keycloak environment produced a workspace-identity handler — " +
			"env configuration must not become a second authority for connection coordinates")
	}
}
