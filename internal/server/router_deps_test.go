package server

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// These tests exist because of the refactor that introduced RouterDeps, not
// because of any behaviour it was meant to change. TD-006 replaced eight
// positional parameters and two variadic options with one struct; the risk of
// that edit is not that it fails to compile but that a field lands in the wrong
// slot and silently mounts — or stops mounting — a route.
//
// So the assertions here are deliberately about the SHAPE of the surface: the
// complete route table, the gate ordering, and what the zero value mounts.
// Everything about what a handler then does is covered by that handler's own
// package.

// fullyWiredDeps builds a RouterDeps with every optional surface present.
//
// Deliberately NOT a shared fixture with the other tests in this package: the
// point of a golden route table is to enumerate what a maximally-configured
// installation exposes, and reusing a fixture tuned for something else would
// quietly shrink what "maximal" means.
func fullyWiredDeps(t *testing.T) RouterDeps {
	t.Helper()

	stubURL := keycloakStub(t)
	cfg := &config.Config{
		KeycloakURL:               stubURL,
		KeycloakRealm:             "saas",
		KeycloakClientID:          "saas-api",
		KeycloakAdminClientID:     "kc-admin",
		KeycloakAdminClientSecret: "shh",
		AdminLiveCheckTTLSeconds:  5,
	}
	identityHandler, _, provider, err := SetupIdentity(cfg)
	if err != nil {
		t.Fatalf("SetupIdentity: %v", err)
	}

	return RouterDeps{
		User:           SetupUser(&gorm.DB{}),
		Identity:       identityHandler,
		Audit:          NewAuditHandler(nil),
		Provider:       &fakeProvider{id: adminIdentity("admin-1")},
		AdminChecker:   &fakeAdminChecker{allow: true},
		SMTP:           NewSMTPHandler(provider),
		EmailTemplates: NewEmailTemplatesHandler(provider),
		Workspace:      stubWorkspaceHandler(),
		Connection:     stubConnectionHandler(t),

		WorkspaceIdentity: stubWorkspaceIdentityHandler(t),
	}
}

func routeTable(r *gin.Engine) []string {
	out := make([]string, 0, len(r.Routes()))
	for _, route := range r.Routes() {
		out = append(out, route.Method+" "+route.Path)
	}
	sort.Strings(out)
	return out
}

// TestRouterDeps_FullRouteTableGolden pins the entire route table of a
// fully-wired router.
//
// This is the regression test the TD-006 refactor needed and did not have. The
// existing tests each cover a slice of the surface (admin routes, v1 routes,
// connection routes); none of them would notice a route that moved between
// groups, and a misassigned struct field is exactly the mistake that produces
// that. An intentional route change updates this list in the same commit —
// which is the point: the diff then states, in one place, what the API gained
// or lost.
func TestRouterDeps_FullRouteTableGolden(t *testing.T) {
	r := newGin()
	SetupRouter(r, fullyWiredDeps(t))

	want := []string{
		"DELETE /admin/invitations/:id",
		"DELETE /admin/roles/:name",
		"DELETE /admin/sessions/:id",
		"DELETE /admin/settings/email-templates/:key",
		"DELETE /admin/users/:id",
		"DELETE /admin/users/:id/roles/:name",
		"DELETE /admin/users/:id/sessions",
		"DELETE /v1/workspaces/:workspace_id/connections/:connection_id",
		"DELETE /v1/workspaces/:workspace_id/invitations/:invitation_id",
		"DELETE /v1/workspaces/:workspace_id/roles/:role_name",
		"DELETE /v1/workspaces/:workspace_id/sessions/:session_id",
		"DELETE /v1/workspaces/:workspace_id/users/:user_id",
		"DELETE /v1/workspaces/:workspace_id/users/:user_id/roles/:role_name",
		"DELETE /v1/workspaces/:workspace_id/users/:user_id/sessions",
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
		"GET /me",
		"GET /v1/workspaces",
		"GET /v1/workspaces/:workspace_id",
		"GET /v1/workspaces/:workspace_id/connections",
		"GET /v1/workspaces/:workspace_id/connections/:connection_id",
		"GET /v1/workspaces/:workspace_id/invitations",
		"GET /v1/workspaces/:workspace_id/roles",
		"GET /v1/workspaces/:workspace_id/roles/:role_name",
		"GET /v1/workspaces/:workspace_id/roles/:role_name/users",
		"GET /v1/workspaces/:workspace_id/sessions",
		"GET /v1/workspaces/:workspace_id/users",
		"GET /v1/workspaces/:workspace_id/users/:user_id",
		"GET /v1/workspaces/:workspace_id/users/:user_id/roles",
		"GET /v1/workspaces/:workspace_id/users/:user_id/sessions",
		"PATCH /admin/roles/:name",
		"PATCH /admin/users/:id",
		"PATCH /v1/workspaces/:workspace_id",
		"PATCH /v1/workspaces/:workspace_id/connections/:connection_id",
		"PATCH /v1/workspaces/:workspace_id/roles/:role_name",
		"PATCH /v1/workspaces/:workspace_id/users/:user_id",
		"POST /admin/invitations",
		"POST /admin/invitations/:id/resend",
		"POST /admin/roles",
		"POST /admin/settings/smtp/test",
		"POST /admin/users/:id/reset-password",
		"POST /admin/users/:id/roles",
		"POST /admin/users/invite",
		"POST /admin/users/password",
		"POST /v1/workspaces",
		"POST /v1/workspaces/:workspace_id/archive",
		"POST /v1/workspaces/:workspace_id/connections",
		"POST /v1/workspaces/:workspace_id/connections/:connection_id/activate",
		"POST /v1/workspaces/:workspace_id/connections/:connection_id/retire",
		"POST /v1/workspaces/:workspace_id/connections/:connection_id/verify",
		"POST /v1/workspaces/:workspace_id/invitations",
		"POST /v1/workspaces/:workspace_id/invitations/:invitation_id/resend",
		"POST /v1/workspaces/:workspace_id/roles",
		"POST /v1/workspaces/:workspace_id/users",
		"POST /v1/workspaces/:workspace_id/users/:user_id/reset-password",
		"POST /v1/workspaces/:workspace_id/users/:user_id/roles",
		"PUT /admin/settings/email-templates/:key",
		"PUT /admin/settings/smtp",
		"PUT /admin/users/:id/password",
		"PUT /v1/workspaces/:workspace_id/users/:user_id/password",
	}

	got := routeTable(r)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("route table drifted.\n--- want ---\n%s\n--- got ---\n%s",
			strings.Join(want, "\n"), strings.Join(got, "\n"))
	}
}

