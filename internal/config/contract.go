package config

import (
	"sort"
	"strings"
)

// The configuration contract.
//
// This table is the single declaration of every environment variable this
// installation involves — what reads it, whether it is required, what it
// defaults to, and whether it holds a secret.
//
// # Why a table and not four documents
//
// [TD-004] is the recurring failure this exists to end: the process grew
// variables and `docker-compose.yml`, `.env.example` and the deployment guide
// each fell behind independently. The symptom is always the same and always
// expensive — a deployment that boots, looks healthy, and quietly has a feature
// switched off because the container never received the variable that enables
// it. Nothing fails; the operator discovers it by trial and error.
//
// Adding four lines by hand fixes the instance and not the mechanism. So the
// declaration moved into the code, next to the loader that consumes it, and two
// things now read it:
//
//	Validate            refuses to boot on missing or malformed configuration
//	contract_test.go    fails the build when the code, .env.example or
//	                    docker-compose.yml disagree with this table
//
// A variable added to LoadConfig without a line here fails a test. A line here
// that never reaches the container fails a test. The table cannot be
// out of date and green at the same time.
//
// # What this is not
//
// It is not a settings framework and it does not load anything. LoadConfig
// still reads each variable explicitly, by name, in one readable block —
// generating that from the table would trade a legible function for a
// reflective one to save nothing.
//
// [TD-004]: docs/TECH_DEBT.md#td-004

// Consumer is who reads a variable. A variable can be essential to the
// deployment and never be seen by the Go process — the database credentials
// compose uses to build DB_URL are the obvious case — and conflating the two is
// how "the API does not read POSTGRES_USER" turns into someone deleting it.
type Consumer string

const (
	// ConsumerProcess — read by the API process, in LoadConfig.
	ConsumerProcess Consumer = "process"

	// ConsumerCompose — read by docker-compose.yml to build the reference
	// deployment. Never reaches the API as-is.
	ConsumerCompose Consumer = "compose"

	// ConsumerBootstrap — read by the bootstrap/seed tooling only.
	ConsumerBootstrap Consumer = "bootstrap"
)

// Requirement is what happens when a variable is absent.
type Requirement string

const (
	// RequirementRequired — the process refuses to start without it. There is
	// no safe default and guessing one would mean serving traffic with an auth
	// stack nobody configured.
	RequirementRequired Requirement = "required"

	// RequirementOptional — absent means a documented default, or a feature
	// that is simply not mounted. Absence is a legitimate deployment state.
	RequirementOptional Requirement = "optional"

	// RequirementDevOnly — exists for local development and must not be set in
	// production. Kept in the table rather than left undeclared precisely so
	// the distinction is visible to whoever writes the production values.
	RequirementDevOnly Requirement = "dev-only"
)

// Setting is one variable's contract.
type Setting struct {
	// Name is the environment variable.
	Name string

	// Consumer is who reads it.
	Consumer Consumer

	// Requirement is what absence means.
	Requirement Requirement

	// Default is the value used when absent, for documentation. Empty when
	// there is none, or when the meaning of absence is "feature off" rather
	// than "substitute this".
	Default string

	// Secret marks a value that must never be logged, echoed in an error, or
	// committed with a real value. Validation errors about a Secret name the
	// variable and never the value.
	Secret bool

	// Purpose is one line, written for whoever is filling in a .env on a VPS.
	Purpose string
}

