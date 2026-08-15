package identityruntime

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/connection"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	"github.com/gin-gonic/gin"
)

// The read-only / write-guard behaviour (Phase 8), and the structural test that
// keeps it central as the surface grows.

// mountAll registers every workspace-scoped route on a bare engine, using the
// same paths internal/server mounts. Kept in sync by
// TestRouteTableMatchesTheServerMounts, so these tests cannot quietly drift
// into exercising paths the product does not serve.
func mountAll(t *testing.T, h *Handler) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()

	for _, rt := range allWorkspaceIdentityRoutes(h) {
		r.Handle(rt.method, rt.path, rt.handler)
	}
	return r
}

type routeSpec struct {
	method  string
	path    string
	handler gin.HandlerFunc
	// mutating records whether this route is expected to go through the
	// write guard. It is the test's independent statement of intent, checked
	// against actual behaviour rather than read from the handler.
	mutating bool
	// body is a request body that passes binding, so a guard rejection can be
	// distinguished from a decode failure.
	body string
}

const (
	testUserUUID    = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	testSessionUUID = "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
)

// allWorkspaceIdentityRoutes is the single list this package's tests walk.
// Adding a route to the product without adding it here fails
// TestRouteTableMatchesTheServerMounts.
func allWorkspaceIdentityRoutes(h *Handler) []routeSpec {
	const ws = "/v1/workspaces/:workspace_id"
	return []routeSpec{
		{http.MethodGet, ws + "/users", h.ListUsers, false, ""},
		{http.MethodPost, ws + "/users", h.CreateUser, true, `{"email":"a@b.co","temporary_password":"password1"}`},
		{http.MethodGet, ws + "/users/:user_id", h.GetUser, false, ""},
		{http.MethodPatch, ws + "/users/:user_id", h.UpdateUser, true, `{}`},
		{http.MethodDelete, ws + "/users/:user_id", h.DeleteUser, true, ""},
		{http.MethodGet, ws + "/users/:user_id/roles", h.ListUserRoles, false, ""},
		{http.MethodPost, ws + "/users/:user_id/roles", h.AssignRolesToUser, true, `{"roles":["support"]}`},
		{http.MethodDelete, ws + "/users/:user_id/roles/:role_name", h.UnassignRoleFromUser, true, ""},
		{http.MethodPost, ws + "/users/:user_id/reset-password", h.ResetUserPassword, true, ""},
		{http.MethodPut, ws + "/users/:user_id/password", h.SetUserPassword, true, `{"password":"password1"}`},
		{http.MethodGet, ws + "/users/:user_id/sessions", h.ListUserSessions, false, ""},
		{http.MethodDelete, ws + "/users/:user_id/sessions", h.LogoutUserSessions, true, ""},
		{http.MethodGet, ws + "/sessions", h.ListSessions, false, ""},
		{http.MethodDelete, ws + "/sessions/:session_id", h.DeleteSession, true, ""},
		{http.MethodGet, ws + "/roles", h.ListRoles, false, ""},
		{http.MethodPost, ws + "/roles", h.CreateRole, true, `{"name":"billing"}`},
		{http.MethodGet, ws + "/roles/:role_name", h.GetRole, false, ""},
		{http.MethodPatch, ws + "/roles/:role_name", h.UpdateRole, true, `{"description":"x"}`},
		{http.MethodDelete, ws + "/roles/:role_name", h.DeleteRole, true, ""},
		{http.MethodGet, ws + "/roles/:role_name/users", h.ListRoleUsers, false, ""},
		{http.MethodGet, ws + "/invitations", h.ListInvitations, false, ""},
		{http.MethodPost, ws + "/invitations", h.CreateInvitation, true, `{"email":"a@b.co","roles":["support"]}`},
		{http.MethodDelete, ws + "/invitations/:invitation_id", h.DeleteInvitation, true, ""},
		{http.MethodPost, ws + "/invitations/:invitation_id/resend", h.ResendInvitation, true, ""},
	}
}

