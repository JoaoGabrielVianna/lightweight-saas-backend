//go:build integration

package database

import (
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// legacyAutoMigrateDDL is the exact SQL gorm's AutoMigrate emitted for
// user.User against PostgreSQL 15 — captured by running
// db.AutoMigrate(&user.User{}) against an empty database with the gorm logger
// at Info and copying the statements verbatim.
//
// It is reproduced here (rather than by calling AutoMigrate, which no longer
// exists in the code path) so the adoption test proves the real thing: that a
// database created by the OLD code is picked up by the NEW code untouched. If
// this constant is ever "cleaned up" to match the migration file, the test
// stops testing anything.
const legacyAutoMigrateDDL = `
CREATE TABLE "users" ("id" bigserial,"keycloak_sub" text NOT NULL,"email" text,"username" text NOT NULL,"created_at" timestamptz,"updated_at" timestamptz,PRIMARY KEY ("id"));
CREATE INDEX IF NOT EXISTS "idx_users_email" ON "users" ("email");
CREATE UNIQUE INDEX IF NOT EXISTS "idx_users_keycloak_sub" ON "users" ("keycloak_sub");
`

// newTestSchema gives the calling test a private, empty postgres schema and a
// DSN scoped to it via search_path, dropped on cleanup.
//
// Schema-level isolation (rather than a database per test) keeps the tests
// runnable against the CI service container and the docker-compose stack alike,
// neither of which grants CREATE DATABASE reliably, and lets the whole file run
// in parallel against a single postgres.
func newTestSchema(t *testing.T) string {
	t.Helper()

	base := requireDSN(t)

	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer func() { _ = admin.Close() }()

	schema := schemaNameFor(t)
	if _, err := admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`); err != nil {
		t.Fatalf("drop stale schema %s: %v", schema, err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		cleanup, err := sql.Open("pgx", base)
		if err != nil {
			return
		}
		defer func() { _ = cleanup.Close() }()
		_, _ = cleanup.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
	})

	return withSearchPath(t, base, schema)
}

// requireDSN returns DB_URL or skips. Matching the pre-existing integration
// test's contract: no DB_URL means "not running inside the stack", which is a
// skip, not a failure.
func requireDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DB_URL")
	if dsn == "" {
		t.Skip("DB_URL unset — integration test requires a reachable postgres")
	}
	return dsn
}

// schemaNameFor derives a legal, collision-free schema identifier from the test
// name. Go guarantees test names are unique within a binary, so this needs no
// randomness — which matters because a random name would leak a schema on every
// crashed run.
func schemaNameFor(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(t.Name())
	var b strings.Builder
	b.WriteString("mig_")
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// withSearchPath returns dsn with search_path pinned to schema. pgx forwards
// unrecognized query parameters to the server as runtime parameters, so this
// scopes every statement on the connection — including the ones golang-migrate
// runs to create schema_migrations.
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

// openScoped opens a raw pool against a search_path-scoped DSN.
func openScoped(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open scoped connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// latestVersion is the highest version in the embedded migration set. Thin
// wrapper over LatestVersion so a failure inside it fails the test loudly.
func latestVersion(t *testing.T) uint {
	t.Helper()
	v, err := LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	return v
}

// TestMigrate_BootstrapsEmptyDatabase is the "fresh install" acceptance
// criterion: an empty database ends up with the full schema and a recorded
// version, with no manual step.
func TestMigrate_BootstrapsEmptyDatabase(t *testing.T) {
	dsn := newTestSchema(t)

	if err := Migrate(dsn); err != nil {
		t.Fatalf("Migrate on empty schema: %v", err)
	}

	version, dirty, err := Version(dsn)
	if err != nil {
		t.Fatalf("Version after Migrate: %v", err)
	}
	if want := latestVersion(t); version != want {
		t.Errorf("version = %d, want %d", version, want)
	}
	if dirty {
		t.Error("database reported dirty after a clean migration")
	}

	assertUsersSchema(t, openScoped(t, dsn))
}

// TestMigrate_AdoptsLegacyAutoMigrateSchema is the backward-compatibility
// acceptance criterion, and the reason the baseline is written with IF NOT
// EXISTS.
//
// It builds a database exactly as the pre-migration code did, puts a row in it,
// then runs the new migration path over it. The migration must succeed, record
// version 1, leave the schema untouched, and — the part that matters to an
// operator — not lose the row.
func TestMigrate_AdoptsLegacyAutoMigrateSchema(t *testing.T) {
	dsn := newTestSchema(t)
	db := openScoped(t, dsn)

	if _, err := db.Exec(legacyAutoMigrateDDL); err != nil {
		t.Fatalf("create legacy AutoMigrate schema: %v", err)
	}

	const sub = "legacy-subject-0001"
	if _, err := db.Exec(
		`INSERT INTO users (keycloak_sub, email, username, created_at, updated_at)
		 VALUES ($1, $2, $3, now(), now())`,
		sub, "legacy@example.test", "legacy",
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	// A pre-migration database has no schema_migrations table at all.
	if _, _, err := Version(dsn); !errors.Is(err, ErrNoMigrationsApplied) {
		t.Fatalf("Version on un-adopted legacy schema: got %v, want ErrNoMigrationsApplied", err)
	}

	if err := Migrate(dsn); err != nil {
		t.Fatalf("Migrate over legacy AutoMigrate schema: %v", err)
	}

	version, dirty, err := Version(dsn)
	if err != nil {
		t.Fatalf("Version after adoption: %v", err)
	}
	if want := latestVersion(t); version != want || dirty {
		t.Errorf("after adoption version=%d dirty=%t, want %d/false", version, dirty, want)
	}

	var got string
	if err := db.QueryRow(`SELECT username FROM users WHERE keycloak_sub = $1`, sub).Scan(&got); err != nil {
		t.Fatalf("legacy row lookup after adoption: %v", err)
	}
	if got != "legacy" {
		t.Errorf("legacy row username = %q, want %q", got, "legacy")
	}

	assertUsersSchema(t, db)
}

// TestMigrate_IsIdempotent covers the ordinary case: every boot after the first
// runs Migrate again and must do nothing.
func TestMigrate_IsIdempotent(t *testing.T) {
	dsn := newTestSchema(t)

	for i := range 3 {
		if err := Migrate(dsn); err != nil {
			t.Fatalf("Migrate call %d: %v", i+1, err)
		}
	}

	version, dirty, err := Version(dsn)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if want := latestVersion(t); version != want || dirty {
		t.Errorf("version=%d dirty=%t after 3 migrations, want %d/false", version, dirty, want)
	}
}

// TestMigrate_FailsFastOnDirtyState pins the "fail immediately on an
// inconsistent migration" requirement. A dirty marker means a migration died
// part-way and the schema shape is unknown; booting anyway would serve traffic
// against a schema nobody has verified.
func TestMigrate_FailsFastOnDirtyState(t *testing.T) {
	dsn := newTestSchema(t)
	if err := Migrate(dsn); err != nil {
		t.Fatalf("initial Migrate: %v", err)
	}

	db := openScoped(t, dsn)
	if _, err := db.Exec(`UPDATE schema_migrations SET dirty = true`); err != nil {
		t.Fatalf("mark schema dirty: %v", err)
	}

	err := Migrate(dsn)
	if err == nil {
		t.Fatal("Migrate succeeded against a dirty database; it must fail")
	}
	var dirtyErr migrate.ErrDirty
	if !errors.As(err, &dirtyErr) {
		t.Fatalf("error = %v, want a migrate.ErrDirty", err)
	}
	if !strings.Contains(err.Error(), "migrate-force") {
		t.Errorf("dirty error omits the recovery hint operators need: %v", err)
	}

	// Force is the documented way out, and must restore normal operation.
	if err := Force(dsn, 1); err != nil {
		t.Fatalf("Force after dirty: %v", err)
	}
	if err := Migrate(dsn); err != nil {
		t.Fatalf("Migrate after Force: %v", err)
	}
}

// TestMigrateDown_RevertsBaseline exercises the down path so the migration set
// stays reversible in development.
func TestMigrateDown_RevertsBaseline(t *testing.T) {
	dsn := newTestSchema(t)
	if err := Migrate(dsn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := MigrateDown(dsn); err != nil {
		t.Fatalf("MigrateDown: %v", err)
	}

	db := openScoped(t, dsn)
	var exists bool
	if err := db.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
		 WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'users')`,
	).Scan(&exists); err != nil {
		t.Fatalf("probe users table: %v", err)
	}
	if exists {
		t.Error("users table still present after MigrateDown")
	}

	if _, _, err := Version(dsn); !errors.Is(err, ErrNoMigrationsApplied) {
		t.Errorf("Version after full down: got %v, want ErrNoMigrationsApplied", err)
	}
}

