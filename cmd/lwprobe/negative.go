package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Layer C of the Slice 14 negative-authorization matrix: the real stack.
//
// Everything here runs against a real LIGHTWEIGHT process, a real PostgreSQL
// and a real Keycloak 26 with several real realms, and it runs from OUTSIDE the
// module — this program imports nothing from it, and holds nothing but base
// URLs, workspace ids and credentials.
//
// ─── What this layer is for, and what it is not for ─────────────────────────
//
// It is NOT for re-proving the scope matrix. Layers A and B already sweep every
// route against every scope in milliseconds, and repeating that here would buy
// twenty minutes of CI for evidence that is strictly weaker per case.
//
// It is for the properties a mock cannot have:
//
//	a rejected mutation left the REALM unchanged
//	a resource id from realm A cannot act through realm B
//	an archived workspace stops a credential that was working a second ago
//	a workspace with no connection does not silently use somebody else's
//	the caller being forbidden and the SERVICE ACCOUNT being forbidden
//	  are different answers, from a real Keycloak that really refused
//	revocation lands on requests already in flight from several goroutines
//
// Each of those is a claim about the whole system. None of them can be made
// honestly against a fake provider, because the fake is the part that would
// have to be wrong for the claim to fail.
//
// ─── Two phases ─────────────────────────────────────────────────────────────
//
//	warm    prove the credentials that are about to be cut off currently WORK,
//	        and leave the server's provider cache populated for them. Without
//	        this, "archiving stopped it" is indistinguishable from "it never
//	        worked", and the cache — the thing most likely to keep a revoked
//	        decision alive — would never have been primed.
//
//	matrix  the operator has since archived a workspace, retired a connection
//	        and revoked a credential. Everything must now fail closed, with no
//	        restart and no cache flush.
//
// scripts/negative-authz-e2e.sh performs the operator actions between them.

