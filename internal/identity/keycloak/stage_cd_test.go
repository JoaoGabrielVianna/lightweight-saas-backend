// Provider-level tests for v0.2 Stage C (UPDATE) + Stage D (DELETE) against
// an httptest stub of the Keycloak Admin API surface. Reuses the stageAStub
// helpers from stage_a_test.go.

package keycloak

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
)

// ─────────────── UpdateUser ───────────────

func TestUpdateUser_MergesWithCurrentRepresentation(t *testing.T) {
	var putBody string
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/users/u1", func(*http.Request) (int, string) {
		return 200, `{"id":"u1","username":"jane","email":"jane@x","firstName":"Jane","lastName":"Doe","enabled":true,"emailVerified":true,"attributes":{"keep":["yes"]}}`
	})
	s.on("PUT", "/admin/realms/saas/users/u1", func(r *http.Request) (int, string) {
		buf, _ := io.ReadAll(r.Body)
		putBody = string(buf)
		return 204, ""
	})
	p := newStageAProvider(t, s)

	first := "Janet"
	_, err := p.UpdateUser(context.Background(), "u1", identity.UpdateUserRequest{FirstName: &first})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	// PUT body must carry the updated field PLUS the original fields the
	// caller didn't touch (last name, email, attributes).
	mustContain := []string{
		`"firstName":"Janet"`,
		`"lastName":"Doe"`,
		`"email":"jane@x"`,
		`"enabled":true`,
		`"keep":["yes"]`,
	}
	for _, m := range mustContain {
		if !strings.Contains(putBody, m) {
			t.Errorf("PUT body missing %q\nfull: %s", m, putBody)
		}
	}
}

func TestUpdateUser_Disable_Propagates(t *testing.T) {
	var putBody string
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/users/u1", func(*http.Request) (int, string) {
		return 200, `{"id":"u1","username":"u","email":"u@x","enabled":true}`
	})
	s.on("PUT", "/admin/realms/saas/users/u1", func(r *http.Request) (int, string) {
		buf, _ := io.ReadAll(r.Body)
		putBody = string(buf)
		return 204, ""
	})
	p := newStageAProvider(t, s)

	f := false
	if _, err := p.UpdateUser(context.Background(), "u1", identity.UpdateUserRequest{Enabled: &f}); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if !strings.Contains(putBody, `"enabled":false`) {
		t.Errorf("PUT body missing enabled=false: %s", putBody)
	}
}

func TestUpdateUser_404_PropagatesNotFound(t *testing.T) {
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/users/missing", func(*http.Request) (int, string) {
		return 404, ``
	})
	p := newStageAProvider(t, s)

	_, err := p.UpdateUser(context.Background(), "missing", identity.UpdateUserRequest{})
	if !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ─────────────── UpdateRole ───────────────

func TestUpdateRole_MergesDescription(t *testing.T) {
	var putBody string
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/roles/editor", func(*http.Request) (int, string) {
		return 200, `{"id":"r-editor","name":"editor","description":"old"}`
	})
	s.on("PUT", "/admin/realms/saas/roles/editor", func(r *http.Request) (int, string) {
		buf, _ := io.ReadAll(r.Body)
		putBody = string(buf)
		return 204, ""
	})
	p := newStageAProvider(t, s)

	desc := "new description"
	_, err := p.UpdateRole(context.Background(), "editor", identity.UpdateRoleRequest{Description: &desc})
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if !strings.Contains(putBody, `"description":"new description"`) {
		t.Errorf("PUT body missing updated description: %s", putBody)
	}
	if !strings.Contains(putBody, `"name":"editor"`) {
		t.Errorf("PUT body must carry name verbatim: %s", putBody)
	}
}

// ─────────────── AssignRoles / UnassignRoles ───────────────

func TestAssignRolesToUser_ResolvesNamesAndPosts(t *testing.T) {
	var postBody string
	var postCalls atomic.Int32
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/roles/editor", func(*http.Request) (int, string) {
		return 200, `{"id":"r-editor","name":"editor"}`
	})
	s.on("POST", "/admin/realms/saas/users/u1/role-mappings/realm", func(r *http.Request) (int, string) {
		postCalls.Add(1)
		buf, _ := io.ReadAll(r.Body)
		postBody = string(buf)
		return 204, ""
	})
	p := newStageAProvider(t, s)

	if err := p.AssignRolesToUser(context.Background(), "u1", []string{"editor"}); err != nil {
		t.Fatalf("AssignRolesToUser: %v", err)
	}
	if postCalls.Load() != 1 {
		t.Errorf("expected one POST, got %d", postCalls.Load())
	}
	if !strings.Contains(postBody, `"id":"r-editor"`) || !strings.Contains(postBody, `"name":"editor"`) {
		t.Errorf("role-mapping body missing id+name: %s", postBody)
	}
}

