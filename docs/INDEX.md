# Documentation Index

**Purpose:** Single entry point for every long-form document in this
repository. The actual source of truth for behaviour is the code and the
generated OpenAPI spec ([`swagger.yaml`](swagger.yaml) /
[`swagger.json`](swagger.json)); the documents catalogued here record
*why* decisions were made, *what* was validated, and *what* remains
open.

---

## 📌 Canonical documentation — start here

These thirteen documents are the official, maintained description of the
project. They are verified against the code — `make check-docs` fails the build
if a link breaks or a published number stops matching. Where any other document
in this repository disagrees with them, **they win** — and the code wins over
all of them.

### Understanding the project

| Doc | Answers |
|---|---|
| **[PROJECT_STATUS.md](PROJECT_STATUS.md)** | What is the project, what state is it in, what are the real numbers? **Read this first.** |
| **[ARCHITECTURE.md](ARCHITECTURE.md)** | How is it built? How does a request flow? What are the invariants? |
| **[MODULES.md](MODULES.md)** | What does each module do, what does it depend on, how mature is it? |
| **[FEATURES.md](FEATURES.md)** | What exists, what is partial, what does not exist — with a code reference for every claim. |

### Where it is going

| Doc | Answers |
|---|---|
| **[ROADMAP.md](ROADMAP.md)** | What comes next, in what order, and why that order. |
| **[MILESTONE_v0.4.md](MILESTONE_v0.4.md)** | The next milestone: objectives, scope, acceptance criteria, risks. |
| **[RISKS.md](RISKS.md)** | The ten biggest threats to future evolution, scored by impact × probability. |

### What is wrong with it

| Doc | Answers |
|---|---|
| **[TECH_DEBT.md](TECH_DEBT.md)** | What shortcuts exist and what they cost. |
| **[KNOWN_ISSUES.md](KNOWN_ISSUES.md)** | What is broken, what is an accepted trade-off, what the workarounds are. |

### Working on it

| Doc | Answers |
|---|---|
| **[QUALITY_GATE.md](QUALITY_GATE.md)** | What must every PR satisfy, and what is automated vs. reviewer judgement. |
| **[testing/BROWSER_E2E.md](testing/BROWSER_E2E.md)** | The operator-boundary end-to-end suite: a real Chromium completing a real PKCE login and clicking through workspace → connection → project → credential → audit → revocation. How to run it, why traces and screenshots are off and how that is proved, and how it differs from the machine-boundary harness. |
| **[CONTRIBUTION_CHECKLIST.md](CONTRIBUTION_CHECKLIST.md)** | The short, tickable form. Copy into your PR. |
| **[HEALTH_CHECK.md](HEALTH_CHECK.md)** | Is the project healthy right now? Three tiers, with expected timings. |
| **[RELEASE_CHECKLIST.md](RELEASE_CHECKLIST.md)** | How to cut a release, end to end. |

New to the repo? Read [PROJECT_STATUS.md](PROJECT_STATUS.md), then
[../README.md](../README.md) for the quick start, then
[ARCHITECTURE.md](ARCHITECTURE.md). About to open a PR? Read
[CONTRIBUTION_CHECKLIST.md](CONTRIBUTION_CHECKLIST.md). Everything below is
supporting detail and historical record.

---

## Navigation

