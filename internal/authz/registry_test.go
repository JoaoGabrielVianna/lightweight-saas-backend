package authz

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestRegistry_EveryMountedIdentityRouteIsClassified is the completeness
// property this slice exists to make provable.
//
// A route added in a future slice cannot reach a provider without someone
// having decided, explicitly, whether a machine credential may call it. The
// route table is declared once in identityruntime and consumed here, so neither
// side can drift quietly: adding a route there fails this test until it is
// classified, and classifying a route that is not mounted fails the reverse
// test below.
func TestRegistry_EveryMountedIdentityRouteIsClassified(t *testing.T) {
	if err := ValidateRegistry(IdentityRoutes()); err != nil {
		t.Fatalf("%v\n\nAdd an entry to internal/authz/registry.go. A route with no entry "+
			"is denied at runtime, which means a security decision nobody made.", err)
	}
}

// TestRegistry_ClassificationIsExactlyOne — a requirement is either
// operator-only or scoped, never both and never neither. The zero value would
// be "scoped to the empty scope", which no credential can hold and which would
// therefore deny silently rather than deny deliberately.
func TestRegistry_ClassificationIsExactlyOne(t *testing.T) {
	for _, route := range RegisteredRoutes() {
		method, path, _ := strings.Cut(route, " ")
		req, ok := RequirementFor(method, path)
		if !ok {
			t.Fatalf("%s is listed but does not resolve", route)
		}

		switch {
		case req.OperatorOnly && req.Scope != "":
			t.Errorf("%s is both operator-only and scoped", route)
		case !req.OperatorOnly && req.Scope == "":
			t.Errorf("%s has neither classification", route)
		case !req.OperatorOnly && !IsKnownScope(req.Scope):
			t.Errorf("%s requires unknown scope %q", route, req.Scope)
		}
	}
}

// TestRegistry_ControlPlaneIsOperatorOnly pins the boundary no scope may cross.
//
// Workspace management, connection management and project management are the
// three surfaces where a machine credential would escalate rather than operate:
// creating a connection would repoint a realm, and minting credentials would
// make revocation meaningless because a revoked key has already issued another.
func TestRegistry_ControlPlaneIsOperatorOnly(t *testing.T) {
	controlPlane := regexp.MustCompile(`^(GET|POST|PATCH|DELETE) /v1/workspaces(/:workspace_id)?$|/connections|/projects|/archive$|/v1/project-scopes`)

	for _, route := range RegisteredRoutes() {
		if !controlPlane.MatchString(route) {
			continue
		}
		method, path, _ := strings.Cut(route, " ")
		req, _ := RequirementFor(method, path)
		if !req.OperatorOnly {
			t.Errorf("%s is control-plane but is reachable with scope %q", route, req.Scope)
		}
	}
}

// TestRegistry_PasswordSetIsOperatorOnly is a named regression.
//
// PUT .../password sets a credential directly, with no email and no consent: a
// complete account-takeover primitive. reset-password covers every legitimate
// backend flow. Including it in users:write could never be walked back, because
// every key issued under the looser rule would keep the capability.
func TestRegistry_PasswordSetIsOperatorOnly(t *testing.T) {
	req, ok := RequirementFor("PUT", "/v1/workspaces/:workspace_id/users/:user_id/password")
	if !ok {
		t.Fatal("the direct password route is not classified")
	}
	if !req.OperatorOnly {
		t.Fatalf("PUT .../password is reachable with scope %q; it must be operator-only", req.Scope)
	}

	// The email-based reset IS available to a project — the two must not have
	// been confused for one another.
	reset, _ := RequirementFor("POST", "/v1/workspaces/:workspace_id/users/:user_id/reset-password")
	if reset.OperatorOnly || reset.Scope != ScopeUsersWrite {
		t.Errorf("reset-password should be scoped users:write, got operatorOnly=%v scope=%q",
			reset.OperatorOnly, reset.Scope)
	}
}

