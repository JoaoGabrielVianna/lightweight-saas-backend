package identityruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/connection"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
)

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

// TestNewResolver_NilCollaboratorMeansNotWired pins the signal the composition
// root reads. Every domain in this codebase answers "I am not configured" with
// a nil constructor result, and the router omits the routes rather than
// mounting handlers that would panic.
func TestNewResolver_NilCollaboratorMeansNotWired(t *testing.T) {
	key, _ := secrets.GenerateKey()
	keyring, _ := secrets.NewSingleVersionKeyring(1, key)

	cases := map[string]*Resolver{
		"no workspaces":  NewResolver(nil, newFakeConnections(), keyring, Options{}),
		"no connections": NewResolver(newFakeWorkspaces(), nil, keyring, Options{}),
		"no keyring":     NewResolver(newFakeWorkspaces(), newFakeConnections(), nil, Options{}),
	}
	for name, got := range cases {
		if got != nil {
			t.Errorf("%s: NewResolver returned non-nil", name)
		}
	}
	if NewHandler(nil) != nil {
		t.Error("NewHandler(nil) must be nil so the router omits the routes")
	}
}

// ---------------------------------------------------------------------------
// The happy path
// ---------------------------------------------------------------------------

// TestForWorkspace_ResolvesTheActiveConnectionsProvider is the whole slice in
// one assertion: a workspace public id goes in, and a provider configured for
// that workspace's active connection — realm, client, and DECRYPTED secret —
// comes out.
func TestForWorkspace_ResolvesTheActiveConnectionsProvider(t *testing.T) {
	f := newFixture(t, Options{})

	p, err := f.resolver.ForWorkspace(context.Background(), testPublicID)
	if err != nil {
		t.Fatalf("ForWorkspace: %v", err)
	}

	got, ok := p.Provider.(*fakeProvider)
	if !ok {
		t.Fatalf("provider is %T, want *fakeProvider", p)
	}
	if got.realm != "realm-a" {
		t.Errorf("realm = %q, want realm-a", got.realm)
	}
	if got.clientID != "svc-a" {
		t.Errorf("client id = %q, want svc-a", got.clientID)
	}
	if got.secret != testSecret {
		t.Errorf("the provider was built with %q — the sealed credential did not round-trip", got.secret)
	}
}

// TestForWorkspace_AcceptsABareUUID mirrors the convenience the workspace and
// connection surfaces already offer. publicid.Parse owns the rule; this only
// pins that the resolver goes through it rather than string-matching a prefix.
func TestForWorkspace_AcceptsABareUUID(t *testing.T) {
	f := newFixture(t, Options{})

	if _, err := f.resolver.ForWorkspace(context.Background(), testWorkspaceID); err != nil {
		t.Fatalf("bare uuid rejected: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The refusals, and their ORDER
// ---------------------------------------------------------------------------

func TestForWorkspace_Refusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fixture)
		want  *Error
	}{
		{
			name:  "malformed workspace id",
			setup: func(*fixture) {},
			want:  ErrInvalidWorkspaceID,
		},
		{
			name:  "unknown workspace",
			setup: func(f *fixture) { f.ws.items = map[string]*workspace.Workspace{} },
			want:  ErrWorkspaceNotFound,
		},
		{
			name: "archived workspace",
			setup: func(f *fixture) {
				f.ws.items[testWorkspaceID].Status = workspace.StatusArchived
			},
			want: ErrWorkspaceArchived,
		},
		{
			name: "no active connection",
			setup: func(f *fixture) {
				f.conns.active = map[string]*connection.Connection{}
			},
			want: ErrConnectionMissing,
		},
		{
			name: "active connection with an empty realm",
			setup: func(f *fixture) {
				f.conns.active[testWorkspaceID].Realm = ""
			},
			want: ErrConnectionUnusable,
		},
		{
			name: "active connection with an empty base url",
			setup: func(f *fixture) {
				f.conns.active[testWorkspaceID].BaseURL = ""
			},
			want: ErrConnectionUnusable,
		},
		{
			name: "active connection naming an unimplemented provider",
			setup: func(f *fixture) {
				f.conns.active[testWorkspaceID].Provider = connection.Provider("auth0")
			},
			want: ErrConnectionUnusable,
		},
		{
			name: "credential row missing",
			setup: func(f *fixture) {
				f.conns.sealed = map[string]*secrets.Sealed{}
			},
			want: ErrCredentialsUnavailable,
		},
		{
			name: "credential sealed under a different key",
			setup: func(f *fixture) {
				otherKey, _ := secrets.GenerateKey()
				other, _ := secrets.NewSingleVersionKeyring(1, otherKey)
				sealed, _ := other.Seal([]byte("nope"), secretAAD(testConnID))
				f.conns.sealed[testConnID] = &sealed
			},
			want: ErrCredentialsUnavailable,
		},
		{
			name: "credential sealed against a different connection",
			setup: func(f *fixture) {
				// The AAD binds a ciphertext to its row. Moving connection X's
				// sealed secret onto connection Y must not open — that is the
				// whole point of the AAD, asserted here at the runtime boundary
				// rather than only in the secrets package.
				sealed, _ := f.keyring.Seal([]byte("stolen"), secretAAD("some-other-connection"))
				f.conns.sealed[testConnID] = &sealed
			},
			want: ErrCredentialsUnavailable,
		},
		{
			name:  "workspace store fails",
			setup: func(f *fixture) { f.ws.err = errBoom },
			want:  ErrInternal,
		},
		{
			name:  "connection store fails",
			setup: func(f *fixture) { f.conns.activeErr = errBoom },
			want:  ErrInternal,
		},
		{
			name:  "secret read fails",
			setup: func(f *fixture) { f.conns.secretErr = errBoom },
			want:  ErrInternal,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, Options{})
			tc.setup(f)

			id := testPublicID
			if tc.want == ErrInvalidWorkspaceID {
				id = "not-a-workspace-id"
			}

			_, err := f.resolver.ForWorkspace(context.Background(), id)
			var got *Error
			if !errors.As(err, &got) {
				t.Fatalf("err = %v (%T), want a catalogued *Error", err, err)
			}
			if got.Code != tc.want.Code {
				t.Errorf("code = %q, want %q", got.Code, tc.want.Code)
			}
		})
	}
}

