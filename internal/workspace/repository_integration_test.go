//go:build integration

package workspace

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// These tests run against a real PostgreSQL because the properties they cover
// cannot exist anywhere else: the CHECK constraints, the unique index under
// genuine concurrency, and the migration's up/down behaviour. Everything the
// in-memory fake can express is already covered by the unit tests.
//
// Each test gets a private schema (via search_path) that the migrations are
// applied into, so tests are isolated and leave nothing behind — the same
// approach internal/database's migration suite uses.

// newTestSchema creates a private schema, migrates it, and returns a scoped
// DSN plus a GORM handle.
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

	// The schema under test is created by the real migrations, not by a test
	// fixture. That is the point: if 000002 does not produce a working table,
	// every test in this file fails.
	if err := database.Migrate(dsn); err != nil {
		t.Fatalf("apply migrations to %s: %v", schema, err)
	}

	return openGorm(t, dsn), dsn
}

func openGorm(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:         gormlogger.Default.LogMode(gormlogger.Silent),
		TranslateError: true, // matches internal/database.Connect
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
	b.WriteString("ws_")
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

// newWorkspace builds an unsaved domain workspace with a fresh id.
func newWorkspace(t *testing.T, slug, name string) *Workspace {
	t.Helper()
	id, err := publicid.New()
	if err != nil {
		t.Fatalf("generate id: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &Workspace{
		ID: id, Slug: slug, Name: name, Status: StatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
}

func mustCreate(t *testing.T, repo *PostgresRepository, slug, name string) *Workspace {
	t.Helper()
	w := newWorkspace(t, slug, name)
	if err := repo.Create(context.Background(), w); err != nil {
		t.Fatalf("create %q: %v", slug, err)
	}
	return w
}

// ---------------------------------------------------------------------------
// Migration
// ---------------------------------------------------------------------------

// TestMigration000002_CreatesTableAndConstraints verifies the schema the
// migration actually produced, rather than the schema the SQL file appears to
// describe.
func TestMigration000002_CreatesTableAndConstraints(t *testing.T) {
	db, dsn := newTestSchema(t)

	// Asserts "fully migrated", derived rather than hard-coded: a literal here
	// broke this suite the moment 000003 landed.
	latest, err := database.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if version, dirty, err := database.Version(dsn); err != nil || version != latest || dirty {
		t.Fatalf("after migrate: version=%d dirty=%t err=%v, want %d/false/nil", version, dirty, err, latest)
	}

	wantColumns := map[string]string{
		"id":          "uuid",
		"slug":        "text",
		"name":        "text",
		"status":      "text",
		"created_at":  "timestamp with time zone",
		"updated_at":  "timestamp with time zone",
		"archived_at": "timestamp with time zone",
	}

	type column struct {
		ColumnName string
		DataType   string
		IsNullable string
	}
	var columns []column
	if err := db.Raw(
		`SELECT column_name, data_type, is_nullable FROM information_schema.columns
		 WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'workspaces'`,
	).Scan(&columns).Error; err != nil {
		t.Fatalf("read columns: %v", err)
	}

	got := map[string]column{}
	for _, c := range columns {
		got[c.ColumnName] = c
	}
	for name, wantType := range wantColumns {
		c, ok := got[name]
		if !ok {
			t.Errorf("column %q missing", name)
			continue
		}
		if c.DataType != wantType {
			t.Errorf("column %q type = %q, want %q", name, c.DataType, wantType)
		}
	}
	for name := range got {
		if _, ok := wantColumns[name]; !ok {
			t.Errorf("unexpected column %q", name)
		}
	}
	if got["archived_at"].IsNullable != "YES" {
		t.Error("archived_at must be nullable — active workspaces have none")
	}
	for _, name := range []string{"id", "slug", "name", "status", "created_at", "updated_at"} {
		if got[name].IsNullable != "NO" {
			t.Errorf("column %q must be NOT NULL", name)
		}
	}

	// Constraints and indexes by name — a rename would silently drop an
	// invariant while every INSERT kept working.
	var constraints []string
	if err := db.Raw(
		`SELECT conname FROM pg_constraint
		 WHERE conrelid = (CURRENT_SCHEMA() || '.workspaces')::regclass AND contype = 'c'`,
	).Scan(&constraints).Error; err != nil {
		t.Fatalf("read constraints: %v", err)
	}
	for _, want := range []string{
		"workspaces_status_check",
		"workspaces_archived_at_check",
		"workspaces_slug_format_check",
		"workspaces_name_not_blank_check",
	} {
		if !contains(constraints, want) {
			t.Errorf("CHECK constraint %q missing (have %v)", want, constraints)
		}
	}

	var indexes []string
	if err := db.Raw(
		`SELECT indexname FROM pg_indexes WHERE schemaname = CURRENT_SCHEMA() AND tablename = 'workspaces'`,
	).Scan(&indexes).Error; err != nil {
		t.Fatalf("read indexes: %v", err)
	}
	for _, want := range []string{"workspaces_pkey", "idx_workspaces_slug", "idx_workspaces_status_name_id"} {
		if !contains(indexes, want) {
			t.Errorf("index %q missing (have %v)", want, indexes)
		}
	}
}

// TestMigration000002_DownPreservesBaseline is the rollback requirement:
// reverting 000002 must remove the workspaces table and leave the 000001
// baseline — the users table and its indexes — completely intact.
func TestMigration000002_DownPreservesBaseline(t *testing.T) {
	db, dsn := newTestSchema(t)

	repo := NewRepository(db)
	mustCreate(t, repo, "production", "Production")

	// Put a row in users too, so "preserved" means the data, not just the DDL.
	if err := db.Exec(
		`INSERT INTO users (keycloak_sub, email, username, created_at, updated_at)
		 VALUES ('baseline-probe', 'probe@example.test', 'probe', now(), now())`,
	).Error; err != nil {
		t.Fatalf("seed users row: %v", err)
	}

	// Revert every migration above the baseline, so 000002's down is the last
	// one to run. Computed from the embedded set: hard-coding one step meant
	// this test reverted 000003 instead of 000002 as soon as 000003 existed.
	latest, err := database.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if err := database.MigrateSteps(dsn, -int(latest-1)); err != nil {
		t.Fatalf("revert down to the baseline: %v", err)
	}

	if version, dirty, err := database.Version(dsn); err != nil || version != 1 || dirty {
		t.Fatalf("after down: version=%d dirty=%t err=%v, want 1/false/nil", version, dirty, err)
	}

	if tableExists(t, db, "workspaces") {
		t.Error("workspaces table still present after reverting 000002")
	}
	if !tableExists(t, db, "users") {
		t.Fatal("users table was dropped — 000002's down must not touch the baseline")
	}

	var count int64
	if err := db.Raw(`SELECT count(*) FROM users WHERE keycloak_sub = 'baseline-probe'`).Scan(&count).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Errorf("baseline row count = %d, want 1 — user data must survive", count)
	}

	// The baseline's indexes must survive too.
	var indexes []string
	if err := db.Raw(
		`SELECT indexname FROM pg_indexes WHERE schemaname = CURRENT_SCHEMA() AND tablename = 'users'`,
	).Scan(&indexes).Error; err != nil {
		t.Fatalf("read users indexes: %v", err)
	}
	for _, want := range []string{"idx_users_email", "idx_users_keycloak_sub"} {
		if !contains(indexes, want) {
			t.Errorf("baseline index %q was lost (have %v)", want, indexes)
		}
	}

	// And it must go back up cleanly.
	if err := database.Migrate(dsn); err != nil {
		t.Fatalf("re-apply 000002: %v", err)
	}
	if !tableExists(t, db, "workspaces") {
		t.Error("workspaces table missing after re-applying 000002")
	}
}

// ---------------------------------------------------------------------------
// Constraints — the invariants the database enforces regardless of the code
// ---------------------------------------------------------------------------

// TestConstraints_RejectInvalidRows drives raw SQL past the service on
// purpose. These constraints exist precisely for the paths the service does
// not control: a future bug, a manual psql session, a second writer.
func TestConstraints_RejectInvalidRows(t *testing.T) {
	db, _ := newTestSchema(t)

	tests := map[string]string{
		"unknown status": `INSERT INTO workspaces (id, slug, name, status)
		                   VALUES (gen_random_uuid(), 'a1', 'A', 'deleted')`,
		"archived without archived_at": `INSERT INTO workspaces (id, slug, name, status)
		                   VALUES (gen_random_uuid(), 'a2', 'A', 'archived')`,
		"active with archived_at": `INSERT INTO workspaces (id, slug, name, status, archived_at)
		                   VALUES (gen_random_uuid(), 'a3', 'A', 'active', now())`,
		"uppercase slug": `INSERT INTO workspaces (id, slug, name)
		                   VALUES (gen_random_uuid(), 'Production', 'A')`,
		"slug with space": `INSERT INTO workspaces (id, slug, name)
		                   VALUES (gen_random_uuid(), 'my workspace', 'A')`,
		"leading hyphen slug": `INSERT INTO workspaces (id, slug, name)
		                   VALUES (gen_random_uuid(), '-prod', 'A')`,
		"doubled hyphen slug": `INSERT INTO workspaces (id, slug, name)
		                   VALUES (gen_random_uuid(), 'a--b', 'A')`,
		"empty slug": `INSERT INTO workspaces (id, slug, name)
		                   VALUES (gen_random_uuid(), '', 'A')`,
		"overlong slug": `INSERT INTO workspaces (id, slug, name)
		                   VALUES (gen_random_uuid(), repeat('a', 64), 'A')`,
		"blank name": `INSERT INTO workspaces (id, slug, name)
		                   VALUES (gen_random_uuid(), 'a4', '   ')`,
	}

	for name, stmt := range tests {
		t.Run(name, func(t *testing.T) {
			if err := db.Exec(stmt).Error; err == nil {
				t.Error("insert succeeded; the constraint did not fire")
			}
		})
	}
}

// TestConstraints_AcceptValidRows is the other half: the constraints must not
// be so tight that legitimate rows are refused.
func TestConstraints_AcceptValidRows(t *testing.T) {
	db, _ := newTestSchema(t)

	statements := map[string]string{
		"minimal active": `INSERT INTO workspaces (id, slug, name)
		                   VALUES (gen_random_uuid(), 'a', 'A')`,
		"archived with timestamp": `INSERT INTO workspaces (id, slug, name, status, archived_at)
		                   VALUES (gen_random_uuid(), 'b', 'B', 'archived', now())`,
		"hyphenated slug": `INSERT INTO workspaces (id, slug, name)
		                   VALUES (gen_random_uuid(), 'production-eu-west-1', 'C')`,
		"numeric slug": `INSERT INTO workspaces (id, slug, name)
		                   VALUES (gen_random_uuid(), '2026', 'D')`,
		"slug at the length limit": `INSERT INTO workspaces (id, slug, name)
		                   VALUES (gen_random_uuid(), repeat('a', 63), 'E')`,
		"name with punctuation": `INSERT INTO workspaces (id, slug, name)
		                   VALUES (gen_random_uuid(), 'f', 'ACME, Inc. — Production')`,
	}

	for name, stmt := range statements {
		t.Run(name, func(t *testing.T) {
			if err := db.Exec(stmt).Error; err != nil {
				t.Errorf("valid insert refused: %v", err)
			}
		})
	}
}

// TestConstraints_DefaultsApply pins the column defaults, which the repository
// relies on for any column it does not write.
func TestConstraints_DefaultsApply(t *testing.T) {
	db, _ := newTestSchema(t)

	if err := db.Exec(`INSERT INTO workspaces (id, slug, name) VALUES (gen_random_uuid(), 'defaults', 'D')`).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}

	var out struct {
		Status     string
		CreatedAt  time.Time
		UpdatedAt  time.Time
		ArchivedAt *time.Time
	}
	if err := db.Raw(`SELECT status, created_at, updated_at, archived_at FROM workspaces WHERE slug = 'defaults'`).
		Scan(&out).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}

	if out.Status != "active" {
		t.Errorf("default status = %q, want active", out.Status)
	}
	if out.CreatedAt.IsZero() || out.UpdatedAt.IsZero() {
		t.Error("created_at/updated_at defaults did not apply")
	}
	if out.ArchivedAt != nil {
		t.Errorf("archived_at = %v, want NULL", out.ArchivedAt)
	}
}

