package authz

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

const (
	wsA = "ws_aaaaaaaa-0000-4000-8000-000000000001"
	wsB = "ws_bbbbbbbb-0000-4000-8000-000000000002"
)

type fakeAdminChecker struct {
	allow bool
	err   error
	calls int
}

func (f *fakeAdminChecker) IsAdmin(context.Context, string) (bool, error) {
	f.calls++
	return f.allow, f.err
}

func projectPrincipal(workspace string, scopes ...string) *auth.Principal {
	return auth.NewProjectPrincipal(&auth.ProjectPrincipal{
		ProjectID:    "prj_7c9e6679-7425-40de-944b-e07fc1f90ae7",
		ProjectName:  "Billing worker",
		CredentialID: "key_9b2f4c1a-1111-4222-8333-444455556666",
		WorkspaceID:  workspace,
		Scopes:       scopes,
	})
}

func operatorPrincipal(roles ...string) *auth.Principal {
	return auth.NewOperatorPrincipal(&auth.Identity{Subject: "operator-1", Roles: roles})
}

func bodyCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var got ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not the /v1 envelope: %s", w.Body.String())
	}
	return got.Error.Code
}

// ─── The workspace boundary ─────────────────────────────────────────────────

func TestAuthorize_ProjectReachesItsOwnWorkspace(t *testing.T) {
	reached := false
	r := buildRouter(Config{}, projectPrincipal(wsA, "users:read"),
		"GET", "/v1/workspaces/:workspace_id/users", &reached)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/v1/workspaces/"+wsA+"/users", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !reached {
		t.Fatal("an authorized project did not reach the handler")
	}
}

// TestAuthorize_WrongWorkspaceIsRefusedBeforeTheHandler is the central property
// of this slice. The handler is where the resolver, the connection, the sealed
// secret and the provider all live, so "the handler was never reached" is the
// executable form of "workspace B was never touched".
func TestAuthorize_WrongWorkspaceIsRefusedBeforeTheHandler(t *testing.T) {
	reached := false
	r := buildRouter(Config{}, projectPrincipal(wsA, "users:read"),
		"GET", "/v1/workspaces/:workspace_id/users", &reached)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/v1/workspaces/"+wsB+"/users", nil))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if got := bodyCode(t, w); got != ErrWorkspaceMismatch.Code {
		t.Errorf("code = %q, want %q", got, ErrWorkspaceMismatch.Code)
	}
	if reached {
		t.Fatal("the handler ran for a workspace the credential is not bound to; " +
			"everything downstream of here loads workspace B and its provider")
	}
}

// TestAuthorize_NonexistentWorkspaceIsIndistinguishable — the check never
// consults the database, so a workspace that does not exist produces exactly
// the same response as one that does. Nothing about the installation leaks.
func TestAuthorize_NonexistentWorkspaceIsIndistinguishable(t *testing.T) {
	const ghost = "ws_99999999-0000-4000-8000-000000000009"

	responses := map[string]*httptest.ResponseRecorder{}
	for name, target := range map[string]string{"existing-other": wsB, "nonexistent": ghost} {
		reached := false
		r := buildRouter(Config{}, projectPrincipal(wsA, "users:read"),
			"GET", "/v1/workspaces/:workspace_id/users", &reached)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/v1/workspaces/"+target+"/users", nil))
		responses[name] = w
		if reached {
			t.Fatalf("%s reached the handler", name)
		}
	}

	a, b := responses["existing-other"], responses["nonexistent"]
	if a.Code != b.Code {
		t.Errorf("status differs: %d vs %d", a.Code, b.Code)
	}
	if stripRequestID(t, a) != stripRequestID(t, b) {
		t.Errorf("body differs:\n  %s\n  %s", a.Body.String(), b.Body.String())
	}
}

func TestAuthorize_BareUUIDAndPrefixedFormAgree(t *testing.T) {
	// publicid.Parse accepts both spellings, so the binding must too, or a
	// curl user copying an id out of psql would be refused for a spelling.
	const bare = "aaaaaaaa-0000-4000-8000-000000000001"

	reached := false
	r := buildRouter(Config{}, projectPrincipal(wsA, "users:read"),
		"GET", "/v1/workspaces/:workspace_id/users", &reached)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/v1/workspaces/"+bare+"/users", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
}

func TestAuthorize_MalformedWorkspaceIdIsRefusedAsMismatch(t *testing.T) {
	// For a project the answer is the same either way, and reporting
	// invalid_workspace_id here would let a credential probe which ids are
	// well-formed before authorization ran.
	reached := false
	r := buildRouter(Config{}, projectPrincipal(wsA, "users:read"),
		"GET", "/v1/workspaces/:workspace_id/users", &reached)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/v1/workspaces/not-an-id/users", nil))

	if w.Code != http.StatusForbidden || bodyCode(t, w) != ErrWorkspaceMismatch.Code {
		t.Errorf("status=%d code=%q, want 403 workspace_mismatch", w.Code, bodyCode(t, w))
	}
	if reached {
		t.Error("handler reached with a malformed workspace id")
	}
}

