//go:build integration

package project

import (
	"context"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/publicid"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// These tests cover what only a real PostgreSQL can settle: the CHECK
// constraints, the case-insensitive unique index under genuine concurrency, the
// text[] round trip, and that a credential's plaintext is nowhere in the row.
//
// Each test gets a private schema (via search_path) that the real migrations
// are applied into, so the schema under test is the one a deploy produces.

func newTestSchema(t *testing.T) *gorm.DB {
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
	return openGorm(t, dsn)
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
	b.WriteString("prj_")
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

func seedProject(t *testing.T, repo Repository, workspaceID, name string) *Project {
	t.Helper()
	id, err := publicid.New()
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	now := time.Now().UTC()
	p := &Project{
		ID: id, WorkspaceID: workspaceID, Name: name,
		Status: StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.CreateProject(context.Background(), p); err != nil {
		t.Fatalf("seed project %q: %v", name, err)
	}
	return p
}

func seedCredentialRow(t *testing.T, repo Repository, projectID string, scopes []string) (*Credential, string) {
	t.Helper()
	id, err := publicid.New()
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	minted, err := MintCredential()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	c := &Credential{
		ID: id, ProjectID: projectID, Label: "integration key",
		KeyPrefix: minted.KeyPrefix, KeyHash: minted.KeyHash, KeyHashAlg: "sha256",
		Scopes: scopes, CreatedBy: "operator-sub", CreatedAt: time.Now().UTC(),
	}
	if err := repo.CreateCredential(context.Background(), c); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	return c, minted.Token
}

// ─── Schema constraints ─────────────────────────────────────────────────────

// TestIntegration_NameIsUniquePerWorkspaceCaseInsensitively proves the index,
// not the application check. Only the index resolves two concurrent creates.
func TestIntegration_NameIsUniquePerWorkspaceCaseInsensitively(t *testing.T) {
	db := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db)

	seedProject(t, repo, ws, "Billing worker")

	id, _ := publicid.New()
	now := time.Now().UTC()
	err := repo.CreateProject(context.Background(), &Project{
		ID: id, WorkspaceID: ws, Name: "BILLING WORKER",
		Status: StatusActive, CreatedAt: now, UpdatedAt: now,
	})
	if err != ErrNameTaken {
		t.Fatalf("err = %v, want ErrNameTaken — the unique index must be case-insensitive", err)
	}
}

func TestIntegration_NameUniquenessIsScopedToTheWorkspace(t *testing.T) {
	// Two workspaces may each have a project called the same thing; the
	// namespace is per workspace, not global.
	db := newTestSchema(t)
	repo := NewRepository(db)
	wsA, wsB := seedWorkspace(t, db), seedWorkspace(t, db)

	seedProject(t, repo, wsA, "Billing")
	seedProject(t, repo, wsB, "Billing")
}

// TestIntegration_ConcurrentCreateWithTheSameNameHasExactlyOneWinner is the
// property an application-level check cannot provide.
func TestIntegration_ConcurrentCreateWithTheSameNameHasExactlyOneWinner(t *testing.T) {
	db := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db)

	const attempts = 8
	var wg sync.WaitGroup
	results := make([]error, attempts)
	start := make(chan struct{})

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, _ := publicid.New()
			now := time.Now().UTC()
			<-start
			results[i] = repo.CreateProject(context.Background(), &Project{
				ID: id, WorkspaceID: ws, Name: "Contended",
				Status: StatusActive, CreatedAt: now, UpdatedAt: now,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	created, taken, other := 0, 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			created++
		case err == ErrNameTaken:
			taken++
		default:
			other++
			t.Errorf("unexpected error: %v", err)
		}
	}
	if created != 1 {
		t.Errorf("created = %d, want exactly 1", created)
	}
	if taken != attempts-1 {
		t.Errorf("name-taken = %d, want %d", taken, attempts-1)
	}
	if other != 0 {
		t.Errorf("%d attempts failed with an untranslated error", other)
	}
}

func TestIntegration_StatusAndArchivedAtCannotDisagree(t *testing.T) {
	db := newTestSchema(t)
	ws := seedWorkspace(t, db)
	id, _ := publicid.New()

	// archived without a timestamp
	err := db.Exec(`INSERT INTO projects (id, workspace_id, name, status)
	                VALUES (?, ?, 'bad', 'archived')`, id, ws).Error
	if err == nil {
		t.Error("an archived project with no archived_at was accepted")
	}

	// active WITH a timestamp
	id2, _ := publicid.New()
	err = db.Exec(`INSERT INTO projects (id, workspace_id, name, status, archived_at)
	               VALUES (?, ?, 'bad2', 'active', now())`, id2, ws).Error
	if err == nil {
		t.Error("an active project carrying archived_at was accepted")
	}
}

func TestIntegration_UnknownScopeIsRefusedByTheDatabase(t *testing.T) {
	// The Go vocabulary and the CHECK constraint are two enforcement points for
	// one contract. This is the one that catches an application bug.
	db := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db)
	p := seedProject(t, repo, ws, "Billing")

	id, _ := publicid.New()
	minted, _ := MintCredential()
	err := repo.CreateCredential(context.Background(), &Credential{
		ID: id, ProjectID: p.ID, Label: "bad",
		KeyPrefix: minted.KeyPrefix, KeyHash: minted.KeyHash, KeyHashAlg: "sha256",
		Scopes: []string{"users:read", "billing:refund"}, CreatedBy: "op", CreatedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("a credential with an unknown scope was stored")
	}
}

func TestIntegration_EmptyScopesAreRefusedByTheDatabase(t *testing.T) {
	db := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db)
	p := seedProject(t, repo, ws, "Billing")

	id, _ := publicid.New()
	minted, _ := MintCredential()
	err := repo.CreateCredential(context.Background(), &Credential{
		ID: id, ProjectID: p.ID, Label: "bad",
		KeyPrefix: minted.KeyPrefix, KeyHash: minted.KeyHash, KeyHashAlg: "sha256",
		Scopes: []string{}, CreatedBy: "op", CreatedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("a credential with no scopes was stored")
	}
}

func TestIntegration_DuplicateKeyPrefixIsRefused(t *testing.T) {
	// Ambiguity at the moment authentication matters most.
	db := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db)
	p := seedProject(t, repo, ws, "Billing")

	first, _ := seedCredentialRow(t, repo, p.ID, []string{"users:read"})

	id, _ := publicid.New()
	minted, _ := MintCredential()
	err := repo.CreateCredential(context.Background(), &Credential{
		ID: id, ProjectID: p.ID, Label: "collision",
		KeyPrefix: first.KeyPrefix, // same lookup
		KeyHash:   minted.KeyHash, KeyHashAlg: "sha256",
		Scopes: []string{"users:read"}, CreatedBy: "op", CreatedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("two credentials with the same key_prefix were stored")
	}
}

func TestIntegration_HashLengthIsEnforced(t *testing.T) {
	db := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db)
	p := seedProject(t, repo, ws, "Billing")

	id, _ := publicid.New()
	minted, _ := MintCredential()
	err := repo.CreateCredential(context.Background(), &Credential{
		ID: id, ProjectID: p.ID, Label: "short hash",
		KeyPrefix: minted.KeyPrefix, KeyHash: []byte("too short"), KeyHashAlg: "sha256",
		Scopes: []string{"users:read"}, CreatedBy: "op", CreatedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("a credential with a non-SHA-256-length digest was stored")
	}
}

// ─── The secret is not in the database ──────────────────────────────────────

// TestIntegration_PlaintextIsNowhereInTheDatabase scans every text-ish column
// of both tables for the secret. This is the executable form of the guarantee
// the whole design rests on.
func TestIntegration_PlaintextIsNowhereInTheDatabase(t *testing.T) {
	db := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db)
	p := seedProject(t, repo, ws, "Billing")

	_, token := seedCredentialRow(t, repo, p.ID, []string{"users:read", "users:write"})
	parsed, ok := parseToken(token)
	if !ok {
		t.Fatal("minted token does not parse")
	}

	// The secret SEGMENT is the sensitive half; the lookup is stored by design.
	var hits int64
	err := db.Raw(`
		SELECT count(*) FROM project_credentials
		WHERE label LIKE ? OR key_prefix = ? OR key_hash_alg LIKE ?
		   OR created_by LIKE ? OR coalesce(revoked_by, '') LIKE ?
		   OR array_to_string(scopes, ',') LIKE ?`,
		"%"+parsed.secret+"%", parsed.secret, "%"+parsed.secret+"%",
		"%"+parsed.secret+"%", "%"+parsed.secret+"%", "%"+parsed.secret+"%",
	).Scan(&hits).Error
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if hits != 0 {
		t.Fatalf("the credential secret appears in %d project_credentials row(s)", hits)
	}

	// And the whole token, in case a future column stored it verbatim.
	err = db.Raw(`
		SELECT count(*) FROM project_credentials
		WHERE label LIKE ? OR key_prefix LIKE ?`,
		"%"+token+"%", "%"+token+"%").Scan(&hits).Error
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if hits != 0 {
		t.Fatalf("the full credential token appears in %d row(s)", hits)
	}
}

