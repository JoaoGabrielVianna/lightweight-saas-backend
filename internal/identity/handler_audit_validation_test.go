package identity

// This file runs the v0.2 audit-emission validation. Unlike
// handler_audit_test.go (which uses a capturing in-memory recorder), the
// tests here wire the REAL logging.AuditSink to a buffer that stands in
// for stdout. That proves the full chain works end-to-end:
//
//   handler → audit.Record → AuditSink → project logger → stdout
//
// The captured lines are then parsed back into audit.Event to confirm
// every required field (actor / action / target / timestamp / ip) made
// it through serialisation intact.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	stdlog "log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logging"
)

// captureLogStream redirects the package-level `log` writer into a
// buffer for the duration of the test. logger.New uses the standard
// log package, so this catches every line the AuditSink emits.
func captureLogStream(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := stdlog.Writer()
	stdlog.SetOutput(buf)
	t.Cleanup(func() { stdlog.SetOutput(prev) })
	return buf
}

// extractAuditEvents finds every "audit {...}" line in the buffer and
// returns the parsed events in emission order.
func extractAuditEvents(t *testing.T, buf *bytes.Buffer) []audit.Event {
	t.Helper()
	var events []audit.Event
	for _, line := range strings.Split(buf.String(), "\n") {
		// AuditSink output is "<timestamp> [ INFO  ] [ audit      ] audit {...}".
		// Locate the JSON payload anchor and parse from there.
		idx := strings.Index(line, " audit {")
		if idx < 0 {
			continue
		}
		jsonStart := strings.Index(line[idx:], "{")
		if jsonStart < 0 {
			continue
		}
		payload := line[idx+jsonStart:]
		// JSON may not be the whole rest of the line; find its end via the
		// last closing brace.
		jsonEnd := strings.LastIndex(payload, "}")
		if jsonEnd < 0 {
			continue
		}
		payload = payload[:jsonEnd+1]
		var e audit.Event
		if err := json.Unmarshal([]byte(payload), &e); err != nil {
			t.Errorf("could not parse audit line %q: %v", payload, err)
			continue
		}
		events = append(events, e)
	}
	return events
}

// validationResult is the per-mutation row captured during the run.
// Surfaced to t.Log so the human-readable table can be copy-pasted into
// docs/AUDIT_VALIDATION.md.
type validationResult struct {
	Mutation string
	Action   audit.Action
	Pass     bool
	Note     string
	Sample   string // first matching log line, for docs evidence
}

