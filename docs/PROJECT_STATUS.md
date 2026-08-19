# LIGHTWEIGHT SAAS BACKEND — Project Status

> **This is the maintainer's status document, not the product's front door.**
> If you are installing or evaluating LIGHTWEIGHT, start at
> [`getting-started/`](getting-started/README.md); if you want to know what the
> product is, start at the [README](../README.md). This page is the current
> state of the *project*: what is built, what is partial, what the numbers are.
>
> Where this document and any other document disagree, this one wins, and the
> code wins over both.

**Last updated:** 2026-08-19
**Last release tag:** `v0.4.0` · Go SDK `sdk/go/v0.1.0` (module `v0.1.0`)
**Prepared, not tagged:** `v0.4.1` on `main` — the console's boot-time
authorization gate. A patch: no API, configuration, or privilege changed.
**Verified against:** the current `main` tree, with the published metrics below
re-derived by `make check-metrics`

---

## Overview

LIGHTWEIGHT is a **self-hosted control plane that puts one or more Keycloak
realms behind a single workspace-scoped HTTP API**, written in Go. Backends
manage users, roles, sessions and invitations through a scoped machine
credential instead of holding Keycloak admin rights.

The product surface is the 47-route `/v1` API: workspaces, connections,
projects, credentials, and workspace-scoped identity and audit. The 32-route
`/admin/*` surface predates workspaces and is retained as operator and
compatibility surface, not as the product.

It is not an application backend: it stores no product data and serves no
product endpoints.

It is deliberately **not** a full SaaS backend. There is no billing, no
multi-tenancy, no queue, no file storage. The business domain is essentially
empty — one local database table projecting Keycloak subjects. What exists is
the identity and access layer, and that layer is mature.

**Stack:** Go 1.25.4 · Gin · GORM · PostgreSQL 15 · Keycloak 26 · Swagger/OpenAPI

Read next:

**Understanding the project**
- [ARCHITECTURE.md](ARCHITECTURE.md) — how it is built and how a request flows
- [MODULES.md](MODULES.md) — per-module responsibilities and maturity
- [FEATURES.md](FEATURES.md) — what exists, with code references

**Where it is going**
- [ROADMAP.md](ROADMAP.md) — what comes next and in what order
- [MILESTONE_v0.4.md](MILESTONE_v0.4.md) — the next milestone proposal
- [RISKS.md](RISKS.md) — the ten biggest threats to future evolution, scored

**What is wrong with it**
- [TECH_DEBT.md](TECH_DEBT.md) — what needs paying down
- [KNOWN_ISSUES.md](KNOWN_ISSUES.md) — bugs, limitations, workarounds

**Contributing to it**
- [QUALITY_GATE.md](QUALITY_GATE.md) — what every PR must satisfy
- [CONTRIBUTION_CHECKLIST.md](CONTRIBUTION_CHECKLIST.md) — the short form
- [HEALTH_CHECK.md](HEALTH_CHECK.md) — is the project healthy right now?
- [RELEASE_CHECKLIST.md](RELEASE_CHECKLIST.md) — how to cut a release

---

## Current State

