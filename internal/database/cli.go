package database

import (
	"errors"
	"fmt"
	"io"
	"strconv"
)

// usage is printed for `help`, for no arguments, and for anything unrecognized.
const usage = `migrate — versioned schema migrations

Usage:
  migrate <command> [args]

Commands:
  up                 Apply all pending migrations (what the API does at boot)
  down               Revert ALL migrations — drops every table. Destructive.
  steps <n>          Apply n migrations (n > 0) or revert -n (n < 0)
  version            Print the applied version and dirty flag
  force <version>    Record <version> and clear the dirty flag WITHOUT running
                     SQL. Recovery only — see docs/MIGRATIONS.md.

The database is read from DB_URL.
`

// RunCLI executes one migration command and returns a process exit code.
//
// The command logic lives here rather than in cmd/migrate/main.go so it is
// reachable from tests: argument handling and the error paths are exactly the
// parts worth pinning, and a main() cannot be called twice.
func RunCLI(args []string, dsn string, out io.Writer, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(errOut, usage)
		return 2
	}

	command := args[0]
	if command == "help" || command == "-h" || command == "--help" {
		fmt.Fprint(out, usage)
		return 0
	}

	if dsn == "" {
		fmt.Fprintln(errOut, "DB_URL is not set")
		return 2
	}

	switch command {
	case "up":
		return report(errOut, Migrate(dsn))

	case "down":
		return report(errOut, MigrateDown(dsn))

	case "steps":
		if len(args) < 2 {
			fmt.Fprintln(errOut, "steps requires a count, e.g. `migrate steps -1`")
			return 2
		}
		n, err := strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintf(errOut, "steps: %q is not an integer\n", args[1])
			return 2
		}
		if n == 0 {
			fmt.Fprintln(errOut, "steps: 0 would do nothing")
			return 2
		}
		return report(errOut, MigrateSteps(dsn, n))

	case "version":
		version, dirty, err := Version(dsn)
		if errors.Is(err, ErrNoMigrationsApplied) {
			fmt.Fprintln(out, "no migrations applied")
			return 0
		}
		if err != nil {
			fmt.Fprintf(errOut, "error: %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "version=%d dirty=%t\n", version, dirty)
		// A dirty database is not a healthy one; exit non-zero so a scripted
		// health check notices without having to parse the line.
		if dirty {
			return 1
		}
		return 0

	case "force":
		if len(args) < 2 {
			fmt.Fprintln(errOut, "force requires a version, e.g. `migrate force 1`")
			return 2
		}
		version, err := strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintf(errOut, "force: %q is not an integer\n", args[1])
			return 2
		}
		return report(errOut, Force(dsn, version))

	default:
		fmt.Fprintf(errOut, "unknown command %q\n\n", command)
		fmt.Fprint(errOut, usage)
		return 2
	}
}

// report maps an error to an exit code, printing it first. Exit 1 is reserved
// for "the migration itself failed"; 2 means the invocation was wrong.
func report(errOut io.Writer, err error) int {
	if err != nil {
		fmt.Fprintf(errOut, "error: %v\n", err)
		return 1
	}
	fmt.Fprintln(errOut, "ok")
	return 0
}
