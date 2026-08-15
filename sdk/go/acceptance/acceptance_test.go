//go:build acceptance

// Package acceptance drives the Go SDK against a REAL LIGHTWEIGHT installation:
// a real process, a real PostgreSQL, a real identity provider, real Project
// Credentials.
//
// It exists because the unit suite cannot answer the only question that finally
// matters. Every fixture in sdk/go/testdata is a statement about what the server
// sends, written by hand, and it stays green forever whether or not the server
// still sends that. This suite is where those statements are checked against the
// thing they describe.
//
// # What it is not allowed to know
//
// The build tag keeps it out of the default run, but the real constraint is the
// module boundary: this package lives inside the SDK module, so it CANNOT import
// the server. It has no database handle, no operator token, and no way to learn
// which identity provider is behind the API. Everything it asserts, it asserts
// through the same exported surface a customer's backend has.
//
// That is the abstraction being proven, so weakening it to make a check easier
// would delete the finding rather than fix it.
//
// # Configuration
//
// The happy path needs exactly the three variables the product promises:
//
//	LIGHTWEIGHT_URL
//	LIGHTWEIGHT_WORKSPACE_ID
//	LIGHTWEIGHT_API_KEY
//
// The negative cases need fixtures only an OPERATOR can create — a second
// workspace, a key with fewer scopes, a key that has been revoked. Those arrive
// as extra LW_SDK_* variables, and each subtest SKIPS when its fixture is
// absent rather than failing, so the suite is runnable by hand with three
// variables and exhaustive under scripts/sdk-acceptance.sh.
//
// Run it with:
//
//	go test -tags acceptance ./acceptance/...
package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	lightweight "github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go"
)

// Environment variables carrying operator-created fixtures.
//
// Every one is OPTIONAL. A missing fixture skips its subtest, visibly.
const (
	envWorkspaceB  = "LW_SDK_WORKSPACE_B"
	envAPIKeyB     = "LW_SDK_API_KEY_B"
	envAPIKeyRead  = "LW_SDK_API_KEY_READONLY"
	envAPIKeyGone  = "LW_SDK_API_KEY_REVOKED"
	envAPIKeyDoom  = "LW_SDK_API_KEY_REVOCABLE"
	envRevokeSig   = "LW_SDK_REVOKE_SIGNAL_FILE"
	envUserInA     = "LW_SDK_USER_A"
	envUserInB     = "LW_SDK_USER_B"
	envCredBurst   = "LW_SDK_CREDENTIAL_BURST"
	envSkipRateLim = "LW_SDK_SKIP_RATE_LIMIT"

	// ─── Slice 14 (KI-018) negative fixtures ────────────────────────────────
	//
	// Three states a consumer will eventually meet in production and which no
	// earlier fixture produced. Each needs an operator to arrange it, and each
	// must arrive at the SDK as a code a program can branch on rather than as
	// "something went wrong".

	// A workspace the operator archived, and a credential still bound to it.
	// The credential is VALID: this is the inactive-parent case, and it must
	// not be reported as a bad key.
	envWorkspaceGone  = "LW_SDK_WORKSPACE_ARCHIVED"
	envAPIKeyInFrozen = "LW_SDK_API_KEY_ARCHIVED_WS"

	// A workspace whose provider service account lost its write privileges
	// after the connection was verified. The credential is valid and fully
	// scoped; the PROVIDER refuses.
	envWorkspaceLimited = "LW_SDK_WORKSPACE_PROVIDER_READONLY"
	envAPIKeyLimited    = "LW_SDK_API_KEY_PROVIDER_READONLY"

	// The opaque id of a user living in workspace B's realm. Valid everywhere
	// and resolvable only in one place.
	envForeignUserID = "LW_SDK_FOREIGN_USER_ID"
)

// The forbidden knowledge. Nothing in this package may read any of these, and
// TestAcceptance_TheProgramNeedsNothingButTheThreeVariables proves it by making
// them poisonous.
var forbiddenEnv = []string{
	"KEYCLOAK_URL", "KEYCLOAK_REALM", "KEYCLOAK_CLIENT_ID", "KEYCLOAK_CLIENT_SECRET",
	"KEYCLOAK_ADMIN_CLIENT_ID", "KEYCLOAK_ADMIN_CLIENT_SECRET", "KEYCLOAK_JWKS_URL",
	"DB_URL", "SECRETS_MASTER_KEY", "LW_OPERATOR_TOKEN",
}

func mainClient(t *testing.T) *lightweight.Client {
	t.Helper()
	requireEnv(t, lightweight.EnvBaseURL, lightweight.EnvWorkspaceID, lightweight.EnvAPIKey)

	client, err := lightweight.NewClientFromEnv()
	if err != nil {
		t.Fatalf("NewClientFromEnv: %v", err)
	}
	return client
}

