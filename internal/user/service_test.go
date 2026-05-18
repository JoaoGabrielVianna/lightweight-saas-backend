package user

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
)

// mockRepo is an in-memory UserRepository for service tests. Lookups by
// sub model the unique index; concurrent inserts return errUniqueConflict
// so we can exercise Service.EnsureUser's race-recovery path.
type mockRepo struct {
	mu             sync.Mutex
	bySub          map[string]*User
	byID           map[uint]*User
	nextID         uint
	createErr      error
	updateErr      error
	findBySubErr   error
	createCalls    atomic.Int32
	updateCalls    atomic.Int32
	findBySubCalls atomic.Int32
}

var errUniqueConflict = errors.New("UNIQUE constraint failed: users.keycloak_sub")

func newMockRepo() *mockRepo {
	return &mockRepo{
		bySub: map[string]*User{},
		byID:  map[uint]*User{},
	}
}

func (m *mockRepo) Create(u *User) error {
	m.createCalls.Add(1)
	if m.createErr != nil {
		return m.createErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.bySub[u.KeycloakSub]; exists {
		return errUniqueConflict
	}
	m.nextID++
	u.ID = m.nextID
	m.bySub[u.KeycloakSub] = u
	m.byID[u.ID] = u
	return nil
}

func (m *mockRepo) Update(u *User) error {
	m.updateCalls.Add(1)
	if m.updateErr != nil {
		return m.updateErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bySub[u.KeycloakSub] = u
	m.byID[u.ID] = u
	return nil
}

func (m *mockRepo) FindBySub(sub string) (*User, error) {
	m.findBySubCalls.Add(1)
	if m.findBySubErr != nil {
		return nil, m.findBySubErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.bySub[sub]; ok {
		return u, nil
	}
	return nil, nil
}

func (m *mockRepo) FindByID(id uint) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.byID[id]; ok {
		return u, nil
	}
	return nil, nil
}

func sampleIdentity() *auth.Identity {
	return &auth.Identity{
		Subject:  "kc-sub-1",
		Email:    "user@test.com",
		Username: "user",
	}
}

func TestEnsureUser_FirstLogin_CreatesLocal(t *testing.T) {
	repo := newMockRepo()
	svc := NewService(repo)

	u, err := svc.EnsureUser(sampleIdentity())
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	if u.ID == 0 {
		t.Fatalf("expected ID populated by Create")
	}
	if u.KeycloakSub != "kc-sub-1" || u.Email != "user@test.com" || u.Username != "user" {
		t.Errorf("user fields wrong: %+v", u)
	}
	if repo.createCalls.Load() != 1 {
		t.Errorf("expected 1 Create, got %d", repo.createCalls.Load())
	}
	if repo.updateCalls.Load() != 0 {
		t.Errorf("expected 0 Update, got %d", repo.updateCalls.Load())
	}
}

func TestEnsureUser_SubsequentLogin_StableID_NoUpdate(t *testing.T) {
	repo := newMockRepo()
	svc := NewService(repo)

	first, _ := svc.EnsureUser(sampleIdentity())
	second, err := svc.EnsureUser(sampleIdentity())
	if err != nil {
		t.Fatalf("second EnsureUser: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("ID not stable across calls: %d vs %d", first.ID, second.ID)
	}
	if repo.createCalls.Load() != 1 {
		t.Errorf("Create called twice; expected once")
	}
	if repo.updateCalls.Load() != 0 {
		t.Errorf("Update called when no claim drift: %d times", repo.updateCalls.Load())
	}
}

func TestEnsureUser_EmailChanged_Updates(t *testing.T) {
	repo := newMockRepo()
	svc := NewService(repo)

	_, _ = svc.EnsureUser(sampleIdentity())

	updated := sampleIdentity()
	updated.Email = "new@test.com"

	u, err := svc.EnsureUser(updated)
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	if u.Email != "new@test.com" {
		t.Errorf("email not updated: %q", u.Email)
	}
	if repo.updateCalls.Load() != 1 {
		t.Errorf("expected 1 Update, got %d", repo.updateCalls.Load())
	}
}

func TestEnsureUser_UsernameChanged_Updates(t *testing.T) {
	repo := newMockRepo()
	svc := NewService(repo)

	_, _ = svc.EnsureUser(sampleIdentity())

	updated := sampleIdentity()
	updated.Username = "renamed"

	u, err := svc.EnsureUser(updated)
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	if u.Username != "renamed" {
		t.Errorf("username not updated: %q", u.Username)
	}
	if repo.updateCalls.Load() != 1 {
		t.Errorf("expected 1 Update, got %d", repo.updateCalls.Load())
	}
}

func TestEnsureUser_EmptyClaimsDoNotClearLocal(t *testing.T) {
	repo := newMockRepo()
	svc := NewService(repo)

	_, _ = svc.EnsureUser(sampleIdentity())

	// Token without email/username (e.g. minimal scope) must not erase
	// previously-stored values.
	stripped := &auth.Identity{Subject: "kc-sub-1"}
	u, err := svc.EnsureUser(stripped)
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	if u.Email != "user@test.com" {
		t.Errorf("email was wiped: %q", u.Email)
	}
	if u.Username != "user" {
		t.Errorf("username was wiped: %q", u.Username)
	}
	if repo.updateCalls.Load() != 0 {
		t.Errorf("expected no Update for empty claims, got %d", repo.updateCalls.Load())
	}
}

func TestEnsureUser_NilOrEmptySub_Rejected(t *testing.T) {
	repo := newMockRepo()
	svc := NewService(repo)

	if _, err := svc.EnsureUser(nil); !errors.Is(err, ErrInvalidIdentity) {
		t.Errorf("nil identity: got %v", err)
	}
	if _, err := svc.EnsureUser(&auth.Identity{}); !errors.Is(err, ErrInvalidIdentity) {
		t.Errorf("empty sub: got %v", err)
	}
	if repo.findBySubCalls.Load() != 0 {
		t.Errorf("repo touched on invalid identity")
	}
}

func TestEnsureUser_RaceCondition_NeverDuplicates(t *testing.T) {
	repo := newMockRepo()
	svc := NewService(repo)

	const goroutines = 50
	var wg sync.WaitGroup
	ids := make([]uint, goroutines)
	errs := make([]error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			u, err := svc.EnsureUser(sampleIdentity())
			if err != nil {
				errs[i] = err
				return
			}
			ids[i] = u.ID
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d errored: %v", i, err)
		}
	}
	// All goroutines must observe the same local id.
	for i := 1; i < goroutines; i++ {
		if ids[i] != ids[0] {
			t.Fatalf("ID drift under contention: ids[0]=%d ids[%d]=%d", ids[0], i, ids[i])
		}
	}
	// Exactly one row created across the whole burst.
	if len(repo.bySub) != 1 {
		t.Errorf("expected 1 row, got %d", len(repo.bySub))
	}
}

func TestEnsureUser_FindBySubFails(t *testing.T) {
	repo := newMockRepo()
	repo.findBySubErr = errors.New("db down")
	svc := NewService(repo)

	_, err := svc.EnsureUser(sampleIdentity())
	if err == nil || err.Error() != "db down" {
		t.Errorf("expected db down error, got %v", err)
	}
}

func TestEnsureUser_CreateFailsAndNoRowFound_PropagatesCreateErr(t *testing.T) {
	repo := newMockRepo()
	repo.createErr = errors.New("disk full")
	svc := NewService(repo)

	_, err := svc.EnsureUser(sampleIdentity())
	if err == nil || err.Error() != "disk full" {
		t.Errorf("expected create error to propagate, got %v", err)
	}
}
