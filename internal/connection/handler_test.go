package connection

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
	"github.com/gin-gonic/gin"
)

// theSecret is used by every request in this file so one assertion can prove it
// never comes back out.
const theSecret = "the-client-secret-value"

// newTestRouter mounts the connection routes with only the request-id
// middleware. Authentication is a property of where these routes are mounted
// and is pinned in internal/server's router tests.
func newTestRouter(t *testing.T, ws *workspace.Workspace, seed ...*Connection) (*gin.Engine, *harness) {
	gin.SetMode(gin.TestMode)

	h := newHarness(t, ws, seed...)
	handler := NewHandler(h.svc)
	handler.now = fixedClock()

	r := gin.New()
	v1 := r.Group("/v1")
	v1.Use(requestid.Middleware())
	{
		v1.GET("/workspaces/:workspace_id/connections", handler.List)
		v1.POST("/workspaces/:workspace_id/connections", handler.Create)
		v1.GET("/workspaces/:workspace_id/connections/:connection_id", handler.Get)
		v1.PATCH("/workspaces/:workspace_id/connections/:connection_id", handler.Update)
		v1.DELETE("/workspaces/:workspace_id/connections/:connection_id", handler.Delete)
		v1.POST("/workspaces/:workspace_id/connections/:connection_id/verify", handler.Verify)
		v1.POST("/workspaces/:workspace_id/connections/:connection_id/activate", handler.Activate)
		v1.POST("/workspaces/:workspace_id/connections/:connection_id/retire", handler.Retire)
	}
	return r, h
}

func do(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// wsPath builds the collection path.
func wsPath() string { return "/v1/workspaces/ws_" + fixtureWorkspaceID + "/connections" }

// connPath builds a single-connection path.
func connPath(suffix string) string {
	return wsPath() + "/conn_" + fixtureConnID + suffix
}

func decodeConnection(t *testing.T, w *httptest.ResponseRecorder) ConnectionResponse {
	t.Helper()
	var out ConnectionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON body %q: %v", w.Body.String(), err)
	}
	return out
}

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

	body := strings.ToLower(w.Body.String())
	for _, leak := range []string{"sql", "gorm", "constraint", "postgres", "23505", theSecret} {
		if strings.Contains(body, strings.ToLower(leak)) {
			t.Errorf("error body leaks %q: %s", leak, w.Body.String())
		}
	}
}

// ---------------------------------------------------------------------------
// The secret must never appear on the wire
// ---------------------------------------------------------------------------