// clientWith builds a client for a fixture credential, skipping when absent.
func clientWith(t *testing.T, workspaceEnv, keyEnv string) *lightweight.Client {
	t.Helper()

	workspace := os.Getenv(workspaceEnv)
	if workspaceEnv == lightweight.EnvWorkspaceID {
		workspace = os.Getenv(lightweight.EnvWorkspaceID)
	}
	key := os.Getenv(keyEnv)
	if workspace == "" || key == "" {
		t.Skipf("SKIP: %s and %s are not both set; this fixture needs an operator to create it",
			workspaceEnv, keyEnv)
	}

	client, err := lightweight.NewClient(lightweight.Config{
		BaseURL:     os.Getenv(lightweight.EnvBaseURL),
		WorkspaceID: workspace,
		APIKey:      key,
	})
	if err != nil {
		t.Fatalf("NewClient for %s: %v", keyEnv, err)
	}
	return client
}

func requireEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if os.Getenv(name) == "" {
			t.Fatalf("%s is not set; the acceptance suite needs the three integration variables", name)
		}
	}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// apiErr extracts the typed refusal, failing with the raw error when there
// isn't one. Every negative assertion in this file goes through it, because
// "the call failed" is not the property being tested — "the call failed with a
// code a program can act on" is.
func apiErr(t *testing.T, err error) *lightweight.APIError {
	t.Helper()
	if err == nil {
		t.Fatal("the call succeeded; a refusal was expected")
	}
	var e *lightweight.APIError
	if !errors.As(err, &e) {
		t.Fatalf("error is %T, want *lightweight.APIError: %v", err, err)
	}
	if e.RequestID == "" {
		t.Error("the refusal carries no request id; it cannot be correlated with the server log")
	}
	return e
}

// uniqueEmail produces a fixture address that cannot collide with another run.
func uniqueEmail(prefix string) string {
	return fmt.Sprintf("%s-%d@sdk-acceptance.test", prefix, time.Now().UnixNano())
}

// ─── The central product test ───────────────────────────────────────────────

