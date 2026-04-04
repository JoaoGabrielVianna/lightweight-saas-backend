// =====================================================
// Package logger provides a lightweight logging utility
// with color-coded output for different log levels.
//
// This logger is designed to be simple, fast, and easy
// to integrate across the entire application. Each module
// should create its own logger instance using New().
// =====================================================
package logger

import (
	"fmt"
	"log"
	"os"
	"time"
)

// =====================================================
// init initializes the logger package by disabling
// the default Go logger timestamp to avoid duplication.
//
// This is called automatically when the package is imported.
// We disable log flags so that we have full control over
// the timestamp format in our custom logger.
// =====================================================
func init() {
	log.SetFlags(0)
}

// =====================================================
// Logger represents a logger instance configured for
// a specific module or component within the application.
//
// Fields:
//   - origin: The name of the module/component that
//     created this logger (e.g., "auth", "database", "server")
//
// =====================================================
type Logger struct {
	origin string
}

// =====================================================
// New creates and returns a new Logger instance
// configured for the specified module.
//
// Parameters:
//   - origin: The name/identifier of the module
//     using this logger
//
// Returns:
//   - *Logger: A new logger instance for the module
//
// Example:
//
//	log := logger.New("auth")
//	log.Info("User authenticated successfully")
//
// =====================================================
func New(origin string) *Logger {
	return &Logger{origin: origin}
}

// =====================================================
// ANSI Color Codes for Terminal Output
//
// These constants define the ANSI escape codes used to format
// log messages in the terminal with colored backgrounds.
// Each log level has its own background color with contrasting
// text color for optimal readability.
//
// Background Colors:
//   - Blue (\033[44m): INFO messages (normal operations)
//   - Yellow (\033[43m): WARN messages (non-critical issues)
//   - Red (\033[41m): ERROR and FATAL messages (failures/critical)
//
// Text Colors:
//   - White (\033[97m): Used with blue and red backgrounds
//   - Black (\033[30m): Used with yellow background
//   - Reset (\033[0m): Resets all formatting
//
// Formatting:
//   - originWidth: Fixed width for origin/module names (10 chars)
//
// =====================================================
const (
	// Origin/module name width for alignment
	originWidth = 10

	// Background colors
	bgBlue   = "\033[44m"
	bgYellow = "\033[43m"
	bgRed    = "\033[41m"

	// Text colors
	textWhite = "\033[97m"
	textBlack = "\033[30m"

	// Reset
	colorReset = "\033[0m"
)

// =====================================================
// format constructs a formatted log message string
// with timestamp, log level (with background color),
// module origin, and message.
//
// This private method is used internally by all public
// logging methods (Info, Warn, Error, Fatal) to ensure
// consistent message formatting across the application.
//
// The format is: TIMESTAMP [ PADDED_COLORED_LEVEL ] [ PADDED_ORIGIN ] MESSAGE
//
// Parameters:
//   - level: The log level name (e.g., "INFO", "WARN", "ERROR")
//   - bgColor: ANSI background color code for the level
//   - textColor: ANSI text color code for contrast
//   - msg: The actual log message content
//
// Returns:
//   - string: The formatted message ready for output
//
// Format Example:
//
//	2026-04-04 14:13:36 [ INFO  ] [ database  ] Database connected
//	2026-04-04 14:13:36 [ ERROR ] [ server    ] Connection failed
//
// Styling:
//   - All levels are padded to 5 characters for visual alignment
//   - All origins are padded/truncated to 10 characters for visual alignment
//   - Level has background color with contrasting text
//   - Timestamp, origin, and message are not colored
//   - Consistent spacing improves readability
//
// Internal Use Only:
//
//	This method is private (lowercase) and not intended
//	to be called directly from other packages.
//
// =====================================================
func (l *Logger) format(level string, bgColor string, textColor string, msg string) string {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	paddedLevel := padLevel(level)
	paddedOrigin := formatOrigin(l.origin)
	styledLevel := fmt.Sprintf("%s%s %s %s", bgColor, textColor, paddedLevel, colorReset)
	return fmt.Sprintf("%s %s [ %s ] %s", timestamp, styledLevel, paddedOrigin, msg)
}

// =====================================================
// padLevel pads a log level string to a fixed width
// of 5 characters for visual alignment.
//
// This ensures all log levels appear at the same width,
// making the log output cleaner and easier to scan.
//
// Parameters:
//   - level: The log level name to pad
//
// Returns:
//   - string: The padded level string (5 characters)
//
// Example:
//
//	padLevel("INFO")  returns "INFO "
//	padLevel("WARN")  returns "WARN "
//	padLevel("ERROR") returns "ERROR"
//	padLevel("FATAL") returns "FATAL"
//
// Implementation:
//   - Uses fmt.Sprintf with "%-5s" format
//   - Left-aligned with spaces on the right
//   - Private function (lowercase)
//
// =====================================================
func padLevel(level string) string {
	return fmt.Sprintf("%-5s", level)
}

