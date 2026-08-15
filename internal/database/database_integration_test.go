//go:build integration

package database

import (
	"errors"
	"os"
	"testing"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/user"
	"gorm.io/gorm"
)

// TestConnect_HappyPathRunsMigration verifies that against a real
// postgres instance (provided by the docker-compose stack), Connect:
//   - returns a non-nil *gorm.DB
//   - applies the versioned migrations so users.keycloak_sub is a real
//     column with the unique index in place
//   - records a schema version, so the next boot is a no-op
//
// Gated by the integration build tag (make test-integration); skipped
// when DB_URL is unset so a developer running `go test -tags=integration`
// outside the stack doesn't see a misleading failure.
func TestConnect_HappyPathRunsMigration(t *testing.T) {
	dsn := os.Getenv("DB_URL")
	if dsn == "" {
		t.Skip("DB_URL unset — integration test requires the docker-compose postgres up")
	}

	db := Connect(dsn)
	if db == nil {
		t.Fatal("Connect returned nil *gorm.DB on a reachable DSN")
	}

	// Round-trip a probe row: insert, find by unique index, delete.
	probe := &user.User{
		KeycloakSub: "integration-probe-" + t.Name(),
		Email:       "probe@example.test",
		Username:    "probe",
	}
	if err := db.Create(probe).Error; err != nil {
		t.Fatalf("Create probe row: %v", err)
	}
	t.Cleanup(func() { db.Unscoped().Delete(probe) })

	var loaded user.User
	if err := db.Where("keycloak_sub = ?", probe.KeycloakSub).First(&loaded).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("lookup probe by keycloak_sub: %v", err)
		}
		t.Fatal("probe row not found by unique index — migration didn't apply")
	}

	// Connect must leave a recorded version behind, not just a working schema:
	// that recording is what makes the next boot (and every future migration)
	// a no-op instead of a guess.
	version, dirty, err := Version(dsn)
	if err != nil {
		t.Fatalf("Version after Connect: %v", err)
	}
	if version == 0 || dirty {
		t.Errorf("after Connect version=%d dirty=%t, want a non-zero clean version", version, dirty)
	}
}

// TestConnect_WithMigrationsDisabled verifies the opt-out: Connect must return
// a usable handle without touching the schema. Run second against the same
// database, so the schema is already current — the assertion is that Connect
// succeeds and the recorded version is unchanged, i.e. nothing ran.
func TestConnect_WithMigrationsDisabled(t *testing.T) {
	dsn := os.Getenv("DB_URL")
	if dsn == "" {
		t.Skip("DB_URL unset — integration test requires the docker-compose postgres up")
	}

	// Ensure the schema exists first; the opt-out path assumes a current schema.
	if err := Migrate(dsn); err != nil {
		t.Fatalf("prepare schema: %v", err)
	}
	before, _, err := Version(dsn)
	if err != nil {
		t.Fatalf("Version before: %v", err)
	}

	db := Connect(dsn, WithMigrations(false))
	if db == nil {
		t.Fatal("Connect returned nil with migrations disabled")
	}

	after, dirty, err := Version(dsn)
	if err != nil {
		t.Fatalf("Version after: %v", err)
	}
	if after != before || dirty {
		t.Errorf("version moved from %d to %d (dirty=%t) with migrations disabled", before, after, dirty)
	}
}
