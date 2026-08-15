package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/authz"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/project"
)

// ─── The completeness gate ──────────────────────────────────────────────────

// TestNegative_TheRequestTableCoversEveryRoute is the gate the slice asks for.
//
// nzRequests is hand-written, because a body that actually satisfies each
// handler cannot be generated. This test is what makes that safe: it compares
// the table against the registry in both directions, so
//
//	a route added to the registry    → this fails until it is exercised here
//	a route removed from the registry → this fails until the stale row goes
//
// Neither is a judgement call, and neither can be satisfied by editing only one
// of the two files.
func TestNegative_TheRequestTableCoversEveryRoute(t *testing.T) {
	inTable := map[string]bool{}
	for _, r := range nzRequests {
		if inTable[r.Key()] {
			t.Errorf("%s appears twice in nzRequests", r.Key())
		}
		inTable[r.Key()] = true
	}

	reachable := map[string]bool{}
	for _, r := range authz.ProjectReachableRoutes() {
		reachable[r.Key()] = true
		if !inTable[r.Key()] {
			t.Errorf("%s is reachable by a project credential and has no entry in nzRequests.\n\n"+
				"  Add one. Every project-reachable route must be proven to fail closed when the\n"+
				"  credential lacks its scope, is bound elsewhere, or has been revoked — and it\n"+
				"  must be proven with a request that genuinely succeeds when it should.",
				r.Key())
		}
	}
	for key := range inTable {
		if !reachable[key] {
			t.Errorf("%s is exercised by nzRequests but is not a project-reachable route.\n"+
				"  Either it was removed, or its path is a typo and some real route is going\n"+
				"  unexercised behind it.", key)
		}
	}

	if len(nzRequests) == 0 || len(reachable) == 0 {
		t.Fatal("the request table or the registry is empty; every sweep in this file would be vacuous")
	}
}

// TestNegative_EveryCapabilityFamilyIsRepresented — the sweeps below iterate
// nzRequests, so a family whose routes all vanished would stop being tested
// without anything failing.
func TestNegative_EveryCapabilityFamilyIsRepresented(t *testing.T) {
	seen := map[authz.Family]int{}
	for _, r := range nzRequests {
		seen[r.Scope().Family()]++
	}
	for _, f := range authz.Families() {
		if seen[f] == 0 {
			t.Errorf("capability family %q has no request in nzRequests", f)
		}
	}
}

// ─── The positive control ───────────────────────────────────────────────────

// TestNegative_PositiveControlReachesTheProvider.
//
// Every negative assertion in this file is "the workspace was never read and
// the provider was never called". That statement is worthless without proof
// that the same request, with the right credential, DOES read the workspace and
// DOES call the provider — otherwise a route that 404'd, or a body the handler
// rejected, would satisfy every denial check while proving nothing.
//
// This is that proof, route by route.
func TestNegative_PositiveControlReachesTheProvider(t *testing.T) {
	in := newNegativeInstallation(t)

	for _, r := range nzRequests {
		token := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(r.Scope())})

		w := in.send(r.Method, r.URL(nzWorkspace), token, r.Body)
		status, code := statusAndCode(t, w)

		if status >= 400 {
			t.Errorf("%s: an authorized credential got %d %s; the negative cases for this route "+
				"would pass without proving anything", r.Key(), status, code)
			continue
		}
		if in.reachedBackingState() == 0 {
			t.Errorf("%s: authorized, but nothing behind the handler was touched", r.Key())
		}
		if r.ProviderCall == "" {
			continue
		}
		if in.provider.callCount() == 0 {
			t.Errorf("%s: authorized and succeeded, but the identity provider was never called; "+
				"the provider-untouched assertions for this route are vacuous", r.Key())
		}
	}
}

// ─── The scope boundary through the real chain ──────────────────────────────

// TestNegative_MissingScopeStopsBeforeTheWorkspaceIsRead.
//
// The Layer A equivalent proves the decision. This proves the CONSEQUENCE: the
// refusal lands before the resolver reads the workspace row, which is before
// the connection is loaded, before the sealed provider credential is opened,
// and therefore necessarily before any provider traffic.
func TestNegative_MissingScopeStopsBeforeTheWorkspaceIsRead(t *testing.T) {
	in := newNegativeInstallation(t)
	noScopes := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, nil)

	for _, r := range nzRequests {
		w := in.send(r.Method, r.URL(nzWorkspace), noScopes, r.Body)
		status, code := statusAndCode(t, w)

		if status != http.StatusForbidden || code != "insufficient_scope" {
			t.Errorf("%s with no scopes: got %d %s, want 403 insufficient_scope", r.Key(), status, code)
		}
		in.untouched(t, r.Key()+" with no scopes")
	}
}

// TestNegative_WrongFamilyScopeStopsBeforeTheWorkspaceIsRead.
//
// One deliberately wrong scope per route, chosen from another family, through
// the real chain. Layer A sweeps every combination; repeating all of them here
// would multiply the router's cost for a decision that layer already proves.
// What this adds is that the wrong-family denial also stops early.
func TestNegative_WrongFamilyScopeStopsBeforeTheWorkspaceIsRead(t *testing.T) {
	in := newNegativeInstallation(t)

	for _, r := range nzRequests {
		other := otherFamilyScope(r.Scope())
		token := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(other)})

		w := in.send(r.Method, r.URL(nzWorkspace), token, r.Body)
		status, code := statusAndCode(t, w)

		if status != http.StatusForbidden || code != "insufficient_scope" {
			t.Errorf("%s requires %s; a credential holding only %s got %d %s, want 403 insufficient_scope",
				r.Key(), r.Scope(), other, status, code)
		}
		in.untouched(t, r.Key()+" with "+string(other))
	}
}

// otherFamilyScope picks a scope from a different capability family.
func otherFamilyScope(want authz.Scope) authz.Scope {
	for _, s := range authz.AllScopes() {
		if s.Family() != want.Family() {
			return s
		}
	}
	return ""
}