// concretePath substitutes real ids for the gin placeholders.
func (rt routeSpec) concretePath() string {
	p := strings.Replace(rt.path, ":workspace_id", testPublicID, 1)
	p = strings.Replace(p, ":user_id", testUserUUID, 1)
	p = strings.Replace(p, ":invitation_id", testUserUUID, 1)
	p = strings.Replace(p, ":session_id", testSessionUUID, 1)
	p = strings.Replace(p, ":role_name", "billing-admin", 1)
	return p
}

// ---------------------------------------------------------------------------
// The structural guarantee
// ---------------------------------------------------------------------------

// TestHandler_EveryMutationGoesThroughTheWriteGuard is the test that makes
// "enforced centrally, not duplicated across twenty handlers" true rather than
// aspirational.
//
// It walks EVERY route and drives it against a connection whose access mode is
// `limited`. Every route the list marks as mutating must be refused with
// connection_read_only, and every route it marks as a read must NOT be. A new
// mutation added without going through h.write() fails here, as does a read
// that acquires the guard by accident.
//
// The value is that it cannot be satisfied by remembering: the enforcement
// point is one function, and this asserts that all twenty-odd routes reach it.
func TestHandler_EveryMutationGoesThroughTheWriteGuard(t *testing.T) {
	f, stub := newStubFixture(t)
	f.conns.active[testWorkspaceID].AccessMode = connection.AccessModeLimited

	h := NewHandler(f.resolver)
	r := mountAll(t, h)

	for _, rt := range allWorkspaceIdentityRoutes(h) {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			w := do(r, rt.method, rt.concretePath(), rt.body)

			if rt.mutating {
				if w.Code != http.StatusConflict {
					t.Fatalf("status = %d, want 409 — this mutation did not go through the write guard (body %s)",
						w.Code, w.Body.String())
				}
				if got := decodeError(t, w).Code; got != "connection_read_only" {
					t.Errorf("code = %q, want connection_read_only", got)
				}
				return
			}

			if w.Code == http.StatusConflict {
				if got := decodeError(t, w).Code; got == "connection_read_only" {
					t.Error("a read operation was refused by the write guard — reads must stay available")
				}
			}
		})
	}

	// Nothing marked mutating may have reached the provider.
	if got := stub.recorded(); len(got) != 0 {
		t.Errorf("mutations reached the provider through a limited connection: %v", got)
	}
}