// ─── Scope ──────────────────────────────────────────────────────────────────

func TestAuthorize_MissingScopeIsRefusedBeforeTheHandler(t *testing.T) {
	reached := false
	r := buildRouter(Config{}, projectPrincipal(wsA, "users:read"),
		"POST", "/v1/workspaces/:workspace_id/invitations", &reached)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/workspaces/"+wsA+"/invitations", nil))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if got := bodyCode(t, w); got != ErrInsufficientScope.Code {
		t.Errorf("code = %q, want %q", got, ErrInsufficientScope.Code)
	}
	if reached {
		t.Fatal("the handler ran without the required scope; a write would have reached the provider")
	}
	// RFC 6750 §3.1: naming the scope is the difference between a developer
	// fixing their key in a minute and filing a ticket.
	if hdr := w.Header().Get("WWW-Authenticate"); hdr != `Bearer error="insufficient_scope", scope="invitations:write"` {
		t.Errorf("WWW-Authenticate = %q", hdr)
	}
}

// TestAuthorize_BindingIsCheckedBeforeScope — a credential probing another
// workspace must learn only "not yours", never which scopes would have been
// needed there.
func TestAuthorize_BindingIsCheckedBeforeScope(t *testing.T) {
	reached := false
	r := buildRouter(Config{}, projectPrincipal(wsA /* no scopes at all */),
		"POST", "/v1/workspaces/:workspace_id/invitations", &reached)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/workspaces/"+wsB+"/invitations", nil))

	if got := bodyCode(t, w); got != ErrWorkspaceMismatch.Code {
		t.Errorf("code = %q, want %q — the binding must be reported before the scope", got, ErrWorkspaceMismatch.Code)
	}
	if w.Header().Get("WWW-Authenticate") != "" {
		t.Error("a cross-workspace probe was told which scope the route requires")
	}
}

// ─── Operator-only ──────────────────────────────────────────────────────────

func TestAuthorize_ProjectCannotReachTheControlPlane(t *testing.T) {
	// Every scope in the vocabulary, and still refused.
	all := ScopeStrings(AllScopes())

	for _, route := range []struct{ method, pattern, url string }{
		{"GET", "/v1/workspaces", "/v1/workspaces"},
		{"POST", "/v1/workspaces", "/v1/workspaces"},
		{"POST", "/v1/workspaces/:workspace_id/archive", "/v1/workspaces/" + wsA + "/archive"},
		{"GET", "/v1/workspaces/:workspace_id/connections", "/v1/workspaces/" + wsA + "/connections"},
		{"POST", "/v1/workspaces/:workspace_id/connections", "/v1/workspaces/" + wsA + "/connections"},
		{"GET", "/v1/workspaces/:workspace_id/projects", "/v1/workspaces/" + wsA + "/projects"},
		{"POST", "/v1/workspaces/:workspace_id/projects", "/v1/workspaces/" + wsA + "/projects"},
		{"PUT", "/v1/workspaces/:workspace_id/users/:user_id/password",
			"/v1/workspaces/" + wsA + "/users/9c1e6679-7425-40de-944b-e07fc1f90ae7/password"},
	} {
		t.Run(route.method+" "+route.pattern, func(t *testing.T) {
			reached := false
			r := buildRouter(Config{}, projectPrincipal(wsA, all...), route.method, route.pattern, &reached)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(route.method, route.url, nil))

			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", w.Code)
			}
			if got := bodyCode(t, w); got != ErrOperatorOnly.Code {
				t.Errorf("code = %q, want %q — no scope can satisfy this, so telling a developer "+
					"to add one would send them looking for a scope that does not exist", got, ErrOperatorOnly.Code)
			}
			if reached {
				t.Fatal("a project credential reached a control-plane handler")
			}
		})
	}
}

// ─── Operator path: unchanged behaviour ─────────────────────────────────────

func TestAuthorize_OperatorNeedsTheAdminRole(t *testing.T) {
	checker := &fakeAdminChecker{allow: true}
	reached := false
	r := buildRouter(Config{AdminChecker: checker}, operatorPrincipal("viewer"),
		"GET", "/v1/workspaces", &reached)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/v1/workspaces", nil))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	// The cheap claim check must short-circuit before the round trip, exactly
	// as the pre-Slice-7 chain did.
	if checker.calls != 0 {
		t.Errorf("live admin checker ran after a claim denial (%d calls)", checker.calls)
	}
	if reached {
		t.Error("handler reached without the admin role")
	}
}

