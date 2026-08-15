# Running LIGHTWEIGHT

**Last updated:** 2026-08-15

This is the operational reference: what to configure, how to tell whether it is
working, what happens when it stops, and what you have to keep in order to
restore it.

It targets a **single VPS running docker compose**, which is the deployment this
product is for. Nothing here needs Kubernetes and none of it is written
assuming you have it.

---

## 1. The shortest path that works

From a clean machine with Docker installed. No Go toolchain, no prompts.

```sh
git clone <repo> && cd lightweight-saas-backend

# Copies .env.example, generates the secrets keyring and a database password,
# and refuses to touch an existing .env. Safe to re-run; idempotent for IaC.
./scripts/init.sh --keycloak-url https://sso.example.com \
                  --realm lightweight --console-client-id lightweight-console

# Postgres + LIGHTWEIGHT, against the Keycloak named above:
docker compose up -d

# Ready means: migrations applied, database reachable, accepting traffic.
curl -fsS localhost:8080/health/ready
```

Your Keycloak needs one public client and one user holding the realm role
`admin` **before** that first boot — the fields are listed in
[KEYCLOAK_SETUP.md §0.1](../getting-started/KEYCLOAK_SETUP.md#01-the-installation-realm--needed-before-first-boot).

Evaluating instead of installing? Drop the flags and add the profile, and a
throwaway Keycloak comes with it, already holding an operator account:

```sh
./scripts/init.sh
docker compose --profile dev-idp up -d      # sign in at /admin as adminuser / password
```

The secrets keyring is the one value with no safe default: without it the
connection and workspace-identity surfaces are not mounted at all, so there is
no way to attach a realm to a workspace. `init.sh` generates it. **Back it up
separately from the database** — a dump restored without it contains provider
credentials nobody can decrypt.

Then follow [§9 Production smoke](#9-production-smoke) to prove the whole path
end to end with a real Project Credential.

If a required value is missing or malformed, the process **refuses to start and
says which one and why** — see [§3](#3-fail-fast). It will not start
half-configured.

---

## 2. Configuration contract

Every environment variable this installation involves. Generated from
`internal/config/contract.go`, which is also what `Validate` enforces and what
`contract_test.go` checks `.env.example` and `docker-compose.yml` against — so
this table cannot be out of date while the build is green.

**Consumer** is who reads it. `process` is the API; `compose` is the reference
deployment building the services around it; `bootstrap` is the seeding CLI.
A `compose` variable is not read by the API and setting it in a production
environment does nothing.

| Variable | Consumer | Required | Default | Secret | Purpose |
|---|---|---|---|:--:|---|
| `ADMIN_CONSOLE_CLIENT_ID` | process | optional | DEV_PLAYGROUND_CLIENT_ID |  | public PKCE client the console logs in with |
| `ADMIN_CONSOLE_ENABLED` | process | optional | false |  | serve the admin console SPA at /admin |
| `ADMIN_LIVE_CHECK_TTL_SECONDS` | process | optional | 30 |  | how long a live-admin check is cached; bounds the out-of-band revocation window |
| `API_HOST_PORT` | compose | optional | 8080 |  | host port the API is published on; PORT is the port inside the container |
| `AUDIT_RETENTION_DAYS` | process | optional | 90 |  | how long durable audit history is kept before a daily sweep deletes it |
| `CORS_ALLOWED_ORIGINS` | process | optional | — |  | comma-separated browser origins allowed to call the API; empty disables CORS |
| `DB_MIGRATE_ON_BOOT` | process | optional | true |  | apply pending SQL migrations at startup; false hands them to a separate deploy step |
| `DB_URL` | process | required | — | yes | PostgreSQL connection string (contains the password) |
| `DEV_PLAYGROUND_CLIENT_ID` | process | dev-only | saas-dev-playground |  | client id the playground uses |
| `DEV_PLAYGROUND_ENABLED` | process | dev-only | false |  | exposes an unauthenticated login playground at /dev/auth. NEVER true in production |
| `GIN_ACCESS_LOG_ENABLED` | process | optional | true |  | per-request access log lines |
| `GIN_LOG_ENABLED` | process | optional | true |  | Gin framework debug logs; false switches Gin to release mode |
| `KC_DB_NAME` | compose | optional | — |  | database name for the bundled dev Keycloak |
| `KC_DB_PASSWORD` | compose | optional | — | yes | database password for the bundled dev Keycloak |
| `KC_DB_USER` | compose | optional | — |  | database user for the bundled dev Keycloak (profile `dev-idp`) |
| `KC_HOST_PORT` | compose | optional | 8081 |  | host port the bundled dev Keycloak is published on |
| `KEYCLOAK_ADMIN` | compose | dev-only | — |  | bootstrap admin username for the bundled dev Keycloak |
| `KEYCLOAK_ADMIN_BASE_URL` | process | optional | KEYCLOAK_URL |  | where the API reaches the Keycloak admin API — often an internal address |
| `KEYCLOAK_ADMIN_CLIENT_ID` | process | optional | — |  | service-account client for /admin/*; unset omits the whole /admin surface |
| `KEYCLOAK_ADMIN_CLIENT_SECRET` | process | optional | — | yes | secret for the above; both must be set or neither |
| `KEYCLOAK_ADMIN_PASSWORD` | compose | dev-only | — | yes | bootstrap admin password for the bundled dev Keycloak |
| `KEYCLOAK_ALLOWED_CLIENT_IDS` | process | optional | — |  | comma-separated client ids accepted in the token's azp/aud; empty accepts any |
| `KEYCLOAK_CLIENT_ID` | process | required | — |  | OIDC client id the console logs in with |
| `KEYCLOAK_CLIENT_SECRET` | process | optional | — | yes | only for a confidential client; a public PKCE client needs none |
| `KEYCLOAK_JWKS_URL` | process | optional | derived from KEYCLOAK_URL + KEYCLOAK_REALM |  | override when the API reaches Keycloak on a different address than clients do |
| `KEYCLOAK_REALM` | process | required | — |  | realm operators authenticate against (the installation realm, not a workspace's) |
| `KEYCLOAK_URL` | process | required | — |  | issuer base URL as CLIENTS see it — drives the expected `iss` claim |
| `METRICS_ENABLED` | process | optional | false |  | expose Prometheus metrics at /metrics |
| `METRICS_TOKEN` | process | optional | — | yes | bearer token a scraper must present; empty serves /metrics to loopback only |
| `PORT` | process | optional | 8080 |  | TCP port the API listens on |
| `POSTGRES_DB` | compose | required | — |  | application database name |
| `POSTGRES_HOST_PORT` | compose | optional | 5432 |  | host port the application database is published on; change when 5432 is taken |
| `POSTGRES_PASSWORD` | compose | required | — | yes | application database password |
| `POSTGRES_USER` | compose | required | — |  | application database user; compose builds DB_URL from this |
| `RATE_LIMIT_CREDENTIAL_RPS` | process | optional | 20 |  | per-credential allowance — the number a machine consumer can actually reach |
| `RATE_LIMIT_EDGE_RPS` | process | optional | 10 |  | per-IP allowance before authentication; meters anonymous and operator traffic |
| `SECRETS_KEYRING` | process | optional | — | yes | versioned keys `1:<base64>,2:<base64>`; every version can decrypt. Unset omits connections AND workspace identity |
| `SECRETS_KEY_CURRENT` | process | optional | — |  | which SECRETS_KEYRING version seals NEW secrets. Optional with a single key, required with more than one |
| `SECRETS_MASTER_KEY` | process | optional | — | yes | LEGACY single key; equivalent to SECRETS_KEYRING=1:<base64>. Cannot be combined with SECRETS_KEYRING |
| `SEED_USER_PASSWORD` | bootstrap | dev-only | — | yes | password given to seed users by the bootstrap CLI |
| `SHUTDOWN_TIMEOUT_SECONDS` | process | optional | 20 |  | how long in-flight requests may finish after SIGTERM before the process exits anyway |

### Reading this table

- **required** — the process exits at boot without it.
- **optional** — absence means the stated default, or a feature that is simply
  not mounted. `SECRETS_MASTER_KEY` is the important one: without it the
  connection API and the workspace-identity API are **absent**, not broken.
- **dev-only** — must not be set in a production deployment.
  `DEV_PLAYGROUND_ENABLED=true` exposes an unauthenticated login playground.
- **secret** — never commit a real value, and note that `.env.example` ships
  placeholders that a test refuses to let become real keys.

### The variable that catches everyone

`KEYCLOAK_URL` and `KEYCLOAK_ADMIN_BASE_URL` are **different addresses** in any
deployment where the API reaches Keycloak internally:

```text
KEYCLOAK_URL             https://auth.example.com     as CLIENTS see it → drives the `iss` claim
KEYCLOAK_ADMIN_BASE_URL  http://keycloak:8080         as the API sees it
```

Set `KEYCLOAK_URL` to the internal address and every token a browser obtained
is rejected, because its `iss` will not match.

---

## 3. Fail-fast

The process validates its whole configuration before serving, and reports
**every** problem at once rather than one per restart:

```text
ERROR [ config ] configuration is not usable — refusing to start:
ERROR [ config ]   • DB_URL must start with postgres:// or postgresql:// (got scheme "mysql")
ERROR [ config ]   • SECRETS_MASTER_KEY decodes to 24 bytes, need exactly 32 — generate one with: openssl rand -base64 32
ERROR [ config ]   • RATE_LIMIT_EDGE_RPS must be a number (got "2O")
FATAL [ config ] fix the 3 problems above; see .env.example
```

What it checks: required values present · URLs absolute and correctly schemed ·
numbers parseable and in range · the master key valid base64 of exactly 32
bytes · CORS origins with no trailing slash or path · combinations that cannot
be honoured (a half-configured admin client; the console enabled with no client
id and the playground off).

**A message never prints a secret.** `DB_URL` contains the database password and
`SECRETS_MASTER_KEY` is key material, so both are reported by name with the
requirement stated and the value withheld.

**Present-but-unparseable is an error, not a fallback.** A previous version
substituted the default silently, so `RATE_LIMIT_CREDENTIAL_RPS=2O` ran as 20
and nothing said so. Absent still means the default; typed wrong now stops the
boot.

---

## 4. Health: liveness and readiness

Two endpoints, two different questions. Using one for both gets both wrong.

| Endpoint | Question | Cost | Use it for |
|---|---|---|---|
| `GET /health/live` | is the process alive? | no I/O | **restart** decisions |
| `GET /health/ready` | can this instance serve? | one database ping | **routing** decisions |
| `GET /health` | (legacy) same as `/health/live` | no I/O | existing monitors |

```jsonc
// 200 — GET /health/ready
{"status":"ready","checks":{"accepting":"ok","database":"ok"}}

// 503 — draining, or a global dependency is down
{"status":"not ready","checks":{"accepting":"draining","database":"ok"}}
```

**Never point a liveness probe at a dependency check.** A thirty-second database
blip would kill and restart the process, which cannot help — the new process
will not find the database either — and the restart loop becomes the outage.

### What readiness deliberately does NOT check

**The health of any workspace's Keycloak.**

If readiness consulted connections, one tenant's provider going down would take
this instance out of the load balancer and every other tenant with it. One
broken provider must degrade exactly one workspace, and it does: that request
answers `provider_unavailable` (502) and everything else keeps working.

Connection health belongs to the Connection, where an operator can see and
repair it. It is not a property of this instance.

---

## 5. Shutdown

`SIGTERM` or `SIGINT` starts an ordered drain:

```text
signal
  ↓ readiness → 503          load balancer stops sending new work
  ↓ 3s                       …give it time to notice
  ↓ listener closes          no new connections
  ↓ in-flight finish         bounded by SHUTDOWN_TIMEOUT_SECONDS (default 20)
  ↓ database pool closes
  ↓ exit 0
```

Marking not-ready **before** closing the listener is the part that matters: a
load balancer learns about readiness by polling, so closing first turns that
window into refused connections. A second signal skips the delay.

`SHUTDOWN_TIMEOUT_SECONDS` must be **below** whatever will `SIGKILL` the
process. Docker's default stop grace is 10s and the compose file therefore sets
`stop_grace_period: 30s`; Kubernetes' default `terminationGracePeriodSeconds` is
30. If the platform's number is smaller, the platform decides the drain and the
log line explaining why is cut off mid-sentence.

---

## 6. Metrics

Off by default. `METRICS_ENABLED=true` exposes Prometheus text at `/metrics`.

| | |
|---|---|
| `METRICS_TOKEN` empty | served to **loopback only**. Under compose that means from inside the api container. |
| `METRICS_TOKEN` set | any caller presenting `Authorization: Bearer <token>`. What a real scraper can send. |

An unauthorized scrape answers **404**, not 401: there is no flow to
authenticate into, and the endpoint's existence is itself information.

What is exposed:

```text
lightweight_http_requests_total{method,route,status}
lightweight_http_request_duration_seconds{method,route}      histogram
lightweight_auth_failures_total{kind}
lightweight_authorization_denials_total{principal}
```

`route` is the registered **pattern** (`/v1/workspaces/:workspace_id/users`),
never the concrete path. No user id, request id, credential id, project id or
workspace id is a label anywhere — that would be unbounded cardinality and a
privacy leak in one.

Rate-limit rejections and provider failures need no separate metric: they are
`status="429"` and `status="502"` on the counter above.

---

## 7. Logs

One line per request, in logfmt:

```text
request_id=7f0a446f method=GET route=/v1/workspaces/:workspace_id/users status=200 dur=11.6ms
  principal=project project_id=prj_… credential_id=key_… workspace_id=ws_…
```

The identifiers are what make a machine request traceable: **reads emit no audit
event** — audit is mutations only — so this line is the only record that a
credential performed a read.

Never logged, at any level: the `Authorization` header, a credential in any
form, a key hash, a connection secret, a provider client secret, a password.
Enforced by tests, and by `scripts/redact-logs.sh` before CI keeps any log as an
artifact.

---

## 8. Backup and recovery

**A database backup alone does not restore this system.**

The recovery unit is two things, and losing either makes the other useless:

```text
PostgreSQL dump        workspaces, connections, projects, credential hashes
+
SECRETS_MASTER_KEY     the key those connection secrets are sealed with
```

Restore the database without the key and every Connection is unreadable: the
workspaces exist, point at realms, and cannot authenticate to any of them. There
is no recovery path — the provider credentials must be re-entered by hand, for
every connection.

**Store the key somewhere the database backup is not.** A key kept beside the
dump means one compromised backup gives up both halves.

**Durable audit history is in the same database**, so the dump already contains
it. Audit rows hold no sealed value, so they restore from the dump alone — the
master key is only needed for connection secrets. See [AUDIT.md](../AUDIT.md).

### What does NOT need backing up

- **Project Credential plaintext.** Only SHA-256 digests are stored, so there is
  nothing to back up and nothing to leak. A lost key is replaced by issuing a
  new one and revoking the old — that is the intended rotation.
- **Keycloak.** It has its own lifecycle and its own backup. LIGHTWEIGHT stores
  which realm a workspace points at, not the realm's contents.

### Key rotation

Not implemented ([TD-019](../TECH_DEBT.md#td-019)). Rotating
`SECRETS_MASTER_KEY` today makes every stored connection secret unreadable.
Treat the key as permanent for the life of the installation.

---

## 9. Production smoke

One reproducible sequence that proves the whole path, from a clean machine.
Twenty minutes, including reading.

```sh
# 1. Configure
cp .env.example .env
openssl rand -base64 32          # → SECRETS_MASTER_KEY
#   set KEYCLOAK_URL / KEYCLOAK_REALM / KEYCLOAK_CLIENT_ID to your Keycloak
#   set ADMIN_CONSOLE_CLIENT_ID to your console's public PKCE client
#   set DEV_PLAYGROUND_ENABLED=false

# 2. Start
docker compose up -d
curl -fsS localhost:8080/health/ready     # {"status":"ready",...}

# 3. Console: http://localhost:8080/admin — sign in as a realm admin
#      create a Workspace
#      add a Connection to a Keycloak realm, Verify, Activate
#      create a Project
#      create a Credential — copy the secret, it is shown once

# 4. Be the backend. Nothing here knows Keycloak exists.
LIGHTWEIGHT_URL=http://localhost:8080 \
LIGHTWEIGHT_WORKSPACE_ID=ws_… \
LIGHTWEIGHT_API_KEY=lw_sk_… \
  go run ./cmd/lwprobe -mode contract

# 5. Restart gracefully and confirm it comes back
docker compose restart api
curl -fsS localhost:8080/health/ready
```

Step 4 prints the full contract check: the flow, the error matrix, the effective
rate limit, secret hygiene. `0 failed` means the installation is serving the
machine contract correctly.

To run the whole thing unattended against throwaway realms, including
revocation, connection rotation and shutdown:

```sh
DB_URL=postgres://…  ./scripts/m2m-harness.sh
```

That is the same script CI runs (`--smoke`).

---

## 10. Startup ordering

What retries and what does not, decided rather than inherited:

| Condition | Behaviour | Why |
|---|---|---|
| PostgreSQL not up yet | **retry**, 10 attempts over ~15s, then exit | Covers a database a few seconds behind on a host reboot. Bounded, because a process that retries forever is running, not serving, and pages nobody. |
| PostgreSQL misconfigured | exits after the same 10 attempts | A wrong host or password fails all ten just as fast. |
| Migration failure | **fail immediately**, no retry | It already connected. The schema is in a state this build does not understand, and retrying is how a half-applied migration becomes a corrupted one. |
| Keycloak issuer unreachable | **fail fast** | JWKS is fetched at boot. Starting without it means serving 401s to everyone while looking healthy. |
| Invalid configuration | **fail fast**, all problems at once | [§3](#3-fail-fast) |
| A **workspace's** Keycloak unreachable | **starts normally** | Per-workspace, resolved per request. It must never be a startup dependency, or one tenant's outage becomes everyone's. |

---

## 10a. Audit

Every change made through this API is recorded durably in PostgreSQL and read
back at `GET /v1/workspaces/{id}/audit`. `AUDIT_RETENTION_DAYS` (default 90)
bounds how long; a sweep runs 30 seconds after boot and once a day thereafter.

There is no value meaning "keep forever", and `0` is refused rather than
defaulted. Full contract in [AUDIT.md](../AUDIT.md).

## 11. Known limitations

- **Single process.** Rate-limit buckets are in-memory, so two replicas permit
  twice the configured rate ([TD-027](../TECH_DEBT.md#td-027)).
- **Control-plane audit is not transactional.** The mutation and its audit row
  are two statements against the same database, so a narrow window exists in
  which one lands without the other. It is logged and counted rather than
  silent ([TD-033](../TECH_DEBT.md#td-033)).
- **No master-key rotation** ([TD-019](../TECH_DEBT.md#td-019)).