// TestNegative_ReadScopeCannotMutateThroughTheRealChain.
//
// The read/write boundary, asserted where it matters most: a credential that an
// operator handed out as "read only" must not be able to change provider state
// on any route, and the attempt must not reach the provider.
func TestNegative_ReadScopeCannotMutateThroughTheRealChain(t *testing.T) {
	in := newNegativeInstallation(t)

	mutations := nzMutations()
	if len(mutations) == 0 {
		t.Fatal("no mutating routes found; the read/write boundary would be untested")
	}

	for _, r := range mutations {
		readScope, ok := authz.ReadScopeOf(r.Scope())
		if !ok {
			t.Errorf("%s requires %s, whose family has no read scope", r.Key(), r.Scope())
			continue
		}
		token := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(readScope)})

		w := in.send(r.Method, r.URL(nzWorkspace), token, r.Body)
		status, code := statusAndCode(t, w)

		if status != http.StatusForbidden || code != "insufficient_scope" {
			t.Errorf("%s is a mutation requiring %s; a %s credential got %d %s",
				r.Key(), r.Scope(), readScope, status, code)
		}
		in.untouched(t, r.Key()+" with the read scope only")
	}
}

// TestNegative_AuditRequiresItsOwnScope, through the real chain and against the
// real audit handler.
func TestNegative_AuditRequiresItsOwnScope(t *testing.T) {
	in := newNegativeInstallation(t)

	var everythingElse []string
	for _, s := range authz.AllScopes() {
		if s != authz.ScopeAuditRead {
			everythingElse = append(everythingElse, string(s))
		}
	}
	powerful := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, everythingElse)

	w := in.send(http.MethodGet, "/v1/workspaces/"+nzWorkspace+"/audit", powerful, "")
	status, code := statusAndCode(t, w)
	if status != http.StatusForbidden || code != "insufficient_scope" {
		t.Errorf("the audit trail was reached by a credential holding every scope except audit:read: "+
			"got %d %s, want 403 insufficient_scope", status, code)
	}

	// And the other direction: audit:read alone opens nothing else.
	auditOnly := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(authz.ScopeAuditRead)})
	for _, r := range nzRequests {
		if r.Scope() == authz.ScopeAuditRead {
			continue
		}
		resp := in.send(r.Method, r.URL(nzWorkspace), auditOnly, r.Body)
		if s, c := statusAndCode(t, resp); s != http.StatusForbidden || c != "insufficient_scope" {
			t.Errorf("%s was reached by an audit:read-only credential: got %d %s", r.Key(), s, c)
		}
		in.untouched(t, r.Key()+" with audit:read only")
	}
}

// ─── The workspace boundary through the real chain ──────────────────────────

// TestNegative_ForeignWorkspaceStopsBeforeEitherWorkspaceIsRead.
//
// Workspace B is REAL here — it exists, it is active, and it has its own live
// connection. That is what makes this test worth running at this layer: the
// refusal must happen without reading B, so a credential probing another tenant
// cannot tell an existing workspace from an invented one, and cannot cause any
// work to be done in a realm it has no business touching.
func TestNegative_ForeignWorkspaceStopsBeforeEitherWorkspaceIsRead(t *testing.T) {
	in := newNegativeInstallation(t)

	for _, r := range nzRequests {
		token := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(r.Scope())})

		w := in.send(r.Method, r.URL(nzOtherWorkspace), token, r.Body)
		status, code := statusAndCode(t, w)

		if status != http.StatusForbidden || code != "workspace_mismatch" {
			t.Errorf("%s against a foreign workspace: got %d %s, want 403 workspace_mismatch",
				r.Key(), status, code)
		}
		in.untouched(t, r.Key()+" against a foreign workspace")
	}
}

// TestNegative_ForeignWorkspaceDoesNotDiscloseExistence.
//
// A workspace that exists and one that never did must produce byte-identical
// answers apart from the correlation id. Anything else turns the boundary into
// a tenant-enumeration oracle: an attacker with one valid key could map which
// workspace ids are real.
func TestNegative_ForeignWorkspaceDoesNotDiscloseExistence(t *testing.T) {
	in := newNegativeInstallation(t)
	token := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(authz.ScopeUsersRead)})

	const invented = "ws_00000000-0000-4000-8000-000000000000"

	existing := in.send(http.MethodGet, "/v1/workspaces/"+nzOtherWorkspace+"/users", token, "")
	fake := in.send(http.MethodGet, "/v1/workspaces/"+invented+"/users", token, "")

	if existing.Code != fake.Code {
		t.Errorf("status differs: existing workspace %d, invented workspace %d", existing.Code, fake.Code)
	}
	if a, b := nzStripRequestID(t, existing.Body.String()), nzStripRequestID(t, fake.Body.String()); a != b {
		t.Errorf("bodies differ and disclose which workspace exists:\n  existing: %s\n  invented: %s", a, b)
	}
}

// TestNegative_MalformedWorkspaceIDIsRefusedSafely.
//
// Every one of these must be refused, must not panic, and must not echo the
// input back — an error body that repeated an attacker-controlled id would put
// it into every log that records a response.
func TestNegative_MalformedWorkspaceIDIsRefusedSafely(t *testing.T) {
	in := newNegativeInstallation(t)
	token := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(authz.ScopeUsersRead)})

	malformed := []string{
		"ws_not-a-uuid",
		"ws_",
		"not-prefixed",
		"ws_" + strings.Repeat("a", 200),
		"ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301'--",
		"ws_%27%20OR%201%3D1",
		"..",
	}

	for _, id := range malformed {
		w := in.send(http.MethodGet, "/v1/workspaces/"+id+"/users", token, "")
		if w.Code < 400 {
			t.Errorf("workspace id %q: status %d, want a refusal", id, w.Code)
		}
		if w.Code >= 500 {
			t.Errorf("workspace id %q: status %d — a malformed id must not be an internal error", id, w.Code)
		}
		in.untouched(t, "malformed workspace id "+id)

		body := w.Body.String()
		for _, marker := range []string{"'--", "OR%201", strings.Repeat("a", 200)} {
			if strings.Contains(body, marker) {
				t.Errorf("workspace id %q: the response echoes the input (%q)", id, marker)
			}
		}
	}
}

// ─── The authentication boundary ────────────────────────────────────────────

