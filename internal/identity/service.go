package identity

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Service is the business-logic seam between HTTP handlers and the
// IdentityProvider. Responsibilities today (Phase 5.1, read-only):
//
//   - bound caller-supplied page sizes
//   - reject obviously-malformed user ids before talking to the provider
//
// Future phases will add authorization (self-vs-other), local-mirror sync
// (Keycloak first, then DB), and conflict handling for mutations.
type Service struct {
	provider IdentityProvider
}

// NewService constructs a Service. Returns nil when provider is nil — the
// caller (server.SetupIdentity) interprets nil as "identity routes
// disabled" and doesn't mount them, so we never reach a service call with
// a nil provider.
func NewService(provider IdentityProvider) *Service {
	if provider == nil {
		return nil
	}
	return &Service{provider: provider}
}

// Defaults the handler can rely on.
const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// ClampListUsersQuery bounds caller-supplied pagination to what the API
// promises: First >= 0, Max in [1, 100] with 0 meaning "use the default".
//
// Exported because two surfaces need the SAME bounds for different reasons.
// Service.ListUsers applies it so Keycloak never sees an absurd `max` — the
// contract must not depend on which Keycloak version is behind it. The
// workspace-scoped API applies it a second time, before calling the service,
// so it can echo the values that were actually used.
//
// That echo is the whole reason this is a separate function rather than
// inlined. /admin/users reports the caller's RAW first/max back to them even
// though the service clamped them ([TD-020]) — a client paginating on the
// echoed `max` computes wrong offsets. /v1 does not reproduce that, and the
// only honest way to fix it in one surface without forking the rule is to make
// the rule callable.
//
// Idempotent: clamping an already-clamped query is a no-op, which is what
// makes it safe for /v1 to apply it and then hand the result to a service that
// will apply it again.
//
// [TD-020]: docs/TECH_DEBT.md#td-020
func ClampListUsersQuery(q ListUsersQuery) ListUsersQuery {
	if q.First < 0 {
		q.First = 0
	}
	if q.Max <= 0 {
		q.Max = defaultPageSize
	}
	if q.Max > maxPageSize {
		q.Max = maxPageSize
	}
	return q
}

// ListUsers proxies to the provider after bounding pagination params.
// Keycloak will silently accept absurd max values; we clamp instead so
// the API contract doesn't depend on Keycloak version.
func (s *Service) ListUsers(ctx context.Context, q ListUsersQuery) ([]User, error) {
	return s.provider.ListUsers(ctx, ClampListUsersQuery(q))
}

// uuidPattern matches Keycloak's standard sub format (RFC 4122 UUID with
// hyphens). Reject anything else before round-tripping to Keycloak — saves
// a network call + gives a clearer error message.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// GetUser proxies to the provider after validating the id format.
func (s *Service) GetUser(ctx context.Context, id string) (*User, error) {
	if !uuidPattern.MatchString(id) {
		return nil, ErrBadRequest
	}
	return s.provider.GetUser(ctx, id)
}

