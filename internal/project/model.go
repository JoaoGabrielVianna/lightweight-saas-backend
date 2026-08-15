// Package project owns Projects and their machine credentials: how a backend
// that is not a human operator authenticates to this API, and what it may do.
//
// A Project belongs to exactly ONE workspace, permanently. That binding is not
// a convenience — it is the authorization boundary. It is compared against the
// workspace in the request path before any workspace, connection, sealed
// credential or provider is touched, which is what makes the guarantee
// concrete:
//
//	one leaked credential  →  one project  →  one workspace  →  one realm
//
// A project that could span workspaces would turn that comparison into a
// lookup, and the guarantee into a query result.
//
// THE CREDENTIAL SECRET IS NEVER STORED. This package mints it, hashes it, and
// returns the plaintext exactly once. See token.go.
package project

import (
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
)

// Status is the Project lifecycle state. There are two, and archived is not
// terminal in the sense retired connections are — it is a freeze, and the row
// keeps its history.
type Status string

const (
	// StatusActive is a project whose credentials may authenticate.
	StatusActive Status = "active"

	// StatusArchived freezes a project. Every one of its credentials stops
	// authenticating immediately, because the authentication query reads the
	// project's status in the same row fetch as the credential. Nothing has to
	// walk the credentials and update them, which is what makes archiving an
	// atomic kill switch rather than a loop that can half-finish.
	StatusArchived Status = "archived"
)

// MaxActiveCredentials caps live credentials per project.
//
// Ten is not a resource limit — the rows are tiny. It is a blast-radius and
// hygiene limit: a project accumulating dozens of live keys has lost track of
// which deployments hold what, and the cap forces the revoke half of a rotation
// to actually happen. It is enforced at creation, so existing rows are never
// invalidated by it.
const MaxActiveCredentials = 10

// MaxNameLength bounds a project name. Generous for a human label, bounded so
// the column cannot be used as free storage.
const MaxNameLength = 120

// MaxLabelLength bounds a credential label.
const MaxLabelLength = 120

// Project is the domain model.
//
// It carries no persistence tags and no credential material. Credentials are a
// separate type loaded by a separate call, so no listing or response can carry
// one by accident.
type Project struct {
	ID          string
	WorkspaceID string
	Name        string
	Status      Status

	CreatedAt  time.Time
	UpdatedAt  time.Time
	ArchivedAt *time.Time
}

// PublicID renders the identifier clients see. The bare UUID is never exposed.
func (p *Project) PublicID() string {
	return publicid.Format(publicid.ProjectPrefix, p.ID)
}

// WorkspacePublicID renders the owning workspace's identifier.
func (p *Project) WorkspacePublicID() string {
	return publicid.Format(publicid.WorkspacePrefix, p.WorkspaceID)
}

// IsArchived reports whether the project is frozen.
func (p *Project) IsArchived() bool { return p.Status == StatusArchived }

// Credential is one machine credential belonging to a Project.
//
// KeyHash is present on the domain type because the authenticator compares
// against it; it is a digest of a value that was never stored, so it discloses
// nothing. The PLAINTEXT has no field here at all, which is what makes "the API
// cannot return the secret" a structural fact rather than a review convention —
// exactly the guarantee connection.Connection makes about provider secrets.
type Credential struct {
	ID        string
	ProjectID string
	Label     string

	KeyPrefix  string
	KeyHash    []byte
	KeyHashAlg string

	Scopes []string

	ExpiresAt *time.Time
	RevokedAt *time.Time

	CreatedBy string
	RevokedBy *string

	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// PublicID renders the credential identifier clients see.
func (c *Credential) PublicID() string {
	return publicid.Format(publicid.CredentialPrefix, c.ID)
}

// IsRevoked reports whether this credential was explicitly revoked.
func (c *Credential) IsRevoked() bool { return c.RevokedAt != nil }

// IsExpired reports whether the credential's optional expiry has passed.
func (c *Credential) IsExpired(now time.Time) bool {
	return c.ExpiresAt != nil && !now.Before(*c.ExpiresAt)
}

// IsUsable reports whether this credential may authenticate a request.
//
// It answers ONE boolean on purpose. The caller must not be able to report
// which condition failed, because revoked, expired and never-existed are
// indistinguishable in every public response — separating them would be a
// credential-enumeration oracle.
func (c *Credential) IsUsable(now time.Time) bool {
	return !c.IsRevoked() && !c.IsExpired(now)
}

// lastUsedThrottle is how stale last_used_at may get before it is refreshed.
//
// Without it, every authenticated READ would become a write, turning the most
// informative field in the model into its bottleneck. One minute is precise
// enough for the question the field answers ("is anything still using this key
// before I revoke it?") and coarse enough to make the write rare.
const lastUsedThrottle = time.Minute

// NeedsLastUsedTouch reports whether last_used_at is stale enough to refresh.
func (c *Credential) NeedsLastUsedTouch(now time.Time) bool {
	return c.LastUsedAt == nil || now.Sub(*c.LastUsedAt) >= lastUsedThrottle
}
