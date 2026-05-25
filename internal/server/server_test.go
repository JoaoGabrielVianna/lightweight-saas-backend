package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// fakeProvider is a stand-in auth.AuthProvider that returns a pre-built
// identity (or an error) without doing any real validation. Routing tests
// only need to confirm "the middleware ran and gated the route"; the
// provider's signature-check semantics live in internal/auth's own tests.
type fakeProvider struct {
	id  *auth.Identity
	err error
}

func (f *fakeProvider) ValidateToken(_ context.Context, _ string) (*auth.Identity, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.id, nil
}

// keycloakStub spins up an httptest.Server that handles the two endpoints
// SetupIdentity-backed handlers actually call: the OIDC token endpoint and
// the admin REST surface. It exists so router tests don't pay DNS-resolution
// time waiting for kc.local to fail. The body is intentionally minimal —
// the test only needs to confirm the auth/role/live-admin chain accepted
// the request; whatever the handler does next is out of scope.
func keycloakStub(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/saas/protocol/openid-connect/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"stub","expires_in":300,"token_type":"Bearer"}`))
	})
	mux.HandleFunc("/admin/realms/saas/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// fakeAdminChecker lets a router test choose whether the live-admin gate
// should pass, deny, or error. Captures the subject it was asked about so
// the test can assert RequireLiveAdmin actually fired.
type fakeAdminChecker struct {
	allow       bool
	err         error
	lastSubject string
	calls       int
}

func (f *fakeAdminChecker) IsAdmin(_ context.Context, subject string) (bool, error) {
	f.calls++
	f.lastSubject = subject
	return f.allow, f.err
}

func newGin() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}

// withRepoCwd switches the process working directory to the repository
// root so handlers that read from on-disk asset paths (web/admin/...,
// web/dev/...) find their files. Walks up from the current test directory
// until it finds the module's go.mod.
func withRepoCwd(t *testing.T) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			t.Chdir(dir)
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root (no go.mod found walking up)")
		}
		dir = parent
	}
}

// ---------------------------------------------------------------------------
// healthHandler / Server constructor
// ---------------------------------------------------------------------------

func TestHealthHandler_ReturnsOK(t *testing.T) {
	r := newGin()
	r.GET("/health", healthHandler)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("body[status] = %q, want ok", body["status"])
	}
}

func TestNewServer_AppliesGinConfig(t *testing.T) {
	prevMode := gin.Mode()
	t.Cleanup(func() { gin.SetMode(prevMode) })

	cfg := &config.Config{GinLogEnabled: false, GinAccessLogEnabled: true}
	s := NewServer(&gorm.DB{}, cfg)

	if s == nil || s.router == nil || s.cfg != cfg {
		t.Fatal("NewServer did not initialize Server fields")
	}
	if gin.Mode() != gin.ReleaseMode {
		t.Errorf("gin.Mode = %q, want %q after NewServer with GinLogEnabled=false", gin.Mode(), gin.ReleaseMode)
	}
}

// ---------------------------------------------------------------------------
// SetupUser — pure wiring, no DB I/O
// ---------------------------------------------------------------------------

func TestSetupUser_WiresHandler(t *testing.T) {
	// NewRepository / NewService / NewHandler are pure constructors that
	// only stash pointers. A zero-value gorm.DB is enough to confirm the
	// wiring assembled without panicking.
	h := SetupUser(&gorm.DB{})
	if h == nil {
		t.Fatal("SetupUser returned nil Handler")
	}
}

// ---------------------------------------------------------------------------
// SetupIdentity — gating on admin client credentials
// ---------------------------------------------------------------------------

func TestSetupIdentity_DisabledWhenBothEmpty(t *testing.T) {
	cfg := &config.Config{
		KeycloakAdminClientID:     "",
		KeycloakAdminClientSecret: "",
	}
	h, checker, err := SetupIdentity(cfg)
	if err != nil {
		t.Fatalf("expected nil error when both creds empty, got %v", err)
	}
	if h != nil || checker != nil {
		t.Errorf("expected (nil, nil) handlers, got h=%v checker=%v", h, checker)
	}
}

func TestSetupIdentity_RejectsHalfConfigured(t *testing.T) {
	t.Run("id set, secret empty", func(t *testing.T) {
		cfg := &config.Config{
			KeycloakURL:               "http://kc.local",
			KeycloakRealm:             "saas",
			KeycloakAdminClientID:     "kc-admin",
			KeycloakAdminClientSecret: "",
		}
		_, _, err := SetupIdentity(cfg)
		if err == nil {
			t.Fatal("expected error on half-configured admin client")
		}
		if !strings.Contains(err.Error(), "half-configured") {
			t.Errorf("error = %q, want substring 'half-configured'", err)
		}
	})
	t.Run("secret set, id empty", func(t *testing.T) {
		cfg := &config.Config{
			KeycloakURL:               "http://kc.local",
			KeycloakRealm:             "saas",
			KeycloakAdminClientID:     "",
			KeycloakAdminClientSecret: "shh",
		}
		_, _, err := SetupIdentity(cfg)
		if err == nil {
			t.Fatal("expected error on half-configured admin client")
		}
	})
}

func TestSetupIdentity_BuildsHandlerAndCacheWhenFullyConfigured(t *testing.T) {
	cfg := &config.Config{
		KeycloakURL:               "http://kc.local",
		KeycloakRealm:             "saas",
		KeycloakClientID:          "saas-api",
		KeycloakAdminBaseURL:      "", // exercises fallback to KeycloakURL
		KeycloakAdminClientID:     "kc-admin",
		KeycloakAdminClientSecret: "shh",
		AdminLiveCheckTTLSeconds:  5,
	}
	h, checker, err := SetupIdentity(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h == nil {
		t.Error("expected non-nil identity Handler")
	}
	if checker == nil {
		t.Error("expected non-nil AdminChecker")
	}
}

func TestSetupIdentity_UsesAdminBaseURLWhenSet(t *testing.T) {
	cfg := &config.Config{
		KeycloakURL:               "http://issuer.example",
		KeycloakRealm:             "saas",
		KeycloakClientID:          "saas-api",
		KeycloakAdminBaseURL:      "http://kc.internal:8080",
		KeycloakAdminClientID:     "kc-admin",
		KeycloakAdminClientSecret: "shh",
	}
	h, checker, err := SetupIdentity(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h == nil || checker == nil {
		t.Fatal("expected handler+checker wired")
	}
}

// ---------------------------------------------------------------------------
// adminCheckerFromProvider — translates provider roles to AdminChecker
// ---------------------------------------------------------------------------

// stubProvider satisfies the identity.IdentityProvider interface but only
// implements ListUserRoles — that's all adminCheckerFromProvider touches.
// Embedding identity.IdentityProvider satisfies the contract via panicking
// fallthrough; the tests never call those methods.
type stubProvider struct {
	identity.IdentityProvider
	roles []identity.Role
	err   error
}

func (s *stubProvider) ListUserRoles(_ context.Context, _ string) ([]identity.Role, error) {
	return s.roles, s.err
}

func TestAdminCheckerFromProvider_TrueWhenAdminRolePresent(t *testing.T) {
	checker := adminCheckerFromProvider(&stubProvider{
		roles: []identity.Role{
			{Name: "user"},
			{Name: adminRoleName},
		},
	})
	ok, err := checker.IsAdmin(context.Background(), "sub-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected IsAdmin=true when admin role present")
	}
}

func TestAdminCheckerFromProvider_FalseWhenAdminRoleAbsent(t *testing.T) {
	checker := adminCheckerFromProvider(&stubProvider{
		roles: []identity.Role{{Name: "viewer"}, {Name: "editor"}},
	})
	ok, err := checker.IsAdmin(context.Background(), "sub-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected IsAdmin=false when admin role absent")
	}
}

func TestAdminCheckerFromProvider_PropagatesError(t *testing.T) {
	want := errors.New("upstream down")
	checker := adminCheckerFromProvider(&stubProvider{err: want})
	ok, err := checker.IsAdmin(context.Background(), "sub-3")
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if ok {
		t.Fatal("expected IsAdmin=false on error")
	}
}

// ---------------------------------------------------------------------------
// SetupRouter — composition + auth gating + admin gating + 404 when off
// ---------------------------------------------------------------------------

func TestSetupRouter_MeRequiresAuth(t *testing.T) {
	r := newGin()
	userHandler := SetupUser(&gorm.DB{})
	provider := &fakeProvider{err: auth.ErrInvalidToken}

	SetupRouter(r, userHandler, nil, nil, provider, nil)

	// No Authorization header → 401, regardless of provider behaviour.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/me", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when Authorization header missing", w.Code)
	}

	// Bad token → provider rejects → 401.
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer rotten")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 on provider error", w.Code)
	}
}

func TestSetupRouter_AdminRoutesAbsentWhenIdentityNil(t *testing.T) {
	r := newGin()
	userHandler := SetupUser(&gorm.DB{})
	provider := &fakeProvider{id: &auth.Identity{Subject: "s", Roles: []string{"admin"}, ExpiresAt: time.Now().Add(time.Hour)}}

	SetupRouter(r, userHandler, nil, nil, provider, nil)

	// /admin/users should 404 — the group was never registered.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	req.Header.Set("Authorization", "Bearer t")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when identity handler nil", w.Code)
	}
}

func TestSetupRouter_AdminRoutesMountedWhenIdentityProvided(t *testing.T) {
	// We don't exercise the identity handler's full logic here; we just
	// want to verify that the admin group is registered, gated by
	// RequireAuth and RequireRole("admin"), and goes through
	// RequireLiveAdmin when adminChecker is non-nil. A real Handler is
	// instantiated through identity.NewHandler so the actual route
	// methods exist.
	stubURL := keycloakStub(t)
	cfg := &config.Config{
		KeycloakURL:               stubURL,
		KeycloakRealm:             "saas",
		KeycloakClientID:          "saas-api",
		KeycloakAdminClientID:     "kc-admin",
		KeycloakAdminClientSecret: "shh",
		AdminLiveCheckTTLSeconds:  5,
	}
	identityHandler, _, err := SetupIdentity(cfg)
	if err != nil {
		t.Fatalf("SetupIdentity: %v", err)
	}

	t.Run("rejects caller without admin role with 403", func(t *testing.T) {
		r := newGin()
		provider := &fakeProvider{id: &auth.Identity{Subject: "s1", Roles: []string{"viewer"}, ExpiresAt: time.Now().Add(time.Hour)}}
		checker := &fakeAdminChecker{allow: true}
		SetupRouter(r, SetupUser(&gorm.DB{}), identityHandler, nil, provider, checker)

		req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
		req.Header.Set("Authorization", "Bearer t")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (RequireRole denial)", w.Code)
		}
		if checker.calls != 0 {
			t.Errorf("RequireLiveAdmin should not run after RequireRole denial, but checker was called %d time(s)", checker.calls)
		}
	})

	t.Run("rejects when live-admin check says no", func(t *testing.T) {
		r := newGin()
		provider := &fakeProvider{id: &auth.Identity{Subject: "s2", Roles: []string{"admin"}, ExpiresAt: time.Now().Add(time.Hour)}}
		checker := &fakeAdminChecker{allow: false}
		SetupRouter(r, SetupUser(&gorm.DB{}), identityHandler, nil, provider, checker)

		req := httptest.NewRequest(http.MethodGet, "/admin/roles", nil)
		req.Header.Set("Authorization", "Bearer t")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (live-admin denial)", w.Code)
		}
		if checker.calls != 1 || checker.lastSubject != "s2" {
			t.Errorf("expected checker called once for subject s2, got calls=%d subject=%q", checker.calls, checker.lastSubject)
		}
	})

	t.Run("admin group passes auth gates and reaches handler", func(t *testing.T) {
		r := newGin()
		provider := &fakeProvider{id: &auth.Identity{Subject: "s3", Roles: []string{"admin"}, ExpiresAt: time.Now().Add(time.Hour)}}
		checker := &fakeAdminChecker{allow: true}
		SetupRouter(r, SetupUser(&gorm.DB{}), identityHandler, nil, provider, checker)

		// /admin/users will fail at the handler (no real Keycloak), but the
		// status will be something other than 401/403/404, proving the
		// middleware chain passed through. We assert "not 401/403/404".
		req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
		req.Header.Set("Authorization", "Bearer t")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden || w.Code == http.StatusNotFound {
			t.Fatalf("status = %d; expected the auth chain to pass and the handler to handle/upstream-error", w.Code)
		}
		if checker.calls != 1 {
			t.Errorf("expected live-admin checker called once, got %d", checker.calls)
		}
	})

	t.Run("admin group skips live check when adminChecker is nil", func(t *testing.T) {
		// Wire identity handler but no checker — exercises the SetupRouter
		// branch where the RequireLiveAdmin middleware is not mounted.
		r := newGin()
		provider := &fakeProvider{id: &auth.Identity{Subject: "s4", Roles: []string{"admin"}, ExpiresAt: time.Now().Add(time.Hour)}}
		SetupRouter(r, SetupUser(&gorm.DB{}), identityHandler, nil, provider, nil)

		req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
		req.Header.Set("Authorization", "Bearer t")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		// As above — handler will fail upstream, but the auth chain must
		// have allowed us to reach it.
		if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden || w.Code == http.StatusNotFound {
			t.Fatalf("status = %d; expected auth chain (without live check) to pass", w.Code)
		}
	})
}

func TestSetupRouter_AuditEventsRouteGatedOnHandlerPresence(t *testing.T) {
	stubURL := keycloakStub(t)
	cfg := &config.Config{
		KeycloakURL:               stubURL,
		KeycloakRealm:             "saas",
		KeycloakClientID:          "saas-api",
		KeycloakAdminClientID:     "kc-admin",
		KeycloakAdminClientSecret: "shh",
	}
	identityHandler, _, err := SetupIdentity(cfg)
	if err != nil {
		t.Fatalf("SetupIdentity: %v", err)
	}
	provider := &fakeProvider{id: &auth.Identity{Subject: "s", Roles: []string{"admin"}, ExpiresAt: time.Now().Add(time.Hour)}}
	checker := &fakeAdminChecker{allow: true}

	t.Run("absent when audit handler nil", func(t *testing.T) {
		r := newGin()
		SetupRouter(r, SetupUser(&gorm.DB{}), identityHandler, nil, provider, checker)
		req := httptest.NewRequest(http.MethodGet, "/admin/audit-events", nil)
		req.Header.Set("Authorization", "Bearer t")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 when audit handler nil", w.Code)
		}
	})

	t.Run("registered when audit handler wired", func(t *testing.T) {
		r := newGin()
		mem := audit.NewMemoryRecorder(4)
		auditHandler := NewAuditHandler(mem)
		SetupRouter(r, SetupUser(&gorm.DB{}), identityHandler, auditHandler, provider, checker)

		req := httptest.NewRequest(http.MethodGet, "/admin/audit-events", nil)
		req.Header.Set("Authorization", "Bearer t")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if cap, _ := body["capacity"].(float64); int(cap) != 4 {
			t.Errorf("capacity = %v, want 4", body["capacity"])
		}
	})
}

// TestSetupRouter_AllExpectedAdminRoutesRegistered ensures the admin group
// mounts every documented verb. If someone removes a route without intending
// to, this test will catch the regression. We don't care about responses,
// only that 404 is NOT returned (the admin group exists and matches).
func TestSetupRouter_AllExpectedAdminRoutesRegistered(t *testing.T) {
	stubURL := keycloakStub(t)
	cfg := &config.Config{
		KeycloakURL:               stubURL,
		KeycloakRealm:             "saas",
		KeycloakClientID:          "saas-api",
		KeycloakAdminClientID:     "kc-admin",
		KeycloakAdminClientSecret: "shh",
	}
	identityHandler, _, err := SetupIdentity(cfg)
	if err != nil {
		t.Fatalf("SetupIdentity: %v", err)
	}

	r := newGin()
	provider := &fakeProvider{id: &auth.Identity{Subject: "s", Roles: []string{"admin"}, ExpiresAt: time.Now().Add(time.Hour)}}
	checker := &fakeAdminChecker{allow: true}
	SetupRouter(r, SetupUser(&gorm.DB{}), identityHandler, nil, provider, checker)

	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/admin/users"},
		{http.MethodGet, "/admin/users/u1"},
		{http.MethodGet, "/admin/users/u1/roles"},
		{http.MethodGet, "/admin/users/u1/sessions"},
		{http.MethodGet, "/admin/roles"},
		{http.MethodGet, "/admin/roles/admin"},
		{http.MethodGet, "/admin/roles/admin/users"},
		{http.MethodGet, "/admin/sessions"},
		{http.MethodGet, "/admin/invitations"},
		{http.MethodPost, "/admin/roles"},
		{http.MethodPost, "/admin/invitations"},
		{http.MethodPost, "/admin/users/invite"},
		{http.MethodPatch, "/admin/users/u1"},
		{http.MethodPatch, "/admin/roles/admin"},
		{http.MethodPost, "/admin/users/u1/roles"},
		{http.MethodPost, "/admin/users/u1/reset-password"},
		{http.MethodPost, "/admin/invitations/inv1/resend"},
		{http.MethodDelete, "/admin/users/u1"},
		{http.MethodDelete, "/admin/users/u1/roles/admin"},
		{http.MethodDelete, "/admin/users/u1/sessions"},
		{http.MethodDelete, "/admin/roles/admin"},
		{http.MethodDelete, "/admin/sessions/sid"},
		{http.MethodDelete, "/admin/invitations/inv1"},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer t")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Errorf("%s %s -> 404; expected route to be registered", tc.method, tc.path)
		}
	}
}

// ---------------------------------------------------------------------------
// mountPlayground / mountAdminConsole — gated by DevPlaygroundEnabled
// ---------------------------------------------------------------------------

func TestMountPlayground_DisabledByDefault(t *testing.T) {
	r := newGin()
	provider := &fakeProvider{id: &auth.Identity{Subject: "s"}}
	mountPlayground(r, &config.Config{DevPlaygroundEnabled: false}, provider)

	for _, path := range []string{"/dev/auth", "/dev/auth/auth.js", "/dev/auth/styles.css", "/dev/auth/config.json", "/auth/debug"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s -> %d, want 404 when DevPlaygroundEnabled=false", path, w.Code)
		}
	}
}

func TestMountAdminConsole_DisabledByDefault(t *testing.T) {
	r := newGin()
	mountAdminConsole(r, &config.Config{DevPlaygroundEnabled: false})

	for _, path := range []string{"/admin", "/admin/config.json", "/admin/static/css/app.css", "/admin/docs/INDEX.md"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s -> %d, want 404 when DevPlaygroundEnabled=false", path, w.Code)
		}
	}
}

func TestMountAdminConsole_ConfigJSONShape(t *testing.T) {
	r := newGin()
	cfg := &config.Config{
		DevPlaygroundEnabled:  true,
		KeycloakURL:           "http://kc.local",
		KeycloakRealm:         "saas",
		DevPlaygroundClientID: "saas-dev-playground",
		Port:                  "8080",
	}
	mountAdminConsole(r, cfg)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/config.json", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	wantPairs := map[string]any{
		"keycloakUrl": "http://kc.local",
		"realm":       "saas",
		"clientId":    "saas-dev-playground",
		"apiBase":     "",
		"redirectUri": "http://localhost:8080/admin",
		"devTools":    true,
		"apiExplorer": true,
	}
	for k, v := range wantPairs {
		if body[k] != v {
			t.Errorf("config[%q] = %v, want %v", k, body[k], v)
		}
	}
}

// In production mode (ADMIN_CONSOLE_ENABLED=true, DEV_PLAYGROUND_ENABLED=false)
// the console must serve AND the runtime config must advertise devTools=false
// + apiExplorer=false so the SPA hides Playground and API Explorer.
func TestMountAdminConsole_ProductionModeHidesDevTools(t *testing.T) {
	r := newGin()
	cfg := &config.Config{
		AdminConsoleEnabled:   true,
		DevPlaygroundEnabled:  false,
		KeycloakURL:           "http://kc.local",
		KeycloakRealm:         "saas",
		DevPlaygroundClientID: "saas-dev-playground",
		Port:                  "8080",
	}
	mountAdminConsole(r, cfg)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/config.json", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (admin console enabled)", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["devTools"] != false {
		t.Errorf("devTools = %v, want false in production mode", body["devTools"])
	}
	if body["apiExplorer"] != false {
		t.Errorf("apiExplorer = %v, want false in production mode", body["apiExplorer"])
	}
}

// When both flags are false the admin console must NOT mount — same fail-safe
// posture as the legacy DevPlaygroundEnabled gate.
func TestMountAdminConsole_BothFlagsFalseDoesNotMount(t *testing.T) {
	r := newGin()
	mountAdminConsole(r, &config.Config{
		AdminConsoleEnabled:  false,
		DevPlaygroundEnabled: false,
	})

	for _, path := range []string{"/admin", "/admin/config.json"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s -> %d, want 404 when neither admin nor playground enabled", path, w.Code)
		}
	}
}

func TestMountAdminConsole_PathTraversalRejected(t *testing.T) {
	r := newGin()
	mountAdminConsole(r, &config.Config{DevPlaygroundEnabled: true})

	for _, path := range []string{
		"/admin/static/..%2fetc/passwd",
		"/admin/static/../something",
		"/admin/docs/../secret.md",
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s -> %d, want 403 (path-traversal defense)", path, w.Code)
		}
	}
}

func TestMountAdminConsole_DocsRejectsNonMarkdown(t *testing.T) {
	r := newGin()
	mountAdminConsole(r, &config.Config{DevPlaygroundEnabled: true})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/docs/INDEX.html", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for non-.md docs path", w.Code)
	}
}

func TestMountAdminConsole_DocsServesEmbeddedMarkdown(t *testing.T) {
	r := newGin()
	mountAdminConsole(r, &config.Config{DevPlaygroundEnabled: true})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/docs/INDEX.md", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for embedded /admin/docs/INDEX.md", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("Content-Type = %q, want text/markdown*", ct)
	}
	if w.Body.Len() == 0 {
		t.Error("expected non-empty markdown body")
	}
}

func TestMountAdminConsole_DocsMissingReturns404(t *testing.T) {
	r := newGin()
	mountAdminConsole(r, &config.Config{DevPlaygroundEnabled: true})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/docs/does-not-exist.md", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestMountAdminConsole_ServesIndexAndStaticAssetsFromDisk(t *testing.T) {
	withRepoCwd(t)
	r := newGin()
	mountAdminConsole(r, &config.Config{
		DevPlaygroundEnabled: true,
		KeycloakURL:          "http://kc.local",
		KeycloakRealm:        "saas",
	})

	// Index page
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/admin status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("/admin Content-Type = %q, want text/html*", ct)
	}

	// A representative static asset for each MIME branch we exercise.
	// We don't hardcode filenames: discover one .css and one .js under
	// web/admin/static so the test stays robust to file renames.
	cssPath := firstAssetWithExt(t, "web/admin/static", ".css")
	jsPath := firstAssetWithExt(t, "web/admin/static", ".js")

	for _, tc := range []struct {
		urlPath, wantPrefix string
	}{
		{"/admin/static/" + cssPath, "text/css"},
		{"/admin/static/" + jsPath, "application/javascript"},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.urlPath, nil))
		if w.Code != http.StatusOK {
			t.Errorf("%s -> %d, want 200", tc.urlPath, w.Code)
			continue
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, tc.wantPrefix) {
			t.Errorf("%s Content-Type = %q, want prefix %q", tc.urlPath, ct, tc.wantPrefix)
		}
	}
}

func TestMountPlayground_ConfigJSONShape(t *testing.T) {
	withRepoCwd(t)
	r := newGin()
	cfg := &config.Config{
		DevPlaygroundEnabled:  true,
		KeycloakURL:           "http://kc.local",
		KeycloakRealm:         "saas",
		DevPlaygroundClientID: "saas-dev-playground",
		KeycloakClientID:      "saas-api",
		Port:                  "8080",
	}
	provider := &fakeProvider{err: errors.New("unused")}
	mountPlayground(r, cfg, provider)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/dev/auth/config.json", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["redirectUri"] != "http://localhost:8080/dev/auth" {
		t.Errorf("redirectUri = %v, want http://localhost:8080/dev/auth", body["redirectUri"])
	}
}

// ---------------------------------------------------------------------------
// authDebugHandler — pure-handler unit tests
// ---------------------------------------------------------------------------

func TestAuthDebugHandler_NoToken(t *testing.T) {
	cfg := &config.Config{KeycloakURL: "http://kc.local", KeycloakRealm: "saas", KeycloakClientID: "saas-api"}
	provider := &fakeProvider{err: auth.ErrInvalidToken}
	r := newGin()
	r.GET("/auth/debug", authDebugHandler(cfg, provider))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/debug", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (debug endpoint always 200)", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["valid"] != false {
		t.Errorf("valid = %v, want false", body["valid"])
	}
	if r, _ := body["reason"].(string); !strings.Contains(r, "no token supplied") {
		t.Errorf("reason = %q, want 'no token supplied'", r)
	}
	if body["issuer"] != "http://kc.local/realms/saas" {
		t.Errorf("issuer = %v, want http://kc.local/realms/saas", body["issuer"])
	}
	// Allowed-clients falls back to the primary client id when no whitelist.
	clients, _ := body["allowed_clients"].([]any)
	if len(clients) != 1 || clients[0] != "saas-api" {
		t.Errorf("allowed_clients = %v, want [saas-api]", clients)
	}
}

func TestAuthDebugHandler_AllowedClientsExplicit(t *testing.T) {
	cfg := &config.Config{
		KeycloakURL:              "http://kc.local",
		KeycloakRealm:            "saas",
		KeycloakClientID:         "saas-api",
		KeycloakAllowedClientIDs: []string{"client-a", "client-b"},
	}
	provider := &fakeProvider{err: auth.ErrInvalidToken}
	r := newGin()
	r.GET("/auth/debug", authDebugHandler(cfg, provider))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/debug", nil))

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	clients, _ := body["allowed_clients"].([]any)
	if len(clients) != 2 {
		t.Errorf("allowed_clients = %v, want 2 entries", clients)
	}
}

func TestAuthDebugHandler_DecodesPayloadEvenOnValidationFail(t *testing.T) {
	cfg := &config.Config{KeycloakURL: "http://kc.local", KeycloakRealm: "saas", KeycloakClientID: "saas-api"}
	provider := &fakeProvider{err: errors.New("signature invalid")}
	r := newGin()
	r.GET("/auth/debug", authDebugHandler(cfg, provider))

	// Hand-rolled JWT: header.payload.signature — only payload is decoded.
	// Payload encodes azp, sub, email, exp (past), iat, realm/resource roles.
	tok := buildTestJWT(t, map[string]any{
		"azp":   "client-a",
		"sub":   "user-1",
		"email": "u@example.com",
		"exp":   float64(time.Now().Add(-time.Hour).Unix()),
		"iat":   float64(time.Now().Add(-2 * time.Hour).Unix()),
		"aud":   []any{"saas-api", "account"},
		"realm_access": map[string]any{
			"roles": []any{"realm-role"},
		},
		"resource_access": map[string]any{
			"saas-api": map[string]any{
				"roles": []any{"client-role", "realm-role"}, // dedupe path
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/auth/debug?token="+tok, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["received_azp"] != "client-a" {
		t.Errorf("received_azp = %v, want client-a", body["received_azp"])
	}
	if body["received_sub"] != "user-1" {
		t.Errorf("received_sub = %v, want user-1", body["received_sub"])
	}
	if body["expired"] != true {
		t.Errorf("expired = %v, want true (exp in past)", body["expired"])
	}
	if body["valid"] != false {
		t.Errorf("valid = %v, want false (provider rejected)", body["valid"])
	}
	if r, _ := body["reason"].(string); !strings.Contains(r, "signature invalid") {
		t.Errorf("reason = %q, want provider error verbatim", r)
	}
	roles, _ := body["roles"].([]any)
	if len(roles) != 2 || roles[0] != "realm-role" || roles[1] != "client-role" {
		t.Errorf("roles = %v, want [realm-role client-role] (deduped, realm first)", roles)
	}
	aud, _ := body["aud"].([]any)
	if len(aud) != 2 {
		t.Errorf("aud = %v, want 2 entries", aud)
	}
}

func TestAuthDebugHandler_AcceptsAuthorizationHeader(t *testing.T) {
	cfg := &config.Config{KeycloakURL: "http://kc.local", KeycloakRealm: "saas", KeycloakClientID: "saas-api"}
	provider := &fakeProvider{id: &auth.Identity{Subject: "ok", ExpiresAt: time.Now().Add(time.Hour)}}
	r := newGin()
	r.GET("/auth/debug", authDebugHandler(cfg, provider))

	tok := buildTestJWT(t, map[string]any{"sub": "abc"})
	req := httptest.NewRequest(http.MethodGet, "/auth/debug", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["valid"] != true {
		t.Errorf("valid = %v, want true", body["valid"])
	}
}

func TestAuthDebugHandler_MalformedTokenStillReturns200(t *testing.T) {
	cfg := &config.Config{KeycloakURL: "http://kc.local", KeycloakRealm: "saas", KeycloakClientID: "saas-api"}
	provider := &fakeProvider{err: errors.New("malformed")}
	r := newGin()
	r.GET("/auth/debug", authDebugHandler(cfg, provider))

	req := httptest.NewRequest(http.MethodGet, "/auth/debug?token=not-a-jwt", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (debug always 200)", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Smaller pure-helper sweeps
// ---------------------------------------------------------------------------

func TestBearerFromHeader(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"Basic abcd", ""},
		{"Bearer abc", "abc"},
		{"Bearer   token  ", "token"},
		{"bearer lower", "lower"},
	}
	for _, tc := range cases {
		if got := bearerFromHeader(tc.in); got != tc.want {
			t.Errorf("bearerFromHeader(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeAudClaim(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{"nil", nil, []string{}},
		{"empty string", "", []string{}},
		{"single string", "aud1", []string{"aud1"}},
		{"array", []any{"a", "b"}, []string{"a", "b"}},
		{"array with non-string + empty", []any{"a", "", 5, "b"}, []string{"a", "b"}},
		{"unrelated type", 42, []string{}},
	}
	for _, tc := range cases {
		got := normalizeAudClaim(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: got[%d] = %q, want %q", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

func TestExtractRolesFromClaims(t *testing.T) {
	claims := map[string]any{
		"realm_access": map[string]any{
			"roles": []any{"admin", "viewer"},
		},
		"resource_access": map[string]any{
			"primary": map[string]any{
				"roles": []any{"editor", "admin"}, // 'admin' duplicates realm-level
			},
			"other": map[string]any{
				"roles": []any{"unused"},
			},
		},
	}
	got := extractRolesFromClaims(claims, "primary")
	want := []string{"admin", "viewer", "editor"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExtractRolesFromClaims_EmptyOnAbsentClaims(t *testing.T) {
	got := extractRolesFromClaims(map[string]any{}, "primary")
	if got == nil {
		t.Fatal("extractRolesFromClaims returned nil; contract requires empty slice")
	}
	if len(got) != 0 {
		t.Errorf("got %v, want []", got)
	}
}

func TestFormatTimestampAndExp(t *testing.T) {
	// non-number claim → empty
	if iso, _ := formatTimestampClaim("not-a-number"); iso != "" {
		t.Errorf("formatTimestampClaim(string) = %q, want empty", iso)
	}
	if iso, expired := formatExpClaim("not-a-number"); iso != "" || expired {
		t.Errorf("formatExpClaim(string) = (%q,%v), want empty", iso, expired)
	}

	past := float64(time.Now().Add(-time.Hour).Unix())
	isoPast, expired := formatExpClaim(past)
	if isoPast == "" || !expired {
		t.Errorf("formatExpClaim(past) = (%q,%v), want (non-empty, true)", isoPast, expired)
	}
	future := float64(time.Now().Add(time.Hour).Unix())
	if _, expired := formatExpClaim(future); expired {
		t.Error("formatExpClaim(future).expired = true, want false")
	}
}

func TestDecodeJWTPayload(t *testing.T) {
	t.Run("malformed segments", func(t *testing.T) {
		if _, err := decodeJWTPayload("only.two"); err == nil {
			t.Error("expected error for 2-segment token")
		}
		if _, err := decodeJWTPayload("a.b.c"); err == nil {
			t.Error("expected error for non-base64 payload")
		}
	})
	t.Run("valid payload", func(t *testing.T) {
		tok := buildTestJWT(t, map[string]any{"sub": "abc"})
		claims, err := decodeJWTPayload(tok)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if claims["sub"] != "abc" {
			t.Errorf("sub = %v, want abc", claims["sub"])
		}
	})
}

// ---------------------------------------------------------------------------
// Server.SetupRoutes — integrating playground+admin gating
// ---------------------------------------------------------------------------

func TestServerSetupRoutes_HealthRegisteredWhenPlaygroundOff(t *testing.T) {
	cfg := &config.Config{GinLogEnabled: false}
	s := NewServer(&gorm.DB{}, cfg)
	provider := &fakeProvider{id: &auth.Identity{Subject: "s"}}
	s.SetupRoutes(SetupUser(&gorm.DB{}), nil, nil, provider, nil)

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/health status = %d, want 200", w.Code)
	}

	// Playground/admin must be 404 when not enabled.
	for _, p := range []string{"/dev/auth", "/admin"} {
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404 when playground disabled", p, w.Code)
		}
	}
}

// ---------------------------------------------------------------------------
// Server.Start — smoke test that the goroutine actually serves a request
// ---------------------------------------------------------------------------

func TestServer_StartServesHealth(t *testing.T) {
	// Pick a free port — Start uses ":<port>" so we resolve a 0-allocated
	// port first, then ask Start to bind it. The race window is small;
	// retry once if hit.
	port := freePort(t)

	cfg := &config.Config{GinLogEnabled: false}
	s := NewServer(&gorm.DB{}, cfg)
	provider := &fakeProvider{id: &auth.Identity{Subject: "s"}}
	s.SetupRoutes(SetupUser(&gorm.DB{}), nil, nil, provider, nil)

	go s.Start(port)

	// Poll /health for up to 2 seconds — Gin's Run is async.
	url := "http://127.0.0.1:" + port + "/health"
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			lastErr = errors.New("status " + resp.Status)
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server did not become ready on port %s within 2s: %v", port, lastErr)
}

// ---------------------------------------------------------------------------
// JWT + filesystem helpers
// ---------------------------------------------------------------------------

// buildTestJWT mints a 3-segment JWT with a fixed header and the given
// claims payload. The signature segment is the literal "sig"; nothing in
// the debug code-path verifies it.
func buildTestJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": "none", "typ": "JWT"}
	encode := func(v map[string]any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return base64URLNoPad(b)
	}
	return encode(header) + "." + encode(claims) + ".sig"
}

func base64URLNoPad(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// firstAssetWithExt finds the first file with the given extension under
// root, returning its path relative to root (forward slashes). Skipped if
// none found.
func firstAssetWithExt(t *testing.T, root, ext string) string {
	t.Helper()
	var found string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ext) && found == "" {
			rel, _ := filepath.Rel(root, path)
			found = filepath.ToSlash(rel)
		}
		return nil
	})
	if err != nil {
		t.Skipf("walk %s: %v", root, err)
	}
	if found == "" {
		t.Skipf("no %s asset found under %s — skipping asset content-type check", ext, root)
	}
	return found
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	_, p, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatalf("split host: %v", err)
	}
	return p
}
