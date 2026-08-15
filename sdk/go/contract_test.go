package lightweight_test

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	lightweight "github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go"
)

// The contract fixtures.
//
// Each file in testdata/ is a response body in the shape the server produces,
// and each test below decodes one THROUGH THE PUBLIC API and asserts every field
// a developer would depend on.
//
// What this catches that nothing else does: a JSON tag renamed on either side. A
// server that starts sending `emailVerified` instead of `email_verified` breaks
// every consumer silently — the field decodes as false, nobody errors, and
// accounts quietly look unverified. There is no compile error for that, on
// either side of the boundary, so there has to be a test.
//
// It is deliberately NOT a snapshot of the OpenAPI document. A 400 KB spec diff
// fails on cosmetic churn and gets regenerated without being read, which trains
// everyone to ignore it. These fixtures are small enough that a failure is read.
//
// They are only as true as the day they were written, which is why the
// acceptance suite runs the same models against a real server.

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(raw)
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

// TestContract_User pins every field of the user model.
func TestContract_User(t *testing.T) {
	client, _ := newTestServer(t, jsonResponse(http.StatusOK, fixture(t, "user.json")))

	user, err := client.Users.Get(testContext(t), "9c1e6679-7425-40de-944b-e07fc1f90ae7")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	want := &lightweight.User{
		ID:            "9c1e6679-7425-40de-944b-e07fc1f90ae7",
		Username:      "ada@example.test",
		Email:         "ada@example.test",
		FirstName:     "Ada",
		LastName:      "Lovelace",
		Enabled:       true,
		EmailVerified: false,
		CreatedAt:     mustTime(t, "2026-08-10T14:03:11Z"),
		Attributes: map[string][]string{
			"department":  {"research"},
			"employee_id": {"e-4471"},
		},
	}
	if !reflect.DeepEqual(user, want) {
		t.Errorf("User decoded as\n  %+v\nwant\n  %+v", user, want)
	}

	// A timestamp typed as a string would be a silent downgrade for every
	// caller doing date arithmetic, so it is asserted separately.
	if user.CreatedAt.IsZero() {
		t.Error("created_at did not decode into a time.Time")
	}
}

