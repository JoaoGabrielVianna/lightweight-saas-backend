package project

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/authz"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
	"github.com/gin-gonic/gin"
)

// HTTP-level tests. What the service decides is service_test.go's business;
// what matters here is the wire contract — status codes, the response shapes,
// what the secret does and does not appear in, and what reaches the audit trail.

func init() { gin.SetMode(gin.TestMode) }

// newHandlerHarness wires a real service over the in-memory repository and
// mounts the routes exactly as the router does, including the operator identity
// the handlers read for created_by/revoked_by attribution.
func newHandlerHarness(t *testing.T) (*gin.Engine, *fakeRepo) {
	t.Helper()

	repo := newFakeRepo()
	workspaces := newFakeWorkspaces()
	workspaces.add(testWorkspaceUUID, workspace.StatusActive)

	h := NewHandler(NewService(repo, workspaces, &fakeRunner{}, &fakeAuditWriter{}))
	if h == nil {
		t.Fatal("NewHandler returned nil with a service present")
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		id := &auth.Identity{Subject: "operator-sub", Email: "op@example.test"}
		auth.StoreIdentity(c, id)
		auth.StorePrincipal(c, auth.NewOperatorPrincipal(id))
		c.Next()
	})
	const ws = "/v1/workspaces/:workspace_id"
	r.GET(ws+"/projects", h.List)
	r.POST(ws+"/projects", h.Create)
	r.GET(ws+"/projects/:project_id", h.Get)
	r.PATCH(ws+"/projects/:project_id", h.Update)
	r.POST(ws+"/projects/:project_id/archive", h.Archive)
	r.GET(ws+"/projects/:project_id/credentials", h.ListCredentials)
	r.POST(ws+"/projects/:project_id/credentials", h.CreateCredential)
	r.POST(ws+"/projects/:project_id/credentials/:credential_id/revoke", h.RevokeCredential)
	r.GET("/v1/project-scopes", h.Scopes)
	return r, repo
}

func call(t *testing.T, r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	return out
}

func httpErrorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	return decode[ErrorResponse](t, w).Error.Code
}

func base() string { return "/v1/workspaces/" + wsID() }

func createProject(t *testing.T, r *gin.Engine, name string) ProjectResponse {
	t.Helper()
	w := call(t, r, http.MethodPost, base()+"/projects", `{"name":"`+name+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create project: status %d (%s)", w.Code, w.Body.String())
	}
	return decode[ProjectResponse](t, w)
}

// ─── Projects ───────────────────────────────────────────────────────────────

