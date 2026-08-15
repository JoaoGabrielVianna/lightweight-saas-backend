package identityruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logging"
	"github.com/gin-gonic/gin"
)

// Audit attribution (Phase 10).
//
// The requirement is narrow and worth stating precisely: workspace-scoped
// mutations must be DISTINGUISHABLE in the sinks that already exist. No durable
// storage, no new event model — just enough that an operator reading
// "user.deleted" in a multi-workspace installation can tell which realm it
// happened in.

// capturingRecorder collects events instead of writing them anywhere.
type capturingRecorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *capturingRecorder) Record(_ context.Context, e audit.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *capturingRecorder) all() []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit.Event(nil), r.events...)
}

// captureAudit swaps in a capturing recorder for the duration of a test and
// restores the previous one afterwards. audit.SetDefault returning the previous
// recorder is what makes this safe to do in a package that runs its tests in
// one process.
func captureAudit(t *testing.T) *capturingRecorder {
	t.Helper()
	rec := &capturingRecorder{}
	prev := audit.SetDefault(rec)
	t.Cleanup(func() { audit.SetDefault(prev) })
	return rec
}

// TestAudit_EveryMutationCarriesTheWorkspace walks every mutating route and
// asserts the emitted event names the workspace.
//
// Route-walking rather than a handful of spot checks, for the same reason the
// write-guard test walks: attribution added by hand to twenty handlers is
// attribution one of them will be missing.
func TestAudit_EveryMutationCarriesTheWorkspace(t *testing.T) {
	f, _ := newStubFixture(t)
	h := NewHandler(f.resolver)
	r := mountAll(t, h)

	for _, rt := range allWorkspaceIdentityRoutes(h) {
		if !rt.mutating {
			continue
		}
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			rec := captureAudit(t)

			w := do(r, rt.method, rt.concretePath(), rt.body)
			if w.Code >= 400 {
				t.Fatalf("fixture problem: status %d (body %s)", w.Code, w.Body.String())
			}

			events := rec.all()
			if len(events) == 0 {
				t.Fatal("mutation emitted no audit event")
			}
			for _, e := range events {
				if e.Workspace != testPublicID {
					t.Errorf("event %q carries workspace %q, want %q",
						e.Action, e.Workspace, testPublicID)
				}
				if e.Action == "" {
					t.Error("event has no action")
				}
				if e.Target.Kind == "" {
					t.Errorf("event %q has no target kind", e.Action)
				}
			}
		})
	}
}

// TestAudit_FailedMutationsAlsoCarryWorkspaceAndReason. A refusal is exactly
// the event an operator most wants to find later, and it is the one a handler
// that returns early is most likely to skip.
func TestAudit_FailedMutationsAlsoCarryWorkspaceAndReason(t *testing.T) {
	f, _ := newStubFixture(t)
	r := mountAll(t, NewHandler(f.resolver))
	rec := captureAudit(t)

	// A reserved role name: refused by the shared service, after the write
	// guard has passed, so the handler reaches its audit call with an error.
	w := do(r, http.MethodPost, "/v1/workspaces/"+testPublicID+"/roles", `{"name":"admin"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body.String())
	}

	events := rec.all()
	if len(events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(events))
	}
	e := events[0]
	if e.Workspace != testPublicID {
		t.Errorf("workspace = %q, want %q", e.Workspace, testPublicID)
	}
	if e.Reason == "" {
		t.Error("a failed mutation emitted no reason")
	}
	if e.Action != audit.ActionRoleCreated {
		t.Errorf("action = %q, want role.created", e.Action)
	}
}

// TestAudit_NoEventForARequestRefusedBeforeResolution.
//
// A request refused by the write guard, or by workspace resolution, never
// touched the provider and changed nothing. Emitting a mutation event for it
// would put "role.created" in the log for a role that was never created —
// worse than silence, because it is wrong rather than missing.
func TestAudit_NoEventForARequestRefusedBeforeResolution(t *testing.T) {
	f, _ := newStubFixture(t)
	f.conns.active[testWorkspaceID].AccessMode = "limited"
	r := mountAll(t, NewHandler(f.resolver))
	rec := captureAudit(t)

	w := do(r, http.MethodPost, "/v1/workspaces/"+testPublicID+"/roles", `{"name":"billing-admin"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if got := rec.all(); len(got) != 0 {
		t.Errorf("emitted %d events for a request that never reached the provider: %+v", len(got), got)
	}
}

// TestAudit_PasswordsNeverReachAnEvent is the leak rule applied to the audit
// trail.
//
// Two routes accept a password. Target.Name is the only free-text field on an
// event and it is set from the request in several handlers, so this is a
// realistic place for a credential to end up — in a log that is deliberately
// kept for a long time.
func TestAudit_PasswordsNeverReachAnEvent(t *testing.T) {
	const secret = "sup3r-s3cret-passw0rd"

	f, _ := newStubFixture(t)
	r := mountAll(t, NewHandler(f.resolver))
	rec := captureAudit(t)

	if w := do(r, http.MethodPut,
		"/v1/workspaces/"+testPublicID+"/users/"+testUserUUID+"/password",
		`{"password":"`+secret+`","temporary":true}`); w.Code != http.StatusNoContent {
		t.Fatalf("set password: status %d (body %s)", w.Code, w.Body.String())
	}
	if w := do(r, http.MethodPost,
		"/v1/workspaces/"+testPublicID+"/users",
		`{"email":"ada@example.test","temporary_password":"`+secret+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("create user: status %d (body %s)", w.Code, w.Body.String())
	}

	for _, e := range rec.all() {
		rendered := string(e.Action) + "|" + e.Target.Kind + "|" + e.Target.ID + "|" +
			e.Target.Name + "|" + e.Reason + "|" + e.Workspace
		if strings.Contains(rendered, secret) {
			t.Errorf("audit event %q carries the password: %s", e.Action, rendered)
		}
		for k, v := range e.Extra {
			if s, isStr := v.(string); isStr && strings.Contains(s, secret) {
				t.Errorf("audit event %q carries the password in extra[%s]", e.Action, k)
			}
		}
	}
}

// TestAudit_LegacyEventsHaveNoWorkspace pins the other half of
// distinguishability: an event with no workspace came from the unscoped legacy
// surface, and that must stay readable as a distinction rather than becoming an
// empty-string default everywhere.
func TestAudit_LegacyEventsHaveNoWorkspace(t *testing.T) {
	// RecordMutation is what every /admin/* handler calls. It must produce an
	// event with an EMPTY workspace, so `omitempty` drops the key entirely and
	// a log consumer can tell an unscoped legacy operation from a scoped one.
	rec := captureAudit(t)

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodDelete, "/admin/users/u1", nil)
	logging.RecordMutation(c, audit.ActionUserDeleted, audit.Target{Kind: "user", ID: "u1"}, nil)

	events := rec.all()
	if len(events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(events))
	}
	if events[0].Workspace != "" {
		t.Errorf("a legacy mutation carries workspace %q — the two surfaces are no longer "+
			"distinguishable in the log", events[0].Workspace)
	}

	// And the JSON really omits the key rather than emitting "".
	encoded, err := json.Marshal(events[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "workspace") {
		t.Errorf("legacy event JSON mentions workspace: %s", encoded)
	}
}
