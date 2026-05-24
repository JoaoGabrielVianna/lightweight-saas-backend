# Backup, Restore & Disaster Recovery

Operator runbook for `lightweight-saas-backend`. All commands are copy-paste runnable from the repo root on the docker-compose stack as shipped. Replace `$(date +%Y%m%d-%H%M%S)` with whatever timestamp scheme your archive store expects.

> **Scope.** Local / single-host docker-compose deployment. Production HA setups (RDS, Aurora, managed Keycloak, multi-AZ) are out of scope — commands assume direct shell access to the host running the containers.

---

## 0. What is in scope

| Artifact | Where it lives | Backup strategy | Loss = ? |
|----------|----------------|-----------------|----------|
| **App database** (`lightweight_saas_backend_db`) | Volume `lightweight-saas-backend_postgres_data` | Logical (`pg_dump`) | Every user record, JIT-provisioned identities, any future business data |
| **Keycloak database** (`keycloak`) | Volume `lightweight-saas-backend_keycloak_postgres_data` | Logical (`pg_dump`) **or** realm-export | Out-of-band realm changes (users via Admin UI, password resets, sessions, audit log) |
| **Keycloak realm seed** (`saas`) | `deploy/keycloak/realm-export.json` (git-tracked) | Already in git + periodic `make keycloak-export` | Clients, roles, realm-level settings made after the last export |
| **App container image** | `lightweight-saas-backend-api` (locally built) | Rebuildable from source via `docker-compose build api` | No real loss — rebuild from git |
| **Mailpit** (dev SMTP) | In-memory by default | Not backed up — by design (test mail only) | Test mail history; no production impact |
| **`.env`** | Repo root (`.gitignore`d) | Off-site backup of the file itself | `KEYCLOAK_CLIENT_SECRET`, `KEYCLOAK_ADMIN_CLIENT_SECRET`, `SEED_USER_PASSWORD`, seed for every regen — see §6.4 |

**Not backed up (intentional):** mailpit storage, API image, generated swagger artifacts under `docs/{docs.go,swagger.{json,yaml}}` (CI rebuilds via `make swagger`).

---

## 1. RPO / RTO targets

Describes **what the tooling supports today**, not promises. Set actual SLOs per environment.

| Scenario | RPO target | RTO target | How achieved |
|----------|------------|------------|--------------|
| App DB single-row corruption | 24h (last nightly `pg_dump`) | < 5 min | §3.2 logical restore into side DB + selective `INSERT … SELECT` |
| App DB full loss | 24h | < 5 min for a small DB (< 1 GB) | §3.1 full logical restore |
| Keycloak DB full loss (seed-only realm) | **0** (realm file in git) | < 2 min | §3.3 — `make realm-reset` |
| Keycloak DB full loss (out-of-band changes) | Time since last `pg_dump` / `make keycloak-export` | < 5 min | §3.3 — restore KC DB dump or replay export |
| Lost docker volumes (both DBs) | Max of both above | < 10 min | §4 |
| Lost host (all volumes, archive store intact) | Worst of above | < 30 min (depends on `docker pull` + restore size) | §5 |
| Failed schema migration (`AutoMigrate`) | 0 (pre-migration `pg_dump` in §6.1) | Time to apply rollback SQL | §6 |

Tightening RPO < 24 h requires WAL archiving (not wired in this compose stack) or shorter cron interval. Tightening RTO mostly means reducing dump size; logical restores are CPU-bound.

---

## 2. Backup procedures

All commands assume the stack is **up** (`docker-compose ps` shows all five containers healthy). `pg_dump` runs against a live database — no maintenance window needed.

```bash
# Suggested layout
export BACKUPS=/var/backups/lightweight-saas
mkdir -p "$BACKUPS"/{appdb,kcdb,realm,env}
```

### 2.1 App database

```bash
docker exec saas-postgres pg_dump \
    -U postgres -d lightweight_saas_backend_db -F c -Z 9 \
  > "$BACKUPS/appdb/appdb-$(date +%Y%m%d-%H%M%S).pgcustom"

# Sanity: file > a few KB AND pg_restore -l lists at least the users table
docker run --rm -i postgres:15-alpine pg_restore -l \
  < "$BACKUPS/appdb/appdb-"*.pgcustom | grep -E "TABLE.*users" \
  || { echo "BACKUP CORRUPT — investigate"; exit 1; }
```

### 2.2 Keycloak database

Captures **everything the live realm contains** — users, sessions, client secrets, roles, audit log — at dump time. Use alongside §2.3 for belt-and-braces recovery.

