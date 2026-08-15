// Package connection owns the Connection domain: a Workspace's configured
// access to an identity provider.
//
// Nothing in the running system consumes a Connection yet. The Identity API
// still talks to the process-level Keycloak configuration; making it resolve a
// Workspace's active Connection instead is a later slice. This package is the
// domain, its persistence, its verification probe, and its admin API — and
// nothing else.
//
// The sealed provider credential is deliberately absent from the Connection
// type. It lives only in the repository's row and is fetched by an explicit
// call, so no listing, response, or log line can carry it by accident. See
// repository.go.
package connection

import (
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
)

// Provider names the kind of identity provider a Connection points at.
//
// One value exists. The type and its CHECK constraint are here so adding a
// second is an explicit decision with a migration behind it — not a plugin
// framework, not a registry, and not an interface with one implementation.
type Provider string

// ProviderKeycloak is the only provider in this slice.
const ProviderKeycloak Provider = "keycloak"

// Status is the Connection lifecycle state.
type Status string

const (
	// StatusDraft is where every Connection starts: configured, not yet
	// verified or in use.
	StatusDraft Status = "draft"

	// StatusActive is the one Connection a Workspace routes through. At most
	// one per Workspace, enforced by a partial unique index.
	StatusActive Status = "active"

	// StatusRetired is terminal. A retired Connection keeps its history and
	// can be deleted, but never returns to service.
	StatusRetired Status = "retired"
)

// Health records the outcome of the last verification probe.
//
// There is no background job in this slice: health changes only when a verify
// runs, and `last_verified_at` says when that was. A caller deciding whether to
// trust it must look at both.
type Health string

const (
	// HealthUnknown means no verification has ever run.
	HealthUnknown Health = "unknown"
	// HealthHealthy means the last probe reached the provider, found the
	// realm, and authenticated the admin client.
	HealthHealthy Health = "healthy"
	// HealthUnhealthy means the last probe failed one of those.
	HealthUnhealthy Health = "unhealthy"
)

// AccessMode records what the admin client turned out to be allowed to do.
//
// It is a separate axis from Health on purpose. Authentication succeeding is
// what makes a Connection usable at all; what its service account may then do
// determines how much of the platform will work through it. A connection that
// authenticates but cannot list users is `healthy` + `limited`, not unhealthy
// — it is correctly configured and under-privileged, and those want different
// fixes.
//
// # The invariant (TD-024, resolved 2026-08-09)
//
// Before Slice 6 this had three values and `full` meant "both admin READS
// succeeded". A genuinely read-only service account — `view-users` granted,
// `manage-users` not — passed both read probes and was labelled `full`, so the
// API told clients writes were supported when it had only proven reads. Any UI
// enabling mutation controls on `full` was being lied to.
//
// The rule now is: **`full` is claimed only when write capability has been
// positively proven.** Everything the probe cannot prove degrades downward,
// never upward. See Verifier for how the proof is obtained without mutating
// the provider.
type AccessMode string

const (
	// AccessModeUnknown means the probe could not determine what the service
	// account may do.
	//
	// Two situations reach it, and they are deliberately not distinguished
	// here (the per-check report is what tells them apart):
	//
	//   1. no verification has ever run;
	//   2. verification ran, the admin reads succeeded, but the provider gave
	//      no trustworthy evidence either way about writes.
	//
	// Writes are ATTEMPTED on `unknown`. Refusing them would break every
	// installation whose provider does not expose its grants, for a signal
	// that was never promised — and the authoritative answer still arrives
	// from the provider as provider_forbidden.
	AccessModeUnknown AccessMode = "unknown"

	// AccessModeFull means the service account read the realm, listed users,
	// AND was proven to hold a grant that permits identity writes.
	//
	// This is the only value on which a client may enable mutation controls.
	AccessModeFull AccessMode = "full"

	// AccessModeReadOnly means the admin reads succeeded and the provider
	// positively reported that the service account holds NO write grant.
	//
	// This is the configuration the old three-value model could not see. It is
	// not a degraded `full`: reads through this connection work perfectly, and
	// the remedy (grant `manage-users`, re-verify) is specific.
	AccessModeReadOnly AccessMode = "read_only"

	// AccessModeLimited means it authenticated but at least one admin READ was
	// refused — almost always a missing realm-management role. Such a
	// connection may not be able to read either, so it is weaker than
	// read_only rather than a sibling of it.
	AccessModeLimited AccessMode = "limited"
)

