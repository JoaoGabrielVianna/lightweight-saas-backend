package lightweight_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	lightweight "github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go"
)

// The request-shape suite.
//
// Every method is asserted on the HTTP request it produces: verb, path, query,
// and body. This is the half of the contract a response fixture cannot check —
// a method that decodes a user perfectly is still broken if it sent PUT to the
// collection.
//
// The paths here are written out literally rather than built with a helper. A
// helper would be the same code the SDK uses to build them, so a bug in it would
// cancel out and the test would pass on a wrong URL.

const (
	testUserID       = "9c1e6679-7425-40de-944b-e07fc1f90ae7"
	testSessionID    = "7a2b3c4d-5e6f-4708-8192-a3b4c5d6e7f8"
	testInvitationID = "4d5e6f70-8192-4a3b-9c5d-6e7f8091a2b3"
	wsPrefix         = "/v1/workspaces/" + testWorkspace
)

// assertRequest checks the verb and path of the last request.
func assertRequest(t *testing.T, ts *testServer, method, path string) capturedRequest {
	t.Helper()
	req := ts.last(t)
	if req.Method != method {
		t.Errorf("method = %s, want %s", req.Method, method)
	}
	if req.Path != path {
		t.Errorf("path = %q, want %q", req.Path, path)
	}
	return req
}

// assertJSONBody checks the request body decodes to want.
func assertJSONBody(t *testing.T, req capturedRequest, want map[string]any) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(req.Body), &got); err != nil {
		t.Fatalf("request body is not JSON (%q): %v", req.Body, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("request body =\n  %v\nwant\n  %v", got, want)
	}
	if ct := req.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestUsers_RequestShapes(t *testing.T) {
	ctx := testContext(t)

	t.Run("List sends only the options that were set", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusOK, `{"users":[],"first":0,"max":20,"count":0}`))

		if _, err := client.Users.List(ctx, lightweight.UserListOptions{}); err != nil {
			t.Fatalf("List: %v", err)
		}
		req := assertRequest(t, ts, http.MethodGet, wsPrefix+"/users")
		if req.Query != "" {
			t.Errorf("query = %q, want empty when no options were set", req.Query)
		}
		if req.Body != "" {
			t.Errorf("a GET carried a body: %q", req.Body)
		}

		if _, err := client.Users.List(ctx, lightweight.UserListOptions{
			Search: "ada lovelace", First: 40, Max: 50,
		}); err != nil {
			t.Fatalf("List: %v", err)
		}
		req = ts.last(t)
		if req.Query != "first=40&max=50&search=ada+lovelace" {
			t.Errorf("query = %q", req.Query)
		}
	})

	t.Run("Get", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusOK, fixture(t, "user.json")))
		if _, err := client.Users.Get(ctx, testUserID); err != nil {
			t.Fatalf("Get: %v", err)
		}
		assertRequest(t, ts, http.MethodGet, wsPrefix+"/users/"+testUserID)
	})

	t.Run("Create sends the documented body", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusCreated, fixture(t, "user.json")))

		if _, err := client.Users.Create(ctx, lightweight.CreateUserRequest{
			Email:             "ada@example.test",
			FirstName:         "Ada",
			LastName:          "Lovelace",
			TemporaryPassword: "ch4nge-me-now",
			Roles:             []string{"support"},
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		req := assertRequest(t, ts, http.MethodPost, wsPrefix+"/users")
		assertJSONBody(t, req, map[string]any{
			"email":              "ada@example.test",
			"first_name":         "Ada",
			"last_name":          "Lovelace",
			"temporary_password": "ch4nge-me-now",
			"roles":              []any{"support"},
		})
	})

	t.Run("Update omits fields that were not supplied", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusOK, fixture(t, "user.json")))

		if _, err := client.Users.Update(ctx, testUserID, lightweight.UpdateUserRequest{
			Enabled: lightweight.Bool(false),
		}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		req := assertRequest(t, ts, http.MethodPatch, wsPrefix+"/users/"+testUserID)

		// The whole point of the pointer fields: a patch that named first_name
		// would clear it, which is not what the caller asked for.
		assertJSONBody(t, req, map[string]any{"enabled": false})
	})

	t.Run("Update can set a field to its zero value", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusOK, fixture(t, "user.json")))

		if _, err := client.Users.Update(ctx, testUserID, lightweight.UpdateUserRequest{
			LastName: lightweight.String(""),
		}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		assertJSONBody(t, ts.last(t), map[string]any{"last_name": ""})
	})

	t.Run("Delete", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusNoContent, ``))
		if err := client.Users.Delete(ctx, testUserID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		assertRequest(t, ts, http.MethodDelete, wsPrefix+"/users/"+testUserID)
	})

	t.Run("SendPasswordReset", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusAccepted, ``))
		if err := client.Users.SendPasswordReset(ctx, testUserID); err != nil {
			t.Fatalf("SendPasswordReset: %v", err)
		}
		req := assertRequest(t, ts, http.MethodPost, wsPrefix+"/users/"+testUserID+"/reset-password")
		if req.Body != "" {
			t.Errorf("a body was sent for an operation that takes none: %q", req.Body)
		}
	})
}