```bash
docker exec saas-keycloak-postgres pg_dump \
    -U keycloak -d keycloak -F c -Z 9 \
  > "$BACKUPS/kcdb/kcdb-$(date +%Y%m%d-%H%M%S).pgcustom"
```

### 2.3 Keycloak realm — declarative export

Snapshots the live realm into `deploy/keycloak/realm-export.json`, which the import flow re-reads on fresh starts. **Git-track this file** — commit it after every intentional realm change.

```bash
make keycloak-export       # overwrites deploy/keycloak/realm-export.json
cp deploy/keycloak/realm-export.json \
   "$BACKUPS/realm/realm-export-$(date +%Y%m%d-%H%M%S).json"
```

> `make keycloak-export` uses `--users realm_file` — every user goes into the JSON. For a 10K+ user realm use the KC DB dump (§2.2) instead; the realm file grows linearly.

### 2.4 `.env` and `config/project.json`

```bash
tar czf "$BACKUPS/env/env-$(date +%Y%m%d-%H%M%S).tgz" \
    .env config/project.json
```

### 2.5 Recommended cron (nightly, 02:00 local)

```cron
0 2 * * *  cd /opt/lightweight-saas-backend && BACKUPS=/var/backups/lightweight-saas \
           docker exec saas-postgres pg_dump -U postgres -d lightweight_saas_backend_db -F c -Z 9 \
           > "$BACKUPS/appdb/appdb-$(date -u +\%Y\%m\%d-\%H\%M\%S).pgcustom"

10 2 * * * cd /opt/lightweight-saas-backend && BACKUPS=/var/backups/lightweight-saas \
           docker exec saas-keycloak-postgres pg_dump -U keycloak -d keycloak -F c -Z 9 \
           > "$BACKUPS/kcdb/kcdb-$(date -u +\%Y\%m\%d-\%H\%M\%S).pgcustom"

20 2 * * * cd /opt/lightweight-saas-backend && make keycloak-export && \
           cp deploy/keycloak/realm-export.json \
              "/var/backups/lightweight-saas/realm/realm-export-$(date -u +\%Y\%m\%d-\%H\%M\%S).json"

# 14-day retention
30 2 * * * find /var/backups/lightweight-saas -type f -mtime +14 -delete
```

---

## 3. Restore procedures

### 3.1 App database — full restore (volume intact, data corrupted)

```bash
# 1. Stop api so nothing writes during the swap.
docker-compose stop api

# 2. Rename existing DB so you can flip back if restore is bad.
docker exec -i saas-postgres psql -U postgres <<'SQL'
ALTER DATABASE lightweight_saas_backend_db RENAME TO lightweight_saas_backend_db_BAD;
CREATE DATABASE lightweight_saas_backend_db OWNER postgres;
SQL

# 3. Restore custom-format dump.
docker exec -i saas-postgres pg_restore \
    -U postgres -d lightweight_saas_backend_db \
    --clean --if-exists --no-owner --no-privileges --exit-on-error \
  < "$BACKUPS/appdb/appdb-20260520-020000.pgcustom"

# 4. Smoke-check + bring api back
docker exec saas-postgres psql -U postgres -d lightweight_saas_backend_db -c "SELECT count(*) FROM users;"
docker-compose up -d api

# 5. Once verified end-to-end, drop the renamed-aside DB.
docker exec saas-postgres psql -U postgres -c "DROP DATABASE lightweight_saas_backend_db_BAD;"
```

### 3.2 App database — single-row recovery

Restore into a side DB, then `INSERT … SELECT` what you need.

```bash
docker exec saas-postgres createdb -U postgres lsb_recovery_tmp
docker exec -i saas-postgres pg_restore \
    -U postgres -d lsb_recovery_tmp --no-owner --no-privileges --exit-on-error \
  < "$BACKUPS/appdb/appdb-20260520-020000.pgcustom"

# Cherry-pick (example: restore one user by keycloak_sub) via dblink
docker exec -i saas-postgres psql -U postgres -d lightweight_saas_backend_db <<'SQL'
INSERT INTO users (id, keycloak_sub, email, username, created_at, updated_at)
SELECT id, keycloak_sub, email, username, created_at, updated_at
FROM dblink('dbname=lsb_recovery_tmp',
            'SELECT id, keycloak_sub, email, username, created_at, updated_at
             FROM users WHERE keycloak_sub = ''<sub-goes-here>''')
       AS t(id bigint, keycloak_sub text, email text, username text,
            created_at timestamptz, updated_at timestamptz)
ON CONFLICT (keycloak_sub) DO NOTHING;
SQL

docker exec saas-postgres dropdb -U postgres lsb_recovery_tmp
```

