package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// The M2M contract check.
//
// Two halves, in this order:
//
//	the flow    what a backend does when everything is configured correctly
//	the matrix  every documented failure, and whether it answers as documented
//
// The matrix is the half that matters for an SDK. A client library can be
// written against a surface whose successes are irregular; it cannot be written
// against a surface whose FAILURES are irregular, because error handling is the
// one thing every call site shares.
//
// For each failure the harness checks four things, because an SDK branches on
// all four:
//
//	HTTP status   what a transport-level retry policy sees
//	error.code    what application code branches on
//	request_id    what a support conversation is keyed by
//	headers       what a correct client is told to do next
type checker struct {
	pass, fail int
}

// There is deliberately no "known gap" tier.
//
// One existed while TD-029 was open: a WARN that named the debt item and did
// not fail the run. It is gone because the gap is gone, and a tolerated-failure
// mechanism kept alive with nothing in it is an invitation to downgrade the
// next real failure into it. If a gap has to be tolerated again, re-adding a
// counter is five lines, and doing it deliberately is the point.

func (k *checker) ok(name string, detail string) {
	k.pass++
	if detail != "" {
		fmt.Printf("  \033[32mPASS\033[0m %-46s %s\n", name, detail)
		return
	}
	fmt.Printf("  \033[32mPASS\033[0m %s\n", name)
}

func (k *checker) bad(name, detail string) {
	k.fail++
	fmt.Printf("  \033[31mFAIL\033[0m %-46s %s\n", name, detail)
}

func (k *checker) skip(name, why string) {
	fmt.Printf("  \033[33mSKIP\033[0m %-46s %s\n", name, why)
}

func (k *checker) eq(name string, got, want any) {
	if fmt.Sprint(got) == fmt.Sprint(want) {
		k.ok(name, fmt.Sprintf("%v", got))
		return
	}
	k.bad(name, fmt.Sprintf("got %v, want %v", got, want))
}

func step(title string) { fmt.Printf("\n\033[1m== %s\033[0m\n", title) }

// expect is the whole error-matrix assertion, in one call.
//
// Passing the expectations together rather than as four separate checks is what
// makes the matrix readable as a specification: each line below is one row of
// the table in PROJECTS.md §12, and a row that disagrees with the code shows up
// as a diff rather than as four unrelated failures.
type expectation struct {
	name       string
	status     int
	code       string
	wantHeader map[string]string // header → required substring ("" = must be present)
	noHeader   []string
}

func (k *checker) expect(e expectation, resp *Response, err error) {
	if err != nil {
		k.bad(e.name, "transport error: "+err.Error())
		return
	}

	var problems []string
	if resp.Status != e.status {
		problems = append(problems, fmt.Sprintf("status %d (want %d)", resp.Status, e.status))
	}
	if resp.Code != e.code {
		problems = append(problems, fmt.Sprintf("code %q (want %q)", resp.Code, e.code))
	}
	// Every /v1 error must carry a correlation id, in the body AND echoed in
	// the header, and the two must be the same value. An id in one but not the
	// other is worse than none: it looks correlatable and is not.
	if resp.RequestID == "" {
		problems = append(problems, "no request_id in the envelope")
	}
	if h := resp.HeaderRequestID(); h == "" {
		problems = append(problems, "no X-Request-Id header")
	} else if resp.RequestID != "" && h != resp.RequestID {
		problems = append(problems, fmt.Sprintf("X-Request-Id %q != envelope request_id %q", h, resp.RequestID))
	}
	for name, want := range e.wantHeader {
		got := resp.Header.Get(name)
		switch {
		case got == "":
			problems = append(problems, "missing header "+name)
		case want != "" && !strings.Contains(got, want):
			problems = append(problems, fmt.Sprintf("header %s = %q, want it to contain %q", name, got, want))
		}
	}
	for _, name := range e.noHeader {
		if got := resp.Header.Get(name); got != "" {
			problems = append(problems, fmt.Sprintf("header %s present (%q) and must not be", name, got))
		}
	}

	if len(problems) > 0 {
		k.bad(e.name, strings.Join(problems, "; "))
		return
	}
	k.ok(e.name, fmt.Sprintf("%d %s", resp.Status, resp.Code))
}

