package identityruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/connection"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
	"github.com/gin-gonic/gin"
)

// The handler tests mount the real routes on a bare engine — no auth, no rate
// limit. Those gates belong to the /v1 group and are pinned in
// internal/server's router tests; duplicating them here would assert the same
// middleware twice and prove it once.

func mountForTest(t *testing.T, h *Handler) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(requestid.Middleware())
	r.GET("/v1/workspaces/:workspace_id/users", h.ListUsers)
	r.GET("/v1/workspaces/:workspace_id/roles", h.ListRoles)
	r.POST("/v1/workspaces/:workspace_id/roles", h.CreateRole)
	return r
}

func do(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decodeError(t *testing.T, w *httptest.ResponseRecorder) ErrorBody {
	t.Helper()
	var out ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode error envelope from %q: %v", w.Body.String(), err)
	}
	return out.Error
}

// ---------------------------------------------------------------------------
// Success
// ---------------------------------------------------------------------------

// TestHandler_ListUsersRoutesThroughTheWorkspacesConnection is Phase 7's
// acceptance in miniature: the workspace id in the path decides which provider
// answers, and the response carries that provider's users.
func TestHandler_ListUsersRoutesThroughTheWorkspacesConnection(t *testing.T) {
	f := newFixture(t, Options{})
	r := mountForTest(t, NewHandler(f.resolver))

	w := do(r, http.MethodGet, "/v1/workspaces/"+testPublicID+"/users", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var out identity.ListUsersResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Count != 1 || out.Users[0].Username != "realm-a-user" {
		t.Errorf("body = %s, want the single user from realm-a", w.Body.String())
	}
}

// TestHandler_PaginationReportsEffectiveValues is the TD-020 fix, asserted on
// both halves of it.
//
// The provider must receive the CLAMPED query — that was already true, since
// identity.Service clamps — and the response must now echo those same clamped
// values rather than the caller's raw input. Slice 4 deliberately reproduced
// /admin/users' raw echo so the two surfaces matched; Slice 5 deliberately
// stops, because a client paginating on the echoed `max` computes wrong
// offsets. /admin/users keeps the old behaviour, pinned by
// TestAdminUsersStillEchoesRawPagination in internal/server.
func TestHandler_PaginationReportsEffectiveValues(t *testing.T) {
	f := newFixture(t, Options{})
	r := mountForTest(t, NewHandler(f.resolver))

	w := do(r, http.MethodGet, "/v1/workspaces/"+testPublicID+"/users?max=9999&first=-5", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	got := f.builder.built[0].query()
	if got.Max != 100 {
		t.Errorf("the provider received max=%d, want 100 — the shared service did not clamp", got.Max)
	}
	if got.First != 0 {
		t.Errorf("the provider received first=%d, want 0 — a negative offset was forwarded", got.First)
	}

	// And the echo reports what was actually used, not what was asked for.
	var out identity.ListUsersResponse
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Max != 100 || out.First != 0 {
		t.Errorf("echo = first %d / max %d, want the EFFECTIVE 0 / 100 (TD-020 is fixed on /v1)",
			out.First, out.Max)
	}
}

// TestHandler_PaginationDefaultsAreReportedToo — the same fix at the other end:
// an omitted `max` must report the default that was applied, not zero.
func TestHandler_PaginationDefaultsAreReportedToo(t *testing.T) {
	f := newFixture(t, Options{})
	r := mountForTest(t, NewHandler(f.resolver))

	w := do(r, http.MethodGet, "/v1/workspaces/"+testPublicID+"/users", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var out identity.ListUsersResponse
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Max != 20 {
		t.Errorf("max = %d, want the applied default of 20 (legacy /admin/users reports 0 here)", out.Max)
	}
}

// TestHandler_ListRolesRoutesThroughTheWorkspacesConnection — the read side of
// the mutation proof.
func TestHandler_ListRolesRoutesThroughTheWorkspacesConnection(t *testing.T) {
	f := newFixture(t, Options{})
	r := mountForTest(t, NewHandler(f.resolver))

	w := do(r, http.MethodGet, "/v1/workspaces/"+testPublicID+"/roles", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "role-realm-a") {
		t.Errorf("body = %s, want realm-a's roles", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Error contract
// ---------------------------------------------------------------------------

// TestHandler_ResolutionFailuresMapToStableCodes walks the catalogue through
// the HTTP surface. The codes are the contract; this is where a reworded
// message stays fine and a renamed code does not.
func TestHandler_ResolutionFailuresMapToStableCodes(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		setup      func(*fixture)
		wantStatus int
		wantCode   string
	}{
		{
			name:       "malformed workspace id",
			path:       "/v1/workspaces/nonsense/users",
			setup:      func(*fixture) {},
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_workspace_id",
		},
		{
			name:       "unknown workspace",
			path:       "/v1/workspaces/" + testPublicID + "/users",
			setup:      func(f *fixture) { f.ws.items = map[string]*workspace.Workspace{} },
			wantStatus: http.StatusNotFound,
			wantCode:   "workspace_not_found",
		},
		{
			name: "archived workspace",
			path: "/v1/workspaces/" + testPublicID + "/users",
			setup: func(f *fixture) {
				f.ws.items[testWorkspaceID].Status = workspace.StatusArchived
			},
			wantStatus: http.StatusConflict,
			wantCode:   "workspace_archived",
		},
		{
			name: "no active connection",
			path: "/v1/workspaces/" + testPublicID + "/users",
			setup: func(f *fixture) {
				f.conns.active = map[string]*connection.Connection{}
			},
			wantStatus: http.StatusConflict,
			wantCode:   "workspace_connection_missing",
		},
		{
			name: "unusable active connection",
			path: "/v1/workspaces/" + testPublicID + "/users",
			setup: func(f *fixture) {
				f.conns.active[testWorkspaceID].ClientID = ""
			},
			wantStatus: http.StatusConflict,
			wantCode:   "workspace_connection_unusable",
		},
		{
			name: "credential cannot be opened",
			path: "/v1/workspaces/" + testPublicID + "/users",
			setup: func(f *fixture) {
				sealed, _ := f.keyring.Seal([]byte("x"), secretAAD("wrong-connection"))
				f.conns.sealed[testConnID] = &sealed
			},
			wantStatus: http.StatusInternalServerError,
			wantCode:   "provider_credentials_unavailable",
		},
		{
			name:       "store failure",
			path:       "/v1/workspaces/" + testPublicID + "/users",
			setup:      func(f *fixture) { f.conns.activeErr = errBoom },
			wantStatus: http.StatusInternalServerError,
			wantCode:   "internal_error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, Options{})
			tc.setup(f)
			r := mountForTest(t, NewHandler(f.resolver))

			w := do(r, http.MethodGet, tc.path, "")
			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", w.Code, tc.wantStatus, w.Body.String())
			}
			if got := decodeError(t, w); got.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", got.Code, tc.wantCode)
			}
		})
	}
}

// TestHandler_ProviderFailuresMapToStableCodes covers the other vocabulary:
// errors raised past the boundary by the provider itself.
func TestHandler_ProviderFailuresMapToStableCodes(t *testing.T) {
	cases := []struct {
		name        string
		providerErr error
		wantStatus  int
		wantCode    string
	}{
		{"upstream unavailable", identity.ErrAdminAPIUnavailable, http.StatusBadGateway, "provider_unavailable"},
		// The two halves of "forbidden", which Slice 5 separated. An upstream
		// 403 is the workspace's service account being refused; a bare
		// ErrForbidden is one of the service's own guards refusing the caller.
		{"service account refused", identity.ErrProviderForbidden, http.StatusConflict, "provider_forbidden"},
		{"caller guard refused", identity.ErrForbidden, http.StatusForbidden, "caller_forbidden"},
		{"user gone", identity.ErrNotFound, http.StatusNotFound, "user_not_found"},
		{"bad request", identity.ErrBadRequest, http.StatusBadRequest, "invalid_request"},
		{"unrecognized", errBoom, http.StatusInternalServerError, "internal_error"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			failing := &failingProvider{err: tc.providerErr}
			f := newFixture(t, Options{
				Build: func(*connection.Connection, string) (identity.IdentityProvider, error) {
					return failing, nil
				},
			})
			r := mountForTest(t, NewHandler(f.resolver))

			w := do(r, http.MethodGet, "/v1/workspaces/"+testPublicID+"/users", "")
			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", w.Code, tc.wantStatus, w.Body.String())
			}
			if got := decodeError(t, w); got.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", got.Code, tc.wantCode)
			}
		})
	}
}