// ---------------------------------------------------------------------------
// Repository
// ---------------------------------------------------------------------------

func TestRepository_CreateAndRead(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)

	created := mustCreate(t, repo, "production", "Production")

	got, err := repo.GetByID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("GetByID returned nil for a row that was just created")
	}
	if got.ID != created.ID || got.Slug != "production" || got.Name != "Production" {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.Status != StatusActive {
		t.Errorf("status = %q, want active", got.Status)
	}
	if got.ArchivedAt != nil {
		t.Errorf("archived_at = %v, want nil", got.ArchivedAt)
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at did not round-trip")
	}
}

func TestRepository_GetByIDMissingReturnsNilNil(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)

	id, _ := publicid.New()
	got, err := repo.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetByID on an absent row returned an error: %v", err)
	}
	if got != nil {
		t.Errorf("GetByID = %+v, want nil", got)
	}
}

// TestRepository_DuplicateSlugRejected is the deterministic-translation
// requirement: the unique index fires and the repository turns it into
// ErrSlugTaken without inspecting PostgreSQL's message text.
func TestRepository_DuplicateSlugRejected(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)

	mustCreate(t, repo, "production", "Production")

	dup := newWorkspace(t, "production", "Another Production")
	err := repo.Create(context.Background(), dup)
	if !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("duplicate create = %v, want ErrSlugTaken", err)
	}

	// And nothing partial was written.
	var count int64
	if err := db.Raw(`SELECT count(*) FROM workspaces WHERE slug = 'production'`).Scan(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("row count for the slug = %d, want 1", count)
	}
}

