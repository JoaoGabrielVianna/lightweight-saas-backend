package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
)

// captureRecorder collects every audit.Event emitted during a test so
// assertions can read them back. Safe for concurrent dispatch (the
// recorder contract requires it) although handler tests are single-flow.
type captureRecorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *captureRecorder) Record(_ context.Context, e audit.Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *captureRecorder) snapshot() []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]audit.Event, len(r.events))
	copy(out, r.events)
	return out
}

// install swaps a fresh captureRecorder into the audit registry and
// restores the previous recorder when the test finishes.
func install(t *testing.T) *captureRecorder {
	t.Helper()
	rec := &captureRecorder{}
	prev := audit.SetDefault(rec)
	t.Cleanup(func() { audit.SetDefault(prev) })
	return rec
}

// adminIdentity is the canonical authenticated caller used across the
// audit tests. The Subject is a stable UUID so failure messages are
// easy to grep.
const (
	adminSubject = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	adminEmail   = "admin@example.com"
	adminUser    = "adminuser"

	targetSubject  = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	targetEmail    = "target@example.com"
	targetSession  = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	clientIPHeader = "127.0.0.1"
)

// buildHandler returns a fresh Handler backed by an in-memory fakeProvider.
// Caller mutates fp.* to stage success / failure cases before invoking
// the handler.
func buildHandler() (*Handler, *fakeProvider) {
	fp := &fakeProvider{}
	return NewHandler(NewService(fp)), fp
}

// newRequest is a small wrapper around httptest.NewRecorder + a fresh
// gin context with the admin identity pre-stashed. Method/path are
// arbitrary — gin only uses path params we set via gin.Params.
func newRequest(t *testing.T, method, path string, body any, params gin.Params) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	var bodyReader *bytes.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		bodyReader = bytes.NewReader(buf)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = clientIPHeader + ":1234"
	c.Request = req
	c.Params = params

	auth.StoreIdentity(c, &auth.Identity{
		Subject:  adminSubject,
		Email:    adminEmail,
		Username: adminUser,
		Roles:    []string{"admin"},
	})
	return w, c
}

// assertRequiredFields verifies the v0.2 audit invariants on a single
// event: who/action/target.kind/timestamp/ip MUST be populated.
func assertRequiredFields(t *testing.T, e audit.Event, wantAction audit.Action, wantTargetKind string) {
	t.Helper()
	if e.Action != wantAction {
		t.Errorf("Action = %q, want %q", e.Action, wantAction)
	}
	if e.Actor.Subject != adminSubject {
		t.Errorf("Actor.Subject = %q, want %q", e.Actor.Subject, adminSubject)
	}
	if e.Actor.Email != adminEmail {
		t.Errorf("Actor.Email = %q, want %q", e.Actor.Email, adminEmail)
	}
	if e.Target.Kind != wantTargetKind {
		t.Errorf("Target.Kind = %q, want %q", e.Target.Kind, wantTargetKind)
	}
	if e.Timestamp.IsZero() {
		t.Errorf("Timestamp is zero — audit invariant violated")
	}
	if e.IP == "" {
		t.Errorf("IP is empty — audit invariant violated")
	}
}

// ─── Success-path coverage: every mutation emits the right event ─────────

func TestAudit_CreateRole_Success(t *testing.T) {
	rec := install(t)
	h, _ := buildHandler()

	w, c := newRequest(t, "POST", "/admin/roles",
		CreateRoleRequestBody{Name: "support", Description: "support team"}, nil)
	h.CreateRole(c)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	assertRequiredFields(t, events[0], audit.ActionRoleCreated, "role")
	if events[0].Target.Name != "support" {
		t.Errorf("Target.Name = %q, want %q", events[0].Target.Name, "support")
	}
	if events[0].Reason != "" {
		t.Errorf("Reason should be empty on success, got %q", events[0].Reason)
	}
}

func TestAudit_CreateInvitation_EmitsBothInvitationAndUser(t *testing.T) {
	rec := install(t)
	h, _ := buildHandler()

	w, c := newRequest(t, "POST", "/admin/invitations",
		CreateInvitationRequestBody{Email: targetEmail, Roles: []string{"user"}}, nil)
	h.CreateInvitation(c)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	events := rec.snapshot()
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2 (invitation+user)", len(events))
	}
	assertRequiredFields(t, events[0], audit.ActionInvitationCreated, "invitation")
	assertRequiredFields(t, events[1], audit.ActionUserCreated, "user")
	if events[0].Target.Name != targetEmail {
		t.Errorf("invitation Target.Name = %q, want %q", events[0].Target.Name, targetEmail)
	}
	if events[1].Target.Name != targetEmail {
		t.Errorf("user Target.Name = %q, want %q", events[1].Target.Name, targetEmail)
	}
}