// TestHTTP_SecretNeverAppearsInAnyResponse walks the whole lifecycle in one
// test and asserts, after every single response, that neither the secret nor
// any wrapping metadata came back.
//
// This is the requirement the domain exists to satisfy, so it is checked
// against real responses from every endpoint rather than by reading dto.go.
func TestHTTP_SecretNeverAppearsInAnyResponse(t *testing.T) {
	r, h := newTestRouter(t, activeWorkspaceFixture())

	responses := map[string]*httptest.ResponseRecorder{}

	created := do(r, http.MethodPost, wsPath(),
		`{"name":"Prod","base_url":"https://kc.example.com","realm":"saas","client_id":"svc","client_secret":"`+theSecret+`"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", created.Code, created.Body.String())
	}
	responses["create"] = created

	id := decodeConnection(t, created).ID
	base := wsPath() + "/" + id

	responses["get"] = do(r, http.MethodGet, base, "")
	responses["list"] = do(r, http.MethodGet, wsPath(), "")
	responses["patch"] = do(r, http.MethodPatch, base, `{"name":"Renamed"}`)
	responses["patch secret"] = do(r, http.MethodPatch, base, `{"client_secret":"`+theSecret+`-rotated"}`)
	responses["verify"] = do(r, http.MethodPost, base+"/verify", "")
	responses["activate"] = do(r, http.MethodPost, base+"/activate", "")
	responses["retire"] = do(r, http.MethodPost, base+"/retire", "")

	// And the sealed material, to be certain none of it is echoed either.
	sealed, err := h.repo.OpenSecret(ctx(), strings.TrimPrefix(id, "conn_"))
	if err != nil || sealed == nil {
		t.Fatalf("OpenSecret: %v", err)
	}

	for name, w := range responses {
		body := w.Body.String()
		if strings.Contains(body, theSecret) {
			t.Errorf("%s response contains the client secret: %s", name, body)
		}
		if strings.Contains(body, string(sealed.Ciphertext)) {
			t.Errorf("%s response contains the ciphertext", name)
		}
		if strings.Contains(body, string(sealed.Nonce)) {
			t.Errorf("%s response contains the nonce", name)
		}
		for _, field := range []string{"client_secret", "secret_ciphertext", "secret_nonce", "ciphertext", "nonce", "secret_key_version"} {
			if strings.Contains(body, `"`+field+`"`) {
				t.Errorf("%s response exposes a %q field: %s", name, field, body)
			}
		}
	}
}

// TestHTTP_Create_ResponseShape pins exactly which fields the API exposes, so a
// future field addition is a deliberate act rather than a leak.
func TestHTTP_Create_ResponseShape(t *testing.T) {
	r, _ := newTestRouter(t, activeWorkspaceFixture())

	w := do(r, http.MethodPost, wsPath(),
		`{"name":"Prod","base_url":"https://kc.example.com/","realm":"saas","client_id":"svc","client_secret":"`+theSecret+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
	}

	got := decodeConnection(t, w)
	if !strings.HasPrefix(got.ID, "conn_") {
		t.Errorf("id = %q, want a conn_ prefix", got.ID)
	}
	if got.WorkspaceID != "ws_"+fixtureWorkspaceID {
		t.Errorf("workspace_id = %q, want the prefixed workspace id", got.WorkspaceID)
	}
	if got.Status != "draft" {
		t.Errorf("status = %q, want draft", got.Status)
	}
	if got.Provider != "keycloak" {
		t.Errorf("provider = %q", got.Provider)
	}
	if !got.HasClientSecret {
		t.Error("has_client_secret should be true")
	}
	if got.Health != "unknown" || got.AccessMode != "unknown" || got.Verified {
		t.Errorf("a new connection must be unverified: health=%q mode=%q verified=%v",
			got.Health, got.AccessMode, got.Verified)
	}
	if got.BaseURL != "https://kc.example.com" {
		t.Errorf("base_url = %q, want the trailing slash stripped", got.BaseURL)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	want := map[string]bool{
		"id": true, "workspace_id": true, "name": true, "provider": true, "status": true,
		"base_url": true, "realm": true, "client_id": true, "has_client_secret": true,
		"health": true, "access_mode": true, "can_write": true, "last_verified_at": true, "verified": true,
		"created_at": true, "updated_at": true, "activated_at": true, "retired_at": true,
		"health_message": true, // omitempty, may be absent
	}
	for k := range raw {
		if !want[k] {
			t.Errorf("unexpected field %q in the response", k)
		}
	}
}

// ---------------------------------------------------------------------------
// CRUD
// ---------------------------------------------------------------------------

func TestHTTP_Create_InvalidPayloads(t *testing.T) {
	tests := map[string]struct {
		body     string
		wantCode string
		status   int
	}{
		"missing name":      {`{"base_url":"https://k","realm":"r","client_id":"c","client_secret":"s"}`, "connection_name_required", 400},
		"bad base url":      {`{"name":"n","base_url":"nope","realm":"r","client_id":"c","client_secret":"s"}`, "connection_base_url_invalid", 400},
		"missing realm":     {`{"name":"n","base_url":"https://k","client_id":"c","client_secret":"s"}`, "connection_realm_required", 400},
		"missing client id": {`{"name":"n","base_url":"https://k","realm":"r","client_secret":"s"}`, "connection_client_id_required", 400},
		"missing secret":    {`{"name":"n","base_url":"https://k","realm":"r","client_id":"c"}`, "connection_client_secret_required", 400},
		"unknown provider":  {`{"name":"n","provider":"okta","base_url":"https://k","realm":"r","client_id":"c","client_secret":"s"}`, "connection_provider_unsupported", 400},
		"malformed json":    {`{"name":`, "invalid_request", 400},
		"empty body":        {``, "invalid_request", 400},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r, _ := newTestRouter(t, activeWorkspaceFixture())
			w := do(r, http.MethodPost, wsPath(), tc.body)
			assertErrorEnvelope(t, w, tc.status, tc.wantCode)
		})
	}
}