// =====================================================
// formatOrigin standardizes origin/module names by
// padding or truncating them to a fixed width.
//
// This ensures all origin names appear at the same width,
// providing consistent visual alignment in log output.
// Very long module names are truncated, shorter ones are padded.
//
// Parameters:
//   - origin: The origin/module name to format
//
// Returns:
//   - string: The formatted origin string (10 characters)
//
// Example:
//
//	formatOrigin("auth")      returns "auth      " (4 + 6 spaces)
//	formatOrigin("database")  returns "database  " (8 + 2 spaces)
//	formatOrigin("server")    returns "server    " (6 + 4 spaces)
//	formatOrigin("very_long_module_name") returns "very_long_" (truncated)
//
// Behavior:
//   - If origin is shorter than originWidth: pad with spaces on the right
//   - If origin is longer than originWidth: truncate from the right
//   - Uses fmt.Sprintf with "%-10s" format for left alignment
//   - Then truncates to originWidth if needed
//
// Implementation:
//   - Left-aligned padding/truncation via fmt.Sprintf
//   - Simple string slicing for truncation
//   - Private function (lowercase)
//
// =====================================================
func formatOrigin(origin string) string {
	padded := fmt.Sprintf("%-*s", originWidth, origin)
	if len(padded) > originWidth {
		return padded[:originWidth]
	}
	return padded
}

// =====================================================
// Info logs an informational message at INFO level.
//
// Use this for normal application operations:
//   - Application startup/shutdown events
//   - Database connections established
//   - API requests received
//   - Authentication successful
//
// Level: INFO (Blue background with white text)
// Execution: Does not stop the application
//
// Output Format:
//
//	2026-04-04 14:13:36 [ INFO  ] [ database  ] Database connected
//
// Example:
//
//	log.Info("Server started successfully on port 8080")
//	log.Info("User login completed for user@example.com")
//
// =====================================================
func (l *Logger) Info(msg string) {
	log.Println(l.format("INFO", bgBlue, textWhite, msg))
}

// =====================================================
// Warn logs a warning message at WARN level.
//
// Use this for non-critical issues that should be noted:
//   - Missing optional configuration files
//   - Deprecated API usage
//   - Resource warnings (high memory, slow queries)
//   - Recoverable errors that were handled gracefully
//
// Level: WARN (Yellow background with black text)
// Execution: Does not stop the application (continues normally)
//
// Output Format:
//
//	2026-04-04 14:13:36 [ WARN  ] [ config    ] Configuration not found
//
// Example:
//
//	log.Warn("Database query took longer than expected: 5.2s")
//	log.Warn("Configuration file not found, using defaults")
//
// =====================================================
func (l *Logger) Warn(msg string) {
	log.Println(l.format("WARN", bgYellow, textBlack, msg))
}

// =====================================================
// Error logs an error message at ERROR level.
//
// Use this for failures that don't stop execution:
//   - API endpoints returning error responses
//   - Database queries that failed but were handled
//   - Validation errors that were caught and recovered
//   - Unexpected but non-critical conditions
//
// Level: ERROR (Red background with white text)
// Execution: Does not stop the application (error is handled)
//
// Output Format:
//
//	2026-04-04 14:13:36 [ ERROR ] [ server    ] Connection failed
//
// Example:
//
//	log.Error("Failed to send email notification: connection timeout")
//	log.Error("Invalid user input: email format is incorrect")
//
// =====================================================
func (l *Logger) Error(msg string) {
	log.Println(l.format("ERROR", bgRed, textWhite, msg))
}

// =====================================================
// Fatal logs a fatal error message and terminates
// the application immediately.
//
// Use this ONLY for critical failures that make the
// application unable to continue:
//   - Database connection failures
//   - Missing required configuration
//   - Panic conditions
//   - Unrecoverable system errors
//
// Level: FATAL (Red background with white text)
// Execution: STOPS the application (os.Exit(1))
//
// Output Format:
//
//	2026-04-04 14:13:36 [ FATAL ] [ database  ] Connection refused
//
// WARNING:
//
//	This method will immediately terminate the process.
//	Only use when the application cannot safely continue.
//	Use Error() for failures that can be handled gracefully.
//
// Example:
//
//	log.Fatal("Failed to connect to database: connection refused")
//	log.Fatal("DATABASE_URL environment variable not set")
//
// =====================================================
func (l *Logger) Fatal(msg string) {
	log.Println(l.format("FATAL", bgRed, textWhite, msg))
	os.Exit(1)
}