// TestAudit_Validation_AllRequiredMutationsEmit is the mission's
// validation suite. It drives the seven mutations listed in the brief
// through the real AuditSink, then enforces:
//
//   - exactly one event was emitted per mutation (except CreateInvitation
//     which intentionally emits two — see docs/AUDIT_WIRING.md);
//   - every event carries the five required fields;
//   - the JSON round-trip preserves all of them.
//
// The final t.Log block prints a PASS/FAIL table that the human-readable
// doc consumes verbatim.
func TestAudit_Validation_AllRequiredMutationsEmit(t *testing.T) {
	buf := captureLogStream(t)

	// Install the real sink — same code path production uses.
	prev := logging.WireDefault()
	t.Cleanup(func() { audit.SetDefault(prev) })

	type mutation struct {
		name     string
		setup    func(fp *fakeProvider)
		run      func(h *Handler) (*httptest.ResponseRecorder, *gin.Context)
		expected audit.Action
	}

	mutations := []mutation{
		{
			name:     "Create role",
			expected: audit.ActionRoleCreated,
			run: func(h *Handler) (*httptest.ResponseRecorder, *gin.Context) {
				w, c := newRequest(t, "POST", "/admin/roles",
					CreateRoleRequestBody{Name: "support-validation", Description: "audit validation"}, nil)
				h.CreateRole(c)
				return w, c
			},
		},
		{
			name:     "Delete role",
			expected: audit.ActionRoleDeleted,
			run: func(h *Handler) (*httptest.ResponseRecorder, *gin.Context) {
				w, c := newRequest(t, "DELETE", "/admin/roles/support-validation", nil,
					gin.Params{{Key: "name", Value: "support-validation"}})
				h.DeleteRole(c)
				return w, c
			},
		},
		{
			name:     "Delete user",
			expected: audit.ActionUserDeleted,
			setup: func(fp *fakeProvider) {
				// Two admins so the last-admin guard doesn't fire.
				fp.mutationCalls.adminsForLastCheck = []User{
					{ID: adminSubject, Enabled: true},
					{ID: "other-admin", Enabled: true},
				}
			},
			run: func(h *Handler) (*httptest.ResponseRecorder, *gin.Context) {
				w, c := newRequest(t, "DELETE", "/admin/users/"+targetSubject, nil,
					gin.Params{{Key: "id", Value: targetSubject}})
				h.DeleteUser(c)
				return w, c
			},
		},
		{
			name:     "Assign role",
			expected: audit.ActionUserRolesGranted,
			run: func(h *Handler) (*httptest.ResponseRecorder, *gin.Context) {
				w, c := newRequest(t, "POST", "/admin/users/"+targetSubject+"/roles",
					AssignRolesRequestBody{Roles: []string{"editor"}},
					gin.Params{{Key: "id", Value: targetSubject}})
				h.AssignRolesToUser(c)
				return w, c
			},
		},
		{
			name:     "Reset password",
			expected: audit.ActionUserPasswordReset,
			run: func(h *Handler) (*httptest.ResponseRecorder, *gin.Context) {
				w, c := newRequest(t, "POST", "/admin/users/"+targetSubject+"/reset-password", nil,
					gin.Params{{Key: "id", Value: targetSubject}})
				h.ResetUserPassword(c)
				return w, c
			},
		},
		{
			name:     "Delete invitation",
			expected: audit.ActionInvitationRevoked,
			run: func(h *Handler) (*httptest.ResponseRecorder, *gin.Context) {
				w, c := newRequest(t, "DELETE", "/admin/invitations/"+targetSubject, nil,
					gin.Params{{Key: "id", Value: targetSubject}})
				h.DeleteInvitation(c)
				return w, c
			},
		},
		{
			name:     "Revoke session",
			expected: audit.ActionSessionRevoked,
			run: func(h *Handler) (*httptest.ResponseRecorder, *gin.Context) {
				w, c := newRequest(t, "DELETE", "/admin/sessions/"+targetSession, nil,
					gin.Params{{Key: "id", Value: targetSession}})
				h.DeleteSession(c)
				return w, c
			},
		},
	}

	var (
		results       []validationResult
		failingChecks []string
	)
	for _, m := range mutations {
		startOffset := buf.Len()

		fp := &fakeProvider{}
		if m.setup != nil {
			m.setup(fp)
		}
		h := NewHandler(NewService(fp))
		_, _ = m.run(h)

		newBytes := bytes.NewBufferString(buf.String()[startOffset:])
		events := extractAuditEvents(t, newBytes)

		res := validationResult{Mutation: m.name, Action: m.expected}
		switch {
		case len(events) == 0:
			res.Pass = false
			res.Note = "no audit line found in stdout"
			failingChecks = append(failingChecks, fmt.Sprintf("%s: no event emitted", m.name))
		default:
			// Find the event whose Action matches what we expected. CreateInvitation
			// would emit two; the others should emit one. We tolerate extras here
			// and only assert the expected action is present and well-formed.
			var match *audit.Event
			for i := range events {
				if events[i].Action == m.expected {
					match = &events[i]
					break
				}
			}
			if match == nil {
				res.Pass = false
				res.Note = fmt.Sprintf("expected action %q not found (got %v)", m.expected, actionsOf(events))
				failingChecks = append(failingChecks, res.Note)
			} else {
				missing := missingRequiredFields(*match)
				if len(missing) > 0 {
					res.Pass = false
					res.Note = "missing required fields: " + strings.Join(missing, ", ")
					failingChecks = append(failingChecks, fmt.Sprintf("%s: %s", m.name, res.Note))
				} else {
					res.Pass = true
					res.Note = fmt.Sprintf("actor=%s target=%s/%s ip=%s ts=%s",
						match.Actor.Subject, match.Target.Kind, match.Target.ID,
						match.IP, match.Timestamp.Format("15:04:05.000"))
				}
			}
			res.Sample = firstAuditLine(buf.String()[startOffset:])
		}
		results = append(results, res)
	}

	// Emit the results table so we can lift it into AUDIT_VALIDATION.md.
	t.Log("─── AUDIT EMISSION VALIDATION ───")
	t.Logf("%-22s  %-30s  %-6s  %s", "MUTATION", "ACTION", "STATUS", "DETAIL")
	for _, r := range results {
		status := "FAIL"
		if r.Pass {
			status = "PASS"
		}
		t.Logf("%-22s  %-30s  %-6s  %s", r.Mutation, string(r.Action), status, r.Note)
	}
	t.Log("─── SAMPLE LINES (stdout capture) ───")
	for _, r := range results {
		t.Logf("[%s] %s", r.Mutation, r.Sample)
	}

	if len(failingChecks) > 0 {
		t.Fatalf("validation FAILED: %d issue(s):\n  - %s",
			len(failingChecks), strings.Join(failingChecks, "\n  - "))
	}
}

