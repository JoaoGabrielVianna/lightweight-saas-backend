package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identityruntime"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Phase 12 — the two worlds must stay separated.
//
// /admin/* is what the frontend talks to. It is legacy compatibility for the
// duration of this slice, which means it may not move: not its routes, not its
// response bodies, not its headers, and not the provider it reaches. Every
// assertion here exists because the alternative is discovering the regression
// from a broken console.

// legacyRoutes returns the sorted /admin route table.
func legacyRoutes(r *gin.Engine) []string {
	var out []string
	for _, route := range r.Routes() {
		if strings.HasPrefix(route.Path, "/admin") {
			out = append(out, route.Method+" "+route.Path)
		}
	}
	sort.Strings(out)
	return out
}

// legacyCfg is the configuration a legacy installation runs with: Keycloak in
// the environment, and nothing else.
func legacyCfg(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		KeycloakURL:               keycloakStub(t),
		KeycloakRealm:             "saas",
		KeycloakClientID:          "saas-api",
		KeycloakAdminClientID:     "kc-admin",
		KeycloakAdminClientSecret: "shh",
		AdminLiveCheckTTLSeconds:  5,
	}
}

// TestLegacyAdminRouteTableIsExactlyWhatItWas is the frozen list.
//
// Slice 5 added twenty-one routes to /v1 and must have added ZERO to /admin.
// The critical rule for this slice is that no new identity capability lands
// only on the legacy surface; the converse — that none lands there at all — is
// what keeps the migration honest, and it is checked by enumeration rather than
// by counting.
func TestLegacyAdminRouteTableIsExactlyWhatItWas(t *testing.T) {
	identityHandler, checker, provider, err := SetupIdentity(legacyCfg(t))
	if err != nil {
		t.Fatalf("SetupIdentity: %v", err)
	}

	r := newGin()
	SetupRouter(r, RouterDeps{
		User:           SetupUser(&gorm.DB{}),
		Identity:       identityHandler,
		Audit:          NewAuditHandler(nil),
		Provider:       &fakeProvider{id: adminIdentity("admin-1")},
		AdminChecker:   checker,
		SMTP:           NewSMTPHandler(provider),
		EmailTemplates: NewEmailTemplatesHandler(provider),
	})

	want := []string{
		"DELETE /admin/invitations/:id",
		"DELETE /admin/roles/:name",
		"DELETE /admin/sessions/:id",
		"DELETE /admin/settings/email-templates/:key",
		"DELETE /admin/users/:id",
		"DELETE /admin/users/:id/roles/:name",
		"DELETE /admin/users/:id/sessions",
		"GET /admin/audit-events",
		"GET /admin/invitations",
		"GET /admin/roles",
		"GET /admin/roles/:name",
		"GET /admin/roles/:name/users",
		"GET /admin/sessions",
		"GET /admin/settings/email-templates",
		"GET /admin/settings/smtp",
		"GET /admin/users",
		"GET /admin/users/:id",
		"GET /admin/users/:id/roles",
		"GET /admin/users/:id/sessions",
		"PATCH /admin/roles/:name",
		"PATCH /admin/users/:id",
		"POST /admin/invitations",
		"POST /admin/invitations/:id/resend",
		"POST /admin/roles",
		"POST /admin/settings/smtp/test",
		"POST /admin/users/:id/reset-password",
		"POST /admin/users/:id/roles",
		"POST /admin/users/invite",
		"POST /admin/users/password",
		"PUT /admin/settings/email-templates/:key",
		"PUT /admin/settings/smtp",
		"PUT /admin/users/:id/password",
	}

	got := legacyRoutes(r)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the legacy /admin surface changed.\n--- want ---\n%s\n--- got ---\n%s",
			strings.Join(want, "\n"), strings.Join(got, "\n"))
	}
}

