package lightweight

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The stable error codes a Project Credential can encounter.
//
// These are CONSTANTS, not an enumerated type with a validator, and that is the
// forward-compatibility contract. A server newer than this package can return a
// code with no constant here; it decodes normally, [APIError.Code] holds it
// verbatim, and a caller's default branch handles it. A closed enum would turn
// "the server grew a more precise error" into "the client stopped working".
//
// The list is the subset reachable by a Project Credential. Operator-only codes
// are deliberately absent: this package cannot call the routes that produce
// them.
const (
	// CodeCredentialInvalid — 401. The credential was not accepted, and the
	// server deliberately does not say which of the possible reasons applied:
	// unknown, wrong, revoked, expired, or belonging to an archived project all
	// answer identically, so that a caller cannot use the API to discover which
	// keys exist. The fix is always the same — obtain a new credential.
	CodeCredentialInvalid = "credential_invalid"

	// CodeInsufficientScope — 403. The credential is bound to the right
	// workspace but was not minted with the scope this operation needs. The
	// response's WWW-Authenticate header names the missing scope. Not fixable by
	// this client: an operator must mint a credential with the scope.
	CodeInsufficientScope = "insufficient_scope"

	// CodeWorkspaceMismatch — 403. The request addressed a workspace this
	// credential is not bound to. A correctly configured Client cannot produce
	// this, because the workspace is fixed at construction; seeing it means
	// WorkspaceID and APIKey came from different environments.
	CodeWorkspaceMismatch = "workspace_mismatch"

	// CodeOperatorOnly — 403. The route is part of the control plane and no
	// scope grants it. Distinct from insufficient_scope because no credential
	// can ever satisfy it. This package does not expose any such route, so it is
	// listed for completeness rather than expected.
	CodeOperatorOnly = "operator_only"

	// CodeRateLimitExceeded — 429. Use [APIError.RetryAfter] to pace the retry.
	// This package will not retry for you; see the package documentation.
	CodeRateLimitExceeded = "rate_limit_exceeded"

	// CodeInvalidRequest — 400. A malformed body or a rejected field.
	// [APIError.Field] names the field when the server identified one.
	CodeInvalidRequest = "invalid_request"

	// CodeInvalidWorkspaceID — 400. The workspace id was not well-formed.
	// [NewClient] rejects this before a request is ever built.
	CodeInvalidWorkspaceID = "invalid_workspace_id"

	// CodeInvalidUserID — 400. The user id in the path was not a UUID.
	CodeInvalidUserID = "invalid_user_id"

	// CodeInvalidRoleName — 400. The role name was outside the permitted
	// character set.
	CodeInvalidRoleName = "invalid_role_name"

	// CodeInvalidSessionID — 400. The session id in the path was not a UUID.
	CodeInvalidSessionID = "invalid_session_id"

	// CodeUserNotFound — 404.
	CodeUserNotFound = "user_not_found"
	// CodeRoleNotFound — 404.
	CodeRoleNotFound = "role_not_found"
	// CodeSessionNotFound — 404.
	CodeSessionNotFound = "session_not_found"
	// CodeInvitationNotFound — 404.
	CodeInvitationNotFound = "invitation_not_found"
	// CodeWorkspaceNotFound — 404.
	CodeWorkspaceNotFound = "workspace_not_found"

	// CodeConflict — 409. A state collision with no more specific code: a
	// duplicate email, an invitation with nothing left to resend.
	CodeConflict = "conflict"

	// CodeRoleAlreadyExists — 409.
	CodeRoleAlreadyExists = "role_already_exists"

	// CodeRoleReserved — 409. The name belongs to the platform and cannot be
	// created, modified or deleted by anyone.
	CodeRoleReserved = "role_reserved"

	// CodeRolePrivileged — 403. An administrative role was granted or revoked.
	// A Project Credential holding roles:write still cannot do this: the bound
	// is what stops that scope being an escalation to full administration. Not
	// fixable by adding a scope.
	CodeRolePrivileged = "role_privileged"

	// CodeCallerForbidden — 403. A product rule refused the operation itself
	// (removing the last administrator, for example), not the caller's
	// authorization.
	CodeCallerForbidden = "caller_forbidden"

	// CodeWorkspaceArchived — 409. The workspace is frozen. Nothing this client
	// can do; an operator must un-archive it.
	CodeWorkspaceArchived = "workspace_archived"

	// CodeConnectionMissing — 409. The workspace has no active connection to an
	// identity provider. An operator must configure one.
	CodeConnectionMissing = "workspace_connection_missing"

	// CodeConnectionUnusable — 409. The workspace's connection exists but cannot
	// be used. An operator must repair it.
	CodeConnectionUnusable = "workspace_connection_unusable"

	// CodeConnectionReadOnly — 409. The workspace's connection does not have
	// write access. An operator must widen it.
	CodeConnectionReadOnly = "connection_read_only"

	// CodeProviderForbidden — 409. The workspace's identity provider refused the
	// operation because the workspace's own service account is under-privileged.
	// An operator problem, not a caller problem, which is why it is not a 403.
	CodeProviderForbidden = "provider_forbidden"

	// CodeProviderUnavailable — 502. The workspace's identity provider could not
	// be reached or did not answer usefully. Transient; safe to retry a read.
	CodeProviderUnavailable = "provider_unavailable"

	// CodeAuthorizationUnavailable — 503. The authorization backend could not
	// decide. Transient.
	CodeAuthorizationUnavailable = "authorization_unavailable"

	// CodeAuditUnavailable — 503. The audit store could not be read. Transient.
	CodeAuditUnavailable = "audit_unavailable"

	// CodeInternalError — 500.
	CodeInternalError = "internal_error"
)

