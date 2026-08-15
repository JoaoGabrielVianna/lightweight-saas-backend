package project

import (
	"context"
	"errors"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"strings"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
)

func newService(t *testing.T) (*Service, *fakeRepo, *fakeWorkspaces) {
	t.Helper()
	repo := newFakeRepo()
	workspaces := newFakeWorkspaces()
	workspaces.add(testWorkspaceUUID, workspace.StatusActive)

	s := NewService(repo, workspaces, &fakeRunner{}, &fakeAuditWriter{})
	if s == nil {
		t.Fatal("NewService returned nil with every collaborator present")
	}
	return s, repo, workspaces
}

func wsID() string { return publicid.Format(publicid.WorkspacePrefix, testWorkspaceUUID) }

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var domainErr *Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error %v is not a catalogued *Error; it would reach a client as internal_error", err)
	}
	return domainErr.Code
}

// ─── Projects ───────────────────────────────────────────────────────────────

func TestService_CreateProject(t *testing.T) {
	s, _, _ := newService(t)

	p, err := s.Create(context.Background(), wsID(), "  Billing   worker ", testEvent(audit.ActionProjectCreated))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Whitespace is normalized so "Billing  worker" and "Billing worker"
	// cannot both exist and be indistinguishable in a list.
	if p.Name != "Billing worker" {
		t.Errorf("name = %q, want normalized %q", p.Name, "Billing worker")
	}
	if p.Status != StatusActive {
		t.Errorf("status = %q, want active", p.Status)
	}
	if !strings.HasPrefix(p.PublicID(), "prj_") {
		t.Errorf("public id = %q, want a prj_ prefix", p.PublicID())
	}
	if p.WorkspaceID != testWorkspaceUUID {
		t.Error("project was not bound to the workspace in the path")
	}
}

func TestService_CreateProject_RejectsBadNames(t *testing.T) {
	s, _, _ := newService(t)

	for name, input := range map[string]string{
		"empty":      "",
		"whitespace": "   ",
		"too long":   strings.Repeat("a", MaxNameLength+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Create(context.Background(), wsID(), input, testEvent(audit.ActionProjectCreated)); codeOf(t, err) != ErrNameRequired.Code {
				t.Errorf("code = %q, want %q", codeOf(t, err), ErrNameRequired.Code)
			}
		})
	}
}

func TestService_CreateProject_NameIsUniquePerWorkspaceCaseInsensitively(t *testing.T) {
	s, _, _ := newService(t)
	if _, err := s.Create(context.Background(), wsID(), "Billing", testEvent(audit.ActionProjectCreated)); err != nil {
		t.Fatalf("first: %v", err)
	}

	_, err := s.Create(context.Background(), wsID(), "BILLING", testEvent(audit.ActionProjectCreated))
	if got := codeOf(t, err); got != ErrNameTaken.Code {
		t.Errorf("code = %q, want %q — names must not differ only by case", got, ErrNameTaken.Code)
	}
}

func TestService_CreateProject_RefusedInsideAnArchivedWorkspace(t *testing.T) {
	// Minting credentials for a frozen workspace would produce keys whose every
	// identity call is already refused.
	s, _, ws := newService(t)
	ws.items[testWorkspaceUUID].Status = workspace.StatusArchived

	_, err := s.Create(context.Background(), wsID(), "Billing", testEvent(audit.ActionProjectCreated))
	if got := codeOf(t, err); got != ErrWorkspaceArchived.Code {
		t.Errorf("code = %q, want %q", got, ErrWorkspaceArchived.Code)
	}
}

func TestService_ProjectFromAnotherWorkspaceReadsAsAbsent(t *testing.T) {
	// The ownership check is what makes project ids workspace-scoped in
	// practice. Without it, an operator of one workspace could confirm the
	// existence of another's projects by id.
	s, repo, ws := newService(t)
	const otherWorkspace = "11111111-2222-4333-8444-555555555555"
	ws.add(otherWorkspace, workspace.StatusActive)

	repo.projects[testProjectUUID] = &Project{
		ID: testProjectUUID, WorkspaceID: otherWorkspace, Name: "Elsewhere", Status: StatusActive,
	}

	_, err := s.Get(context.Background(), wsID(), publicid.Format(publicid.ProjectPrefix, testProjectUUID))
	if got := codeOf(t, err); got != ErrNotFound.Code {
		t.Errorf("code = %q, want %q (identical to an id that never existed)", got, ErrNotFound.Code)
	}
}