// roleNamePattern bounds what we'll forward to Keycloak. Keycloak itself is
// permissive (most printable ASCII works), but we cap to a sensible subset
// to avoid surprises and keep audit logs readable. Operators wanting wider
// charsets can relax this later.
//
// Permitted: letters, digits, hyphen, underscore, dot, colon, and a single
// space between tokens. 1–128 chars.
var roleNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_\-.:]{0,30}(?: [A-Za-z0-9_\-.:]{1,30})*$`)

// IsValidUserID and IsValidRoleName expose the id-shape rules the service
// already enforces, so a surface with machine-readable error codes can report
// `invalid_user_id` or `invalid_role_name` instead of a generic
// `invalid_request`.
//
// The alternative would be to parse the service's error messages, which breaks
// the first time someone rewords one. These are the same regexes the service
// itself uses — not a second copy — so a surface calling them cannot come to a
// different conclusion than the service will a moment later.
func IsValidUserID(id string) bool { return uuidPattern.MatchString(id) }

// IsValidRoleName reports whether a role name is well-formed for READING.
// Creation is stricter (see roleCreatePattern) because a new name is
// permanent, whereas reading a legacy quirky name must keep working.
func IsValidRoleName(name string) bool { return roleNamePattern.MatchString(name) }

// ListRoles returns every realm role.
func (s *Service) ListRoles(ctx context.Context) ([]Role, error) {
	return s.provider.ListRoles(ctx)
}

// GetRole fetches a role by name.
func (s *Service) GetRole(ctx context.Context, name string) (*Role, error) {
	if !roleNamePattern.MatchString(name) {
		return nil, ErrBadRequest
	}
	return s.provider.GetRole(ctx, name)
}

// ListUsersByRole returns the users carrying the named realm role.
func (s *Service) ListUsersByRole(ctx context.Context, name string) ([]User, error) {
	if !roleNamePattern.MatchString(name) {
		return nil, ErrBadRequest
	}
	return s.provider.ListUsersByRole(ctx, name)
}

// ListUserRoles returns the realm roles for a user.
func (s *Service) ListUserRoles(ctx context.Context, userID string) ([]Role, error) {
	if !uuidPattern.MatchString(userID) {
		return nil, ErrBadRequest
	}
	return s.provider.ListUserRoles(ctx, userID)
}

// ListUserSessions returns active sessions for a user.
func (s *Service) ListUserSessions(ctx context.Context, userID string) ([]Session, error) {
	if !uuidPattern.MatchString(userID) {
		return nil, ErrBadRequest
	}
	return s.provider.ListUserSessions(ctx, userID)
}

// ListSessions returns active sessions across the realm.
func (s *Service) ListSessions(ctx context.Context) ([]Session, error) {
	return s.provider.ListSessions(ctx)
}

// ListInvitations returns derived-invitation users.
func (s *Service) ListInvitations(ctx context.Context) ([]Invitation, error) {
	return s.provider.ListInvitations(ctx)
}

// ─── Stage 5.2B — CREATE ──────────────────────────────────────────────────

// reservedRoleNames are names the service refuses to create. Two reasons:
//
//   - admin, user: already provisioned in every realm by the bootstrap;
//     allowing creation by these names would silently no-op or create
//     subtle confusion ("which 'admin' role am I editing?").
//   - offline_access, uma_authorization, default-roles-*: managed by
//     Keycloak itself. POST against them succeeds but the resulting role
//     fights with realm internals.
//
// The check is performed AFTER name normalization (lowercase + trim) so a
// request with "Admin" or "ADMIN " still fails.
var reservedRoleNames = map[string]struct{}{
	"admin":             {},
	"user":              {},
	"offline_access":    {},
	"uma_authorization": {},
}

const maxRoleNameLen = 255

// emailPattern is intentionally permissive — exhaustive RFC 5322 in regex
// is a known anti-pattern. Real validation happens when Keycloak attempts
// to send mail; we reject only the obviously-broken cases here.
var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// CreateRole validates the request, normalizes the name, then provisions
// the role via the provider.
func (s *Service) CreateRole(ctx context.Context, req CreateRoleRequest) (*Role, error) {
	// Normalize first so all downstream checks see the canonical form.
	req.Name = strings.ToLower(strings.TrimSpace(req.Name))
	req.Description = strings.TrimSpace(req.Description)

	if req.Name == "" {
		return nil, invalidField("name", "is required")
	}
	if len(req.Name) > maxRoleNameLen {
		return nil, fmt.Errorf("%w: role name exceeds %d characters", ErrBadRequest, maxRoleNameLen)
	}
	if _, reserved := reservedRoleNames[req.Name]; reserved {
		return nil, fmt.Errorf("%w: %q is a reserved role name", ErrRoleReserved, req.Name)
	}
	if strings.HasPrefix(req.Name, "default-roles-") {
		return nil, fmt.Errorf("%w: names starting with default-roles- are reserved by Keycloak", ErrRoleReserved)
	}
	// We're stricter than the GET-side roleNamePattern for create because
	// new names are forever; reading legacy quirky names is fine, writing
	// them is not. Lowercase letters, digits, hyphen, underscore, dot,
	// colon. 1–maxRoleNameLen chars.
	if !roleCreatePattern.MatchString(req.Name) {
		return nil, invalidField("name", "must match [a-z0-9_.:-]+")
	}

	return s.provider.CreateRole(ctx, req)
}

var roleCreatePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_\-.:]{0,254}$`)

