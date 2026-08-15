package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auditlog"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/authz"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/connection"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identityruntime"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/project"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Layer B of the Slice 14 negative-authorization matrix: the real chain.
//
// Layer A (internal/authz) proves the DECISION is right for every route. It
// says nothing about whether the router mounts the middleware that makes it, in
// what order, or what the request had already touched by the time it was
// refused. Those are properties of the assembled system, and this file is where
// they are proven — against the real SetupRouter, the real
// AuthenticatePrincipal, the real project.Authenticator and the real
// identityruntime resolver.
//
// ─── The seam, and why it is where it is ────────────────────────────────────
//
// "The provider was never reached" is asserted through TWO counters, and the
// weaker-looking one is the stronger evidence:
//
//	workspaceLookups  the resolver's FIRST act on entering a handler
//	providerCalls     an actual call on the identity provider
//
// A rejected request must leave BOTH at zero. workspaceLookups == 0 is the
// stronger statement: it means the refusal happened before the workspace row
// was read, which is before the connection is loaded, before the sealed
// credential is opened, and therefore necessarily before any provider traffic.
// Asserting only providerCalls == 0 would also pass for an implementation that
// resolved the workspace, opened its credential, and then changed its mind.
//
// Both are compared against a positive control on the SAME route with the SAME
// body, where only the credential differs. Without that control, a route that
// 404'd for an unrelated reason would satisfy every negative assertion here.

// ─── Fixture identifiers ────────────────────────────────────────────────────

const (
	nzWorkspaceUUID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	nzWorkspace     = "ws_" + nzWorkspaceUUID

	// A second, real workspace with its own connection. Used to prove a
	// credential bound to one never reaches the other, and that a workspace
	// with no active connection does not fall back to this one.
	nzOtherWorkspaceUUID = "bbbbbbbb-0000-4000-8000-000000000002"
	nzOtherWorkspace     = "ws_" + nzOtherWorkspaceUUID

	nzProjectUUID = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	nzUserID      = "9c1e6679-7425-40de-944b-e07fc1f90ae7"
	nzSessionID   = "1a2b3c4d-0000-4000-8000-000000000005"
	nzRoleName    = "billing-reader"

	// nzUserInRealmB exists ONLY in workspace B's realm. Its counterpart,
	// nzUserID, exists only in workspace A's. The pair is what the cross-realm
	// resource-id case is built from: each is a perfectly valid identifier, in
	// the wrong realm.
	nzUserInRealmB = "4e8f6679-7425-40de-944b-e07fc1f90ae7"
)

// ─── The instrumented provider ──────────────────────────────────────────────

// nzProvider is an identity.IdentityProvider that records that it was called
// and otherwise does as little as possible.
//
// It answers successfully rather than erroring, so a positive control reaches
// the end of the handler and a mutation that WAS authorized produces its audit
// event — which is what makes "no success audit on a rejected mutation"
// falsifiable rather than vacuous.
// It also models REALM MEMBERSHIP, which is what makes the cross-realm
// resource-id case provable at this layer: a user id belongs to one realm, and
// asking the other realm's provider for it answers "not found" rather than
// answering with the user. Without that, workspace A and workspace B would
// share one flat universe of ids and an implementation that authorized the
// workspace path correctly but then acted on a foreign identifier would look
// identical to a correct one.
type nzProvider struct {
	// log is shared by every provider instance, so a call made through ANY
	// realm is visible to the assertions. Per-instance logs would let a call
	// into the wrong realm go unnoticed simply because the test held the other
	// instance's handle.
	log *nzCallLog

	// realm is the connection's realm, as the resolver built it.
	realm string

	// users are the ids that exist in this realm.
	users map[string]bool
}

// nzCallLog is the shared record of every provider call, tagged with the realm
// it went through.
type nzCallLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *nzCallLog) add(entry string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, entry)
}

func (l *nzCallLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.calls)
}

func (l *nzCallLog) entries() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.calls))
	copy(out, l.calls)
	return out
}