// TestUsers_NoDirectPasswordSetting.
//
// Setting a password directly is operator-only on purpose: it is a complete
// account-takeover primitive, and the reset flow covers the legitimate need
// without one. This package must not offer a method for it, however convenient
// that would look, because every key issued under a looser rule would keep the
// capability.
func TestUsers_NoDirectPasswordSetting(t *testing.T) {
	forbidden := []string{"SetPassword", "UpdatePassword", "ChangePassword", "ResetPasswordTo"}

	svcType := reflect.TypeOf(&lightweight.UsersService{})
	for i := 0; i < svcType.NumMethod(); i++ {
		name := svcType.Method(i).Name
		for _, bad := range forbidden {
			if name == bad {
				t.Errorf("UsersService exposes %s.\n\n"+
					"PUT .../users/{id}/password is operator-only. A Project Credential cannot\n"+
					"call it, so a method here could only ever produce a 403 — and offering it\n"+
					"would suggest a scope exists that would make it work.", name)
			}
		}
	}
}

func TestRoles_RequestShapes(t *testing.T) {
	ctx := testContext(t)

	t.Run("List unwraps the envelope", func(t *testing.T) {
		body := `{"roles":[` + fixture(t, "role.json") + `],"count":1}`
		client, ts := newTestServer(t, jsonResponse(http.StatusOK, body))

		roles, err := client.Roles.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		assertRequest(t, ts, http.MethodGet, wsPrefix+"/roles")
		if len(roles) != 1 || roles[0].Name != "billing-admin" {
			t.Errorf("roles = %+v", roles)
		}
	})

	t.Run("Get", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusOK, fixture(t, "role.json")))
		if _, err := client.Roles.Get(ctx, "billing-admin"); err != nil {
			t.Fatalf("Get: %v", err)
		}
		assertRequest(t, ts, http.MethodGet, wsPrefix+"/roles/billing-admin")
	})

	t.Run("Create", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusCreated, fixture(t, "role.json")))
		if _, err := client.Roles.Create(ctx, lightweight.CreateRoleRequest{
			Name: "billing-admin", Description: "Can view and refund invoices",
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		req := assertRequest(t, ts, http.MethodPost, wsPrefix+"/roles")
		assertJSONBody(t, req, map[string]any{
			"name": "billing-admin", "description": "Can view and refund invoices",
		})
	})

	t.Run("Update", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusOK, fixture(t, "role.json")))
		if _, err := client.Roles.Update(ctx, "billing-admin", lightweight.UpdateRoleRequest{
			Description: lightweight.String("Now also issues credit notes"),
		}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		req := assertRequest(t, ts, http.MethodPatch, wsPrefix+"/roles/billing-admin")
		assertJSONBody(t, req, map[string]any{"description": "Now also issues credit notes"})
	})

	t.Run("Delete", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusNoContent, ``))
		if err := client.Roles.Delete(ctx, "billing-admin"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		assertRequest(t, ts, http.MethodDelete, wsPrefix+"/roles/billing-admin")
	})

	t.Run("ListUsers returns the user page shape", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusOK, fixture(t, "user_page.json")))
		page, err := client.Roles.ListUsers(ctx, "billing-admin")
		if err != nil {
			t.Fatalf("ListUsers: %v", err)
		}
		assertRequest(t, ts, http.MethodGet, wsPrefix+"/roles/billing-admin/users")
		if len(page.Users) != 2 {
			t.Errorf("%d users", len(page.Users))
		}
	})

	t.Run("ListForUser", func(t *testing.T) {
		body := `{"roles":[` + fixture(t, "role.json") + `],"count":1}`
		client, ts := newTestServer(t, jsonResponse(http.StatusOK, body))
		if _, err := client.Roles.ListForUser(ctx, testUserID); err != nil {
			t.Fatalf("ListForUser: %v", err)
		}
		assertRequest(t, ts, http.MethodGet, wsPrefix+"/users/"+testUserID+"/roles")
	})

	t.Run("Grant", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusNoContent, ``))
		if err := client.Roles.Grant(ctx, testUserID, "support", "billing-admin"); err != nil {
			t.Fatalf("Grant: %v", err)
		}
		req := assertRequest(t, ts, http.MethodPost, wsPrefix+"/users/"+testUserID+"/roles")
		assertJSONBody(t, req, map[string]any{"roles": []any{"support", "billing-admin"}})
	})

	t.Run("Revoke is singular", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusNoContent, ``))
		if err := client.Roles.Revoke(ctx, testUserID, "billing-admin"); err != nil {
			t.Fatalf("Revoke: %v", err)
		}
		assertRequest(t, ts, http.MethodDelete, wsPrefix+"/users/"+testUserID+"/roles/billing-admin")
	})
}