func TestAuthorize_OperatorLiveAdminDenial(t *testing.T) {
	checker := &fakeAdminChecker{allow: false}
	reached := false
	r := buildRouter(Config{AdminChecker: checker}, operatorPrincipal("admin"),
		"GET", "/v1/workspaces", &reached)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/v1/workspaces", nil))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (the GAP-1 stale-token path)", w.Code)
	}
	if checker.calls != 1 {
		t.Errorf("checker calls = %d, want 1", checker.calls)
	}
	if reached {
		t.Error("a stale admin token reached the handler")
	}
}

func TestAuthorize_OperatorLiveAdminFailureFailsClosed(t *testing.T) {
	checker := &fakeAdminChecker{err: context.DeadlineExceeded}
	reached := false
	r := buildRouter(Config{AdminChecker: checker}, operatorPrincipal("admin"),
		"GET", "/v1/workspaces", &reached)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/v1/workspaces", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — an admin verb must never run on a guess", w.Code)
	}
	if reached {
		t.Error("handler reached while authorization was undecidable")
	}
}

func TestAuthorize_OperatorPassesEveryClassification(t *testing.T) {
	// An operator is authorized for the whole surface by being a live realm
	// admin; scopes describe what a MACHINE may do. Giving operators a second,
	// parallel permission model would be a new RBAC system.
	checker := &fakeAdminChecker{allow: true}

	for _, route := range []struct{ method, pattern, url string }{
		{"GET", "/v1/workspaces", "/v1/workspaces"},
		{"GET", "/v1/workspaces/:workspace_id/users", "/v1/workspaces/" + wsA + "/users"},
		{"PUT", "/v1/workspaces/:workspace_id/users/:user_id/password",
			"/v1/workspaces/" + wsA + "/users/9c1e6679-7425-40de-944b-e07fc1f90ae7/password"},
		{"POST", "/v1/workspaces/:workspace_id/projects", "/v1/workspaces/" + wsA + "/projects"},
	} {
		t.Run(route.method+" "+route.pattern, func(t *testing.T) {
			reached := false
			r := buildRouter(Config{AdminChecker: checker}, operatorPrincipal("admin"),
				route.method, route.pattern, &reached)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(route.method, route.url, nil))

			if w.Code != http.StatusOK || !reached {
				t.Errorf("status = %d reached = %v, want 200 and reached", w.Code, reached)
			}
		})
	}
}

// ─── Fail-closed defaults ───────────────────────────────────────────────────

func TestAuthorize_UnclassifiedRouteIsDenied(t *testing.T) {
	// The runtime half of the registry guarantee. The boot check should make
	// this unreachable; if a route somehow escapes it, the answer is a denial,
	// not an open door.
	reached := false
	r := buildRouter(Config{}, projectPrincipal(wsA, "users:read"),
		"GET", "/v1/workspaces/:workspace_id/not-registered", &reached)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/v1/workspaces/"+wsA+"/not-registered", nil))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for an unclassified route", w.Code)
	}
	if reached {
		t.Fatal("an unclassified route reached its handler")
	}
}

func TestAuthorize_NoPrincipalIsDenied(t *testing.T) {
	reached := false
	r := buildRouter(Config{}, nil, "GET", "/v1/workspaces", &reached)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/v1/workspaces", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if reached {
		t.Error("handler reached with no principal")
	}
}

func TestRequireOperator(t *testing.T) {
	for name, tc := range map[string]struct {
		principal *auth.Principal
		want      int
	}{
		"operator": {operatorPrincipal("admin"), http.StatusOK},
		"project":  {projectPrincipal(wsA, "users:read"), http.StatusForbidden},
		"none":     {nil, http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			r := gin.New()
			r.Use(func(c *gin.Context) {
				if tc.principal != nil {
					auth.StorePrincipal(c, tc.principal)
				}
				c.Next()
			})
			r.Use(RequireOperator())
			r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
			if w.Code != tc.want {
				t.Errorf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

// ─── helpers ────────────────────────────────────────────────────────────────

func buildRouter(cfg Config, principal *auth.Principal, method, pattern string, reached *bool) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if principal != nil {
			auth.StorePrincipal(c, principal)
		}
		c.Next()
	})
	r.Use(Authorize(cfg))
	r.Handle(method, pattern, func(c *gin.Context) {
		*reached = true
		c.Status(http.StatusOK)
	})
	return r
}

// stripRequestID removes the correlation id so two responses can be compared
// for everything else. The id differs per request by design.
func stripRequestID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var got ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("not the /v1 envelope: %s", w.Body.String())
	}
	got.Error.RequestID = ""
	out, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(out)
}