func (l *nzCallLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = nil
}

func (p *nzProvider) record(name string) { p.log.add(p.realm + ":" + name) }

func (p *nzProvider) callCount() int { return p.log.count() }

func (p *nzProvider) reset() { p.log.reset() }

// calls returns every provider call made during the last request, each tagged
// with the realm it went through. The tag is the whole point: "a call happened"
// is not the same evidence as "a call happened in the WRONG realm".
func (p *nzProvider) calls() []string { return p.log.entries() }

// knows reports whether an id exists in this provider's realm.
func (p *nzProvider) knows(id string) bool { return p.users[id] }

var nzSampleUser = identity.User{ID: nzUserID, Username: "ada", Email: "ada@example.test", Enabled: true}
var nzSampleRole = identity.Role{ID: "role-1", Name: nzRoleName}

// nzOtherUser is who ListUsersByRole reports, and it is deliberately NOT the
// user the request table addresses.
//
// identity.Service refuses to delete the realm's last enabled admin, and it
// decides that by listing the members of `admin`. A fake that answered with the
// very user under test would trip that guard on DELETE /users/{id} — a correct
// product rule, firing for a reason invented by the fixture, and it would make
// the positive control for that route impossible.
var nzOtherUser = identity.User{
	ID: "5d3e6679-7425-40de-944b-e07fc1f90ae7", Username: "grace",
	Email: "grace@example.test", Enabled: true,
}

func (p *nzProvider) ListUsers(context.Context, identity.ListUsersQuery) ([]identity.User, error) {
	p.record("ListUsers")
	return []identity.User{nzSampleUser}, nil
}
func (p *nzProvider) GetUser(_ context.Context, id string) (*identity.User, error) {
	p.record("GetUser")
	if !p.knows(id) {
		return nil, identity.ErrNotFound
	}
	u := nzSampleUser
	u.ID = id
	return &u, nil
}
func (p *nzProvider) ListRoles(context.Context) ([]identity.Role, error) {
	p.record("ListRoles")
	return []identity.Role{nzSampleRole}, nil
}
func (p *nzProvider) GetRole(context.Context, string) (*identity.Role, error) {
	p.record("GetRole")
	r := nzSampleRole
	return &r, nil
}
func (p *nzProvider) ListUsersByRole(context.Context, string) ([]identity.User, error) {
	p.record("ListUsersByRole")
	return []identity.User{nzOtherUser}, nil
}
func (p *nzProvider) ListUserRoles(_ context.Context, id string) ([]identity.Role, error) {
	p.record("ListUserRoles")
	if !p.knows(id) {
		return nil, identity.ErrNotFound
	}
	return []identity.Role{nzSampleRole}, nil
}
func (p *nzProvider) ListUserSessions(_ context.Context, id string) ([]identity.Session, error) {
	p.record("ListUserSessions")
	if !p.knows(id) {
		return nil, identity.ErrNotFound
	}
	return nil, nil
}
func (p *nzProvider) ListSessions(context.Context) ([]identity.Session, error) {
	p.record("ListSessions")
	return nil, nil
}
func (p *nzProvider) ListInvitations(context.Context) ([]identity.Invitation, error) {
	p.record("ListInvitations")
	return nil, nil
}
func (p *nzProvider) CreateUser(context.Context, identity.CreateUserRequest) (*identity.User, error) {
	p.record("CreateUser")
	u := nzSampleUser
	return &u, nil
}
func (p *nzProvider) CreateRole(context.Context, identity.CreateRoleRequest) (*identity.Role, error) {
	p.record("CreateRole")
	r := nzSampleRole
	return &r, nil
}
func (p *nzProvider) CreateInvitation(context.Context, identity.CreateInvitationRequest) (*identity.Invitation, error) {
	p.record("CreateInvitation")
	return &identity.Invitation{ID: nzUserID, Email: "invited@example.test"}, nil
}
func (p *nzProvider) UpdateUser(_ context.Context, id string, _ identity.UpdateUserRequest) (*identity.User, error) {
	p.record("UpdateUser")
	if !p.knows(id) {
		return nil, identity.ErrNotFound
	}
	u := nzSampleUser
	u.ID = id
	return &u, nil
}
func (p *nzProvider) UpdateRole(context.Context, string, identity.UpdateRoleRequest) (*identity.Role, error) {
	p.record("UpdateRole")
	r := nzSampleRole
	return &r, nil
}
func (p *nzProvider) AssignRolesToUser(_ context.Context, id string, _ []string) error {
	p.record("AssignRolesToUser")
	if !p.knows(id) {
		return identity.ErrNotFound
	}
	return nil
}
func (p *nzProvider) UnassignRolesFromUser(_ context.Context, id string, _ []string) error {
	p.record("UnassignRolesFromUser")
	if !p.knows(id) {
		return identity.ErrNotFound
	}
	return nil
}
func (p *nzProvider) SendResetPasswordEmail(_ context.Context, id string) error {
	p.record("SendResetPasswordEmail")
	if !p.knows(id) {
		return identity.ErrNotFound
	}
	return nil
}
func (p *nzProvider) SetUserPassword(_ context.Context, id, _ string, _ bool) error {
	p.record("SetUserPassword")
	if !p.knows(id) {
		return identity.ErrNotFound
	}
	return nil
}
func (p *nzProvider) ResendInvitation(_ context.Context, id string) (*identity.Invitation, error) {
	p.record("ResendInvitation")
	if !p.knows(id) {
		return nil, identity.ErrNotFound
	}
	return &identity.Invitation{ID: id}, nil
}
func (p *nzProvider) DeleteUser(_ context.Context, id string) error {
	p.record("DeleteUser")
	if !p.knows(id) {
		return identity.ErrNotFound
	}
	return nil
}
func (p *nzProvider) DeleteRole(context.Context, string) error {
	p.record("DeleteRole")
	return nil
}
func (p *nzProvider) DeleteSession(context.Context, string) error {
	p.record("DeleteSession")
	return nil
}
func (p *nzProvider) LogoutUserSessions(_ context.Context, id string) error {
	p.record("LogoutUserSessions")
	if !p.knows(id) {
		return identity.ErrNotFound
	}
	return nil
}