// expectFn is expect over a call rather than over its already-unpacked result.
//
// Go will not let a two-result call fill the trailing parameters of expect, so
// the alternative at every site would be two statements and a pair of
// throwaway variables. Passing the call keeps each row of the matrix readable
// as one row.
func (k *checker) expectFn(e expectation, call func() (*Response, error)) {
	resp, err := call()
	k.expect(e, resp, err)
}

func runContract(cfg *Config) int {
	k := &checker{}
	c := cfg.Client()

	fmt.Printf("\033[1mlwprobe — external M2M contract check\033[0m\n")
	fmt.Printf("  url        %s\n", c.BaseURL)
	fmt.Printf("  workspace  %s\n", c.WorkspaceID)
	fmt.Printf("  key        %s…\n", safePrefix(c.APIKey))

	runHappyPath(k, cfg, c)
	runErrorMatrix(k, cfg, c)
	runRateLimitContract(k, cfg)
	runRevocation(k, cfg)
	runSecretHygiene(k, c)

	fmt.Printf("\n\033[1m%d passed, %d failed\033[0m\n", k.pass, k.fail)
	if k.fail > 0 {
		return 1
	}
	return 0
}

// safePrefix renders enough of a key to identify it in a log and not enough to
// use it. The harness prints this instead of the token for the same reason the
// server never logs one.
func safePrefix(key string) string {
	if len(key) < 14 {
		return "lw_sk_…"
	}
	return key[:14]
}

// ---------------------------------------------------------------------------
// The flow
// ---------------------------------------------------------------------------

func runHappyPath(k *checker, cfg *Config, c *Client) {
	step("the flow a backend performs")

	// 1. Read users.
	resp, err := c.ListUsers()
	if err != nil {
		k.bad("GET users", err.Error())
		return
	}
	k.eq("GET users", resp.Status, 200)
	if h := resp.HeaderRequestID(); h == "" {
		k.bad("success carries X-Request-Id", "header absent")
	} else {
		k.ok("success carries X-Request-Id", h)
	}

	// 2. Provision a user.
	email := fmt.Sprintf("probe-%d@example.test", time.Now().UnixNano())
	resp, err = c.CreateUser(email, "Probe", "Client")
	if err != nil {
		k.bad("POST users", err.Error())
		return
	}
	if resp.Status != 200 && resp.Status != 201 {
		k.bad("POST users", fmt.Sprintf("status %d code %q body %s", resp.Status, resp.Code, truncate(string(resp.Body), 200)))
		return
	}
	k.ok("POST users", fmt.Sprintf("%d", resp.Status))

	userID, err := userIDFrom(resp.Body)
	if err != nil {
		k.bad("create response carries the new user's id", err.Error())
		return
	}
	k.ok("create response carries the new user's id", userID)

	// 3. Read it back — proves the write landed in the realm this workspace
	//    resolves to, using only the public surface.
	if resp, err = c.GetUser(userID); err != nil || resp.Status != 200 {
		k.bad("GET the created user", describe(resp, err))
	} else {
		k.ok("GET the created user", "200")
	}

	// 4. Roles.
	if resp, err = c.ListRoles(); err != nil || resp.Status != 200 {
		k.bad("GET roles", describe(resp, err))
	} else {
		k.ok("GET roles", "200")
	}

	// 5. Create and assign a NON-privileged role. Privileged ones are refused
	//    by design and are checked in the matrix below.
	roleName := fmt.Sprintf("probe-role-%d", time.Now().UnixNano()%100000)
	if resp, err = c.CreateRole(roleName); err != nil || (resp.Status != 200 && resp.Status != 201) {
		k.bad("POST roles", describe(resp, err))
	} else {
		k.ok("POST roles", fmt.Sprintf("%d", resp.Status))

		if resp, err = c.AssignRoles(userID, []string{roleName}); err != nil || resp.Status >= 300 {
			k.bad("assign a non-privileged role", describe(resp, err))
		} else {
			k.ok("assign a non-privileged role", fmt.Sprintf("%d", resp.Status))
		}
		if resp, err = c.ListUserRoles(userID); err != nil || resp.Status != 200 {
			k.bad("GET the user's roles", describe(resp, err))
		} else if !strings.Contains(string(resp.Body), roleName) {
			k.bad("GET the user's roles", "the assigned role is not in the response")
		} else {
			k.ok("GET the user's roles", "200, role present")
		}
	}

	// 6. Sessions.
	if resp, err = c.ListUserSessions(userID); err != nil || resp.Status != 200 {
		k.bad("GET the user's sessions", describe(resp, err))
	} else {
		k.ok("GET the user's sessions", "200")
	}
	if resp, err = c.ListSessions(); err != nil || resp.Status != 200 {
		k.bad("GET realm sessions", describe(resp, err))
	} else {
		k.ok("GET realm sessions", "200")
	}

	// Clean up after ourselves — a harness that leaves users behind makes the
	// next run's assertions weaker.
	_, _ = c.DeleteUser(userID)
	_ = cfg
}

