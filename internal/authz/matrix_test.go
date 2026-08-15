package authz

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Layer A of the Slice 14 negative-authorization matrix: the mechanical layer.
//
// Every test here iterates ProjectReachableRoutes() or OperatorOnlyRoutes()
// rather than a list written in this file. That is the whole point. A route
// added to the registry in a future slice is swept by all of them on the next
// run, and a route that somehow reaches the registry without a working denial
// fails here in milliseconds rather than in a real-stack suite twenty minutes
// later — or, worse, not at all.
//
// What this layer proves:
//
//	required scope present            → allowed
//	required scope absent             → refused, handler never runs
//	any OTHER scope in the vocabulary → refused
//	read scope on a write route       → refused
//	unknown / wildcard-looking scope  → refused
//	duplicated scopes                 → same decision, no surprise
//	wrong workspace + right scope     → refused as a mismatch
//	operator-only route + every scope → refused as operator_only
//
// What it deliberately does NOT prove: that the router mounts this middleware,
// that the handler behind it never ran, or that Keycloak was never contacted.
// Those need the real chain and the real stack, and are Layers B and C.

// concretePath turns a gin pattern into a URL by giving every parameter a
// syntactically plausible value.
//
// The values are irrelevant to every assertion in this file — authorization
// runs before any of them is looked at — but they must be well formed, because
// an id that fails to parse would let a route be refused for the wrong reason
// and quietly weaken the case it is standing in for.
func concretePath(pattern, workspaceID string) string {
	return strings.NewReplacer(
		":workspace_id", workspaceID,
		":user_id", "9c1e6679-7425-40de-944b-e07fc1f90ae7",
		":role_name", "billing-reader",
		":session_id", "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		":invitation_id", "7c9e6679-7425-40de-944b-e07fc1f90ae7",
		":project_id", "prj_7c9e6679-7425-40de-944b-e07fc1f90ae7",
		":connection_id", "conn_7c9e6679-7425-40de-944b-e07fc1f90ae7",
		":credential_id", "key_9b2f4c1a-1111-4222-8333-444455556666",
	).Replace(pattern)
}

// call runs one request through Authorize alone and reports what happened.
func call(t *testing.T, r RouteRequirement, workspaceInPath string, scopes []string) (status int, code string, reached bool) {
	t.Helper()

	router := buildRouter(Config{}, projectPrincipal(wsA, scopes...), r.Method, r.Path, &reached)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(r.Method, concretePath(r.Path, workspaceInPath), nil))

	if w.Code == http.StatusOK {
		return w.Code, "", reached
	}
	return w.Code, bodyCode(t, w), reached
}

// ─── The list itself ────────────────────────────────────────────────────────

// TestMatrix_TheSweepIsNotVacuous is the guard on every other test in this file.
//
// Each of them iterates a slice. A slice that became empty — a refactor that
// broke the derivation, a registry emptied by a bad merge — would make all of
// them pass while proving nothing at all. This is the one assertion that fails
// in that case, so it is worth its five lines.
func TestMatrix_TheSweepIsNotVacuous(t *testing.T) {
	reachable := ProjectReachableRoutes()
	if len(reachable) < 20 {
		t.Fatalf("%d project-reachable routes; the negative sweep would be vacuous or the "+
			"registry has shrunk unexpectedly", len(reachable))
	}
	if len(OperatorOnlyRoutes()) < 10 {
		t.Fatalf("%d operator-only routes; the control-plane sweep would be vacuous",
			len(OperatorOnlyRoutes()))
	}
	if len(Families()) < 5 {
		t.Fatalf("%d capability families; the family sweep would be vacuous", len(Families()))
	}
}

// TestMatrix_EveryProjectReachableRouteDemandsAKnownScope.
//
// A scoped route whose scope is not in the vocabulary can never be satisfied,
// because NormalizeScopes refuses to store it on a credential. It would present
// as an endpoint that answers 403 to every key ever issued, which is a bug that
// looks exactly like a permissions problem and would be debugged as one.
func TestMatrix_EveryProjectReachableRouteDemandsAKnownScope(t *testing.T) {
	for _, r := range ProjectReachableRoutes() {
		if r.Scope == "" {
			t.Errorf("%s is project-reachable with an empty scope", r.Key())
			continue
		}
		if !IsKnownScope(r.Scope) {
			t.Errorf("%s demands %q, which is not in the vocabulary; no credential can ever hold it",
				r.Key(), r.Scope)
		}
		if r.Scope.Family() == "" || r.Scope.Verb() == "" {
			t.Errorf("%s demands %q, which is not in `resource:verb` form", r.Key(), r.Scope)
		}
	}
}

