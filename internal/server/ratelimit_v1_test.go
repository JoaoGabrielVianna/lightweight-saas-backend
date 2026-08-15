package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/requestid"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// The TD-026 acceptance suite.
//
// Slice 7 shipped two /v1 limiters that competed: a 10 req/s per-IP bucket in
// front of authentication and a 20 req/s per-credential bucket behind it. A
// backend calls from one address, so the second was unreachable and the
// published machine contract was untrue.
//
// Every test here is a property of the FIX rather than of the numbers, so
// retuning the defaults cannot make the suite pass vacuously: each one derives
// its expectations from the configured settings it passes in.
//
// They run against the real router — SetupRouter, the real middleware chain,
// the real authz registry — because the bug was in how the middlewares
// composed, and a test of either middleware alone would have passed before the
// fix as well.

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

// rlKey is one credential the multiKeyAuth below will accept.
type rlKey struct {
	token        string
	credentialID string
	workspace    string
	scopes       []string
}

// multiKeyAuth authenticates several distinct credentials, so a test can prove
// one key's bucket is not another's. It counts lookups, which is how the
// anonymous-flood test shows the edge limiter still caps the work an
// unauthenticated caller can buy.
type multiKeyAuth struct {
	keys    map[string]rlKey
	lookups int
}

func newMultiKeyAuth(keys ...rlKey) *multiKeyAuth {
	m := &multiKeyAuth{keys: make(map[string]rlKey, len(keys))}
	for _, k := range keys {
		m.keys[k.token] = k
	}
	return m
}

func (m *multiKeyAuth) AuthenticateCredential(_ context.Context, token string) (*auth.ProjectPrincipal, error) {
	m.lookups++
	k, ok := m.keys[token]
	if !ok {
		return nil, nil
	}
	return &auth.ProjectPrincipal{
		ProjectID:    projTestProject,
		ProjectName:  "Rate limit fixture",
		CredentialID: k.credentialID,
		WorkspaceID:  k.workspace,
		Scopes:       k.scopes,
	}, nil
}

// token builds a syntactically valid `lw_sk_` value from a short label, so a
// test can name its keys "alpha" and "bravo" and still exercise the real prefix
// discrimination in AuthenticatePrincipal.
func rlToken(label string) string {
	pad := func(s string, n int) string {
		for len(s) < n {
			s += "a"
		}
		return s[:n]
	}
	return auth.ProjectTokenPrefix + pad(label, 16) + "_" + pad(label, 52)
}

// rlRouter builds the real /v1 surface with the given limits and credentials.
func rlRouter(t *testing.T, limits RateLimitSettings, pa auth.ProjectAuthenticator) *gin.Engine {
	t.Helper()
	r := newGin()
	SetupRouter(r, RouterDeps{
		User:              SetupUser(&gorm.DB{}),
		Provider:          &fakeProvider{id: adminIdentity("op")},
		AdminChecker:      &fakeAdminChecker{allow: true},
		Workspace:         stubWorkspaceHandler(),
		Project:           stubProjectHandler(),
		ProjectAuth:       pa,
		WorkspaceIdentity: stubWorkspaceIdentityHandler(t),
		RateLimits:        limits,
	})
	return r
}

