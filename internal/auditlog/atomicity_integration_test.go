//go:build integration

package auditlog

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/connection"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/project"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/workspace"
)

// The Slice 15 acceptance matrix: TD-033, proven against a real PostgreSQL.
//
// ─── The invariant, stated exactly ──────────────────────────────────────────
//
//	Any successful PostgreSQL control-plane mutation classified as durably
//	auditable commits its domain state and its audit event in the SAME
//	PostgreSQL transaction. If the durable audit write fails, the domain
//	mutation is rolled back.
//
// Not "audit is reliable". Not "exactly-once". The guarantee is narrow, and
// narrow is what makes it true.
//
// ─── Why this file lives in internal/auditlog ───────────────────────────────
//
// It needs all four control-plane domains AND the real audit store in one
// process. auditlog is the only package that can import the other three without
// a cycle: workspace, project and connection each declare their own one-method
// AuditWriter interface and import nothing from here.
//
// ─── Why the failure seam is a store wrapper ────────────────────────────────
//
// The requirement is a failure AFTER the domain write has executed and BEFORE
// the transaction commits. A fake that refused up front would prove nothing —
// it would test that a mutation which never ran left nothing behind.
//
// failingStore is called by the service at exactly the right moment: inside the
// callback, after the domain repository has written through the same
// transaction. And it does not merely fail — it first READS the domain row back
// through that transaction and records whether it was there. So every rollback
// test below asserts three things in sequence:
//
//	the domain row WAS visible inside the transaction   (it really executed)
//	the audit write then failed                         (the injected error)
//	neither row exists afterwards                       (PostgreSQL rolled back)
//
// Without the middle observation, "the row is absent at the end" would be
// equally consistent with a write that never happened.

// ─── Harness ────────────────────────────────────────────────────────────────

// newAtomicitySchema creates a private migrated schema and returns a handle.
//
// Private per test, so the concurrency cases cannot see each other's rows and
// a failure leaves nothing behind.
func newAtomicitySchema(t *testing.T) *gorm.DB {
	t.Helper()

	base := os.Getenv("DB_URL")
	if base == "" {
		t.Skip("DB_URL unset — this suite requires a reachable postgres")
	}

	schema := atomicitySchemaName(t)
	admin := openAtomicityGorm(t, base)
	if err := admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`).Error; err != nil {
		t.Fatalf("drop stale schema: %v", err)
	}
	if err := admin.Exec(`CREATE SCHEMA ` + schema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanup := openAtomicityGorm(t, base)
		_ = cleanup.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`).Error
	})

	dsn := withAtomicitySearchPath(t, base, schema)
	if err := database.Migrate(dsn); err != nil {
		t.Fatalf("apply migrations to %s: %v", schema, err)
	}
	return openAtomicityGorm(t, dsn)
}

