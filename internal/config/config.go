// =====================================================
// Package config handles application configuration
// loading from environment variables.
//
// This package provides a centralized way to load and
// manage application settings. Configuration can come from:
//   - .env file (local development)
//   - Environment variables (production & Docker)
//
// The LoadConfig function will try to load a .env file first,
// but will gracefully fall back to environment variables if
// the file is not found. This makes it safe for both development
// and production environments.
// =====================================================
package config

import (
	"io"
	"os"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logger"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

var log = logger.New("config")

// =====================================================
// Config holds all application configuration values.
//
// This struct contains the essential settings needed
// for the application to run. Add new configuration fields
// here as the application grows.
//
// Fields:
//   - Port: The TCP port the server will listen on
//     (default: "8080")
//   - DBUrl: PostgreSQL connection string
//     Format: postgres://user:password@host:port/database
//   - JWTSecret: Secret key for signing JWT tokens
//     (should be long and random in production)
//   - GinLogEnabled: Enable/disable Gin framework logs
//     (default: "true") - Set to "false" to disable
//   - GinAccessLogEnabled: Enable/disable Gin HTTP request/response logs
//     (default: "true") - Set to "false" to suppress access logs
//
// Example Config Values:
//
//	Config{
//	  Port:                 "8080",
//	  DBUrl:                "postgres://user:pass@localhost:5432/saas_db",
//	  JWTSecret:            "your-secret-key-here",
//	  GinLogEnabled:        true,
//	  GinAccessLogEnabled:  false,
//	}
//
// =====================================================
type Config struct {
	Port                string
	DBUrl               string
	JWTSecret           string
	GinLogEnabled       bool
	GinAccessLogEnabled bool
}

// =====================================================
// LoadConfig loads application configuration from
// environment variables and optional .env file.
//
// This function performs the following operations:
//  1. Attempts to load a .env file from the current directory
//  2. Falls back to environment variables if .env is not found
//  3. Returns a Config struct with all application settings
//
// Configuration Priority (highest to lowest):
//  1. Environment variables (always take precedence)
//  2. .env file values (if file exists and readable)
//  3. Default fallback values (used if variable not set)
//
// Environment Variables:
//   - PORT: Server port (default: "8080")
//   - DB_URL: Database connection string (required for production)
//   - JWT_SECRET: Secret for JWT signing (required for auth)
//   - GIN_LOG_ENABLED: Enable/disable Gin engine logs (default: "true")
//   - GIN_ACCESS_LOG_ENABLED: Enable/disable Gin HTTP request/response logs (default: "true")
//     Set to "false" to suppress access logs (recommended for production with centralized logging)
//
// Returns:
//   - *Config: Pointer to Config struct with all values loaded
//
// Example Usage:
//
//	func main() {
//	  cfg := LoadConfig()
//	  fmt.Printf("Starting server on port %s\n", cfg.Port)
//	}
//
// Notes:
//   - Missing .env file is NOT an error (warns only)
//   - Empty DB_URL in production should be caught by database.Connect()
//   - Default JWT_SECRET "secret" is for development ONLY
//
// =====================================================
func LoadConfig() *Config {

	err := godotenv.Load()
	if err != nil {
		log.Warn("No .env file found, using default environment variables")
	}

	return &Config{
		Port:                getEnv("PORT", "8080"),
		DBUrl:               getEnv("DB_URL", ""),
		JWTSecret:           getEnv("JWT_SECRET", "secret"),
		GinLogEnabled:       parseBool(getEnv("GIN_LOG_ENABLED", "true")),
		GinAccessLogEnabled: parseBool(getEnv("GIN_ACCESS_LOG_ENABLED", "true")),
	}
}

// =====================================================
// getEnv retrieves an environment variable value
// with a fallback default if not found.
//
// This is a private helper function used internally
// by LoadConfig() to safely read environment variables
// without causing errors if a variable is not set.
//
// Parameters:
//   - key: The name of the environment variable to look up
//   - fallback: The default value to return if key is not set
//
// Returns:
//   - string: The environment variable value, or fallback if not found
//
// Behavior:
//   - First checks if the environment variable exists
//   - If found: returns the variable's value (even if empty string)
//   - If not found: returns the provided fallback value
//
// Example:
//
//	port := getEnv("PORT", "8080")
//	// If PORT env var is set: uses that value
//	// If PORT env var is NOT set: uses "8080"
//
// Implementation Notes:
//   - Uses os.LookupEnv() which returns (value, exists)
//   - More reliable than os.Getenv() as it can distinguish
//     between unset variables and empty string values
//   - Private function (lowercase): only used within this package
//
// =====================================================
func getEnv(key string, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

// =====================================================
// parseBool converts a string value to a boolean.
//
// This is a private helper function used internally to safely
// parse boolean configuration values from environment variables.
//
// Parameters:
//   - value: String representation of a boolean
//     Accepted values (case-insensitive):
//   - True: "true", "1", "yes", "on"
//   - False: "false", "0", "no", "off", or any other value
//
// Returns:
//   - bool: true for recognized true values, false otherwise
//
// Example:
//
//	enabled := parseBool("true")     // returns true
//	enabled := parseBool("false")    // returns false
//	enabled := parseBool("1")        // returns true
//	enabled := parseBool("yes")      // returns true
//	enabled := parseBool("invalid")  // returns false
//
// Notes:
//   - Case-insensitive (handles "True", "TRUE", etc.)
//   - Defaults to false for any unrecognized value
//   - Private function (lowercase): only used within this package
//
// =====================================================
func parseBool(value string) bool {
	switch value {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

// =====================================================
// ApplyGinConfig applies Gin HTTP framework configuration
// based on the loaded configuration settings.
//
// This method centralizes all Gin-specific configuration,
// keeping framework setup details away from the server package.
// This improves separation of concerns and makes configuration
// management more cohesive.
//
// Behavior:
//   - If GinLogEnabled is false: Sets Gin to ReleaseMode (minimal logging)
//   - If GinAccessLogEnabled is false: Discards HTTP request/response logs
//   - Both controls work independently and can be used together
//
// Note:
//   - GinLogEnabled controls internal Gin engine logs
//   - GinAccessLogEnabled controls HTTP request/response access logs
//   - When both are true, Gin uses default logging behavior
//   - This method should be called before creating the Gin engine
//
// Example:
//
//	cfg := LoadConfig()
//	cfg.ApplyGinConfig()  // Apply configuration
//	router := gin.Default()  // Now use Gin with applied config
//
// =====================================================
func (c *Config) ApplyGinConfig() {
	// Disable Gin framework debug logs if configured
	if !c.GinLogEnabled {
		gin.SetMode(gin.ReleaseMode)
	}

	// Disable HTTP request/response access logs if configured
	if !c.GinAccessLogEnabled {
		gin.DefaultWriter = io.Discard
	}
}