If `dblink` isn't installed, use `pg_restore -t users` into a temp db, then `pg_dump --table users --data-only`, then `psql \copy` between two `docker exec` shells.

### 3.3 Keycloak — restore strategies (cheapest → strongest)

| You lost | … and you have | Use |
|----------|----------------|-----|
| Realm config only (clients/roles/realm settings) | `deploy/keycloak/realm-export.json` (git-tracked) | §3.3a — re-import realm file |
| Realm + all users + sessions | A `keycloak` DB dump | §3.3b — restore Keycloak DB dump |
| Everything (volume gone) | Both above | §3.3b (full) — DB dump rehydrates everything |

#### 3.3a Re-import the realm file only

Use when realm config drifted but you want to keep the live user table. **WARNING:** `--import-realm` only imports if the realm is ABSENT. Forcing re-import deletes every user in the realm.

```bash
docker exec saas-keycloak /opt/keycloak/bin/kc.sh import \
    --dir /opt/keycloak/data/import \
    --override true
docker-compose restart keycloak
```

#### 3.3b Restore Keycloak Postgres DB from a dump

Right answer for "everything in Keycloak is gone".

```bash
docker-compose stop keycloak                                   # holds open connections / row locks
docker exec -i saas-keycloak-postgres psql -U keycloak -d postgres <<'SQL'
DROP DATABASE keycloak;
CREATE DATABASE keycloak OWNER keycloak;
SQL
docker exec -i saas-keycloak-postgres pg_restore \
    -U keycloak -d keycloak --no-owner --no-privileges --exit-on-error \
  < "$BACKUPS/kcdb/kcdb-20260520-021000.pgcustom"

# Do NOT pass --import-realm — realm is now in the DB; re-import conflicts.
docker-compose up -d keycloak

# Smoke
curl -sf http://localhost:8081/realms/saas/.well-known/openid-configuration | jq .issuer
curl -sf "http://localhost:8081/realms/saas/protocol/openid-connect/certs" | jq '.keys|length'
```

---

## 4. Docker volume recovery

A "lost docker volume" usually means: `docker volume rm`, `docker-compose down -v`, `make purge` / `make reset-dev` (both documented destructive), or volume-driver corruption.

```bash
docker volume ls --filter "name=lightweight-saas-backend"
# Expected:
#   lightweight-saas-backend_postgres_data
#   lightweight-saas-backend_keycloak_postgres_data
```

The `lightweight-saas-backend_` prefix is the compose project name (from the directory name). Different directory → different prefix; verify with `docker volume ls` before any `rm`.

### 4.1 Lost only the app DB volume

```bash
docker-compose stop api postgres
docker volume rm lightweight-saas-backend_postgres_data
docker-compose up -d postgres
until docker exec saas-postgres pg_isready -U postgres -d lightweight_saas_backend_db >/dev/null 2>&1; do sleep 1; done
# Now run §3.1 restore, then:
docker-compose up -d api
```

### 4.2 Lost only the Keycloak DB volume

```bash
docker-compose stop api keycloak keycloak-postgres
docker volume rm lightweight-saas-backend_keycloak_postgres_data
docker-compose up -d keycloak-postgres
until docker exec saas-keycloak-postgres pg_isready -U keycloak -d keycloak >/dev/null 2>&1; do sleep 1; done

# Path A: you have a kcdb pg_dump → §3.3b
# Path B: only seed realm file → bring KC up cold; entrypoint re-imports realm-export.json
docker-compose up -d keycloak
docker logs -f saas-keycloak 2>&1 | grep -m1 "Imported realm"
docker-compose up -d api
```

### 4.3 Lost both volumes

Do §4.1 and §4.2 in either order — independent. **Restore order rule:** start DB container → wait for `pg_isready` → restore dump → start consumer (KC or API). Never start the consumer against a half-loaded schema.

---

## 5. Disaster recovery — full host loss

You lost the host. Off-site archive store survived.

