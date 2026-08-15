package auditlog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// Cursor
// ---------------------------------------------------------------------------

// TestCursor_RoundTrips at microsecond resolution, which is what PostgreSQL
// stores and therefore the precision a cursor has to survive.
func TestCursor_RoundTrips(t *testing.T) {
	original := Cursor{
		OccurredAt: time.Date(2026, 8, 10, 14, 3, 11, 123456000, time.UTC),
		ID:         "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
	}

	decoded, err := decodeCursor(encodeCursor(original))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !decoded.OccurredAt.Equal(original.OccurredAt) {
		t.Errorf("timestamp = %v, want %v", decoded.OccurredAt, original.OccurredAt)
	}
	if decoded.ID != original.ID {
		t.Errorf("id = %q, want %q", decoded.ID, original.ID)
	}
}

// TestCursor_IsOpaqueButNotSecret — base64url, so it survives a query string
// without escaping and a client cannot read a format we intend to change.
func TestCursor_IsOpaqueButNotSecret(t *testing.T) {
	encoded := encodeCursor(Cursor{OccurredAt: time.Now(), ID: "abc"})

	for _, ch := range []string{"+", "/", "=", " "} {
		if strings.Contains(encoded, ch) {
			t.Errorf("cursor %q contains %q, which needs URL escaping", encoded, ch)
		}
	}
}