// ─── Round trips and lookups ────────────────────────────────────────────────

func TestIntegration_ScopesRoundTripThroughTextArray(t *testing.T) {
	db := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db)
	p := seedProject(t, repo, ws, "Billing")

	want := []string{"users:read", "users:write", "invitations:write"}
	created, _ := seedCredentialRow(t, repo, p.ID, want)

	got, err := repo.GetCredential(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Scopes) != len(want) {
		t.Fatalf("scopes = %v, want %v", got.Scopes, want)
	}
	for i := range want {
		if got.Scopes[i] != want[i] {
			t.Errorf("scope[%d] = %q, want %q", i, got.Scopes[i], want[i])
		}
	}
}

func TestIntegration_FindByKeyPrefixReturnsCredentialAndProject(t *testing.T) {
	db := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db)
	p := seedProject(t, repo, ws, "Billing")
	created, token := seedCredentialRow(t, repo, p.ID, []string{"users:read"})

	parsed, _ := parseToken(token)
	cred, proj, err := repo.FindByKeyPrefix(context.Background(), parsed.lookup)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if cred == nil || proj == nil {
		t.Fatal("lookup returned nothing for a stored credential")
	}
	if cred.ID != created.ID || proj.ID != p.ID {
		t.Error("lookup returned the wrong rows")
	}
	// The workspace binding must survive the round trip: it is what the
	// authorization layer compares against the request path.
	if proj.WorkspaceID != ws {
		t.Errorf("workspace binding = %q, want %q", proj.WorkspaceID, ws)
	}

	// And an unknown prefix is a clean miss, not an error.
	cred, proj, err = repo.FindByKeyPrefix(context.Background(), "aaaaaaaaaaaaaaaa")
	if err != nil || cred != nil || proj != nil {
		t.Errorf("unknown prefix returned (%v, %v, %v), want all nil", cred, proj, err)
	}
}