// rlSend issues one /v1 request from a named source address.
func rlSend(r *gin.Engine, token, ip, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.RemoteAddr = ip + ":41000"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// rlUsersPath is a scoped, project-reachable route: the one a machine consumer
// actually calls. Using an operator-only route would prove nothing about the
// credential limiter, because Authorize would refuse before the handler.
func rlUsersPath(ws string) string { return "/v1/workspaces/" + ws + "/users" }

// countUntilThrottled sends up to max requests and reports how many were
// admitted before the first 429. Returns max when none was refused.
func countUntilThrottled(r *gin.Engine, token, ip, path string, max int) (admitted int, last *httptest.ResponseRecorder) {
	for i := 0; i < max; i++ {
		w := rlSend(r, token, ip, path)
		if w.Code == http.StatusTooManyRequests {
			return i, w
		}
		last = w
	}
	return max, last
}

// ---------------------------------------------------------------------------
// The headline property: TD-026
// ---------------------------------------------------------------------------

// TestV1_CredentialReachesItsOwnLimitNotTheEdgeCeiling is the test TD-026
// exists for.
//
// Before the fix a credential calling from one address was refused after the
// EDGE burst (20 by default) no matter what its own allowance was. It must now
// be refused only by its own bucket, at the credential burst.
func TestV1_CredentialReachesItsOwnLimitNotTheEdgeCeiling(t *testing.T) {
	limits := RateLimitSettings{EdgeRPS: 10, CredentialRPS: 20}
	pa := newMultiKeyAuth(rlKey{
		token: rlToken("alpha"), credentialID: "key_alpha",
		workspace: projTestWorkspace, scopes: []string{"users:read"},
	})
	r := rlRouter(t, limits, pa)

	admitted, throttled := countUntilThrottled(r,
		rlToken("alpha"), "203.0.113.10", rlUsersPath(projTestWorkspace), 200)

	edgeBurst := limits.EdgeBurst()
	if admitted <= edgeBurst {
		t.Fatalf("credential admitted %d requests, want more than the edge burst of %d — "+
			"TD-026 is not fixed: the edge limiter is still the binding constraint",
			admitted, edgeBurst)
	}

	// And it IS refused, at roughly its own burst. Roughly, because the bucket
	// refills while the loop runs; the assertion is that the ceiling is the
	// credential's, not that no time passed.
	credBurst := limits.CredentialBurst()
	if admitted < credBurst || admitted > credBurst*2 {
		t.Errorf("credential admitted %d requests, want ≈ its own burst of %d", admitted, credBurst)
	}
	if throttled == nil {
		t.Fatal("credential was never throttled — its own limit is not enforced either")
	}
	if got := errorCode(t, throttled); got != "rate_limit_exceeded" {
		t.Errorf("error.code = %q, want rate_limit_exceeded", got)
	}
}

// TestV1_OverLimitCredentialDoesNotDrainTheEdgeBucket is the other half of the
// same property, and the one a naive fix gets wrong.
//
// If the edge token were released only for requests the credential limiter
// ADMITTED, a credential over its limit would drain the shared per-IP bucket
// and take its neighbours down with it. The release therefore covers every
// request that authenticated, including the ones its own bucket then refused.
func TestV1_OverLimitCredentialDoesNotDrainTheEdgeBucket(t *testing.T) {
	limits := RateLimitSettings{EdgeRPS: 10, CredentialRPS: 20}
	pa := newMultiKeyAuth(rlKey{
		token: rlToken("alpha"), credentialID: "key_alpha",
		workspace: projTestWorkspace, scopes: []string{"users:read"},
	})
	r := rlRouter(t, limits, pa)

	// Push far past the credential's burst from one address.
	for i := 0; i < 300; i++ {
		rlSend(r, rlToken("alpha"), "203.0.113.11", rlUsersPath(projTestWorkspace))
	}

	// An operator from the SAME address must be unaffected: the machine's
	// traffic was metered to the machine, not to the address.
	w := rlSend(r, "operator-jwt", "203.0.113.11", "/v1/workspaces")
	if w.Code == http.StatusTooManyRequests {
		t.Fatal("an over-limit credential exhausted the shared edge bucket — " +
			"its refused requests were charged to the IP instead of to the credential")
	}
}

// ---------------------------------------------------------------------------
// Isolation between credentials
// ---------------------------------------------------------------------------

// TestV1_CredentialIsolation_SameSourceAddress — key A saturated must not
// throttle key B, INCLUDING when both call from the same host, which is the
// normal deployment (one backend, several keys) and the case a per-IP ceiling
// gets wrong.
func TestV1_CredentialIsolation_SameSourceAddress(t *testing.T) {
	limits := RateLimitSettings{EdgeRPS: 10, CredentialRPS: 20}
	pa := newMultiKeyAuth(
		rlKey{token: rlToken("alpha"), credentialID: "key_alpha",
			workspace: projTestWorkspace, scopes: []string{"users:read"}},
		rlKey{token: rlToken("bravo"), credentialID: "key_bravo",
			workspace: projTestWorkspace, scopes: []string{"users:read"}},
	)
	r := rlRouter(t, limits, pa)

	const sameHost = "203.0.113.20"

	// Saturate A.
	admitted, _ := countUntilThrottled(r, rlToken("alpha"), sameHost, rlUsersPath(projTestWorkspace), 200)
	if admitted >= 200 {
		t.Fatal("key A was never throttled; the test cannot prove isolation")
	}
	if w := rlSend(r, rlToken("alpha"), sameHost, rlUsersPath(projTestWorkspace)); w.Code != http.StatusTooManyRequests {
		t.Fatalf("key A status = %d, want 429 (its bucket is empty)", w.Code)
	}

	// B is a different credential and must be untouched.
	if w := rlSend(r, rlToken("bravo"), sameHost, rlUsersPath(projTestWorkspace)); w.Code == http.StatusTooManyRequests {
		t.Error("key B was throttled because key A flooded — buckets are not per credential")
	}
}

// TestV1_CredentialIsolation_DifferentProjects pins the same property across
// projects, which is the multi-tenant version of it.
func TestV1_CredentialIsolation_DifferentProjects(t *testing.T) {
	limits := RateLimitSettings{EdgeRPS: 10, CredentialRPS: 20}
	pa := newMultiKeyAuth(
		rlKey{token: rlToken("alpha"), credentialID: "key_alpha",
			workspace: projTestWorkspace, scopes: []string{"users:read"}},
		rlKey{token: rlToken("other"), credentialID: "key_other_project",
			workspace: projTestWorkspace, scopes: []string{"users:read"}},
	)
	r := rlRouter(t, limits, pa)

	countUntilThrottled(r, rlToken("alpha"), "203.0.113.21", rlUsersPath(projTestWorkspace), 200)

	if w := rlSend(r, rlToken("other"), "203.0.113.22", rlUsersPath(projTestWorkspace)); w.Code == http.StatusTooManyRequests {
		t.Error("a second project's credential was throttled by the first project's flood")
	}
}

// ---------------------------------------------------------------------------
// The bucket follows the credential, not the address
// ---------------------------------------------------------------------------

// TestV1_CredentialBucketIsSharedAcrossSourceAddresses — a credential used from
// two addresses draws on ONE allowance. Otherwise a backend could multiply its
// quota by scaling out, or by sitting behind a proxy pool, and the published
// per-credential number would be a per-credential-per-address number.
func TestV1_CredentialBucketIsSharedAcrossSourceAddresses(t *testing.T) {
	limits := RateLimitSettings{EdgeRPS: 10, CredentialRPS: 20}
	pa := newMultiKeyAuth(rlKey{
		token: rlToken("alpha"), credentialID: "key_alpha",
		workspace: projTestWorkspace, scopes: []string{"users:read"},
	})
	r := rlRouter(t, limits, pa)

	// Drain the credential's bucket from one address.
	admitted, _ := countUntilThrottled(r, rlToken("alpha"), "198.51.100.1", rlUsersPath(projTestWorkspace), 200)
	if admitted >= 200 {
		t.Fatal("the credential was never throttled; the test cannot prove a shared bucket")
	}

	// Moving to a fresh address must NOT reset it. A fresh address has a full
	// edge bucket, so a 429 here can only come from the credential's own.
	w := rlSend(r, rlToken("alpha"), "198.51.100.2", rlUsersPath(projTestWorkspace))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status from the second address = %d, want 429 — "+
			"changing source IP reset the credential's limit", w.Code)
	}
	if got := errorCode(t, w); got != "rate_limit_exceeded" {
		t.Errorf("error.code = %q, want rate_limit_exceeded", got)
	}
}

