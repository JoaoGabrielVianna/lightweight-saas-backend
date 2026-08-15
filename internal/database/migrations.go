package database

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

// migrationsFS holds the versioned SQL migrations, compiled into the binary.
//
// Embedding rather than shipping a directory is deliberate: the API runs from a
// scratch-based container image (see Dockerfile) that contains the binary and
// nothing else. A migration set that lives on disk would have to be copied into
// the image and kept in sync with it — an easy way to boot a binary against the
// wrong schema. Embedded, the migrations and the code that runs them are the
// same artifact and cannot drift.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationsDir is the path prefix inside migrationsFS. golang-migrate's iofs
// source driver wants a filesystem rooted at the migrations themselves, so
// callers pass this to fs.Sub.
const migrationsDir = "migrations"

// MigrationsFS returns the embedded migration set rooted at the migration
// files (i.e. "000001_baseline.up.sql", not "migrations/000001_...").
//
// Exported so tests can assert the shipped set without reaching into package
// internals, and so a future migration-inspection command can list it.
func MigrationsFS() (fs.FS, error) {
	return fs.Sub(migrationsFS, migrationsDir)
}

// LatestVersion returns the highest version in the embedded migration set —
// the version a fully migrated database sits at.
//
// It exists because tests kept hard-coding it. A test asserting `version == 2`
// is really asserting "the database is fully migrated", and every new migration
// broke it: 000002 broke the suite written for 000001, and 000003 broke the one
// written for 000002. Deriving the number makes that class of failure
// impossible, and gives an operator-facing command somewhere to read it from.
func LatestVersion() (uint, error) {
	sub, err := MigrationsFS()
	if err != nil {
		return 0, fmt.Errorf("open embedded migrations: %w", err)
	}
	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		return 0, fmt.Errorf("read embedded migrations: %w", err)
	}

	var highest uint
	for _, e := range entries {
		digits, _, found := strings.Cut(e.Name(), "_")
		if !found {
			continue
		}
		n, err := strconv.ParseUint(digits, 10, 32)
		if err != nil {
			continue
		}
		if uint(n) > highest {
			highest = uint(n)
		}
	}
	if highest == 0 {
		return 0, errors.New("no versioned migrations found in the embedded set")
	}
	return highest, nil
}
