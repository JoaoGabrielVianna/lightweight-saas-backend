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

// In-memory doubles for the two stores. They exist so the resolver's control
// flow — which error for which state, in which order — can be exercised without
// a database. The repositories' own behaviour is covered against real
// PostgreSQL in their packages' integration suites.

type fakeWorkspaces struct {
	mu    sync.Mutex
	items map[string]*workspace.Workspace
	err   error
	calls int
}

func newFakeWorkspaces() *fakeWorkspaces {
	return &fakeWorkspaces{items: map[string]*workspace.Workspace{}}
}

func (f *fakeWorkspaces) add(w *workspace.Workspace) { f.items[w.ID] = w }

func (f *fakeWorkspaces) GetByID(_ context.Context, id string) (*workspace.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.items[id], nil
}

type fakeConnections struct {
	mu sync.Mutex
	// active maps workspace id → the connection it routes through.
	active map[string]*connection.Connection
	// sealed maps connection id → its sealed credential.
	sealed map[string]*secrets.Sealed

	activeErr error
	secretErr error

	activeCalls int
	secretCalls int
}

func newFakeConnections() *fakeConnections {
	return &fakeConnections{
		active: map[string]*connection.Connection{},
		sealed: map[string]*secrets.Sealed{},
	}
}

func (f *fakeConnections) GetActiveByWorkspace(_ context.Context, workspaceID string) (*connection.Connection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activeCalls++
	if f.activeErr != nil {
		return nil, f.activeErr
	}
	return f.active[workspaceID], nil
}

func (f *fakeConnections) OpenSecret(_ context.Context, id string) (*secrets.Sealed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secretCalls++
	if f.secretErr != nil {
		return nil, f.secretErr
	}
	return f.sealed[id], nil
}

func (f *fakeConnections) counts() (activeCalls, secretCalls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeCalls, f.secretCalls
}

// fakeProvider records which connection it was built from, so a test can assert
// that a request reached the provider for the RIGHT workspace — the property
// every isolation claim in this slice reduces to.
type fakeProvider struct {
	identity.IdentityProvider // embedded: only the methods a test calls are implemented

	realm    string
	clientID string
	secret   string

	mu sync.Mutex
	// lastQuery is what the provider actually received, which is where the
	// identity service's clamping becomes observable — the response body echoes
	// the caller's raw parameters, not the bounded ones.
	lastQuery identity.ListUsersQuery
}

func (p *fakeProvider) ListUsers(_ context.Context, q identity.ListUsersQuery) ([]identity.User, error) {
	p.mu.Lock()
	p.lastQuery = q
	p.mu.Unlock()
	return []identity.User{{ID: "u-" + p.realm, Username: p.realm + "-user"}}, nil
}

func (p *fakeProvider) query() identity.ListUsersQuery {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastQuery
}

func (p *fakeProvider) ListRoles(_ context.Context) ([]identity.Role, error) {
	return []identity.Role{{Name: "role-" + p.realm}}, nil
}

// recordingBuilder is a ProviderBuilder that counts constructions and hands
// back a provider stamped with what it was built from.
type recordingBuilder struct {
	mu    sync.Mutex
	calls int
	built []*fakeProvider
	err   error
}

func (b *recordingBuilder) build(c *connection.Connection, clientSecret string) (identity.IdentityProvider, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.err != nil {
		return nil, b.err
	}
	p := &fakeProvider{realm: c.Realm, clientID: c.ClientID, secret: clientSecret}
	b.built = append(b.built, p)
	return p, nil
}

func (b *recordingBuilder) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// ---------------------------------------------------------------------------
// fixture
// ---------------------------------------------------------------------------

// fixture wires a resolver over in-memory stores with one live workspace and
// one active connection whose credential is genuinely sealed — not stubbed —
// so the decrypt path is the real one.
type fixture struct {
	resolver *Resolver
	keyring  *secrets.Keyring
	ws       *fakeWorkspaces
	conns    *fakeConnections
	builder  *recordingBuilder
}

const (
	testWorkspaceID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	testPublicID    = "ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	testConnID      = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	testSecret      = "super-secret-client-credential"
)

func newFixture(t interface {
	Fatalf(string, ...any)
	Helper()
}, opts Options) *fixture {
	t.Helper()

	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyring, err := secrets.NewSingleVersionKeyring(1, key)
	if err != nil {
		t.Fatalf("new keyring: %v", err)
	}

	ws := newFakeWorkspaces()
	ws.add(&workspace.Workspace{ID: testWorkspaceID, Slug: "acme", Name: "Acme", Status: workspace.StatusActive})

	conns := newFakeConnections()
	builder := &recordingBuilder{}

	f := &fixture{keyring: keyring, ws: ws, conns: conns, builder: builder}
	f.seal(t, &connection.Connection{
		ID:          testConnID,
		WorkspaceID: testWorkspaceID,
		Name:        "primary",
		Provider:    connection.ProviderKeycloak,
		Status:      connection.StatusActive,
		BaseURL:     "http://keycloak.test",
		Realm:       "realm-a",
		ClientID:    "svc-a",
		Health:      connection.HealthHealthy,
		UpdatedAt:   time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}, testSecret)

	if opts.Build == nil {
		opts.Build = builder.build
	}
	f.resolver = NewResolver(ws, conns, keyring, opts)
	if f.resolver == nil {
		t.Fatalf("NewResolver returned nil with every collaborator present")
	}
	return f
}

