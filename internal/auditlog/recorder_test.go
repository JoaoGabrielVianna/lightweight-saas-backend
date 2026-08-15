package auditlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/metrics"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
)

const (
	testWorkspaceUUID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	testWorkspacePub  = "ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	testProjectID     = "prj_7c9e6679-7425-40de-944b-e07fc1f90ae7"
	testCredentialID  = "key_9b2f4c1a-1111-4222-8333-444455556666"
)

// fakeStore captures what would be persisted, and can be made to fail.
type fakeStore struct {
	mu       sync.Mutex
	recorded []Record
	recErr   error

	page    Page
	listErr error
	lastQ   Query
}

// WithTx returns the receiver. A fake has no transaction, which is exactly why
// the atomicity proof lives in the integration suite and not here.
func (f *fakeStore) WithTx(database.Tx) Store { return f }

func (f *fakeStore) Record(_ context.Context, r Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recErr != nil {
		return f.recErr
	}
	f.recorded = append(f.recorded, r)
	return nil
}

func (f *fakeStore) List(_ context.Context, q Query) (Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastQ = q
	return f.page, f.listErr
}

func (f *fakeStore) DeleteOlderThan(context.Context, time.Time) (int64, error) { return 0, nil }

func (f *fakeStore) all() []Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Record(nil), f.recorded...)
}

func (f *fakeStore) query() Query {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastQ
}

func operatorEvent() audit.Event {
	return audit.Event{
		Action: audit.ActionCredentialRevoked,
		Actor: audit.Actor{
			Type:    audit.ActorOperator,
			Subject: "9c1e6679-7425-40de-944b-e07fc1f90ae7",
			Email:   "ada@example.com",
		},
		Target:    audit.Target{Kind: "project_credential", ID: testCredentialID},
		Workspace: testWorkspacePub,
		RequestID: "req-1",
		IP:        "203.0.113.5",
		Timestamp: time.Now(),
	}
}

func projectEvent() audit.Event {
	return audit.Event{
		Action: audit.ActionUserCreated,
		Actor: audit.Actor{
			Type:         audit.ActorProject,
			ProjectID:    testProjectID,
			CredentialID: testCredentialID,
		},
		Target:    audit.Target{Kind: "user", ID: "u-1", Name: "ada@example.com"},
		Workspace: testWorkspacePub,
		RequestID: "req-2",
		Timestamp: time.Now(),
	}
}

// ---------------------------------------------------------------------------
// Mapping
// ---------------------------------------------------------------------------

func TestRecorder_MapsAnOperatorEvent(t *testing.T) {
	store := &fakeStore{}
	NewRecorder(store).Record(context.Background(), operatorEvent())

	all := store.all()
	if len(all) != 1 {
		t.Fatalf("recorded %d events, want 1", len(all))
	}
	rec := all[0]

	if rec.ActorType != audit.ActorOperator {
		t.Errorf("actor type = %q", rec.ActorType)
	}
	if rec.ActorSubject == "" || rec.ActorEmail == "" {
		t.Error("operator subject/email were not carried")
	}
	if rec.ActorProjectID != "" || rec.ActorCredentialID != "" {
		t.Errorf("operator row carries project fields: %+v", rec)
	}
	// The public workspace id must be resolved to the UUID the column holds.
	if rec.WorkspaceID != testWorkspaceUUID {
		t.Errorf("workspace = %q, want the bare UUID %q", rec.WorkspaceID, testWorkspaceUUID)
	}
	if rec.Outcome != OutcomeSuccess {
		t.Errorf("outcome = %q, want success", rec.Outcome)
	}
	if rec.RequestID != "req-1" {
		t.Errorf("request id = %q", rec.RequestID)
	}
	if rec.ID == "" {
		t.Error("no event id was generated")
	}
}

// TestRecorder_AProjectNeverOccupiesTheSubject is the actor-model invariant.
//
// `actor_subject` means "a Keycloak sub". A prj_ value there would make a
// machine indistinguishable from a person in exactly the records that exist to
// tell them apart — and the database CHECK would reject the row, so getting
// this wrong loses the event entirely.
func TestRecorder_AProjectNeverOccupiesTheSubject(t *testing.T) {
	store := &fakeStore{}
	NewRecorder(store).Record(context.Background(), projectEvent())

	rec := store.all()[0]
	if rec.ActorSubject != "" {
		t.Errorf("project event set actor_subject = %q", rec.ActorSubject)
	}
	if rec.ActorEmail != "" {
		t.Errorf("project event set actor_email = %q", rec.ActorEmail)
	}
	if rec.ActorProjectID != testProjectID || rec.ActorCredentialID != testCredentialID {
		t.Errorf("project ids were not carried: %+v", rec)
	}
}

