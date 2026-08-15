package identityruntime

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/gin-gonic/gin"
)

// The privilege guard closes a gap the identity service does not cover.
//
// identity.Service refuses reserved role names on CREATE, UPDATE and DELETE for
// every caller. It does not refuse role ASSIGNMENT, and deliberately so: an
// operator reaching that endpoint is already a live realm admin, and granting
// admin is a normal thing for them to do.
//
// A project credential is a different principal on the same endpoint. Without
// the guard, `roles:write` would be an escalation path — grant `admin` in the
// workspace's realm to a user the backend controls, and in the ordinary
// single-realm deployment that is a grant of console-operator privilege.

func guardContext(t *testing.T, principal *auth.Principal) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/workspaces/ws_x/users/u/roles", nil)
	if principal != nil {
		auth.StorePrincipal(c, principal)
	}
	return c, w
}

func projectPrincipal(scopes ...string) *auth.Principal {
	return auth.NewProjectPrincipal(&auth.ProjectPrincipal{
		ProjectID:    "prj_1",
		CredentialID: "key_1",
		WorkspaceID:  "ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		Scopes:       scopes,
	})
}

// TestGuardPrivilegedRoles_ProjectCannotTouchProtectedRoles is the escalation
// test. Every name here is one that, granted, would hand out realm
// administration or fight with Keycloak internals.
func TestGuardPrivilegedRoles_ProjectCannotTouchProtectedRoles(t *testing.T) {
	h := &Handler{}

	for _, name := range []string{
		"admin",
		"ADMIN",   // normalization must not be a bypass
		" admin ", // nor whitespace
		"user",
		"offline_access",
		"uma_authorization",
		"default-roles-saas",
	} {
		t.Run(name, func(t *testing.T) {
			c, w := guardContext(t, projectPrincipal("roles:write"))

			if h.guardPrivilegedRoles(c, name) {
				t.Fatalf("a project credential was allowed to touch the protected role %q", name)
			}
			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", w.Code)
			}
			if body := w.Body.String(); !strings.Contains(body, ErrRolePrivileged.Code) {
				t.Errorf("body %s does not carry %q", body, ErrRolePrivileged.Code)
			}
		})
	}
}

// TestGuardPrivilegedRoles_ProjectMayTouchOrdinaryRoles — the guard is a bound
// on roles:write, not a repeal of it. A backend that models per-tenant roles
// must still be able to manage them.
func TestGuardPrivilegedRoles_ProjectMayTouchOrdinaryRoles(t *testing.T) {
	h := &Handler{}

	for _, name := range []string{"editor", "billing-admin", "support", "tenant.owner"} {
		t.Run(name, func(t *testing.T) {
			c, w := guardContext(t, projectPrincipal("roles:write"))

			if !h.guardPrivilegedRoles(c, name) {
				t.Fatalf("an ordinary role %q was refused; roles:write would grant nothing usable", name)
			}
			if w.Code != http.StatusOK && w.Body.Len() != 0 {
				t.Errorf("the guard wrote a response while allowing the request: %s", w.Body.String())
			}
		})
	}
}

// TestGuardPrivilegedRoles_ChecksEveryNameInABatch — a grant carries a list,
// and one protected name anywhere in it must refuse the whole request. Checking
// only the first would make the bypass a matter of ordering.
func TestGuardPrivilegedRoles_ChecksEveryNameInABatch(t *testing.T) {
	h := &Handler{}
	c, _ := guardContext(t, projectPrincipal("roles:write"))

	if h.guardPrivilegedRoles(c, "editor", "support", "admin") {
		t.Fatal("a protected role hidden at the end of a batch was allowed")
	}
}

// TestGuardPrivilegedRoles_OperatorIsUnaffected — operator behaviour must be
// byte-identical to before this slice. Their protections are the service's own
// (no self-strip of admin, no removing the realm's last admin), which are
// unchanged.
func TestGuardPrivilegedRoles_OperatorIsUnaffected(t *testing.T) {
	h := &Handler{}
	operator := auth.NewOperatorPrincipal(&auth.Identity{Subject: "op-1", Roles: []string{"admin"}})

	c, w := guardContext(t, operator)
	if !h.guardPrivilegedRoles(c, "admin") {
		t.Fatal("an operator was refused a role grant they have always been allowed to make")
	}
	if w.Body.Len() != 0 {
		t.Errorf("the guard wrote a response for an operator: %s", w.Body.String())
	}
}

// TestGuardPrivilegedRoles_NoPrincipalIsUnaffected covers the legacy /admin/*
// shape, where no principal is stored at all.
func TestGuardPrivilegedRoles_NoPrincipalIsUnaffected(t *testing.T) {
	h := &Handler{}
	c, _ := guardContext(t, nil)

	if !h.guardPrivilegedRoles(c, "admin") {
		t.Fatal("the guard fired without a principal; it would change /admin/* behaviour")
	}
}