func TestCursor_RejectsGarbage(t *testing.T) {
	for _, bad := range []string{
		"not-base64!!!",
		"",
		encodeStringForTest("no-separator"),
		encodeStringForTest("notanumber.abc"),
		encodeStringForTest("123."), // empty id
		encodeStringForTest("123." + strings.Repeat("x", 200)), // unbounded id
	} {
		if _, err := decodeCursor(bad); err == nil {
			t.Errorf("decodeCursor(%q) succeeded, want an error", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func TestService_LimitIsRefusedNotClamped(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store)

	t.Run("absent uses the default", func(t *testing.T) {
		if _, err := svc.List(context.Background(), testWorkspaceUUID, ListParams{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := store.query().Limit; got != defaultPageSize {
			t.Errorf("limit = %d, want the default %d", got, defaultPageSize)
		}
	})

	// Out of range is REFUSED. Silently answering `limit=100000` with 200 makes
	// a caller believe it has the whole history when it has a page — and that
	// belief is the bug, because it stops paginating.
	for _, bad := range []string{"0", "-1", "201", "100000", "abc", "50.5"} {
		t.Run("limit="+bad, func(t *testing.T) {
			_, err := svc.List(context.Background(), testWorkspaceUUID, ListParams{Limit: bad})
			assertFieldError(t, err, "limit")
		})
	}
}

func TestService_FiltersAreValidated(t *testing.T) {
	svc := NewService(&fakeStore{})

	cases := []struct {
		name  string
		p     ListParams
		field string
	}{
		{"unknown actor type", ListParams{ActorType: "robot"}, "actor_type"},
		{"unknown outcome", ListParams{Outcome: "maybe"}, "outcome"},
		{"unparseable from", ListParams{From: "yesterday"}, "from"},
		{"unparseable to", ListParams{To: "2026-13-45"}, "to"},
		{"inverted range", ListParams{From: "2026-08-10T00:00:00Z", To: "2026-08-01T00:00:00Z"}, "from"},
		{"malformed cursor", ListParams{Cursor: "not-a-cursor!!"}, "cursor"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.List(context.Background(), testWorkspaceUUID, tc.p)
			assertFieldError(t, err, tc.field)
		})
	}
}

// TestService_AnUnknownEventTypeIsAnEmptyPageNotAnError.
//
// The vocabulary grows every slice. An operator filtering for an event a newer
// version emits should see nothing, which is the truthful answer for THIS
// installation — a 400 would say the filter is malformed when it is merely
// unsatisfied.
func TestService_AnUnknownEventTypeIsAnEmptyPageNotAnError(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store)

	if _, err := svc.List(context.Background(), testWorkspaceUUID,
		ListParams{EventType: "something.from.the.future"}); err != nil {
		t.Fatalf("an unknown event type was rejected: %v", err)
	}
	if store.query().EventType != "something.from.the.future" {
		t.Error("the filter was not passed through")
	}
}

// TestService_TheWorkspaceCannotBeInfluencedByAQueryParameter.
//
// The boundary is the shape of the call: ListParams has no workspace field, so
// there is nothing a query string could bind into. This asserts the resulting
// Query carries the workspace the CALLER was authorized for and nothing else
// can change it.
func TestService_TheWorkspaceCannotBeInfluencedByAQueryParameter(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store)

	_, err := svc.List(context.Background(), testWorkspaceUUID, ListParams{
		EventType: "user.created",
		ActorType: "project",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := store.query().WorkspaceID; got != testWorkspaceUUID {
		t.Errorf("query workspace = %q, want the authorized %q", got, testWorkspaceUUID)
	}
}

// ---------------------------------------------------------------------------
// Retention
// ---------------------------------------------------------------------------

// recordingPurger captures the cutoff a purge computed.
type recordingPurger struct {
	fakeStore
	cutoff time.Time
	called bool
}

func (r *recordingPurger) DeleteOlderThan(_ context.Context, cutoff time.Time) (int64, error) {
	r.cutoff = cutoff
	r.called = true
	return 3, nil
}

func TestService_PurgeComputesTheCutoffFromTheWindow(t *testing.T) {
	store := &recordingPurger{}
	svc := NewService(store)

	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	deleted, err := svc.Purge(context.Background(), 90*24*time.Hour)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted != 3 {
		t.Errorf("deleted = %d, want 3", deleted)
	}

	want := now.Add(-90 * 24 * time.Hour)
	if !store.cutoff.Equal(want) {
		t.Errorf("cutoff = %v, want %v", store.cutoff, want)
	}
}

// TestService_PurgeRefusesANonPositiveWindow.
//
// A zero window means "delete everything". Config validation rejects it; this
// is the second line, inside the function that does the deleting, because the
// cost of getting it wrong is the entire trail.
func TestService_PurgeRefusesANonPositiveWindow(t *testing.T) {
	for _, window := range []time.Duration{0, -time.Hour} {
		store := &recordingPurger{}
		svc := NewService(store)

		deleted, err := svc.Purge(context.Background(), window)
		if err != nil {
			t.Errorf("purge(%v) errored: %v", window, err)
		}
		if deleted != 0 {
			t.Errorf("purge(%v) deleted %d", window, deleted)
		}
		if store.called {
			t.Errorf("purge(%v) reached the store — a zero window would delete everything", window)
		}
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func auditRouter(t *testing.T, store Store) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewHandler(NewService(store))
	r.GET("/v1/workspaces/:workspace_id/audit", h.List)
	return r
}

func getAudit(r *gin.Engine, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestHandler_RendersAPage(t *testing.T) {
	store := &fakeStore{page: Page{
		Items: []Record{{
			ID:                "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
			EventType:         "project_credential.revoked",
			Outcome:           OutcomeSuccess,
			ActorType:         audit.ActorProject,
			ActorProjectID:    testProjectID,
			ActorCredentialID: testCredentialID,
			ResourceType:      ResourceCredential,
			ResourceID:        testCredentialID,
			RequestID:         "req-1",
			OccurredAt:        time.Now().UTC(),
		}},
	}}

	w := getAudit(auditRouter(t, store), "/v1/workspaces/"+testWorkspacePub+"/audit")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}

	var body ListEventsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(body.Items))
	}

	item := body.Items[0]
	if !strings.HasPrefix(item.ID, "evt_") {
		t.Errorf("id = %q, want the evt_ public form", item.ID)
	}
	if item.Actor.Type != "project" || item.Actor.ProjectID != testProjectID {
		t.Errorf("actor = %+v", item.Actor)
	}
	if item.Actor.Subject != "" {
		t.Errorf("a project event rendered a subject: %q", item.Actor.Subject)
	}
	if body.Pagination.Count != 1 || body.Pagination.Limit != defaultPageSize {
		t.Errorf("pagination = %+v", body.Pagination)
	}
	if body.Pagination.NextCursor != "" {
		t.Error("a last page advertised a next cursor")
	}
}

// TestHandler_NeverRendersTheSourceIP.
//
// The column exists for an operator with database access; the API is read by
// any credential holding audit:read, and an operator's network location is a
// disclosure nobody asked for.
func TestHandler_NeverRendersTheSourceIP(t *testing.T) {
	store := &fakeStore{page: Page{Items: []Record{{
		ID: "3f2504e0-4f89-41d3-9a0c-0305e82c3301", EventType: "user.created",
		Outcome: OutcomeSuccess, ActorType: audit.ActorOperator,
		ActorSubject: "op-1", SourceIP: "198.51.100.77",
		OccurredAt: time.Now(),
	}}}}

	w := getAudit(auditRouter(t, store), "/v1/workspaces/"+testWorkspacePub+"/audit")
	if strings.Contains(w.Body.String(), "198.51.100.77") {
		t.Errorf("the response exposed the source IP:\n%s", w.Body.String())
	}
}

func TestHandler_MalformedWorkspaceID(t *testing.T) {
	w := getAudit(auditRouter(t, &fakeStore{}), "/v1/workspaces/nope/audit")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if code := errorCodeOf(t, w); code != "invalid_workspace_id" {
		t.Errorf("code = %q", code)
	}
}

// TestHandler_AStoreFailureIsA503AndLeaksNothing.
//
// 503 rather than 500: the request was well-formed and the failure is
// infrastructure. The driver's message — which can name a host, a user or a
// constraint — must not reach a caller that may be a project credential.
func TestHandler_AStoreFailureIsA503AndLeaksNothing(t *testing.T) {
	store := &fakeStore{listErr: errors.New(
		"pq: password authentication failed for user \"saas\" on host db.internal")}

	w := getAudit(auditRouter(t, store), "/v1/workspaces/"+testWorkspacePub+"/audit")

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", w.Code, w.Body.String())
	}
	if code := errorCodeOf(t, w); code != "audit_unavailable" {
		t.Errorf("code = %q", code)
	}
	for _, leak := range []string{"password", "db.internal", "saas", "pq:"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("the 503 body leaked %q:\n%s", leak, w.Body.String())
		}
	}
}

// TestHandler_InvalidFilterNamesTheField — the Slice 9 `field` contract applied
// here, so a caller that sent a bad cursor and one that sent a bad limit can
// tell which to fix.
func TestHandler_InvalidFilterNamesTheField(t *testing.T) {
	cases := map[string]string{
		"?limit=99999":      "limit",
		"?cursor=%21%21":    "cursor",
		"?outcome=perhaps":  "outcome",
		"?actor_type=robot": "actor_type",
		"?from=not-a-date":  "from",
	}

	r := auditRouter(t, &fakeStore{})
	for query, wantField := range cases {
		t.Run(query, func(t *testing.T) {
			w := getAudit(r, "/v1/workspaces/"+testWorkspacePub+"/audit"+query)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body.String())
			}

			var body ErrorResponse
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if body.Error.Field != wantField {
				t.Errorf("field = %q, want %q", body.Error.Field, wantField)
			}
		})
	}
}

