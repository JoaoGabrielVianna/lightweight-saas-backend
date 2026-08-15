package database

import (
	"context"

	"gorm.io/gorm"
)

// The transaction seam.
//
// Slice 15 needs one property that the repositories could not previously
// express: a domain mutation and its durable audit row must commit together or
// not at all ([TD-033]). Both live in this PostgreSQL, so the answer is one
// transaction — but the transaction has to be owned ABOVE the repository,
// because no single repository knows about both tables.
//
// ─── Why Tx is *gorm.DB and not an interface of our own ─────────────────────
//
// The obvious shape is a `DBTX` interface with ExecContext/QueryContext, so a
// repository can be handed either the pool or a transaction. That is the right
// pattern for a codebase using database/sql directly. This one does not: every
// repository is written against GORM's fluent API — `db.Model(...).Where(...)
// .Updates(...)`, `db.Create(...)`, GORM's translated errors — and none of it
// goes through ExecContext.
//
// Introducing a DBTX interface here would therefore mean rewriting every
// repository in raw SQL to satisfy an abstraction whose only purpose is to
// avoid naming GORM. That is a repository-wide rewrite in service of elegance,
// which is exactly what this slice was told not to do.
//
// So Tx is an alias for the type the persistence layer already speaks. *gorm.DB
// is ALREADY the "either" type this seam needs — the same value represents the
// pool and an open transaction, and every repository method works against
// both — so the abstraction the pattern is reaching for already exists. Naming
// it Tx is documentation, not indirection.
//
// [TD-033]: docs/TECH_DEBT.md#td-033

// Tx executes repository operations. It is either the connection pool or an
// open transaction, and a repository cannot tell which — which is the whole
// point.
type Tx = *gorm.DB

// Runner owns transaction boundaries.
//
// Declared as an interface so a service can be constructed in a unit test
// without a database, and so the composition root remains the only place that
// knows a *gorm.DB exists.
type Runner interface {
	// InTx runs fn inside one transaction.
	//
	// Contract, and every clause is load-bearing:
	//
	//	fn returns nil     → COMMIT; the commit's own error is returned
	//	fn returns error   → ROLLBACK; that error is returned unwrapped
	//	fn panics          → ROLLBACK, then the panic continues
	//	ctx is cancelled   → the driver fails the statement and fn's error path
	//	                     is taken, so the transaction is rolled back
	//
	// There is no nesting fiction: calling InTx from inside fn is not part of
	// this contract and no code does it.
	InTx(ctx context.Context, fn func(tx Tx) error) error
}

// TxRunner is the PostgreSQL Runner.
type TxRunner struct {
	db *gorm.DB
}

// NewTxRunner constructs a Runner over the shared pool.
//
// Returns nil for a nil handle, the same "this is not wired" signal every other
// constructor in this codebase uses — the composition root reads it as "omit
// these routes" rather than mounting a service that would panic on first write.
func NewTxRunner(db *gorm.DB) *TxRunner {
	if db == nil {
		return nil
	}
	return &TxRunner{db: db}
}

// InTx implements Runner.
//
// It delegates to GORM's Transaction, which already provides the three
// properties the contract promises: it rolls back on a returned error, it
// recovers a panic, rolls back and re-panics, and it returns the commit error
// when the callback succeeded but COMMIT did not. Re-implementing that with
// Begin/Commit/Rollback by hand would be a second, divergeable copy of a
// correct one.
//
// WithContext is applied to the outer handle, so the transaction and every
// statement inside it observe cancellation.
func (r *TxRunner) InTx(ctx context.Context, fn func(tx Tx) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(tx)
	})
}