// TestNegative_AuthenticationFailuresNeverReachTheWorkspace sweeps the
// authentication cases against a real route.
//
// All of them must answer identically — one status, one code — because
// distinguishing "malformed", "unknown" and "revoked" would be a
// credential-enumeration oracle. The header assertion pins the RFC 6750 shape
// an SDK branches on.
func TestNegative_AuthenticationFailuresNeverReachTheWorkspace(t *testing.T) {
	in := newNegativeInstallation(t)
	valid := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(authz.ScopeUsersRead)})
	path := "/v1/workspaces/" + nzWorkspace + "/users"

	// The control: this token works.
	if w := in.send(http.MethodGet, path, valid, ""); w.Code != http.StatusOK {
		t.Fatalf("the control credential does not work (%d %s); every case below would pass vacuously",
			w.Code, w.Body.String())
	}

	cases := []struct {
		name          string
		authorization string
	}{
		{"no Authorization header", ""},
		{"empty bearer", "Bearer "},
		{"bearer with whitespace only", "Bearer    "},
		{"no scheme", valid},
		{"wrong scheme", "Basic " + valid},
		{"lowercase scheme", "bearer " + valid},
		{"prefix only", "Bearer lw_sk_"},
		{"truncated token", "Bearer " + valid[:len(valid)-1]},
		{"one character changed", "Bearer " + flipLastChar(valid)},
		{"wrong prefix", "Bearer lw_pk_" + strings.Repeat("a", 16) + "_" + strings.Repeat("b", 52)},
		{"uppercased token", "Bearer " + strings.ToUpper(valid)},
		{"unknown but well-formed", "Bearer lw_sk_" + strings.Repeat("a", 16) + "_" + strings.Repeat("b", 52)},
		{"non-base32 alphabet", "Bearer lw_sk_" + strings.Repeat("1", 16) + "_" + strings.Repeat("0", 52)},
		{"token with an embedded space", "Bearer " + valid[:20] + " " + valid[20:]},
		{"a JWT-shaped value", "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhIn0.c2ln"},
	}

	for _, tc := range cases {
		w := in.sendRaw(http.MethodGet, path, tc.authorization, "")
		status, code := statusAndCode(t, w)

		if status != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", tc.name, status)
		}
		if code != "credential_invalid" {
			t.Errorf("%s: code = %q, want credential_invalid", tc.name, code)
		}
		if h := w.Header().Get("WWW-Authenticate"); !strings.Contains(h, "invalid_token") {
			t.Errorf("%s: WWW-Authenticate = %q, want it to carry invalid_token", tc.name, h)
		}
		in.untouched(t, tc.name)
	}
}

// TestNegative_SurroundingWhitespaceOnTheTokenIsTolerated records a deliberate
// leniency rather than leaving it to be rediscovered as a suspected bug.
//
// extractBearer trims the value after the scheme, so `Bearer <token>\t` still
// authenticates. That is correct and intended: RFC 7230 already allows optional
// whitespace around a header's field value, so a client that appends one is not
// presenting a different credential — the bytes that matter are identical. The
// token itself is still matched exactly, which the embedded-space case above
// proves.
//
// Pinned here so that tightening it later is a decision someone makes on
// purpose, and so nobody reads the sweep above and concludes the parser is
// stricter than it is.
func TestNegative_SurroundingWhitespaceOnTheTokenIsTolerated(t *testing.T) {
	in := newNegativeInstallation(t)
	valid := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(authz.ScopeUsersRead)})
	path := "/v1/workspaces/" + nzWorkspace + "/users"

	for _, header := range []string{
		"Bearer " + valid + "\t",
		"Bearer " + valid + " ",
		"Bearer  " + valid,
	} {
		if w := in.sendRaw(http.MethodGet, path, header, ""); w.Code != http.StatusOK {
			t.Errorf("%q: status = %d, want 200 — surrounding whitespace is trimmed by design",
				header[:20]+"…", w.Code)
		}
	}
}

func flipLastChar(s string) string {
	if s == "" {
		return s
	}
	last := s[len(s)-1]
	replacement := byte('a')
	if last == 'a' {
		replacement = 'b'
	}
	return s[:len(s)-1] + string(replacement)
}

// TestNegative_CredentialIdAndSecretCannotBeRecombined.
//
// The token is `lw_sk_<lookup>_<secret>`, and the lookup half is stored in
// clear. That is safe only if the two halves cannot be mixed: a leaked lookup
// (from a database dump, a support ticket, a log of key prefixes) must not
// become usable when paired with any other credential's secret.
func TestNegative_CredentialIdAndSecretCannotBeRecombined(t *testing.T) {
	in := newNegativeInstallation(t)
	a := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(authz.ScopeUsersRead)})
	b := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(authz.ScopeUsersRead)})
	path := "/v1/workspaces/" + nzWorkspace + "/users"

	lookupA, secretA := splitToken(t, a)
	lookupB, secretB := splitToken(t, b)

	for _, tc := range []struct {
		name  string
		token string
	}{
		{"lookup A with secret B", "lw_sk_" + lookupA + "_" + secretB},
		{"lookup B with secret A", "lw_sk_" + lookupB + "_" + secretA},
	} {
		w := in.send(http.MethodGet, path, tc.token, "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401 — the two halves of a credential are not bound",
				tc.name, w.Code)
		}
		in.untouched(t, tc.name)
	}
}

func splitToken(t *testing.T, token string) (lookup, secret string) {
	t.Helper()
	const prefix = "lw_sk_"
	if !strings.HasPrefix(token, prefix) {
		t.Fatalf("token %q does not carry the expected prefix", token[:6])
	}
	lookup, secret, found := strings.Cut(token[len(prefix):], "_")
	if !found {
		t.Fatal("token has no separator")
	}
	return lookup, secret
}

// TestNegative_SecretComparisonIsConstantTime is a code-level assertion, and
// deliberately not a timing measurement.
//
// A network timing test would be measuring the scheduler, the HTTP stack and
// the test machine's neighbours; it would be flaky when it worked and would
// prove nothing when it passed. The implementation uses
// subtle.ConstantTimeCompare and burns the same work against a fixed digest
// when the lookup matches nothing — both of which are properties of the source.
// What CAN be asserted from outside is the observable consequence: a known
// lookup with a wrong secret and an unknown lookup are indistinguishable.
func TestNegative_UnknownLookupAndWrongSecretAreIndistinguishable(t *testing.T) {
	in := newNegativeInstallation(t)
	valid := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(authz.ScopeUsersRead)})
	path := "/v1/workspaces/" + nzWorkspace + "/users"

	lookup, secret := splitToken(t, valid)

	knownLookupWrongSecret := "lw_sk_" + lookup + "_" + flipLastChar(secret)
	unknownLookup := "lw_sk_" + flipLastChar(lookup) + "_" + secret

	a := in.send(http.MethodGet, path, knownLookupWrongSecret, "")
	b := in.send(http.MethodGet, path, unknownLookup, "")

	if a.Code != b.Code {
		t.Errorf("status differs: known lookup %d, unknown lookup %d", a.Code, b.Code)
	}
	if x, y := nzStripRequestID(t, a.Body.String()), nzStripRequestID(t, b.Body.String()); x != y {
		t.Errorf("bodies differ and disclose whether a key prefix exists:\n  known:   %s\n  unknown: %s", x, y)
	}
}