// TestHandler_WriteGuardRefusesBeforeTheProviderIsTouched pins that the guard is
// a PRE-flight. A refusal that happened after the mutation reached Keycloak
// would return the same status and none of the protection.
func TestHandler_WriteGuardRefusesBeforeTheProviderIsTouched(t *testing.T) {
	recorder := &recordingRoleProvider{}
	f := newFixture(t, Options{
		Build: func(*connection.Connection, string) (identity.IdentityProvider, error) { return recorder, nil },
	})
	f.conns.active[testWorkspaceID].AccessMode = connection.AccessModeLimited

	h := NewHandler(f.resolver)
	r := mountAll(t, h)

	w := do(r, http.MethodPost, "/v1/workspaces/"+testPublicID+"/roles", `{"name":"billing-admin"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if len(recorder.created) != 0 {
		t.Errorf("the provider was reached despite the write guard: %v", recorder.created)
	}
}

// TestHandler_FullAndUnknownAccessModesPermitWrites — the guard must refuse only
// the mode that was actually shown to be under-privileged.
//
// `unknown` permits writes on purpose: a connection reaches active only by
// passing verification, so unknown means the row predates the field or the
// probe stopped early. Refusing on it would break installations for a signal
// that was never promised.
func TestHandler_FullAndUnknownAccessModesPermitWrites(t *testing.T) {
	for _, mode := range []connection.AccessMode{connection.AccessModeFull, connection.AccessModeUnknown} {
		t.Run(string(mode), func(t *testing.T) {
			recorder := &recordingRoleProvider{}
			f := newFixture(t, Options{
				Build: func(*connection.Connection, string) (identity.IdentityProvider, error) { return recorder, nil },
			})
			f.conns.active[testWorkspaceID].AccessMode = mode

			r := mountAll(t, NewHandler(f.resolver))
			w := do(r, http.MethodPost, "/v1/workspaces/"+testPublicID+"/roles", `{"name":"billing-admin"}`)

			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201 (body %s)", w.Code, w.Body.String())
			}
			if len(recorder.created) != 1 {
				t.Errorf("the mutation did not reach the provider: %v", recorder.created)
			}
		})
	}
}

// TestResolved_CanWrite states the rule directly, so a future edit to it has to
// argue with a test rather than only with a comment.
func TestResolved_CanWrite(t *testing.T) {
	cases := map[connection.AccessMode]bool{
		connection.AccessModeFull:    true,
		connection.AccessModeUnknown: true,
		connection.AccessModeLimited: false,
	}
	for mode, want := range cases {
		if got := (Resolved{AccessMode: mode}).CanWrite(); got != want {
			t.Errorf("AccessMode %q: CanWrite = %v, want %v", mode, got, want)
		}
	}
}

// TestHandler_AccessModeIsReadFreshOnEveryRequest pins that re-verifying a
// connection takes effect immediately.
//
// The provider is cached; the metadata around it is not. An operator who grants
// their service account realm-management roles and re-verifies must be able to
// write straight away, without waiting for a provider rebuild that may never
// come — the cache key does not change when only access_mode does.
func TestHandler_AccessModeIsReadFreshOnEveryRequest(t *testing.T) {
	f, _ := newStubFixture(t)
	f.conns.active[testWorkspaceID].AccessMode = connection.AccessModeLimited
	r := mountAll(t, NewHandler(f.resolver))

	w := do(r, http.MethodPost, "/v1/workspaces/"+testPublicID+"/roles", `{"name":"billing-admin"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 while limited", w.Code)
	}

	// Re-verification promoted it. Same connection row, same cache key, same
	// cached provider — only the mode moved.
	f.conns.mu.Lock()
	f.conns.active[testWorkspaceID].AccessMode = connection.AccessModeFull
	f.conns.mu.Unlock()

	w = do(r, http.MethodPost, "/v1/workspaces/"+testPublicID+"/roles", `{"name":"billing-admin"}`)
	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201 after re-verification (body %s) — "+
			"access mode was cached alongside the provider", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Path-parameter validation
// ---------------------------------------------------------------------------

// TestHandler_MalformedPathIDsGetPreciseCodes. A client that receives
// `invalid_request` for a malformed user id learns nothing about which field
// was wrong; `invalid_user_id` tells it exactly.
func TestHandler_MalformedPathIDsGetPreciseCodes(t *testing.T) {
	f, _ := newStubFixture(t)
	h := NewHandler(f.resolver)
	r := mountAll(t, h)

	cases := []struct {
		name     string
		method   string
		path     string
		body     string
		wantCode string
	}{
		{"user id", http.MethodGet, "/v1/workspaces/" + testPublicID + "/users/not-a-uuid", "", "invalid_user_id"},
		{"user id on patch", http.MethodPatch, "/v1/workspaces/" + testPublicID + "/users/nope", `{}`, "invalid_user_id"},
		{"session id", http.MethodDelete, "/v1/workspaces/" + testPublicID + "/sessions/nope", "", "invalid_session_id"},
		{"role name", http.MethodGet, "/v1/workspaces/" + testPublicID + "/roles/has%20two%20%20spaces!", "", "invalid_role_name"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(r, tc.method, tc.path, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
			}
			if got := decodeError(t, w).Code; got != tc.wantCode {
				t.Errorf("code = %q, want %q", got, tc.wantCode)
			}
		})
	}
}

// TestHandler_MalformedBodyIsInvalidRequest — every route that takes a body
// answers the same way when it will not decode.
func TestHandler_MalformedBodyIsInvalidRequest(t *testing.T) {
	f, _ := newStubFixture(t)
	h := NewHandler(f.resolver)
	r := mountAll(t, h)

	for _, rt := range allWorkspaceIdentityRoutes(h) {
		if rt.body == "" {
			continue
		}
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			w := do(r, rt.method, rt.concretePath(), `{"broken":`)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
			}
			if got := decodeError(t, w).Code; got != "invalid_request" {
				t.Errorf("code = %q, want invalid_request", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Error translation ordering
// ---------------------------------------------------------------------------

// TestTranslate_SpecificSentinelsWinOverTheirBase is the ordering guard.
//
// Three identity sentinels WRAP another one, which is what keeps /admin/*'s
// error mapping working unchanged. The cost is that translateIdentityError's
// switch is order-dependent: check ErrForbidden before ErrProviderForbidden and
// every upstream 403 is reported as if the caller were at fault. Reordering the
// switch compiles, passes vet, and silently sends operators to the wrong system.
func TestTranslate_SpecificSentinelsWinOverTheirBase(t *testing.T) {
	cases := []struct {
		name string
		err  error
		kind resourceKind
		want string
	}{
		{"provider forbidden beats forbidden", identity.ErrProviderForbidden, kindUser, "provider_forbidden"},
		{"role protected beats forbidden", identity.ErrRoleProtected, kindRole, "role_reserved"},
		{"role reserved beats bad request", identity.ErrRoleReserved, kindRole, "role_reserved"},
		{"bare forbidden is the caller", identity.ErrForbidden, kindUser, "caller_forbidden"},
		{"bare bad request", identity.ErrBadRequest, kindUser, "invalid_request"},
		{"not found is per-resource: user", identity.ErrNotFound, kindUser, "user_not_found"},
		{"not found is per-resource: role", identity.ErrNotFound, kindRole, "role_not_found"},
		{"not found is per-resource: session", identity.ErrNotFound, kindSession, "session_not_found"},
		{"not found is per-resource: invitation", identity.ErrNotFound, kindInvitation, "invitation_not_found"},
		{"conflict on a role is a name collision", identity.ErrConflict, kindRole, "role_already_exists"},
		{"conflict elsewhere is generic", identity.ErrConflict, kindUser, "conflict"},
		{"not configured", identity.ErrNotConfigured, kindUser, "workspace_connection_unusable"},
		{"upstream unavailable", identity.ErrAdminAPIUnavailable, kindUser, "provider_unavailable"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := translateIdentityError(tc.err, tc.kind).(*Error)
			if !ok {
				t.Fatalf("translate returned %T, want *Error", got)
			}
			if got.Code != tc.want {
				t.Errorf("code = %q, want %q", got.Code, tc.want)
			}
		})
	}
}

// TestTranslate_WrappedSentinelsStillMatchTheirBase is the other half of the
// contract, asserted from /admin/*'s point of view: the new sentinels must
// remain invisible to code that only knows the originals.
func TestTranslate_WrappedSentinelsStillMatchTheirBase(t *testing.T) {
	if !errors.Is(identity.ErrProviderForbidden, identity.ErrForbidden) {
		t.Error("ErrProviderForbidden no longer matches ErrForbidden — /admin/* would stop returning 403")
	}
	if !errors.Is(identity.ErrRoleProtected, identity.ErrForbidden) {
		t.Error("ErrRoleProtected no longer matches ErrForbidden")
	}
	if !errors.Is(identity.ErrRoleReserved, identity.ErrBadRequest) {
		t.Error("ErrRoleReserved no longer matches ErrBadRequest — /admin/* would stop returning 400")
	}
}

// TestRouteTableMatchesTheServerMounts keeps this package's test route list
// honest against what internal/server actually mounts.
//
// Without it the two drift: a route added to the router but not here is never
// checked for the write guard, and a route here but not in the router is tested
// but unreachable. The comparison is on method+path, which is exactly the pair
// that has to agree.
func TestRouteTableMatchesTheServerMounts(t *testing.T) {
	f, _ := newStubFixture(t)
	h := NewHandler(f.resolver)

	local := make([]string, 0)
	for _, rt := range allWorkspaceIdentityRoutes(h) {
		local = append(local, rt.method+" "+rt.path)
	}
	sort.Strings(local)

	// MountedWorkspaceIdentityRoutes is exported by this package and consumed
	// by internal/server's own test, so the list has exactly one home.
	mounted := append([]string(nil), MountedWorkspaceIdentityRoutes()...)
	sort.Strings(mounted)

	if strings.Join(local, "\n") != strings.Join(mounted, "\n") {
		t.Errorf("the test route list and the declared mount list disagree:\nlocal:\n%s\ndeclared:\n%s",
			strings.Join(local, "\n"), strings.Join(mounted, "\n"))
	}
}