// TestMigrateSteps_UpAndDown covers the stepwise path used by the dev CLI.
func TestMigrateSteps_UpAndDown(t *testing.T) {
	dsn := newTestSchema(t)

	if err := MigrateSteps(dsn, 1); err != nil {
		t.Fatalf("MigrateSteps(+1): %v", err)
	}
	if version, _, err := Version(dsn); err != nil || version != 1 {
		t.Fatalf("after +1 step: version=%d err=%v, want 1/nil (one step = the baseline)", version, err)
	}

	if err := MigrateSteps(dsn, -1); err != nil {
		t.Fatalf("MigrateSteps(-1): %v", err)
	}
	if _, _, err := Version(dsn); !errors.Is(err, ErrNoMigrationsApplied) {
		t.Errorf("after -1 step: got %v, want ErrNoMigrationsApplied", err)
	}
}

// assertUsersSchema pins the shape the application depends on, whether the
// table was created by the baseline migration or adopted from AutoMigrate. This
// is the "zero functional change" guarantee expressed as a schema assertion:
// both paths must land on identical column types and index names.
func assertUsersSchema(t *testing.T, db *sql.DB) {
	t.Helper()

	wantColumns := map[string]string{
		"id":           "bigint",
		"keycloak_sub": "text",
		"email":        "text",
		"username":     "text",
		"created_at":   "timestamp with time zone",
		"updated_at":   "timestamp with time zone",
	}

	rows, err := db.Query(
		`SELECT column_name, data_type FROM information_schema.columns
		 WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'users'`)
	if err != nil {
		t.Fatalf("read users columns: %v", err)
	}
	defer func() { _ = rows.Close() }()

	got := map[string]string{}
	for rows.Next() {
		var name, dataType string
		if err := rows.Scan(&name, &dataType); err != nil {
			t.Fatalf("scan column row: %v", err)
		}
		got[name] = dataType
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}

	for name, wantType := range wantColumns {
		gotType, ok := got[name]
		if !ok {
			t.Errorf("users.%s missing", name)
			continue
		}
		if gotType != wantType {
			t.Errorf("users.%s type = %q, want %q", name, gotType, wantType)
		}
	}
	for name := range got {
		if _, ok := wantColumns[name]; !ok {
			t.Errorf("unexpected column users.%s — the baseline must not add columns", name)
		}
	}

	// Index names matter: gorm derives them from the model tags, so a rename
	// here would mean AutoMigrate-era databases and migrated ones disagree.
	for _, idx := range []string{"idx_users_email", "idx_users_keycloak_sub", "users_pkey"} {
		var exists bool
		if err := db.QueryRow(
			`SELECT EXISTS (SELECT 1 FROM pg_indexes
			 WHERE schemaname = CURRENT_SCHEMA() AND tablename = 'users' AND indexname = $1)`,
			idx,
		).Scan(&exists); err != nil {
			t.Fatalf("probe index %s: %v", idx, err)
		}
		if !exists {
			t.Errorf("index %s missing", idx)
		}
	}

	// The unique constraint is the invariant Service.EnsureUser relies on.
	var isUnique bool
	if err := db.QueryRow(
		`SELECT indisunique FROM pg_index
		 WHERE indexrelid = (CURRENT_SCHEMA() || '.idx_users_keycloak_sub')::regclass`,
	).Scan(&isUnique); err != nil {
		t.Fatalf("probe uniqueness of idx_users_keycloak_sub: %v", err)
	}
	if !isUnique {
		t.Error("idx_users_keycloak_sub is not unique — the EnsureUser race guard is gone")
	}
}