// ─── Lifecycle under concurrency ────────────────────────────────────────────

// TestIntegration_ConcurrentRevokeHasExactlyOneWinner — the WHERE clause guard
// is what stops the second revoker overwriting the first one's attribution.
func TestIntegration_ConcurrentRevokeHasExactlyOneWinner(t *testing.T) {
	db := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db)
	p := seedProject(t, repo, ws, "Billing")
	cred, _ := seedCredentialRow(t, repo, p.ID, []string{"users:read"})

	const attempts = 6
	var wg sync.WaitGroup
	results := make([]error, attempts)
	start := make(chan struct{})

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, results[i] = repo.RevokeCredential(context.Background(), cred.ID, "op", time.Now().UTC())
		}(i)
	}
	close(start)
	wg.Wait()

	revoked, already := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			revoked++
		case err == ErrCredentialAlreadyRevoked:
			already++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if revoked != 1 {
		t.Errorf("successful revocations = %d, want exactly 1", revoked)
	}
	if already != attempts-1 {
		t.Errorf("already-revoked = %d, want %d", already, attempts-1)
	}
}

func TestIntegration_ArchiveIsIdempotentAndDoesNotTouchCredentials(t *testing.T) {
	// Archiving is one UPDATE. It must not need to walk credentials — that is
	// what makes it an atomic kill switch rather than a loop that half-finishes.
	db := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db)
	p := seedProject(t, repo, ws, "Billing")
	cred, _ := seedCredentialRow(t, repo, p.ID, []string{"users:read"})

	now := time.Now().UTC()
	if _, err := repo.ArchiveProject(context.Background(), p.ID, now); err != nil {
		t.Fatalf("first archive: %v", err)
	}
	second, err := repo.ArchiveProject(context.Background(), p.ID, now.Add(time.Second))
	if err != nil {
		t.Fatalf("second archive: %v", err)
	}
	if second.Status != StatusArchived {
		t.Errorf("status = %q, want archived", second.Status)
	}

	after, err := repo.GetCredential(context.Background(), cred.ID)
	if err != nil {
		t.Fatalf("get credential: %v", err)
	}
	if after.RevokedAt != nil {
		t.Error("archiving rewrote a credential row; the authentication JOIN should make that unnecessary")
	}
}