// ─── Credential and parent lifecycle ────────────────────────────────────────

// TestNegative_RevocationTakesEffectOnTheNextRequest.
//
// No restart, no cache to wait out. The credential works, an operator revokes
// it, and the very next request is refused — and refused before the workspace
// is read, so a revoked key cannot cause even a database read on the tenant it
// used to serve.
func TestNegative_RevocationTakesEffectOnTheNextRequest(t *testing.T) {
	in := newNegativeInstallation(t)
	token := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(authz.ScopeUsersRead)})
	path := "/v1/workspaces/" + nzWorkspace + "/users"

	if w := in.send(http.MethodGet, path, token, ""); w.Code != http.StatusOK {
		t.Fatalf("before revocation: status = %d, want 200", w.Code)
	}

	now := timeNowUTC()
	in.credentials.mutate(t, token, func(e *nzCredential) { e.cred.RevokedAt = &now })

	w := in.send(http.MethodGet, path, token, "")
	if status, code := statusAndCode(t, w); status != http.StatusUnauthorized || code != "credential_invalid" {
		t.Errorf("after revocation: got %d %s, want 401 credential_invalid", status, code)
	}
	in.untouched(t, "a revoked credential")
}

// TestNegative_ExpiryTakesEffectOnTheNextRequest — the same property for the
// other way a credential stops being usable, and it must be indistinguishable
// from revocation from outside.
func TestNegative_ExpiryTakesEffectOnTheNextRequest(t *testing.T) {
	in := newNegativeInstallation(t)
	token := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(authz.ScopeUsersRead)})
	path := "/v1/workspaces/" + nzWorkspace + "/users"

	if w := in.send(http.MethodGet, path, token, ""); w.Code != http.StatusOK {
		t.Fatalf("before expiry: status = %d, want 200", w.Code)
	}

	past := timeNowUTC().Add(-time.Second)
	in.credentials.mutate(t, token, func(e *nzCredential) { e.cred.ExpiresAt = &past })

	w := in.send(http.MethodGet, path, token, "")
	if status, code := statusAndCode(t, w); status != http.StatusUnauthorized || code != "credential_invalid" {
		t.Errorf("after expiry: got %d %s, want 401 credential_invalid", status, code)
	}
	in.untouched(t, "an expired credential")
}

// TestNegative_ArchivingTheProjectStopsEveryCredential.
//
// The lifecycle question the slice asks: is credential status alone sufficient,
// or does an inactive PARENT also stop authorization? It must be the second.
// Archiving a project is the operator's kill switch for a whole integration,
// and a credential that kept working because nobody walked its rows would make
// that switch a lie.
//
// Two credentials, so the test proves the parent stops ALL of them rather than
// the one that happened to be checked.
func TestNegative_ArchivingTheProjectStopsEveryCredential(t *testing.T) {
	in := newNegativeInstallation(t)
	first := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(authz.ScopeUsersRead)})
	second := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(authz.ScopeUsersRead)})
	path := "/v1/workspaces/" + nzWorkspace + "/users"

	for _, token := range []string{first, second} {
		if w := in.send(http.MethodGet, path, token, ""); w.Code != http.StatusOK {
			t.Fatalf("before archiving: status = %d, want 200", w.Code)
		}
	}

	in.credentials.mutate(t, first, func(e *nzCredential) { e.proj.Status = project.StatusArchived })
	in.credentials.mutate(t, second, func(e *nzCredential) { e.proj.Status = project.StatusArchived })

	for _, token := range []string{first, second} {
		w := in.send(http.MethodGet, path, token, "")
		if status, code := statusAndCode(t, w); status != http.StatusUnauthorized || code != "credential_invalid" {
			t.Errorf("after archiving the project: got %d %s, want 401 credential_invalid", status, code)
		}
		in.untouched(t, "a credential whose project is archived")
	}
}

// TestNegative_ArchivingTheWorkspaceStopsIdentityOperations.
//
// Archiving is a boundary, not a display state. The credential is still valid —
// it authenticates, and it is bound to this workspace — so the refusal comes
// from the resolver rather than from authorization, which is why this asserts
// 409 workspace_archived rather than a 403.
//
// The provider assertion is the security half: whatever the status code says,
// no traffic may reach the realm behind an archived workspace. And it must take
// effect with no restart, which the provider cache makes a real question rather
// than a formality.
func TestNegative_ArchivingTheWorkspaceStopsIdentityOperations(t *testing.T) {
	in := newNegativeInstallation(t)
	token := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID,
		[]string{string(authz.ScopeUsersRead), string(authz.ScopeUsersWrite)})
	path := "/v1/workspaces/" + nzWorkspace + "/users"

	if w := in.send(http.MethodGet, path, token, ""); w.Code != http.StatusOK {
		t.Fatalf("before archiving: status = %d, want 200", w.Code)
	}
	// The provider is now in the resolver's cache, which is precisely the state
	// in which a stale positive decision could survive an archive.
	if in.provider.callCount() == 0 {
		t.Fatal("the control request never reached the provider")
	}

	in.stores.archiveWorkspace(nzWorkspaceUUID)
	t.Cleanup(func() { in.stores.unarchiveWorkspace(nzWorkspaceUUID) })

	for _, r := range []nzRequest{
		{Method: "GET", Path: nzWS + "/users"},
		{Method: "POST", Path: nzWS + "/users",
			Body: `{"email":"new@example.test","temporary_password":"lw-negative-matrix-pw"}`},
	} {
		w := in.send(r.Method, r.URL(nzWorkspace), token, r.Body)
		status, code := statusAndCode(t, w)
		if status != http.StatusConflict || code != "workspace_archived" {
			t.Errorf("%s through an archived workspace: got %d %s, want 409 workspace_archived",
				r.Key(), status, code)
		}
		if in.provider.callCount() != 0 {
			t.Errorf("%s through an archived workspace reached the identity provider %d time(s)",
				r.Key(), in.provider.callCount())
		}
	}
}