func TestHandler_NextCursorAppearsOnlyWhenThereIsMore(t *testing.T) {
	at := time.Now().UTC()
	store := &fakeStore{page: Page{
		Items:      []Record{{ID: "id-1", EventType: "user.created", ActorType: audit.ActorOperator, OccurredAt: at}},
		NextCursor: &Cursor{OccurredAt: at, ID: "id-1"},
	}}

	w := getAudit(auditRouter(t, store), "/v1/workspaces/"+testWorkspacePub+"/audit")

	var body ListEventsResponse
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Pagination.NextCursor == "" {
		t.Fatal("a page with more history advertised no cursor")
	}

	// And it must be usable: decoding it has to give back the position.
	decoded, err := decodeCursor(body.Pagination.NextCursor)
	if err != nil {
		t.Fatalf("the advertised cursor does not decode: %v", err)
	}
	if decoded.ID != "id-1" {
		t.Errorf("cursor id = %q, want id-1", decoded.ID)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// encodeStringForTest base64url-encodes a raw cursor payload, so the rejection
// cases exercise the DECODER rather than base64.
func encodeStringForTest(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func errorsAs(err error, target **Error) bool { return errors.As(err, target) }

func assertFieldError(t *testing.T, err error, wantField string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error naming %q, got nil", wantField)
	}
	var domainErr *Error
	if !errorsAs(err, &domainErr) {
		t.Fatalf("error is not a domain error: %v", err)
	}
	if domainErr.Code != "invalid_request" {
		t.Errorf("code = %q, want invalid_request", domainErr.Code)
	}
	if domainErr.Field != wantField {
		t.Errorf("field = %q, want %q", domainErr.Field, wantField)
	}
}

func errorCodeOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not the /v1 envelope: %s", w.Body.String())
	}
	return body.Error.Code
}
