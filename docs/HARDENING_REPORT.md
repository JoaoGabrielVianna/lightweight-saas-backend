# Hardening Report — Post-v0.2.0

**Date:** 2026-05-20
**Role:** Agent E — Release Manager / Technical Auditor (hardening pass)
**Branch:** `milestone/auth-v1` (5 commits ahead of `origin`; v0.2.0 tag at HEAD `e9c00c9`)
**Scope:** Consolidate every adversarial/QA finding produced by Agents A–D on top of the v0.2.0 release; classify by severity; recommend whether the open work fits a `v0.2.1` patch or a `v0.3` minor.
**Constraints:** No code changes, no commits, no pushes, no tags. Analysis only.

---

## 1. GO / NO-GO

```
┌───────────────────────────────────────────────────────────────┐
│  CURRENT TAG (v0.2.0):         ▶▶ GO — already shipped ◀◀     │
│  HARDENING BACKLOG:             ▶▶ GO WITH LIMITATIONS ◀◀     │
│                                                               │
│  Recommended next release:     v0.2.1 (patch, focused)        │
│  Followed by:                  v0.3   (minor, scoped backlog) │
└───────────────────────────────────────────────────────────────┘
```

**Rationale.** v0.2.0 is already tagged at the right SHA with GAP-1 closed, CHANGELOG correct, `make ci` green, and audit-wiring functional — see [FINAL_TAG_REPORT_v2.md](FINAL_TAG_REPORT_v2.md). The hardening backlog this report consolidates does NOT block the v0.2.0 push. It defines the *next* slice of work:

- **v0.2.1 (patch)** — 2 P0 + 4 P1 UI-only fixes. Narrow, no API contract change, no new infrastructure. Closes the highest-impact post-tag findings.
- **v0.3 (minor)** — GAP-2 / GAP-3 / GAP-4 security backlog (needs new infrastructure: backchannel-logout listener, realm-wide terminate endpoint, strict JSON binding), plus the P2 UI hardening pass.

