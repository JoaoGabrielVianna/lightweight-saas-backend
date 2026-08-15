package authz

import "net/http"

// Error is a client-facing authorization failure with a stable code, in the
// same shape internal/workspace, internal/connection and internal/identityruntime
// use. The /v1 surface has one error contract.
type Error struct {
	Code    string
	Message string
	Status  int
}

func (e *Error) Error() string { return e.Code }

// ErrorResponse is the stable /v1 envelope.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody carries the stable code, prose, and the request id tying the
// response to the log line holding the real cause.
type ErrorBody struct {
	Code      string `json:"code"       example:"insufficient_scope"`
	Message   string `json:"message"    example:"This credential does not carry the required scope"`
	RequestID string `json:"request_id" example:"3f2504e0-4f89-41d3-9a0c-0305e82c3301"`
}

var (
	// ErrUnauthenticated — no principal on the request. Defensive: it means a
	// group was assembled without AuthenticatePrincipal.
	ErrUnauthenticated = &Error{
		Code:    "credential_invalid",
		Message: "The provided credential is not valid",
		Status:  http.StatusUnauthorized,
	}

	// ErrWorkspaceMismatch — a project credential addressed a workspace it is
	// not bound to.
	//
	// 403 rather than 404, and the reasoning is the reverse of the usual one.
	// Hiding existence behind a 404 would be pointless here: the check never
	// consults the database, so the response is byte-identical whether the
	// workspace exists, is archived, or was never created. Nothing leaks, and
	// 403 tells a developer the truth — the credential is fine, the target is
	// not theirs.
	ErrWorkspaceMismatch = &Error{
		Code:    "workspace_mismatch",
		Message: "This credential is not authorized for the requested workspace",
		Status:  http.StatusForbidden,
	}

	// ErrInsufficientScope — the credential is in the right workspace but does
	// not carry the capability. Accompanied by a WWW-Authenticate header naming
	// the required scope (RFC 6750 §3.1).
	ErrInsufficientScope = &Error{
		Code:    "insufficient_scope",
		Message: "This credential does not carry the scope required for this operation",
		Status:  http.StatusForbidden,
	}

	// ErrOperatorOnly — the route is part of the control plane and no scope
	// grants it. Distinct from insufficient_scope on purpose: no key can be
	// issued that would satisfy this, so telling a developer to add a scope
	// would send them looking for one that does not exist.
	ErrOperatorOnly = &Error{
		Code:    "operator_only",
		Message: "This operation is restricted to console operators and is not available to project credentials",
		Status:  http.StatusForbidden,
	}

	// ErrForbidden — the operator-side denial (missing realm admin role, live
	// admin check refused), and the fail-closed answer for an unclassified
	// route. Kept as the generic code the /v1 surface already returned for
	// these, so operator-visible behaviour does not change.
	ErrForbidden = &Error{
		Code:    "forbidden",
		Message: "Forbidden",
		Status:  http.StatusForbidden,
	}

	// ErrAuthorizationUnavailable — the live admin backend could not answer.
	// 503, matching RequireLiveAdmin: the request was well-formed and the
	// authorization backend is temporarily unable to decide.
	ErrAuthorizationUnavailable = &Error{
		Code:    "authorization_unavailable",
		Message: "Authorization check unavailable",
		Status:  http.StatusServiceUnavailable,
	}

	// ErrRateLimited — too many requests for this credential.
	ErrRateLimited = &Error{
		Code:    "rate_limit_exceeded",
		Message: "Rate limit exceeded for this credential",
		Status:  http.StatusTooManyRequests,
	}
)