// TestAcceptance_TheFullConsumerJourney is the slice in one function.
//
// A backend configured with three environment variables reads real state,
// performs an authorized mutation, reads it back, and reverts it — against a
// real identity provider it has never been told about.
//
// The fixture user is created, exercised and DELETED, in that order, with the
// delete in a cleanup so a failure part-way through does not leave a stranger in
// somebody's directory.
func TestAcceptance_TheFullConsumerJourney(t *testing.T) {
	client := mainClient(t)
	ctx := testCtx(t)

	// ── read real state ─────────────────────────────────────────────────────
	page, err := client.Users.List(ctx, lightweight.UserListOptions{Max: 50})
	if err != nil {
		t.Fatalf("Users.List: %v", err)
	}
	if page.Count != len(page.Users) {
		t.Errorf("count=%d but %d users returned", page.Count, len(page.Users))
	}
	t.Logf("workspace %s currently holds %d user(s) on the first page", client.WorkspaceID(), page.Count)

	// ── create a fixture ────────────────────────────────────────────────────
	email := uniqueEmail("journey")
	created, err := client.Users.Create(ctx, lightweight.CreateUserRequest{
		Email:             email,
		FirstName:         "Journey",
		LastName:          "Fixture",
		TemporaryPassword: "acceptance-temp-9task",
	})
	if err != nil {
		t.Fatalf("Users.Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("the created user has no id")
	}
	t.Cleanup(func() {
		// A fresh context: the test's may already be cancelled by the time
		// cleanup runs, and a fixture that survives because cleanup was
		// cancelled is the worst kind of leftover.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := client.Users.Delete(cleanupCtx, created.ID); err != nil {
			t.Errorf("cleanup: Users.Delete(%s): %v", created.ID, err)
		}
	})

	if created.Email != email {
		t.Errorf("created email = %q, want %q", created.Email, email)
	}
	if !created.Enabled {
		t.Error("a newly created user is not enabled")
	}
	if created.CreatedAt.IsZero() {
		t.Error("created_at did not decode; the timestamp contract is broken")
	}

	// The temporary password must never come back.
	if strings.Contains(fmt.Sprintf("%+v", created), "acceptance-temp-9task") {
		t.Error("the create response echoed the temporary password")
	}

	// ── read it back ────────────────────────────────────────────────────────
	fetched, err := client.Users.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Users.Get: %v", err)
	}
	if fetched.ID != created.ID || fetched.Email != email {
		t.Errorf("Get returned %+v, want the user just created", fetched)
	}

	// ── it appears in a search ──────────────────────────────────────────────
	found, err := client.Users.List(ctx, lightweight.UserListOptions{Search: email, Max: 10})
	if err != nil {
		t.Fatalf("Users.List(search): %v", err)
	}
	if len(found.Users) == 0 {
		t.Error("the created user is not findable by search; the search parameter is not reaching the provider")
	}

	// ── mutate ──────────────────────────────────────────────────────────────
	updated, err := client.Users.Update(ctx, created.ID, lightweight.UpdateUserRequest{
		Enabled:  lightweight.Bool(false),
		LastName: lightweight.String("Fixture-Renamed"),
	})
	if err != nil {
		t.Fatalf("Users.Update: %v", err)
	}
	if updated.Enabled {
		t.Error("enabled=false was not applied")
	}
	if updated.LastName != "Fixture-Renamed" {
		t.Errorf("last_name = %q, want the patched value", updated.LastName)
	}
	if updated.FirstName != "Journey" {
		t.Errorf("first_name = %q; a field the patch did not name was changed", updated.FirstName)
	}

	// ── roles, through the same credential ──────────────────────────────────
	roles, err := client.Roles.List(ctx)
	if err != nil {
		t.Fatalf("Roles.List: %v", err)
	}
	t.Logf("workspace defines %d role(s)", len(roles))

	roleName := "sdk-acceptance-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	role, err := client.Roles.Create(ctx, lightweight.CreateRoleRequest{
		Name: roleName, Description: "Created by the SDK acceptance suite",
	})
	if err != nil {
		t.Fatalf("Roles.Create: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := client.Roles.Delete(cleanupCtx, roleName); err != nil {
			t.Errorf("cleanup: Roles.Delete(%s): %v", roleName, err)
		}
	})
	if role.Name != roleName {
		t.Errorf("created role name = %q", role.Name)
	}

	if err := client.Roles.Grant(ctx, created.ID, roleName); err != nil {
		t.Fatalf("Roles.Grant: %v", err)
	}
	granted, err := client.Roles.ListForUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("Roles.ListForUser: %v", err)
	}
	if !hasRole(granted, roleName) {
		t.Errorf("the granted role is absent from the user's roles: %v", roleNames(granted))
	}

	holders, err := client.Roles.ListUsers(ctx, roleName)
	if err != nil {
		t.Fatalf("Roles.ListUsers: %v", err)
	}
	if len(holders.Users) != 1 || holders.Users[0].ID != created.ID {
		t.Errorf("role holders = %+v, want exactly the fixture user", holders.Users)
	}

	if err := client.Roles.Revoke(ctx, created.ID, roleName); err != nil {
		t.Fatalf("Roles.Revoke: %v", err)
	}
	granted, err = client.Roles.ListForUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("Roles.ListForUser after revoke: %v", err)
	}
	if hasRole(granted, roleName) {
		t.Errorf("the role survived revocation: %v", roleNames(granted))
	}

	// ── sessions ────────────────────────────────────────────────────────────
	//
	// A freshly created user has none, which is the assertion: the call works
	// and the answer is empty, rather than the call working and the answer
	// being somebody else's sessions.
	userSessions, err := client.Sessions.ListForUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("Sessions.ListForUser: %v", err)
	}
	if len(userSessions) != 0 {
		t.Errorf("a user who has never signed in has %d session(s)", len(userSessions))
	}
	if _, err := client.Sessions.List(ctx); err != nil {
		t.Fatalf("Sessions.List: %v", err)
	}

	// ── the trail recorded it ───────────────────────────────────────────────
	assertAuditRecorded(t, ctx, client, created.ID)
}

func hasRole(roles []lightweight.Role, name string) bool {
	for _, r := range roles {
		if r.Name == name {
			return true
		}
	}
	return false
}

func roleNames(roles []lightweight.Role) []string {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		out = append(out, r.Name)
	}
	return out
}