// TestRecorder_DropsUnattributedEvents — the trail answers "who". An event with
// no principal cannot, and writing it with a blank actor would put rows in the
// table that no investigation can use while making it look complete.
func TestRecorder_DropsUnattributedEvents(t *testing.T) {
	store := &fakeStore{}
	e := operatorEvent()
	e.Actor = audit.Actor{} // no principal

	NewRecorder(store).Record(context.Background(), e)

	if n := len(store.all()); n != 0 {
		t.Errorf("recorded %d unattributed events, want 0", n)
	}
}

// TestRecorder_DropsEventsWithAnUnusableWorkspace.
//
// A workspace-scoped event whose workspace cannot be resolved would be written
// with NULL and become invisible to the only API that reads it. A row that
// exists and cannot be found is worse than a logged failure.
func TestRecorder_DropsEventsWithAnUnusableWorkspace(t *testing.T) {
	store := &fakeStore{}
	e := operatorEvent()
	e.Workspace = "not-a-workspace-id"

	NewRecorder(store).Record(context.Background(), e)

	if n := len(store.all()); n != 0 {
		t.Errorf("recorded %d events with an unusable workspace, want 0", n)
	}
}

// TestRecorder_GlobalEventsArePersistedWithNoWorkspace.
//
// The legacy /admin/* surface has no workspace. Those events are durable — an
// operator deleting a user through /admin/users is exactly the kind of thing a
// trail is for — and are unreachable through the workspace API by construction,
// because that query filters on equality.
func TestRecorder_GlobalEventsArePersistedWithNoWorkspace(t *testing.T) {
	store := &fakeStore{}
	e := operatorEvent()
	e.Workspace = ""

	NewRecorder(store).Record(context.Background(), e)

	all := store.all()
	if len(all) != 1 {
		t.Fatalf("recorded %d events, want 1", len(all))
	}
	if all[0].WorkspaceID != "" {
		t.Errorf("workspace = %q, want empty (NULL)", all[0].WorkspaceID)
	}
}

// TestRecorder_DerivesOutcomeFromTheReason — audit.Event has no outcome field;
// the emitters set Reason only on failure, and that is the existing contract.
func TestRecorder_DerivesOutcomeFromTheReason(t *testing.T) {
	store := &fakeStore{}
	r := NewRecorder(store)

	r.Record(context.Background(), operatorEvent())

	failed := operatorEvent()
	failed.Reason = "identityruntime: provider_unavailable"
	r.Record(context.Background(), failed)

	all := store.all()
	if all[0].Outcome != OutcomeSuccess {
		t.Errorf("event with no reason = %q, want success", all[0].Outcome)
	}
	if all[1].Outcome != OutcomeFailure {
		t.Errorf("event with a reason = %q, want failure", all[1].Outcome)
	}
	if all[1].ReasonCode != "provider_unavailable" {
		t.Errorf("reason code = %q, want provider_unavailable", all[1].ReasonCode)
	}
}

// TestRecorder_DropsAnUnknownResourceKindButKeepsTheEvent.
//
// The database CHECK would reject an unknown kind and lose the whole row. The
// actor, the verb and the outcome are what the row is for; the resource label
// is not worth losing them over.
func TestRecorder_DropsAnUnknownResourceKindButKeepsTheEvent(t *testing.T) {
	store := &fakeStore{}
	e := operatorEvent()
	e.Target = audit.Target{Kind: "something_new", ID: "x-1"}

	NewRecorder(store).Record(context.Background(), e)

	all := store.all()
	if len(all) != 1 {
		t.Fatalf("recorded %d events, want 1 — an unknown kind lost the whole event", len(all))
	}
	if all[0].ResourceType != "" {
		t.Errorf("resource type = %q, want empty", all[0].ResourceType)
	}
	if all[0].ResourceID != "x-1" {
		t.Errorf("resource id = %q, want it preserved", all[0].ResourceID)
	}
}

// ---------------------------------------------------------------------------
// The failure policy
// ---------------------------------------------------------------------------

// TestRecorder_AStoreFailureDoesNotPanicOrBlock.
//
// The policy is: the business mutation already succeeded, so the response
// succeeds and the failure is logged and counted. `Record` returns no error by
// interface — audit.Recorder cannot fail a request — so what has to be proven
// here is that a failing store is survivable and observable, not silent.
func TestRecorder_AStoreFailureDoesNotPanicOrBlock(t *testing.T) {
	store := &fakeStore{recErr: errors.New("connection pool exhausted")}
	r := NewRecorder(store)

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Record(context.Background(), operatorEvent())
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record blocked on a failing store; it runs on the request path")
	}
}