// ErrInvalidArgument is returned, without any network traffic, when an argument
// cannot produce a meaningful request.
//
// The bar for checking something here is deliberately high: the server is
// authoritative about validity, and a client that duplicates its rules will
// eventually disagree with it. What IS checked is the small set of local
// invariants where the wrong value does not fail cleanly — an empty user id, for
// instance, builds a URL that addresses a DIFFERENT endpoint rather than a
// missing user, so letting it through would turn a typo into a call nobody
// intended to make.
var ErrInvalidArgument = errors.New("lightweight: invalid argument")

// requiredArg checks a path segment that must not be empty.
func requiredArg(op, name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s: %s is empty", ErrInvalidArgument, op, name)
	}
	return nil
}

// APIError is a refusal by LIGHTWEIGHT: the request arrived, was understood,
// and was answered with a non-2xx status carrying the standard error envelope.
//
// Branch on [APIError.Code]. It is the stable half of the contract — messages
// get reworded and statuses occasionally collide, but a caller switching on the
// code keeps working across both.
//
//	var apiErr *lightweight.APIError
//	if errors.As(err, &apiErr) {
//		switch apiErr.Code {
//		case lightweight.CodeUserNotFound:
//			return nil          // already gone; nothing to do
//		case lightweight.CodeRateLimitExceeded:
//			d, _ := apiErr.RetryAfter()
//			time.Sleep(d)
//		default:
//			return fmt.Errorf("lightweight (request %s): %w", apiErr.RequestID, err)
//		}
//	}
type APIError struct {
	// StatusCode is the HTTP status.
	StatusCode int

	// Code is the stable machine-readable code from the response envelope.
	//
	// Empty only when the server answered non-2xx without a readable envelope —
	// which in practice means something between this client and LIGHTWEIGHT
	// answered instead of LIGHTWEIGHT. Check [APIError.Code] against "" before
	// trusting it to mean anything specific.
	Code string

	// Message is the server's human-readable prose. Safe to log; it never
	// carries secret material, upstream error text or database detail. Not safe
	// to branch on.
	Message string

	// Field names the request field a validation failure was about, when the
	// server identified one. Empty for every error that is not about a field.
	Field string

	// RequestID correlates this response with the server-side log line holding
	// the real cause. Quote it in a support request; it is the single most
	// useful thing to carry into your own logs.
	RequestID string

	// Op is the SDK operation that failed, e.g. "Users.Create". Present so an
	// error read in isolation says what was being attempted.
	Op string

	// Method and Path are the HTTP request that produced this error. Path never
	// contains the API key, which is sent as a header and nowhere else.
	Method string
	Path   string

	// retryAfter is the parsed Retry-After header. Unexported so the accessor
	// can report presence, which a zero duration cannot.
	retryAfter    time.Duration
	retryAfterSet bool
}

