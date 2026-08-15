package bootstrap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DevPlaygroundClientID is the public OIDC client id used by the browser-
// based dev auth playground (PKCE flow). Hardcoded because the playground
// HTML/JS shipped in web/dev/ is also DEV-ONLY and doesn't need this
// configurable — flipping features.dev_playground in project.json controls
// whether it's registered at all.
const DevPlaygroundClientID = "saas-dev-playground"

// IdentityAdminClientID is the confidential OIDC client whose service-account
// the API uses to call Keycloak's Admin REST API for identity management
// (users, roles, sessions, password actions). Separate from the
// auth.client.id (saas-backend) so the token-validation client and the
// admin client have independent secrets and can be rotated independently.
// Registered only when features.identity_management=true.
const IdentityAdminClientID = "saas-backend-admin"

// Secrets are the credentials that never enter project.json. They are read
// from .env on regen and prompted for on `make init`.
type Secrets struct {
	AdminPassword     string // Keycloak bootstrap admin password
	ClientSecret      string // saas-backend confidential client secret
	SeedUserPassword  string // shared password for seed_users[]
	AdminClientSecret string // saas-backend-admin service-account secret
}

// defaultSecrets returns dev-friendly placeholders used when no .env exists.
func defaultSecrets() Secrets {
	return Secrets{
		AdminPassword:     "admin",
		ClientSecret:      "saas-backend-secret",
		SeedUserPassword:  "password",
		AdminClientSecret: "saas-backend-admin-secret",
	}
}

// LoadSecrets reads credentials from <repoRoot>/.env. Missing or unset values
// fall back to defaultSecrets so a fresh clone produces a working stack.
func LoadSecrets(repoRoot string) Secrets {
	s := defaultSecrets()
	for k, v := range parseEnvFile(filepath.Join(repoRoot, ".env")) {
		if v == "" {
			continue
		}
		switch k {
		case "KEYCLOAK_ADMIN_PASSWORD":
			s.AdminPassword = v
		case "KEYCLOAK_CLIENT_SECRET":
			s.ClientSecret = v
		case "SEED_USER_PASSWORD":
			s.SeedUserPassword = v
		case "KEYCLOAK_ADMIN_CLIENT_SECRET":
			s.AdminClientSecret = v
		}
	}
	return s
}

// Preserved carries the .env values a regeneration must NOT invent.
//
// # Why this exists
//
// `make regen` used to emit a fixed list of variables built from
// project.json. Everything an operator had configured that project.json has no
// opinion about — the secrets keyring, the console's PKCE client id, CORS
// origins, rate limits, metrics — was simply not in the list, so regenerating
// deleted it. The installation kept booting and quietly lost the connection
// API, the console login, or its allowed origins: exactly the silent
// half-configured deployment [TD-004] and the configuration contract exist to
// prevent.
//
// So variables now fall into two classes, and the class decides who wins:
//
//	project-derived  regenerated from project.json every time — realm, client
//	                 id, ports, database name. Editing project.json and
//	                 re-running regen is how these are meant to change.
//	operator-owned   project.json cannot express them. The value already in
//	                 .env wins; the default is only a starting point for a
//	                 file that does not exist yet.
//
// A variable is operator-owned by not being derivable, not by being secret —
// CORS_ALLOWED_ORIGINS is no more guessable from project.json than the keyring
// is.
//
// [TD-004]: docs/TECH_DEBT.md#td-004
type Preserved map[string]string

// LoadPreserved reads every KEY=VALUE already present in <repoRoot>/.env.
// A missing file yields an empty map, which makes every default apply — the
// fresh-clone case.
func LoadPreserved(repoRoot string) Preserved {
	return Preserved(parseEnvFile(filepath.Join(repoRoot, ".env")))
}

// keep returns the operator's existing value for name, or def when the file
// had no non-empty entry for it.
//
// An entry explicitly set to empty is treated as absent rather than as a
// deliberate empty value. The distinction matters for exactly one variable —
// KEYCLOAK_JWKS_URL, where empty means "derive it" — and preserving an empty
// there would be indistinguishable from the default anyway.
func (p Preserved) keep(name, def string) string {
	if v, ok := p[name]; ok && v != "" {
		return v
	}
	return def
}

