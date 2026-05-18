// Package user — service layer.
//
// The service owns one operation: EnsureUser, the idempotent JIT
// provisioning + reconciliation of the local user row from a Keycloak
// identity. It does NOT validate tokens (that's the auth provider) and
// it does NOT issue credentials (Keycloak owns identity).
package user

import (
	"errors"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
)

// ErrInvalidIdentity is returned when EnsureUser is called with an identity
// whose Subject is empty — a malformed token would never reach here, but
// the guard keeps the contract explicit for future callers.
var ErrInvalidIdentity = errors.New("invalid identity: empty subject")

// Service contains the user-domain operations.
type Service struct {
	repo UserRepository
}

// NewService constructs a Service over the given repository.
func NewService(repo UserRepository) *Service {
	return &Service{repo: repo}
}

// EnsureUser is the idempotent JIT provisioning entry point:
//
//   - first call for an identity → creates the local row from claims
//   - subsequent calls → returns the existing row; updates email/username
//     in place if those claims changed in Keycloak since last login
//   - never creates duplicates: KeycloakSub is unique-indexed at the DB
//     level. On a concurrent first-login race the Create fails with a
//     unique-constraint violation; we re-read by sub and return that.
//
// The returned *User has a stable local ID across calls for the same
// identity. Handlers can rely on user.ID for FKs, audits, etc.
func (s *Service) EnsureUser(id *auth.Identity) (*User, error) {
	if id == nil || id.Subject == "" {
		return nil, ErrInvalidIdentity
	}

	existing, err := s.repo.FindBySub(id.Subject)
	if err != nil {
		return nil, err
	}

	if existing == nil {
		user := &User{
			KeycloakSub: id.Subject,
			Email:       id.Email,
			Username:    id.Username,
		}
		if err := s.repo.Create(user); err != nil {
			// Concurrent first-login race: another request just created the
			// row. Re-read by sub. If we still can't find it, the original
			// Create error is the real failure — propagate it.
			if dup, lookupErr := s.repo.FindBySub(id.Subject); lookupErr == nil && dup != nil {
				return dup, nil
			}
			return nil, err
		}
		return user, nil
	}

	// Reconcile drift. Only mutate fields the provider actually populated
	// (empty string from claims is "unknown", not "cleared").
	dirty := false
	if id.Email != "" && existing.Email != id.Email {
		existing.Email = id.Email
		dirty = true
	}
	if id.Username != "" && existing.Username != id.Username {
		existing.Username = id.Username
		dirty = true
	}
	if dirty {
		if err := s.repo.Update(existing); err != nil {
			return nil, err
		}
	}
	return existing, nil
}
