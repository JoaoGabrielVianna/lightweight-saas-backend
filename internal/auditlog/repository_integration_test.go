//go:build integration

package auditlog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// What only a real PostgreSQL can settle.
//
// The unit tests cover mapping, redaction and validation against a fake store.
// Everything here needs the database itself: the CHECK constraints that make
// the actor model a guarantee rather than a convention, cursor pagination under
// genuine concurrent inserts, the retention boundary, and the query plan.
//
// Each test gets a private schema the real migrations are applied into, so the
// schema under test is the one a deploy produces.

func newTestSchema(t *testing.T) (*gorm.DB, string) {
	t.Helper()

	base := os.Getenv("DB_URL")
	if base == "" {
		t.Skip("DB_URL unset — integration test requires a reachable postgres")
	}

	schema := schemaNameFor(t)
	admin := openGorm(t, base)
	if err := admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`).Error; err != nil {
		t.Fatalf("drop stale schema: %v", err)
	}
	if err := admin.Exec(`CREATE SCHEMA ` + schema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanup := openGorm(t, base)
		_ = cleanup.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`).Error
	})

	dsn := withSearchPath(t, base, schema)
	if err := database.Migrate(dsn); err != nil {
		t.Fatalf("apply migrations to %s: %v", schema, err)
	}
	return openGorm(t, dsn), schema
}

