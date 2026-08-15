package database

import (
	"errors"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// migrationName matches golang-migrate's required file naming:
// {version}_{title}.{up|down}.sql. A file that does not match is silently
// ignored by the source driver, which is the worst possible failure mode — a
// migration that exists in the tree and never runs. Hence this suite.
var migrationName = regexp.MustCompile(`^(\d{6})_([a-z0-9_]+)\.(up|down)\.sql$`)

// TestEmbeddedMigrations_AreLoadable is the check that catches the mistake that
// costs the most: adding an .sql file and shipping a binary that cannot read
// it. iofs.New parses the whole set the same way the runtime does.
func TestEmbeddedMigrations_AreLoadable(t *testing.T) {
	sub, err := MigrationsFS()
	if err != nil {
		t.Fatalf("MigrationsFS: %v", err)
	}

	source, err := iofs.New(sub, ".")
	if err != nil {
		t.Fatalf("iofs.New over the embedded set: %v", err)
	}
	defer func() { _ = source.Close() }()

	first, err := source.First()
	if err != nil {
		t.Fatalf("source has no first migration: %v", err)
	}
	if first != 1 {
		t.Errorf("first migration version = %d, want 1", first)
	}
}

// TestLatestVersion_MatchesTheEmbeddedSet pins the helper the integration
// suites use instead of hard-coding a version number.
//
// The number itself is derived here too, from a separate walk of the directory,
// so this test does not need editing when a migration is added — which is the
// entire point of the function existing.
func TestLatestVersion_MatchesTheEmbeddedSet(t *testing.T) {
	got, err := LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}

	sub, err := MigrationsFS()
	if err != nil {
		t.Fatalf("MigrationsFS: %v", err)
	}
	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}

	var want uint
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if uint(n) > want {
			want = uint(n)
		}
	}

	if want == 0 {
		t.Fatal("the embedded set has no parseable migrations")
	}
	if got != want {
		t.Errorf("LatestVersion = %d, want %d", got, want)
	}

	// It must agree with the source driver's own view, which is what actually
	// decides where `migrate up` stops.
	source, err := iofs.New(sub, ".")
	if err != nil {
		t.Fatalf("iofs.New: %v", err)
	}
	defer func() { _ = source.Close() }()

	version, err := source.First()
	if err != nil {
		t.Fatalf("source.First: %v", err)
	}
	for {
		next, err := source.Next(version)
		if err != nil {
			break // no further migration
		}
		version = next
	}
	if uint(version) != got {
		t.Errorf("LatestVersion = %d but the source driver's last version is %d", got, version)
	}
}

// TestEmbeddedMigrations_Naming pins the file naming and the up/down pairing.
func TestEmbeddedMigrations_Naming(t *testing.T) {
	sub, err := MigrationsFS()
	if err != nil {
		t.Fatalf("MigrationsFS: %v", err)
	}

	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no migrations embedded — go:embed matched nothing")
	}

	// version -> directions seen
	seen := map[int]map[string]bool{}
	titles := map[int]string{}

	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("unexpected directory %q inside migrations/", e.Name())
			continue
		}
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			t.Errorf("migration %q does not match {version}_{title}.{up|down}.sql — "+
				"golang-migrate would ignore it silently", e.Name())
			continue
		}
		version, err := strconv.Atoi(m[1])
		if err != nil {
			t.Errorf("migration %q has an unparseable version: %v", e.Name(), err)
			continue
		}
		if seen[version] == nil {
			seen[version] = map[string]bool{}
		}
		if seen[version][m[3]] {
			t.Errorf("duplicate %s migration for version %d", m[3], version)
		}
		seen[version][m[3]] = true

		if prev, ok := titles[version]; ok && prev != m[2] {
			t.Errorf("version %d has two titles (%q and %q); up and down must agree",
				version, prev, m[2])
		}
		titles[version] = m[2]
	}

	versions := make([]int, 0, len(seen))
	for v, directions := range seen {
		versions = append(versions, v)
		if !directions["up"] {
			t.Errorf("version %d has no .up.sql", v)
		}
		if !directions["down"] {
			t.Errorf("version %d has no .down.sql — the set must stay reversible", v)
		}
	}
	sort.Ints(versions)

	// Gaps are legal for golang-migrate but make "which version am I on"
	// ambiguous for operators reading `make migrate-version`. Keep them dense.
	for i, v := range versions {
		if v != i+1 {
			t.Errorf("migration versions must be dense starting at 1; got %v", versions)
			break
		}
	}
}