// TestNegative_NoActiveConnectionDoesNotFallBackAnywhere.
//
// The highest-value resolver negative. A workspace with no active connection
// must fail deterministically — it must NOT quietly fall back to the process's
// legacy Keycloak configuration, to a retired connection, or to another
// workspace's connection.
//
// The last of those is what this test is really shaped around: workspace B is
// live and connected throughout, so an implementation that resolved "some
// active connection" instead of "THIS workspace's active connection" would pass
// a test that only checked for an error, and fails this one.
func TestNegative_NoActiveConnectionDoesNotFallBackAnywhere(t *testing.T) {
	in := newNegativeInstallation(t)
	token := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID,
		[]string{string(authz.ScopeUsersRead), string(authz.ScopeUsersWrite)})
	path := "/v1/workspaces/" + nzWorkspace + "/users"

	if w := in.send(http.MethodGet, path, token, ""); w.Code != http.StatusOK {
		t.Fatalf("before retiring the connection: status = %d, want 200", w.Code)
	}

	in.stores.retireConnection(nzWorkspaceUUID)
	t.Cleanup(func() { in.stores.restoreConnection(nzWorkspaceUUID, in.connA) })

	w := in.send(http.MethodGet, path, token, "")
	status, code := statusAndCode(t, w)
	if status != http.StatusConflict || code != "workspace_connection_missing" {
		t.Errorf("with no active connection: got %d %s, want 409 workspace_connection_missing",
			status, code)
	}
	if in.provider.callCount() != 0 {
		t.Errorf("a workspace with no active connection still reached a provider %d time(s) — "+
			"something fell back to another connection", in.provider.callCount())
	}

	// Workspace B is still connected. Proving it here is what makes the
	// assertion above mean "no fallback" rather than "nothing was configured".
	otherToken := in.credentials.mint(t, "cccccccc-0000-4000-8000-000000000003",
		nzOtherWorkspaceUUID, []string{string(authz.ScopeUsersRead)})
	if w := in.send(http.MethodGet, "/v1/workspaces/"+nzOtherWorkspace+"/users", otherToken, ""); w.Code != http.StatusOK {
		t.Fatalf("workspace B should still resolve; got %d. The no-fallback assertion above is "+
			"weakened if nothing was reachable at all", w.Code)
	}
}

// ─── The cross-realm resource-id attack ─────────────────────────────────────

// TestNegative_AResourceIdFromAnotherRealmCannotEscapeItsWorkspace.
//
// The attack this is shaped around is subtle and would survive every test
// above. The caller is FULLY authorized: it holds a valid credential, bound to
// workspace B, carrying exactly the scope the route requires, and it addresses
// workspace B in the path. Every authorization check passes, correctly.
//
// What it then supplies is a resource identifier belonging to workspace A's
// realm. An implementation that authorized the PATH and then acted on the
// IDENTIFIER — because the identifier is globally addressable in the provider's
// admin API — would perform the operation in the wrong tenant while looking
// perfectly authorized from the outside.
//
// The property that prevents it is structural rather than a check: the request
// is routed to workspace B's connection, so the identifier is resolved inside
// realm B, where it does not exist. The evidence is therefore twofold —
//
//	the answer is "not found"
//	the call went to realm B and NOT to realm A
//
// and the second half is what an assertion on the status code alone would miss.
func TestNegative_AResourceIdFromAnotherRealmCannotEscapeItsWorkspace(t *testing.T) {
	in := newNegativeInstallation(t)

	const projectInB = "cccccccc-0000-4000-8000-000000000003"
	tokenB := in.credentials.mint(t, projectInB, nzOtherWorkspaceUUID, authz.ScopeStrings(authz.AllScopes()))

	// The control: workspace B's OWN user is reachable through workspace B.
	if w := in.send(http.MethodGet,
		"/v1/workspaces/"+nzOtherWorkspace+"/users/"+nzUserInRealmB, tokenB, ""); w.Code != http.StatusOK {
		t.Fatalf("workspace B cannot read its own user (%d); the cross-realm assertions would "+
			"pass for the wrong reason", w.Code)
	}

	// Every route that takes a user id, addressed with realm A's user id
	// through workspace B.
	attacks := []nzRequest{
		{Method: "GET", Path: nzWS + "/users/:user_id"},
		{Method: "PATCH", Path: nzWS + "/users/:user_id", Body: `{"first_name":"Taken"}`},
		{Method: "DELETE", Path: nzWS + "/users/:user_id"},
		{Method: "POST", Path: nzWS + "/users/:user_id/reset-password"},
		{Method: "GET", Path: nzWS + "/users/:user_id/roles"},
		{Method: "POST", Path: nzWS + "/users/:user_id/roles", Body: `{"roles":["` + nzRoleName + `"]}`},
		{Method: "GET", Path: nzWS + "/users/:user_id/sessions"},
		{Method: "DELETE", Path: nzWS + "/users/:user_id/sessions"},
		{Method: "POST", Path: nzWS + "/invitations/:invitation_id/resend"},
	}

	for _, r := range attacks {
		// r.URL substitutes nzUserID, which is realm A's user, into workspace
		// B's path. That is exactly the attack.
		w := in.send(r.Method, r.URL(nzOtherWorkspace), tokenB, r.Body)

		if w.Code < 400 {
			t.Errorf("%s: workspace B accepted realm A's user id and answered %d — the resource "+
				"boundary was crossed", r.Key(), w.Code)
			continue
		}

		for _, call := range in.provider.calls() {
			if strings.HasPrefix(call, "realm-a:") {
				t.Errorf("%s: a request through workspace B reached realm A (%s)", r.Key(), call)
			}
		}
	}
}