func describe(r *Response, err error) string {
	if err != nil {
		return err.Error()
	}
	if r == nil {
		return "no response"
	}
	return fmt.Sprintf("status %d code %q", r.Status, r.Code)
}

// ---------------------------------------------------------------------------
// The matrix
// ---------------------------------------------------------------------------

func runErrorMatrix(k *checker, cfg *Config, c *Client) {
	step("error contract")

	anonymous := NewClient(cfg.URL, cfg.WorkspaceID, "")
	resp, err := anonymous.ListUsers()
	k.expect(expectation{
		name: "missing bearer", status: http.StatusUnauthorized, code: "credential_invalid",
		wantHeader: map[string]string{"WWW-Authenticate": "invalid_token"},
	}, resp, err)

	// A syntactically valid token that names no credential. Distinct from
	// garbage: it exercises the database lookup rather than the parser.
	bogus := cfg.ClientWith("lw_sk_" + strings.Repeat("a", 16) + "_" + strings.Repeat("b", 52))
	resp, err = bogus.ListUsers()
	k.expect(expectation{
		name: "invalid credential", status: http.StatusUnauthorized, code: "credential_invalid",
	}, resp, err)

	// Malformed, which must be indistinguishable from the above.
	malformed := cfg.ClientWith("lw_sk_nope")
	resp, err = malformed.ListUsers()
	k.expect(expectation{
		name: "malformed credential", status: http.StatusUnauthorized, code: "credential_invalid",
	}, resp, err)

	matrixKey(k, cfg, cfg.KeyExpired, "LW_KEY_EXPIRED", expectation{
		name: "expired credential", status: http.StatusUnauthorized, code: "credential_invalid",
	})
	matrixKey(k, cfg, cfg.KeyRevoked, "LW_KEY_REVOKED", expectation{
		name: "revoked credential", status: http.StatusUnauthorized, code: "credential_invalid",
	})
	matrixKey(k, cfg, cfg.KeyArchived, "LW_KEY_ARCHIVED", expectation{
		name: "archived project", status: http.StatusUnauthorized, code: "credential_invalid",
	})

	// Workspace mismatch — refused before the resolver, so it must answer the
	// same whether the other workspace exists or not.
	if cfg.ForeignWorkspace == "" {
		k.skip("workspace mismatch", "LW_FOREIGN_WORKSPACE_ID unset")
	} else {
		resp, err = c.DoForWorkspace(cfg.ForeignWorkspace, http.MethodGet, "/users", nil)
		k.expect(expectation{
			name: "workspace mismatch (real workspace)", status: http.StatusForbidden, code: "workspace_mismatch",
		}, resp, err)
	}
	resp, err = c.DoForWorkspace("ws_00000000-0000-4000-8000-000000000000", http.MethodGet, "/users", nil)
	k.expect(expectation{
		name: "workspace mismatch (nonexistent)", status: http.StatusForbidden, code: "workspace_mismatch",
	}, resp, err)

	// Insufficient scope — must name the scope, per RFC 6750 §3.1, or a
	// developer cannot fix their key without reading the source.
	if cfg.KeyReadOnly == "" {
		k.skip("insufficient scope", "LW_KEY_READONLY unset")
	} else {
		ro := cfg.ClientWith(cfg.KeyReadOnly)
		if resp, err = ro.ListUsers(); err != nil || resp.Status != 200 {
			k.bad("read-only key can read", describe(resp, err))
		} else {
			k.ok("read-only key can read", "200")
		}
		resp, err = ro.CreateUser("denied@example.test", "No", "Scope")
		k.expect(expectation{
			name: "insufficient scope", status: http.StatusForbidden, code: "insufficient_scope",
			wantHeader: map[string]string{"WWW-Authenticate": "users:write"},
		}, resp, err)
	}

	// Operator-only, in both flavours the registry has: a control-plane route,
	// and a route inside the workspace that no scope grants.
	resp, err = c.DoRaw(http.MethodGet, "/v1/workspaces", nil)
	k.expect(expectation{
		name: "operator-only (control plane)", status: http.StatusForbidden, code: "operator_only",
	}, resp, err)

	resp, err = c.SetPassword("00000000-0000-0000-0000-000000000000", "irrelevant")
	k.expect(expectation{
		name: "operator-only (direct password set)", status: http.StatusForbidden, code: "operator_only",
	}, resp, err)

	resp, err = c.DoRaw(http.MethodPost, "/v1/workspaces/"+c.WorkspaceID+"/projects",
		map[string]any{"name": "self-minted"})
	k.expect(expectation{
		name: "operator-only (mint a credential)", status: http.StatusForbidden, code: "operator_only",
	}, resp, err)

	// A privileged role has its own code, because the credential DOES hold
	// roles:write and "add a scope" would be the wrong advice.
	resp, err = c.Do(http.MethodPost, "/users/00000000-0000-0000-0000-000000000000/roles",
		map[string]any{"roles": []string{"admin"}})
	if err != nil {
		k.bad("privileged role refused", err.Error())
	} else if resp.Status == http.StatusForbidden && resp.Code == "role_privileged" {
		k.ok("privileged role refused", "403 role_privileged")
	} else {
		// A key without roles:write legitimately gets insufficient_scope first;
		// that is the documented ordering, not a failure.
		k.eq("privileged role refused (403)", resp.Status, http.StatusForbidden)
	}

	// Validation. Not a security boundary, but the error an SDK's users hit
	// most often, so its quality is part of whether the contract is usable.
	resp, err = c.CreateUserWithoutPassword("no-password@example.test")
	if err != nil {
		k.bad("missing required field is a 400", err.Error())
	} else if resp.Status != http.StatusBadRequest {
		k.bad("missing required field is a 400", fmt.Sprintf("status %d", resp.Status))
	} else {
		k.ok("missing required field is a 400", "400 "+resp.Code)

		// The TD-029 contract: the field travels as DATA, in error.field, not
		// as prose in the message. A client branching on the message would
		// break the first time someone reworded it.
		switch {
		case resp.Field == "":
			k.bad("400 names the offending field",
				fmt.Sprintf("no error.field on %q — a client cannot tell WHICH field to fix", resp.Message))
		case resp.Field != "temporary_password":
			k.bad("400 names the offending field",
				fmt.Sprintf("error.field = %q, want temporary_password", resp.Field))
		default:
			k.ok("400 names the offending field", "error.field="+resp.Field)
		}
	}

	// And an error that is NOT about a field must carry no field key, or a
	// client cannot tell "no field" from "the field is blank".
	if resp, err = c.DoRaw(http.MethodGet, "/v1/workspaces", nil); err == nil {
		if resp.Field != "" {
			k.bad("non-validation errors carry no field",
				fmt.Sprintf("operator_only carried error.field=%q", resp.Field))
		} else {
			k.ok("non-validation errors carry no field", "")
		}
	}

	// Provider unavailable — the workspace resolves, the realm behind it does
	// not. 502 rather than 500: the failure is downstream, and a client should
	// retry rather than treat its own request as malformed.
	if cfg.KeyDeadProvider == "" || cfg.DeadProviderWS == "" {
		k.skip("provider unavailable", "LW_KEY_DEAD_PROVIDER / LW_DEAD_PROVIDER_WORKSPACE_ID unset")
	} else {
		dead := NewClient(cfg.URL, cfg.DeadProviderWS, cfg.KeyDeadProvider)
		resp, err = dead.ListUsers()
		if err != nil {
			k.bad("provider unavailable", "transport error: "+err.Error())
		} else if resp.Status == http.StatusBadGateway && resp.Code == "provider_unavailable" {
			k.ok("provider unavailable", "502 provider_unavailable")
		} else {
			k.bad("provider unavailable",
				fmt.Sprintf("got %d %q, want 502 provider_unavailable", resp.Status, resp.Code))
		}
		if resp != nil && resp.RequestID == "" {
			k.bad("provider failure carries request_id", "absent")
		} else if resp != nil {
			k.ok("provider failure carries request_id", resp.RequestID)
		}
	}
}

