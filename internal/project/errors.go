package project

import "net/http"

// Error is a client-facing failure with a stable machine-readable code, in the
// same shape internal/workspace, internal/connection, internal/identityruntime
// and internal/authz use. The /v1 surface has one error contract, not one per
// package.
type Error struct {
	// Code is the stable identifier clients match on.
	Code string
	// Message is human-readable prose. It NEVER contains a database error, a
	// SQL fragment, a constraint name, or any part of a credential.
	Message string
	// Status is the HTTP status this error maps to.
	Status int
}

func (e *Error) Error() string { return e.Code }

// ErrorResponse is the stable /v1 envelope.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody carries the stable code, prose, and the correlation id.
type ErrorBody struct {
	Code      string `json:"code"       example:"project_not_found"`
	Message   string `json:"message"    example:"Project not found"`
	RequestID string `json:"request_id" example:"3f2504e0-4f89-41d3-9a0c-0305e82c3301"`
}

// The stable catalogue. Adding an entry is an API change; changing a Code is a
// breaking one.
var (
	// ErrNotFound — no project with that id in this workspace. Also returned
	// for a project that exists in ANOTHER workspace, so that probing cannot
	// distinguish "never existed" from "not in your workspace".
	ErrNotFound = &Error{
		Code:    "project_not_found",
		Message: "Project not found",
		Status:  http.StatusNotFound,
	}

	// ErrArchived — the project exists but is frozen, and the requested change
	// is not permitted in that state. Archived projects remain readable.
	ErrArchived = &Error{
		Code:    "project_archived",
		Message: "Project is archived and cannot be modified",
		Status:  http.StatusConflict,
	}

	// ErrNameTaken — another project in this workspace already holds this name,
	// compared case-insensitively.
	ErrNameTaken = &Error{
		Code:    "project_name_taken",
		Message: "A project with this name already exists in this workspace",
		Status:  http.StatusConflict,
	}

	// ErrNameRequired — name absent, blank, or too long.
	ErrNameRequired = &Error{
		Code:    "project_name_required",
		Message: "Name is required and must be at most 120 characters",
		Status:  http.StatusBadRequest,
	}

	// ErrInvalidID — the path id is not `prj_<uuid>` or a bare UUID. Returned
	// before any query runs, and never conflated with ErrNotFound: a wrong
	// prefix is a client bug, not a missing object.
	ErrInvalidID = &Error{
		Code:    "invalid_project_id",
		Message: "Project id must be in the form prj_<uuid>",
		Status:  http.StatusBadRequest,
	}

	// ErrCredentialNotFound — no credential with that id on this project.
	ErrCredentialNotFound = &Error{
		Code:    "credential_not_found",
		Message: "Credential not found",
		Status:  http.StatusNotFound,
	}

	// ErrInvalidCredentialID — the path id is not `key_<uuid>` or a bare UUID.
	ErrInvalidCredentialID = &Error{
		Code:    "invalid_credential_id",
		Message: "Credential id must be in the form key_<uuid>",
		Status:  http.StatusBadRequest,
	}

	// ErrCredentialAlreadyRevoked — revoking twice. 409 rather than a silent
	// success: an operator pressing revoke on a key someone else already
	// revoked should learn that, not be told they did it.
	ErrCredentialAlreadyRevoked = &Error{
		Code:    "credential_already_revoked",
		Message: "Credential is already revoked",
		Status:  http.StatusConflict,
	}

	// ErrCredentialLimitReached — the project already holds MaxActiveCredentials
	// live credentials. Revoke one before issuing another.
	ErrCredentialLimitReached = &Error{
		Code:    "credential_limit_reached",
		Message: "This project already has the maximum number of active credentials; revoke one before creating another",
		Status:  http.StatusConflict,
	}

	// ErrLabelRequired — credential label absent, blank, or too long. The label
	// is required because an unlabelled key is one nobody dares revoke.
	ErrLabelRequired = &Error{
		Code:    "credential_label_required",
		Message: "Label is required and must be at most 120 characters",
		Status:  http.StatusBadRequest,
	}

	// ErrInvalidScope — a scope was empty, unknown, or the list was empty.
	//
	// An empty list is rejected rather than accepted as "no permissions": a
	// credential that can authenticate and do nothing is a configuration
	// mistake, and silently minting one would waste an operator's time twice,
	// once creating it and once debugging it.
	ErrInvalidScope = &Error{
		Code:    "invalid_scope",
		Message: "Scopes must be a non-empty list drawn from the supported scope vocabulary",
		Status:  http.StatusBadRequest,
	}

	// ErrInvalidExpiry — expires_at is in the past.
	ErrInvalidExpiry = &Error{
		Code:    "invalid_request",
		Message: "expires_at must be in the future",
		Status:  http.StatusBadRequest,
	}

	// ErrInvalidRequest — unparseable body, or an immutable field.
	ErrInvalidRequest = &Error{
		Code:    "invalid_request",
		Message: "Request body is invalid",
		Status:  http.StatusBadRequest,
	}

	// ErrWorkspaceNotFound — the workspace in the path does not exist. Same
	// code the workspace and connection domains use: "that workspace does not
	// exist" is one fact with one name whichever endpoint reported it.
	ErrWorkspaceNotFound = &Error{
		Code:    "workspace_not_found",
		Message: "Workspace not found",
		Status:  http.StatusNotFound,
	}

	// ErrWorkspaceArchived — creating or changing a project inside a frozen
	// workspace.
	ErrWorkspaceArchived = &Error{
		Code:    "workspace_archived",
		Message: "Workspace is archived and cannot be modified",
		Status:  http.StatusConflict,
	}

	// ErrInvalidWorkspaceID — the workspace path id is malformed.
	ErrInvalidWorkspaceID = &Error{
		Code:    "invalid_workspace_id",
		Message: "Workspace id must be in the form ws_<uuid>",
		Status:  http.StatusBadRequest,
	}

	// ErrInternal — anything unexpected. The real cause is logged with the
	// request id and never returned.
	ErrInternal = &Error{
		Code:    "internal_error",
		Message: "Internal error",
		Status:  http.StatusInternalServerError,
	}
)

// invalidScopeError names the offending value while keeping the stable code.
func invalidScopeError(bad string) *Error {
	if bad == "" {
		return ErrInvalidScope
	}
	return &Error{
		Code:    ErrInvalidScope.Code,
		Message: "Unknown scope: " + bad,
		Status:  http.StatusBadRequest,
	}
}

// immutableFieldError builds an ErrInvalidRequest variant naming the field the
// caller tried to change.
func immutableFieldError(field string) *Error {
	return &Error{
		Code:    ErrInvalidRequest.Code,
		Message: field + " is immutable and cannot be changed",
		Status:  http.StatusBadRequest,
	}
}
