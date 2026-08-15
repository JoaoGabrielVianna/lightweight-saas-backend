package project

import (
	"context"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
)

// The authenticator answers exactly one question: which project and credential
// is this, and may it authenticate at all? Everything below is about the two
// properties that question has to hold — it must never say yes to an unusable
// credential, and it must never let a caller tell WHY it said no.

const (
	testWorkspaceUUID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	testProjectUUID   = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	testCredUUID      = "9b2f4c1a-1111-4222-8333-444455556666"
)

// seedCredential wires a project and a usable credential, returning the token.
func seedCredential(t *testing.T, repo *fakeRepo, mutate func(*Project, *Credential)) string {
	t.Helper()

	minted, err := MintCredential()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	p := &Project{
		ID:          testProjectUUID,
		WorkspaceID: testWorkspaceUUID,
		Name:        "Billing worker",
		Status:      StatusActive,
	}
	c := &Credential{
		ID:         testCredUUID,
		ProjectID:  p.ID,
		Label:      "staging",
		KeyPrefix:  minted.KeyPrefix,
		KeyHash:    minted.KeyHash,
		KeyHashAlg: "sha256",
		Scopes:     []string{"users:read"},
	}
	if mutate != nil {
		mutate(p, c)
	}
	repo.projects[p.ID] = p
	repo.credentials[c.ID] = c
	return minted.Token
}

