//go:build integration

package identityruntime

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
)

// Phase 6 and 7 acceptance: every migrated capability family proves the
// workspace boundary against two REAL Keycloak realms, and the whole surface
// keeps honouring connection rotation.
//
// The existing isolation_integration_test.go covers the resolver. This file
// covers the API built on top of it — the distinction matters because a handler
// can resolve the right provider and still act on the wrong thing, and only an
// end-to-end read or write through the route proves otherwise.
//
// Where a family's shared service path is already well covered by unit tests,
// one live proof per family is enough; that is the trade the mission allows and
// it is why this file has ten tests rather than forty.

// twoRealms builds two workspaces on two live realms and returns the
// installation plus both workspaces.
func twoRealms(t *testing.T, prefix string) (*installation, *workspace.Workspace, *workspace.Workspace) {
	t.Helper()

	inst := newInstallation(t)
	wsA := inst.newWorkspace(prefix + "-alpha")
	wsB := inst.newWorkspace(prefix + "-beta")
	inst.connectRealm(wsA, prefix+"-a", "primary")
	inst.connectRealm(wsB, prefix+"-b", "primary")
	return inst, wsA, wsB
}

// service resolves a workspace and wraps its provider exactly as the handler
// does, so these tests exercise the same composition the API uses.
func (i *installation) service(t *testing.T, ws *workspace.Workspace) *identity.Service {
	t.Helper()
	resolved, err := i.resolver.ForWorkspace(context.Background(), ws.PublicID())
	if err != nil {
		t.Fatalf("resolve %s: %v", ws.Slug, err)
	}
	return identity.NewService(resolved.Provider)
}

// realmUsernames reads a realm's users through Keycloak's own admin API, so an
// isolation claim is not verified by the component under test.
func (a *kcAdmin) realmUsernames(realm string) []string {
	a.t.Helper()
	var users []struct {
		Username string `json:"username"`
	}
	a.do(http.MethodGet, "/admin/realms/"+realm+"/users?max=500", nil, &users)
	out := make([]string, 0, len(users))
	for _, u := range users {
		out = append(out, u.Username)
	}
	return out
}