func TestService_ArchiveIsIdempotent(t *testing.T) {
	s, _, _ := newService(t)
	p, _ := s.Create(context.Background(), wsID(), "Billing", testEvent(audit.ActionProjectCreated))

	first, err := s.Archive(context.Background(), wsID(), p.PublicID(), testEvent(audit.ActionProjectArchived))
	if err != nil {
		t.Fatalf("first archive: %v", err)
	}
	if first.Status != StatusArchived || first.ArchivedAt == nil {
		t.Fatal("archive did not set both status and archived_at")
	}

	// A retried request must succeed, not conflict.
	if _, err := s.Archive(context.Background(), wsID(), p.PublicID(), testEvent(audit.ActionProjectArchived)); err != nil {
		t.Errorf("second archive: %v — archiving must be idempotent so a retry is safe", err)
	}
}

func TestService_RenameRefusedOnArchivedProject(t *testing.T) {
	s, _, _ := newService(t)
	p, _ := s.Create(context.Background(), wsID(), "Billing", testEvent(audit.ActionProjectCreated))
	if _, err := s.Archive(context.Background(), wsID(), p.PublicID(), testEvent(audit.ActionProjectArchived)); err != nil {
		t.Fatalf("archive: %v", err)
	}

	_, err := s.Rename(context.Background(), wsID(), p.PublicID(), "Billing EU", testEvent(audit.ActionProjectRenamed))
	if got := codeOf(t, err); got != ErrArchived.Code {
		t.Errorf("code = %q, want %q", got, ErrArchived.Code)
	}
}

// ─── Credentials ────────────────────────────────────────────────────────────

func TestService_CreateCredential_ReturnsTheSecretOnceAndStoresOnlyADigest(t *testing.T) {
	s, repo, _ := newService(t)
	p, _ := s.Create(context.Background(), wsID(), "Billing", testEvent(audit.ActionProjectCreated))

	cred, secret, err := s.CreateCredential(context.Background(), wsID(), p.PublicID(), CreateCredentialInput{
		Label:     "staging",
		Scopes:    []string{"users:read"},
		CreatedBy: "operator-sub",
	}, testEvent(audit.ActionCredentialCreated))
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}

	if !strings.HasPrefix(secret, "lw_sk_") {
		t.Fatalf("returned secret %q is not a credential token", redact(secret))
	}

	// The stored row must carry no part of the plaintext beyond the lookup
	// segment, and no field anywhere may equal the token.
	stored := repo.credentials[cred.ID]
	if strings.Contains(secret, string(stored.KeyHash)) {
		t.Error("the stored hash is a substring of the token")
	}
	parsed, ok := parseToken(secret)
	if !ok {
		t.Fatal("returned token does not parse")
	}
	if stored.KeyPrefix != parsed.lookup {
		t.Error("stored prefix is not the token's lookup segment")
	}
	if strings.Contains(stored.Label, parsed.secret) || stored.KeyPrefix == parsed.secret {
		t.Error("the secret segment leaked into a stored field")
	}
	// And the domain type has nowhere to put it, which is the structural half.
	if strings.Contains(cred.Label+cred.KeyPrefix+cred.KeyHashAlg, parsed.secret) {
		t.Error("the secret segment is present on the returned Credential")
	}
}

func TestService_CreateCredential_ScopesAreExplicitAndValidated(t *testing.T) {
	s, _, _ := newService(t)
	p, _ := s.Create(context.Background(), wsID(), "Billing", testEvent(audit.ActionProjectCreated))

	cases := map[string][]string{
		"empty list":    {},
		"nil list":      nil,
		"unknown scope": {"users:read", "billing:refund"},
		"blank scope":   {"users:read", "  "},
	}
	for name, scopes := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := s.CreateCredential(context.Background(), wsID(), p.PublicID(), CreateCredentialInput{
				Label: "x", Scopes: scopes,
			}, testEvent(audit.ActionCredentialCreated))
			if got := codeOf(t, err); got != ErrInvalidScope.Code {
				t.Errorf("code = %q, want %q", got, ErrInvalidScope.Code)
			}
		})
	}
}

func TestService_CreateCredential_NormalizesAndDeduplicatesScopes(t *testing.T) {
	s, _, _ := newService(t)
	p, _ := s.Create(context.Background(), wsID(), "Billing", testEvent(audit.ActionProjectCreated))

	cred, _, err := s.CreateCredential(context.Background(), wsID(), p.PublicID(), CreateCredentialInput{
		Label:  "x",
		Scopes: []string{" USERS:READ ", "users:read", "users:write"},
	}, testEvent(audit.ActionCredentialCreated))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(cred.Scopes) != 2 {
		t.Fatalf("scopes = %v, want two after normalization and dedup", cred.Scopes)
	}
}

func TestService_CreateCredential_RequiresALabel(t *testing.T) {
	// An unlabelled key is one nobody dares revoke.
	s, _, _ := newService(t)
	p, _ := s.Create(context.Background(), wsID(), "Billing", testEvent(audit.ActionProjectCreated))

	_, _, err := s.CreateCredential(context.Background(), wsID(), p.PublicID(), CreateCredentialInput{
		Label: "   ", Scopes: []string{"users:read"},
	}, testEvent(audit.ActionCredentialCreated))
	if got := codeOf(t, err); got != ErrLabelRequired.Code {
		t.Errorf("code = %q, want %q", got, ErrLabelRequired.Code)
	}
}

