package identityruntime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/connection"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
)

// The evidence behind the caching decision.
//
// Phase 6 of this slice says: do not add caching automatically, measure first,
// and if construction is cheap and does no I/O, prefer a simple resolver. The
// measurement landed somewhere more interesting than either branch of that
// instruction, and these tests are what it rests on:
//
//	construction  — no network I/O, ~one allocation. Caching buys nothing.
//	FIRST CALL    — one client_credentials round trip, per provider instance.
//	                Caching buys exactly this, on every request.
//
// So the resolver caches, but not for the reason the phase anticipated, and the
// cache's unit is the token holder rather than the struct. If either fact below
// stops being true, the cache should be reconsidered rather than kept out of
// habit — which is why they are assertions and not a paragraph in a design doc.

// countingKeycloak stands in for a provider endpoint and counts every request
// it receives, tagged by kind.
type countingKeycloak struct {
	server     *httptest.Server
	tokenCalls atomic.Int64
	adminCalls atomic.Int64
}

func newCountingKeycloak(t *testing.T) *countingKeycloak {
	t.Helper()

	k := &countingKeycloak{}
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/", func(w http.ResponseWriter, _ *http.Request) {
		k.tokenCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"t","expires_in":300,"token_type":"Bearer"}`))
	})
	mux.HandleFunc("/admin/realms/", func(w http.ResponseWriter, _ *http.Request) {
		k.adminCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})

	k.server = httptest.NewServer(mux)
	t.Cleanup(k.server.Close)
	return k
}

func keycloakConnection(baseURL, realm string) *connection.Connection {
	return &connection.Connection{
		ID:          testConnID,
		WorkspaceID: testWorkspaceID,
		Provider:    connection.ProviderKeycloak,
		Status:      connection.StatusActive,
		BaseURL:     baseURL,
		Realm:       realm,
		ClientID:    "svc",
		UpdatedAt:   time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}
}

// TestProviderConstructionPerformsNoNetworkIO is the first half of the
// measurement: building a provider contacts nothing.
//
// This is what makes per-request construction viable at all, and it is why the
// cache is NOT justified by construction cost. Asserted against a real HTTP
// server rather than by reading the constructor, so a future change that adds
// an eager discovery call — a plausible, well-meaning change — fails here.
func TestProviderConstructionPerformsNoNetworkIO(t *testing.T) {
	kc := newCountingKeycloak(t)

	for i := 0; i < 100; i++ {
		if _, err := keycloakBuilder(keycloakConnection(kc.server.URL, "realm-a"), "s3cr3t"); err != nil {
			t.Fatalf("build %d: %v", i, err)
		}
	}

	if got := kc.tokenCalls.Load() + kc.adminCalls.Load(); got != 0 {
		t.Errorf("100 provider constructions made %d HTTP requests, want 0 — "+
			"construction is no longer free and the resolver's cost model needs revisiting", got)
	}
}

// TestEachProviderMintsItsOwnToken is the second half, and the actual
// justification for the cache.
//
// A fresh provider has an empty token cache, so its first admin call costs a
// client_credentials round trip. Ten providers, ten token requests. An uncached
// resolver builds a provider per request, so this is a token grant per request
// — an extra round trip on the hot path and a service-account session per
// request accumulating in Keycloak.
func TestEachProviderMintsItsOwnToken(t *testing.T) {
	kc := newCountingKeycloak(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		p, err := keycloakBuilder(keycloakConnection(kc.server.URL, "realm-a"), "s3cr3t")
		if err != nil {
			t.Fatalf("build %d: %v", i, err)
		}
		if _, err := p.ListUsers(ctx, identity.ListUsersQuery{}); err != nil {
			t.Fatalf("list %d: %v", i, err)
		}
	}

	if got := kc.tokenCalls.Load(); got != 10 {
		t.Errorf("10 freshly-built providers made %d token requests, want 10", got)
	}
}

// TestOneProviderMintsOneTokenForManyCalls is the same measurement from the
// cache's side: reuse the instance and the token is acquired once.
//
// Together with the test above, this is the whole argument. 10 requests cost 10
// token grants uncached and 1 cached, and the difference is entirely a property
// of whether the provider INSTANCE survives between requests — which is exactly
// what providerCache holds.
func TestOneProviderMintsOneTokenForManyCalls(t *testing.T) {
	kc := newCountingKeycloak(t)
	ctx := context.Background()

	p, err := keycloakBuilder(keycloakConnection(kc.server.URL, "realm-a"), "s3cr3t")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for i := 0; i < 10; i++ {
		if _, err := p.ListUsers(ctx, identity.ListUsersQuery{}); err != nil {
			t.Fatalf("list %d: %v", i, err)
		}
	}

	if got := kc.tokenCalls.Load(); got != 1 {
		t.Errorf("one provider made %d token requests for 10 calls, want 1", got)
	}
	if got := kc.adminCalls.Load(); got != 10 {
		t.Errorf("admin calls = %d, want 10", got)
	}
}

// TestTwoProvidersDoNotShareATokenCache is the isolation property the cache
// must not undermine.
//
// Provider state is per-instance in internal/identity/keycloak — the token
// lives in an atomic.Pointer field on AdminClient, not in a package-level map.
// This pins that: two providers built for two realms each acquire their own
// token, so a token minted with workspace A's service account can never be
// presented on workspace B's request.
func TestTwoProvidersDoNotShareATokenCache(t *testing.T) {
	kc := newCountingKeycloak(t)
	ctx := context.Background()

	a, err := keycloakBuilder(keycloakConnection(kc.server.URL, "realm-a"), "secret-a")
	if err != nil {
		t.Fatalf("build a: %v", err)
	}
	b, err := keycloakBuilder(keycloakConnection(kc.server.URL, "realm-b"), "secret-b")
	if err != nil {
		t.Fatalf("build b: %v", err)
	}

	if _, err := a.ListUsers(ctx, identity.ListUsersQuery{}); err != nil {
		t.Fatalf("list a: %v", err)
	}
	if _, err := b.ListUsers(ctx, identity.ListUsersQuery{}); err != nil {
		t.Fatalf("list b: %v", err)
	}

	if got := kc.tokenCalls.Load(); got != 2 {
		t.Errorf("token requests = %d, want 2 — the two providers shared a token cache", got)
	}
}

// TestKeycloakBuilderRefusesAnUnimplementedProvider — the builder is where the
// one-provider-per-build assumption is enforced, and it must not silently treat
// an auth0 connection as a Keycloak one.
func TestKeycloakBuilderRefusesAnUnimplementedProvider(t *testing.T) {
	c := keycloakConnection("http://example.test", "realm-a")
	c.Provider = connection.Provider("auth0")

	if _, err := keycloakBuilder(c, "s"); err != identity.ErrNotConfigured {
		t.Errorf("err = %v, want identity.ErrNotConfigured", err)
	}
}

// BenchmarkProviderConstruction quantifies the thing the cache is NOT for.
//
//	go test -run xxx -bench ProviderConstruction ./internal/identityruntime/
func BenchmarkProviderConstruction(b *testing.B) {
	c := keycloakConnection("http://keycloak.test", "realm-a")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := keycloakBuilder(c, "s3cr3t"); err != nil {
			b.Fatal(err)
		}
	}
}
