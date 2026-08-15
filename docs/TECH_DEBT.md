# Technical Debt

**Last updated:** 2026-08-10 · Companion to [PROJECT_STATUS.md](PROJECT_STATUS.md), [KNOWN_ISSUES.md](KNOWN_ISSUES.md) and [RISKS.md](RISKS.md)

Debt is a **deliberate or accumulated shortcut that raises the cost of future
change**. Defects and runtime limitations live in
[KNOWN_ISSUES.md](KNOWN_ISSUES.md) instead; a few items are cross-referenced
where the distinction is thin.

**Priority scale**
- **High** — pay down in the current or next cycle; actively slowing work or
  hiding risk
- **Medium** — schedule deliberately; cost is real but bounded
- **Low** — opportunistic; fix when touching the surrounding code

---

## Summary

> Debt describes shortcuts and their cost. For **forward-looking** threats to
> the project's ability to evolve — scored by impact × probability — see
> [RISKS.md](RISKS.md).

| ID | Category | Item | Priority |
|---|---|---|:--:|
| [TD-001](#td-001) | Documentation | Documentation drifted ~2 months behind code | ~~High~~ **Resolved 2026-07-26** |
| [TD-002](#td-002) | Code | Boot-time route collision + degraded console payload | ~~High~~ **Resolved 2026-07-26** |
| [TD-003](#td-003) | Testing | No automated e2e (CI wiring resolved; e2e remains) | ~~High~~ **Resolved 2026-08-09** |
| [TD-004](#td-004) | Infrastructure | docker-compose omits environment variables the code reads | ~~High~~ **Resolved 2026-08-09** |
| [TD-005](#td-005) | Architecture | `AutoMigrate` with no versioned migrations | ~~Medium~~ **Resolved 2026-07-28** |
| [TD-006](#td-006) | Code | `SetupRouter` takes 8 positional parameters | ~~Medium~~ **Resolved 2026-08-09** |
| [TD-007](#td-007) | Performance | N+1 in `ListSessions`; inconsistent pagination | **Medium** |
| [TD-008](#td-008) | Architecture | Audit trail is volatile | ~~Medium~~ **Resolved 2026-08-10** |
| [TD-009](#td-009) | Observability | No metrics, no tracing, no readiness probe | ~~Medium~~ **Partially resolved 2026-08-09** (tracing remains, **Low**) |
| [TD-010](#td-010) | Architecture | Multi-tenancy deferred; cost compounds | **Medium** |
| [TD-011](#td-011) | Infrastructure | No linter in CI | ~~Low~~ **Resolved 2026-07-27** |
| [TD-012](#td-012) | Code | Stale and disproportionate comments in `internal/config` | **Low** |
| [TD-013](#td-013) | Infrastructure | No graceful shutdown | ~~Low~~ **Resolved 2026-08-09** |
| [TD-014](#td-014) | Code | Unused `RequireAnyRole` middleware | **Low** |
| [TD-015](#td-015) | Bootstrap | Three feature flags collected but never read | **Low** |
| [TD-016](#td-016) | Testing | `internal/logging` at 25% coverage | **Low** |
| [TD-017](#td-017) | Documentation | Missing community health files referenced by CONTRIBUTING | **Low** |
| [TD-018](#td-018) | Code | Keycloak token acquisition duplicated in the connection verifier | **Low** |
| [TD-019](#td-019) | Security | No master-key rotation for credentials at rest | ~~Medium~~ **Resolved 2026-08-11** |
| [TD-020](#td-020) | Code | `ListUsersResponse` echoes unclamped pagination | ~~Low~~ **Fixed on `/v1` 2026-08-09; open on `/admin`** |
| [TD-021](#td-021) | Testing | Keycloak test scaffolding duplicated across packages | **Low** |
| [TD-022](#td-022) | Architecture | `/admin/*` and `/v1` are two identity code paths | **Medium** |
| [TD-023](#td-023) | Code | Last-admin guard assumed every realm has an `admin` role | ~~High~~ **Resolved 2026-08-09** |
| [TD-024](#td-024) | Architecture | `access_mode` cannot detect a read-only service account | ~~Medium~~ **Resolved 2026-08-09** |
| [TD-025](#td-025) | Testing | `POST /admin/users/password` still bypasses the service | **Low** |
| [TD-026](#td-026) | Architecture | The `/v1` edge IP limit is below the per-credential limit | ~~Medium~~ **Resolved 2026-08-09** |
| [TD-027](#td-027) | Architecture | Rate-limit buckets are per process | **Low** |
| [TD-028](#td-028) | Architecture | A credential over its limit still buys one lookup per request | **Low** |
| [TD-029](#td-029) | API contract | `invalid_request` does not name the field it rejected | ~~Medium~~ **Resolved 2026-08-09** |
| [TD-030](#td-030) | Testing | The integration suite cannot be run with `-race` at default parallelism | ~~Low~~ **Resolved 2026-08-11** |
| [TD-031](#td-031) | Testing | The admin console has no end-to-end coverage | ~~Medium~~ **Resolved 2026-08-10** |
| [TD-032](#td-032) | Observability | `/metrics` has no scrape-side integration proof | **Low** |
| [TD-033](#td-033) | Architecture | Control-plane mutations are not transactional with their audit write | ~~Low~~ **Resolved 2026-08-14** |
| [TD-034](#td-034) | API contract | The `/admin/*` audit surface and the workspace trail are two vocabularies | **Low** |
| [TD-035](#td-035) | Release | No release automation understands the nested SDK module's tag prefix | ~~Medium~~ **Resolved 2026-08-15** |
| [TD-036](#td-036) | API contract | No idempotency keys, so a client cannot safely retry a mutation | **Medium** |
| [TD-037](#td-037) | Observability | Authenticated authorization failures reach no durable trail | **Low** |
| [TD-038](#td-038) | Architecture | Provider mutations cannot be atomic with their audit write | **Low** |

**Open:** 18 · **Resolved:** 17 · **Partially resolved:** 1 (TD-009 — tracing
remains)

Resolved: TD-001, TD-002 on 2026-07-26 · TD-011 on 2026-07-27 · TD-005 on
2026-07-28 · TD-006, TD-023, TD-024, TD-026 on 2026-08-09 (Slice 8) ·
TD-003, TD-004, TD-013, TD-029 on 2026-08-09 (Slice 9) · TD-008 on 2026-08-10
(Slice 10) · TD-031 on 2026-08-10 (Slice 11) · TD-019, TD-030 on 2026-08-11
(Slice 12)

---

## TD-001

### Documentation drifted ~2 months behind the code — **RESOLVED 2026-07-26**

**Was.** Documentation was frozen at `v0.3.0` (2026-05-25) while 20 commits
landed through July. Concretely: `KNOWN_LIMITATIONS.md` still listed L4 (audit
wiring) and L5 (SMTP) as open when both were implemented; the README claimed
"22 routes" against an actual 46 and "13 canonical actions" against 14; tag
`v0.3.1` had no CHANGELOG entry; `CLAUDE_FIX_TESTS.md` described a broken test
suite that commit `e2a3bcd` had already fixed.

**Impact.** Documentation was actively misleading. An engineer or AI agent
reading it would have concluded that audit emission was a no-op and that the
email endpoints returned 502 — both false.

**Resolution.** This documentation set. Numbers re-derived from the code, L4/L5
marked resolved, CHANGELOG updated, `CLAUDE_FIX_TESTS.md` removed.

**Preventing recurrence.** [PROJECT_STATUS.md](PROJECT_STATUS.md#metrics)
carries the shell commands to re-derive every count, and the rule that numbers
must be re-derived rather than copied between documents.

---

## TD-002

### Boot-time route collision and degraded console payload — **RESOLVED 2026-07-26**

**Was.** `GET /auth/debug` was registered in two places, so the process
panicked at startup whenever `DEV_PLAYGROUND_ENABLED=true` — the default in
`.env.example`. With the playground off, the surviving handler omitted fields
the admin console depends on, rendering an authenticated admin as "not signed
in".

**Root cause.** Two shortcuts compounding: a quick reimplementation of an
existing handler instead of reusing it, and a test suite that only exercised
`SetupRoutes` with one flag combination.

**Resolution.** One handler, two clearly-scoped routes; flag-matrix and payload
contract tests added. Full detail in [KI-001](KNOWN_ISSUES.md#ki-001).

---

## TD-003

### No automated end-to-end tests — **RESOLVED 2026-08-09**

**Priority: High** · Category: Testing · Partially resolved 2026-07-27, closed 2026-08-09

**Was.** Every test was unit-level. What passed for end-to-end coverage was
[evidence/](evidence/) — screenshots and JSON captures from manual runs in May
2026, which are records of a past run rather than tests.

**Impact.** This is the debt that produced [KI-001](KNOWN_ISSUES.md#ki-001): a
process-crashing defect shipped on 2026-06-13 and survived until 2026-07-26,
through 19 subsequent commits and a green CI.

**Resolved 2026-07-27 — the CI-wiring half.** Integration suite against a real
PostgreSQL, admin console suites, coverage floors, blocking linters.

**Resolved 2026-08-09 — the half that mattered.** CI now has an `e2e` job that
starts PostgreSQL, starts Keycloak 26, applies migrations, boots the real
`./bin/api`, waits for **readiness**, and then drives the product through
`scripts/m2m-harness.sh --smoke`:

```text
workspace → connection → project → credential → external client
  → real read · real write · wrong workspace refused · insufficient scope refused
  → revocation immediate · connection rotation invisible to the credential
  → multi-realm isolation verified through Keycloak's own API
  → no secret in the log · graceful shutdown
```

The external half is `cmd/lwprobe`, which imports nothing from this module —
a test fails the build if it ever does — so what it proves, it proves over the
public HTTP contract.

**Why the same script as local, rather than a CI suite.** A CI-only harness is
the one that rots, because nobody runs it while debugging. `--smoke` drops time,
not coverage: the expired-credential row (which needs a real wait) and the
measurement pass. `lwprobe` prints SKIP for what it did not check, so the
omission is visible in the CI log rather than implied.

**What is still NOT covered.** The console SPA is exercised by its own
`node --test` suites and by nothing end to end — no browser drives a real login.
`[R-01](RISKS.md#r-01)` narrows to that; the API path it was mostly about is now
covered on every push.

## TD-004

### docker-compose omits environment variables the code reads — **RESOLVED 2026-08-09**

**Priority: High** · Category: Infrastructure

**Was.** Four variables when this was written; **seven** by the time it was
fixed, because the mechanism kept producing more. `docker-compose.yml`,
`.env.example`, the deployment guide and the loader were four descriptions of
one set, and three of them were updated by remembering to.

The failure is silent and specific. Docker does **not** forward the host
environment into a container: a variable set in `.env` that compose does not
name in the service's `environment:` block never arrives, and the process falls
back to its default with no warning anywhere. The deployment boots, reports
healthy, and has a feature switched off. At the point of the fix the api service
was not passing `ADMIN_CONSOLE_ENABLED`, `ADMIN_CONSOLE_CLIENT_ID`,
`ADMIN_LIVE_CHECK_TTL_SECONDS`, `CORS_ALLOWED_ORIGINS`, `RATE_LIMIT_EDGE_RPS`,
`RATE_LIMIT_CREDENTIAL_RPS` or `SHUTDOWN_TIMEOUT_SECONDS`, and `.env.example` was
missing six.

**Resolution.** The declaration moved into the code, as a table in
[`internal/config/contract.go`](../internal/config/contract.go) carrying every
variable's consumer, requirement, default, secrecy and purpose. Three things now
read it: `Validate` (which required variables to enforce), the published
configuration matrix in [operations/RUNNING.md](operations/RUNNING.md#2-configuration-contract),
and the gate below.

**Preventing recurrence.** `internal/config/contract_test.go` closes each
direction of the drift against the REAL files:

```text
code → table     a variable LoadConfig reads with no contract entry
table → code     a contract entry nothing reads (a stale row)
table → env      a variable an operator cannot discover from .env.example
table → compose  a variable the reference deployment never passes through
```

Adding four lines by hand would have fixed the instance. This makes the table
and the deployment unable to disagree while the build is green — and it found
the seven above the first time it ran.

## TD-005

### `AutoMigrate` with no versioned migrations

**~~Priority: Medium~~ · Resolved 2026-07-28** · Category: Architecture

**Resolution.** Replaced by versioned SQL migrations run through
[golang-migrate](https://github.com/golang-migrate/migrate) and embedded in the
binary with `go:embed`. `AutoMigrate` is gone from the code base.
`000001_baseline` reproduces the exact schema `AutoMigrate` produced, written
idempotently so existing installations are adopted with no manual step — proven
by `TestMigrate_AdoptsLegacyAutoMigrateSchema`. Migrations run at boot
(`DB_MIGRATE_ON_BOOT`, default true) and fail the process rather than serve
traffic against an unknown schema. Commands: `make migrate`,
`make migrate-version`, `make migrate-new`, `make migrate-force`. See
[MIGRATIONS.md](MIGRATIONS.md). The original entry follows.

**Description.** Schema management is a single `AutoMigrate` call at boot
([database.go](../internal/database/database.go)). There is no migration tool,
no version table, no rollback path.

**Impact.** Acceptable today — one table, six columns. It becomes serious the
moment a second table or any column change exists, because `AutoMigrate`:
- never drops columns or constraints, so removals silently do not happen;
- has no ordering guarantees across models;
- has no down path;
- gives no record of what ran against a given environment.

The cost of adopting migrations rises once an `AutoMigrate`-managed schema
exists in production, because the first migration must be reconciled against
whatever `AutoMigrate` actually produced there.

**Recommendation.** Adopt `golang-migrate` or `atlas` **before** the schema
grows. Generate the baseline from the current `users` table and gate `make
migrate` in CI.

**Roadmap:** [V1-07](ROADMAP.md#v1-07--versioned-migrations)

---

## TD-006

### `SetupRouter` took 8 positional parameters — **RESOLVED 2026-08-09**

**Was.**

```go
func SetupRouter(
    router *gin.Engine,
    userHandler *user.Handler,
    identityHandler *identity.Handler,
    auditHandler *AuditHandler,
    provider auth.AuthProvider,
    adminChecker auth.AdminChecker,
    smtpHandler *SMTPHandler,
    emailTemplatesHandler *EmailTemplatesHandler,
    opts ...RouterOption,          // + two variadic options added in Slices 2-3
)
```

Four of the eight were nilable and meaningful only by position. Commit `e2a3bcd`
exists solely to repair tests broken by a signature change to it, and a
root-level troubleshooting document was once written to explain that breakage.

**Resolution.** One `RouterDeps` struct; `SetupRouter(router, deps)` and
`SetupRoutes(deps)`. `RouterOption`, `WithWorkspaces` and `WithConnections` are
gone — the options were a stopgap for this entry, and absorbing them was the
point.

Adding the workspace-scoped identity surface in the same slice was the proof:
the new `WorkspaceIdentity` field touched **no existing call site**.

**A live bug fell out of it.** `SetupIdentity` declared
`*auth.CachedAdminChecker` as its second return type and returned a typed nil on
the not-configured path. Stored in an interface-typed field that becomes a
*non-nil* interface wrapping a nil pointer, so `checker != nil` reads true,
`RequireLiveAdmin` gets mounted, and the first request through it dereferences a
nil receiver. Reachable by any deployment with `SECRETS_MASTER_KEY` and
workspaces but no admin client credentials. The return type is now the
`auth.AdminChecker` interface, so the nil check means what it says.

**Regression coverage added.** A golden full route table, a gate-ordering test
that proves the rate limiter still precedes auth on both groups, the zero-value
mount set, and two tests pinning the typed-nil fix.

---

## TD-007

### N+1 in `ListSessions`; pagination is inconsistent

**Priority: Medium** · Category: Performance

**Description — N+1.**
[`ListSessions`](../internal/identity/keycloak/sessions.go) fetches
`GET /clients`, then issues **one request per enabled client**, sequentially. No
concurrency, no cache, no aggregate timeout. A single client failure is
swallowed and the listing continues, so results can be silently incomplete.

The provider's own doc comment acknowledges it: *"large realms will pay a
per-client RTT."* Acknowledged is not mitigated.

**Description — pagination.** Four different behaviours across seven list
endpoints:

| Endpoint | Behaviour |
|---|---|
| `ListUsers` | Clamps `first`/`max` to [1,100] — correct |
| `ListInvitations` | Pages internally to a hard cap, returns everything |
| `ListUsersByRole` | Pages internally to a hard cap, returns everything |
| `ListRoles`, `ListSessions`, `ListUserRoles`, `ListUserSessions` | No pagination at all |

**Impact.** Latency on `/admin/sessions` grows linearly with realm client count.
Realms above the hard caps are silently truncated — the API reports success
while omitting data. Consumers cannot page uniformly.

**Recommendation.**
1. Bounded concurrent fan-out (`errgroup` with a semaphore) plus an aggregate
   context timeout for `ListSessions`; surface partial failure explicitly
   rather than swallowing it.
2. A single pagination convention applied to every `List*`, with the hard-cap
   truncation made visible in the response.

**Roadmap:** [V1-10](ROADMAP.md#v1-10--performance-and-consistency-cleanup)

---

## TD-008

### Audit trail is volatile — **RESOLVED 2026-08-10**

**Priority: Medium** · Category: Architecture

**Was.** Audit events went to two places: a structured log line, durable only if
somebody was shipping logs, and a 500-entry in-process ring buffer that a
restart emptied. For a product that administers IAM, the answer to "who revoked
that credential last week" was "nobody knows".

**Resolution.** A durable, workspace-scoped trail in PostgreSQL
(migration `000006`, [`internal/auditlog`](../internal/auditlog),
[AUDIT.md](AUDIT.md)) with a read API at
`GET /v1/workspaces/{id}/audit`, cursor pagination, a dedicated `audit:read`
scope, and 90-day retention.

**What the durable trail found on its first pass.** Eleven control-plane
mutations emitted NO audit event at all — every workspace and connection
operation, including `connection.activated`, which silently redirects an entire
workspace to a different Keycloak realm. Nobody decided that; the routes were
added over three slices and the audit call never was. All eleven now emit.

**What it deliberately does not do.** It is not a request log: reads, health
checks and traffic that never passed authentication produce zero rows, which is
what keeps the table bounded by real activity rather than by traffic.

**Preventing recurrence.** `internal/auditlog/coverage.go` classifies every
mutating `/v1` route as `audited(<event>)` or explicitly not-audited-because,
and `TestCoverage_EveryMutatingRouteIsClassified` walks the authorization
registry — already proven complete against the mounted routes — and fails the
build on anything unclassified. A future mutation cannot merge without someone
writing down what it records. Verified to catch exactly the regression above by
removing an entry and watching it fail.

The ring buffer survives, unchanged, behind `/admin/audit-events`: it answers
"what just happened on this box", the table answers "what has ever happened in
this workspace", and neither is authority for the other's question
([TD-034](#td-034) tracks folding the two surfaces together when `/admin/*`
retires).

## TD-009

### No metrics, no tracing, no readiness probe — **PARTIALLY RESOLVED 2026-08-09**

**Priority: Medium** → **Low** · Category: Observability

**Was.** Observability was structured log lines plus the audit ring. No metrics,
no tracing, and one `/health` that answered "the process is up" — which an
orchestrator cannot use to decide whether to route traffic.

**Resolved — the operational half:**

- **Liveness and readiness are separate.** `/health/live` does no I/O;
  `/health/ready` checks the database and whether a drain has begun. Readiness
  deliberately does NOT consult workspace connections: one tenant's Keycloak
  going down must not take the instance out of rotation and every other tenant
  with it.
- **Minimal metrics** at `/metrics`, off by default, loopback-only without a
  token: request counts by method/route-pattern/status, a duration histogram,
  authentication failures and authorization denials. No high-cardinality or
  identifying label anywhere — rate-limit rejections and provider failures read
  as `status="429"` and `status="502"` rather than needing their own series.
- **Request logs correlate a machine call** by `request_id`, `project_id`,
  `credential_id` and `workspace_id`. Reads emit no audit event, so this line is
  the only record that a credential performed one.

No new dependency: the exposition format is ~60 lines for three closed metric
families, and `prometheus/client_golang` would add four transitive modules to a
fifteen-entry dependency set. The trade is stated in
[`internal/metrics/metrics.go`](../internal/metrics/metrics.go) and is worth
revisiting the day exemplars or a shared registry are needed.

**Still open — tracing.** No spans, no context propagation, no OpenTelemetry.
Distributed tracing is worth having when there is something distributed to
trace; today a request touches this process and one Keycloak, and `request_id`
already ties the two ends together. Reopen when a second service exists.

## TD-010

### Multi-tenancy deferred; the cost compounds

**Priority: Medium** (rising) · Category: Architecture

**Description.** No tenancy exists. A code comment in
[identity/provider.go](../internal/identity/provider.go) states that
*"multi-tenancy work in v0.3 will promote specific keys (tenant_id, etc.)"* —
v0.3 shipped without it, and the bootstrap CLI collects a `multi_tenant` flag
that nothing reads.

**Impact.** This is **latent** debt rather than active: nothing is broken today.
But the tenant boundary determines the primary key strategy of every future
table, the shape of every query, and the contract of every handler. Retrofitting
it is one of the most expensive refactors in SaaS engineering, and the cost
scales with how much has been built without it.

**Recommendation.** Make the decision — and write the ADR — **during v1**, even
if implementation waits. Three candidate strategies with trade-offs are laid out
in [V2-01](ROADMAP.md#v2-01--decide-and-implement-multi-tenancy). Until then,
treat every new table as a future migration target and say so in review.

**Roadmap:** [V2-01](ROADMAP.md#v2-01--decide-and-implement-multi-tenancy)

---

## TD-011

### No linter in CI — **RESOLVED 2026-07-27**

**Was.** `make ci` ran only `gofmt -l` and `go vet`. `make lint` invoked
`golangci-lint` **only if it happened to be installed**, and the workflow never
installed it — so linting silently never ran. The target existed and reported
success, which is worse than not having it.

**What it was hiding.** The first real run produced **34 findings**, including
two the compiler could not catch: a dead `newProviderWithKeyfunc` function
documented as a "test seam" that no test used, and an ineffectual `// go:embed`
compiler directive in `landing.go` (a comment that merely looked like one).

**Resolution.** [`.golangci.yml`](../.golangci.yml) added, binary pinned to
`v2.12.2` and installed in CI, `make lint` now **blocking** inside `make ci`.

Enabled green and blocking from day one:
`govet · ineffassign · unused · nilerr · bodyclose · rowserrcheck ·
durationcheck · copyloopvar · misspell`.

Deferred with documented counts and a promotion procedure: `errcheck` (20
findings) and `staticcheck` (13). See
[QUALITY_GATE.md](QUALITY_GATE.md#the-lint-ratchet--read-before-adding-a-linter).

**Why a ratchet rather than enabling everything.** 34 findings would have meant
either a red gate or a large mechanical PR bundled with real work. A gate that
is green and enforced beats one that is comprehensive and bypassed.

**Delivered by:** [V1-02](ROADMAP.md#v1-02--complete-the-ci-gate--delivered-2026-07-27)

---

## TD-012

### Stale and disproportionate comments in `internal/config`

**Priority: Low** · Category: Code

**Description.** Two separate problems in
[internal/config/config.go](../internal/config/config.go):

1. **Stale.** The package doc comment and `LoadConfig`'s comment both document a
   `JWTSecret` field and a `JWT_SECRET` variable. Neither exists — the field was
   removed when Keycloak took over token signing. The comment still says
   *"Default JWT_SECRET 'secret' is for development ONLY"*, describing behaviour
   that cannot occur.
2. **Disproportionate.** Roughly 180 lines of ASCII-banner comments document
   trivial helpers. `getEnv` — a five-line `os.LookupEnv` wrapper — carries a
   30-line comment block with a worked example.

**Impact.** The stale part is actively misleading: a reader may look for
JWT-signing configuration that does not exist. The verbose part dilutes the
codebase's otherwise excellent signal-to-noise ratio, and sets a bad local
precedent.

**Recommendation.** Delete the `JWTSecret` references. Compress the helper
comments to one line each. Keep the substantive documentation on `Config` fields
and `Validate` — that part earns its space.

---

## TD-013

### No graceful shutdown — **RESOLVED 2026-08-09**

**Was.** `Server.Start` called `router.Run(":" + port)` and blocked. No
`http.Server`, no `Shutdown`, no `signal.Notify`, and — less noticed — no
timeouts of any kind, so a client that opened a connection and never sent a
request held a goroutine and a file descriptor indefinitely.

**Impact.** Every deploy, restart and `docker compose down` dropped whatever was
in flight, including requests that had already written to Keycloak and were
killed before answering.

**Resolution.** An explicit `http.Server` with an ordered drain
([`internal/server/lifecycle.go`](../internal/server/lifecycle.go)):

```text
SIGTERM/SIGINT → readiness 503 → 3s → close listener → drain (bounded) → close DB → exit 0
```

Readiness flips **before** the listener closes, which is the part most
implementations skip: a load balancer learns about readiness by polling, so
closing first turns that window into refused connections. `SHUTDOWN_TIMEOUT_SECONDS`
(default 20) bounds the drain; a second signal skips the delay.

Timeouts came with it — `ReadHeaderTimeout` 10s, `ReadTimeout` 30s,
`WriteTimeout` 60s, `IdleTimeout` 120s, `MaxHeaderBytes` 64KB — each derived
from the Slice 8 measurements rather than copied from a template.

**Preventing recurrence.** `lifecycle_test.go` runs a real listener and asserts
the ordering: readiness 503 while an in-flight request is still running, that
request completing with its real response, and a hung handler still exiting
within the bound. `signal_test.go` proves a real `SIGTERM` and `SIGINT` reach
the drain, in a subprocess the test creates. `scripts/m2m-harness.sh` exercises
the same sequence against the actual binary, in CI.

## TD-014

### Unused `RequireAnyRole` middleware

**Priority: Low** · Category: Code

**Description.** [`RequireAnyRole`](../internal/auth/middleware.go) is fully
implemented, documented, and tested — and mounted on zero routes. Verified: the
only non-test reference is its own definition.

**Impact.** Minor. It is dead code that must still be maintained and read, and
it can mislead a reader into thinking multi-role gating is in use somewhere.

**Why it survives the `unused` linter.** `RequireAnyRole` is **exported**, so
`unused` cannot prove nothing calls it — an external consumer might. Its
unexported sibling `newProviderWithKeyfunc` was flagged immediately and deleted
on 2026-07-27. Exported dead code needs a human decision; that is this entry.

**Recommendation.** Either use it (the natural candidate is a future non-admin
privileged tier) or delete it. Keeping it is defensible **if** the doc comment
says explicitly that it is provided for future use — currently it does not.

---

## TD-015

### Three bootstrap feature flags collected but never read

**Priority: Low** · Category: Bootstrap

**Description.** [bootstrap/prompt.go](../internal/bootstrap/prompt.go#L97)
prompts for five features. Only `dev_playground` and `seed_users` are read by
any code. `multi_tenant`, `google_login` and `mfa` are written to
`project.json` and then ignored. (`swagger` is also prompted for and unread,
though Swagger is unconditionally enabled.)

**Impact.** The bootstrap CLI implies capabilities that do not exist. An
operator who answers "yes" to `multi_tenant` gets a config file asserting
multi-tenancy in a system with no tenancy whatsoever. This is a documentation
integrity problem embedded in a tool.

**Recommendation.** Remove the three prompts, or keep them and have the CLI
print `not yet implemented` next to each. Do not leave them silently inert.

Cross-referenced as [KI-007](KNOWN_ISSUES.md#ki-007).

---

## TD-016

### `internal/logging` at 25% coverage

**Priority: Low** · Category: Testing

**Description.** [internal/logging](../internal/logging/) has the lowest
coverage in the codebase at 25.0%. The gap is mostly in `AuditSink`'s
formatting paths.

**Impact.** Bounded but not trivial: this package sits on the audit path, and
`RecordMutation` is the single choke point for the "every mutation emits exactly
one event" invariant. A formatting regression could corrupt the durable trail
without any test failing.

**Recommendation.** Table-driven tests over `AuditSink` output for each of the
14 action types, plus assertions that `RecordMutation` emits exactly once on
both the success and failure paths.

---

## TD-017

### Missing community health files referenced by CONTRIBUTING

**Priority: Low** · Category: Documentation

**Description.** [CONTRIBUTING.md](../CONTRIBUTING.md) referenced four files
that do not exist in the repository:

| Referenced | Reality |
|---|---|
| `CODE_OF_CONDUCT.md` | Absent |
| `.github/ISSUE_TEMPLATE/bug_report.yml` | `.github/ISSUE_TEMPLATE/` does not exist |
| `.github/ISSUE_TEMPLATE/feature_request.yml` | Same |
| "the PR template" | No `PULL_REQUEST_TEMPLATE.md` anywhere |

`.github/` contains only `workflows/ci.yml` and `workflows/codeql.yml`.

**Impact.** Low but real: four dead links in the file a first-time contributor
reads first. The `v0.3.0` changelog claims *"Repo metadata: LICENSE,
CONTRIBUTING.md, SECURITY.md"* were added — which is accurate, but CONTRIBUTING
was written as though the surrounding GitHub scaffolding had landed with it.

**Resolution applied 2026-07-26.** The references were corrected to point at
plain issue URLs and the conduct expectations were inlined, so no link is dead.
The underlying files are still absent.

**Recommendation.** Add the actual files — a Contributor Covenant
`CODE_OF_CONDUCT.md`, two issue-form templates, and a PR template — then restore
the structured references. This is ordinary open-source hygiene and takes under
an hour.

---

## TD-018

### Keycloak token acquisition is duplicated in the connection verifier

**Priority: Low** · Category: Code

**Description.** `connection.KeycloakVerifier` performs its own
`client_credentials` grant rather than reusing
[`identity/keycloak.AdminClient`](../internal/identity/keycloak/admin.go), which
already does this with caching and a 401 retry.

**Why it was done.** `AdminClient` collapses every failure into
`ErrAdminAPIUnavailable`. The verification report exists precisely to separate
*unreachable* from *wrong realm* from *bad credentials* from *insufficient
privileges* — four different operator actions. Reusing the client would have
meant either losing that distinction or widening `AdminClient`'s error surface
for one caller.

**Impact.** Small and bounded: roughly 40 lines of token-request code exist
twice. The risk is drift — a change to how the platform talks to Keycloak's
token endpoint (a proxy, a client-assertion auth method) must be made in both
places.

**Recommendation.** When Slice 4 makes the Identity layer resolve a Workspace's
active Connection, both paths will need a per-connection admin client. Extract a
shared low-level token client then, with an error type rich enough for the
verifier's report. Doing it before that point would be speculative.

---

## TD-019

### No master-key rotation for credentials at rest — **RESOLVED 2026-08-11**

**Priority: ~~Medium~~ Resolved** · Category: Security

**Was.** Provider client secrets were sealed with AES-256-GCM under a single
master key from `SECRETS_MASTER_KEY`. There was no way to rotate it: the sealer
held exactly one key, stamped a hard-coded version 1 on everything, and a row
sealed under any other version simply failed to open. Retiring a key meant
re-entering every stored connection secret by hand, so the standard response to
a suspected compromise — rotate now, re-wrap later — was unavailable.

**Resolution.** A bounded keyring ([internal/secrets/keyring.go](../internal/secrets/keyring.go)):

* `SECRETS_KEYRING=1:<key>,2:<key>` configures every version the process can
  DECRYPT with; `SECRETS_KEY_CURRENT` names the one that ENCRYPTS. Both are
  normalised at config parsing, and the legacy `SECRETS_MASTER_KEY` maps to
  version 1 — the version every existing row already carries — so existing
  installations upgrade without touching their data. Setting both is refused.
* A row is opened with the key its own `secret_key_version` names and no other.
  There is no try-every-key fallback, and `TestKeyring_DoesNotTryOtherKeys`
  holds a key that WOULD open the row under a different number to prove it.
* `secrets rotate` re-seals every persisted credential under the current key,
  one row per transaction under `SELECT … FOR UPDATE`. Idempotent (already-current
  rows are skipped, not re-encrypted), resumable, and safe to run concurrently.
* `secrets status` reports rows per key version and which keys are safe to
  remove — the question that decides whether an old key can be destroyed.
* A missing key version degrades exactly the affected workspaces, never
  readiness. Visible through the boot log, the
  `lightweight_secret_key_version_rows` gauge and
  `lightweight_secret_open_failures_total`.

No schema change was needed: `secret_key_version` and `secret_alg` have been
carried since 000003 precisely for this.

**Not resolved by this.** Master keys still come from the process environment.
Sourcing them from a KMS or an HSM remains future work, and the keyring is the
seam that would take it.

**Evidence.** Real PostgreSQL + real Keycloak:
`TestKeyRotation_ConnectionSurvivesTheWholeLifecycle` takes a connection from
v1, through a mixed keyring, through rotation, to a process holding only v2, and
performs real admin reads and writes at every stage.
`make secrets-check` drives the compiled CLI through the same lifecycle and
scans everything it produced for the keys and secrets it used.

**Related:** [SECRET_KEY_ROTATION.md](SECRET_KEY_ROTATION.md) · [CONNECTIONS.md §3](CONNECTIONS.md#3-secrets-at-rest)

---

## Debt by category

| Category | Open items | Highest priority |
|---|---:|---|
| Architecture | 5 | Medium ([TD-010](#td-010), [TD-022](#td-022)) |
| Code | 4 | Medium ([TD-014](#td-014)) |
| Testing | 3 | Medium ([TD-012](#td-012)) |
| Observability | 2 | Medium ([TD-009](#td-009)) |
| Performance | 1 | Medium ([TD-007](#td-007)) |
| API contract | 1 | Low ([TD-020](#td-020)) |
| Bootstrap | 1 | Low ([TD-015](#td-015)) |
| Documentation | 1 | Low ([TD-017](#td-017)) |
| Security | **0** | — ([TD-019](#td-019) closed 2026-08-11) |

## Maintenance

When an item is paid down, mark it **Resolved** with the date and keep the entry
— the record of *why* the shortcut existed is worth more than a clean list. Add
new items with an ID, a concrete impact statement, and a recommendation
specific enough to act on. Cross-reference [ROADMAP.md](ROADMAP.md) so debt and
planned work stay connected.

---

## TD-020

### `ListUsersResponse` echoes unclamped pagination — **FIXED ON `/v1` 2026-08-09**

**Priority: Low** · Category: Code · **Open on `/admin/*` only**

**Description.** `identity.Service.ListUsers` bounds `max` to [1, 100] and
`first` to >= 0 on its own copy of the query. `/admin/users` echoes the caller's
**raw** values back:

```
GET /admin/users?max=9999   →   {"max": 9999, ...}   # 100 were actually requested
GET /admin/users            →   {"max": 0, ...}      # 20 were actually requested
```

**Impact.** A client paginating on the echoed `max` computes wrong offsets.

**Fixed on `/v1`.** `GET /v1/workspaces/{id}/users` returns the EFFECTIVE
values. The rule was not forked to do it: `identity.ClampListUsersQuery` is
exported and idempotent, so the workspace-scoped handler clamps before calling
and echoes the result, while the service clamps again and reaches the same
answer.

**Still open on `/admin/users`, deliberately.** The console parses that
response, and changing a body it depends on is exactly what Slice 5 promised not
to do. Both behaviours are pinned by tests —
`TestAdminUsersStillEchoesRawPagination` and
`TestHandler_PaginationReportsEffectiveValues` — so the difference is a
recorded decision rather than drift.

**Closes when** the console moves to `/v1` and `/admin/*` is retired.

---

## TD-021

### Keycloak test scaffolding duplicated across packages

**Priority: Low** · Category: Testing

**Description.** `kcAdmin` — the admin client that creates disposable realms,
service-account clients and role grants for live-Keycloak tests — now exists
twice, in `internal/connection/verifier_keycloak_integration_test.go` and
`internal/identityruntime/isolation_integration_test.go`, at roughly 150 lines
each. The PostgreSQL `newTestSchema` helper is duplicated a fourth time in the
same file.

**Impact.** Bounded but real: a Keycloak version bump that changes an admin
endpoint has to be fixed in every copy, and copies drift.

**Why it accumulated.** Go test helpers cannot cross package boundaries without
a non-test package, and adding one would put test-only code in the production
tree and in the coverage denominator. The repository has consistently chosen
duplication over that.

**Recommendation.** At the third copy, add `internal/testsupport` with a build
tag so it compiles only under `-tags=integration`. Not before — two copies do
not yet justify the indirection.

---

## TD-022

### `/admin/*` and `/v1` are two identity code paths

**Priority: Medium** · Category: Architecture

**Description.** Identity operations now reach Keycloak two ways: `/admin/*`
through a process-level provider built from `KEYCLOAK_*`, and
`/v1/workspaces/{id}/*` through a provider resolved per request from the
workspace's active Connection. Both wrap the same `identity.Service`, so the
business rules are shared, but the routing, the error envelope and the
configuration source are not.

**Impact.** Every identity endpoint added from here has to choose a surface, and
the frontend still talks exclusively to the old one. The two error envelopes in
particular are a client-visible inconsistency: `/admin/*` returns its legacy
shape, `/v1` returns `{error: {code, message, request_id}}`.

**This is deliberate, not accidental** — see
[WORKSPACE_IDENTITY_RUNTIME.md §6](WORKSPACE_IDENTITY_RUNTIME.md#6-bootstrap-why-admin-was-left-alone)
for why the migration was deferred rather than attempted. It is recorded here
because a deliberate shortcut with an ongoing cost is exactly what this document
is for.

**Recommendation.** Migrate `/admin/*` onto the runtime behind a bootstrap
Workspace, with a stated precedence rule between env configuration and the
persisted Connection, and move the frontend in the same slice. Until then, do
not add an endpoint to both surfaces.

---

## TD-023

### Last-admin guard assumed every realm has an `admin` role — **RESOLVED 2026-08-09**

**Was.** `Service.assertNotLastAdmin` called `ListUsersByRole("admin")` and
treated **any** error as "cannot enumerate, fail safe":

```go
admins, err := s.provider.ListUsersByRole(ctx, adminRoleName)
if err != nil {
    return err   // including Keycloak's 404 for a role that does not exist
}
```

**Impact.** `admin` is a LIGHTWEIGHT bootstrap convention, not something every
Keycloak realm has. In a connected realm without that role, **`DeleteUser` and
disable-user failed unconditionally** — and the caller received a bare 404
pointing at the user they asked about rather than at a role they never
mentioned.

Invisible on `/admin/*`, whose realm is provisioned with the role by the
bootstrap. It became reachable the moment a workspace could point at a realm
this product did not create, which is the entire premise of the Connection
model.

**Found by** the live multi-realm integration suite, not by review — the unit
tests all staged an admin set, so none of them exercised the realm-has-no-admin
-role path.

**Resolution.** `ErrNotFound` from the role lookup now means "no admin role, so
no last admin to protect" and permits the operation. Every other error still
fails safe, because those genuinely leave the guard unable to decide.
Regression tests cover both branches.

---

## TD-024

### `access_mode` cannot detect a read-only service account — **RESOLVED 2026-08-09**

**Priority: ~~Medium~~ Resolved** · Category: Architecture

**Was.** Slice 3's verification probe set `access_mode` to `full` or `limited`
based on two admin **reads** (read the realm, list users). Slice 5 used it to
refuse writes centrally. The signal was weaker than it looked, in both
directions:

- `limited` meant the read probes were refused. Such a connection is
  under-privileged, but it may not be able to read either — it was not a
  "reads work, writes do not" marker.
- A genuinely read-only service account (`view-users` granted, `manage-users`
  not) passed both read probes and verified as **`full`**. The configuration
  that most deserves the name was the one `access_mode` could not detect.

**Impact.** The API told clients that writes were supported when it had only
proven reads. A UI could not use `access_mode == full` to enable mutation
controls, which is exactly what Slice 6's console needed to do.

**Resolution.** A fourth value, `read_only`, and a redefinition: **`full` is
claimed only when write capability has been positively proven.** Everything the
probe cannot prove degrades downward, never upward.

| Value | Meaning | `can_write` |
|---|---|:--:|
| `full` | reads work **and** a write grant was proven | yes |
| `read_only` | reads work; the provider reported **no** write grant | **no** |
| `limited` | the admin reads themselves were refused | **no** |
| `unknown` | the provider gave no usable evidence either way | yes |

The recommendation above — create and delete a throwaway realm role — was
**rejected**. It mutates a realm during an operation advertised as a read-only
probe, which is a promise an operator relies on when pressing Verify against
production.

What shipped instead costs **no extra request and mutates nothing**: Keycloak
stamps a service account's `realm-management` roles into the
`client_credentials` access token the probe has already obtained, so the
provider has already stated what it will allow. `realm-admin` or `manage-users`
proves write capability. `manage-realm` alone does not — it permits realm-role
writes while leaving every user mutation refused, which would reproduce the same
over-claim one endpoint over.

The token's signature is not verified, and that is correct rather than merely
tolerable: this is not authentication. The probe is reading its own credential's
grant sheet, minted seconds earlier by the provider it just authenticated to. An
unparseable or unexpected token degrades to `unknown`, so the worst a bad value
can do is make the API claim *less* capability than it has.

**Residual limitation, deliberately accepted.** `unknown` still permits the
write attempt. It means the client's scope does not publish its
`realm-management` roles — the Keycloak default publishes them, but it is
configurable. Refusing writes on absent evidence would break working
installations for a signal that was never promised; the authoritative answer
still arrives from Keycloak as `provider_forbidden`. The invariant that matters
holds either way: **the API never reports write capability it has only inferred
from reads.**

**Verified against a live Keycloak.**
`TestLiveVerify_AccessModeMatchesRealWriteOutcome` builds a read-write and a
genuinely read-only administrative client in a disposable realm, asks Keycloak to
perform a real user creation with each, and asserts the recorded verdict
predicted the outcome (201 vs 403). `TestLiveVerify_ReadOnlyAdminClient` pins the
label itself. Schema change: migration `000004`.

---

## TD-025

### `POST /admin/users/password` still bypasses the service

**Priority: Low** · Category: Testing

**Description.** The legacy direct-provisioning route is handled by
`server.SMTPHandler`, which calls the concrete `identitykc.Provider` directly —
past both `identity.IdentityProvider` and `identity.Service` — with its
validation inline in the handler.

**Partly addressed.** Slice 5 promoted the capability onto the shared seam:
`IdentityProvider.CreateUser` and `Service.CreateUser` now exist, the Keycloak
implementation is a thin adapter onto the same provisioning sequence (one
implementation, not two), and the validation has tests for the first time.
`POST /v1/workspaces/{id}/users` uses that path.

**What remains.** The legacy route still calls the concrete provider, so its
validation is a second copy of rules that now live in the service. Left alone
deliberately: rewiring it would change `/admin` response bodies, which Slice 5
must not do.

**Recommendation.** Point the legacy handler at `Service.CreateUser` in the
slice that retires `/admin/*`, and delete the inline validation with it.

---

## TD-026

### The `/v1` edge IP limit is below the per-credential limit — **RESOLVED 2026-08-09**

**Priority: Medium** · Category: Architecture

**Was.** Slice 7 added a per-credential bucket at 20 req/s, burst 40, which is
the rate a project credential is nominally allowed. It sat behind the
pre-existing per-IP limiter on `/v1` — 10 req/s, burst 20, tuned in Slice 2 for
a human admin's click-rate, before any machine consumer existed.

The edge limiter runs before authentication and therefore could not distinguish
a backend from an anonymous flood. The effective allowance for a project was
`min(10/s per IP, 20/s per credential)`, so the per-credential limit was
unreachable and the number published in the docs was not the one enforced.

**Impact.** A backend calling from one address was throttled at 10 req/s
regardless of its credential. Measured with `scripts/m2m-harness.sh --bench`
against a real installation: before the fix a credential was admitted **20
requests** before its first 429 — exactly the edge burst, never its own. Worse,
those refusals were charged to the shared IP bucket, so a single credential's
traffic degraded everything else from the same host: in the pre-fix measurement
run, thirteen unrelated error-matrix checks returned `rate_limit_exceeded`
instead of the code they were testing.

**Resolution.** The edge limiter now **reserves** a token from the IP bucket and
**releases** it once the request turns out to have authenticated as a project
credential. The two limiters meter different traffic — unknown callers at the
edge, known machines per credential — and were only in competition because the
edge bucket was charged for both.

No number changed. Anonymous-flood protection and operator throughput are
bit-for-bit what they were; the credential's own bucket is simply no longer
shadowed by a smaller one. Same measurement after the fix: **44 requests**
admitted, refused by `rate_limit_exceeded` from the credential limiter, with the
whole error matrix answering its own codes again.

The release deliberately covers requests the credential limiter then refuses.
Charging those to the IP would reintroduce the coupling in a subtler form: one
runaway key would throttle its siblings on the same host.

Two smaller fixes travelled with it, both required for the same contract to be
usable: the `/v1` edge 429 now uses the standard envelope with a `request_id`
(it used to answer the legacy `{"error":"…"}` body, the one `/v1` response an
SDK could not decode), and `RateLimit-Limit` now advertises the sustained rate
rather than the burst, with `RateLimit-Remaining` emitted on success instead of
only as a constant `0` on the refusal.

**Preventing recurrence.** `internal/server/ratelimit_v1_test.go` derives its
expectations from the configured settings rather than from constants, so
retuning cannot make it pass vacuously. `TestV1_CredentialReachesItsOwnLimitNotTheEdgeCeiling`
fails if the edge ever becomes the binding constraint again, and
`TestV1_OverLimitCredentialDoesNotDrainTheEdgeBucket` fails if the release is
narrowed to admitted requests only. Both were confirmed to fail against the
pre-fix behaviour before being committed. `scripts/m2m-harness.sh` checks the
same property from outside, over HTTP.

---

## TD-027

### Rate-limit buckets are per process

**Priority: Low** · Category: Architecture

**Description.** Both limiters keep their token buckets in process memory. Two
replicas therefore permit twice the configured rate, and a rolling deploy resets
every bucket.

**Impact.** None for the single-process self-hosted deployment this targets, and
documented in [PROJECTS.md §10](PROJECTS.md#10-rate-limiting) rather than
implied. It becomes real the first time someone runs more than one replica.

**Why it was not solved here.** A shared store is a new runtime dependency
(Redis or equivalent), which the slice explicitly excluded, and adding one
preemptively for a deployment shape nobody runs yet would be the larger mistake.

**Recommendation.** Revisit when horizontal scaling is actually on the table.
The bucket key is already a plain string (`credentialID` or IP), so the
substitution is a `rateLimiter` implementation swap rather than a redesign.

---

## TD-028

### A credential over its limit still buys one lookup per request

**Priority: Low** · Category: Architecture

**Description.** The [TD-026](#td-026) fix releases the edge token for any
request that authenticated as a project credential, including the ones the
credential limiter then refuses. Those refusals are therefore not charged to the
per-IP bucket, and nothing above the credential bucket bounds them: a backend
retrying in a tight loop can drive one indexed `SELECT` plus one SHA-256 per
request, at whatever rate its network allows.

**Impact.** Small, and bounded by three things that do not apply to the
anonymous case. The work is one indexed row fetch — credential hashing is
SHA-256 by design, not a memory-hard KDF, for exactly this reason
([token.go](../internal/project/token.go)) — the caller is a known, attributable
principal, and an operator can stop it in one request by revoking the key.

**Why it was not solved here.** The alternative is to charge those refusals to
the IP bucket, which reintroduces [TD-026](#td-026) in a subtler form: one
runaway key would throttle its siblings on the same host, which is the coupling
the per-credential bucket exists to remove. The other alternative is a
short-lived memo of "this credential is currently over its limit", keyed by the
token's lookup segment so the refusal lands before the database. That is a real
option and it is small — the key space is bounded by real credentials, and a
memo that only ever REFUSES cannot make a revoked key valid — but it is a second
mechanism added for a load nobody has produced.

**Recommendation.** Add the negative memo if a runaway backend is ever observed,
or if the credential limit is configured high enough that the refusal path
becomes the hot one. Not before. If it is added, it must be strictly
refuse-only: a positive cache of authenticated credentials would break immediate
revocation, which is a hard requirement.

---

## TD-029

### `invalid_request` does not name the field it rejected — **RESOLVED 2026-08-09**

**Priority: Medium** · Category: API contract

**Was.** Every validation failure on the workspace-identity surface collapsed
into one code with one message: `invalid_request` / "Request is invalid". The
service knew exactly what was wrong — "email is malformed",
"temporary_password is required" — and none of it reached the client.

Found by `cmd/lwprobe`, which is the value of a consumer restricted to the
public contract: every test inside the module already knew which field it had
omitted.

**Impact.** The error an SDK's users hit most often and the only one they could
not act on. The first thing a new integration does is create a user, and the
first thing it got was an unactionable 400 — with the OpenAPI document not
marking the field required either, so the spec did not fill the gap.

**Resolution.** An optional `field` on the error envelope:

```json
{"error":{"code":"invalid_request","message":"Request is invalid",
          "field":"temporary_password","request_id":"…"}}
```

- `omitempty`, so an error that is not about a field carries **no key at all** —
  a client written before this decodes every response exactly as it did, and
  `field == ""` never has to mean two things.
- The name travels as DATA, in `identity.FieldError`, not parsed out of prose
  that will be reworded.
- Never derived from input. Every construction site passes a literal, and the
  boundary drops anything that is not shaped like one of our field names.

`temporary_password`, `email`, `name` and `roles` are now also marked
`required` in the OpenAPI document, which was the other half of the same
finding.

**Preventing recurrence.** `TestErrors_FieldNamesMatchTheRequestDTOs` reads the
service source and checks every field name it can produce against the actual
JSON tags of the request DTOs — a name that matches nothing the client sent is
worse than no name. `openapi_required_test.go` verifies each required field from
both sides: the runtime is made to reject it, and the document is checked for
the annotation, so neither can drift alone.

## TD-030

### The integration suite cannot be run with `-race` at default parallelism — **RESOLVED 2026-08-11**

**Priority: ~~Low~~ Resolved** · Category: Testing

**Was.** Under `-race` the integration packages exhausted PostgreSQL's
`max_connections`, and the failure did not look like what it was:
`TestIsolation_ConcurrentRequestsDoNotCrossContaminate` reported
`resolve alpha: internal_error` from a resolver that was working perfectly.
`make test-race-integration` passed `-p 1` to serialise the packages, which was
believed to be sufficient.

**It was not.** Serialising the packages does nothing about a package that
exhausts the pool on its own, and `internal/identityruntime` did:
its `openGorm` was the only one of the five integration fixtures that never
CLOSED its pool, so three pools per test across nineteen tests kept their idle
connections for the lifetime of the test BINARY. The concurrency test then asked
for a burst of eighty and got `too many clients already`.

**Resolution.** `internal/identityruntime`'s fixture now closes its pool in
`t.Cleanup`, matching the connection, workspace, project and auditlog suites,
and bounds it to sixteen connections so the eighty-goroutine burst exercises
real contention without demanding eighty backends. `-p 1` stays: it is still
right for packages competing over one database, and the comment at the Makefile
target now says what it does and does not buy.

**Recommendation retained.** The fixture is still duplicated across five
packages — [TD-021](#td-021). Consolidating it would have caught this by
construction, and remains worth doing.

---

## TD-031

### The admin console has no end-to-end coverage

**~~Priority: Medium~~ · Resolved 2026-08-10** · Category: Testing

**Description.** [TD-003](#td-003) closed the API half of end-to-end coverage:
CI now boots the real stack and drives the product through `cmd/lwprobe`. The
console SPA is not in that path. It is covered by `node --test` suites that
exercise its modules against fakes, and by nothing that opens a browser,
completes a PKCE login and clicks through a workspace.

**Impact.** The console is how an operator does everything the product does not
expose to a machine — creating workspaces, wiring connections, minting and
revoking credentials. A regression in the login flow, the workspace selector or
the credential modal ships green.

This is also the residue of [KI-001](KNOWN_ISSUES.md#ki-001): the defect that
motivated the whole e2e effort presented as "an authenticated admin renders as
not signed in", which is precisely a console symptom.

**Why it was not solved here.** A browser driver (Playwright) is a new toolchain,
a new CI image and a class of flakiness this repository has so far avoided
entirely. Adding it in the same slice as the API e2e job would have made both
harder to trust.

**Recommendation.** Playwright against the compose stack, covering the smallest
set that would have caught KI-001: login, workspace list, and one credential
lifecycle. Reuse `scripts/m2m-harness.sh`'s realm setup rather than building a
third fixture.

**Resolved 2026-08-10 (Slice 11).** The recommendation was followed, including
the fixture-reuse part: `scripts/lib/keycloak-fixture.sh` is now sourced by both
`scripts/m2m-harness.sh` and the new `scripts/browser-e2e.sh`, so there are two
harnesses and one definition of a fixture realm.

CI job `browser-e2e` runs 27 tests in a real Chromium against a real
PostgreSQL, a real Keycloak 26 and a real LIGHTWEIGHT process, with readiness
awaited before the browser opens and no mocked boundary anywhere:

- a real Authorization Code + PKCE login through Keycloak's own form. No token
  is injected and no callback is constructed — the console's own boot sequence
  starts the login, because that is the flow a mock cannot prove;
- the full operator journey by clicking: workspace → connection → verify →
  activate → project → credential → the one-time secret;
- the browser-minted credential used from outside over plain HTTP, its mutation
  appearing in the console's Audit view attributed to the project, revocation
  through the UI, and the machine refused on its next request;
- workspace-state and multi-realm isolation across two live realms;
- unexpected page errors and `console.error` calls fail the run.

Documented in [testing/BROWSER_E2E.md](testing/BROWSER_E2E.md).

**What it found immediately**, neither visible to any prior gate:
[KI-019](KNOWN_ISSUES.md#ki-019) (OAuth authorization codes written to the
access log, caught by the artifact scanner) and
[KI-020](KNOWN_ISSUES.md#ki-020) (the Workspace Audit view threw on any
workspace with events, so the durable trail shipped in Slice 10 was unreadable
in the console). Both are fixed, each with a regression guard at the lowest
level the rule lives at rather than only in the browser.

**What this did NOT close, and what did.** [KI-018](KNOWN_ISSUES.md#ki-018)
stayed open here: the suite proved the operator journey works without
enumerating every way it should be refused. Slice 14 closed it with a
three-layer negative matrix and a single-admin realm fixture — see
[security/AUTHORIZATION_MATRIX.md](security/AUTHORIZATION_MATRIX.md). ([TD-030](#td-030), the other item left open here, was closed in
Slice 12.)

---

## TD-032

### `/metrics` has no scrape-side integration proof

**Priority: Low** · Category: Observability

**Description.** The exposition format is hand-written and covered by tests that
parse it back, assert cumulative bucket semantics, reject duplicate series and
check label escaping. Nothing has ever pointed a real Prometheus at it.

**Impact.** Small and bounded. The failure mode of a malformed exposition is a
scraper silently dropping series — the dashboard is empty and nothing errors —
which is exactly the failure the parsing tests were written to catch. What they
cannot catch is a semantic disagreement a parser tolerates: a `_count` that
disagrees with the `+Inf` bucket, say, which Prometheus accepts and
`histogram_quantile` then answers wrongly about.

**Why it was not solved here.** Standing up Prometheus in CI to scrape one
endpoint is a service container, a config file and a query language, for a
surface that is four metric families and off by default.

**Recommendation.** Either add `promtool check metrics` to CI — it is a single
static binary and reads the format on stdin, which would close most of this for
almost nothing — or adopt `prometheus/client_golang` if the metric set ever
grows past what a person can verify by reading. The dependency trade is recorded
in [`internal/metrics/metrics.go`](../internal/metrics/metrics.go).

---

## TD-033

### Control-plane mutations are not transactional with their audit write — **RESOLVED**

**Priority: ~~Low~~** · Category: Architecture · **Resolved 2026-08-14 (Slice 15)**

**What it said.** Workspace, connection, project and credential mutations write
to the same PostgreSQL as `audit_events`, so a single transaction was
theoretically available and would make those events exactly complete. They were
two separate statements instead — the service committed, and the HTTP handler
emitted the audit event afterwards. In the window between them (pool exhaustion,
or the process dying) a control-plane change happened and its audit row did not.

**What closed it.** All fourteen control-plane mutations now commit their domain
rows and their audit row in ONE transaction, and a failed audit write rolls the
mutation back.

The shape is the one this entry recommended: the service accepts an
`audit.Event` alongside its input and persists both inside one transaction, with
the handler reduced to constructing the event. It was done for all four domains
rather than one, because the acceptance matrix needed workspace, project,
credential AND connection activation anyway — partial delivery was not really
available.

```
BEGIN
    domain mutation
    audit insert
COMMIT          ← or ROLLBACK BOTH
```

**The pieces.**

- [`internal/database/tx.go`](../internal/database/tx.go) — `Runner`/`Tx`. `Tx`
  is an alias for `*gorm.DB` rather than a new `DBTX` interface, because every
  repository is written against GORM's fluent API and none of it goes through
  `ExecContext`; the "either the pool or a transaction" type this pattern
  reaches for already exists.
- `Repository.WithTx` on each domain, and `Store.WithTx` on the audit store. One
  SQL implementation per statement, bound to a different handle — not a parallel
  set of `…Tx` methods that could drift.
- `Recorder.RecordTx` returns the error that `Record` absorbs. That difference
  is the whole of this item in one signature: an audit row the service cannot
  write is a mutation it must not commit.
- `audit.Event.PersistedInTransaction` lets one emission path serve both, so
  every mutation still ends with a single `audit.Record` call and the durable
  recorder skips the row it already wrote.

**Evidence.**
[`internal/auditlog/atomicity_integration_test.go`](../internal/auditlog/atomicity_integration_test.go)
— 17 cases against real PostgreSQL. Each rollback case asserts three things in
sequence: the domain row was **visible inside the transaction** when the audit
write was refused, the write then failed, and neither row exists afterwards.
Without the middle observation, "the row is absent" would be equally consistent
with a write that never ran.

Connection activation is the case worth naming: it retires the incumbent AND
promotes the successor, so a failed audit write must undo two row updates. The
test asserts the workspace still has exactly one active connection, and that it
is the original one.

`scripts/audit-mutation-check.sh` breaks the guarantee nine ways — including
moving the audit write after the commit, which is precisely the code this entry
described — and requires the suite to go red each time. All nine are caught,
against a verified-green baseline.

**What it does NOT close.** Provider mutations, which cannot be transactional
with a PostgreSQL write under any design short of an outbox. That is
[TD-038](#td-038), recorded separately rather than left inside a resolved item.

---

## TD-034

### The `/admin/*` audit surface and the workspace trail are two vocabularies

**Priority: Low** · Category: API contract

**Description.** There are now two audit read surfaces:
`GET /admin/audit-events` (the in-process ring, process-level, volatile, no
workspace) and `GET /v1/workspaces/{id}/audit` (durable, workspace-scoped). They
answer different questions and both should exist, but their RESPONSE SHAPES are
unrelated: the legacy one returns `{events, count, capacity, dropped}` with an
`action` field and no id, the new one returns `{items, pagination}` with `event`
and `evt_` ids.

**Impact.** Small today: the console renders each with its own view, and the two
are labelled clearly enough that an operator is unlikely to confuse them. It
becomes real when an SDK is written — a client library would expose two audit
types for one concept, and the naming (`action` versus `event`) invites the
wrong one.

**Why it was not solved here.** `/admin/*` is required to stay byte-compatible,
so aligning the shapes means changing the surface that must not change. The
alternative — retiring `/admin/audit-events` — is a console change and a
deprecation, which belongs with whatever finally migrates `/admin/*`
([TD-022](#td-022)).

**Recommendation.** Fold it into TD-022 rather than treating it separately. When
`/admin/*` is retired, the ring's endpoint goes with it and the durable trail
becomes the only audit surface.

---

## TD-035

### No release automation understands the nested SDK module's tag prefix — **RESOLVED**

**Priority: ~~Medium~~** · Category: Release · **Resolved 2026-08-15 (Slice 16)**

**What it said.** `sdk/go` is a separate Go module. Go resolves a nested module
only from a tag carrying its directory as a prefix — `sdk/go/v0.1.0`, not
`v0.1.0` — and nothing in the repository knew to create the other kind. An
external backend could not `go get` the SDK at all, every release was a manual
step that could silently be skipped, and nothing enforced that an `sdk/go/vX` tag
pointed at a commit whose SDK actually passed its gates: a broken release was
exactly as easy to publish as a good one.

**What was done.**

*The tag format was proven rather than assumed.* A snapshot of the working tree
was committed into a throwaway repository, published through a local bare remote
one tag shape at a time, and consumed by a module outside this repository. Only
`sdk/go/v0.1.0` makes `go get …/sdk/go@v0.1.0` resolve; `v0.1.0`, `sdk/v0.1.0`,
`go/v0.1.0` and `sdk/go/0.1.0` each fail, and Go's own error names the tag it
looked for. The consumer builds with **no `replace` directive** and a `go.sum`
containing the SDK and nothing else. `scripts/sdk-release-simulation.sh` is that
experiment, repeatable.

*A documentation bug was found and fixed.* Both READMEs published
`go get …/sdk/go@sdk/go/v0.1.0` — the **tag** as the version query. It resolves,
but only because Go also accepts a bare revision name, and it is not what the
proxy or pkg.go.dev show. The canonical form is `@v0.1.0`, and the release gate
now rejects the other in the docs.

*One authoritative source.* `scripts/lib/sdk-release.sh` holds the single literal
`sdk/go` and DERIVES the module path, tag prefix and install command from the two
`go.mod` files, failing if they disagree. The four copies that could drift are
gone.

*The gates.* `scripts/check-sdk-release.sh` validates tag prefix, SemVer, the
`/vN` major rule, that the tagged **commit** actually contains `sdk/go/go.mod`
declaring the right module path, tidiness, zero dependencies, the exported-API
snapshot, vet, tests, race, coverage, a real Go 1.23 toolchain, and the install
command in the docs. `.github/workflows/sdk-release.yml` runs it on `sdk/go/v*`
tags under `permissions: contents: read`, and additionally requires the tagged
commit to be an ancestor of `main` so a narrow release gate cannot bless what the
full branch CI never saw.

*Evidence it works.* `scripts/sdk-release-mutation-check.sh` breaks 19 release
properties in turn — root-style tag, deleted test step, renamed module path,
added dependency, removed exported method, changed error-code value, wrong
`go get` path, invented version, a tag whose commit has no SDK, a tag validated
from a different checkout, invalid SemVer, unsuffixed v2, a too-new Go symbol, an
untidy `go.mod` — and every one is caught, each checked against a pristine copy
first.

**Why this is closed rather than partially closed.** The only remaining step is
`git push origin sdk/go/v0.1.0`, which is an operator decision and not a missing
capability. What genuinely cannot be proven before that push — `proxy.golang.org`
resolution, the `sum.golang.org` entry, pkg.go.dev rendering — is infrastructure
outside this repository; `scripts/first-publish-smoke.sh` tests exactly those
afterwards and refuses to run against a version that does not exist yet. The
boundary is stated in [SDK_GO.md](SDK_GO.md#what-is-proven-and-what-waits-for-the-first-tag)
rather than papered over.

**Found on the way.** The zero-dependency gate had a blind spot: `go list -m all`
exits non-zero and prints nothing when a `require` cannot be resolved, so a
`go.mod` declaring an undownloadable dependency read as "no dependencies" —
the offline case, which is the CI cache-miss case. It now also reads the
declaration with `go mod edit -json`.

## TD-036

### No idempotency keys, so a client cannot safely retry a mutation

**Priority: Medium** · Category: API contract

**Description.** `/v1` mutations carry no idempotency key. A client whose request
succeeded server-side but whose response was lost — a dropped connection, a
proxy timeout, a cancelled context — has no way to find out which happened, and
no way to repeat the call safely.

**Impact.** It surfaced while writing the Go SDK, which is why it is recorded
now. The SDK's answer is to never retry anything, which is correct but is a
limitation rather than a design: a backend that wants at-least-once delivery has
to build its own reconciliation (search for the user by email before creating
it, and accept the race), and every consumer will build a slightly different one.

`POST .../users` and `POST .../invitations` are the sharp cases: repeating either
creates a duplicate, or fails with `conflict` in a way indistinguishable from a
genuine collision with someone else's write.

**Why it was not solved here.** Solving it client-side means guessing — deriving
a key from the request body, hoping the server treats it as one — which produces
a client that appears to be safe and is not. That is worse than the honest
limitation. It is a server contract change: an `Idempotency-Key` header, a
storage table keyed by (credential, key), and a replay window.

**Recommendation.** Take it with whatever slice next touches the write path.
Until then the SDK's no-retry policy stands, and
[`sdk/go/README.md`](../sdk/go/README.md) states plainly that retry policy is the
caller's.

---

## TD-037

### Authenticated authorization failures reach no durable trail

**Priority: Low** · Category: Observability · Recorded 2026-08-13 (Slice 14) ·
**Re-analysed 2026-08-14 (Slice 15): stays open, Model A confirmed**

**Description.** A `403 insufficient_scope`, `403 workspace_mismatch` or
`403 operator_only` reaches the security event channel with the project and
credential id, and the process log. It does **not** produce a row in the
workspace's durable audit trail. An operator asking "has anything been probing
this workspace this month?" can answer it from shipped logs, not from the
product.

**Slice 15 re-analysed this properly rather than deferring it again**, because
both items are about durable security semantics and the transactional machinery
now exists. Three models were considered.

| | |
|---|---|
| **A — logs and security telemetry only** | **current, and confirmed** |
| B — selected durable refusals in the existing trail | rejected, see below |
| C — a dedicated security-event class with its own store and retention | the eventual answer, not now |

**Why B is rejected, with the arithmetic.** The durable trail has ONE retention
policy: `AUDIT_RETENTION_DAYS`, 90 by default, applied by age across every
workspace and every event type. Refusals would share it.

A single credential is rate-limited at **20 req/s sustained**. A misconfigured
backend retrying a 403 in a tight loop — not an attacker, an ordinary
deployment mistake — produces:

```
20/s × 86,400 s   = 1.73 M rows/day
× 90-day retention = 155 M rows, from ONE credential
```

Domain mutations, by contrast, are operator-paced: hundreds per day for a busy
installation. So one misconfigured backend would outweigh the real history by
roughly **four orders of magnitude**, in the same table, with no way to evict it
early — retention is by age, not by size or class. The audit UI has no filter
that would separate them, and the composite index that makes the listing an
index-only walk would be walking somebody's retry loop.

That is not a tuning problem. It is the wrong table.

**Why C is right and is not being built now.** A separate class needs its own
retention (days, not months), its own volume controls, its own read surface, and
a decision about whether it is workspace-scoped at all. That is a slice, not a
refinement, and doing it badly — a row per 403 in the existing table — would
degrade the trail this product's audit story now rests on.

**401s must NOT become durable rows under any model.** They are
attacker-controlled in volume, and they have no trustworthy workspace to
attribute to: the credential was never resolved, so the only workspace available
is the one in the attacker-supplied path. Writing rows keyed by that would let
an unauthenticated caller write into any workspace's history. This is a security
rule, not a volume argument, and it holds even if the volume problem is solved.

**What would change the decision.** A real incident where log-only refusal
telemetry was insufficient, or an operator asking for it. Both are cheap to wait
for and neither has happened.

**Recommendation.** Keep open. Take it with whatever slice builds the security
event class, and size that slice around retention and volume rather than around
the event vocabulary — the vocabulary is the easy part.

---

## TD-038

### Provider mutations cannot be atomic with their audit write

**Priority: Low** · Category: Architecture · Recorded 2026-08-14 (Slice 15)

**Description.** [TD-033](#td-033) closed the control-plane half: a workspace,
connection, project or credential mutation and its audit row now commit
together. The other half cannot be closed the same way. A user created in
Keycloak, a session revoked, a role granted — fifteen mutating routes — happen
in a realm this PostgreSQL cannot roll back, so their audit row is written
afterwards and a failure leaves the mutation without its record.

**Impact.** Narrower than it sounds, and real.

Narrower, because the two writes still share a database and a connection pool:
the common failure (the database is unreachable) means the audit write fails on
a request whose provider call already succeeded, which is the window — but the
same outage would have failed the control-plane mutations outright. The failure
is also loud: an ERROR log naming the event, workspace and request id, plus
`lightweight_audit_persist_failures_total{event}`.

Real, because the events in this half are the ones an investigation most often
starts from. "Who deleted this user" is a provider mutation.

**Why it is not solved by failing the response.** That was considered and is
actively harmful here: the Keycloak user has been created, so telling the caller
it failed invites a retry that either creates a second user or answers 409 for a
user the caller believes does not exist. Corrupting the caller's model of what
exists to save a log row is a bad trade. See
[AUDIT.md §6.2](AUDIT.md#62-provider--best-effort-loudly).

**What would solve it.** An outbox, or a reconciliation pass. Both are real
work — a table, a worker, a retry policy, a poison-message story — and both add
eventual consistency to a system that currently has none. Worth building once
the failure has been observed rather than imagined; the metric exists so that
observation is possible.

**Recommendation.** Leave open and watch the metric. If
`lightweight_audit_persist_failures_total` is ever non-zero in a real
deployment, that is the signal to size the work.