func TestAuthenticate_AcceptsAUsableCredential(t *testing.T) {
	repo := newFakeRepo()
	token := seedCredential(t, repo, nil)

	principal, err := NewAuthenticator(repo).AuthenticateCredential(context.Background(), token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if principal == nil {
		t.Fatal("a usable credential was rejected")
	}

	// The principal carries the PUBLIC ids and the workspace binding. The
	// binding is what the authorization layer compares against the path, so
	// getting its form wrong would silently break every workspace check.
	if want := publicid.Format(publicid.ProjectPrefix, testProjectUUID); principal.ProjectID != want {
		t.Errorf("project id = %q, want %q", principal.ProjectID, want)
	}
	if want := publicid.Format(publicid.CredentialPrefix, testCredUUID); principal.CredentialID != want {
		t.Errorf("credential id = %q, want %q", principal.CredentialID, want)
	}
	if want := publicid.Format(publicid.WorkspacePrefix, testWorkspaceUUID); principal.WorkspaceID != want {
		t.Errorf("workspace binding = %q, want %q", principal.WorkspaceID, want)
	}
	if len(principal.Scopes) != 1 || principal.Scopes[0] != "users:read" {
		t.Errorf("scopes = %v, want [users:read]", principal.Scopes)
	}
}

// TestAuthenticate_EveryRejectionIsIndistinguishable is the enumeration
// property. Five different causes, one answer, and the answer carries no field
// that could be used to tell them apart.
func TestAuthenticate_EveryRejectionIsIndistinguishable(t *testing.T) {
	past := time.Now().Add(-time.Hour)

	cases := map[string]func(t *testing.T) (*fakeRepo, string){
		"unknown prefix": func(t *testing.T) (*fakeRepo, string) {
			repo := newFakeRepo()
			seedCredential(t, repo, nil)
			other, _ := MintCredential()
			return repo, other.Token // well-formed, nothing stored under it
		},
		"wrong secret": func(t *testing.T) (*fakeRepo, string) {
			repo := newFakeRepo()
			token := seedCredential(t, repo, nil)
			impostor, _ := MintCredential()
			// Same lookup, different secret: the row is found, the hash is not.
			parsed, _ := parseToken(impostor.Token)
			stored, _ := parseToken(token)
			return repo, "lw_sk_" + stored.lookup + "_" + parsed.secret
		},
		"revoked": func(t *testing.T) (*fakeRepo, string) {
			repo := newFakeRepo()
			return repo, seedCredential(t, repo, func(_ *Project, c *Credential) {
				c.RevokedAt = &past
				by := "operator-1"
				c.RevokedBy = &by
			})
		},
		"expired": func(t *testing.T) (*fakeRepo, string) {
			repo := newFakeRepo()
			return repo, seedCredential(t, repo, func(_ *Project, c *Credential) {
				c.ExpiresAt = &past
			})
		},
		"archived project": func(t *testing.T) (*fakeRepo, string) {
			repo := newFakeRepo()
			return repo, seedCredential(t, repo, func(p *Project, _ *Credential) {
				p.Status = StatusArchived
				p.ArchivedAt = &past
			})
		},
	}

	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			repo, token := build(t)

			principal, err := NewAuthenticator(repo).AuthenticateCredential(context.Background(), token)

			if err != nil {
				t.Fatalf("%s produced an ERROR (%v); an unusable credential must be a plain rejection, "+
					"because an error becomes a 503 and tells the caller the credential itself was fine", name, err)
			}
			if principal != nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}

// TestAuthenticate_InfrastructureFailureIsAnError separates "not valid" from
// "could not decide". Collapsing them would tell a correctly configured backend
// its credential is invalid during a database outage, sending an operator to
// rotate keys that were never the problem.
func TestAuthenticate_InfrastructureFailureIsAnError(t *testing.T) {
	repo := newFakeRepo()
	token := seedCredential(t, repo, nil)
	repo.failFind = errBoom

	principal, err := NewAuthenticator(repo).AuthenticateCredential(context.Background(), token)

	if err == nil {
		t.Fatal("a repository failure was reported as an invalid credential")
	}
	if principal != nil {
		t.Fatal("a principal was returned alongside an error")
	}
}

func TestAuthenticate_MalformedTokenNeverReachesTheRepository(t *testing.T) {
	// Both a cost property and a denial-of-service one: arbitrary garbage must
	// not be convertible into database load.
	repo := newFakeRepo()
	seedCredential(t, repo, nil)
	repo.failFind = errBoom // would surface as an error if it were consulted

	for _, token := range []string{"", "lw_sk_", "lw_sk_short_short", "eyJhbGciOiJSUzI1NiJ9.x.y"} {
		principal, err := NewAuthenticator(repo).AuthenticateCredential(context.Background(), token)
		if err != nil {
			t.Fatalf("malformed token %q reached the repository", token)
		}
		if principal != nil {
			t.Fatalf("malformed token %q was accepted", token)
		}
	}
}

func TestAuthenticate_TouchesLastUsedAtMostOncePerWindow(t *testing.T) {
	repo := newFakeRepo()
	token := seedCredential(t, repo, nil)
	a := NewAuthenticator(repo)

	if _, err := a.AuthenticateCredential(context.Background(), token); err != nil {
		t.Fatalf("first: %v", err)
	}
	if repo.touches != 1 {
		t.Fatalf("touches after first request = %d, want 1", repo.touches)
	}

	// A second request inside the throttle window must not write again —
	// otherwise every authenticated READ becomes a write.
	for i := 0; i < 5; i++ {
		if _, err := a.AuthenticateCredential(context.Background(), token); err != nil {
			t.Fatalf("repeat: %v", err)
		}
	}
	if repo.touches != 1 {
		t.Errorf("touches = %d, want 1: last_used_at must be throttled", repo.touches)
	}
}

func TestAuthenticate_LastUsedFailureDoesNotBreakAuthentication(t *testing.T) {
	// Operational metadata is not part of the decision. A credential that has
	// been proven valid must not be refused because a bookkeeping write failed.
	repo := newFakeRepo()
	token := seedCredential(t, repo, nil)
	repo.failTouch = errBoom

	principal, err := NewAuthenticator(repo).AuthenticateCredential(context.Background(), token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if principal == nil {
		t.Fatal("authentication failed because last_used_at could not be written")
	}
}

func TestAuthenticate_ExpiryBoundary(t *testing.T) {
	repo := newFakeRepo()
	future := time.Now().Add(time.Hour)
	token := seedCredential(t, repo, func(_ *Project, c *Credential) { c.ExpiresAt = &future })

	principal, err := NewAuthenticator(repo).AuthenticateCredential(context.Background(), token)
	if err != nil || principal == nil {
		t.Fatal("a credential expiring in the future must still authenticate")
	}
}

func TestNewAuthenticator_NilRepositoryIsNil(t *testing.T) {
	// The "this is not wired" signal every domain here uses: the composition
	// root omits the surface rather than mounting something that panics.
	if NewAuthenticator(nil) != nil {
		t.Error("NewAuthenticator(nil) must return nil")
	}
}

// Compile-time proof that the fake satisfies the same contract the Postgres
// repository does. Without it, the unit tests could drift into testing a
// narrower interface than production uses.
var _ Repository = (*fakeRepo)(nil)
var _ WorkspaceStore = (*fakeWorkspaces)(nil)
var _ workspace.Status = workspace.StatusActive
