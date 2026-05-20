package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// stubChecker is an AdminChecker that returns canned answers and counts calls.
type stubChecker struct {
	calls     atomic.Int64
	isAdmin   bool
	err       error
	bySubject map[string]bool
}

func (s *stubChecker) IsAdmin(_ context.Context, subject string) (bool, error) {
	s.calls.Add(1)
	if s.err != nil {
		return false, s.err
	}
	if v, ok := s.bySubject[subject]; ok {
		return v, nil
	}
	return s.isAdmin, nil
}

// ─── RequireLiveAdmin behavior ──────────────────────────────────────────────

func TestRequireLiveAdmin_NoIdentity_Returns401(t *testing.T) {
	stub := &stubChecker{isAdmin: true}
	r := newGin(RequireLiveAdmin(stub))
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no identity, got %d", w.Code)
	}
	if got := stub.calls.Load(); got != 0 {
		t.Errorf("expected 0 upstream calls when identity missing, got %d", got)
	}
}

func TestRequireLiveAdmin_EmptySubject_Returns401(t *testing.T) {
	stub := &stubChecker{isAdmin: true}
	r := newGin(
		withIdentity(&Identity{Subject: "", Roles: []string{"admin"}}),
		RequireLiveAdmin(stub),
	)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for empty subject, got %d", w.Code)
	}
	if got := stub.calls.Load(); got != 0 {
		t.Errorf("upstream should not be consulted for empty subject (got %d calls)", got)
	}
}

// GAP-1 core repro: token's claim still says admin, but Keycloak no longer
// reports the role for this subject. The middleware MUST deny.
func TestRequireLiveAdmin_DemotedAdmin_Returns403(t *testing.T) {
	stub := &stubChecker{isAdmin: false}
	r := newGin(
		withIdentity(&Identity{Subject: "demoted-user", Roles: []string{"admin"}}),
		RequireLiveAdmin(stub),
	)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for demoted admin, got %d body=%s", w.Code, w.Body.String())
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 upstream call, got %d", got)
	}
}

// A current admin (claim says admin AND server confirms) must pass.
func TestRequireLiveAdmin_CurrentAdmin_PassesThrough(t *testing.T) {
	stub := &stubChecker{isAdmin: true}
	r := newGin(
		withIdentity(&Identity{Subject: "current-admin", Roles: []string{"admin"}}),
		RequireLiveAdmin(stub),
	)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for current admin, got %d body=%s", w.Code, w.Body.String())
	}
}

// Upstream failure must fail closed — never allow an admin verb to run when
// we can't verify the role state.
func TestRequireLiveAdmin_UpstreamError_FailsClosed(t *testing.T) {
	stub := &stubChecker{err: errors.New("keycloak: connection refused")}
	r := newGin(
		withIdentity(&Identity{Subject: "any", Roles: []string{"admin"}}),
		RequireLiveAdmin(stub),
	)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on upstream error, got %d", w.Code)
	}
}

func TestRequireLiveAdmin_EmitsForbiddenEventWithMarker(t *testing.T) {
	var observed AuthEvent
	prev := SetEventHook(func(e AuthEvent) { observed = e })
	defer SetEventHook(prev)

	stub := &stubChecker{isAdmin: false}
	r := newGin(
		withIdentity(&Identity{Subject: "demoted", Roles: []string{"admin"}}),
		RequireLiveAdmin(stub),
	)
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/protected", nil))

	if observed.Kind != EventForbidden {
		t.Errorf("expected EventForbidden, got %q", observed.Kind)
	}
	if observed.Subject != "demoted" {
		t.Errorf("event missing subject: %+v", observed)
	}
	// The marker is consumed by ops dashboards to distinguish GAP-1 denials
	// from plain RBAC denials.
	if got := observed.Reason; !contains(got, "live admin check denied") {
		t.Errorf("event reason missing GAP-1 marker, got %q", got)
	}
}

// ─── CachedAdminChecker ─────────────────────────────────────────────────────

func TestCachedAdminChecker_CachesPositiveResult(t *testing.T) {
	stub := &stubChecker{isAdmin: true}
	c := NewCachedAdminChecker(stub, 1*time.Minute)

	for i := 0; i < 5; i++ {
		ok, err := c.IsAdmin(context.Background(), "u1")
		if err != nil || !ok {
			t.Fatalf("iter %d: ok=%v err=%v", i, ok, err)
		}
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("expected 1 upstream call across 5 lookups (cache hit), got %d", got)
	}
}

func TestCachedAdminChecker_CachesNegativeResult(t *testing.T) {
	// A demoted-admin JWT might hit /admin/* repeatedly. Without negative
	// caching every request would round-trip to Keycloak.
	stub := &stubChecker{isAdmin: false}
	c := NewCachedAdminChecker(stub, 1*time.Minute)

	for i := 0; i < 5; i++ {
		ok, _ := c.IsAdmin(context.Background(), "demoted")
		if ok {
			t.Fatalf("iter %d: expected non-admin", i)
		}
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("expected 1 upstream call across 5 negative lookups, got %d", got)
	}
}

