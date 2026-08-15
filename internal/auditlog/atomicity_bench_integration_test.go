//go:build integration

package auditlog

import (
	"context"
	"net/url"
	"os"
	"strconv"
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

// What the transaction costs, measured rather than asserted.
//
// Slice 15 moved the audit INSERT inside the mutation's transaction, so it is
// now part of control-plane latency by design. That trade is worth making for
// correctness, and it is worth knowing the size of.
//
// Two benchmarks, identical except for where the audit row goes:
//
//	Transactional   BEGIN → domain insert → audit insert → COMMIT   (current)
//	SeparateWrites  BEGIN → domain insert → COMMIT, then audit      (before)
//
// The second is not a supported code path — it is the shape the code had before
// this slice, reconstructed here only to make the comparison honest. A
// benchmark of the new path alone would produce a number nobody could read.
func BenchmarkControlPlaneMutation_Transactional(b *testing.B) {
	db, svc := benchService(b)
	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := svc.Create(ctx, workspace.CreateInput{Name: benchName(i)}, benchEvent()); err != nil {
			b.Fatalf("create: %v", err)
		}
	}
	b.StopTimer()
	_ = db
}

func BenchmarkControlPlaneMutation_SeparateWrites(b *testing.B) {
	db, _ := benchService(b)
	repo := workspace.NewRepository(db)
	recorder := NewRecorder(NewRepository(db))
	runner := database.NewTxRunner(db)
	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		w := benchWorkspace(b, i)
		if err := runner.InTx(ctx, func(tx database.Tx) error {
			return repo.WithTx(tx).Create(ctx, w)
		}); err != nil {
			b.Fatalf("create: %v", err)
		}
		// The pre-Slice-15 shape: a second, separate write.
		ev := benchEvent()
		ev.Workspace = w.PublicID()
		ev.Target = audit.Target{Kind: "workspace", ID: w.PublicID()}
		recorder.Record(ctx, *ev)
	}
	b.StopTimer()
}

func benchService(b *testing.B) (*gorm.DB, *workspace.Service) {
	b.Helper()
	db := newAtomicityBenchSchema(b)
	svc := workspace.NewService(workspace.NewRepository(db),
		database.NewTxRunner(db), NewRecorder(NewRepository(db)))
	if svc == nil {
		b.Fatal("NewService returned nil")
	}
	return db, svc
}

func benchEvent() *audit.Event {
	return &audit.Event{
		Action: audit.ActionWorkspaceCreated,
		Actor: audit.Actor{
			Type: audit.ActorOperator, Subject: "8f14e45f-ceea-467a-9e6f-4a5c1b2d3e4f",
		},
		RequestID: "bench",
	}
}

// benchName produces a unique workspace name per iteration. Slug uniqueness is
// enforced by the database, so a repeated name would turn the benchmark into a
// measurement of constraint violations.
func benchName(i int) string { return "bench-" + itoa(i) }

func benchWorkspace(b *testing.B, i int) *workspace.Workspace {
	b.Helper()
	id, err := publicid.New()
	if err != nil {
		b.Fatalf("generate id: %v", err)
	}
	now := time.Now().UTC()
	return &workspace.Workspace{
		ID: id, Slug: "bench-" + itoa(i), Name: benchName(i),
		Status: workspace.StatusActive, CreatedAt: now, UpdatedAt: now,
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// newAtomicityBenchSchema is newAtomicitySchema for a *testing.B.
//
// A near-duplicate rather than a generic helper: testing.TB does not carry
// Cleanup's semantics for benchmarks in a way that would let one function serve
// both without a type switch, and a type switch here would be harder to read
// than eight duplicated lines.
func newAtomicityBenchSchema(b *testing.B) *gorm.DB {
	b.Helper()

	base := os.Getenv("DB_URL")
	if base == "" {
		b.Skip("DB_URL unset — this benchmark requires a reachable postgres")
	}

	schema := "atom_bench"
	admin, err := gorm.Open(postgres.Open(base), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent), TranslateError: true,
	})
	if err != nil {
		b.Fatalf("open gorm: %v", err)
	}
	if err := admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`).Error; err != nil {
		b.Fatalf("drop stale schema: %v", err)
	}
	if err := admin.Exec(`CREATE SCHEMA ` + schema).Error; err != nil {
		b.Fatalf("create schema: %v", err)
	}

	u, err := url.Parse(base)
	if err != nil {
		b.Fatalf("parse DB_URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	dsn := u.String()

	if err := database.Migrate(dsn); err != nil {
		b.Fatalf("apply migrations: %v", err)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent), TranslateError: true,
	})
	if err != nil {
		b.Fatalf("open gorm: %v", err)
	}
	return db
}