func TestHTTP_CreateProject(t *testing.T) {
	r, _ := newHandlerHarness(t)

	w := call(t, r, http.MethodPost, base()+"/projects", `{"name":"Billing worker"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
	}

	got := decode[ProjectResponse](t, w)
	if !strings.HasPrefix(got.ID, "prj_") {
		t.Errorf("id = %q, want a prj_ prefix", got.ID)
	}
	if got.WorkspaceID != wsID() {
		t.Errorf("workspace_id = %q, want %q", got.WorkspaceID, wsID())
	}
	if got.Status != "active" {
		t.Errorf("status = %q, want active", got.Status)
	}
}

func TestHTTP_CreateProject_MalformedBody(t *testing.T) {
	r, _ := newHandlerHarness(t)

	w := call(t, r, http.MethodPost, base()+"/projects", `{"name":`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := httpErrorCode(t, w); got != ErrInvalidRequest.Code {
		t.Errorf("code = %q, want %q", got, ErrInvalidRequest.Code)
	}
}

// TestHTTP_UpdateProject_RefusesTheWorkspaceBinding is the wire-level half of
// the guarantee the whole authorization model rests on. Silently ignoring the
// field would let a client believe it had moved a project.
func TestHTTP_UpdateProject_RefusesTheWorkspaceBinding(t *testing.T) {
	r, _ := newHandlerHarness(t)
	p := createProject(t, r, "Billing")

	w := call(t, r, http.MethodPatch, base()+"/projects/"+p.ID,
		`{"name":"Billing EU","workspace_id":"ws_00000000-0000-4000-8000-000000000000"}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(decode[ErrorResponse](t, w).Error.Message, "workspace_id") {
		t.Errorf("message %q does not name the refused field", decode[ErrorResponse](t, w).Error.Message)
	}
}

func TestHTTP_UpdateProject_RefusesStatus(t *testing.T) {
	// Status changes go through archive, never through a generic patch.
	r, _ := newHandlerHarness(t)
	p := createProject(t, r, "Billing")

	w := call(t, r, http.MethodPatch, base()+"/projects/"+p.ID, `{"status":"archived"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHTTP_UpdateProject_RequiresAName(t *testing.T) {
	r, _ := newHandlerHarness(t)
	p := createProject(t, r, "Billing")

	w := call(t, r, http.MethodPatch, base()+"/projects/"+p.ID, `{}`)
	if w.Code != http.StatusBadRequest || httpErrorCode(t, w) != ErrNameRequired.Code {
		t.Errorf("status=%d code=%q, want 400 %s", w.Code, httpErrorCode(t, w), ErrNameRequired.Code)
	}
}

func TestHTTP_RenameAndArchive(t *testing.T) {
	r, _ := newHandlerHarness(t)
	p := createProject(t, r, "Billing")

	w := call(t, r, http.MethodPatch, base()+"/projects/"+p.ID, `{"name":"Billing EU"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("rename status = %d: %s", w.Code, w.Body.String())
	}
	if decode[ProjectResponse](t, w).Name != "Billing EU" {
		t.Error("rename did not take effect")
	}

	w = call(t, r, http.MethodPost, base()+"/projects/"+p.ID+"/archive", "")
	if w.Code != http.StatusOK {
		t.Fatalf("archive status = %d: %s", w.Code, w.Body.String())
	}
	if got := decode[ProjectResponse](t, w); got.Status != "archived" || got.ArchivedAt == nil {
		t.Error("archive did not set both status and archived_at")
	}
}

func TestHTTP_ListProjectsIncludesArchivedAndCounts(t *testing.T) {
	r, _ := newHandlerHarness(t)
	live := createProject(t, r, "Live")
	gone := createProject(t, r, "Gone")
	call(t, r, http.MethodPost, base()+"/projects/"+gone.ID+"/archive", "")
	call(t, r, http.MethodPost, base()+"/projects/"+live.ID+"/credentials",
		`{"label":"k","scopes":["users:read"]}`)

	w := call(t, r, http.MethodGet, base()+"/projects", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	got := decode[ListProjectsResponse](t, w)
	if got.Count != 2 {
		t.Fatalf("count = %d, want 2 — archived projects must stay visible in a management listing", got.Count)
	}
	for _, p := range got.Projects {
		if p.ID == live.ID && p.ActiveCredentials != 1 {
			t.Errorf("active_credentials = %d, want 1", p.ActiveCredentials)
		}
	}
}

func TestHTTP_InvalidIdsAreClientErrorsNotNotFound(t *testing.T) {
	// A wrong prefix is a client bug. Reporting it as "not found" would send a
	// developer looking for a resource that was never named.
	r, _ := newHandlerHarness(t)

	w := call(t, r, http.MethodGet, base()+"/projects/conn_3f2504e0-4f89-41d3-9a0c-0305e82c3301", "")
	if w.Code != http.StatusBadRequest || httpErrorCode(t, w) != ErrInvalidID.Code {
		t.Errorf("status=%d code=%q, want 400 %s", w.Code, httpErrorCode(t, w), ErrInvalidID.Code)
	}

	w = call(t, r, http.MethodGet, "/v1/workspaces/not-an-id/projects", "")
	if w.Code != http.StatusBadRequest || httpErrorCode(t, w) != ErrInvalidWorkspaceID.Code {
		t.Errorf("status=%d code=%q, want 400 %s", w.Code, httpErrorCode(t, w), ErrInvalidWorkspaceID.Code)
	}
}

// ─── Credentials ────────────────────────────────────────────────────────────

// TestHTTP_CreateCredential_ReturnsTheSecretExactlyOnce is the wire contract
// the entire design depends on.
func TestHTTP_CreateCredential_ReturnsTheSecretExactlyOnce(t *testing.T) {
	r, _ := newHandlerHarness(t)
	p := createProject(t, r, "Billing")

	w := call(t, r, http.MethodPost, base()+"/projects/"+p.ID+"/credentials",
		`{"label":"staging","scopes":["users:read","users:write"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
	}

	created := decode[CreateCredentialResponse](t, w)
	if !strings.HasPrefix(created.Secret, "lw_sk_") {
		t.Fatalf("secret %q is not a credential token", redact(created.Secret))
	}
	if !strings.HasPrefix(created.Credential.ID, "key_") {
		t.Errorf("credential id = %q, want a key_ prefix", created.Credential.ID)
	}
	if created.Credential.Status != "active" {
		t.Errorf("status = %q, want active", created.Credential.Status)
	}

	parsed, ok := parseToken(created.Secret)
	if !ok {
		t.Fatal("the returned secret does not parse")
	}

	// The listing must not carry it, and there is no endpoint that could.
	list := call(t, r, http.MethodGet, base()+"/projects/"+p.ID+"/credentials", "")
	if strings.Contains(list.Body.String(), parsed.secret) {
		t.Fatal("the credential secret appears in the credential listing")
	}
	if strings.Contains(list.Body.String(), created.Secret) {
		t.Fatal("the full credential token appears in the credential listing")
	}
	// The public lookup segment IS shown: it is what matches a log line to a row.
	if !strings.Contains(list.Body.String(), created.Credential.KeyPrefix) {
		t.Error("the listing does not carry the key prefix")
	}
	// And no digest is published either.
	if strings.Contains(strings.ToLower(list.Body.String()), "key_hash") {
		t.Error("the listing exposes the stored digest")
	}
}

func TestHTTP_CreateCredential_RejectsAnEmptyScopeList(t *testing.T) {
	r, _ := newHandlerHarness(t)
	p := createProject(t, r, "Billing")

	for name, body := range map[string]string{
		"empty":   `{"label":"x","scopes":[]}`,
		"absent":  `{"label":"x"}`,
		"unknown": `{"label":"x","scopes":["billing:refund"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := call(t, r, http.MethodPost, base()+"/projects/"+p.ID+"/credentials", body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
			if got := httpErrorCode(t, w); got != ErrInvalidScope.Code {
				t.Errorf("code = %q, want %q", got, ErrInvalidScope.Code)
			}
		})
	}
}

func TestHTTP_RevokeCredential(t *testing.T) {
	r, _ := newHandlerHarness(t)
	p := createProject(t, r, "Billing")
	created := decode[CreateCredentialResponse](t,
		call(t, r, http.MethodPost, base()+"/projects/"+p.ID+"/credentials",
			`{"label":"staging","scopes":["users:read"]}`))

	path := base() + "/projects/" + p.ID + "/credentials/" + created.Credential.ID + "/revoke"

	w := call(t, r, http.MethodPost, path, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	got := decode[CredentialResponse](t, w)
	if got.Status != "revoked" || got.RevokedAt == nil {
		t.Error("the response does not report the credential as revoked")
	}
	if got.RevokedBy == nil || *got.RevokedBy != "operator-sub" {
		t.Error("revocation was not attributed to the acting operator")
	}

	// Twice is a conflict, so an operator learns someone else got there first.
	w = call(t, r, http.MethodPost, path, "")
	if w.Code != http.StatusConflict || httpErrorCode(t, w) != ErrCredentialAlreadyRevoked.Code {
		t.Errorf("status=%d code=%q, want 409 %s", w.Code, httpErrorCode(t, w), ErrCredentialAlreadyRevoked.Code)
	}
}

func TestHTTP_RevokeCredential_InvalidId(t *testing.T) {
	r, _ := newHandlerHarness(t)
	p := createProject(t, r, "Billing")

	w := call(t, r, http.MethodPost,
		base()+"/projects/"+p.ID+"/credentials/prj_3f2504e0-4f89-41d3-9a0c-0305e82c3301/revoke", "")
	if w.Code != http.StatusBadRequest || httpErrorCode(t, w) != ErrInvalidCredentialID.Code {
		t.Errorf("status=%d code=%q, want 400 %s", w.Code, httpErrorCode(t, w), ErrInvalidCredentialID.Code)
	}
}

func TestHTTP_Scopes(t *testing.T) {
	r, _ := newHandlerHarness(t)

	w := call(t, r, http.MethodGet, "/v1/project-scopes", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	// Compared against the authoritative vocabulary rather than a hard-coded
	// count. The count was 8 and became 9 when `audit:read` arrived; a number
	// here says nothing about correctness and breaks on every legitimate
	// addition, while this says the endpoint advertises exactly what the
	// server accepts.
	got := decode[ScopesResponse](t, w)
	want := authz.ScopeStrings(authz.AllScopes())
	if !reflect.DeepEqual(got.Scopes, want) {
		t.Errorf("scopes = %v, want the server's vocabulary %v", got.Scopes, want)
	}
}

// ─── Audit ──────────────────────────────────────────────────────────────────

// TestHTTP_AuditAttribution pins who the audit trail says acted, and — more
// importantly — that no credential material reaches it.
func TestHTTP_AuditAttribution(t *testing.T) {
	var events []audit.Event
	restore := audit.SetDefault(audit.RecorderFunc(func(_ context.Context, e audit.Event) {
		events = append(events, e)
	}))
	t.Cleanup(func() { audit.SetDefault(restore) })

	r, _ := newHandlerHarness(t)
	p := createProject(t, r, "Billing")
	created := decode[CreateCredentialResponse](t,
		call(t, r, http.MethodPost, base()+"/projects/"+p.ID+"/credentials",
			`{"label":"staging","scopes":["users:read"]}`))

	if len(events) < 2 {
		t.Fatalf("expected project.created and project_credential.created, got %d events", len(events))
	}

	var credEvent *audit.Event
	for i := range events {
		if events[i].Action == audit.ActionCredentialCreated {
			credEvent = &events[i]
		}
	}
	if credEvent == nil {
		t.Fatal("no project_credential.created event was emitted")
	}

	if credEvent.Actor.Type != audit.ActorOperator || credEvent.Actor.Subject != "operator-sub" {
		t.Errorf("actor = %+v, want an operator with the acting subject", credEvent.Actor)
	}
	if credEvent.Actor.ProjectID != "" || credEvent.Actor.CredentialID != "" {
		t.Error("an operator event carries project fields")
	}
	if credEvent.Target.ID != created.Credential.ID {
		t.Errorf("target id = %q, want the credential's public id", credEvent.Target.ID)
	}

	// The secret must not be anywhere in the event, including Extra.
	blob, err := json.Marshal(credEvent)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	parsed, _ := parseToken(created.Secret)
	if strings.Contains(string(blob), parsed.secret) || strings.Contains(string(blob), created.Secret) {
		t.Fatal("the credential secret reached the audit event")
	}
	// The scopes DO belong there: they are what an operator needs when working
	// out what a leaked key could do.
	if !strings.Contains(string(blob), "users:read") {
		t.Error("the granted scopes are absent from the audit event")
	}
}
