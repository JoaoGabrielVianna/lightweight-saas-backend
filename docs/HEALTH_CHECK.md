# Health Check

**Last updated:** 2026-07-27

How to answer "is this project healthy right now?" — for a returning
contributor, a reviewer, or an operator after a deploy.

Three tiers by budget. Start at the top and go deeper only if something fails.

| Tier | Command | Time | Needs |
|---|---|---:|---|
| **1 — Code** | `make ci` | **~5 s** | Go toolchain |
| **2 — Full local** | `make ci-full` | **~10 s** | + Node 20 |
| **3 — Live stack** | `make up && make e2e` | **~3–5 min** | + Docker |

Timings measured 2026-07-27 on Apple Silicon with a warm module cache and a
cold test cache. CI is slower — it starts cold every run.

---

## Tier 1 — Code health (~5 s)

```bash
make ci
```

Runs, in order: `fmt-check` → `vet` → `lint` → `build` → `test` →
`swagger-check` → `check-docs`. This is exactly what CI job `gate` enforces,
so a green run here means a green run there.

**Expected output ends with:**

```
  + swagger.{json,yaml,docs.go} match annotations
  + docs links OK (62 markdown files, 0 broken)
  + CI checks passed
```

### Per-step budget

| Step | Time | Fails when |
|---|---:|---|
| `fmt-check` | <1 s | A file needs `gofmt`. Fix: `make fmt` |
| `vet` | <1 s | Compile-adjacent correctness problem |
| `lint` | ~1 s | One of 9 blocking linters fired ([.golangci.yml](../.golangci.yml)) |
| `build` | ~2 s | Compilation error |
| `test` | ~4 s | A unit test failed |
| `swagger-check` | <1 s | Handler annotations changed without `make docs` |
| `check-docs` | <1 s | A broken doc link, or a documented number no longer matches the code |

If `lint` fails on a fresh clone with *"golangci-lint not installed"*, run
`make lint-install` once.

---

## Tier 2 — Full local verification (~10 s)

```bash
make ci-full
```

Adds the coverage floor and the admin console tests.

| Check | Command | Expect |
|---|---|---|
| Coverage floor | `make coverage-gate` | `+ coverage 73.2% (floor 73.0%)` |
| Admin console | `make test-frontend` | `# pass 30` / `# fail 0` |

> Coverage is measured with `-count=1`. Without it Go serves cached
> per-package results and the aggregate is wrong — a cached run once reported
> 69.1% for a tree that measured 74.1%.

---

## Tier 3 — Live stack (~3–5 min)

Needed only when changing auth, the Keycloak integration, or deployment
configuration.

```bash
make doctor        # toolchain, docker daemon, port conflicts   (~5 s)
make up            # build + start the 5-container stack        (~2–4 min first run)
make e2e           # wait for readiness, then auth smoke test   (~30 s)
```

### Container health

```bash
docker compose ps
```

All five must be `Up`, and the four with healthchecks must be `(healthy)`:

| Container | Port | Health |
|---|---|---|
| `saas-api` | 8080 | no healthcheck — verify with `/health` below |
| `saas-postgres` | 5432 | `pg_isready` |
| `saas-keycloak-postgres` | 5433 | `pg_isready` |
| `saas-keycloak` | 8081 | `/health/ready` (start period 30 s — slow first boot is normal) |
| `saas-mailpit` | 8025 / 1025 | `/readyz` |

### Endpoint probes

```bash
curl -s localhost:8080/health                       # {"status":"ok"}
curl -si localhost:8080/me                          # 401 without a token
curl -si localhost:8080/admin/users                 # 401 without a token
make auth-test                                      # acquire a token + call /me → 200
```

