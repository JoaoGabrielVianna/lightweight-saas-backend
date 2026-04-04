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

func Connect(dbUrl string) *gorm.DB {

	db, err := gorm.Open(postgres.Open(dbUrl), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		log.Fatal(fmt.Sprintf("Failed to connect to database: %v", err))
	}

	if err := db.AutoMigrate(&user.User{}); err != nil {
		log.Fatal(fmt.Sprintf("failed to run migrations: %v", err))
	}

	// Seed default test user if not exists
	seedDefaultUser(db)

	log.Info("Database connection established successfully")

	return db
}

// =====================================================
// seedDefaultUser creates a default test user if it doesn't exist.
//
// This function checks if a test user already exists before creating it,
// preventing duplicate entries on multiple application starts.
//
// Parameters:
//   - db: The database connection
//
// =====================================================
func seedDefaultUser(db *gorm.DB) {
	const defaultEmail = "test@test.com"

	// Check if user already exists
	var count int64
	db.Model(&user.User{}).Where("email = ?", defaultEmail).Count(&count)

	if count > 0 {
		log.Info("Default test user already exists")
		return
	}

	// Create default test user
	defaultUser := &user.User{
		Email:    defaultEmail,
		Password: "123",
	}

	if err := db.Create(defaultUser).Error; err != nil {
		log.Error(fmt.Sprintf("failed to seed default user: %v", err))
		return
	}

	log.Info("Default test user created successfully")
}