func matrixKey(k *checker, cfg *Config, key, envName string, e expectation) {
	if key == "" {
		k.skip(e.name, envName+" unset")
		return
	}
	resp, err := cfg.ClientWith(key).ListUsers()
	k.expect(e, resp, err)
}

// ---------------------------------------------------------------------------
// Rate limiting, from outside
// ---------------------------------------------------------------------------

// runRateLimitContract is the external proof of the TD-026 fix.
//
// It asserts the property, not a number: a credential must be able to exceed
// the EDGE allowance (which is what used to cap it) and must then be refused by
// its own. The two thresholds are read from the environment so the harness
// still means something after the defaults are retuned.
func runRateLimitContract(k *checker, cfg *Config) {
	step("rate limit, observed from a real client")

	if cfg.SkipRateLimitTest {
		k.skip("effective credential limit", "LW_SKIP_RATE_LIMIT_TEST=true")
		return
	}

	// Both thresholds come from the environment so the check keeps meaning the
	// same thing after a retune. The drain budget is sized from the CREDENTIAL
	// burst rather than a constant: on an installation configured for high
	// machine throughput, a fixed budget would run out before the bucket did
	// and report "never throttled" for a limiter that works.
	edgeCeiling := envInt("LW_EDGE_BURST", 20)
	credBurst := envInt("LW_CREDENTIAL_BURST", 40)
	c := cfg.Client()

	admitted, throttled := drain(c, credBurst*3)
	if throttled == nil {
		k.bad("credential is throttled by its own bucket",
			fmt.Sprintf("%d requests, never refused — the credential limiter is not enforcing", admitted))
		return
	}

	if admitted <= edgeCeiling {
		k.bad("credential exceeds the old edge ceiling",
			fmt.Sprintf("admitted %d, edge burst is %d — TD-026 is still present", admitted, edgeCeiling))
	} else {
		k.ok("credential exceeds the old edge ceiling",
			fmt.Sprintf("%d admitted before 429 (edge burst %d)", admitted, edgeCeiling))
	}

	k.expect(expectation{
		name: "429 contract", status: http.StatusTooManyRequests, code: "rate_limit_exceeded",
		wantHeader: map[string]string{"Retry-After": "", "RateLimit-Limit": "", "RateLimit-Remaining": "0"},
	}, throttled, nil)

	// A second credential on the same project, from the same host, must be
	// unaffected. This is the property a per-IP ceiling gets wrong.
	if cfg.KeyB == "" {
		k.skip("second credential unaffected", "LW_KEY_B unset")
	} else {
		b := cfg.ClientWith(cfg.KeyB)
		resp, err := b.ListUsers()
		if err != nil {
			k.bad("second credential unaffected", err.Error())
		} else if resp.Status == http.StatusTooManyRequests {
			k.bad("second credential unaffected", "429 — key A's flood drained a shared bucket")
		} else {
			k.ok("second credential unaffected", fmt.Sprintf("%d", resp.Status))
		}
	}

	// Let the bucket refill so later checks are not measuring this one.
	time.Sleep(3 * time.Second)
}