// TestRecorder_AStoreFailureIsCounted — the metric is what makes the "succeed
// the response" policy acceptable. Without it the trail would be silently
// incomplete, which invites trust it has not earned.
func TestRecorder_AStoreFailureIsCounted(t *testing.T) {
	before := auditFailureCount(t)

	store := &fakeStore{recErr: errors.New("boom")}
	NewRecorder(store).Record(context.Background(), operatorEvent())

	if after := auditFailureCount(t); after <= before {
		t.Errorf("audit persist failures did not increase (%d → %d); the failure is silent",
			before, after)
	}
}

// TestRecorder_NilStoreYieldsNilRecorder — the no-database deployment. The
// composition root relies on this to omit the sink rather than wire one that
// panics on first use.
func TestRecorder_NilStoreYieldsNilRecorder(t *testing.T) {
	if NewRecorder(nil) != nil {
		t.Error("NewRecorder(nil) returned a recorder")
	}
}

// ---------------------------------------------------------------------------
// Secret isolation
// ---------------------------------------------------------------------------

// TestRecorder_NoSecretSurvivesTheMapping is the aggressive check the mission
// asks for.
//
// It builds an event carrying every kind of secret this system handles — in the
// reason text, in the target name, in the extra map — and then flattens the
// ENTIRE resulting record to JSON and searches it. Field-by-field assertions
// would only cover the fields someone thought of; this covers a field added
// later.
func TestRecorder_NoSecretSurvivesTheMapping(t *testing.T) {
	secrets := []string{
		"lw_sk_zzzzsecretzzzzz_qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq",
		"temporary-password-9task",
		"connection-client-secret-value",
		"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.c2ln",
		"Bearer super-secret-token",
		"sha256:deadbeefcafebabe",
	}

	e := operatorEvent()
	// Every field an emitter fills from a runtime value.
	e.Reason = "keycloak returned 400: password " + secrets[1] +
		" rejected for client secret " + secrets[2]
	e.Target = audit.Target{
		Kind: "user",
		ID:   "u-1",
		// Target.Name is the one free-text field set from the request body.
		Name: secrets[1],
	}
	e.Extra = map[string]any{
		"temporary_password": secrets[1],
		"authorization":      secrets[4],
		"key":                secrets[0],
		"scopes":             []string{"users:read"},
	}
	e.UserAgent = "acme/1.0 " + secrets[3]

	store := &fakeStore{}
	NewRecorder(store).Record(context.Background(), e)

	all := store.all()
	if len(all) != 1 {
		t.Fatalf("recorded %d events, want 1", len(all))
	}

	flattened, err := json.Marshal(all[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(flattened), secret) {
			t.Errorf("the durable record contains %q:\n%s", secret, flattened)
		}
	}
}

// TestRecorder_TargetNameIsNeverPersisted, stated as its own property.
//
// It is the only field a call site fills from the request body, and this table
// is readable by anyone holding audit:read. ResourceID identifies the thing;
// the name is display sugar sourced from input.
func TestRecorder_TargetNameIsNeverPersisted(t *testing.T) {
	store := &fakeStore{}
	e := operatorEvent()
	e.Target = audit.Target{Kind: "user", ID: "u-1", Name: "a-value-from-the-request-body"}

	NewRecorder(store).Record(context.Background(), e)

	flattened, _ := json.Marshal(store.all()[0])
	if strings.Contains(string(flattened), "a-value-from-the-request-body") {
		t.Errorf("Target.Name reached the durable record:\n%s", flattened)
	}
}

