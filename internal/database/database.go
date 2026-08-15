// Package database owns the gorm connection lifecycle and schema migration.
//
// User identity now lives in Keycloak. The local users table is just a
// projection of "subjects we've seen", populated JIT by Service.EnsureUser.
// No bcrypt seed here — Keycloak imports seed users via the realm export.
//
// Schema changes go through the versioned SQL migrations in migrations/, not
// through gorm AutoMigrate. See docs/MIGRATIONS.md.
package database

import (
	"fmt"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var log = logger.New("database")

// options carries Connect's tunables. Zero value is not used directly —
// defaultOptions supplies the boot-time behaviour.
type options struct {
	runMigrations bool
}

func defaultOptions() options {
	// Migrating on connect preserves the behaviour AutoMigrate had: a process
	// that reaches Connect successfully is talking to an up-to-date schema.
	return options{runMigrations: true}
}

// Option customizes Connect. Kept variadic so the common call site stays
// Connect(dsn) and every existing caller compiles unchanged.
type Option func(*options)

// WithMigrations controls whether Connect applies pending migrations. Disable
// it when migrations are run as a separate deploy step (an init container, a
// release job) rather than by the API process itself; the API then requires the
// schema to be current already and will fail on the first query if it is not.
func WithMigrations(enabled bool) Option {
	return func(o *options) { o.runMigrations = enabled }
}

// connectAttempts and connectBackoff bound the wait for a database that is
// still starting.
//
// # Why retry at all, when compose already waits
//
// `depends_on: service_healthy` covers the reference deployment and nothing
// else. Two ordinary cases are outside it: a managed PostgreSQL that is a few
// seconds behind on a host reboot, and any orchestrator that starts containers
// concurrently. In both, the API exits, the supervisor restarts it, and it
// works on the second or third try — with a stack trace in the log each time,
// which trains an operator to ignore startup errors.
//
// # Why bounded, and why this short
//
// A retry loop that never gives up produces the worst failure mode there is: a
// process that is running, not serving, and not reporting anything an
// orchestrator can act on. Readiness would answer 503 forever and nobody would
// be paged, because nothing crashed.
//
// Ten attempts over roughly fifteen seconds covers "still starting" and does
// not cover "misconfigured" — a wrong host or a wrong password fails all ten
// just as fast, and the process exits with the driver's reason.
//
// Variables rather than constants so a test can shrink them. The BEHAVIOUR
// under test is "bounded, then fatal"; making a test wait fifteen real seconds
// to observe a bound proves only that the bound is fifteen seconds, and makes
// the suite slower for everyone forever.
var (
	connectAttempts = 10
	connectBackoff  = 1500 * time.Millisecond
)

// Connect opens the postgres connection and, unless disabled, applies pending
// migrations.
//
// Fatal after a bounded retry. There is no graceful continuation when the
// database is unreachable: every route either reads it or resolves a workspace
// through it, so a process that started without one would serve nothing but
// 503s while looking alive.
//
// Migration failure is fatal IMMEDIATELY, with no retry. A migration that fails
// does not fail because the database is not ready — it already connected — it
// fails because the schema is in a state this build does not understand, and
// retrying that is how a half-applied migration becomes a corrupted one.
func Connect(dbUrl string, opts ...Option) *gorm.DB {
	cfg := defaultOptions()
	for _, opt := range opts {
		opt(&cfg)
	}

	db, err := connectWithRetry(dbUrl)
	if err != nil {
		log.Fatal(fmt.Sprintf("Failed to connect to database after %d attempts: %v",
			connectAttempts, err))
	}

	if cfg.runMigrations {
		if err := Migrate(dbUrl); err != nil {
			log.Fatal(fmt.Sprintf("failed to run migrations: %v", err))
		}
	} else {
		log.Warn("migrations on boot are disabled — the schema must already be current")
	}

	log.Info("Database connection established successfully")
	return db
}

// ConnectDSN opens a pool for a command-line tool: no migrations, and an error
// instead of log.Fatal.
//
// Connect is right for the API — it applies migrations, retries a database that
// is still booting alongside it, and a failure there genuinely is fatal. None
// of that suits a tool an operator runs at a prompt: `secrets rotate` must not
// migrate a schema as a side effect of rotating keys, and it must be able to
// print its own diagnosis rather than exiting from inside a library.
//
// The retry is kept, because a compose stack that has just come up is exactly
// where these commands are typed.
func ConnectDSN(dsn string) (*gorm.DB, error) {
	return connectWithRetry(dsn)
}

// connectWithRetry opens the pool, retrying only while the failure looks like
// "not up yet".
//
// gorm.Open does not necessarily contact the server, so a successful Open is
// not evidence of anything. The ping is what makes each attempt meaningful —
// without it the loop would "succeed" immediately and the first real query
// would fail somewhere far less legible.
func connectWithRetry(dsn string) (*gorm.DB, error) {
	var lastErr error

	for attempt := 1; attempt <= connectAttempts; attempt++ {
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
			Logger:                                   gormlogger.Default.LogMode(gormlogger.Silent),
			TranslateError:                           true,
			DisableForeignKeyConstraintWhenMigrating: false,
		})
		if err == nil {
			err = ping(db)
			if err == nil {
				if attempt > 1 {
					log.Info(fmt.Sprintf("database reachable after %d attempts", attempt))
				}
				return db, nil
			}
		}
		lastErr = err

		if attempt < connectAttempts {
			// The error can name the host and the user; it never carries the
			// password, because the driver does not echo the DSN. Logged at
			// warn so a slow start is visible without being alarming.
			log.Warn(fmt.Sprintf("database not reachable (attempt %d/%d), retrying in %s: %v",
				attempt, connectAttempts, connectBackoff, err))
			time.Sleep(connectBackoff)
		}
	}
	return nil, lastErr
}

// ping confirms the server answers, through the pool the application will use.
func ping(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Ping()
}