// drain sends until the first 429 and returns how many were admitted.
func drain(c *Client, max int) (admitted int, throttled *Response) {
	for i := 0; i < max; i++ {
		resp, err := c.ListUsers()
		if err != nil {
			return i, nil
		}
		if resp.Status == http.StatusTooManyRequests {
			return i, resp
		}
		admitted = i + 1
	}
	return admitted, nil
}

// ---------------------------------------------------------------------------
// Revocation
// ---------------------------------------------------------------------------

// runRevocation proves revocation takes effect on the NEXT request, with no
// restart and no cache to wait out.
//
// The revoke itself is an operator action, so the harness cannot perform it: a
// credential that could revoke a credential would be a control-plane
// credential. It runs in two passes instead, and scripts/m2m-harness.sh does
// the revoking between them through the same route the console uses.
func runRevocation(k *checker, cfg *Config) {
	if cfg.RevocableKey == "" {
		return
	}
	step("revocation")

	phase := os.Getenv("LW_REVOCATION_PHASE")
	c := cfg.ClientWith(cfg.RevocableKey)
	resp, err := c.ListUsers()

	switch phase {
	case "before":
		if err != nil || resp.Status != 200 {
			k.bad("revocable key works before revocation", describe(resp, err))
		} else {
			k.ok("revocable key works before revocation", "200")
		}
	case "after":
		k.expect(expectation{
			name:   "revoked key fails on the very next request",
			status: http.StatusUnauthorized, code: "credential_invalid",
		}, resp, err)
	default:
		k.skip("revocation", "LW_REVOCATION_PHASE not set to before|after")
	}
}

