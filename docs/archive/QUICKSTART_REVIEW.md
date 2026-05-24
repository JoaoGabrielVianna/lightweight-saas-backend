# Quick Start Review

> **Subject:** [`docs/getting-started/QUICKSTART.md`](../getting-started/QUICKSTART.md)
> **Reviewer role:** Senior Developer Experience Auditor.
> **Method:** every factual claim cross-checked against
> [`Makefile`](../../Makefile),
> [`docker-compose.yml`](../../docker-compose.yml),
> [`.env.example`](../../.env.example),
> [`config/project.json`](../../config/project.json),
> [`deploy/keycloak/realm-export.json`](../../deploy/keycloak/realm-export.json),
> [`internal/server/*`](../../internal/server),
> [`internal/bootstrap/*`](../../internal/bootstrap),
> [`README.md`](../../README.md),
> [`docs/getting-started/KEYCLOAK_SETUP.md`](../getting-started/KEYCLOAK_SETUP.md),
> [`docs/architecture/bootstrap.md`](../architecture/bootstrap.md),
> [`CHANGELOG.md`](../../CHANGELOG.md).
>
> **Outcome:** three factual issues found and corrected in `getting-started/QUICKSTART.md`
> in this same pass. The doc is now consistent with the code.

---

## 1. Verdict at a glance

| Axis              | Verdict | Notes                                                                 |
|-------------------|---------|-----------------------------------------------------------------------|
| Completeness      | **PASS** | Linear path clone → configure → run → login → reach `/admin` works without external help, given the seeded `adminuser` / `password` credentials. |
| Accuracy          | **PASS (post-fix)** | 1 fabrication + 2 minor errors found, all corrected in this pass. |
| Consistency       | **PASS** | No contradictions with README, KEYCLOAK_SETUP, bootstrap, CHANGELOG after fixes. |
| Missing sections  | **PARTIAL** | Backup is mentioned in one sentence; upgrade, rollback, monitoring, and prod secrets-management have no coverage. Detail below. |
| DX score          | **7.5 / 10** | Strong baseline. Loses points for cross-origin honesty deferred to §12, no upgrade/rollback story, no observability hook. |

---

## 2. Completeness — PASS

**Question asked:** can a new engineer `git clone → configure → run →
login → reach /admin` without external help?

**Walk-through against the doc:**

| Step                  | Section | Verified against                                  | Outcome |
|-----------------------|---------|---------------------------------------------------|---------|
| Clone                 | §4      | `git clone` — generic                             | ✓       |
| Toolchain check       | §3      | `make doctor` exists ([Makefile:53](../../Makefile)) | ✓       |
| Configure             | §4 + §5 | `make init` exists ([Makefile:276](../../Makefile)); env vars match `.env.example` post-fix | ✓ |
| Run                   | §4 + §6 | `make up` exists ([Makefile:165](../../Makefile)); 5 compose services verified | ✓ |
| Smoke test            | §4      | `make auth-test` exists ([Makefile:288](../../Makefile)); expected `/me` JSON matches handler shape in [`internal/user/dto.go`](../../internal/user/dto.go) | ✓ |
| Login (browser)       | §9      | `/admin` console mounted by [`internal/server/admin.go`](../../internal/server/admin.go) when `DEV_PLAYGROUND_ENABLED=true` (default for local) | ✓ |
| Reach `/admin/*`      | §9      | `adminuser` carries the `admin` realm role per [`config/project.json`](../../config/project.json); RBAC verified live by `RequireLiveAdmin` per CHANGELOG | ✓ |

**Pass conditions met.** A reader following §4 → §9 reaches a green
`/admin/users` call with no out-of-band knowledge required.

---

## 3. Accuracy — issues found and fixed

### 3.1 FABRICATED — CORS middleware (§12 step 5) — **fixed**

**Original claim:**

> The Go API's CORS configuration lives in `internal/server/router.go`.
> Local dev already permits `http://localhost:3000` and similar.

