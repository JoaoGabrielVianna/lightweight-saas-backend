package identityruntime

import "net/http"

// Error is a client-facing failure with a stable machine-readable code, in the
// same shape internal/workspace and internal/connection use. The /v1 surface
// has one error contract, not one per package.
//
// The code is the contract. Statuses collide and messages get reworded; a
// client branching on `workspace_connection_missing` keeps working through
// both.
type Error struct {
	Code string
	// Message never contains a SQL error, a constraint name, a ciphertext, a
	// nonce, a client secret, or a raw provider response body. Those go to the
	// log, keyed by request id. The rule is enforced by construction: every
	// value below is a literal, and the only thing that ever varies is which
	// literal gets returned.
	Message string
	Status  int

	// Field names the request field a validation failure is about, when there
	// is one. Empty for every error that is not about a field, which is most
	// of them — an authorization refusal has no field, and inventing one would
	// send a client editing a request that was fine.
	//
	// It is never set from client input. The only values that reach it are the
	// literals internal/identity passes to invalidField, filtered again by
	// WithField below. See [TD-029].
	//
	// [TD-029]: docs/TECH_DEBT.md#td-029
	Field string
}

func (e *Error) Error() string { return e.Code }

// WithField returns a copy of this error naming the offending request field.
//
// A COPY, because the catalogue entries below are package-level singletons:
// mutating one would make the next request about a different field inherit
// this one's, which is the kind of bug that only shows up under concurrency.
//
// The name is validated rather than trusted. Every caller today passes a
// literal, so the check can never fire — which is exactly why it is cheap to
// keep: it means a future caller that plumbs input through here produces no
// field instead of echoing attacker-controlled text into an error body and,
// from there, into every log that records one.
func (e *Error) WithField(field string) *Error {
	if !isKnownFieldName(field) {
		return e
	}
	clone := *e
	clone.Field = field
	return &clone
}

// isKnownFieldName accepts the shape of a JSON field this API defines: lower
// snake_case, short, no punctuation. Not a whitelist of specific names — that
// would be a second list to maintain against the DTOs — but tight enough that
// nothing a client could send survives it.
//
// TestErrors_FieldNamesMatchTheRequestDTOs checks the stronger property: that
// every field name actually produced corresponds to a real JSON tag.
func isKnownFieldName(field string) bool {
	if field == "" || len(field) > 40 {
		return false
	}
	for i := 0; i < len(field); i++ {
		c := field[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9', c == '_':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// The stable runtime-resolution error catalogue.
//
// Three of these codes are deliberately identical to internal/workspace's and
// internal/connection's. "That workspace does not exist" is one fact with one
// name whichever endpoint reported it, and a client that already handles
// `workspace_archived` from PATCH /v1/workspaces should not need a second
// branch to handle it from GET /v1/workspaces/{id}/users.
var (
	ErrInvalidWorkspaceID = &Error{
		Code:    "invalid_workspace_id",
		Message: "Workspace id must be in the form ws_<uuid>",
		Status:  http.StatusBadRequest,
	}

	ErrWorkspaceNotFound = &Error{
		Code:    "workspace_not_found",
		Message: "Workspace not found",
		Status:  http.StatusNotFound,
	}

	// ErrWorkspaceArchived — an archived workspace is frozen. Identity
	// operations through it are refused BEFORE the provider is contacted, so
	// archiving is a real boundary rather than a display state.
	ErrWorkspaceArchived = &Error{
		Code:    "workspace_archived",
		Message: "Workspace is archived and cannot be modified",
		Status:  http.StatusConflict,
	}

	// ErrConnectionMissing — the workspace exists but routes nowhere. This is
	// 409 rather than 404: the resource named in the path is real, and the
	// request fails because of the workspace's state, which is exactly what a
	// conflict is. A 404 here would send a client looking for a typo in an id
	// that was correct.
	ErrConnectionMissing = &Error{
		Code:    "workspace_connection_missing",
		Message: "Workspace has no active connection; activate one before performing identity operations",
		Status:  http.StatusConflict,
	}

	// ErrConnectionUnusable — an active connection whose configuration cannot
	// be turned into a working provider (a field is empty, or it names a
	// provider this build does not implement). Distinct from Missing because
	// the fix is different: one activates a connection, the other repairs one.
	ErrConnectionUnusable = &Error{
		Code:    "workspace_connection_unusable",
		Message: "Workspace's active connection is not usable; re-verify its configuration",
		Status:  http.StatusConflict,
	}

	// ErrCredentialsUnavailable — the sealed provider credential could not be
	// opened: wrong master key, a rotated key, or a tampered row.
	//
	// 500, not 409. This is not a state the caller can resolve and not a
	// property of their request; it is an operator emergency in which the
	// process holds a credential it can no longer read. The message says
	// nothing about which of those it was, for the reason secrets.ErrOpen
	// gives: distinguishing them tells an attacker which guess was closer.
	ErrCredentialsUnavailable = &Error{
		Code:    "provider_credentials_unavailable",
		Message: "Provider credentials could not be loaded",
		Status:  http.StatusInternalServerError,
	}

	// ErrProviderUnavailable — the provider was reached and did not answer
	// usefully: transport failure, 5xx, or an authentication rejection.
	// Matches identity.ErrAdminAPIUnavailable's existing 502 mapping.
	ErrProviderUnavailable = &Error{
		Code:    "provider_unavailable",
		Message: "Identity provider is unavailable",
		Status:  http.StatusBadGateway,
	}

	// ErrConnectionReadOnly — a write was refused because the workspace's
	// active connection has already been shown to be under-privileged.
	//
	// 409, not 403. A 403 on this surface means "you, the caller, may not do
	// this", and every /v1 route already answers 403 from its auth chain for
	// exactly that. This is the opposite situation: the caller is fully
	// authorized and the WORKSPACE is not in a state where the write can be
	// performed — which is what a conflict is, and what every other
	// workspace-state refusal here already returns. Sending 403 would tell an
	// operator to look at their own token for a problem that lives in the
	// connection's service-account roles.
	//
	// See Resolved.CanWrite for why this catches less than its name suggests,
	// and why provider_forbidden is the authoritative answer for a genuinely
	// read-only service account.
	ErrConnectionReadOnly = &Error{
		Code:    "connection_read_only",
		Message: "Workspace's active connection does not have write access; grant its service account realm-management roles and re-verify",
		Status:  http.StatusConflict,
	}

	// ErrRolePrivileged — a machine credential tried to grant or revoke a
	// protected role (admin, user, offline_access, uma_authorization,
	// default-roles-*).
	//
	// 403, and a code of its own rather than insufficient_scope: the credential
	// DOES hold roles:write, so telling a developer to add a scope would send
	// them looking for one that does not exist. What they need to know is that
	// this class of role is outside every machine's reach by design.
	//
	// Operators are unaffected — see Handler.guardPrivilegedRoles.
	ErrRolePrivileged = &Error{
		Code:    "role_privileged",
		Message: "Project credentials cannot grant or revoke administrative roles",
		Status:  http.StatusForbidden,
	}

	ErrInternal = &Error{
		Code:    "internal_error",
		Message: "Internal error",
		Status:  http.StatusInternalServerError,
	}
)
