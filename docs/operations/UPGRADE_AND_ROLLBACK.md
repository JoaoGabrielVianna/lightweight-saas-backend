# Upgrade and Rollback — Operator Runbook

Procedures for moving a running deployment between tagged releases of
`lightweight-saas-backend`, and for unwinding the change if the new
release misbehaves. Audience: whoever is on the keyboard when the
deploy goes out — not the author of the change.

Every command in this doc is **copy-pasteable**. Where a step needs a
judgement call, the runbook says so explicitly.

---

## 1. What this runbook assumes

- The docker-compose stack in `docker-compose.yml` is the unit of
  deployment (postgres + keycloak-postgres + mailpit + keycloak + api).
- Migrations run automatically — `gorm.AutoMigrate(&user.User{})` in
  [`internal/database/database.go`](../../internal/database/database.go)
  executes at API startup. There is **no separate migration binary**;
  `make migrate` is currently a no-op placeholder.
- The current release is **v0.2.0**. Previous tag for rollback is
  **v0.1.0-auth-foundation**.
- Git identity, ssh keys, and registry credentials are already in place.
  The runbook does not cover provisioning the host.

Inventory of tags available right now:

```sh
git tag -l --format='%(refname:short) %(creatordate:short) %(subject)'
# v0.1.0-auth-foundation 2026-05-18 Authentication foundation milestone
# v0.2.0                 2026-05-20 Reusable IAM foundation with RBAC, admin CRUD, audit validation and GAP-1 closure
```

---

## 2. Pre-flight (always)

Before touching anything in prod, **capture state you can roll back to**.

### 2.1 Snapshot the application database

`gorm.AutoMigrate` only ever adds columns / indexes — it never drops or
narrows. So a snapshot of the DB taken before the upgrade is a valid
restore target if the new release misbehaves; a snapshot taken *after*
the upgrade may carry columns the old binary doesn't know about (benign:
old code ignores unknown columns).

```sh
# Snapshot the app DB (host port 5432 in compose)
PGPASSWORD="$POSTGRES_PASSWORD" pg_dump \
        -h localhost -p 5432 -U "$POSTGRES_USER" \
        -F c -f "backups/app-$(date +%Y%m%d-%H%M%S)-pre-upgrade.dump" \
        "$POSTGRES_DB"

# And the Keycloak DB (host port 5433)
PGPASSWORD="$KC_DB_PASSWORD" pg_dump \
        -h localhost -p 5433 -U "$KC_DB_USER" \
        -F c -f "backups/keycloak-$(date +%Y%m%d-%H%M%S)-pre-upgrade.dump" \
        "$KC_DB_NAME"
```

`pg_dump -F c` (custom format) is the file `pg_restore` reads — see
section 7.2.

### 2.2 Capture the realm export

The Keycloak realm definition lives at
`deploy/keycloak/realm-export.json` and is re-imported on container
boot. Production realms are also worth exporting live in case manual
edits happened in Keycloak Admin UI between releases:

```sh
make keycloak-export   # writes deploy/keycloak/realm-export.json
cp deploy/keycloak/realm-export.json \
   "backups/realm-$(date +%Y%m%d-%H%M%S).json"
```

### 2.3 Record what's currently deployed

So the rollback target is unambiguous:

```sh
git rev-parse HEAD                   # current commit
git describe --tags --always         # human-readable: "v0.2.0" or "v0.2.0-3-gabcdef0"
docker compose ps                    # container image tags + status
curl -s http://localhost:8080/health # 200 + {"status":"ok"} sanity
```

Paste those four outputs into the incident channel / deploy ticket
before proceeding.

---

## 3. Pull latest

```sh
# Update local tracking refs without touching the working tree
git fetch --tags origin

# Move to the target tag (replace v0.2.0 with the intended release)
git checkout v0.2.0
```

Verify the tag is signed/expected:

```sh
git tag -v v0.2.0       # if tags are signed; otherwise:
git show v0.2.0 | head  # confirm the tag commit subject matches the release notes
```

If the tag points at an unexpected commit, **stop here** and escalate.
Do not proceed with a tag whose target you don't recognise.

---

## 4. Migrate

This codebase has no separate migration tool. Schema changes are
applied by `gorm.AutoMigrate` when the API binary starts. The
operationally relevant fact:

- **AutoMigrate is additive only.** It will create tables, add columns,
  and add indexes. It will not drop or narrow.
- That means: starting the new API *is* the migration. There is no
  "migrate then start" — the steps are fused.
- A failed migration manifests as the API binary exiting non-zero at
  start; the container will be in `Restarting` status.

If a release ever introduces a **destructive** schema change (see
[`docs/architecture/PHASE3_BREAKING_CHANGE.md`](../architecture/PHASE3_BREAKING_CHANGE.md)
for what that looks like), the release notes for that tag MUST call it
out explicitly and this runbook is **not** sufficient — follow the
breaking-change procedure attached to that release.

