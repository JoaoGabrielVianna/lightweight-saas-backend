package workspace

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"github.com/gin-gonic/gin"
)

// newTestRouter mounts the workspace routes on a bare engine with only the
// request-id middleware.
//
// Authentication is deliberately absent here: these tests are about status
// codes and payloads. That the routes are gated is a property of where they
// are mounted, and is pinned in internal/server's router tests — asserting it
// again here would test a middleware this package does not own.
func newTestRouter(seed ...*Workspace) (*gin.Engine, *fakeRepository) {
	gin.SetMode(gin.TestMode)

	repo := newFakeRepository(seed...)
	svc := NewService(repo, &fakeRunner{}, &fakeAuditWriter{})
	svc.now = fixedClock()
	h := NewHandler(svc)

	r := gin.New()
	v1 := r.Group("/v1")
	v1.Use(requestid.Middleware())
	{
		v1.GET("/workspaces", h.List)
		v1.POST("/workspaces", h.Create)
		v1.GET("/workspaces/:workspace_id", h.Get)
		v1.PATCH("/workspaces/:workspace_id", h.Update)
		v1.POST("/workspaces/:workspace_id/archive", h.Archive)
	}
	return r, repo
}

func do(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// decodeWorkspace parses a success body, failing the test on anything else.
func decodeWorkspace(t *testing.T, w *httptest.ResponseRecorder) WorkspaceResponse {
	t.Helper()
	var out WorkspaceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON body %q: %v", w.Body.String(), err)
	}
	return out
}

// assertErrorEnvelope checks status and the stable error code, and pins the
// envelope's shape — the contract clients branch on.
func assertErrorEnvelope(t *testing.T, w *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()

	if w.Code != wantStatus {
		t.Errorf("status = %d, want %d (body %s)", w.Code, wantStatus, w.Body.String())
	}

	var out ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON body %q: %v", w.Body.String(), err)
	}
	if out.Error.Code != wantCode {
		t.Errorf("error.code = %q, want %q", out.Error.Code, wantCode)
	}
	if out.Error.Message == "" {
		t.Error("error.message is empty")
	}
	if out.Error.RequestID == "" {
		t.Error("error.request_id is empty — the envelope must be correlatable")
	}
	if got := w.Header().Get(requestid.Header); got != out.Error.RequestID {
		t.Errorf("X-Request-Id header = %q but body says %q; they must match", got, out.Error.RequestID)
	}

	// No internal detail may reach the client.
	body := strings.ToLower(w.Body.String())
	for _, leak := range []string{"sql", "pq:", "gorm", "constraint", "postgres", "23505"} {
		if strings.Contains(body, leak) {
			t.Errorf("error body leaks internal detail %q: %s", leak, w.Body.String())
		}
	}
}

// ---------------------------------------------------------------------------
// POST /v1/workspaces
// ---------------------------------------------------------------------------

func TestHTTP_Create_Returns201(t *testing.T) {
	r, _ := newTestRouter()

	w := do(r, http.MethodPost, "/v1/workspaces", `{"name":"Production","slug":"production"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", w.Code, w.Body.String())
	}

	got := decodeWorkspace(t, w)
	if !strings.HasPrefix(got.ID, "ws_") {
		t.Errorf("id = %q, want a ws_ prefix", got.ID)
	}
	if got.Slug != "production" || got.Name != "Production" {
		t.Errorf("slug/name = %q/%q", got.Slug, got.Name)
	}
	if got.Status != "active" {
		t.Errorf("status = %q, want active", got.Status)
	}
	if got.ArchivedAt != nil {
		t.Errorf("archived_at = %v, want null", got.ArchivedAt)
	}

	// The response must expose exactly the documented fields — no internal
	// ones leaking in through a future struct change.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	want := map[string]bool{
		"id": true, "slug": true, "name": true, "status": true,
		"created_at": true, "updated_at": true, "archived_at": true,
	}
	for k := range raw {
		if !want[k] {
			t.Errorf("unexpected field %q in the response", k)
		}
	}
	for k := range want {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing field %q in the response", k)
		}
	}
}

func TestHTTP_Create_DerivesSlug(t *testing.T) {
	r, _ := newTestRouter()

	w := do(r, http.MethodPost, "/v1/workspaces", `{"name":"Production EU"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", w.Code, w.Body.String())
	}
	if got := decodeWorkspace(t, w); got.Slug != "production-eu" {
		t.Errorf("derived slug = %q, want production-eu", got.Slug)
	}
}

func TestHTTP_Create_DuplicateSlugReturns409(t *testing.T) {
	r, _ := newTestRouter(activeWorkspace(fixtureIDProd, "production", "Production"))

	w := do(r, http.MethodPost, "/v1/workspaces", `{"name":"Another","slug":"production"}`)
	assertErrorEnvelope(t, w, http.StatusConflict, "workspace_slug_taken")
}

func TestHTTP_Create_InvalidPayloadsReturn400(t *testing.T) {
	tests := map[string]struct {
		body     string
		wantCode string
	}{
		"missing name":    {`{"slug":"production"}`, "workspace_name_required"},
		"blank name":      {`{"name":"   "}`, "workspace_name_required"},
		"reserved slug":   {`{"name":"Admin Area","slug":"admin"}`, "workspace_slug_reserved"},
		"invalid slug":    {`{"name":"X","slug":"Not A Slug"}`, "workspace_slug_invalid"},
		"malformed json":  {`{"name":`, "invalid_request"},
		"wrong slug type": {`{"name":"X","slug":123}`, "invalid_request"},
		"empty body":      {``, "invalid_request"},
		"unslugifiable":   {`{"name":"生产环境"}`, "workspace_slug_invalid"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r, _ := newTestRouter()
			w := do(r, http.MethodPost, "/v1/workspaces", tc.body)
			assertErrorEnvelope(t, w, http.StatusBadRequest, tc.wantCode)
		})
	}
}

