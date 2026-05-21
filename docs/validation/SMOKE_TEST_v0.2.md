# Browser Smoke Test — v0.2

**Date:** 2026-05-20
**Tester:** Claude (automated, headless Chromium via Playwright 1.60)
**Target:** IAM Admin Console at `http://localhost:8080/admin`
**Branch:** `milestone/auth-v1`
**Verdict:** **PASS WITH GAPS**

---

## 1. Result summary

| # | Area         | Result | Evidence |
|---|--------------|--------|----------|
| 1 | Stack up     | PASS   | `docker-compose ps` — all four containers healthy |
| 2 | Login (PKCE) | PASS   | `screenshots/03_keycloak_login.png`, `04_admin_post_login.png`, `api/auth_debug.json` |
| 3 | Users (read) | PASS   | `screenshots/06_users.png`, `api/users.json` (3 users returned) |
| 4 | Roles (read) | PASS   | `screenshots/07_roles.png`, `api/roles.json` (5 realm roles) |
| 5 | Sessions (read)    | PASS | `screenshots/08_sessions.png`, `api/sessions.json` (≥9 active sessions) |
| 6 | Invitations (read) | PASS | `screenshots/09_invitations.png`, `api/invitations.json` (1 pending) |

**Verdict rationale:** every visible tab loads, every backing GET returns HTTP 200, the rendered DOM contains rows for each list, and the PKCE login flow exchanges a real access token (1201 bytes) tied to a sub with the `admin` realm role. Marked **PASS WITH GAPS** because write paths (POST/PATCH/DELETE — create user, assign role, revoke session, send invitation, etc.) and the logout round-trip were not exercised. See §6.

---

## 2. Environment

```
docker-compose ps
NAME                     STATUS
saas-api                 Up 43 minutes
saas-keycloak            Up 10 hours (healthy)
saas-keycloak-postgres   Up 10 hours (healthy)
saas-postgres            Up 10 hours (healthy)
```