// ─── The instrumented stores ────────────────────────────────────────────────

// nzStores backs the identityruntime resolver with two live workspaces and
// counts every workspace read.
//
// The count is the "did this request get past authorization" seam described at
// the top of the file. It is incremented in GetByID because that is the
// resolver's first act, and because a request refused earlier cannot possibly
// have reached it.
type nzStores struct {
	mu               sync.Mutex
	workspaceLookups int

	// workspaces and connections are keyed by bare UUID, which is what the
	// resolver passes after parsing `ws_<uuid>`.
	workspaces  map[string]*workspace.Workspace
	connections map[string]*connection.Connection
	sealed      map[string]*secrets.Sealed
}

func (s *nzStores) GetByID(_ context.Context, id string) (*workspace.Workspace, error) {
	s.mu.Lock()
	s.workspaceLookups++
	ws := s.workspaces[id]
	s.mu.Unlock()
	return ws, nil
}

func (s *nzStores) GetActiveByWorkspace(_ context.Context, workspaceID string) (*connection.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connections[workspaceID], nil
}

func (s *nzStores) OpenSecret(_ context.Context, id string) (*secrets.Sealed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sealed[id], nil
}

func (s *nzStores) lookups() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workspaceLookups
}

func (s *nzStores) resetLookups() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaceLookups = 0
}

func (s *nzStores) archiveWorkspace(uuid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaces[uuid].Status = workspace.StatusArchived
}

func (s *nzStores) unarchiveWorkspace(uuid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaces[uuid].Status = workspace.StatusActive
}

func (s *nzStores) retireConnection(workspaceUUID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.connections, workspaceUUID)
}