func TestCachedAdminChecker_TTLExpires(t *testing.T) {
	stub := &stubChecker{isAdmin: true}
	c := NewCachedAdminChecker(stub, 1*time.Minute)

	// Inject a manual clock so we don't have to sleep.
	clock := time.Now()
	c.now = func() time.Time { return clock }

	if _, err := c.IsAdmin(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	// Within TTL: cache hit.
	clock = clock.Add(30 * time.Second)
	if _, err := c.IsAdmin(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("expected 1 upstream call within TTL, got %d", got)
	}
	// Past TTL: re-fetch.
	clock = clock.Add(31 * time.Second)
	if _, err := c.IsAdmin(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	if got := stub.calls.Load(); got != 2 {
		t.Errorf("expected 2 upstream calls after TTL, got %d", got)
	}
}

func TestCachedAdminChecker_ErrorsNotCached(t *testing.T) {
	stub := &stubChecker{err: errors.New("upstream down")}
	c := NewCachedAdminChecker(stub, 1*time.Minute)

	for i := 0; i < 3; i++ {
		if _, err := c.IsAdmin(context.Background(), "u1"); err == nil {
			t.Fatalf("iter %d: expected error", i)
		}
	}
	if got := stub.calls.Load(); got != 3 {
		t.Errorf("errors must not be cached; expected 3 calls, got %d", got)
	}
}

func TestCachedAdminChecker_Invalidate_RefetchesOnNextCall(t *testing.T) {
	stub := &stubChecker{bySubject: map[string]bool{"u1": true}}
	c := NewCachedAdminChecker(stub, 1*time.Minute)

	ok, _ := c.IsAdmin(context.Background(), "u1")
	if !ok {
		t.Fatal("expected admin on first call")
	}

	// Simulate Keycloak revocation: change the upstream answer + invalidate.
	stub.bySubject["u1"] = false
	c.Invalidate("u1")

	ok, _ = c.IsAdmin(context.Background(), "u1")
	if ok {
		t.Fatal("expected non-admin after invalidate")
	}
	if got := stub.calls.Load(); got != 2 {
		t.Errorf("expected 2 upstream calls (cache invalidated), got %d", got)
	}
}

func TestCachedAdminChecker_InvalidateAll_RefetchesEveryone(t *testing.T) {
	stub := &stubChecker{bySubject: map[string]bool{"u1": true, "u2": true}}
	c := NewCachedAdminChecker(stub, 1*time.Minute)

	_, _ = c.IsAdmin(context.Background(), "u1")
	_, _ = c.IsAdmin(context.Background(), "u2")
	if got := stub.calls.Load(); got != 2 {
		t.Fatalf("setup: expected 2 calls, got %d", got)
	}

	c.InvalidateAll()

	_, _ = c.IsAdmin(context.Background(), "u1")
	_, _ = c.IsAdmin(context.Background(), "u2")
	if got := stub.calls.Load(); got != 4 {
		t.Errorf("expected 4 calls after InvalidateAll, got %d", got)
	}
}

func TestCachedAdminChecker_ZeroTTLFallsBackToDefault(t *testing.T) {
	stub := &stubChecker{isAdmin: true}
	c := NewCachedAdminChecker(stub, 0)
	if c.ttl != DefaultAdminTTL {
		t.Errorf("expected DefaultAdminTTL, got %s", c.ttl)
	}
}

func TestCachedAdminChecker_EmptySubject_NoUpstreamCall(t *testing.T) {
	stub := &stubChecker{isAdmin: true}
	c := NewCachedAdminChecker(stub, 1*time.Minute)
	ok, err := c.IsAdmin(context.Background(), "")
	if err != nil || ok {
		t.Fatalf("expected (false, nil) for empty subject, got (%v, %v)", ok, err)
	}
	if got := stub.calls.Load(); got != 0 {
		t.Errorf("empty subject must not hit upstream, got %d calls", got)
	}
}

// ─── End-to-end: RequireRole + RequireLiveAdmin composition ────────────────

// This is the closest in-process repro of the GAP-1 attack: a JWT carries
// `admin` in realm_access.roles (so RequireRole passes), but the live check
// against Keycloak reports no admin (GAP-1 closed → 403).
func TestRequireRole_then_RequireLiveAdmin_DemotedJWT_Denied(t *testing.T) {
	stub := &stubChecker{isAdmin: false}
	r := newGin(
		withIdentity(&Identity{Subject: "demoted", Roles: []string{"admin", "user"}}),
		RequireRole("admin"),
		RequireLiveAdmin(stub),
	)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for stale-admin JWT, got %d", w.Code)
	}
}

// Non-admin must short-circuit at RequireRole BEFORE hitting Keycloak — this
// matters operationally because a flood of non-admin probes should not turn
// into a flood of Keycloak round trips.
func TestRequireRole_then_RequireLiveAdmin_NonAdmin_ShortCircuits(t *testing.T) {
	stub := &stubChecker{isAdmin: false}
	r := newGin(
		withIdentity(&Identity{Subject: "u1", Roles: []string{"user"}}),
		RequireRole("admin"),
		RequireLiveAdmin(stub),
	)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d", w.Code)
	}
	if got := stub.calls.Load(); got != 0 {
		t.Errorf("non-admin must short-circuit before live check; got %d upstream calls", got)
	}
}

// Current admin passes both gates.
func TestRequireRole_then_RequireLiveAdmin_CurrentAdmin_Allowed(t *testing.T) {
	stub := &stubChecker{isAdmin: true}
	r := newGin(
		withIdentity(&Identity{Subject: "admin-user", Roles: []string{"admin", "user"}}),
		RequireRole("admin"),
		RequireLiveAdmin(stub),
	)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for current admin, got %d", w.Code)
	}
}

// contains is a tiny substring helper so this file doesn't pull in strings
// (keeps the test imports tight).
func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