// TestRouterDeps_ZeroValueMountsOnlyTheAlwaysOnRoutes pins what a router built
// from nothing exposes.
//
// The zero RouterDeps has a nil Provider and nil User handler, which is not a
// deployment anyone runs — but it IS the state every field-by-field mistake
// degrades toward, and asserting it catches a nil check written backwards.
// /me is registered because its group is unconditional; the gates it carries
// are what stop the nil handler ever running.
func TestRouterDeps_ZeroValueMountsOnlyTheAlwaysOnRoutes(t *testing.T) {
	r := newGin()
	SetupRouter(r, RouterDeps{})

	want := []string{"GET /me"}
	got := routeTable(r)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("zero RouterDeps mounted %v, want %v", got, want)
	}
}

// TestRouterDeps_GateOrderIsRateLimitThenAuth proves the rate limiter still
// sits BEFORE token validation on both gated groups.
//
// Order is not observable from the route table, so it is asserted
// behaviourally: flood an UNAUTHENTICATED request past the burst and watch the
// status change from 401 to 429. If RequireAuth ran first, every response would
// stay 401 and an unauthenticated flood would burn CPU on JWT validation —
// which is the property the F1 closure added and the one a parameter-to-field
// slip would silently drop.
func TestRouterDeps_GateOrderIsRateLimitThenAuth(t *testing.T) {
	for _, path := range []string{"/admin/users", "/v1/workspaces"} {
		t.Run(path, func(t *testing.T) {
			deps := fullyWiredDeps(t)
			deps.Provider = &fakeProvider{err: auth.ErrInvalidToken}

			r := newGin()
			SetupRouter(r, deps)

			sawUnauthorized, sawRateLimited := false, false
			for i := 0; i < 200; i++ {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.RemoteAddr = "203.0.113.9:1234"
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)

				switch w.Code {
				case http.StatusUnauthorized:
					sawUnauthorized = true
				case http.StatusTooManyRequests:
					sawRateLimited = true
				}
				if sawRateLimited {
					break
				}
			}

			if !sawUnauthorized {
				t.Error("never saw 401 — RequireAuth is not mounted on this group")
			}
			if !sawRateLimited {
				t.Error("never saw 429 — the rate limiter is not mounted before auth")
			}
		})
	}
}

// TestSetupIdentity_UnconfiguredCheckerIsAGenuineNilInterface guards the trap
// the RouterDeps refactor exposed.
//
// SetupIdentity used to declare *auth.CachedAdminChecker as its second return
// type. On the not-configured path it returned a TYPED nil, and storing that in
// an interface-typed field produces a non-nil interface wrapping a nil pointer:
// `deps.AdminChecker != nil` reads true, RequireLiveAdmin gets mounted, and the
// first request through it dereferences a nil receiver. Declaring the interface
// as the return type is what makes the nil check downstream mean what it says.
//
// The assertion is on the interface value specifically — comparing against a
// typed nil would pass either way and prove nothing.
func TestSetupIdentity_UnconfiguredCheckerIsAGenuineNilInterface(t *testing.T) {
	handler, checker, provider, err := SetupIdentity(&config.Config{})
	if err != nil {
		t.Fatalf("SetupIdentity with no credentials: %v", err)
	}
	if handler != nil || provider != nil {
		t.Fatal("no admin credentials must yield no identity handler and no provider")
	}
	if checker != nil {
		t.Errorf("checker is a non-nil interface (%T) — a typed nil would mount RequireLiveAdmin with a receiver that panics", checker)
	}
}

// TestRouterDeps_V1MountsWithoutAnAdminChecker is the request-path half of the
// test above: an installation with workspaces but no identity provider has
// nothing to ask "is this subject still an admin", and /v1 must serve rather
// than panic.
func TestRouterDeps_V1MountsWithoutAnAdminChecker(t *testing.T) {
	r := newGin()
	SetupRouter(r, RouterDeps{
		User:      SetupUser(&gorm.DB{}),
		Provider:  &fakeProvider{id: adminIdentity("admin-1")},
		Workspace: stubWorkspaceHandler(),
	})

	w := v1Request(r, http.MethodGet, "/v1/workspaces", true)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — /v1 must serve when no admin checker is configured", w.Code)
	}
}
