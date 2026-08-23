# Technical Risk Register

**Last updated:** 2026-08-10 · Companion to [TECH_DEBT.md](TECH_DEBT.md), [KNOWN_ISSUES.md](KNOWN_ISSUES.md) and [ROADMAP.md](ROADMAP.md)

The ten largest threats to this project's ability to **evolve**. Forward-looking
and scored, unlike the other two registers:

| Register | Question it answers |
|---|---|
| [KNOWN_ISSUES.md](KNOWN_ISSUES.md) | What is broken **now**? |
| [TECH_DEBT.md](TECH_DEBT.md) | What shortcut exists, and what does it cost? |
| **RISKS.md** (this) | What could make future work **expensive or impossible**? |

Entries cross-reference `TD-` / `KI-` IDs rather than restating them.

## Scoring

**Impact** — cost if it materialises · **Probability** — likelihood within the
next two milestones, given current trajectory.

| Score | Impact | Probability |
|---|---|---|
| 5 | Project-threatening; months of rework | Near-certain |
| 4 | Milestone-threatening; weeks of rework | Likely |
| 3 | Feature-threatening; days of rework | Possible |
| 2 | Contained; hours of rework | Unlikely |
| 1 | Nuisance | Rare |

**Priority = Impact × Probability.** 20+ act now · 12–19 next milestone ·
6–11 planned · ≤5 monitor.

---

## Summary

