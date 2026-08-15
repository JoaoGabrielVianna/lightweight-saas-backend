package connection

import "net/http"

// Error is a client-facing failure with a stable machine-readable code, in the
// same shape internal/workspace uses — the /v1 surface has one error contract,
// not one per domain.
//
// The code is the contract. Statuses collide and messages get reworded; a
// client branching on `connection_not_verified` keeps working through both.
type Error struct {
	Code string
	// Message never contains a database error, a SQL fragment, a constraint
	// name, a URL from the provider, or any part of a credential. Those go to
	// the log, keyed by request id.
	Message string
	Status  int
}

func (e *Error) Error() string { return e.Code }

// The stable connection error catalogue.
var (
	ErrNotFound = &Error{
		Code:    "connection_not_found",
		Message: "Connection not found",
		Status:  http.StatusNotFound,
	}

	ErrInvalidID = &Error{
		Code:    "invalid_connection_id",
		Message: "Connection id must be in the form conn_<uuid>",
		Status:  http.StatusBadRequest,
	}

	// ErrNotVerified — activation attempted on a connection whose last probe
	// did not pass (or never ran).
	ErrNotVerified = &Error{
		Code:    "connection_not_verified",
		Message: "Connection must pass verification before it can be activated",
		Status:  http.StatusConflict,
	}

	// ErrVerificationExpired — the probe passed, but too long ago. Re-verify.
	ErrVerificationExpired = &Error{
		Code:    "connection_verification_expired",
		Message: "Verification has expired; verify the connection again before activating",
		Status:  http.StatusConflict,
	}

	// ErrAlreadyActive — activating the connection that is already active.
	// Deliberately NOT idempotent-success: unlike archiving a workspace, this
	// is not a retry-safe no-op, because the caller may believe they are
	// switching away from a different connection.
	ErrAlreadyActive = &Error{
		Code:    "connection_already_active",
		Message: "Connection is already active",
		Status:  http.StatusConflict,
	}

	// ErrWorkspaceHasActive — another connection in the workspace won the
	// activation race. Surfaced from the partial unique index, not from a
	// pre-flight read.
	ErrWorkspaceHasActive = &Error{
		Code:    "workspace_has_active_connection",
		Message: "Another connection in this workspace was activated concurrently; retry",
		Status:  http.StatusConflict,
	}

	// ErrRetired — retired is terminal. Any state-changing operation on a
	// retired connection except delete is refused.
	ErrRetired = &Error{
		Code:    "connection_retired",
		Message: "Connection is retired and cannot be modified or reactivated",
		Status:  http.StatusConflict,
	}

	// ErrActiveCannotDelete — retire before deleting. See Connection.CanDelete.
	ErrActiveCannotDelete = &Error{
		Code:    "connection_active_cannot_delete",
		Message: "Active connection cannot be deleted; retire it first",
		Status:  http.StatusConflict,
	}

	// ErrNotDraft — configuration is editable only while draft.
	ErrNotDraft = &Error{
		Code:    "connection_not_draft",
		Message: "Only a draft connection's configuration can be changed",
		Status:  http.StatusConflict,
	}

	ErrNameRequired = &Error{
		Code:    "connection_name_required",
		Message: "Name is required",
		Status:  http.StatusBadRequest,
	}

	ErrBaseURLInvalid = &Error{
		Code:    "connection_base_url_invalid",
		Message: "base_url must be an absolute http or https URL",
		Status:  http.StatusBadRequest,
	}

	ErrRealmRequired = &Error{
		Code:    "connection_realm_required",
		Message: "realm is required",
		Status:  http.StatusBadRequest,
	}

	ErrClientIDRequired = &Error{
		Code:    "connection_client_id_required",
		Message: "client_id is required",
		Status:  http.StatusBadRequest,
	}

	ErrClientSecretRequired = &Error{
		Code:    "connection_client_secret_required",
		Message: "client_secret is required",
		Status:  http.StatusBadRequest,
	}

	ErrProviderUnsupported = &Error{
		Code:    "connection_provider_unsupported",
		Message: "provider must be one of: keycloak",
		Status:  http.StatusBadRequest,
	}

	ErrInvalidStatusFilter = &Error{
		Code:    "invalid_status_filter",
		Message: "status must be one of: draft, active, retired, all",
		Status:  http.StatusBadRequest,
	}

	// The two workspace-scoped errors a connection request can hit. The codes
	// deliberately match internal/workspace's: from a client's point of view
	// "that workspace does not exist" is one fact with one name, whichever
	// endpoint reported it.
	ErrWorkspaceNotFound = &Error{
		Code:    "workspace_not_found",
		Message: "Workspace not found",
		Status:  http.StatusNotFound,
	}

	ErrWorkspaceArchived = &Error{
		Code:    "workspace_archived",
		Message: "Workspace is archived and cannot be modified",
		Status:  http.StatusConflict,
	}

	ErrInvalidWorkspaceID = &Error{
		Code:    "invalid_workspace_id",
		Message: "Workspace id must be in the form ws_<uuid>",
		Status:  http.StatusBadRequest,
	}

	ErrInvalidRequest = &Error{
		Code:    "invalid_request",
		Message: "Request body is invalid",
		Status:  http.StatusBadRequest,
	}

	ErrInternal = &Error{
		Code:    "internal_error",
		Message: "Internal error",
		Status:  http.StatusInternalServerError,
	}
)

// immutableFieldError names the field a caller tried to change. Same stable
// code, sharper message.
func immutableFieldError(field string) *Error {
	return &Error{
		Code:    ErrInvalidRequest.Code,
		Message: field + " is immutable and cannot be changed",
		Status:  http.StatusBadRequest,
	}
}