// TestForWorkspace_ArchivedIsRejectedBeforeAnythingElseIsRead pins ORDER, not
// outcome.
//
// "An archived workspace must fail before contacting Keycloak" is only
// meaningful as a statement about sequence: a check performed after the
// provider call would return the same status code and none of the guarantee.
// Counting the collaborator calls is how that becomes testable — an archived
// workspace must produce zero connection reads and zero secret reads, so there
// is no path by which a provider could have been built, let alone called.
func TestForWorkspace_ArchivedIsRejectedBeforeAnythingElseIsRead(t *testing.T) {
	f := newFixture(t, Options{})
	f.ws.items[testWorkspaceID].Status = workspace.StatusArchived

	if _, err := f.resolver.ForWorkspace(context.Background(), testPublicID); err != ErrWorkspaceArchived {
		t.Fatalf("err = %v, want workspace_archived", err)
	}

	activeCalls, secretCalls := f.conns.counts()
	if activeCalls != 0 {
		t.Errorf("read the connection table %d times for an archived workspace — the check is in the wrong place", activeCalls)
	}
	if secretCalls != 0 {
		t.Errorf("read a sealed credential %d times for an archived workspace", secretCalls)
	}
	if f.builder.count() != 0 {
		t.Errorf("built %d providers for an archived workspace", f.builder.count())
	}
}

// TestForWorkspace_MissingConnectionNeverOpensASecret is the same
// order-of-operations argument one step further in.
func TestForWorkspace_MissingConnectionNeverOpensASecret(t *testing.T) {
	f := newFixture(t, Options{})
	f.conns.active = map[string]*connection.Connection{}

	if _, err := f.resolver.ForWorkspace(context.Background(), testPublicID); err != ErrConnectionMissing {
		t.Fatalf("err = %v, want workspace_connection_missing", err)
	}
	if _, secretCalls := f.conns.counts(); secretCalls != 0 {
		t.Errorf("opened %d secrets with no connection to open them for", secretCalls)
	}
}

// TestForWorkspace_StoreFailuresNeverReachTheClient pins the leak rule at the
// resolver: whatever a driver says, the caller gets a catalogued error whose
// message is a literal from errors.go.
func TestForWorkspace_StoreFailuresNeverReachTheClient(t *testing.T) {
	f := newFixture(t, Options{})
	f.ws.err = errBoom

	_, err := f.resolver.ForWorkspace(context.Background(), testPublicID)
	if strings.Contains(err.Error(), "constraint") || strings.Contains(err.Error(), "boom") {
		t.Errorf("driver error reached the caller: %v", err)
	}
	var domainErr *Error
	if !errors.As(err, &domainErr) || domainErr.Message != ErrInternal.Message {
		t.Errorf("err = %v, want the internal_error literal", err)
	}
}