// TestHandler_RawProviderBodiesNeverReachTheClient is the leak rule at the HTTP
// edge.
//
// identity.ErrAdminAPIUnavailable wraps whatever Keycloak returned, and
// Keycloak's error bodies quote realm names, client ids and occasionally the
// URL it was configured with. The /v1 envelope must carry the stable code and
// the catalogue's own literal message — nothing from upstream.
func TestHandler_RawProviderBodiesNeverReachTheClient(t *testing.T) {
	leaky := &failingProvider{
		err: wrapf(identity.ErrAdminAPIUnavailable,
			"upstream HTTP 500: realm=tenant-acme client_secret=hunter2 at http://internal-keycloak:8080"),
	}
	f := newFixture(t, Options{
		Build: func(*connection.Connection, string) (identity.IdentityProvider, error) { return leaky, nil },
	})
	r := mountForTest(t, NewHandler(f.resolver))

	w := do(r, http.MethodGet, "/v1/workspaces/"+testPublicID+"/users", "")
	body := w.Body.String()

	for _, forbidden := range []string{"hunter2", "tenant-acme", "internal-keycloak", "HTTP 500"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("response leaked %q: %s", forbidden, body)
		}
	}
	if got := decodeError(t, w); got.Code != "provider_unavailable" || got.Message != ErrProviderUnavailable.Message {
		t.Errorf("envelope = %+v, want the provider_unavailable literal", got)
	}
}