// TestRegistry_RoleGrantsAreRolesWriteNotUsersWrite pins the least-privilege
// split that lets an operator hand a backend profile management without also
// handing it the ability to grant privileges.
func TestRegistry_RoleGrantsAreRolesWriteNotUsersWrite(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{"POST", "/v1/workspaces/:workspace_id/users/:user_id/roles"},
		{"DELETE", "/v1/workspaces/:workspace_id/users/:user_id/roles/:role_name"},
	} {
		req, ok := RequirementFor(route.method, route.path)
		if !ok {
			t.Fatalf("%s %s is not classified", route.method, route.path)
		}
		if req.Scope != ScopeRolesWrite {
			t.Errorf("%s %s requires %q, want %q — the sensitive thing is the privilege, not the user record",
				route.method, route.path, req.Scope, ScopeRolesWrite)
		}
	}
}

// TestRegistry_ReadsNeverRequireAWriteScope guards against a copy-paste that
// would force an operator to grant write access to obtain read access.
func TestRegistry_ReadsNeverRequireAWriteScope(t *testing.T) {
	writeScopes := map[Scope]bool{
		ScopeUsersWrite: true, ScopeRolesWrite: true,
		ScopeSessionsRevoke: true, ScopeInvitationsWrite: true,
	}
	for _, route := range RegisteredRoutes() {
		method, path, _ := strings.Cut(route, " ")
		if method != "GET" {
			continue
		}
		req, _ := RequirementFor(method, path)
		if !req.OperatorOnly && writeScopes[req.Scope] {
			t.Errorf("%s is a read but requires the write scope %q", route, req.Scope)
		}
	}
}

// TestRegistry_UnclassifiedRouteIsReported proves ValidateRegistry actually
// detects a gap, so the boot-time check is not a no-op that always passes.
func TestRegistry_UnclassifiedRouteIsReported(t *testing.T) {
	err := ValidateRegistry([]string{"GET /v1/workspaces/:workspace_id/something-new"})
	if err == nil {
		t.Fatal("ValidateRegistry accepted an unclassified route")
	}
	if !strings.Contains(err.Error(), "something-new") {
		t.Errorf("error %q does not name the offending route", err)
	}
}

// TestScopes_MatchTheDatabaseConstraint keeps the Go vocabulary and the CHECK
// constraint in the migrations in agreement.
//
// They are two enforcement points for one contract. If Go gains a scope the
// database refuses, credential creation fails at INSERT with a constraint
// violation an operator cannot act on; if the database allows one Go does not
// know, a stored scope silently grants nothing.
//
// It reads the EFFECTIVE constraint — the last definition across every up
// migration, in version order — rather than one named file. The vocabulary was
// introduced in 000005 and widened by 000006 (`audit:read`), and it will move
// again: a gate pinned to one filename would either break on the next widening
// or, worse, keep passing while checking a definition the database has since
// replaced.
func TestScopes_MatchTheDatabaseConstraint(t *testing.T) {
	block := effectiveScopesConstraint(t)

	for _, s := range AllScopes() {
		if !strings.Contains(block, "'"+string(s)+"'") {
			t.Errorf("scope %q is defined in Go but absent from the effective CHECK constraint", s)
		}
	}

	// And the reverse: every quoted scope-shaped literal inside the CHECK must
	// be a scope Go knows.
	for _, m := range regexp.MustCompile(`'([a-z]+:[a-z]+)'`).FindAllStringSubmatch(block, -1) {
		if !IsKnownScope(Scope(m[1])) {
			t.Errorf("the migrations allow scope %q which Go does not define", m[1])
		}
	}
}

// effectiveScopesConstraint returns the body of the LAST
// project_credentials_scopes_known CHECK defined across the up migrations.
//
// Sorted by filename, which is version order because the files are
// zero-padded — the same ordering golang-migrate applies, so "last one wins"
// here means the same thing it means in a migrated database.
func effectiveScopesConstraint(t *testing.T) string {
	t.Helper()

	files, err := filepath.Glob("../database/migrations/*.up.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no up migrations found; this gate would pass vacuously")
	}
	sort.Strings(files)

	// Non-greedy to the closing `]::text[]`, so one file defining the
	// constraint twice (drop + re-add) yields each definition separately and
	// the last of them wins.
	pattern := regexp.MustCompile(`(?s)project_credentials_scopes_known.*?\]::text\[\]`)

	var last string
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if matches := pattern.FindAllString(string(sql), -1); len(matches) > 0 {
			last = matches[len(matches)-1]
		}
	}
	if last == "" {
		t.Fatal("could not locate the scopes CHECK constraint in any migration")
	}
	return last
}
