# Quality Gate

**Last updated:** 2026-07-27 · Companion to [CONTRIBUTION_CHECKLIST.md](CONTRIBUTION_CHECKLIST.md) and [PROJECT_STATUS.md](PROJECT_STATUS.md)

The criteria every Pull Request must satisfy. **Automated wherever possible** —
a human checklist item that a machine could evaluate is a checklist item that
will eventually be skipped.

**Run everything a machine can check, locally, with one command:**

```bash
make ci          # what CI enforces on every PR  (~40s)
make ci-full     # + coverage floor + frontend tests  (~70s)
```

---

## What is automated vs. what needs judgement

| Criterion | Enforced by | Blocking |
|---|---|---|
| Code compiles | `make build` → CI job `gate` | ✅ automatic |
| Formatting (`gofmt`) | `make fmt-check` + pre-commit hook | ✅ automatic |
| `go vet` clean | `make vet` + pre-commit hook | ✅ automatic |
| Lint clean (9 linters) | `make lint` → `.golangci.yml` | ✅ automatic |
| Tests pass | `make test` | ✅ automatic |
| Coverage ≥ floor | `make coverage-gate` → CI job `coverage` | ✅ automatic |
| Admin console tests pass | CI job `frontend` | ✅ automatic |
| Integration tests pass | CI job `integration` | ✅ automatic |
| OpenAPI matches annotations | `make swagger-check` | ✅ automatic |
| No broken doc links/anchors | `make check-links` | ✅ automatic |
| Documented numbers match code | `make check-metrics` | ✅ automatic |
| No `.env` committed | pre-commit hook | ✅ automatic |
| Documentation updated | — | 👤 **reviewer judgement** |
| Security checklist | — | 👤 **reviewer judgement** |
| Performance checklist | — | 👤 **reviewer judgement** |
| Migration rollback validated | — | 👤 **reviewer judgement** |

Everything marked 👤 is where review effort should actually go. The rest is
already covered — do not spend review cycles re-checking it by hand.

---

## Build

| # | Criterion | Command | Notes |
|---|---|---|---|
| B1 | Project compiles | `make build` | Binary lands in `bin/api` |
| B2 | `make ci` passes | `make ci` | Full local mirror of CI job `gate` |
| B3 | `go vet` clean | `make vet` | Also runs in the pre-commit hook |
| B4 | Lint clean | `make lint` | Config: [`.golangci.yml`](../.golangci.yml) |
| B5 | Formatting clean | `make fmt-check` | `make fmt` fixes it |

### The lint ratchet — read before adding a linter