func TestService_CreateCredential_RejectsPastExpiry(t *testing.T) {
	s, _, _ := newService(t)
	p, _ := s.Create(context.Background(), wsID(), "Billing", testEvent(audit.ActionProjectCreated))
	past := time.Now().Add(-time.Minute)

	_, _, err := s.CreateCredential(context.Background(), wsID(), p.PublicID(), CreateCredentialInput{
		Label: "x", Scopes: []string{"users:read"}, ExpiresAt: &past,
	}, testEvent(audit.ActionCredentialCreated))
	if got := codeOf(t, err); got != ErrInvalidExpiry.Code {
		t.Errorf("code = %q, want %q", got, ErrInvalidExpiry.Code)
	}
}

func TestService_CreateCredential_EnforcesTheActiveCap(t *testing.T) {
	s, _, _ := newService(t)
	p, _ := s.Create(context.Background(), wsID(), "Billing", testEvent(audit.ActionProjectCreated))

	for i := 0; i < MaxActiveCredentials; i++ {
		if _, _, err := s.CreateCredential(context.Background(), wsID(), p.PublicID(), CreateCredentialInput{
			Label: "k", Scopes: []string{"users:read"},
		}, testEvent(audit.ActionCredentialCreated)); err != nil {
			t.Fatalf("credential %d: %v", i, err)
		}
	}

	_, _, err := s.CreateCredential(context.Background(), wsID(), p.PublicID(), CreateCredentialInput{
		Label: "one too many", Scopes: []string{"users:read"},
	}, testEvent(audit.ActionCredentialCreated))
	if got := codeOf(t, err); got != ErrCredentialLimitReached.Code {
		t.Errorf("code = %q, want %q", got, ErrCredentialLimitReached.Code)
	}
}

func TestService_CreateCredential_RevokingFreesCapacity(t *testing.T) {
	// The cap counts LIVE credentials, so rotation (create, deploy, revoke) is
	// never blocked by history.
	s, _, _ := newService(t)
	p, _ := s.Create(context.Background(), wsID(), "Billing", testEvent(audit.ActionProjectCreated))

	var first *Credential
	for i := 0; i < MaxActiveCredentials; i++ {
		c, _, err := s.CreateCredential(context.Background(), wsID(), p.PublicID(), CreateCredentialInput{
			Label: "k", Scopes: []string{"users:read"},
		}, testEvent(audit.ActionCredentialCreated))
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		if i == 0 {
			first = c
		}
	}

	if _, err := s.RevokeCredential(context.Background(), wsID(), p.PublicID(), first.PublicID(), "op", testEvent(audit.ActionCredentialRevoked)); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, _, err := s.CreateCredential(context.Background(), wsID(), p.PublicID(), CreateCredentialInput{
		Label: "replacement", Scopes: []string{"users:read"},
	}, testEvent(audit.ActionCredentialCreated)); err != nil {
		t.Errorf("creating after a revoke: %v — revoked keys must not consume the cap", err)
	}
}

func TestService_CreateCredential_RefusedOnArchivedProject(t *testing.T) {
	s, _, _ := newService(t)
	p, _ := s.Create(context.Background(), wsID(), "Billing", testEvent(audit.ActionProjectCreated))
	if _, err := s.Archive(context.Background(), wsID(), p.PublicID(), testEvent(audit.ActionProjectArchived)); err != nil {
		t.Fatalf("archive: %v", err)
	}

	_, _, err := s.CreateCredential(context.Background(), wsID(), p.PublicID(), CreateCredentialInput{
		Label: "x", Scopes: []string{"users:read"},
	}, testEvent(audit.ActionCredentialCreated))
	if got := codeOf(t, err); got != ErrArchived.Code {
		t.Errorf("code = %q, want %q", got, ErrArchived.Code)
	}
}

func TestService_RevokeCredential(t *testing.T) {
	s, _, _ := newService(t)
	p, _ := s.Create(context.Background(), wsID(), "Billing", testEvent(audit.ActionProjectCreated))
	cred, _, _ := s.CreateCredential(context.Background(), wsID(), p.PublicID(), CreateCredentialInput{
		Label: "x", Scopes: []string{"users:read"},
	}, testEvent(audit.ActionCredentialCreated))

	revoked, err := s.RevokeCredential(context.Background(), wsID(), p.PublicID(), cred.PublicID(), "operator-sub", testEvent(audit.ActionCredentialRevoked))
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if revoked.RevokedAt == nil || revoked.RevokedBy == nil || *revoked.RevokedBy != "operator-sub" {
		t.Error("revocation did not record when and by whom")
	}

	// Twice is a conflict, not a silent success: an operator pressing revoke on
	// a key someone else already revoked should learn that.
	_, err = s.RevokeCredential(context.Background(), wsID(), p.PublicID(), cred.PublicID(), "operator-sub", testEvent(audit.ActionCredentialRevoked))
	if got := codeOf(t, err); got != ErrCredentialAlreadyRevoked.Code {
		t.Errorf("code = %q, want %q", got, ErrCredentialAlreadyRevoked.Code)
	}
}