// TestRepository_ArchivedSlugStaysTaken pins permanence at the storage layer.
func TestRepository_ArchivedSlugStaysTaken(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ctx := context.Background()

	original := mustCreate(t, repo, "production", "Production")
	if _, err := repo.Archive(ctx, original.ID, time.Now().UTC()); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	err := repo.Create(ctx, newWorkspace(t, "production", "New Production"))
	if !errors.Is(err, ErrSlugTaken) {
		t.Errorf("reusing an archived slug = %v, want ErrSlugTaken", err)
	}
}

func TestRepository_ListFilters(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ctx := context.Background()

	mustCreate(t, repo, "production", "Production")
	mustCreate(t, repo, "staging", "Staging")
	old := mustCreate(t, repo, "legacy", "Legacy")
	if _, err := repo.Archive(ctx, old.ID, time.Now().UTC()); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	tests := map[StatusFilter]int{
		FilterActive:   2,
		FilterArchived: 1,
		FilterAll:      3,
	}
	for filter, want := range tests {
		items, err := repo.List(ctx, filter)
		if err != nil {
			t.Fatalf("List(%q): %v", filter, err)
		}
		if len(items) != want {
			t.Errorf("List(%q) returned %d, want %d", filter, len(items), want)
		}
	}
}

// TestRepository_ListOrdersByNameThenID pins the ordering the index serves,
// including the id tiebreaker for equal names.
func TestRepository_ListOrdersByNameThenID(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ctx := context.Background()

	mustCreate(t, repo, "zebra", "Zebra")
	mustCreate(t, repo, "alpha", "Alpha")
	mustCreate(t, repo, "shared-one", "Shared")
	mustCreate(t, repo, "shared-two", "Shared")

	items, err := repo.List(ctx, FilterActive)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4", len(items))
	}

	wantNames := []string{"Alpha", "Shared", "Shared", "Zebra"}
	for i, want := range wantNames {
		if items[i].Name != want {
			t.Fatalf("position %d = %q, want %q", i, items[i].Name, want)
		}
	}
	if items[1].ID > items[2].ID {
		t.Errorf("equal names not tie-broken by id: %q then %q", items[1].ID, items[2].ID)
	}
}