func openGorm(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:         gormlogger.Default.LogMode(gormlogger.Silent),
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("open gorm: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func schemaNameFor(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("aud_")
	for _, r := range strings.ToLower(t.Name()) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func withSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DB_URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

// seedWorkspace creates a real workspace row, because audit_events.workspace_id
// is a foreign key and a fabricated UUID would fail the constraint.
func seedWorkspace(t *testing.T, db *gorm.DB, slug string) string {
	t.Helper()

	id, err := publicid.New()
	if err != nil {
		t.Fatalf("generate id: %v", err)
	}
	now := time.Now().UTC()
	ws := &workspace.Workspace{
		ID:        id,
		Name:      slug,
		Slug:      slug,
		Status:    "active",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := workspace.NewRepository(db).Create(context.Background(), ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return id
}

// event builds a valid operator record for a workspace.
func event(workspaceUUID, eventType string, at time.Time) Record {
	id, _ := publicid.New()
	return Record{
		ID:           id,
		WorkspaceID:  workspaceUUID,
		EventType:    eventType,
		Outcome:      OutcomeSuccess,
		ActorType:    audit.ActorOperator,
		ActorSubject: "9c1e6679-7425-40de-944b-e07fc1f90ae7",
		ActorEmail:   "ada@example.com",
		ResourceType: ResourceUser,
		ResourceID:   "u-1",
		RequestID:    "req-" + eventType,
		OccurredAt:   at.UTC(),
	}
}

// ---------------------------------------------------------------------------
// The schema's own guarantees
// ---------------------------------------------------------------------------

// TestIntegration_RoundTrip — every field survives a write and a read.
func TestIntegration_RoundTrip(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db, "round-trip")
	ctx := context.Background()

	rec := event(ws, "project_credential.created", time.Now())
	rec.ActorType = audit.ActorProject
	rec.ActorSubject, rec.ActorEmail = "", ""
	rec.ActorProjectID, rec.ActorCredentialID = testProjectID, testCredentialID
	rec.ResourceType, rec.ResourceID = ResourceCredential, testCredentialID
	rec.SourceIP = "198.51.100.7"
	rec.Metadata = map[string]any{"scopes": []string{"users:read", "audit:read"}}

	if err := repo.Record(ctx, rec); err != nil {
		t.Fatalf("record: %v", err)
	}

	page, err := repo.List(ctx, Query{WorkspaceID: ws, Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(page.Items))
	}

	got := page.Items[0]
	if got.ID != rec.ID || got.EventType != rec.EventType {
		t.Errorf("identity did not round trip: %+v", got)
	}
	if got.ActorProjectID != testProjectID || got.ActorCredentialID != testCredentialID {
		t.Errorf("project actor did not round trip: %+v", got)
	}
	if got.ActorSubject != "" {
		t.Errorf("actor_subject came back %q for a project row", got.ActorSubject)
	}
	if got.Metadata == nil {
		t.Error("metadata did not round trip")
	}
	if !got.OccurredAt.Equal(rec.OccurredAt.Truncate(time.Microsecond)) &&
		got.OccurredAt.Sub(rec.OccurredAt).Abs() > time.Microsecond {
		t.Errorf("occurred_at = %v, want ≈ %v", got.OccurredAt, rec.OccurredAt)
	}
}

// TestIntegration_TheActorShapeConstraintIsEnforced.
//
// The disjointness of operator and project actors is a database CHECK, not a
// convention in the mapper. This proves the database refuses the mixed shape —
// which is what makes "a project id never appears in actor_subject" a guarantee
// about the DATA rather than about whichever code path happened to write it.
func TestIntegration_TheActorShapeConstraintIsEnforced(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db, "actor-shape")
	ctx := context.Background()

	cases := []struct {
		name   string
		mutate func(*Record)
	}{
		{"a project id in the subject", func(r *Record) {
			r.ActorType = audit.ActorProject
			r.ActorSubject = testProjectID
			r.ActorProjectID = testProjectID
		}},
		{"an operator carrying project fields", func(r *Record) {
			r.ActorType = audit.ActorOperator
			r.ActorProjectID = testProjectID
		}},
		{"a project with no project id", func(r *Record) {
			r.ActorType = audit.ActorProject
			r.ActorSubject, r.ActorEmail = "", ""
			r.ActorProjectID = ""
		}},
		{"an unknown actor type", func(r *Record) {
			r.ActorType = "robot"
		}},
		{"an unknown outcome", func(r *Record) {
			r.Outcome = "maybe"
		}},
		{"an unknown resource type", func(r *Record) {
			r.ResourceType = "spaceship"
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := event(ws, "user.created", time.Now())
			tc.mutate(&rec)

			if err := repo.Record(ctx, rec); err == nil {
				t.Error("the database accepted a row the constraints should refuse")
			}
		})
	}
}

// TestIntegration_ResourceTypesMatchTheDatabaseConstraint — the Go vocabulary
// and the CHECK are two enforcement points for one contract, and this proves
// every Go value is actually accepted.
func TestIntegration_ResourceTypesMatchTheDatabaseConstraint(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db, "resource-types")
	ctx := context.Background()

	for _, rt := range AllResourceTypes() {
		rec := event(ws, "user.created", time.Now())
		rec.ResourceType = rt
		if err := repo.Record(ctx, rec); err != nil {
			t.Errorf("the database refused resource type %q, which Go defines: %v", rt, err)
		}
	}
}

// TestIntegration_AuditReadIsAcceptedByTheScopeConstraint — migration 000006
// widened project_credentials_scopes_known, and a credential granted the new
// scope has to be storable or the whole feature is unreachable.
func TestIntegration_AuditReadIsAcceptedByTheScopeConstraint(t *testing.T) {
	db, _ := newTestSchema(t)

	// Inserted through the constraint the migration actually installed, rather
	// than probed with a standalone expression: what matters is that a real
	// credential row carrying the scope is storable.
	ws := seedWorkspace(t, db, "scope-widening")
	var projectID string
	if err := db.Raw(`INSERT INTO projects (id, workspace_id, name, status)
		VALUES (gen_random_uuid(), ?, 'audit-reader', 'active') RETURNING id`, ws).
		Scan(&projectID).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}

	err := db.Exec(`INSERT INTO project_credentials
		(id, project_id, label, key_prefix, key_hash, scopes, created_by)
		VALUES (gen_random_uuid(), ?, 'reader', 'abcdefghijklmnop',
		        sha256('probe'::bytea),
		        ARRAY['audit:read']::text[], 'op')`, projectID).Error
	if err != nil {
		t.Fatalf("the widened scope constraint refused 'audit:read': %v", err)
	}
}

// ---------------------------------------------------------------------------
// Workspace isolation
// ---------------------------------------------------------------------------