func (s *nzStores) restoreConnection(workspaceUUID string, c *connection.Connection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connections[workspaceUUID] = c
}

// ─── The credential store ───────────────────────────────────────────────────

// nzCredentials is a project.Repository slice big enough to authenticate.
//
// The REAL project.Authenticator runs on top of it — token parsing, the indexed
// lookup, the constant-time hash comparison, the usability check and the
// project-status check are all the production code path. Only storage is
// substituted, which is the one thing a unit test cannot have.
type nzCredentials struct {
	stubProjectRepo

	mu    sync.Mutex
	byKey map[string]*nzCredential
}

type nzCredential struct {
	token string
	cred  *project.Credential
	proj  *project.Project
}

func (r *nzCredentials) FindByKeyPrefix(_ context.Context, prefix string) (*project.Credential, *project.Project, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.byKey[prefix]
	if !ok {
		return nil, nil, nil
	}
	return entry.cred, entry.proj, nil
}

func (r *nzCredentials) TouchLastUsed(context.Context, string, time.Time) error { return nil }

// WithTx returns the receiver. Authentication performs no transactional write,
// so this store has nothing to bind; the control-plane atomicity proof lives in
// internal/project's integration suite.
func (r *nzCredentials) WithTx(database.Tx) project.Repository { return r }

// mint creates a usable credential and returns its one-time token.
func (r *nzCredentials) mint(t *testing.T, projectUUID, workspaceUUID string, scopes []string) string {
	t.Helper()
	minted, err := project.MintCredential()
	if err != nil {
		t.Fatalf("mint credential: %v", err)
	}

	entry := &nzCredential{
		token: minted.Token,
		cred: &project.Credential{
			ID:         "cred-" + minted.KeyPrefix,
			ProjectID:  projectUUID,
			Label:      "negative-matrix",
			KeyPrefix:  minted.KeyPrefix,
			KeyHash:    minted.KeyHash,
			KeyHashAlg: "sha256",
			Scopes:     scopes,
			CreatedAt:  time.Now().Add(-time.Hour),
		},
		proj: &project.Project{
			ID:          projectUUID,
			WorkspaceID: workspaceUUID,
			Name:        "negative matrix",
			Status:      project.StatusActive,
		},
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byKey == nil {
		r.byKey = map[string]*nzCredential{}
	}
	r.byKey[minted.KeyPrefix] = entry
	return minted.Token
}

// mutate applies a change to the stored credential or project behind a token.
// Used for the lifecycle transitions — revoke, expire, archive the project —
// that must take effect on the very next request with no restart.
func (r *nzCredentials) mutate(t *testing.T, token string, fn func(*nzCredential)) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, entry := range r.byKey {
		if entry.token == token {
			fn(entry)
			return
		}
	}
	t.Fatalf("no stored credential for the given token")
}

// ─── The audit capture ──────────────────────────────────────────────────────

// nzAudit records every domain event emitted while a request runs.
//
// It is installed as the package-level recorder for the duration of one test,
// which is why the tests that use it must not run in parallel with each other.
// audit.SetDefault is process-global by design (the alternative is threading a
// recorder through every handler), so the capture has to be too.
type nzAudit struct {
	mu     sync.Mutex
	events []audit.Event
}

func (a *nzAudit) Record(_ context.Context, e audit.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

func (a *nzAudit) snapshot() []audit.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]audit.Event, len(a.events))
	copy(out, a.events)
	return out
}

func (a *nzAudit) reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = nil
}

// captureAudit installs a capturing recorder and restores the previous one.
func captureAudit(t *testing.T) *nzAudit {
	t.Helper()
	rec := &nzAudit{}
	previous := audit.SetDefault(rec)
	t.Cleanup(func() { audit.SetDefault(previous) })
	return rec
}

// ─── The durable audit store ────────────────────────────────────────────────