> `/health` is **liveness only** — it checks neither the database nor Keycloak
> ([KI-012](KNOWN_ISSUES.md#ki-012)). A 200 here does not mean the service can
> serve traffic. Use `make auth-test` for that, since it exercises Keycloak,
> JWT validation, and the database write path in one call.

### Database

```bash
docker exec saas-postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c '\d users'
```

Expect one table, `users`, with a unique index on `keycloak_sub`. That is the
entire schema this service owns — everything else lives in Keycloak's own
database.

### Authentication chain

`make auth-test` proves the whole chain end to end: Keycloak reachable → token
issued → JWKS fetched → signature verified → `iss`/`azp` accepted → identity
stored → user row created just-in-time. A 200 means every link works.

If it fails, the fastest diagnosis is the dev playground:

```bash
# with DEV_PLAYGROUND_ENABLED=true
curl -s "localhost:8080/dev/auth/debug?token=$TOKEN" | jq '{valid, reason, issuer, allowed_clients}'
```

`/dev/auth/debug` is the **only** endpoint that explains *why* a token was
rejected — the authenticated `/auth/debug` cannot, because middleware rejects
a bad token before the handler runs.

### Security probes (optional, ~1 min)

Three black-box scripts exist and run against the live stack:

```bash
./scripts/security_live_check.sh       # public/protected/role-gated surfaces
./scripts/security_gap1_check.sh       # live-admin revocation (GAP-1)
./scripts/security_advanced_check.sh   # rate limiting, replay, escalation
```

These are **not** in CI — they need the full stack. Run them before a release
(see [RELEASE_CHECKLIST.md](RELEASE_CHECKLIST.md)) or after touching auth.

---

## Reading the result

| Symptom | Likely cause | Where to look |
|---|---|---|
| `make ci` fails at `lint` on a fresh clone | Binary not installed | `make lint-install` |
| `swagger-check` fails | Handler annotations changed | `make docs && git add docs/` |
| `check-metrics` fails | A documented number drifted | Re-derive it — [PROJECT_STATUS.md §Metrics](PROJECT_STATUS.md#metrics) |
| `check-links` fails | A doc references something absent | Fix the link or restore the target |
| Coverage below floor | Tests removed, or untested code added | `go tool cover -html=coverage.out` |
| Keycloak never healthy | Slow first boot, or realm import failed | `docker logs saas-keycloak` |
| `auth-test` returns 401 | `iss` mismatch — the classic setup error | `KEYCLOAK_URL` must match the URL clients use, not the docker-internal one |
| `/admin/*` returns 404 | Admin client unconfigured | `KEYCLOAK_ADMIN_CLIENT_ID` + `..._SECRET` — 404 is intentional here (AD-004) |
| API container exits on boot | Config validation or JWKS fetch failed | `docker logs saas-api` — startup is fail-fast by design |

---

## What this cannot tell you

Be honest about the blind spots — a green health check is not proof of a
working system.

- **No runtime authorization signal.** The authorization boundary itself is now
  covered end to end — the guards, the scope matrix and the tenant boundary all
  have real-stack negative evidence
  ([security/AUTHORIZATION_MATRIX.md](security/AUTHORIZATION_MATRIX.md),
  [KI-018](KNOWN_ISSUES.md#ki-018) closed) — but a health check still cannot
  tell you whether refusals are SPIKING. Authorization failures reach the
  security event channel; nothing aggregates them.
- **No runtime metrics.** There is no `/metrics`, no tracing, no error-rate
  signal. Health in production means reading logs
  ([TD-009](TECH_DEBT.md#td-009)).
- **No readiness probe.** `/health` cannot distinguish "process up" from
  "process able to serve".
- **`docker-compose.yml` does not carry the production config.** Four
  environment variables are missing, so the documented production recipe is
  not reproducible from it ([TD-004](TECH_DEBT.md#td-004)).

---

## Quick reference

```bash
make ci             # code health                       ~5 s
make ci-full        # + coverage + frontend             ~10 s
make doctor         # toolchain and port diagnostics    ~5 s
make up             # start the stack                   ~2–4 min
make e2e            # readiness + auth smoke test       ~30 s
make auth-test      # token + /me → 200                 ~5 s
make logs           # tail all services
make reset-dev      # nuke and rebuild (DATA LOSS)      ~3–5 min
```