```bash
# 1. New host, same Docker version (tested: Docker Engine ≥ 24 / Colima on macOS).

# 2. Recover the repo
git clone https://github.com/joaogabrielvianna/lightweight-saas-backend.git
cd lightweight-saas-backend
git checkout <tag-or-sha-that-was-running>

# 3. Restore .env and config
tar xzf /restore/env/env-LATEST.tgz

# 4. Infra only (no api — DB is empty)
make up-infra

# 5. App DB
docker exec -i saas-postgres psql -U postgres -d postgres -c \
    "CREATE DATABASE lightweight_saas_backend_db OWNER postgres;"
docker exec -i saas-postgres pg_restore -U postgres -d lightweight_saas_backend_db \
    --no-owner --no-privileges --exit-on-error \
  < /restore/appdb/appdb-LATEST.pgcustom

# 6. Keycloak DB (stop KC first — row locks)
docker-compose stop keycloak
docker exec -i saas-keycloak-postgres psql -U keycloak -d postgres <<'SQL'
DROP DATABASE IF EXISTS keycloak;
CREATE DATABASE keycloak OWNER keycloak;
SQL
docker exec -i saas-keycloak-postgres pg_restore -U keycloak -d keycloak \
    --no-owner --no-privileges --exit-on-error \
  < /restore/kcdb/kcdb-LATEST.pgcustom
docker-compose up -d keycloak

# 7. Bring up api + smoke
docker-compose up -d api
curl -sf http://localhost:8080/health
TOK=$(curl -s -X POST http://localhost:8081/realms/saas/protocol/openid-connect/token \
        -d "client_id=$KEYCLOAK_CLIENT_ID" -d "client_secret=$KEYCLOAK_CLIENT_SECRET" \
        -d "grant_type=password" -d "username=adminuser" -d "password=$SEED_USER_PASSWORD" \
        -d "scope=openid" | jq -r .access_token)
curl -sf http://localhost:8080/admin/users -H "Authorization: Bearer $TOK" | jq '.count'
```

Expected: `200 OK`, non-zero `count`.

---

## 6. Migrations & schema rollback