// nzAuditStore is an in-memory auditlog.Store, so GET /v1/workspaces/{id}/audit
// is actually mounted and actually answers.
//
// Without it the audit route would be absent and every `audit:read` assertion
// would be testing a 404.
//
// It counts reads for the same reason nzStores counts workspace lookups: the
// audit route does NOT go through the identityruntime resolver — it reads the
// trail directly — so the workspace-lookup counter is blind to it. Its own
// counter is what makes "the trail was never read" a real assertion for that
// route instead of a vacuous one.
type nzAuditStore struct {
	mu    sync.Mutex
	lists int
}

func (s *nzAuditStore) Record(context.Context, auditlog.Record) error { return nil }

func (s *nzAuditStore) List(context.Context, auditlog.Query) (auditlog.Page, error) {
	s.mu.Lock()
	s.lists++
	s.mu.Unlock()
	return auditlog.Page{}, nil
}

func (s *nzAuditStore) DeleteOlderThan(context.Context, time.Time) (int64, error) { return 0, nil }

// WithTx returns the receiver. This store has no transaction; the atomicity
// proof lives in internal/*/…_integration_test.go against a real database.
func (s *nzAuditStore) WithTx(database.Tx) auditlog.Store { return s }

func (s *nzAuditStore) reads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lists
}

func (s *nzAuditStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lists = 0
}

// ─── The assembled installation ─────────────────────────────────────────────

// nzInstallation is everything one negative-matrix test needs: a real router,
// the counters behind it, and the credential store it authenticates against.
type nzInstallation struct {
	router      *gin.Engine
	stores      *nzStores
	provider    *nzProvider
	credentials *nzCredentials
	auditStore  *nzAuditStore

	connA *connection.Connection
}

// newNegativeInstallation assembles the router the way the composition root
// does, with every /v1 surface wired.
//
// Rate limits are raised well above anything these tests generate. Throttling
// is a real property with its own tests (ratelimit_v1_test.go); here it would
// only turn a matrix sweep into a flaky 429, and a 429 in the middle of a
// scope sweep would be indistinguishable from the denial being asserted.
func newNegativeInstallation(t *testing.T) *nzInstallation {
	t.Helper()
	return newNegativeInstallationWithLimits(t, RateLimitSettings{EdgeRPS: 5000, CredentialRPS: 5000})
}