| | |
|---|---|
| **Overall maturity** | **8.0 / 10** |
| **Production ready?** | **The answer differs by topology, and conflating the two would hide the distinction that matters.** Single-instance, documented: **yes** for the IAM scope. Horizontally scaled / HA: **no** — see [Production readiness by topology](#production-readiness-by-topology). As a SaaS product backend: no either way — the product domain does not exist. |
| **CI** | Green. 6 jobs + CodeQL. `make ci` = fmt · vet · lint · build · test · swagger · docs. The two end-to-end jobs cover opposite boundaries: `e2e` (a machine using the API) and `browser-e2e` (an operator configuring it in Chromium) |
| **Test coverage** | 74.3% unit (floor 73%) · **82.2% authoritative** (floor 80%, `-tags=integration`). Plus 183 frontend cases across 15 `node --test` suites and 27 browser journeys. Authorization is measured differently and reported separately — see [security/AUTHORIZATION_MATRIX.md](security/AUTHORIZATION_MATRIX.md) |
| **Blocking linters** | 9 (`errcheck` and `staticcheck` deferred — see [QUALITY_GATE.md](QUALITY_GATE.md#the-lint-ratchet--read-before-adding-a-linter)) |
| **Open critical bugs** | 0. Two defects found and fixed by the browser suite on 2026-08-10: [KI-019](KNOWN_ISSUES.md#ki-019) (OAuth codes in the access log) and [KI-020](KNOWN_ISSUES.md#ki-020) (the Audit view threw on any workspace with events) |
| **Top risk** | [R-01](RISKS.md#r-01) — **further reduced 2026-08-13**. Both boundaries have real end-to-end coverage in CI, and Slice 14 added the negative half: every project-reachable route swept against every scope, refusals proven to land before the provider is reached, and real-stack evidence per boundary family ([KI-018](KNOWN_ISSUES.md#ki-018) closed, [security/AUTHORIZATION_MATRIX.md](security/AUTHORIZATION_MATRIX.md)) |
| **Audit** | Durable and workspace-scoped, in PostgreSQL. Survives restarts, readable at `GET /v1/workspaces/{id}/audit` by an operator or a credential holding `audit:read`. See [AUDIT.md](AUDIT.md) |
| **Operator surface** | The console is proven by a real browser: a real PKCE login against Keycloak, then workspace → connection → project → credential → audit → revocation, with no mocked boundary. See [testing/BROWSER_E2E.md](testing/BROWSER_E2E.md) |
| **M2M surface** | Usable by an external backend. A Project Credential reaches its documented rate limit, the error contract is uniform, and a consumer needs only a URL, a workspace id and a key — proven from outside by `scripts/m2m-harness.sh` + `cmd/lwprobe`. See [PROJECTS.md](PROJECTS.md) |

### Production readiness by topology

Added 2026-08-13, after Slice 14. Until now this document answered "is it
production ready?" with one label, and that label was hiding a real distinction:
**every remaining structural limitation is a property of running more than one
instance.** A self-hosted product can be genuinely production-capable in a
documented single-instance topology while not yet being HA-capable, and
collapsing those into one word serves nobody.

#### Single-instance production — **capable**

One API process, one PostgreSQL, one Keycloak. This is the topology the product
targets, documents and tests.

What now supports the claim:

- the authorization boundary is evidenced end to end, in both directions, with
  refusals proven to land before any provider is touched and rejected mutations
  proven to leave the realm unchanged
  ([security/AUTHORIZATION_MATRIX.md](security/AUTHORIZATION_MATRIX.md));
- revocation, project archiving, workspace archiving and connection retirement
  all take effect on the next request, with no restart and no cache to wait out,
  proven against a live process with the provider cache warm;
- tenant isolation is proven across real realms, including the cross-realm
  resource-id case that authorizes correctly and then acts on the wrong object;
- eight deliberate breaks of the boundary are all caught by the test suite.

- **control-plane mutations are atomic with their audit rows** (Slice 15,
  [TD-033](TECH_DEBT.md#td-033) closed). All fourteen commit their domain state
  and their audit event in one PostgreSQL transaction; a failed audit write
  rolls the mutation back, so the system does not commit a control-plane change
  without its record.

What is still true and must be documented for an operator:

- **[TD-038](TECH_DEBT.md#td-038)** — the same guarantee is NOT available for
  provider mutations. A user created in Keycloak whose audit row then fails to
  write leaves a change with no record. No PostgreSQL transaction can prevent
  that, and failing the response instead would invite a retry that creates a
  second user;
- **[TD-036](TECH_DEBT.md#td-036)** — no idempotency keys, so a client whose
  response was lost cannot safely retry;
- **[TD-037](TECH_DEBT.md#td-037)** — authenticated authorization failures reach
  the log and the security event channel, not the durable trail. Re-analysed in
  Slice 15 and deliberately left that way; the arithmetic is in the entry.

#### Horizontally scaled / HA production — **not capable**

- **[TD-027](TECH_DEBT.md#td-027)** — rate-limit buckets are per process. Two
  replicas permit twice the published rate, three permit three times. The
  published per-credential number stops being the enforced number, which makes
  the documented contract untrue rather than merely conservative. This is the
  blocker, and it is not a small one to fix honestly: it means a shared store.

Nothing else on the register is HA-specific. The provider cache is per process
and correct per process — it is keyed by connection identity plus configuration
generation, so two replicas cannot disagree about what a workspace routes
through, they simply each hold their own.

#### How the remaining debt classifies

| Item | Blocks single-instance? | Blocks HA? | Blocks SDK adoption? | Operational correctness |
|---|---|---|---|---|
| [TD-027](TECH_DEBT.md#td-027) rate limiting | no | **yes** | no | the published limit is per replica |
| ~~[TD-033](TECH_DEBT.md#td-033) control-plane audit atomicity~~ | — | — | — | **resolved 2026-08-14** |
| [TD-038](TECH_DEBT.md#td-038) provider audit atomicity | no | no | no | **yes** — a provider change can outlive its record |
| ~~[TD-035](TECH_DEBT.md#td-035) SDK release automation~~ | — | — | — | **resolved 2026-08-15** |
| [TD-036](TECH_DEBT.md#td-036) idempotency keys | no | no | **yes** — every consumer builds its own reconciliation | no |
| [TD-037](TECH_DEBT.md#td-037) refusal audit | no | no | no | partial — probing is visible in logs only |

None blocks a documented single-instance deployment. One blocks HA, one is an
operational-correctness limit an operator must be told about, and one is
adoption friction for consumers of the SDK.

Slice 16 removed the other adoption blocker. The SDK is now releasable through
standard Go module tooling: the nested-module tag format is proven by
resolution rather than assumed, an external consumer compiles with no `replace`
directive, and a tag gate refuses a release whose commit does not contain the
module. Nothing has been published — that is deliberately an operator decision —
but the remaining step is `git push`, not engineering. See
[SDK_GO.md § Release](SDK_GO.md#release).

Slice 15 moved the operational-correctness row rather than removing it: the half
that could be made atomic was, and the half that cannot be is now recorded under
its own number with its own reasoning instead of being implied by a resolved
item.

---

### How the maturity score breaks down

| Dimension | Score | Note |
|---|---:|---|
| Code quality & inline documentation | 9 | Comments explain *why*; boundaries are deliberate |
| Security design | 9 | Strong posture, and the authorization boundary is now evidenced rather than asserted (Slice 14). Two open items remain (KI-004, KI-005) |
| IAM feature completeness | 8 | CRUD complete; realm-wide session revoke missing |
| SaaS product domain | 0 | Does not exist by design at this stage |
| Operational reliability | 7 | Critical boot defect fixed; e2e now covers both boundaries in both directions |
| Documentation fidelity | 9 | Consolidated and mechanically verified |
| Process & automation | 9 | Enforced gates on code, coverage, and documentation |

**Score history.** 6.5 → 7.5 on 2026-07-26 (boot-time route collision fixed and
covered; documentation reconciled with the code). 7.5 → **8.0** on 2026-07-27
(quality gates: blocking lint, coverage floor, integration and frontend suites
in CI, mechanical documentation checks, git hooks).

It is capped below 9 by [R-01](RISKS.md#r-01) — coverage is unit-level, and
nothing verifies the assembled system. Closing that is the centre of
[v0.4](MILESTONE_v0.4.md).

### Enforced quality gates

Every one of these blocks a PR. Full detail in [QUALITY_GATE.md](QUALITY_GATE.md).

| Gate | Command | CI job |
|---|---|---|
| Formatting, vet, lint, build, unit tests | `make ci` | `gate` |
| OpenAPI matches handler annotations | `make swagger-check` | `gate` |
| Doc links resolve · published numbers match code | `make check-docs` | `gate` |
| Coverage ≥ 73% | `make coverage-gate` | `coverage` |
| Admin console tests (30) | `make test-frontend` | `frontend` |
| Integration suite vs. real PostgreSQL | `make test-integration` | `integration` |
| Security + quality static analysis | — | `codeql` |

Local pre-commit and pre-push hooks: `make hooks-install`.

---

## Architecture

**Pattern: modular monolith, layered, with dependency inversion at the
external boundaries.** It is not Clean Architecture or DDD, and does not
claim to be.

```
HTTP request
   │
   ▼
Gin engine ── CORS ─→ [route group middleware chain]
   │
   ▼
Handler      (internal/identity, internal/user, internal/server)
   │           HTTP concerns only: bind, validate shape, map errors
   ▼
Service      (internal/identity/service.go, internal/user/service.go)
   │           business rules: validation, normalization, self-protection guards
   ▼
Provider / Repository        ← the two ports
   │           IdentityProvider → Keycloak Admin REST API
   │           UserRepository   → PostgreSQL via GORM
   ▼
External system
```

Two ports keep the core provider-agnostic:

| Port | Contract | Adapter |
|---|---|---|
| [`auth.AuthProvider`](../internal/auth/provider.go) | Runtime token validation | [`auth/keycloak`](../internal/auth/keycloak/) |
| [`identity.IdentityProvider`](../internal/identity/provider.go) | Admin operations (22 methods) | [`identity/keycloak`](../internal/identity/keycloak/) |

Dependency injection is manual and lives entirely in
[cmd/api/main.go](../cmd/api/main.go). No container, no reflection.

Full detail, including request lifecycle and Mermaid diagrams:
**[ARCHITECTURE.md](ARCHITECTURE.md)**.

### Patterns actually present

| Present | Absent |
|---|---|
| Layered / modular monolith · Ports & Adapters (partial) · Repository · Service Layer · Manual DI · Anti-corruption layer · Adapter · Observer (hooks) | DDD tactical patterns (aggregates, value objects, domain events) · CQRS · Event sourcing · Message broker · Microservices |

---

## Implemented Features

Status legend: ✅ implemented · 🟡 partial · 🔴 not started · ⚪ planned only

| Feature | Status | Notes |
|---|:--:|---|
| OIDC / PKCE login | ✅ | Delegated to Keycloak; no login endpoint in Go by design |
| JWT validation (JWKS) | ✅ | Asymmetric algorithms only; `iss` + `azp` + `exp` enforced |
| RBAC by realm role | ✅ | `RequireRole`, `RequireAnyRole` |
| Live-admin check (stale-JWT defense) | ✅ | Closes the token revocation window to a 30 s cache TTL |
| User CRUD (admin) | ✅ | 13 routes |
| Role CRUD (admin) | ✅ | Rename intentionally unsupported |
| Session listing & revocation | ✅ | Per-user and per-session; realm-wide missing |
| Invitations | ✅ | Derived from Keycloak user state |
| Password reset (email) + direct set | ✅ | `POST .../reset-password`, `PUT .../password` |
| Self-protection guards | ✅ | self-delete, self-disable, self-strip-admin, last-admin |
| Audit subsystem | ✅ | 14 canonical actions, 2 sinks |
| Per-IP rate limiting | ✅ | Token bucket on `/admin/*` |
| CORS | ✅ | Explicit allow-list, disabled by default |
| SMTP config + email templates | 🟡 | Works; no dedicated Go tests; FTL theme does not survive rebuild |
| Workspaces (`/v1`) | 🟡 | CRUD-minus-delete + archive — [WORKSPACES.md](WORKSPACES.md) |
| Connections (`/v1`) | 🟡 | Lifecycle + verify + sealed secrets; **not consumed by the Identity API yet** — [CONNECTIONS.md](CONNECTIONS.md) |
| Admin console SPA | ✅ | 14 views, PKCE, i18n, embedded docs viewer |
| Dev auth playground | ✅ | Restored 2026-07-26 (see KI-001) |
| Embedded docs viewer | ✅ | Serves `docs/**.md` from `embed.FS` |
| Swagger / OpenAPI | ✅ | CI gate fails if annotations drift |
| Project bootstrap CLI | ✅ | `project.json` → `.env` + realm export |
| Observability | 🟡 | Structured logs + in-memory ring buffer only |

Complete list with per-feature code references: **[FEATURES.md](FEATURES.md)**.

## Partial Features

| Feature | What works | What is missing |
|---|---|---|
| **Observability** | Structured auth + audit event streams; `GET /admin/audit-events` ring buffer | No Prometheus metrics, no tracing, no `/metrics`. Hooks exist ([`auth.SetEventHook`](../internal/auth/events.go), [`audit.SetDefault`](../internal/audit/recorder.go)) but nothing consumes them |
| **Audit persistence** | Events emitted and logged | Ring buffer is volatile and capped at 500; nothing writes to a database table |
| **Pagination** | `ListUsers` clamps to [1,100]; `ListInvitations` / `ListUsersByRole` page internally with a hard cap | `ListRoles`, `ListSessions`, `ListUserRoles`, `ListUserSessions` do not paginate at all |
| **Email subsystem** | SMTP config, connection test, template customization, custom FTL theme | No Go tests; theme not persisted across container rebuild |
| **Deployment** | Dockerfile + docker-compose + written runbook | No CD pipeline, no IaC; compose omits 4 env vars ([TD-004](TECH_DEBT.md#td-004)) |
| **Secrets management** | `.env` correctly git-ignored; documented rotation procedure | Manual; no Vault / Secrets Manager integration |

## Planned Features

Nothing below exists in code. Do not assume otherwise.

| Feature | Status | Blocking dependency |
|---|:--:|---|
| Multi-tenancy | ⚪ | Architectural decision required first — see [ROADMAP.md](ROADMAP.md#v2--make-it-a-saas-backend) |
| Organizations / Teams | 🔴 | Multi-tenancy |
| Billing | 🔴 | Multi-tenancy + queue |
| File upload / object storage | 🔴 | — |
| Job queue + workers | 🔴 | — |
| Outbound webhooks | 🔴 | Queue |
| Scheduler / cron | 🔴 | Queue |
| Runtime feature flags | ⚪ | Only build-time flags exist in the bootstrap CLI |
| API keys | 🔴 | — |
| Social OAuth (Google) | ⚪ | `google_login` is a bootstrap prompt that nothing reads |
| Distributed cache | 🔴 | — |

---

## Modules

| Module | Responsibility | Status | Maturity |
|---|---|:--:|---|
| [`internal/auth`](../internal/auth/) | Provider-agnostic authn/authz middleware | ✅ | High |
| [`internal/auth/keycloak`](../internal/auth/keycloak/) | JWKS token validation | ✅ | High |
| [`internal/identity`](../internal/identity/) | Admin identity management (handler → service → port) | ✅ | High |
| [`internal/identity/keycloak`](../internal/identity/keycloak/) | Keycloak Admin REST client | ✅ | High |
| [`internal/server`](../internal/server/) | HTTP shell, routing, rate limit, console mounting | ✅ | High |
| [`internal/audit`](../internal/audit/) | Canonical audit event model + recorders | ✅ | High |
| [`internal/logging`](../internal/logging/) | Audit sinks + Gin helpers | ✅ | Medium |
| [`internal/config`](../internal/config/) | Env configuration + fail-fast validation | ✅ | High |
| [`internal/bootstrap`](../internal/bootstrap/) | `project.json` → `.env` + realm export | ✅ | High |
| [`internal/database`](../internal/database/) | GORM connection + versioned SQL migrations | ✅ | Medium |
| [`internal/user`](../internal/user/) | Local user projection | 🟡 | Low |
| [`internal/workspace`](../internal/workspace/) | Workspace domain + `/v1` API | ✅ | Medium |
| [`internal/publicid`](../internal/publicid/) | Prefixed public ids (`ws_<uuid>`) | ✅ | Medium |
| [`internal/requestid`](../internal/requestid/) | Per-request correlation id for `/v1` | ✅ | Medium |
| [`internal/connection`](../internal/connection/) | Identity-provider connections + verify | ✅ | Medium |
| [`internal/secrets`](../internal/secrets/) | AES-256-GCM sealing of credentials at rest | ✅ | Medium-High |
| [`web/admin`](../web/admin/) | Admin console SPA | ✅ | Medium-High |
| [`web/dev`](../web/dev/) | Dev auth playground | ✅ | Medium |

Per-module detail — objective, dependencies, key files, endpoints:
**[MODULES.md](MODULES.md)**.

---

## Project Structure

```
.
├── cmd/
│   ├── api/              Composition root — builds the whole dependency graph
│   └── bootstrap/        Interactive project bootstrap CLI
├── internal/
│   ├── auth/             Authn/authz middleware + AuthProvider port
│   │   └── keycloak/     JWKS adapter
│   ├── identity/         Admin identity management + IdentityProvider port
│   │   └── keycloak/     Keycloak Admin REST adapter
│   ├── user/             Local user projection (repo → service → handler)
│   ├── server/           HTTP shell: engine, routing, rate limit, consoles
│   ├── audit/            Audit event model + recorders
│   ├── logging/          Audit sinks + Gin context helpers
│   ├── config/           Environment configuration
│   ├── bootstrap/        Config-as-source-of-truth generation
│   ├── database/         GORM connection + migration
│   └── logger|banner|fonts/   Logging util + boot banner (cosmetic)
├── web/
│   ├── admin/            Admin console SPA (vanilla JS, no build step)
│   ├── dev/              Dev auth playground
│   └── landing/          Landing page
├── deploy/keycloak/      Realm export + custom email theme (FTL, PT-BR)
├── docs/                 This documentation set + generated OpenAPI
├── config/               project.json — bootstrap source of truth
├── scripts/              Operational shell scripts
└── .github/workflows/    CI + CodeQL
```

There is **no** Kubernetes, Terraform, or Helm configuration. Deployment is
docker-compose plus [operations/PRODUCTION_DEPLOYMENT.md](operations/PRODUCTION_DEPLOYMENT.md).

---

## Database

**ORM:** GORM · **Migrations:** versioned SQL via `golang-migrate`, embedded in
the binary and applied at boot ([MIGRATIONS.md](MIGRATIONS.md)). **Tables owned
by this service: one.**

```
users
  id            uint       PK
  keycloak_sub  string     UNIQUE NOT NULL   ← canonical identity
  email         string     INDEX
  username      string     NOT NULL
  created_at    timestamp
  updated_at    timestamp
```

Defined in [internal/user/model.go](../internal/user/model.go); migrated in
[internal/database/database.go](../internal/database/database.go). No foreign
keys, no relationships, no seed data.

All real identity data — credentials, roles, sessions, attributes — lives in
Keycloak's own PostgreSQL instance and is reachable only through the Admin
REST API. The `users` table is a projection of "subjects this API has seen",
keyed on `keycloak_sub`.

The schema is managed by versioned SQL migrations embedded in the binary and
applied at boot ([MIGRATIONS.md](MIGRATIONS.md)); `AutoMigrate` was removed on
2026-07-28 ([TD-005](TECH_DEBT.md#td-005)).

---

## Authentication

Keycloak owns identity end to end. This API never handles a password and
never signs a token.

Validation performed by [`auth/keycloak.Provider.ValidateToken`](../internal/auth/keycloak/provider.go):

1. Signature verified against the realm JWKS (fetched at boot, fail-fast).
2. Algorithm restricted to `RS*` / `PS*` / `ES*`. **`HS*` is explicitly
   excluded** — it would let anyone holding the verification key mint tokens.
3. `iss` must match the configured realm issuer.
4. `exp` is required.
5. `azp`, when present, must be in the configured allow-list.

Failures return a generic `401 {"error":"unauthorized"}`. The specific reason
goes only to the `AuthEvent` stream — it is never put on the wire.

Two introspection routes exist, and the distinction matters:

| Route | Auth | Availability | Purpose |
|---|---|---|---|
| `GET /auth/debug` | Required | Always | What the admin console reads (`valid`, `issuer`, `allowed_clients`) |
| `GET /dev/auth/debug` | None | `DEV_PLAYGROUND_ENABLED=true` only | Explains *why* a bad token was rejected — the authenticated route cannot, since middleware rejects first |

## Authorization

Three layers, mounted in this order on `/admin/*`
([router.go](../internal/server/router.go)):

```
RateLimitPerIP  →  RequireAuth  →  RequireRole("admin")  →  RequireLiveAdmin
```

1. **Rate limit before auth** so an unauthenticated flood cannot burn CPU on
   JWT validation.
2. **`RequireAuth`** — valid token required.
3. **`RequireRole("admin")`** — reads the JWT claim. Cheap; short-circuits
   non-admins without a network call.
4. **`RequireLiveAdmin`** — asks Keycloak whether the subject *currently*
   holds the role. **Fails closed with 503** if the check itself errors; it
   never falls back to the JWT claim. A 30 s TTL cache bounds Keycloak load,
   and in-band mutations invalidate it immediately.

Layer 4 is what makes revocation meaningful: without it a signed admin token
stays privileged until `exp` (up to an hour), regardless of server-side role
removal.

Business-rule guards live in the service tier
([identity/service.go](../internal/identity/service.go)): no self-delete, no
self-disable, no self-strip-admin, no removing the last enabled admin,
protected/reserved role names.

**Not implemented:** granular permissions, ABAC/policies, API keys, social
OAuth, backchannel logout.

---

## Testing

| | |
|---|---|
| **Aggregate coverage** | **73.2%** (floor 73%) — `go test -count=1 -coverprofile=… -coverpkg=./... ./...` |
| **Go test functions** | 1162 across 112 test files |
| **Frontend tests** | 150 across 13 `node --test` suites in [web/admin/static/js/tests/](../web/admin/static/js/tests/) |
| **Tooling** | Standard `testing` + `httptest`. No external assertion or mocking library |

> Always pass `-count=1` when measuring coverage. Go's test cache will
> otherwise return stale per-package results and the aggregate will be wrong.

**Strategy.** Unit tests dominate, using `httptest.Server` to stand in for
Keycloak so no test needs a live stack. Handler tests exercise the full
middleware chain through the real Gin engine.

### Coverage by package

| Package | Coverage |
|---|---:|
| `internal/config` | 94.4% |
| `internal/bootstrap` | 87.2% |
| `internal/audit` | 86.5% |
| `internal/auth/keycloak` | 86.4% |
| `internal/identity/keycloak` | 74.4% |
| `internal/auth` | 71.0% |
| `internal/identity` | 67.5% |
| `internal/server` | 62.7% |
| `internal/database` | 42.9% |
| `internal/user` | 40.0% |
| `internal/logging` | 25.0% |
| `cmd/*`, `logger`, `banner`, `fonts` | 0% |

### Gaps

- **No automated end-to-end tests.** The artifacts under
  [evidence/](evidence/) are static screenshots and JSON captures from manual
  runs in May 2026. They are records, not regression tests. See
  [TD-003](TECH_DEBT.md#td-003).
- **Integration and frontend tests do not run in CI.** The integration test is
  behind the `integration` build tag; the frontend suites need `node --test`.
  `make ci` runs neither.

---

## Metrics

Re-derived at the current `main` tree. Every row marked ✓ is checked by
`make check-metrics`, which fails the build when the code and this table
disagree; the rest are re-derived by hand at release.

| Metric | Value | |
|---|---:|:--:|
| Go packages | 30 | ✓ |
| Go source files (non-test) | 141 | ✓ |
| Go test files | 113 | ✓ |
| Go test functions | 1162 | ✓ |
| Frontend test cases | 166 | |
| Lines of Go (incl. tests) | 84,358 | |
| Lines of Go (excl. tests and generated OpenAPI) | 32,562 | |
| Lines of frontend JS | 9,132 | |
| **Unit test coverage** | **74.3%** (floor 73.0%) | |
| **Full test coverage (authoritative, `-tags=integration`)** | **80.4%** (floor 80.0%) | |
| **HTTP routes (total)** | **96** | ✓ |
| — **Product API (`/v1/*`)** | **47** | ✓ |
| — Admin API (`/admin/*`, fully gated) | 32 | ✓ |
| — Authenticated (`/me`, `/auth/debug`) | 2 | |
| — Public operational (`/health`, `/health/live`, `/health/ready`, `/swagger/*`, `/`) | 5 | |
| — Admin console shell (public, static) | 4 | |
| — Dev playground (`DEV_PLAYGROUND_ENABLED` only) | 5 | |
| — Metrics (`METRICS_ENABLED` only) | 1 | |
| Credential scopes | 9 | ✓ |
| HTTP handler methods | 32 | ✓ |
| `IdentityProvider` interface methods | 23 | ✓ |
| Canonical audit actions (declared = emitted) | 28 | ✓ |
| Database tables owned | 6 | |
| Docker Compose services | 5 | ✓ |

> The route total omitted the entire `/v1` surface until 2026-08-16: it read
> `46`, which was the count before workspaces existed. That is exactly the rot
> `check_doc_metrics.py` exists to stop, so the total and the `/v1` line are
> now derived rather than maintained.

> **`Go packages` counts the SERVER module only.** `sdk/go` is a separate Go
> module, so `go list ./...` stops at its boundary, while the file and
> test-function counts above use `find`, which does not. That asymmetry is why
> the SDK reports its coverage separately; see [SDK_GO.md](SDK_GO.md#gates).

Admin API routes by verb: 12 GET · 8 POST · 3 PUT · 2 PATCH · 7 DELETE.

**How to re-derive these numbers** (do this before editing any count):

```bash
go list ./... | wc -l                                     # packages
grep -rh "^func Test" --include='*_test.go' . | wc -l     # test functions
grep -cE 'admin\.(GET|POST|PUT|PATCH|DELETE)\(' internal/server/router.go
grep -cE '^\tAction[A-Za-z]+ +Action = ' internal/audit/event.go
go test -count=1 -coverprofile=/tmp/c.out -coverpkg=./... ./... \
  && go tool cover -func=/tmp/c.out | tail -1
```

---

## Pending Work

Ordered by priority. Full detail in [TECH_DEBT.md](TECH_DEBT.md) and
[ROADMAP.md](ROADMAP.md).

| # | Item | Severity | Reference |
|---|---|---|---|
| 1 | ~~No automated e2e suite — regressions escape (KI-001 did)~~ resolved in two halves: machine boundary 2026-08-09, operator boundary 2026-08-10 | — | [TD-003](TECH_DEBT.md#td-003) · [TD-031](TECH_DEBT.md#td-031) |
| 2 | docker-compose omits 4 env vars; documented production recipe is not runnable | High | [TD-004](TECH_DEBT.md#td-004) |
| 3 | Rate limit trusts `X-Forwarded-For` unconditionally — trivially bypassable | Medium | [KI-004](KNOWN_ISSUES.md#ki-004) |
| 4 | Integration + frontend tests excluded from CI | Medium | [TD-003](TECH_DEBT.md#td-003) |
| 5 | ~~`AutoMigrate` with no versioned migrations~~ resolved 2026-07-28 | — | [TD-005](TECH_DEBT.md#td-005) |
| 6 | Multi-tenancy decision deferred; cost grows with every feature | Medium | [ROADMAP.md](ROADMAP.md#v2--make-it-a-saas-backend) |
| 7 | `SetupRouter` takes 8 positional parameters | Medium | [TD-006](TECH_DEBT.md#td-006) |
| 8 | N+1 in `ListSessions`; inconsistent pagination | Medium | [TD-007](TECH_DEBT.md#td-007) |
| 9 | Audit trail is volatile (ring buffer only) | Medium | [TD-008](TECH_DEBT.md#td-008) |
| 10 | No metrics or tracing | Medium | [TD-009](TECH_DEBT.md#td-009) |
| 11 | No realm-wide session revocation | Low | [KI-006](KNOWN_ISSUES.md#ki-006) |
| 12 | No `golangci-lint` in CI | Low | [TD-011](TECH_DEBT.md#td-011) |

---

## Roadmap Summary

| Horizon | Theme | Detail |
|---|---|---|
| **MVP** (done) | IAM foundation | Auth, RBAC, admin CRUD, audit, console — shipped |
| **v1** | Make it operable | e2e tests, CI completeness, metrics, durable audit, migrations, compose fixes |
| **v2** | Make it a SaaS backend | Multi-tenancy decision → organizations → queue → billing |
| **Future** | Scale and ecosystem | Webhooks, storage, runtime feature flags, IaC + CD |

Full breakdown with priority, impact and dependencies: **[ROADMAP.md](ROADMAP.md)**.

---

## Architectural Decisions

Decisions that shape the codebase and should not be reversed casually.

### AD-001 — Keycloak owns identity
No password handling, no JWT signing, no `/login` or `/register` route in Go.
**Why:** credential handling is the highest-risk code in any SaaS backend, and
Keycloak already solves it with a far larger security budget.
**Cost:** every admin operation is a network round-trip to the Admin REST API,
and Keycloak's data model leaks into what is expressible.

### AD-002 — Two separate ports for two separate concerns
[`auth.AuthProvider`](../internal/auth/provider.go) validates tokens on the
request path; [`identity.IdentityProvider`](../internal/identity/provider.go)
performs admin operations. Separate credentials, separate lifecycles.
**Why:** the token-validation client and the privileged admin service account
must not share a secret.

### AD-003 — Live admin check overrides the JWT claim, failing closed
[`RequireLiveAdmin`](../internal/auth/admin_check.go) consults Keycloak and
returns 503 on lookup failure rather than trusting the claim.
**Why:** a stateless JWT stays privileged until `exp`. Falling back to the
claim on error would reopen exactly the window this layer closes.
**Cost:** admin requests depend on Keycloak availability.

### AD-004 — Admin routes are omitted, not disabled, when unconfigured
Without admin client credentials, `/admin/*` is never registered — callers get
404, not 403 or 503 ([server.go](../internal/server/server.go)).
**Why:** a 403 confirms the feature exists. A 404 does not.

### AD-005 — The admin console HTML shell is public
`/admin` serves static HTML to anyone. Every action it performs goes through
the gated API.
**Why:** the shell contains no secrets; gating it would add a login wall in
front of a login wall.

### AD-006 — `/auth/debug` is authenticated; its dev twin is not
Two routes, one handler ([playground.go](../internal/server/playground.go)).
**Why:** the console needs the payload in production, so it must be safe to
expose there — hence authenticated. But an authenticated route cannot explain
why a *bad* token failed, because middleware rejects it first. The dev-only
unauthenticated twin at `/dev/auth/debug` covers that.
**History:** collapsing these into one path caused [KI-001](KNOWN_ISSUES.md#ki-001).

### AD-007 — Configuration is generated from `project.json`
[internal/bootstrap](../internal/bootstrap/) generates `.env` and the Keycloak
realm export from a single source of truth.
**Why:** keeps the API config and the realm config from drifting apart.
**Caveat:** three feature flags in the prompt (`multi_tenant`, `google_login`,
`mfa`) are collected but never read — see [KI-007](KNOWN_ISSUES.md#ki-007).

### AD-008 — Manual dependency injection
The entire graph is wired by hand in [cmd/api/main.go](../cmd/api/main.go).
**Why:** the graph is small enough to read in one screen; a DI container
would trade that clarity for reflection and startup magic.
**Cost:** `SetupRouter` has grown to 8 positional parameters — see
[TD-006](TECH_DEBT.md#td-006).

---

## History

| Date | Change |
|---|---|
| 2026-07-26 | Documentation consolidation. Created this document plus ARCHITECTURE / MODULES / FEATURES / ROADMAP / TECH_DEBT / KNOWN_ISSUES. Fixed KI-001 (boot-time route collision + degraded console payload) and added the flag-matrix regression tests. Corrected route count (22 → 46 total, 32 admin), audit action count (13 → 14), and marked L4/L5 resolved. |
| 2026-07-16 | `e3c4e22` — server tests realigned with `SetupIdentity` / `SetupRouter` signatures; CI restored to green |
| 2026-06-15 | `PUT /admin/users/:id/password`, `email_verified` on user PATCH |
| 2026-06-14 | Configurable CORS via `CORS_ALLOWED_ORIGINS` |
| 2026-06-13 | SMTP settings, email template customization, custom Keycloak FTL theme |
| 2026-05-25 | `v0.3.1` — landing page. **No CHANGELOG entry** (see [KI-008](KNOWN_ISSUES.md#ki-008)) |
| 2026-05-25 | `v0.3.0` — Production Hardening: rate limiting, audit ring buffer, `ADMIN_CONSOLE_ENABLED`, CI + CodeQL |
| 2026-05-20 | GAP-1 remediation — live admin check |
| — | `v0.2.0` — identity management CRUD |
| — | `v0.1.0-auth-foundation` — authentication foundation |

**Maintenance rule:** update this document whenever a module changes status, a
metric moves, or an architectural decision is made. If you change a number
here, re-derive it with the commands in [Metrics](#metrics) — do not copy it
from another document.
