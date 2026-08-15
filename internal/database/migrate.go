package database

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	pgxdriver "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	// Registers the "pgx" database/sql driver. The gorm postgres driver already
	// pulls pgx/v5 in, so this adds no dependency — it just makes sql.Open
	// usable for the migration pool.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// ErrNoMigrationsApplied is returned by Version when the database has no
// recorded schema version yet — either a fresh database, or one that predates
// versioned migrations and has not been adopted.
var ErrNoMigrationsApplied = errors.New("no migrations applied yet")

// Migrate applies every pending migration and returns once the database is at
// the latest embedded version.
//
// It is safe to call on:
//   - an empty database (bootstraps the full schema);
//   - a database already at the latest version (no-op);
//   - a database created by the pre-migration gorm AutoMigrate (adopted without
//     modification — see migrations/000001_baseline.up.sql).
//
// Concurrent callers are safe: the postgres driver takes an advisory lock for
// the duration, so two replicas booting simultaneously cannot interleave.
func Migrate(dsn string) error {
	return withMigrator(dsn, func(m *migrate.Migrate) error {
		err := m.Up()
		if errors.Is(err, migrate.ErrNoChange) {
			log.Info("schema already at the latest migration")
			return nil
		}
		if err != nil {
			return err
		}
		version, dirty, verr := m.Version()
		if verr != nil {
			// The migration itself succeeded; failing to read the version back
			// is worth a warning but not worth failing the boot.
			log.Warn(fmt.Sprintf("migrations applied but version read-back failed: %v", verr))
			return nil
		}
		log.Info(fmt.Sprintf("migrations applied (version=%d dirty=%t)", version, dirty))
		return nil
	})
}

// MigrateDown reverts every migration, leaving an empty schema. Destructive;
// exposed for local development and tests, never called at boot.
func MigrateDown(dsn string) error {
	return withMigrator(dsn, func(m *migrate.Migrate) error {
		err := m.Down()
		if errors.Is(err, migrate.ErrNoChange) {
			return nil
		}
		return err
	})
}

// MigrateSteps applies (n > 0) or reverts (n < 0) exactly n migrations.
func MigrateSteps(dsn string, n int) error {
	return withMigrator(dsn, func(m *migrate.Migrate) error {
		err := m.Steps(n)
		if errors.Is(err, migrate.ErrNoChange) {
			return nil
		}
		return err
	})
}

// Version reports the applied schema version and whether the database is in a
// dirty state — meaning a migration failed part-way and the schema is of
// unknown shape. A dirty database must be inspected by hand and then cleared
// with Force; nothing else is safe.
//
// Returns ErrNoMigrationsApplied when no version has been recorded.
func Version(dsn string) (version uint, dirty bool, err error) {
	err = withMigrator(dsn, func(m *migrate.Migrate) error {
		v, d, verr := m.Version()
		if errors.Is(verr, migrate.ErrNilVersion) {
			return ErrNoMigrationsApplied
		}
		if verr != nil {
			return verr
		}
		version, dirty = v, d
		return nil
	})
	return version, dirty, err
}

// Force writes version into the schema_migrations table and clears the dirty
// flag WITHOUT running any SQL.
//
// This is a recovery tool, not a migration command. The only correct use is:
// a migration failed, you inspected the schema by hand, you know exactly which
// version it actually matches, and you are telling the tool that. Forcing a
// version the schema does not match leaves the database permanently out of sync
// with the migration set.
func Force(dsn string, version int) error {
	return withMigrator(dsn, func(m *migrate.Migrate) error {
		return m.Force(version)
	})
}

// withMigrator builds a migrate.Migrate over the embedded migration set and a
// connection pool dedicated to migrations, runs fn, and tears both down.
//
// The pool is deliberately NOT the application's gorm pool: the pgx driver's
// Close() closes the *sql.DB it was handed, so sharing would close the pool the
// API serves traffic from. A short-lived pool of its own costs one connection
// for the duration of the migration and removes the hazard entirely.
func withMigrator(dsn string, fn func(*migrate.Migrate) error) error {
	if dsn == "" {
		return errors.New("empty database DSN")
	}

	src, err := MigrationsFS()
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}
	source, err := iofs.New(src, ".")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}

	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open migration connection: %w", err)
	}
	// Migrations are strictly serial; one connection is all the work needs and
	// capping it keeps a booting replica from briefly doubling the pool size.
	sqlDB.SetMaxOpenConns(1)
	defer func() { _ = sqlDB.Close() }()

	driver, err := pgxdriver.WithInstance(sqlDB, &pgxdriver.Config{})
	if err != nil {
		return fmt.Errorf("init migration driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}
	// m.Close() would close driver (and with it sqlDB, already deferred above)
	// plus the source. Closing only the source keeps teardown single-owner.
	defer func() { _ = source.Close() }()

	if err := fn(m); err != nil {
		return annotate(err)
	}
	return nil
}

// annotate turns golang-migrate's terse dirty-state error into something an
// operator woken at 3am can act on. Every other error passes through unchanged.
func annotate(err error) error {
	var dirty migrate.ErrDirty
	if errors.As(err, &dirty) {
		return fmt.Errorf(
			"database is dirty at version %d: a previous migration failed part-way. "+
				"Inspect the schema, then run `make migrate-force VERSION=<actual>` "+
				"to record the version it really matches: %w",
			dirty.Version, err)
	}
	return err
}