```
docs/
├── INDEX.md                      ← you are here
│
│   ── CANONICAL SET ──
├── PROJECT_STATUS.md             ← current state, metrics, maturity, ADRs
├── ARCHITECTURE.md               ← layers, request lifecycle, diagrams, invariants
├── MODULES.md                    ← per-module reference
├── FEATURES.md                   ← feature inventory with code references
├── ROADMAP.md                    ← MVP / v1 / v2 / future
├── TECH_DEBT.md                  ← debt register
├── KNOWN_ISSUES.md               ← defects, limitations, workarounds
│
│   ── SUPPORTING ──
├── WORKSPACES.md                 ← the Workspace domain and the /v1 API surface
├── CONNECTIONS.md                ← identity-provider connections, secrets at rest, verify
├── SECRET_KEY_ROTATION.md        ← master-key lifecycle: rotate, inspect, retire a key
├── WORKSPACE_IDENTITY_RUNTIME.md ← how a request is routed to a workspace's realm
├── WORKSPACE_IDENTITY_API.md     ← the /v1 identity surface, errors, route parity
├── WORKSPACE_CONSOLE.md          ← the multi-workspace console: routing, isolation, TD-024
├── PROJECTS.md                   ← machine credentials: scopes, workspace binding, revocation
├── SDK_GO.md                     ← the Go SDK: coverage matrix, boundary gates, release
├── FRONTEND_READINESS.md         ← pre-migration assessment (superseded)
├── MIGRATIONS.md                 ← schema migrations: commands, authoring, recovery
├── getting-started/QUICKSTART.md      ← linear path: clone → run → first admin call
├── getting-started/KEYCLOAK_SETUP.md  ← onboarding, env vars, troubleshooting
├── architecture/                 ← bootstrap design + breaking-change records
│   ├── bootstrap.md
│   └── PHASE3_BREAKING_CHANGE.md
├── docs.go / swagger.json/yaml   ← generated OpenAPI (do not hand-edit)
├── audit/                        ← audit subsystem: model, wiring, operations
├── operations/                   ← operator runbooks (backup, upgrade, monitoring)
├── security/                     ← gap audits, remediations, secrets management
├── ui/                           ← admin-console UX catalog + dev playground guide
│
│   ── HISTORICAL RECORD (not maintained) ──
├── release/                      ← per-release reports, checklists, tag freezes
├── validation/                   ← smoke / audit / CRUD validation runs
├── roadmap/                      ← superseded by ROADMAP.md + KNOWN_ISSUES.md
├── archive/                      ← superseded reports
└── evidence/                     ← raw artifacts from manual runs, May 2026
```