// newNegativeInstallationWithLimits is the same installation with the rate
// limits under the caller's control, for the two tests whose subject IS the
// limiter's position in the chain.
func newNegativeInstallationWithLimits(t *testing.T, limits RateLimitSettings) *nzInstallation {
	t.Helper()

	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyring, err := secrets.NewSingleVersionKeyring(1, key)
	if err != nil {
		t.Fatalf("new keyring: %v", err)
	}

	now := time.Now().UTC()
	connA := &connection.Connection{
		ID: "conn-a", WorkspaceID: nzWorkspaceUUID, Name: "realm A",
		Provider: connection.ProviderKeycloak, Status: connection.StatusActive,
		BaseURL: "http://provider.invalid", Realm: "realm-a", ClientID: "lw-conn",
		AccessMode: connection.AccessModeFull, UpdatedAt: now,
	}
	connB := &connection.Connection{
		ID: "conn-b", WorkspaceID: nzOtherWorkspaceUUID, Name: "realm B",
		Provider: connection.ProviderKeycloak, Status: connection.StatusActive,
		BaseURL: "http://provider.invalid", Realm: "realm-b", ClientID: "lw-conn",
		AccessMode: connection.AccessModeFull, UpdatedAt: now,
	}

	sealed := map[string]*secrets.Sealed{}
	for _, c := range []*connection.Connection{connA, connB} {
		s, err := keyring.Seal([]byte("provider-client-secret"), secrets.AAD("connection", c.ID, "client_secret"))
		if err != nil {
			t.Fatalf("seal connection secret: %v", err)
		}
		sealed[c.ID] = &s
	}

	stores := &nzStores{
		workspaces: map[string]*workspace.Workspace{
			nzWorkspaceUUID:      {ID: nzWorkspaceUUID, Slug: "alpha", Name: "Alpha", Status: workspace.StatusActive},
			nzOtherWorkspaceUUID: {ID: nzOtherWorkspaceUUID, Slug: "bravo", Name: "Bravo", Status: workspace.StatusActive},
		},
		connections: map[string]*connection.Connection{
			nzWorkspaceUUID:      connA,
			nzOtherWorkspaceUUID: connB,
		},
		sealed: sealed,
	}

	// One provider instance per realm, over a shared call log.
	//
	// The realms hold DISJOINT user ids, which is what turns "workspace B was
	// authorized correctly" into a real question: an implementation that checked
	// the workspace in the path and then acted on whatever identifier it was
	// given would reach realm B's provider with realm A's user id, and realm B
	// would have to invent a user to satisfy it.
	callLog := &nzCallLog{}
	provider := &nzProvider{log: callLog, realm: "realm-a", users: map[string]bool{
		nzUserID: true, nzOtherUser.ID: true,
	}}
	providerB := &nzProvider{log: callLog, realm: "realm-b", users: map[string]bool{
		nzUserInRealmB: true, nzOtherUser.ID: true,
	}}

	resolver := identityruntime.NewResolver(stores, stores, keyring, identityruntime.Options{
		Build: func(c *connection.Connection, _ string) (identity.IdentityProvider, error) {
			if c.Realm == "realm-b" {
				return providerB, nil
			}
			return provider, nil
		},
	})
	if resolver == nil {
		t.Fatal("identityruntime.NewResolver returned nil with every collaborator present")
	}

	credentials := &nzCredentials{}
	auditStore := &nzAuditStore{}
	auditService := auditlog.NewService(auditStore)
	if auditService == nil {
		t.Fatal("auditlog.NewService returned nil for a non-nil store")
	}

	r := newGin()
	SetupRouter(r, RouterDeps{
		User:              SetupUser(&gorm.DB{}),
		Provider:          &fakeProvider{err: auth.ErrInvalidToken},
		AdminChecker:      &fakeAdminChecker{allow: true},
		Workspace:         stubWorkspaceHandler(),
		Connection:        stubConnectionHandler(t),
		Project:           stubProjectHandler(),
		ProjectAuth:       project.NewAuthenticator(credentials),
		WorkspaceAudit:    auditlog.NewHandler(auditService),
		WorkspaceIdentity: identityruntime.NewHandler(resolver),
		RateLimits:        limits,
	})

	return &nzInstallation{
		router: r, stores: stores, provider: provider,
		credentials: credentials, auditStore: auditStore, connA: connA,
	}
}

// resetCounters zeroes every "was this reached" seam.
func (in *nzInstallation) resetCounters() {
	in.stores.resetLookups()
	in.provider.reset()
	in.auditStore.reset()
}

// reachedBackingState reports whether the request got past authorization and
// into a handler that touched something.
//
// Two seams, because there are two kinds of handler behind /v1: the identity
// routes resolve a workspace, and the audit route reads the trail directly.
// Summing them means one assertion covers both, and a route that started
// reading something new would have to be given a seam here rather than
// silently escaping the check.
func (in *nzInstallation) reachedBackingState() int {
	return in.stores.lookups() + in.auditStore.reads()
}

// send issues one request and returns the recorder, having first zeroed every
// counter so the caller reads only this request's effect.
func (in *nzInstallation) send(method, path, token, body string) *httptest.ResponseRecorder {
	in.resetCounters()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	in.router.ServeHTTP(w, req)
	return w
}

// sendRaw issues a request with a verbatim Authorization header, for the
// malformed-header cases a token string cannot express.
func (in *nzInstallation) sendRaw(method, path, authorization, body string) *httptest.ResponseRecorder {
	in.resetCounters()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	in.router.ServeHTTP(w, req)
	return w
}