func TestAssignRolesToUser_MissingRole_NoMappingCall(t *testing.T) {
	var postCalled atomic.Bool
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/roles/typo", func(*http.Request) (int, string) {
		return 404, ``
	})
	s.on("POST", "/admin/realms/saas/users/u1/role-mappings/realm", func(*http.Request) (int, string) {
		postCalled.Store(true)
		return 204, ""
	})
	p := newStageAProvider(t, s)

	err := p.AssignRolesToUser(context.Background(), "u1", []string{"typo"})
	if !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	if postCalled.Load() {
		t.Errorf("role-mapping POST must NOT fire when role lookup fails")
	}
}

func TestUnassignRolesFromUser_SendsDeleteWithBody(t *testing.T) {
	var delBody string
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/roles/editor", func(*http.Request) (int, string) {
		return 200, `{"id":"r-editor","name":"editor"}`
	})
	s.on("DELETE", "/admin/realms/saas/users/u1/role-mappings/realm", func(r *http.Request) (int, string) {
		buf, _ := io.ReadAll(r.Body)
		delBody = string(buf)
		return 204, ""
	})
	p := newStageAProvider(t, s)

	if err := p.UnassignRolesFromUser(context.Background(), "u1", []string{"editor"}); err != nil {
		t.Fatalf("UnassignRolesFromUser: %v", err)
	}
	if !strings.Contains(delBody, `"id":"r-editor"`) {
		t.Errorf("DELETE body missing role id: %s", delBody)
	}
}

// ─────────────── SendResetPasswordEmail / ResendInvitation ───────────────

func TestSendResetPasswordEmail_PutsCorrectActions(t *testing.T) {
	var putBody string
	s := newStageAStub(t)
	s.on("PUT", "/admin/realms/saas/users/u1/execute-actions-email", func(r *http.Request) (int, string) {
		buf, _ := io.ReadAll(r.Body)
		putBody = string(buf)
		return 204, ""
	})
	p := newStageAProvider(t, s)

	if err := p.SendResetPasswordEmail(context.Background(), "u1"); err != nil {
		t.Fatalf("SendResetPasswordEmail: %v", err)
	}
	if !strings.Contains(putBody, "UPDATE_PASSWORD") {
		t.Errorf("body should request UPDATE_PASSWORD: %s", putBody)
	}
	if strings.Contains(putBody, "VERIFY_EMAIL") {
		t.Errorf("reset should NOT include VERIFY_EMAIL: %s", putBody)
	}
}

func TestResendInvitation_OnlyResendsPendingActions(t *testing.T) {
	// User has VERIFY_EMAIL still pending but already completed
	// UPDATE_PASSWORD. Resend must NOT re-add UPDATE_PASSWORD to the
	// required-actions email — doing so would force the user to redo
	// work they already finished. Bug fix tied to invitation reliability:
	// see docs/INVITATION_RELIABILITY_v0.2.md.
	var putBody string
	var getCalls atomic.Int32
	s := newStageAStub(t)
	s.on("PUT", "/admin/realms/saas/users/u1/execute-actions-email", func(r *http.Request) (int, string) {
		buf, _ := io.ReadAll(r.Body)
		putBody = string(buf)
		return 204, ""
	})
	s.on("GET", "/admin/realms/saas/users/u1", func(*http.Request) (int, string) {
		getCalls.Add(1)
		return 200, `{"id":"u1","email":"j@x","enabled":true,"requiredActions":["VERIFY_EMAIL"]}`
	})
	p := newStageAProvider(t, s)

	inv, err := p.ResendInvitation(context.Background(), "u1")
	if err != nil {
		t.Fatalf("ResendInvitation: %v", err)
	}
	if !strings.Contains(putBody, "VERIFY_EMAIL") {
		t.Errorf("resend should include pending VERIFY_EMAIL: %s", putBody)
	}
	if strings.Contains(putBody, "UPDATE_PASSWORD") {
		t.Errorf("resend must NOT re-dispatch already-completed UPDATE_PASSWORD: %s", putBody)
	}
	if inv.Status != identity.InvitationStatusPending {
		t.Errorf("returned invitation status = %q", inv.Status)
	}
	// One GET to inspect state before the PUT, another to refresh after.
	if getCalls.Load() != 2 {
		t.Errorf("expected two GETs (pre-check + refresh), got %d", getCalls.Load())
	}
}

func TestResendInvitation_AlreadyAccepted_ReturnsConflict(t *testing.T) {
	// No pending invite actions → invitation is terminal (accepted).
	// Provider must NOT issue the PUT and must surface ErrConflict so the
	// handler returns 409.
	var putCalled atomic.Bool
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/users/u-accepted", func(*http.Request) (int, string) {
		return 200, `{"id":"u-accepted","email":"j@x","enabled":true,"requiredActions":[],"attributes":{"invited_by":["admin"]}}`
	})
	s.on("PUT", "/admin/realms/saas/users/u-accepted/execute-actions-email", func(*http.Request) (int, string) {
		putCalled.Store(true)
		return 204, ""
	})
	p := newStageAProvider(t, s)

	_, err := p.ResendInvitation(context.Background(), "u-accepted")
	if !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("expected ErrConflict for accepted invitation, got %v", err)
	}
	if putCalled.Load() {
		t.Errorf("PUT execute-actions-email must NOT fire when invitation is already accepted")
	}
}