// seal installs a connection as its workspace's active one, sealing the given
// plaintext against it with the real Keyring and the real AAD.
func (f *fixture) seal(t interface {
	Fatalf(string, ...any)
	Helper()
}, c *connection.Connection, plaintext string) {
	t.Helper()

	sealed, err := f.keyring.Seal([]byte(plaintext), secretAAD(c.ID))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	f.conns.mu.Lock()
	f.conns.active[c.WorkspaceID] = c
	f.conns.sealed[c.ID] = &sealed
	f.conns.mu.Unlock()
}

var errBoom = errors.New("boom: driver exploded with a constraint name in it")

// stubProvider implements the WHOLE identity.IdentityProvider surface benignly.
//
// The other doubles in this file embed the interface, which is right when a
// test drives one or two methods: an unimplemented call panics loudly instead
// of silently returning a zero value. But tests that walk every route need a
// provider that answers every route, and there a panic tells you only that the
// fake is incomplete.
//
// It records mutations so a test can assert that a guard refused BEFORE the
// provider was reached.
type stubProvider struct {
	mu        sync.Mutex
	mutations []string
}

func (p *stubProvider) record(op string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mutations = append(p.mutations, op)
}

func (p *stubProvider) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.mutations...)
}

func (p *stubProvider) ListUsers(context.Context, identity.ListUsersQuery) ([]identity.User, error) {
	return nil, nil
}
func (p *stubProvider) GetUser(_ context.Context, id string) (*identity.User, error) {
	return &identity.User{ID: id, Email: "stub@example.test"}, nil
}
func (p *stubProvider) ListRoles(context.Context) ([]identity.Role, error) { return nil, nil }
func (p *stubProvider) GetRole(_ context.Context, name string) (*identity.Role, error) {
	return &identity.Role{Name: name}, nil
}
func (p *stubProvider) ListUsersByRole(context.Context, string) ([]identity.User, error) {
	return nil, nil
}
func (p *stubProvider) ListUserRoles(context.Context, string) ([]identity.Role, error) {
	return nil, nil
}
func (p *stubProvider) ListUserSessions(context.Context, string) ([]identity.Session, error) {
	return nil, nil
}
func (p *stubProvider) ListSessions(context.Context) ([]identity.Session, error) { return nil, nil }
func (p *stubProvider) ListInvitations(context.Context) ([]identity.Invitation, error) {
	return nil, nil
}
func (p *stubProvider) CreateUser(_ context.Context, req identity.CreateUserRequest) (*identity.User, error) {
	p.record("create_user:" + req.Email)
	return &identity.User{ID: testUserUUID, Email: req.Email}, nil
}
func (p *stubProvider) CreateRole(_ context.Context, req identity.CreateRoleRequest) (*identity.Role, error) {
	p.record("create_role:" + req.Name)
	return &identity.Role{Name: req.Name}, nil
}
func (p *stubProvider) CreateInvitation(_ context.Context, req identity.CreateInvitationRequest) (*identity.Invitation, error) {
	p.record("create_invitation:" + req.Email)
	return &identity.Invitation{ID: testUserUUID, Email: req.Email}, nil
}
func (p *stubProvider) UpdateUser(_ context.Context, id string, _ identity.UpdateUserRequest) (*identity.User, error) {
	p.record("update_user:" + id)
	return &identity.User{ID: id}, nil
}
func (p *stubProvider) UpdateRole(_ context.Context, name string, _ identity.UpdateRoleRequest) (*identity.Role, error) {
	p.record("update_role:" + name)
	return &identity.Role{Name: name}, nil
}
func (p *stubProvider) AssignRolesToUser(_ context.Context, id string, roles []string) error {
	p.record("assign_roles:" + id + ":" + strings.Join(roles, ","))
	return nil
}
func (p *stubProvider) UnassignRolesFromUser(_ context.Context, id string, roles []string) error {
	p.record("unassign_roles:" + id + ":" + strings.Join(roles, ","))
	return nil
}
func (p *stubProvider) SendResetPasswordEmail(_ context.Context, id string) error {
	p.record("reset_password_email:" + id)
	return nil
}
func (p *stubProvider) SetUserPassword(_ context.Context, id string, _ string, _ bool) error {
	p.record("set_password:" + id)
	return nil
}
func (p *stubProvider) ResendInvitation(_ context.Context, id string) (*identity.Invitation, error) {
	p.record("resend_invitation:" + id)
	return &identity.Invitation{ID: id}, nil
}
func (p *stubProvider) DeleteUser(_ context.Context, id string) error {
	p.record("delete_user:" + id)
	return nil
}
func (p *stubProvider) DeleteRole(_ context.Context, name string) error {
	p.record("delete_role:" + name)
	return nil
}
func (p *stubProvider) DeleteSession(_ context.Context, id string) error {
	p.record("delete_session:" + id)
	return nil
}
func (p *stubProvider) LogoutUserSessions(_ context.Context, id string) error {
	p.record("logout_sessions:" + id)
	return nil
}

// newStubFixture wires a resolver whose provider answers every method.
func newStubFixture(t *testing.T) (*fixture, *stubProvider) {
	t.Helper()
	stub := &stubProvider{}
	f := newFixture(t, Options{
		Build: func(*connection.Connection, string) (identity.IdentityProvider, error) { return stub, nil },
	})
	return f, stub
}