func TestHTTP_Create_WorkspaceErrors(t *testing.T) {
	valid := `{"name":"n","base_url":"https://k.example.com","realm":"r","client_id":"c","client_secret":"s"}`

	t.Run("archived workspace", func(t *testing.T) {
		r, _ := newTestRouter(t, archivedWorkspaceFixture())
		w := do(r, http.MethodPost, wsPath(), valid)
		assertErrorEnvelope(t, w, http.StatusConflict, "workspace_archived")
	})

	t.Run("unknown workspace", func(t *testing.T) {
		r, _ := newTestRouter(t, activeWorkspaceFixture())
		w := do(r, http.MethodPost, "/v1/workspaces/ws_"+fixtureConnID2+"/connections", valid)
		assertErrorEnvelope(t, w, http.StatusNotFound, "workspace_not_found")
	})

	t.Run("malformed workspace id", func(t *testing.T) {
		r, _ := newTestRouter(t, activeWorkspaceFixture())
		w := do(r, http.MethodPost, "/v1/workspaces/nonsense/connections", valid)
		assertErrorEnvelope(t, w, http.StatusBadRequest, "invalid_workspace_id")
	})
}

func TestHTTP_Get(t *testing.T) {
	r, _ := newTestRouter(t, activeWorkspaceFixture(), draftConnection(fixtureConnID, "Prod"))

	w := do(r, http.MethodGet, connPath(""), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if got := decodeConnection(t, w); got.ID != "conn_"+fixtureConnID {
		t.Errorf("id = %q", got.ID)
	}
}

func TestHTTP_Get_NotFound(t *testing.T) {
	r, _ := newTestRouter(t, activeWorkspaceFixture())
	w := do(r, http.MethodGet, connPath(""), "")
	assertErrorEnvelope(t, w, http.StatusNotFound, "connection_not_found")
}

func TestHTTP_Get_InvalidConnectionID(t *testing.T) {
	for name, id := range map[string]string{
		"workspace prefix": "ws_" + fixtureConnID,
		"no prefix":        "banana",
		"prefix only":      "conn_",
		"partial uuid":     "conn_3f2504e0",
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := newTestRouter(t, activeWorkspaceFixture(), draftConnection(fixtureConnID, "Prod"))
			w := do(r, http.MethodGet, wsPath()+"/"+id, "")
			assertErrorEnvelope(t, w, http.StatusBadRequest, "invalid_connection_id")
		})
	}
}

func TestHTTP_List(t *testing.T) {
	r, _ := newTestRouter(t, activeWorkspaceFixture(),
		draftConnection(fixtureConnID, "A"),
		activeConnection(fixtureConnID2, "B"),
	)

	t.Run("default shows every status", func(t *testing.T) {
		w := do(r, http.MethodGet, wsPath(), "")
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		var out ListConnectionsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if out.Count != 2 {
			t.Errorf("count = %d, want 2", out.Count)
		}
	})

	t.Run("filtered", func(t *testing.T) {
		w := do(r, http.MethodGet, wsPath()+"?status=active", "")
		var out ListConnectionsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if out.Count != 1 {
			t.Errorf("count = %d, want 1", out.Count)
		}
	})

	t.Run("invalid filter", func(t *testing.T) {
		w := do(r, http.MethodGet, wsPath()+"?status=archived", "")
		assertErrorEnvelope(t, w, http.StatusBadRequest, "invalid_status_filter")
	})
}

func TestHTTP_List_EmptyMarshalsAsArray(t *testing.T) {
	r, _ := newTestRouter(t, activeWorkspaceFixture())
	w := do(r, http.MethodGet, wsPath(), "")
	if !strings.Contains(w.Body.String(), `"connections":[]`) {
		t.Errorf("empty listing = %s, want connections:[]", w.Body.String())
	}
}

