// Tests for v0.2 Stage B (CREATE) against an httptest stub of the Keycloak
// Admin API surface. The stub from stage_a_test.go is reused for plain
// status+body responses; CreateInvitation needs Location-header support so
// we re-wire the stub server inline for those tests.

package keycloak

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
)

// ─────────────── CreateRole ───────────────

func TestCreateRole_Success_ReFetchesAfterCreate(t *testing.T) {
	var postCalls atomic.Int32
	var lastPostBody string

	// CreateRole uses doCreate which requires a Location header on 201,
	// so we use captureKeycloak (which supports header injection) rather
	// than the simpler stageAStub.on() helper.
	s := captureKeycloak(t, map[string]func(r *http.Request) (int, string, string){
		"POST /admin/realms/saas/roles": func(r *http.Request) (int, string, string) {
			postCalls.Add(1)
			buf := make([]byte, 1024)
			n, _ := r.Body.Read(buf)
			lastPostBody = string(buf[:n])
			return 201, "", "/admin/realms/saas/roles-by-id/r-support"
		},
		"GET /admin/realms/saas/roles/support": func(*http.Request) (int, string, string) {
			return 200, `{"id":"r-support","name":"support","description":"Support team","composite":false}`, ""
		},
	})
	p := newStageAProvider(t, s)

	role, err := p.CreateRole(context.Background(), identity.CreateRoleRequest{
		Name:        "support",
		Description: "Support team",
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if postCalls.Load() != 1 {
		t.Errorf("expected exactly one POST /roles, got %d", postCalls.Load())
	}
	if !strings.Contains(lastPostBody, `"name":"support"`) {
		t.Errorf("POST body should carry the role name; got %s", lastPostBody)
	}
	if role.ID != "r-support" || role.Name != "support" {
		t.Errorf("re-fetched role wrong: %+v", role)
	}
	if role.Composite {
		t.Errorf("composite flag passthrough lost")
	}
}

func TestCreateRole_409_MapsToConflict(t *testing.T) {
	s := newStageAStub(t)
	s.on("POST", "/admin/realms/saas/roles", func(*http.Request) (int, string) {
		return 409, `{"errorMessage":"Role with name support already exists"}`
	})
	p := newStageAProvider(t, s)

	_, err := p.CreateRole(context.Background(), identity.CreateRoleRequest{Name: "support"})
	if !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestCreateRole_500_MapsToAdminAPIUnavailable(t *testing.T) {
	s := newStageAStub(t)
	s.on("POST", "/admin/realms/saas/roles", func(*http.Request) (int, string) {
		return 502, ``
	})
	p := newStageAProvider(t, s)

	_, err := p.CreateRole(context.Background(), identity.CreateRoleRequest{Name: "support"})
	if !errors.Is(err, identity.ErrAdminAPIUnavailable) {
		t.Fatalf("expected ErrAdminAPIUnavailable, got %v", err)
	}
}

// ─────────────── CreateInvitation ───────────────

// captureKeycloak builds a full Keycloak fixture that responds correctly to
// every endpoint the CreateInvitation chain hits. Each test customizes one
// or two responses via the override hook. We re-wire the stub server inline
// (instead of reusing stageAStub.on) so the response can include a Location
// header — the 201/Location pattern is core to the doCreate path.
func captureKeycloak(t *testing.T, overrides map[string]func(r *http.Request) (status int, body string, locationHeader string)) *stageAStub {
	t.Helper()
	s := newStageAStub(t)

	defaults := map[string]func(r *http.Request) (int, string, string){
		"GET /admin/realms/saas/roles/user": func(r *http.Request) (int, string, string) {
			return 200, `{"id":"r-user","name":"user","composite":false}`, ""
		},
		"POST /admin/realms/saas/users": func(r *http.Request) (int, string, string) {
			return 201, "", "/admin/realms/saas/users/u-new"
		},
		"POST /admin/realms/saas/users/u-new/role-mappings/realm": func(r *http.Request) (int, string, string) {
			return 204, "", ""
		},
		"PUT /admin/realms/saas/users/u-new/execute-actions-email": func(r *http.Request) (int, string, string) {
			return 204, "", ""
		},
		"GET /admin/realms/saas/users/u-new": func(r *http.Request) (int, string, string) {
			return 200, `{"id":"u-new","username":"jane@example.com","email":"jane@example.com","enabled":true,"emailVerified":false,"requiredActions":["VERIFY_EMAIL","UPDATE_PASSWORD"],"attributes":{"invited_by":["adminuser"],"expires_at":["2030-01-01T00:00:00Z"]}}`, ""
		},
	}
	for k, v := range overrides {
		defaults[k] = v
	}

	// Replace the stub's HTTP handler so we can set the Location header.
	s.srv.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":300}`))
			return
		}
		key := r.Method + " " + r.URL.Path
		fn, ok := defaults[key]
		if !ok {
			t.Logf("captureKeycloak miss: %s", key)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		status, body, location := fn(r)
		if location != "" {
			w.Header().Set("Location", location)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func TestCreateInvitation_Success_FullChain(t *testing.T) {
	var postUserBody, putEmailBody, roleMapBody string
	s := captureKeycloak(t, map[string]func(r *http.Request) (int, string, string){
		"POST /admin/realms/saas/users": func(r *http.Request) (int, string, string) {
			buf := make([]byte, 4096)
			n, _ := r.Body.Read(buf)
			postUserBody = string(buf[:n])
			return 201, "", "/admin/realms/saas/users/u-new"
		},
		"POST /admin/realms/saas/users/u-new/role-mappings/realm": func(r *http.Request) (int, string, string) {
			buf := make([]byte, 1024)
			n, _ := r.Body.Read(buf)
			roleMapBody = string(buf[:n])
			return 204, "", ""
		},
		"PUT /admin/realms/saas/users/u-new/execute-actions-email": func(r *http.Request) (int, string, string) {
			buf := make([]byte, 1024)
			n, _ := r.Body.Read(buf)
			putEmailBody = string(buf[:n])
			return 204, "", ""
		},
	})
	p := newStageAProvider(t, s)

	inv, err := p.CreateInvitation(context.Background(), identity.CreateInvitationRequest{
		Email:     "jane@example.com",
		FirstName: "Jane",
		LastName:  "Doe",
		Roles:     []string{"user"},
		ExpiresAt: "2030-01-01T00:00:00Z",
		InvitedBy: "adminuser",
	})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}

	// The user create body must include enabled=true, requiredActions, and
	// the user attributes for invited_by + expires_at.
	for _, must := range []string{
		`"email":"jane@example.com"`,
		`"firstName":"Jane"`,
		`"enabled":true`,
		`"emailVerified":false`,
		`VERIFY_EMAIL`,
		`UPDATE_PASSWORD`,
		`"invited_by":["adminuser"]`,
		`"expires_at":["2030-01-01T00:00:00Z"]`,
	} {
		if !strings.Contains(postUserBody, must) {
			t.Errorf("POST /users body missing %q\nfull body: %s", must, postUserBody)
		}
	}

	// The role-mapping payload must carry both id and name (Keycloak requires both).
	if !strings.Contains(roleMapBody, `"id":"r-user"`) || !strings.Contains(roleMapBody, `"name":"user"`) {
		t.Errorf("role-mapping body missing required fields: %s", roleMapBody)
	}

	// execute-actions-email body must be the JSON array of action names.
	if !strings.Contains(putEmailBody, "VERIFY_EMAIL") || !strings.Contains(putEmailBody, "UPDATE_PASSWORD") {
		t.Errorf("execute-actions-email body missing actions: %s", putEmailBody)
	}

	// Returned invitation should reflect the GET response.
	if inv.ID != "u-new" || inv.Email != "jane@example.com" || inv.Status != "pending" {
		t.Errorf("invitation result wrong: %+v", inv)
	}
	if inv.InvitedBy != "adminuser" {
		t.Errorf("invited_by not surfaced from attributes: %q", inv.InvitedBy)
	}
}

func TestCreateInvitation_RoleNotFound_NoUserCreated(t *testing.T) {
	var postUserCalled atomic.Bool
	s := captureKeycloak(t, map[string]func(r *http.Request) (int, string, string){
		"GET /admin/realms/saas/roles/typo": func(*http.Request) (int, string, string) {
			return 404, `{"error":"not found"}`, ""
		},
		"POST /admin/realms/saas/users": func(*http.Request) (int, string, string) {
			postUserCalled.Store(true)
			return 201, "", "/admin/realms/saas/users/should-not-happen"
		},
	})
	p := newStageAProvider(t, s)

	_, err := p.CreateInvitation(context.Background(), identity.CreateInvitationRequest{
		Email: "jane@example.com",
		Roles: []string{"typo"},
	})
	if !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if postUserCalled.Load() {
		t.Errorf("POST /users must NOT fire when a requested role doesn't exist")
	}
}

func TestCreateInvitation_DuplicateEmail_409(t *testing.T) {
	s := captureKeycloak(t, map[string]func(r *http.Request) (int, string, string){
		"POST /admin/realms/saas/users": func(*http.Request) (int, string, string) {
			return 409, `{"errorMessage":"User exists with same email"}`, ""
		},
	})
	p := newStageAProvider(t, s)

	_, err := p.CreateInvitation(context.Background(), identity.CreateInvitationRequest{
		Email: "existing@example.com",
		Roles: []string{"user"},
	})
	if !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestCreateInvitation_NoLocationHeader_Errors(t *testing.T) {
	s := captureKeycloak(t, map[string]func(r *http.Request) (int, string, string){
		"POST /admin/realms/saas/users": func(*http.Request) (int, string, string) {
			// 201 but no Location header — Keycloak contract violation
			return 201, "", ""
		},
	})
	p := newStageAProvider(t, s)

	_, err := p.CreateInvitation(context.Background(), identity.CreateInvitationRequest{
		Email: "jane@example.com",
		Roles: []string{"user"},
	})
	if err == nil {
		t.Fatalf("expected error when Location header is absent")
	}
	if !errors.Is(err, identity.ErrAdminAPIUnavailable) {
		t.Errorf("expected ErrAdminAPIUnavailable, got %v", err)
	}
}

// ─────────────── Reliability: compensating delete ───────────────
//
// If CreateInvitation gets past the POST /users step but fails on either
// the role-mapping POST or the execute-actions-email PUT, the provider
// must best-effort DELETE the half-provisioned user. Without this, every
// retry with the same email returns 409 Conflict from Keycloak because
// the orphan still exists.

func TestCreateInvitation_RoleMappingFails_CompensatesDelete(t *testing.T) {
	var deleteCalled atomic.Bool
	s := captureKeycloak(t, map[string]func(r *http.Request) (int, string, string){
		"POST /admin/realms/saas/users/u-new/role-mappings/realm": func(*http.Request) (int, string, string) {
			return 500, ``, ""
		},
		"DELETE /admin/realms/saas/users/u-new": func(*http.Request) (int, string, string) {
			deleteCalled.Store(true)
			return 204, "", ""
		},
	})
	p := newStageAProvider(t, s)

	_, err := p.CreateInvitation(context.Background(), identity.CreateInvitationRequest{
		Email: "jane@example.com",
		Roles: []string{"user"},
	})
	if err == nil {
		t.Fatalf("expected error when role-mapping fails")
	}
	if !errors.Is(err, identity.ErrAdminAPIUnavailable) {
		t.Errorf("expected ErrAdminAPIUnavailable, got %v", err)
	}
	if !deleteCalled.Load() {
		t.Errorf("compensating DELETE /users/u-new must fire when role-mapping fails after user creation")
	}
}

func TestCreateInvitation_EmailDispatchFails_CompensatesDelete(t *testing.T) {
	var deleteCalled atomic.Bool
	s := captureKeycloak(t, map[string]func(r *http.Request) (int, string, string){
		// SMTP misconfigured → Keycloak returns 500.
		"PUT /admin/realms/saas/users/u-new/execute-actions-email": func(*http.Request) (int, string, string) {
			return 500, ``, ""
		},
		"DELETE /admin/realms/saas/users/u-new": func(*http.Request) (int, string, string) {
			deleteCalled.Store(true)
			return 204, "", ""
		},
	})
	p := newStageAProvider(t, s)

	_, err := p.CreateInvitation(context.Background(), identity.CreateInvitationRequest{
		Email: "jane@example.com",
		Roles: []string{"user"},
	})
	if err == nil {
		t.Fatalf("expected error when email dispatch fails")
	}
	if !errors.Is(err, identity.ErrAdminAPIUnavailable) {
		t.Errorf("expected ErrAdminAPIUnavailable, got %v", err)
	}
	if !deleteCalled.Load() {
		t.Errorf("compensating DELETE /users/u-new must fire when email dispatch fails after user creation")
	}
}

func TestCreateInvitation_FinalGetFails_StillReturnsInvitation(t *testing.T) {
	// The user IS provisioned (create + roles + email all succeeded). The
	// trailing GET is informational — a cosmetic failure must NOT roll
	// back the invitation (the email is already in the user's inbox).
	var deleteCalled atomic.Bool
	s := captureKeycloak(t, map[string]func(r *http.Request) (int, string, string){
		"GET /admin/realms/saas/users/u-new": func(*http.Request) (int, string, string) {
			return 500, ``, ""
		},
		"DELETE /admin/realms/saas/users/u-new": func(*http.Request) (int, string, string) {
			deleteCalled.Store(true)
			return 204, "", ""
		},
	})
	p := newStageAProvider(t, s)

	inv, err := p.CreateInvitation(context.Background(), identity.CreateInvitationRequest{
		Email:     "jane@example.com",
		Roles:     []string{"user"},
		InvitedBy: "adminuser",
	})
	if err != nil {
		t.Fatalf("CreateInvitation must succeed when only the trailing GET fails, got %v", err)
	}
	if inv == nil || inv.ID != "u-new" || inv.Email != "jane@example.com" {
		t.Errorf("synthesized invitation lost request fields: %+v", inv)
	}
	if inv.Status != identity.InvitationStatusPending {
		t.Errorf("synthesized invitation should be pending, got %q", inv.Status)
	}
	if deleteCalled.Load() {
		t.Errorf("compensating DELETE must NOT fire after the invitation is fully provisioned")
	}
}

func TestCreateInvitation_Success_NoCompensatingDelete(t *testing.T) {
	// Sanity check: the happy path must not invoke DELETE.
	var deleteCalled atomic.Bool
	s := captureKeycloak(t, map[string]func(r *http.Request) (int, string, string){
		"DELETE /admin/realms/saas/users/u-new": func(*http.Request) (int, string, string) {
			deleteCalled.Store(true)
			return 204, "", ""
		},
	})
	p := newStageAProvider(t, s)

	if _, err := p.CreateInvitation(context.Background(), identity.CreateInvitationRequest{
		Email: "jane@example.com",
		Roles: []string{"user"},
	}); err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if deleteCalled.Load() {
		t.Errorf("compensating DELETE must NOT fire on the happy path")
	}
}