// settings is the contract. Grouped the way an operator fills them in.
var settings = []Setting{
	// ── Server ──────────────────────────────────────────────────────────────
	{
		Name: "PORT", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Default: "8080",
		Purpose: "TCP port the API listens on",
	},
	{
		Name: "GIN_LOG_ENABLED", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Default: "true",
		Purpose: "Gin framework debug logs; false switches Gin to release mode",
	},
	{
		Name: "GIN_ACCESS_LOG_ENABLED", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Default: "true",
		Purpose: "per-request access log lines",
	},
	{
		Name: "SHUTDOWN_TIMEOUT_SECONDS", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Default: "20",
		Purpose: "how long in-flight requests may finish after SIGTERM before the process exits anyway",
	},

	// ── Database ────────────────────────────────────────────────────────────
	{
		Name: "DB_URL", Consumer: ConsumerProcess, Requirement: RequirementRequired,
		Secret:  true,
		Purpose: "PostgreSQL connection string (contains the password)",
	},
	{
		Name: "DB_MIGRATE_ON_BOOT", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Default: "true",
		Purpose: "apply pending SQL migrations at startup; false hands them to a separate deploy step",
	},

	// ── Operator authentication (OIDC) ──────────────────────────────────────
	{
		Name: "KEYCLOAK_URL", Consumer: ConsumerProcess, Requirement: RequirementRequired,
		Purpose: "issuer base URL as CLIENTS see it — drives the expected `iss` claim",
	},
	{
		Name: "KEYCLOAK_REALM", Consumer: ConsumerProcess, Requirement: RequirementRequired,
		Purpose: "realm operators authenticate against (the installation realm, not a workspace's)",
	},
	{
		Name: "KEYCLOAK_CLIENT_ID", Consumer: ConsumerProcess, Requirement: RequirementRequired,
		Purpose: "OIDC client id the console logs in with",
	},
	{
		Name: "KEYCLOAK_CLIENT_SECRET", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Secret:  true,
		Purpose: "only for a confidential client; a public PKCE client needs none",
	},
	{
		Name: "KEYCLOAK_JWKS_URL", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Default: "derived from KEYCLOAK_URL + KEYCLOAK_REALM",
		Purpose: "override when the API reaches Keycloak on a different address than clients do",
	},
	{
		Name: "KEYCLOAK_ALLOWED_CLIENT_IDS", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Purpose: "comma-separated client ids accepted in the token's azp/aud; empty accepts any",
	},

	// ── Legacy /admin/* identity management ─────────────────────────────────
	{
		Name: "KEYCLOAK_ADMIN_CLIENT_ID", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Purpose: "service-account client for /admin/*; unset omits the whole /admin surface",
	},
	{
		Name: "KEYCLOAK_ADMIN_CLIENT_SECRET", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Secret:  true,
		Purpose: "secret for the above; both must be set or neither",
	},
	{
		Name: "KEYCLOAK_ADMIN_BASE_URL", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Default: "KEYCLOAK_URL",
		Purpose: "where the API reaches the Keycloak admin API — often an internal address",
	},
	{
		Name: "ADMIN_LIVE_CHECK_TTL_SECONDS", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Default: "30",
		Purpose: "how long a live-admin check is cached; bounds the out-of-band revocation window",
	},

	// ── Workspace connections ───────────────────────────────────────────────
	{
		Name: "SECRETS_KEYRING", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Secret: true,
		Purpose: "versioned keys `1:<base64>,2:<base64>`; every version can decrypt. " +
			"Unset omits connections AND workspace identity",
	},
	{
		Name: "SECRETS_KEY_CURRENT", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Purpose: "which SECRETS_KEYRING version seals NEW secrets. Optional with a single key, " +
			"required with more than one",
	},
	{
		Name: "SECRETS_MASTER_KEY", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Secret: true,
		Purpose: "LEGACY single key; equivalent to SECRETS_KEYRING=1:<base64>. " +
			"Cannot be combined with SECRETS_KEYRING",
	},

	// ── Console and browser clients ─────────────────────────────────────────
	{
		Name: "ADMIN_CONSOLE_ENABLED", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Default: "false",
		Purpose: "serve the admin console SPA at /admin",
	},
	{
		Name: "ADMIN_CONSOLE_CLIENT_ID", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Default: "DEV_PLAYGROUND_CLIENT_ID",
		Purpose: "public PKCE client the console logs in with",
	},
	{
		Name: "CORS_ALLOWED_ORIGINS", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Purpose: "comma-separated browser origins allowed to call the API; empty disables CORS",
	},

	// ── Rate limiting ───────────────────────────────────────────────────────
	{
		Name: "RATE_LIMIT_EDGE_RPS", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Default: "10",
		Purpose: "per-IP allowance before authentication; meters anonymous and operator traffic",
	},
	{
		Name: "RATE_LIMIT_CREDENTIAL_RPS", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Default: "20",
		Purpose: "per-credential allowance — the number a machine consumer can actually reach",
	},

	// ── Audit ───────────────────────────────────────────────────────────────
	{
		Name: "AUDIT_RETENTION_DAYS", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Default: "90",
		Purpose: "how long durable audit history is kept before a daily sweep deletes it",
	},

	// ── Metrics ─────────────────────────────────────────────────────────────
	{
		Name: "METRICS_ENABLED", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Default: "false",
		Purpose: "expose Prometheus metrics at /metrics",
	},
	{
		Name: "METRICS_TOKEN", Consumer: ConsumerProcess, Requirement: RequirementOptional,
		Secret:  true,
		Purpose: "bearer token a scraper must present; empty serves /metrics to loopback only",
	},

	// ── Development only ────────────────────────────────────────────────────
	{
		Name: "DEV_PLAYGROUND_ENABLED", Consumer: ConsumerProcess, Requirement: RequirementDevOnly,
		Default: "false",
		Purpose: "exposes an unauthenticated login playground at /dev/auth. NEVER true in production",
	},
	{
		Name: "DEV_PLAYGROUND_CLIENT_ID", Consumer: ConsumerProcess, Requirement: RequirementDevOnly,
		Default: "saas-dev-playground",
		Purpose: "client id the playground uses",
	},

	// ── Reference deployment (docker-compose) ───────────────────────────────
	//
	// Never read by the API. They exist so compose can stand up the services
	// the API talks to, and DB_URL is assembled from three of them.
	{
		Name: "POSTGRES_USER", Consumer: ConsumerCompose, Requirement: RequirementRequired,
		Purpose: "application database user; compose builds DB_URL from this",
	},
	{
		Name: "POSTGRES_PASSWORD", Consumer: ConsumerCompose, Requirement: RequirementRequired,
		Secret: true, Purpose: "application database password",
	},
	{
		Name: "POSTGRES_DB", Consumer: ConsumerCompose, Requirement: RequirementRequired,
		Purpose: "application database name",
	},
	{
		Name: "KC_DB_USER", Consumer: ConsumerCompose, Requirement: RequirementOptional,
		Purpose: "database user for the bundled dev Keycloak (profile `dev-idp`)",
	},
	{
		Name: "KC_DB_PASSWORD", Consumer: ConsumerCompose, Requirement: RequirementOptional,
		Secret: true, Purpose: "database password for the bundled dev Keycloak",
	},
	{
		Name: "KC_DB_NAME", Consumer: ConsumerCompose, Requirement: RequirementOptional,
		Purpose: "database name for the bundled dev Keycloak",
	},
	{
		Name: "KEYCLOAK_ADMIN", Consumer: ConsumerCompose, Requirement: RequirementDevOnly,
		Purpose: "bootstrap admin username for the bundled dev Keycloak",
	},
	{
		Name: "KEYCLOAK_ADMIN_PASSWORD", Consumer: ConsumerCompose, Requirement: RequirementDevOnly,
		Secret: true, Purpose: "bootstrap admin password for the bundled dev Keycloak",
	},
	{
		Name: "KC_HOST_PORT", Consumer: ConsumerCompose, Requirement: RequirementOptional,
		Default: "8081",
		Purpose: "host port the bundled dev Keycloak is published on",
	},
	{
		Name: "API_HOST_PORT", Consumer: ConsumerCompose, Requirement: RequirementOptional,
		Default: "8080",
		Purpose: "host port the API is published on; PORT is the port inside the container",
	},
	{
		Name: "POSTGRES_HOST_PORT", Consumer: ConsumerCompose, Requirement: RequirementOptional,
		Default: "5432",
		Purpose: "host port the application database is published on; change when 5432 is taken",
	},

	// ── Bootstrap tooling ───────────────────────────────────────────────────
	{
		Name: "SEED_USER_PASSWORD", Consumer: ConsumerBootstrap, Requirement: RequirementDevOnly,
		Secret: true, Purpose: "password given to seed users by the bootstrap CLI",
	},
}

