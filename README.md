# Lightweight SaaS Backend

> **Status:** Sprint 3 complete — Keycloak-based auth, end-to-end validated.

A reusable Go backend foundation for SaaS-style products. Authentication is
delegated to [Keycloak](https://www.keycloak.org/) (or any future OIDC
provider — see [§Architecture](#architecture)); business code never handles
passwords or JWT signing. The whole stack — API, app DB, Keycloak, Keycloak's
DB — is one `make up` away.

If you've used this repo before Sprint 3, **start here:**
[docs/migrations/PHASE3_BREAKING_CHANGE.md](docs/migrations/PHASE3_BREAKING_CHANGE.md).

---

## Quickstart

```bash
git clone <repo> && cd lightweight-saas-backend
make doctor          # verify toolchain (go, docker, docker-compose, curl, jq)
make init            # interactive bootstrap (writes config/project.json + .env)
make up              # build api, pull keycloak, start the 4-container stack
make auth-test       # acquire a Keycloak token + call /me  → expect 200
```

Full onboarding walkthrough: **[docs/KEYCLOAK_SETUP.md](docs/KEYCLOAK_SETUP.md)**.

---

## What's in the box

- **Provider-agnostic auth.** `auth.AuthProvider` interface; today's
  implementation is Keycloak (JWKS-validated RS256), tomorrow's could be
  Auth0/Supabase/Clerk — zero changes to business code.
- **Idempotent user provisioning.** First protected request for a Keycloak
  `sub` creates a local row; later requests reuse the same `users.id`. Race-
  safe via DB unique index on `keycloak_sub`.
- **Single-source-of-truth bootstrap.** Edit
  [`config/project.json`](config/project.json), run `make regen`, and `.env`,
  the Keycloak realm export, and `config/project.schema.json` rebuild
  themselves.
- **Day-one DX.** Categorized `make help`, `make doctor` toolchain probe,
  `make reset-dev` one-command rescue.
- **Structured auth events.** Hookable via `auth.SetEventHook` — plug
  Prometheus or OpenTelemetry without touching middleware.
- **Tested.** 41 unit tests including a 50-goroutine race on user
  provisioning; CI gate includes `swagger-check` for doc drift.

---

## API surface

The canonical reference is the generated OpenAPI:

```
http://localhost:8080/swagger/index.html
```

Today the surface is intentionally tiny:

| Method | Path       | Auth          | Purpose                                                  |
|--------|------------|---------------|----------------------------------------------------------|
| `GET`  | `/health`  | none          | Liveness probe (200 always).                             |
| `GET`  | `/me`      | Bearer        | Returns the local user row; JIT-creates it on first call.|
| `GET`  | `/swagger/*` | none        | Swagger UI for the generated OpenAPI spec.              |

**There is no `/register` or `/login` here by design** — Keycloak owns
identity. Clients obtain tokens directly from Keycloak (Authorization Code
+ PKCE for browsers, Direct Access Grants for CLI/tests), and call protected
endpoints with `Authorization: Bearer <token>`.

### Example: token → `/me`

```bash
TOKEN=$(curl -fsS -X POST http://localhost:8081/realms/saas/protocol/openid-connect/token \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d 'client_id=saas-backend' -d 'client_secret=saas-backend-secret' \
  -d 'grant_type=password' -d 'username=testuser' -d 'password=password' \
  | jq -r .access_token)

curl -fsS http://localhost:8080/me -H "Authorization: Bearer $TOKEN" | jq
```

Response:
```json
{
  "id": 1,
  "keycloak_sub": "fbe56e3a-3bd2-4ed3-8ff1-37c655f3fbdc",
  "email":        "testuser@test.com",
  "username":     "testuser",
  "created_at":   "2026-05-17T08:14:48Z",
  "updated_at":   "2026-05-17T08:14:48Z"
}
```

Call it a second time with the same token → same `id`, no new row.
See [KEYCLOAK_SETUP.md §7-§8](docs/KEYCLOAK_SETUP.md) for the full flow.

---

## Architecture

```
HTTP request
    ↓
auth.RequireAuth(provider)        ← AuthProvider interface — Keycloak today
    ↓                               (validates JWT signature + iss + azp + exp)
auth.Identity in gin.Context      ← { Subject, Email, Username, Roles, Raw }
    ↓
user.Handler (HTTP layer)
    ↓
user.Service.EnsureUser(identity) ← idempotent JIT provisioning
    ↓
user.Repository (GORM)
    ↓
PostgreSQL (users table — keycloak_sub uniquely indexed)
```

The boundary between **auth identity** (Keycloak's `sub`) and **business
identity** (`users.id`) is enforced by package layout:

- `internal/auth/` and `internal/auth/keycloak/` know about JWTs.
- `internal/user/` does not — verified by `grep -r keycloak internal/user/`.
- `internal/server/` mounts `auth.RequireAuth` once at the route group.

Swap Keycloak for another OIDC provider by writing a new
`auth.AuthProvider` implementation and re-wiring `cmd/api/main.go`. No
business code changes.

Full diagrams and rationale: [docs/KEYCLOAK_SETUP.md §1](docs/KEYCLOAK_SETUP.md#1-overview).

---

## Project layout

```
.
├── cmd/
│   ├── api/main.go                 # API entrypoint (banner → config → db → provider → server)
│   └── bootstrap/main.go           # `make init` CLI
├── internal/
│   ├── auth/
│   │   ├── provider.go             # AuthProvider interface + sentinel errors
│   │   ├── identity.go             # Identity struct + gin context helpers
│   │   ├── middleware.go           # RequireAuth(provider) — provider-agnostic
│   │   ├── events.go               # AuthEvent + SetEventHook for observability
│   │   └── keycloak/
│   │       ├── config.go           # KeycloakConfig
│   │       ├── jwks.go             # JWKS cache wrapping MicahParks/keyfunc/v3
│   │       └── provider.go         # implements auth.AuthProvider
│   ├── user/
│   │   ├── model.go                # User (id, keycloak_sub, email, username, timestamps)
│   │   ├── repository.go           # FindBySub / FindByID / Create / Update
│   │   ├── service.go              # EnsureUser (idempotent JIT provisioning)
│   │   ├── handler.go              # /me handler
│   │   └── dto.go                  # UserResponse
│   ├── server/                     # gin engine + route composition + swagger mount
│   ├── config/                     # env loader + fail-fast Validate()
│   ├── database/                   # gorm connect + AutoMigrate
│   ├── logger/                     # structured (text) logger
│   └── bootstrap/                  # config-as-source-of-truth + generators
├── config/
│   ├── project.json                # source-of-truth (committed, no secrets)
│   └── project.schema.json         # JSON Schema (mirror of embedded canonical)
├── deploy/
│   └── keycloak/realm-export.json  # imported by Keycloak on first boot
├── docs/
│   ├── KEYCLOAK_SETUP.md           # onboarding + troubleshooting
│   ├── bootstrap.md                # bootstrap design + lifecycle commands
│   ├── VALIDATION_PHASE3.md        # Sprint 3 sign-off
│   ├── migrations/                 # breaking change records
│   ├── docs.go / swagger.json / swagger.yaml  # generated by `make docs`
├── scripts/
│   ├── auth-test.sh                # token → /me smoke test
│   └── e2e.sh                      # readiness waits + auth-test
├── Dockerfile                      # multi-stage Go build
├── docker-compose.yml              # api + postgres + keycloak + keycloak-postgres
├── Makefile                        # categorized; `make help` for the menu
└── .env / .env.example             # generated by `make init` / `make regen`
```

---

## Stack lifecycle

`make help` prints the full menu. The everyday targets:

| Command          | Effect                                                  | Data |
|------------------|---------------------------------------------------------|------|
| `make up`        | Build + start full stack (postgres, keycloak, api).     | preserves volumes |
| `make stop`      | Pause containers; resume with `make start`.             | preserves all |
| `make down`      | Stop + remove containers; volumes survive.              | preserves data |
| `make purge`     | Wipe containers, volumes, network, api image, `bin/`. Prompts y/N. | DATA LOSS |
| `make reset-dev` | One-shot: `purge` + rebuild + start. Prompts y/N.       | DATA LOSS |
| `make logs`      | Tail logs from all services.                            | — |
| `make doctor`    | Toolchain + docker daemon + container + port + reachability probe. | — |

When something breaks, run `make doctor` first, then `make reset-dev` if
nothing else helps. See
[docs/KEYCLOAK_SETUP.md §9](docs/KEYCLOAK_SETUP.md#9-troubleshooting) for
symptom-by-symptom fixes.

---

## Bootstrap & configuration

[`config/project.json`](config/project.json) is the **single source of
truth** for non-secret project description (project name, realm name,
client id, roles, ports, feature flags, seed user list). Secrets
(`KEYCLOAK_CLIENT_SECRET`, `KEYCLOAK_ADMIN_PASSWORD`, `SEED_USER_PASSWORD`)
live in `.env` and are never committed.

```
make init             # interactive prompts — write project.json + regen all derived files
make regen            # non-interactive — re-run generators against current project.json
```

Generated files (overwritten in place — don't hand-edit):
`.env`, `.env.example`, `config/project.schema.json`,
`deploy/keycloak/realm-export.json`.

Full design + customization recipes: [docs/bootstrap.md](docs/bootstrap.md).

---

## Testing & quality

```
make test              # all unit tests (41 total: 11 keycloak + 21 bootstrap + 9 user)
make test-race         # tests with -race
make test-cover        # coverage report (writes coverage.out)
make vet               # go vet
make fmt-check         # fail if gofmt would touch any file
make lint              # golangci-lint if installed, else fmt-check
make ci                # fmt-check + vet + build + test + swagger-check
```

The `swagger-check` step in CI fails if `docs/swagger.{json,yaml}` /
`docs/docs.go` drift from the handler annotations — i.e. someone changed a
handler signature without running `make docs`. Long-form rationale below.

---

## Documentation workflow

The Swagger spec is generated from `// @Router`, `// @Summary`, etc.
annotations on the handlers. Generation is **manual + CI-gated**:

| Command             | Purpose |
|---------------------|---------|
| `make docs`         | Regenerate `docs/{docs.go,swagger.json,swagger.yaml}` from annotations. |
| `make docs-clean`   | Delete the three generated files (next `make docs` recreates them). |
| `make swagger-check`| CI gate — fails if committed docs are out of sync with annotations.  |
| `make swagger`      | Original target name; `make docs` is an alias.                       |

**Why not auto-generate on every `make up`?**

- Adds a swag dependency + a few seconds to every dev cycle, for an
  artifact that's only useful when handlers change.
- Couples runtime startup to a code-generation step.
- `make ci` catches drift before merge; that's enough.

If you change a handler annotation, run `make docs && git add docs/` before
committing. `make ci` will tell you if you forget.

---

## Tech stack

| Component        | Choice |
|------------------|--------|
| Language         | Go 1.25 |
| HTTP framework   | Gin |
| Database         | PostgreSQL 15 |
| ORM              | GORM |
| Identity         | Keycloak 26 (JWKS, RS256) |
| JWKS cache       | github.com/MicahParks/keyfunc/v3 (with kid-miss refresh) |
| JWT parser       | github.com/golang-jwt/jwt/v5 |
| Config schema    | github.com/santhosh-tekuri/jsonschema/v6 |
| API documentation| Swagger / OpenAPI (swaggo) |
| Test runner      | Go standard library + race detector |

---

## Environment variables

The complete list is documented in
[docs/KEYCLOAK_SETUP.md §2](docs/KEYCLOAK_SETUP.md#2-environment-variables),
including which are required, defaults, and risk-if-wrong.

`.env.example` is the always-current template; `make init` produces a
working `.env` from it.

---

## Production hardening

Several Sprint-3 defaults are dev-only and must be replaced before any
non-local deployment (Direct Access Grants disabled, seeded users
dropped, real secrets manager, TLS, Keycloak `start --optimized` mode,
etc.). Full checklist:
[docs/KEYCLOAK_SETUP.md §10](docs/KEYCLOAK_SETUP.md#10-production-considerations).

---

## License

MIT
