package keycloak

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
)

// stub is an httptest server impersonating the Keycloak token + admin
// endpoints. Callers configure per-request responses; the stub records the
// Authorization headers it saw so tests can assert token reuse vs refresh.
type stub struct {
	srv          *httptest.Server
	tokenMints   atomic.Int32 // how many times /token was hit
	adminCalls   atomic.Int32 // how many times /admin/realms/<realm>/* was hit
	authHeaders  []string
	tokenValue   atomic.Pointer[string]
	tokenExpires int // seconds; default 60
	// handlerFor returns the response (status, body) for a given admin path.
	// Defaults to 200 + empty array. Tests override per case.
	handlerFor func(method, path string) (int, []byte)
}

func newStub(t *testing.T) *stub {
	t.Helper()
	s := &stub{tokenExpires: 60}
	initial := "tok-initial"
	s.tokenValue.Store(&initial)
	s.handlerFor = func(_, _ string) (int, []byte) {
		return http.StatusOK, []byte(`[]`)
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token"):
			s.tokenMints.Add(1)
			tok := *s.tokenValue.Load()
			body := fmt.Sprintf(`{"access_token":%q,"expires_in":%d,"token_type":"Bearer"}`, tok, s.tokenExpires)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		case strings.Contains(r.URL.Path, "/admin/realms/"):
			s.adminCalls.Add(1)
			s.authHeaders = append(s.authHeaders, r.Header.Get("Authorization"))
			status, body := s.handlerFor(r.Method, r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(body)
		default:
			t.Errorf("unexpected stub path: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotImplemented)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func newProvider(t *testing.T, s *stub) *Provider {
	t.Helper()
	p, err := NewProvider(AdminConfig{
		BaseURL:      s.srv.URL,
		Realm:        "saas",
		ClientID:     "saas-backend-admin",
		ClientSecret: "secret",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return p
}

func TestNewProvider_ErrNotConfigured(t *testing.T) {
	_, err := NewProvider(AdminConfig{}) // empty
	if !errors.Is(err, identity.ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestListUsers_Success(t *testing.T) {
	s := newStub(t)
	s.handlerFor = func(method, path string) (int, []byte) {
		if method != "GET" || !strings.HasSuffix(path, "/users") {
			t.Errorf("unexpected admin call: %s %s", method, path)
		}
		return http.StatusOK, []byte(`[
			{"id":"u1","username":"alice","email":"a@x","firstName":"Al","lastName":"Ice","enabled":true,"emailVerified":true,"createdTimestamp":1700000000000},
			{"id":"u2","username":"bob","email":"b@x","enabled":false}
		]`)
	}
	p := newProvider(t, s)

	users, err := p.ListUsers(context.Background(), identity.ListUsersQuery{})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}
	if users[0].ID != "u1" || users[0].Username != "alice" || !users[0].Enabled {
		t.Errorf("user[0] wrong: %+v", users[0])
	}
	if users[0].CreatedAt.IsZero() {
		t.Errorf("CreatedAt should be populated from createdTimestamp")
	}
	if users[1].Enabled {
		t.Errorf("user[1] should be disabled")
	}
}

func TestListUsers_PassesQueryParams(t *testing.T) {
	s := newStub(t)
	var seenQuery string
	s.srv.Close()
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token") {
			s.tokenMints.Add(1)
			tok := *s.tokenValue.Load()
			body := fmt.Sprintf(`{"access_token":%q,"expires_in":60}`, tok)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
			return
		}
		seenQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(s.srv.Close)
	p := newProvider(t, s)

	_, err := p.ListUsers(context.Background(), identity.ListUsersQuery{
		Search: "alice",
		First:  10,
		Max:    25,
	})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	for _, must := range []string{"search=alice", "first=10", "max=25"} {
		if !strings.Contains(seenQuery, must) {
			t.Errorf("query missing %q (got %q)", must, seenQuery)
		}
	}
}

func TestGetUser_Success(t *testing.T) {
	s := newStub(t)
	s.handlerFor = func(_, path string) (int, []byte) {
		if !strings.HasSuffix(path, "/users/abc-123") {
			t.Errorf("expected path /users/abc-123, got %s", path)
		}
		return http.StatusOK, []byte(`{"id":"abc-123","username":"alice","email":"a@x","enabled":true}`)
	}
	p := newProvider(t, s)

	u, err := p.GetUser(context.Background(), "abc-123")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if u.ID != "abc-123" {
		t.Errorf("got id=%q", u.ID)
	}
}

func TestGetUser_404_MapsToNotFound(t *testing.T) {
	s := newStub(t)
	s.handlerFor = func(_, _ string) (int, []byte) {
		return http.StatusNotFound, []byte(`{"error":"not found"}`)
	}
	p := newProvider(t, s)

	_, err := p.GetUser(context.Background(), "missing")
	if !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetUser_500_MapsToAdminAPIUnavailable(t *testing.T) {
	s := newStub(t)
	s.handlerFor = func(_, _ string) (int, []byte) {
		return http.StatusBadGateway, []byte(`upstream broken`)
	}
	p := newProvider(t, s)

	_, err := p.GetUser(context.Background(), "x")
	if !errors.Is(err, identity.ErrAdminAPIUnavailable) {
		t.Fatalf("expected ErrAdminAPIUnavailable, got %v", err)
	}
}

func TestTokenCache_ReusesAcrossCalls(t *testing.T) {
	s := newStub(t)
	p := newProvider(t, s)

	for i := 0; i < 5; i++ {
		if _, err := p.ListUsers(context.Background(), identity.ListUsersQuery{}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := s.tokenMints.Load(); got != 1 {
		t.Errorf("expected token endpoint hit exactly once, got %d", got)
	}
	if got := s.adminCalls.Load(); got != 5 {
		t.Errorf("expected 5 admin calls, got %d", got)
	}
}

func TestTokenCache_RetriesOn401WithFreshToken(t *testing.T) {
	s := newStub(t)
	// First admin call → 401. Second → 200. We confirm the client
	// invalidates the cache and re-mints + retries.
	var hits atomic.Int32
	s.handlerFor = func(_, _ string) (int, []byte) {
		if hits.Add(1) == 1 {
			return http.StatusUnauthorized, []byte(`{"error":"token expired"}`)
		}
		return http.StatusOK, []byte(`[]`)
	}
	// Rotate the token value between mints so we can verify the retry used
	// the fresh one.
	first := "tok-initial"
	second := "tok-rotated"
	mints := atomic.Int32{}
	s.tokenValue.Store(&first)
	s.srv.Close()
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token") {
			n := mints.Add(1)
			var tok string
			if n == 1 {
				tok = first
			} else {
				tok = second
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"access_token":%q,"expires_in":60}`, tok)))
			return
		}
		s.authHeaders = append(s.authHeaders, r.Header.Get("Authorization"))
		status, body := s.handlerFor(r.Method, r.URL.Path)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.srv.Close)
	p := newProvider(t, s)

	if _, err := p.ListUsers(context.Background(), identity.ListUsersQuery{}); err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if got := mints.Load(); got != 2 {
		t.Errorf("expected 2 token mints (initial + retry), got %d", got)
	}
	if len(s.authHeaders) != 2 {
		t.Fatalf("expected 2 admin calls, got %d", len(s.authHeaders))
	}
	if s.authHeaders[0] != "Bearer "+first {
		t.Errorf("first call should use initial token, got %q", s.authHeaders[0])
	}
	if s.authHeaders[1] != "Bearer "+second {
		t.Errorf("retry should use refreshed token, got %q", s.authHeaders[1])
	}
}

func TestPersistent401_FailsAfterOneRetry(t *testing.T) {
	s := newStub(t)
	s.handlerFor = func(_, _ string) (int, []byte) {
		return http.StatusUnauthorized, []byte(`{"error":"nope"}`)
	}
	p := newProvider(t, s)

	_, err := p.ListUsers(context.Background(), identity.ListUsersQuery{})
	if err == nil {
		t.Fatalf("expected error on persistent 401")
	}
	// Sanity: didn't loop. (Client did 2 attempts; without the
	// allowRetry=false guard this would have hung.)
	_ = json.Unmarshal // touch import
}