> **On the historical sections.** `release/`, `validation/`, `roadmap/`,
> `archive/` and `evidence/` are point-in-time records. They are useful for
> understanding *why* something was done, but they are **not** kept current and
> some of their claims are now stale. In particular
> [`roadmap/KNOWN_LIMITATIONS.md`](roadmap/KNOWN_LIMITATIONS.md) is superseded by
> [KNOWN_ISSUES.md](KNOWN_ISSUES.md), and the artifacts in `evidence/` all
> predate 2026-06-13 — they are screenshots, not tests
> ([TD-003](TECH_DEBT.md#td-003)).

---

## Quick Start

Zero-to-running-stack path for engineers cloning the repo. Pair with
[`getting-started/KEYCLOAK_SETUP.md`](getting-started/KEYCLOAK_SETUP.md) for the realm config and with
[`architecture/bootstrap.md`](architecture/bootstrap.md) for the config-as-source-of-truth model.

| Doc | Scope |
|-----|-------|
| [`getting-started/QUICKSTART.md`](getting-started/QUICKSTART.md) | Linear walkthrough: install → docker-compose → Keycloak → bootstrap → first admin call. ~10 min. |
| [`archive/QUICKSTART_REVIEW.md`](archive/QUICKSTART_REVIEW.md) | DX-audit log of `getting-started/QUICKSTART.md` — factual cross-checks against `Makefile`, `docker-compose.yml`, `.env.example`, `config/project.json`, `realm-export.json`. Records what was corrected and what remains gapped (operations/secrets — covered below). |

Once the stack is up, branch to the [Operations](#operations) and
[Security](#security-reports) sections for production hardening.

---

## Operations

Operator runbooks for running the stack beyond `make up`. Each doc is
copy-paste runnable against the shipped `docker-compose.yml` and
cross-references the security backlog in
[`security/SECURITY_GAPS.md`](security/SECURITY_GAPS.md) and the post-tag
roadmap in [`roadmap/HARDENING_REPORT.md`](roadmap/HARDENING_REPORT.md).

| Doc | Scope |
|-----|-------|
| [`CONNECTIONS.md`](CONNECTIONS.md) | Identity-provider connections: the draft → active → retired lifecycle, AES-256-GCM secrets at rest and the master key, the read-only verification probe, and the partial index enforcing one active connection per workspace. |
| [`SECRET_KEY_ROTATION.md`](SECRET_KEY_ROTATION.md) | The master-key lifecycle: the versioned keyring, how to rotate without downtime or re-entering credentials, how to know when an old key is safe to destroy, what a missing key degrades, and why a database backup alone does not restore an installation. |
| [`WORKSPACE_IDENTITY_RUNTIME.md`](WORKSPACE_IDENTITY_RUNTIME.md) | The runtime boundary: how `/v1/workspaces/{id}/users` resolves a workspace's active Connection to a Keycloak realm per request, what the boundary hides, why the provider cache is keyed on connection id plus `updated_at`, and why legacy `/admin/*` was deliberately left on environment configuration. |
| [`WORKSPACE_IDENTITY_API.md`](WORKSPACE_IDENTITY_API.md) | The complete `/v1/workspaces/{id}/...` identity surface: 24 operations across users, roles, sessions and invitations, the stable machine-readable error catalogue, the `caller_forbidden` vs `provider_forbidden` distinction, read-only connection semantics, and the route parity matrix against legacy `/admin/*`. |
| [`PROJECTS.md`](PROJECTS.md) | Projects and machine credentials: how an external backend authenticates without an operator token, the opaque key format and why it is hashed rather than sealed, the permanent workspace binding that is the authorization boundary, the eight scopes and the two placements that make least privilege real, what `roles:write` cannot do, the operator-only control plane, and the credential lifecycle. |
| [`SDK_GO.md`](SDK_GO.md) | The official Go SDK (`sdk/go`): why the workspace is bound at client construction rather than passed per call, why it is a separate Go module with no dependencies, the 24-of-24 parity matrix against the authorization registry and the OpenAPI document, the gates that stop the SDK drifting or learning what provider is behind the API, the real-stack acceptance suite, and the release model: the proven nested-module tag format, what the offline simulation establishes and what waits for the first pushed tag, and the bad-release policy. |
| [`WORKSPACE_CONSOLE.md`](WORKSPACE_CONSOLE.md) | The multi-workspace admin console: why the workspace lives in the route rather than in application state, the three isolation mechanisms that stop one workspace's data or mutations reaching another, the connection-state vocabulary the UI gates writes on, how TD-024 was resolved without mutating a realm, and what deliberately stayed on legacy `/admin/*`. |
| [`FRONTEND_READINESS.md`](FRONTEND_READINESS.md) | **Superseded** by `WORKSPACE_CONSOLE.md`. The pre-Slice-6 assessment of the admin console against the `/v1` surface: which views were expected to migrate mechanically, which were blocked, and the API-shape differences needing more than path replacement. Kept as the record of what was predicted. |
| [`WORKSPACES.md`](WORKSPACES.md) | The Workspace domain: slug and lifecycle rules, public `ws_` ids, the `/v1` surface and its stable error codes, and which invariants the database enforces rather than the code. |
| [`MIGRATIONS.md`](MIGRATIONS.md) | Schema migrations: the `make migrate*` commands, `DB_MIGRATE_ON_BOOT`, authoring rules, how databases predating versioned migrations are adopted, and dirty-state recovery. |
| [`operations/RUNNING.md`](operations/RUNNING.md) | **The operational reference.** Full configuration matrix (generated from `internal/config/contract.go`), fail-fast validation, liveness vs readiness, graceful shutdown, metrics exposure, log correlation, the recovery unit (`DB` + `SECRETS_MASTER_KEY`), the production smoke procedure, and what retries at startup versus what fails fast. Start here to deploy. |
| [`operations/BACKUP_AND_RECOVERY.md`](operations/BACKUP_AND_RECOVERY.md) | Backup & restore for both Postgres instances (app + Keycloak), realm export/import, disaster recovery drill. Cross-link: invitation orphan recovery in [`validation/BUG_REPORT_CRUD.md`](validation/BUG_REPORT_CRUD.md) §I14b. |
| [`operations/UPGRADE_AND_ROLLBACK.md`](operations/UPGRADE_AND_ROLLBACK.md) | Per-component upgrade procedure (api, Keycloak, Postgres), rollback to `v0.1.0-auth-foundation`, breaking-change history in [`architecture/PHASE3_BREAKING_CHANGE.md`](architecture/PHASE3_BREAKING_CHANGE.md). |
| [`operations/MONITORING.md`](operations/MONITORING.md) | Health endpoints, audit/auth structured logs to alert on, GAP-1 live-admin denial fingerprint, future Prometheus/OTel hooks. Reads [`security/SECURITY_REMEDIATION_GAP1.md`](security/SECURITY_REMEDIATION_GAP1.md) for the marker semantics. |

For audit-log inspection workflows specifically, see
[`audit/AUDIT_OPERATIONS.md`](audit/AUDIT_OPERATIONS.md).

---

## Release history

| Milestone | Notes | Checklist | Tag freezes |
|-----------|-------|-----------|-------------|
| `v0.1.0-auth-foundation` (2026-05-17) | Initial tag. Keycloak-delegated auth, JIT user provisioning, bootstrap pipeline. | — | — |
| `v0.2.0` (2026-05-20) — Identity Management | [`release/RELEASE_v0.2.md`](release/RELEASE_v0.2.md) · [`release/FINAL_RELEASE_REPORT.md`](release/FINAL_RELEASE_REPORT.md) | [`release/RELEASE_CHECKLIST.md`](release/RELEASE_CHECKLIST.md) · [`release/RC1_REPORT.md`](release/RC1_REPORT.md) | [`release/FINAL_TAG_REPORT.md`](release/FINAL_TAG_REPORT.md) (pre-stash → SAFE_TO_TAG=false) → [`release/FINAL_TAG_REPORT_v2.md`](release/FINAL_TAG_REPORT_v2.md) (post-stash → SAFE_TO_TAG=true) |

Per-release functional sign-off: [`release/FINAL_SMOKE.md`](release/FINAL_SMOKE.md).
Canonical changelog lives at repo root: [`../CHANGELOG.md`](../CHANGELOG.md).

---

## Security reports

Adversarial probes, gap analysis, and remediation evidence for the
Identity Management surface, plus the production-grade secrets and audit
runbooks operators need post-tag.

### Adversarial reports

| Doc | Scope |
|-----|-------|
| [`security/SECURITY_VALIDATION_v0.2.md`](security/SECURITY_VALIDATION_v0.2.md) | 17 black-box guard probes (G01–G17). |
| [`security/SECURITY_VALIDATION_v0.3.md`](security/SECURITY_VALIDATION_v0.3.md) | 6-surface advanced probes (rate-limit, brute force, fixation, replay, concurrency, escalation). |
| [`security/SECURITY_GAPS.md`](security/SECURITY_GAPS.md) | Adversarial gap catalogue. GAP-1 (HIGH, fixed), GAP-2 (MED, open), GAP-3 (LOW, open), GAP-4 (INFO, open). |
| [`security/SECURITY_REMEDIATION_GAP1.md`](security/SECURITY_REMEDIATION_GAP1.md) | Design + implementation of the GAP-1 fix (`auth.RequireLiveAdmin` + `CachedAdminChecker`). |
| [`security/SECURITY_REGRESSION_GAP1.md`](security/SECURITY_REGRESSION_GAP1.md) | Post-fix adversarial regression (R1–R7 PASS). |
| [`security/AUTHORIZATION_MATRIX.md`](security/AUTHORIZATION_MATRIX.md) | The `/v1` authorization boundary: the pipeline, the lifecycle states, caches and their staleness consequences, rate-limit ordering, audit semantics for rejected requests, and the three layers of negative evidence behind each. Closes [KI-018](KNOWN_ISSUES.md#ki-018). |
| [`security/FINAL_SECURITY.md`](security/FINAL_SECURITY.md) | Security gate verdict — synthesis of the above. |

### Operator runbooks

| Doc | Scope |
|-----|-------|
| [`security/SECRETS_MANAGEMENT.md`](security/SECRETS_MANAGEMENT.md) | Production secrets inventory (`.env.example` vars, realm-export credentials, SMTP block, Keycloak signing keys), rotation procedures, and trade-offs vs cloud-native secret stores. Pair with [`operations/UPGRADE_AND_ROLLBACK.md`](operations/UPGRADE_AND_ROLLBACK.md) when rotating during a release. |
| [`audit/AUDIT_OPERATIONS.md`](audit/AUDIT_OPERATIONS.md) | Inspection runbook for the audit subsystem — "who did what on `/admin/*`". Builds on the model in [`audit/AUDIT_EVENTS.md`](audit/AUDIT_EVENTS.md) and the wiring inventory in [`audit/AUDIT_WIRING.md`](audit/AUDIT_WIRING.md). Pair with [`operations/MONITORING.md`](operations/MONITORING.md) for the alerting layer. |

Raw evidence: [`evidence/security/`](evidence/security).

---

## Validation reports

Functional, CRUD, smoke, and audit-emission validation. Sign-off
material for the release gates.

| Doc | Scope |
|-----|-------|
| [`validation/VALIDATION_PHASE3.md`](validation/VALIDATION_PHASE3.md) | Sprint 3 sign-off (Keycloak hand-off). |
| [`validation/SMOKE_TEST_v0.2.md`](validation/SMOKE_TEST_v0.2.md) | RC1 smoke pass. |
| [`validation/CRUD_VALIDATION.md`](validation/CRUD_VALIDATION.md) | End-to-end CRUD validation (35/35). |
| [`validation/BUG_REPORT_CRUD.md`](validation/BUG_REPORT_CRUD.md) | Destructive QA (71 checks, 1 defect fixed: I14b). |
| [`validation/INVITATION_RELIABILITY_v0.2.md`](validation/INVITATION_RELIABILITY_v0.2.md) | Invitation lifecycle reliability + pagination stress. |
| [`audit/AUDIT_EVENTS.md`](audit/AUDIT_EVENTS.md) | Audit-event model and action vocabulary. |
| [`audit/AUDIT_WIRING.md`](audit/AUDIT_WIRING.md) | Per-handler audit emission inventory. |
| [`audit/AUDIT_VALIDATION.md`](audit/AUDIT_VALIDATION.md) | End-to-end audit-emission validation (PASS). |

Raw evidence: [`evidence/crud/`](evidence/crud), [`evidence/final/`](evidence/final), [`evidence/api/`](evidence/api).

---

## UI

| Doc | Scope |
|-----|-------|
| [`ui/UI_BUGS.md`](ui/UI_BUGS.md) | Static-analysis catalogue of `web/admin/` (20 bugs: 2 P0, 4 P1, 7 P2, 7 P3). |
| [`ui/DEV_AUTH_PLAYGROUND.md`](ui/DEV_AUTH_PLAYGROUND.md) | Dev-only auth playground at `/dev/auth` — flows, env gate, troubleshooting. |

---

## Roadmap

Forward-looking work — gaps acknowledged at release time and the
post-tag hardening backlog.

| Doc | Scope |
|-----|-------|
| [`roadmap/KNOWN_LIMITATIONS.md`](roadmap/KNOWN_LIMITATIONS.md) | Limitations carried forward from RC1 (security backlog, observability, invitation residual). |
| [`roadmap/HARDENING_REPORT.md`](roadmap/HARDENING_REPORT.md) | Post-v0.2.0 hardening backlog — consolidates references to every validation/security/UI/audit report. |

The root-level [`../archive/AUDITORIA_TECNICA.md`](archive/AUDITORIA_TECNICA.md) (in
Portuguese) is the original technical audit that preceded the v0.2
milestone. Kept at repo root for historical visibility; not relinked
into the subtree.

---

## Evidence

Raw artifacts — JSON responses, console logs, screenshots, security
probe outputs. Linked from the report that produced them; not browsed
on their own.

```
evidence/
├── api/               REST responses captured during exploratory probes
├── crud/              CRUD E2E run — api/, api_validation/, mailpit/, network/, screenshots/
├── crud-bugs/         destructive CRUD pass (api/, repro/, ui/)
├── final/             release-gate evidence (auth/, crud/, go/, security/, smoke/)
├── screenshots/       admin-console smoke screenshots (01..09_*.png)
└── security/          advanced/, checks/, gaps/ (incl. remediation/), regression/, summary.txt
```

---

## Duplicate-report audit (2026-05-21)

Conducted as part of this cleanup; recommendations are advisory.

| Pair | Status | Reason | Recommendation |
|------|--------|--------|----------------|
| [`release/FINAL_TAG_REPORT.md`](release/FINAL_TAG_REPORT.md) ↔ [`release/FINAL_TAG_REPORT_v2.md`](release/FINAL_TAG_REPORT_v2.md) | Not duplicates | Sequential snapshots of the same gate. v1 = pre-stash, `SAFE_TO_TAG=false`. v2 = post-stash, `SAFE_TO_TAG=true`. Both are cited by [`roadmap/HARDENING_REPORT.md`](roadmap/HARDENING_REPORT.md) as the canonical audit trail of the freeze. | **Keep both.** They are not redundant; deleting v1 would erase the failed-gate record that motivated the stash. |
| [`security/SECURITY_VALIDATION_v0.2.md`](security/SECURITY_VALIDATION_v0.2.md) ↔ [`security/SECURITY_VALIDATION_v0.3.md`](security/SECURITY_VALIDATION_v0.3.md) | Not duplicates | v0.2 = 17 baseline guard probes; v0.3 = 6 advanced threat-surface probes following v0.2. v0.3 explicitly extends v0.2. | **Keep both.** |
| [`audit/AUDIT_EVENTS.md`](audit/AUDIT_EVENTS.md) ↔ [`audit/AUDIT_WIRING.md`](audit/AUDIT_WIRING.md) ↔ [`audit/AUDIT_VALIDATION.md`](audit/AUDIT_VALIDATION.md) | Not duplicates | Model / wiring inventory / emission validation — three layers of the same subsystem. | **Keep all three.** |
| [`security/FINAL_SECURITY.md`](security/FINAL_SECURITY.md) ↔ [`release/FINAL_SMOKE.md`](release/FINAL_SMOKE.md) ↔ [`release/FINAL_RELEASE_REPORT.md`](release/FINAL_RELEASE_REPORT.md) | Distinct gates | Security gate vs functional gate vs combined release sign-off. | **Keep all three.** |

No merge / archive actions were taken — the existing report graph is
narrated by [`roadmap/HARDENING_REPORT.md`](roadmap/HARDENING_REPORT.md)
and breaking that graph would lose context.

---

## Conventions

- Filenames are **uppercase + snake_case** for milestone reports
  (e.g. `FINAL_SMOKE.md`) and **lowercase** for evergreen design docs
  (e.g. `architecture/bootstrap.md`). Preserved as-is during this reorg.
- Internal links use **relative paths**: a doc in `security/` links to
  a sibling in `validation/` via `../validation/FILE.md`.
- Evidence paths are not links into a navigation surface — they're
  citations. Treat them as immutable once written.
- Generated files (`docs.go`, `swagger.json`, `swagger.yaml`) are
  produced by `make docs` and gated by `make swagger-check`. Never
  hand-edit; reorg them only if the generator's output path also
  changes.