| Endpoint | Probe | Status |
|----------|-------|--------|
| `GET http://localhost:8080/health`                          | `curl` | `200 {"status":"ok"}` |
| `GET http://localhost:8080/admin`                           | `curl` | `200` (HTML shell) |
| `GET http://localhost:8080/admin/config.json`               | `curl` | `200` (realm=saas, clientId=saas-dev-playground) |
| `GET http://localhost:8081/realms/saas/.well-known/openid-configuration` | `curl` | `200` (issuer=http://localhost:8081/realms/saas) |

Seed credentials used: `adminuser` / `password` (from `.env` `SEED_USER_PASSWORD`, member of realm role `admin`).

---

## 3. Test method

A headless Chromium browser (Playwright 1.60 / Chromium-headless-shell 148) was driven through the full flow that a human operator would follow:

1. `GET /admin` → boot the SPA shell.
2. Click into `/playground` and trigger `startLogin()` (PKCE Authorization Code flow against Keycloak at `http://localhost:8081/realms/saas`).
3. On the Keycloak-rendered login page, fill `username` / `password` and submit the form.
4. Follow the redirect back to `/admin?code=...&state=...`; the SPA exchanges the code at `/realms/saas/protocol/openid-connect/token`; the resulting `access_token` lands in `sessionStorage[kc_admin_access_token]`.
5. For each tab — `/overview`, `/users`, `/roles`, `/sessions`, `/invitations` — navigate, screenshot, and intercept every `/admin/*` and `/auth/*` HTTP response.
6. Re-issue each GET inside the page context with the live bearer token and dump the raw JSON payload to `docs/evidence/api/*.json` for inspection.

The Playwright driver script lives at `/tmp/smoketest_v02/smoke.spec.mjs` (outside the repo on purpose — scope only allows writes to `docs/SMOKE_TEST_v0.2.md` and `docs/evidence/**`).

---

## 4. Evidence index

### Screenshots — `docs/evidence/screenshots/`
| File | Step |
|------|------|
| `01_admin_boot.png`           | SPA shell rendered, sidebar visible, no auth yet |
| `02_playground_pre_login.png` | Playground tab before login |
| `03_keycloak_login.png`       | Keycloak-rendered login form on `localhost:8081` |
| `04_admin_post_login.png`     | Post-callback admin landing (`#/overview`) |
| `05_overview.png`             | Overview tab |
| `06_users.png`                | Users tab — table with 3 rows |
| `07_roles.png`                | Roles tab — table with 10 rendered rows |
| `08_sessions.png`             | Sessions tab — table with active sessions |
| `09_invitations.png`          | Invitations tab — 1 pending invite (`user@example.com`) |

### Raw API payloads — `docs/evidence/api/`
| File | Endpoint | Result |
|------|----------|--------|
| `users.json`       | `GET /admin/users?max=20`  | `200` — 3 users (`adminuser`, `testuser`, `user@example.com`) |
| `roles.json`       | `GET /admin/roles`         | `200` — 5 realm roles (`admin`, `user`, plus 3 built-in) |
| `sessions.json`    | `GET /admin/sessions`      | `200` — ≥9 active sessions, includes `adminuser` + `testuser` |
| `invitations.json` | `GET /admin/invitations`   | `200` — 1 pending invitation |
| `auth_debug.json`  | `GET /auth/debug`          | `200` — `valid:true`, `roles:["admin","user"]`, `azp:saas-dev-playground` |
| `me.json`          | `GET /me`                  | `200` — local user row matched to Keycloak sub |

### Run log — `docs/evidence/console_log.txt`
Full chronological log including the live Keycloak auth URL with PKCE challenge and every captured network response.

### Structured results — `docs/evidence/results.json`
Per-area pass/fail with intercepted call list and rendered-row counts.

---

## 5. Detailed per-area observations

### 5.1 Login (PASS)
- `startLogin()` redirected to `http://localhost:8081/realms/saas/protocol/openid-connect/auth?response_type=code&client_id=saas-dev-playground&...&code_challenge=...&code_challenge_method=S256`.
- After credential submit, the browser landed at `http://localhost:8080/admin#/overview`.
- `sessionStorage[kc_admin_access_token]` was populated (1201 chars).
- `/auth/debug` confirmed `valid:true`, `received_azp:saas-dev-playground`, `roles:["admin","user"]` — the admin gate (`auth.RequireRole("admin")`) is satisfied.

### 5.2 Users (PASS, read-only)
- Tab fired `GET /admin/users?max=20` → `200`.
- SPA rendered 3 rows.
- Payload includes seeded `adminuser` and `testuser` plus the `user@example.com` invitee, fields `id/username/email/first_name/last_name/enabled/email_verified/created_at`.

### 5.3 Roles (PASS, read-only)
- `GET /admin/roles` → `200`.
- Payload contained 5 realm roles: `admin`, `user`, `default-roles-saas`, `offline_access`, `uma_authorization`.
- 10 row-elements rendered (rows + auxiliary list elements).

### 5.4 Sessions (PASS, read-only)
- `GET /admin/sessions` → `200`.
- 9–10 active SSO sessions returned, all containing `id/user_id/username/ip_address/started_at/last_access/clients`.
- Confirms the admin client can list cross-user sessions via the Keycloak admin API.

### 5.5 Invitations (PASS, read-only)
- `GET /admin/invitations` → `200`.
- 1 pending invitation (`user@example.com`, required actions `VERIFY_EMAIL`, `UPDATE_PASSWORD`).
- 2 row-elements rendered (header + invite row).

### 5.6 Overview (PASS)
- Tab rendered without error; no admin API calls required.

---

## 6. Gaps & remaining risks

The mission scope said "test users / roles / sessions / invitations" without specifying CRUD coverage. The areas below were **not** exercised:

| Gap | Why | Suggested next pass |
|-----|-----|---------------------|
| Write paths: `POST /admin/users/invite`, `POST /admin/roles`, `PATCH /admin/users/:id`, `POST /admin/users/:id/roles`, `POST /admin/users/:id/reset-password`, `DELETE /admin/users/:id`, `DELETE /admin/users/:id/sessions`, `DELETE /admin/roles/:name`, `DELETE /admin/sessions/:id`, `DELETE /admin/invitations/:id`, `POST /admin/invitations/:id/resend` | Smoke scope kept read-only to avoid mutating the seed dataset another agent may be relying on | Run a second smoke pass against a disposable Keycloak realm with full CRUD chains |
| `logout()` (Keycloak end-session round-trip) | Not on the task list; would have invalidated the captured session | Add as a separate test |
| 401/403 negative tests (no token, wrong role) | Out of scope (positive-path smoke only) | Add to integration suite, not smoke |
| Pagination, search, filter UI controls on `/users` | Not requested | Add to v0.3 smoke once write paths are covered |
| Browser console errors / warnings | Captured none during the run (`errors` array empty) | — |

No bugs surfaced. Three contextual observations (not regressions):

- `users.json` reports `created_at:"0001-01-01T00:00:00Z"` for `adminuser` and `testuser` — the seeded users come without a `createdTimestamp` from the realm import; the live invitee (`user@example.com`) has a proper timestamp. Cosmetic.
- Tab navigation triggers a refetch on every visit (no client-side cache). Expected for v0.2.
- Sessions tab listed 9 sessions where the smoke driver added 2 — the dataset already had pre-existing sessions; not a leak from this run.

---

## 7. Files changed

- `docs/SMOKE_TEST_v0.2.md`               — this report
- `docs/evidence/results.json`            — structured per-area results
- `docs/evidence/console_log.txt`         — full smoke driver log
- `docs/evidence/screenshots/01..09_*.png` — 9 full-page screenshots
- `docs/evidence/api/{users,roles,sessions,invitations,auth_debug,me}.json` — raw API payloads

No code or config under `internal/**`, `web/admin/**`, `scripts/**`, `README.md` was modified.

---

## 8. Scope completion

**PASS WITH GAPS** — every requested tab loads and exchanges a real admin token end-to-end through the browser; write paths and logout left for the next pass.