// TestContract_UserPage pins the offset-pagination fields.
//
// First and Max are echoed by the server and are the only way a caller knows
// which page it received and whether its requested size was clamped.
func TestContract_UserPage(t *testing.T) {
	client, _ := newTestServer(t, jsonResponse(http.StatusOK, fixture(t, "user_page.json")))

	page, err := client.Users.List(testContext(t), lightweight.UserListOptions{First: 20, Max: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if page.First != 20 || page.Max != 50 || page.Count != 2 {
		t.Errorf("pagination decoded as first=%d max=%d count=%d, want 20/50/2",
			page.First, page.Max, page.Count)
	}
	if len(page.Users) != 2 {
		t.Fatalf("%d users, want 2", len(page.Users))
	}
	if page.Users[1].Enabled {
		t.Error("enabled=false did not decode; a disabled account would look active")
	}
	if !page.Users[1].EmailVerified {
		t.Error("email_verified=true did not decode")
	}
	if page.Count != len(page.Users) {
		t.Errorf("count=%d but %d users; count is documented as the page length",
			page.Count, len(page.Users))
	}
}

// TestContract_Role.
func TestContract_Role(t *testing.T) {
	client, _ := newTestServer(t, jsonResponse(http.StatusOK, fixture(t, "role.json")))

	role, err := client.Roles.Get(testContext(t), "billing-admin")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	want := &lightweight.Role{
		ID:          "2f1c8f2a-6d51-4a1e-9f2b-3c4d5e6f7a8b",
		Name:        "billing-admin",
		Description: "Can view and refund invoices",
		Composite:   false,
		Builtin:     false,
	}
	if !reflect.DeepEqual(role, want) {
		t.Errorf("Role decoded as\n  %+v\nwant\n  %+v", role, want)
	}
}

// TestContract_Session.
func TestContract_Session(t *testing.T) {
	body := `{"sessions":[` + fixture(t, "session.json") + `],"count":1}`
	client, _ := newTestServer(t, jsonResponse(http.StatusOK, body))

	sessions, err := client.Sessions.List(testContext(t))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("%d sessions, want 1", len(sessions))
	}

	want := lightweight.Session{
		ID:         "7a2b3c4d-5e6f-4708-8192-a3b4c5d6e7f8",
		UserID:     "9c1e6679-7425-40de-944b-e07fc1f90ae7",
		Username:   "ada@example.test",
		IPAddress:  "203.0.113.42",
		UserAgent:  "Mozilla/5.0",
		StartedAt:  mustTime(t, "2026-08-12T08:00:00Z"),
		LastAccess: mustTime(t, "2026-08-12T08:42:17Z"),
		Clients:    map[string]string{"account-console": "Account Console"},
	}
	if !reflect.DeepEqual(sessions[0], want) {
		t.Errorf("Session decoded as\n  %+v\nwant\n  %+v", sessions[0], want)
	}
}

// TestContract_Invitation, including the deliberately-string expiry.
func TestContract_Invitation(t *testing.T) {
	body := `{"invitations":[` + fixture(t, "invitation.json") + `],"count":1}`
	client, _ := newTestServer(t, jsonResponse(http.StatusOK, body))

	invitations, err := client.Invitations.List(testContext(t))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(invitations) != 1 {
		t.Fatalf("%d invitations, want 1", len(invitations))
	}
	inv := invitations[0]

	if inv.ID != "4d5e6f70-8192-4a3b-9c5d-6e7f8091a2b3" {
		t.Errorf("ID = %q", inv.ID)
	}
	if inv.Email != "newcomer@example.test" || inv.Status != "pending" {
		t.Errorf("email/status decoded as %q/%q", inv.Email, inv.Status)
	}
	if !reflect.DeepEqual(inv.RequiredActions, []string{"UPDATE_PASSWORD", "VERIFY_EMAIL"}) {
		t.Errorf("RequiredActions = %v", inv.RequiredActions)
	}
	if inv.InvitedBy != "operator@example.test" {
		t.Errorf("InvitedBy = %q", inv.InvitedBy)
	}
	if inv.CreatedAt != mustTime(t, "2026-08-12T10:00:00Z") {
		t.Errorf("CreatedAt = %v", inv.CreatedAt)
	}

	expiry, ok := inv.ExpiresAtTime()
	if !ok {
		t.Fatal("ExpiresAtTime reports the expiry is unparseable, but the fixture is RFC 3339")
	}
	if !expiry.Equal(mustTime(t, "2026-12-31T23:59:59Z")) {
		t.Errorf("ExpiresAtTime = %v", expiry)
	}
}

// TestContract_InvitationExpiryToleratesAValueThisAPIDidNotWrite.
//
// The expiry is read back out of a provider-side attribute that other tooling
// can also write. Typing it as a time.Time would have made an unparseable value
// fail the whole response; keeping it a string with an explicit parser means the
// rest of the invitation still decodes and the caller is told.
func TestContract_InvitationExpiryToleratesAValueThisAPIDidNotWrite(t *testing.T) {
	body := `{"invitations":[{"id":"x","email":"a@b.test","username":"a@b.test",` +
		`"required_actions":[],"expires_at":"next tuesday","created_at":"2026-08-12T10:00:00Z",` +
		`"status":"pending"}],"count":1}`
	client, _ := newTestServer(t, jsonResponse(http.StatusOK, body))

	invitations, err := client.Invitations.List(testContext(t))
	if err != nil {
		t.Fatalf("an unparseable expiry broke the whole response: %v", err)
	}
	if got := invitations[0].ExpiresAt; got != "next tuesday" {
		t.Errorf("ExpiresAt = %q, want the raw value preserved", got)
	}
	if _, ok := invitations[0].ExpiresAtTime(); ok {
		t.Error("ExpiresAtTime claims to have parsed a value that is not a timestamp")
	}
}

// TestContract_AuditPage pins the cursor model and both actor shapes.
func TestContract_AuditPage(t *testing.T) {
	client, _ := newTestServer(t, jsonResponse(http.StatusOK, fixture(t, "audit_page.json")))

	page, err := client.Audit.List(testContext(t), lightweight.AuditListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if page.Pagination.Count != 2 || page.Pagination.Limit != 50 {
		t.Errorf("pagination count=%d limit=%d", page.Pagination.Count, page.Pagination.Limit)
	}
	if page.Pagination.NextCursor != "MTc1NDg0MTM5MTAwMDAwMDAwMC4z" {
		t.Errorf("NextCursor = %q", page.Pagination.NextCursor)
	}
	if !page.HasMore() {
		t.Error("HasMore() is false while a next cursor is present")
	}
	if len(page.Items) != 2 {
		t.Fatalf("%d items, want 2", len(page.Items))
	}

	// A machine actor: project and credential, never a subject.
	machine := page.Items[0]
	if machine.Event != "user.created" || machine.Outcome != lightweight.AuditOutcomeSuccess {
		t.Errorf("event/outcome = %q/%q", machine.Event, machine.Outcome)
	}
	if machine.Actor.Type != lightweight.AuditActorProject {
		t.Errorf("actor type = %q", machine.Actor.Type)
	}
	if machine.Actor.CredentialID != "key_9b2f4c1a-1111-4222-8333-444455556666" {
		t.Errorf("credential_id = %q; without it a revocation cannot be targeted", machine.Actor.CredentialID)
	}
	if machine.Actor.Subject != "" {
		t.Errorf("a project actor carries a subject %q; the two actor shapes must stay disjoint", machine.Actor.Subject)
	}
	if machine.Resource == nil || machine.Resource.Type != "user" {
		t.Errorf("resource = %+v", machine.Resource)
	}
	if machine.Metadata["email"] != "ada@example.test" {
		t.Errorf("metadata = %v", machine.Metadata)
	}
	if machine.OccurredAt != mustTime(t, "2026-08-10T14:03:11Z") {
		t.Errorf("occurred_at = %v", machine.OccurredAt)
	}

	// A human actor and a failure: subject and email, a reason code, no resource.
	human := page.Items[1]
	if human.Actor.Type != lightweight.AuditActorOperator {
		t.Errorf("actor type = %q", human.Actor.Type)
	}
	if human.Actor.Email != "operator@example.test" || human.Actor.Subject == "" {
		t.Errorf("operator actor = %+v", human.Actor)
	}
	if human.Actor.ProjectID != "" || human.Actor.CredentialID != "" {
		t.Errorf("an operator actor carries project fields: %+v", human.Actor)
	}
	if human.Outcome != lightweight.AuditOutcomeFailure {
		t.Errorf("outcome = %q", human.Outcome)
	}
	if human.ReasonCode != lightweight.CodeProviderUnavailable {
		t.Errorf("reason_code = %q", human.ReasonCode)
	}
	if human.Resource != nil {
		t.Errorf("an event with no resource decoded one: %+v", human.Resource)
	}
}

// TestContract_AuditLastPageHasNoCursor — the absence of the cursor is the
// end-of-history signal, and a loop that checked len(Items) instead would either
// stop early or never stop.
func TestContract_AuditLastPageHasNoCursor(t *testing.T) {
	client, _ := newTestServer(t, jsonResponse(http.StatusOK,
		`{"items":[],"pagination":{"count":0,"limit":50}}`))

	page, err := client.Audit.List(testContext(t), lightweight.AuditListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if page.HasMore() {
		t.Error("HasMore() is true on a page with no next_cursor")
	}
}

// TestContract_ErrorEnvelope decodes the two shapes of the error contract.
func TestContract_ErrorEnvelope(t *testing.T) {
	t.Run("without a field", func(t *testing.T) {
		client, _ := newTestServer(t, jsonResponse(http.StatusForbidden, fixture(t, "error_envelope.json")))

		_, err := client.Audit.List(testContext(t), lightweight.AuditListOptions{})

		var apiErr *lightweight.APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("error is %T", err)
		}
		if apiErr.Code != lightweight.CodeInsufficientScope {
			t.Errorf("Code = %q", apiErr.Code)
		}
		if apiErr.Field != "" {
			t.Errorf("Field = %q; an error that is not about a field must carry none", apiErr.Field)
		}
		if apiErr.RequestID != testRequestID {
			t.Errorf("RequestID = %q", apiErr.RequestID)
		}
	})

	t.Run("with a field", func(t *testing.T) {
		client, _ := newTestServer(t, jsonResponse(http.StatusBadRequest,
			fixture(t, "error_envelope_with_field.json")))

		_, err := client.Users.Create(testContext(t), lightweight.CreateUserRequest{})

		var apiErr *lightweight.APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("error is %T", err)
		}
		if apiErr.Field != "temporary_password" {
			t.Errorf("Field = %q; without it a caller cannot say which input to fix", apiErr.Field)
		}
	})
}