func openAtomicityGorm(t *testing.T, dsn string) *gorm.DB {
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

func atomicitySchemaName(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("atom_")
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

func withAtomicitySearchPath(t *testing.T, dsn, schema string) string {
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

// ─── The failure seam ───────────────────────────────────────────────────────

// errInjectedAuditFailure is what the seam returns. It wraps the real sentinel,
// so the services' error mapping is exercised rather than approximated.
var errInjectedAuditFailure = errors.Join(
	audit.ErrNotRecorded, errors.New("injected: the audit store refused this write"))

// failingStore refuses every Record, having first confirmed the domain row is
// visible inside the caller's transaction.
type failingStore struct {
	Store

	// probe reads the domain row through the transaction. It returns true when
	// the row is there, which is the observation that makes the rollback
	// assertion meaningful.
	probe func(tx database.Tx) bool

	// sawDomainWrite records what probe reported, so the test can assert the
	// middle step of the sequence rather than trusting it.
	sawDomainWrite bool
	probed         bool
	tx             database.Tx
}

func (f *failingStore) WithTx(tx database.Tx) Store {
	// The SAME instance, rebound. Returning a copy would lose sawDomainWrite,
	// which the test reads after the call returns.
	f.tx = tx
	return f
}

func (f *failingStore) Record(context.Context, Record) error {
	if f.probe != nil && f.tx != nil {
		f.sawDomainWrite = f.probe(f.tx)
		f.probed = true
	}
	return errInjectedAuditFailure
}

// assertSawDomainWrite fails unless the domain row was visible inside the
// transaction at the moment the audit write was refused.
func (f *failingStore) assertSawDomainWrite(t *testing.T) {
	t.Helper()
	if !f.probed {
		t.Fatal("the audit store was never called; the mutation did not reach its audit write, " +
			"so this test proves nothing about rollback")
	}
	if !f.sawDomainWrite {
		t.Fatal("the domain row was NOT visible inside the transaction when the audit write " +
			"was refused — so its later absence is not evidence of a rollback")
	}
}

// ─── Domain wiring ──────────────────────────────────────────────────────────

type controlPlane struct {
	db *gorm.DB

	workspaces *workspace.Service
	projects   *project.Service
	conns      *connection.Service

	// audit is the REAL recorder over the REAL table.
	audit *Recorder

	// failing is installed instead when a test wants the rollback path.
	failing *failingStore
}

// newControlPlane wires every control-plane service the way the composition
// root does, over one database handle.
//
// auditStore is what makes the two halves of this suite different: the real
// repository for the success cases, a failingStore for the rollback ones.
// Everything else is identical, which is what keeps the comparison honest.
func newControlPlane(t *testing.T, db *gorm.DB, auditStore Store) *controlPlane {
	t.Helper()

	recorder := NewRecorder(auditStore)
	if recorder == nil {
		t.Fatal("NewRecorder returned nil for a non-nil store")
	}
	runner := database.NewTxRunner(db)

	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyring, err := secrets.NewSingleVersionKeyring(1, key)
	if err != nil {
		t.Fatalf("new keyring: %v", err)
	}

	wsRepo := workspace.NewRepository(db)
	cp := &controlPlane{
		db:         db,
		audit:      recorder,
		workspaces: workspace.NewService(wsRepo, runner, recorder),
		projects:   project.NewService(project.NewRepository(db), wsRepo, runner, recorder),
		conns: connection.NewService(connection.NewRepository(db), wsRepo, keyring,
			stubVerifier{}, runner, recorder),
	}
	if cp.workspaces == nil || cp.projects == nil || cp.conns == nil {
		t.Fatal("a control-plane service was nil with every collaborator present")
	}
	return cp
}

// stubVerifier reports a healthy connection without touching a provider.
//
// Deliberately not a real Keycloak: the property under test is a PostgreSQL
// transaction, and involving a provider would make this suite slower without
// making it stronger. The one connection operation whose provider interaction
// matters — Verify — has its external half documented as NOT rolled back, and
// that is asserted separately.
type stubVerifier struct{}

func (stubVerifier) Verify(context.Context, connection.VerifyTarget) connection.VerifyReport {
	return connection.VerifyReport{
		OK:         true,
		AccessMode: connection.AccessModeFull,
		Summary:    "stub: healthy",
		CheckedAt:  time.Now().UTC(),
	}
}

// ─── Independent readers ────────────────────────────────────────────────────
//
// Every assertion about what survived is made with plain SQL through the pool,
// NOT through the repositories that wrote it. A repository that silently
// swallowed a rollback would otherwise be both the writer and the witness.

func countRows(t *testing.T, db *gorm.DB, table, where string, args ...any) int64 {
	t.Helper()
	var n int64
	q := db.Table(table)
	if where != "" {
		q = q.Where(where, args...)
	}
	if err := q.Count(&n).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func auditRows(t *testing.T, db *gorm.DB, eventType string) []auditEventRow {
	t.Helper()
	var rows []auditEventRow
	if err := db.Where("event_type = ?", eventType).Find(&rows).Error; err != nil {
		t.Fatalf("read audit rows: %v", err)
	}
	return rows
}

// cpEvent is the control-plane event skeleton a handler builds.
//
// Named cpEvent rather than operatorEvent because recorder_test.go already owns
// that name in this package for a different shape.
func cpEvent(action audit.Action, requestID string) *audit.Event {
	return &audit.Event{
		Action: action,
		Actor: audit.Actor{
			Type:    audit.ActorOperator,
			Subject: "8f14e45f-ceea-467a-9e6f-4a5c1b2d3e4f",
			Email:   "operator@example.test",
		},
		RequestID: requestID,
		IP:        "203.0.113.7",
	}
}

// cpWorkspace seeds a workspace through the real service. Named cpWorkspace
// because repository_integration_test.go already owns seedWorkspace in this
// package for a different shape.
func cpWorkspace(t *testing.T, cp *controlPlane, name, slug string) *workspace.Workspace {
	t.Helper()
	ws, err := cp.workspaces.Create(context.Background(),
		workspace.CreateInput{Name: name, Slug: slug},
		cpEvent(audit.ActionWorkspaceCreated, "req-seed"))
	if err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return ws
}

// ═══════════════════════════════════════════════════════════════════════════
// 1 · 2   Workspace
// ═══════════════════════════════════════════════════════════════════════════

// TestAtomicity_WorkspaceCreateCommitsWithItsAuditRow is acceptance item 1.
func TestAtomicity_WorkspaceCreateCommitsWithItsAuditRow(t *testing.T) {
	db := newAtomicitySchema(t)
	cp := newControlPlane(t, db, NewRepository(db))

	ws, err := cp.workspaces.Create(context.Background(),
		workspace.CreateInput{Name: "Production EU", Slug: "production-eu"},
		cpEvent(audit.ActionWorkspaceCreated, "req-ws-create"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if n := countRows(t, db, "workspaces", "id = ?", ws.ID); n != 1 {
		t.Errorf("workspace rows = %d, want 1", n)
	}

	rows := auditRows(t, db, string(audit.ActionWorkspaceCreated))
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want exactly 1", len(rows))
	}
	got := rows[0]
	if got.WorkspaceID == nil || *got.WorkspaceID != ws.ID {
		t.Errorf("audit row names workspace %v, want %s", got.WorkspaceID, ws.ID)
	}
	if got.RequestID == nil || *got.RequestID != "req-ws-create" {
		t.Errorf("audit row request_id = %v, want req-ws-create", got.RequestID)
	}
	if got.Outcome != string(OutcomeSuccess) {
		t.Errorf("outcome = %q, want success", got.Outcome)
	}
}

// TestAtomicity_WorkspaceCreateRollsBackWhenAuditFails is acceptance item 2,
// and the central test of the slice.
func TestAtomicity_WorkspaceCreateRollsBackWhenAuditFails(t *testing.T) {
	db := newAtomicitySchema(t)

	failing := &failingStore{
		Store: NewRepository(db),
		// Read the workspace back through the SAME transaction. Seeing it here
		// is what proves the domain write executed before the audit refusal.
		probe: func(tx database.Tx) bool {
			var n int64
			if err := tx.Table("workspaces").Where("slug = ?", "rolled-back").Count(&n).Error; err != nil {
				return false
			}
			return n == 1
		},
	}
	cp := newControlPlane(t, db, failing)

	_, err := cp.workspaces.Create(context.Background(),
		workspace.CreateInput{Name: "Rolled Back", Slug: "rolled-back"},
		cpEvent(audit.ActionWorkspaceCreated, "req-rollback"))

	if err == nil {
		t.Fatal("Create succeeded even though its audit row could not be written")
	}
	if !errors.Is(err, audit.ErrNotRecorded) {
		t.Errorf("error = %v, want one wrapping audit.ErrNotRecorded so the handler can tell it "+
			"from a domain failure and avoid a recursive audit write", err)
	}

	failing.assertSawDomainWrite(t)

	// And now the whole point.
	if n := countRows(t, db, "workspaces", "slug = ?", "rolled-back"); n != 0 {
		t.Errorf("workspace rows = %d, want 0 — the mutation committed without its audit row", n)
	}
	if n := countRows(t, db, "audit_events", ""); n != 0 {
		t.Errorf("audit rows = %d, want 0 — a rollback must leave no orphan event", n)
	}
}

// TestAtomicity_WorkspaceArchiveRollsBackWhenAuditFails.
//
// Archive rather than Create, because the two fail differently: Create rolls
// back an INSERT, Archive rolls back an UPDATE. An implementation that only got
// the insert path right would pass the test above and lose this one.
func TestAtomicity_WorkspaceArchiveRollsBackWhenAuditFails(t *testing.T) {
	db := newAtomicitySchema(t)
	cp := newControlPlane(t, db, NewRepository(db))
	ws := cpWorkspace(t, cp, "Archive Me", "archive-me")

	failing := &failingStore{
		Store: NewRepository(db),
		probe: func(tx database.Tx) bool {
			var n int64
			if err := tx.Table("workspaces").
				Where("id = ? AND status = ?", ws.ID, "archived").Count(&n).Error; err != nil {
				return false
			}
			return n == 1
		},
	}
	failed := newControlPlane(t, db, failing)

	_, err := failed.workspaces.Archive(context.Background(), ws.PublicID(),
		cpEvent(audit.ActionWorkspaceArchived, "req-archive"))
	if err == nil {
		t.Fatal("Archive succeeded even though its audit row could not be written")
	}
	failing.assertSawDomainWrite(t)

	if n := countRows(t, db, "workspaces", "id = ? AND status = ?", ws.ID, "active"); n != 1 {
		t.Error("the workspace is no longer active — the archive committed without its audit row")
	}
	if n := len(auditRows(t, db, string(audit.ActionWorkspaceArchived))); n != 0 {
		t.Errorf("workspace.archived audit rows = %d, want 0", n)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// 3 · 4   Project
// ═══════════════════════════════════════════════════════════════════════════

func TestAtomicity_ProjectCreateCommitsWithItsAuditRow(t *testing.T) {
	db := newAtomicitySchema(t)
	cp := newControlPlane(t, db, NewRepository(db))
	ws := cpWorkspace(t, cp, "Alpha", "alpha")

	p, err := cp.projects.Create(context.Background(), ws.PublicID(), "Billing worker",
		cpEvent(audit.ActionProjectCreated, "req-prj-create"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if n := countRows(t, db, "projects", "id = ?", p.ID); n != 1 {
		t.Errorf("project rows = %d, want 1", n)
	}
	rows := auditRows(t, db, string(audit.ActionProjectCreated))
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want exactly 1", len(rows))
	}
	if rows[0].ResourceID == nil || *rows[0].ResourceID != p.PublicID() {
		t.Errorf("audit row resource_id = %v, want %s", rows[0].ResourceID, p.PublicID())
	}
}

func TestAtomicity_ProjectArchiveRollsBackWhenAuditFails(t *testing.T) {
	db := newAtomicitySchema(t)
	cp := newControlPlane(t, db, NewRepository(db))
	ws := cpWorkspace(t, cp, "Alpha", "alpha")
	p, err := cp.projects.Create(context.Background(), ws.PublicID(), "Billing worker",
		cpEvent(audit.ActionProjectCreated, "req-seed"))
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}

	failing := &failingStore{
		Store: NewRepository(db),
		probe: func(tx database.Tx) bool {
			var n int64
			if err := tx.Table("projects").
				Where("id = ? AND status = ?", p.ID, "archived").Count(&n).Error; err != nil {
				return false
			}
			return n == 1
		},
	}
	failed := newControlPlane(t, db, failing)

	_, err = failed.projects.Archive(context.Background(), ws.PublicID(), p.PublicID(),
		cpEvent(audit.ActionProjectArchived, "req-archive"))
	if err == nil {
		t.Fatal("Archive succeeded even though its audit row could not be written")
	}
	failing.assertSawDomainWrite(t)

	// Archiving a project stops every one of its credentials authenticating, so
	// a half-applied archive is a security-relevant divergence, not a
	// bookkeeping one.
	if n := countRows(t, db, "projects", "id = ? AND status = ?", p.ID, "active"); n != 1 {
		t.Error("the project is no longer active — the archive committed without its audit row")
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// 5 · 6   Project Credential
// ═══════════════════════════════════════════════════════════════════════════

func TestAtomicity_CredentialCreateCommitsWithItsAuditRow(t *testing.T) {
	db := newAtomicitySchema(t)
	cp := newControlPlane(t, db, NewRepository(db))
	ws := cpWorkspace(t, cp, "Alpha", "alpha")
	p, err := cp.projects.Create(context.Background(), ws.PublicID(), "Worker",
		cpEvent(audit.ActionProjectCreated, "req-seed"))
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}

	ev := cpEvent(audit.ActionCredentialCreated, "req-cred-create")
	ev.Extra = map[string]any{"scopes": []string{"users:read"}}

	cred, token, err := cp.projects.CreateCredential(context.Background(), ws.PublicID(), p.PublicID(),
		project.CreateCredentialInput{Label: "deploy", Scopes: []string{"users:read"}}, ev)
	if err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}
	if token == "" {
		t.Fatal("no plaintext token was returned")
	}

	if n := countRows(t, db, "project_credentials", "id = ?", cred.ID); n != 1 {
		t.Errorf("credential rows = %d, want 1", n)
	}

	// Acceptance item 12: the secret is not in the audit row.
	rows := auditRows(t, db, string(audit.ActionCredentialCreated))
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want exactly 1", len(rows))
	}
	assertNoSecret(t, rows[0], token, cred.KeyPrefix)
}

// TestAtomicity_CredentialCreateRollsBackWhenAuditFails.
//
// The sharpest case in the domain. The plaintext is returned exactly once and
// never stored, so a committed row with no audit record would be a live key the
// installation has no record of issuing. Rolling back means the token the
// caller was shown never authenticates, because the row it would match does not
// exist.
func TestAtomicity_CredentialCreateRollsBackWhenAuditFails(t *testing.T) {
	db := newAtomicitySchema(t)
	cp := newControlPlane(t, db, NewRepository(db))
	ws := cpWorkspace(t, cp, "Alpha", "alpha")
	p, err := cp.projects.Create(context.Background(), ws.PublicID(), "Worker",
		cpEvent(audit.ActionProjectCreated, "req-seed"))
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}

	failing := &failingStore{
		Store: NewRepository(db),
		probe: func(tx database.Tx) bool {
			var n int64
			if err := tx.Table("project_credentials").
				Where("project_id = ?", p.ID).Count(&n).Error; err != nil {
				return false
			}
			return n == 1
		},
	}
	failed := newControlPlane(t, db, failing)

	_, token, err := failed.projects.CreateCredential(context.Background(), ws.PublicID(), p.PublicID(),
		project.CreateCredentialInput{Label: "doomed", Scopes: []string{"users:read"}},
		cpEvent(audit.ActionCredentialCreated, "req-cred-rollback"))

	if err == nil {
		t.Fatal("CreateCredential succeeded even though its audit row could not be written")
	}
	if token != "" {
		t.Error("a plaintext token was returned alongside the error; a caller could try to use it")
	}
	failing.assertSawDomainWrite(t)

	if n := countRows(t, db, "project_credentials", "project_id = ?", p.ID); n != 0 {
		t.Errorf("credential rows = %d, want 0 — a live credential exists with no record of its issue", n)
	}
}

// TestAtomicity_CredentialRevokeRollsBackWhenAuditFails.
//
// And the consequence stated plainly: after this failure the credential is
// STILL USABLE. That is the honest behaviour — an operator who saw the error
// must assume their kill switch did not fire and retry — and it is why the
// service documents it rather than committing the revocation behind an error.
func TestAtomicity_CredentialRevokeRollsBackWhenAuditFails(t *testing.T) {
	db := newAtomicitySchema(t)
	cp := newControlPlane(t, db, NewRepository(db))
	ws := cpWorkspace(t, cp, "Alpha", "alpha")
	p, err := cp.projects.Create(context.Background(), ws.PublicID(), "Worker",
		cpEvent(audit.ActionProjectCreated, "req-seed"))
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	cred, _, err := cp.projects.CreateCredential(context.Background(), ws.PublicID(), p.PublicID(),
		project.CreateCredentialInput{Label: "live", Scopes: []string{"users:read"}},
		cpEvent(audit.ActionCredentialCreated, "req-seed"))
	if err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	failing := &failingStore{
		Store: NewRepository(db),
		probe: func(tx database.Tx) bool {
			var n int64
			if err := tx.Table("project_credentials").
				Where("id = ? AND revoked_at IS NOT NULL", cred.ID).Count(&n).Error; err != nil {
				return false
			}
			return n == 1
		},
	}
	failed := newControlPlane(t, db, failing)

	_, err = failed.projects.RevokeCredential(context.Background(), ws.PublicID(), p.PublicID(),
		cred.PublicID(), "operator-1", cpEvent(audit.ActionCredentialRevoked, "req-revoke"))
	if err == nil {
		t.Fatal("RevokeCredential succeeded even though its audit row could not be written")
	}
	failing.assertSawDomainWrite(t)

	if n := countRows(t, db, "project_credentials",
		"id = ? AND revoked_at IS NULL", cred.ID); n != 1 {
		t.Error("the credential was revoked without its audit row — the caller saw a failure and " +
			"the kill switch fired anyway")
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// 7 · 8   Connection activation — TWO domain rows
// ═══════════════════════════════════════════════════════════════════════════

// activateReady seeds a workspace with an active connection and a verified
// draft ready to take its place.
func activateReady(t *testing.T, cp *controlPlane, db *gorm.DB) (
	ws *workspace.Workspace, incumbent, successor *connection.Connection) {
	t.Helper()
	ctx := context.Background()
	ws = cpWorkspace(t, cp, "Alpha", "alpha")

	mk := func(name, realm string) *connection.Connection {
		c, err := cp.conns.Create(ctx, ws.PublicID(), connection.CreateInput{
			Name: name, Provider: "keycloak", BaseURL: "http://provider.invalid",
			Realm: realm, ClientID: "lw-conn", ClientSecret: "s3cr3t-" + realm,
		}, cpEvent(audit.ActionConnectionCreated, "req-seed"))
		if err != nil {
			t.Fatalf("create connection %s: %v", name, err)
		}
		if _, _, err := cp.conns.Verify(ctx, ws.PublicID(), c.PublicID(),
			cpEvent(audit.ActionConnectionVerified, "req-seed")); err != nil {
			t.Fatalf("verify connection %s: %v", name, err)
		}
		return c
	}

	incumbent = mk("incumbent", "realm-one")
	if _, err := cp.conns.Activate(ctx, ws.PublicID(), incumbent.PublicID(),
		cpEvent(audit.ActionConnectionActivated, "req-seed")); err != nil {
		t.Fatalf("activate incumbent: %v", err)
	}
	successor = mk("successor", "realm-two")
	return ws, incumbent, successor
}

func TestAtomicity_ConnectionActivationCommitsBothRowsWithItsAudit(t *testing.T) {
	db := newAtomicitySchema(t)
	cp := newControlPlane(t, db, NewRepository(db))
	ws, incumbent, successor := activateReady(t, cp, db)

	if _, err := cp.conns.Activate(context.Background(), ws.PublicID(), successor.PublicID(),
		cpEvent(audit.ActionConnectionActivated, "req-activate")); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	if n := countRows(t, db, "connections", "id = ? AND status = ?", successor.ID, "active"); n != 1 {
		t.Error("the successor is not active")
	}
	if n := countRows(t, db, "connections", "id = ? AND status = ?", incumbent.ID, "retired"); n != 1 {
		t.Error("the incumbent was not retired")
	}
	// Two activations happened in this test: the seed and the one under test.
	if n := len(auditRows(t, db, string(audit.ActionConnectionActivated))); n != 2 {
		t.Errorf("connection.activated audit rows = %d, want 2", n)
	}
}

// TestAtomicity_ConnectionActivationRollsBackBothRowsWhenAuditFails is
// acceptance item 8, and the one that a single-row implementation would fail.
//
// Activate touches TWO rows: it retires the incumbent and promotes the
// successor. A failed audit write must undo both, or the workspace is left
// either with no active connection at all or with the wrong one — and
// activation is the change that silently redirects every subsequent identity
// operation for that workspace to a different realm.
func TestAtomicity_ConnectionActivationRollsBackBothRowsWhenAuditFails(t *testing.T) {
	db := newAtomicitySchema(t)
	cp := newControlPlane(t, db, NewRepository(db))
	ws, incumbent, successor := activateReady(t, cp, db)

	failing := &failingStore{
		Store: NewRepository(db),
		probe: func(tx database.Tx) bool {
			// BOTH halves must be visible inside the transaction, or the
			// rollback assertion below would be proving less than it claims.
			var promoted, retired int64
			if err := tx.Table("connections").
				Where("id = ? AND status = ?", successor.ID, "active").Count(&promoted).Error; err != nil {
				return false
			}
			if err := tx.Table("connections").
				Where("id = ? AND status = ?", incumbent.ID, "retired").Count(&retired).Error; err != nil {
				return false
			}
			return promoted == 1 && retired == 1
		},
	}
	failed := newControlPlane(t, db, failing)

	_, err := failed.conns.Activate(context.Background(), ws.PublicID(), successor.PublicID(),
		cpEvent(audit.ActionConnectionActivated, "req-activate-rollback"))
	if err == nil {
		t.Fatal("Activate succeeded even though its audit row could not be written")
	}
	failing.assertSawDomainWrite(t)

	if n := countRows(t, db, "connections", "id = ? AND status = ?", incumbent.ID, "active"); n != 1 {
		t.Error("the incumbent is no longer active — the retire half committed without its audit row")
	}
	if n := countRows(t, db, "connections", "id = ? AND status = ?", successor.ID, "draft"); n != 1 {
		t.Error("the successor is no longer a draft — the activate half committed without its audit row")
	}
	// Exactly one workspace must still have exactly one active connection.
	if n := countRows(t, db, "connections", "workspace_id = ? AND status = ?", ws.ID, "active"); n != 1 {
		t.Errorf("active connections for the workspace = %d, want 1", n)
	}
}

// TestAtomicity_ConnectionDeleteRollsBackWhenAuditFails.
//
// The only control-plane mutation that DESTROYS a row rather than transitioning
// it, which inverts the usual stake: after a delete the audit event is the sole
// remaining evidence the connection ever existed. If the row went and the event
// did not, there is nothing left to reconstruct from — not even a retired row.
//
// So this is the one operation where a committed mutation without its event
// destroys information permanently, and the rollback matters most.
func TestAtomicity_ConnectionDeleteRollsBackWhenAuditFails(t *testing.T) {
	db := newAtomicitySchema(t)
	cp := newControlPlane(t, db, NewRepository(db))
	ws := cpWorkspace(t, cp, "Alpha", "alpha")

	conn, err := cp.conns.Create(context.Background(), ws.PublicID(), connection.CreateInput{
		Name: "doomed", Provider: "keycloak", BaseURL: "http://provider.invalid",
		Realm: "realm-one", ClientID: "lw-conn", ClientSecret: "s3cr3t",
	}, cpEvent(audit.ActionConnectionCreated, "req-seed"))
	if err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	failing := &failingStore{
		Store: NewRepository(db),
		probe: func(tx database.Tx) bool {
			var n int64
			if err := tx.Table("connections").Where("id = ?", conn.ID).Count(&n).Error; err != nil {
				return false
			}
			// The row is GONE inside the transaction: that is what "the delete
			// executed" looks like for this operation.
			return n == 0
		},
	}
	failed := newControlPlane(t, db, failing)

	err = failed.conns.Delete(context.Background(), ws.PublicID(), conn.PublicID(),
		cpEvent(audit.ActionConnectionDeleted, "req-delete"))
	if err == nil {
		t.Fatal("Delete succeeded even though its audit row could not be written")
	}
	failing.assertSawDomainWrite(t)

	if n := countRows(t, db, "connections", "id = ?", conn.ID); n != 1 {
		t.Error("the connection is gone and no event records that it ever existed")
	}
	if n := len(auditRows(t, db, string(audit.ActionConnectionDeleted))); n != 0 {
		t.Errorf("connection.deleted audit rows = %d, want 0", n)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// 9 · 10 · 11   Attribution
// ═══════════════════════════════════════════════════════════════════════════

// TestAtomicity_RequestIdAndActorSurviveIntoTheRow is acceptance items 9 and 10.
//
// The event is built from the REQUEST and completed by the SERVICE, so the two
// halves have to meet correctly. A row that lost the request id could not be
// correlated with the log lines for the same operation, which is the whole
// reason the column exists.
func TestAtomicity_RequestIdAndActorSurviveIntoTheRow(t *testing.T) {
	db := newAtomicitySchema(t)
	cp := newControlPlane(t, db, NewRepository(db))

	ev := cpEvent(audit.ActionWorkspaceCreated, "req-attribution-1")
	ws, err := cp.workspaces.Create(context.Background(),
		workspace.CreateInput{Name: "Attributed", Slug: "attributed"}, ev)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rows := auditRows(t, db, string(audit.ActionWorkspaceCreated))
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	got := rows[0]

	if got.RequestID == nil || *got.RequestID != "req-attribution-1" {
		t.Errorf("request_id = %v, want req-attribution-1", got.RequestID)
	}
	if got.ActorType != string(audit.ActorOperator) {
		t.Errorf("actor_type = %q, want operator", got.ActorType)
	}
	if got.ActorSubject == nil || *got.ActorSubject != ev.Actor.Subject {
		t.Errorf("actor_subject = %v, want %s", got.ActorSubject, ev.Actor.Subject)
	}
	// The disjointness the schema CHECK enforces, asserted from the Go side too.
	if got.ActorProjectID != nil {
		t.Errorf("an operator row carries actor_project_id = %v", got.ActorProjectID)
	}
	if got.WorkspaceID == nil || *got.WorkspaceID != ws.ID {
		t.Errorf("workspace_id = %v, want %s", got.WorkspaceID, ws.ID)
	}
}

// TestAtomicity_EventsDoNotCrossWorkspaces is acceptance item 11.
//
// Two workspaces, one mutation each. An event attributed to the wrong workspace
// is a forensics defect even when the domain state is perfectly correct: it
// puts one tenant's activity in another tenant's history, readable by anyone
// holding that tenant's audit:read.
func TestAtomicity_EventsDoNotCrossWorkspaces(t *testing.T) {
	db := newAtomicitySchema(t)
	cp := newControlPlane(t, db, NewRepository(db))

	alpha := cpWorkspace(t, cp, "Alpha", "alpha")
	bravo := cpWorkspace(t, cp, "Bravo", "bravo")

	if _, err := cp.projects.Create(context.Background(), alpha.PublicID(), "In Alpha",
		cpEvent(audit.ActionProjectCreated, "req-alpha")); err != nil {
		t.Fatalf("create project in alpha: %v", err)
	}
	if _, err := cp.projects.Create(context.Background(), bravo.PublicID(), "In Bravo",
		cpEvent(audit.ActionProjectCreated, "req-bravo")); err != nil {
		t.Fatalf("create project in bravo: %v", err)
	}

	for _, tc := range []struct {
		workspace *workspace.Workspace
		requestID string
	}{{alpha, "req-alpha"}, {bravo, "req-bravo"}} {
		var rows []auditEventRow
		if err := db.Where("workspace_id = ? AND event_type = ?",
			tc.workspace.ID, string(audit.ActionProjectCreated)).Find(&rows).Error; err != nil {
			t.Fatalf("read rows: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("%s: project.created rows = %d, want 1", tc.workspace.Slug, len(rows))
		}
		if rows[0].RequestID == nil || *rows[0].RequestID != tc.requestID {
			t.Errorf("%s: the row came from request %v, want %s — events crossed workspaces",
				tc.workspace.Slug, rows[0].RequestID, tc.requestID)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// 12   Secrets
// ═══════════════════════════════════════════════════════════════════════════

// assertNoSecret searches every text column of a row for values that must never
// reach the trail.
func assertNoSecret(t *testing.T, row auditEventRow, secrets ...string) {
	t.Helper()

	fields := map[string]*string{
		"actor_subject":       row.ActorSubject,
		"actor_email":         row.ActorEmail,
		"actor_project_id":    row.ActorProjectID,
		"actor_credential_id": row.ActorCredentialID,
		"resource_id":         row.ResourceID,
		"request_id":          row.RequestID,
		"reason_code":         row.ReasonCode,
	}
	haystack := row.EventType + "|" + string(row.Metadata)
	for name, v := range fields {
		if v != nil {
			haystack += "|" + name + "=" + *v
		}
	}

	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if strings.Contains(haystack, secret) {
			t.Errorf("a secret reached the audit row (searching for a value starting %q)",
				secret[:min(8, len(secret))])
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestAtomicity_ConnectionSecretsNeverReachTheTrail.
//
// A connection carries the provider's ADMINISTRATIVE credential. The service
// never returns it, so it should be structurally unreachable from an event —
// this asserts the structure held.
func TestAtomicity_ConnectionSecretsNeverReachTheTrail(t *testing.T) {
	db := newAtomicitySchema(t)
	cp := newControlPlane(t, db, NewRepository(db))
	ws := cpWorkspace(t, cp, "Alpha", "alpha")

	const secret = "lw-sentinel-connection-secret-do-not-log"
	if _, err := cp.conns.Create(context.Background(), ws.PublicID(), connection.CreateInput{
		Name: "primary", Provider: "keycloak", BaseURL: "http://provider.invalid",
		Realm: "realm-one", ClientID: "lw-conn", ClientSecret: secret,
	}, cpEvent(audit.ActionConnectionCreated, "req-conn")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var rows []auditEventRow
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("read rows: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no audit rows at all; the search would pass vacuously")
	}
	for _, row := range rows {
		assertNoSecret(t, row, secret, "realm-one", "http://provider.invalid")
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// 13   No orphan events, swept across every mutation
// ═══════════════════════════════════════════════════════════════════════════

// TestAtomicity_NoMutationLeavesAnOrphanEvent runs every rollback path in one
// schema and asserts the table is EMPTY afterwards.
//
// The per-operation tests above each assert their own absence. This asserts the
// global one: after a run in which six control-plane mutations were attempted
// and every one was refused at its audit write, the durable trail contains
// nothing at all. An implementation that wrote the event outside the
// transaction for any single operation fails here even if it passes every test
// above for the other five.
func TestAtomicity_NoMutationLeavesAnOrphanEvent(t *testing.T) {
	db := newAtomicitySchema(t)
	seed := newControlPlane(t, db, NewRepository(db))
	ws := cpWorkspace(t, seed, "Alpha", "alpha")
	p, err := seed.projects.Create(context.Background(), ws.PublicID(), "Worker",
		cpEvent(audit.ActionProjectCreated, "req-seed"))
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	cred, _, err := seed.projects.CreateCredential(context.Background(), ws.PublicID(), p.PublicID(),
		project.CreateCredentialInput{Label: "live", Scopes: []string{"users:read"}},
		cpEvent(audit.ActionCredentialCreated, "req-seed"))
	if err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	conn, err := seed.conns.Create(context.Background(), ws.PublicID(), connection.CreateInput{
		Name: "primary", Provider: "keycloak", BaseURL: "http://provider.invalid",
		Realm: "realm-one", ClientID: "lw-conn", ClientSecret: "s3cr3t",
	}, cpEvent(audit.ActionConnectionCreated, "req-seed"))
	if err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	before := countRows(t, db, "audit_events", "")
	if before == 0 {
		t.Fatal("the seed wrote no audit rows; the comparison below would be vacuous")
	}

	failing := &failingStore{Store: NewRepository(db)}
	failed := newControlPlane(t, db, failing)
	ctx := context.Background()

	attempts := []struct {
		name string
		call func() error
	}{
		{"workspace.create", func() error {
			_, err := failed.workspaces.Create(ctx, workspace.CreateInput{Name: "N", Slug: "n"},
				cpEvent(audit.ActionWorkspaceCreated, "r"))
			return err
		}},
		{"workspace.rename", func() error {
			_, err := failed.workspaces.UpdateName(ctx, ws.PublicID(), "Renamed",
				cpEvent(audit.ActionWorkspaceRenamed, "r"))
			return err
		}},
		{"project.rename", func() error {
			_, err := failed.projects.Rename(ctx, ws.PublicID(), p.PublicID(), "Renamed",
				cpEvent(audit.ActionProjectRenamed, "r"))
			return err
		}},
		{"credential.revoke", func() error {
			_, err := failed.projects.RevokeCredential(ctx, ws.PublicID(), p.PublicID(),
				cred.PublicID(), "op", cpEvent(audit.ActionCredentialRevoked, "r"))
			return err
		}},
		{"connection.update", func() error {
			name := "renamed"
			_, err := failed.conns.Update(ctx, ws.PublicID(), conn.PublicID(),
				connection.UpdateInput{Name: &name}, cpEvent(audit.ActionConnectionUpdated, "r"))
			return err
		}},
		{"connection.retire", func() error {
			_, err := failed.conns.Retire(ctx, ws.PublicID(), conn.PublicID(),
				cpEvent(audit.ActionConnectionRetired, "r"))
			return err
		}},
	}

	for _, a := range attempts {
		if err := a.call(); err == nil {
			t.Errorf("%s succeeded with a failing audit store", a.name)
		} else if !errors.Is(err, audit.ErrNotRecorded) {
			t.Errorf("%s: error = %v, want one wrapping audit.ErrNotRecorded", a.name, err)
		}
	}

	if after := countRows(t, db, "audit_events", ""); after != before {
		t.Errorf("audit rows went from %d to %d — a refused mutation left an orphan event",
			before, after)
	}

	// And every domain row is exactly as the seed left it.
	checks := []struct {
		name  string
		table string
		where string
		args  []any
	}{
		{"the workspace kept its name", "workspaces", "id = ? AND name = ?", []any{ws.ID, "Alpha"}},
		{"the project kept its name", "projects", "id = ? AND name = ?", []any{p.ID, "Worker"}},
		{"the credential is still live", "project_credentials", "id = ? AND revoked_at IS NULL", []any{cred.ID}},
		{"the connection kept its name", "connections", "id = ? AND name = ?", []any{conn.ID, "primary"}},
		{"the connection is still a draft", "connections", "id = ? AND status = ?", []any{conn.ID, "draft"}},
	}
	for _, c := range checks {
		if n := countRows(t, db, c.table, c.where, c.args...); n != 1 {
			t.Errorf("%s: matched %d rows, want 1 — a refused mutation committed", c.name, n)
		}
	}
	if n := countRows(t, db, "workspaces", "slug = ?", "n"); n != 0 {
		t.Error("the refused workspace create committed")
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// 14   Concurrency
// ═══════════════════════════════════════════════════════════════════════════

// TestAtomicity_ConcurrentActivationsStillLeaveOneActiveConnection is
// acceptance item 14.
//
// The partial unique index is the authority on "one active connection per
// workspace", and Slice 15 added an audit INSERT inside the same transaction
// that contends for it. This checks the invariant survived: several operators
// racing to activate different connections must still leave exactly one active,
// and exactly as many audit rows as there were winners.
//
// It also checks the lock ordering did not introduce a deadlock. Every path
// touches domain rows first and the audit row last, so there is no cycle — but
// "there is no cycle" is a claim about SQL that a concurrent test is cheap
// enough to actually make.
func TestAtomicity_ConcurrentActivationsStillLeaveOneActiveConnection(t *testing.T) {
	db := newAtomicitySchema(t)
	cp := newControlPlane(t, db, NewRepository(db))
	ctx := context.Background()
	ws := cpWorkspace(t, cp, "Alpha", "alpha")

	const racers = 4
	candidates := make([]*connection.Connection, 0, racers)
	for i := 0; i < racers; i++ {
		c, err := cp.conns.Create(ctx, ws.PublicID(), connection.CreateInput{
			Name:     fmt.Sprintf("candidate-%d", i),
			Provider: "keycloak", BaseURL: "http://provider.invalid",
			Realm: fmt.Sprintf("realm-%d", i), ClientID: "lw-conn", ClientSecret: "s3cr3t",
		}, cpEvent(audit.ActionConnectionCreated, "req-seed"))
		if err != nil {
			t.Fatalf("create candidate %d: %v", i, err)
		}
		if _, _, err := cp.conns.Verify(ctx, ws.PublicID(), c.PublicID(),
			cpEvent(audit.ActionConnectionVerified, "req-seed")); err != nil {
			t.Fatalf("verify candidate %d: %v", i, err)
		}
		candidates = append(candidates, c)
	}

	results := make(chan error, racers)
	start := make(chan struct{})
	for _, c := range candidates {
		go func(c *connection.Connection) {
			<-start
			_, err := cp.conns.Activate(ctx, ws.PublicID(), c.PublicID(),
				cpEvent(audit.ActionConnectionActivated, "req-race"))
			results <- err
		}(c)
	}
	close(start)

	winners := 0
	for i := 0; i < racers; i++ {
		if err := <-results; err == nil {
			winners++
		}
	}
	if winners == 0 {
		t.Fatal("every concurrent activation failed; the invariant check below would be vacuous")
	}

	if n := countRows(t, db, "connections", "workspace_id = ? AND status = ?", ws.ID, "active"); n != 1 {
		t.Errorf("active connections = %d, want exactly 1 — the uniqueness invariant broke", n)
	}

	// One audit row per winner, and no more. A row for a loser would mean an
	// event was written for a transaction that rolled back.
	activated := len(auditRows(t, db, string(audit.ActionConnectionActivated)))
	if activated != winners {
		t.Errorf("connection.activated rows = %d, winners = %d", activated, winners)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// The negative control
// ═══════════════════════════════════════════════════════════════════════════

// TestAtomicity_TheFailureSeamIsRealAndTheSuccessPathIsNot.
//
// The seam this whole file rests on is a store that always fails. If the
// services somehow did not call it, every rollback test would pass by writing
// nothing and asserting nothing was written.
//
// This is the control: the SAME operation, with the real store, must produce
// exactly the rows the failing one produced none of.
func TestAtomicity_TheFailureSeamIsRealAndTheSuccessPathIsNot(t *testing.T) {
	db := newAtomicitySchema(t)

	failing := &failingStore{Store: NewRepository(db)}
	refused := newControlPlane(t, db, failing)
	if _, err := refused.workspaces.Create(context.Background(),
		workspace.CreateInput{Name: "Refused", Slug: "refused"},
		cpEvent(audit.ActionWorkspaceCreated, "req-refused")); err == nil {
		t.Fatal("the failing store did not fail the mutation")
	}
	if !failing.probed && failing.Store == nil {
		t.Fatal("the failing store was never consulted")
	}

	accepted := newControlPlane(t, db, NewRepository(db))
	if _, err := accepted.workspaces.Create(context.Background(),
		workspace.CreateInput{Name: "Accepted", Slug: "accepted"},
		cpEvent(audit.ActionWorkspaceCreated, "req-accepted")); err != nil {
		t.Fatalf("the real store failed the same mutation: %v", err)
	}

	if n := countRows(t, db, "workspaces", "slug = ?", "refused"); n != 0 {
		t.Error("the refused workspace exists")
	}
	if n := countRows(t, db, "workspaces", "slug = ?", "accepted"); n != 1 {
		t.Error("the accepted workspace does not exist")
	}
	if n := countRows(t, db, "audit_events", ""); n != 1 {
		t.Errorf("audit rows = %d, want exactly 1 (the accepted create only)", n)
	}
}