// TestForWorkspace_BuilderNotConfiguredBecomesUnusable — a builder that reports
// the connection cannot be configured is the caller's problem to fix, not an
// internal fault.
func TestForWorkspace_BuilderNotConfiguredBecomesUnusable(t *testing.T) {
	f := newFixture(t, Options{})
	f.builder.err = identity.ErrNotConfigured

	if _, err := f.resolver.ForWorkspace(context.Background(), testPublicID); err != ErrConnectionUnusable {
		t.Errorf("err = %v, want workspace_connection_unusable", err)
	}
}

// ---------------------------------------------------------------------------
// Isolation
// ---------------------------------------------------------------------------

// TestForWorkspace_TwoWorkspacesGetTwoProviders is the unit-level shape of the
// isolation claim the integration suite proves against real realms.
func TestForWorkspace_TwoWorkspacesGetTwoProviders(t *testing.T) {
	f := newFixture(t, Options{})

	const otherWorkspaceID = "9f8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	f.ws.add(&workspace.Workspace{ID: otherWorkspaceID, Slug: "beta", Name: "Beta", Status: workspace.StatusActive})
	f.seal(t, &connection.Connection{
		ID:          "1a2b3c4d-5e6f-4708-9a0b-1c2d3e4f5a6b",
		WorkspaceID: otherWorkspaceID,
		Provider:    connection.ProviderKeycloak,
		Status:      connection.StatusActive,
		BaseURL:     "http://keycloak.test",
		Realm:       "realm-b",
		ClientID:    "svc-b",
		UpdatedAt:   time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}, "another-secret-entirely")

	a, err := f.resolver.ForWorkspace(context.Background(), testWorkspaceID)
	if err != nil {
		t.Fatalf("resolve A: %v", err)
	}
	b, err := f.resolver.ForWorkspace(context.Background(), otherWorkspaceID)
	if err != nil {
		t.Fatalf("resolve B: %v", err)
	}

	if a.Provider == b.Provider {
		t.Fatal("both workspaces resolved to the SAME provider instance — state is shared between them")
	}
	pa, pb := a.Provider.(*fakeProvider), b.Provider.(*fakeProvider)
	if pa.realm == pb.realm {
		t.Errorf("both providers point at realm %q", pa.realm)
	}
	if pa.secret == pb.secret {
		t.Error("both providers were built with the same credential")
	}
}

// TestForWorkspace_ConcurrentResolutionKeepsWorkspacesApart runs both
// workspaces hard and in parallel.
//
// Under -race this is the test that would catch a shared map, a shared provider
// field, or a cache that returns another workspace's entry. Without enough
// concurrency the cache serialises everything and the test proves nothing, so
// the goroutine count is deliberately well above the number of distinct keys.
func TestForWorkspace_ConcurrentResolutionKeepsWorkspacesApart(t *testing.T) {
	f := newFixture(t, Options{})

	const otherWorkspaceID = "9f8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	f.ws.add(&workspace.Workspace{ID: otherWorkspaceID, Status: workspace.StatusActive})
	f.seal(t, &connection.Connection{
		ID:          "1a2b3c4d-5e6f-4708-9a0b-1c2d3e4f5a6b",
		WorkspaceID: otherWorkspaceID,
		Provider:    connection.ProviderKeycloak,
		Status:      connection.StatusActive,
		BaseURL:     "http://keycloak.test",
		Realm:       "realm-b",
		ClientID:    "svc-b",
		UpdatedAt:   time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}, "another-secret-entirely")

	want := map[string]string{testWorkspaceID: "realm-a", otherWorkspaceID: "realm-b"}

	var wg sync.WaitGroup
	errs := make(chan string, 400)
	for i := 0; i < 200; i++ {
		for id, realm := range want {
			wg.Add(1)
			go func(id, realm string) {
				defer wg.Done()
				p, err := f.resolver.ForWorkspace(context.Background(), id)
				if err != nil {
					errs <- "resolve " + id + ": " + err.Error()
					return
				}
				users, err := p.Provider.ListUsers(context.Background(), identity.ListUsersQuery{})
				if err != nil {
					errs <- "list " + id + ": " + err.Error()
					return
				}
				if len(users) != 1 || users[0].Username != realm+"-user" {
					errs <- "workspace " + id + " saw users from another realm: " + users[0].Username
				}
			}(id, realm)
		}
	}
	wg.Wait()
	close(errs)

	for msg := range errs {
		t.Error(msg)
	}
}