// TestNegative_ACrossRealmIdIsIndistinguishableFromAnInventedOne.
//
// The resource-enumeration question. Asking workspace B about a user that
// exists in realm A must look exactly like asking it about a user that exists
// nowhere — otherwise one valid credential in any workspace becomes an oracle
// for "does this user id exist somewhere in this installation", which is a
// cross-tenant disclosure even though no data crosses.
//
// This asserts the codes match rather than demanding byte-identical bodies:
// the product promises a stable machine-readable code, not identical prose, and
// over-asserting would make the test fail on a reworded message that disclosed
// nothing.
func TestNegative_ACrossRealmIdIsIndistinguishableFromAnInventedOne(t *testing.T) {
	in := newNegativeInstallation(t)

	const projectInB = "cccccccc-0000-4000-8000-000000000003"
	tokenB := in.credentials.mint(t, projectInB, nzOtherWorkspaceUUID, []string{string(authz.ScopeUsersRead)})
	const invented = "00000000-1111-4000-8000-999999999999"

	fromRealmA := in.send(http.MethodGet, "/v1/workspaces/"+nzOtherWorkspace+"/users/"+nzUserID, tokenB, "")
	nowhere := in.send(http.MethodGet, "/v1/workspaces/"+nzOtherWorkspace+"/users/"+invented, tokenB, "")

	statusA, codeA := statusAndCode(t, fromRealmA)
	statusB, codeB := statusAndCode(t, nowhere)

	if statusA != statusB || codeA != codeB {
		t.Errorf("a user id that exists in another realm is distinguishable from one that exists "+
			"nowhere: %d %s vs %d %s", statusA, codeA, statusB, codeB)
	}
	if statusA != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a resource outside the addressed workspace's realm", statusA)
	}
}

// ─── Cross-project isolation inside one workspace ───────────────────────────

// TestNegative_TwoProjectsInOneWorkspaceKeepSeparateCapabilities.
//
// Authorization must derive from the CREDENTIAL's own project and scopes, not
// from the workspace it happens to share. Two projects in one workspace, with
// disjoint scopes: neither may borrow the other's.
//
// This is the case an implementation gets wrong by caching "what may this
// workspace do" instead of "what may this credential do", and it would pass
// every single-project test in this file.
func TestNegative_TwoProjectsInOneWorkspaceKeepSeparateCapabilities(t *testing.T) {
	in := newNegativeInstallation(t)

	const (
		projectOne = "11111111-0000-4000-8000-000000000001"
		projectTwo = "22222222-0000-4000-8000-000000000002"
	)
	readerToken := in.credentials.mint(t, projectOne, nzWorkspaceUUID, []string{string(authz.ScopeUsersRead)})
	roleToken := in.credentials.mint(t, projectTwo, nzWorkspaceUUID, []string{string(authz.ScopeRolesWrite)})

	users := "/v1/workspaces/" + nzWorkspace + "/users"
	roles := "/v1/workspaces/" + nzWorkspace + "/roles"
	roleBody := `{"name":"cross-project-role"}`

	// Each does its own job.
	if w := in.send(http.MethodGet, users, readerToken, ""); w.Code != http.StatusOK {
		t.Fatalf("the users:read credential cannot read users (%d); the isolation check would be vacuous", w.Code)
	}
	if w := in.send(http.MethodPost, roles, roleToken, roleBody); w.Code >= 400 {
		t.Fatalf("the roles:write credential cannot create a role (%d); the isolation check would be vacuous", w.Code)
	}

	// Neither acquires the other's.
	w := in.send(http.MethodPost, roles, readerToken, roleBody)
	if status, code := statusAndCode(t, w); status != http.StatusForbidden || code != "insufficient_scope" {
		t.Errorf("project one's users:read credential created a role in the shared workspace: got %d %s",
			status, code)
	}
	in.untouched(t, "project one reaching project two's capability")

	w = in.send(http.MethodGet, users, roleToken, "")
	if status, code := statusAndCode(t, w); status != http.StatusForbidden || code != "insufficient_scope" {
		t.Errorf("project two's roles:write credential read users in the shared workspace: got %d %s",
			status, code)
	}
	in.untouched(t, "project two reaching project one's capability")
}

// TestNegative_RevokingOneCredentialLeavesItsSiblingWorking.
//
// Revocation is per credential, not per project. An operator revoking a leaked
// key must not take down the project's other deployments — and, in the other
// direction, a revoked key must not keep working because a sibling is still
// live.
func TestNegative_RevokingOneCredentialLeavesItsSiblingWorking(t *testing.T) {
	in := newNegativeInstallation(t)
	doomed := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(authz.ScopeUsersRead)})
	sibling := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(authz.ScopeUsersRead)})
	path := "/v1/workspaces/" + nzWorkspace + "/users"

	now := timeNowUTC()
	in.credentials.mutate(t, doomed, func(e *nzCredential) { e.cred.RevokedAt = &now })

	if w := in.send(http.MethodGet, path, doomed, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("the revoked credential: status = %d, want 401", w.Code)
	}
	if w := in.send(http.MethodGet, path, sibling, ""); w.Code != http.StatusOK {
		t.Errorf("the sibling credential: status = %d, want 200 — revocation is per credential", w.Code)
	}
}

// ─── State integrity on rejection ───────────────────────────────────────────

// TestNegative_RejectedMutationsEmitNoSuccessEvent.
//
// Non-negotiable, and swept over every mutating route rather than sampled: a
// refused request must never produce `user.created`, `role.deleted`,
// `session.revoked` or any other successful domain event. An audit trail that
// records mutations that did not happen is worse than none — it would send an
// incident responder chasing a change nobody made.
//
// Four refusal kinds per route, because they are refused at four different
// points in the chain and only the audit recorder can tell they all stopped
// short of emitting.
func TestNegative_RejectedMutationsEmitNoSuccessEvent(t *testing.T) {
	in := newNegativeInstallation(t)
	rec := captureAudit(t)

	noScopes := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, nil)

	for _, r := range nzMutations() {
		readScope, _ := authz.ReadScopeOf(r.Scope())
		readOnly := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(readScope)})
		rightScope := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(r.Scope())})

		refusals := []struct {
			why       string
			token     string
			workspace string
		}{
			{"no scopes", noScopes, nzWorkspace},
			{"read scope only", readOnly, nzWorkspace},
			{"foreign workspace", rightScope, nzOtherWorkspace},
			{"unknown credential", "lw_sk_" + strings.Repeat("a", 16) + "_" + strings.Repeat("b", 52), nzWorkspace},
		}

		for _, refusal := range refusals {
			rec.reset()
			w := in.send(r.Method, r.URL(refusal.workspace), refusal.token, r.Body)

			if w.Code < 400 {
				t.Errorf("%s (%s): status %d — this was supposed to be refused", r.Key(), refusal.why, w.Code)
				continue
			}
			// Zero events, not "no SUCCESS event". A refusal at this layer never
			// reaches a handler, so there is nothing to record — and a failed
			// mutation is distinguished from a successful one only by a Reason
			// string, which makes "emitted nothing" the assertion that cannot be
			// satisfied by an event that merely looks unsuccessful.
			for _, e := range rec.snapshot() {
				outcome := "SUCCESS"
				if e.Reason != "" {
					outcome = "failure: " + e.Reason
				}
				t.Errorf("%s refused for %q still emitted the audit event %q (%s)",
					r.Key(), refusal.why, e.Action, outcome)
			}
		}
	}
}