// ---------------------------------------------------------------------------
// Secret hygiene
// ---------------------------------------------------------------------------

// runSecretHygiene checks that nothing a consumer receives echoes back a value
// it is supposed to be the only holder of.
//
// The log side of the same rule is checked by the shell harness, which can read
// the process's output; from here the only observable surface is the response,
// and a response that echoed the key would put it in every proxy log on the way
// back.
func runSecretHygiene(k *checker, c *Client) {
	step("secret hygiene in responses")

	probes := []struct {
		name string
		call func() (*Response, error)
	}{
		{"success", c.ListUsers},
		{"authorization failure", func() (*Response, error) {
			return c.DoRaw(http.MethodGet, "/v1/workspaces", nil)
		}},
		{"authentication failure", func() (*Response, error) {
			return NewClient(c.BaseURL, c.WorkspaceID, c.APIKey+"x").ListUsers()
		}},
	}

	for _, p := range probes {
		resp, err := p.call()
		if err != nil {
			k.bad("no key material in the "+p.name+" response", err.Error())
			continue
		}
		leak := ""
		if strings.Contains(string(resp.Body), c.APIKey) {
			leak = "body echoes the API key"
		}
		for name, values := range resp.Header {
			for _, v := range values {
				if strings.Contains(v, c.APIKey) {
					leak = "header " + name + " echoes the API key"
				}
			}
		}
		// The lookup segment alone is enough to identify the credential and
		// must not come back either.
		if lookup := lookupSegment(c.APIKey); lookup != "" && strings.Contains(string(resp.Body), lookup) {
			leak = "body echoes the key's lookup segment"
		}
		if leak != "" {
			k.bad("no key material in the "+p.name+" response", leak)
		} else {
			k.ok("no key material in the "+p.name+" response", "")
		}
	}
}

// lookupSegment extracts the non-secret half of a token, which is still an
// identifier and still must not be echoed.
func lookupSegment(key string) string {
	const prefix = "lw_sk_"
	if !strings.HasPrefix(key, prefix) {
		return ""
	}
	rest := key[len(prefix):]
	if i := strings.IndexByte(rest, '_'); i > 0 {
		return rest[:i]
	}
	return ""
}
