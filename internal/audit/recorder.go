package audit

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// Recorder receives audit events. Implementations MUST be non-blocking
// (audit.Record is called inline on the request hot path) and
// concurrency-safe (multiple goroutines may emit events at once).
type Recorder interface {
	Record(ctx context.Context, e Event)
}

// RecorderFunc adapts a plain function to the Recorder interface.
type RecorderFunc func(ctx context.Context, e Event)

// Record satisfies Recorder.
func (f RecorderFunc) Record(ctx context.Context, e Event) { f(ctx, e) }

// noop is the recorder installed at process start. It exists so
// audit.Record is always safe to call — even before bootstrap has wired
// a real sink — and so unit tests that exercise mutation paths don't
// have to register a sink they don't care about.
type noop struct{}

func (noop) Record(context.Context, Event) {}

// def holds the active recorder behind an atomic pointer so SetDefault
// can be called concurrently with in-flight Record calls (e.g. test
// reset paths racing with background workers).
var def atomic.Pointer[Recorder]

func init() {
	var r Recorder = noop{}
	def.Store(&r)
}

// SetDefault swaps the package-level recorder. Passing nil reverts to
// the no-op recorder. Returns the previously-installed recorder so
// callers can chain (e.g. a fan-out wrapper) or restore in tests via
// t.Cleanup.
func SetDefault(r Recorder) Recorder {
	prev := *def.Load()
	if r == nil {
		r = noop{}
	}
	def.Store(&r)
	return prev
}

// Default returns the currently-installed recorder. Useful when a
// composite recorder wants to wrap whatever is already in place.
func Default() Recorder {
	return *def.Load()
}

// Record stamps Timestamp (if zero) and dispatches the event to the
// currently-installed default recorder. Safe to call from any goroutine.
func Record(ctx context.Context, e Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	(*def.Load()).Record(ctx, e)
}

// ErrNotRecorded is the sentinel a transactional writer returns when the
// durable audit row could not be persisted.
//
// It exists so a caller can tell "the mutation failed" from "the mutation was
// rolled back because its audit row could not be written", and the distinction
// matters for exactly one reason: the second case must NOT be followed by an
// attempt to record a failure event, because that would be a second write to
// the store that just failed. See docs/AUDIT.md §7.
//
// It lives in this package rather than in each domain so one errors.Is check
// works everywhere, and so a new domain cannot invent a fourth spelling of it.
var ErrNotRecorded = errors.New("audit: the durable event could not be recorded")