// Settings returns the contract, sorted by name.
func Settings() []Setting {
	out := append([]Setting(nil), settings...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SettingsFor returns the contract entries a given consumer reads.
func SettingsFor(c Consumer) []Setting {
	var out []Setting
	for _, s := range Settings() {
		if s.Consumer == c {
			out = append(out, s)
		}
	}
	return out
}

// SettingByName returns one entry.
func SettingByName(name string) (Setting, bool) {
	for _, s := range settings {
		if s.Name == name {
			return s, true
		}
	}
	return Setting{}, false
}

// IsSecret reports whether a variable holds secret material. Used by the
// validation errors, which name a variable but never print one of these.
func IsSecret(name string) bool {
	s, ok := SettingByName(name)
	return ok && s.Secret
}

// RequiredProcessVars are the variables the process refuses to start without.
// Derived from the table rather than repeated, so Validate cannot drift from
// the documentation the same table generates.
func RequiredProcessVars() []string {
	var out []string
	for _, s := range Settings() {
		if s.Consumer == ConsumerProcess && s.Requirement == RequirementRequired {
			out = append(out, s.Name)
		}
	}
	return out
}

// RenderContractTable renders the contract as a Markdown table.
//
// The documentation is generated from the same declaration the gate enforces,
// which is the only way a published matrix stays true: a hand-written one is a
// second source that starts drifting the day it is written.
func RenderContractTable() string {
	var b strings.Builder
	b.WriteString("| Variable | Consumer | Required | Default | Secret | Purpose |\n")
	b.WriteString("|---|---|---|---|:--:|---|\n")
	for _, s := range Settings() {
		def := s.Default
		if def == "" {
			def = "—"
		}
		secret := ""
		if s.Secret {
			secret = "yes"
		}
		b.WriteString("| `" + s.Name + "` | " + string(s.Consumer) + " | " +
			string(s.Requirement) + " | " + def + " | " + secret + " | " + s.Purpose + " |\n")
	}
	return b.String()
}