func TestAudit_UpdateUser_Success(t *testing.T) {
	rec := install(t)
	h, _ := buildHandler()

	enabled := true
	w, c := newRequest(t, "PATCH", "/admin/users/"+targetSubject,
		UpdateUserRequestBody{Enabled: &enabled},
		gin.Params{{Key: "id", Value: targetSubject}})
	h.UpdateUser(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	assertRequiredFields(t, events[0], audit.ActionUserUpdated, "user")
	if events[0].Target.ID != targetSubject {
		t.Errorf("Target.ID = %q, want %q", events[0].Target.ID, targetSubject)
	}
}

func TestAudit_UpdateRole_Success(t *testing.T) {
	rec := install(t)
	h, _ := buildHandler()

	desc := "new desc"
	w, c := newRequest(t, "PATCH", "/admin/roles/support",
		UpdateRoleRequestBody{Description: &desc},
		gin.Params{{Key: "name", Value: "support"}})
	h.UpdateRole(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	assertRequiredFields(t, events[0], audit.ActionRoleUpdated, "role")
	if events[0].Target.ID != "support" {
		t.Errorf("Target.ID = %q, want support", events[0].Target.ID)
	}
}

func TestAudit_AssignRolesToUser_CarriesRolesInExtra(t *testing.T) {
	rec := install(t)
	h, _ := buildHandler()

	w, c := newRequest(t, "POST", "/admin/users/"+targetSubject+"/roles",
		AssignRolesRequestBody{Roles: []string{"editor", "support"}},
		gin.Params{{Key: "id", Value: targetSubject}})
	h.AssignRolesToUser(c)

	// c.Status(204) on the test recorder stores the status on gin's Writer
	// but doesn't always flush to the underlying ResponseRecorder; read the
	// cached status instead.
	if c.Writer.Status() != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", c.Writer.Status(), w.Body.String())
	}
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	assertRequiredFields(t, events[0], audit.ActionUserRolesGranted, "user")
	roles, ok := events[0].Extra["roles"].([]string)
	if !ok {
		t.Fatalf("Extra[roles] type = %T, want []string", events[0].Extra["roles"])
	}
	if strings.Join(roles, ",") != "editor,support" {
		t.Errorf("Extra[roles] = %v, want [editor support]", roles)
	}
}

func TestAudit_UnassignRoleFromUser_Success(t *testing.T) {
	rec := install(t)
	h, _ := buildHandler()

	w, c := newRequest(t, "DELETE", "/admin/users/"+targetSubject+"/roles/editor", nil,
		gin.Params{{Key: "id", Value: targetSubject}, {Key: "name", Value: "editor"}})
	h.UnassignRoleFromUser(c)

	// c.Status(204) on the test recorder stores the status on gin's Writer
	// but doesn't always flush to the underlying ResponseRecorder; read the
	// cached status instead.
	if c.Writer.Status() != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", c.Writer.Status(), w.Body.String())
	}
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	assertRequiredFields(t, events[0], audit.ActionUserRoleRevoked, "user")
	if events[0].Target.Name != "editor" {
		t.Errorf("Target.Name = %q, want editor", events[0].Target.Name)
	}
}

func TestAudit_ResetUserPassword_Success(t *testing.T) {
	rec := install(t)
	h, _ := buildHandler()

	w, c := newRequest(t, "POST", "/admin/users/"+targetSubject+"/reset-password", nil,
		gin.Params{{Key: "id", Value: targetSubject}})
	h.ResetUserPassword(c)

	// c.Status(204) on the test recorder stores the status on gin's Writer
	// but doesn't always flush to the underlying ResponseRecorder; read the
	// cached status instead.
	if c.Writer.Status() != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", c.Writer.Status(), w.Body.String())
	}
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	assertRequiredFields(t, events[0], audit.ActionUserPasswordReset, "user")
}

func TestAudit_ResendInvitation_Success(t *testing.T) {
	rec := install(t)
	h, _ := buildHandler()

	w, c := newRequest(t, "POST", "/admin/invitations/"+targetSubject+"/resend", nil,
		gin.Params{{Key: "id", Value: targetSubject}})
	h.ResendInvitation(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	assertRequiredFields(t, events[0], audit.ActionInvitationResent, "invitation")
}

func TestAudit_DeleteUser_Success(t *testing.T) {
	rec := install(t)
	h, fp := buildHandler()
	// last-admin guard runs ListUsersByRole("admin"); stage two admins so
	// deleting one doesn't trip the safety check.
	fp.mutationCalls.adminsForLastCheck = []User{
		{ID: adminSubject, Enabled: true},
		{ID: "other-admin", Enabled: true},
	}

	w, c := newRequest(t, "DELETE", "/admin/users/"+targetSubject, nil,
		gin.Params{{Key: "id", Value: targetSubject}})
	h.DeleteUser(c)

	// c.Status(204) on the test recorder stores the status on gin's Writer
	// but doesn't always flush to the underlying ResponseRecorder; read the
	// cached status instead.
	if c.Writer.Status() != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", c.Writer.Status(), w.Body.String())
	}
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	assertRequiredFields(t, events[0], audit.ActionUserDeleted, "user")
}