// CreateInvitation validates the request, defaults invited_by from the
// authenticated identity when missing, then provisions the invited user.
//
// defaultInvitedBy is the value the handler passes from the caller's
// identity (email or sub). It's only consulted when req.InvitedBy is empty
// — explicit values in the body win.
func (s *Service) CreateInvitation(ctx context.Context, req CreateInvitationRequest, defaultInvitedBy string) (*Invitation, error) {
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.FirstName = strings.TrimSpace(req.FirstName)
	req.LastName = strings.TrimSpace(req.LastName)
	req.InvitedBy = strings.TrimSpace(req.InvitedBy)
	req.ExpiresAt = strings.TrimSpace(req.ExpiresAt)

	if req.Email == "" {
		return nil, invalidField("email", "is required")
	}
	if !emailPattern.MatchString(req.Email) {
		return nil, fmt.Errorf("%w: email %q is malformed", ErrBadRequest, req.Email)
	}
	if len(req.Roles) == 0 {
		return nil, invalidField("roles", "must contain at least one role")
	}
	// Normalize role names so downstream lookup matches Keycloak storage.
	for i, r := range req.Roles {
		r = strings.ToLower(strings.TrimSpace(r))
		if r == "" {
			return nil, fmt.Errorf("%w: role at index %d is empty", ErrBadRequest, i)
		}
		req.Roles[i] = r
	}
	if req.ExpiresAt != "" {
		t, err := parseExpiresAt(req.ExpiresAt)
		if err != nil {
			return nil, fmt.Errorf("%w: expires_at must be RFC3339 (got %q)", ErrBadRequest, req.ExpiresAt)
		}
		if !t.After(time.Now()) {
			return nil, invalidField("expires_at", "must be in the future")
		}
		// Re-emit in canonical RFC3339 so storage is normalized.
		req.ExpiresAt = t.UTC().Format(time.RFC3339)
	}
	if req.InvitedBy == "" {
		req.InvitedBy = defaultInvitedBy
	}

	return s.provider.CreateInvitation(ctx, req)
}

// parseExpiresAt tries the RFC3339 variants we expect operators to write.
func parseExpiresAt(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable")
}

// ─── Stage 5.2C / 5.2D — UPDATE & DELETE ─────────────────────────────────

// adminRoleName is the realm role that gates admin endpoints. Hard-coded
// here because it's the same role auth.RequireRole consults at the
// middleware tier — drifting them apart silently breaks the guards below.
const adminRoleName = "admin"

// protectedRoleNames are role names the service refuses to delete. Two
// reasons: built-ins (Keycloak rejects deletion of these anyway, but we
// short-circuit so the API contract is consistent across providers), and
// app-managed roles whose deletion would leave the system unusable (admin,
// user). The check runs AFTER lowercase normalization.
//
// Membership reuses reservedRoleNames intentionally — a name we refuse to
// create is a name we should also refuse to delete.
//
// IsProtectedRoleName is the exported form. It exists so the workspace-scoped
// surface can refuse a MACHINE credential granting or revoking one of these
// names, without keeping a second list that would drift from this one. The
// predicate is the same; only who is subject to it differs, and that decision
// lives with the caller.
func IsProtectedRoleName(name string) bool {
	return isProtectedRoleName(strings.ToLower(strings.TrimSpace(name)))
}

func isProtectedRoleName(name string) bool {
	if _, ok := reservedRoleNames[name]; ok {
		return true
	}
	if strings.HasPrefix(name, "default-roles-") {
		return true
	}
	return false
}

// minPasswordLength is the floor for a directly-set password.
//
// Eight, matching what /admin/users/password has enforced since it shipped.
// This is a sanity floor, not a policy: Keycloak owns password policy for the
// realm and will reject anything its configured policy forbids. Duplicating a
// full policy here would produce two rules that disagree the moment an
// operator tightens the realm's.
const minPasswordLength = 8

