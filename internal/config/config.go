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
	"strconv"
	"strings"
	"time"

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
	GinLogEnabled       bool
	GinAccessLogEnabled bool

	// DBMigrateOnBoot controls whether the API applies pending SQL migrations
	// during startup. Default true, which is the behaviour the previous
	// AutoMigrate-on-connect had.
	//
	// Set DB_MIGRATE_ON_BOOT=false when migrations are applied by a separate
	// deploy step (init container, release job) — typically because several API
	// replicas start at once and you want exactly one of them touching the
	// schema, or because the runtime database role is not allowed to DDL. With
	// it off, the schema must already be current: nothing checks at boot.
	DBMigrateOnBoot bool

	// Keycloak / OIDC provider configuration.
	// KeycloakJWKSURL is optional — derived from URL+Realm when empty.
	// KeycloakClientSecret is optional (reserved for future Admin API calls).
	// KeycloakAllowedClientIDs is optional — parsed from a comma-separated
	// env var; when empty the provider falls back to validating azp against
	// just {KeycloakClientID}.
	KeycloakURL              string
	KeycloakRealm            string
	KeycloakClientID         string
	KeycloakClientSecret     string
	KeycloakJWKSURL          string
	KeycloakAllowedClientIDs []string

	// Service-account credentials for Keycloak Admin REST API calls.
	// Distinct from KeycloakClientID/Secret so the token-validation client
	// and the admin client have independent secrets. The identity module
	// fails to initialize when these are empty; the rest of the API still
	// runs (auth + /me unaffected).
	//
	// KeycloakAdminBaseURL is the URL the API uses to REACH Keycloak for
	// admin calls (e.g. http://keycloak:8080 inside docker). When empty,
	// falls back to KeycloakURL. Distinct from KeycloakURL (used for `iss`
	// claim matching) because in docker those are different hostnames.
	KeycloakAdminClientID     string
	KeycloakAdminClientSecret string
	KeycloakAdminBaseURL      string

	// DEV-ONLY auth playground (served at /dev/auth when enabled).
	// Driven by features.dev_playground in config/project.json.
	DevPlaygroundEnabled  bool
	DevPlaygroundClientID string

	// AdminConsoleClientID_ is the Keycloak client used by the /admin SPA
	// for PKCE login. Exposed via AdminConsoleClientID(). When empty,
	// falls back to DevPlaygroundClientID.
	AdminConsoleClientID_ string

	// AdminConsoleEnabled gates the /admin SPA independently from
	// DevPlaygroundEnabled so production can serve the admin console
	// WITHOUT exposing the /playground or /api-explorer dev tools.
	//
	// Backward-compat: when AdminConsoleEnabled is false but
	// DevPlaygroundEnabled is true, the admin console is still mounted
	// (the dev-time convenience pre-dating the split).
	//
	// Production recipe: ADMIN_CONSOLE_ENABLED=true + DEV_PLAYGROUND_ENABLED=false.
	AdminConsoleEnabled bool

	// AdminLiveCheckTTLSeconds bounds how long the live-admin authorization
	// cache may serve a positive/negative answer before re-consulting
	// Keycloak. The GAP-1 remediation (see docs/SECURITY_REMEDIATION_GAP1.md)
	// closes the "stale JWT" window from `accessTokenLifespan` to this TTL
	// for out-of-band role changes; in-band changes through /admin/* hit
	// the invalidation hooks immediately.
	//
	// 0 or unset means "use auth.DefaultAdminTTL (30 s)". A value > 0 in
	// seconds is honored verbatim.
	AdminLiveCheckTTLSeconds int

	// SecretsMasterKey is the LEGACY single-key form: a base64-encoded 32-byte
	// AES key sealing provider credentials at rest (internal/secrets).
	//
	// Still honoured, and normalised to keyring version 1 — the version every
	// existing row carries. New installations should use SecretsKeyringSpec
	// instead; setting both is refused. See secrets.go for the whole contract.
	//
	// OPTIONAL, and its absence is a feature: with no key configured at all the
	// connection domain is not wired and /v1/workspaces/*/connections is not
	// mounted. An installation that never stores a provider credential
	// therefore needs no key, and an existing deployment upgrades without one.
	// Storing a credential without a key is not offered as an option.
	//
	// Read through Keyring(), never directly: this field is the environment's
	// spelling, not the model the runtime uses.
	//
	// Generate one with: openssl rand -base64 32
	SecretsMasterKey string

	// SecretsKeyringSpec is the versioned keyring: `1:<base64>,2:<base64>`.
	//
	// Every version listed can DECRYPT; exactly one — SecretsKeyCurrent —
	// encrypts. This is what makes a master key rotatable without re-entering
	// every stored credential by hand.
	//
	// Read through Keyring().
	SecretsKeyringSpec string

	// SecretsKeyCurrent nominates the version new secrets are sealed under.
	//
	// A string rather than an int because "unset" and "0" must not be the same
	// value: unset is legitimate when exactly one key is configured, and 0 is a
	// version that cannot exist. Parsed by Keyring().
	SecretsKeyCurrent string

	// CORSAllowedOrigins is a comma-separated list of origins allowed to make
	// cross-origin requests (e.g. "https://backoffice.example.com,http://localhost:5174").
	// When empty, CORS is disabled (requests from other origins are rejected by the browser).
	CORSAllowedOrigins []string

	// RateLimitEdgeRPS is the per-IP allowance at the /v1 edge, in requests per
	// second. It meters unauthenticated traffic and operator traffic; a project
	// credential's token is released once it authenticates, so this is NOT the
	// ceiling a machine consumer sees.
	//
	// 0 (unset) means the built-in default. So does a negative value: this is a
	// tuning knob, never an off switch, and a typo must not silently remove the
	// anti-flood protection.
	RateLimitEdgeRPS float64

	// RateLimitCredentialRPS is the per-credential allowance in requests per
	// second — the number a machine consumer can actually reach, and the number
	// published as the machine contract. 0 or negative means the default.
	//
	// Raise it for an installation whose backends do more than routine identity
	// work. It is per credential and per process, so two credentials get two
	// buckets and two replicas get two of each ([TD-027]).
	//
	// [TD-027]: docs/TECH_DEBT.md#td-027
	RateLimitCredentialRPS float64

	// ShutdownTimeoutSeconds bounds how long in-flight requests may finish
	// after a SIGTERM before the process exits regardless.
	//
	// It is a ceiling, not a delay: an idle process exits immediately. The
	// value only matters when a request is still running, and it must be
	// shorter than whatever will SIGKILL the process — Docker's default stop
	// grace is 10s, Kubernetes' is 30 — or the drain is decided by the
	// platform instead of here, with the log line cut off mid-sentence.
	ShutdownTimeoutSeconds int

	// AuditRetentionDays is how long durable audit history is kept.
	//
	// There is deliberately no value meaning "keep forever". An audit table
	// that only grows is a disk-exhaustion outage scheduled for whenever the
	// installation gets busy, and an operator who genuinely wants indefinite
	// history needs an export, not an unbounded table. If that requirement
	// arrives it should be an explicit sentinel someone had to type, not
	// something 0 or -1 quietly happens to mean.
	AuditRetentionDays int

	// MetricsEnabled exposes /metrics. Off by default: an installation that has
	// not decided how to protect operational data should expose none of it.
	MetricsEnabled bool

	// MetricsToken is the bearer token a scraper must present. Empty serves
	// /metrics to loopback clients only, which is the single-VPS case.
	MetricsToken string

	// malformed collects values that were PRESENT and unparseable, recorded
	// during LoadConfig because only the loader still has the raw string.
	//
	// They are not applied. An unparseable value falls back to the default for
	// the field so the rest of loading can proceed and report every problem at
	// once, and then Validate refuses the boot. Silently accepting the fallback
	// — which is what the tolerant parsers used to do — leaves an installation
	// configured differently from what its operator believes.
	malformed []string
}