// ---------------------------------------------------------------------------
// Rotation
// ---------------------------------------------------------------------------

// TestForWorkspace_RotatingTheSecretInPlaceRebuildsTheProvider is the reason
// the cache key carries updated_at.
//
// Rotating a credential is an UPDATE on the same connection row: the id does
// not change. Keying the cache on the id alone would keep serving a provider
// holding the revoked secret for as long as the entry lived — which is the
// exact failure a rotation exists to prevent.
func TestForWorkspace_RotatingTheSecretInPlaceRebuildsTheProvider(t *testing.T) {
	f := newFixture(t, Options{})
	ctx := context.Background()

	first, err := f.resolver.ForWorkspace(ctx, testPublicID)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if first.Provider.(*fakeProvider).secret != testSecret {
		t.Fatalf("first provider has the wrong secret")
	}

	// Rotate: same row, new credential, new updated_at — exactly what
	// connection.Service.Update writes.
	rotated := *f.conns.active[testWorkspaceID]
	rotated.UpdatedAt = rotated.UpdatedAt.Add(time.Minute)
	f.seal(t, &rotated, "rotated-credential")

	second, err := f.resolver.ForWorkspace(ctx, testPublicID)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if got := second.Provider.(*fakeProvider).secret; got != "rotated-credential" {
		t.Errorf("provider still holds %q after rotation — the cache served a stale credential", got)
	}
}

// TestForWorkspace_ActivatingAnotherConnectionSwitchesProvider covers the other
// rotation shape: a new connection is activated and the old one retires.
func TestForWorkspace_ActivatingAnotherConnectionSwitchesProvider(t *testing.T) {
	f := newFixture(t, Options{})
	ctx := context.Background()

	if _, err := f.resolver.ForWorkspace(ctx, testPublicID); err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	f.seal(t, &connection.Connection{
		ID:          "2b3c4d5e-6f70-4819-ab1c-2d3e4f5a6b7c",
		WorkspaceID: testWorkspaceID,
		Provider:    connection.ProviderKeycloak,
		Status:      connection.StatusActive,
		BaseURL:     "http://keycloak.test",
		Realm:       "realm-a2",
		ClientID:    "svc-a2",
		UpdatedAt:   time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC),
	}, "second-generation-secret")

	p, err := f.resolver.ForWorkspace(ctx, testPublicID)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if got := p.Provider.(*fakeProvider).realm; got != "realm-a2" {
		t.Errorf("realm = %q, want realm-a2 — activation did not take effect without a restart", got)
	}
}

// TestForWorkspace_RetiringTheOnlyConnectionStopsResolution — retire is
// terminal, and the runtime must stop routing immediately rather than serve the
// retired connection from cache.
func TestForWorkspace_RetiringTheOnlyConnectionStopsResolution(t *testing.T) {
	f := newFixture(t, Options{})
	ctx := context.Background()

	if _, err := f.resolver.ForWorkspace(ctx, testPublicID); err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	// Retiring removes it from "the workspace's active connection".
	f.conns.mu.Lock()
	delete(f.conns.active, testWorkspaceID)
	f.conns.mu.Unlock()

	if _, err := f.resolver.ForWorkspace(ctx, testPublicID); err != ErrConnectionMissing {
		t.Errorf("err = %v, want workspace_connection_missing — a retired connection was served from cache", err)
	}
}

// ---------------------------------------------------------------------------
// Caching — behaviour, and the evidence for having one
// ---------------------------------------------------------------------------

// TestForWorkspace_RepeatedResolutionReusesTheProvider pins what the cache
// buys: one provider construction, and therefore one service-account token,
// across many requests.
func TestForWorkspace_RepeatedResolutionReusesTheProvider(t *testing.T) {
	f := newFixture(t, Options{})
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		if _, err := f.resolver.ForWorkspace(ctx, testPublicID); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}

	if got := f.builder.count(); got != 1 {
		t.Errorf("built %d providers for 50 requests, want 1", got)
	}
	if _, secretCalls := f.conns.counts(); secretCalls != 1 {
		t.Errorf("opened the sealed credential %d times for 50 requests, want 1", secretCalls)
	}
}