func TestAudit_DeleteRole_Success(t *testing.T) {
	rec := install(t)
	h, _ := buildHandler()

	w, c := newRequest(t, "DELETE", "/admin/roles/support", nil,
		gin.Params{{Key: "name", Value: "support"}})
	h.DeleteRole(c)

	// c.Status(204) on the test recorder stores the status on gin's Writer
	// but doesn't always flush to the underlying ResponseRecorder; read the
	// cached status instead.
	if c.Writer.Status() != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", c.Writer.Status(), w.Body.String())
	}
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	assertRequiredFields(t, events[0], audit.ActionRoleDeleted, "role")
}

func TestAudit_DeleteSession_Success(t *testing.T) {
	rec := install(t)
	h, _ := buildHandler()

	w, c := newRequest(t, "DELETE", "/admin/sessions/"+targetSession, nil,
		gin.Params{{Key: "id", Value: targetSession}})
	h.DeleteSession(c)

	// c.Status(204) on the test recorder stores the status on gin's Writer
	// but doesn't always flush to the underlying ResponseRecorder; read the
	// cached status instead.
	if c.Writer.Status() != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", c.Writer.Status(), w.Body.String())
	}
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	assertRequiredFields(t, events[0], audit.ActionSessionRevoked, "session")
}

func TestAudit_LogoutUserSessions_Success(t *testing.T) {
	rec := install(t)
	h, _ := buildHandler()

	w, c := newRequest(t, "DELETE", "/admin/users/"+targetSubject+"/sessions", nil,
		gin.Params{{Key: "id", Value: targetSubject}})
	h.LogoutUserSessions(c)

	// c.Status(204) on the test recorder stores the status on gin's Writer
	// but doesn't always flush to the underlying ResponseRecorder; read the
	// cached status instead.
	if c.Writer.Status() != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", c.Writer.Status(), w.Body.String())
	}
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	assertRequiredFields(t, events[0], audit.ActionUserSessionsLoggedOut, "user")
}

func TestAudit_DeleteInvitation_Success(t *testing.T) {
	rec := install(t)
	h, _ := buildHandler()

	w, c := newRequest(t, "DELETE", "/admin/invitations/"+targetSubject, nil,
		gin.Params{{Key: "id", Value: targetSubject}})
	h.DeleteInvitation(c)

	// c.Status(204) on the test recorder stores the status on gin's Writer
	// but doesn't always flush to the underlying ResponseRecorder; read the
	// cached status instead.
	if c.Writer.Status() != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", c.Writer.Status(), w.Body.String())
	}
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	assertRequiredFields(t, events[0], audit.ActionInvitationRevoked, "invitation")
}

// ─── Failure-path coverage: Reason MUST be populated ─────────────────────

func TestAudit_DeleteUser_Failure_CarriesReason(t *testing.T) {
	rec := install(t)
	h, fp := buildHandler()
	fp.mutationCalls.deleteUserErr = errors.New("upstream rejected delete")
	fp.mutationCalls.adminsForLastCheck = []User{
		{ID: adminSubject, Enabled: true},
		{ID: "other-admin", Enabled: true},
	}

	_, c := newRequest(t, "DELETE", "/admin/users/"+targetSubject, nil,
		gin.Params{{Key: "id", Value: targetSubject}})
	h.DeleteUser(c)

	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1 (failure still audits)", len(events))
	}
	assertRequiredFields(t, events[0], audit.ActionUserDeleted, "user")
	if events[0].Reason == "" {
		t.Error("Reason MUST be populated on failure — invariant violated")
	}
	if !strings.Contains(events[0].Reason, "upstream rejected delete") {
		t.Errorf("Reason = %q, want it to contain the upstream error", events[0].Reason)
	}
}

func TestAudit_AssignRoles_Failure_CarriesReason(t *testing.T) {
	rec := install(t)
	h, fp := buildHandler()
	fp.mutationCalls.assignErr = errors.New("role not found")

	_, c := newRequest(t, "POST", "/admin/users/"+targetSubject+"/roles",
		AssignRolesRequestBody{Roles: []string{"missing"}},
		gin.Params{{Key: "id", Value: targetSubject}})
	h.AssignRolesToUser(c)

	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1 (failure still audits)", len(events))
	}
	if events[0].Reason == "" {
		t.Error("Reason MUST be populated on failure — invariant violated")
	}
}

func TestAudit_CreateRole_Failure_StillEmitsForBothInvitationAndUser(t *testing.T) {
	rec := install(t)
	h, fp := buildHandler()
	fp.createCalls.createRoleErr = errors.New("upstream conflict")

	_, c := newRequest(t, "POST", "/admin/roles",
		CreateRoleRequestBody{Name: "support"}, nil)
	h.CreateRole(c)

	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	if events[0].Reason == "" {
		t.Error("Reason MUST be populated on failure — invariant violated")
	}
}
