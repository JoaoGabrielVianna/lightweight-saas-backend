package database

import (
	"fmt"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var log = logger.New("database")

func Connect(dbUrl string) *gorm.DB {

	db, err := gorm.Open(postgres.Open(dbUrl), &gorm.Config{})
	if err != nil {
		log.Fatal(fmt.Sprintf("Failed to connect to database: %v", err))
	}

	log.Info("Database connection established successfully")

	return db
}