// TestAdminSurfaceUnchangedByTheFullWorkspaceIdentityAPI extends the Slice 4
// check to the completed surface: routes, a representative response body, and
// headers all identical with and without the workspace runtime mounted.
func TestAdminSurfaceUnchangedByTheFullWorkspaceIdentityAPI(t *testing.T) {
	cfg := legacyCfg(t)

	build := func(withRuntime bool) *gin.Engine {
		identityHandler, checker, provider, err := SetupIdentity(cfg)
		if err != nil {
			t.Fatalf("SetupIdentity: %v", err)
		}
		r := newGin()
		deps := RouterDeps{
			User:           SetupUser(&gorm.DB{}),
			Identity:       identityHandler,
			Audit:          NewAuditHandler(nil),
			Provider:       &fakeProvider{id: adminIdentity("admin-1")},
			AdminChecker:   checker,
			SMTP:           NewSMTPHandler(provider),
			EmailTemplates: NewEmailTemplatesHandler(provider),
			Workspace:      stubWorkspaceHandler(),
			Connection:     stubConnectionHandler(t),
		}
		if withRuntime {
			deps.WorkspaceIdentity = stubWorkspaceIdentityHandler(t)
		}
		SetupRouter(r, deps)
		return r
	}

	without, with := build(false), build(true)

	if a, b := legacyRoutes(without), legacyRoutes(with); strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Errorf("/admin route table changed when the workspace identity API was mounted:\n%s\n---\n%s",
			strings.Join(a, "\n"), strings.Join(b, "\n"))
	}

	// A representative call from each family the frontend uses.
	for _, path := range []string{"/admin/users", "/admin/roles", "/admin/sessions", "/admin/invitations"} {
		a := v1Request(without, http.MethodGet, path, true)
		b := v1Request(with, http.MethodGet, path, true)

		if a.Code != b.Code || a.Body.String() != b.Body.String() {
			t.Errorf("%s response changed: %d/%s → %d/%s",
				path, a.Code, a.Body.String(), b.Code, b.Body.String())
		}
		if b.Header().Get(requestid.Header) != "" {
			t.Errorf("%s grew an X-Request-Id header", path)
		}
	}
}

