// Package authz is the authorization boundary for the /v1 surface.
//
// It answers one question — "may THIS principal perform THIS operation?" — and
// deliberately does not answer "who is the caller?", which belongs to
// internal/auth. Keeping them apart is what makes the pipeline readable:
//
//	AuthenticatePrincipal  →  Principal  →  Authorize  →  handler
//
// Nothing in this package knows about workspaces' contents, connections, or
// providers. It runs BEFORE any of those are touched, which is the property the
// workspace-binding check exists to have: a credential bound to workspace A
// asking for workspace B is refused without B ever being loaded.
package authz

import "strings"

// Scope is a capability a project credential may hold.
//
// The vocabulary is `resource:verb`, is small on purpose, and is duplicated in
// the database as a CHECK constraint (migration 000005). Adding one is a
// migration plus a constant here: a new scope is a change to the authorization
// contract, and making it cost a migration is what stops the set drifting into
// dozens of half-meant permissions.
type Scope string

const (
	// ScopeUsersRead — list and read users in the bound workspace's realm.
	ScopeUsersRead Scope = "users:read"

	// ScopeUsersWrite — create, update, delete users, and trigger the
	// password-reset action email.
	//
	// It does NOT include setting a password directly: PUT .../password is
	// operator-only, because it is a complete account-takeover primitive that
	// the reset flow already covers safely. See registry.go.
	ScopeUsersWrite Scope = "users:write"

	// ScopeRolesRead — list and read realm roles and their membership.
	ScopeRolesRead Scope = "roles:read"

	// ScopeRolesWrite — create, update and delete realm roles, and grant or
	// revoke them on users.
	//
	// Bounded: a project holding this scope cannot touch a PROTECTED role
	// (admin, user, offline_access, uma_authorization, default-roles-*). That
	// bound is what stops `roles:write` being an escalation to realm admin.
	// See internal/identityruntime's role guard.
	ScopeRolesWrite Scope = "roles:write"

	// ScopeSessionsRead — list realm sessions and a user's sessions.
	ScopeSessionsRead Scope = "sessions:read"

	// ScopeSessionsRevoke — revoke one session, or all of a user's.
	//
	// Named `revoke` rather than `write` because a session is never edited,
	// only destroyed, and a permission should say what it does.
	ScopeSessionsRevoke Scope = "sessions:revoke"

	// ScopeInvitationsRead — list pending invitations.
	ScopeInvitationsRead Scope = "invitations:read"

	// ScopeAuditRead — read the workspace's durable audit trail.
	//
	// Read-only and deliberately its own scope rather than part of any other.
	// The trail is the record of everything every actor has done in this
	// workspace, including operators and including OTHER projects: a backend
	// holding this can see that a colleague's credential deleted a user at
	// 03:00. That is a genuine capability, and bundling it into `users:read` —
	// which sounds like "may list users" — would grant it to every integration
	// that only needed a directory.
	//
	// It grants no write of any kind. There is no `audit:write`: events are
	// emitted by the system as a side effect of other operations, and an API
	// that let a caller author history would make the trail worthless.
	ScopeAuditRead Scope = "audit:read"

	// ScopeInvitationsWrite — create, resend and revoke invitations.
	//
	// Note what revoking means in this product: an invitation IS a user in an
	// invited-but-incomplete state, so revoking deletes that user. This scope
	// therefore includes deleting not-yet-accepted users, which the console
	// states when the scope is selected.
	ScopeInvitationsWrite Scope = "invitations:write"
)

// AllScopes is the complete vocabulary, in the order the console renders it.
//
// It MUST agree with the project_credentials_scopes_known CHECK constraint in
// migration 000005; TestScopes_MatchTheDatabaseConstraint pins that.
func AllScopes() []Scope {
	return []Scope{
		ScopeUsersRead, ScopeUsersWrite,
		ScopeRolesRead, ScopeRolesWrite,
		ScopeSessionsRead, ScopeSessionsRevoke,
		ScopeInvitationsRead, ScopeInvitationsWrite,
		ScopeAuditRead,
	}
}

// IsKnownScope reports whether s is in the vocabulary.
func IsKnownScope(s Scope) bool {
	for _, known := range AllScopes() {
		if known == s {
			return true
		}
	}
	return false
}

// NormalizeScopes trims and lowercases each entry, rejects unknown values, and
// removes duplicates while preserving the caller's order.
//
// An empty result is returned for an empty input; the CALLER decides whether
// that is acceptable. It is not acceptable when creating a credential — a
// credential with no scopes could authenticate and do nothing, which is a
// configuration mistake worth reporting rather than a useful state — and the
// service enforces that, so the rule lives with the domain rather than here.
//
// Returns the offending value as `bad` when a scope is not in the vocabulary,
// so the error message can name it.
func NormalizeScopes(raw []string) (out []Scope, bad string, ok bool) {
	seen := make(map[Scope]struct{}, len(raw))
	out = make([]Scope, 0, len(raw))

	for _, r := range raw {
		s := Scope(strings.ToLower(strings.TrimSpace(r)))
		if s == "" {
			return nil, "", false
		}
		if !IsKnownScope(s) {
			return nil, string(s), false
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out, "", true
}

// HasScope reports whether the granted set contains want.
//
// Linear over at most eight entries, which is cheaper than the map that would
// have to be built per request to avoid it.
func HasScope(granted []string, want Scope) bool {
	for _, g := range granted {
		if Scope(g) == want {
			return true
		}
	}
	return false
}

// ScopeStrings renders a scope slice for storage and for the wire.
func ScopeStrings(scopes []Scope) []string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, string(s))
	}
	return out
}