// CreateUser provisions a user directly with a temporary password.
//
// The validation here is the same set the legacy /admin/users/password handler
// applied inline, moved to the tier that owns rules so both surfaces enforce
// it identically — and so it is testable without an HTTP request. That route
// had NO test coverage at all; this is the first.
//
// Roles are normalized (trimmed, lowercased) exactly as AssignRolesToUser
// normalizes them, because they end up in the same Keycloak role-mapping call.
func (s *Service) CreateUser(ctx context.Context, req CreateUserRequest) (*User, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		return nil, invalidField("email", "is required")
	}
	if !emailPattern.MatchString(email) {
		return nil, invalidField("email", "is malformed")
	}
	req.Email = email

	// NOT trimmed: leading or trailing whitespace can be part of a password,
	// and silently altering a credential produces a login failure that looks
	// like the user's fault.
	if req.TemporaryPassword == "" {
		return nil, invalidField("temporary_password", "is required")
	}
	if len(req.TemporaryPassword) < minPasswordLength {
		return nil, invalidField("temporary_password", fmt.Sprintf("must be at least %d characters", minPasswordLength))
	}

	req.FirstName = strings.TrimSpace(req.FirstName)
	req.LastName = strings.TrimSpace(req.LastName)

	// Roles are optional here, unlike on an invitation: a user created with a
	// password can be given roles afterwards, and refusing to create one
	// without them would block the ordinary "add the person, decide access
	// later" flow.
	if len(req.Roles) > 0 {
		normalized, err := normalizeRoleList(req.Roles)
		if err != nil {
			return nil, err
		}
		req.Roles = normalized
	}

	return s.provider.CreateUser(ctx, req)
}

// UpdateUser applies a partial update to a user. Guards:
//
//   - target id must be a UUID
//   - if disabling (Enabled=false) AND target==caller, reject — admins
//     don't lock themselves out
//   - if disabling AND target carries `admin` AND would be last enabled
//     admin, reject — prevents the realm from becoming admin-less
func (s *Service) UpdateUser(ctx context.Context, callerSubject, targetID string, req UpdateUserRequest) (*User, error) {
	if !uuidPattern.MatchString(targetID) {
		return nil, fmt.Errorf("%w: id must be a UUID", ErrBadRequest)
	}
	if req.Email != nil {
		e := strings.ToLower(strings.TrimSpace(*req.Email))
		if e == "" || !emailPattern.MatchString(e) {
			return nil, invalidField("email", "is malformed")
		}
		req.Email = &e
	}
	if req.FirstName != nil {
		v := strings.TrimSpace(*req.FirstName)
		req.FirstName = &v
	}
	if req.LastName != nil {
		v := strings.TrimSpace(*req.LastName)
		req.LastName = &v
	}

	if req.Enabled != nil && !*req.Enabled {
		if callerSubject != "" && callerSubject == targetID {
			return nil, fmt.Errorf("%w: cannot disable your own account", ErrForbidden)
		}
		if err := s.assertNotLastAdmin(ctx, targetID); err != nil {
			return nil, err
		}
	}
	return s.provider.UpdateUser(ctx, targetID, req)
}

// UpdateRole patches a role's mutable fields. Description is normalized to
// trimmed form. Built-in/protected roles cannot have their description
// edited via this path either — operators changing them confuses everyone.
func (s *Service) UpdateRole(ctx context.Context, name string, req UpdateRoleRequest) (*Role, error) {
	if !roleNamePattern.MatchString(name) {
		return nil, invalidField("name", "is malformed")
	}
	norm := strings.ToLower(strings.TrimSpace(name))
	if isProtectedRoleName(norm) {
		return nil, fmt.Errorf("%w: role %q is protected and cannot be modified", ErrRoleProtected, norm)
	}
	if req.Description != nil {
		d := strings.TrimSpace(*req.Description)
		req.Description = &d
	}
	return s.provider.UpdateRole(ctx, name, req)
}