func TestRepository_UpdateName(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ctx := context.Background()

	created := mustCreate(t, repo, "production", "Production")
	later := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)

	updated, err := repo.UpdateName(ctx, created.ID, "Production EU", later)
	if err != nil {
		t.Fatalf("UpdateName: %v", err)
	}
	if updated == nil {
		t.Fatal("UpdateName returned nil for an existing active row")
	}
	if updated.Name != "Production EU" {
		t.Errorf("name = %q, want %q", updated.Name, "Production EU")
	}
	if updated.Slug != "production" {
		t.Errorf("slug = %q — a rename must not move the slug", updated.Slug)
	}
	if !updated.UpdatedAt.Equal(later) {
		t.Errorf("updated_at = %v, want %v", updated.UpdatedAt, later)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt.Truncate(time.Microsecond)) {
		t.Errorf("created_at moved: %v → %v", created.CreatedAt, updated.CreatedAt)
	}
}

func TestRepository_UpdateNameMissingReturnsNilNil(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)

	id, _ := publicid.New()
	got, err := repo.UpdateName(context.Background(), id, "X", time.Now().UTC())
	if err != nil {
		t.Fatalf("UpdateName on an absent row: %v", err)
	}
	if got != nil {
		t.Errorf("UpdateName = %+v, want nil", got)
	}
}

