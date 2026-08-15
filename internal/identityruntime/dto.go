package identityruntime

import "net/http"

// The wire types for the workspace-scoped identity surface.
//
// Success payloads are deliberately NOT redefined here: users, roles, sessions
// and invitations are rendered with internal/identity's own response types, so
// /admin/* and /v1/workspaces/{id}/* cannot disagree about what a user looks
// like on the wire. Only the request bodies and the error envelope are local,
// and the envelope matches the one internal/workspace and internal/connection
// already publish.

// CreateUserRequest is the body for POST /v1/workspaces/{workspace_id}/users.
//
// This mirrors the existing direct-provisioning flow (a user with a temporary
// password they must change on first login), not a new one. The alternative
// creation path is an invitation, which sends an email and therefore needs
// working SMTP on the realm; this one does not.
type CreateUserRequest struct {
	Email     string `json:"email"      validate:"required" example:"ada@example.com"`
	FirstName string `json:"first_name" example:"Ada"`
	LastName  string `json:"last_name"  example:"Lovelace"`
	// TemporaryPassword is REQUIRED and must be at least 8 characters. There is
	// no way to create a user without choosing a credential for them; the
	// alternative is an invitation, which needs working SMTP on the realm.
	//
	// It is never echoed back, never logged, and never part of an audit event.
	TemporaryPassword string `json:"temporary_password" validate:"required" example:"ch4nge-me-now"`
	// Roles is optional: realm role names to grant at creation.
	Roles []string `json:"roles" example:"support"`
}

// UpdateUserRequest is the body for PATCH .../users/{user_id}.
// Every field is a pointer: absent means "leave alone", which is what makes
// this a patch rather than a replace.
type UpdateUserRequest struct {
	FirstName     *string `json:"first_name"`
	LastName      *string `json:"last_name"`
	Email         *string `json:"email"`
	Enabled       *bool   `json:"enabled"`
	EmailVerified *bool   `json:"email_verified"`
}

// CreateRoleRequest is the body for POST .../roles.
//
// Description is a plain field rather than a pointer: unlike a PATCH, absent
// and empty mean the same thing when creating.
type CreateRoleRequest struct {
	Name        string `json:"name"        validate:"required" example:"billing-admin"`
	Description string `json:"description" example:"Can view and refund invoices"`
}

// UpdateRoleRequest is the body for PATCH .../roles/{role_name}.
// Description is the only mutable field; renaming would require rewriting
// every role-mapping that references the old name.
type UpdateRoleRequest struct {
	Description *string `json:"description"`
}

// AssignRolesRequest is the body for POST .../users/{user_id}/roles.
type AssignRolesRequest struct {
	Roles []string `json:"roles" validate:"required" example:"support"`
}

// SetPasswordRequest is the body for PUT .../users/{user_id}/password.
type SetPasswordRequest struct {
	Password string `json:"password" validate:"required" example:"ch4nge-me-now"`
	// Temporary forces a change on next login. Defaults to false, so an
	// omitted field sets a permanent password — the caller must opt in to
	// the friction rather than out of it.
	Temporary bool `json:"temporary" example:"true"`
}

// CreateInvitationRequest is the body for POST .../invitations.
type CreateInvitationRequest struct {
	Email     string `json:"email"      validate:"required" example:"ada@example.com"`
	FirstName string `json:"first_name" example:"Ada"`
	LastName  string `json:"last_name"  example:"Lovelace"`
	// Roles is REQUIRED and must contain at least one existing realm role name.
	// Unlike CreateUserRequest, where roles can be granted afterwards, an
	// invitation with no roles would invite someone to an account that can do
	// nothing.
	Roles []string `json:"roles" validate:"required" example:"support"`
	// ExpiresAt is optional, RFC 3339, and must be in the future.
	ExpiresAt string `json:"expires_at" example:"2026-12-31T23:59:59Z"`
	// InvitedBy defaults to the authenticated caller when omitted.
	InvitedBy string `json:"invited_by" example:"admin@example.com"`
}

// ErrorResponse is the /v1 error envelope.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody carries the stable code, human-readable prose, and the request id
// that ties the response to the server-side log line holding the real cause.
type ErrorBody struct {
	Code    string `json:"code"       example:"workspace_connection_missing"`
	Message string `json:"message"    example:"Workspace has no active connection"`

	// Field names the request field a validation failure is about.
	//
	// `omitempty`, and that is the compatibility contract: an error that is not
	// about a field carries no key at all, so a client written before this
	// existed decodes every response exactly as it did. Adding a key is safe;
	// adding one that is sometimes an empty string is not, because it invites
	// `if err.Field == ""` to be read as "no field" in one place and "field is
	// blank" in another.
	//
	// Set only for `invalid_request`. Never derived from client input.
	Field string `json:"field,omitempty" example:"temporary_password"`

	RequestID string `json:"request_id" example:"3f2504e0-4f89-41d3-9a0c-0305e82c3301"`
}