// TestAdminUsersStillEchoesRawPagination is the TD-020 compatibility pin, and
// the counterpart to the /v1 test that asserts the fix.
//
// /admin/users echoes the caller's RAW first/max even though the service
// clamped them. That is a bug, it is documented as TD-020, and it is
// deliberately NOT fixed here: the frontend is still on this surface, and
// changing a response body it parses is exactly the class of change this slice
// promised not to make. The two assertions together — raw here, effective on
// /v1 — are what make the difference a decision rather than an accident.
func TestAdminUsersStillEchoesRawPagination(t *testing.T) {
	identityHandler, _, _, err := SetupIdentity(legacyCfg(t))
	if err != nil {
		t.Fatalf("SetupIdentity: %v", err)
	}

	r := newGin()
	SetupRouter(r, RouterDeps{
		User:     SetupUser(&gorm.DB{}),
		Identity: identityHandler,
		Provider: &fakeProvider{id: adminIdentity("admin-1")},
		// The live-admin check is stubbed to allow: this test is about the
		// response body past the gate, and the real checker would consult the
		// keycloak stub's empty role list and stop at 403.
		AdminChecker: &fakeAdminChecker{allow: true},
	})

	req := httptest.NewRequest(http.MethodGet, "/admin/users?max=9999&first=-5", nil)
	req.Header.Set("Authorization", "Bearer t")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}

	var out struct {
		First int `json:"first"`
		Max   int `json:"max"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.First != -5 || out.Max != 9999 {
		t.Errorf("first/max = %d/%d, want the raw -5/9999 — TD-020 must stay reproduced on /admin "+
			"until the frontend moves off it", out.First, out.Max)
	}
}

// TestWorkspaceConnectionsCannotRedirectAdmin is the separation stated as a
// property rather than as a hope.
//
// /admin/* builds its provider from KEYCLOAK_* at boot. /v1 resolves one per
// request from a workspace's connection. Here the workspace runtime is wired
// with stores that resolve NOTHING — no workspace, no connection — and
// /admin/users must still answer 200 from its own provider. If the two ever
// share a path, this is where it shows.
func TestWorkspaceConnectionsCannotRedirectAdmin(t *testing.T) {
	identityHandler, _, _, err := SetupIdentity(legacyCfg(t))
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

	if w := v1Request(r, http.MethodGet, "/admin/users", true); w.Code != http.StatusOK {
		t.Errorf("/admin/users = %d, want 200 — it must keep using the process-level provider "+
			"regardless of what the connections table says", w.Code)
	}

	scoped := v1Request(r, http.MethodGet,
		"/v1/workspaces/ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301/users", true)
	if scoped.Code != http.StatusNotFound {
		t.Errorf("/v1/.../users = %d, want 404 — the runtime has no workspace to resolve", scoped.Code)
	}
}

// TestAdminErrorEnvelopeIsUnchanged pins the legacy error SHAPE.
//
// /admin/* returns `{"error": "not found"}` — a flat string with no code. /v1
// returns the structured envelope. Phase 4 says /admin must not be forced to
// adopt the new shape in this slice (that is TD-022), so the old shape is
// asserted here to make sure a shared-seam change did not quietly upgrade it.
func TestAdminErrorEnvelopeIsUnchanged(t *testing.T) {
	identityHandler, _, _, err := SetupIdentity(legacyCfg(t))
	if err != nil {
		t.Fatalf("SetupIdentity: %v", err)
	}

	r := newGin()
	SetupRouter(r, RouterDeps{
		User:     SetupUser(&gorm.DB{}),
		Identity: identityHandler,
		Provider: &fakeProvider{id: adminIdentity("admin-1")},
		// The live-admin check is stubbed to allow: this test is about the
		// response body past the gate, and the real checker would consult the
		// keycloak stub's empty role list and stop at 403.
		AdminChecker: &fakeAdminChecker{allow: true},
	})

	// A malformed user id: rejected by the service as ErrBadRequest.
	w := v1Request(r, http.MethodGet, "/admin/users/not-a-uuid", true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}

	var flat struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &flat); err != nil {
		t.Fatalf("the legacy body is no longer a flat {\"error\": string}: %s", w.Body.String())
	}
	if flat.Error != "bad request" {
		t.Errorf("error = %q, want the legacy literal %q", flat.Error, "bad request")
	}
}

// TestWorkspaceIdentityRoutesMatchTheDeclaredList keeps internal/server's mounts
// and internal/identityruntime's declared surface in agreement.
//
// The declared list is what identityruntime's own tests walk to prove every
// mutation is write-guarded. A route mounted here but absent there would escape
// that check entirely — mounted, reachable, and never asserted to refuse writes
// through an under-privileged connection.
func TestWorkspaceIdentityRoutesMatchTheDeclaredList(t *testing.T) {
	r := newGin()
	SetupRouter(r, RouterDeps{
		User:              SetupUser(&gorm.DB{}),
		Provider:          &fakeProvider{id: adminIdentity("admin-1")},
		Workspace:         stubWorkspaceHandler(),
		Connection:        stubConnectionHandler(t),
		WorkspaceIdentity: stubWorkspaceIdentityHandler(t),
	})

	// Everything under /v1/workspaces that is NOT a workspace or connection
	// route — i.e. what the identity runtime contributed.
	connectionOrWorkspace := func(path string) bool {
		return !strings.Contains(path, "/connections") &&
			path != "/v1/workspaces" &&
			path != "/v1/workspaces/:workspace_id" &&
			path != "/v1/workspaces/:workspace_id/archive"
	}

	var mounted []string
	for _, route := range r.Routes() {
		if strings.HasPrefix(route.Path, "/v1/workspaces") && connectionOrWorkspace(route.Path) {
			mounted = append(mounted, route.Method+" "+route.Path)
		}
	}
	sort.Strings(mounted)

	declared := append([]string(nil), identityruntime.MountedWorkspaceIdentityRoutes()...)
	sort.Strings(declared)

	if strings.Join(mounted, "\n") != strings.Join(declared, "\n") {
		t.Errorf("router mounts and identityruntime's declared list disagree:\n--- mounted ---\n%s\n--- declared ---\n%s",
			strings.Join(mounted, "\n"), strings.Join(declared, "\n"))
	}
}
