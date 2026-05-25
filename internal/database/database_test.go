package database

import (
	"bytes"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/user"
)

// TestUserModelMigrationShape pins the schema contract the database
// package's AutoMigrate is responsible for. The actual migration runs
// against a postgres instance only under -tags=integration; here we
// verify the model exposes the columns/tags the migration depends on,
// so a future rename of the GORM tag is caught at unit-test time.
func TestUserModelMigrationShape(t *testing.T) {
	// Reflect on the User struct so we don't depend on a specific
	// field order — the contract is "these fields exist with these
	// tags", not "they appear at offsets 0..N".
	rt := reflect.TypeOf(user.User{})

	want := map[string]string{
		"ID":          "primaryKey",
		"KeycloakSub": "uniqueIndex;not null",
		"Email":       "index",
		"Username":    "not null",
	}

	for name, wantTag := range want {
		f, ok := rt.FieldByName(name)
		if !ok {
			t.Errorf("user.User missing field %q", name)
			continue
		}
		gotTag := f.Tag.Get("gorm")
		// Tags are semicolon-joined directives; we assert the
		// expected directives appear, not that the literal string
		// matches (so future authors can append directives without
		// breaking this test).
		for _, want := range strings.Split(wantTag, ";") {
			if !strings.Contains(gotTag, want) {
				t.Errorf("user.User.%s gorm tag = %q, missing directive %q", name, gotTag, want)
			}
		}
	}

	// The TableName method is part of the migration contract — gorm
	// uses it to name the table. Pin it so a rename of the table is
	// a conscious decision (and a migration step), not an accident.
	tableName := user.User{}.TableName()
	if tableName != "users" {
		t.Errorf("user.User.TableName() = %q, want %q", tableName, "users")
	}
}

// TestConnect_FatalOnUnreachable exercises the log.Fatal exit path that
// Connect takes when the postgres driver cannot establish a connection.
// We run this test's logic in a child process (os.Exit terminates the
// test binary otherwise) and assert:
//
//   - the child exits non-zero
//   - the failure message mentions "Failed to connect" — the precise
//     phrasing the SRE runbook greps for
//
// The DSN points at a guaranteed-unroutable address (TEST-NET-1, RFC
// 5737) so we never accidentally hit a real database. connect_timeout=1
// keeps the child quick even on hosts with very slow DNS.
func TestConnect_FatalOnUnreachable(t *testing.T) {
	if os.Getenv("LSB_DB_CONNECT_CHILD") == "1" {
		// gorm.Open with the postgres driver does a real connection
		// preflight; with an unroutable host + 1s timeout it surfaces
		// the failure to log.Fatal which calls os.Exit(1).
		Connect("postgres://nobody:nopass@192.0.2.1:1/nodb?sslmode=disable&connect_timeout=1")
		// If we reach here the function did NOT fatal — exit cleanly
		// so the parent test catches the regression.
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestConnect_FatalOnUnreachable", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "LSB_DB_CONNECT_CHILD=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err == nil {
		t.Fatalf("child exited 0; expected Connect to log.Fatal. output:\n%s", out.String())
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 0 {
		t.Fatalf("child exited 0 unexpectedly. output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Failed to connect to database") {
		t.Errorf("child output did not contain expected message. got:\n%s", out.String())
	}
}