> **Honest disclosure.** Schema migrations are driven by GORM `AutoMigrate` ([`internal/database/database.go:33`](../../internal/database/database.go#L33)) — **additive only**. No `migrate down` button. Schema rollback is a **manual SQL** exercise.

### 6.1 Before any deploy that bumps the schema

```bash
# Always. Tag the dump with the SHA so you know which schema version it was taken under.
docker exec saas-postgres pg_dump \
    -U postgres -d lightweight_saas_backend_db -F c -Z 9 \
  > "$BACKUPS/appdb/predeploy-$(git rev-parse --short HEAD)-$(date +%Y%m%d-%H%M%S).pgcustom"
```

### 6.2 If `AutoMigrate` broke the schema post-deploy

`AutoMigrate` failures surface as an API container that panics on boot, with the DB mid-migration.

```bash
# 1. Stop the new api
docker-compose stop api

# 2. Diff live schema vs pre-deploy dump to see what AutoMigrate changed
docker exec saas-postgres pg_dump --schema-only -U postgres -d lightweight_saas_backend_db > /tmp/schema-now.sql
docker run --rm -i postgres:15-alpine pg_restore -l \
    < "$BACKUPS/appdb/predeploy-XXXXXX.pgcustom" > /tmp/schema-pre.sql
diff /tmp/schema-pre.sql /tmp/schema-now.sql

# 3. Hand-craft the inverse SQL from the diff, e.g.:
#    ALTER TABLE users DROP COLUMN new_field;
#    DROP INDEX IF EXISTS idx_users_new_field;
docker exec -i saas-postgres psql -U postgres -d lightweight_saas_backend_db < rollback.sql

# 4. Pin api image back to previous SHA, rebuild, verify
git checkout <previous-sha> -- Dockerfile cmd internal
docker-compose up -d --build api
curl -sf http://localhost:8080/health
```

If the inverse SQL is too risky under time pressure, **fall back to full restore from the §6.1 dump** (procedure §3.1). You lose rows written between deploy and rollback — trade-off vs. busted schema.

### 6.3 If migration broke Keycloak

Keycloak migrations are Quarkus/Liquibase inside the KC image; they run on version change.

```bash
# Pin previous KC version in docker-compose.yml:  image: quay.io/keycloak/keycloak:<previous>
docker-compose up -d keycloak
# Liquibase rolls back to that version's expected schema.
```

If schema is unrecoverable: restore the KC DB dump from before the version bump (§3.3b), then start the previous-version image.

### 6.4 If `.env` was lost and no off-site copy exists

Regen pipeline preserves existing secrets in `.env`. If `.env` is **completely** gone:

```bash
make regen   # rebuilds .env from config/project.json, generating NEW secrets
```

This rotates `KEYCLOAK_CLIENT_SECRET`, `KEYCLOAK_ADMIN_CLIENT_SECRET`, and seed-user passwords. You must then either re-export with `make keycloak-export`, or update live realm client secrets via Admin UI / `kcadm` before any client can re-authenticate.

**Archive `.env` off-site** with the DB dumps (§2.4) so this path is never needed.

---

## 7. Verification checklist (post-restore)

Run all six. Green on each = "restore complete".

| # | Check | Command | Expected |
|---|-------|---------|----------|
| 1 | Stack health | `docker-compose ps` | All 5 services `Up (healthy)` |
| 2 | API liveness | `curl -sf http://localhost:8080/health` | `{"status":"ok"}` |
| 3 | KC OIDC discovery | `curl -sf http://localhost:8081/realms/saas/.well-known/openid-configuration \| jq .issuer` | `"http://localhost:8081/realms/saas"` |
| 4 | Admin login | `curl -s -X POST http://localhost:8081/realms/saas/protocol/openid-connect/token -d "client_id=saas-backend" -d "client_secret=$KEYCLOAK_CLIENT_SECRET" -d "grant_type=password" -d "username=adminuser" -d "password=$SEED_USER_PASSWORD" -d "scope=openid" \| jq -r .access_token \| wc -c` | non-zero token length |
| 5 | Identity surface | `curl -sf http://localhost:8080/admin/users -H "Authorization: Bearer $TOK" \| jq .count` | ≥ 1 (the seeded admin) |
| 6 | App row count | `docker exec saas-postgres psql -U postgres -d lightweight_saas_backend_db -c "SELECT count(*) FROM users;"` | matches pre-loss expectation |

---

## 8. Common failures

| Symptom | Fix |
|---------|-----|
| `pg_restore: ... input file appears to be a text format dump` | You passed a plain-text dump to `pg_restore`. Use `psql` for plain text, `pg_restore` for `-F c` / `-F d`. |
| `pg_restore: ... role "postgres" does not exist` | Restored into a different role-name Postgres. Pass `--no-owner --no-privileges` (already in all commands above). |
| Keycloak skips realm import on start | `--import-realm` only imports if realm is ABSENT. Force re-import: delete realm via Admin UI/`kcadm`, restart KC. Or restore the KC DB dump (§3.3b). |
| `No such volume` | Compose prefixes with project name (defaults to dir basename). Different dir → different prefix. Confirm with `docker volume ls --filter "name=postgres_data"`. |
| `pg_dump`: relation does not exist (PG major mismatch) | Postgres majors are NOT cross-compatible at the data-dir level. To bump major: pin the OLD image, `pg_dump`, bump image, recreate volume, restore. |
| `--clean --if-exists` errors with FK dependency drops | Drop and recreate the whole database instead: `DROP DATABASE … ; CREATE DATABASE …` then `pg_restore`. |
| `make realm-reset` aborted halfway | `docker-compose down keycloak keycloak-postgres` → `docker volume rm lightweight-saas-backend_keycloak_postgres_data` → `docker-compose up -d keycloak-postgres keycloak`. |
| API panics on boot post-restore (`column ... does not exist`) | Schema older than api binary. Either roll api back (`git checkout <prev-sha>`) or let AutoMigrate run forward. If AutoMigrate fails, restore api to previous SHA and file a forward-migration bug. |
| `[identity-kc] compensating delete failed` lines | Invitation rollback path tried and failed (KC temporarily unavailable). Manually clean orphan: get an admin token via `client_credentials` against `saas-backend-admin`, then `DELETE /admin/realms/saas/users/<user-id>`. Background: [BUG_REPORT_CRUD.md §I14b](../validation/BUG_REPORT_CRUD.md). |

---

## 9. Tested gaps & TODOs

Describes **what works today**. The following would tighten the RPO/RTO targets in §1 and remain backlog:

- **Streaming WAL archiving / PITR** — drops RPO from "since last nightly dump" to "since last WAL segment shipped". Needs `archive_mode=on`, archive destination (S3 / shared mount), WAL-replay script.
- **Backup verification job** — daily `pg_restore -l | grep -c TABLE` on the latest archive, fail loudly if truncated. Cron-friendly, ~10 lines of shell.
- **Off-site archive automation** — §2.5 cron writes to a local path; add `aws s3 cp` / `restic backup` step.
- **`make backup` / `make restore` targets** — Makefile exposes `keycloak-export` / `keycloak-import` / `realm-reset`; add `backup-appdb`, `backup-kcdb`, restore wizard.
- **Schema migration tool with down-migrations** — replace `AutoMigrate` with `golang-migrate` / `goose` to make §6 rollback declarative instead of hand-crafted SQL.

None block the procedures above from working as documented.