// AssignRolesToUser grants realm roles to a user. The service normalizes
// role names so the provider doesn't have to. No "self-grant admin"
// restriction — escalating roles for a user OTHER than yourself is the
// whole point of admin. (Self-grant is symmetrical: requires admin to call
// the endpoint, so the caller already has it.)
func (s *Service) AssignRolesToUser(ctx context.Context, targetID string, roles []string) error {
	if !uuidPattern.MatchString(targetID) {
		return fmt.Errorf("%w: id must be a UUID", ErrBadRequest)
	}
	normalized, err := normalizeRoleList(roles)
	if err != nil {
		return err
	}
	return s.provider.AssignRolesToUser(ctx, targetID, normalized)
}

// UnassignRolesFromUser revokes roles. Guards specific to revocation:
//
//   - if `admin` is in the list AND target==caller, reject — admins
//     don't strip their own admin
//   - if `admin` is in the list AND target would become the last admin
//     in the realm, reject
func (s *Service) UnassignRolesFromUser(ctx context.Context, callerSubject, targetID string, roles []string) error {
	if !uuidPattern.MatchString(targetID) {
		return fmt.Errorf("%w: id must be a UUID", ErrBadRequest)
	}
	normalized, err := normalizeRoleList(roles)
	if err != nil {
		return err
	}
	for _, r := range normalized {
		if r == adminRoleName {
			if callerSubject != "" && callerSubject == targetID {
				return fmt.Errorf("%w: cannot remove your own admin role", ErrForbidden)
			}
			if err := s.assertNotLastAdmin(ctx, targetID); err != nil {
				return err
			}
			break
		}
	}
	return s.provider.UnassignRolesFromUser(ctx, targetID, normalized)
}

// normalizeRoleList lowercases + trims each entry and rejects empties.
func normalizeRoleList(roles []string) ([]string, error) {
	if len(roles) == 0 {
		return nil, invalidField("roles", "must contain at least one role")
	}
	out := make([]string, 0, len(roles))
	for i, r := range roles {
		v := strings.ToLower(strings.TrimSpace(r))
		if v == "" {
			return nil, fmt.Errorf("%w: role at index %d is empty", ErrBadRequest, i)
		}
		out = append(out, v)
	}
	return out, nil
}

// SendResetPasswordEmail dispatches an UPDATE_PASSWORD action email.
func (s *Service) SendResetPasswordEmail(ctx context.Context, targetID string) error {
	if !uuidPattern.MatchString(targetID) {
		return fmt.Errorf("%w: id must be a UUID", ErrBadRequest)
	}
	return s.provider.SendResetPasswordEmail(ctx, targetID)
}

// SetUserPassword sets the user's password directly. When temporary is true
// the user must change it on next login.
func (s *Service) SetUserPassword(ctx context.Context, targetID, password string, temporary bool) error {
	if !uuidPattern.MatchString(targetID) {
		return fmt.Errorf("%w: id must be a UUID", ErrBadRequest)
	}
	if len(password) < 8 {
		return invalidField("password", "must be at least 8 characters")
	}
	return s.provider.SetUserPassword(ctx, targetID, password, temporary)
}

// ResendInvitation re-sends the invitation action email. Idempotent.
func (s *Service) ResendInvitation(ctx context.Context, targetID string) (*Invitation, error) {
	if !uuidPattern.MatchString(targetID) {
		return nil, fmt.Errorf("%w: id must be a UUID", ErrBadRequest)
	}
	return s.provider.ResendInvitation(ctx, targetID)
}

// DeleteUser removes a user. Guards:
//
//   - target id must be a UUID
//   - reject if target == caller (no self-delete)
//   - reject if target is the last enabled admin in the realm
func (s *Service) DeleteUser(ctx context.Context, callerSubject, targetID string) error {
	if !uuidPattern.MatchString(targetID) {
		return fmt.Errorf("%w: id must be a UUID", ErrBadRequest)
	}
	if callerSubject != "" && callerSubject == targetID {
		return fmt.Errorf("%w: cannot delete your own account", ErrForbidden)
	}
	if err := s.assertNotLastAdmin(ctx, targetID); err != nil {
		return err
	}
	return s.provider.DeleteUser(ctx, targetID)
}