// ---------------------------------------------------------------------------
// Anonymous protection is unchanged
// ---------------------------------------------------------------------------

// TestV1_InvalidCredentialFloodIsStillCappedAtTheEdge is the test that stops
// the TD-026 fix from being a hole.
//
// The release only applies to a request that authenticated. A flood of
// well-formed but unknown credentials must still be cut off at the edge burst,
// and — the part that matters — must stop buying credential lookups once it is.
func TestV1_InvalidCredentialFloodIsStillCappedAtTheEdge(t *testing.T) {
	limits := RateLimitSettings{EdgeRPS: 10, CredentialRPS: 20}
	pa := newMultiKeyAuth() // knows no keys: every lookup fails
	r := rlRouter(t, limits, pa)

	const attacker = "203.0.113.66"
	const attempts = 200

	throttled := 0
	for i := 0; i < attempts; i++ {
		w := rlSend(r, rlToken("bogus"+strconv.Itoa(i)), attacker, rlUsersPath(projTestWorkspace))
		if w.Code == http.StatusTooManyRequests {
			throttled++
		}
	}

	if throttled == 0 {
		t.Fatal("an invalid-credential flood was never throttled — the edge limiter is gone")
	}

	// The real property: the flood bought roughly `burst` lookups, not one per
	// request. The bucket refills during the loop, so allow generous slack; the
	// claim is order-of-magnitude, and 200 lookups would fail it.
	edgeBurst := limits.EdgeBurst()
	if pa.lookups > edgeBurst*3 {
		t.Errorf("an invalid-credential flood bought %d credential lookups from %d requests "+
			"(edge burst is %d) — the edge limiter is no longer protecting the authentication path",
			pa.lookups, attempts, edgeBurst)
	}
}