// assertAuditRecorded looks for the creation of userID in the durable trail.
//
// Skipped rather than failed when the credential lacks audit:read: that is a
// property of the key the harness handed over, not a defect, and a suite that
// failed on it could not be run with a narrow credential at all.
func assertAuditRecorded(t *testing.T, ctx context.Context, client *lightweight.Client, userID string) {
	t.Helper()

	page, err := client.Audit.List(ctx, lightweight.AuditListOptions{Event: "user.created", Limit: 100})
	if err != nil {
		if e := new(lightweight.APIError); errors.As(err, &e) && e.Code == lightweight.CodeInsufficientScope {
			t.Skipf("SKIP: this credential does not carry %s", "audit:read")
		}
		t.Fatalf("Audit.List: %v", err)
	}

	var found *lightweight.AuditEvent
	for i := range page.Items {
		if page.Items[i].Resource != nil && page.Items[i].Resource.ID == userID {
			found = &page.Items[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("the trail has no user.created event for %s among %d events", userID, len(page.Items))
	}

	if found.Actor.Type != lightweight.AuditActorProject {
		t.Errorf("actor type = %q, want %q — the mutation was made by a Project Credential",
			found.Actor.Type, lightweight.AuditActorProject)
	}
	if found.Actor.CredentialID == "" {
		t.Error("the event names no credential; a compromised key could not be identified from the trail")
	}
	if found.Actor.Subject != "" {
		t.Errorf("a machine actor carries a human subject %q", found.Actor.Subject)
	}
	if found.Outcome != lightweight.AuditOutcomeSuccess {
		t.Errorf("outcome = %q", found.Outcome)
	}
	if found.RequestID == "" {
		t.Error("the event carries no request id")
	}
	if found.OccurredAt.IsZero() {
		t.Error("occurred_at did not decode")
	}

	// No secret material, whoever is asking.
	rendered := fmt.Sprintf("%+v", page)
	if strings.Contains(rendered, "acceptance-temp-9task") {
		t.Error("the audit trail contains the temporary password")
	}
	if strings.Contains(rendered, lightweight.APIKeyPrefix) {
		t.Error("the audit trail contains something shaped like a credential")
	}
}

// TestAcceptance_AuditPaginationWalksTheRealTrail.
//
// Cursor pagination against real data, with a page size of one, so the loop runs
// more than once and the cursor is genuinely exercised rather than being
// returned empty on the first page.
func TestAcceptance_AuditPaginationWalksTheRealTrail(t *testing.T) {
	client := mainClient(t)
	ctx := testCtx(t)

	first, err := client.Audit.List(ctx, lightweight.AuditListOptions{Limit: 1})
	if err != nil {
		if e := new(lightweight.APIError); errors.As(err, &e) && e.Code == lightweight.CodeInsufficientScope {
			t.Skip("SKIP: this credential does not carry audit:read")
		}
		t.Fatalf("Audit.List: %v", err)
	}
	if len(first.Items) == 0 {
		t.Skip("SKIP: the trail is empty; run the journey test first")
	}
	if !first.HasMore() {
		t.Skip("SKIP: the trail holds a single event; there is no second page to walk")
	}

	second, err := client.Audit.List(ctx, lightweight.AuditListOptions{
		Limit: 1, Cursor: first.Pagination.NextCursor,
	})
	if err != nil {
		t.Fatalf("Audit.List(cursor): %v", err)
	}
	if len(second.Items) == 0 {
		t.Fatal("the second page is empty while the first advertised a cursor")
	}
	if second.Items[0].ID == first.Items[0].ID {
		t.Error("the cursor returned the same event; pagination is not advancing")
	}

	// The iterator must agree with the manual loop and must terminate.
	var viaIterator int
	for _, err := range client.Audit.All(ctx, lightweight.AuditListOptions{Limit: 2}) {
		if err != nil {
			t.Fatalf("Audit.All: %v", err)
		}
		viaIterator++
		if viaIterator > 5000 {
			break
		}
	}
	if viaIterator < 2 {
		t.Errorf("Audit.All yielded %d events while the page API found at least 2", viaIterator)
	}
	t.Logf("Audit.All walked %d event(s)", viaIterator)
}

// ─── Workspace isolation ────────────────────────────────────────────────────

// TestAcceptance_TwoWorkspacesTwoTenants.
//
// Two clients, each holding nothing but its own three values, reading two
// separate tenants of the same installation. Neither client has any notion of
// which tenant of the underlying provider it is reaching — that mapping is the
// operator's, held server-side, and is exactly what this SDK is supposed to hide.
//
// The assertion is directional on purpose: A must see A's user AND must not see
// B's. Checking only the first would pass even if both clients were reading the
// same tenant.
func TestAcceptance_TwoWorkspacesTwoTenants(t *testing.T) {
	clientA := mainClient(t)
	clientB := clientWith(t, envWorkspaceB, envAPIKeyB)

	userInA, userInB := os.Getenv(envUserInA), os.Getenv(envUserInB)
	if userInA == "" || userInB == "" {
		t.Skipf("SKIP: %s and %s name the users that distinguish the two tenants", envUserInA, envUserInB)
	}
	ctx := testCtx(t)

	usersA, err := clientA.Users.List(ctx, lightweight.UserListOptions{Max: 100})
	if err != nil {
		t.Fatalf("A.Users.List: %v", err)
	}
	usersB, err := clientB.Users.List(ctx, lightweight.UserListOptions{Max: 100})
	if err != nil {
		t.Fatalf("B.Users.List: %v", err)
	}

	if !containsUsername(usersA.Users, userInA) {
		t.Errorf("client A does not see %q, which lives in its own tenant", userInA)
	}
	if containsUsername(usersA.Users, userInB) {
		t.Errorf("client A sees %q, which belongs to another tenant — the isolation has failed", userInB)
	}
	if !containsUsername(usersB.Users, userInB) {
		t.Errorf("client B does not see %q", userInB)
	}
	if containsUsername(usersB.Users, userInA) {
		t.Errorf("client B sees %q, which belongs to another tenant", userInA)
	}

	// A write through B must not become visible to A.
	email := uniqueEmail("isolation")
	createdInB, err := clientB.Users.Create(ctx, lightweight.CreateUserRequest{
		Email: email, FirstName: "Iso", LastName: "Lation",
		TemporaryPassword: "acceptance-temp-9task",
	})
	if err != nil {
		t.Fatalf("B.Users.Create: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := clientB.Users.Delete(cleanupCtx, createdInB.ID); err != nil {
			t.Errorf("cleanup: B.Users.Delete: %v", err)
		}
	})

	seenFromA, err := clientA.Users.List(ctx, lightweight.UserListOptions{Search: email, Max: 10})
	if err != nil {
		t.Fatalf("A.Users.List(search): %v", err)
	}
	if len(seenFromA.Users) != 0 {
		t.Errorf("a user created through workspace B is visible from workspace A: %+v", seenFromA.Users)
	}

	// And A's credential cannot address the user by id either, which is the
	// stronger statement: not merely absent from a listing, unreachable.
	if _, err := clientA.Users.Get(ctx, createdInB.ID); err == nil {
		t.Error("workspace A's credential fetched a user belonging to workspace B by id")
	} else if e := apiErr(t, err); e.Code != lightweight.CodeUserNotFound {
		t.Errorf("code = %q, want %q", e.Code, lightweight.CodeUserNotFound)
	}
}

func containsUsername(users []lightweight.User, name string) bool {
	for _, u := range users {
		if u.Username == name || u.Email == name {
			return true
		}
	}
	return false
}

// TestAcceptance_WorkspaceMismatchIsRefused.
//
// The SDK binds the workspace at construction, so a correctly configured client
// cannot address another tenant. What it CAN express — and what happens in
// practice — is a client built from two environments' worth of configuration:
// workspace B's id with workspace A's key.
//
// This is the reason the workspace stays in the path rather than being inferred
// from the credential. A URL whose meaning depends on which key was presented
// could not be read, logged or reasoned about, and the mismatch would have no
// way to be detected at all.
func TestAcceptance_WorkspaceMismatchIsRefused(t *testing.T) {
	requireEnv(t, lightweight.EnvBaseURL, lightweight.EnvAPIKey)
	workspaceB := os.Getenv(envWorkspaceB)
	if workspaceB == "" {
		t.Skipf("SKIP: %s names the workspace this credential is NOT bound to", envWorkspaceB)
	}

	// Workspace B, key A. Construction succeeds — both values are well-formed,
	// and nothing local can know they belong to different environments.
	crossed, err := lightweight.NewClient(lightweight.Config{
		BaseURL:     os.Getenv(lightweight.EnvBaseURL),
		WorkspaceID: workspaceB,
		APIKey:      os.Getenv(lightweight.EnvAPIKey),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = crossed.Users.List(testCtx(t), lightweight.UserListOptions{})
	e := apiErr(t, err)

	if e.Code != lightweight.CodeWorkspaceMismatch {
		t.Errorf("code = %q, want %q", e.Code, lightweight.CodeWorkspaceMismatch)
	}
	if e.StatusCode != 403 {
		t.Errorf("status = %d, want 403", e.StatusCode)
	}
}

// ─── Negative authorization ─────────────────────────────────────────────────

// TestAcceptance_MissingScopeIsRefusedWithAMachineReadableCode.
//
// A key holding only users:read. The read works, so the key is genuinely valid;
// every write and every other family is refused — with insufficient_scope rather
// than a generic 403, because the two mean different things to whoever has to
// fix it.
func TestAcceptance_MissingScopeIsRefusedWithAMachineReadableCode(t *testing.T) {
	readOnly := clientWith(t, lightweight.EnvWorkspaceID, envAPIKeyRead)
	ctx := testCtx(t)

	if _, err := readOnly.Users.List(ctx, lightweight.UserListOptions{Max: 1}); err != nil {
		t.Fatalf("the read-only credential cannot read: %v", err)
	}

	refusals := map[string]func() error{
		"users:write via Create": func() error {
			_, err := readOnly.Users.Create(ctx, lightweight.CreateUserRequest{
				Email: uniqueEmail("should-not-exist"), TemporaryPassword: "acceptance-temp-9task",
			})
			return err
		},
		"roles:read via Roles.List":       func() error { _, err := readOnly.Roles.List(ctx); return err },
		"sessions:read via Sessions.List": func() error { _, err := readOnly.Sessions.List(ctx); return err },
		"invitations:read via List":       func() error { _, err := readOnly.Invitations.List(ctx); return err },
		"audit:read via Audit.List": func() error {
			_, err := readOnly.Audit.List(ctx, lightweight.AuditListOptions{})
			return err
		},
	}

	for name, call := range refusals {
		t.Run(name, func(t *testing.T) {
			e := apiErr(t, call())
			if e.Code != lightweight.CodeInsufficientScope {
				t.Errorf("code = %q, want %q", e.Code, lightweight.CodeInsufficientScope)
			}
			if e.StatusCode != 403 {
				t.Errorf("status = %d, want 403", e.StatusCode)
			}
		})
	}
}

// TestAcceptance_AnArchivedWorkspaceIsReportedAsAWorkspaceState.
//
// The inactive-parent case, and the distinction it makes is the whole value of
// the test. The credential is perfectly valid — it authenticates, it is bound
// to this workspace, it carries every scope. What changed is the WORKSPACE.
//
// Reporting that as `credential_invalid` would send a developer rotating a key
// that was never the problem, and would hide an operator action behind an
// authentication error. It has to arrive as a workspace state, with a 409,
// which is what a consumer needs to tell "fix your key" from "ask your operator
// what they did".
func TestAcceptance_AnArchivedWorkspaceIsReportedAsAWorkspaceState(t *testing.T) {
	frozen := clientWith(t, envWorkspaceGone, envAPIKeyInFrozen)
	ctx := testCtx(t)

	// A read and a write, because a client that only refused writes would leak
	// the archived workspace's directory.
	for name, call := range map[string]func() error{
		"read": func() error {
			_, err := frozen.Users.List(ctx, lightweight.UserListOptions{Max: 1})
			return err
		},
		"write": func() error {
			_, err := frozen.Users.Create(ctx, lightweight.CreateUserRequest{
				Email: uniqueEmail("into-an-archived-workspace"), TemporaryPassword: "acceptance-temp-9task",
			})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := apiErr(t, call())
			if e.Code != lightweight.CodeWorkspaceArchived {
				t.Errorf("code = %q, want %q — an archived workspace is a workspace state, "+
					"not a bad credential", e.Code, lightweight.CodeWorkspaceArchived)
			}
			if e.StatusCode != 409 {
				t.Errorf("status = %d, want 409", e.StatusCode)
			}
		})
	}
}

// TestAcceptance_AProviderRefusalIsNotBlamedOnTheCaller.
//
// The credential carries every scope, addresses its own workspace, and is
// refused anyway — because the workspace's Keycloak service account lost the
// privileges the write needs after the connection was verified.
//
// These are different problems with different owners:
//
//	insufficient_scope  the developer asks their operator for a better key
//	provider_forbidden  the operator fixes the realm's service account
//
// A client that could not tell them apart would route every one of those
// conversations to the wrong person. The read is asserted too: the connection
// still works, so this is specifically about the write.
func TestAcceptance_AProviderRefusalIsNotBlamedOnTheCaller(t *testing.T) {
	limited := clientWith(t, envWorkspaceLimited, envAPIKeyLimited)
	ctx := testCtx(t)

	if _, err := limited.Users.List(ctx, lightweight.UserListOptions{Max: 1}); err != nil {
		t.Fatalf("the under-privileged connection cannot even read (%v); the refusal below "+
			"would not be specific to writes", err)
	}

	_, err := limited.Roles.Create(ctx, lightweight.CreateRoleRequest{
		Name: fmt.Sprintf("sdk-provider-forbidden-%d", time.Now().UnixNano()%1000000),
	})
	e := apiErr(t, err)

	switch e.Code {
	case lightweight.CodeInsufficientScope, lightweight.CodeWorkspaceMismatch, lightweight.CodeOperatorOnly:
		t.Errorf("code = %q — a provider-side refusal is being reported as a caller-side one", e.Code)
	case lightweight.CodeProviderForbidden, lightweight.CodeConnectionReadOnly:
		if e.StatusCode != 409 {
			t.Errorf("status = %d, want 409 for %s", e.StatusCode, e.Code)
		}
	default:
		// Another refusal is a contract detail, not a security failure: the
		// write did not happen and the caller was not blamed. Reported rather
		// than failed so a legitimate change does not read as a vulnerability.
		t.Logf("provider refusal surfaced as %d %s", e.StatusCode, e.Code)
	}
}

// TestAcceptance_AForeignRealmsUserIdIsNotResolvable.
//
// The cross-realm resource-id case, through the SDK. The client is entirely
// legitimate; only the id belongs elsewhere. It must not resolve, and it must
// be indistinguishable from an id that exists nowhere — otherwise one valid key
// in any workspace becomes an oracle for "does this user exist somewhere in
// this installation".
func TestAcceptance_AForeignRealmsUserIdIsNotResolvable(t *testing.T) {
	foreignID := os.Getenv(envForeignUserID)
	if foreignID == "" {
		t.Skipf("SKIP: %s names a user in another workspace's realm", envForeignUserID)
	}
	client := mainClient(t)
	ctx := testCtx(t)

	foreign := apiErr(t, func() error { _, err := client.Users.Get(ctx, foreignID); return err }())
	nobody := apiErr(t, func() error {
		_, err := client.Users.Get(ctx, "00000000-1111-4000-8000-999999999999")
		return err
	}())

	if foreign.StatusCode != nobody.StatusCode || foreign.Code != nobody.Code {
		t.Errorf("a user from another realm is distinguishable from one that exists nowhere: "+
			"%d %s vs %d %s", foreign.StatusCode, foreign.Code, nobody.StatusCode, nobody.Code)
	}
	if foreign.StatusCode != 404 {
		t.Errorf("status = %d, want 404 for an id outside this workspace's realm", foreign.StatusCode)
	}
}

// TestAcceptance_ARevokedCredentialIsRefused — the simple form: a key the
// operator revoked before this process started.
func TestAcceptance_ARevokedCredentialIsRefused(t *testing.T) {
	revoked := clientWith(t, lightweight.EnvWorkspaceID, envAPIKeyGone)

	_, err := revoked.Users.List(testCtx(t), lightweight.UserListOptions{})
	e := apiErr(t, err)

	if e.Code != lightweight.CodeCredentialInvalid {
		t.Errorf("code = %q, want %q", e.Code, lightweight.CodeCredentialInvalid)
	}
	if e.StatusCode != 401 {
		t.Errorf("status = %d, want 401", e.StatusCode)
	}
}

// TestAcceptance_RevocationTakesEffectOnTheSameLiveClient is the strong form,
// and the one that actually proves something about this SDK.
//
// The claim under test is that the SDK caches no authorization. A test that
// restarted the process between the two calls would prove nothing: any client,
// however badly written, works after a restart.
//
// So the revocation happens WHILE this client is alive, and the client is never
// rebuilt:
//
//  1. the client succeeds
//  2. this test touches a signal file
//  3. the harness, watching for it, revokes the credential as the operator
//  4. the SAME client object keeps calling until it is refused
//
// The operator token stays on the other side of that file. This process never
// sees it, which is the second thing the arrangement proves.
func TestAcceptance_RevocationTakesEffectOnTheSameLiveClient(t *testing.T) {
	signalFile := os.Getenv(envRevokeSig)
	if signalFile == "" {
		t.Skipf("SKIP: %s is not set; this needs a harness able to revoke on request", envRevokeSig)
	}
	doomed := clientWith(t, lightweight.EnvWorkspaceID, envAPIKeyDoom)
	ctx := testCtx(t)

	if _, err := doomed.Users.List(ctx, lightweight.UserListOptions{Max: 1}); err != nil {
		t.Fatalf("the credential does not work before revocation: %v", err)
	}
	t.Log("the credential works; asking the operator to revoke it")

	if err := os.WriteFile(signalFile, []byte("revoke\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", signalFile, err)
	}

	deadline := time.Now().Add(45 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		_, last = doomed.Users.List(ctx, lightweight.UserListOptions{Max: 1})
		if last != nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	e := apiErr(t, last)
	if e.Code != lightweight.CodeCredentialInvalid {
		t.Errorf("code = %q, want %q", e.Code, lightweight.CodeCredentialInvalid)
	}
	if e.StatusCode != 401 {
		t.Errorf("status = %d, want 401", e.StatusCode)
	}
	t.Log("the same client instance was refused after revocation, with no restart")
}

// TestAcceptance_RateLimitIsSurfacedNotSwallowed.
//
// Deliberately over-driving one credential, from one client, to prove three
// things at once: the limit is enforced per credential, the refusal decodes as
// rate_limit_exceeded, and the SDK does not quietly retry it into a success —
// which would turn a backpressure signal into a hidden retry storm.
func TestAcceptance_RateLimitIsSurfacedNotSwallowed(t *testing.T) {
	if os.Getenv(envSkipRateLim) != "" {
		t.Skipf("SKIP: %s is set", envSkipRateLim)
	}
	client := mainClient(t)
	ctx := testCtx(t)

	burst := 120
	if raw := os.Getenv(envCredBurst); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			burst = n * 3
		}
	}

	var (
		mu       sync.Mutex
		limited  *lightweight.APIError
		attempts int
	)
	var wg sync.WaitGroup
	for range burst {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := client.Users.List(ctx, lightweight.UserListOptions{Max: 1})
			mu.Lock()
			defer mu.Unlock()
			attempts++
			var e *lightweight.APIError
			if errors.As(err, &e) && e.Code == lightweight.CodeRateLimitExceeded && limited == nil {
				limited = e
			}
		}()
	}
	wg.Wait()

	if limited == nil {
		t.Skipf("SKIP: %d concurrent reads did not reach the limit; it may be configured higher here", attempts)
	}

	if limited.StatusCode != 429 {
		t.Errorf("status = %d, want 429", limited.StatusCode)
	}
	if limited.RequestID == "" {
		t.Error("the rate-limit refusal carries no request id")
	}
	if wait, ok := limited.RetryAfter(); !ok {
		t.Error("the server sent no Retry-After; a backend has nothing to pace itself by")
	} else if wait <= 0 {
		t.Errorf("Retry-After = %v", wait)
	} else {
		t.Logf("rate limited after %d concurrent reads; Retry-After %v", attempts, wait)
	}
}