// ─── The scope boundary, route by route ─────────────────────────────────────

// TestMatrix_RequiredScopeIsSufficient is the positive half, and it is here so
// the negative half cannot pass by refusing everything.
//
// A middleware that denied unconditionally would satisfy every other test in
// this file. This is the one that stops it.
func TestMatrix_RequiredScopeIsSufficient(t *testing.T) {
	for _, r := range ProjectReachableRoutes() {
		status, code, reached := call(t, r, wsA, []string{string(r.Scope)})
		if !reached {
			t.Errorf("%s: a credential holding %s was refused (%d %s)", r.Key(), r.Scope, status, code)
		}
	}
}

// TestMatrix_MissingScopeIsRefusedOnEveryRoute — the core of KI-018.
//
// A credential with no scopes at all, on every project-reachable route. The
// header assertion is not decoration: RFC 6750 §3.1 names the required scope,
// and it is the difference between a developer fixing their key and filing a
// ticket. A route that refused without naming it would be a usability
// regression that no status-code assertion would notice.
func TestMatrix_MissingScopeIsRefusedOnEveryRoute(t *testing.T) {
	for _, r := range ProjectReachableRoutes() {
		reached := false
		router := buildRouter(Config{}, projectPrincipal(wsA), r.Method, r.Path, &reached)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(r.Method, concretePath(r.Path, wsA), nil))

		if reached {
			t.Errorf("%s: a credential with NO scopes reached the handler", r.Key())
			continue
		}
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", r.Key(), w.Code)
		}
		if got := bodyCode(t, w); got != ErrInsufficientScope.Code {
			t.Errorf("%s: code = %q, want %q", r.Key(), got, ErrInsufficientScope.Code)
		}
		if h := w.Header().Get("WWW-Authenticate"); !strings.Contains(h, string(r.Scope)) {
			t.Errorf("%s: WWW-Authenticate = %q, want it to name %q", r.Key(), h, r.Scope)
		}
	}
}

// TestMatrix_EveryOtherScopeIsRefused is the wrong-family and wrong-verb sweep,
// exhaustively: every route against every scope in the vocabulary that is not
// the one it requires.
//
// It is one loop rather than separate "wrong family" and "read cannot write"
// tests because the property is one property — the capability model has no
// implication of any kind, and a scope grants exactly the routes that name it.
// Splitting it would leave the combinations that fall into neither category
// (`audit:read` against `sessions:revoke`, say) untested by both.
//
// If an implication is ever introduced deliberately, this test is where it must
// be declared, and the cost of declaring it is that a reviewer sees it.
func TestMatrix_EveryOtherScopeIsRefused(t *testing.T) {
	for _, r := range ProjectReachableRoutes() {
		for _, other := range AllScopes() {
			if other == r.Scope {
				continue
			}
			status, code, reached := call(t, r, wsA, []string{string(other)})
			if reached {
				t.Errorf("%s requires %s but was reached by a credential holding only %s",
					r.Key(), r.Scope, other)
				continue
			}
			if status != http.StatusForbidden || code != ErrInsufficientScope.Code {
				t.Errorf("%s with only %s: got %d %s, want 403 insufficient_scope",
					r.Key(), other, status, code)
			}
		}
	}
}