// untouched asserts the request never reached any backing state, and therefore
// never reached the provider.
func (in *nzInstallation) untouched(t *testing.T, what string) {
	t.Helper()
	if got := in.stores.lookups(); got != 0 {
		t.Errorf("%s: the workspace was read %d time(s); the refusal happened AFTER resolution began",
			what, got)
	}
	if got := in.auditStore.reads(); got != 0 {
		t.Errorf("%s: the durable audit trail was read %d time(s) on a rejected request", what, got)
	}
	if got := in.provider.callCount(); got != 0 {
		t.Errorf("%s: the identity provider was called %d time(s) on a rejected request", what, got)
	}
}

// ─── The concrete request table ─────────────────────────────────────────────

// nzRequest is one project-reachable route, made concrete.
//
// Body is what makes a positive control actually reach the provider rather than
// stopping at validation. It is deliberately a hand-written literal per route:
// a generated body would drift from the DTOs silently, and the point of the
// positive control is that it genuinely succeeds.
type nzRequest struct {
	Method string
	Path   string // gin pattern, matching the registry key
	Body   string

	// ProviderCall is the identity.IdentityProvider method an authorized
	// request must invoke. Empty means the handler legitimately answers without
	// touching the provider, which no route does today — the field exists so
	// that a route which stopped calling the provider is a visible decision.
	ProviderCall string
}

// URL renders the gin pattern as a concrete path inside the given workspace.
func (r nzRequest) URL(workspaceID string) string {
	return strings.NewReplacer(
		":workspace_id", workspaceID,
		":user_id", nzUserID,
		":role_name", nzRoleName,
		":session_id", nzSessionID,
		":invitation_id", nzUserID,
	).Replace(r.Path)
}

// Key is the registry key this request exercises.
func (r nzRequest) Key() string { return r.Method + " " + r.Path }

// IsMutation reports whether this request changes provider state, derived from
// the scope the registry demands rather than from the HTTP verb.
//
// The verb would be wrong for `POST .../reset-password` in one direction and
// for nothing in the other; the scope is the thing the product actually reasons
// about, and deriving from it means a route reclassified in the registry is
// reclassified here too.
func (r nzRequest) IsMutation() bool {
	req, ok := authz.RequirementFor(r.Method, r.Path)
	return ok && !req.OperatorOnly && req.Scope.IsWrite()
}

// Scope is the capability the registry demands for this route.
func (r nzRequest) Scope() authz.Scope {
	req, _ := authz.RequirementFor(r.Method, r.Path)
	return req.Scope
}

const nzWS = "/v1/workspaces/:workspace_id"

