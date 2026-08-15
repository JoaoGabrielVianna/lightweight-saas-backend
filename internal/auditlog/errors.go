package auditlog

import (
	"errors"
	"net/http"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
)

// auditActorType aliases audit.ActorType so service.go can name it without
// importing the audit package for one conversion.
type auditActorType = audit.ActorType

var errBadCursor = errors.New("auditlog: malformed cursor")

// Error is a client-facing failure, in the same shape internal/workspace,
// internal/connection, internal/project and internal/identityruntime use. The
// /v1 surface has one error contract and this package does not get a second.
type Error struct {
	Code    string
	Message string
	Status  int

	// Field names the query parameter a validation failure is about, matching
	// the `field` key added to the envelope in Slice 9 ([TD-029]). A caller
	// that sent a bad cursor and a caller that sent a bad limit both get
	// `invalid_request`, and only this tells them which to fix.
	//
	// [TD-029]: docs/TECH_DEBT.md#td-029
	Field string
}

func (e *Error) Error() string { return e.Code }

// WithField returns a COPY naming the offending parameter.
//
// A copy, because the catalogue below is package-level singletons: mutating one
// would make the next request inherit this one's field, which is a bug that
// only appears under concurrency.
func (e *Error) WithField(field string) *Error {
	clone := *e
	clone.Field = field
	return &clone
}

var (
	// ErrInvalidFilter — a malformed query parameter. One code for every one of
	// them, with `field` saying which, exactly as the identity surface does:
	// a client branches on the code and reads the field, rather than needing a
	// code per parameter.
	ErrInvalidFilter = &Error{
		Code:    "invalid_request",
		Message: "Request is invalid",
		Status:  http.StatusBadRequest,
	}

	// ErrAuditUnavailable — the store could not answer.
	//
	// 503 and not 500: the request was well-formed and the failure is
	// infrastructure, so a client should retry rather than change anything.
	ErrAuditUnavailable = &Error{
		Code:    "audit_unavailable",
		Message: "Audit history is temporarily unavailable",
		Status:  http.StatusServiceUnavailable,
	}
)
