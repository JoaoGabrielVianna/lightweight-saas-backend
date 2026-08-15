package logging

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
)

// The handler's half of TD-033.
//
// The services own the transaction; these two helpers own what happens
// afterwards, and the third branch is the one worth testing hardest: when a
// mutation was rolled back BECAUSE the audit store could not be written,
// recording a failure event would send a second write to the store that just
// failed. That is how a transient database problem becomes an outage.

type capturingRecorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *capturingRecorder) Record(_ context.Context, e audit.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *capturingRecorder) snapshot() []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]audit.Event, len(r.events))
	copy(out, r.events)
	return out
}

func captureEvents(t *testing.T) *capturingRecorder {
	t.Helper()
	rec := &capturingRecorder{}
	previous := audit.SetDefault(rec)
	t.Cleanup(func() { audit.SetDefault(previous) })
	return rec
}

func testGinContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/workspaces", nil)
	return c
}

// ─── The no-recursion rule ──────────────────────────────────────────────────

// TestRecordControlPlaneOutcome_AuditFailureEmitsNothing.
//
// The rule, stated as a test because a comment cannot enforce it: when the
// mutation was rolled back because its audit row would not write, NOTHING is
// emitted on the audit channel.
//
// Not "a failure event is emitted and the durable sink happens to drop it" —
// nothing is emitted at all. The durable recorder is one sink among three, and
// an event sent here would reach it and attempt exactly the write that just
// failed. auditlog.Recorder.RecordTx has already logged and counted the
// failure, which is where an operator learns about it.
func TestRecordControlPlaneOutcome_AuditFailureEmitsNothing(t *testing.T) {
	rec := captureEvents(t)
	c := testGinContext()

	ev := ControlPlaneEvent(c, audit.ActionWorkspaceCreated)
	rolledBack := errors.Join(audit.ErrNotRecorded, errors.New("the store refused"))

	RecordControlPlaneOutcome(c, ev, rolledBack)

	if got := rec.snapshot(); len(got) != 0 {
		t.Errorf("emitted %d event(s) after an audit-write failure; the store that would receive "+
			"them is the one that just failed. Got: %+v", len(got), got)
	}
}

// TestRecordControlPlaneOutcome_AuditFailureIsDetectedThroughWrapping.
//
// The check is errors.Is, not a string match or an equality test, so a service
// that wraps the sentinel with its own context still takes the silent branch.
// A stricter check would work in the unit test and fail in production, where
// every service adds context.
func TestRecordControlPlaneOutcome_AuditFailureIsDetectedThroughWrapping(t *testing.T) {
	for name, err := range map[string]error{
		"joined":  errors.Join(audit.ErrNotRecorded, errors.New("context")),
		"wrapped": fmt.Errorf("saving workspace: %w", audit.ErrNotRecorded),
		"double":  fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", audit.ErrNotRecorded)),
		"bare":    audit.ErrNotRecorded,
	} {
		t.Run(name, func(t *testing.T) {
			rec := captureEvents(t)
			c := testGinContext()
			RecordControlPlaneOutcome(c, ControlPlaneEvent(c, audit.ActionWorkspaceCreated), err)
			if got := rec.snapshot(); len(got) != 0 {
				t.Errorf("emitted %d event(s) for an audit failure wrapped as %q", len(got), name)
			}
		})
	}
}

// ─── The other two branches ─────────────────────────────────────────────────

