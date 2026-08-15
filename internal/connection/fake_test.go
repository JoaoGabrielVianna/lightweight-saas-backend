package connection

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
)

const (
	fixtureWorkspaceID = "5a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d"
	fixtureConnID      = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	fixtureConnID2     = "7b8c9d0e-1f2a-4b3c-9d4e-5f6a7b8c9d0e"
)

var testNow = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

func fixedClock() func() time.Time { return func() time.Time { return testNow } }

func ctx() context.Context { return context.Background() }

// ---------------------------------------------------------------------------
// fake repository
// ---------------------------------------------------------------------------

// fakeRepository reproduces the behaviours the service depends on, including
// the one-active-per-workspace rule and the status guards. The properties it
// cannot express — the CHECK constraints, the partial unique index under real
// concurrency — are covered by repository_integration_test.go.
type fakeRepository struct {
	mu      sync.Mutex
	items   map[string]*Connection
	secrets map[string]secrets.Sealed

	failWith error
}

func newFakeRepository(seed ...*Connection) *fakeRepository {
	r := &fakeRepository{
		items:   map[string]*Connection{},
		secrets: map[string]secrets.Sealed{},
	}
	for _, c := range seed {
		copied := *c
		r.items[c.ID] = &copied
	}
	return r
}

// WithTx returns the receiver: a fake has no transaction to bind to. That is
// the honest shape, and it is why the rollback proof lives in the integration
// suite rather than here.
func (r *fakeRepository) WithTx(database.Tx) Repository { return r }

func (r *fakeRepository) Create(_ context.Context, c *Connection, sealed secrets.Sealed) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return r.failWith
	}
	copied := *c
	r.items[c.ID] = &copied
	r.secrets[c.ID] = sealed
	return nil
}

func (r *fakeRepository) GetByID(_ context.Context, id string) (*Connection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return nil, r.failWith
	}
	c, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	copied := *c
	return &copied, nil
}

func (r *fakeRepository) List(_ context.Context, workspaceID string, filter StatusFilter) ([]Connection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return nil, r.failWith
	}
	out := []Connection{}
	for _, c := range r.items {
		if c.WorkspaceID != workspaceID {
			continue
		}
		if filter != FilterAll && string(c.Status) != string(filter) {
			continue
		}
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (r *fakeRepository) OpenSecret(_ context.Context, id string) (*secrets.Sealed, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return nil, r.failWith
	}
	s, ok := r.secrets[id]
	if !ok {
		return nil, nil
	}
	return &s, nil
}

func (r *fakeRepository) UpdateConfig(_ context.Context, id string, patch ConfigPatch, sealed *secrets.Sealed, now time.Time) (*Connection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return nil, r.failWith
	}
	c, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	if c.Status == StatusRetired {
		return nil, ErrRetired
	}
	if c.Status != StatusDraft {
		return nil, ErrNotDraft
	}

	if patch.Name != nil {
		c.Name = *patch.Name
	}
	if patch.BaseURL != nil {
		c.BaseURL = *patch.BaseURL
	}
	if patch.Realm != nil {
		c.Realm = *patch.Realm
	}
	if patch.ClientID != nil {
		c.ClientID = *patch.ClientID
	}
	if sealed != nil {
		r.secrets[id] = *sealed
	}
	if patch.BaseURL != nil || patch.Realm != nil || patch.ClientID != nil || sealed != nil {
		c.Health = HealthUnknown
		c.HealthMessage = ""
		c.AccessMode = AccessModeUnknown
		c.LastVerifiedAt = nil
	}
	c.UpdatedAt = now

	copied := *c
	return &copied, nil
}

func (r *fakeRepository) SaveVerification(_ context.Context, id string, report VerifyReport) (*Connection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return nil, r.failWith
	}
	c, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	if report.OK {
		c.Health = HealthHealthy
	} else {
		c.Health = HealthUnhealthy
	}
	c.HealthMessage = report.Summary
	c.AccessMode = report.AccessMode
	at := report.CheckedAt
	c.LastVerifiedAt = &at
	c.UpdatedAt = at

	copied := *c
	return &copied, nil
}

func (r *fakeRepository) Activate(_ context.Context, id, workspaceID string, now time.Time) (*Connection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return nil, r.failWith
	}
	c, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	for otherID, other := range r.items {
		if otherID != id && other.WorkspaceID == workspaceID && other.Status == StatusActive {
			other.Status = StatusRetired
			at := now
			other.RetiredAt = &at
			other.UpdatedAt = now
		}
	}
	c.Status = StatusActive
	at := now
	c.ActivatedAt = &at
	c.UpdatedAt = now

	copied := *c
	return &copied, nil
}

func (r *fakeRepository) Retire(_ context.Context, id string, now time.Time) (*Connection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return nil, r.failWith
	}
	c, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	if c.Status == StatusRetired {
		return nil, ErrRetired
	}
	c.Status = StatusRetired
	at := now
	c.RetiredAt = &at
	c.UpdatedAt = now

	copied := *c
	return &copied, nil
}

func (r *fakeRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return r.failWith
	}
	c, ok := r.items[id]
	if !ok {
		return nil
	}
	if c.Status == StatusActive {
		return ErrActiveCannotDelete
	}
	delete(r.items, id)
	delete(r.secrets, id)
	return nil
}

// ---------------------------------------------------------------------------
// fake workspace store
// ---------------------------------------------------------------------------

type fakeWorkspaces struct {
	items    map[string]*workspace.Workspace
	failWith error
}

func newFakeWorkspaces(items ...*workspace.Workspace) *fakeWorkspaces {
	f := &fakeWorkspaces{items: map[string]*workspace.Workspace{}}
	for _, w := range items {
		f.items[w.ID] = w
	}
	return f
}