// Error implements error.
//
// It names the operation, the status and the code, and it carries the request
// id, because an error line without one is an error line nobody can follow up.
// It NEVER contains the API key: the key is never stored on this type and never
// appears in Path.
func (e *APIError) Error() string {
	msg := fmt.Sprintf("lightweight: %s: %s %s: %d", e.Op, e.Method, e.Path, e.StatusCode)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.Field != "" {
		msg += " (field " + e.Field + ")"
	}
	if e.RequestID != "" {
		msg += " [request_id=" + e.RequestID + "]"
	}
	return msg
}

// RetryAfter reports the server's Retry-After hint.
//
// ok is false when the response carried no such header, which is the common
// case: only rate-limit refusals set one. It is reported separately rather than
// as a zero duration because "retry immediately" and "the server said nothing"
// call for different behaviour, and a caller that cannot tell them apart will
// hammer a server that never asked to be hammered.
//
// This package never acts on the hint itself. Backing off is the caller's
// decision, because only the caller knows whether the operation can be repeated
// safely.
func (e *APIError) RetryAfter() (d time.Duration, ok bool) {
	return e.retryAfter, e.retryAfterSet
}

// parseRetryAfter reads the header in both forms RFC 9110 permits.
//
// A negative or unparseable value yields ok=false rather than a zero duration:
// a malformed hint is no hint, and treating it as "retry now" would turn a
// broken proxy into a retry storm.
func parseRetryAfter(h http.Header) (time.Duration, bool) {
	raw := h.Get("Retry-After")
	if raw == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if when, err := http.ParseTime(raw); err == nil {
		d := time.Until(when)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// RequestError is a failure to obtain any answer at all: DNS, TCP, TLS, a
// cancelled context, an exceeded deadline, a connection reset mid-body.
//
// It is a distinct type from [APIError] because the two mean opposite things. An
// APIError is the server telling you something; a RequestError is the server
// telling you nothing, and a caller that treats "the network was down" as "the
// user does not exist" will delete the wrong things.
//
// It unwraps to the underlying error, so the standard checks keep working:
//
//	errors.Is(err, context.Canceled)
//	errors.Is(err, context.DeadlineExceeded)
type RequestError struct {
	// Op is the SDK operation that failed, e.g. "Users.List".
	Op string
	// Method and Path are what was attempted.
	Method string
	Path   string
	// Err is the underlying transport or context error.
	Err error
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("lightweight: %s: %s %s: %v", e.Op, e.Method, e.Path, e.Err)
}

// Unwrap exposes the transport error to errors.Is and errors.As.
func (e *RequestError) Unwrap() error { return e.Err }

// ProtocolError is an answer this client could not read: a success status with
// a body that is not the documented JSON, a truncated body, or an error status
// whose envelope did not parse.
//
// It exists so that malformed input is never silently turned into zero values.
// A caller receiving this knows the call did not fail — something between here
// and LIGHTWEIGHT (a captive portal, a misconfigured proxy, an HTML error page)
// answered instead, or the operation may in fact have succeeded server-side
// while the response was lost. That distinction matters for a mutation and is
// why this is not folded into [APIError].
type ProtocolError struct {
	// Op is the SDK operation, e.g. "Audit.List".
	Op string
	// Method and Path are the request.
	Method string
	Path   string
	// StatusCode is the status that accompanied the unreadable body.
	StatusCode int
	// RequestID from the X-Request-Id header, when the response carried one.
	// Often empty, precisely because the answer may not have come from
	// LIGHTWEIGHT at all.
	RequestID string
	// Err is the decoding failure.
	Err error
}

func (e *ProtocolError) Error() string {
	msg := fmt.Sprintf("lightweight: %s: %s %s: %d: unreadable response: %v",
		e.Op, e.Method, e.Path, e.StatusCode, e.Err)
	if e.RequestID != "" {
		msg += " [request_id=" + e.RequestID + "]"
	}
	return msg
}

// Unwrap exposes the decoding failure.
func (e *ProtocolError) Unwrap() error { return e.Err }