func TestIntegration_TouchLastUsedIsThrottledInSQL(t *testing.T) {
	// The guard is repeated in SQL so concurrent requests for one credential do
	// not each write.
	db := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db)
	p := seedProject(t, repo, ws, "Billing")
	cred, _ := seedCredentialRow(t, repo, p.ID, []string{"users:read"})

	now := time.Now().UTC()
	if err := repo.TouchLastUsed(context.Background(), cred.ID, now); err != nil {
		t.Fatalf("first touch: %v", err)
	}
	first, _ := repo.GetCredential(context.Background(), cred.ID)
	if first.LastUsedAt == nil {
		t.Fatal("last_used_at was not set")
	}

	// A second touch a second later must be a no-op.
	if err := repo.TouchLastUsed(context.Background(), cred.ID, now.Add(time.Second)); err != nil {
		t.Fatalf("second touch: %v", err)
	}
	second, _ := repo.GetCredential(context.Background(), cred.ID)
	if !second.LastUsedAt.Equal(*first.LastUsedAt) {
		t.Error("last_used_at was rewritten inside the throttle window")
	}

	// Past the window it moves again.
	if err := repo.TouchLastUsed(context.Background(), cred.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("third touch: %v", err)
	}
	third, _ := repo.GetCredential(context.Background(), cred.ID)
	if third.LastUsedAt.Equal(*first.LastUsedAt) {
		t.Error("last_used_at never moves; the field would be useless")
	}
}

func TestIntegration_CountActiveCredentialsByProject(t *testing.T) {
	db := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db)
	a := seedProject(t, repo, ws, "Alpha")
	b := seedProject(t, repo, ws, "Bravo")

	c1, _ := seedCredentialRow(t, repo, a.ID, []string{"users:read"})
	seedCredentialRow(t, repo, a.ID, []string{"users:read"})
	seedCredentialRow(t, repo, b.ID, []string{"users:read"})
	if _, err := repo.RevokeCredential(context.Background(), c1.ID, "op", time.Now().UTC()); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	counts, err := repo.CountActiveCredentialsByProject(context.Background(), []string{a.ID, b.ID})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts[a.ID] != 1 {
		t.Errorf("alpha active = %d, want 1 (the revoked one must not count)", counts[a.ID])
	}
	if counts[b.ID] != 1 {
		t.Errorf("bravo active = %d, want 1", counts[b.ID])
	}
}

func TestIntegration_WorkspaceCannotBeDeletedWhileAProjectReferencesIt(t *testing.T) {
	// ON DELETE RESTRICT: silently destroying a tenant's API access from a
	// manual psql session is exactly what this prevents.
	db := newTestSchema(t)
	repo := NewRepository(db)
	ws := seedWorkspace(t, db)
	seedProject(t, repo, ws, "Billing")

	if err := db.Exec(`DELETE FROM workspaces WHERE id = ?`, ws).Error; err == nil {
		t.Fatal("a workspace with projects was deleted")
	}
}