// TestNegative_AnAuthorizedMutationDoesEmitItsEvent is the control for the
// test above.
//
// Without it, an audit pipeline that emitted nothing at all — a broken
// recorder, a handler that stopped recording — would make every "no event on
// rejection" assertion pass while the trail was empty.
func TestNegative_AnAuthorizedMutationDoesEmitItsEvent(t *testing.T) {
	in := newNegativeInstallation(t)
	rec := captureAudit(t)

	token := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(authz.ScopeUsersWrite)})

	rec.reset()
	w := in.send(http.MethodPost, "/v1/workspaces/"+nzWorkspace+"/users", token,
		`{"email":"audited@example.test","first_name":"Aud","last_name":"Ited","temporary_password":"lw-negative-matrix-pw"}`)
	if w.Code >= 400 {
		t.Fatalf("the authorized mutation failed (%d %s); the control proves nothing", w.Code, w.Body.String())
	}
	if len(rec.snapshot()) == 0 {
		t.Fatal("an authorized, successful mutation emitted no audit event; every " +
			"no-event-on-refusal assertion in this package is vacuous")
	}
}

// ─── The error contract on refusal ──────────────────────────────────────────

// TestNegative_EveryRefusalCarriesACorrelationId.
//
// The SDK's typed errors are keyed by request id and the support conversation
// that follows a 403 is keyed by it too. It must survive every refusal kind,
// including the ones written by middleware that runs before any handler, and
// the header and the body must agree — an id in one but not the other looks
// correlatable and is not.
func TestNegative_EveryRefusalCarriesACorrelationId(t *testing.T) {
	in := newNegativeInstallation(t)
	valid := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(authz.ScopeUsersRead)})
	noScopes := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, nil)
	users := "/v1/workspaces/" + nzWorkspace + "/users"

	cases := []struct {
		name       string
		method     string
		path       string
		token      string
		body       string
		wantStatus int
		wantCode   string
	}{
		{"401 unauthenticated", http.MethodGet, users, "", "", http.StatusUnauthorized, "credential_invalid"},
		{"403 insufficient_scope", http.MethodGet, users, noScopes, "", http.StatusForbidden, "insufficient_scope"},
		{"403 workspace_mismatch", http.MethodGet, "/v1/workspaces/" + nzOtherWorkspace + "/users", valid, "",
			http.StatusForbidden, "workspace_mismatch"},
		{"403 operator_only", http.MethodGet, "/v1/workspaces", valid, "", http.StatusForbidden, "operator_only"},
		{"403 on a malformed workspace id", http.MethodGet, "/v1/workspaces/ws_nope/users", valid, "",
			http.StatusForbidden, "workspace_mismatch"},
	}

	for _, tc := range cases {
		w := in.send(tc.method, tc.path, tc.token, tc.body)
		status, code := statusAndCode(t, w)
		if status != tc.wantStatus || code != tc.wantCode {
			t.Errorf("%s: got %d %s, want %d %s", tc.name, status, code, tc.wantStatus, tc.wantCode)
		}

		bodyID := nzEnvelopeField(t, w.Body.String(), "request_id")
		headerID := w.Header().Get("X-Request-Id")
		switch {
		case bodyID == "":
			t.Errorf("%s: no request_id in the envelope", tc.name)
		case headerID == "":
			t.Errorf("%s: no X-Request-Id header", tc.name)
		case bodyID != headerID:
			t.Errorf("%s: X-Request-Id %q != envelope request_id %q", tc.name, headerID, bodyID)
		}
	}
}

// TestNegative_RefusalsCarryNoField — an authorization refusal is not about a
// request field, and inventing one would send a client editing a request that
// was fine.
func TestNegative_RefusalsCarryNoField(t *testing.T) {
	in := newNegativeInstallation(t)
	noScopes := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, nil)

	for _, r := range nzRequests {
		w := in.send(r.Method, r.URL(nzWorkspace), noScopes, r.Body)
		if field := nzEnvelopeField(t, w.Body.String(), "field"); field != "" {
			t.Errorf("%s: an insufficient_scope refusal carried error.field=%q", r.Key(), field)
		}
	}
}

// TestNegative_RefusalsNeverEchoTheCredential.
//
// A 401 or 403 body that quoted the presented token would put it into every
// proxy log, error tracker and support ticket on the way back — the one place a
// credential must never reach.
func TestNegative_RefusalsNeverEchoTheCredential(t *testing.T) {
	in := newNegativeInstallation(t)
	token := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, nil)
	lookup, _ := splitToken(t, token)

	probes := []struct {
		name  string
		token string
		path  string
	}{
		{"insufficient scope", token, "/v1/workspaces/" + nzWorkspace + "/users"},
		{"workspace mismatch", token, "/v1/workspaces/" + nzOtherWorkspace + "/users"},
		{"operator only", token, "/v1/workspaces"},
		{"bad credential", token + "x", "/v1/workspaces/" + nzWorkspace + "/users"},
	}

	for _, p := range probes {
		w := in.send(http.MethodGet, p.path, p.token, "")
		body := w.Body.String()

		if strings.Contains(body, p.token) {
			t.Errorf("%s: the response body echoes the presented credential", p.name)
		}
		if strings.Contains(body, lookup) {
			t.Errorf("%s: the response body echoes the credential's lookup segment", p.name)
		}
		for name, values := range w.Header() {
			for _, v := range values {
				if strings.Contains(v, p.token) || strings.Contains(v, lookup) {
					t.Errorf("%s: header %s echoes credential material", p.name, name)
				}
			}
		}
	}
}

// ─── The control plane, through the real chain ──────────────────────────────