// TestV1_UnauthenticatedFloodIsStillCappedAtTheEdge — the same, for requests
// with no Authorization header at all, which never reach an authenticator.
func TestV1_UnauthenticatedFloodIsStillCappedAtTheEdge(t *testing.T) {
	limits := RateLimitSettings{EdgeRPS: 10, CredentialRPS: 20}
	r := rlRouter(t, limits, newMultiKeyAuth())

	admitted, throttled := countUntilThrottled(r, "", "203.0.113.67", rlUsersPath(projTestWorkspace), 200)
	if throttled == nil {
		t.Fatal("an unauthenticated flood was never throttled")
	}
	if admitted > limits.EdgeBurst()*2 {
		t.Errorf("%d unauthenticated requests were admitted before the first 429; "+
			"the edge burst is %d", admitted, limits.EdgeBurst())
	}
}

// ---------------------------------------------------------------------------
// Operator compatibility
// ---------------------------------------------------------------------------

// TestV1_OperatorKeepsTheEdgeLimit — the console's throughput must not have
// moved. Operators were metered at the edge before this change and still are:
// giving them a second, parallel allowance would be a silent change to a
// working surface to solve a problem nobody reported.
func TestV1_OperatorKeepsTheEdgeLimit(t *testing.T) {
	limits := RateLimitSettings{EdgeRPS: 10, CredentialRPS: 20}
	r := rlRouter(t, limits, newMultiKeyAuth())

	admitted, throttled := countUntilThrottled(r, "operator-jwt", "203.0.113.30", "/v1/workspaces", 200)
	if throttled == nil {
		t.Fatal("an operator was never throttled — the edge limiter no longer applies to operators")
	}
	if admitted > limits.EdgeBurst()*2 {
		t.Errorf("operator admitted %d requests before the first 429; the edge burst is %d — "+
			"operator throughput changed", admitted, limits.EdgeBurst())
	}
}

// TestAdminRateLimit_BodyIsStillTheLegacyShape — /admin/* must not have picked
// up the /v1 envelope. The console parses `{"error":"…"}` as a string, and the
// slice's rule is that /admin/* stays byte-compatible.
func TestAdminRateLimit_BodyIsStillTheLegacyShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimitPerIP(1, 1))
	r.GET("/admin/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/admin/x", nil)
		req.RemoteAddr = "203.0.113.40:1"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	_ = send()
	w := send()

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
	var legacy struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &legacy); err != nil {
		t.Fatalf("/admin 429 is no longer the legacy shape: %s", w.Body.String())
	}
	if legacy.Error != "rate limit exceeded" {
		t.Errorf("body error = %q, want the unchanged legacy string", legacy.Error)
	}
	if w.Header().Get(requestid.Header) != "" {
		t.Error("/admin/* grew an X-Request-Id header; that surface must stay unchanged")
	}
}

// ---------------------------------------------------------------------------
// The 429 contract
// ---------------------------------------------------------------------------