func TestSessions_RequestShapes(t *testing.T) {
	ctx := testContext(t)
	empty := `{"sessions":[],"count":0}`

	t.Run("List is workspace-wide", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusOK, empty))
		if _, err := client.Sessions.List(ctx); err != nil {
			t.Fatalf("List: %v", err)
		}
		assertRequest(t, ts, http.MethodGet, wsPrefix+"/sessions")
	})

	t.Run("ListForUser is scoped to one user", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusOK, empty))
		if _, err := client.Sessions.ListForUser(ctx, testUserID); err != nil {
			t.Fatalf("ListForUser: %v", err)
		}
		assertRequest(t, ts, http.MethodGet, wsPrefix+"/users/"+testUserID+"/sessions")
	})

	t.Run("Revoke ends one session", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusNoContent, ``))
		if err := client.Sessions.Revoke(ctx, testSessionID); err != nil {
			t.Fatalf("Revoke: %v", err)
		}
		assertRequest(t, ts, http.MethodDelete, wsPrefix+"/sessions/"+testSessionID)
	})

	t.Run("RevokeAllForUser ends all of one user's", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusNoContent, ``))
		if err := client.Sessions.RevokeAllForUser(ctx, testUserID); err != nil {
			t.Fatalf("RevokeAllForUser: %v", err)
		}
		assertRequest(t, ts, http.MethodDelete, wsPrefix+"/users/"+testUserID+"/sessions")
	})
}

// TestSessions_NoWorkspaceWideRevocation.
//
// The API has no operation that signs out an entire workspace. If one is ever
// added, it must not arrive here under a name a reader could mistake for the
// per-user or per-session variants — which is what this test is really pinning.
func TestSessions_NoWorkspaceWideRevocation(t *testing.T) {
	svcType := reflect.TypeOf(&lightweight.SessionsService{})

	for i := 0; i < svcType.NumMethod(); i++ {
		name := svcType.Method(i).Name
		if name == "RevokeAll" || name == "DeleteAll" || name == "LogoutEveryone" {
			t.Errorf("SessionsService exposes %s.\n\n"+
				"There is no workspace-wide sign-out in the API. A method with a name this\n"+
				"broad would either be a lie or the most destructive call in the package\n"+
				"hiding next to the safe ones.", name)
		}
	}
}