func TestResendInvitation_Revoked_ReturnsConflict(t *testing.T) {
	// Disabled user → invitation is revoked. Resending email won't
	// re-enable them; provider must surface ErrConflict so the admin
	// re-enables first.
	var putCalled atomic.Bool
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/users/u-revoked", func(*http.Request) (int, string) {
		return 200, `{"id":"u-revoked","email":"j@x","enabled":false,"requiredActions":["VERIFY_EMAIL"]}`
	})
	s.on("PUT", "/admin/realms/saas/users/u-revoked/execute-actions-email", func(*http.Request) (int, string) {
		putCalled.Store(true)
		return 204, ""
	})
	p := newStageAProvider(t, s)

	_, err := p.ResendInvitation(context.Background(), "u-revoked")
	if !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("expected ErrConflict for revoked invitation, got %v", err)
	}
	if putCalled.Load() {
		t.Errorf("PUT execute-actions-email must NOT fire on a revoked invitation")
	}
}

func TestResendInvitation_IgnoresUnrelatedRequiredActions(t *testing.T) {
	// User only has CONFIGURE_TOTP pending — that's not an invite action.
	// Treat as accepted (no invite work remaining) and refuse the resend.
	var putCalled atomic.Bool
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/users/u-totp", func(*http.Request) (int, string) {
		return 200, `{"id":"u-totp","email":"j@x","enabled":true,"requiredActions":["CONFIGURE_TOTP"]}`
	})
	s.on("PUT", "/admin/realms/saas/users/u-totp/execute-actions-email", func(*http.Request) (int, string) {
		putCalled.Store(true)
		return 204, ""
	})
	p := newStageAProvider(t, s)

	_, err := p.ResendInvitation(context.Background(), "u-totp")
	if !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("expected ErrConflict when only non-invite actions remain, got %v", err)
	}
	if putCalled.Load() {
		t.Errorf("PUT must NOT fire when there are no invite actions to resend")
	}
}

// ─────────────── DeleteUser / DeleteRole / DeleteSession / Logout ───────────────

func TestDeleteUser_SendsDelete(t *testing.T) {
	var called atomic.Bool
	s := newStageAStub(t)
	s.on("DELETE", "/admin/realms/saas/users/u1", func(*http.Request) (int, string) {
		called.Store(true)
		return 204, ""
	})
	p := newStageAProvider(t, s)
	if err := p.DeleteUser(context.Background(), "u1"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if !called.Load() {
		t.Errorf("DELETE /users/u1 not fired")
	}
}

func TestDeleteUser_404_PropagatesNotFound(t *testing.T) {
	s := newStageAStub(t)
	s.on("DELETE", "/admin/realms/saas/users/u-gone", func(*http.Request) (int, string) {
		return 404, ``
	})
	p := newStageAProvider(t, s)
	err := p.DeleteUser(context.Background(), "u-gone")
	if !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteRole_SendsDelete(t *testing.T) {
	var called atomic.Bool
	s := newStageAStub(t)
	s.on("DELETE", "/admin/realms/saas/roles/editor", func(*http.Request) (int, string) {
		called.Store(true)
		return 204, ""
	})
	p := newStageAProvider(t, s)
	if err := p.DeleteRole(context.Background(), "editor"); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}
	if !called.Load() {
		t.Errorf("DELETE /roles/editor not fired")
	}
}

func TestDeleteSession_SendsDelete(t *testing.T) {
	var called atomic.Bool
	s := newStageAStub(t)
	s.on("DELETE", "/admin/realms/saas/sessions/sess-1", func(*http.Request) (int, string) {
		called.Store(true)
		return 204, ""
	})
	p := newStageAProvider(t, s)
	if err := p.DeleteSession(context.Background(), "sess-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if !called.Load() {
		t.Errorf("DELETE /sessions/sess-1 not fired")
	}
}

func TestLogoutUserSessions_PostsToLogout(t *testing.T) {
	var called atomic.Bool
	s := newStageAStub(t)
	s.on("POST", "/admin/realms/saas/users/u1/logout", func(*http.Request) (int, string) {
		called.Store(true)
		return 204, ""
	})
	p := newStageAProvider(t, s)
	if err := p.LogoutUserSessions(context.Background(), "u1"); err != nil {
		t.Fatalf("LogoutUserSessions: %v", err)
	}
	if !called.Load() {
		t.Errorf("POST /users/u1/logout not fired")
	}
}