func runNegative(cfg *Config) int {
	k := &checker{}

	fmt.Printf("\033[1mlwprobe — negative authorization matrix (real stack)\033[0m\n")
	fmt.Printf("  url        %s\n", cfg.URL)
	fmt.Printf("  workspace  %s\n", cfg.WorkspaceID)
	fmt.Printf("  phase      %s\n", cfg.NegativePhase)

	// Deliberately NO key prefix, unlike the contract mode's header.
	//
	// This suite's output is scanned by scripts/scan-artifacts.sh, which
	// searches for the SHAPE `lw_sk_…` as well as for recorded values — and
	// rightly so, since a shape rule is what catches a secret that took a path
	// nobody registered. Printing a truncated prefix would trip it, and the
	// fix that suggests itself (loosen the rule) is the wrong one: a
	// security-test tool that writes token-shaped strings into a CI log is a
	// habit worth not having, and the workspace id already identifies the run.

	switch cfg.NegativePhase {
	case "warm":
		runNegativeWarm(k, cfg)
	case "matrix":
		runAuthenticationBoundary(k, cfg)
		runWorkspaceBoundary(k, cfg)
		runScopeBoundary(k, cfg)
		runCrossRealmResourceIDs(k, cfg)
		runCrossProjectIsolation(k, cfg)
		runStateIntegrityOnRejection(k, cfg)
		runCallerVersusProviderForbidden(k, cfg)
		runLifecycleTransitions(k, cfg)
		runConcurrentRevocation(k, cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown -phase %q (want warm or matrix)\n", cfg.NegativePhase)
		return 2
	}

	fmt.Printf("\n\033[1m%d passed, %d failed\033[0m\n", k.pass, k.fail)
	if k.fail > 0 {
		return 1
	}
	return 0
}

// ─── Phase: warm ────────────────────────────────────────────────────────────

// runNegativeWarm establishes the "before" half of every transition.
//
// Every check here is a POSITIVE one, and that is the point. A negative test
// that follows a state change proves the change caused the refusal only if the
// same call succeeded beforehand — otherwise archiving an already-broken
// workspace would "pass".
func runNegativeWarm(k *checker, cfg *Config) {
	step("warm: the credentials that are about to be cut off currently work")

	warm := []struct {
		name      string
		key       string
		workspace string
	}{
		{"credential in the workspace that will be archived", cfg.KeyArchivableWS, cfg.ArchivableWS},
		{"credential in the workspace whose connection will be retired", cfg.KeyLosesConnection, cfg.LosesConnectionWS},
		{"credential that will be revoked", cfg.RevocableKey, cfg.WorkspaceID},
	}

	for _, w := range warm {
		if w.key == "" || w.workspace == "" {
			k.skip(w.name, "fixture not provided")
			continue
		}
		c := NewClient(cfg.URL, w.workspace, w.key)
		resp, err := c.ListUsers()
		if err != nil || resp.Status != http.StatusOK {
			k.bad(w.name, describe(resp, err)+" — it must work BEFORE the transition, or the "+
				"matrix phase would pass for the wrong reason")
			continue
		}
		k.ok(w.name, "200, provider now cached server-side")
	}
}

// ─── Authentication ─────────────────────────────────────────────────────────

// runAuthenticationBoundary re-states the authentication cases against the real
// stack, where "rejected" also means "no realm was contacted".
//
// The status/code half duplicates the contract mode deliberately: this suite is
// run on its own in CI, and a negative matrix that assumed another suite had
// already checked its foundations would be reporting on something it did not
// observe.
func runAuthenticationBoundary(k *checker, cfg *Config) {
	step("authentication")

	valid := cfg.Client()
	if resp, err := valid.ListUsers(); err != nil || resp.Status != http.StatusOK {
		k.bad("the control credential works", describe(resp, err))
		return
	}
	k.ok("the control credential works", "200")

	unauthenticated := expectation{status: http.StatusUnauthorized, code: "credential_invalid"}

	cases := []struct {
		name string
		key  string
	}{
		{"no credential", ""},
		{"malformed credential", "lw_sk_nope"},
		{"prefix only", "lw_sk_"},
		{"unknown but well-formed", "lw_sk_" + strings.Repeat("a", 16) + "_" + strings.Repeat("b", 52)},
		{"one character changed", flipLast(cfg.APIKey)},
		{"lookup half of a real key, wrong secret", recombine(cfg.APIKey, cfg.KeyB)},
		{"a JWT-shaped value", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhIn0.c2ln"},
	}

	for _, tc := range cases {
		if strings.Contains(tc.name, "real key") && cfg.KeyB == "" {
			k.skip(tc.name, "LW_KEY_B unset")
			continue
		}
		e := unauthenticated
		e.name = tc.name
		resp, err := cfg.ClientWith(tc.key).ListUsers()
		k.expect(e, resp, err)
	}
}

// flipLast changes the final character of a token, producing a syntactically
// valid credential with the wrong secret.
func flipLast(s string) string {
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

// recombine pairs one credential's public lookup segment with another's secret.
//
// The lookup half is stored in clear and appears in operator tooling, so it has
// to be worthless on its own. This is the check that says so.
func recombine(a, b string) string {
	const prefix = "lw_sk_"
	if !strings.HasPrefix(a, prefix) || !strings.HasPrefix(b, prefix) {
		return "lw_sk_invalid"
	}
	lookupA, _, okA := strings.Cut(a[len(prefix):], "_")
	_, secretB, okB := strings.Cut(b[len(prefix):], "_")
	if !okA || !okB {
		return "lw_sk_invalid"
	}
	return prefix + lookupA + "_" + secretB
}

// ─── The workspace boundary ─────────────────────────────────────────────────

func runWorkspaceBoundary(k *checker, cfg *Config) {
	step("workspace boundary")

	if cfg.ForeignWorkspace == "" {
		k.skip("workspace mismatch", "LW_FOREIGN_WORKSPACE_ID unset")
		return
	}
	c := cfg.Client()

	// A read and a WRITE, because the write is the one whose failure mode
	// matters: a mutation that was refused after being performed would look
	// identical here and be caught by the state-integrity checks below.
	k.expectFn(expectation{
		name: "read against a foreign workspace", status: http.StatusForbidden, code: "workspace_mismatch",
	}, func() (*Response, error) {
		return c.DoForWorkspace(cfg.ForeignWorkspace, http.MethodGet, "/users", nil)
	})

	k.expectFn(expectation{
		name: "write against a foreign workspace", status: http.StatusForbidden, code: "workspace_mismatch",
	}, func() (*Response, error) {
		return c.DoForWorkspace(cfg.ForeignWorkspace, http.MethodPost, "/users", map[string]any{
			"email":              "crosses-the-boundary@example.test",
			"first_name":         "Should",
			"last_name":          "NotExist",
			"temporary_password": cfg.PasswordSentinel,
		})
	})

	// Existence must not leak: a real foreign workspace and an invented one
	// answer the same, because the check never reads either.
	realResp, realErr := c.DoForWorkspace(cfg.ForeignWorkspace, http.MethodGet, "/users", nil)
	fakeResp, fakeErr := c.DoForWorkspace("ws_00000000-0000-4000-8000-000000000000", http.MethodGet, "/users", nil)
	switch {
	case realErr != nil || fakeErr != nil:
		k.bad("a real foreign workspace is indistinguishable from an invented one", "transport error")
	case realResp.Status != fakeResp.Status || realResp.Code != fakeResp.Code:
		k.bad("a real foreign workspace is indistinguishable from an invented one",
			fmt.Sprintf("%d %s vs %d %s", realResp.Status, realResp.Code, fakeResp.Status, fakeResp.Code))
	default:
		k.ok("a real foreign workspace is indistinguishable from an invented one",
			fmt.Sprintf("%d %s", realResp.Status, realResp.Code))
	}
}

// ─── Scopes, representatively ───────────────────────────────────────────────

// runScopeBoundary takes ONE case per capability family rather than sweeping
// every route.
//
// The sweep lives in Layers A and B, where it costs milliseconds. What this
// adds is that the same denial holds when the middleware, the resolver, the
// provider and Keycloak are all real — which is a property of the assembly, and
// is proven by a representative rather than by repetition.
func runScopeBoundary(k *checker, cfg *Config) {
	step("scope boundary, one representative per capability family")

	if cfg.KeyReadOnly == "" {
		k.skip("insufficient scope", "LW_KEY_READONLY unset")
		return
	}
	ro := cfg.ClientWith(cfg.KeyReadOnly)

	// The control: users:read genuinely reads.
	if resp, err := ro.ListUsers(); err != nil || resp.Status != http.StatusOK {
		k.bad("a users:read credential can read users", describe(resp, err))
	} else {
		k.ok("a users:read credential can read users", "200")
	}

	// users:read against every other family, and against its own write half.
	family := []struct {
		name   string
		scope  string
		method string
		path   string
		body   any
	}{
		{"users:write is refused", "users:write", http.MethodPost, "/users", map[string]any{
			"email": "denied@example.test", "first_name": "No", "last_name": "Scope",
			"temporary_password": cfg.PasswordSentinel,
		}},
		{"roles:read is refused", "roles:read", http.MethodGet, "/roles", nil},
		{"roles:write is refused", "roles:write", http.MethodPost, "/roles",
			map[string]any{"name": "should-not-exist"}},
		{"sessions:read is refused", "sessions:read", http.MethodGet, "/sessions", nil},
		{"invitations:read is refused", "invitations:read", http.MethodGet, "/invitations", nil},
		{"invitations:write is refused", "invitations:write", http.MethodPost, "/invitations",
			map[string]any{"email": "denied@example.test", "roles": []string{}}},
		{"audit:read is refused", "audit:read", http.MethodGet, "/audit", nil},
	}

	for _, f := range family {
		resp, err := ro.Do(f.method, f.path, f.body)
		k.expect(expectation{
			name: f.name, status: http.StatusForbidden, code: "insufficient_scope",
			// RFC 6750 §3.1: the refusal names the scope that would have worked.
			wantHeader: map[string]string{"WWW-Authenticate": f.scope},
		}, resp, err)
	}

	// The audit trail is the one capability a powerful identity key must not
	// pick up on the way past.
	if cfg.KeyNoAudit == "" {
		k.skip("a key with every identity scope still cannot read the audit trail", "LW_KEY_NO_AUDIT unset")
	} else {
		powerful := cfg.ClientWith(cfg.KeyNoAudit)
		if resp, err := powerful.ListUsers(); err != nil || resp.Status != http.StatusOK {
			k.bad("the audit-less key is otherwise powerful", describe(resp, err))
		} else {
			k.ok("the audit-less key is otherwise powerful", "200 on users")
		}
		k.expectFn(expectation{
			name:   "a key with every identity scope still cannot read the audit trail",
			status: http.StatusForbidden, code: "insufficient_scope",
			wantHeader: map[string]string{"WWW-Authenticate": "audit:read"},
		}, func() (*Response, error) { return powerful.Do(http.MethodGet, "/audit", nil) })
	}

	// And in the other direction.
	if cfg.KeyAuditOnly == "" {
		k.skip("an audit:read-only key cannot touch identity", "LW_KEY_AUDIT_ONLY unset")
	} else {
		auditOnly := cfg.ClientWith(cfg.KeyAuditOnly)
		if resp, err := auditOnly.Do(http.MethodGet, "/audit", nil); err != nil || resp.Status != http.StatusOK {
			k.bad("the audit-only key can read the trail", describe(resp, err))
		} else {
			k.ok("the audit-only key can read the trail", "200")
		}
		k.expectFn(expectation{
			name: "an audit:read-only key cannot read users", status: http.StatusForbidden, code: "insufficient_scope",
		}, auditOnly.ListUsers)
		k.expectFn(expectation{
			name: "an audit:read-only key cannot create a role", status: http.StatusForbidden, code: "insufficient_scope",
		}, func() (*Response, error) { return auditOnly.CreateRole("audit-only-should-not-create") })
	}
}

// ─── Cross-realm resource ids ───────────────────────────────────────────────

// runCrossRealmResourceIDs is the critical case, and the one that only a real
// multi-realm stack can settle.
//
// The caller is entirely legitimate: right credential, right workspace, right
// scope. The only thing wrong is the resource identifier, which names a user
// living in ANOTHER workspace's Keycloak realm. Keycloak's admin API addresses
// users by a realm-scoped UUID, so an implementation that authorized the path
// and then interpolated the identifier would be issuing an admin call for a
// user it has no business touching — and, in a shared-Keycloak deployment,
// might well succeed.
func runCrossRealmResourceIDs(k *checker, cfg *Config) {
	step("cross-realm resource ids")

	if cfg.ForeignUserID == "" {
		k.skip("a user id from another realm cannot be used", "LW_FOREIGN_USER_ID unset")
		return
	}
	c := cfg.Client()

	// The control: this credential CAN address its own realm's users, so a
	// refusal below is about the identifier and not about the capability.
	if cfg.OwnUserID != "" {
		if resp, err := c.GetUser(cfg.OwnUserID); err != nil || resp.Status != http.StatusOK {
			k.bad("the credential can read its own realm's user", describe(resp, err))
		} else {
			k.ok("the credential can read its own realm's user", "200")
		}
	}

	attacks := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"read a foreign realm's user", http.MethodGet, "/users/" + cfg.ForeignUserID, nil},
		{"update a foreign realm's user", http.MethodPatch, "/users/" + cfg.ForeignUserID,
			map[string]any{"first_name": "Taken"}},
		{"delete a foreign realm's user", http.MethodDelete, "/users/" + cfg.ForeignUserID, nil},
		{"reset a foreign realm's user's password", http.MethodPost,
			"/users/" + cfg.ForeignUserID + "/reset-password", nil},
		{"read a foreign realm's user's roles", http.MethodGet, "/users/" + cfg.ForeignUserID + "/roles", nil},
		{"grant a role to a foreign realm's user", http.MethodPost, "/users/" + cfg.ForeignUserID + "/roles",
			map[string]any{"roles": []string{"lw-negative-role"}}},
		{"list a foreign realm's user's sessions", http.MethodGet,
			"/users/" + cfg.ForeignUserID + "/sessions", nil},
		{"revoke a foreign realm's user's sessions", http.MethodDelete,
			"/users/" + cfg.ForeignUserID + "/sessions", nil},
	}

	for _, a := range attacks {
		resp, err := c.Do(a.method, a.path, a.body)
		switch {
		case err != nil:
			k.bad(a.name, "transport error: "+err.Error())
		case resp.Status < 400:
			k.bad(a.name, fmt.Sprintf("status %d — the resource boundary was crossed", resp.Status))
		case resp.Status == http.StatusNotFound:
			k.ok(a.name, "404 "+resp.Code)
		default:
			// Any refusal is acceptable as a security outcome; 404 is the
			// documented one. A different refusal is reported rather than
			// failed, so a legitimate contract change does not read as a
			// vulnerability.
			k.ok(a.name, fmt.Sprintf("%d %s (refused, though 404 is the documented answer)",
				resp.Status, resp.Code))
		}
	}

	// Existence disclosure: a user that exists in another realm must look the
	// same as one that exists nowhere.
	const invented = "00000000-1111-4000-8000-999999999999"
	foreign, errF := c.GetUser(cfg.ForeignUserID)
	nobody, errN := c.GetUser(invented)
	switch {
	case errF != nil || errN != nil:
		k.bad("a foreign realm's user is indistinguishable from a nonexistent one", "transport error")
	case foreign.Status != nobody.Status || foreign.Code != nobody.Code:
		k.bad("a foreign realm's user is indistinguishable from a nonexistent one",
			fmt.Sprintf("%d %s vs %d %s — the boundary discloses existence",
				foreign.Status, foreign.Code, nobody.Status, nobody.Code))
	default:
		k.ok("a foreign realm's user is indistinguishable from a nonexistent one",
			fmt.Sprintf("%d %s", foreign.Status, foreign.Code))
	}
}

// ─── Cross-project isolation inside one workspace ───────────────────────────

// runCrossProjectIsolation proves authorization derives from the credential's
// own project, not from the workspace the credential happens to sit in.
//
// Two projects, one workspace, disjoint scopes. The failure mode this catches
// is an implementation that resolved capabilities per WORKSPACE — which would
// work perfectly in every single-project installation and would silently merge
// every project's permissions in a real one.
func runCrossProjectIsolation(k *checker, cfg *Config) {
	step("two projects in one workspace")

	if cfg.KeySecondProject == "" {
		k.skip("cross-project isolation", "LW_KEY_SECOND_PROJECT unset")
		return
	}

	// Project 1's key is users:read only (KeyReadOnly). Project 2's is
	// roles:write only.
	if cfg.KeyReadOnly == "" {
		k.skip("cross-project isolation", "LW_KEY_READONLY unset")
		return
	}
	one := cfg.ClientWith(cfg.KeyReadOnly)
	two := cfg.ClientWith(cfg.KeySecondProject)

	roleName := fmt.Sprintf("cross-project-%d", time.Now().UnixNano()%1000000)

	if resp, err := one.ListUsers(); err != nil || resp.Status != http.StatusOK {
		k.bad("project one does its own job", describe(resp, err))
	} else {
		k.ok("project one does its own job", "200 on users")
	}
	if resp, err := two.CreateRole(roleName); err != nil || resp.Status >= 300 {
		k.bad("project two does its own job", describe(resp, err))
	} else {
		k.ok("project two does its own job", fmt.Sprintf("%d on roles", resp.Status))
	}

	k.expectFn(expectation{
		name: "project one cannot use project two's capability", status: http.StatusForbidden, code: "insufficient_scope",
	}, func() (*Response, error) { return one.CreateRole(roleName + "-denied") })

	k.expectFn(expectation{
		name: "project two cannot use project one's capability", status: http.StatusForbidden, code: "insufficient_scope",
	}, two.ListUsers)

	// Clean up the role project two legitimately created.
	_, _ = two.Do(http.MethodDelete, "/roles/"+roleName, nil)
}

// ─── State integrity on rejection ───────────────────────────────────────────

// runStateIntegrityOnRejection is the check the slice insists on: a 403 is not
// enough.
//
// Every rejected mutation is followed by an independent read, through a
// DIFFERENT credential that is fully authorized, to confirm the realm is
// unchanged. A rejected request that still performed its mutation would answer
// 403 and pass every status-code assertion ever written.
//
// The audit trail is read the same way, and for the same reason: a rejected
// mutation must not appear in the workspace's history as something that
// happened.
func runStateIntegrityOnRejection(k *checker, cfg *Config) {
	step("a rejected mutation changes nothing")

	if cfg.KeyReadOnly == "" || cfg.KeyNoAudit == "" {
		k.skip("rejected mutations leave the realm unchanged", "LW_KEY_READONLY / LW_KEY_NO_AUDIT unset")
		return
	}

	observer := cfg.Client()                // every scope, including audit:read
	weak := cfg.ClientWith(cfg.KeyReadOnly) // users:read only

	before, err := observer.ListUsers()
	if err != nil || before.Status != http.StatusOK {
		k.bad("the observer can read the realm", describe(before, err))
		return
	}
	auditBefore, _ := observer.Do(http.MethodGet, "/audit", nil)

	email := fmt.Sprintf("must-not-exist-%d@example.test", time.Now().UnixNano())
	roleName := fmt.Sprintf("must-not-exist-role-%d", time.Now().UnixNano()%1000000)

	rejected := []struct {
		name   string
		method string
		path   string
		body   any
		client *Client
	}{
		{"create a user without users:write", http.MethodPost, "/users", map[string]any{
			"email": email, "first_name": "Must", "last_name": "NotExist",
			"temporary_password": cfg.PasswordSentinel,
		}, weak},
		{"create a role without roles:write", http.MethodPost, "/roles",
			map[string]any{"name": roleName}, weak},
		{"create a user in a foreign workspace", http.MethodPost, "/users", map[string]any{
			"email": email, "first_name": "Must", "last_name": "NotExist",
			"temporary_password": cfg.PasswordSentinel,
		}, nil}, // nil ⇒ foreign-workspace client, built below
	}

	for _, r := range rejected {
		var resp *Response
		var callErr error
		if r.client != nil {
			resp, callErr = r.client.Do(r.method, r.path, r.body)
		} else {
			if cfg.ForeignWorkspace == "" {
				k.skip(r.name, "LW_FOREIGN_WORKSPACE_ID unset")
				continue
			}
			resp, callErr = observer.DoForWorkspace(cfg.ForeignWorkspace, r.method, r.path, r.body)
		}
		if callErr != nil {
			k.bad(r.name, "transport error: "+callErr.Error())
			continue
		}
		if resp.Status != http.StatusForbidden {
			k.bad(r.name, fmt.Sprintf("status %d — this was supposed to be refused", resp.Status))
			continue
		}
		k.ok(r.name, fmt.Sprintf("%d %s", resp.Status, resp.Code))
	}

	// Now the part that matters. Nothing may have landed.
	after, err := observer.ListUsers()
	if err != nil || after.Status != http.StatusOK {
		k.bad("the realm is unchanged after the rejections", describe(after, err))
	} else if strings.Contains(string(after.Body), email) {
		k.bad("the realm is unchanged after the rejections",
			"the user from a REJECTED create exists in the realm")
	} else {
		k.ok("the realm is unchanged after the rejections", "the rejected user does not exist")
	}

	roles, err := observer.ListRoles()
	if err != nil || roles.Status != http.StatusOK {
		k.bad("the rejected role was not created", describe(roles, err))
	} else if strings.Contains(string(roles.Body), roleName) {
		k.bad("the rejected role was not created", "the role from a REJECTED create exists in the realm")
	} else {
		k.ok("the rejected role was not created", "absent")
	}

	// The password sentinel must not have reached the realm through any of the
	// rejected calls, and must not appear in anything the API hands back.
	if strings.Contains(string(after.Body), cfg.PasswordSentinel) {
		k.bad("the password sentinel never reaches a response", "it is in the user listing")
	} else {
		k.ok("the password sentinel never reaches a response", "")
	}

	// And the audit trail must not have grown a success event for any of them.
	auditAfter, err := observer.Do(http.MethodGet, "/audit", nil)
	switch {
	case err != nil || auditAfter.Status != http.StatusOK:
		k.bad("no success event was recorded for a rejected mutation", describe(auditAfter, err))
	case strings.Contains(string(auditAfter.Body), email),
		strings.Contains(string(auditAfter.Body), roleName):
		k.bad("no success event was recorded for a rejected mutation",
			"the durable trail names a target from a REJECTED mutation")
	case auditBefore != nil && len(auditAfter.Body) < len(auditBefore.Body)/2:
		k.bad("no success event was recorded for a rejected mutation",
			"the trail shrank, which means this check is not observing what it thinks it is")
	default:
		k.ok("no success event was recorded for a rejected mutation", "trail unchanged for both targets")
	}
}

// ─── Caller-forbidden versus provider-forbidden ─────────────────────────────

// runCallerVersusProviderForbidden proves the two stay distinguishable against
// a real Keycloak that really refuses.
//
// They are different problems with different fixes and different owners:
//
//	caller forbidden    the KEY lacks the capability
//	                    → the developer asks their operator for a better key
//	provider forbidden  the workspace's SERVICE ACCOUNT lacks the Keycloak
//	                    privilege
//	                    → the operator grants realm-management roles
//
// Conflating them sends every one of those conversations to the wrong person.
// The fixture is a workspace whose connection authenticates fine and can read,
// but whose service account was never granted the roles a write needs.
func runCallerVersusProviderForbidden(k *checker, cfg *Config) {
	step("caller-forbidden vs provider-forbidden")

	// Caller forbidden: the key is short of a scope. Provider never involved.
	if cfg.KeyReadOnly == "" {
		k.skip("caller forbidden is a 403 about the caller", "LW_KEY_READONLY unset")
	} else {
		k.expectFn(expectation{
			name: "caller forbidden is a 403 about the caller", status: http.StatusForbidden,
			code: "insufficient_scope",
		}, func() (*Response, error) {
			return cfg.ClientWith(cfg.KeyReadOnly).CreateRole("caller-forbidden-probe")
		})
	}

	// Provider forbidden: the key is fine, the service account is not.
	if cfg.KeyProviderReadOnly == "" || cfg.ProviderReadOnlyWS == "" {
		k.skip("provider forbidden is NOT a 403 about the caller",
			"LW_KEY_PROVIDER_READONLY / LW_PROVIDER_READONLY_WORKSPACE_ID unset")
		return
	}

	limited := NewClient(cfg.URL, cfg.ProviderReadOnlyWS, cfg.KeyProviderReadOnly)

	resp, err := limited.CreateRole(fmt.Sprintf("provider-forbidden-%d", time.Now().UnixNano()%1000000))
	switch {
	case err != nil:
		k.bad("provider forbidden is NOT a 403 about the caller", "transport error: "+err.Error())
	case resp.Status == http.StatusForbidden &&
		(resp.Code == "insufficient_scope" || resp.Code == "operator_only" || resp.Code == "workspace_mismatch"):
		k.bad("provider forbidden is NOT a 403 about the caller",
			fmt.Sprintf("got %d %s — a provider-side refusal is being reported as a caller-side one, "+
				"which sends operators to the wrong system", resp.Status, resp.Code))
	case resp.Code == "provider_forbidden" || resp.Code == "connection_read_only":
		k.ok("provider forbidden is NOT a 403 about the caller",
			fmt.Sprintf("%d %s", resp.Status, resp.Code))
	case resp.Status < 400:
		k.bad("provider forbidden is NOT a 403 about the caller",
			fmt.Sprintf("status %d — an under-privileged service account performed a write", resp.Status))
	default:
		// Some other refusal. Report it: the security property (the write did
		// not happen, and it was not blamed on the caller's key) holds, and the
		// exact code is a contract detail worth seeing rather than failing on.
		k.ok("provider forbidden is NOT a 403 about the caller",
			fmt.Sprintf("%d %s (neither provider_forbidden nor connection_read_only)", resp.Status, resp.Code))
	}

	// The same workspace must still READ, or the fixture proves nothing about
	// writes specifically.
	if r, e := limited.ListUsers(); e != nil || r.Status != http.StatusOK {
		k.bad("the under-privileged connection can still read", describe(r, e))
	} else {
		k.ok("the under-privileged connection can still read", "200")
	}
}

// ─── Lifecycle transitions ──────────────────────────────────────────────────

// runLifecycleTransitions is the "after" half of the warm phase.
//
// Each of these worked in the warm phase, against the same running process,
// with the provider already cached. If any still works, a cached positive
// decision is outliving the state change that was supposed to end it — which is
// a security bug, not a staleness inconvenience.
func runLifecycleTransitions(k *checker, cfg *Config) {
	step("lifecycle transitions take effect with no restart")

	if cfg.KeyArchivableWS == "" || cfg.ArchivableWS == "" {
		k.skip("an archived workspace stops serving identity", "fixture not provided")
	} else {
		c := NewClient(cfg.URL, cfg.ArchivableWS, cfg.KeyArchivableWS)
		k.expectFn(expectation{
			name:   "an archived workspace stops serving identity reads",
			status: http.StatusConflict, code: "workspace_archived",
		}, c.ListUsers)
		k.expectFn(expectation{
			name:   "an archived workspace stops serving identity writes",
			status: http.StatusConflict, code: "workspace_archived",
		}, func() (*Response, error) {
			return c.CreateUser("after-archive@example.test", "After", "Archive")
		})
	}

	if cfg.KeyLosesConnection == "" || cfg.LosesConnectionWS == "" {
		k.skip("a workspace with no active connection does not fall back", "fixture not provided")
	} else {
		c := NewClient(cfg.URL, cfg.LosesConnectionWS, cfg.KeyLosesConnection)
		resp, err := c.ListUsers()
		k.expect(expectation{
			name:   "a workspace with no active connection is refused deterministically",
			status: http.StatusConflict, code: "workspace_connection_missing",
		}, resp, err)

		// The fallback question, answered from outside: if the resolver had
		// used the process's legacy Keycloak configuration, another workspace's
		// connection, or the retired one, this would be a 200 with somebody
		// else's users in it.
		if err == nil && resp.Status == http.StatusOK {
			k.bad("no fallback to another realm", "200 — the request was served by SOME provider")
		} else {
			k.ok("no fallback to another realm", "no provider served the request")
		}
	}
}

// ─── Concurrent revocation ──────────────────────────────────────────────────

// runConcurrentRevocation states a guarantee carefully rather than an
// impossible one.
//
// What is NOT claimed: that revocation cancels requests already in flight. It
// does not, and no HTTP server cancels a response it has begun writing because
// a row changed. Asserting that would be asserting a behaviour nobody
// implemented.
//
// What IS claimed, and checked: once the revocation has committed, every
// request that BEGINS afterwards fails, from every concurrent caller, with no
// restart and no per-goroutine warm-up. A per-connection or per-goroutine cache
// of the authentication decision would break exactly this and nothing else.
func runConcurrentRevocation(k *checker, cfg *Config) {
	step("concurrent revocation")

	if cfg.RevocableKey == "" {
		k.skip("revocation lands on every concurrent caller", "LW_KEY_REVOCABLE unset")
		return
	}

	const callers = 6
	var wg sync.WaitGroup
	results := make([]int, callers)

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c := cfg.ClientWith(cfg.RevocableKey)
			resp, err := c.ListUsers()
			if err != nil {
				results[idx] = -1
				return
			}
			results[idx] = resp.Status
		}(i)
	}
	wg.Wait()

	failed := 0
	for _, status := range results {
		if status == http.StatusUnauthorized {
			failed++
		}
	}
	if failed == callers {
		k.ok("revocation lands on every concurrent caller",
			fmt.Sprintf("%d/%d goroutines refused with 401", failed, callers))
	} else {
		k.bad("revocation lands on every concurrent caller",
			fmt.Sprintf("only %d/%d goroutines were refused; statuses %v", failed, callers, results))
	}
}