// TestMatrix_ReadScopeCannotWrite states the read/write boundary on its own,
// even though the sweep above subsumes it.
//
// It is named separately because it is the property an operator reasons about
// when handing out a key — "this integration only reads" — and a named failure
// says that in one line instead of as one row of a 200-case sweep.
func TestMatrix_ReadScopeCannotWrite(t *testing.T) {
	checked := map[Family]bool{}

	for _, r := range ProjectReachableRoutes() {
		if !r.Scope.IsWrite() {
			continue
		}
		readScope, ok := ReadScopeOf(r.Scope)
		if !ok {
			t.Errorf("%s requires the write scope %s, whose family has no read counterpart; "+
				"the read/write boundary cannot be stated for it", r.Key(), r.Scope)
			continue
		}
		checked[r.Scope.Family()] = true

		status, code, reached := call(t, r, wsA, []string{string(readScope)})
		if reached {
			t.Errorf("%s is a write requiring %s, and %s reached it", r.Key(), r.Scope, readScope)
			continue
		}
		if status != http.StatusForbidden || code != ErrInsufficientScope.Code {
			t.Errorf("%s with only %s: got %d %s, want 403 insufficient_scope",
				r.Key(), readScope, status, code)
		}
	}

	// Every family that HAS a write scope must have contributed a case. A
	// family whose write routes all disappeared would otherwise pass silently.
	for _, s := range AllScopes() {
		if s.IsWrite() && !checked[s.Family()] {
			t.Errorf("no route requires %s, so the read/write boundary for the %s family is untested",
				s, s.Family())
		}
	}
}

// TestMatrix_AuditReadIsIsolatedInBothDirections.
//
// `audit:read` is the one scope that reads the record of what every other actor
// in the workspace has done, including operators and including other projects.
// Both directions matter and they fail differently:
//
//	powerful identity scopes → must NOT reach the trail
//	audit:read alone         → must NOT reach identity
func TestMatrix_AuditReadIsIsolatedInBothDirections(t *testing.T) {
	var auditRoutes, identityRoutes []RouteRequirement
	for _, r := range ProjectReachableRoutes() {
		if r.Scope == ScopeAuditRead {
			auditRoutes = append(auditRoutes, r)
		} else {
			identityRoutes = append(identityRoutes, r)
		}
	}
	if len(auditRoutes) == 0 || len(identityRoutes) == 0 {
		t.Fatalf("audit routes = %d, identity routes = %d; the isolation check would be vacuous",
			len(auditRoutes), len(identityRoutes))
	}

	// Direction 1: everything except audit:read — the whole identity vocabulary
	// at once, which is the most powerful key that can be issued without it.
	var everythingElse []string
	for _, s := range AllScopes() {
		if s != ScopeAuditRead {
			everythingElse = append(everythingElse, string(s))
		}
	}
	for _, r := range auditRoutes {
		status, code, reached := call(t, r, wsA, everythingElse)
		if reached {
			t.Errorf("%s was reached by a credential holding every scope EXCEPT audit:read", r.Key())
			continue
		}
		if status != http.StatusForbidden || code != ErrInsufficientScope.Code {
			t.Errorf("%s without audit:read: got %d %s, want 403 insufficient_scope", r.Key(), status, code)
		}
	}

	// Direction 2: audit:read is not a back door into identity.
	for _, r := range identityRoutes {
		if _, _, reached := call(t, r, wsA, []string{string(ScopeAuditRead)}); reached {
			t.Errorf("%s (requires %s) was reached by an audit:read-only credential", r.Key(), r.Scope)
		}
	}
}

// ─── Scopes that do not exist ───────────────────────────────────────────────

// TestMatrix_NoWildcardOrAdminScopeExists answers the "is there a super-scope"
// question with a test rather than with prose in a document.
//
// There is none today. If one is ever added, this fails and whoever adds it has
// to come here and say so deliberately — which is the point, because a scope
// that grants everything is the single change most likely to invalidate every
// other test in this file.
func TestMatrix_NoWildcardOrAdminScopeExists(t *testing.T) {
	for _, s := range AllScopes() {
		switch strings.ToLower(string(s)) {
		case "*", "admin", "all", "full", "superuser", "owner":
			t.Errorf("%q is in the vocabulary and reads as a super-scope; the negative matrix "+
				"assumes scopes grant exactly the routes that name them", s)
		}
		if strings.Contains(string(s), "*") {
			t.Errorf("%q contains a wildcard; HasScope compares whole strings, so this would "+
				"grant nothing while looking as though it granted everything", s)
		}
	}
}

