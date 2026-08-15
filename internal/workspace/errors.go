package workspace

import "net/http"

// Error is a client-facing failure with a stable machine-readable code.
//
// The code — not the HTTP status, and never the message — is the contract.
// Statuses collide (three different things return 400) and messages are prose
// that will be reworded; a client that branches on `workspace_slug_taken`
// keeps working through both.
//
// Every value of this type is a package-level sentinel below. Constructing one
// ad hoc would put an uncatalogued code on the wire, so don't: add a sentinel.
type Error struct {
	// Code is the stable identifier clients match on.
	Code string
	// Message is human-readable prose. It NEVER contains a database error, a
	// SQL fragment, a constraint name, or any other internal detail — those go
	// to the log, keyed by request id.
	Message string
	// Status is the HTTP status this error maps to.
	Status int
}

// Error implements the error interface. The code is used rather than the
// message so that a wrapped chain printed in a log line stays greppable by the
// same token the client saw.
func (e *Error) Error() string { return e.Code }

// The stable /v1 error catalogue. Adding an entry is an API change; changing
// an existing Code is a breaking one.
var (
	// ErrNotFound — no workspace with that id. Also returned for a
	// syntactically valid id that belongs to nothing, so that probing cannot
	// distinguish "never existed" from "not yours".
	ErrNotFound = &Error{
		Code:    "workspace_not_found",
		Message: "Workspace not found",
		Status:  http.StatusNotFound,
	}

	// ErrSlugTaken — another workspace already holds this slug. Archiving does
	// not release a slug, so this can be reported for a workspace the caller
	// cannot see in the default listing.
	ErrSlugTaken = &Error{
		Code:    "workspace_slug_taken",
		Message: "A workspace with this slug already exists",
		Status:  http.StatusConflict,
	}

	// ErrSlugReserved — the slug is one the platform keeps for itself.
	// Deliberately 400 rather than 409: nothing occupies the slug, the input
	// is simply not acceptable, and retrying after a delete would not help.
	ErrSlugReserved = &Error{
		Code:    "workspace_slug_reserved",
		Message: "This slug is reserved by the platform",
		Status:  http.StatusBadRequest,
	}

	// ErrSlugInvalid — the slug is empty, too long, or not in the normalized
	// form (lowercase alphanumerics joined by single hyphens).
	ErrSlugInvalid = &Error{
		Code:    "workspace_slug_invalid",
		Message: "Slug must be lowercase alphanumeric groups separated by single hyphens, at most 63 characters",
		Status:  http.StatusBadRequest,
	}

	// ErrNameRequired — name was absent, or contained only whitespace.
	ErrNameRequired = &Error{
		Code:    "workspace_name_required",
		Message: "Name is required",
		Status:  http.StatusBadRequest,
	}

	// ErrArchived — the workspace exists but is archived, and the requested
	// change is not permitted in that state. Archived workspaces remain
	// readable; they are not mutable.
	ErrArchived = &Error{
		Code:    "workspace_archived",
		Message: "Workspace is archived and cannot be modified",
		Status:  http.StatusConflict,
	}

	// ErrInvalidID — the path id is not `ws_<uuid>` or a bare UUID. Returned
	// before any query runs, and never conflated with ErrNotFound: a wrong
	// prefix is a client bug, not a missing object.
	ErrInvalidID = &Error{
		Code:    "invalid_workspace_id",
		Message: "Workspace id must be in the form ws_<uuid>",
		Status:  http.StatusBadRequest,
	}

	// ErrInvalidStatusFilter — the `status` query parameter was not one of
	// active, archived, all.
	ErrInvalidStatusFilter = &Error{
		Code:    "invalid_status_filter",
		Message: "status must be one of: active, archived, all",
		Status:  http.StatusBadRequest,
	}

	// ErrInvalidRequest — the body was unparseable, or carried a field that
	// cannot be changed. Used where no more specific code applies.
	ErrInvalidRequest = &Error{
		Code:    "invalid_request",
		Message: "Request body is invalid",
		Status:  http.StatusBadRequest,
	}

	// ErrInternal — anything unexpected. The real cause is logged with the
	// request id and never returned; a client learns only that it was not
	// their fault.
	ErrInternal = &Error{
		Code:    "internal_error",
		Message: "Internal error",
		Status:  http.StatusInternalServerError,
	}
)

// immutableFieldError builds an ErrInvalidRequest variant naming the field the
// caller tried to change. Same code, sharper message — the code stays stable
// while the prose tells a developer exactly what to remove from their payload.
func immutableFieldError(field string) *Error {
	return &Error{
		Code:    ErrInvalidRequest.Code,
		Message: field + " is immutable and cannot be changed",
		Status:  http.StatusBadRequest,
	}
}