| # | Risk | Impact | Prob. | Score | Priority |
|---|---|:--:|:--:|:--:|---|
| [R-01](#r-01) | ~~No end-to-end test coverage~~ · both boundaries covered in CI 2026-08-10; residual is negative assertion of the guards | 4 | 2 | ~~25~~ **8** | Monitor |
| [R-02](#r-02) | ~~Multi-tenancy deferred while the schema grows~~ · workspaces shipped in v0.4.0; residual is the unwritten ADR | 3 | 2 | ~~20~~ **6** | Planned |
| [R-03](#r-03) | Bus factor of one | 4 | 5 | **20** | **Act now** |
| [R-04](#r-04) | ~~No versioned migrations before a second table~~ · resolved 2026-07-28 | — | — | ~~16~~ **0** | Resolved |
| [R-05](#r-05) | ~~Cannot observe production~~ · metrics, readiness and correlation shipped; tracing remains | 3 | 2 | ~~16~~ **6** | Planned |
| [R-06](#r-06) | Keycloak coupling limits what is expressible | 4 | 3 | **12** | Next milestone |
| [R-07](#r-07) | Deployment is manual and not reproducible · compose recipe fixed and gated; no CD or IaC | 2 | 3 | ~~12~~ **6** | Planned |
| [R-08](#r-08) | ~~Composition root does not scale~~ · `RouterDeps` landed 2026-08-09 | 2 | 2 | ~~12~~ **4** | Monitor |
| [R-09](#r-09) | Frontend has no framework, build step, or type safety | 3 | 3 | **9** | Planned |
| [R-10](#r-10) | Unreviewed security surfaces | 4 | 2 | **8** | Planned |

Two risks (R-01, R-03) are **not** technical debt — no shortcut created them.
They are structural properties of how the project is built and staffed.

**Re-scored 2026-08-24**, during the v0.4.2 documentation reconciliation. Five
rows had gone stale: R-04 was already marked resolved in its own entry while the
table still scored it 16, and R-02, R-05, R-07 and R-08 were all materially
addressed by work that shipped between v0.3.1 and v0.4.1 without the table being
touched. **R-03 (bus factor) is unchanged at 20 and is now the highest score on
the register.** The per-risk entries below carry the evidence.

---

## R-01

### No end-to-end test coverage

**Originally Impact 5 · Probability 5 · Score 25 — act now**
**Now Impact 4 · Probability 2 · Score 8 — monitor** (see the mitigation below)

*As originally written, 2026-07-27:* Every automated test is unit-level. There
is no test that starts the stack, authenticates, and exercises a real request
path. The artifacts under [evidence/](evidence/) are screenshots from manual
runs in May 2026 — records, not tests.

**Why probability is 5, not an estimate.** This has already happened.
[KI-001](KNOWN_ISSUES.md#ki-001) crashed the process at boot in the default dev
configuration, and silently broke the admin console in the production
configuration. It shipped 2026-06-13 and survived until 2026-07-26 — six weeks,
19 commits, and a green CI throughout. The gap is not hypothetical; it is
demonstrated.

**Why impact is 5.** It compounds with every other risk. R-02, R-04, R-06 and
R-08 all describe changes that touch broad surfaces, and none of them can be
attempted confidently without regression detection. This risk is the multiplier
on the rest of the register.

**Partially mitigated 2026-07-27.** Integration and frontend suites now run in
CI, coverage has a floor, and 9 linters block ([V1-02](ROADMAP.md#v1-02--complete-the-ci-gate--delivered-2026-07-27)).
That raises the floor but does not close the gap — running more unit tests is
not end-to-end coverage.

**Substantially mitigated 2026-08-09 and 2026-08-10.** Both boundaries now have
real end-to-end coverage in CI, in two separate jobs:

- **machine** ([TD-003](TECH_DEBT.md#td-003), job `e2e`) —
  `scripts/m2m-harness.sh` boots the stack and drives it through `cmd/lwprobe`,
  an external consumer that imports nothing from this module;
- **operator** ([TD-031](TECH_DEBT.md#td-031), job `browser-e2e`) — a real
  Chromium completes a real PKCE login and clicks through workspace →
  connection → project → credential → audit → revocation.

The second is the half that addresses this risk's own evidence. KI-001 was
scored 5-for-probability *because it had already happened*, and it presented as
a console symptom that no server-side assertion could see. That class is now
covered — demonstrated, not asserted: the browser suite's first runs found
[KI-019](KNOWN_ISSUES.md#ki-019) and [KI-020](KNOWN_ISSUES.md#ki-020), the
latter being the Slice 10 audit trail rendering as a permanent "loading…" for
every workspace that had events.

**Residual: score 6 (Impact 4 · Probability 1.5), 2026-08-13.** Slice 14 closed
the negative half: every project-reachable route is swept against every scope,
each refusal is proven to land before the provider is touched, and the boundary
families have real-stack evidence
([security/AUTHORIZATION_MATRIX.md](security/AUTHORIZATION_MATRIX.md),
[KI-018](KNOWN_ISSUES.md#ki-018) closed). What is still uncovered is the
console beyond the operator journey, which remains `node --test` against
fakes.

**Recommendation, and what is left of it.**
[V1-01](ROADMAP.md#v1-01--automated-end-to-end-test-suite) is delivered. Two
items from the original recommendation are NOT:

- ~~**negatively assert every authorization guard**~~ — **delivered in Slice
  14** ([KI-018](KNOWN_ISSUES.md#ki-018)). Three layers: a mechanical sweep of
  every route against every scope, the same refusals proven to land before the
  provider is reached, and a real-stack suite for the properties a mock cannot
  hold. Eight deliberate mutations of the boundary are all caught;
- **cover both console flag configurations.** The browser suite runs the
  production-shaped one (`ADMIN_CONSOLE_ENABLED=true`,
  `DEV_PLAYGROUND_ENABLED=false`) — deliberately, because that is the
  configuration KI-001 broke and the one the pre-existing unit test did not
  reach. The dev-tools configuration is still covered only by
  `TestServerSetupRoutes_FlagMatrix*`.

**Related:** [TD-003](TECH_DEBT.md#td-003) · [TD-031](TECH_DEBT.md#td-031) · [KI-001](KNOWN_ISSUES.md#ki-001) · [KI-018](KNOWN_ISSUES.md#ki-018)

---

## R-02

### Multi-tenancy deferred while the schema grows

> **Re-scored 2026-08-24: 20 → 6.** The risk as written was that tenancy would be
> retrofitted into a grown schema. That did not happen: `v0.4.0` shipped
> Workspaces as the tenant boundary, each bound to its own Keycloak realm and
> resolved per request, with isolation proven across real realms in CI. The
> expensive retrofit this risk existed to prevent was avoided.
>
> **What remains** is that the decision was never written down as an ADR, so the
> reasoning lives in code and commit messages rather than on paper. That is a
> documentation risk with a real but bounded cost — it compounds the bus factor
> ([R-03](#r-03)) rather than the schema. Tracked as
> [TD-010](TECH_DEBT.md#td-010).
>
> The original text is preserved below.

**Impact 5 · Probability 4 · Score 20 — act now**

There is no tenancy of any kind — no `tenant_id`, no resolution middleware, no
query scoping. Meanwhile the product roadmap (organizations, billing, storage)
consists almost entirely of features that need a tenant boundary.

**Why this is a risk and not merely debt.** Deferring costs nothing *today* —
one table, no customers. The cost is a function of how much is built first.
Retrofitting a tenant boundary means touching the primary key strategy of every
table, every query, every handler, and every authorization check, and doing it
without an e2e suite (R-01) to prove nothing leaked across tenants. Cross-tenant
data leakage is the failure mode, and it is the kind that ends products.

**Trajectory.** A code comment promised this for v0.3; v0.3 shipped without it.
The bootstrap CLI still collects a `multi_tenant` flag that nothing reads
([KI-007](KNOWN_ISSUES.md#ki-007)), which means `project.json` can already
assert multi-tenancy for a system that has none.

**Recommendation.** Make the decision — and write the ADR — **during v1**, even
if implementation waits. Three candidate strategies with trade-offs are laid out
in [V2-01](ROADMAP.md#v2-01--decide-and-implement-multi-tenancy). Until it is
decided, treat every new table as a future migration target and say so in review.

**Related:** [TD-010](TECH_DEBT.md#td-010) · [KI-007](KNOWN_ISSUES.md#ki-007)

---

## R-03

### Bus factor of one

**Impact 4 · Probability 5 · Score 20 — act now**

Every commit in this repository has a single author. There is no second person
who has reviewed the architecture, and no evidence of any PR having been
reviewed by anyone else.

**Why probability is 5.** This is not a prediction — it is the current state.
The risk materialises the moment the author is unavailable for any reason.

**Why impact is only 4, not 5.** The project has an unusually strong defence
already: the documentation set is genuinely good, inline comments explain *why*
rather than *what*, and the architectural decisions are recorded with their
rationale ([PROJECT_STATUS.md §Architectural Decisions](PROJECT_STATUS.md#architectural-decisions)).
A competent Go engineer could pick this up. What they could not recover is the
undocumented judgement — why Keycloak over alternatives, which trade-offs were
considered and rejected.

**Recommendation.** Lower-effort, high-value, in order:
1. The quality gates delivered 2026-07-27 are themselves a mitigation — they
   encode standards that previously lived only in the author's head.
2. Record the *rejected* alternatives, not only the chosen ones, in the AD
   entries.
3. Add the missing community-health files so an outside contributor has an
   on-ramp ([TD-017](TECH_DEBT.md#td-017)).
4. Get one external review of `internal/auth` — the highest-consequence package.

**Related:** [TD-017](TECH_DEBT.md#td-017)

---

## R-04

### No versioned migrations before a second table

**~~Impact 4 · Probability 4 · Score 16~~ — resolved 2026-07-28**

Schema management is one `AutoMigrate` call. Tolerable for one table; a
liability the moment there are several, because `AutoMigrate` never drops
columns, has no cross-model ordering guarantees, no down path, and no record of
what ran against which environment.

**Why probability is 4.** Every substantial roadmap item adds tables: durable
audit ([V1-06](ROADMAP.md#v1-06--durable-audit-trail)), organizations, billing,
queue. The schema *will* grow.

**Sequencing trap.** Adopting migrations gets harder after `AutoMigrate` has
produced a production schema, because the baseline migration must be reconciled
against whatever it actually created there — which may differ per environment.
The cheap moment is now, with one table.

**Resolved 2026-07-28.** [V1-07](ROADMAP.md#v1-07--versioned-migrations) shipped
while the schema was still one table, which is the cheap moment the sequencing
trap above describes. The baseline was captured from a live `AutoMigrate` run
rather than reconstructed, and is idempotent, so existing environments adopt it
without reconciliation. See [MIGRATIONS.md](MIGRATIONS.md).

**Related:** [TD-005](TECH_DEBT.md#td-005)

---

## R-05

### Cannot observe production

> **Re-scored 2026-08-24: 16 → 6.** `/metrics` (request counts, a duration
> histogram, authentication failures, authorization denials), `/health/live` and
> `/health/ready`, and request correlation by `request_id` / `project_id` /
> `credential_id` / `workspace_id` all shipped 2026-08-09.
>
> **What remains** is tracing — no spans, no context propagation, no
> OpenTelemetry ([TD-009](TECH_DEBT.md#td-009)) — and the fact that `/metrics`
> has never been scraped by a real Prometheus ([TD-032](TECH_DEBT.md#td-032)).
> Neither is acute for a single-instance deployment.
>
> The original text is preserved below.

**Impact 4 · Probability 4 · Score 16 — next milestone**

No `/metrics`, no tracing, no error-rate signal, no readiness probe. `/health`
returns `{"status":"ok"}` unconditionally — it checks neither the database nor
Keycloak, so an orchestrator cannot tell "process up" from "able to serve".

**Consequences on the current trajectory.** No alerting on latency, error rate,
rate-limit rejections, or live-admin denials without parsing logs. Degradation
is discovered by users. The `RequireLiveAdmin` path in particular depends on
Keycloak availability and fails closed with 503 — a Keycloak slowdown becomes an
admin outage with no signal until someone reads logs.

**Mitigating factor that lowers effort, not impact.** The extension points
already exist and are documented: `auth.SetEventHook` and `audit.SetDefault`.
This is additive work — the architecture anticipated it. That is why this is
rated 16 rather than 20.

**Recommendation.** [V1-05](ROADMAP.md#v1-05--metrics-and-tracing). Add the
readiness probe first — it is an hour of work and the highest ratio of value to
effort on this register.

**Related:** [TD-009](TECH_DEBT.md#td-009) · [KI-012](KNOWN_ISSUES.md#ki-012)

---

## R-06

### Keycloak coupling limits what is expressible

**Impact 4 · Probability 3 · Score 12 — next milestone**

Delegating identity to Keycloak (AD-001) was the right call and remains so. The
risk is the ceiling it imposes, which becomes visible only as the product grows.

**Where the ceiling already shows:**

- Invitations are not a Keycloak resource — they are *derived* from user state,
  so status is inferred rather than stored, and cannot carry product-specific
  fields.
- `ListSessions` is N+1 by construction: Keycloak exposes sessions per client,
  so a realm-wide listing costs one request per client
  ([TD-007](TECH_DEBT.md#td-007)).
- Every admin operation is a network round-trip. There is no transaction across
  Keycloak and the local database, so multi-step operations need compensating
  deletes — already the case for invitation creation.
- Realms larger than the hard caps silently truncate
  ([KI-013](KNOWN_ISSUES.md#ki-013)).

**Why probability is 3, not higher.** These are tolerable at current scale. They
become acute with organizations (R-02) or with any realm large enough to make
the caps bite.

**Recommendation.** Do not reverse AD-001 — the alternative is owning credential
storage, which is far worse. Instead plan a **local mirror** for read-heavy
paths: project Keycloak users into the local database, serve listings from
there, reconcile asynchronously. That needs [V1-07](ROADMAP.md#v1-07--versioned-migrations)
and [V2-03](ROADMAP.md#v2-03--job-queue-and-workers). The provider comment
already gestures at this ("adopt the v0.3 local-mirror approach") — but v0.3
shipped without it, so treat it as unplanned work, not a plan.

**Related:** [TD-007](TECH_DEBT.md#td-007) · [KI-013](KNOWN_ISSUES.md#ki-013) · [KI-010](KNOWN_ISSUES.md#ki-010)

---

## R-07

### Deployment is manual and not reproducible

> **Re-scored 2026-08-24: 12 → 6.** Three of the four specifics in the original
> text are closed: the compose file carries the variables the code reads
> ([TD-004](TECH_DEBT.md#td-004)), graceful shutdown exists with
> `SHUTDOWN_TIMEOUT_SECONDS` ([TD-013](TECH_DEBT.md#td-013)), and the install
> path is now `./scripts/init.sh` plus `docker compose up -d`, gated by
> `make product-acceptance`.
>
> **What remains** is the absence of a CD pipeline, Kubernetes manifests or a
> Helm chart, and any IaC, plus the manual `docker exec` for the Keycloak email
> theme ([KI-011](KNOWN_ISSUES.md#ki-011)). Reproducible by hand is not the same
> as automated.
>
> The original text is preserved below.

**Impact 3 · Probability 4 · Score 12 — next milestone**

No CD pipeline, no IaC, no graceful shutdown. The `docker-compose.yml` omits
four environment variables the code reads, so **the documented production recipe
cannot be reproduced from the shipped artifact** ([TD-004](TECH_DEBT.md#td-004)).
A whole CORS feature is unreachable through it.

**Compounding factor.** The production environment (EasyPanel / Docker Swarm)
already needs a manual `docker exec` after every restart to restore the Keycloak
email theme ([KI-011](KNOWN_ISSUES.md#ki-011)). Manual steps in a deploy path
are forgotten under pressure, and this one fails *silently* — emails just revert
to unbranded defaults.

Every deploy also drops in-flight requests: there is no `http.Server` with
`Shutdown`, no signal handling ([TD-013](TECH_DEBT.md#td-013)).

**Recommendation.** [V1-03](ROADMAP.md#v1-03--fix-docker-compose-environment-wiring)
first — it is small and now blocks [V1-01](ROADMAP.md#v1-01--automated-end-to-end-test-suite).
Then [V1-08](ROADMAP.md#v1-08--deployment-automation) for CD and graceful
shutdown, and bake the Keycloak theme into a custom image to remove the manual
step entirely.

**Related:** [TD-004](TECH_DEBT.md#td-004) · [TD-013](TECH_DEBT.md#td-013) · [KI-011](KNOWN_ISSUES.md#ki-011)

---

## R-08

### Composition root does not scale

> **Re-scored 2026-08-24: 12 → 4.** `SetupRouter`'s eight positional parameters
> were replaced by a `RouterDeps` struct on 2026-08-09
> ([TD-006](TECH_DEBT.md#td-006)), which is exactly the recommendation below.
> Adding a handler is no longer a breaking signature change. Manual DI
> ([AD-008](PROJECT_STATUS.md#ad-008--manual-dependency-injection)) was kept, as
> the entry advised.
>
> The original text is preserved below.

**Impact 3 · Probability 4 · Score 12 — next milestone**

`SetupRouter` takes eight positional parameters, four of them nilable with
meaning carried by position. `SetupIdentity` returns four values. Manual DI
(AD-008) is a deliberate and, so far, correct choice — but the wiring signatures
are already past comfortable.

**Why probability is 4.** It has already broken once: commit `e2a3bcd` exists
solely to repair tests after a signature change, and a whole root-level
troubleshooting document was written to explain it. Every roadmap item that adds
a handler — durable audit, organizations, webhooks — widens the signature again.

**Recommendation.** [V1-11](ROADMAP.md#v1-11--refactor-setuprouter-to-a-dependency-struct):
replace the parameter list with a `RouterDeps` struct so additions stop being
breaking changes. Do it after [V1-01](ROADMAP.md#v1-01--automated-end-to-end-test-suite)
so the refactor is verifiable. **Do not** replace manual DI with a container —
that would trade a readable graph for reflection and startup magic.

**Related:** [TD-006](TECH_DEBT.md#td-006)

---

## R-09

### Frontend has no framework, build step, or type safety

**Impact 3 · Probability 3 · Score 9 — planned**

The admin console is 5,809 lines of dependency-free vanilla JavaScript across 14
views. Zero-dependency was a defensible choice at 1,000 lines. At 5,800 the
trade-offs are visible: no type checking, no component model, manual DOM
construction, and an implicit contract with the API that nothing verifies.

**Demonstrated failure mode.** The console silently depends on twelve fields
from `GET /auth/debug`. When a handler returned a subset, the UI rendered an
authenticated admin as "not signed in" — no error, no failing test, no log line
(second symptom of [KI-001](KNOWN_ISSUES.md#ki-001)). That contract is now
pinned by a Go test, but only because it was found the hard way.

**Why impact is 3, not higher.** It is an internal admin tool, not a customer
surface. Degradation is annoying, not existential.

**Recommendation.** Do not rewrite. Incrementally: keep pinning API contracts
with server-side tests as the console grows; add JSDoc type annotations plus
`tsc --checkJs` for type safety without a build step; hold the line at 14 views
until the framework question is answered deliberately. Revisit only if the
console becomes customer-facing.

**Related:** [KI-005](KNOWN_ISSUES.md#ki-005) · [KI-001](KNOWN_ISSUES.md#ki-001)

---

## R-10

### Unreviewed security surfaces

**Impact 4 · Probability 2 · Score 8 — planned**

Two open items where the risk is genuinely *unknown* rather than assessed:

1. **The SPA has never been reviewed for XSS** ([KI-005](KNOWN_ISSUES.md#ki-005)).
   It builds DOM from template strings and renders attacker-influenceable data —
   an invited user controls their own name; a session's user-agent is
   client-supplied. Stored XSS in an admin console is a privilege-escalation
   vector, and there is no CSP to blunt it ([KI-003](KNOWN_ISSUES.md#ki-003)).
2. **The rate limit is bypassable** by forging `X-Forwarded-For`
   ([KI-004](KNOWN_ISSUES.md#ki-004)), nullifying the only rate limit in the
   system — the control added specifically to close finding F1.

**Why probability is only 2.** Both need a motivated attacker with access to an
admin console that is not yet widely deployed. This is the lowest-probability
entry, not the least important.

**Why impact is 4.** Either one, exploited, compromises the admin tier — which
is the whole product.

**Recommendation.** [V1-04](ROADMAP.md#v1-04--close-the-rate-limit-bypass) for
the rate limit (small, mechanical, no dependencies) and
[V1-09](ROADMAP.md#v1-09--security-hardening-pass) for the XSS review plus
security headers. The three existing black-box scripts in [`scripts/`](../scripts/)
should also run at every release — see
[RELEASE_CHECKLIST.md](RELEASE_CHECKLIST.md#security-probes).

**Related:** [KI-003](KNOWN_ISSUES.md#ki-003) · [KI-004](KNOWN_ISSUES.md#ki-004) · [KI-005](KNOWN_ISSUES.md#ki-005)

---

## What is deliberately not on this list

| Not a risk | Why |
|---|---|
| Absence of billing, queue, storage | Not built yet by design. Missing scope is not risk |
| Go 1.25 / Gin / GORM choices | Mainstream, maintained, no migration pressure |
| Keycloak as the identity provider | Correct decision (AD-001). The *coupling* is R-06; the choice is not a risk |
| Monolith rather than microservices | Right for the size. Premature decomposition would be the risk |
| Manual dependency injection | Deliberate (AD-008) and readable. The *signature growth* is R-08 |
| Coverage at 75% | Above the floor, enforced, and rising. Quality of tests (R-01) matters more than quantity — and the tiers that matter most now exist at both boundaries |

## Maintenance

Re-score at each milestone boundary. A risk whose probability drops to 1 for two
consecutive milestones can be retired — record why. A risk that materialises
becomes an entry in [KNOWN_ISSUES.md](KNOWN_ISSUES.md), and its register entry
gets a post-mortem line explaining what the score got wrong.
