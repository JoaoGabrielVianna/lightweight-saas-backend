// Package identityruntime is the boundary where a Workspace stops being
// administrative metadata and starts routing traffic.
//
// Everything before this package treats a Workspace and its Connection as rows
// an operator manages. This package is what makes them mean something at
// request time: given a workspace's public id, it finds that workspace's active
// Connection, opens the credential sealed against it, and hands back a provider
// already pointed at the right Keycloak realm.
//
//	workspace public id
//	  → workspace           (must exist, must not be archived)
//	  → active connection   (must exist, must be usable)
//	  → sealed secret       (must open)
//	  → identity provider   (pointed at that connection's realm)
//
// Three things are deliberately NOT visible outside this package:
//
//   - the connection row. Handlers receive an identity.IdentityProvider and
//     have no way to ask which realm it points at or what it is called.
//   - encryption. Nothing above this layer knows a ciphertext exists.
//   - credentials. The decrypted secret lives inside one function call and is
//     wiped from the buffer it arrived in.
//
// Isolation between workspaces is structural rather than disciplined: each
// Connection gets its own provider instance, and every piece of state that
// could leak — the service-account token cache above all — is a field on that
// instance. There is no package-level mutable state here or in the Keycloak
// provider it builds.
package identityruntime

import (
	"context"
	"errors"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/connection"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	identitykc "github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity/keycloak"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/metrics"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
)

var log = logger.New("identity-runtime")

// WorkspaceStore is the slice of the workspace repository this package needs.
// Declared consumer-side; *workspace.PostgresRepository satisfies it directly.
type WorkspaceStore interface {
	GetByID(ctx context.Context, id string) (*workspace.Workspace, error)
}

// ConnectionStore is the slice of the connection repository this package needs.
//
// Two methods, and the split between them is the point: GetActiveByWorkspace
// returns a Connection, which structurally cannot hold secret material, and
// OpenSecret is the single conspicuous call that does. A new call site for the
// second one is worth a second look in a diff; a new call site for the first
// one is not.
type ConnectionStore interface {
	GetActiveByWorkspace(ctx context.Context, workspaceID string) (*connection.Connection, error)
	OpenSecret(ctx context.Context, id string) (*secrets.Sealed, error)
}

// ProviderBuilder turns a resolved connection plus its opened credential into a
// provider. Injected so tests can resolve without a Keycloak, and so the one
// place that touches a plaintext credential is a named seam rather than an
// inline literal.
type ProviderBuilder func(c *connection.Connection, clientSecret string) (identity.IdentityProvider, error)

// Resolved is what a workspace resolves to: a provider pointed at its realm,
// plus the small amount of metadata callers legitimately need ABOUT the
// routing decision.
//
// The metadata is deliberately minimal and deliberately not the connection
// row. WorkspaceID exists so a mutation can say which workspace it was routed
// through in its audit event; AccessMode exists so writes can be refused
// centrally when the connection's service account has already been shown to be
// under-privileged. Neither tells a handler the realm name, the base URL, the
// client id, or anything about a credential — a handler still cannot discover
// where its request went.
type Resolved struct {
	// Provider is the identity provider for this workspace's realm.
	Provider identity.IdentityProvider

	// WorkspacePublicID is the `ws_` id, for audit attribution. The public
	// form, not the UUID, because that is what appears in the request path
	// and in every response.
	WorkspacePublicID string

	// ConnectionPublicID is the `conn_` id of the connection that served
	// this resolution. Not surfaced to clients; useful in logs when an
	// operator is working out which of a workspace's connections answered.
	ConnectionPublicID string

	// AccessMode is the verdict of the connection's last verification probe.
	// See CanWrite for what the runtime does with it, and for why it is a
	// weaker signal than its name suggests.
	AccessMode connection.AccessMode
}

// CanWrite reports whether a mutation should be attempted through this
// connection at all.
//
// ─── What access_mode means (post TD-024) ───────────────────────────────────
//
// Two of the four values refuse a write, for different reasons:
//
//   - `limited` means the verification probe's admin READS were refused — the
//     service account could not read the realm, or could not list users. Such
//     a connection is under-privileged in a way that makes writes very
//     unlikely to succeed, and it may not be able to read either.
//
//   - `read_only` means the reads succeeded and Keycloak positively reported
//     that the account holds no grant permitting writes. This is the case the
//     pre-Slice-6 three-value model labelled `full`, which is precisely the
//     over-claim TD-024 recorded.
//
// `unknown` is permitted, and that is a deliberate asymmetry: it means the
// provider gave no usable evidence either way (a client whose scope omits its
// realm-management roles, or a token this build cannot read). Refusing writes
// on absent evidence would break working installations for a signal that was
// never promised. The authoritative answer for those still arrives from
// Keycloak as a 403 and surfaces as provider_forbidden — which is why that
// error code exists.
//
// The decision itself lives on connection.AccessMode so the runtime guard, the
// verifier's summary, and the console cannot drift apart on what "may write"
// means.
func (r Resolved) CanWrite() bool {
	return r.AccessMode.CanWrite()
}