// CanWrite reports whether identity writes should be attempted through a
// connection in this access mode.
//
// It lives on the type so the runtime guard, the verifier's summary, and any
// future consumer cannot drift apart on what "may write" means.
func (m AccessMode) CanWrite() bool {
	return m != AccessModeLimited && m != AccessModeReadOnly
}

// VerifyValidity is how long a successful verification authorizes an
// activation.
//
// One hour: long enough that an operator can verify, read the report, and
// decide without racing a clock; short enough that activating on a verdict
// from last week is impossible. The point is that "this provider answered
// correctly" is a perishable fact — credentials get rotated, realms get
// deleted — and activation is exactly the moment where acting on a stale one
// is most expensive.
const VerifyValidity = time.Hour

// Connection is the domain model.
//
// It carries no GORM tags, no persistence import, and — importantly — no
// secret material. The sealed client secret is loaded separately, only by the
// code that needs to use it.
type Connection struct {
	ID          string
	WorkspaceID string
	Name        string
	Provider    Provider
	Status      Status

	// Provider coordinates. BaseURL is the URL this API uses to REACH the
	// provider, which in docker is not the URL browsers use.
	BaseURL  string
	Realm    string
	ClientID string

	Health         Health
	HealthMessage  string
	AccessMode     AccessMode
	LastVerifiedAt *time.Time

	CreatedAt   time.Time
	UpdatedAt   time.Time
	ActivatedAt *time.Time
	RetiredAt   *time.Time
}

// PublicID renders the identifier clients see. The bare UUID is never exposed.
func (c *Connection) PublicID() string {
	return publicid.Format(publicid.ConnectionPrefix, c.ID)
}

// WorkspacePublicID renders the owning workspace's identifier.
func (c *Connection) WorkspacePublicID() string {
	return publicid.Format(publicid.WorkspacePrefix, c.WorkspaceID)
}

// IsVerified reports whether the last probe passed AND is still within
// VerifyValidity as of now.
//
// Both halves matter: `healthy` alone says a probe once passed, and a
// timestamp alone says a probe once ran. Only together do they mean "this
// provider answered correctly recently enough to act on".
func (c *Connection) IsVerified(now time.Time) bool {
	if c.Health != HealthHealthy || c.LastVerifiedAt == nil {
		return false
	}
	return now.Sub(*c.LastVerifiedAt) <= VerifyValidity
}

// CanActivate reports whether this Connection may be activated, returning the
// specific reason if not.
//
// It deliberately does NOT check whether the Workspace already has another
// active Connection — that is not a property of this row, cannot be decided
// without reading others, and is resolved by the partial unique index at write
// time. Checking it here would produce a rule that looks authoritative and
// silently fails to hold under concurrency.
func (c *Connection) CanActivate(now time.Time) error {
	switch c.Status {
	case StatusActive:
		return ErrAlreadyActive
	case StatusRetired:
		return ErrRetired
	}
	if c.Health != HealthHealthy {
		return ErrNotVerified
	}
	if !c.IsVerified(now) {
		return ErrVerificationExpired
	}
	return nil
}

// CanRetire reports whether this Connection may be retired.
//
// Both draft and active can be retired: retiring a draft is how an operator
// takes a half-configured connection out of the list without deleting the
// record of it having existed.
func (c *Connection) CanRetire() error {
	if c.Status == StatusRetired {
		return ErrRetired
	}
	return nil
}

// CanDelete reports whether this Connection may be deleted.
//
// Active connections cannot be deleted directly. Deleting the thing a Workspace
// is currently routing through, in one call, with no intermediate state, is the
// kind of operation that gets performed by accident at 3am. Retire first — that
// is an explicit, reversible-in-intent step that makes the consequence visible
// before the row disappears.
func (c *Connection) CanDelete() error {
	if c.Status == StatusActive {
		return ErrActiveCannotDelete
	}
	return nil
}

// CanUpdate reports whether this Connection's configuration may be edited.
//
// Draft only. Editing the base URL, realm, client id or secret of an active
// Connection would silently invalidate the verification that authorized its
// activation, leaving a row that claims to be healthy against coordinates
// nothing ever probed.
func (c *Connection) CanUpdate() error {
	switch c.Status {
	case StatusActive:
		return ErrNotDraft
	case StatusRetired:
		return ErrRetired
	}
	return nil
}
