// Package identity is the identity-management layer: a thin façade in front
// of an external identity provider (Keycloak today) that exposes user,
// role, session and credential operations as HTTP endpoints.
//
// Two providers in this codebase, sharing the same backing Keycloak realm
// but separate concerns:
//
//   - auth.AuthProvider           — runtime token validation (JWKS, iss, azp)
//   - identity.IdentityProvider   — admin operations (REST Admin API)
//
// Adding a method here is a breaking change for every implementation; keep
// the interface minimal. Phase 5.1 starts read-only (ListUsers, GetUser).
// Subsequent phases extend with mutations and other resources.
package identity

import (
	"context"
	"time"
)

// IdentityProvider abstracts the identity-management operations the service
// layer needs. Implementations live in subpackages
// (internal/identity/keycloak, future: auth0, supabase, ...).
//
// Implementations must be safe for concurrent use. Adding a method here is
// a breaking change for every implementation — keep the surface curated.
type IdentityProvider interface {
	// ─── Users ─────────────────────────────────────────────────────────
	ListUsers(ctx context.Context, q ListUsersQuery) ([]User, error)
	GetUser(ctx context.Context, id string) (*User, error)

	// ─── Roles ─────────────────────────────────────────────────────────
	// Roles are realm-level. Identifiers are role NAMES (Keycloak's
	// natural key); implementations are responsible for URL-encoding them.
	ListRoles(ctx context.Context) ([]Role, error)
	GetRole(ctx context.Context, name string) (*Role, error)

	// ListUsersByRole returns the users carrying a given realm role.
	// Pagination is left to the implementation; v0.2 returns the first
	// page Keycloak yields.
	ListUsersByRole(ctx context.Context, role string) ([]User, error)

	// ListUserRoles returns the realm roles assigned to a user.
	ListUserRoles(ctx context.Context, userID string) ([]Role, error)

	// ─── Sessions ─────────────────────────────────────────────────────
	// ListUserSessions returns all sessions for a user (across clients).
	ListUserSessions(ctx context.Context, userID string) ([]Session, error)
	// ListSessions returns active sessions across every client in the
	// realm. The implementation aggregates client-by-client; large realms
	// will pay a per-client RTT.
	ListSessions(ctx context.Context) ([]Session, error)

	// ─── Invitations ──────────────────────────────────────────────────
	// ListInvitations returns users that look invitation-like: required
	// actions pending, or carrying invitation user-attributes. Keycloak
	// has no first-class invitation resource, so "invitations" are
	// derived from user state.
	ListInvitations(ctx context.Context) ([]Invitation, error)

	// ─── Stage 5.2B — CREATE ──────────────────────────────────────────

	// CreateRole creates a new realm role. The provider returns the
	// freshly-created role as it sits in Keycloak (id assigned).
	// Returns ErrConflict when a role with the same name already exists.
	CreateRole(ctx context.Context, req CreateRoleRequest) (*Role, error)

	// CreateInvitation provisions a Keycloak user in invited-but-incomplete
	// state, assigns the requested realm roles, and dispatches the action
	// email. The flow is:
	//
	//   1. Verify every requested role exists (so we don't half-create).
	//   2. POST /users with enabled=true + requiredActions=[VERIFY_EMAIL,
	//      UPDATE_PASSWORD] + invited_by/expires_at user attributes.
	//   3. Assign realm roles via POST /users/:id/role-mappings/realm.
	//   4. PUT /users/:id/execute-actions-email to dispatch the email.
	//   5. GET /users/:id to capture the final representation.
	//
	// Reliability invariant: if steps 3 or 4 fail after step 2 succeeded,
	// the implementation MUST best-effort delete the half-provisioned user
	// so the caller can retry with the same email. Step 5 is informational
	// only — failures from there MUST NOT roll back the invitation (the
	// email is already on its way to the user's inbox). See
	// docs/INVITATION_RELIABILITY_v0.2.md for the rationale.
	CreateInvitation(ctx context.Context, req CreateInvitationRequest) (*Invitation, error)

	// ─── Stage 5.2C — UPDATE ──────────────────────────────────────────

	// UpdateUser patches a user. Fields left nil in the request are
	// preserved by GET→merge→PUT. Returns the updated user.
	UpdateUser(ctx context.Context, id string, req UpdateUserRequest) (*User, error)

	// UpdateRole patches a role's mutable fields (description only in v0.2).
	// Renaming is intentionally out of scope; new names must be created and
	// roles re-mapped.
	UpdateRole(ctx context.Context, name string, req UpdateRoleRequest) (*Role, error)

	// AssignRolesToUser grants the named realm roles to a user. Caller
	// supplies role NAMES; the provider resolves each to its kc id+name
	// payload Keycloak requires.
	AssignRolesToUser(ctx context.Context, userID string, roles []string) error

	// UnassignRolesFromUser removes the named realm roles from a user.
	// Same resolution contract as AssignRolesToUser.
	UnassignRolesFromUser(ctx context.Context, userID string, roles []string) error

	// SendResetPasswordEmail dispatches an UPDATE_PASSWORD action email to
	// the user via Keycloak's execute-actions-email endpoint. Requires SMTP
	// to be configured on the realm.
	SendResetPasswordEmail(ctx context.Context, userID string) error

	// SetUserPassword sets a password directly via the Keycloak Admin API.
	// When temporary is true the user must change it on next login.
	SetUserPassword(ctx context.Context, userID, password string, temporary bool) error

	// ResendInvitation re-dispatches the invitation action email for an
	// existing invited user.
	//
	// Reliability contract:
	//
	//   - Only the intersection of {VERIFY_EMAIL, UPDATE_PASSWORD} with the
	//     user's currently-pending required actions is re-dispatched.
	//     Re-adding a completed action would force the user to redo work
	//     they already finished (e.g. re-verify an already-verified email).
	//   - Resending an ACCEPTED invitation (no pending invite actions)
	//     returns ErrConflict — the invitation flow has nothing left to do.
	//   - Resending a REVOKED invitation (user disabled) returns
	//     ErrConflict — the admin must re-enable the user first.
	//
	// Implementations MUST GET the user before the PUT to enforce these
	// guards; the PUT alone has no preconditions in Keycloak.
	ResendInvitation(ctx context.Context, userID string) (*Invitation, error)

	// ─── Stage 5.2D — DELETE ──────────────────────────────────────────

	// DeleteUser removes a user from Keycloak. Cascades to role mappings
	// and sessions inside Keycloak.
	DeleteUser(ctx context.Context, userID string) error

	// DeleteRole removes a realm role. Keycloak rejects deletion of
	// built-in roles; we surface those as ErrBadRequest in the service tier.
	DeleteRole(ctx context.Context, name string) error

	// DeleteSession revokes a single session by id.
	DeleteSession(ctx context.Context, sessionID string) error

	// LogoutUserSessions revokes every session for a user (across clients).
	LogoutUserSessions(ctx context.Context, userID string) error
}