// ---------------------------------------------------------------------------
// GET /v1/workspaces
// ---------------------------------------------------------------------------

func TestHTTP_List_DefaultReturnsOnlyActive(t *testing.T) {
	r, _ := newTestRouter(
		activeWorkspace(fixtureIDProd, "production", "Production"),
		archivedWorkspace(fixtureIDOld, "old", "Old"),
	)

	w := do(r, http.MethodGet, "/v1/workspaces", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var out ListWorkspacesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if out.Count != 1 || len(out.Workspaces) != 1 {
		t.Fatalf("default listing returned %d items, want 1 (archived must be excluded)", out.Count)
	}
	if out.Workspaces[0].Slug != "production" {
		t.Errorf("returned %q, want the active workspace", out.Workspaces[0].Slug)
	}
}

func TestHTTP_List_Filters(t *testing.T) {
	seed := []*Workspace{
		activeWorkspace(fixtureIDProd, "production", "Production"),
		activeWorkspace(fixtureIDStaging, "staging", "Staging"),
		archivedWorkspace(fixtureIDOld, "old", "Old"),
	}

	for query, want := range map[string]int{
		"?status=active":   2,
		"?status=archived": 1,
		"?status=all":      3,
	} {
		t.Run(query, func(t *testing.T) {
			r, _ := newTestRouter(seed...)
			w := do(r, http.MethodGet, "/v1/workspaces"+query, "")
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			var out ListWorkspacesResponse
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if out.Count != want {
				t.Errorf("count = %d, want %d", out.Count, want)
			}
		})
	}
}

func TestHTTP_List_InvalidFilterReturns400(t *testing.T) {
	r, _ := newTestRouter()
	w := do(r, http.MethodGet, "/v1/workspaces?status=achieved", "")
	assertErrorEnvelope(t, w, http.StatusBadRequest, "invalid_status_filter")
}

// TestHTTP_List_EmptyMarshalsAsArray pins that no results is `[]`, not `null`,
// so clients can iterate without a nil check.
func TestHTTP_List_EmptyMarshalsAsArray(t *testing.T) {
	r, _ := newTestRouter()
	w := do(r, http.MethodGet, "/v1/workspaces", "")

	if !strings.Contains(w.Body.String(), `"workspaces":[]`) {
		t.Errorf("empty listing body = %s, want workspaces:[]", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// GET /v1/workspaces/{id}
// ---------------------------------------------------------------------------

func TestHTTP_Get_ReturnsWorkspace(t *testing.T) {
	r, _ := newTestRouter(activeWorkspace(fixtureIDProd, "production", "Production"))

	w := do(r, http.MethodGet, "/v1/workspaces/ws_"+fixtureIDProd, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if got := decodeWorkspace(t, w); got.ID != "ws_"+fixtureIDProd {
		t.Errorf("id = %q, want ws_%s", got.ID, fixtureIDProd)
	}
}

func TestHTTP_Get_MissingReturns404(t *testing.T) {
	r, _ := newTestRouter()
	w := do(r, http.MethodGet, "/v1/workspaces/ws_"+fixtureIDProd, "")
	assertErrorEnvelope(t, w, http.StatusNotFound, "workspace_not_found")
}

// TestHTTP_Get_BadIDReturns400 is the distinction that matters: a wrong-typed
// or malformed id is a client error, never a 404. A 404 would both hide the
// client's bug and hint at whether an object with that UUID exists elsewhere.
func TestHTTP_Get_BadIDReturns400(t *testing.T) {
	for name, id := range map[string]string{
		"wrong prefix":  "conn_" + fixtureIDProd,
		"prj prefix":    "prj_" + fixtureIDProd,
		"no uuid":       "ws_",
		"not an id":     "banana",
		"partial uuid":  "ws_3f2504e0",
		"uppercase pfx": "WS_" + fixtureIDProd,
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := newTestRouter(activeWorkspace(fixtureIDProd, "production", "Production"))
			w := do(r, http.MethodGet, "/v1/workspaces/"+id, "")
			assertErrorEnvelope(t, w, http.StatusBadRequest, "invalid_workspace_id")
		})
	}
}

// TestHTTP_Get_AcceptsBareUUID covers the documented development convenience.
func TestHTTP_Get_AcceptsBareUUID(t *testing.T) {
	r, _ := newTestRouter(activeWorkspace(fixtureIDProd, "production", "Production"))

	w := do(r, http.MethodGet, "/v1/workspaces/"+fixtureIDProd, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	// Even when addressed by bare UUID, the response renders the prefixed form.
	if got := decodeWorkspace(t, w); got.ID != "ws_"+fixtureIDProd {
		t.Errorf("id = %q, want the prefixed form regardless of how it was addressed", got.ID)
	}
}

// ---------------------------------------------------------------------------
// PATCH /v1/workspaces/{id}
// ---------------------------------------------------------------------------

func TestHTTP_Patch_RenamesWithoutTouchingSlug(t *testing.T) {
	r, _ := newTestRouter(activeWorkspace(fixtureIDProd, "production", "Production"))

	w := do(r, http.MethodPatch, "/v1/workspaces/ws_"+fixtureIDProd, `{"name":"Production EU"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}

	got := decodeWorkspace(t, w)
	if got.Name != "Production EU" {
		t.Errorf("name = %q, want %q", got.Name, "Production EU")
	}
	if got.Slug != "production" {
		t.Errorf("slug = %q — a rename must not move the slug", got.Slug)
	}
}

// TestHTTP_Patch_RejectsImmutableFields pins the explicit-rejection choice. A
// silently-ignored slug change would return 200 to a client that believes it
// renamed the slug, and the divergence would surface much later somewhere else.
func TestHTTP_Patch_RejectsImmutableFields(t *testing.T) {
	for name, body := range map[string]string{
		"slug":            `{"name":"X","slug":"new-slug"}`,
		"status":          `{"name":"X","status":"archived"}`,
		"slug alone":      `{"slug":"new-slug"}`,
		"status alone":    `{"status":"active"}`,
		"slug same value": `{"slug":"production"}`,
	} {
		t.Run(name, func(t *testing.T) {
			r, repo := newTestRouter(activeWorkspace(fixtureIDProd, "production", "Production"))
			w := do(r, http.MethodPatch, "/v1/workspaces/ws_"+fixtureIDProd, body)
			assertErrorEnvelope(t, w, http.StatusBadRequest, "invalid_request")

			// And nothing was written.
			stored, _ := repo.GetByID(ctx(), fixtureIDProd)
			if stored.Slug != "production" || stored.Name != "Production" {
				t.Errorf("stored row changed to %q/%q despite the rejection", stored.Slug, stored.Name)
			}
		})
	}
}

func TestHTTP_Patch_MissingNameReturns400(t *testing.T) {
	r, _ := newTestRouter(activeWorkspace(fixtureIDProd, "production", "Production"))

	w := do(r, http.MethodPatch, "/v1/workspaces/ws_"+fixtureIDProd, `{}`)
	assertErrorEnvelope(t, w, http.StatusBadRequest, "workspace_name_required")
}

func TestHTTP_Patch_ArchivedReturns409(t *testing.T) {
	r, _ := newTestRouter(archivedWorkspace(fixtureIDOld, "old", "Old"))

	w := do(r, http.MethodPatch, "/v1/workspaces/ws_"+fixtureIDOld, `{"name":"New Name"}`)
	assertErrorEnvelope(t, w, http.StatusConflict, "workspace_archived")
}

func TestHTTP_Patch_MissingReturns404(t *testing.T) {
	r, _ := newTestRouter()
	w := do(r, http.MethodPatch, "/v1/workspaces/ws_"+fixtureIDProd, `{"name":"X"}`)
	assertErrorEnvelope(t, w, http.StatusNotFound, "workspace_not_found")
}

// ---------------------------------------------------------------------------
// POST /v1/workspaces/{id}/archive
// ---------------------------------------------------------------------------

func TestHTTP_Archive_Succeeds(t *testing.T) {
	r, _ := newTestRouter(activeWorkspace(fixtureIDProd, "production", "Production"))

	w := do(r, http.MethodPost, "/v1/workspaces/ws_"+fixtureIDProd+"/archive", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}

	got := decodeWorkspace(t, w)
	if got.Status != "archived" {
		t.Errorf("status = %q, want archived", got.Status)
	}
	if got.ArchivedAt == nil {
		t.Error("archived_at must be set on an archived workspace")
	}
}

// TestHTTP_Archive_IsIdempotent covers the retry case end to end: the second
// call is a 200 with the same body, not a 409.
func TestHTTP_Archive_IsIdempotent(t *testing.T) {
	r, _ := newTestRouter(activeWorkspace(fixtureIDProd, "production", "Production"))
	path := "/v1/workspaces/ws_" + fixtureIDProd + "/archive"

	first := do(r, http.MethodPost, path, "")
	if first.Code != http.StatusOK {
		t.Fatalf("first archive status = %d, want 200", first.Code)
	}
	firstBody := decodeWorkspace(t, first)

	second := do(r, http.MethodPost, path, "")
	if second.Code != http.StatusOK {
		t.Fatalf("repeat archive status = %d, want 200 (idempotent)", second.Code)
	}
	secondBody := decodeWorkspace(t, second)

	if !secondBody.ArchivedAt.Equal(*firstBody.ArchivedAt) {
		t.Errorf("archived_at moved from %v to %v on retry", firstBody.ArchivedAt, secondBody.ArchivedAt)
	}
	if secondBody.Slug != firstBody.Slug {
		t.Errorf("slug changed on retry: %q → %q", firstBody.Slug, secondBody.Slug)
	}
}

func TestHTTP_Archive_MissingReturns404(t *testing.T) {
	r, _ := newTestRouter()
	w := do(r, http.MethodPost, "/v1/workspaces/ws_"+fixtureIDProd+"/archive", "")
	assertErrorEnvelope(t, w, http.StatusNotFound, "workspace_not_found")
}

func TestHTTP_Archive_BadIDReturns400(t *testing.T) {
	r, _ := newTestRouter()
	w := do(r, http.MethodPost, "/v1/workspaces/conn_"+fixtureIDProd+"/archive", "")
	assertErrorEnvelope(t, w, http.StatusBadRequest, "invalid_workspace_id")
}

// TestHTTP_Archive_ThenGetStillReadable pins that archiving is not a delete.
func TestHTTP_Archive_ThenGetStillReadable(t *testing.T) {
	r, _ := newTestRouter(activeWorkspace(fixtureIDProd, "production", "Production"))

	if w := do(r, http.MethodPost, "/v1/workspaces/ws_"+fixtureIDProd+"/archive", ""); w.Code != http.StatusOK {
		t.Fatalf("archive status = %d", w.Code)
	}

	w := do(r, http.MethodGet, "/v1/workspaces/ws_"+fixtureIDProd, "")
	if w.Code != http.StatusOK {
		t.Fatalf("get after archive = %d, want 200 — archiving is not a delete", w.Code)
	}
	if got := decodeWorkspace(t, w); got.Status != "archived" {
		t.Errorf("status = %q, want archived", got.Status)
	}
}

// ---------------------------------------------------------------------------
// Internal errors
// ---------------------------------------------------------------------------

// TestHTTP_RepositoryFailureReturnsInternalError pins the last line of the
// no-leak requirement: an arbitrary storage error becomes internal_error with
// no trace of the cause on the wire.
func TestHTTP_RepositoryFailureReturnsInternalError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := newFakeRepository()
	repo.failWith = &pgLikeError{}
	svc := NewService(repo, &fakeRunner{}, &fakeAuditWriter{})
	svc.now = fixedClock()
	h := NewHandler(svc)

	r := gin.New()
	v1 := r.Group("/v1")
	v1.Use(requestid.Middleware())
	v1.GET("/workspaces", h.List)

	w := do(r, http.MethodGet, "/v1/workspaces", "")
	assertErrorEnvelope(t, w, http.StatusInternalServerError, "internal_error")
}

// pgLikeError mimics the shape of a driver error — the exact thing that must
// never reach a client.
type pgLikeError struct{}

func (e *pgLikeError) Error() string {
	return `ERROR: duplicate key value violates unique constraint "idx_workspaces_slug" (SQLSTATE 23505)`
}