Linting **never ran** in this repository before 2026-07-26: the CI workflow
did not install the binary, so `make lint` silently fell back to `fmt-check`
(recorded as [TD-011](TECH_DEBT.md#td-011)). Enabling everything at once would
have produced 34 findings and a red gate that contributors learn to route
around.

So the enabled set is exactly **the set that is green against the current
tree**, and it is blocking from day one:

```
govet · ineffassign · unused · nilerr · bodyclose
rowserrcheck · durationcheck · copyloopvar · misspell
```

Two linters are **deferred with a documented count**, to be promoted one PR at
a time:

| Linter | Findings | Where | Target |
|---|---:|---|---|
| `errcheck` | 20 | `bootstrap/prompt.go` 6 · `smtp_handler.go` 3 · `identity/keycloak/admin.go` 3 · tests 8 | v0.4 |
| `staticcheck` | 13 | 8× S1016 (convertible struct literals) · 3× QF1012 · 1× SA9009 | v0.4 |

**To promote a deferred linter:** fix every finding, move it into `enable:`,
and commit the fixes and the config change together. Never add a bare
`//nolint` — always `//nolint:linter // reason`.

> A gate that is green and enforced beats a gate that is comprehensive and
> bypassed.

---

## Tests

| # | Criterion | Enforced by |
|---|---|---|
| T1 | New behaviour has tests | 👤 reviewer |
| T2 | Bug fixes have a regression test that fails without the fix | 👤 reviewer |
| T3 | Coverage stays above both floors | `make coverage-gate-unit` · `make coverage-gate-full` |
| T4 | Tests are deterministic | 👤 reviewer + T5 |
| T5 | No flaky tests introduced | `go test -count=1` in CI |

### Two measurements, two floors

Repository code in this project is exercised almost entirely under
`-tags=integration` against a real PostgreSQL, because what it has to get right
— CHECK constraints, a partial unique index under genuine concurrency,
migrations up and down — cannot exist against a fake. Measuring without the tag
answers a different question, and drifts further from the truth with every slice
that adds persistence.

| Measurement | Command | Floor | Current | Needs |
|---|---|:--:|:--:|---|
| Unit | `make coverage-gate-unit` | **73.0%** | 74.1% | nothing |
| **Full (authoritative)** | `make coverage-gate-full` | **80.0%** | 80.8% | `DB_URL` |

`make coverage-gate` picks between them: full when `DB_URL` is set, unit
otherwise. Both are enforced in CI, in different jobs.

**The two are not merged, and there is no merge tool.** A build tag is additive:
`-tags=integration` compiles the untagged test files *as well as* the tagged
ones, so the full run is already a strict superset of the unit run in one pass.
No second profile means no merge step in which a package could be counted twice
and inflate the number. Both runs use the same `-coverpkg` list, so they share a
denominator and are directly comparable — the ~6-point gap between them is
precisely the share of the codebase that only a database can reach.

**The denominator is everything that ships, and nothing else.** One package is
excluded, by name, in [check_coverage.sh](../scripts/check_coverage.sh):
`cmd/lwprobe`, the external M2M consumer, which is exercised only by
`scripts/m2m-harness.sh` against a live installation because that is the whole
point of it. Its predecessor `scripts/two-realm-demo.sh` was never in the
denominator either; writing the next harness in Go — for type safety, and for
the import guard that keeps it honest — should not be charged against product
coverage. `cmd/api` and `cmd/bootstrap` DO ship and stay in at 0%, which is a
real gap kept visible rather than hidden. The exclusion list is explicit rather
than a pattern, so adding to it is a decision someone has to defend.

Floors rather than a strict never-decrease ratchet, deliberately: a ratchet
punishes the honest case where a PR adds a hard-to-test integration shim, and it
makes the gate flaky at the margins. Raise a floor in its own commit once
coverage has genuinely moved up and stayed there.

> **Always measure with `-count=1`.** Go's test cache serves stale per-package
> results otherwise. During the 2026-07-26 audit a cached run reported 69.1%
> for a tree that actually measured 74.1% — a 5-point phantom regression that
> cost real investigation time.

### Writing tests that do not become flaky

The existing suite is deterministic and should stay that way. The patterns it
already uses, which new tests should follow:

- **No live Keycloak in the untagged suite.** Stand up an `httptest.Server` —
  see `keycloakStub` in
  [`internal/server/server_test.go`](../internal/server/server_test.go). Tests
  that genuinely need a real provider go behind `//go:build integration` and
  skip without `KEYCLOAK_VERIFY_URL`.
- **No wall-clock dependence.** `CachedAdminChecker` takes an injectable
  `now func() time.Time` for exactly this reason. Do not `time.Sleep` to wait
  for a TTL.
- **No shared global state between tests.** `audit.SetDefault` returns the
  previous recorder so tests can restore it in `t.Cleanup`.
- **No ordering assumptions.** Tests must pass under `-shuffle=on`.
- **No network, no ports, no filesystem outside `t.TempDir()`.**

### What the test tiers cover

| Tier | Command | Runs in CI | Needs |
|---|---|---|---|
| Unit | `make test` | ✅ job `gate` | nothing |
| Coverage (unit) | `make coverage-gate-unit` | ✅ job `coverage` | nothing |
| Frontend | `make test-frontend` | ✅ job `frontend` | Node 20 |
| Integration + authoritative coverage | `make coverage-gate-full` | ✅ job `integration` | PostgreSQL + Keycloak |
| Race (unit) | `make test-race` | ✅ job `gate` | nothing |
| Race (integration) | `make test-race-integration` | ❌ manual | PostgreSQL + Keycloak |
| **E2E — machine boundary** | `make e2e-m2m` | ✅ job `e2e` | PostgreSQL + Keycloak |
| **E2E — operator boundary** | `make e2e-browser` | ✅ job `browser-e2e` | PostgreSQL + Keycloak + Chromium |
| **E2E — key lifecycle** | `make secrets-check` | ❌ manual | PostgreSQL |

`make test-race-integration` passes `-p 1`, and that is still required: the
integration suites share one PostgreSQL, and running the packages concurrently
under `-race` exhausts it. What `-p 1` never fixed was a package exhausting the
pool on its own, which `internal/identityruntime` did until Slice 12 —
[TD-030](TECH_DEBT.md#td-030) has the whole story, and it is worth reading
before diagnosing a `too many clients already` as a product bug.

`make secrets-check` is the third end-to-end tier and the newest. It drives the
compiled `cmd/secrets` binary through a complete master-key rotation against a
real database — legacy variable, mixed keyring, rotation, idempotent re-run, old
key removed, every refusal path — and then runs `scripts/scan-artifacts.sh` over
everything the run produced, looking for the keys and provider secrets it
actually used. See [SECRET_KEY_ROTATION.md](SECRET_KEY_ROTATION.md) §9.

### The two end-to-end tiers are not redundant

They test opposite sides of the same product, and passing one says nothing about
the other.

`scripts/m2m-harness.sh` is the **machine** boundary. It stands up realms,
workspaces, connections, projects and credentials, then hands over to
`cmd/lwprobe` — a consumer that imports nothing from this module — to exercise
the flow, the error matrix, the effective rate limit, revocation, connection
rotation and multi-realm isolation over HTTP.

`scripts/browser-e2e.sh` is the **operator** boundary: a real Chromium
completing a real PKCE login and clicking through workspace → connection →
project → credential → audit → revocation. Everything the machine harness
consumes has to be created by an operator first, and a console that renders but
cannot complete that sequence is a product nobody can install. See
[testing/BROWSER_E2E.md](testing/BROWSER_E2E.md).

Both run in CI, in **separate jobs**, so a failure names its own boundary:
"backend green, browser red" is a diagnosis, not a bisect.

The tier that closed the top risk on this register
([R-01](RISKS.md#r-01)) arrived in two halves — the machine half on 2026-08-09
([TD-003](TECH_DEBT.md#td-003)) and the operator half on 2026-08-10
([TD-031](TECH_DEBT.md#td-031)). The second half is the one that covers the
class of defect that produced [KI-001](KNOWN_ISSUES.md#ki-001): a console-only
regression, invisible to every server-side assertion. It earned that claim on
its first runs by finding [KI-019](KNOWN_ISSUES.md#ki-019) and
[KI-020](KNOWN_ISSUES.md#ki-020).

The negative half arrived in Slice 14 and is documented in
[security/AUTHORIZATION_MATRIX.md](security/AUTHORIZATION_MATRIX.md):
`make test` carries the mechanical route-by-scope sweep and the
provider-untouched assertions, the `e2e` job carries
`scripts/negative-authz-e2e.sh`, and `make authz-mutation-check` proves the
matrix would go red if the boundary were broken
([KI-018](KNOWN_ISSUES.md#ki-018) closed).

### Browser e2e has two rules of its own

**Artifacts may not contain secrets, and that is checked, not assumed.**
Playwright traces, screenshots and video are all off, because a trace stores
input values and the most valuable screenshot is the one taken while the
one-time credential modal is open.
[`scripts/scan-artifacts.sh`](../scripts/scan-artifacts.sh) then searches
everything published for the exact values the run used, plus `lw_sk_`/JWT/Bearer
shapes, and fails the build on a hit. Do not relax the policy to debug a flake.

**An unexpected browser error fails the test.** Not "is logged" — fails. Every
view is `async`, so a rejection after the first `await` escapes `router.js`'s
`try/catch` and leaves the page on "loading…" with the only evidence in the
console. Allowlist entries live in `tests/browser/fixtures/console-errors.js`
and each carries the reason it is benign; widening a test instead is how this
gate stops working.

---

## Documentation

`make check-docs` mechanically enforces two properties: **every link
resolves**, and **every number published in the docs matches the code**. What
it cannot check is whether the prose is still true — that is the reviewer's job.

Update whichever of these your change affects:

| If your change… | Update |
|---|---|
| Adds/removes/renames a route | [FEATURES.md](FEATURES.md) route tables · route counts in [PROJECT_STATUS.md](PROJECT_STATUS.md#metrics) |
| Adds an audit action | [FEATURES.md](FEATURES.md) · action count in PROJECT_STATUS |
| Changes a module's scope or maturity | [MODULES.md](MODULES.md) |
| Changes layering, middleware order, or an invariant | [ARCHITECTURE.md](ARCHITECTURE.md) |
| Makes an architectural decision | New `AD-nnn` in [PROJECT_STATUS.md](PROJECT_STATUS.md#architectural-decisions) |
| Introduces a shortcut | New `TD-nnn` in [TECH_DEBT.md](TECH_DEBT.md) |
| Fixes or finds a defect | [KNOWN_ISSUES.md](KNOWN_ISSUES.md) — **including the regression guard** |
| Ships or reprioritizes roadmap work | [ROADMAP.md](ROADMAP.md) |
| Is user-visible | [CHANGELOG.md](../CHANGELOG.md) under `[Unreleased]` |
| Changes the quick-start or feature list | [README.md](../README.md) |

**Never copy a number between documents — re-derive it.** The derivation
commands live in [PROJECT_STATUS.md §Metrics](PROJECT_STATUS.md#metrics), and
`make check-metrics` verifies 17 published claims across four documents on
every run. Copying is how this repository accumulated two months of misleading
documentation ([TD-001](TECH_DEBT.md#td-001)).

---

## API

Applies whenever a route is added, removed, or changes shape.

| # | Criterion | How |
|---|---|---|
| A1 | Swagger annotation on the handler | `// @Summary`, `@Tags`, `@Success`, `@Failure`, `@Router` |
| A2 | Generated OpenAPI regenerated and committed | `make docs && git add docs/` |
| A3 | `make swagger-check` passes | Automatic in `make ci` |
| A4 | Route table in [FEATURES.md](FEATURES.md) updated | Manual |
| A5 | Route counts still match | `make check-metrics` |
| A6 | Mutations emit an audit event | `logging.RecordMutation(...)` — success **and** failure |
| A7 | Correct route group / middleware chain | See [ARCHITECTURE.md §5](ARCHITECTURE.md#5-request-lifecycle) |
| A8 | Error mapping follows the existing contract | 401 unauthenticated · 403 authorized-but-denied · 404 not-found-or-not-configured · 409 conflict · 503 authz backend down |

> **A6 is not optional.** The audit invariant is *every mutation emits exactly
> one event carrying who/action/target/timestamp/ip, and failures additionally
> emit reason*. `logging.RecordMutation` centralises the branch precisely so
> the call sites cannot drift apart.

---

## Database

Applies whenever the schema changes. The schema is managed by versioned SQL
migrations — authoring rules and recovery procedure in
[MIGRATIONS.md](MIGRATIONS.md).

| # | Criterion | Notes |
|---|---|---|
| D1 | Migration is versioned | `make migrate-new NAME=…`, never a hand-edited applied migration. Naming and up/down pairing are enforced by `TestEmbeddedMigrations_Naming` |
| D2 | Rollback path validated | Apply, roll back, re-apply against a scratch database: `make migrate`, `make migrate-steps N=-1`, `make migrate` |
| D3 | Impact described in the PR | Locking behaviour, table size, expected duration, whether it is online-safe |
| D4 | Backfill strategy stated | Separate from the schema change for anything non-trivial |
| D5 | Data model documented | [ARCHITECTURE.md §8](ARCHITECTURE.md#8-data-layer) + [PROJECT_STATUS.md](PROJECT_STATUS.md#database) |
| D6 | Integration test covers the new schema | CI job `integration` runs against real PostgreSQL |

> **Never edit a migration that has already been applied.** It has run somewhere
> already, so editing it changes only fresh databases and silently forks the
> schema between environments. Add a new migration instead. This is the one
> database rule that cannot be recovered from mechanically.

---

## Security

Mandatory review for any change touching auth, authorization, input handling,
logging, or configuration.

### Authentication
- [ ] No new code path bypasses `RequireAuth`
- [ ] No token parsing outside `auth/keycloak` — the provider is the only validator
- [ ] Signing algorithms remain asymmetric-only (`HS*` stays rejected)
- [ ] No credential, token, or secret is written to a log or a response body

### Authorization
- [ ] New `/admin/*` routes are inside the gated group — never mounted standalone
- [ ] `RequireLiveAdmin` still **fails closed** (503 on lookup failure, never a claim fallback)
- [ ] Self-protection guards intact: self-delete, self-disable, self-strip-admin, last-admin
- [ ] Role/permission checks live in the **service** tier, not the handler
- [ ] Admin-cache invalidation called on anything that can change admin status

**A new `/v1` route is a security decision, and three gates make you take it.**
See [security/AUTHORIZATION_MATRIX.md](security/AUTHORIZATION_MATRIX.md).

- [ ] It has an entry in [`internal/authz/registry.go`](../internal/authz/registry.go)
      — `operatorOnly()` or `scoped(...)`. **The process refuses to boot without
      one**, so this is enforced rather than reviewed
- [ ] If it is project-reachable, it has a row in `nzRequests`
      ([`internal/server/authz_negative_harness_test.go`](../internal/server/authz_negative_harness_test.go))
      with a body that genuinely succeeds. `TestNegative_TheRequestTableCoversEveryRoute`
      fails the package until it does
- [ ] It has an entry in [`sdk/go/apicoverage.json`](../sdk/go/apicoverage.json)
      — `supported` with a method, or `unsupported` with a reason. Silence is
      what is refused, not absence
- [ ] Its OpenAPI annotation offers `ProjectKeyAuth` and names the required
      scope in the description
- [ ] If the route is a mutation, it goes through `Handler.write`, not
      `Handler.read`

Adding a **scope** costs more, and deliberately: a constant in
[`internal/authz/scope.go`](../internal/authz/scope.go), a migration changing the
`project_credentials_scopes_known` CHECK constraint, and a family that at least
one route uses. Making it cost a migration is what stops the vocabulary drifting
into dozens of half-meant permissions.

### Audit durability

A new **mutating** `/v1` route is a durability decision, and the gate makes you
take it. See [AUDIT.md §6](AUDIT.md#6-when-the-audit-write-fails).

- [ ] It has an entry in [`internal/auditlog/coverage.go`](../internal/auditlog/coverage.go)
      — `atomic(<event>)` if this database owns the state, `audited(<event>)` if a
      Keycloak realm does. `TestCoverage_EveryMutatingRouteIsClassified` fails
      without one, and `TestCoverage_DurabilityMatchesWhereTheStateLives` fails
      if the two disagree
- [ ] If it is `atomic(...)`, the service opens the transaction, binds BOTH its
      repository and the audit store to it, and returns the audit error rather
      than logging it
- [ ] If it is `atomic(...)`, it has a success case AND an audit-failure
      rollback case in
      [`atomicity_integration_test.go`](../internal/auditlog/atomicity_integration_test.go).
      `TestCoverage_EveryControlPlaneMutationHasAnAcceptanceCase` fails without
      them
- [ ] The handler builds the event with `logging.ControlPlaneEvent` and closes it
      with `logging.RecordControlPlaneOutcome` — never a bare `audit.Record` on
      the success path, which would write a second row
- [ ] Nothing secret is in `Extra`. The per-event allowlist in
      [`redaction.go`](../internal/auditlog/redaction.go) is a second barrier,
      not the first

### Input validation
- [ ] Every caller-supplied identifier validated before reaching a provider (UUID / role-name patterns)
- [ ] Pagination clamped — no unbounded `max`
- [ ] Request bodies are explicit structs; no `map[string]any` binding
- [ ] Length limits on free-text fields

### Logging and secrets
- [ ] No password, token, secret, or full request body in logs
- [ ] Secrets redacted in responses (see the SMTP password pattern in [`smtp_handler.go`](../internal/server/smtp_handler.go))
- [ ] New config values documented in [MODULES.md](MODULES.md#internalconfig), `.env.example`, **and `docker-compose.yml`**
- [ ] No secret committed — the pre-commit hook blocks `.env`, but `git add -f` would not

### Error responses
- [ ] Auth failures return the generic `{"error":"unauthorized"}` — reasons go to `AuthEvent` only
- [ ] No stack trace, SQL, or internal path in any response body

> Known open security items are tracked in [KNOWN_ISSUES.md](KNOWN_ISSUES.md):
> [KI-003](KNOWN_ISSUES.md#ki-003) (no security headers),
> [KI-004](KNOWN_ISSUES.md#ki-004) (rate limit bypassable via forged
> `X-Forwarded-For`), [KI-005](KNOWN_ISSUES.md#ki-005) (SPA never reviewed for
> XSS). Do not add to that list without recording it.

---

## Performance

| Area | Check |
|---|---|
| **Pagination** | Every list endpoint paginates. If a hard cap truncates, the response says so — silent truncation reports success while omitting data ([KI-013](KNOWN_ISSUES.md#ki-013)) |
| **Queries** | No N+1. If a loop issues a request per item, bound the concurrency and set an aggregate timeout — the existing offender is `ListSessions` ([TD-007](TECH_DEBT.md#td-007)) |
| **External calls** | Every outbound call carries a context with a deadline |
| **Cache** | New caches state their TTL, their invalidation trigger, and their behaviour on upstream error. Caches are **in-process** — they do not survive a restart and are not shared across replicas |
| **Concurrency** | Shared mutable state is guarded. New goroutines have a defined lifecycle — no unbounded spawning per request |
| **Allocation** | No per-request allocation proportional to total data size |
| **Blocking** | Nothing slow on the request path that could be deferred. Email dispatch is currently synchronous; making it async is [V2-03](ROADMAP.md#v2-03--job-queue-and-workers) |

---

## Local enforcement — git hooks

```bash
make hooks-install     # sets core.hooksPath to .githooks/
```

| Hook | Runs | Budget | Bypass |
|---|---|---|---|
| `pre-commit` | gofmt · go vet · `.env` guard | ~3 s | `git commit --no-verify` |
| `pre-push` | `make ci` + `make check-docs` | ~60 s | `git push --no-verify` |

Hooks are **opt-in by design**: `core.hooksPath` cannot be set by a commit, so
each clone enables them explicitly. CI is the real gate; hooks exist to move
the failure earlier, when it is cheap to fix.

`pre-commit` deliberately stays under 5 seconds. A hook that makes people wait
gets disabled, and a disabled hook catches nothing. `pre-push` deliberately
skips integration tests — a hook that fails because Docker is not running
teaches people to pass `--no-verify` reflexively.

---

## CI job map

| Job | Enforces | Needs |
|---|---|---|
| `gate` | fmt · vet · lint · build · test · swagger · docs | — |
| `coverage` | Unit coverage floor, uploads the profile | — |
| `frontend` | 30 admin console tests | Node 20 |
| `integration` | `-tags=integration` suite **and** the authoritative coverage floor | PostgreSQL service + Keycloak container |
| `e2e` | The m2m harness (what an authorized backend can do) **and** `scripts/negative-authz-e2e.sh` (what every other caller cannot) | PostgreSQL service + Keycloak container |
| `browser-e2e` | The operator journeys through real Chromium, plus the artifact secret scan | PostgreSQL + Keycloak + Playwright |
| `codeql` | Security + quality static analysis | — (weekly + PR) |

Two gates are deliberately **on demand** rather than per push, because both take
minutes and both answer "would the tests notice?" rather than "is the code
right?":

| Command | Answers |
|---|---|
| `make authz-mutation-check` | Breaks the authorization boundary eight ways; the matrix must go red each time |
| `make audit-mutation-check` | Breaks control-plane audit atomicity nine ways; the acceptance suite must go red each time. Needs `DB_URL` — mutations about ROLLBACK cannot be tested without PostgreSQL |
| `make sdk-mutation-check` | Breaks the SDK sixteen ways; its suite must go red each time |
| `make sdk-release-mutation-check` | Breaks release mechanics nineteen ways; the release gates must refuse each time |

Jobs run in parallel; a PR is mergeable when all are green.

---

## Releasing the Go SDK

The SDK is a separate module with its own release tag namespace, so it has its
own gate. Two layers, answering different questions.

**Before tagging**, the full branch CI above is the requirement: it proves the
repository works. Nothing below substitutes for it.

**On the tag**, `.github/workflows/sdk-release.yml` fires for `sdk/go/v*` and runs
a narrow, fast gate — the module's own releasability, in a couple of minutes
rather than the branch gate's tens. Running PostgreSQL, Keycloak and Chromium
again would prove nothing new, *given* that the tagged commit already went
through the branch gate, which is why the workflow's first job requires the
tagged commit to be an ancestor of `main`. That ancestry check is what stops
"narrower" from meaning "a way around".

| Command | Answers |
|---|---|
| `make sdk-release-check VERSION=vX.Y.Z` | Would a tag on **HEAD** be a valid release? Reads the commit, not the working tree |
| `make sdk-release-dev VERSION=vX.Y.Z` | Same content gates against the **working tree**; preconditions reported, not enforced |
| `make sdk-release-verify-tag TAG=sdk/go/vX.Y.Z` | Validates an existing tag — what CI runs |
| `make sdk-release-simulate` | Publishes to a throwaway Git remote and consumes it externally, with no `replace` |
| `make sdk-identity-check` | Do the module path, the tag prefix and the documented `go get` still agree? Cheap; runs on every push inside `make sdk-check` |
| `make sdk-api-check` | Has the exported SDK API drifted from `sdk/go/api.txt`? |
| `make sdk-consumer-check` | Does a module outside this repository compile against the SDK alone? |
| `make sdk-publish-smoke VERSION=vX.Y.Z` | **After** pushing a tag: is it installable from `proxy.golang.org`? |

None of these tags, pushes, or mutates any remote. `sdk-publish-smoke` reads
`origin` and refuses to run against a version that was never published, because
a failure there would otherwise mean nothing.

The permission model is deliberately minimal: the release workflow holds
`contents: read`. It validates and refuses; it publishes nothing. A GitHub
Release step would need `contents: write`, and that is the moment to grant it.

Full model, including what is proven offline and what waits for the first real
tag: [SDK_GO.md § Release](SDK_GO.md#release).

---

## When a gate is wrong

Gates are code and can be wrong. If one blocks a legitimate change:

1. **Do not disable it silently.** A commented-out check is invisible debt.
2. Fix the gate in the same PR if the gate is at fault, explaining why in the
   commit message.
3. If a finding is a genuine false positive, suppress it **narrowly and with a
   reason** — `//nolint:nilerr // read-back is informational; see comment
   above`, never a bare `//nolint`. There is exactly one such suppression today
   ([`realm.go`](../internal/identity/keycloak/realm.go)); adding a second
   should require a sentence of justification in review.
4. If a gate is wrong often enough to be annoying, that is a defect in the
   gate. Open an issue rather than working around it repeatedly.

## Maintenance

Review this document when adding a CI job, changing the coverage floor,
promoting a linter, or after any incident a gate failed to catch. When a gate
misses something, the fix is not only the bug — it is the gate.