func (f *fakeWorkspaces) GetByID(_ context.Context, id string) (*workspace.Workspace, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	w, ok := f.items[id]
	if !ok {
		return nil, nil
	}
	copied := *w
	return &copied, nil
}

func activeWorkspaceFixture() *workspace.Workspace {
	return &workspace.Workspace{
		ID: fixtureWorkspaceID, Slug: "production", Name: "Production",
		Status: workspace.StatusActive, CreatedAt: testNow, UpdatedAt: testNow,
	}
}

func archivedWorkspaceFixture() *workspace.Workspace {
	at := testNow
	return &workspace.Workspace{
		ID: fixtureWorkspaceID, Slug: "production", Name: "Production",
		Status: workspace.StatusArchived, CreatedAt: testNow, UpdatedAt: testNow, ArchivedAt: &at,
	}
}

// ---------------------------------------------------------------------------
// fake verifier
// ---------------------------------------------------------------------------

// fakeVerifier returns a canned report and records what it was asked to probe —
// which is how the tests assert that the secret was opened correctly and handed
// over in plaintext.
type fakeVerifier struct {
	mu      sync.Mutex
	report  VerifyReport
	targets []VerifyTarget
}

func (f *fakeVerifier) Verify(_ context.Context, target VerifyTarget) VerifyReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = append(f.targets, target)
	report := f.report
	if report.CheckedAt.IsZero() {
		report.CheckedAt = testNow
	}
	return report
}

func (f *fakeVerifier) lastTarget() (VerifyTarget, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.targets) == 0 {
		return VerifyTarget{}, false
	}
	return f.targets[len(f.targets)-1], true
}

func okReport() VerifyReport {
	return VerifyReport{
		OK:         true,
		AccessMode: AccessModeFull,
		Summary:    "all good",
		CheckedAt:  testNow,
		Checks: []Check{
			{Name: CheckReachable, OK: true},
			{Name: CheckRealmExists, OK: true},
			{Name: CheckClientAuth, OK: true},
			{Name: CheckRealmRead, OK: true},
			{Name: CheckUsersListing, OK: true},
		},
	}
}

func failedReport() VerifyReport {
	return VerifyReport{
		OK:         false,
		AccessMode: AccessModeUnknown,
		Summary:    "admin client authentication failed",
		CheckedAt:  testNow,
		Checks: []Check{
			{Name: CheckReachable, OK: true},
			{Name: CheckRealmExists, OK: true},
			{Name: CheckClientAuth, OK: false, Detail: "client id or client secret was rejected"},
		},
	}
}

// ---------------------------------------------------------------------------
// service assembly
// ---------------------------------------------------------------------------

func testKeyring() *secrets.Keyring {
	k, err := secrets.NewSingleVersionKeyring(1, bytes.Repeat([]byte{0x11}, secrets.KeySize))
	if err != nil {
		panic(err)
	}
	return k
}

type harness struct {
	svc        *Service
	repo       *fakeRepository
	workspaces *fakeWorkspaces
	verifier   *fakeVerifier
	keyring    *secrets.Keyring

	// runner is the transaction seam, exposed so a test can assert that one
	// mutation opens exactly ONE transaction. Two would mean the domain write
	// and the audit row can commit separately.
	runner *fakeRunner
}

// newHarness assembles a Service over fakes, with a frozen clock.
func newHarness(t interface{ Fatalf(string, ...any) }, ws *workspace.Workspace, seed ...*Connection) *harness {
	repo := newFakeRepository(seed...)
	workspaces := newFakeWorkspaces(ws)
	verifier := &fakeVerifier{report: okReport()}
	keyring := testKeyring()

	runner := &fakeRunner{}
	svc := NewService(repo, workspaces, keyring, verifier, runner, &fakeAuditWriter{})
	if svc == nil {
		t.Fatalf("NewService returned nil with every collaborator present")
	}
	svc.now = fixedClock()

	// Seed secrets for any pre-existing connections so Verify can open them.
	for _, c := range seed {
		sealed, err := keyring.Seal([]byte("seeded-secret"), secretAAD(c.ID))
		if err != nil {
			t.Fatalf("seal seeded secret: %v", err)
		}
		repo.secrets[c.ID] = sealed
	}

	return &harness{svc: svc, repo: repo, workspaces: workspaces, verifier: verifier, keyring: keyring, runner: runner}
}

// draftConnection builds a draft fixture.
func draftConnection(id, name string) *Connection {
	return &Connection{
		ID: id, WorkspaceID: fixtureWorkspaceID, Name: name,
		Provider: ProviderKeycloak, Status: StatusDraft,
		BaseURL: "https://kc.example.com", Realm: "saas", ClientID: "svc",
		Health: HealthUnknown, AccessMode: AccessModeUnknown,
		CreatedAt: testNow, UpdatedAt: testNow,
	}
}

// verifiedConnection builds a draft that has just passed verification.
func verifiedConnection(id, name string) *Connection {
	c := draftConnection(id, name)
	at := testNow
	c.Health = HealthHealthy
	c.AccessMode = AccessModeFull
	c.LastVerifiedAt = &at
	return c
}

// activeConnection builds an active fixture.
func activeConnection(id, name string) *Connection {
	c := verifiedConnection(id, name)
	at := testNow
	c.Status = StatusActive
	c.ActivatedAt = &at
	return c
}

// retiredConnection builds a retired fixture.
func retiredConnection(id, name string) *Connection {
	c := draftConnection(id, name)
	at := testNow
	c.Status = StatusRetired
	c.RetiredAt = &at
	return c
}

var errBoom = errors.New("connection reset by peer")