The bugs are categorised under [§3](#3-classified-bug-list-p0--p1--p2) and routed to a release in [§4](#4-release-routing-recommendation).

---

## 2. Sources collected

Every adversarial/QA report produced by Agents A–D on top of v0.2.0, plus the freeze-verification reports:

| # | Document | Author | Surface | Findings |
|---|----------|--------|---------|----------|
| 1 | [BUG_REPORT_CRUD.md](BUG_REPORT_CRUD.md) | Agent A — Destructive QA Engineer | API + UI destructive (71 checks) | 1 defect (I14b — fixed) + 1 contract clarity (R02 — by design) |
| 2 | [SECURITY_GAPS.md](SECURITY_GAPS.md) | Agent D — Security Tester (adversarial probes) | Privilege/role/session attack surface | GAP-1 (HIGH, fixed), GAP-2 (MEDIUM, open), GAP-3 (LOW, open), GAP-4 (INFO, open) |
| 3 | [SECURITY_REMEDIATION_GAP1.md](SECURITY_REMEDIATION_GAP1.md) | GAP-1 fix author | `internal/auth` + handler invalidation | Fix design + 16 unit tests + live-stack G1.1–G1.10 PASS |
| 4 | [SECURITY_REGRESSION_GAP1.md](SECURITY_REGRESSION_GAP1.md) | Agent F regression | `/admin/*` post-fix replay | 7/7 PASS (R1–R7) |
| 5 | [UI_BUGS.md](UI_BUGS.md) | UI catalog (static analysis of `web/admin/`) | Admin console JS | 20 bugs: 2 P0, 4 P1, 7 P2, 7 P3 |
| 6 | [AUDIT_VALIDATION.md](AUDIT_VALIDATION.md) | Audit-coverage validator | `internal/identity/handler.go` | All 13 mutations emit audit events (validated by handler-paired tests) |
| 7 | [FINAL_TAG_REPORT.md](FINAL_TAG_REPORT.md) | Release Manager (v1 freeze) | git state | SAFE_TO_TAG=false (pre-stash) |
| 8 | [FINAL_TAG_REPORT_v2.md](FINAL_TAG_REPORT_v2.md) | Release Manager (v2 freeze) | git state | SAFE_TO_TAG=true (post-stash) — tag created |

All eight inputs were read in full by this report; nothing is taken on trust from a summary.

---

## 3. Classified bug list (P0 / P1 / P2)

Severity convention: **P0** = security-relevant or causes data corruption/loss · **P1** = user-visible misbehaviour with no manual recovery · **P2** = confusing UX, recoverable by reload or with operator awareness.

### 3.1 P0 — release-blocking-class

These are not currently in v0.2.0 (which already shipped), but they are the next release's must-fix tier.

| ID | Title | Surface | Source | Status |
|----|-------|---------|--------|--------|
| **UI-001** | Double-click on "Login with Keycloak" corrupts PKCE state — token exchange fails with `invalid_grant` / `OAuth state mismatch`, user must re-click | `web/admin/static/js/views/playground.js:69-71` + `lib/auth.js:41-60` | UI_BUGS.md §P0 | OPEN |
| **UI-002** | Overview view's late async resolve overwrites the next view — user navigates to `/users`, sees the users table briefly, then it's replaced by the Overview cards while the URL still says `#/users` | `web/admin/static/js/views/overview.js:44-67` | UI_BUGS.md §P0 | OPEN |

**Why P0:** UI-001 breaks the entry-point auth flow on the only timing-sensitive interaction in the SPA. UI-002 silently lies about the user's location in the app (the route/URL says one thing, the rendered DOM says another) — exactly the class of bug that produces "the admin clicked Delete on the wrong row" incidents. Both are 5–10-line fixes per the UI_BUGS triage table.

### 3.2 P1 — high-impact, hotfix-tier

| ID | Title | Surface | Source | Status |
|----|-------|---------|--------|--------|
| **GAP-2** | Session-terminate ≠ JWT revocation — killing an admin's sessions via `DELETE /admin/users/:id/sessions` does not invalidate their already-issued bearer JWT; the same token keeps authorizing `/admin/users` until `exp` | `internal/auth/middleware.go` (root cause shared with GAP-1) | SECURITY_GAPS.md §E1, §E5 | OPEN — partially mitigated by GAP-1 fix for the **admin** surface specifically (when ops also unassigns the admin role, the GAP-1 cache invalidation takes effect on next request); standalone fix tracked separately |
| **UI-003** | Double-click on "Send reset email" dispatches multiple password-reset emails to the user's inbox | `web/admin/static/js/views/user-detail.js:48,214-223` | UI_BUGS.md §P1 | OPEN |
| **UI-004** | Double-click on invitation "Resend" dispatches duplicate invitation emails | `web/admin/static/js/views/invitations.js:57,181-193` | UI_BUGS.md §P1 | OPEN |
| **UI-005** | Double-click on "Refresh token" breaks the in-flight refresh due to Keycloak's rotation — second click hits with the already-invalidated refresh token and returns `invalid_grant`; user may believe the session died | `web/admin/static/js/views/playground.js:76-79` + `lib/auth.js:90-107` | UI_BUGS.md §P1 | OPEN |
| **UI-006** | API Explorer Send-button race — when the user clicks Send, edits the form, clicks Send again, the response panel can show the *first* request's reply (whichever finishes last wins, not whichever was sent last) | `web/admin/static/js/views/apiexplorer.js:97,110-149` | UI_BUGS.md §P1 | OPEN |

**Why P1:** GAP-2 is a real auth gap but is bounded — the GAP-1 closure already covers the demoted-admin case for the `/admin/*` surface (operators who unassign the admin role get instant effect; the JWT keeps "authenticating" but the gate denies it). UI-003 / UI-004 are operationally important (operators don't expect double-emails to land in user inboxes). UI-005 / UI-006 are operator-facing confusion bugs.

### 3.3 P2 — backlog-tier

Grouped by category for triage.

#### Security (defence in depth)

| ID | Title | Source | Status |
|----|-------|--------|--------|
| GAP-3 | Realm-wide bulk session terminate is not implemented — `DELETE /admin/sessions` (no `:id`) returns 404; UI shows the button disabled with `coming-soon` badge | SECURITY_GAPS.md §E2 | OPEN by design for v0.2 |
| GAP-4 | `PATCH /admin/users/:id` silently drops unknown fields — `{"realm_access":{"roles":["admin"]}}` returns 200 with no warning (no privilege gained, but a stricter decoder would surface the attempted mass-assignment in audit logs) | SECURITY_GAPS.md §B5/GAP-4 | OPEN |
| F1  | No per-IP / per-`sub` rate limiting at API tier (100 req/s served without `429`) | SECURITY_VALIDATION_v0.3.md §3 | OPEN — known DoS surface |
| F2  | Stateless JWT — access token remains valid up to `accessTokenLifespan` (3600 s) post-OIDC logout | SECURITY_VALIDATION_v0.3.md §5 | OPEN — bounded; GAP-1 closure narrows blast radius |
| F3  | No DPoP / `jti` revocation — bearer token replayable until `exp` | SECURITY_VALIDATION_v0.3.md §6 | OPEN — documented OAuth2 model |

#### UI hardening (`web/admin/`)

| ID | Title | Source |
|----|-------|--------|
| UI-007 | No client-side validation in user-edit modal; cleared email field round-trips to a generic 400 | UI_BUGS.md §P2 |
| UI-008 | Empty role name on create returns generic 400 with no field hint | UI_BUGS.md §P2 |
| UI-009 | Expired token mid-session → state drift (sidebar says "signed in", views say 401) | UI_BUGS.md §P2 |
| UI-010 | Refresh-token failure leaves the stale token in `sessionStorage` | UI_BUGS.md §P2 |
| UI-011 | Modal Esc-during-action: action proceeds, but the modal is gone → "User deleted" toast appears without context | UI_BUGS.md §P2 |
| UI-012 | Stacked modals: opening a second modal wipes the first's DOM but leaves its keydown handler bound | UI_BUGS.md §P2 |
| UI-013 | Module-level state leaks across navigations (API Explorer's edited path, users list's `pageState`) | UI_BUGS.md §P2 |
| UI-014…UI-020 | Misc: refresh-button busy guards, pager edge cases, token countdown drift, unbounded toast stack, dead "sortable columns" comment, leaked event listeners, no `AbortController` on route change | UI_BUGS.md §P3 |

#### Contract / observability

| ID | Title | Source | Disposition |
|----|-------|--------|-------------|
| R02 | `POST /admin/roles {"name":"UPPERCASE"}` returns 201 with name lowercased | BUG_REPORT_CRUD.md §2 | **By design** — left as-is; remediation is one-line API doc note |
| FS-2 / FS-3 | CRUD playwright fixture drift (resend/revoke depend on `user@example.com` pre-seed; revoke-one needs a non-admin session at test time) | FINAL_SMOKE.md §5 | Test-data only; not a runtime bug |
| FS-4 | One-time `go test ./...` first-run flake on audit tests — not reproduced over 5 subsequent runs (including the `-race` runs in [FINAL_TAG_REPORT_v2.md §Gate 2](FINAL_TAG_REPORT_v2.md#gate-2--go-test--race)) | FINAL_SMOKE.md §5 | Monitor — promote to P2 only if recurs |

### 3.4 Closed in v0.2.0 (verified by this auditor)

For completeness, items that landed in v0.2.0 and are not part of the hardening backlog:

| ID | What it was | How it was closed | Verified by |
|----|-------------|-------------------|-------------|
| **GAP-1** | Stale-admin-JWT replay on `/admin/*` (HIGH) — demoted user kept full admin powers for up to 60 minutes | `auth.RequireLiveAdmin` + `auth.CachedAdminChecker` mounted on `/admin/*` as the third gate after `RequireAuth + RequireRole("admin")`; identity handlers invalidate cache on mutations; fails closed on Keycloak unavailability | This auditor grepped `internal/auth/admin_check.go`, `internal/server/router.go:55` mounts it; `cmd/api/main.go:52` threads the checker through; 16 new unit tests in `admin_check_test.go`; live G1.6/G1.7 PASS |
| **I14b** | Orphan user left after SMTP-failed invitation (Medium) — `compensateInvitationCreate` swallowed the cleanup DELETE result with `_ = …` | Made the cleanup loud — logged success/failure via `identity-kc` logger | Stress run (5/5 zero orphans); destructive test I14b PASS |
| **L4** | Audit events not emitted by handlers | 14 emission sites across 13 mutation handlers in `internal/identity/handler.go` + `cmd/api/main.go:44` calls `logging.WireDefault()` | Re-verified by this auditor (test suite + grep) |
| **L5** | SMTP-dependent endpoints returned 502 in dev | Mailpit added to `docker-compose.yml` + `smtpServer` block in `realm-export.json` | Re-verified by this auditor |
| **E1** | `gofmt -l` reported 4 files with drift (RC1 finding) | Reformatted before final tag | This auditor: `gofmt -l .` → empty |

---

## 4. Release routing recommendation

### 4.1 v0.2.1 — Patch release

**Scope:** UI-only fixes for the highest-impact post-tag findings.

| ID | Why patch-tier |
|----|----------------|
| UI-001 | P0; entry-point auth-flow regression in the admin console; ~10-line guard on a single button |
| UI-002 | P0; visible misbehaviour on the most-used SPA view; ~5-line scope of `mount(...)` call to a sub-container |
| UI-003 | P1; user-visible side-effects (duplicate password-reset emails); ~5-line per-button busy guard |
| UI-004 | P1; same shape as UI-003 (duplicate invitation emails); ~5-line per-button busy guard |
| UI-005 | P1; operator confusion; same per-button busy-guard pattern |
| UI-006 | P1; operator confusion; ~5-line request-sequence counter |

**Why patch and not minor:**

- All six fixes live entirely under `web/admin/static/js/`. Zero changes to `internal/*`, swagger, CHANGELOG `## Added`, or any binary artifact.
- No API contract change. The server stays on `0.2.0`.
- Pattern is well-rehearsed (busy-flag / sub-container scoping) — small blast radius, easy to test.
- README explicitly labels `web/admin/` as "dev-only" — but a P0 in the dev console is still a P0; users running this in dev are the ones who would file the issue.

**Tag plan:** `git tag -a v0.2.1` against a commit titled e.g. `fix(admin-ui): guard busy buttons, scope late mounts, sequence API explorer responses`. CHANGELOG `[0.2.1]` entry under `### Fixed` enumerates UI-001–UI-006 with one-line each.

**Validation gate:** Re-run the Playwright `smoke.spec.mjs` + `crud.spec.mjs` against the fixed build; specifically add a new spec that double-clicks each guarded button and asserts only one API call fires. No security re-run needed (security surface unchanged).

### 4.2 v0.3 — Next minor release

**Scope:** Security backlog, P2 UI polish, and any new features pulled in from outside this report.

| Bucket | IDs |
|--------|-----|
| Security hardening (needs new infrastructure) | GAP-2 (backchannel-logout listener OR token revocation cache), GAP-3 (realm-wide `DELETE /admin/sessions` endpoint), GAP-4 (`DecoderConfig.DisallowUnknownFields()` on admin PATCH handlers), F1 (rate-limit middleware), F2 (shorten admin-scope token lifespan), F3 (DPoP if scope warrants) |
| UI hardening (P2 catalog) | UI-007 through UI-013 — client-side validation, 401 centralisation, modal busy state, stacked-modal manager, navigation-bound state, etc. |
| UI polish (P3 catalog) | UI-014 through UI-020 — busy guards on refresh, pager edge cases, toast cap, AbortController on route change, etc. |
| Contract clarity | R02 doc note in the swagger summary / `RELEASE_v0.3.md`. |

**Why minor and not patch:**

- GAP-2 requires either a backchannel-logout listener (new infrastructure, new env, new failure modes) or a server-side token revocation cache (new persistence concern). Both deserve their own design pass.
- GAP-3 adds a new HTTP route (`DELETE /admin/sessions` realm-wide) — by convention, new routes get a minor bump, even if the swagger surface is "additive."
- F1 rate-limiting is a non-trivial cross-cutting middleware decision.
- The UI catalog is broad enough that bundling it cleanly justifies a minor, not a patch.

**Tag plan:** `git tag -a v0.3.0` after a feature branch with at least the GAP-2 / GAP-3 / GAP-4 design landed. CHANGELOG `[0.3.0]` entry under `### Added` / `### Changed` / `### Security`.

**Validation gate:** Full Agent A–D re-run plus a new GAP-2 regression suite analogous to GAP-1's. Re-issue the freeze verification before tagging.

### 4.3 Why this split is defensible

A "ship everything in one v0.3" alternative is also defensible — the GAP-2 fix would be the headline and the UI fixes would ride along. The reason this auditor recommends the split is:

1. **Time-to-fix asymmetry.** The 6 UI bugs are ~5–10 lines each — they can be in a build by end-of-day. GAP-2 alone is a week of design + implementation + regression.
2. **Blast radius asymmetry.** UI fixes are zero-risk to the auth surface. Bundling them with GAP-2 means UI fixes don't ship until GAP-2 lands.
3. **Public signalling.** Tagging v0.2.1 signals "we found two P0s in the admin UI, here's the fix." Bundling into v0.3 hides that signal under a "minor release" headline.

If the project prefers fewer tags over faster fixes, the alternative stands. The split is the auditor's call, not a hard requirement.

---

## 5. Hardening priority order (for the maintainer)

In order, regardless of release routing:

1. **UI-002** (P0) — 5-line `mount` scope fix; lowest-effort highest-impact item in the backlog.
2. **UI-001** (P0) — 10-line login-button busy guard; ships the same patch.
3. **UI-003 / UI-004 / UI-005 / UI-006** (P1) — same per-button busy pattern as UI-001; bundle into the same patch.
4. **GAP-3** — implement `DELETE /admin/sessions` realm-wide; the SPA already exposes the button as `coming-soon`.
5. **GAP-4** — flip admin PATCH handlers to `DecoderConfig{DisallowUnknownFields: true}`; trivial change, hardens audit-log visibility.
6. **GAP-2** — design the backchannel-logout listener or token-revocation cache; the GAP-1 fix already covers the demoted-admin case; this closes the broader "logout invalidates the token" expectation.
7. **F1** — pick a rate-limit middleware (in-process token bucket / leaky bucket, or front the API with a gateway).
8. **UI-007..UI-020** — work through the UI catalog in order of operator impact.
9. **F2 / F3** — only if regulatory/operational scope warrants DPoP or shorter admin-scope tokens.
10. **R02** — one-line API doc note in the role-create swagger summary.

---

## 6. Final GO / NO-GO

| Question | Answer |
|----------|--------|
| Is v0.2.0 itself release-grade? | **YES.** Tagged at `e9c00c9` with GAP-1 closed, audit emitting, CHANGELOG accurate, `make ci` green, `-race` clean. See [FINAL_TAG_REPORT_v2.md](FINAL_TAG_REPORT_v2.md). |
| Does the hardening backlog block v0.2.0? | **NO.** Every finding in this report is either fixed in v0.2.0 (GAP-1, I14b, L4, L5, E1) or is post-tag work targeted at v0.2.1 / v0.3. |
| Are the open P0/P1 findings actionable? | **YES.** UI-001/UI-002 are 5–10-line fixes per the UI_BUGS triage table; UI-003–006 are the same per-button busy pattern. |
| Is the recommended path correct (v0.2.1 patch → v0.3 minor)? | **AUDITOR'S RECOMMENDATION.** See [§4.3](#43-why-this-split-is-defensible) for the trade-off. Alternative (single v0.3) is also defensible. |

```
HARDENING VERDICT:  GO WITH LIMITATIONS
NEXT TAG:           v0.2.1 (UI patch, 6 fixes)
THEN:               v0.3   (security backlog + UI polish)
```

No commits, no pushes, no tags created by this audit.
