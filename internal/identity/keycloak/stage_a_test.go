// Tests for v0.2 Stage A read methods. Driven by an httptest stub of the
// Keycloak Admin API surface — same pattern as provider_test.go.
//
// Each test owns its own stub so failures don't cross-contaminate.

package keycloak

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
)

// newStageAStub spins a Keycloak-stub server whose handler is a map from
// (METHOD, PATH-suffix) → response. The token endpoint is wired generically.
// Tests register one or more handlers via stub.on("GET", "/roles", ...).
type stageAStub struct {
	srv      *httptest.Server
	handlers map[string]func(r *http.Request) (int, string)
	misses   atomic.Int32
}

func newStageAStub(t *testing.T) *stageAStub {
	t.Helper()
	s := &stageAStub{handlers: map[string]func(*http.Request) (int, string){}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":300}`))
			return
		}
		// Match against any registered admin path suffix.
		for prefix, fn := range s.handlers {
			if r.Method+" "+r.URL.Path == prefix || strings.HasSuffix(r.URL.Path, prefix) {
				status, body := fn(r)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
				return
			}
		}
		s.misses.Add(1)
		t.Logf("stub miss: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stageAStub) on(method, suffix string, fn func(r *http.Request) (int, string)) {
	s.handlers[method+" "+suffix] = fn
}

func newStageAProvider(t *testing.T, s *stageAStub) *Provider {
	t.Helper()
	p, err := NewProvider(AdminConfig{
		BaseURL:      s.srv.URL,
		Realm:        "saas",
		ClientID:     "saas-backend-admin",
		ClientSecret: "secret",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return p
}

// ─────────────────────────── Roles ───────────────────────────

func TestListRoles_DropsClientRoles(t *testing.T) {
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/roles", func(*http.Request) (int, string) {
		return 200, `[
			{"id":"r1","name":"admin","description":"god","composite":true, "clientRole":false},
			{"id":"r2","name":"user","description":"","composite":false,"clientRole":false},
			{"id":"r3","name":"view-users","description":"","clientRole":true},
			{"id":"r4","name":"offline_access","clientRole":false}
		]`
	})
	p := newStageAProvider(t, s)

	roles, err := p.ListRoles(context.Background())
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	// client roles MUST be filtered out — admin surface is realm-only.
	if len(roles) != 3 {
		t.Fatalf("expected 3 realm roles, got %d: %+v", len(roles), roles)
	}
	// Composite flag flows through.
	if !roles[0].Composite {
		t.Errorf("admin role should be composite")
	}
	// Built-in detection.
	var seenOffline bool
	for _, r := range roles {
		if r.Name == "offline_access" {
			seenOffline = true
			if !r.Builtin {
				t.Errorf("offline_access should be flagged builtin")
			}
		}
	}
	if !seenOffline {
		t.Errorf("offline_access role missing from output")
	}
}

func TestGetRole_URLEscapesName(t *testing.T) {
	s := newStageAStub(t)
	var escaped string
	// Go's stdlib decodes r.URL.Path before exposing it, so the stub key
	// matches the DECODED form. EscapedPath() reconstructs the on-the-wire
	// encoded form (RawPath is only populated when the server's parser
	// preserved it — not always).
	s.on("GET", "/admin/realms/saas/roles/team lead", func(r *http.Request) (int, string) {
		escaped = r.URL.EscapedPath()
		return 200, `{"id":"r9","name":"team lead","composite":false}`
	})
	p := newStageAProvider(t, s)

	role, err := p.GetRole(context.Background(), "team lead")
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if !strings.Contains(escaped, "team%20lead") {
		t.Errorf("role name should be URL-escaped on the wire, got EscapedPath=%q", escaped)
	}
	if role.Name != "team lead" {
		t.Errorf("role name unmarshalled wrong: %q", role.Name)
	}
}

func TestGetRole_404_MapsToNotFound(t *testing.T) {
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/roles/missing", func(*http.Request) (int, string) {
		return 404, `{"error":"not found"}`
	})
	p := newStageAProvider(t, s)

	_, err := p.GetRole(context.Background(), "missing")
	if !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListUsersByRole_Success(t *testing.T) {
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/roles/admin/users", func(*http.Request) (int, string) {
		return 200, `[{"id":"u1","username":"alice","email":"a@x","enabled":true}]`
	})
	p := newStageAProvider(t, s)

	users, err := p.ListUsersByRole(context.Background(), "admin")
	if err != nil {
		t.Fatalf("ListUsersByRole: %v", err)
	}
	if len(users) != 1 || users[0].Username != "alice" {
		t.Errorf("unexpected users: %+v", users)
	}
}