func (a *kcAdmin) userIDByUsername(realm, username string) string {
	a.t.Helper()
	var users []struct {
		ID string `json:"id"`
	}
	a.do(http.MethodGet, "/admin/realms/"+realm+"/users?username="+username+"&exact=true", nil, &users)
	if len(users) == 0 {
		a.t.Fatalf("user %s not found in realm %s", username, realm)
	}
	return users[0].ID
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

// TestAPIIsolation_UserCreatedInALandsOnlyInA is the users-family write proof.
func TestAPIIsolation_UserCreatedInALandsOnlyInA(t *testing.T) {
	inst, wsA, _ := twoRealms(t, "u-create")

	if _, err := inst.service(t, wsA).CreateUser(context.Background(), identity.CreateUserRequest{
		Email:             "ada@example.test",
		FirstName:         "Ada",
		TemporaryPassword: "temporary-1234",
	}); err != nil {
		t.Fatalf("create user through alpha: %v", err)
	}

	if !contains(inst.kc.realmUsernames("u-create-a"), "ada@example.test") {
		t.Error("realm A does not have the user — the write did not land where it was aimed")
	}
	if contains(inst.kc.realmUsernames("u-create-b"), "ada@example.test") {
		t.Error("realm B has the user — the write crossed the workspace boundary")
	}
}

// TestAPIIsolation_UserVisibleOnlyThroughItsOwnWorkspace is the read proof, and
// the one that catches a resolver that ignores the path.
func TestAPIIsolation_UserVisibleOnlyThroughItsOwnWorkspace(t *testing.T) {
	inst, wsA, wsB := twoRealms(t, "u-read")
	inst.kc.createUser("u-read-a", "only-in-a")

	userID := inst.kc.userIDByUsername("u-read-a", "only-in-a")

	if _, err := inst.service(t, wsA).GetUser(context.Background(), userID); err != nil {
		t.Errorf("workspace alpha cannot read its own user: %v", err)
	}

	// The same id through the other workspace must not resolve: it is a user
	// id from a different realm, and realm B has never heard of it.
	_, err := inst.service(t, wsB).GetUser(context.Background(), userID)
	if err == nil {
		t.Fatal("workspace beta read a user that exists only in realm A — REALM LEAK")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want a not-found from realm B", err)
	}
}

// TestAPIIsolation_UpdateAndDeleteAffectOnlyTheirRealm covers the remaining
// user mutations in one pass, since they share the resolution path and differ
// only in the provider call they make.
func TestAPIIsolation_UpdateAndDeleteAffectOnlyTheirRealm(t *testing.T) {
	inst, wsA, wsB := twoRealms(t, "u-mutate")

	// The same username exists in BOTH realms — the strongest form of the
	// test, because a leak now shows as the wrong realm's row changing rather
	// than as an error.
	inst.kc.createUser("u-mutate-a", "shared-name")
	inst.kc.createUser("u-mutate-b", "shared-name")

	idInA := inst.kc.userIDByUsername("u-mutate-a", "shared-name")
	ctx := context.Background()

	// FirstName rather than Enabled, deliberately. Disabling a user runs the
	// last-admin guard, which enumerates the realm's `admin` role — and a bare
	// realm created by this fixture has no such role, so Keycloak answers 404
	// and the guard fails closed with a not-found that says nothing about the
	// real situation. That is a pre-existing rough edge in the shared service
	// (recorded as TD-023), not the property under test here, and routing this
	// test around it keeps the isolation assertion about isolation.
	renamed := "Renamed-In-A"
	if _, err := inst.service(t, wsA).UpdateUser(ctx, "", idInA, identity.UpdateUserRequest{
		FirstName: &renamed,
	}); err != nil {
		t.Fatalf("update through alpha: %v", err)
	}

	// A's copy changed.
	userA, err := inst.service(t, wsA).GetUser(ctx, idInA)
	if err != nil {
		t.Fatalf("read A's user: %v", err)
	}
	if userA.FirstName != renamed {
		t.Errorf("realm A's user was not updated: first name = %q", userA.FirstName)
	}

	// B's copy is untouched.
	svcB := inst.service(t, wsB)
	idInB := inst.kc.userIDByUsername("u-mutate-b", "shared-name")
	userB, err := svcB.GetUser(ctx, idInB)
	if err != nil {
		t.Fatalf("read B's user: %v", err)
	}
	if userB.FirstName == renamed {
		t.Error("realm B's user was renamed by an update aimed at realm A — REALM LEAK")
	}

	// Delete through A, and B's copy survives.
	if err := inst.service(t, wsA).DeleteUser(ctx, "", idInA); err != nil {
		t.Fatalf("delete through alpha: %v", err)
	}
	if contains(inst.kc.realmUsernames("u-mutate-a"), "shared-name") {
		t.Error("realm A still has the user after deletion")
	}
	if !contains(inst.kc.realmUsernames("u-mutate-b"), "shared-name") {
		t.Error("realm B's user was deleted by a delete aimed at realm A — REALM LEAK")
	}
}

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

// TestAPIIsolation_RoleLifecycleStaysInOneRealm walks create → update → delete
// through workspace A and checks realm B at each step.
func TestAPIIsolation_RoleLifecycleStaysInOneRealm(t *testing.T) {
	inst, wsA, wsB := twoRealms(t, "r-life")
	ctx := context.Background()
	const roleName = "billing-admin"

	svcA := inst.service(t, wsA)
	if _, err := svcA.CreateRole(ctx, identity.CreateRoleRequest{Name: roleName, Description: "v1"}); err != nil {
		t.Fatalf("create role through alpha: %v", err)
	}
	if !contains(inst.kc.realmRoleNames("r-life-a"), roleName) {
		t.Error("realm A does not have the role")
	}
	if contains(inst.kc.realmRoleNames("r-life-b"), roleName) {
		t.Error("realm B has the role — creation crossed the boundary")
	}

	desc := "v2"
	if _, err := svcA.UpdateRole(ctx, roleName, identity.UpdateRoleRequest{Description: &desc}); err != nil {
		t.Fatalf("update role through alpha: %v", err)
	}

	// B cannot even see it, so an update through B must fail rather than
	// silently create something.
	if _, err := inst.service(t, wsB).GetRole(ctx, roleName); err == nil {
		t.Error("workspace beta resolved a role that exists only in realm A")
	}

	if err := svcA.DeleteRole(ctx, roleName); err != nil {
		t.Fatalf("delete role through alpha: %v", err)
	}
	if contains(inst.kc.realmRoleNames("r-life-a"), roleName) {
		t.Error("realm A still has the role after deletion")
	}
}

// TestAPIIsolation_RoleAssignmentStaysInOneRealm is the user-roles family
// proof: granting in A must not change membership in B.
func TestAPIIsolation_RoleAssignmentStaysInOneRealm(t *testing.T) {
	inst, wsA, wsB := twoRealms(t, "r-assign")
	ctx := context.Background()
	const roleName = "support"

	// Same role name in both realms, same username in both realms. Only the
	// workspace in the path distinguishes them.
	for _, ws := range []*workspace.Workspace{wsA, wsB} {
		if _, err := inst.service(t, ws).CreateRole(ctx, identity.CreateRoleRequest{Name: roleName}); err != nil {
			t.Fatalf("create role in %s: %v", ws.Slug, err)
		}
	}
	inst.kc.createUser("r-assign-a", "member")
	inst.kc.createUser("r-assign-b", "member")

	idInA := inst.kc.userIDByUsername("r-assign-a", "member")
	if err := inst.service(t, wsA).AssignRolesToUser(ctx, idInA, []string{roleName}); err != nil {
		t.Fatalf("assign through alpha: %v", err)
	}

	rolesInA, err := inst.service(t, wsA).ListUserRoles(ctx, idInA)
	if err != nil {
		t.Fatalf("list roles in A: %v", err)
	}
	if !containsRole(rolesInA, roleName) {
		t.Error("the grant did not take effect in realm A")
	}

	idInB := inst.kc.userIDByUsername("r-assign-b", "member")
	rolesInB, err := inst.service(t, wsB).ListUserRoles(ctx, idInB)
	if err != nil {
		t.Fatalf("list roles in B: %v", err)
	}
	if containsRole(rolesInB, roleName) {
		t.Error("realm B's user gained the role — the grant crossed the boundary")
	}
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

// TestAPIIsolation_SessionListingIsPerRealm.
//
// Neither realm has interactive sessions in a test, so the assertion is about
// WHICH realm was asked rather than about session contents: a listing through A
// must succeed against realm A and must not surface anything from B. The
// mutation half — revoking a session id that exists in neither realm — must
// fail rather than silently succeed against the wrong one.
func TestAPIIsolation_SessionListingIsPerRealm(t *testing.T) {
	inst, wsA, wsB := twoRealms(t, "s-list")
	ctx := context.Background()

	for _, ws := range []*workspace.Workspace{wsA, wsB} {
		sessions, err := inst.service(t, ws).ListSessions(ctx)
		if err != nil {
			t.Fatalf("list sessions through %s: %v", ws.Slug, err)
		}
		if len(sessions) != 0 {
			t.Errorf("%s reported %d sessions in a realm with no logins", ws.Slug, len(sessions))
		}
	}

	// A session id from nowhere: Keycloak answers 404, and the important part
	// is that the request went to A's realm and produced an error rather than
	// a success from somewhere else.
	err := inst.service(t, wsA).DeleteSession(ctx, "cccccccc-3333-4333-8333-cccccccccccc")
	if err == nil {
		t.Error("revoking a nonexistent session succeeded")
	}
}

// TestAPIIsolation_LogoutTargetsOneRealmsUser — the per-user session mutation.
// A user id that exists only in A must not be actionable through B.
func TestAPIIsolation_LogoutTargetsOneRealmsUser(t *testing.T) {
	inst, wsA, wsB := twoRealms(t, "s-logout")
	inst.kc.createUser("s-logout-a", "sessioned")
	ctx := context.Background()

	idInA := inst.kc.userIDByUsername("s-logout-a", "sessioned")

	// Through A: the user exists, so logout succeeds (a no-op with no active
	// sessions, which is the correct outcome, not an error).
	if err := inst.service(t, wsA).LogoutUserSessions(ctx, idInA); err != nil {
		t.Errorf("logout through alpha: %v", err)
	}

	// Through B: that id is meaningless, and Keycloak must refuse.
	if err := inst.service(t, wsB).LogoutUserSessions(ctx, idInA); err == nil {
		t.Error("workspace beta logged out a user that exists only in realm A — REALM LEAK")
	}
}

// ---------------------------------------------------------------------------
// Invitations
// ---------------------------------------------------------------------------

// TestAPIIsolation_InvitationListingAndRevocationArePerRealm.
//
// Creating an invitation is NOT exercised live, and the reason is a real
// limitation rather than an omission: CreateInvitation dispatches Keycloak's
// action email, and a realm without SMTP fails that step — at which point the
// implementation's compensating delete removes the user. A live create would
// therefore assert the rollback, not the isolation.
//
// What IS exercised is the derived model the other two routes rest on: a user
// with pending required actions appears as an invitation in its own realm and
// nowhere else, and revoking it removes exactly that user.
func TestAPIIsolation_InvitationListingAndRevocationArePerRealm(t *testing.T) {
	inst, wsA, wsB := twoRealms(t, "i-derive")
	ctx := context.Background()

	// An invited-looking user: enabled, with a required action pending. This
	// is precisely what the provider derives an invitation from.
	inst.kc.createInvitedUser("i-derive-a", "invited-in-a")

	invitesA, err := inst.service(t, wsA).ListInvitations(ctx)
	if err != nil {
		t.Fatalf("list invitations in A: %v", err)
	}
	if !containsInvitationFor(invitesA, "invited-in-a") {
		t.Fatalf("realm A's invitation is not listed: %+v", invitesA)
	}

	invitesB, err := inst.service(t, wsB).ListInvitations(ctx)
	if err != nil {
		t.Fatalf("list invitations in B: %v", err)
	}
	if containsInvitationFor(invitesB, "invited-in-a") {
		t.Error("realm B lists realm A's invitation — REALM LEAK")
	}

	// Revoking through A removes the user from realm A only.
	idInA := inst.kc.userIDByUsername("i-derive-a", "invited-in-a")
	if err := inst.service(t, wsA).DeleteInvitation(ctx, "", idInA); err != nil {
		t.Fatalf("revoke through alpha: %v", err)
	}
	if contains(inst.kc.realmUsernames("i-derive-a"), "invited-in-a") {
		t.Error("realm A still has the invited user after revocation")
	}
}

// ---------------------------------------------------------------------------
// Phase 7 — rotation across the expanded API
// ---------------------------------------------------------------------------

// TestAPIRotation_ReadAndWriteFollowTheActiveConnection is Phase 7.
//
// One workspace, two realms in sequence. The same resolver instance serves
// both, with no restart, and the assertion is on externally observable realm
// behaviour — what the read returns, and which realm the write landed in —
// rather than on provider pointer identity, which would only prove the cache
// changed its mind.
func TestAPIRotation_ReadAndWriteFollowTheActiveConnection(t *testing.T) {
	inst := newInstallation(t)
	ctx := context.Background()

	ws := inst.newWorkspace("rotating")
	inst.connectRealm(ws, "rot-api-a", "generation-1")
	inst.kc.createUser("rot-api-a", "lives-in-a")

	// READ before rotation.
	usersBefore, err := inst.service(t, ws).ListUsers(ctx, identity.ListUsersQuery{Max: 100})
	if err != nil {
		t.Fatalf("list before rotation: %v", err)
	}
	if !containsUser(usersBefore, "lives-in-a") {
		t.Fatalf("baseline read did not see realm A's user: %+v", usersBefore)
	}

	// WRITE before rotation, so there is something to check did NOT move.
	if _, err := inst.service(t, ws).CreateRole(ctx, identity.CreateRoleRequest{Name: "before-rotation"}); err != nil {
		t.Fatalf("create role before rotation: %v", err)
	}

	// Rotate: activate a connection to a different realm. The previous one is
	// retired in the same transaction.
	inst.connectRealm(ws, "rot-api-b", "generation-2")
	inst.kc.createUser("rot-api-b", "lives-in-b")

	// READ after rotation follows the new connection.
	usersAfter, err := inst.service(t, ws).ListUsers(ctx, identity.ListUsersQuery{Max: 100})
	if err != nil {
		t.Fatalf("list after rotation: %v", err)
	}
	if !containsUser(usersAfter, "lives-in-b") {
		t.Errorf("after rotation the read does not see realm B's user: %+v", usersAfter)
	}
	if containsUser(usersAfter, "lives-in-a") {
		t.Errorf("after rotation the read still sees realm A's user: %+v", usersAfter)
	}

	// WRITE after rotation lands in the new realm, and only there.
	if _, err := inst.service(t, ws).CreateRole(ctx, identity.CreateRoleRequest{Name: "after-rotation"}); err != nil {
		t.Fatalf("create role after rotation: %v", err)
	}
	if !contains(inst.kc.realmRoleNames("rot-api-b"), "after-rotation") {
		t.Error("the post-rotation write did not land in realm B")
	}
	if contains(inst.kc.realmRoleNames("rot-api-a"), "after-rotation") {
		t.Error("the post-rotation write landed in the RETIRED connection's realm")
	}
	if !contains(inst.kc.realmRoleNames("rot-api-a"), "before-rotation") {
		t.Error("the pre-rotation write is missing from realm A")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// createInvitedUser makes a user in the state the provider derives an
// invitation from: enabled, with a required action still pending.
func (a *kcAdmin) createInvitedUser(realm, username string) {
	a.t.Helper()
	if code := a.do(http.MethodPost, "/admin/realms/"+realm+"/users", map[string]any{
		"username":        username,
		"email":           username + "@example.test",
		"enabled":         true,
		"emailVerified":   false,
		"requiredActions": []string{"UPDATE_PASSWORD"},
		"attributes":      map[string]any{"invited_by": []string{"test@example.test"}},
	}, nil); code >= 300 {
		a.t.Fatalf("create invited user %s in %s: HTTP %d", username, realm, code)
	}
}

func containsRole(roles []identity.Role, name string) bool {
	for _, r := range roles {
		if r.Name == name {
			return true
		}
	}
	return false
}

func containsUser(users []identity.User, username string) bool {
	for _, u := range users {
		if u.Username == username {
			return true
		}
	}
	return false
}

func containsInvitationFor(invites []identity.Invitation, username string) bool {
	for _, i := range invites {
		if i.Username == username {
			return true
		}
	}
	return false
}