// User is the provider-agnostic user representation surfaced by the service
// + handler layers. Field set is deliberately conservative — only fields
// every supported provider can populate.
type User struct {
	// ID is the canonical identity id (Keycloak `sub`).
	ID string
	// Username matches the realm's `preferred_username` claim.
	Username string
	Email    string
	// FirstName + LastName are required by Keycloak's default user profile
	// schema for any user with the "user" role; surfaced here for parity.
	FirstName string
	LastName  string
	// Enabled is false for invited-but-not-yet-completed users and for
	// admin-disabled users.
	Enabled bool
	// EmailVerified gates whether the user can perform email-sensitive
	// actions in Keycloak.
	EmailVerified bool
	// CreatedAt is the user's createdTimestamp (Keycloak ms since epoch,
	// normalized to UTC).
	CreatedAt time.Time
	// Attributes carries Keycloak's user attribute map verbatim. Used today
	// only as a passthrough display field; multi-tenancy work in v0.3 will
	// promote specific keys (tenant_id, etc.) into first-class fields.
	Attributes map[string][]string
}

// ListUsersQuery is the input shape for IdentityProvider.ListUsers. The
// service layer is responsible for bounding First and Max — implementations
// MAY further clamp them but MUST NOT silently widen.
type ListUsersQuery struct {
	// Search performs a substring match against username, email, first
	// name, last name (Keycloak's stock semantics). Empty = match-all.
	Search string
	// First is the offset (zero-based). Negative values are caller errors;
	// the service rejects them before reaching the provider.
	First int
	// Max is the page size. The service bounds this to [1, 100]; zero
	// means "use service default" (20).
	Max int
}

// Role is the provider-agnostic realm role representation. Keycloak's
// notion of "permissions" doesn't exist as a first-class concept — roles
// compose other roles via the Composite flag, and clients consult realm
// roles directly to authorize. Future RBAC permissions land in v0.3 with a
// custom mapper.
type Role struct {
	ID          string // Keycloak role UUID (rarely used at API surface)
	Name        string // Realm role name — the canonical identifier
	Description string
	// Composite = true when this role transitively grants other roles.
	Composite bool
	// Builtin = true for Keycloak-managed roles like default-roles-*,
	// offline_access, uma_authorization. The service computes this from
	// the name; the provider sets the raw value verbatim.
	Builtin bool
}