func TestHTTP_Patch(t *testing.T) {
	t.Run("renames a draft", func(t *testing.T) {
		r, _ := newTestRouter(t, activeWorkspaceFixture(), draftConnection(fixtureConnID, "Old"))
		w := do(r, http.MethodPatch, connPath(""), `{"name":"New"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		if got := decodeConnection(t, w); got.Name != "New" {
			t.Errorf("name = %q", got.Name)
		}
	})

	t.Run("rejects immutable status", func(t *testing.T) {
		r, _ := newTestRouter(t, activeWorkspaceFixture(), draftConnection(fixtureConnID, "Old"))
		w := do(r, http.MethodPatch, connPath(""), `{"status":"active"}`)
		assertErrorEnvelope(t, w, http.StatusBadRequest, "invalid_request")
	})

	t.Run("rejects immutable provider", func(t *testing.T) {
		r, _ := newTestRouter(t, activeWorkspaceFixture(), draftConnection(fixtureConnID, "Old"))
		w := do(r, http.MethodPatch, connPath(""), `{"provider":"okta"}`)
		assertErrorEnvelope(t, w, http.StatusBadRequest, "invalid_request")
	})

	t.Run("rejects an empty patch", func(t *testing.T) {
		r, _ := newTestRouter(t, activeWorkspaceFixture(), draftConnection(fixtureConnID, "Old"))
		w := do(r, http.MethodPatch, connPath(""), `{}`)
		assertErrorEnvelope(t, w, http.StatusBadRequest, "invalid_request")
	})

	t.Run("refuses an active connection", func(t *testing.T) {
		r, _ := newTestRouter(t, activeWorkspaceFixture(), activeConnection(fixtureConnID, "Old"))
		w := do(r, http.MethodPatch, connPath(""), `{"name":"New"}`)
		assertErrorEnvelope(t, w, http.StatusConflict, "connection_not_draft")
	})

	t.Run("refuses a retired connection", func(t *testing.T) {
		r, _ := newTestRouter(t, activeWorkspaceFixture(), retiredConnection(fixtureConnID, "Old"))
		w := do(r, http.MethodPatch, connPath(""), `{"name":"New"}`)
		assertErrorEnvelope(t, w, http.StatusConflict, "connection_retired")
	})
}

func TestHTTP_Delete(t *testing.T) {
	t.Run("draft returns 204", func(t *testing.T) {
		r, _ := newTestRouter(t, activeWorkspaceFixture(), draftConnection(fixtureConnID, "c"))
		w := do(r, http.MethodDelete, connPath(""), "")
		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204: %s", w.Code, w.Body.String())
		}
		if w.Body.Len() != 0 {
			t.Errorf("204 must have an empty body, got %q", w.Body.String())
		}
		if after := do(r, http.MethodGet, connPath(""), ""); after.Code != http.StatusNotFound {
			t.Errorf("the connection survived the delete: %d", after.Code)
		}
	})

	t.Run("retired returns 204", func(t *testing.T) {
		r, _ := newTestRouter(t, activeWorkspaceFixture(), retiredConnection(fixtureConnID, "c"))
		if w := do(r, http.MethodDelete, connPath(""), ""); w.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", w.Code)
		}
	})

	t.Run("active is refused", func(t *testing.T) {
		r, _ := newTestRouter(t, activeWorkspaceFixture(), activeConnection(fixtureConnID, "c"))
		w := do(r, http.MethodDelete, connPath(""), "")
		assertErrorEnvelope(t, w, http.StatusConflict, "connection_active_cannot_delete")

		if after := do(r, http.MethodGet, connPath(""), ""); after.Code != http.StatusOK {
			t.Error("a refused delete must leave the connection intact")
		}
	})

	t.Run("missing returns 404", func(t *testing.T) {
		r, _ := newTestRouter(t, activeWorkspaceFixture())
		w := do(r, http.MethodDelete, connPath(""), "")
		assertErrorEnvelope(t, w, http.StatusNotFound, "connection_not_found")
	})
}

// ---------------------------------------------------------------------------
// Verify / Activate / Retire
// ---------------------------------------------------------------------------

func TestHTTP_Verify_Success(t *testing.T) {
	r, _ := newTestRouter(t, activeWorkspaceFixture(), draftConnection(fixtureConnID, "c"))

	w := do(r, http.MethodPost, connPath("/verify"), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	var out VerifyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !out.Report.OK {
		t.Error("report.ok should be true")
	}
	if len(out.Report.Checks) == 0 {
		t.Error("the report must carry per-check results")
	}
	if out.Connection.Health != "healthy" || !out.Connection.Verified {
		t.Errorf("connection health=%q verified=%v after a passing probe",
			out.Connection.Health, out.Connection.Verified)
	}
	if out.Connection.AccessMode != "full" {
		t.Errorf("access_mode = %q, want full", out.Connection.AccessMode)
	}
}

// TestHTTP_Verify_FailedProbeStillReturns200 — the verification RAN. A 4xx
// would claim this API malfunctioned, which is not what "the provider refused
// our credentials" means.
func TestHTTP_Verify_FailedProbeStillReturns200(t *testing.T) {
	r, h := newTestRouter(t, activeWorkspaceFixture(), draftConnection(fixtureConnID, "c"))
	h.verifier.report = failedReport()

	w := do(r, http.MethodPost, connPath("/verify"), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a failed probe is a verdict, not an API error", w.Code)
	}

	var out VerifyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if out.Report.OK {
		t.Error("report.ok should be false")
	}
	if out.Connection.Health != "unhealthy" || out.Connection.Verified {
		t.Errorf("connection health=%q verified=%v", out.Connection.Health, out.Connection.Verified)
	}
}

func TestHTTP_Verify_NotFound(t *testing.T) {
	r, _ := newTestRouter(t, activeWorkspaceFixture())
	w := do(r, http.MethodPost, connPath("/verify"), "")
	assertErrorEnvelope(t, w, http.StatusNotFound, "connection_not_found")
}

func TestHTTP_Activate_FullFlow(t *testing.T) {
	r, _ := newTestRouter(t, activeWorkspaceFixture(), draftConnection(fixtureConnID, "c"))

	// Unverified first.
	assertErrorEnvelope(t, do(r, http.MethodPost, connPath("/activate"), ""),
		http.StatusConflict, "connection_not_verified")

	// Verify, then activate.
	if w := do(r, http.MethodPost, connPath("/verify"), ""); w.Code != http.StatusOK {
		t.Fatalf("verify status = %d", w.Code)
	}

	w := do(r, http.MethodPost, connPath("/activate"), "")
	if w.Code != http.StatusOK {
		t.Fatalf("activate status = %d: %s", w.Code, w.Body.String())
	}
	got := decodeConnection(t, w)
	if got.Status != "active" || got.ActivatedAt == nil {
		t.Errorf("status=%q activated_at=%v", got.Status, got.ActivatedAt)
	}

	// Not idempotent, by design.
	assertErrorEnvelope(t, do(r, http.MethodPost, connPath("/activate"), ""),
		http.StatusConflict, "connection_already_active")
}

func TestHTTP_Activate_Errors(t *testing.T) {
	tests := map[string]struct {
		ws       *workspace.Workspace
		conn     *Connection
		wantCode string
	}{
		"archived workspace": {archivedWorkspaceFixture(), verifiedConnection(fixtureConnID, "c"), "workspace_archived"},
		"retired connection": {activeWorkspaceFixture(), retiredConnection(fixtureConnID, "c"), "connection_retired"},
		"unverified":         {activeWorkspaceFixture(), draftConnection(fixtureConnID, "c"), "connection_not_verified"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r, _ := newTestRouter(t, tc.ws, tc.conn)
			w := do(r, http.MethodPost, connPath("/activate"), "")
			assertErrorEnvelope(t, w, http.StatusConflict, tc.wantCode)
		})
	}
}

// TestHTTP_Activate_ExpiredVerification drives the clock past the validity
// window rather than mocking the check.
func TestHTTP_Activate_ExpiredVerification(t *testing.T) {
	r, h := newTestRouter(t, activeWorkspaceFixture(), verifiedConnection(fixtureConnID, "c"))
	h.svc.now = func() time.Time { return testNow.Add(VerifyValidity + time.Minute) }

	w := do(r, http.MethodPost, connPath("/activate"), "")
	assertErrorEnvelope(t, w, http.StatusConflict, "connection_verification_expired")
}

func TestHTTP_Retire(t *testing.T) {
	r, _ := newTestRouter(t, activeWorkspaceFixture(), activeConnection(fixtureConnID, "c"))

	w := do(r, http.MethodPost, connPath("/retire"), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	got := decodeConnection(t, w)
	if got.Status != "retired" || got.RetiredAt == nil {
		t.Errorf("status=%q retired_at=%v", got.Status, got.RetiredAt)
	}
	// Terminal: retiring again is a conflict.
	assertErrorEnvelope(t, do(r, http.MethodPost, connPath("/retire"), ""),
		http.StatusConflict, "connection_retired")
}

// TestHTTP_InternalErrorIsOpaque pins the last line of the no-leak rule.
func TestHTTP_InternalErrorIsOpaque(t *testing.T) {
	r, h := newTestRouter(t, activeWorkspaceFixture(), draftConnection(fixtureConnID, "c"))
	h.repo.failWith = errBoom

	w := do(r, http.MethodGet, wsPath(), "")
	assertErrorEnvelope(t, w, http.StatusInternalServerError, "internal_error")
	if strings.Contains(w.Body.String(), "connection reset") {
		t.Error("the underlying error reached the client")
	}
}