// Resolver resolves the identity provider for a workspace.
type Resolver struct {
	workspaces  WorkspaceStore
	connections ConnectionStore
	keyring     *secrets.Keyring
	build       ProviderBuilder
	cache       *providerCache
}

// Options tunes a Resolver. The zero value is the production configuration.
type Options struct {
	// Build overrides provider construction. nil selects the Keycloak builder.
	Build ProviderBuilder
	// CacheSize bounds the provider cache. Zero selects defaultCacheSize;
	// negative disables caching entirely, which is what the resolver's own
	// tests use to assert the uncached path still resolves correctly.
	CacheSize int
}

// NewResolver constructs a Resolver.
//
// Returns nil when a collaborator is missing, the same "this is not wired"
// signal the workspace, connection and identity domains use — the composition
// root reads it as "omit these routes" rather than mounting handlers that would
// panic. The Keyring is the one genuinely optional in deployment: no master key
// means no connections to resolve, so no workspace-scoped identity API.
func NewResolver(workspaces WorkspaceStore, connections ConnectionStore, keyring *secrets.Keyring, opts Options) *Resolver {
	if workspaces == nil || connections == nil || keyring == nil {
		return nil
	}

	build := opts.Build
	if build == nil {
		build = keycloakBuilder
	}

	return &Resolver{
		workspaces:  workspaces,
		connections: connections,
		keyring:     keyring,
		build:       build,
		cache:       newProviderCache(opts.CacheSize),
	}
}

// ForWorkspace resolves what a workspace routes through.
//
// Every failure is a catalogued *Error. Anything the collaborators return that
// is not one — a driver error, a decode failure — is logged here and reported
// as ErrInternal, which is what keeps SQL fragments and constraint names off
// the wire without each caller having to remember to strip them.
//
// It returns a Resolved rather than a bare provider so callers can attribute an
// audit event to the workspace and refuse a write through an under-privileged
// connection, without either of those needing a second lookup — both facts were
// already loaded to get here.
func (r *Resolver) ForWorkspace(ctx context.Context, workspacePublicID string) (*Resolved, error) {
	ws, err := r.requireLiveWorkspace(ctx, workspacePublicID)
	if err != nil {
		return nil, err
	}

	conn, err := r.connections.GetActiveByWorkspace(ctx, ws.ID)
	if err != nil {
		log.Error("load active connection for workspace " + ws.PublicID() + ": " + err.Error())
		return nil, ErrInternal
	}
	if conn == nil {
		return nil, ErrConnectionMissing
	}
	if err := usable(conn); err != nil {
		return nil, err
	}

	resolved := &Resolved{
		WorkspacePublicID:  ws.PublicID(),
		ConnectionPublicID: conn.PublicID(),
		AccessMode:         conn.AccessMode,
	}

	// The cache key carries the connection's identity AND its configuration
	// generation, so a rotated credential or an edited base URL cannot be
	// served from a provider built against the old one. See providerCache.
	//
	// Only the PROVIDER is cached, never the Resolved around it: the metadata
	// is read fresh from the connection row on every request, so an operator
	// re-verifying a connection into a different access mode takes effect
	// immediately rather than when the provider happens to be rebuilt.
	key := cacheKey(conn)
	if p, ok := r.cache.get(key); ok {
		resolved.Provider = p
		return resolved, nil
	}

	p, err := r.buildProvider(ctx, conn)
	if err != nil {
		return nil, err
	}

	r.cache.put(conn.ID, key, p)
	resolved.Provider = p
	return resolved, nil
}

// requireLiveWorkspace loads the workspace and refuses an archived one.
//
// Archived is checked here, before anything reads a connection or opens a
// secret, because "provider operations through an archived workspace must fail
// before contacting Keycloak" is a property of ordering, not of outcome: a
// check performed after the provider call would produce the same status code
// and none of the guarantee.
func (r *Resolver) requireLiveWorkspace(ctx context.Context, workspacePublicID string) (*workspace.Workspace, error) {
	id, err := publicid.Parse(publicid.WorkspacePrefix, workspacePublicID)
	if err != nil {
		return nil, ErrInvalidWorkspaceID
	}

	ws, err := r.workspaces.GetByID(ctx, id)
	if err != nil {
		log.Error("load workspace: " + err.Error())
		return nil, ErrInternal
	}
	if ws == nil {
		return nil, ErrWorkspaceNotFound
	}
	if ws.IsArchived() {
		return nil, ErrWorkspaceArchived
	}
	return ws, nil
}