// TestNegative_EveryOperatorOnlyRouteRefusesTheFullVocabulary.
//
// Mechanically, over every operator-only route the registry declares, with a
// credential holding every scope that exists, inside its own workspace.
//
// A route that is not mounted in this installation would answer 404 and pass a
// naive assertion, so 404 is treated as a failure of the test's own setup
// rather than as a pass.
func TestNegative_EveryOperatorOnlyRouteRefusesTheFullVocabulary(t *testing.T) {
	in := newNegativeInstallation(t)
	token := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, authz.ScopeStrings(authz.AllScopes()))

	routes := authz.OperatorOnlyRoutes()
	if len(routes) == 0 {
		t.Fatal("no operator-only routes; the control-plane sweep would be vacuous")
	}

	for _, r := range routes {
		url := strings.NewReplacer(
			":workspace_id", nzWorkspace,
			":user_id", nzUserID,
			":connection_id", "conn_"+publicidSample,
			":project_id", "prj_"+publicidSample,
			":credential_id", "key_"+publicidSample,
		).Replace(r.Path)

		w := in.send(r.Method, url, token, "{}")
		if w.Code == http.StatusNotFound {
			t.Errorf("%s answered 404 — it is not mounted in this installation, so the "+
				"operator-only assertion for it is vacuous", r.Key())
			continue
		}
		status, code := statusAndCode(t, w)
		if status != http.StatusForbidden || code != "operator_only" {
			t.Errorf("%s with every scope: got %d %s, want 403 operator_only", r.Key(), status, code)
		}
		in.untouched(t, r.Key()+" from a project credential")
	}
}

const publicidSample = "7c9e6679-7425-40de-944b-e07fc1f90ae7"

// TestNegative_DirectPasswordSetIsUnreachableByAnyKey states the most sensitive
// single classification on its own.
//
// PUT .../password sets a credential with no email and no consent — a complete
// account-takeover primitive. It is operator-only, and this pins that no
// combination of scopes reaches it, including users:write, which is the one a
// developer would assume covers it.
func TestNegative_DirectPasswordSetIsUnreachableByAnyKey(t *testing.T) {
	in := newNegativeInstallation(t)
	path := "/v1/workspaces/" + nzWorkspace + "/users/" + nzUserID + "/password"
	const sentinel = "lw-sentinel-password-must-not-be-set"

	for _, scopes := range [][]string{
		nil,
		{string(authz.ScopeUsersWrite)},
		{string(authz.ScopeUsersRead), string(authz.ScopeUsersWrite)},
		authz.ScopeStrings(authz.AllScopes()),
	} {
		token := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, scopes)
		w := in.send(http.MethodPut, path, token, `{"password":"`+sentinel+`","temporary":false}`)

		status, code := statusAndCode(t, w)
		if status != http.StatusForbidden || code != "operator_only" {
			t.Errorf("PUT .../password with scopes %v: got %d %s, want 403 operator_only", scopes, status, code)
		}
		in.untouched(t, "PUT .../password with scopes")

		if strings.Contains(w.Body.String(), sentinel) {
			t.Error("the refusal echoed the password back to the caller")
		}
	}
}

// ─── Rate-limit ordering ────────────────────────────────────────────────────

// TestNegative_InvalidCredentialsDoNotDrainAValidCredentialsBudget.
//
// The architectural question the slice poses: can an unauthenticated attacker
// exhaust a valid Project's rate-limit budget by presenting its identifiers?
//
// It cannot, and the reason is the middleware order —
//
//	AuthenticatePrincipal  →  RateLimitPerCredential  →  Authorize
//
// The per-credential bucket is keyed by the credential id, which only exists
// once authentication has SUCCEEDED. A request that fails authentication is
// refused before that middleware runs, so it can never charge a bucket
// belonging to a credential it did not prove it holds.
//
// What such an attacker CAN exhaust is the per-IP edge bucket, which is that
// limiter's job and is metered separately.
func TestNegative_InvalidCredentialsDoNotDrainAValidCredentialsBudget(t *testing.T) {
	in := newNegativeInstallationWithLimits(t, RateLimitSettings{EdgeRPS: 5000, CredentialRPS: 5})
	valid := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, []string{string(authz.ScopeUsersRead)})
	path := "/v1/workspaces/" + nzWorkspace + "/users"

	lookup, secret := splitToken(t, valid)
	// The attacker knows the credential's public half — from a database dump, a
	// log of key prefixes, a support ticket — and not its secret.
	guess := "lw_sk_" + lookup + "_" + flipLastChar(secret)

	for i := 0; i < 60; i++ {
		if w := in.send(http.MethodGet, path, guess, ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d: status = %d, want 401", i, w.Code)
		}
	}

	if w := in.send(http.MethodGet, path, valid, ""); w.Code != http.StatusOK {
		t.Errorf("after 60 failed authentications against its key prefix, the real credential "+
			"got %d — an unauthenticated attacker can exhaust a valid project's budget", w.Code)
	}
}

// TestNegative_InsufficientScopeConsumesTheCredentialBudget.
//
// The other half of the ordering, and the answer is the opposite one: a request
// that DID authenticate is metered, even when it is then refused for scope.
//
// This is deliberate rather than accidental. The caller is a known,
// attributable, revocable machine, and a refused request still costs a
// credential lookup and a hash; leaving it unmetered would make
// "spray every endpoint until one answers" the one traffic pattern with no
// ceiling. It is asserted here so that reordering the middleware to make
// denials free shows up as a failing test rather than as a quiet change.
func TestNegative_InsufficientScopeConsumesTheCredentialBudget(t *testing.T) {
	in := newNegativeInstallationWithLimits(t, RateLimitSettings{EdgeRPS: 5000, CredentialRPS: 5})
	token := in.credentials.mint(t, nzProjectUUID, nzWorkspaceUUID, nil)
	path := "/v1/workspaces/" + nzWorkspace + "/users"

	sawThrottle := false
	for i := 0; i < 40; i++ {
		w := in.send(http.MethodGet, path, token, "")
		if w.Code == http.StatusTooManyRequests {
			sawThrottle = true
			break
		}
		if w.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: status = %d, want 403 or 429", i, w.Code)
		}
	}

	if !sawThrottle {
		t.Error("40 insufficient_scope requests were never throttled; an authenticated credential " +
			"can spray refused requests without consuming its own budget")
	}
}
