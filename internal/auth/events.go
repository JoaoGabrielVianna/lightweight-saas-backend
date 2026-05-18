package auth

import (
	"sync/atomic"
	"time"
)

// AuthEventKind enumerates the observable moments in token validation.
// New kinds are added at the bottom; renames are breaking for downstream
// metric/log consumers.
type AuthEventKind string

const (
	EventTokenValidated   AuthEventKind = "token_validated"
	EventValidationFailed AuthEventKind = "validation_failed"
	EventMissingHeader    AuthEventKind = "missing_header"
	EventMalformedHeader  AuthEventKind = "malformed_header"
)

// AuthEvent is emitted by the authentication middleware for every protected
// request. The shape is deliberately wide enough to feed structured logs,
// metrics, or distributed traces without changing the middleware later.
type AuthEvent struct {
	Kind     AuthEventKind
	Subject  string        // empty when validation failed
	Reason   string        // populated on failure
	Path     string        // request path
	Method   string        // HTTP method
	Duration time.Duration // total middleware latency
	Time     time.Time     // when the event fired
}

// EventHook receives every AuthEvent. Implementations must be non-blocking
// and concurrency-safe.
type EventHook func(AuthEvent)

// hook holds the active hook behind an atomic pointer so SetEventHook is safe
// to call from init() while requests are in flight (test reset paths).
var hook atomic.Pointer[EventHook]

func init() {
	noop := EventHook(func(AuthEvent) {})
	hook.Store(&noop)
}

// SetEventHook replaces the current event hook. Pass nil to revert to no-op.
// Returns the previous hook for callers that want to chain.
func SetEventHook(h EventHook) EventHook {
	prev := *hook.Load()
	if h == nil {
		h = func(AuthEvent) {}
	}
	hook.Store(&h)
	return prev
}

// EmitEvent stamps the time and dispatches to the current hook. Called from
// middleware; not part of the public API.
func EmitEvent(e AuthEvent) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	(*hook.Load())(e)
}