// TestBaselineMigration_IsIdempotent guards the property the whole
// backward-compatibility story rests on: the baseline must be safe to run
// against a database that gorm's AutoMigrate already built.
//
// The behavioural proof is TestMigrate_AdoptsLegacyAutoMigrateSchema (build tag
// integration, needs a real postgres). This unit-level check runs everywhere,
// including on a laptop with no database, so the property cannot be broken
// unnoticed by someone who only runs `go test ./...`.
func TestBaselineMigration_IsIdempotent(t *testing.T) {
	sub, err := MigrationsFS()
	if err != nil {
		t.Fatalf("MigrationsFS: %v", err)
	}
	raw, err := fs.ReadFile(sub, "000001_baseline.up.sql")
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	body := string(raw)

	for _, stmt := range sqlStatements(body) {
		upper := strings.ToUpper(stmt)
		switch {
		case strings.HasPrefix(upper, "CREATE TABLE"):
			if !strings.Contains(upper, "IF NOT EXISTS") {
				t.Errorf("baseline CREATE TABLE is not idempotent, "+
					"AutoMigrate-era databases would fail to adopt:\n%s", stmt)
			}
		case strings.HasPrefix(upper, "CREATE INDEX"),
			strings.HasPrefix(upper, "CREATE UNIQUE INDEX"):
			if !strings.Contains(upper, "IF NOT EXISTS") {
				t.Errorf("baseline CREATE INDEX is not idempotent:\n%s", stmt)
			}
		case strings.HasPrefix(upper, "ALTER "), strings.HasPrefix(upper, "DROP "):
			t.Errorf("baseline must not modify an existing schema — "+
				"adoption of AutoMigrate databases depends on it being additive only:\n%s", stmt)
		}
	}
}

// TestBaselineMigration_MatchesAutoMigrateShape pins the columns and index
// names the pre-migration AutoMigrate produced. If someone edits the baseline
// instead of adding a new migration, this fails.
func TestBaselineMigration_MatchesAutoMigrateShape(t *testing.T) {
	sub, err := MigrationsFS()
	if err != nil {
		t.Fatalf("MigrationsFS: %v", err)
	}
	raw, err := fs.ReadFile(sub, "000001_baseline.up.sql")
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	body := strings.ToLower(string(raw))

	for _, want := range []string{
		"id           bigserial   primary key",
		"keycloak_sub text        not null",
		"email        text",
		"username     text        not null",
		"created_at   timestamptz",
		"updated_at   timestamptz",
		"idx_users_email",
		"idx_users_keycloak_sub",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("baseline no longer declares %q — it must reproduce the "+
				"AutoMigrate schema exactly", want)
		}
	}
}

// TestMigrate_RejectsEmptyDSN covers the guard that turns a missing DB_URL into
// a clear message instead of a driver-level parse error.
func TestMigrate_RejectsEmptyDSN(t *testing.T) {
	for name, call := range map[string]func() error{
		"Migrate":  func() error { return Migrate("") },
		"Down":     func() error { return MigrateDown("") },
		"Steps":    func() error { return MigrateSteps("", 1) },
		"Force":    func() error { return Force("", 1) },
		"Version":  func() error { _, _, err := Version(""); return err },
		"NoScheme": func() error { return Migrate("") },
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatal("expected an error for an empty DSN")
			}
			if !strings.Contains(err.Error(), "DSN") {
				t.Errorf("error = %v, want it to mention the DSN", err)
			}
		})
	}
}