func TestRepository_UpdateNameOnArchivedIsRejected(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ctx := context.Background()

	created := mustCreate(t, repo, "legacy", "Legacy")
	if _, err := repo.Archive(ctx, created.ID, time.Now().UTC()); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	_, err := repo.UpdateName(ctx, created.ID, "Renamed", time.Now().UTC())
	if !errors.Is(err, ErrArchived) {
		t.Fatalf("UpdateName on archived = %v, want ErrArchived", err)
	}

	// The rejection must have written nothing.
	after, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if after.Name != "Legacy" {
		t.Errorf("name changed to %q despite the rejection", after.Name)
	}
}

// TestRepository_Archive covers the transition and the constraint that makes
// it atomic: status and archived_at are written in one statement, so the
// biconditional CHECK is never even transiently violated.
func TestRepository_Archive(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ctx := context.Background()

	created := mustCreate(t, repo, "production", "Production")
	at := time.Now().UTC().Truncate(time.Microsecond)

	archived, err := repo.Archive(ctx, created.ID, at)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if archived.Status != StatusArchived {
		t.Errorf("status = %q, want archived", archived.Status)
	}
	if archived.ArchivedAt == nil || !archived.ArchivedAt.Equal(at) {
		t.Errorf("archived_at = %v, want %v", archived.ArchivedAt, at)
	}
	if archived.Slug != "production" {
		t.Errorf("slug = %q — archiving must not release the slug", archived.Slug)
	}

	// Nothing was deleted.
	var count int64
	if err := db.Raw(`SELECT count(*) FROM workspaces WHERE id = ?`, created.ID).Scan(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1 — archiving is not a delete", count)
	}
}

