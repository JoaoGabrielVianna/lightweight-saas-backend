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
//   - runs AutoMigrate so users.keycloak_sub is a real column with the
//     unique index in place
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
}