// unreachableDSN points at TEST-NET-1 (RFC 5737), which is guaranteed not to
// route, with a 1s connect timeout. Using it means these tests can never
// accidentally touch a real database, and they fail fast on hosts with slow DNS.
const unreachableDSN = "postgres://nobody:nopass@192.0.2.1:1/nodb?sslmode=disable&connect_timeout=1"

// TestMigrationOperations_FailOnUnreachableDatabase pins that every entry point
// surfaces a connection failure as an error rather than panicking or hanging.
// Connect turns these into log.Fatal at boot, so an entry point that swallowed
// one would let the API serve traffic against an unmigrated schema.
func TestMigrationOperations_FailOnUnreachableDatabase(t *testing.T) {
	t.Parallel()

	operations := map[string]func() error{
		"Migrate":      func() error { return Migrate(unreachableDSN) },
		"MigrateDown":  func() error { return MigrateDown(unreachableDSN) },
		"MigrateSteps": func() error { return MigrateSteps(unreachableDSN, 1) },
		"Force":        func() error { return Force(unreachableDSN, 1) },
		"Version":      func() error { _, _, err := Version(unreachableDSN); return err },
	}

	for name, op := range operations {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := op(); err == nil {
				t.Fatal("expected an error against an unreachable database")
			}
		})
	}
}

// TestAnnotate_DirtyStateCarriesRecoveryInstructions covers the error text an
// operator sees when a migration has failed part-way. golang-migrate's own
// message is just "Dirty database version N. Fix and force version." — which
// does not say how, or that guessing the version is the dangerous part.
func TestAnnotate_DirtyStateCarriesRecoveryInstructions(t *testing.T) {
	err := annotate(migrate.ErrDirty{Version: 7})
	if err == nil {
		t.Fatal("annotate(ErrDirty) returned nil")
	}

	var dirty migrate.ErrDirty
	if !errors.As(err, &dirty) {
		t.Fatal("annotate must wrap, not replace, ErrDirty — callers match on it")
	}
	if dirty.Version != 7 {
		t.Errorf("wrapped version = %d, want 7", dirty.Version)
	}

	for _, want := range []string{"dirty", "7", "migrate-force"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("dirty error %q is missing %q", err.Error(), want)
		}
	}
}

// TestAnnotate_PassesOtherErrorsThrough guards against annotate swallowing or
// rewriting errors it does not understand.
func TestAnnotate_PassesOtherErrorsThrough(t *testing.T) {
	original := errors.New("connection refused")
	if got := annotate(original); got != original {
		t.Errorf("annotate rewrote an unrelated error: %v", got)
	}
	if got := annotate(nil); got != nil {
		t.Errorf("annotate(nil) = %v, want nil", got)
	}
}

// TestWithMigrations_TogglesTheBootHook pins the Option wiring. The default
// must stay "migrate on connect" — that is the behaviour AutoMigrate had, and
// an upgrade that silently stopped migrating would surface much later as a
// missing-column error under traffic.
func TestWithMigrations_TogglesTheBootHook(t *testing.T) {
	if !defaultOptions().runMigrations {
		t.Error("migrations must be on by default")
	}

	opts := defaultOptions()
	WithMigrations(false)(&opts)
	if opts.runMigrations {
		t.Error("WithMigrations(false) did not disable migrations")
	}

	WithMigrations(true)(&opts)
	if !opts.runMigrations {
		t.Error("WithMigrations(true) did not re-enable migrations")
	}
}

// sqlStatements splits a migration body into statements, dropping comments and
// blank lines. Good enough for the shapes this project's migrations use; it is
// a lint helper, not a SQL parser.
func sqlStatements(body string) []string {
	var cleaned []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		cleaned = append(cleaned, trimmed)
	}
	var out []string
	for _, stmt := range strings.Split(strings.Join(cleaned, "\n"), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}