// AdminConsoleClientID returns the Keycloak client ID used by the admin
// console SPA for PKCE login. Falls back to DevPlaygroundClientID when
// ADMIN_CONSOLE_CLIENT_ID is not explicitly set.
func (c *Config) AdminConsoleClientID() string {
	if c.AdminConsoleClientID_ != "" {
		return c.AdminConsoleClientID_
	}
	return c.DevPlaygroundClientID
}

// AdminLiveCheckTTL returns the configured live-admin cache TTL as a
// time.Duration, falling back to a safe default when unset. Centralized so
// the wiring layer doesn't repeat the zero-handling logic.
func (c *Config) AdminLiveCheckTTL() time.Duration {
	if c.AdminLiveCheckTTLSeconds <= 0 {
		return 30 * time.Second
	}
	return time.Duration(c.AdminLiveCheckTTLSeconds) * time.Second
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
//   - DB_MIGRATE_ON_BOOT: Apply pending SQL migrations at startup (default: "true")
//   - SECRETS_MASTER_KEY: base64 32-byte key sealing provider credentials
//     (optional; without it the connection API is not mounted)
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

	// A missing .env is the NORMAL case for every containerised deployment:
	// compose, Kubernetes and systemd all inject the environment directly, and
	// there is no file to find. Warning about it made the first line of a
	// healthy container's log look like something had gone wrong, which is a
	// bad way to start reading a log you are about to trust.
	//
	// It stays at Info because it is still worth knowing which of the two
	// mechanisms supplied the configuration when one of them turns out to be
	// empty. Anything genuinely missing is reported by Validate, by name.
	if err := godotenv.Load(); err != nil {
		log.Info("no .env file; reading configuration from the environment")
	}

	// bad collects present-but-unparseable values as loading proceeds. Passed
	// into the strict parsers by pointer so one traversal both parses and
	// records, and so Validate can report every problem at once instead of one
	// per restart.
	var bad []string

	cfg := &Config{
		Port:                      getEnv("PORT", "8080"),
		DBUrl:                     getEnv("DB_URL", ""),
		DBMigrateOnBoot:           parseBoolStrict("DB_MIGRATE_ON_BOOT", getEnv("DB_MIGRATE_ON_BOOT", ""), true, &bad),
		GinLogEnabled:             parseBoolStrict("GIN_LOG_ENABLED", getEnv("GIN_LOG_ENABLED", ""), true, &bad),
		GinAccessLogEnabled:       parseBoolStrict("GIN_ACCESS_LOG_ENABLED", getEnv("GIN_ACCESS_LOG_ENABLED", ""), true, &bad),
		KeycloakURL:               getEnv("KEYCLOAK_URL", ""),
		KeycloakRealm:             getEnv("KEYCLOAK_REALM", ""),
		KeycloakClientID:          getEnv("KEYCLOAK_CLIENT_ID", ""),
		KeycloakClientSecret:      getEnv("KEYCLOAK_CLIENT_SECRET", ""),
		KeycloakJWKSURL:           getEnv("KEYCLOAK_JWKS_URL", ""),
		KeycloakAllowedClientIDs:  parseCSV(getEnv("KEYCLOAK_ALLOWED_CLIENT_IDS", "")),
		KeycloakAdminClientID:     getEnv("KEYCLOAK_ADMIN_CLIENT_ID", ""),
		KeycloakAdminClientSecret: getEnv("KEYCLOAK_ADMIN_CLIENT_SECRET", ""),
		KeycloakAdminBaseURL:      getEnv("KEYCLOAK_ADMIN_BASE_URL", ""),
		DevPlaygroundEnabled:      parseBoolStrict("DEV_PLAYGROUND_ENABLED", getEnv("DEV_PLAYGROUND_ENABLED", ""), false, &bad),
		DevPlaygroundClientID:     getEnv("DEV_PLAYGROUND_CLIENT_ID", "saas-dev-playground"),
		AdminConsoleClientID_:     getEnv("ADMIN_CONSOLE_CLIENT_ID", ""),
		AdminConsoleEnabled:       parseBoolStrict("ADMIN_CONSOLE_ENABLED", getEnv("ADMIN_CONSOLE_ENABLED", ""), false, &bad),
		AdminLiveCheckTTLSeconds:  parseIntStrict("ADMIN_LIVE_CHECK_TTL_SECONDS", getEnv("ADMIN_LIVE_CHECK_TTL_SECONDS", ""), 0, &bad),
		SecretsMasterKey:          getEnv("SECRETS_MASTER_KEY", ""),
		SecretsKeyringSpec:        getEnv("SECRETS_KEYRING", ""),
		SecretsKeyCurrent:         getEnv("SECRETS_KEY_CURRENT", ""),
		CORSAllowedOrigins:        parseCSV(getEnv("CORS_ALLOWED_ORIGINS", "")),
		RateLimitEdgeRPS:          parseFloatStrict("RATE_LIMIT_EDGE_RPS", getEnv("RATE_LIMIT_EDGE_RPS", ""), 0, &bad),
		RateLimitCredentialRPS:    parseFloatStrict("RATE_LIMIT_CREDENTIAL_RPS", getEnv("RATE_LIMIT_CREDENTIAL_RPS", ""), 0, &bad),
		ShutdownTimeoutSeconds:    parseIntStrict("SHUTDOWN_TIMEOUT_SECONDS", getEnv("SHUTDOWN_TIMEOUT_SECONDS", ""), DefaultShutdownTimeoutSeconds, &bad),
		AuditRetentionDays:        parseIntStrict("AUDIT_RETENTION_DAYS", getEnv("AUDIT_RETENTION_DAYS", ""), DefaultAuditRetentionDays, &bad),
		MetricsEnabled:            parseBoolStrict("METRICS_ENABLED", getEnv("METRICS_ENABLED", ""), false, &bad),
		MetricsToken:              getEnv("METRICS_TOKEN", ""),
	}
	cfg.malformed = bad

	if cfg.KeycloakJWKSURL == "" && cfg.KeycloakURL != "" && cfg.KeycloakRealm != "" {
		cfg.KeycloakJWKSURL = strings.TrimRight(cfg.KeycloakURL, "/") +
			"/realms/" + cfg.KeycloakRealm + "/protocol/openid-connect/certs"
	}

	cfg.Validate()

	return cfg
}