// ─── The abstraction itself ─────────────────────────────────────────────────

// TestAcceptance_TheProgramNeedsNothingButTheThreeVariables.
//
// The claim is not merely that a backend CAN work with three variables — it is
// that it needs nothing else. So every variable a consumer must never need is
// poisoned for the duration of this test, and the full journey is run again
// against the poisoned environment.
//
// If any code path in this package or in the SDK ever started reading a provider
// URL, a tenant name or a database handle, it would read garbage here and the
// test would fail. Nothing else in the suite could catch that: absence is not
// observable by running the happy path in an environment where the values happen
// to be correct.
func TestAcceptance_TheProgramNeedsNothingButTheThreeVariables(t *testing.T) {
	for _, name := range forbiddenEnv {
		t.Setenv(name, "poisoned-by-the-acceptance-suite-if-you-read-this-you-should-not-have")
	}

	client := mainClient(t)
	ctx := testCtx(t)

	if _, err := client.Users.List(ctx, lightweight.UserListOptions{Max: 5}); err != nil {
		t.Fatalf("a read failed with the provider variables poisoned: %v\n\n"+
			"Something in this program or in the SDK is reading configuration a consumer is\n"+
			"promised it never needs.", err)
	}
	if _, err := client.Roles.List(ctx); err != nil {
		t.Fatalf("Roles.List failed with the provider variables poisoned: %v", err)
	}
}