// buildProvider opens the sealed credential and constructs the provider.
//
// This is the only function in the codebase that holds a plaintext provider
// credential, and it holds it for the duration of one call. The buffer is
// wiped on the way out — which does not make the secret unrecoverable (the
// string handed to the builder outlives it, at the garbage collector's
// discretion) but does keep it from sitting in a heap buffer that some later
// panic dump or core file could carry off.
func (r *Resolver) buildProvider(ctx context.Context, conn *connection.Connection) (identity.IdentityProvider, error) {
	sealed, err := r.connections.OpenSecret(ctx, conn.ID)
	if err != nil {
		log.Error("read sealed credential for connection " + conn.PublicID() + ": " + err.Error())
		return nil, ErrInternal
	}
	if sealed == nil {
		// An active connection with no credential row. The schema makes this
		// unreachable; reaching it means the row was tampered with.
		log.Error("active connection " + conn.PublicID() + " has no sealed credential")
		return nil, ErrCredentialsUnavailable
	}

	plaintext, err := r.keyring.Open(*sealed, secretAAD(conn.ID))
	if err != nil {
		// The REASON is logged and counted; the ciphertext and the nonce are
		// not, and never will be.
		//
		// Which failure it was used to be withheld too, on the reasoning that
		// an operator could do nothing differently. Rotation makes that false:
		// `unknown_key_version` means a key was removed from SECRETS_KEYRING
		// before rotation finished and putting it back fixes every affected
		// workspace, while `authentication_failed` means the configured key
		// does not open this row and no amount of restarting will help. Those
		// are different pages in the middle of the night.
		//
		// The reason is a closed vocabulary and carries nothing derived from
		// the ciphertext, so it is safe both to log and to use as a metric
		// label. The CLIENT still learns only credentials_unavailable.
		reason := secrets.OpenFailureReason(err)
		metrics.Default.ObserveSecretOpenFailure(reason)
		log.Error("cannot open sealed credential for connection " + conn.PublicID() +
			" (" + reason + ")")
		return nil, ErrCredentialsUnavailable
	}
	defer wipe(plaintext)

	p, err := r.build(conn, string(plaintext))
	if err != nil {
		if errors.Is(err, identity.ErrNotConfigured) {
			return nil, ErrConnectionUnusable
		}
		log.Error("build provider for connection " + conn.PublicID() + ": " + err.Error())
		return nil, ErrInternal
	}
	if p == nil {
		log.Error("provider builder returned nil for connection " + conn.PublicID())
		return nil, ErrInternal
	}
	return p, nil
}

// keycloakBuilder is the production ProviderBuilder.
//
// It reuses identitykc.NewProvider unchanged. That constructor performs no
// network I/O — it validates four strings and assembles two URLs — so building
// one per cache miss costs an allocation, not a round trip. What it does own is
// a per-instance service-account token cache, and that is precisely the state
// that must not be shared between workspaces.
func keycloakBuilder(c *connection.Connection, clientSecret string) (identity.IdentityProvider, error) {
	if c.Provider != connection.ProviderKeycloak {
		return nil, identity.ErrNotConfigured
	}
	return identitykc.NewProvider(identitykc.AdminConfig{
		BaseURL:      c.BaseURL,
		Realm:        c.Realm,
		ClientID:     c.ClientID,
		ClientSecret: clientSecret,
	})
}

// usable reports whether an active connection can be turned into a provider at
// all, without contacting anything.
//
// It checks configuration completeness, NOT health. Health is the verdict of
// the last verification probe, and refusing to route on a stale `unhealthy`
// would mean an operator who has already fixed their Keycloak cannot use it
// until they remember to re-verify. Verification gates ACTIVATION — that is
// where a perishable fact belongs — and this gates construction.
func usable(c *connection.Connection) error {
	if c.Status != connection.StatusActive {
		return ErrConnectionUnusable
	}
	if c.Provider != connection.ProviderKeycloak {
		return ErrConnectionUnusable
	}
	if c.BaseURL == "" || c.Realm == "" || c.ClientID == "" {
		return ErrConnectionUnusable
	}
	return nil
}

// secretAAD rebuilds the additional authenticated data connection.Service
// sealed the credential with. The two MUST agree; a mismatch makes every
// credential in the installation unopenable, which is why this mirrors
// connection.secretAAD exactly rather than inventing its own scheme.
func secretAAD(connectionID string) []byte {
	return secrets.AAD("connection", connectionID, "client_secret")
}

// wipe zeroes a buffer that held secret material.
func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// cacheKey identifies a provider by the exact configuration it was built from.
//
// Connection id AND updated_at, never the workspace id alone. Keying on the
// workspace would survive a credential rotation and keep serving a provider
// holding the revoked secret — the precise failure a rotation exists to
// prevent. Both halves are needed even so: the id alone misses an in-place
// secret change (which is an UPDATE on the same row), and updated_at alone is
// not unique across connections.
func cacheKey(c *connection.Connection) string {
	return c.ID + "@" + c.UpdatedAt.UTC().Format(time.RFC3339Nano)
}
