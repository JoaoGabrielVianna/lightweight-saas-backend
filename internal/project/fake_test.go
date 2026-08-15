package project

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
)

// fakeRepo is an in-memory Repository.
//
// It reproduces the two invariants the real schema enforces that the service
// depends on — case-insensitive name uniqueness per workspace, and the
// revoked-at guard on revocation — because a fake that is more permissive than
// the database turns a passing unit test into a false assurance. Everything
// else is a map.
type fakeRepo struct {
	mu sync.Mutex

	projects    map[string]*Project
	credentials map[string]*Credential

	// failures let a test drive the internal-error paths.
	failCreateProject error
	failFind          error
	failTouch         error

	touches int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		projects:    map[string]*Project{},
		credentials: map[string]*Credential{},
	}
}

// WithTx returns the receiver: a fake has no transaction to bind to. That is
// the honest shape, and it is why the rollback proof lives in the integration
// suite rather than here.
func (f *fakeRepo) WithTx(database.Tx) Repository { return f }

func (f *fakeRepo) CreateProject(_ context.Context, p *Project) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCreateProject != nil {
		return f.failCreateProject
	}
	for _, existing := range f.projects {
		if existing.WorkspaceID == p.WorkspaceID &&
			strings.EqualFold(existing.Name, p.Name) {
			return ErrNameTaken
		}
	}
	cp := *p
	f.projects[p.ID] = &cp
	return nil
}

func (f *fakeRepo) GetProject(_ context.Context, id string) (*Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[id]
	if !ok {
		return nil, nil
	}
	cp := *p
	return &cp, nil
}

func (f *fakeRepo) ListProjects(_ context.Context, workspaceID string) ([]Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Project{}
	for _, p := range f.projects {
		if p.WorkspaceID == workspaceID {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (f *fakeRepo) UpdateProjectName(_ context.Context, id, name string, now time.Time) (*Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[id]
	if !ok {
		return nil, nil
	}
	if p.Status == StatusArchived {
		return nil, ErrArchived
	}
	for _, other := range f.projects {
		if other.ID != id && other.WorkspaceID == p.WorkspaceID && strings.EqualFold(other.Name, name) {
			return nil, ErrNameTaken
		}
	}
	p.Name = name
	p.UpdatedAt = now
	cp := *p
	return &cp, nil
}

func (f *fakeRepo) ArchiveProject(_ context.Context, id string, now time.Time) (*Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[id]
	if !ok {
		return nil, nil
	}
	if p.Status == StatusActive {
		p.Status = StatusArchived
		p.ArchivedAt = &now
		p.UpdatedAt = now
	}
	cp := *p
	return &cp, nil
}

func (f *fakeRepo) CreateCredential(_ context.Context, c *Credential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *c
	f.credentials[c.ID] = &cp
	return nil
}

func (f *fakeRepo) ListCredentials(_ context.Context, projectID string) ([]Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Credential{}
	for _, c := range f.credentials {
		if c.ProjectID == projectID {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (f *fakeRepo) GetCredential(_ context.Context, id string) (*Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.credentials[id]
	if !ok {
		return nil, nil
	}
	cp := *c
	return &cp, nil
}

func (f *fakeRepo) CountActiveCredentials(_ context.Context, projectID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.credentials {
		if c.ProjectID == projectID && c.RevokedAt == nil {
			n++
		}
	}
	return n, nil
}

func (f *fakeRepo) CountActiveCredentialsByProject(_ context.Context, ids []string) (map[string]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	out := map[string]int{}
	for _, c := range f.credentials {
		if want[c.ProjectID] && c.RevokedAt == nil {
			out[c.ProjectID]++
		}
	}
	return out, nil
}

func (f *fakeRepo) RevokeCredential(_ context.Context, id, revokedBy string, now time.Time) (*Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.credentials[id]
	if !ok {
		return nil, nil
	}
	if c.RevokedAt != nil {
		return nil, ErrCredentialAlreadyRevoked
	}
	c.RevokedAt = &now
	c.RevokedBy = &revokedBy
	cp := *c
	return &cp, nil
}

func (f *fakeRepo) FindByKeyPrefix(_ context.Context, keyPrefix string) (*Credential, *Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failFind != nil {
		return nil, nil, f.failFind
	}
	for _, c := range f.credentials {
		if c.KeyPrefix == keyPrefix {
			p, ok := f.projects[c.ProjectID]
			if !ok {
				return nil, nil, nil
			}
			cc, pp := *c, *p
			return &cc, &pp, nil
		}
	}
	return nil, nil, nil
}

func (f *fakeRepo) TouchLastUsed(_ context.Context, id string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touches++
	if f.failTouch != nil {
		return f.failTouch
	}
	if c, ok := f.credentials[id]; ok {
		c.LastUsedAt = &now
	}
	return nil
}

// fakeWorkspaces is an in-memory WorkspaceStore.
type fakeWorkspaces struct {
	items map[string]*workspace.Workspace
	err   error
}

func newFakeWorkspaces() *fakeWorkspaces {
	return &fakeWorkspaces{items: map[string]*workspace.Workspace{}}
}

func (f *fakeWorkspaces) GetByID(_ context.Context, id string) (*workspace.Workspace, error) {
	if f.err != nil {
		return nil, f.err
	}
	ws, ok := f.items[id]
	if !ok {
		return nil, nil
	}
	cp := *ws
	return &cp, nil
}

func (f *fakeWorkspaces) add(id string, status workspace.Status) *workspace.Workspace {
	ws := &workspace.Workspace{ID: id, Slug: "ws-" + id[:4], Name: "Workspace", Status: status}
	f.items[id] = ws
	return ws
}

// errBoom is an infrastructure failure that must never reach a client verbatim.
var errBoom = errors.New("boom: driver exploded with a constraint name in it")