// DeleteInvitation is the invitation-resource alias for DeleteUser. An
// invitation is just a user in invited-but-incomplete state; deleting it
// is deleting the user. Same guards apply (UUID, no self-delete) but the
// last-admin check is meaningless here — an invited user can't have admin
// yet (admins must exist to invite).
func (s *Service) DeleteInvitation(ctx context.Context, callerSubject, targetID string) error {
	if !uuidPattern.MatchString(targetID) {
		return fmt.Errorf("%w: id must be a UUID", ErrBadRequest)
	}
	if callerSubject != "" && callerSubject == targetID {
		return fmt.Errorf("%w: cannot delete your own invitation", ErrForbidden)
	}
	return s.provider.DeleteUser(ctx, targetID)
}

// DeleteRole removes a realm role. Guards:
//
//   - name must match the role-name pattern (defense in depth)
//   - protected names (admin, user, default-roles-*, offline_access,
//     uma_authorization) are rejected
func (s *Service) DeleteRole(ctx context.Context, name string) error {
	if !roleNamePattern.MatchString(name) {
		return invalidField("name", "is malformed")
	}
	norm := strings.ToLower(strings.TrimSpace(name))
	if isProtectedRoleName(norm) {
		return fmt.Errorf("%w: role %q is protected and cannot be deleted", ErrRoleProtected, norm)
	}
	return s.provider.DeleteRole(ctx, name)
}

// DeleteSession revokes a single session.
func (s *Service) DeleteSession(ctx context.Context, sessionID string) error {
	if !uuidPattern.MatchString(sessionID) {
		return fmt.Errorf("%w: session id must be a UUID", ErrBadRequest)
	}
	return s.provider.DeleteSession(ctx, sessionID)
}

// LogoutUserSessions logs the target user out of every active session. We
// do NOT block self-logout here — an admin asking to log themselves out
// of OTHER browsers is a valid recovery action; the worst case is they
// invalidate their current token too, which the UI handles cleanly.
func (s *Service) LogoutUserSessions(ctx context.Context, targetID string) error {
	if !uuidPattern.MatchString(targetID) {
		return fmt.Errorf("%w: id must be a UUID", ErrBadRequest)
	}
	return s.provider.LogoutUserSessions(ctx, targetID)
}

// assertNotLastAdmin returns ErrForbidden when the target user is the only
// enabled member of the `admin` role. Used by DeleteUser, UnassignRoles
// (when removing admin), and UpdateUser (when disabling). The check is
// best-effort: it scans the current admin set; a concurrent operation
// shrinking the set further is the operator's problem (Keycloak isn't
// transactional across these endpoints).
func (s *Service) assertNotLastAdmin(ctx context.Context, targetID string) error {
	admins, err := s.provider.ListUsersByRole(ctx, adminRoleName)
	switch {
	case errors.Is(err, ErrNotFound):
		// The realm has no `admin` role at all, so there is no last admin to
		// protect and nothing to refuse.
		//
		// This is NOT the same as failing to enumerate, and conflating the two
		// was a real bug: `admin` is a LIGHTWEIGHT bootstrap convention, not
		// something every Keycloak realm has. Treating its absence as an error
		// made DeleteUser and disable-user impossible in any connected realm
		// that did not happen to use that name — and the caller saw a bare 404,
		// which points at the user they asked about rather than at a role they
		// never mentioned.
		//
		// Invisible to /admin/*, whose realm is provisioned with the role by
		// the bootstrap; it only surfaced once workspaces could point at realms
		// this product did not create.
		return nil
	case err != nil:
		// Any OTHER failure means we could not enumerate. Fail safe — refuse
		// the operation rather than risk wiping the last admin.
		// ErrAdminAPIUnavailable surfaces as 502 so the caller knows it is not
		// a guard hit.
		return err
	}
	enabledAdmins := 0
	targetIsAdmin := false
	for _, u := range admins {
		if !u.Enabled {
			continue
		}
		enabledAdmins++
		if u.ID == targetID {
			targetIsAdmin = true
		}
	}
	if targetIsAdmin && enabledAdmins <= 1 {
		return fmt.Errorf("%w: cannot remove the last enabled admin from the realm", ErrForbidden)
	}
	return nil
}