// TestRecorder_MetadataIsAllowlistedPerEvent.
//
// The scopes on a new credential ARE stored — they are the most useful fact for
// reconstructing what a leaked key could do. Everything else, including the
// same key on a different event, is not.
func TestRecorder_MetadataIsAllowlistedPerEvent(t *testing.T) {
	t.Run("an allowlisted key on its own event survives", func(t *testing.T) {
		store := &fakeStore{}
		e := operatorEvent()
		e.Action = audit.ActionCredentialCreated
		e.Extra = map[string]any{
			"scopes": []string{"users:read", "audit:read"},
			"secret": "should-not-survive",
		}

		NewRecorder(store).Record(context.Background(), e)
		rec := store.all()[0]

		if rec.Metadata["scopes"] == nil {
			t.Error("the allowlisted scopes key was dropped")
		}
		if _, present := rec.Metadata["secret"]; present {
			t.Error("a non-allowlisted key survived")
		}
	})

	t.Run("the same key on a different event does not", func(t *testing.T) {
		store := &fakeStore{}
		e := operatorEvent() // project_credential.revoked, which allowlists nothing
		e.Extra = map[string]any{"scopes": []string{"users:read"}}

		NewRecorder(store).Record(context.Background(), e)

		if rec := store.all()[0]; rec.Metadata != nil {
			t.Errorf("metadata survived on an event with no allowlist: %v", rec.Metadata)
		}
	})

	t.Run("a nested map is refused outright", func(t *testing.T) {
		store := &fakeStore{}
		e := operatorEvent()
		e.Action = audit.ActionCredentialCreated
		e.Extra = map[string]any{"scopes": map[string]any{"nested": "body"}}

		NewRecorder(store).Record(context.Background(), e)

		if rec := store.all()[0]; rec.Metadata != nil {
			t.Errorf("a nested map survived: %v — that is how a whole request body gets in",
				rec.Metadata)
		}
	})
}

// TestReasonCode_NeverEchoesTheErrorText.
//
// audit.Event.Reason is err.Error(), which includes provider responses. The
// stored code is drawn from a closed vocabulary; anything unrecognised becomes
// a marker, never the original string.
func TestReasonCode_NeverEchoesTheErrorText(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{"identityruntime: provider_unavailable", "provider_unavailable"},
		{"role_privileged: cannot grant admin", "role_privileged"},
		{"workspace_mismatch", "workspace_mismatch"},
		{"", ""},
		{"pq: duplicate key value violates unique constraint \"users_email_key\"", reasonCodeUnclassified},
		{"keycloak said: password P@ssw0rd! is too weak", reasonCodeUnclassified},
	}

	for _, tc := range cases {
		got := reasonCodeFor(tc.reason)
		if got != tc.want {
			t.Errorf("reasonCodeFor(%q) = %q, want %q", tc.reason, got, tc.want)
		}
		// Whatever it returns must be a constant, never a fragment of input.
		if got != "" && got != reasonCodeUnclassified && !isKnownReasonCode(got) {
			t.Errorf("reasonCodeFor(%q) returned %q, which is not in the vocabulary", tc.reason, got)
		}
		if strings.Contains(tc.reason, "P@ssw0rd") && strings.Contains(got, "P@ssw0rd") {
			t.Errorf("the reason code echoed the input: %q", got)
		}
	}
}

func isKnownReasonCode(code string) bool {
	for _, known := range knownReasonCodes {
		if known == code {
			return true
		}
	}
	return false
}

// TestMetadata_StringsAreBounded — an allowlisted key whose value is enormous
// must not turn one event into a large row.
func TestMetadata_StringsAreBounded(t *testing.T) {
	huge := strings.Repeat("x", 10_000)
	out := allowlistMetadata("project_credential.created",
		map[string]any{"scopes": []string{huge}})

	values, ok := out["scopes"].([]string)
	if !ok || len(values) == 0 {
		t.Fatalf("scopes did not survive: %#v", out)
	}
	if len(values[0]) > maxMetadataStringLength {
		t.Errorf("stored string is %d bytes, above the %d bound",
			len(values[0]), maxMetadataStringLength)
	}
}

// TestTruncate_CutsOnRuneBoundaries — a column holding half a UTF-8 sequence
// renders as a replacement character everywhere it is displayed.
func TestTruncate_CutsOnRuneBoundaries(t *testing.T) {
	// Each emoji is four bytes, so a naive cut at 10 splits one.
	s := strings.Repeat("🔐", 10)

	got := truncate(s, 10)
	if len(got) > 10 {
		t.Errorf("truncate returned %d bytes for a bound of 10", len(got))
	}
	if !utf8Valid(got) {
		t.Errorf("truncate produced invalid UTF-8: %q", got)
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

// auditFailureCount reads the counter out of the rendered metrics.
//
// Through the public rendering rather than the internal map, so this also
// proves the metric is actually EXPOSED — a counter that increments and is
// never rendered would satisfy an internal assertion and alert nobody.
func auditFailureCount(t *testing.T) int {
	t.Helper()

	var total int
	for _, line := range strings.Split(metricsSnapshot(), "\n") {
		if !strings.HasPrefix(line, "lightweight_audit_persist_failures_total{") {
			continue
		}
		_, value, found := strings.Cut(line, "} ")
		if !found {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(value, "%d", &n); err == nil {
			total += n
		}
	}
	return total
}

func metricsSnapshot() string { return metrics.Default.Render() }