// TestHandler_ErrorEnvelopeCarriesTheRequestID — the request id is what ties a
// client's internal_error to the log line holding the real cause. Without it
// the leak rule would make failures untraceable rather than merely opaque.
func TestHandler_ErrorEnvelopeCarriesTheRequestID(t *testing.T) {
	f := newFixture(t, Options{})
	f.conns.activeErr = errBoom
	r := mountForTest(t, NewHandler(f.resolver))

	w := do(r, http.MethodGet, "/v1/workspaces/"+testPublicID+"/users", "")
	if got := decodeError(t, w).RequestID; got == "" {
		t.Error("error envelope has no request_id")
	}
}

// ---------------------------------------------------------------------------
// Mutation
// ---------------------------------------------------------------------------

// TestHandler_CreateRoleWritesThroughTheWorkspacesConnection pins that the
// mutation reaches the provider resolved for the workspace in the path, and
// that the created role comes back on the wire.
func TestHandler_CreateRoleWritesThroughTheWorkspacesConnection(t *testing.T) {
	recorder := &recordingRoleProvider{}
	f := newFixture(t, Options{
		Build: func(c *connection.Connection, _ string) (identity.IdentityProvider, error) {
			recorder.realm = c.Realm
			return recorder, nil
		},
	})
	r := mountForTest(t, NewHandler(f.resolver))

	w := do(r, http.MethodPost, "/v1/workspaces/"+testPublicID+"/roles",
		`{"name":"billing-admin","description":"Can refund"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	if recorder.realm != "realm-a" {
		t.Errorf("the role was created against realm %q, want realm-a", recorder.realm)
	}
	if len(recorder.created) != 1 || recorder.created[0] != "billing-admin" {
		t.Errorf("created = %v, want [billing-admin]", recorder.created)
	}
	if !strings.Contains(w.Body.String(), "billing-admin") {
		t.Errorf("body = %s, want the created role", w.Body.String())
	}
}

// TestHandler_CreateRoleRejectsAMalformedBody — a body that will not decode is
// invalid_request, and it must be reported before anything is written.
func TestHandler_CreateRoleRejectsAMalformedBody(t *testing.T) {
	recorder := &recordingRoleProvider{}
	f := newFixture(t, Options{
		Build: func(*connection.Connection, string) (identity.IdentityProvider, error) { return recorder, nil },
	})
	r := mountForTest(t, NewHandler(f.resolver))

	w := do(r, http.MethodPost, "/v1/workspaces/"+testPublicID+"/roles", `{"name":`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := decodeError(t, w).Code; got != "invalid_request" {
		t.Errorf("code = %q, want invalid_request", got)
	}
	if len(recorder.created) != 0 {
		t.Errorf("created %v despite a malformed body", recorder.created)
	}
}

// TestHandler_CreateRoleAppliesTheSharedNameRules — reserved names are refused
// by identity.Service, not by a second copy of the rule here.
func TestHandler_CreateRoleAppliesTheSharedNameRules(t *testing.T) {
	recorder := &recordingRoleProvider{}
	f := newFixture(t, Options{
		Build: func(*connection.Connection, string) (identity.IdentityProvider, error) { return recorder, nil },
	})
	r := mountForTest(t, NewHandler(f.resolver))

	w := do(r, http.MethodPost, "/v1/workspaces/"+testPublicID+"/roles", `{"name":"admin"}`)
	if w.Code == http.StatusCreated {
		t.Fatal("creating the reserved role 'admin' succeeded")
	}
	if len(recorder.created) != 0 {
		t.Errorf("a reserved name reached the provider: %v", recorder.created)
	}
}

// TestHandler_MutationRefusedForAnArchivedWorkspaceBeforeWriting — the archived
// check must gate writes as firmly as reads, and before the provider is built.
func TestHandler_MutationRefusedForAnArchivedWorkspaceBeforeWriting(t *testing.T) {
	recorder := &recordingRoleProvider{}
	f := newFixture(t, Options{
		Build: func(*connection.Connection, string) (identity.IdentityProvider, error) { return recorder, nil },
	})
	f.ws.items[testWorkspaceID].Status = workspace.StatusArchived
	r := mountForTest(t, NewHandler(f.resolver))

	w := do(r, http.MethodPost, "/v1/workspaces/"+testPublicID+"/roles", `{"name":"billing-admin"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if got := decodeError(t, w).Code; got != "workspace_archived" {
		t.Errorf("code = %q, want workspace_archived", got)
	}
	if len(recorder.created) != 0 {
		t.Errorf("wrote %v to an archived workspace's realm", recorder.created)
	}
}

// ---------------------------------------------------------------------------
// doubles
// ---------------------------------------------------------------------------

type failingProvider struct {
	identity.IdentityProvider
	err error
}

func (p *failingProvider) ListUsers(context.Context, identity.ListUsersQuery) ([]identity.User, error) {
	return nil, p.err
}

type recordingRoleProvider struct {
	identity.IdentityProvider
	realm   string
	created []string
}

func (p *recordingRoleProvider) CreateRole(_ context.Context, req identity.CreateRoleRequest) (*identity.Role, error) {
	p.created = append(p.created, req.Name)
	return &identity.Role{ID: "r1", Name: req.Name, Description: req.Description}, nil
}

func (p *recordingRoleProvider) ListRoles(context.Context) ([]identity.Role, error) {
	out := make([]identity.Role, 0, len(p.created))
	for _, n := range p.created {
		out = append(out, identity.Role{Name: n})
	}
	return out, nil
}

// wrapf builds an error that wraps sentinel and carries extra text, mirroring
// how internal/identity/keycloak reports upstream failures.
func wrapf(sentinel error, msg string) error {
	return &wrapped{sentinel: sentinel, msg: msg}
}

type wrapped struct {
	sentinel error
	msg      string
}

func (w *wrapped) Error() string { return w.sentinel.Error() + ": " + w.msg }
func (w *wrapped) Unwrap() error { return w.sentinel }

var _ = time.Now
