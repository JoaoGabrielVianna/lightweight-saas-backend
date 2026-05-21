# Release Candidate 1 — v0.2.0-rc1

**Release:** `v0.2.0-rc1` (Identity Management milestone)
**Branch:** `milestone/auth-v1`
**Report date:** 2026-05-20
**Coordinator:** release-prep agent (this document is a roll-up; the underlying validations were performed by four parallel agents whose reports are linked below)

---

## 1. Decision

```
┌─────────────────────────────────────┐
│                                     │
│   VERDICT:  ▶▶  GO  ◀◀  for RC1     │
│                                     │
└─────────────────────────────────────┘
```

**Headline:** 0 FAIL · 0 regressions · 4/4 agent reports landed · all gaps are documented limitations rather than defects.

This is a *release candidate*: it goes to RC1 with the understanding that the gaps in [§5](#5-known-gaps-at-rc1) are acknowledged and either acceptable for the milestone or scheduled for v0.3. Promotion from `v0.2.0-rc1` → `v0.2.0` should follow the [docs/RELEASE_CHECKLIST.md](RELEASE_CHECKLIST.md) acceptance gate.

---

## 2. Scope under test

The v0.2 surface — admin-only `/admin/*` HTTP API plus minimal static UI under `web/admin/` — as described in [docs/RELEASE_v0.2.md](RELEASE_v0.2.md) and tracked in [CHANGELOG.md §0.2.0](../../CHANGELOG.md#020--2026-05-20).

Exercised against the running `docker-compose` stack at commit on `milestone/auth-v1` with seeded users `adminuser` (admin role) and `testuser` (user role) against realm `saas`.

---

## 3. Agent reports

Four isolated agents validated v0.2 in parallel. Each owned a disjoint scope; cross-cutting concerns are reconciled in [§4](#4-cross-cutting-synthesis).

### Agent A — Security

**Reports:** [SECURITY_VALIDATION_v0.2.md](../security/SECURITY_VALIDATION_v0.2.md) · [SECURITY_VALIDATION_v0.3.md](../security/SECURITY_VALIDATION_v0.3.md)
**Runners:** [scripts/security_live_check.sh](../../scripts/security_live_check.sh), [scripts/security_advanced_check.sh](../../scripts/security_advanced_check.sh)
**Evidence:** [docs/evidence/security/](../evidence/security/)

| Suite                 | Probes | PASS | FAIL | INFO | Result |
|-----------------------|-------:|-----:|-----:|-----:|--------|
| v0.2 baseline guards  |    17  |  17  |   0  |   —  | **PASS** |
| v0.3 advanced probes  |    11  |   5  |   0  |   6  | **PASS** (no FAILs) |

Highlights:
- Auth / RBAC / path-traversal: all 17 baseline guards return the specified status.
- Brute-force protection (T2): Keycloak realm-level lockout fires after 30 sequential bad passwords; admin-recoverable via attack-detection API.
- Privilege escalation (T6): every admin verb denied to non-admin tokens; header injection (`X-User-Role: admin`) ignored; cross-client tokens rejected at `azp` check (401) *before* RBAC.
- Concurrent admin actions (T5): 10 parallel `POST /admin/roles` with the same name → `1×201 / 9×409` (race-safe).

Findings carried to [KNOWN_LIMITATIONS.md §1](../roadmap/KNOWN_LIMITATIONS.md#1-security--hardening-backlog): **F1** (no rate limiting, Medium), **F2** (post-logout JWT remains valid until `exp`, Low-Med), **F3** (no DPoP / `jti` revocation, Low). All three are consistent with the documented OAuth2 bearer model — not regressions, surfaced for the next iteration.

**Agent A verdict: GO.**

---

### Agent B — Browser smoke + CRUD E2E

**Reports:** [SMOKE_TEST_v0.2.md](../validation/SMOKE_TEST_v0.2.md) · [evidence/crud/CRUD_E2E_REPORT.md](../evidence/crud/CRUD_E2E_REPORT.md)
**Driver:** `/tmp/smoketest_v02/{smoke,crud}.spec.mjs` (headless Chromium · Playwright 1.60 · outside repo by design)
**Evidence:** [docs/evidence/screenshots/](../evidence/screenshots/), [docs/evidence/api/](../evidence/api/), [docs/evidence/crud/](../evidence/crud/)

| Pass        | Surface                                       | Status            |
|-------------|-----------------------------------------------|-------------------|
| Smoke (read)| Login (PKCE), users, roles, sessions, invitations tabs | **PASS WITH GAPS** — read paths only |
| CRUD (write)| 14 mutation actions across all 4 surfaces     | **PASS WITH GAPS** — 12 PASS · 3 PASS_GAP · 1 NOT_IMPLEMENTED · 0 FAIL |

Detail on CRUD outcomes:
- **12 PASS** — every non-SMTP mutation returned its specified 2xx/204.
- **3 PASS_GAP (HTTP 502)** — `POST /admin/invitations`, `POST /admin/invitations/{id}/resend`, `POST /admin/users/{id}/reset-password`. The local dev Keycloak realm has no SMTP wired; endpoint code paths are exercised up to `executeActionsEmail`, and the compensating-delete contract (Agent C) is observed: failed create leaves no orphan.
- **1 NOT_IMPLEMENTED** — realm-wide "Terminate all sessions" button is rendered disabled with a `coming-soon` badge; no backend endpoint exists in v0.2.

Browser console captured **3 errors**, all `Failed to load resource: 502 (Bad Gateway)` on the SMTP endpoints; no JavaScript exceptions, no view-render crashes.

**Agent B verdict: GO WITH GAPS** — gaps are documented, not bugs.

---

### Agent C — Invitation reliability

**Report:** [INVITATION_RELIABILITY_v0.2.md](../validation/INVITATION_RELIABILITY_v0.2.md)
**Code scope:** `internal/identity/keycloak/invitations.go` (reviewed, not modified by this aggregator)

Three failure modes closed in v0.2:

| # | Failure mode pre-v0.2                                                                 | v0.2 contract                                                                 |
|---|---------------------------------------------------------------------------------------|-------------------------------------------------------------------------------|
| 1 | Partial `CreateInvitation` left half-provisioned users; retries hit `409 Conflict`.  | Compensating DELETE on failure of role-mapping or email-dispatch step; deferred via `committed` flag; fresh `context.Background()` with 5s timeout. |
| 2 | `ResendInvitation` re-added the full `[VERIFY_EMAIL, UPDATE_PASSWORD]` set on every call, including to fully-accepted or disabled users. | GETs user first; returns `ErrConflict` (HTTP 409) for disabled or no-pending-invite-actions; otherwise PUTs only the intersection. |
| 3 | `deriveInvitationStatus` checked `expires_at` before `requiredActions` — accepted-but-stale invites were reported `expired`. | Explicit total ordering: `revoked > accepted > expired > pending`. Public status constants on `identity` package. |

Pagination added to `ListInvitations` and `ListUsersByRole` (page size 200, hard cap 10,000). Stress test evidence in [INVITATION_RELIABILITY_v0.2.md §Pagination](../validation/INVITATION_RELIABILITY_v0.2.md#pagination-added-after-the-initial-reliability-pass). The `ListUsersByRole` fix matters for the `assertNotLastAdmin` guard in realms with >100 admins.

New tests cover all three changes plus the pagination ceiling; one prior test that asserted the bug was replaced.

Remaining limitations explicitly enumerated by the agent — carried to [KNOWN_LIMITATIONS.md §3](../roadmap/KNOWN_LIMITATIONS.md#3-invitation-reliability-residual).

**Agent C verdict: GO.**

---

### Agent D — Audit / Observability foundation

**Report:** [AUDIT_EVENTS.md](../validation/AUDIT_EVENTS.md)
**Code scope:** `internal/audit/`, `internal/logging/` (audit sink) — model + log sink shipped

What landed in RC1:
- `audit.Event` model with required fields: `who` (Actor), `action` (canonical enum), `target` (Kind/ID/Name), `timestamp` (auto-stamped UTC), `ip` (`gin.Context.ClientIP()`); optional `Reason`, `Extra`.
- Action vocabulary across user / role / session / invitation mutations (13 actions enumerated).
- `internal/logging.AuditSink` writes one JSON-per-line entry prefixed with `audit ` so downstream filters can grep cheaply.
- Architecture is provider-agnostic: `internal/audit` has no gin / keycloak / logger imports; swap-by-`SetDefault` for tests.
- Default recorder is a no-op until `logging.WireDefault()` is called from bootstrap.

What is **not** landed in RC1 (handed off to the identity owner):
1. `logging.WireDefault()` call at bootstrap (likely `cmd/api/main.go` or `internal/server/server.go`).
2. `audit.Record(...)` call sites in `internal/identity/handler.go` (mapping table of 13 handlers → 13 actions in [AUDIT_EVENTS.md "Call sites"](../validation/AUDIT_EVENTS.md#2-call-sites-in-internalidentityhandlergo)).

Until both are wired, every `audit.Record` call is silently dropped. **This is the most material limitation of RC1** — see [§5](#5-known-gaps-at-rc1) and [KNOWN_LIMITATIONS.md §4](../roadmap/KNOWN_LIMITATIONS.md#4-audit-events-not-yet-emitted).

**Agent D verdict: GO with explicit handoff** — infrastructure is shipped and stable; the wiring step is a separate, low-risk follow-up that does not affect any other v0.2 contract.

---

## 4. Cross-cutting synthesis

Items where two or more agent reports touch the same surface and must reconcile:

| Concern                                          | Agents       | Reconciled outcome |
|--------------------------------------------------|--------------|---------------------|
| SMTP-dependent endpoints                         | B, C         | Agent B observes `502` from the wire; Agent C's compensating-delete contract guarantees no orphan user is left behind. Verified by Agent B re-listing users post-failure. **Behaviour matches the documented contract.** |
| Realm-wide "Terminate all sessions"              | B            | UI exposes the button disabled with `coming-soon`; no backend route in v0.2. Acknowledged as a non-feature, not a regression. |
| Concurrent admin role creation                   | A (T5), C    | Agent A confirms `1×201 / 9×409` under 10× parallelism; Agent C's pagination & status-precedence work doesn't change the create path. **No conflict.** |
| Cross-client tokens                              | A (T6)       | `azp` check at auth tier rejects `saas-backend-admin` client_credentials tokens with `401` *before* RBAC. The admin service account cannot impersonate a user-tier caller — important because it interacts with Agent D's `audit.Actor.Subject` extraction (the admin client would not pass `RequireAuth`, so it can never produce an audit event). |
| Pre-existing pending invitation `user@example.com` | B (smoke), B (CRUD), C | The smoke pass observed it; the CRUD pass **revoked it** (DELETE → 204). Future smokes can no longer assume its presence — Agent B's CRUD report flags this in its own §6. **Operational note, not a defect.** |
| Sessions row count drift                         | B            | Smoke saw 9 sessions where the run added 2 — pre-existing sessions in the realm, not a leak. Cross-checked with Agent A's T3 which doesn't depend on session count. **No conflict.** |

Nothing in cross-cutting synthesis blocks RC1.

---

## 5. Known gaps at RC1

The full enumeration is in [docs/KNOWN_LIMITATIONS.md](../roadmap/KNOWN_LIMITATIONS.md). Tier-1 items (most material to RC1):

- **L4 — Audit events not yet emitted.** Model + sink shipped; `audit.Record` call sites in identity handlers and the `logging.WireDefault()` bootstrap call are pending. Until wired, RC1 emits **no audit log lines** for admin mutations. *Mitigation:* `auth.AuthEvent` continues to emit authn/RBAC events as in v0.1.
- **L5 — SMTP not configured in the dev stack.** Three endpoints (`/admin/invitations` POST, `/admin/invitations/{id}/resend`, `/admin/users/{id}/reset-password`) cannot complete in a `make up` environment; they return `502` with the compensating-DELETE contract observed. *Operational:* wire MailHog or equivalent.
- **L6 — No realm-wide "Terminate all sessions" backend.** UI shows disabled `coming-soon`; per-user logout-all works.
- **F1 — No rate limiting at API tier.** Bursts of 100 req/s are served without `429`. *DoS surface; not a confidentiality regression.*
- **F2 — Post-logout JWT remains valid until `exp`.** Standard JWT trade-off; bounded by `accessTokenLifespan` (3600s).
- **F3 — No DPoP / `jti` revocation.** Bearer JWT is replayable for its full TTL by any holder.

None of L4–L6 / F1–F3 are regressions against v0.2's documented contract.

---

## 6. Evidence inventory

All evidence committed under `docs/evidence/`:

```
docs/evidence/
├── api/                          ← raw JSON payloads from smoke pass (6 files)
├── screenshots/                  ← 9 PNGs from smoke pass
├── console_log.txt               ← smoke driver log
├── results.json                  ← structured smoke per-area outcomes
├── crud/
│   ├── CRUD_E2E_REPORT.md        ← Agent B's CRUD report
│   ├── screenshots/              ← 34 PNGs from CRUD pass
│   ├── api/                      ← 17 raw JSON dumps of each mutation
│   ├── network/all.jsonl         ← 104 captured /admin/* + /auth/* responses
│   ├── console_log.txt           ← CRUD driver log
│   └── results.json              ← structured CRUD per-action outcomes
└── security/
    ├── summary.txt               ← Agent A v0.2 roll-up
    ├── checks/G01..G17.txt       ← per-probe evidence for v0.2 baseline guards
    └── advanced/
        ├── summary.txt           ← Agent A v0.3 roll-up
        └── T1..T6_*.txt          ← per-probe evidence for advanced threat probes
```

All runners are idempotent — re-running overwrites the evidence subtree with fresh tokens. Re-run from the repo root:

```bash
bash scripts/security_live_check.sh        # Agent A baseline
bash scripts/security_advanced_check.sh    # Agent A advanced
# Agent B/CRUD: drivers live at /tmp/smoketest_v02/{smoke,crud}.spec.mjs
```

---

## 7. Acceptance gate for `v0.2.0` (final, not RC1)

Per [docs/RELEASE_CHECKLIST.md](RELEASE_CHECKLIST.md), before promoting RC1 → final the maintainer should:

- [ ] Decide whether **L4 (audit wiring)** ships in 0.2.0 or slips to 0.2.1.
  - If it ships in 0.2.0: identity-package owner does the wiring; re-run Agent B CRUD pass to confirm audit lines emit on each mutation.
  - If it slips: tighten the `AUDIT_EVENTS.md` "Wiring (pending)" note and call it out explicitly in `CHANGELOG.md`.
- [ ] Configure dev SMTP (MailHog) and re-run Agent B's CRUD pass to flip the 3 PASS_GAPs to PASS — or document the SMTP-dependent endpoints as "requires SMTP" in the README.
- [ ] Decide whether **F1 (rate limiting)** is a 0.2.0 blocker or 0.3 backlog. Recommendation: 0.3 (matches the F1 severity assessment).
- [ ] Re-run both security scripts and Agent B's two Playwright passes against the final RC build; archive a fresh `docs/evidence/` snapshot.
- [ ] Tag `v0.2.0` against the same commit as the final RC build.

---

## 8. Sign-off

This RC1 report aggregates four independent validations against the v0.2 surface. No agent reported a FAIL or a regression. The aggregator did not modify any code; the only files written by this mission are the three release artifacts:

- [docs/RC1_REPORT.md](RC1_REPORT.md) (this file)
- [docs/KNOWN_LIMITATIONS.md](../roadmap/KNOWN_LIMITATIONS.md)
- [docs/RELEASE_CHECKLIST.md](RELEASE_CHECKLIST.md)

```
Verdict:        GO for v0.2.0-rc1
Blocking FAILs: 0
Regressions:    0
Known gaps:     6 documented (L4–L6, F1–F3) — none release-blocking at RC tier
```