func TestListUserRoles_Success(t *testing.T) {
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/users/abc-123/role-mappings/realm", func(*http.Request) (int, string) {
		return 200, `[{"id":"r1","name":"admin"},{"id":"r2","name":"user"}]`
	})
	p := newStageAProvider(t, s)

	roles, err := p.ListUserRoles(context.Background(), "abc-123")
	if err != nil {
		t.Fatalf("ListUserRoles: %v", err)
	}
	if len(roles) != 2 || roles[0].Name != "admin" {
		t.Errorf("unexpected role mapping: %+v", roles)
	}
}

// ─────────────────────────── Sessions ───────────────────────────

func TestListUserSessions_Success(t *testing.T) {
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/users/u1/sessions", func(*http.Request) (int, string) {
		return 200, `[
			{"id":"sess-a","userId":"u1","username":"alice","ipAddress":"127.0.0.1","start":1700000000000,"lastAccess":1700000100000,"clients":{"cuuid":"saas-backend"}}
		]`
	})
	p := newStageAProvider(t, s)

	sessions, err := p.ListUserSessions(context.Background(), "u1")
	if err != nil {
		t.Fatalf("ListUserSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].IPAddress != "127.0.0.1" {
		t.Errorf("ip not parsed: %q", sessions[0].IPAddress)
	}
	if sessions[0].StartedAt.IsZero() {
		t.Errorf("StartedAt should be populated from start ms timestamp")
	}
}

func TestListSessions_AggregatesAcrossClients_DedupesBySessionID(t *testing.T) {
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/clients", func(*http.Request) (int, string) {
		return 200, `[
			{"id":"c1","clientId":"saas-backend","enabled":true},
			{"id":"c2","clientId":"saas-dev-playground","enabled":true},
			{"id":"c3","clientId":"saas-backend-admin","enabled":false}
		]`
	})
	// c1 sees session sess-1 + sess-2; c2 sees sess-2 + sess-3. sess-2 must
	// dedupe; final list has 3 sessions.
	s.on("GET", "/admin/realms/saas/clients/c1/user-sessions", func(*http.Request) (int, string) {
		return 200, `[
			{"id":"sess-1","userId":"u1","username":"alice","ipAddress":"127.0.0.1","start":1700000000000,"lastAccess":1700000000000},
			{"id":"sess-2","userId":"u2","username":"bob","ipAddress":"127.0.0.2","start":1700000100000,"lastAccess":1700000100000}
		]`
	})
	s.on("GET", "/admin/realms/saas/clients/c2/user-sessions", func(*http.Request) (int, string) {
		return 200, `[
			{"id":"sess-2","userId":"u2","username":"bob","ipAddress":"127.0.0.2"},
			{"id":"sess-3","userId":"u3","username":"carol","ipAddress":"127.0.0.3"}
		]`
	})
	// c3 disabled — must NOT be queried; if a stub miss is recorded the
	// test below catches it.
	p := newStageAProvider(t, s)

	sessions, err := p.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("expected 3 deduped sessions, got %d", len(sessions))
	}
	if s.misses.Load() > 0 {
		t.Errorf("aggregation hit unrequested endpoint %d times (disabled client should be skipped)", s.misses.Load())
	}
	// sess-2 must list BOTH client names (c1 + c2)
	var bobSession *identity.Session
	for i := range sessions {
		if sessions[i].ID == "sess-2" {
			bobSession = &sessions[i]
		}
	}
	if bobSession == nil {
		t.Fatalf("sess-2 missing")
	}
	if len(bobSession.Clients) != 2 {
		t.Errorf("sess-2 should list 2 clients, got %v", bobSession.Clients)
	}
}

func TestListSessions_OneClientFails_OthersStillReturned(t *testing.T) {
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/clients", func(*http.Request) (int, string) {
		return 200, `[
			{"id":"c1","clientId":"good","enabled":true},
			{"id":"c2","clientId":"flaky","enabled":true}
		]`
	})
	s.on("GET", "/admin/realms/saas/clients/c1/user-sessions", func(*http.Request) (int, string) {
		return 200, `[{"id":"sess-good","userId":"u1","username":"alice"}]`
	})
	s.on("GET", "/admin/realms/saas/clients/c2/user-sessions", func(*http.Request) (int, string) {
		return 500, `upstream broken`
	})
	p := newStageAProvider(t, s)

	sessions, err := p.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions should tolerate one bad client, got %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("expected 1 session from healthy client, got %d", len(sessions))
	}
}