// Session is the provider-agnostic representation of a Keycloak user session.
// Field set is the intersection of what every Keycloak version reliably
// returns from the various session endpoints.
type Session struct {
	ID         string // Session UUID (used by DELETE /sessions/:id)
	UserID     string // Keycloak sub of the principal
	Username   string
	IPAddress  string
	UserAgent  string // Keycloak doesn't always set this; "" when unknown
	StartedAt  time.Time
	LastAccess time.Time
	// Clients holds the clients this session has touched, mapping client
	// UUID → client id. A session can fan out across multiple clients
	// over its lifetime.
	Clients map[string]string
}

// CreateRoleRequest is the input shape for IdentityProvider.CreateRole.
// Caller-supplied fields after service-layer normalization (lowercased name,
// trimmed). Implementations forward verbatim to Keycloak; validation must
// happen at the service tier so every provider behaves identically.
type CreateRoleRequest struct {
	Name        string
	Description string
}

// UpdateUserRequest is the input shape for IdentityProvider.UpdateUser.
// Every field is optional — a nil pointer means "do not touch". The provider
// is expected to read-modify-write so omitted fields preserve their value.
type UpdateUserRequest struct {
	FirstName     *string
	LastName      *string
	Email         *string
	Enabled       *bool
	EmailVerified *bool
}

// UpdateRoleRequest is the input shape for IdentityProvider.UpdateRole.
// Description is the only mutable field in v0.2 — renaming would require
// rewriting every role-mapping that references the old name, which is out
// of scope for this iteration.
type UpdateRoleRequest struct {
	Description *string
}

// CreateInvitationRequest is the input shape for IdentityProvider.CreateInvitation.
// All fields are validated at the service tier before reaching the provider.
type CreateInvitationRequest struct {
	Email     string
	FirstName string
	LastName  string
	// Roles is the realm-role NAMES to assign to the new user. At least one
	// must be supplied; the service rejects empty slices. Every name must
	// resolve to an existing realm role (verified by the provider before
	// it creates the user, to avoid half-provisioned principals).
	Roles []string
	// ExpiresAt is the optional invitation expiry. The service rejects
	// values in the past; the provider stores it verbatim as the
	// `expires_at` user attribute.
	ExpiresAt string
	// InvitedBy is the identifier (email or username) of the admin who
	// initiated the invitation. Stored as the `invited_by` user attribute.
	// The handler defaults this to the authenticated identity when the
	// request body omits it.
	InvitedBy string
}

// Invitation status values. These are the canonical strings written to the
// JSON `status` field (consumed by the frontend pill). Provider
// implementations derive these from user state — keep them in sync.
//
// The set is intentionally small and totally ordered:
//
//	revoked  → terminal: user disabled (admin opted out, or self-revoke)
//	accepted → terminal: user enabled, no pending required actions
//	expired  → reversible: pending actions + expires_at in the past
//	pending  → reversible: pending actions + not yet expired
//
// `revoked` and `accepted` are terminal — ResendInvitation refuses to act
// on those (see [[invitation-reliability]] in docs/INVITATION_RELIABILITY_v0.2.md).
const (
	InvitationStatusPending  = "pending"
	InvitationStatusAccepted = "accepted"
	InvitationStatusExpired  = "expired"
	InvitationStatusRevoked  = "revoked"
)

// Invitation is a derived view over Keycloak users that look like pending
// invitations: users with requiredActions != [] (e.g. UPDATE_PASSWORD,
// VERIFY_EMAIL) or carrying invitation user-attributes.
//
// Invitations are NOT a first-class Keycloak resource — provider
// implementations synthesize them from User state. The ID is the Keycloak
// user id (so DELETE /admin/invitations/:id maps cleanly to DELETE
// /users/:id once Stage D ships).
type Invitation struct {
	ID              string
	Email           string
	Username        string
	RequiredActions []string
	// InvitedBy is the value of the `invited_by` user attribute when set
	// (populated by Stage B's POST /admin/users/invite). For pre-existing
	// users without the attribute, this stays empty.
	InvitedBy string
	// ExpiresAt is the value of the `expires_at` user attribute when set.
	// Stage B writes this; Stage A only reads it.
	ExpiresAt string
	// CreatedAt mirrors the Keycloak user createdTimestamp.
	CreatedAt time.Time
	// Status is derived: pending | accepted | expired | revoked.
	Status string
}