---

## 5. Restart

Two flavours — choose by **what changed**.

### 5.1 Code-only change (binary rebuild, no infra)

```sh
make stop                # graceful stop, containers + volumes kept
docker compose build api # rebuild only the API image
make start               # restart all previously-stopped containers
```

### 5.2 Compose / infra change (image bump, env vars, port edits)

```sh
make down                # remove containers, KEEP volumes
make up                  # recreate everything from current compose file
```

`make down` only removes containers — `postgres_data` and
`keycloak_postgres_data` named volumes are preserved. **Never use
`make purge` or `make reset-dev` on a real deployment** — both wipe
volumes and are documented as `DATA LOSS` in the Makefile.

### 5.3 Confirm processes came up

```sh
docker compose ps
# All five services should show STATUS = "Up (healthy)" or "Up".
# If any container is "Restarting", read its logs immediately:
docker compose logs --tail=200 api
docker compose logs --tail=200 keycloak
```

A common failure mode at this point is the API exiting because JWKS
fetch against Keycloak failed — wait 30s for keycloak healthcheck to
pass, then `docker compose restart api`.

---

## 6. Verify

Run these checks in order. The runbook is complete only when **all
seven** pass.

### 6.1 Liveness

```sh
curl -fsS http://localhost:8080/health
# Expected: HTTP 200, body {"status":"ok"}
```

### 6.2 Auth round-trip

```sh
make auth-test
# Acquires a Keycloak token and calls /me. PASS = HTTP 200 with a
# JSON body carrying the seeded user. FAIL = bad token, JWKS issue,
# or middleware regression.
```

### 6.3 Admin surface reachable (and gated)

```sh
# Without a token — must be 401
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/admin/users
# Expected: 401

# With an admin token, must be 200. The token shell call is documented
# in scripts/auth-test.sh — adapt to your env.
curl -fsS -H "Authorization: Bearer $ADMIN_TOKEN" \
        http://localhost:8080/admin/users | jq '.count'
# Expected: an integer >= 1
```

### 6.4 GAP-1 live-admin check is active (v0.2.0+)

```sh
# Repeat the call with a token whose `admin` role was revoked in
# Keycloak ≤ AUTH_ADMIN_LIVE_CHECK_TTL_SECONDS ago (default 30s):
curl -s -o /dev/null -w '%{http_code}\n' \
        -H "Authorization: Bearer $STALE_ADMIN_TOKEN" \
        http://localhost:8080/admin/users
# Expected: 403 once the cache TTL has elapsed — proves RequireLiveAdmin
# is consulting the upstream truth, not just the JWT claim.

# The full regression script:
./scripts/security_gap1_check.sh
```

### 6.5 Audit subsystem is emitting

```sh
# In one shell, tail the API logs:
docker compose logs -f api | grep --line-buffered ' audit '

# In another shell, perform any admin mutation — e.g. invite a user:
curl -fsS -X POST \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" \
        -d '{"email":"verify-upgrade@example.com","roles":["user"]}' \
        http://localhost:8080/admin/invitations

# Within 1 second the first shell must print two lines that contain:
#   audit {"action":"invitation.created", ... }
#   audit {"action":"user.created",       ... }
# Each line must carry actor / target / ip / ts fields. See
# docs/AUDIT_VALIDATION.md for the field-by-field invariants.
```

### 6.6 Full e2e smoke

```sh
make e2e
# Brings the stack up if needed and runs the end-to-end script. Exit
# code 0 = PASS.
```

### 6.7 Test suite (on the running binary's source tree)

```sh
make test
# All packages OK. A regression in any package here means the deployed
# binary doesn't match the source — investigate before declaring the
# upgrade complete.
```

If all seven pass, **mark the deploy complete** in the deploy ticket
and stop reading. Otherwise: section 7.

---

## 7. Rollback

Two scenarios. The runbook for each is different.

### 7.1 New release is broken but DB is intact

This is the common case — the binary misbehaves but the schema
migration that ran at startup added only columns the old binary will
ignore.

```sh
# 1. Stop the failing API
make stop

# 2. Move back to the previous tag
git checkout v0.1.0-auth-foundation
# (substitute whichever tag was running before; record from section 2.3)

# 3. Rebuild and start
docker compose build api
make start

# 4. Re-run section 6 verifications against the old release
curl -fsS http://localhost:8080/health
make auth-test
```

**Caveat:** the previous binary may not know about env vars the new
release introduced (e.g. v0.2.0 adds `AUTH_ADMIN_LIVE_CHECK_TTL_SECONDS`).
That's benign — unknown env is ignored. But if the new release *removed*
or *renamed* an env var the old binary required, the old binary will
fail-fast. In that case go to 7.2.

