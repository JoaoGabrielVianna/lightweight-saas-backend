package workspace

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
)

// The transactional collaborators, faked.
//
// ─── What these fakes can and cannot prove ──────────────────────────────────
//
// They can prove the CONTROL FLOW: that the service calls the audit writer for
// every mutation, that it completes the event from its own result, that an
// audit failure surfaces as an error wrapping audit.ErrNotRecorded, and that
// the caller never sees a success it did not get.
//
// They CANNOT prove atomicity. fakeRunner has no transaction, so "the domain
// write was rolled back" is a statement about PostgreSQL that only PostgreSQL
// can make. That proof lives in the integration suite
// (service_audit_integration_test.go), against a real database, with the row
// read back through a connection the transaction never touched.
//
// Stating the split here rather than leaving it implied is the point: a reader
// who sees the unit tests pass should not conclude atomicity was tested.

// fakeRunner runs the callback with no transaction at all.
//
// It passes nil as the handle, which every WithTx in this package treats as
// "stay on the current one" — so the fakes behave exactly as they did before
// this slice, and the service's transactional shape is exercised without a
// database.
type fakeRunner struct {
	mu sync.Mutex

	calls int

	// commitErr, when set, is returned INSTEAD of the callback's result after
	// the callback has already run. It models the one failure the callback
	// cannot cause: COMMIT itself failing.
	commitErr error
}

func (r *fakeRunner) InTx(ctx context.Context, fn func(tx database.Tx) error) error {
	r.mu.Lock()
	r.calls++
	commitErr := r.commitErr
	r.mu.Unlock()

	if err := fn(nil); err != nil {
		return err
	}
	return commitErr
}

func (r *fakeRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// fakeAuditWriter records what the service asked to persist, and can refuse.
type fakeAuditWriter struct {
	mu sync.Mutex

	events []audit.Event

	// failWith, when set, is returned by every RecordTx. It is the seam the
	// rollback tests drive: the domain write has ALREADY run inside the
	// callback by the time this fires, which is what makes it a real
	// after-the-write failure rather than a pre-flight refusal.
	failWith error
}

func (w *fakeAuditWriter) RecordTx(_ context.Context, _ database.Tx, e audit.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failWith != nil {
		return w.failWith
	}
	w.events = append(w.events, e)
	return nil
}

func (w *fakeAuditWriter) recorded() []audit.Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]audit.Event, len(w.events))
	copy(out, w.events)
	return out
}

// only returns the single event recorded, failing when there is not exactly
// one. Every mutation in this domain emits exactly one, so "exactly one" is the
// assertion, not a convenience.
func (w *fakeAuditWriter) only(t *testing.T) audit.Event {
	t.Helper()
	got := w.recorded()
	if len(got) != 1 {
		t.Fatalf("recorded %d events, want exactly 1", len(got))
	}
	return got[0]
}

// auditRefused is what a writer returns when the durable row will not go in.
// It wraps the real sentinel so the service's error mapping is exercised.
var auditRefused = errors.New("fake: " + audit.ErrNotRecorded.Error())

func refusingWriter() *fakeAuditWriter {
	return &fakeAuditWriter{failWith: wrapNotRecorded()}
}

func wrapNotRecorded() error {
	return errors.Join(audit.ErrNotRecorded, auditRefused)
}

// newAuditedService builds a Service over fakes and returns the collaborators
// the audit assertions need. Distinct from newTestService, which wires
// always-succeeding fakes for the tests that are not about audit at all.
func newAuditedService(repo Repository, writer *fakeAuditWriter) (*Service, *fakeRunner) {
	runner := &fakeRunner{}
	svc := NewService(repo, runner, writer)
	svc.now = fixedClock()
	return svc, runner
}

// testEvent is the skeleton a handler would build. Actor and request id are
// present because the service must carry them through to the durable row
// unchanged — see TestService_TheEventCarriesTheRequestsIdentity.
func testEvent(action audit.Action) *audit.Event {
	return &audit.Event{
		Action:    action,
		Actor:     audit.Actor{Type: audit.ActorOperator, Subject: "operator-1", Email: "op@example.test"},
		RequestID: "req-fixture-1",
		IP:        "203.0.113.7",
	}
}