func TestService_RevokeCredential_FromAnotherProjectReadsAsAbsent(t *testing.T) {
	s, repo, _ := newService(t)
	mine, _ := s.Create(context.Background(), wsID(), "Mine", testEvent(audit.ActionProjectCreated))
	theirs, _ := s.Create(context.Background(), wsID(), "Theirs", testEvent(audit.ActionProjectCreated))

	cred, _, _ := s.CreateCredential(context.Background(), wsID(), theirs.PublicID(), CreateCredentialInput{
		Label: "x", Scopes: []string{"users:read"},
	}, testEvent(audit.ActionCredentialCreated))

	_, err := s.RevokeCredential(context.Background(), wsID(), mine.PublicID(), cred.PublicID(), "op", testEvent(audit.ActionCredentialRevoked))
	if got := codeOf(t, err); got != ErrCredentialNotFound.Code {
		t.Errorf("code = %q, want %q", got, ErrCredentialNotFound.Code)
	}
	// And it must still be live: a cross-project revoke must not take effect.
	if repo.credentials[cred.ID].RevokedAt != nil {
		t.Fatal("a credential was revoked through another project's path")
	}
}

func TestService_InfrastructureErrorsNeverReachTheClient(t *testing.T) {
	// The driver error carries a constraint name. A client must see only
	// internal_error; the real cause goes to the log keyed by request id.
	s, repo, _ := newService(t)
	repo.failCreateProject = errBoom

	_, err := s.Create(context.Background(), wsID(), "Billing", testEvent(audit.ActionProjectCreated))
	if got := codeOf(t, err); got != ErrInternal.Code {
		t.Fatalf("code = %q, want %q", got, ErrInternal.Code)
	}
	var domainErr *Error
	_ = errors.As(err, &domainErr)
	if strings.Contains(domainErr.Message, "constraint") || strings.Contains(domainErr.Message, "boom") {
		t.Errorf("message %q leaks the driver error", domainErr.Message)
	}
}

func TestService_ListReportsActiveCredentialCounts(t *testing.T) {
	s, _, _ := newService(t)
	p, _ := s.Create(context.Background(), wsID(), "Billing", testEvent(audit.ActionProjectCreated))
	c1, _, _ := s.CreateCredential(context.Background(), wsID(), p.PublicID(), CreateCredentialInput{
		Label: "a", Scopes: []string{"users:read"},
	}, testEvent(audit.ActionCredentialCreated))
	if _, _, err := s.CreateCredential(context.Background(), wsID(), p.PublicID(), CreateCredentialInput{
		Label: "b", Scopes: []string{"users:read"},
	}, testEvent(audit.ActionCredentialCreated)); err != nil {
		t.Fatalf("second credential: %v", err)
	}
	if _, err := s.RevokeCredential(context.Background(), wsID(), p.PublicID(), c1.PublicID(), "op", testEvent(audit.ActionCredentialRevoked)); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	_, counts, err := s.List(context.Background(), wsID())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if counts[p.ID] != 1 {
		t.Errorf("active credential count = %d, want 1 (the revoked one must not count)", counts[p.ID])
	}
}

// TestNewService_NilCollaboratorsAreNil.
//
// The transactional collaborators are in this list deliberately. A Service that
// fell back to a non-transactional path when they were absent would make
// atomicity conditional on wiring nobody checks, and the failure mode would be
// a production deployment that is silently non-atomic while every test passes.
// Missing means absent routes, which is loud.
func TestNewService_NilCollaboratorsAreNil(t *testing.T) {
	runner := &fakeRunner{}
	writer := &fakeAuditWriter{}

	if NewService(nil, newFakeWorkspaces(), runner, writer) != nil {
		t.Error("NewService with a nil repository must return nil")
	}
	if NewService(newFakeRepo(), nil, runner, writer) != nil {
		t.Error("NewService with a nil workspace store must return nil")
	}
	if NewService(newFakeRepo(), newFakeWorkspaces(), nil, writer) != nil {
		t.Error("NewService with no transaction runner must return nil — atomicity is not optional")
	}
	if NewService(newFakeRepo(), newFakeWorkspaces(), runner, nil) != nil {
		t.Error("NewService with no audit writer must return nil — the audit row is not optional")
	}
}