// DefaultShutdownTimeoutSeconds is the drain window when none is configured.
//
// 20 seconds: long enough for a slow Keycloak round trip to finish — provider
// calls are the longest thing a request here does, and the measurements in
// Slice 8 put a p99 write at ~300ms — and short enough to sit under a
// deliberately raised platform grace period. It is a ceiling, so a process with
// nothing in flight exits at once and never waits for it.
const DefaultShutdownTimeoutSeconds = 20

// DefaultAuditRetentionDays is the retention window when none is configured.
//
// 90 days: long enough to cover a quarterly access review and the lag on
// noticing a compromise, short enough that the table stays a working record
// rather than an archive. An installation with a compliance requirement will
// have a number; one without needs a default that is defensible, not generous.
const DefaultAuditRetentionDays = 90

// maxAuditRetentionDays bounds the window at ten years.
//
// Not a policy claim — it is a typo guard. `AUDIT_RETENTION_DAYS=36500000` is
// indistinguishable from "forever", and the whole point of not offering
// "forever" is that unbounded growth should be a decision rather than a
// slipped digit.
const maxAuditRetentionDays = 3650

// AuditRetention returns the retention window as a duration.
func (c *Config) AuditRetention() time.Duration {
	days := c.AuditRetentionDays
	if days <= 0 {
		days = DefaultAuditRetentionDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// ShutdownTimeout returns the drain window as a duration.
func (c *Config) ShutdownTimeout() time.Duration {
	if c.ShutdownTimeoutSeconds <= 0 {
		return DefaultShutdownTimeoutSeconds * time.Second
	}
	return time.Duration(c.ShutdownTimeoutSeconds) * time.Second
}

// Validate lives in validate.go, alongside the rules it enforces.

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

// ─── Strict parsers ─────────────────────────────────────────────────────────
//
// Absent means the default. Present-and-unparseable means the boot fails.
//
// The previous helpers returned the default for both, on the reasoning that a
// typo should not take an installation down. That is right for an absent value
// and wrong for a present one: an operator who typed something meant something,
// and quietly substituting a different number leaves them running an
// installation they cannot reason about. `RATE_LIMIT_CREDENTIAL_RPS=2O` became
// 20 and nothing said so.
//
// Each parser records the problem rather than returning an error, because
// LoadConfig builds a struct literal and threading errors through it would turn
// one readable block into thirty statements. The recorded list is reported all
// at once by Validate, so configuring a fresh deployment is one pass rather
// than a restart per mistake.
//
// The fallback is still applied to the field. Nothing runs with it — Validate
// refuses the boot — but it keeps loading total so every OTHER problem is found
// in the same pass.

func parseBoolStrict(name, value string, fallback bool, bad *[]string) bool {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		*bad = append(*bad, name+" must be true or false (got "+quoteOrEmpty(value)+")")
		return fallback
	}
}

func parseIntStrict(name, value string, fallback int, bad *[]string) int {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		*bad = append(*bad, name+" must be a whole number (got "+quoteOrEmpty(value)+")")
		return fallback
	}
	return n
}

func parseFloatStrict(name, value string, fallback float64, bad *[]string) float64 {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		*bad = append(*bad, name+" must be a number (got "+quoteOrEmpty(value)+")")
		return fallback
	}
	return f
}

// parseCSV splits a comma-separated env-var value into a clean string slice:
// each element trimmed of surrounding whitespace, blank entries dropped.
// "" → nil. "a, b ,, c" → ["a","b","c"].
//
// Used by LoadConfig for KEYCLOAK_ALLOWED_CLIENT_IDS. Keeping the parser
// tolerant of whitespace + stray commas matches how operators actually
// hand-edit env files.
func parseCSV(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