// Errors raised past the runtime boundary — by the provider or by the identity
// service, rather than by workspace resolution. Separate values from the
// catalogue in errors.go because they mean something different: those are
// about the workspace's configuration, these are about the request.
var (
	// ErrInvalidRequest — a malformed body, or a field the identity service
	// rejected for a reason with no more specific code.
	ErrInvalidRequest = &Error{
		Code:    "invalid_request",
		Message: "Request is invalid",
		Status:  http.StatusBadRequest,
	}

	// ErrInvalidUserID — the path's user id is not a UUID. Reported before
	// the provider is contacted.
	ErrInvalidUserID = &Error{
		Code:    "invalid_user_id",
		Message: "User id must be a UUID",
		Status:  http.StatusBadRequest,
	}

	// ErrInvalidRoleName — the path's role name is outside the permitted
	// charset.
	ErrInvalidRoleName = &Error{
		Code:    "invalid_role_name",
		Message: "Role name is malformed",
		Status:  http.StatusBadRequest,
	}

	// ErrInvalidSessionID — the path's session id is not a UUID.
	ErrInvalidSessionID = &Error{
		Code:    "invalid_session_id",
		Message: "Session id must be a UUID",
		Status:  http.StatusBadRequest,
	}

	// ErrUserNotFound — the workspace resolved and the provider answered, but
	// there is no such user in that realm. Distinct from workspace_not_found,
	// which is about the path's first segment.
	ErrUserNotFound = &Error{
		Code:    "user_not_found",
		Message: "User not found in this workspace",
		Status:  http.StatusNotFound,
	}

	ErrRoleNotFound = &Error{
		Code:    "role_not_found",
		Message: "Role not found in this workspace",
		Status:  http.StatusNotFound,
	}

	ErrSessionNotFound = &Error{
		Code:    "session_not_found",
		Message: "Session not found in this workspace",
		Status:  http.StatusNotFound,
	}

	ErrInvitationNotFound = &Error{
		Code:    "invitation_not_found",
		Message: "Invitation not found in this workspace",
		Status:  http.StatusNotFound,
	}

	// ErrRoleAlreadyExists — the realm already has a role by that name.
	ErrRoleAlreadyExists = &Error{
		Code:    "role_already_exists",
		Message: "A role with that name already exists in this workspace",
		Status:  http.StatusConflict,
	}

	// ErrRoleReserved — the name belongs to the platform or to Keycloak.
	// Covers both halves of that rule: creating a reserved name, and editing
	// or deleting a protected one. One fact, one code — a client cannot act
	// differently on the two, and the recovery is the same (pick another
	// name / leave it alone).
	ErrRoleReserved = &Error{
		Code:    "role_reserved",
		Message: "That role name is reserved and cannot be created, modified or deleted",
		Status:  http.StatusConflict,
	}

	// ErrConflict — the provider refused because of a state collision this
	// surface has no more specific code for (a duplicate user email, an
	// invitation with nothing left to resend).
	ErrConflict = &Error{
		Code:    "conflict",
		Message: "The request conflicts with the current state of this workspace",
		Status:  http.StatusConflict,
	}

	// ErrCallerForbidden — a product rule refused the CALLER: self-delete,
	// self-disable, removing your own admin role, or removing the realm's
	// last enabled admin.
	//
	// The distinction from provider_forbidden is the whole point. This one
	// means "the product will not let you do that"; the other means "the
	// workspace's service account is not allowed to". They have completely
	// different fixes, and conflating them sends operators to the wrong place.
	ErrCallerForbidden = &Error{
		Code:    "caller_forbidden",
		Message: "This operation is not permitted",
		Status:  http.StatusForbidden,
	}

	// ErrProviderForbidden — Keycloak refused the workspace's service
	// account. Not the caller's problem to fix.
	//
	// 409 rather than 403 for the same reason as connection_read_only: the
	// caller is authorized, the workspace's connection is misconfigured. A
	// 403 here would be read as "your token is insufficient" and send an
	// operator to the wrong system entirely.
	ErrProviderForbidden = &Error{
		Code:    "provider_forbidden",
		Message: "Workspace's identity provider refused the operation; its service account is missing realm-management roles",
		Status:  http.StatusConflict,
	}
)