func TestInvitations_RequestShapes(t *testing.T) {
	ctx := testContext(t)

	t.Run("List", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusOK, `{"invitations":[],"count":0}`))
		if _, err := client.Invitations.List(ctx); err != nil {
			t.Fatalf("List: %v", err)
		}
		assertRequest(t, ts, http.MethodGet, wsPrefix+"/invitations")
	})

	t.Run("Create without an expiry omits the field", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusCreated, fixture(t, "invitation.json")))

		if _, err := client.Invitations.Create(ctx, lightweight.CreateInvitationRequest{
			Email: "newcomer@example.test", Roles: []string{"support"},
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		req := assertRequest(t, ts, http.MethodPost, wsPrefix+"/invitations")
		assertJSONBody(t, req, map[string]any{
			"email": "newcomer@example.test", "roles": []any{"support"},
		})
	})

	t.Run("Create serialises the expiry as RFC 3339 in UTC", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusCreated, fixture(t, "invitation.json")))

		// Deliberately in a non-UTC zone: the wire format is UTC, and a client
		// that sent a local offset would be at the mercy of the server's parser.
		zone := time.FixedZone("UTC-3", -3*60*60)
		expiry := time.Date(2026, 12, 31, 20, 59, 59, 0, zone)

		if _, err := client.Invitations.Create(ctx, lightweight.CreateInvitationRequest{
			Email: "newcomer@example.test", Roles: []string{"support"}, ExpiresAt: &expiry,
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		assertJSONBody(t, ts.last(t), map[string]any{
			"email":      "newcomer@example.test",
			"roles":      []any{"support"},
			"expires_at": "2026-12-31T23:59:59Z",
		})
	})

	t.Run("Resend", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusOK, fixture(t, "invitation.json")))
		if _, err := client.Invitations.Resend(ctx, testInvitationID); err != nil {
			t.Fatalf("Resend: %v", err)
		}
		assertRequest(t, ts, http.MethodPost, wsPrefix+"/invitations/"+testInvitationID+"/resend")
	})

	t.Run("Revoke", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusNoContent, ``))
		if err := client.Invitations.Revoke(ctx, testInvitationID); err != nil {
			t.Fatalf("Revoke: %v", err)
		}
		assertRequest(t, ts, http.MethodDelete, wsPrefix+"/invitations/"+testInvitationID)
	})
}