### 7.2 DB state is suspect — restore from snapshot

Use this when:

- The new release ran a destructive migration (would be called out in
  release notes — none in v0.2.0).
- Mutations under the new release wrote rows the old release cannot
  read (rare; audit table doesn't exist in v0.2.0 — Sprint 4).
- You don't know whether the DB is intact and would rather start clean.

```sh
# 1. Stop EVERYTHING that talks to the DBs
make down

# 2. Drop the postgres volumes (DATA LOSS — that's the whole point)
docker volume rm lightweight-saas-backend_postgres_data
docker volume rm lightweight-saas-backend_keycloak_postgres_data
# (volume names may be prefixed with your compose project name — check
# `docker volume ls` first)

# 3. Bring the infra back up empty
make up-infra

# 4. Restore both DBs from the pre-upgrade snapshot
PGPASSWORD="$POSTGRES_PASSWORD" pg_restore \
        -h localhost -p 5432 -U "$POSTGRES_USER" \
        -d "$POSTGRES_DB" --clean --if-exists \
        backups/app-<TIMESTAMP>-pre-upgrade.dump

PGPASSWORD="$KC_DB_PASSWORD" pg_restore \
        -h localhost -p 5433 -U "$KC_DB_USER" \
        -d "$KC_DB_NAME" --clean --if-exists \
        backups/keycloak-<TIMESTAMP>-pre-upgrade.dump

# 5. Restart keycloak so it picks up the restored DB
docker compose restart keycloak
# Wait for healthcheck to go green before proceeding.

# 6. Check out the previous tag and start the API on it
git checkout v0.1.0-auth-foundation
docker compose build api
docker compose up -d api

# 7. Verify (section 6 — at minimum 6.1, 6.2, 6.3)
```

### 7.3 Document the rollback

A rollback is itself a deploy. After step 7.1 or 7.2 succeeds, repeat
section 2.3 to record what's now running, and post the new
`git describe --tags --always` output to the same channel where the
upgrade was announced. Without that, the next operator does not know
what's in production.

---

## 8. Common failures and what they mean

| Symptom on `docker compose ps`         | Likely cause                                            | First thing to try                                                |
|----------------------------------------|---------------------------------------------------------|-------------------------------------------------------------------|
| `api` is `Restarting (1)`              | Bad env, JWKS fetch failed, DB unreachable              | `docker compose logs --tail=200 api`                              |
| `keycloak` is `Restarting`             | Realm-import error, KC_DB unreachable                   | `docker compose logs --tail=200 keycloak`                         |
| `keycloak-postgres` healthcheck failing| Volume corrupted, port 5433 conflict                    | `make doctor` (checks port conflicts), inspect volume             |
| `/health` returns 200 but `/admin/*` returns 503 | Identity admin client creds unset             | Check `KEYCLOAK_ADMIN_CLIENT_ID` / `_SECRET` in `.env`            |
| `make auth-test` returns 401           | Token issued for wrong realm or expired                 | Re-acquire token; check `KEYCLOAK_ALLOWED_CLIENT_IDS`             |
| Audit lines absent from stdout         | `logging.WireDefault()` skipped at bootstrap            | Confirm `cmd/api/main.go` matches the running tag                 |

---

## 9. Quick reference (single block, no prose)

```sh
# Pre-flight
PGPASSWORD="$POSTGRES_PASSWORD" pg_dump -h localhost -p 5432 -U "$POSTGRES_USER" -F c -f backups/app-pre.dump "$POSTGRES_DB"
PGPASSWORD="$KC_DB_PASSWORD"   pg_dump -h localhost -p 5433 -U "$KC_DB_USER"   -F c -f backups/kc-pre.dump  "$KC_DB_NAME"
git describe --tags --always

# Pull + migrate + restart (code-only change)
git fetch --tags origin
git checkout v0.2.0
make stop
docker compose build api
make start

# Verify
curl -fsS http://localhost:8080/health
make auth-test
./scripts/security_gap1_check.sh
make e2e

# Rollback (binary only)
make stop
git checkout v0.1.0-auth-foundation
docker compose build api
make start

# Rollback (DB too — destructive)
make down
docker volume rm lightweight-saas-backend_postgres_data lightweight-saas-backend_keycloak_postgres_data
make up-infra
pg_restore -h localhost -p 5432 -U "$POSTGRES_USER" -d "$POSTGRES_DB"  --clean --if-exists backups/app-pre.dump
pg_restore -h localhost -p 5433 -U "$KC_DB_USER"   -d "$KC_DB_NAME"    --clean --if-exists backups/kc-pre.dump
docker compose restart keycloak
git checkout v0.1.0-auth-foundation
docker compose build api
docker compose up -d api
```
