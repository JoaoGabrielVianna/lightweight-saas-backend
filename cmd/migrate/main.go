// Command migrate applies, reverts and inspects the versioned schema
// migrations embedded in the API binary.
//
// It exists for development and for deployments that apply migrations as a
// separate step (DB_MIGRATE_ON_BOOT=false). The API applies them itself by
// default, so a normal `make up` never needs this.
//
// Usage: go run ./cmd/migrate <command>   (or `make migrate`, `make migrate-version`, …)
package main

import (
	"os"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env the same way the API does so `make migrate` works from a
	// checkout with no exported environment. Absence is not an error — the DSN
	// may come from the real environment. config.LoadConfig is deliberately not
	// used: it validates the whole Keycloak stack and would refuse to run a
	// migration just because an auth variable is missing.
	_ = godotenv.Load()

	os.Exit(database.RunCLI(os.Args[1:], os.Getenv("DB_URL"), os.Stdout, os.Stderr))
}
