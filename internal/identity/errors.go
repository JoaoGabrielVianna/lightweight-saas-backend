package identity

import (
	"errors"
	"fmt"
)

// Sentinel errors. Concrete providers wrap these (via fmt.Errorf with %w)
// so callers can errors.Is on the kind without knowing the implementation.
//
// HTTP mapping handled centrally by the handler layer:
//
//	ErrNotFound            -> 404
//	ErrBadRequest          -> 400
//	ErrForbidden           -> 403
//	ErrConflict            -> 409
//	ErrNotConfigured       -> 503 (admin credentials missing at boot)
//	ErrAdminAPIUnavailable -> 502 (upstream Keycloak failed / network / 5xx)
//	other                  -> 500
var (
	ErrNotFound            = errors.New("identity: not found")
	ErrBadRequest          = errors.New("identity: bad request")
	ErrForbidden           = errors.New("identity: forbidden")
	ErrConflict            = errors.New("identity: conflict")
	ErrNotConfigured       = errors.New("identity: admin client credentials not configured")
	ErrAdminAPIUnavailable = errors.New("identity: admin API unavailable")
)

// ErrProviderForbidden is the UPSTREAM half of ErrForbidden: the identity
// provider refused the admin client, rather than this service refusing the
// caller.
//
// The two are worlds apart in what an operator must do about them. A bare
// ErrForbidden comes from a service guard — self-delete, last-admin,
// protected role — and means the CALLER asked for something the product does
// not allow. ErrProviderForbidden means Keycloak returned 403 to our
// service account, and the fix is to grant that account its realm-management
// roles. Reporting the second as if it were the first sends an operator
// looking at their own token for a problem that is in the connection's
// service-account configuration.
//
// It deliberately WRAPS ErrForbidden. Every existing `errors.Is(err,
// ErrForbidden)` — including the whole /admin/* error mapping — keeps
// matching, so introducing this changes no legacy behaviour. Callers that
// want the distinction check ErrProviderForbidden FIRST; callers that do not
// care are unaffected.
//
// This distinction is only as good as the provider's ability to draw it: a
// Keycloak 403 is unambiguous, but an endpoint that answers 404 for an
// unauthorized read is indistinguishable from a genuinely absent resource.
// See docs/WORKSPACE_IDENTITY_API.md §Errors for that limitation.
var ErrProviderForbidden = fmt.Errorf("%w: identity provider refused the admin client", ErrForbidden)

// ErrRoleReserved and ErrRoleProtected name the same fact — this role belongs
// to the platform or to Keycloak, and you may not do that to it — at the two
// points where it is enforced with different consequences.
//
// ErrRoleReserved wraps ErrBadRequest: you asked to CREATE a name that is not
// yours to take. ErrRoleProtected wraps ErrForbidden: the role exists and is
// managed, so editing or deleting it is refused.
//
// They exist as sentinels rather than as bare wrapped errors so a surface with
// machine-readable codes can report `role_reserved` instead of a generic
// `invalid_request`, which tells a client nothing about how to recover. Both
// wrap their original sentinel, so /admin/*'s error mapping — and every test
// asserting on it — is unchanged.
var (
	ErrRoleReserved  = fmt.Errorf("%w: role name is reserved", ErrBadRequest)
	ErrRoleProtected = fmt.Errorf("%w: role is protected", ErrForbidden)
)

// FieldError names the request field a validation failure is about.
//
// # Why this exists
//
// Every validation failure in this service used to reach the client as one
// code with one message: `invalid_request` / "Request is invalid". The service
// knew exactly what was wrong — "email is malformed", "temporary_password is
// required" — and the wire carried none of it ([TD-029]).
//
// That is the error an SDK's users hit most often and the only one they cannot
// act on. It was found by cmd/lwprobe, which is the value of having a consumer
// restricted to the public contract: every test inside this module already knew
// which field it had omitted.
//
// # Why a type and not a parsed message
//
// The message is prose and will be reworded. A client branching on it would
// break the first time someone improved the wording, which is the failure the
// stable-code contract exists to prevent. The field travels as data.
//
// # What may go in Field
//
// The name of a field the CLIENT SENT, spelled as it appears in the request
// body. Never a value, never a column, never anything derived from input:
// every construction site in this package passes a literal, and the boundary in
// internal/identityruntime refuses anything that does not look like one of our
// field names. Echoing input here would put attacker-controlled text into an
// error body and, from there, into logs.
//
// [TD-029]: docs/TECH_DEBT.md#td-029
type FieldError struct {
	// Field is the request field, e.g. "email" or "temporary_password".
	Field string
	// Err is the underlying error, always wrapping ErrBadRequest so every
	// existing errors.Is check keeps matching.
	Err error
}

func (e *FieldError) Error() string { return e.Err.Error() }

// Unwrap keeps errors.Is(err, ErrBadRequest) working, which is what stops this
// from being a breaking change to /admin/*'s error mapping.
func (e *FieldError) Unwrap() error { return e.Err }

// invalidField builds a bad-request error that names the offending field.
//
// reason completes the sentence "<field> <reason>", so call sites read
// invalidField("email", "is required").
func invalidField(field, reason string) error {
	return &FieldError{
		Field: field,
		Err:   fmt.Errorf("%w: %s %s", ErrBadRequest, field, reason),
	}
}

// FieldOf returns the request field a validation error is about, or "".
func FieldOf(err error) string {
	var fe *FieldError
	if errors.As(err, &fe) {
		return fe.Field
	}
	return ""
}