// ─────────────────────────── Invitations ───────────────────────────

func TestListInvitations_FiltersUsersWithoutRequiredActionsOrInviteAttr(t *testing.T) {
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/users", func(*http.Request) (int, string) {
		return 200, `[
			{"id":"u1","username":"alice","email":"a@x","enabled":true,"requiredActions":[]},
			{"id":"u2","username":"bob","email":"b@x","enabled":true,"requiredActions":["UPDATE_PASSWORD","VERIFY_EMAIL"]},
			{"id":"u3","username":"carol","email":"c@x","enabled":false,"requiredActions":["VERIFY_EMAIL"]},
			{"id":"u4","username":"dave","email":"d@x","enabled":true,"requiredActions":[],"attributes":{"invited_by":["adminuser"]}}
		]`
	})
	p := newStageAProvider(t, s)

	invs, err := p.ListInvitations(context.Background())
	if err != nil {
		t.Fatalf("ListInvitations: %v", err)
	}
	// u1 has no signal → filtered out. u2/u3/u4 → included.
	if len(invs) != 3 {
		t.Fatalf("expected 3 invitations, got %d (%+v)", len(invs), invs)
	}

	byID := map[string]identity.Invitation{}
	for _, i := range invs {
		byID[i.ID] = i
	}
	if byID["u2"].Status != "pending" {
		t.Errorf("u2 status: %q", byID["u2"].Status)
	}
	if byID["u3"].Status != "revoked" {
		t.Errorf("u3 (disabled) status: %q", byID["u3"].Status)
	}
	if byID["u4"].Status != "accepted" {
		t.Errorf("u4 (enabled, no actions, has invited_by) status: %q", byID["u4"].Status)
	}
	if byID["u4"].InvitedBy != "adminuser" {
		t.Errorf("u4 invited_by attribute lost: %q", byID["u4"].InvitedBy)
	}
}

func TestListInvitations_ExpiresAtInPast_MarksExpired(t *testing.T) {
	s := newStageAStub(t)
	pastISO := "2000-01-01T00:00:00Z"
	body := fmt.Sprintf(`[
		{"id":"u9","username":"old","email":"o@x","enabled":true,"requiredActions":["VERIFY_EMAIL"],"attributes":{"invited_by":["adminuser"],"expires_at":[%q]}}
	]`, pastISO)
	s.on("GET", "/admin/realms/saas/users", func(*http.Request) (int, string) {
		return 200, body
	})
	p := newStageAProvider(t, s)

	invs, err := p.ListInvitations(context.Background())
	if err != nil {
		t.Fatalf("ListInvitations: %v", err)
	}
	if len(invs) != 1 {
		t.Fatalf("expected 1 invitation, got %d", len(invs))
	}
	if invs[0].Status != identity.InvitationStatusExpired {
		t.Errorf("past expires_at should map to expired, got %q", invs[0].Status)
	}
}

func TestListInvitations_AcceptedWithPastExpiresAt_StaysAccepted(t *testing.T) {
	// Reliability bug fix: an invitation that the user already completed
	// (no pending actions) must report "accepted" even if its expires_at
	// has since drifted into the past. The previous behavior reported
	// "expired" because expires_at was checked before required-actions
	// emptiness, which confused admins trying to audit invitation
	// completion.
	s := newStageAStub(t)
	pastISO := "2000-01-01T00:00:00Z"
	body := fmt.Sprintf(`[
		{"id":"u-done","username":"done","email":"d@x","enabled":true,"requiredActions":[],"attributes":{"invited_by":["adminuser"],"expires_at":[%q]}}
	]`, pastISO)
	s.on("GET", "/admin/realms/saas/users", func(*http.Request) (int, string) {
		return 200, body
	})
	p := newStageAProvider(t, s)

	invs, err := p.ListInvitations(context.Background())
	if err != nil {
		t.Fatalf("ListInvitations: %v", err)
	}
	if len(invs) != 1 {
		t.Fatalf("expected 1 invitation, got %d", len(invs))
	}
	if invs[0].Status != identity.InvitationStatusAccepted {
		t.Errorf("accepted invite with past expires_at must stay accepted, got %q", invs[0].Status)
	}
}