// nzRequests is the concrete form of every project-reachable route.
//
// It is hand-written, and TestNegative_TheRequestTableCoversEveryRoute is what
// stops that being a liability: the table is compared against
// authz.ProjectReachableRoutes() in both directions, so a route added to the
// registry fails this package until someone writes the request that exercises
// it. That is the gate the slice asks for —
//
//	new route added → developer forgets negative test → CI stays green
//
// broken at the point where the omission is cheapest to fix.
var nzRequests = []nzRequest{
	// users
	{Method: "GET", Path: nzWS + "/users", ProviderCall: "ListUsers"},
	{Method: "POST", Path: nzWS + "/users", ProviderCall: "CreateUser",
		Body: `{"email":"new@example.test","first_name":"New","last_name":"User","temporary_password":"lw-negative-matrix-pw"}`},
	{Method: "GET", Path: nzWS + "/users/:user_id", ProviderCall: "GetUser"},
	{Method: "PATCH", Path: nzWS + "/users/:user_id", ProviderCall: "UpdateUser",
		Body: `{"first_name":"Renamed"}`},
	{Method: "DELETE", Path: nzWS + "/users/:user_id", ProviderCall: "DeleteUser"},

	// password
	{Method: "POST", Path: nzWS + "/users/:user_id/reset-password", ProviderCall: "SendResetPasswordEmail"},

	// user roles
	{Method: "GET", Path: nzWS + "/users/:user_id/roles", ProviderCall: "ListUserRoles"},
	{Method: "POST", Path: nzWS + "/users/:user_id/roles", ProviderCall: "AssignRolesToUser",
		Body: `{"roles":["` + nzRoleName + `"]}`},
	{Method: "DELETE", Path: nzWS + "/users/:user_id/roles/:role_name", ProviderCall: "UnassignRolesFromUser"},

	// sessions
	{Method: "GET", Path: nzWS + "/users/:user_id/sessions", ProviderCall: "ListUserSessions"},
	{Method: "DELETE", Path: nzWS + "/users/:user_id/sessions", ProviderCall: "LogoutUserSessions"},
	{Method: "GET", Path: nzWS + "/sessions", ProviderCall: "ListSessions"},
	{Method: "DELETE", Path: nzWS + "/sessions/:session_id", ProviderCall: "DeleteSession"},

	// roles
	{Method: "GET", Path: nzWS + "/roles", ProviderCall: "ListRoles"},
	{Method: "POST", Path: nzWS + "/roles", ProviderCall: "CreateRole",
		Body: `{"name":"negative-matrix-role","description":"created by the matrix"}`},
	{Method: "GET", Path: nzWS + "/roles/:role_name", ProviderCall: "GetRole"},
	{Method: "PATCH", Path: nzWS + "/roles/:role_name", ProviderCall: "UpdateRole",
		Body: `{"description":"edited by the matrix"}`},
	{Method: "DELETE", Path: nzWS + "/roles/:role_name", ProviderCall: "DeleteRole"},
	{Method: "GET", Path: nzWS + "/roles/:role_name/users", ProviderCall: "ListUsersByRole"},

	// audit
	{Method: "GET", Path: nzWS + "/audit"},

	// invitations
	{Method: "GET", Path: nzWS + "/invitations", ProviderCall: "ListInvitations"},
	{Method: "POST", Path: nzWS + "/invitations", ProviderCall: "CreateInvitation",
		Body: `{"email":"invited@example.test","first_name":"In","last_name":"Vited","roles":["` + nzRoleName + `"]}`},
	{Method: "DELETE", Path: nzWS + "/invitations/:invitation_id", ProviderCall: "DeleteUser"},
	{Method: "POST", Path: nzWS + "/invitations/:invitation_id/resend", ProviderCall: "ResendInvitation"},
}

// nzMutations returns the subset that changes provider state.
func nzMutations() []nzRequest {
	var out []nzRequest
	for _, r := range nzRequests {
		if r.IsMutation() {
			out = append(out, r)
		}
	}
	return out
}

// statusAndCode reads the /v1 envelope off a recorder.
//
// A body that is not the envelope is a failure of the test's own setup — a
// route that is not mounted, a panic recovered into gin's default 404 — and is
// reported as an empty code rather than fataling, so the caller can say which
// route it was looking at.
func statusAndCode(t *testing.T, w *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	if w.Code < 400 {
		return w.Code, ""
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		return w.Code, ""
	}
	return w.Code, body.Error.Code
}

// nzEnvelopeField reads one field out of the /v1 error envelope's `error`
// object. Returns "" when the body is not an envelope or the field is absent,
// which is what every caller wants to assert about anyway.
func nzEnvelopeField(t *testing.T, body, field string) string {
	t.Helper()
	var parsed struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return ""
	}
	v, ok := parsed.Error[field].(string)
	if !ok {
		return ""
	}
	return v
}

// nzStripRequestID renders an error envelope with the correlation id removed,
// so two responses can be compared for everything else. The id differs per
// request by design, and is the only thing that legitimately does.
func nzStripRequestID(t *testing.T, body string) string {
	t.Helper()
	var parsed struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return body
	}
	delete(parsed.Error, "request_id")
	out, err := json.Marshal(parsed)
	if err != nil {
		return body
	}
	return string(out)
}

// timeNowUTC is the clock the lifecycle transitions use. Named rather than
// inlined so a reader can see that revocation and expiry are compared against
// the same instant the authenticator uses.
func timeNowUTC() time.Time { return time.Now().UTC() }