// TestV1_EdgeThrottleUsesTheV1Envelope — before this slice the /v1 edge 429 was
// the legacy `{"error":"rate limit exceeded"}`: no `error.code`, no
// `request_id`. It was the one /v1 response an SDK could not decode with the
// same type as every other error.
func TestV1_EdgeThrottleUsesTheV1Envelope(t *testing.T) {
	limits := RateLimitSettings{EdgeRPS: 1, CredentialRPS: 20}
	r := rlRouter(t, limits, newMultiKeyAuth())

	const ip = "203.0.113.50"
	_, throttled := countUntilThrottled(r, "", ip, rlUsersPath(projTestWorkspace), 50)
	if throttled == nil {
		t.Fatal("never throttled")
	}

	if got := errorCode(t, throttled); got != "rate_limit_exceeded" {
		t.Errorf("error.code = %q, want rate_limit_exceeded", got)
	}
	var body struct {
		Error struct {
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	_ = json.Unmarshal(throttled.Body.Bytes(), &body)
	if body.Error.RequestID == "" {
		t.Error("edge 429 has no request_id — it cannot be correlated with a log line")
	}
	if h := throttled.Header().Get(requestid.Header); h == "" || h != body.Error.RequestID {
		t.Errorf("X-Request-Id header = %q, want it to match the body's %q", h, body.Error.RequestID)
	}
	if throttled.Header().Get("Retry-After") == "" {
		t.Error("edge 429 has no Retry-After")
	}
}

// TestV1_EdgeThrottleDoesNotPublishTheAnonymousQuota — RateLimit-* headers
// describe an allowance that belongs to a caller. An unauthenticated caller has
// none, and telling it how the anti-flood limiter is tuned only helps it stay
// just under the threshold.
func TestV1_EdgeThrottleDoesNotPublishTheAnonymousQuota(t *testing.T) {
	limits := RateLimitSettings{EdgeRPS: 1, CredentialRPS: 20}
	r := rlRouter(t, limits, newMultiKeyAuth())

	_, throttled := countUntilThrottled(r, "", "203.0.113.51", rlUsersPath(projTestWorkspace), 50)
	if throttled == nil {
		t.Fatal("never throttled")
	}
	for _, h := range []string{"RateLimit-Limit", "RateLimit-Remaining"} {
		if v := throttled.Header().Get(h); v != "" {
			t.Errorf("edge 429 published %s: %q — that is internal tuning", h, v)
		}
	}
}

// TestV1_RateLimitHeadersAreSemanticallyTrue — the headers a machine consumer
// paces itself by must be worth pacing by.
//
// The pre-fix middleware advertised the BURST as RateLimit-Limit (40 while the
// sustained rate was 20) and only ever emitted RateLimit-Remaining on the 429,
// where it was the constant "0". A client that trusted either was misled.
func TestV1_RateLimitHeadersAreSemanticallyTrue(t *testing.T) {
	limits := RateLimitSettings{EdgeRPS: 10, CredentialRPS: 20}
	pa := newMultiKeyAuth(rlKey{
		token: rlToken("alpha"), credentialID: "key_alpha",
		workspace: projTestWorkspace, scopes: []string{"users:read"},
	})
	r := rlRouter(t, limits, pa)

	first := rlSend(r, rlToken("alpha"), "203.0.113.60", rlUsersPath(projTestWorkspace))

	limit := first.Header().Get("RateLimit-Limit")
	if limit != strconv.Itoa(int(limits.CredentialRPS)) {
		t.Errorf("RateLimit-Limit = %q, want the sustained rate %v — "+
			"advertising the burst tells a client to send more than it may",
			limit, limits.CredentialRPS)
	}

	remaining := first.Header().Get("RateLimit-Remaining")
	if remaining == "" {
		t.Fatal("RateLimit-Remaining missing on an admitted request — " +
			"a client can never see how much of its quota is left")
	}
	rem, err := strconv.Atoi(remaining)
	if err != nil {
		t.Fatalf("RateLimit-Remaining = %q, not an integer", remaining)
	}
	lim, _ := strconv.Atoi(limit)
	if rem > lim {
		t.Errorf("RateLimit-Remaining %d exceeds RateLimit-Limit %d; the pair is unreadable", rem, lim)
	}

	// Remaining must actually track consumption.
	second := rlSend(r, rlToken("alpha"), "203.0.113.60", rlUsersPath(projTestWorkspace))
	rem2, _ := strconv.Atoi(second.Header().Get("RateLimit-Remaining"))
	if rem2 > rem {
		t.Errorf("RateLimit-Remaining went up (%d → %d) while spending the quota", rem, rem2)
	}

	// And it must bottom out at 0 on the refusal, with a Retry-After.
	_, throttled := countUntilThrottled(r, rlToken("alpha"), "203.0.113.60", rlUsersPath(projTestWorkspace), 300)
	if throttled == nil {
		t.Fatal("never throttled")
	}
	if got := throttled.Header().Get("RateLimit-Remaining"); got != "0" {
		t.Errorf("RateLimit-Remaining on 429 = %q, want 0", got)
	}
	if throttled.Header().Get("Retry-After") == "" {
		t.Error("credential 429 has no Retry-After")
	}
}

// TestV1_RateLimitHeadersCarryNoSecret — the headers must describe a quota and
// nothing else. A credential id or a project name in a response header would
// leak into every proxy log between here and the caller.
func TestV1_RateLimitHeadersCarryNoSecret(t *testing.T) {
	limits := RateLimitSettings{EdgeRPS: 10, CredentialRPS: 20}
	token := rlToken("alpha")
	pa := newMultiKeyAuth(rlKey{
		token: token, credentialID: "key_alpha_secret_id",
		workspace: projTestWorkspace, scopes: []string{"users:read"},
	})
	r := rlRouter(t, limits, pa)

	w := rlSend(r, token, "203.0.113.61", rlUsersPath(projTestWorkspace))

	for name, values := range w.Header() {
		for _, v := range values {
			for _, secret := range []string{token, "key_alpha_secret_id", projTestProject} {
				if strings.Contains(v, secret) {
					t.Errorf("header %s leaks %q", name, secret)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

// TestRateLimitSettings_ZeroMeansDefaultsNotUnlimited — the knob must never be
// an off switch. A missing or mistyped env var lands on 0, and 0 has to mean
// "the shipped default", or a typo silently removes the protection.
func TestRateLimitSettings_ZeroMeansDefaultsNotUnlimited(t *testing.T) {
	var zero RateLimitSettings

	if got := zero.EdgeBurst(); got != rateLimitDefaultBurst {
		t.Errorf("zero EdgeBurst() = %d, want the default %d", got, rateLimitDefaultBurst)
	}
	if got := zero.CredentialBurst(); got != credentialRateLimitBurst {
		t.Errorf("zero CredentialBurst() = %d, want the default %d", got, credentialRateLimitBurst)
	}

	negative := RateLimitSettings{EdgeRPS: -5, CredentialRPS: -5}
	if got := negative.EdgeBurst(); got != rateLimitDefaultBurst {
		t.Errorf("negative EdgeBurst() = %d, want the default %d", got, rateLimitDefaultBurst)
	}

	// And the middleware itself must reject a non-positive rate the same way,
	// since it is what a caller bypassing RouterDeps would reach.
	r := gin.New()
	gin.SetMode(gin.TestMode)
	r.Use(RateLimitEdge(-1, -1))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	throttled := false
	for i := 0; i < rateLimitDefaultBurst*3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "203.0.113.70:1"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			throttled = true
			break
		}
	}
	if !throttled {
		t.Error("RateLimitEdge(-1, -1) never throttled — a negative rate was read as unlimited")
	}
}

// TestRateLimitSettings_BurstTracksTheRate — burst is derived, so raising the
// rate raises the burst with it and the pair cannot be configured into a state
// where the burst is below the rate (which cannot be sustained).
func TestRateLimitSettings_BurstTracksTheRate(t *testing.T) {
	s := RateLimitSettings{EdgeRPS: 50, CredentialRPS: 200}
	if got := s.EdgeBurst(); got != 100 {
		t.Errorf("EdgeBurst() = %d, want 100", got)
	}
	if got := s.CredentialBurst(); got != 400 {
		t.Errorf("CredentialBurst() = %d, want 400", got)
	}
}

// TestRateLimiter_RefundIsCappedAtBurst — a bucket must not be refunded above
// its capacity, or a long-lived key would accumulate credit and the burst would
// stop being a ceiling.
func TestRateLimiter_RefundIsCappedAtBurst(t *testing.T) {
	l := newRateLimiter(10, 3)

	if ok, _ := l.allow("k"); !ok {
		t.Fatal("first request refused")
	}
	for i := 0; i < 10; i++ {
		l.refund("k")
	}

	admitted := 0
	for i := 0; i < 20; i++ {
		if ok, _ := l.allow("k"); !ok {
			break
		}
		admitted++
	}
	if admitted > 4 {
		t.Errorf("bucket admitted %d after being over-refunded; capacity is 3", admitted)
	}
}

// TestRateLimiter_RefundDoesNotResurrectASweptBucket — refunding a key with no
// bucket must not create one, or a refund could allocate a map entry for an
// address that has stopped sending traffic.
func TestRateLimiter_RefundDoesNotResurrectASweptBucket(t *testing.T) {
	l := newRateLimiter(10, 3)
	l.refund("never-seen")

	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()

	if n != 0 {
		t.Errorf("refund created %d bucket(s) for an unseen key", n)
	}
}