// TestMatrix_UnknownScopeGrantsNothing — fail closed on a value that should be
// impossible.
//
// Creation validation rejects unknown scopes (NormalizeScopes), so a credential
// carrying one can only arrive by manual SQL, a bad migration, or a restored
// backup from a build with a different vocabulary. The runtime must not
// interpret any of those as broader permission — least of all the values a
// person would reach for by hand.
func TestMatrix_UnknownScopeGrantsNothing(t *testing.T) {
	imposters := []string{
		"*", "admin", "all", "full", "superuser",
		"users:*", "*:*", "users:admin", "users:write:all",
		"USERS:READ",  // right scope, wrong case: HasScope is exact
		" users:read", // right scope, untrimmed
		"users:read ",
		"users:read,users:write", // a whole set jammed into one entry
	}

	for _, r := range ProjectReachableRoutes() {
		for _, bad := range imposters {
			status, code, reached := call(t, r, wsA, []string{bad})
			if reached {
				t.Errorf("%s was reached by a credential whose only scope is %q", r.Key(), bad)
				continue
			}
			if status != http.StatusForbidden || code != ErrInsufficientScope.Code {
				t.Errorf("%s with %q: got %d %s, want 403 insufficient_scope", r.Key(), bad, status, code)
			}
		}
	}
}

// TestMatrix_DuplicateScopesAreDeterministic.
//
// Lower severity than the rest of this file, and checked anyway because
// authorization normalization that is not deterministic is authorization nobody
// can reason about. A duplicated grant must decide exactly as a single one, and
// a duplicated WRONG scope must not accumulate into a right one.
func TestMatrix_DuplicateScopesAreDeterministic(t *testing.T) {
	for _, r := range ProjectReachableRoutes() {
		granted := string(r.Scope)

		if _, _, reached := call(t, r, wsA, []string{granted, granted, granted}); !reached {
			t.Errorf("%s: three copies of %s were refused where one is accepted", r.Key(), granted)
		}

		wrong, ok := ReadScopeOf(r.Scope)
		if !ok || wrong == r.Scope {
			continue
		}
		if _, _, reached := call(t, r, wsA, []string{string(wrong), string(wrong)}); reached {
			t.Errorf("%s: two copies of %s reached a route requiring %s", r.Key(), wrong, r.Scope)
		}
	}
}

// ─── The workspace boundary, route by route ─────────────────────────────────

// TestMatrix_WrongWorkspaceIsRefusedOnEveryRoute.
//
// The credential holds exactly the right scope; only the workspace in the path
// is another tenant's. Every route must answer workspace_mismatch, and must
// answer it in preference to any scope complaint, so a credential probing
// another tenant learns nothing about which scopes would have been needed
// there.
func TestMatrix_WrongWorkspaceIsRefusedOnEveryRoute(t *testing.T) {
	for _, r := range ProjectReachableRoutes() {
		status, code, reached := call(t, r, wsB, []string{string(r.Scope)})
		if reached {
			t.Errorf("%s: a credential bound to %s reached workspace %s", r.Key(), wsA, wsB)
			continue
		}
		if status != http.StatusForbidden || code != ErrWorkspaceMismatch.Code {
			t.Errorf("%s against a foreign workspace: got %d %s, want 403 workspace_mismatch",
				r.Key(), status, code)
		}
	}
}

// TestMatrix_ForeignWorkspaceOutranksMissingScope pins the ORDER on every
// route, not just on the one the ordering test in authorize_test.go uses.
//
// A credential with no scopes, addressing someone else's workspace, must be
// told "not yours" rather than "you need users:write". The second answer would
// let an outsider enumerate the capability model of a tenant they cannot reach.
func TestMatrix_ForeignWorkspaceOutranksMissingScope(t *testing.T) {
	for _, r := range ProjectReachableRoutes() {
		status, code, _ := call(t, r, wsB, nil)
		if status != http.StatusForbidden || code != ErrWorkspaceMismatch.Code {
			t.Errorf("%s with no scopes against a foreign workspace: got %d %s, "+
				"want 403 workspace_mismatch (the binding must be answered first)",
				r.Key(), status, code)
		}
	}
}

// ─── The control plane ──────────────────────────────────────────────────────