func TestAudit_RequestShapes(t *testing.T) {
	ctx := testContext(t)

	t.Run("List sends only the filters that were set", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusOK, fixture(t, "audit_page.json")))

		if _, err := client.Audit.List(ctx, lightweight.AuditListOptions{}); err != nil {
			t.Fatalf("List: %v", err)
		}
		req := assertRequest(t, ts, http.MethodGet, wsPrefix+"/audit")
		if req.Query != "" {
			t.Errorf("query = %q, want empty", req.Query)
		}
	})

	t.Run("List renders every filter", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusOK, fixture(t, "audit_page.json")))

		from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
		to := time.Date(2026, 8, 31, 23, 59, 59, 0, time.UTC)

		if _, err := client.Audit.List(ctx, lightweight.AuditListOptions{
			Event:     "user.created",
			ActorType: lightweight.AuditActorProject,
			Outcome:   lightweight.AuditOutcomeFailure,
			From:      from,
			To:        to,
			Cursor:    "MTc1NDg0MTM5MTAwMDAwMDAwMC4z",
			Limit:     200,
		}); err != nil {
			t.Fatalf("List: %v", err)
		}

		got := ts.last(t).Query
		for _, want := range []string{
			"event=user.created",
			"actor_type=project",
			"outcome=failure",
			"from=2026-08-01T00%3A00%3A00Z",
			"to=2026-08-31T23%3A59%3A59Z",
			"cursor=MTc1NDg0MTM5MTAwMDAwMDAwMC4z",
			"limit=200",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("query %q is missing %q", got, want)
			}
		}
	})

	t.Run("All follows the server's cursor and stops when it is absent", func(t *testing.T) {
		pages := []string{
			`{"items":[{"id":"evt_1","event":"user.created","outcome":"success",` +
				`"actor":{"type":"project"},"occurred_at":"2026-08-10T14:03:11Z"}],` +
				`"pagination":{"count":1,"limit":1,"next_cursor":"CURSOR-2"}}`,
			`{"items":[{"id":"evt_2","event":"user.deleted","outcome":"success",` +
				`"actor":{"type":"project"},"occurred_at":"2026-08-10T14:02:00Z"}],` +
				`"pagination":{"count":1,"limit":1}}`,
		}
		var served int
		client, ts := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			body := pages[min(served, len(pages)-1)]
			served++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		})

		var ids []string
		for ev, err := range client.Audit.All(ctx, lightweight.AuditListOptions{Limit: 1}) {
			if err != nil {
				t.Fatalf("All: %v", err)
			}
			ids = append(ids, ev.ID)
		}

		if !reflect.DeepEqual(ids, []string{"evt_1", "evt_2"}) {
			t.Errorf("ids = %v, want [evt_1 evt_2]", ids)
		}
		if ts.count() != 2 {
			t.Errorf("%d requests; a correct cursor loop makes exactly one per page", ts.count())
		}
		if !strings.Contains(ts.last(t).Query, "cursor=CURSOR-2") {
			t.Errorf("the second request did not carry the server's cursor: %q", ts.last(t).Query)
		}
	})

	t.Run("All honours break in the loop body", func(t *testing.T) {
		// A server with an endless cursor: without an honoured break this would
		// page forever, which is exactly the bug worth pinning.
		client, ts := newTestServer(t, jsonResponse(http.StatusOK,
			`{"items":[{"id":"evt_1","event":"x","outcome":"success","actor":{"type":"project"},`+
				`"occurred_at":"2026-08-10T14:03:11Z"}],"pagination":{"count":1,"limit":1,"next_cursor":"C2"}}`))

		var seen int
		for range client.Audit.All(ctx, lightweight.AuditListOptions{}) {
			seen++
			break
		}

		if seen != 1 {
			t.Errorf("the body ran %d times after breaking on the first", seen)
		}
		if n := ts.count(); n != 1 {
			t.Errorf("%d requests; breaking must stop the iterator before the next page", n)
		}
	})

	t.Run("All yields the error once and then stops", func(t *testing.T) {
		client, ts := newTestServer(t, jsonResponse(http.StatusForbidden,
			errorEnvelope(lightweight.CodeInsufficientScope, "no audit:read")))

		var pairs int
		var lastErr error
		// The body deliberately does NOT break on the error: an iterator that
		// kept going would spin here, and the assertion below is what catches it.
		// (No t.Fatal inside the body — aborting a range-over-func from its own
		// loop body is its own kind of hang.)
		for _, err := range client.Audit.All(ctx, lightweight.AuditListOptions{}) {
			pairs++
			lastErr = err
			if pairs > 5 {
				break
			}
		}

		if pairs != 1 {
			t.Errorf("%d pairs yielded, want exactly one carrying the error", pairs)
		}
		var apiErr *lightweight.APIError
		if !errors.As(lastErr, &apiErr) || apiErr.Code != lightweight.CodeInsufficientScope {
			t.Errorf("the yielded error is %v", lastErr)
		}
		if ts.count() != 1 {
			t.Errorf("%d requests after a failure", ts.count())
		}
	})

	t.Run("All does not modify the caller's options", func(t *testing.T) {
		client, _ := newTestServer(t, jsonResponse(http.StatusOK,
			`{"items":[{"id":"evt_1","event":"x","outcome":"success","actor":{"type":"project"},`+
				`"occurred_at":"2026-08-10T14:03:11Z"}],"pagination":{"count":1,"limit":1,"next_cursor":"C2"}}`))

		opts := lightweight.AuditListOptions{Cursor: "START"}
		for range client.Audit.All(ctx, opts) {
			break
		}
		if opts.Cursor != "START" {
			t.Errorf("the caller's options were mutated: Cursor = %q", opts.Cursor)
		}
	})
}
