# Documentation Index

**Purpose:** Single entry point for every long-form document in this
repository. The actual source of truth for behaviour is the code and the
generated OpenAPI spec ([`swagger.yaml`](swagger.yaml) /
[`swagger.json`](swagger.json)); the documents catalogued here record
*why* decisions were made, *what* was validated, and *what* remains
open.

If you are new to the repo: start with [`../README.md`](../README.md),
then [`KEYCLOAK_SETUP.md`](KEYCLOAK_SETUP.md), then come back here for
the milestone history.

---

## Navigation

```
docs/
├── INDEX.md                      ← you are here
├── KEYCLOAK_SETUP.md             ← onboarding, env vars, troubleshooting
├── bootstrap.md                  ← config-as-source-of-truth design
├── docs.go / swagger.json/yaml   ← generated OpenAPI (do not hand-edit)
├── migrations/                   ← breaking-change records
│   └── PHASE3_BREAKING_CHANGE.md
├── release/                      ← per-release reports, checklists, tag freezes
├── security/                     ← gap audits, remediations, regressions, validations
├── validation/                   ← functional / smoke / audit / CRUD validation evidence
├── ui/                           ← admin-console UX catalog + dev playground guide
├── roadmap/                      ← known limitations + post-tag hardening backlog
└── evidence/                     ← raw artifacts (api/, screenshots/, security/, ...)
```

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
Identity Management surface.

| Doc | Scope |
|-----|-------|
| [`security/SECURITY_VALIDATION_v0.2.md`](security/SECURITY_VALIDATION_v0.2.md) | 17 black-box guard probes (G01–G17). |
| [`security/SECURITY_VALIDATION_v0.3.md`](security/SECURITY_VALIDATION_v0.3.md) | 6-surface advanced probes (rate-limit, brute force, fixation, replay, concurrency, escalation). |
| [`security/SECURITY_GAPS.md`](security/SECURITY_GAPS.md) | Adversarial gap catalogue. GAP-1 (HIGH, fixed), GAP-2 (MED, open), GAP-3 (LOW, open), GAP-4 (INFO, open). |
| [`security/SECURITY_REMEDIATION_GAP1.md`](security/SECURITY_REMEDIATION_GAP1.md) | Design + implementation of the GAP-1 fix (`auth.RequireLiveAdmin` + `CachedAdminChecker`). |
| [`security/SECURITY_REGRESSION_GAP1.md`](security/SECURITY_REGRESSION_GAP1.md) | Post-fix adversarial regression (R1–R7 PASS). |
| [`security/FINAL_SECURITY.md`](security/FINAL_SECURITY.md) | Security gate verdict — synthesis of the above. |

Raw evidence: [`evidence/security/`](evidence/security/).

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
| [`validation/AUDIT_EVENTS.md`](validation/AUDIT_EVENTS.md) | Audit-event model and action vocabulary. |
| [`validation/AUDIT_WIRING.md`](validation/AUDIT_WIRING.md) | Per-handler audit emission inventory. |
| [`validation/AUDIT_VALIDATION.md`](validation/AUDIT_VALIDATION.md) | End-to-end audit-emission validation (PASS). |

Raw evidence: [`evidence/crud/`](evidence/crud/), [`evidence/final/`](evidence/final/), [`evidence/api/`](evidence/api/).

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

The root-level [`../AUDITORIA_TECNICA.md`](../AUDITORIA_TECNICA.md) (in
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
| [`validation/AUDIT_EVENTS.md`](validation/AUDIT_EVENTS.md) ↔ [`validation/AUDIT_WIRING.md`](validation/AUDIT_WIRING.md) ↔ [`validation/AUDIT_VALIDATION.md`](validation/AUDIT_VALIDATION.md) | Not duplicates | Model / wiring inventory / emission validation — three layers of the same subsystem. | **Keep all three.** |
| [`security/FINAL_SECURITY.md`](security/FINAL_SECURITY.md) ↔ [`release/FINAL_SMOKE.md`](release/FINAL_SMOKE.md) ↔ [`release/FINAL_RELEASE_REPORT.md`](release/FINAL_RELEASE_REPORT.md) | Distinct gates | Security gate vs functional gate vs combined release sign-off. | **Keep all three.** |

No merge / archive actions were taken — the existing report graph is
narrated by [`roadmap/HARDENING_REPORT.md`](roadmap/HARDENING_REPORT.md)
and breaking that graph would lose context.

---

## Conventions

- Filenames are **uppercase + snake_case** for milestone reports
  (e.g. `FINAL_SMOKE.md`) and **lowercase** for evergreen design docs
  (e.g. `bootstrap.md`). Preserved as-is during this reorg.
- Internal links use **relative paths**: a doc in `security/` links to
  a sibling in `validation/` via `../validation/FILE.md`.
- Evidence paths are not links into a navigation surface — they're
  citations. Treat them as immutable once written.
- Generated files (`docs.go`, `swagger.json`, `swagger.yaml`) are
  produced by `make docs` and gated by `make swagger-check`. Never
  hand-edit; reorg them only if the generator's output path also
  changes.