// TestRepository_ArchiveIsIdempotent pins that a retry does not move the
// original timestamp — the guard is in the WHERE clause, so the second UPDATE
// affects zero rows and the read-back returns the row as it stands.
func TestRepository_ArchiveIsIdempotent(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ctx := context.Background()

	created := mustCreate(t, repo, "production", "Production")
	first, err := repo.Archive(ctx, created.ID, time.Now().UTC().Truncate(time.Microsecond))
	if err != nil {
		t.Fatalf("first Archive: %v", err)
	}

	for i := 0; i < 3; i++ {
		later := time.Now().UTC().Add(time.Duration(i+1) * time.Hour).Truncate(time.Microsecond)
		again, err := repo.Archive(ctx, created.ID, later)
		if err != nil {
			t.Fatalf("repeat Archive %d: %v", i, err)
		}
		if again.Status != StatusArchived {
			t.Errorf("repeat %d: status = %q", i, again.Status)
		}
		if !again.ArchivedAt.Equal(*first.ArchivedAt) {
			t.Errorf("repeat %d moved archived_at from %v to %v", i, first.ArchivedAt, again.ArchivedAt)
		}
	}
}

func TestRepository_ArchiveMissingReturnsNilNil(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)

	id, _ := publicid.New()
	got, err := repo.Archive(context.Background(), id, time.Now().UTC())
	if err != nil {
		t.Fatalf("Archive on an absent row: %v", err)
	}
	if got != nil {
		t.Errorf("Archive = %+v, want nil", got)
	}
}

// TestRepository_ConcurrentCreateSameSlug is the case only a real database can
// settle: two writers racing on the same slug. Exactly one must win, and the
// loser must get ErrSlugTaken — not a driver error, and not a second row.
func TestRepository_ConcurrentCreateSameSlug(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ctx := context.Background()

	const writers = 8
	errs := make(chan error, writers)
	start := make(chan struct{})

	for i := 0; i < writers; i++ {
		w := newWorkspace(t, "contended", "Contended")
		go func() {
			<-start
			errs <- repo.Create(ctx, w)
		}()
	}
	close(start)

	var wins, taken int
	for i := 0; i < writers; i++ {
		switch err := <-errs; {
		case err == nil:
			wins++
		case errors.Is(err, ErrSlugTaken):
			taken++
		default:
			t.Errorf("unexpected error from a racing writer: %v", err)
		}
	}

	if wins != 1 {
		t.Errorf("%d writers succeeded, want exactly 1", wins)
	}
	if taken != writers-1 {
		t.Errorf("%d writers got ErrSlugTaken, want %d", taken, writers-1)
	}

	var count int64
	if err := db.Raw(`SELECT count(*) FROM workspaces WHERE slug = 'contended'`).Scan(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1", count)
	}
}

// TestRepository_ConcurrentArchive pins that racing archive calls converge on
// one timestamp rather than overwriting each other.
func TestRepository_ConcurrentArchive(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	ctx := context.Background()

	created := mustCreate(t, repo, "contended", "Contended")

	const callers = 8
	results := make(chan *Workspace, callers)
	start := make(chan struct{})

	for i := 0; i < callers; i++ {
		at := time.Now().UTC().Add(time.Duration(i) * time.Minute).Truncate(time.Microsecond)
		go func() {
			<-start
			w, err := repo.Archive(ctx, created.ID, at)
			if err != nil {
				results <- nil
				return
			}
			results <- w
		}()
	}
	close(start)

	var stamps []time.Time
	for i := 0; i < callers; i++ {
		w := <-results
		if w == nil {
			t.Fatal("a concurrent archive failed; the operation must be idempotent")
		}
		if w.Status != StatusArchived {
			t.Errorf("concurrent archive returned status %q", w.Status)
		}
		stamps = append(stamps, *w.ArchivedAt)
	}

	// Every caller must observe the same archived_at: only one write happened.
	for i, s := range stamps {
		if !s.Equal(stamps[0]) {
			t.Errorf("caller %d saw archived_at %v, caller 0 saw %v — more than one write landed", i, s, stamps[0])
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func tableExists(t *testing.T, db *gorm.DB, name string) bool {
	t.Helper()
	var exists bool
	if err := db.Raw(
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
		 WHERE table_schema = CURRENT_SCHEMA() AND table_name = ?)`, name,
	).Scan(&exists).Error; err != nil {
		t.Fatalf("probe table %s: %v", name, err)
	}
	return exists
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