**Reality:**

```
$ grep -rnE "CORS|AllowOrigins|Access-Control-Allow" internal/
(no output)
```

There is **no CORS middleware** in this repository. A SPA on
`http://localhost:3000` calling `http://localhost:8080/me` will be blocked
by the browser's same-origin policy on the preflight response.

**Impact if shipped uncorrected:** anyone following §12 to integrate
a separate-origin frontend would hit a browser CORS error and waste an
hour searching for a config that does not exist.

**Fix applied to §12:** the false claim has been removed and replaced
with three concrete paths forward (same-origin serving, reverse proxy,
or adding CORS middleware as a code change) plus an explicit "this
repo does not ship CORS" callout. §13 troubleshooting table also gained
a row pointing back to §12 for the browser CORS symptom.

### 3.2 STALE DEFAULT — `DB_URL` in §5 env-var table — **fixed**

**Original claim:** the `DB_URL` row's Default column read
`local: postgres:5432`.

**Reality:** [`.env.example:11`](../../.env.example#L11) sets
`DB_URL=postgres://postgres:postgres@localhost:5432/...` — the host-facing
local default uses `localhost`, not `postgres`. The `postgres:5432` form
only exists inside the docker-compose override
([`docker-compose.yml:118`](../../docker-compose.yml#L118)).

**Impact:** would mislead a reader running the API on the host with
`go run` into using the wrong DSN.

**Fix applied:** Default cell now reads `localhost:5432 (host)`; the
purpose cell clarifies that the api **container** overrides to
`postgres:5432`.

### 3.3 MISSING CAVEAT — `/auth/debug` always available (§10) — **fixed**

**Original claim:** §10 listed `curl /auth/debug` as a routine local
verification with no precondition.

**Reality:** [`internal/server/playground.go:67`](../../internal/server/playground.go#L67)
mounts `/auth/debug` **only** when `DEV_PLAYGROUND_ENABLED=true`. The
endpoint is intentionally dev-gated — the same gate as `/admin` and
`/dev/auth`.

**Impact:** a reader who set `DEV_PLAYGROUND_ENABLED=false` (e.g. to
test prod-like config) would see a 404 and assume the doc was wrong
about something deeper.

**Fix applied:** the curl example now carries a one-line
`# DEV-ONLY: this endpoint exists only when DEV_PLAYGROUND_ENABLED=true.`
comment.

### 3.4 Items spot-checked and confirmed correct (no changes)

| Section | Claim | Source verified |
|---------|-------|-----------------|
| §1      | Go 1.25, Postgres 15, Keycloak 26, Mailpit 1.20 | [`docker-compose.yml`](../../docker-compose.yml) |
| §1      | "No `/login` or `/register` endpoints"          | [`internal/server/router.go`](../../internal/server/router.go); confirmed in [README §API surface](../../README.md) |
| §2      | `users.keycloak_sub` unique-indexed, JIT provisioning | [README §Architecture](../../README.md); [`internal/user/repository.go`](../../internal/user/repository.go) |
| §3      | `make doctor` toolchain probe                   | [`Makefile:53`](../../Makefile) |
| §4      | Expected `/me` JSON shape                        | [`internal/user/dto.go`](../../internal/user/dto.go); [README §Example](../../README.md) |
| §5      | Every env-var default                            | [`.env.example`](../../.env.example) line-by-line |
| §6      | 5 containers + their host ports                  | [`docker-compose.yml`](../../docker-compose.yml) line-by-line |
| §6      | Lifecycle matrix                                 | [`Makefile`](../../Makefile); [`docs/architecture/bootstrap.md`](../architecture/bootstrap.md) |
| §7      | 3 OIDC clients (`saas-backend`, `saas-backend-admin`, `saas-dev-playground`) | [`deploy/keycloak/realm-export.json`](../../deploy/keycloak/realm-export.json) lines 6, 23, 39 |
| §7      | Realm SMTP → `mailpit:1025`                      | `realm-export.json` lines 77–79 |
| §7      | `saas-dev-playground` is public + PKCE           | `realm-export.json` line 45 (`publicClient: true`) |
| §8      | `make init` / `make regen` semantics             | [`internal/bootstrap/generate.go`](../../internal/bootstrap/generate.go); [`docs/architecture/bootstrap.md`](../architecture/bootstrap.md) |
| §9      | Seed user credentials + roles                    | [`config/project.json`](../../config/project.json) |
| §9      | Promoting via admin console (Users → Roles)      | [`web/admin/static/js/views/user-detail.js`](../../web/admin/static/js/views/user-detail.js) |
| §11     | Disable Direct Access Grants on `saas-backend`   | `realm-export.json:7` (`directAccessGrantsEnabled: true`) |
| §11     | Keycloak `start --optimized` for prod            | Keycloak 26 documented runtime mode |
| §12 step 1-3 | Public client + PKCE registration steps    | Standard Keycloak admin-UI flow; `KEYCLOAK_ALLOWED_CLIENT_IDS` whitelist enforced by [`internal/auth/keycloak/provider.go`](../../internal/auth/keycloak/provider.go) |
| §13     | `KEYCLOAK_JWKS_URL` docker-internal hostname     | [`docker-compose.yml:126`](../../docker-compose.yml#L126) |
| §13     | `make realm-reset` semantics                     | [`Makefile:252`](../../Makefile) |

---

## 4. Consistency — PASS

Cross-checks against the other long-form docs and the changelog:

| Compared with | Conflict found? | Notes |
|---------------|-----------------|-------|
| [`README.md`](../../README.md) | No | Both call `make up` the canonical bring-up command, both reach for `make auth-test` as the smoke verification, both name the same 3 OIDC clients. |
| [`getting-started/KEYCLOAK_SETUP.md`](../getting-started/KEYCLOAK_SETUP.md) | No | QUICKSTART links into §2 for full env-var coverage, §9 for full troubleshooting, §10 for the full hardening checklist — does not re-claim those tables. |
| [`architecture/bootstrap.md`](../architecture/bootstrap.md) | No | The "make regen overwrites generated files" guidance in §8 of QUICKSTART matches the canonical anti-pattern list in `architecture/bootstrap.md`. |
| [`CHANGELOG.md`](../../CHANGELOG.md) | One omission, no conflict | QUICKSTART omits two env vars added in v0.2 (`ADMIN_LIVE_CHECK_TTL_SECONDS`, `KEYCLOAK_ADMIN_BASE_URL`). It is deliberate scoping — §5 frames its table as "the minimum you need to understand on day one" and links to `KEYCLOAK_SETUP §2` for full coverage. Acceptable but flagged in §5 below. |

**No contradictions found.** The doc graph is internally consistent.

### 4.1 Minor consistency observation (not corrected)

`docs/getting-started/KEYCLOAK_SETUP.md` opens with "Tested against the state of the
repo at commit `Sprint 3`" — that line predates the v0.2.0 tag and is
slightly out of date relative to CHANGELOG. Not in scope for this
review (QUICKSTART does not propagate the claim), but worth refreshing
in a future pass.

---

## 5. Missing sections — PARTIAL

The brief asked specifically about: **backup, upgrade, rollback, prod
secrets, TLS, monitoring**. Current state of each:

| Topic              | Coverage in getting-started/QUICKSTART.md                         | Gap |
|--------------------|---------------------------------------------------|-----|
| **Backup**         | §11 item 7 — one sentence: "Snapshot them with your provider's tooling, or `pg_dump` on a schedule." | No concrete `pg_dump` / `pg_restore` recipe, no mention of the realm-export sidecar pattern as a backup of Keycloak config, no retention guidance. |
| **Upgrade**        | None | No story for upgrading Keycloak (26 → 27), Postgres major versions, or the Go API binary. Implicit assumption: replace the image tag and pray. |
| **Rollback**       | None | No mention of which artifacts are safe to roll back (the API binary is; the database is not, without a snapshot). No "test rollback before you need it" reminder. |
| **Prod secrets**   | §11 item 1 + §14 — lists what to rotate. | No guidance on *where* to store rotated secrets (Vault / SOPS / AWS Secrets Manager / Doppler / sealed-secrets), no rotation-without-downtime story, no warning that `make regen` will overwrite `.env` (it preserves secrets but does not preserve hand-edits to other lines). |
| **TLS**            | §11 item 6 — Caddy snippet. | No Let's Encrypt renewal note, no mention that Keycloak's `iss` claim must match the **HTTPS** URL once TLS is on (a re-export of the realm or `KC_HOSTNAME` change is required), no nginx alternative. |
| **Monitoring**     | None | The codebase exposes `auth.SetEventHook` (per README §What's in the box) explicitly so Prometheus / OTel can attach without touching middleware. QUICKSTART never mentions it. No `/health` scraping guidance, no Keycloak metrics hint, no log-shipping pointer. |

### 5.1 Additional gaps I noticed (not in the original brief)

| Topic | Why it matters | Suggested home |
|-------|----------------|----------------|
| **Audit-log access** | v0.2 added audit-event emission on every admin mutation (CHANGELOG `Security` bullet). The admin console has an "Audit Logs" view at [`web/admin/static/js/views/auditlogs.js`](../../web/admin/static/js/views/auditlogs.js). Adopters need to know it exists. | New paragraph in §10 (or §9 "First admin"). |
| **CI / non-interactive bootstrap** | `make init` is interactive — fine for a developer laptop, blocks any CI green-field provisioning. `cmd/bootstrap -non-interactive` is documented in `architecture/bootstrap.md` but not surfaced in QUICKSTART. | Sub-bullet in §8. |
| **Windows / WSL** | The doc implicitly assumes macOS/Linux shells. `fish`/`zsh`/`bash` differences are negligible, but Windows users need WSL. | One-line callout in §3 Requirements. |
| **Port-conflict escape hatch** | `KC_HOST_PORT` (compose) lets an adopter move Keycloak off 8081 without `make regen`. Useful when 8081 is taken. | Troubleshooting entry in §13. |
| **Extending the API** | "How do I add my own protected route?" is the obvious next question. No pointer to where to grow the codebase. | "Next steps" sub-bullet linking to `internal/server/router.go` and an example existing handler. |
| **Time-to-first-token estimate** | The doc opens with "~10 minutes" and "~60 seconds on the first run" — both are reasonable but unverified estimates. | Either benchmark and pin, or downgrade to "the first `make up` is image-pull-bound and may take a minute or two." |

---

## 6. DX score — 7.5 / 10

Breakdown:

| Dimension                                    | Score | Notes |
|----------------------------------------------|-------|-------|
| Linear path (clone → run → reach `/admin`)    | 9 / 10 | Single command stream, no detours. Loses 1 for the `make init` interactive blocker (no CI mode surfaced). |
| Copy-paste correctness                        | 9 / 10 | After the §5/§10/§12 fixes, every shell block runs as written. Loses 1 because the "~10 min" claim is unverified. |
| Diagrams                                      | 8 / 10 | One ASCII architecture diagram + one token-flow diagram + one bootstrap diagram. All readable. No multi-tenant diagram (defensible: feature isn't wired), no admin-RBAC diagram (would help). |
| Env-var coverage                              | 7 / 10 | The "minimum 8" framing works, but two v0.2 admin-client vars (`KEYCLOAK_ADMIN_BASE_URL`, `ADMIN_LIVE_CHECK_TTL_SECONDS`) are mentioned nowhere — adopters disabling the dev playground won't know they exist. |
| Troubleshooting depth                         | 8 / 10 | 11 symptom rows post-fix cover the high-frequency failures. No row for "Keycloak port 8081 conflicts with my other dev tool" (the `KC_HOST_PORT` escape hatch). |
| Honesty about prod gaps                       | 8 / 10 | §11 calls itself "the floor, not a runbook" — good. §14 lists every dev-only default. Loses 2 for no upgrade / rollback / monitoring story. |
| Forward navigation                            | 8 / 10 | "Next steps" + cross-links into KEYCLOAK_SETUP / bootstrap / INDEX / security. No pointer into the admin console's audit-log view. |
| Self-contained execution                      | 8 / 10 | A reader can finish §1–§10 without opening any other file. §11/§12 need code/config decisions that link out, which is correct scoping but reduces self-containment. |

**Composite:** 7.5 / 10.

> Reference floors: a typical first-pass IAM-kit Quick Start in this
> codebase shape would score ~5 / 10 (single dense file, no diagrams,
> no troubleshooting). A best-in-class one (Supabase, Auth0 quickstart)
> scores ~9 / 10 — concrete benchmarks, video walkthrough, working CI
> recipe, no copy-paste landmines. This doc lands above the median,
> below the top tier — the remaining points come from upgrade/rollback,
> monitoring, and a stable benchmark.

---

## 7. Remaining onboarding gaps (prioritized)

Highest-leverage additions, in the order I'd ship them:

1. **Audit-log access paragraph** in §9 or §10. The feature exists and
   is hidden. One paragraph + a link unblocks adopters from re-building
   audit visibility they already have.

2. **Upgrade & rollback section** (new §11.5 or §15). Cover: which
   artifacts are stateless (api binary), which are stateful (both
   postgres volumes), the right order to upgrade (Keycloak realm export
   first, then image bump, then api), and the truth that you can't
   roll back a database without a snapshot.

3. **Backup recipe** in §11. A concrete `pg_dump` invocation for each
   postgres, plus the realm-export sidecar (`make keycloak-export` —
   exists at [`Makefile:243`](../../Makefile)), plus a one-line `cron`
   suggestion. Five lines of doc, weeks of grief avoided.

4. **Monitoring hook** in §10 or §11. One paragraph naming
   `auth.SetEventHook` (already referenced in README), Keycloak's
   `KC_HEALTH_ENABLED` (already in compose), and a "this is where you
   attach Prometheus / OTel" sentence.

5. **Prod secrets-management pointer** in §11 item 1. Don't pick a
   tool, but name the category (Vault / SOPS / cloud secret manager /
   sealed-secrets) and warn that hand-editing `.env` in prod is
   incompatible with `make regen`.

6. **`make init` non-interactive escape hatch** in §8. Single line:
   "For CI, `go run ./cmd/bootstrap -non-interactive
   -admin-password=$KC_ADMIN_PASS`." Already exists in code, not yet
   in QUICKSTART.

7. **CORS middleware decision** — out of scope for this fix (no code
   changes allowed), but the right next move for the kit. The §12
   correction made the gap honest; closing it is a small code change
   in `internal/server/router.go`.

---

## 8. What was changed in this pass

Three edits to [`docs/getting-started/QUICKSTART.md`](../getting-started/QUICKSTART.md) — content only, no
code, no routes, no auth:

| # | Section | Change |
|---|---------|--------|
| 1 | §5 — Env-var table, `DB_URL` row | Default column corrected from `local: postgres:5432` to `localhost:5432 (host)`; purpose cell clarifies that the override to `postgres:5432` is the api **container's** behavior. |
| 2 | §10 — Verification curls | `/auth/debug` curl now carries a `# DEV-ONLY: this endpoint exists only when DEV_PLAYGROUND_ENABLED=true.` comment. |
| 3 | §12 — Integrating another frontend | The false "Local dev already permits `http://localhost:3000`" CORS claim removed. Replaced with an honest "this repo does not ship CORS middleware" callout and three concrete paths forward (same-origin serving, reverse proxy, adding CORS as a code change). A matching row was added to §13 troubleshooting. |

No code changed. No auth touched. No routes created. No commits made.