// TestMatrix_OperatorOnlyRoutesRefuseTheEntireVocabulary.
//
// Not "a credential without the right scope" — the most powerful credential the
// system can issue, holding every scope that exists, inside its OWN workspace.
// It must still be refused, and refused with operator_only rather than
// insufficient_scope, because no key can be minted that would satisfy these and
// telling a developer to add a scope would send them hunting for one that does
// not exist.
func TestMatrix_OperatorOnlyRoutesRefuseTheEntireVocabulary(t *testing.T) {
	every := ScopeStrings(AllScopes())

	for _, r := range OperatorOnlyRoutes() {
		// Control-plane routes without a :workspace_id cannot be addressed
		// "inside the credential's own workspace"; concretePath leaves the
		// pattern alone and the answer is the same either way.
		status, code, reached := call(t, r, wsA, every)
		if reached {
			t.Errorf("%s is operator-only and was reached by a credential holding every scope", r.Key())
			continue
		}
		if status != http.StatusForbidden || code != ErrOperatorOnly.Code {
			t.Errorf("%s with every scope: got %d %s, want 403 operator_only", r.Key(), status, code)
		}
	}
}

// TestMatrix_OperatorOnlyOutranksTheWorkspaceBinding.
//
// A project credential addressing an operator-only route in ANOTHER workspace
// gets operator_only, not workspace_mismatch. Both are refusals and neither
// leaks anything — the check consults no storage in either case — and pinning
// which one arrives keeps the contract stable for a client that branches on it.
func TestMatrix_OperatorOnlyOutranksTheWorkspaceBinding(t *testing.T) {
	for _, r := range OperatorOnlyRoutes() {
		if !strings.Contains(r.Path, ":workspace_id") {
			continue
		}
		status, code, _ := call(t, r, wsB, ScopeStrings(AllScopes()))
		if status != http.StatusForbidden || code != ErrOperatorOnly.Code {
			t.Errorf("%s in a foreign workspace: got %d %s, want 403 operator_only", r.Key(), status, code)
		}
	}
}

// ─── Coverage of the matrix itself ──────────────────────────────────────────

// TestMatrix_EveryCapabilityFamilyIsExercised.
//
// The sweeps above iterate routes, so a family whose routes were all removed
// would simply stop being tested — silently, and while every remaining
// assertion still passed. This asserts the shape of the surface rather than its
// behaviour: every family in the vocabulary must own at least one route, and
// every route must belong to a family in the vocabulary.
func TestMatrix_EveryCapabilityFamilyIsExercised(t *testing.T) {
	routesPerFamily := map[Family]int{}
	for _, r := range ProjectReachableRoutes() {
		routesPerFamily[FamilyOfRoute(r)]++
	}

	for _, f := range Families() {
		if routesPerFamily[f] == 0 {
			t.Errorf("capability family %q has no project-reachable route; either the family is "+
				"dead and should leave the vocabulary, or its routes were lost", f)
		}
	}
	for family := range routesPerFamily {
		known := false
		for _, f := range Families() {
			if f == family {
				known = true
			}
		}
		if !known {
			t.Errorf("routes are classified under family %q, which is not in the scope vocabulary", family)
		}
	}
}

// TestMatrix_ClassificationCountsAreStable prints the shape of the surface and
// fails when it changes in a direction nobody declared.
//
// It is a canary, not a rule: the numbers are allowed to grow, and growing them
// means editing this test, which is exactly the moment to ask whether the new
// route got its negative evidence. Shrinking is what it really catches — a
// route that quietly stopped being classified, which every per-route sweep
// above would then skip.
func TestMatrix_ClassificationCountsAreStable(t *testing.T) {
	const (
		wantReachable    = 24
		wantOperatorOnly = 23
		wantScopes       = 9
		wantFamilies     = 5
	)

	if got := len(ProjectReachableRoutes()); got != wantReachable {
		t.Errorf("project-reachable routes = %d, want %d.\n"+
			"  If a route was added, add its negative evidence and update this number.\n"+
			"  If a route vanished, find out why before updating it.", got, wantReachable)
	}
	if got := len(OperatorOnlyRoutes()); got != wantOperatorOnly {
		t.Errorf("operator-only routes = %d, want %d", got, wantOperatorOnly)
	}
	if got := len(AllScopes()); got != wantScopes {
		t.Errorf("scopes = %d, want %d", got, wantScopes)
	}
	if got := len(Families()); got != wantFamilies {
		t.Errorf("capability families = %d, want %d", got, wantFamilies)
	}
}
