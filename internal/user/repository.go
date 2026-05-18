// Package user holds the local user projection and its repository.
//
// The repository contract is intentionally narrow: lookup by subject
// (the canonical identity), lookup by primary key, create, update.
// FindByEmail is gone — emails are not stable identities in an OIDC world
// (users can change them in Keycloak).
package user

import (
	"errors"

	"gorm.io/gorm"
)

// UserRepository is the persistence contract consumed by Service.
// Implementations must:
//   - return (nil, nil) for "not found" rather than an error
//   - surface DB errors verbatim so callers can branch on them
type UserRepository interface {
	Create(user *User) error
	Update(user *User) error
	FindBySub(sub string) (*User, error)
	FindByID(id uint) (*User, error)
}

// Repository is the GORM-backed implementation of UserRepository.
type Repository struct {
	db *gorm.DB
}

// NewRepository constructs a Repository over the given GORM connection.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Create inserts a new row. On unique-constraint conflict (concurrent
// first-login race), gorm surfaces the underlying postgres error which
// the Service inspects to retry the lookup.
func (r *Repository) Create(user *User) error {
	return r.db.Create(user).Error
}

// Update persists changes to an existing row. Caller is expected to have
// loaded the row via FindBySub/FindByID first so optimistic concurrency is
// handled at the Go level for this MVP (no version column yet).
func (r *Repository) Update(user *User) error {
	return r.db.Save(user).Error
}

// FindBySub looks up the user row by their Keycloak subject UUID.
// Returns (nil, nil) when no row exists.
func (r *Repository) FindBySub(sub string) (*User, error) {
	var u User
	res := r.db.Where("keycloak_sub = ?", sub).First(&u)
	if errors.Is(res.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if res.Error != nil {
		return nil, res.Error
	}
	return &u, nil
}

// FindByID looks up the user row by primary key.
// Returns (nil, nil) when no row exists.
func (r *Repository) FindByID(id uint) (*User, error) {
	var u User
	res := r.db.First(&u, id)
	if errors.Is(res.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if res.Error != nil {
		return nil, res.Error
	}
	return &u, nil
}