// TestIntegration_ListNeverCrossesTheWorkspaceBoundary.
//
// The last line of the boundary, at the level that actually issues the query. A
// bug in the handler's authorization would be caught upstream; a bug HERE would
// let an authorized caller see another tenant's history.
func TestIntegration_ListNeverCrossesTheWorkspaceBoundary(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ctx := context.Background()

	wsA := seedWorkspace(t, db, "tenant-a")
	wsB := seedWorkspace(t, db, "tenant-b")

	for i := 0; i < 5; i++ {
		if err := repo.Record(ctx, event(wsA, "user.created", time.Now())); err != nil {
			t.Fatalf("record A: %v", err)
		}
		if err := repo.Record(ctx, event(wsB, "user.deleted", time.Now())); err != nil {
			t.Fatalf("record B: %v", err)
		}
	}
	// A global event, which no workspace query may return.
	global := event("", "user.created", time.Now())
	global.WorkspaceID = ""
	if err := repo.Record(ctx, global); err != nil {
		t.Fatalf("record global: %v", err)
	}

	page, err := repo.List(ctx, Query{WorkspaceID: wsA, Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 5 {
		t.Fatalf("workspace A sees %d events, want exactly its own 5", len(page.Items))
	}
	for _, item := range page.Items {
		if item.WorkspaceID != wsA {
			t.Errorf("workspace A's page contains an event for %q — CROSS-TENANT LEAK",
				item.WorkspaceID)
		}
		if item.EventType != "user.created" {
			t.Errorf("unexpected event %q in A's page", item.EventType)
		}
	}
}

// TestIntegration_AnEmptyWorkspaceReturnsNothingNotEverything.
//
// The repository's own guard. A future caller that forgets to set the workspace
// must get an empty page, never the whole installation's history.
func TestIntegration_AnEmptyWorkspaceReturnsNothingNotEverything(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ctx := context.Background()

	ws := seedWorkspace(t, db, "guard")
	for i := 0; i < 3; i++ {
		_ = repo.Record(ctx, event(ws, "user.created", time.Now()))
	}

	page, err := repo.List(ctx, Query{WorkspaceID: "", Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("a query with no workspace returned %d events", len(page.Items))
	}
}

// ---------------------------------------------------------------------------
// Cursor pagination
// ---------------------------------------------------------------------------

// TestIntegration_CursorPaginationHasNoGapsOrDuplicates.
//
// Walked to exhaustion over events that SHARE timestamps, which is the case a
// timestamp-only cursor gets wrong: it either skips the ties or repeats them.
func TestIntegration_CursorPaginationHasNoGapsOrDuplicates(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db, "pagination")
	ctx := context.Background()

	// 47 events over 5 distinct timestamps, so most share one with a neighbour.
	const total = 47
	base := time.Now().UTC().Add(-time.Hour)
	written := map[string]bool{}
	for i := 0; i < total; i++ {
		rec := event(ws, "user.created", base.Add(time.Duration(i%5)*time.Second))
		if err := repo.Record(ctx, rec); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		written[rec.ID] = true
	}

	seen := map[string]bool{}
	var cursor *Cursor
	pages := 0

	for {
		page, err := repo.List(ctx, Query{WorkspaceID: ws, Limit: 10, After: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		pages++
		if pages > 20 {
			t.Fatal("pagination did not terminate")
		}

		for _, item := range page.Items {
			if seen[item.ID] {
				t.Errorf("event %s appeared on two pages", item.ID)
			}
			seen[item.ID] = true
		}
		if page.NextCursor == nil {
			break
		}
		cursor = page.NextCursor
	}

	if len(seen) != total {
		t.Errorf("walked %d events over %d pages, want all %d — the walk skipped some",
			len(seen), pages, total)
	}
	for id := range written {
		if !seen[id] {
			t.Errorf("event %s was never returned", id)
		}
	}
}

// TestIntegration_OrderingIsNewestFirstAndDeterministic.
func TestIntegration_OrderingIsNewestFirstAndDeterministic(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db, "ordering")
	ctx := context.Background()

	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 20; i++ {
		// Every event shares one timestamp, so ordering is decided entirely by
		// the id tiebreak — which is the property that has to be deterministic.
		_ = repo.Record(ctx, event(ws, "user.created", base))
	}

	first, err := repo.List(ctx, Query{WorkspaceID: ws, Limit: 20})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	second, err := repo.List(ctx, Query{WorkspaceID: ws, Limit: 20})
	if err != nil {
		t.Fatalf("list again: %v", err)
	}

	for i := range first.Items {
		if first.Items[i].ID != second.Items[i].ID {
			t.Fatalf("two identical queries returned different orders at position %d", i)
		}
		if i > 0 && first.Items[i-1].ID < first.Items[i].ID {
			t.Errorf("ordering is not descending by id at position %d", i)
		}
	}
}

// TestIntegration_NextCursorIsAbsentOnTheExactBoundary.
//
// The off-by-one a `len(items) == limit` implementation gets wrong: with
// exactly `limit` events, a final page must NOT advertise another.
func TestIntegration_NextCursorIsAbsentOnTheExactBoundary(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db, "boundary")
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		_ = repo.Record(ctx, event(ws, "user.created", time.Now().Add(time.Duration(i)*time.Second)))
	}

	page, err := repo.List(ctx, Query{WorkspaceID: ws, Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 10 {
		t.Fatalf("items = %d, want 10", len(page.Items))
	}
	if page.NextCursor != nil {
		t.Error("a page holding every remaining event advertised another page")
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

// TestIntegration_ConcurrentEventsAreAllDurableAndUnique.
//
// Twenty simultaneous mutations in one workspace, as a burst of parallel
// requests would produce. All twenty must be stored, with distinct ids, and
// pagination over them must still be exhaustive.
func TestIntegration_ConcurrentEventsAreAllDurableAndUnique(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db, "concurrent")
	ctx := context.Background()

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := repo.Record(ctx, event(ws, "user.created", time.Now())); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent record failed: %v", err)
	}

	// Walk every page: the count has to come from the pagination the API uses,
	// not from a COUNT(*) that pagination might not agree with.
	seen := map[string]bool{}
	var cursor *Cursor
	for {
		page, err := repo.List(ctx, Query{WorkspaceID: ws, Limit: 7, After: cursor})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Errorf("duplicate id %s", item.ID)
			}
			seen[item.ID] = true
		}
		if page.NextCursor == nil {
			break
		}
		cursor = page.NextCursor
	}

	if len(seen) != n {
		t.Errorf("stored %d of %d concurrent events", len(seen), n)
	}
}

// ---------------------------------------------------------------------------
// Retention
// ---------------------------------------------------------------------------

// TestIntegration_RetentionDeletesStrictlyOlderThanTheCutoff.
//
// The boundary is tested exactly. `<` versus `<=` is invisible for every input
// except the one on the cutoff, and a test that only used "old" and "new" would
// pass against either.
func TestIntegration_RetentionDeletesStrictlyOlderThanTheCutoff(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db, "retention")
	ctx := context.Background()

	cutoff := time.Now().UTC().Add(-90 * 24 * time.Hour).Truncate(time.Microsecond)

	older := event(ws, "user.created", cutoff.Add(-time.Second))
	exact := event(ws, "user.updated", cutoff)
	newer := event(ws, "user.deleted", cutoff.Add(time.Second))
	for _, rec := range []Record{older, exact, newer} {
		if err := repo.Record(ctx, rec); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	deleted, err := repo.DeleteOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted %d events, want exactly the one strictly older", deleted)
	}

	page, err := repo.List(ctx, Query{WorkspaceID: ws, Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	survivors := map[string]bool{}
	for _, item := range page.Items {
		survivors[item.EventType] = true
	}
	if survivors["user.created"] {
		t.Error("the event older than the cutoff survived")
	}
	if !survivors["user.updated"] {
		t.Error("the event exactly ON the cutoff was deleted; the boundary is inclusive-of-newer")
	}
	if !survivors["user.deleted"] {
		t.Error("the event newer than the cutoff was deleted")
	}
}

// ---------------------------------------------------------------------------
// Filters
// ---------------------------------------------------------------------------

func TestIntegration_Filters(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db, "filters")
	ctx := context.Background()

	base := time.Now().UTC().Add(-24 * time.Hour)

	mk := func(eventType string, outcome Outcome, actor audit.ActorType, at time.Time) {
		rec := event(ws, eventType, at)
		rec.Outcome = outcome
		if actor == audit.ActorProject {
			rec.ActorType = audit.ActorProject
			rec.ActorSubject, rec.ActorEmail = "", ""
			rec.ActorProjectID, rec.ActorCredentialID = testProjectID, testCredentialID
		}
		if outcome == OutcomeFailure {
			rec.ReasonCode = "provider_unavailable"
		}
		if err := repo.Record(ctx, rec); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	mk("user.created", OutcomeSuccess, audit.ActorOperator, base)
	mk("user.created", OutcomeFailure, audit.ActorProject, base.Add(time.Hour))
	mk("user.deleted", OutcomeSuccess, audit.ActorProject, base.Add(2*time.Hour))
	mk("role.created", OutcomeSuccess, audit.ActorOperator, base.Add(3*time.Hour))

	cases := []struct {
		name  string
		query Query
		want  int
	}{
		{"no filter", Query{WorkspaceID: ws}, 4},
		{"by event", Query{WorkspaceID: ws, EventType: "user.created"}, 2},
		{"by actor", Query{WorkspaceID: ws, ActorType: audit.ActorProject}, 2},
		{"by outcome", Query{WorkspaceID: ws, Outcome: OutcomeFailure}, 1},
		{"by time range", Query{WorkspaceID: ws,
			From: base.Add(30 * time.Minute), To: base.Add(150 * time.Minute)}, 2},
		{"combined", Query{WorkspaceID: ws,
			EventType: "user.created", ActorType: audit.ActorProject}, 1},
		{"matching nothing", Query{WorkspaceID: ws, EventType: "nope.nothing"}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.query.Limit = 100
			page, err := repo.List(ctx, tc.query)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(page.Items) != tc.want {
				t.Errorf("got %d events, want %d", len(page.Items), tc.want)
			}
			// Every filter narrows WITHIN the workspace; none may escape it.
			for _, item := range page.Items {
				if item.WorkspaceID != ws {
					t.Errorf("a filter returned an event from workspace %q", item.WorkspaceID)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Secret isolation, against the real table
// ---------------------------------------------------------------------------

// TestIntegration_NoSecretReachesTheTable.
//
// The unit test proves the MAPPER drops secrets. This proves the same thing
// about what is actually on disk, by writing an event through the full emission
// path and then reading the entire row back as JSON — every column, including
// any added later — and searching it.
func TestIntegration_NoSecretReachesTheTable(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db, "secrets")
	ctx := context.Background()

	secrets := []string{
		"lw_sk_zzzzsecretzzzzz_qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq",
		"temporary-password-9task",
		"connection-client-secret-value",
		"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.c2ln",
	}

	// Emitted the way a handler emits, so the recorder's redaction is what is
	// under test rather than a hand-built Record.
	recorder := NewRecorder(repo)
	recorder.Record(ctx, audit.Event{
		Action:    audit.ActionCredentialCreated,
		Actor:     audit.Actor{Type: audit.ActorOperator, Subject: "op-1", Email: "ada@example.com"},
		Target:    audit.Target{Kind: "project_credential", ID: testCredentialID, Name: secrets[1]},
		Workspace: publicid.Format(publicid.WorkspacePrefix, ws),
		Reason:    "keycloak refused: secret " + secrets[2] + " invalid",
		Extra: map[string]any{
			"scopes":             []string{"users:read"},
			"temporary_password": secrets[1],
			"token":              secrets[3],
			"key":                secrets[0],
		},
		IP:        "198.51.100.7",
		UserAgent: "acme/1.0 " + secrets[3],
		Timestamp: time.Now(),
	})

	// Read every column of every row as JSON — not the mapped struct, which
	// could omit a column that a future migration adds.
	var rows []string
	if err := db.Raw(`SELECT row_to_json(a)::text FROM audit_events a`).Scan(&rows).Error; err != nil {
		t.Fatalf("dump rows: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no rows were written; the test cannot prove anything")
	}

	dump := strings.Join(rows, "\n")
	for _, secret := range secrets {
		if strings.Contains(dump, secret) {
			t.Errorf("the audit_events table contains %q:\n%s", secret, dump)
		}
	}
	// And the free-text reason must have become a code.
	if strings.Contains(dump, "keycloak refused") {
		t.Errorf("the upstream error text was persisted:\n%s", dump)
	}
}

// ---------------------------------------------------------------------------
// Query plan
// ---------------------------------------------------------------------------

// TestIntegration_ListUsesTheCompositeIndex is the query-plan evidence.
//
// It asserts the two properties that decide whether this scales — the index is
// used, and there is no sort — rather than a cost number, which would be a
// brittle test of the planner's mood on the day.
//
// The plan is printed either way, so a run of the suite is also the report.
func TestIntegration_ListUsesTheCompositeIndex(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ctx := context.Background()

	// MANY workspaces, not two.
	//
	// This is the shape the composite index exists for, and getting it wrong is
	// how a plan test passes while proving nothing. With two tenants, scanning
	// a time-ordered index and filtering by workspace discards half the rows —
	// cheap enough that the planner may prefer it, and the test would then
	// "pass" against an index choice that degrades linearly with tenant count.
	//
	// With twenty, filling a 50-row page from a time-only index means touching
	// roughly a thousand rows, and the correct choice becomes unambiguous —
	// which is exactly the point at which this measurement is worth making.
	const (
		workspaces   = 20
		perWorkspace = 500
	)
	base := time.Now().UTC().Add(-30 * 24 * time.Hour)

	ids := make([]string, 0, workspaces)
	for w := 0; w < workspaces; w++ {
		ids = append(ids, seedWorkspace(t, db, fmt.Sprintf("plan-%02d", w)))
	}
	wsA := ids[0]

	for i := 0; i < perWorkspace; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		for _, ws := range ids {
			if err := repo.Record(ctx, event(ws, "user.created", at)); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
	}
	if err := db.Exec(`ANALYZE audit_events`).Error; err != nil {
		t.Fatalf("analyze: %v", err)
	}

	cursorTime := base.Add(400 * time.Minute)

	// EXPLAIN the query the REPOSITORY builds, not a hand-written copy of it.
	//
	// The first version of this test wrote the SQL out by hand and therefore
	// kept reporting a plan for a predicate the repository had already stopped
	// using — a plan test measuring a query no code path issues is worse than
	// none, because it produces evidence for the wrong thing. gorm's DryRun
	// renders the real statement and its arguments.
	sql, args := explainableListQuery(t, db, Query{
		WorkspaceID: wsA,
		Limit:       50,
		After:       &Cursor{OccurredAt: cursorTime, ID: "ffffffff-ffff-4fff-8fff-ffffffffffff"},
	})
	t.Logf("the query under test:\n%s\n%v", sql, args)

	var lines []string
	if err := db.Raw("EXPLAIN (ANALYZE, BUFFERS) "+sql, args...).Scan(&lines).Error; err != nil {
		t.Fatalf("explain: %v", err)
	}

	plan := strings.Join(lines, "\n")
	t.Logf("query plan for the main listing (%d rows across %d workspaces):\n%s",
		perWorkspace*workspaces, workspaces, plan)

	if !strings.Contains(plan, "audit_events_workspace_time_idx") {
		t.Errorf("the main listing query does not use the composite index:\n%s", plan)
	}
	// A Sort node would mean the index order and the ORDER BY disagree, which
	// turns a bounded page into a sort of the whole workspace's history.
	if strings.Contains(plan, "Sort Method") {
		t.Errorf("the plan contains a sort; the index does not match the ordering:\n%s", plan)
	}
	// The whole point of the index is that the work is bounded by the PAGE, not
	// by the history or by the number of tenants.
	if rows := scannedRows(plan); rows > 100 {
		t.Errorf("the plan touches ~%d rows to return a 50-row page:\n%s", rows, plan)
	}
}

// explainableListQuery renders the repository's real listing statement.
func explainableListQuery(t *testing.T, db *gorm.DB, q Query) (string, []any) {
	t.Helper()

	dry := db.Session(&gorm.Session{DryRun: true})
	stmt := listQuery(dry, q).
		Order("occurred_at DESC, id DESC").
		Limit(q.Limit + 1).
		Find(&[]auditEventRow{}).Statement

	if stmt.SQL.Len() == 0 {
		t.Fatal("gorm produced no SQL for the listing query")
	}
	return stmt.SQL.String(), stmt.Vars
}

// scannedRows extracts the actual row count from the top plan node.
func scannedRows(plan string) int {
	for _, line := range strings.Split(plan, "\n") {
		if !strings.Contains(line, "actual") {
			continue
		}
		idx := strings.Index(line, "rows=")
		if idx < 0 {
			continue
		}
		rest := line[idx+len("rows="):]
		var n int
		if _, err := fmt.Sscanf(rest, "%d", &n); err == nil {
			return n
		}
	}
	return 0
}

// TestIntegration_RetentionSweepUsesItsIndex.
//
// Measured on a dataset where the index choice MATTERS. On a few thousand rows
// a sequential scan is genuinely cheaper and the planner is right to take it —
// asserting BRIN there would be asserting a preference, not a property. What
// has to hold is that the sweep stays sub-linear on the table it will actually
// run against, which is one that has been accumulating for a retention window.
//
// Seeded with a single bulk INSERT rather than 40,000 round trips: this test is
// about the plan, and the seeding is not the thing under test.
func TestIntegration_RetentionSweepUsesItsIndex(t *testing.T) {
	db, _ := newTestSchema(t)
	ws := seedWorkspace(t, db, "sweep-plan")

	const rows = 40000
	if err := db.Exec(`
		INSERT INTO audit_events
			(id, workspace_id, event_type, outcome, actor_type, actor_subject, occurred_at)
		SELECT gen_random_uuid(), ?, 'user.created', 'success', 'operator', 'op',
		       now() - ((?::int - i) || ' minutes')::interval
		FROM generate_series(1, ?) i`, ws, rows, rows).Error; err != nil {
		t.Fatalf("bulk seed: %v", err)
	}
	if err := db.Exec(`ANALYZE audit_events`).Error; err != nil {
		t.Fatalf("analyze: %v", err)
	}

	// A first sweep after a long gap: a substantial slice of the table, which
	// is the shape retention actually deletes.
	var lines []string
	if err := db.Raw(`EXPLAIN (ANALYZE)
		DELETE FROM audit_events WHERE occurred_at < now() - interval '20000 minutes'`).
		Scan(&lines).Error; err != nil {
		t.Fatalf("explain delete: %v", err)
	}

	plan := strings.Join(lines, "\n")
	t.Logf("query plan for the retention sweep over %d rows:\n%s", rows, plan)

	if !strings.Contains(plan, "audit_events_occurred_at_brin") {
		t.Errorf("the retention sweep does not use the BRIN index on a %d-row table; "+
			"it is a sequential scan:\n%s", rows, plan)
	}
	// And the listing must still be served by the composite on the same table,
	// which is the property the BRIN choice exists to protect.
	sql, args := explainableListQuery(t, db, Query{WorkspaceID: ws, Limit: 50})
	var listLines []string
	if err := db.Raw("EXPLAIN "+sql, args...).Scan(&listLines).Error; err != nil {
		t.Fatalf("explain list: %v", err)
	}
	listPlan := strings.Join(listLines, "\n")
	if !strings.Contains(listPlan, "audit_events_workspace_time_idx") {
		t.Errorf("the BRIN index displaced the composite for the listing query:\n%s", listPlan)
	}
}

// TestIntegration_MetadataIsQueryableJSONB — stored as jsonb rather than text,
// so it round-trips as structure and a future need can index into it.
func TestIntegration_MetadataIsQueryableJSONB(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db, "jsonb")
	ctx := context.Background()

	rec := event(ws, "project_credential.created", time.Now())
	rec.Metadata = map[string]any{"scopes": []string{"users:read", "audit:read"}}
	if err := repo.Record(ctx, rec); err != nil {
		t.Fatalf("record: %v", err)
	}

	var raw string
	if err := db.Raw(`SELECT metadata->>'scopes' FROM audit_events WHERE id = ?`, rec.ID).
		Scan(&raw).Error; err != nil {
		t.Fatalf("query jsonb: %v", err)
	}

	var scopes []string
	if err := json.Unmarshal([]byte(raw), &scopes); err != nil {
		t.Fatalf("metadata is not structured JSON: %q", raw)
	}
	if len(scopes) != 2 {
		t.Errorf("scopes = %v, want two entries", scopes)
	}
}
