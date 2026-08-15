package workspace

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
)

// fakeRepository is an in-memory Repository for service and handler tests.
//
// It reproduces the behaviours the service depends on — slug uniqueness, the
// active-only guard on writes, (nil, nil) for absent rows, ordering by
// (name, id) — so a test that passes here is testing the real contract rather
// than a permissive stub. The behaviours it CANNOT reproduce (the CHECK
// constraints, real concurrency on the unique index) are covered by
// repository_integration_test.go against PostgreSQL.
type fakeRepository struct {
	mu    sync.Mutex
	items map[string]*Workspace

	// failWith, when set, is returned by every method. Used to exercise the
	// handler's internal_error path without a real database failure.
	failWith error

	createCalls int
}

func newFakeRepository(seed ...*Workspace) *fakeRepository {
	r := &fakeRepository{items: make(map[string]*Workspace, len(seed))}
	for _, w := range seed {
		copied := *w
		r.items[w.ID] = &copied
	}
	return r
}

// WithTx returns the receiver: a fake has no transaction to bind to.
//
// That is not a shortcut, it is the honest shape — and it is exactly why the
// rollback proof cannot live in this file. See fake_audit_test.go.
func (r *fakeRepository) WithTx(database.Tx) Repository { return r }

func (r *fakeRepository) Create(_ context.Context, w *Workspace) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createCalls++
	if r.failWith != nil {
		return r.failWith
	}
	for _, existing := range r.items {
		if existing.Slug == w.Slug {
			return ErrSlugTaken
		}
	}
	copied := *w
	r.items[w.ID] = &copied
	return nil
}

func (r *fakeRepository) GetByID(_ context.Context, id string) (*Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return nil, r.failWith
	}
	w, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	copied := *w
	return &copied, nil
}

func (r *fakeRepository) List(_ context.Context, filter StatusFilter) ([]Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return nil, r.failWith
	}

	out := make([]Workspace, 0, len(r.items))
	for _, w := range r.items {
		if filter != FilterAll && string(w.Status) != string(filter) {
			continue
		}
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (r *fakeRepository) UpdateName(_ context.Context, id, name string, now time.Time) (*Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return nil, r.failWith
	}
	w, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	if w.Status == StatusArchived {
		return nil, ErrArchived
	}
	w.Name = name
	w.UpdatedAt = now
	copied := *w
	return &copied, nil
}

func (r *fakeRepository) Archive(_ context.Context, id string, now time.Time) (*Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return nil, r.failWith
	}
	w, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	if w.Status == StatusActive {
		w.Status = StatusArchived
		w.ArchivedAt = &now
		w.UpdatedAt = now
	}
	copied := *w
	return &copied, nil
}

// fixedClock returns a deterministic now() so tests can assert on exact
// timestamps without sleeping or tolerating skew.
var testNow = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

func fixedClock() func() time.Time {
	return func() time.Time { return testNow }
}

// newTestService builds a Service over a fake repository with a frozen clock.
//
// The transactional collaborators are wired to fakes that always succeed, so
// the twenty-odd existing tests keep asserting what they always asserted. The
// tests that care about the audit half construct their own — see
// newAuditedService in fake_audit_test.go.
func newTestService(seed ...*Workspace) (*Service, *fakeRepository) {
	repo := newFakeRepository(seed...)
	svc := NewService(repo, &fakeRunner{}, &fakeAuditWriter{})
	svc.now = fixedClock()
	return svc, repo
}

// activeWorkspace is a convenience constructor for seeded fixtures.
func activeWorkspace(id, slug, name string) *Workspace {
	return &Workspace{
		ID:        id,
		Slug:      slug,
		Name:      name,
		Status:    StatusActive,
		CreatedAt: testNow,
		UpdatedAt: testNow,
	}
}

// archivedWorkspace is a convenience constructor for seeded fixtures.
func archivedWorkspace(id, slug, name string) *Workspace {
	at := testNow
	return &Workspace{
		ID:         id,
		Slug:       slug,
		Name:       name,
		Status:     StatusArchived,
		CreatedAt:  testNow,
		UpdatedAt:  testNow,
		ArchivedAt: &at,
	}
}
