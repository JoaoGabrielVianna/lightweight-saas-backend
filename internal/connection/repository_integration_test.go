//go:build integration

package connection

import (
	"context"
	"errors"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// These tests cover what only a real PostgreSQL can settle: the CHECK
// constraints, the partial unique index enforcing one active connection per
// workspace under genuine concurrency, and the migration's up/down behaviour.
//
// Each test gets a private schema (via search_path) that the real migrations
// are applied into, so the schema under test is the one the deploy produces.

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
	b.WriteString("conn_")
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

// seedWorkspace inserts a workspace so the foreign key is satisfiable.
func seedWorkspace(t *testing.T, db *gorm.DB) string {
	t.Helper()
	repo := workspace.NewRepository(db)

	id, err := publicid.New()
	if err != nil {
		t.Fatalf("generate id: %v", err)
	}
	now := time.Now().UTC()
	ws := &workspace.Workspace{
		ID: id, Slug: "ws-" + id[:8], Name: "Test Workspace",
		Status: workspace.StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Create(context.Background(), ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return id
}

func intTestKeyring(t *testing.T) *secrets.Keyring {
	t.Helper()
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	k, err := secrets.NewSingleVersionKeyring(1, key)
	if err != nil {
		t.Fatalf("new keyring: %v", err)
	}
	return k
}

// insertConnection creates a draft connection with a real sealed secret.
func insertConnection(t *testing.T, repo *PostgresRepository, k *secrets.Keyring, workspaceID, name string) *Connection {
	t.Helper()

	id, err := publicid.New()
	if err != nil {
		t.Fatalf("generate id: %v", err)
	}
	sealed, err := k.Seal([]byte("secret-for-"+name), secretAAD(id))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	c := &Connection{
		ID: id, WorkspaceID: workspaceID, Name: name,
		Provider: ProviderKeycloak, Status: StatusDraft,
		BaseURL: "https://kc.example.com", Realm: "saas", ClientID: "svc",
		Health: HealthUnknown, AccessMode: AccessModeUnknown,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Create(context.Background(), c, sealed); err != nil {
		t.Fatalf("create connection %q: %v", name, err)
	}
	return c
}

// markVerified stamps a healthy verification so a connection can be activated.
func markVerified(t *testing.T, repo *PostgresRepository, id string) {
	t.Helper()
	_, err := repo.SaveVerification(context.Background(), id, VerifyReport{
		OK: true, AccessMode: AccessModeFull, Summary: "ok",
		CheckedAt: time.Now().UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		t.Fatalf("save verification: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Migration
// ---------------------------------------------------------------------------

func TestMigration000003_CreatesTableConstraintsAndPartialIndex(t *testing.T) {
	db, dsn := newTestSchema(t)

	// Derived, not hard-coded: a literal version here is what broke the 000001
	// and 000002 suites when the next migration landed.
	latest, err := database.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if version, dirty, err := database.Version(dsn); err != nil || version != latest || dirty {
		t.Fatalf("after migrate: version=%d dirty=%t err=%v, want %d/false/nil", version, dirty, err, latest)
	}

	var constraints []string
	if err := db.Raw(
		`SELECT conname FROM pg_constraint
		 WHERE conrelid = (CURRENT_SCHEMA() || '.connections')::regclass AND contype = 'c'`,
	).Scan(&constraints).Error; err != nil {
		t.Fatalf("read constraints: %v", err)
	}
	for _, want := range []string{
		"connections_provider_check",
		"connections_status_check",
		"connections_health_check",
		"connections_access_mode_check",
		"connections_retired_at_check",
		"connections_activated_at_check",
		"connections_verified_at_check",
		"connections_secret_present_check",
		"connections_secret_key_version_check",
	} {
		if !contains(constraints, want) {
			t.Errorf("CHECK constraint %q missing (have %v)", want, constraints)
		}
	}

	// The foreign key to workspaces, with RESTRICT rather than CASCADE.
	var fkAction string
	if err := db.Raw(
		`SELECT confdeltype FROM pg_constraint
		 WHERE conrelid = (CURRENT_SCHEMA() || '.connections')::regclass AND contype = 'f'`,
	).Scan(&fkAction).Error; err != nil {
		t.Fatalf("read foreign key: %v", err)
	}
	if fkAction != "r" {
		t.Errorf("ON DELETE action = %q, want 'r' (RESTRICT) — a cascade would silently destroy credentials", fkAction)
	}

	// THE index: unique, partial, over workspace_id where status = 'active'.
	var indexDef string
	if err := db.Raw(
		`SELECT indexdef FROM pg_indexes
		 WHERE schemaname = CURRENT_SCHEMA() AND indexname = 'idx_connections_one_active_per_workspace'`,
	).Scan(&indexDef).Error; err != nil {
		t.Fatalf("read index: %v", err)
	}
	if indexDef == "" {
		t.Fatal("idx_connections_one_active_per_workspace is missing")
	}
	for _, want := range []string{"UNIQUE", "workspace_id", "WHERE", "active"} {
		if !strings.Contains(indexDef, want) {
			t.Errorf("index definition %q is missing %q", indexDef, want)
		}
	}
}

// TestMigration000003_DownPreservesEarlierMigrations is the rollback
// requirement: reverting 000003 removes only `connections`.
func TestMigration000003_DownPreservesEarlierMigrations(t *testing.T) {
	db, dsn := newTestSchema(t)

	workspaceID := seedWorkspace(t, db)
	repo := NewRepository(db)
	insertConnection(t, repo, intTestKeyring(t), workspaceID, "A")

	if err := db.Exec(
		`INSERT INTO users (keycloak_sub, email, username, created_at, updated_at)
		 VALUES ('baseline-probe', 'p@example.test', 'probe', now(), now())`,
	).Error; err != nil {
		t.Fatalf("seed users row: %v", err)
	}

	latest, err := database.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	// Revert down to just below 000003, so its own down migration is the one
	// under test whatever else has landed above it since.
	if err := database.MigrateSteps(dsn, -int(latest-2)); err != nil {
		t.Fatalf("revert 000003: %v", err)
	}
	if version, dirty, err := database.Version(dsn); err != nil || version != 2 || dirty {
		t.Fatalf("after down: version=%d dirty=%t err=%v, want 2/false/nil", version, dirty, err)
	}

	if tableExists(t, db, "connections") {
		t.Error("connections table survived the rollback")
	}
	for _, table := range []string{"workspaces", "users"} {
		if !tableExists(t, db, table) {
			t.Fatalf("%s was dropped — 000003's down must touch only its own table", table)
		}
	}

	var wsCount, userCount int64
	if err := db.Raw(`SELECT count(*) FROM workspaces`).Scan(&wsCount).Error; err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if err := db.Raw(`SELECT count(*) FROM users`).Scan(&userCount).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if wsCount != 1 || userCount != 1 {
		t.Errorf("earlier-migration data was lost: workspaces=%d users=%d", wsCount, userCount)
	}

	if err := database.Migrate(dsn); err != nil {
		t.Fatalf("re-apply 000003: %v", err)
	}
	if !tableExists(t, db, "connections") {
		t.Error("connections missing after re-applying 000003")
	}
}

// ---------------------------------------------------------------------------
// Constraints
// ---------------------------------------------------------------------------

// TestConstraints_RejectInvalidRows drives raw SQL past the service, which is
// exactly the path these constraints exist for.
func TestConstraints_RejectInvalidRows(t *testing.T) {
	db, _ := newTestSchema(t)
	workspaceID := seedWorkspace(t, db)

	insert := func(columns, values string) error {
		return db.Exec(
			`INSERT INTO connections (id, workspace_id, name, base_url, realm, client_id,
			   secret_ciphertext, secret_nonce`+columns+`)
			 VALUES (gen_random_uuid(), ?, 'n', 'https://k', 'r', 'c',
			   '\x01'::bytea, '\x02'::bytea`+values+`)`, workspaceID).Error
	}

	tests := map[string]func() error{
		"unknown provider": func() error { return insert(", provider", ", 'okta'") },
		"unknown status":   func() error { return insert(", status", ", 'deleted'") },
		"unknown health":   func() error { return insert(", health, last_verified_at", ", 'degraded', now()") },
		"unknown access":   func() error { return insert(", access_mode", ", 'partial'") },
		"retired no stamp": func() error { return insert(", status", ", 'retired'") },
		"draft with stamp": func() error { return insert(", retired_at", ", now()") },
		"active no stamp":  func() error { return insert(", status", ", 'active'") },
		"draft activated":  func() error { return insert(", activated_at", ", now()") },
		"healthy no stamp": func() error { return insert(", health", ", 'healthy'") },
		"unknown w/ stamp": func() error { return insert(", last_verified_at", ", now()") },
		"blank name":       func() error { return insert(", name", ", '  '") },
		"empty ciphertext": func() error { return insert(", secret_ciphertext", ", ''::bytea") },
		"empty nonce":      func() error { return insert(", secret_nonce", ", ''::bytea") },
		"key version zero": func() error { return insert(", secret_key_version", ", 0") },
		"unknown workspace": func() error {
			return db.Exec(`INSERT INTO connections (id, workspace_id, name, base_url, realm, client_id, secret_ciphertext, secret_nonce) VALUES (gen_random_uuid(), gen_random_uuid(), 'n', 'https://k', 'r', 'c', '\x01'::bytea, '\x02'::bytea)`).Error
		},
	}

	for name, stmt := range tests {
		t.Run(name, func(t *testing.T) {
			if err := stmt(); err == nil {
				t.Error("insert succeeded; the constraint did not fire")
			}
		})
	}
}

// TestConstraints_AcceptValidRows keeps the constraints from being so tight
// that legitimate states are impossible.
func TestConstraints_AcceptValidRows(t *testing.T) {
	db, _ := newTestSchema(t)
	workspaceID := seedWorkspace(t, db)

	valid := map[string][2]string{
		"minimal draft":              {"", ""},
		"healthy draft":              {", health, last_verified_at, access_mode", ", 'healthy', now(), 'full'"},
		"unhealthy draft":            {", health, last_verified_at", ", 'unhealthy', now()"},
		"limited access":             {", health, last_verified_at, access_mode", ", 'healthy', now(), 'limited'"},
		"active":                     {", status, activated_at", ", 'active', now()"},
		"retired from draft":         {", status, retired_at", ", 'retired', now()"},
		"retired keeps activated_at": {", status, activated_at, retired_at", ", 'retired', now(), now()"},
	}

	for name, cols := range valid {
		t.Run(name, func(t *testing.T) {
			err := db.Exec(
				`INSERT INTO connections (id, workspace_id, name, base_url, realm, client_id,
				   secret_ciphertext, secret_nonce`+cols[0]+`)
				 VALUES (gen_random_uuid(), ?, 'n', 'https://k', 'r', 'c',
				   '\x01'::bytea, '\x02'::bytea`+cols[1]+`)`, workspaceID).Error
			if err != nil {
				t.Errorf("valid insert refused: %v", err)
			}
		})
	}
}

// TestPartialIndex_OneActivePerWorkspace is THE invariant of this table.
func TestPartialIndex_OneActivePerWorkspace(t *testing.T) {
	db, _ := newTestSchema(t)
	workspaceA := seedWorkspace(t, db)

	insertActive := func(workspaceID, name string) error {
		return db.Exec(
			`INSERT INTO connections (id, workspace_id, name, base_url, realm, client_id,
			   secret_ciphertext, secret_nonce, status, activated_at)
			 VALUES (gen_random_uuid(), ?, ?, 'https://k', 'r', 'c',
			   '\x01'::bytea, '\x02'::bytea, 'active', now())`, workspaceID, name).Error
	}

	if err := insertActive(workspaceA, "first"); err != nil {
		t.Fatalf("first active insert: %v", err)
	}
	if err := insertActive(workspaceA, "second"); err == nil {
		t.Fatal("a second active connection was accepted in the same workspace")
	}

	// Drafts and retired rows must not compete for the slot.
	for _, status := range []string{"draft", "retired"} {
		extra := ", status"
		values := ", '" + status + "'"
		if status == "retired" {
			extra += ", retired_at"
			values += ", now()"
		}
		err := db.Exec(
			`INSERT INTO connections (id, workspace_id, name, base_url, realm, client_id,
			   secret_ciphertext, secret_nonce`+extra+`)
			 VALUES (gen_random_uuid(), ?, 'extra-`+status+`', 'https://k', 'r', 'c',
			   '\x01'::bytea, '\x02'::bytea`+values+`)`, workspaceA).Error
		if err != nil {
			t.Errorf("a %s connection was refused; the index must be partial: %v", status, err)
		}
	}

	// A different workspace has its own slot.
	workspaceB := seedWorkspace(t, db)
	if err := insertActive(workspaceB, "other-workspace"); err != nil {
		t.Errorf("another workspace could not activate: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Repository
// ---------------------------------------------------------------------------

func TestRepository_CreateReadAndOpenSecret(t *testing.T) {
	db, _ := newTestSchema(t)
	workspaceID := seedWorkspace(t, db)
	repo := NewRepository(db)
	keyring := intTestKeyring(t)

	created := insertConnection(t, repo, keyring, workspaceID, "Prod")

	got, err := repo.GetByID(context.Background(), created.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != StatusDraft || got.Provider != ProviderKeycloak {
		t.Errorf("status/provider = %q/%q", got.Status, got.Provider)
	}
	if got.Health != HealthUnknown || got.LastVerifiedAt != nil {
		t.Errorf("a new connection must be unverified: %q / %v", got.Health, got.LastVerifiedAt)
	}

	// The secret round-trips through bytea and opens only with the right AAD.
	sealed, err := repo.OpenSecret(context.Background(), created.ID)
	if err != nil || sealed == nil {
		t.Fatalf("OpenSecret: %v", err)
	}
	opened, err := keyring.Open(*sealed, secretAAD(created.ID))
	if err != nil {
		t.Fatalf("open stored secret: %v", err)
	}
	if string(opened) != "secret-for-Prod" {
		t.Errorf("opened secret = %q", opened)
	}
	if _, err := keyring.Open(*sealed, secretAAD("another-connection")); err == nil {
		t.Error("the stored secret opened under the wrong AAD")
	}
	// The stored version is the keyring's CURRENT one, read from the keyring
	// rather than from a constant: "which version new secrets carry" is a
	// property of the configured keyring now, and a constant here would keep
	// passing after rotation made it wrong.
	if sealed.Algorithm != secrets.AlgorithmAESGCM || sealed.KeyVersion != keyring.CurrentVersion() {
		t.Errorf("stored metadata = %q/%d, want %q/%d",
			sealed.Algorithm, sealed.KeyVersion, secrets.AlgorithmAESGCM, keyring.CurrentVersion())
	}
}

func TestRepository_MissingRowsReturnNilNil(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)

	id, _ := publicid.New()
	if got, err := repo.GetByID(context.Background(), id); err != nil || got != nil {
		t.Errorf("GetByID = %v, %v; want nil, nil", got, err)
	}
	if got, err := repo.OpenSecret(context.Background(), id); err != nil || got != nil {
		t.Errorf("OpenSecret = %v, %v; want nil, nil", got, err)
	}
}

func TestRepository_ListScopedToWorkspace(t *testing.T) {
	db, _ := newTestSchema(t)
	repo := NewRepository(db)
	keyring := intTestKeyring(t)

	workspaceA := seedWorkspace(t, db)
	workspaceB := seedWorkspace(t, db)
	insertConnection(t, repo, keyring, workspaceA, "A1")
	insertConnection(t, repo, keyring, workspaceA, "A2")
	insertConnection(t, repo, keyring, workspaceB, "B1")

	items, err := repo.List(context.Background(), workspaceA, FilterAll)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 — the listing must be workspace-scoped", len(items))
	}
	if items[0].Name != "A1" || items[1].Name != "A2" {
		t.Errorf("ordering is wrong: %+v", items)
	}
}

func TestRepository_UpdateConfigResetsVerification(t *testing.T) {
	db, _ := newTestSchema(t)
	workspaceID := seedWorkspace(t, db)
	repo := NewRepository(db)
	c := insertConnection(t, repo, intTestKeyring(t), workspaceID, "Prod")
	markVerified(t, repo, c.ID)

	newRealm := "other"
	updated, err := repo.UpdateConfig(context.Background(), c.ID,
		ConfigPatch{Realm: &newRealm}, nil, time.Now().UTC())
	if err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if updated.Realm != "other" {
		t.Errorf("realm = %q", updated.Realm)
	}
	if updated.Health != HealthUnknown || updated.LastVerifiedAt != nil || updated.AccessMode != AccessModeUnknown {
		t.Errorf("changing a probed field must reset the verification: health=%q at=%v mode=%q",
			updated.Health, updated.LastVerifiedAt, updated.AccessMode)
	}
}

func TestRepository_UpdateConfigRefusesNonDraft(t *testing.T) {
	db, _ := newTestSchema(t)
	workspaceID := seedWorkspace(t, db)
	repo := NewRepository(db)

	active := insertConnection(t, repo, intTestKeyring(t), workspaceID, "Active")
	markVerified(t, repo, active.ID)
	if _, err := repo.Activate(context.Background(), active.ID, workspaceID, time.Now().UTC()); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	name := "renamed"
	if _, err := repo.UpdateConfig(context.Background(), active.ID, ConfigPatch{Name: &name}, nil, time.Now().UTC()); !errors.Is(err, ErrNotDraft) {
		t.Errorf("UpdateConfig on active = %v, want ErrNotDraft", err)
	}
}

func TestRepository_ActivateRetiresIncumbentAtomically(t *testing.T) {
	db, _ := newTestSchema(t)
	workspaceID := seedWorkspace(t, db)
	repo := NewRepository(db)
	keyring := intTestKeyring(t)

	first := insertConnection(t, repo, keyring, workspaceID, "First")
	markVerified(t, repo, first.ID)
	if _, err := repo.Activate(context.Background(), first.ID, workspaceID, time.Now().UTC()); err != nil {
		t.Fatalf("activate first: %v", err)
	}

	second := insertConnection(t, repo, keyring, workspaceID, "Second")
	markVerified(t, repo, second.ID)
	activated, err := repo.Activate(context.Background(), second.ID, workspaceID, time.Now().UTC())
	if err != nil {
		t.Fatalf("activate second: %v", err)
	}
	if activated.Status != StatusActive {
		t.Errorf("status = %q", activated.Status)
	}

	old, err := repo.GetByID(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("reload first: %v", err)
	}
	if old.Status != StatusRetired || old.RetiredAt == nil {
		t.Errorf("the incumbent is %q (retired_at=%v), want retired", old.Status, old.RetiredAt)
	}

	// And the invariant still holds at the database level.
	var activeCount int64
	if err := db.Raw(`SELECT count(*) FROM connections WHERE workspace_id = ? AND status = 'active'`, workspaceID).
		Scan(&activeCount).Error; err != nil {
		t.Fatalf("count active: %v", err)
	}
	if activeCount != 1 {
		t.Errorf("%d active connections, want exactly 1", activeCount)
	}
}

// TestRepository_ConcurrentActivate is the case only a real database settles:
// several connections in one workspace racing to activate. The partial unique
// index must leave exactly one winner, and no run may end with two active rows.
func TestRepository_ConcurrentActivate(t *testing.T) {
	db, _ := newTestSchema(t)
	workspaceID := seedWorkspace(t, db)
	repo := NewRepository(db)
	keyring := intTestKeyring(t)

	const racers = 6
	ids := make([]string, 0, racers)
	for i := 0; i < racers; i++ {
		c := insertConnection(t, repo, keyring, workspaceID, "C"+string(rune('A'+i)))
		markVerified(t, repo, c.ID)
		ids = append(ids, c.ID)
	}

	start := make(chan struct{})
	results := make(chan error, racers)
	for _, id := range ids {
		go func() {
			<-start
			_, err := repo.Activate(context.Background(), id, workspaceID, time.Now().UTC())
			results <- err
		}()
	}
	close(start)

	var succeeded int
	for i := 0; i < racers; i++ {
		if err := <-results; err == nil {
			succeeded++
		}
	}
	if succeeded == 0 {
		t.Fatal("every racer failed; at least one activation must win")
	}

	// The invariant that actually matters, whatever the interleaving was.
	var activeCount int64
	if err := db.Raw(`SELECT count(*) FROM connections WHERE workspace_id = ? AND status = 'active'`, workspaceID).
		Scan(&activeCount).Error; err != nil {
		t.Fatalf("count active: %v", err)
	}
	if activeCount != 1 {
		t.Errorf("%d active connections after a concurrent activation, want exactly 1", activeCount)
	}
}

func TestRepository_Retire(t *testing.T) {
	db, _ := newTestSchema(t)
	workspaceID := seedWorkspace(t, db)
	repo := NewRepository(db)

	c := insertConnection(t, repo, intTestKeyring(t), workspaceID, "Prod")
	retired, err := repo.Retire(context.Background(), c.ID, time.Now().UTC())
	if err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if retired.Status != StatusRetired || retired.RetiredAt == nil {
		t.Errorf("status/retired_at = %q/%v", retired.Status, retired.RetiredAt)
	}

	if _, err := repo.Retire(context.Background(), c.ID, time.Now().UTC()); !errors.Is(err, ErrRetired) {
		t.Errorf("re-retiring = %v, want ErrRetired", err)
	}
}

func TestRepository_Delete(t *testing.T) {
	db, _ := newTestSchema(t)
	workspaceID := seedWorkspace(t, db)
	repo := NewRepository(db)
	keyring := intTestKeyring(t)

	t.Run("draft", func(t *testing.T) {
		c := insertConnection(t, repo, keyring, workspaceID, "Draft")
		if err := repo.Delete(context.Background(), c.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		got, _ := repo.GetByID(context.Background(), c.ID)
		if got != nil {
			t.Error("the connection survived the delete")
		}
		// The sealed credential must go with the row.
		sealed, _ := repo.OpenSecret(context.Background(), c.ID)
		if sealed != nil {
			t.Error("the sealed secret outlived the connection row")
		}
	})

	t.Run("retired", func(t *testing.T) {
		c := insertConnection(t, repo, keyring, workspaceID, "Retired")
		if _, err := repo.Retire(context.Background(), c.ID, time.Now().UTC()); err != nil {
			t.Fatalf("Retire: %v", err)
		}
		if err := repo.Delete(context.Background(), c.ID); err != nil {
			t.Errorf("Delete on retired: %v", err)
		}
	})

	t.Run("active is refused at the SQL level", func(t *testing.T) {
		c := insertConnection(t, repo, keyring, workspaceID, "Active")
		markVerified(t, repo, c.ID)
		if _, err := repo.Activate(context.Background(), c.ID, workspaceID, time.Now().UTC()); err != nil {
			t.Fatalf("Activate: %v", err)
		}

		if err := repo.Delete(context.Background(), c.ID); !errors.Is(err, ErrActiveCannotDelete) {
			t.Errorf("Delete on active = %v, want ErrActiveCannotDelete", err)
		}
		got, _ := repo.GetByID(context.Background(), c.ID)
		if got == nil {
			t.Fatal("the active connection was deleted despite the guard")
		}

		// Clean up so the workspace's active slot is free for later subtests.
		if _, err := repo.Retire(context.Background(), c.ID, time.Now().UTC()); err != nil {
			t.Fatalf("cleanup retire: %v", err)
		}
	})
}

// TestRepository_WorkspaceDeleteIsRestricted covers the ON DELETE RESTRICT
// choice: credentials must not vanish because someone removed a workspace row
// by hand.
func TestRepository_WorkspaceDeleteIsRestricted(t *testing.T) {
	db, _ := newTestSchema(t)
	workspaceID := seedWorkspace(t, db)
	repo := NewRepository(db)
	insertConnection(t, repo, intTestKeyring(t), workspaceID, "Prod")

	if err := db.Exec(`DELETE FROM workspaces WHERE id = ?`, workspaceID).Error; err == nil {
		t.Error("deleting a workspace with connections succeeded; RESTRICT is not in force")
	}
}

// TestRepository_SaveVerification covers both verdicts and the constraint that
// health and last_verified_at move together.
func TestRepository_SaveVerification(t *testing.T) {
	db, _ := newTestSchema(t)
	workspaceID := seedWorkspace(t, db)
	repo := NewRepository(db)
	c := insertConnection(t, repo, intTestKeyring(t), workspaceID, "Prod")

	at := time.Now().UTC().Truncate(time.Microsecond)
	healthy, err := repo.SaveVerification(context.Background(), c.ID, VerifyReport{
		OK: true, AccessMode: AccessModeFull, Summary: "all good", CheckedAt: at,
	})
	if err != nil {
		t.Fatalf("SaveVerification: %v", err)
	}
	if healthy.Health != HealthHealthy || healthy.AccessMode != AccessModeFull {
		t.Errorf("health/mode = %q/%q", healthy.Health, healthy.AccessMode)
	}
	if healthy.LastVerifiedAt == nil || !healthy.LastVerifiedAt.Equal(at) {
		t.Errorf("last_verified_at = %v, want %v", healthy.LastVerifiedAt, at)
	}
	if healthy.HealthMessage != "all good" {
		t.Errorf("health_message = %q", healthy.HealthMessage)
	}

	later := at.Add(time.Minute)
	unhealthy, err := repo.SaveVerification(context.Background(), c.ID, VerifyReport{
		OK: false, AccessMode: AccessModeUnknown, Summary: "credentials rejected", CheckedAt: later,
	})
	if err != nil {
		t.Fatalf("SaveVerification (failure): %v", err)
	}
	if unhealthy.Health != HealthUnhealthy {
		t.Errorf("health = %q, want unhealthy", unhealthy.Health)
	}
	if unhealthy.IsVerified(later) {
		t.Error("a failed verification must clear the verified state")
	}
}

// ---------------------------------------------------------------------------
// Service against a real database
// ---------------------------------------------------------------------------

// TestService_ArchivedWorkspaceBlocksActivate is the cross-domain rule, checked
// end to end against real rows rather than against a fake workspace store.
func TestService_ArchivedWorkspaceBlocksActivate(t *testing.T) {
	db, _ := newTestSchema(t)
	workspaceID := seedWorkspace(t, db)

	repo := NewRepository(db)
	keyring := intTestKeyring(t)
	c := insertConnection(t, repo, keyring, workspaceID, "Prod")
	markVerified(t, repo, c.ID)

	wsRepo := workspace.NewRepository(db)
	if _, err := wsRepo.Archive(context.Background(), workspaceID, time.Now().UTC()); err != nil {
		t.Fatalf("archive workspace: %v", err)
	}

	svc := NewService(repo, wsRepo, keyring, &fakeVerifier{report: okReport()}, &fakeRunner{}, &fakeAuditWriter{})
	if svc == nil {
		t.Fatal("NewService returned nil")
	}

	_, err := svc.Activate(context.Background(),
		publicid.Format(publicid.WorkspacePrefix, workspaceID),
		publicid.Format(publicid.ConnectionPrefix, c.ID), testEvent(audit.ActionConnectionActivated))
	if !errors.Is(err, ErrWorkspaceArchived) {
		t.Fatalf("Activate in an archived workspace = %v, want ErrWorkspaceArchived", err)
	}

	// And nothing was written.
	after, _ := repo.GetByID(context.Background(), c.ID)
	if after.Status != StatusDraft {
		t.Errorf("status = %q, want it unchanged", after.Status)
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
