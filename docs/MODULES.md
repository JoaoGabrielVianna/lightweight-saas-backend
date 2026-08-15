# Modules

**Last updated:** 2026-07-26 · Companion to [PROJECT_STATUS.md](PROJECT_STATUS.md) and [ARCHITECTURE.md](ARCHITECTURE.md)

One section per module. Maturity ratings are grounded in test coverage,
completeness against the module's own stated contract, and whether the module
has been exercised against a live stack.

**Maturity scale**
- **High** — complete for its scope, well covered, no known defects
- **Medium** — works, but has coverage gaps or documented rough edges
- **Low** — minimal implementation; effectively a placeholder

---

## Overview

| Module | LOC | Coverage | Status | Maturity |
|---|---:|---:|:--:|---|
| [`internal/auth`](#internalauth) | 513 | 71.0% | ✅ | High |
| [`internal/auth/keycloak`](#internalauthkeycloak) | 300 | 86.4% | ✅ | High |
| [`internal/identity`](#internalidentity) | 1,923 | 67.5% | ✅ | High |
| [`internal/identity/keycloak`](#internalidentitykeycloak) | 1,478 | 74.4% | ✅ | High |
| [`internal/server`](#internalserver) | 1,499 | 62.7% | ✅ | High |
| [`internal/audit`](#internalaudit) | 296 | 86.5% | ✅ | High |
| [`internal/logging`](#internallogging) | 174 | 25.0% | ✅ | Medium |
| [`internal/config`](#internalconfig) | 409 | 94.4% | ✅ | High |
| [`internal/bootstrap`](#internalbootstrap) | 749 | 87.2% | ✅ | High |
| [`internal/database`](#internaldatabase) | 39 | 42.9% | 🟡 | Low |
| [`internal/user`](#internaluser) | 280 | 40.0% | 🟡 | Low |
| [`internal/workspace`](#internalworkspace) | 1,130 | 66.8% | ✅ | Medium |
| [`internal/publicid`](#internalpublicid) | 147 | 97.4% | ✅ | Medium |
| [`internal/requestid`](#internalrequestid) | 101 | 95.5% | ✅ | Medium |
| [`internal/connection`](#internalconnection) | 2,116 | 68.8% | ✅ | Medium |
| [`internal/secrets`](#internalsecrets) | 228 | 91.5% | ✅ | Medium-High |
| [`internal/logger`, `banner`, `fonts`](#support-packages) | ~200 | 0% | ✅ | Low (cosmetic) |
| [`web/admin`](#webadmin) | 5,809 JS | 30 tests | ✅ | Medium-High |
| [`web/dev`](#webdev) | — | — | ✅ | Medium |

LOC excludes test files. Coverage is per-package statement coverage.

---

## `internal/auth`

**Objective.** Provide authentication and authorization middleware that knows
nothing about which identity provider is in use.

**Responsibilities**
- Define the [`AuthProvider`](../internal/auth/provider.go) port and the
  canonical `Identity` type
- `RequireAuth` — extract and validate the bearer token, store the identity
- `RequireRole` / `RequireAnyRole` — realm-role gating from JWT claims
- `RequireLiveAdmin` — live server-side admin verification, fail-closed
- `CachedAdminChecker` — TTL cache with explicit invalidation
- Emit `AuthEvent` for every outcome so observability can subscribe

**Dependencies.** Gin only. **Must never import `internal/identity`** — the
`AdminChecker` interface is the seam, and the concrete adapter is built in the
composition root.

**Key files**

| File | Contents |
|---|---|
| [provider.go](../internal/auth/provider.go) | `AuthProvider` port, `Identity`, error sentinels |
| [middleware.go](../internal/auth/middleware.go) | `RequireAuth`, `RequireRole`, `RequireAnyRole` |
| [admin_check.go](../internal/auth/admin_check.go) | `AdminChecker`, `CachedAdminChecker`, `RequireLiveAdmin` |
| [identity.go](../internal/auth/identity.go) | Gin-context storage/retrieval of `Identity` |
| [events.go](../internal/auth/events.go) | `AuthEvent`, event kinds, `SetEventHook` |

**Endpoints.** None — middleware only.

**Maturity: High.** The trickiest logic in the codebase (negative caching,
fail-closed error handling, invalidation) is well covered and thoroughly
documented inline.

---

## `internal/auth/keycloak`

**Objective.** Implement `auth.AuthProvider` against a Keycloak realm's JWKS.

**Responsibilities**
- Blocking JWKS fetch at construction so misconfiguration fails at boot
- Verify signature, restrict algorithms to asymmetric families
- Enforce `iss`, `exp`, and `azp` allow-list
- Project Keycloak claims onto `auth.Identity`, flattening realm and
  client roles

**Dependencies.** `internal/auth`, `MicahParks/keyfunc`, `golang-jwt/jwt`.

**Key files**

| File | Contents |
|---|---|
| [provider.go](../internal/auth/keycloak/provider.go) | `ValidateToken`, claim projection, `allowedAlgs` |
| [jwks.go](../internal/auth/keycloak/jwks.go) | JWKS client construction and options |
| [config.go](../internal/auth/keycloak/config.go) | Validation, issuer derivation, allowed-client set |

**Endpoints.** None.

**Maturity: High.** Contains the codebase's most consequential security
decision — excluding `HS*` algorithms — with the rationale written into the
source.

---

## `internal/identity`

**Objective.** Expose Keycloak's administrative surface as a validated,
guarded, audited HTTP API.

**Responsibilities**
- Define the [`IdentityProvider`](../internal/identity/provider.go) port (22
  methods) and provider-agnostic types (`User`, `Role`, `Session`, `Invitation`)
- **Service tier:** input validation (UUID and role-name patterns), pagination
  clamping, name normalization, reserved-name enforcement, and all
  self-protection guards
- **Handler tier:** binding, error → HTTP status mapping, audit emission,
  admin-cache invalidation

**Dependencies.** `internal/auth` (read the caller), `internal/audit` +
`internal/logging` (emit events), Gin.

**Key files**

| File | LOC | Contents |
|---|---:|---|
| [provider.go](../internal/identity/provider.go) | 320 | Port + domain types + documented reliability contracts |
| [service.go](../internal/identity/service.go) | 508 | Business rules and guards |
| [handler.go](../internal/identity/handler.go) | 891 | 23 HTTP handlers + Swagger annotations |
| [dto.go](../internal/identity/dto.go) | 178 | Request/response bodies |
| [errors.go](../internal/identity/errors.go) | — | Error sentinels (`ErrBadRequest`, `ErrForbidden`, `ErrConflict`, …) |

**Endpoints.** All 32 routes under `/admin/*` except `/admin/audit-events`,
`/admin/settings/*` and `/admin/users/password`. See
[FEATURES.md](FEATURES.md) for the full route table.

**Self-protection guards** (all in `service.go`)

| Guard | Behaviour |
|---|---|
| Self-disable | `PATCH {enabled:false}` on self → 403 |
| Self-delete | `DELETE` own user → 403 |
| Self-strip-admin | Remove own `admin` role → 403 |
| Last admin | Any operation emptying the enabled-admin set → 403; **fails closed** if it cannot enumerate admins |
| Reserved roles | `admin`, `user`, `offline_access`, `uma_authorization`, `default-roles-*` cannot be created or modified |

**Maturity: High.** Feature-complete for v0.3 scope. Coverage at 67.5% is the
lowest of the core modules — the gap is mostly in error-path branches of the
larger handlers.

---

## `internal/identity/keycloak`

**Objective.** Implement `IdentityProvider` against the Keycloak Admin REST
API, and act as the anti-corruption layer.

**Responsibilities**
- Service-account token acquisition with caching, a 10 s refresh skew, and a
  single transparent retry on 401 (covers key rotation)
- All Admin REST calls; translate `kcUser`/`kcUserSession`/… into
  provider-agnostic types
- Derive invitation status from user state (Keycloak has no invitation resource)
- Compensating delete on partial invitation creation
- Realm-level SMTP config and email-template localization

**Dependencies.** `internal/identity` (the port and its types), stdlib HTTP.

**Key files**

| File | LOC | Contents |
|---|---:|---|
| [admin.go](../internal/identity/keycloak/admin.go) | 332 | `AdminClient`: token cache, `doJSON`/`doCreate`/`doText`, 401 retry |
| [users.go](../internal/identity/keycloak/users.go) | — | User CRUD, GET→merge→PUT semantics, password set |
| [roles.go](../internal/identity/keycloak/roles.go) | — | Realm role CRUD, mapping assign/unassign |
| [sessions.go](../internal/identity/keycloak/sessions.go) | — | Session listing and revocation |
| [invitations.go](../internal/identity/keycloak/invitations.go) | — | Derived invitations, status computation, resend guards |
| [realm.go](../internal/identity/keycloak/realm.go) | 145 | SMTP block, email templates |
| [provider.go](../internal/identity/keycloak/provider.go) | — | Port implementation, `ListUsers` |

**Documented reliability contracts** (enforced here, specified in the port's
doc comments):

1. **Invitation creation is compensating.** If role assignment or email
   dispatch fails after the user was created, the user is best-effort deleted
   so the caller can retry with the same email. The final `GET` is
   informational — its failure must not roll back an invitation whose email is
   already in flight.
2. **Resend only re-dispatches still-pending actions.** Re-adding a completed
   action would force the user to redo work. Accepted or revoked invitations
   return `ErrConflict`.

**Known weaknesses**
- `ListSessions` is N+1: one request per enabled realm client — see
  [TD-007](TECH_DEBT.md#td-007)
- Retries only on 401, not on 5xx or transport errors — see
  [KI-010](KNOWN_ISSUES.md#ki-010)
- `ListInvitations` and `ListUsersByRole` page internally up to a hard cap;
  realms above it silently truncate

**Maturity: High** for the implemented surface, with the performance caveats
above.

---

## `internal/server`

**Objective.** Own the HTTP shell — the Gin engine, routing, middleware
composition, and static-asset surfaces.

**Responsibilities**
- Build the Gin engine and apply CORS
- Compose domain wiring: `SetupUser`, `SetupIdentity`
- Mount all route groups with the correct middleware order
- Per-IP rate limiting
- Mount the admin console, dev playground, and landing page under their flags
- Host handlers that need `*config.Config` or the raw Keycloak provider:
  SMTP, email templates, audit events, token introspection

**Dependencies.** Everything. This is the composition tier.

**Key files**

| File | Contents |
|---|---|
| [server.go](../internal/server/server.go) | `Server`, `SetupRoutes`, `SetupUser`, `SetupIdentity`, `adminCheckerFromProvider`, `/health`, `/auth/debug` |
| [router.go](../internal/server/router.go) | `SetupRouter` — all `/admin/*` and `/me` routes |
| [ratelimit.go](../internal/server/ratelimit.go) | Per-IP token bucket + stale-bucket sweeper |
| [admin.go](../internal/server/admin.go) | Admin console mounting, `config.json`, static assets, embedded docs |
| [playground.go](../internal/server/playground.go) | Dev playground, `authDebugHandler` (backs both introspection routes) |
| [smtp_handler.go](../internal/server/smtp_handler.go) | SMTP settings, connection test, user provisioning with temp password |
| [email_templates_handler.go](../internal/server/email_templates_handler.go) | Email template get/update/reset |
| [audit_handler.go](../internal/server/audit_handler.go) | `GET /admin/audit-events` |
| [landing.go](../internal/server/landing.go) | `GET /` |

**Endpoints.** All 46 routes are registered here. Breakdown:

| Group | Count | Gate |
|---|---:|---|
| `/admin/*` API | 32 | rate limit → auth → role → live-admin |
| Authenticated | 2 | `/me`, `/auth/debug` |
| Public operational | 3 | `/health`, `/swagger/*any`, `/` |
| Admin console shell | 4 | none (static) — `ADMIN_CONSOLE_ENABLED` or `DEV_PLAYGROUND_ENABLED` |
| Dev playground | 5 | none — `DEV_PLAYGROUND_ENABLED` only |

**Design notes**
- `SetupIdentity` returns four nils when the admin client is unconfigured, and
  the router uses that to omit `/admin/*` entirely — 404, not 403 (AD-004).
- The rate limiter is mounted **per route group, not globally**, on the
  assumption production fronts the API with an LB-level limiter for other
  surfaces.
- `/auth/debug` is mounted here rather than in `SetupRouter` because it needs
  `*config.Config`. Registering it in both places is what caused
  [KI-001](KNOWN_ISSUES.md#ki-001).

**Known weaknesses**
- `SetupRouter` takes 8 positional parameters — [TD-006](TECH_DEBT.md#td-006)
- `clientIP` trusts `X-Forwarded-For` unconditionally — [KI-004](KNOWN_ISSUES.md#ki-004)

**Maturity: High.**

---

## `internal/audit`

**Objective.** Define the canonical audit-event model and its dispatch
mechanism, with zero knowledge of transport or provider.

**Responsibilities**
- `Event`, `Actor`, `Target`, `Action` — the stable wire model
- 14 canonical action constants
- `Recorder` interface plus a no-op default, so `Record` is always safe
- `MemoryRecorder` — bounded ring buffer
- `MultiRecorder` — fan-out

**Dependencies.** **None** beyond stdlib. This is intentional and is an
invariant: adding a Gin or Keycloak import here breaks the model's portability.

**Key files**

| File | Contents |
|---|---|
| [event.go](../internal/audit/event.go) | Model + the 14 action constants |
| [recorder.go](../internal/audit/recorder.go) | `Recorder`, `SetDefault`/`Default`/`Record` via `atomic.Pointer` |
| [memory.go](../internal/audit/memory.go) | Ring buffer |
| [multi.go](../internal/audit/multi.go) | Fan-out recorder |

**The 14 actions**

| Domain | Actions |
|---|---|
| User | `user.created`, `user.updated`, `user.deleted`, `user.roles_granted`, `user.role_revoked`, `user.password_reset` |
| Role | `role.created`, `role.updated`, `role.deleted` |
| Session | `session.revoked`, `user.sessions_logged_out` |
| Invitation | `invitation.created`, `invitation.resent`, `invitation.revoked` |

> Action strings are a public contract. Adding one is backwards-compatible;
> renaming or removing one breaks every log and metric consumer.

**Endpoints.** None directly; surfaced via `GET /admin/audit-events`.

**Maturity: High.** Cleanest module in the codebase.

---

## `internal/logging`

**Objective.** Bridge the transport-agnostic audit model to Gin, and provide
the durable log sink.

**Responsibilities**
- `AuditSink` — writes events as structured log lines
- `WireDefaultWithMemory(n)` — install the fan-out (log + ring buffer)
- `ActorFromGin` / `IPFromGin` / `EventFromGin` — context extraction
- `RecordMutation` — the single emitter every mutation handler calls

**Dependencies.** `internal/audit`, `internal/auth`, Gin.

**Key files**

| File | Contents |
|---|---|
| [audit_sink.go](../internal/logging/audit_sink.go) | Log sink + wiring helpers |
| [gin_helpers.go](../internal/logging/gin_helpers.go) | Context extraction + `RecordMutation` |

**Why `RecordMutation` matters.** It centralises the success/failure branch so
all 15 call sites cannot drift apart. The invariant it enforces: *every
mutation emits who/action/target/timestamp/ip, and failures additionally emit
reason.*

**Maturity: Medium.** The logic is small and correct, but 25.0% coverage is the
lowest in the codebase — the sink's formatting paths are largely untested.

---

## `internal/config`

**Objective.** Load and validate configuration from the environment.

**Responsibilities**
- Load `.env` (optional) then environment variables, env taking precedence
- Parse booleans, integers, and comma-separated lists tolerantly
- Derive `KEYCLOAK_JWKS_URL` from URL + realm when not set
- `Validate()` — `log.Fatal` on any missing required value
- Apply Gin log configuration

**Dependencies.** `godotenv`, Gin (log mode only).

**Key file.** [config.go](../internal/config/config.go)

**Environment variables**

| Variable | Required | Default | Purpose |
|---|:--:|---|---|
| `DB_URL` | ✅ | — | PostgreSQL DSN |
| `KEYCLOAK_URL` | ✅ | — | Public Keycloak URL — drives expected `iss` |
| `KEYCLOAK_REALM` | ✅ | — | Realm name |
| `KEYCLOAK_CLIENT_ID` | ✅ | — | Token-validation client |
| `KEYCLOAK_JWKS_URL` | ✅* | derived | *Derived from URL + realm if unset |
| `PORT` | | `8080` | Listen port |
| `KEYCLOAK_CLIENT_SECRET` | | — | Reserved |
| `KEYCLOAK_ALLOWED_CLIENT_IDS` | | `{CLIENT_ID}` | `azp` allow-list, CSV |
| `KEYCLOAK_ADMIN_CLIENT_ID` | | — | Admin service account; unset ⇒ `/admin/*` omitted |
| `KEYCLOAK_ADMIN_CLIENT_SECRET` | | — | Must be set together with the id |
| `KEYCLOAK_ADMIN_BASE_URL` | | `KEYCLOAK_URL` | Server-to-server Keycloak URL |
| `ADMIN_CONSOLE_ENABLED` | | `false` | Mount `/admin` console |
| `ADMIN_CONSOLE_CLIENT_ID` | | `DEV_PLAYGROUND_CLIENT_ID` | Console PKCE client |
| `ADMIN_LIVE_CHECK_TTL_SECONDS` | | `30` | Live-admin cache TTL |
| `DEV_PLAYGROUND_ENABLED` | | `false` | Mount `/dev/auth*` — **never in production** |
| `DEV_PLAYGROUND_CLIENT_ID` | | `saas-dev-playground` | Playground PKCE client |
| `CORS_ALLOWED_ORIGINS` | | — | CSV allow-list; empty ⇒ CORS disabled |
| `GIN_LOG_ENABLED` | | `true` | Gin engine logs |
| `GIN_ACCESS_LOG_ENABLED` | | `true` | HTTP access logs |

**Production recipe:** `ADMIN_CONSOLE_ENABLED=true` + `DEV_PLAYGROUND_ENABLED=false`.
Note this is **not currently reproducible via docker-compose** — [TD-004](TECH_DEBT.md#td-004).

**Known weakness.** The package doc comment still documents a `JWTSecret`
field that no longer exists — [KI-009](KNOWN_ISSUES.md#ki-009).

**Maturity: High.** 94.4% coverage, the highest in the project.

---

## `internal/bootstrap`

**Objective.** Make `config/project.json` the single source of truth for both
the API's `.env` and the Keycloak realm export.

**Responsibilities**
- Load and JSON-Schema-validate `project.json`
- Interactive prompting for first-time setup
- Generate `.env`
- Generate `deploy/keycloak/realm-export.json` including clients, roles, and
  optional seed users

**Dependencies.** `santhosh-tekuri/jsonschema`.

**Key files**

| File | Contents |
|---|---|
| [config.go](../internal/bootstrap/config.go) | `project.json` model + loading |
| [generate.go](../internal/bootstrap/generate.go) | `.env` and realm-export generation |
| [prompt.go](../internal/bootstrap/prompt.go) | Interactive prompts |
| [schema.go](../internal/bootstrap/schema.go) + [schema/](../internal/bootstrap/schema/) | Embedded JSON Schema |

**Feature flags.** `project.json` carries a `features` map. **Only two are
read by any code:** `dev_playground` and `seed_users`. The prompt also collects
`multi_tenant`, `google_login`, `mfa` and `swagger`, which nothing consumes —
[KI-007](KNOWN_ISSUES.md#ki-007). Do not infer capability from their presence.

**Endpoints.** None — CLI, via `make init` / `make regen`.

**Maturity: High.** 87.2% coverage. Carries the only `TODO` in the Go codebase
([generate.go:95](../internal/bootstrap/generate.go#L95)).

---

## `internal/database`

**Objective.** Own the GORM connection lifecycle and schema migration.

**Responsibilities**
- Open the PostgreSQL connection with `TranslateError` enabled
- Apply the embedded versioned SQL migrations (unless `DB_MIGRATE_ON_BOOT=false`)
- Expose the migration commands the `cmd/migrate` CLI drives
- `log.Fatal` on connection or migration failure

**Dependencies.** GORM + `gorm.io/driver/postgres`, `golang-migrate` (`iofs` +
`pgx/v5` drivers).

**Key files.** [database.go](../internal/database/database.go) — connection
lifecycle; [migrate.go](../internal/database/migrate.go) — the migration runner;
[migrations/](../internal/database/migrations/) — the versioned SQL, embedded
with `go:embed`. Operator guide: [MIGRATIONS.md](MIGRATIONS.md).

**Endpoints.** None.

**Known weaknesses**
- Connection pool uses GORM defaults, never tuned
- The `make seed` target claims a `database.seedDefaultUser` function that
  does not exist — corrected 2026-07-26

**Maturity: Low** — not because it is poorly written, but because it does
almost nothing. One table, no migrations, no pooling strategy.

---

## `internal/user`

**Objective.** Maintain a local projection of authenticated Keycloak subjects
so future platform features can reference users by a stable integer id.

**Responsibilities**
- `User` model — `keycloak_sub` as canonical key with a unique index
- Repository: `Create`, `Update`, `FindBySub`, `FindByID`
- Service: just-in-time user creation on first request
- Handler: `GET /me`

**Dependencies.** GORM.

**Key files**

| File | Contents |
|---|---|
| [model.go](../internal/user/model.go) | `User` entity + `TableName` |
| [repository.go](../internal/user/repository.go) | `UserRepository` port + GORM implementation |
| [service.go](../internal/user/service.go) | `EnsureUser` JIT provisioning |
| [handler.go](../internal/user/handler.go) | `Me` |
| [dto.go](../internal/user/dto.go) | Response shape |

**Endpoints.** `GET /me` (authenticated).

**Design notes**
- Repository contract: return `(nil, nil)` for not-found, never an error.
- `FindByEmail` is deliberately absent — email is not a stable OIDC identity.
- No optimistic-concurrency version column; documented as an MVP trade-off.

**Maturity: Low.** This is where a real business domain would live, and it is
effectively empty: one entity, six fields, no rules. That is the honest state
of the product domain.

---

## `internal/workspace`

**Objective.** Own the Workspace domain — an isolated administrative context
that will later hold a Connection to an identity provider.

**Responsibilities**
- `Workspace` model, slug rules and the reserved slug set
- Repository over the `workspaces` table (migration `000002`)
- Service: validation, slug derivation, the archive transition
- Handler: the `/v1/workspaces` surface and its stable error envelope

**Dependencies.** GORM, `internal/publicid`, `internal/requestid`. Deliberately
**not** `internal/identity` or `internal/auth` — this domain performs no
Keycloak operation, and its routes are gated by where they are mounted.

**Key files**

| File | Contents |
|---|---|
| [model.go](../internal/workspace/model.go) | `Workspace` domain type — no GORM tags |
| [slug.go](../internal/workspace/slug.go) | normalization, derivation, validation, reserved set |
| [errors.go](../internal/workspace/errors.go) | the stable `/v1` error catalogue |
| [dto.go](../internal/workspace/dto.go) | wire types + the error envelope |
| [repository.go](../internal/workspace/repository.go) | the only file that knows GORM exists |
| [service.go](../internal/workspace/service.go) | business rules |
| [handler.go](../internal/workspace/handler.go) | HTTP + OpenAPI annotations |

**Endpoints.** `GET`/`POST /v1/workspaces`,
`GET`/`PATCH /v1/workspaces/{workspace_id}`,
`POST /v1/workspaces/{workspace_id}/archive`. No `DELETE`.

**Design notes**
- Slug is immutable and never released by archiving.
- Archive is idempotent; a repeat call returns the row unchanged.
- `PATCH` **rejects** `slug`/`status` rather than ignoring them.
- Package coverage understates the real figure: `repository.go` is exercised by
  the integration suite (`-tags=integration`), which the coverage run excludes.

Full domain reference: [WORKSPACES.md](WORKSPACES.md).

---

## `internal/connection`

**Objective.** Own the Connection domain — a Workspace's configured access to an
identity provider — including its verification probe.

**Responsibilities**
- `Connection` model and its state machine (draft → active → retired)
- Repository over the `connections` table (migration `000003`), the only place
  sealed secret material lives
- Service: validation, sealing, the activation transition, workspace scoping
- `KeycloakVerifier`: a read-only probe producing a structured report
- Handler: the `/v1/workspaces/{id}/connections` surface

**Dependencies.** GORM, `internal/secrets`, `internal/workspace` (a one-method
consumer-side interface), `internal/publicid`, `internal/requestid`.
Deliberately **not** `internal/identity` — see the design note below.

**Key files**

| File | Contents |
|---|---|
| [model.go](../internal/connection/model.go) | domain type, states, transitions, `VerifyValidity` |
| [errors.go](../internal/connection/errors.go) | the stable error catalogue |
| [dto.go](../internal/connection/dto.go) | wire types — no secret material |
| [repository.go](../internal/connection/repository.go) | persistence; `OpenSecret` is the only way out for a credential |
| [service.go](../internal/connection/service.go) | business rules |
| [verifier.go](../internal/connection/verifier.go) | the provider probe |
| [handler.go](../internal/connection/handler.go) | HTTP + OpenAPI annotations |

**Endpoints.** 8 under `/v1/workspaces/{workspace_id}/connections`, gated
identically to `/admin/*`.

**Design notes**
- The `Connection` type has **no field for secret material**, so no response
  conversion can leak one. That is structural, not a review convention.
- `KeycloakVerifier` does not reuse `identity/keycloak.AdminClient`: that client
  collapses every failure into `ErrAdminAPIUnavailable`, and the report exists
  precisely to separate unreachable / wrong realm / bad credentials /
  insufficient privileges. The cost is a duplicated `client_credentials` call —
  recorded as [TD-018](TECH_DEBT.md#td-018).
- One active connection per workspace is enforced by a partial unique index, not
  by application code, so it holds under concurrency.
- Package coverage understates the real figure: `repository.go` is exercised
  only by the integration suite, which the coverage run excludes.

Full domain reference: [CONNECTIONS.md](CONNECTIONS.md).

---

## `internal/secrets`

**Objective.** Seal and open small values — provider credentials — with
AES-256-GCM under a master key from the environment.

**Dependencies.** None (`crypto/aes`, `crypto/cipher`, `crypto/rand`).

**Key file.** [aesgcm.go](../internal/secrets/aesgcm.go).

**Design notes**
- Authenticated encryption, a fresh random nonce per seal, and AAD binding each
  ciphertext to the row it belongs to.
- A key version travels with every ciphertext, so a keyring can be introduced
  later without a data migration. Rotation itself is **not** implemented —
  [TD-019](TECH_DEBT.md#td-019).
- Every `Open` failure returns the same error on purpose: distinguishing "wrong
  key" from "wrong AAD" tells an attacker which guess was closer.
- Protects against a stolen dump, not against an attacker who can read the
  master key. Moving to a KMS is the next step and the format allows it.

---

## `internal/publicid`

**Objective.** Render and parse the prefixed identifiers the public API
exposes: a stored UUID becomes `ws_<uuid>` on the wire.

**Dependencies.** None (`crypto/rand`, `encoding/hex`, `strings`).

**Key file.** [publicid.go](../internal/publicid/publicid.go) — `New`, `Format`,
`Parse`, and the prefix constants.

**Design notes**
- A formatting helper, not an identifier framework: the prefix is a plain
  argument, so `conn_`/`prj_` need no change here.
- UUIDv4 is generated in-process rather than by the database, so the id is
  known before the INSERT.
- Accepts a bare UUID as a development convenience; rejects the braced, URN and
  unhyphenated spellings, so one object has exactly one public name.

---

## `internal/requestid`

**Objective.** Attach a correlation id to each request and make it readable by
handlers, for the `/v1` error envelope.

**Dependencies.** Gin, `internal/publicid`.

**Key file.** [requestid.go](../internal/requestid/requestid.go).

**Design notes**
- Its own package rather than part of `internal/server`, because domain
  handlers must read the id and `internal/server` already imports them — the
  accessor there would be an import cycle.
- Mounted on the `/v1` group **only**, so `/admin/*` responses are unchanged.
- A client-supplied `X-Request-Id` is honoured but validated first: the value
  reaches a log line and a response header, so CR/LF and oversized input are
  replaced rather than reflected.

---

## Support packages

`internal/logger` · `internal/banner` · `internal/fonts`

Named structured logger and the ASCII boot banner. Cosmetic, zero coverage, no
tests needed. `internal/fonts` holds a single embedded ASCII font used only by
the banner.

---

## `web/admin`

**Objective.** A full IAM administration console with no build step and no
dependencies.

**Responsibilities.** PKCE login, workspace selection, workspace/connection
management, workspace-scoped users/roles/sessions/invitations CRUD, legacy SMTP
and email-template settings, audit log viewer, embedded documentation viewer,
Swagger UI embed, EN/PT-BR localization, theme toggle.

**Workspace scoping (Slice 6).** Identity views consume
`/v1/workspaces/{id}/…`, and the workspace lives in the ROUTE
(`#/workspaces/ws_x/users`) rather than in application state — so refresh,
bookmarks, the back button and two realms in two tabs all work without a
persistence rule. Installation-scoped pages (Overview, Audit Logs, Swagger) and
the legacy provider settings (SMTP, email templates) carry no workspace segment,
which makes "this page is not workspace-scoped" structural rather than a
convention. See [WORKSPACE_CONSOLE.md](WORKSPACE_CONSOLE.md).

**Structure.** 1 entry (`main.js`) · 10 `lib/` modules · 8 `components/` ·
18 `views/` · 8,600 lines of JS.

**Tests.** 157 cases across 14 `node --test` suites in
[static/js/tests/](../web/admin/static/js/tests/). **These do not run in the
`make ci` gate** — [TD-003](TECH_DEBT.md#td-003) — but they do run in
`make ci-full` via `make test-frontend`.

**API contract dependency.** The console reads a specific field set from
`GET /auth/debug`: `valid`, `issuer`, `allowed_clients`, `received_sub`,
`received_azp`, `roles`, `exp`, `expired`. Breaking it degrades the UI
*silently* — the second symptom of [KI-001](KNOWN_ISSUES.md#ki-001) was exactly
this. The contract is now pinned by `TestAuthDebug_ReturnsSPAContract`.

**Maturity: Medium-High.** Functionally broad and actively developed, but its
tests are outside the CI gate and it has never had an XSS review despite
building DOM from template strings — [KI-005](KNOWN_ISSUES.md#ki-005).

---

## `web/dev`

**Objective.** A standalone six-section token debugger for developers
integrating against this API.

**Responsibilities.** Connection diagnostics, PKCE login/refresh/logout, token
introspection, human-readable explanation of *why* a token was rejected, API
smoke calls, raw payload dumps.

**Files.** `auth.html`, `auth.js`, `styles.css`.

**Endpoints consumed.** `/dev/auth/config.json`, `/dev/auth/debug`, `/health`,
`/me`.

**Enforced rule.** The UI must never judge token validity itself — every
`valid` / `expired` / `roles` value shown comes from `/dev/auth/debug`. The
local JWT decode is explicitly cosmetic.

**Gate.** `DEV_PLAYGROUND_ENABLED=true` only. Never enable in production.

**Maturity: Medium.** Works and is genuinely useful, but has no automated
tests and was broken from 2026-06-13 to 2026-07-26 by
[KI-001](KNOWN_ISSUES.md#ki-001).