// TestRecordControlPlaneOutcome_SuccessMarksTheEventAsAlreadyPersisted.
//
// On success the durable row is already in, written inside the committed
// transaction. The event still goes out — the log line and the in-process ring
// are not what was written transactionally, and losing them would make a
// successful control-plane mutation invisible to `journalctl`.
//
// The marker is what stops the durable recorder writing a SECOND row.
func TestRecordControlPlaneOutcome_SuccessMarksTheEventAsAlreadyPersisted(t *testing.T) {
	rec := captureEvents(t)
	c := testGinContext()

	ev := ControlPlaneEvent(c, audit.ActionWorkspaceCreated)
	ev.Workspace = "ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301"

	RecordControlPlaneOutcome(c, ev, nil)

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("emitted %d events, want exactly 1", len(got))
	}
	if !got[0].PersistedInTransaction {
		t.Error("the event is not marked as already persisted; the durable recorder would write " +
			"a second row for a mutation that already has one")
	}
	if got[0].Reason != "" {
		t.Errorf("a successful mutation carries Reason %q, which would make it an outcome=failure row",
			got[0].Reason)
	}
}

// TestRecordControlPlaneOutcome_DomainFailureIsStillRecorded.
//
// A mutation refused for a DOMAIN reason — a taken slug, an archived
// workspace — committed nothing, so there is nothing for its event to be atomic
// with. It stays best-effort and takes the ordinary path, exactly as before
// this slice: the trail still answers "who tried to archive this and was
// refused".
//
// And it must NOT carry the marker, or the durable sink would skip a row nobody
// wrote.
func TestRecordControlPlaneOutcome_DomainFailureIsStillRecorded(t *testing.T) {
	rec := captureEvents(t)
	c := testGinContext()

	ev := ControlPlaneEvent(c, audit.ActionWorkspaceArchived)
	RecordControlPlaneOutcome(c, ev, errors.New("workspace_slug_taken"))

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("emitted %d events, want exactly 1", len(got))
	}
	if got[0].PersistedInTransaction {
		t.Error("a domain failure is marked as already persisted; the durable sink would skip " +
			"a row that was never written, and the refusal would vanish from the trail")
	}
	if got[0].Reason != "workspace_slug_taken" {
		t.Errorf("Reason = %q, want the domain error — a failure event with no reason is "+
			"indistinguishable from a success", got[0].Reason)
	}
}

// ─── The skeleton ───────────────────────────────────────────────────────────

// TestControlPlaneEvent_CarriesTheRequestsIdentityAndNothingElse.
//
// The skeleton is built from the REQUEST. It must carry who, from where and
// under which correlation id — and it must NOT guess the workspace or the
// target, because for a create neither exists yet and for everything else the
// authoritative value is the row the service actually touched.
func TestControlPlaneEvent_CarriesTheRequestsIdentityAndNothingElse(t *testing.T) {
	c := testGinContext()
	c.Request.Header.Set("User-Agent", "lightweight-console/1.0")

	ev := ControlPlaneEvent(c, audit.ActionProjectCreated)

	if ev == nil {
		t.Fatal("ControlPlaneEvent returned nil")
	}
	if ev.Action != audit.ActionProjectCreated {
		t.Errorf("Action = %q, want project.created", ev.Action)
	}
	if ev.IP == "" {
		t.Error("no IP; an event that cannot say where it came from is half a record")
	}
	if ev.UserAgent != "lightweight-console/1.0" {
		t.Errorf("UserAgent = %q, want the request's", ev.UserAgent)
	}
	if ev.Workspace != "" {
		t.Errorf("the skeleton pre-filled Workspace = %q; the service sets it from its own "+
			"result so the row names what was touched, not what was asked for", ev.Workspace)
	}
	if ev.Target != (audit.Target{}) {
		t.Errorf("the skeleton pre-filled Target = %+v", ev.Target)
	}
	if ev.PersistedInTransaction {
		t.Error("the skeleton claims to be already persisted before anything ran")
	}
}

// TestRecordControlPlaneOutcome_NilEventIsSafe — defensive, because the
// alternative is a nil dereference on a path that only runs when something has
// already gone wrong.
func TestRecordControlPlaneOutcome_NilEventIsSafe(t *testing.T) {
	rec := captureEvents(t)
	RecordControlPlaneOutcome(testGinContext(), nil, errors.New("boom"))
	if got := rec.snapshot(); len(got) != 0 {
		t.Errorf("emitted %d events for a nil event", len(got))
	}
}