// parseEnvFile reads a dotenv-style file into a map. Comments and lines
// without '=' are skipped. Not a full dotenv implementation: no quoting, no
// interpolation, no `export` prefix — this reads files this package wrote.
func parseEnvFile(path string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(path) // #nosec G304 -- path is repoRoot/.env
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

// GenerateAll regenerates every file owned by the bootstrap layer from the
// given ProjectConfig + Secrets. Paths are resolved relative to repoRoot.
//
// Owned files (rewritten in place):
//   - .env
//   - .env.example
//   - config/project.schema.json   (mirror of the embedded canonical copy)
//   - deploy/keycloak/realm-export.json
//
// TODO (future): docker-compose override, README snippet, frontend env file.
func GenerateAll(repoRoot string, cfg *ProjectConfig, secrets Secrets) error {
	// The working .env keeps whatever the operator already put in it; the
	// example is always the pristine defaults, and must never absorb a value
	// from a real installation — it is the file that gets committed.
	if err := writeEnv(filepath.Join(repoRoot, ".env"), cfg, secrets, LoadPreserved(repoRoot), false); err != nil {
		return err
	}
	if err := writeEnv(filepath.Join(repoRoot, ".env.example"), cfg, defaultSecrets(), Preserved{}, true); err != nil {
		return err
	}
	if err := writeSchemaFile(filepath.Join(repoRoot, "config/project.schema.json")); err != nil {
		return err
	}
	if err := writeRealmExport(filepath.Join(repoRoot, "deploy/keycloak/realm-export.json"), cfg, secrets); err != nil {
		return err
	}
	return nil
}

// writeSchemaFile materializes the embedded canonical schema to disk.
// IDEs reference it via the $schema key in project.json — the file must
// exist at that path. The embedded copy stays the source of truth.
func writeSchemaFile(path string) error {
	return os.WriteFile(path, schemaJSON, 0o644)
}

// writeEnv emits a deterministic .env file. annotate=true adds the inline
// guidance suitable for .env.example; false omits it for the working .env.
// All secret values come from the Secrets parameter — they are never sourced
// from cfg, which is committed to git.
func writeEnv(path string, cfg *ProjectConfig, secrets Secrets, keep Preserved, annotate bool) error {
	var b strings.Builder
	hdr := func(s string) {
		b.WriteString("# =====================================================\n")
		b.WriteString("# " + s + "\n")
		b.WriteString("# =====================================================\n")
	}
	// note emits guidance only into .env.example. The working .env is a file
	// an operator greps and edits, and re-explaining every knob on every regen
	// buries their own values in prose.
	note := func(lines ...string) {
		if !annotate {
			return
		}
		for _, l := range lines {
			b.WriteString("# " + l + "\n")
		}
	}
	db := sanitize(cfg.Project.Name) + "_db"

	if annotate {
		b.WriteString(`# LIGHTWEIGHT configuration.
#
# Copy to .env and change the four values under REQUIRED. Everything below
# that section already has a working default; you can install without reading
# any of it.
#
#   ./scripts/init.sh     does the copy and generates the secret key for you
#
# The defaults describe the EVALUATION stack: the throwaway Keycloak that
# ` + "`docker compose --profile dev-idp up -d`" + ` starts for you. Self-hosting against
# a Keycloak you already run means changing the three KEYCLOAK_* values in
# REQUIRED and blanking the two container-internal overrides marked there.
#
# Full reference, every variable: docs/operations/RUNNING.md §2.
# That table is generated from internal/config/contract.go, which is also what
# refuses to boot on a bad value — so it cannot drift from this file.

`)
	}

	// ── REQUIRED ────────────────────────────────────────────────────────────
	hdr("REQUIRED — the process refuses to start without these")
	note(
		"Where operators log in. This is the INSTALLATION realm — the one that",
		"says who may administer LIGHTWEIGHT. It is NOT a realm you manage with",
		"it: those are attached later, per workspace, through the console.",
		"",
		"KEYCLOAK_URL is the issuer as a BROWSER reaches it, because it decides",
		"the `iss` claim tokens must carry. Getting it wrong rejects every token",
		"with `invalid issuer` while everything else looks healthy.",
	)
	b.WriteString(fmt.Sprintf("KEYCLOAK_URL=http://localhost:%d\n", cfg.Ports.Keycloak))
	b.WriteString(fmt.Sprintf("KEYCLOAK_REALM=%s\n", cfg.Auth.Realm))
	b.WriteString(fmt.Sprintf("KEYCLOAK_CLIENT_ID=%s\n", cfg.Auth.Client.ID))
	b.WriteString("\n")
	note(
		"The public PKCE client the operator console at /admin logs in with.",
		"Its Keycloak redirect URI must include <this installation's URL>/admin/*",
		"— see docs/getting-started/KEYCLOAK_SETUP.md §1.",
	)
	b.WriteString(fmt.Sprintf("ADMIN_CONSOLE_CLIENT_ID=%s\n\n", keep.keep("ADMIN_CONSOLE_CLIENT_ID", "")))
	note(
		"CONTAINER-INTERNAL OVERRIDES — only for the bundled evaluation Keycloak,",
		"which the API reaches on the compose network under a different address",
		"than your browser does. Pointing at your own Keycloak? BLANK BOTH: empty",
		"derives them from KEYCLOAK_URL, which is then the only address there is.",
	)
	b.WriteString(fmt.Sprintf("KEYCLOAK_JWKS_URL=http://keycloak:8080/realms/%s/protocol/openid-connect/certs\n", cfg.Auth.Realm))
	b.WriteString("KEYCLOAK_ADMIN_BASE_URL=http://keycloak:8080\n\n")

	// ── DATABASE ────────────────────────────────────────────────────────────
	hdr("DATABASE")
	note(
		"compose builds the API's DB_URL from the three POSTGRES_* values and",
		"hands it to the container pointed at the `postgres` service. DB_URL",
		"itself is for running the API or the tools OUTSIDE compose.",
		"",
		"CHANGE POSTGRES_PASSWORD before exposing this installation to anything.",
	)
	b.WriteString(fmt.Sprintf("POSTGRES_USER=%s\n", keep.keep("POSTGRES_USER", "postgres")))
	b.WriteString(fmt.Sprintf("POSTGRES_PASSWORD=%s\n", keep.keep("POSTGRES_PASSWORD", "postgres")))
	b.WriteString(fmt.Sprintf("POSTGRES_DB=%s\n", db))
	b.WriteString(fmt.Sprintf("DB_URL=postgres://%s:%s@localhost:%d/%s?sslmode=disable\n",
		keep.keep("POSTGRES_USER", "postgres"), keep.keep("POSTGRES_PASSWORD", "postgres"),
		cfg.Ports.Postgres, db))
	note(
		"Apply pending SQL migrations at API startup. Set false when a separate",
		"deploy step runs `migrate up`, or when replicas start together",
		"(docs/MIGRATIONS.md).",
	)
	b.WriteString(fmt.Sprintf("DB_MIGRATE_ON_BOOT=%s\n\n", keep.keep("DB_MIGRATE_ON_BOOT", "true")))

	// ── SECRETS ─────────────────────────────────────────────────────────────
	hdr("SECRETS — the keyring that seals provider credentials at rest")
	note(
		"Generate with: openssl rand -base64 32   (./scripts/init.sh does this)",
		"",
		"Format: <version>:<base64 32-byte key>, comma-separated. Every version",
		"listed can DECRYPT; SECRETS_KEY_CURRENT names the one that ENCRYPTS.",
		"With a single key, SECRETS_KEY_CURRENT may stay empty.",
		"",
		"OPTIONAL, but only in the sense that LIGHTWEIGHT starts without it: with",
		"no key the connection API and the workspace identity runtime are not",
		"mounted at all, so there is no way to attach a realm to a workspace and",
		"nothing a project credential can reach. An installation you intend to",
		"USE needs this set.",
		"",
		"BACK IT UP SEPARATELY FROM THE DATABASE. A pg_dump without the keyring",
		"restores rows nobody can read. Rotation: docs/SECRET_KEY_ROTATION.md.",
	)
	b.WriteString(fmt.Sprintf("SECRETS_KEYRING=%s\n", keep.keep("SECRETS_KEYRING", "")))
	b.WriteString(fmt.Sprintf("SECRETS_KEY_CURRENT=%s\n", keep.keep("SECRETS_KEY_CURRENT", "")))
	note(
		"LEGACY single-key form, still honoured: equivalent to SECRETS_KEYRING=1:<key>.",
		"Setting it together with SECRETS_KEYRING is refused. New installations",
		"should leave this empty and use SECRETS_KEYRING.",
	)
	b.WriteString(fmt.Sprintf("SECRETS_MASTER_KEY=%s\n\n", keep.keep("SECRETS_MASTER_KEY", "")))

	// ── SERVER ──────────────────────────────────────────────────────────────
	hdr("SERVER")
	b.WriteString(fmt.Sprintf("PORT=%d\n", cfg.Ports.API))
	note("Host ports compose publishes on. Change if something already owns one.")
	b.WriteString(fmt.Sprintf("API_HOST_PORT=%s\n", keep.keep("API_HOST_PORT", fmt.Sprintf("%d", cfg.Ports.API))))
	b.WriteString(fmt.Sprintf("POSTGRES_HOST_PORT=%s\n", keep.keep("POSTGRES_HOST_PORT", fmt.Sprintf("%d", cfg.Ports.Postgres))))
	b.WriteString(fmt.Sprintf("GIN_LOG_ENABLED=%s\n", keep.keep("GIN_LOG_ENABLED", "true")))
	b.WriteString(fmt.Sprintf("GIN_ACCESS_LOG_ENABLED=%s\n", keep.keep("GIN_ACCESS_LOG_ENABLED", "true")))
	note(
		"Ceiling on how long in-flight requests may finish after SIGTERM, not a",
		"delay: an idle process exits immediately. Keep it BELOW whatever will",
		"SIGKILL the process, or the platform decides the drain instead.",
	)
	b.WriteString(fmt.Sprintf("SHUTDOWN_TIMEOUT_SECONDS=%s\n\n", keep.keep("SHUTDOWN_TIMEOUT_SECONDS", "20")))

	// ── CONSOLE / BROWSER ───────────────────────────────────────────────────
	hdr("OPERATOR CONSOLE")
	note("Serves the operator SPA at /admin. Its login client is ADMIN_CONSOLE_CLIENT_ID, up in REQUIRED.")
	b.WriteString(fmt.Sprintf("ADMIN_CONSOLE_ENABLED=%s\n", keep.keep("ADMIN_CONSOLE_ENABLED", "true")))
	note(
		"Browser origins allowed to call this API. Empty disables CORS, which is",
		"correct when the console is served from this same origin — the default.",
		"Entries are scheme://host[:port], no trailing slash, no path: a browser's",
		"Origin header never carries one, and a mistyped entry is refused at boot",
		"rather than silently never matching.",
	)
	b.WriteString(fmt.Sprintf("CORS_ALLOWED_ORIGINS=%s\n", keep.keep("CORS_ALLOWED_ORIGINS", "")))
	note(
		"Client ids accepted in a token's azp/aud claim. Empty accepts any client",
		"in the realm.",
	)
	b.WriteString(fmt.Sprintf("KEYCLOAK_ALLOWED_CLIENT_IDS=%s\n\n", strings.Join(allowedClientIDs(cfg), ",")))

	// ── ADVANCED ────────────────────────────────────────────────────────────
	hdr("ADVANCED — tuning. Safe to ignore on a first install")
	note(
		"/v1 rate limits, both in-process and per replica. The credential limit is",
		"what a machine consumer actually gets and what RateLimit-Limit advertises.",
		"0 or unparseable means the default: a tuning knob, never an off switch.",
	)
	b.WriteString(fmt.Sprintf("RATE_LIMIT_EDGE_RPS=%s\n", keep.keep("RATE_LIMIT_EDGE_RPS", "10")))
	b.WriteString(fmt.Sprintf("RATE_LIMIT_CREDENTIAL_RPS=%s\n", keep.keep("RATE_LIMIT_CREDENTIAL_RPS", "20")))
	note(
		"How long the durable audit trail is kept. There is no value meaning",
		"\"forever\" — an audit table that only grows is a scheduled outage — and 0",
		"is refused rather than defaulted.",
	)
	b.WriteString(fmt.Sprintf("AUDIT_RETENTION_DAYS=%s\n", keep.keep("AUDIT_RETENTION_DAYS", "90")))
	note(
		"Prometheus at /metrics, off by default. With METRICS_TOKEN empty it is",
		"served to loopback only — which inside a container means from inside THAT",
		"container, so a scraper on the host needs a token: openssl rand -hex 32",
	)
	b.WriteString(fmt.Sprintf("METRICS_ENABLED=%s\n", keep.keep("METRICS_ENABLED", "false")))
	b.WriteString(fmt.Sprintf("METRICS_TOKEN=%s\n\n", keep.keep("METRICS_TOKEN", "")))

	// ── LEGACY ──────────────────────────────────────────────────────────────
	hdr("LEGACY /admin/* identity surface")
	note(
		"The pre-workspace identity API: one service-account client against the",
		"installation realm. Workspace connections replaced it — leave these empty",
		"and the whole /admin/* surface is simply not mounted.",
		"",
		"KEYCLOAK_CLIENT_SECRET is likewise only for a CONFIDENTIAL login client.",
		"A public PKCE client, which is what the console should use, needs none.",
	)
	b.WriteString(fmt.Sprintf("KEYCLOAK_CLIENT_SECRET=%s\n", secrets.ClientSecret))
	if cfg.Features["identity_management"] {
		b.WriteString(fmt.Sprintf("KEYCLOAK_ADMIN_CLIENT_ID=%s\n", IdentityAdminClientID))
		b.WriteString(fmt.Sprintf("KEYCLOAK_ADMIN_CLIENT_SECRET=%s\n", secrets.AdminClientSecret))
	} else {
		b.WriteString("KEYCLOAK_ADMIN_CLIENT_ID=\n")
		b.WriteString("KEYCLOAK_ADMIN_CLIENT_SECRET=\n")
	}
	note("Bounds how long a role revoked directly in Keycloak is still honoured here.")
	b.WriteString(fmt.Sprintf("ADMIN_LIVE_CHECK_TTL_SECONDS=%s\n\n", keep.keep("ADMIN_LIVE_CHECK_TTL_SECONDS", "30")))

	// ── DEVELOPMENT / EVALUATION ────────────────────────────────────────────
	hdr("DEVELOPMENT AND EVALUATION ONLY\n# Everything below serves the bundled throwaway Keycloak\n# (`docker compose --profile dev-idp up -d`). A self-hosted\n# installation pointed at a real Keycloak uses none of it.")
	note("!!! DEV-ONLY — /dev/auth is an unauthenticated login playground. Never true in production.")
	if cfg.Features["dev_playground"] {
		b.WriteString("DEV_PLAYGROUND_ENABLED=true\n")
	} else {
		b.WriteString("DEV_PLAYGROUND_ENABLED=false\n")
	}
	b.WriteString(fmt.Sprintf("DEV_PLAYGROUND_CLIENT_ID=%s\n\n", DevPlaygroundClientID))
	note("Bootstrap admin for the bundled Keycloak's own master realm.")
	b.WriteString(fmt.Sprintf("KEYCLOAK_ADMIN=%s\n", cfg.Auth.Admin.Username))
	b.WriteString(fmt.Sprintf("KEYCLOAK_ADMIN_PASSWORD=%s\n", secrets.AdminPassword))
	note("Host port the bundled Keycloak is published on. It is part of KEYCLOAK_URL above — change both or neither.")
	b.WriteString(fmt.Sprintf("KC_HOST_PORT=%d\n", cfg.Ports.Keycloak))
	note("The bundled Keycloak's own database.")
	b.WriteString(fmt.Sprintf("KC_DB_USER=%s\n", keep.keep("KC_DB_USER", "keycloak")))
	b.WriteString(fmt.Sprintf("KC_DB_PASSWORD=%s\n", keep.keep("KC_DB_PASSWORD", "keycloak")))
	b.WriteString(fmt.Sprintf("KC_DB_NAME=%s\n", keep.keep("KC_DB_NAME", "keycloak")))
	note("Password given to the seed users defined in config/project.json.")
	b.WriteString(fmt.Sprintf("SEED_USER_PASSWORD=%s\n", secrets.SeedUserPassword))

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// writeRealmExport regenerates the Keycloak realm import file by composing a
// minimal JSON shape from the project config and serializing deterministically.
// Secret values (client secret, seed user passwords) come exclusively from
// the Secrets parameter — never from cfg.
func writeRealmExport(path string, cfg *ProjectConfig, secrets Secrets) error {
	realmRoles := make([]map[string]any, 0, len(cfg.Auth.Roles))
	for _, r := range cfg.Auth.Roles {
		realmRoles = append(realmRoles, map[string]any{
			"name":        r,
			"description": fmt.Sprintf("realm role: %s", r),
		})
	}

	// NOTE: defaultClientScopes is intentionally omitted so Keycloak applies
	// its realm-level default scope set (basic + email + profile + roles +
	// web-origins + acr). The "basic" scope is what attaches the sub mapper
	// — overriding the list here would drop sub from issued tokens, which
	// breaks the provider's identityFromClaims invariant.
	client := map[string]any{
		"clientId":                  cfg.Auth.Client.ID,
		"name":                      cfg.Project.Name + " API",
		"enabled":                   true,
		"protocol":                  "openid-connect",
		"publicClient":              false,
		"secret":                    secrets.ClientSecret,
		"standardFlowEnabled":       true,
		"directAccessGrantsEnabled": true,
		"serviceAccountsEnabled":    false,
		"redirectUris":              []string{fmt.Sprintf("http://localhost:%d/*", cfg.Ports.API)},
		"webOrigins":                []string{fmt.Sprintf("http://localhost:%d", cfg.Ports.API)},
	}

	clients := []map[string]any{client}

	// When features.identity_management is on, register a confidential
	// service-account client. The API uses this client's credentials to
	// call Keycloak's Admin REST API (users, roles, sessions, password
	// actions). Separate from the token-validation client (saas-backend)
	// so a leak of one secret doesn't compromise the other capability.
	//
	// No user-facing flows (standardFlow + directAccessGrants both off)
	// — this client exists solely for the client_credentials grant.
	if cfg.Features["identity_management"] {
		clients = append(clients, map[string]any{
			"clientId":                  IdentityAdminClientID,
			"name":                      cfg.Project.Name + " — Identity Admin",
			"description":               "Service-account client used by the API to call Keycloak's Admin REST API. No user-facing flows.",
			"enabled":                   true,
			"protocol":                  "openid-connect",
			"publicClient":              false,
			"secret":                    secrets.AdminClientSecret,
			"standardFlowEnabled":       false,
			"directAccessGrantsEnabled": false,
			"serviceAccountsEnabled":    true,
		})
	}

	// When features.dev_playground is on, register a SECOND public client
	// for the in-browser auth playground at /dev/auth. Public-PKCE-only:
	// no client secret, S256 challenge enforced. Mirrors how a real
	// frontend (keycloak-js) would integrate.
	if cfg.Features["dev_playground"] {
		clients = append(clients, map[string]any{
			"clientId":                  DevPlaygroundClientID,
			"name":                      cfg.Project.Name + " — Dev Auth Playground + Admin Console",
			"description":               "DEV-ONLY public PKCE client used by both the legacy /dev/auth playground and the new /admin console. Disable in non-local environments.",
			"enabled":                   true,
			"protocol":                  "openid-connect",
			"publicClient":              true,
			"standardFlowEnabled":       true,
			"directAccessGrantsEnabled": false,
			"serviceAccountsEnabled":    false,
			// Two redirect URIs because the same PKCE client backs two
			// frontends now: the original playground at /dev/auth and the
			// IAM admin console at /admin. Both are local-dev-only.
			"redirectUris": []string{
				fmt.Sprintf("http://localhost:%d/dev/auth", cfg.Ports.API),
				fmt.Sprintf("http://localhost:%d/admin", cfg.Ports.API),
			},
			"webOrigins": []string{fmt.Sprintf("http://localhost:%d", cfg.Ports.API)},
			"attributes": map[string]any{
				"pkce.code.challenge.method": "S256",
				// Keycloak accepts a "+"-separated list here.
				"post.logout.redirect.uris": fmt.Sprintf("http://localhost:%d/dev/auth+http://localhost:%d/admin", cfg.Ports.API, cfg.Ports.API),
			},
		})
	}

	var users []map[string]any
	if cfg.Features["seed_users"] {
		for _, u := range cfg.SeedUsers {
			// firstName/lastName are required by the default user-profile
			// schema for any user with the "user" role. Derive from the
			// username when the SeedUser doesn't supply them explicitly,
			// so seeded accounts can complete password-grant logins.
			first, last := splitDisplayName(u.Username)
			users = append(users, map[string]any{
				"username":      u.Username,
				"enabled":       true,
				"emailVerified": true,
				"email":         u.Email,
				"firstName":     first,
				"lastName":      last,
				"credentials": []map[string]any{
					{"type": "password", "value": secrets.SeedUserPassword, "temporary": false},
				},
				"realmRoles": u.Roles,
			})
		}
	}

	// Service-account user for the identity-admin client. Keycloak auto-
	// creates this user when serviceAccountsEnabled=true, but in a realm
	// IMPORT we have to declare it explicitly to bind the realm-management
	// client roles up front. Without this block the service account would
	// be created at first boot with NO admin roles and every admin call
	// would 403.
	//
	// realm-admin is the umbrella role under realm-management; it covers
	// view-users / manage-users / view-realm / manage-realm / etc. We use
	// the umbrella for simplicity; production deployments may want to
	// scope down to the specific roles their identity flows actually need.
	if cfg.Features["identity_management"] {
		users = append(users, map[string]any{
			"username":               "service-account-" + IdentityAdminClientID,
			"enabled":                true,
			"emailVerified":          true,
			"serviceAccountClientId": IdentityAdminClientID,
			"clientRoles": map[string]any{
				"realm-management": []string{"realm-admin"},
			},
		})
	}

	// NOTE: Keycloak's realm parser rejects unknown top-level fields, so the
	// DEV-ONLY warning lives in docs/bootstrap.md and config/project.json
	// (in _meta), not in this generated artifact.
	realm := map[string]any{
		"realm":                  cfg.Auth.Realm,
		"enabled":                true,
		"displayName":            cfg.Project.Name,
		"sslRequired":            "none",
		"registrationAllowed":    false,
		"loginWithEmailAllowed":  true,
		"duplicateEmailsAllowed": false,
		"resetPasswordAllowed":   true,
		"editUsernameAllowed":    false,
		"bruteForceProtected":    true,
		"accessTokenLifespan":    3600,
		"ssoSessionIdleTimeout":  1800,
		"ssoSessionMaxLifespan":  36000,
		"roles":                  map[string]any{"realm": realmRoles},
		"clients":                clients,
		"users":                  users,
	}

	b, err := json.MarshalIndent(realm, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal realm: %w", err)
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

// allowedClientIDs computes the deduplicated list of token-issuing client
// ids that the API should accept (used to populate KEYCLOAK_ALLOWED_CLIENT_IDS
// in .env).
//
// Precedence:
//  1. cfg.Auth.AllowedClientIDs — explicit override from project.json.
//  2. Auto-derive from registered clients:
//     - auth.client.id (always)
//     - DevPlaygroundClientID (when features.dev_playground=true)
//
// Order is preserved with the primary client first so logs/configs are
// stable across regens.
func allowedClientIDs(cfg *ProjectConfig) []string {
	if len(cfg.Auth.AllowedClientIDs) > 0 {
		seen := map[string]struct{}{}
		out := make([]string, 0, len(cfg.Auth.AllowedClientIDs))
		for _, id := range cfg.Auth.AllowedClientIDs {
			if id == "" {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
		return out
	}
	list := []string{cfg.Auth.Client.ID}
	if cfg.Features["dev_playground"] {
		list = append(list, DevPlaygroundClientID)
	}
	return list
}

// splitDisplayName produces (firstName, lastName) pairs from a username
// when the SeedUser doesn't supply them. Used to satisfy Keycloak's
// default user-profile schema, which requires firstName/lastName for any
// user holding the "user" role.
//
// Rules:
//   - Splits on common separators (".", "-", "_", " ")
//   - Returns (Capitalize(part0), Capitalize(part1...)) when separable
//   - Otherwise returns (Capitalize(username), "User")
func splitDisplayName(username string) (string, string) {
	for _, sep := range []string{".", "-", "_", " "} {
		if i := strings.Index(username, sep); i > 0 && i < len(username)-1 {
			return capFirst(username[:i]), capFirst(username[i+len(sep):])
		}
	}
	return capFirst(username), "User"
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] = r[0] - 32
	}
	return string(r)
}

// sanitize collapses a project name into a database-friendly identifier:
// lowercase, dashes/spaces -> underscores, strip non-[a-z0-9_].
func sanitize(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		case r == '-' || r == ' ' || r == '.':
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		out = "app"
	}
	return out
}
