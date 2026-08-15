// Command secrets inspects and rotates the master keys that seal identity
// provider credentials at rest.
//
// A key rotation is an operational and security action, so it is a command an
// operator runs deliberately — not something the API does at boot, and not
// something that happens as a side effect of serving a request. See
// docs/SECRET_KEY_ROTATION.md for the full procedure.
//
// Usage: go run ./cmd/secrets <command>   (or `make secrets-status`, `make secrets-rotate`)
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/connection"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/database"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/secrets"
	"github.com/joho/godotenv"
	"gorm.io/gorm"
)

func main() {
	// Load .env the same way cmd/migrate does so this works from a checkout
	// with no exported environment. Absence is not an error — the values may
	// come from the real environment. config.LoadConfig is deliberately not
	// used: it validates the whole Keycloak stack and would refuse to run a key
	// rotation because an unrelated auth variable is missing.
	_ = godotenv.Load()

	// Ctrl-C cancels between rows rather than mid-write. Rotation is per-row
	// transactional, so an interrupted run leaves committed rows rotated and
	// the rest untouched, and re-running resumes.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	deps := connection.SecretsCLIDeps{}

	kc, err := config.LoadSecretsKeyring()
	switch {
	case err != nil:
		deps.ConfigError = err
	case kc.Configured():
		ring, ringErr := secrets.NewKeyringFromBase64(kc.Keys, kc.Current)
		if ringErr != nil {
			deps.ConfigError = ringErr
		} else {
			deps.Keyring = ring
			deps.Configured = true
		}
	}

	// The database is opened only when there is something to do with it, so a
	// misconfigured keyring reports the configuration problem rather than a
	// connection error that sends the operator looking in the wrong place.
	if deps.ConfigError == nil && deps.Configured {
		db, closeDB, dbErr := openDatabase()
		if dbErr != nil {
			fmt.Fprintf(os.Stderr, "database: %v\n", dbErr)
			os.Exit(2)
		}
		if closeDB != nil {
			defer closeDB()
		}
		deps.DB = db
	}

	os.Exit(connection.RunSecretsCLI(ctx, os.Args[1:], deps, os.Stdout, os.Stderr))
}

// openDatabase connects using DB_URL. Returns (nil, nil, nil) when DB_URL is
// unset, leaving RunSecretsCLI to report it with the rest of the invocation
// checks.
func openDatabase() (*gorm.DB, func(), error) {
	dsn := os.Getenv("DB_URL")
	if dsn == "" {
		return nil, nil, nil
	}

	db, err := database.ConnectDSN(dsn)
	if err != nil {
		return nil, nil, err
	}
	closeDB := func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
	return db, closeDB, nil
}