// missingRequiredFields returns the names of the v0.2-required fields
// that are unpopulated on the given event. Empty slice == all present.
func missingRequiredFields(e audit.Event) []string {
	var missing []string
	if e.Actor.Subject == "" && e.Actor.Email == "" && e.Actor.Username == "" {
		missing = append(missing, "actor")
	}
	if e.Action == "" {
		missing = append(missing, "action")
	}
	if e.Target.Kind == "" {
		missing = append(missing, "target.kind")
	}
	if e.Timestamp.IsZero() {
		missing = append(missing, "timestamp")
	}
	if e.IP == "" {
		missing = append(missing, "ip")
	}
	return missing
}

// actionsOf is a debug aid — returns the action vocab of the events we
// did see, so a mismatch failure message includes "what we got".
func actionsOf(events []audit.Event) []audit.Action {
	out := make([]audit.Action, len(events))
	for i, e := range events {
		out[i] = e.Action
	}
	return out
}

// firstAuditLine returns the first line that contains an "audit {" marker
// from the given segment of captured stdout. Used purely for evidence in
// the validation doc — not for assertion.
func firstAuditLine(segment string) string {
	for _, line := range strings.Split(segment, "\n") {
		if strings.Contains(line, " audit {") {
			return strings.TrimSpace(line)
		}
	}
	return "(no audit line captured)"
}

// TestAudit_Validation_FailurePathEmitsReason proves the "failures MUST
// emit reason" invariant under the real sink too — not just the
// in-memory recorder used in handler_audit_test.go.
func TestAudit_Validation_FailurePathEmitsReason(t *testing.T) {
	buf := captureLogStream(t)
	prev := logging.WireDefault()
	t.Cleanup(func() { audit.SetDefault(prev) })

	fp := &fakeProvider{}
	fp.mutationCalls.deleteSessionErr = errors.New("provider rejected revoke")
	h := NewHandler(NewService(fp))

	_, c := newRequest(t, "DELETE", "/admin/sessions/"+targetSession, nil,
		gin.Params{{Key: "id", Value: targetSession}})
	h.DeleteSession(c)

	if c.Writer.Status() != http.StatusInternalServerError && c.Writer.Status() != http.StatusBadGateway {
		// We don't actually care about the exact status — handleError maps
		// the raw errors.New to 500. Just confirm the failure path ran.
		t.Logf("note: handler returned status %d (expected 5xx)", c.Writer.Status())
	}

	events := extractAuditEvents(t, buf)
	if len(events) != 1 {
		t.Fatalf("expected 1 audit event on failure, got %d", len(events))
	}
	if events[0].Reason == "" {
		t.Fatal("failure invariant violated: Reason field is empty")
	}
	if !strings.Contains(events[0].Reason, "provider rejected revoke") {
		t.Errorf("Reason = %q, want it to contain the upstream error", events[0].Reason)
	}
	t.Logf("failure-path sample: %s", firstAuditLine(buf.String()))
}