// TestForWorkspace_ReadsTheConnectionRowEveryTime is the cost the cache does
// NOT avoid, stated as an assertion so nobody optimizes it away.
//
// The connection row IS the invalidation signal. Caching it too would mean
// rotation no longer takes effect without a restart, which is acceptance
// criterion 10 of this slice.
func TestForWorkspace_ReadsTheConnectionRowEveryTime(t *testing.T) {
	f := newFixture(t, Options{})
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		if _, err := f.resolver.ForWorkspace(ctx, testPublicID); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}

	if activeCalls, _ := f.conns.counts(); activeCalls != 10 {
		t.Errorf("read the active connection %d times for 10 requests, want 10 — "+
			"caching the row would break rotation-without-restart", activeCalls)
	}
}

// TestForWorkspace_UncachedResolutionIsIdentical proves the cache is an
// optimization rather than a dependency. With caching off every request
// rebuilds, and the answers are the same.
func TestForWorkspace_UncachedResolutionIsIdentical(t *testing.T) {
	f := newFixture(t, Options{CacheSize: -1})
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		p, err := f.resolver.ForWorkspace(ctx, testPublicID)
		if err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
		if got := p.Provider.(*fakeProvider); got.realm != "realm-a" || got.secret != testSecret {
			t.Fatalf("uncached resolve %d produced a different provider", i)
		}
	}
	if got := f.builder.count(); got != 5 {
		t.Errorf("built %d providers with caching disabled, want 5", got)
	}
}

// TestProviderCache_EvictsLeastRecentlyUsed pins the bound.
func TestProviderCache_EvictsLeastRecentlyUsed(t *testing.T) {
	c := newProviderCache(2)

	c.put("c1", "c1@v1", &fakeProvider{realm: "one"})
	c.put("c2", "c2@v1", &fakeProvider{realm: "two"})

	// Touch c1 so c2 becomes the least recently used.
	if _, ok := c.get("c1@v1"); !ok {
		t.Fatal("c1 missing before eviction")
	}

	c.put("c3", "c3@v1", &fakeProvider{realm: "three"})

	if c.len() != 2 {
		t.Errorf("cache holds %d entries, want 2", c.len())
	}
	if _, ok := c.get("c2@v1"); ok {
		t.Error("c2 survived — the least recently used entry was not the one evicted")
	}
	if _, ok := c.get("c1@v1"); !ok {
		t.Error("c1 was evicted despite being the most recently used")
	}
}

// TestProviderCache_SupersededGenerationsAreDropped pins the rotation
// housekeeping: an entry for an older generation of the same connection is
// removed on insert rather than left to age out, so the superseded provider's
// still-valid Keycloak token is released promptly.
func TestProviderCache_SupersededGenerationsAreDropped(t *testing.T) {
	c := newProviderCache(8)

	c.put("c1", "c1@v1", &fakeProvider{realm: "old"})
	c.put("c1", "c1@v2", &fakeProvider{realm: "new"})

	if _, ok := c.get("c1@v1"); ok {
		t.Error("the superseded generation is still cached, holding a credential that has been rotated away")
	}
	if c.len() != 1 {
		t.Errorf("cache holds %d entries after a rotation, want 1", c.len())
	}
}

// TestProviderCache_DisabledNeverStores — the negative-size escape hatch.
func TestProviderCache_DisabledNeverStores(t *testing.T) {
	c := newProviderCache(-1)
	c.put("c1", "c1@v1", &fakeProvider{})

	if _, ok := c.get("c1@v1"); ok {
		t.Error("a disabled cache returned an entry")
	}
	if c.len() != 0 {
		t.Errorf("a disabled cache reports %d entries", c.len())
	}
}

// TestCacheKey_ChangesWithEveryConfigurationChange states the key's contract
// directly, so a future edit to cacheKey has to argue with a test rather than
// only with a comment.
func TestCacheKey_ChangesWithEveryConfigurationChange(t *testing.T) {
	base := &connection.Connection{
		ID:        testConnID,
		UpdatedAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}

	rotated := *base
	rotated.UpdatedAt = base.UpdatedAt.Add(time.Nanosecond)

	other := *base
	other.ID = "0f0e0d0c-0b0a-4908-8706-050403020100"

	if cacheKey(base) == cacheKey(&rotated) {
		t.Error("a changed updated_at produced the same key — an in-place secret rotation would be served stale")
	}
	if cacheKey(base) == cacheKey(&other) {
		t.Error("two different connections share a key")
	}

	// Same row, same generation, read twice: the key must be stable, or the
	// cache never hits and every request mints a token.
	again := *base
	if cacheKey(base) != cacheKey(&again) {
		t.Error("the key is not stable across two reads of the same row")
	}
}