// TestAcceptance_ErrorsNeverCarryTheCredential.
//
// The unit suite proves this against synthetic responses. This proves it against
// the real server's real refusals, which is where a message this package did not
// author could pick something up.
func TestAcceptance_ErrorsNeverCarryTheCredential(t *testing.T) {
	requireEnv(t, lightweight.EnvAPIKey)
	key := os.Getenv(lightweight.EnvAPIKey)
	secret := key
	if i := strings.LastIndexByte(key, '_'); i >= 0 {
		secret = key[i+1:]
	}

	client := mainClient(t)
	ctx := testCtx(t)

	// A handful of real refusals from a real server.
	var errs []error
	if _, err := client.Users.Get(ctx, "00000000-0000-0000-0000-000000000000"); err != nil {
		errs = append(errs, err)
	}
	if _, err := client.Users.Get(ctx, "not-a-uuid"); err != nil {
		errs = append(errs, err)
	}
	if _, err := client.Roles.Get(ctx, "no-such-role-in-this-workspace"); err != nil {
		errs = append(errs, err)
	}
	if _, err := client.Users.Create(ctx, lightweight.CreateUserRequest{}); err != nil {
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		t.Fatal("no refusal was provoked; the check would pass vacuously")
	}

	for _, err := range errs {
		for _, rendered := range []string{
			err.Error(),
			fmt.Sprintf("%v", err),
			fmt.Sprintf("%+v", err),
			fmt.Sprintf("%#v", err),
		} {
			if strings.Contains(rendered, key) {
				t.Errorf("an error rendered the whole API key:\n%s", rendered)
			}
			if strings.Contains(rendered, secret) {
				t.Errorf("an error rendered the key's secret segment:\n%s", rendered)
			}
		}
	}
}
