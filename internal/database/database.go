// Package database owns the gorm connection lifecycle and schema migration.
//
// User identity now lives in Keycloak. The local users table is just a
// projection of "subjects we've seen", populated JIT by Service.EnsureUser.
// No bcrypt seed here — Keycloak imports seed users via the realm export.
package database

import (
	"fmt"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/user"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var log = logger.New("database")

// Connect opens the postgres connection and runs AutoMigrate for the
// owned models. Fatal on connection or migration failure — there is no
// graceful continuation when the database is unreachable.
func Connect(dbUrl string) *gorm.DB {
	db, err := gorm.Open(postgres.Open(dbUrl), &gorm.Config{
		Logger:                                   gormlogger.Default.LogMode(gormlogger.Silent),
		TranslateError:                           true,
		DisableForeignKeyConstraintWhenMigrating: false,
	})
	if err != nil {
		log.Fatal(fmt.Sprintf("Failed to connect to database: %v", err))
	}

	if err := db.AutoMigrate(&user.User{}); err != nil {
		log.Fatal(fmt.Sprintf("failed to run migrations: %v", err))
	}

	log.Info("Database connection established successfully")
	return db
}
