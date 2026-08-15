package config

import (
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Fail-fast configuration validation.
//
// # The rule
//
// A process must not start with configuration it cannot honour, and it must not
// substitute a value the operator did not choose.
//
// Both halves matter, and the second is the one this file changed. The previous
// helpers were deliberately tolerant: `parseIntDefault` returned the default for
// anything it could not parse, with the reasoning that a typo should not crash
// the boot. That reasoning is right for an absent value and wrong for a present
// one. `RATE_LIMIT_CREDENTIAL_RPS=2O` (letter O) silently became 20 — which is
// harmless — and `ADMIN_LIVE_CHECK_TTL_SECONDS=30s` silently became 30 seconds
// by luck rather than by parsing. The failure mode of tolerance is an
// installation that is not configured the way its operator believes, and that
// belief survives until an incident.
//
// So: absent means the documented default, present-and-unparseable means the
// process refuses to start and says which variable and why.
//
// # Secrets
//
// A validation message names the VARIABLE and never the VALUE when the contract
// marks it secret. "SECRETS_MASTER_KEY must be base64 of exactly 32 bytes (got
// 24 bytes)" is actionable; echoing the key into a log an operator will paste
// into an issue is not.
//
// # What is deliberately not validated here
//
// Reachability. Whether PostgreSQL answers, or Keycloak is up, is a startup
// concern with retries and timeouts — see the boot sequence in cmd/api. This
// file only decides whether the configuration is internally coherent, which is
// a pure function and is tested as one.

// problem is one validation failure, already rendered.
type problem = string

// Validate refuses to start on incoherent configuration.
//
// It calls log.Fatal so a misconfigured process exits before serving traffic.
// The decision logic lives in validationProblems, which returns rather than
// exits, so every rule is testable without a subprocess.
func (c *Config) Validate() {
	problems := c.validationProblems()
	if len(problems) == 0 {
		return
	}

	log.Error("configuration is not usable — refusing to start:")
	for _, p := range problems {
		log.Error("  • " + p)
	}
	// Points at the configuration REFERENCE rather than the deployment guide.
	// Someone reading this line is holding a specific variable and needs the
	// table that explains it, which is RUNNING.md §2 — generated from the same
	// contract these problems are derived from.
	log.Fatal("fix the " + plural(len(problems), "problem", "problems") +
		" above; every variable is explained in .env.example and docs/operations/RUNNING.md §2")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// validationProblems returns every problem found, not just the first.
//
// All of them, deliberately. Reporting one at a time turns configuring a fresh
// deployment into a sequence of restarts, each revealing the next thing wrong —
// which is precisely the trial-and-error experience this slice exists to remove.
func (c *Config) validationProblems() []problem {
	var out []problem

	// Anything LoadConfig could not parse. Recorded there because only the
	// loader sees the raw string; by the time it reaches a typed field the
	// evidence is gone.
	out = append(out, c.malformed...)

	// ── Required ────────────────────────────────────────────────────────────
	//
	// Driven by the contract table, so declaring a variable required is enough
	// to make the boot enforce it. A hand-written list here was a second place
	// to forget.
	for _, name := range RequiredProcessVars() {
		if c.valueOf(name) == "" {
			out = append(out, name+" is required and is not set — "+purposeOf(name))
		}
	}

	// JWKS is required but derivable, so it gets its own message: telling an
	// operator to set it when KEYCLOAK_URL + KEYCLOAK_REALM would have produced
	// it sends them to configure something they do not need.
	if c.KeycloakJWKSURL == "" {
		out = append(out, "KEYCLOAK_JWKS_URL is empty and could not be derived — "+
			"set it, or set both KEYCLOAK_URL and KEYCLOAK_REALM")
	}

	// ── URLs ────────────────────────────────────────────────────────────────
	out = appendIf(out, absoluteHTTPURL("KEYCLOAK_URL", c.KeycloakURL))
	out = appendIf(out, absoluteHTTPURL("KEYCLOAK_JWKS_URL", c.KeycloakJWKSURL))
	out = appendIf(out, absoluteHTTPURL("KEYCLOAK_ADMIN_BASE_URL", c.KeycloakAdminBaseURL))
	out = appendIf(out, postgresURL("DB_URL", c.DBUrl))

	for _, origin := range c.CORSAllowedOrigins {
		if p := corsOrigin(origin); p != "" {
			out = append(out, p)
		}
	}

	// ── Numbers ─────────────────────────────────────────────────────────────
	out = appendIf(out, portNumber("PORT", c.Port))

	if c.ShutdownTimeoutSeconds <= 0 {
		out = append(out, "SHUTDOWN_TIMEOUT_SECONDS must be a positive number of seconds; "+
			"an unbounded shutdown leaves a process that never exits")
	}
	if c.ShutdownTimeoutSeconds > maxShutdownTimeoutSeconds {
		out = append(out, "SHUTDOWN_TIMEOUT_SECONDS is "+strconv.Itoa(c.ShutdownTimeoutSeconds)+
			", above the "+strconv.Itoa(maxShutdownTimeoutSeconds)+"s ceiling — an orchestrator "+
			"will SIGKILL long before that, so the value would only delay the log line saying why")
	}

	// Rate limits: negative is the interesting case. Zero means "the default"
	// by documented contract, but a negative number is someone trying to switch
	// the limiter off, and it must not read as "use the default" silently.
	if c.RateLimitEdgeRPS < 0 {
		out = append(out, "RATE_LIMIT_EDGE_RPS is negative — "+
			"rate limiting cannot be switched off; omit the variable for the default")
	}
	if c.RateLimitCredentialRPS < 0 {
		out = append(out, "RATE_LIMIT_CREDENTIAL_RPS is negative — "+
			"rate limiting cannot be switched off; omit the variable for the default")
	}
	// Retention: zero is refused rather than defaulted, unlike the tuning knobs
	// above. A rate limit of 0 falling back to the default is harmless; an
	// audit retention of 0 that fell back would hide an operator who typed a
	// number meaning "keep nothing" and got 90 days — which is the opposite of
	// what they asked for, in the direction of keeping data they wanted gone.
	if c.AuditRetentionDays < 0 {
		out = append(out, "AUDIT_RETENTION_DAYS cannot be negative")
	}
	if c.AuditRetentionDays == 0 && auditRetentionWasSet(c) {
		out = append(out, "AUDIT_RETENTION_DAYS is 0, which would delete every audit event on "+
			"the next sweep — there is no value meaning \"keep forever\"; omit the variable for "+
			strconv.Itoa(DefaultAuditRetentionDays)+" days")
	}
	if c.AuditRetentionDays > maxAuditRetentionDays {
		out = append(out, "AUDIT_RETENTION_DAYS is "+strconv.Itoa(c.AuditRetentionDays)+
			", above the "+strconv.Itoa(maxAuditRetentionDays)+"-day ceiling — that is almost "+
			"certainly a typo, and unbounded audit growth should be a deliberate decision")
	}

	if c.AdminLiveCheckTTLSeconds < 0 {
		out = append(out, "ADMIN_LIVE_CHECK_TTL_SECONDS is negative; omit it for the default")
	}

	// ── Secrets ─────────────────────────────────────────────────────────────
	//
	// One check, because there is one parser. Keyring() is the same function
	// the server and the rotation CLI call, so a configuration that boots is by
	// construction one they can both build a keyring from — as opposed to a
	// validator that agrees with the loader only while someone maintains both.
	if _, err := c.Keyring(); err != nil {
		out = append(out, problem(err.Error()))
	}

	// ── Combinations that cannot be honoured ────────────────────────────────
	idSet := c.KeycloakAdminClientID != ""
	secretSet := c.KeycloakAdminClientSecret != ""
	if idSet != secretSet {
		out = append(out, "KEYCLOAK_ADMIN_CLIENT_ID and KEYCLOAK_ADMIN_CLIENT_SECRET must be "+
			"set together or not at all (id_set="+strconv.FormatBool(idSet)+
			", secret_set="+strconv.FormatBool(secretSet)+") — "+
			"half-configured, the /admin surface would be silently absent")
	}

	// The console is a browser client: it needs a client id to run PKCE
	// against, and it falls back to the playground's. In production the
	// playground is off, so relying on that fallback means the console logs in
	// as a client whose purpose is development.
	if c.AdminConsoleEnabled && c.AdminConsoleClientID_ == "" && !c.DevPlaygroundEnabled {
		out = append(out, "ADMIN_CONSOLE_ENABLED is true but ADMIN_CONSOLE_CLIENT_ID is unset — "+
			"the console would fall back to DEV_PLAYGROUND_CLIENT_ID, which is a "+
			"development client and is not what a production console should authenticate as")
	}

	return out
}

// auditRetentionWasSet distinguishes "the operator typed 0" from "the field is
// simply zero because nothing set it".
//
// LoadConfig substitutes the default for an absent value, so a zero here can
// only come from an explicit "0" — except in a hand-built Config, which is
// every test. Reading the environment directly is what keeps the distinction
// honest without threading a "was present" flag through the loader.
func auditRetentionWasSet(c *Config) bool {
	_, present := os.LookupEnv("AUDIT_RETENTION_DAYS")
	return present
}

// maxShutdownTimeoutSeconds bounds the drain window.
//
// Chosen against what actually kills the process: Docker's default stop grace
// is 10s and Kubernetes' terminationGracePeriodSeconds is 30. A drain longer
// than either is not a longer drain, it is a SIGKILL with extra steps and a
// truncated log. 120 leaves room for an operator who has raised the platform's
// grace period deliberately, and refuses the value that can only be a mistake.
const maxShutdownTimeoutSeconds = 120

func appendIf(out []problem, p problem) []problem {
	if p == "" {
		return out
	}
	return append(out, p)
}

// valueOf reads a required variable off the parsed config by name.
//
// Required variables are all plain strings, so this stays a small switch rather
// than reflection. A required variable added to the contract without a case
// here is caught by TestContract_RequiredVarsAreDerivedNotRepeated.
func (c *Config) valueOf(name string) string {
	switch name {
	case "DB_URL":
		return c.DBUrl
	case "KEYCLOAK_URL":
		return c.KeycloakURL
	case "KEYCLOAK_REALM":
		return c.KeycloakRealm
	case "KEYCLOAK_CLIENT_ID":
		return c.KeycloakClientID
	default:
		return ""
	}
}

func purposeOf(name string) string {
	if s, ok := SettingByName(name); ok {
		return s.Purpose
	}
	return "see .env.example"
}

// absoluteHTTPURL rejects the mistakes that actually happen: a bare host with
// no scheme, and a path-only value copied out of a different config.
func absoluteHTTPURL(name, raw string) problem {
	if raw == "" {
		return "" // absence is handled by the required check
	}
	u, err := url.Parse(raw)
	if err != nil {
		return name + " is not a valid URL"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return name + " must be an absolute http:// or https:// URL (got scheme " +
			quoteOrEmpty(u.Scheme) + ")"
	}
	if u.Host == "" {
		return name + " has no host"
	}
	return ""
}

// postgresURL checks the shape without ever echoing the value: DB_URL carries
// the database password.
func postgresURL(name, raw string) problem {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return name + " is not a valid URL (value withheld: it contains the database password)"
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return name + " must start with postgres:// or postgresql:// (got scheme " +
			quoteOrEmpty(u.Scheme) + ")"
	}
	if u.Host == "" {
		return name + " has no host"
	}
	if strings.TrimPrefix(u.Path, "/") == "" {
		return name + " names no database (expected postgres://user:pass@host:port/dbname)"
	}
	return ""
}

// corsOrigin rejects the two values that look right and are not: a trailing
// slash, which never matches a browser's Origin header, and "*" combined with
// credentials, which no browser honours.
func corsOrigin(origin string) problem {
	if origin == "*" {
		return "CORS_ALLOWED_ORIGINS contains \"*\", which browsers refuse when credentials " +
			"are allowed — list the origins explicitly"
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "CORS_ALLOWED_ORIGINS entry " + quoteOrEmpty(origin) +
			" is not an origin (expected scheme://host[:port])"
	}
	if u.Path != "" && u.Path != "/" {
		return "CORS_ALLOWED_ORIGINS entry " + quoteOrEmpty(origin) +
			" has a path; an Origin header never carries one"
	}
	if strings.HasSuffix(origin, "/") {
		return "CORS_ALLOWED_ORIGINS entry " + quoteOrEmpty(origin) +
			" has a trailing slash; it will never match a browser's Origin header"
	}
	return ""
}

func portNumber(name, raw string) problem {
	if raw == "" {
		return ""
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return name + " must be a number (got " + quoteOrEmpty(raw) + ")"
	}
	if n < 1 || n > 65535 {
		return name + " must be between 1 and 65535 (got " + raw + ")"
	}
	return ""
}

// Key-material validation lives in secrets.go, in the same function the server
// and the rotation CLI use to build the keyring. It used to live here as a
// second base64 decoder, which was one more place for "what counts as a valid
// key" to drift from the place that actually opens the rows.

// quoteOrEmpty renders a non-secret value for a message.
func quoteOrEmpty(s string) string {
	if s == "" {
		return "empty"
	}
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return "\"" + s + "\""
}
