# Documentation Index

**Purpose:** Single entry point for every long-form document in this
repository. The actual source of truth for behaviour is the code and the
generated OpenAPI spec ([`swagger.yaml`](swagger.yaml) /
[`swagger.json`](swagger.json)); the documents catalogued here record
*why* decisions were made, *what* was validated, and *what* remains
open.

If you are new to the repo: start with [`../README.md`](../README.md),
then the [Quick Start](#quick-start) below, then
[`getting-started/KEYCLOAK_SETUP.md`](getting-started/KEYCLOAK_SETUP.md), then come back here for the
milestone history.

---

## Navigation

```
docs/
├── INDEX.md                      ← you are here
├── getting-started/QUICKSTART.md                 ← linear path: clone → run → first admin call
├── archive/QUICKSTART_REVIEW.md       ← DX audit of getting-started/QUICKSTART.md (accuracy/consistency)
├── getting-started/KEYCLOAK_SETUP.md             ← onboarding, env vars, troubleshooting
├── architecture/                 ← config-as-source-of-truth design + breaking-change records
│   ├── bootstrap.md
│   └── PHASE3_BREAKING_CHANGE.md
├── docs.go / swagger.json/yaml   ← generated OpenAPI (do not hand-edit)
├── audit/                        ← audit subsystem: model, wiring, operations, validation
├── operations/                   ← operator runbooks (backup, upgrade, monitoring)
├── release/                      ← per-release reports, checklists, tag freezes
├── security/                     ← gap audits, remediations, regressions, validations,
│                                   secrets management, audit operations
├── validation/                   ← functional / smoke / audit / CRUD validation evidence
├── ui/                           ← admin-console UX catalog + dev playground guide
├── roadmap/                      ← known limitations + post-tag hardening backlog
└── evidence/                     ← raw artifacts (api/, screenshots/, security/, ...)
```

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
