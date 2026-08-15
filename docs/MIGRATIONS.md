# Database Migrations

**Scope:** how the application database schema is created, changed and
inspected. Applies to the application database only — Keycloak owns its own
schema and manages it independently ([`keycloak-postgres`](../docker-compose.yml)).

**Status:** versioned SQL migrations, live since v0.4. Replaces the gorm
`AutoMigrate` call that previously ran inside `database.Connect`
([TD-005](TECH_DEBT.md#td-005) / [V1-07](ROADMAP.md#v1-07--versioned-migrations),
both closed by this change).

---

## 1. How it works

| Piece | Where |
|---|---|
| Migration files | [`internal/database/migrations/`](../internal/database/migrations/) |
| Embedding | [`internal/database/migrations.go`](../internal/database/migrations.go) — `go:embed migrations/*.sql` |
| Runner | [`internal/database/migrate.go`](../internal/database/migrate.go) — [golang-migrate](https://github.com/golang-migrate/migrate) with the `iofs` source and `pgx/v5` database drivers |
| Dev CLI | [`cmd/migrate`](../cmd/migrate/main.go), driven by `make migrate*` |
| Boot hook | [`database.Connect`](../internal/database/database.go), gated by `DB_MIGRATE_ON_BOOT` |

The migrations are **compiled into the binary**. The API image is built `FROM
scratch` and contains the binary and nothing else, so a migration set living on
disk would have to be copied in and kept in sync — an easy way to boot a binary
against the wrong schema. Embedded, code and schema are one artifact and cannot
drift.

golang-migrate records progress in a `schema_migrations` table (`version`,
`dirty`) and takes a PostgreSQL advisory lock while it runs, so several API
replicas booting at once cannot interleave.

### At boot

`database.Connect` applies pending migrations before returning. Any failure is
fatal — the process exits rather than serve traffic against a schema of unknown
shape.

Set `DB_MIGRATE_ON_BOOT=false` to opt out. Do that when migrations belong to a
separate deploy step (an init container, a release job), typically because the
runtime database role is not allowed to run DDL. With it off **nothing checks
the schema at boot**; it must already be current.

---

## 2. Commands

All read `DB_URL` (from the environment or `.env`).

| Command | Does |
|---|---|
| `make migrate` | Apply all pending migrations |
| `make migrate-version` | Print `version=N dirty=false`. Exits non-zero when dirty |
| `make migrate-steps N=-1` | Apply `N` migrations, or revert `-N` |
| `make migrate-down` | Revert **everything** — drops every table. Prompts first |
| `make migrate-force VERSION=n` | Recovery only, see §5 |
| `make migrate-new NAME=add_workspaces` | Scaffold an up/down pair |

Nothing needs to be installed: the commands run `go run ./cmd/migrate`, not the
golang-migrate CLI.

---

## 3. Adding a migration

```bash
make migrate-new NAME=add_workspaces
# → internal/database/migrations/000002_add_workspaces.up.sql
# → internal/database/migrations/000002_add_workspaces.down.sql
```

Rules, all enforced by `TestEmbeddedMigrations_Naming`:

- **Name:** `{6-digit version}_{lower_snake_title}.{up|down}.sql`. A file that
  does not match is **silently ignored** by the source driver — the worst
  possible failure, hence the test.
- **Versions are dense and sequential** starting at 1. Gaps are legal for
  golang-migrate but make "which version am I on" ambiguous for whoever reads
  `make migrate-version` at 3am.
- **Every up has a down.** A down that does not restore the previous schema is
  worse than no down at all.
- **Never edit an applied migration.** It has already run somewhere; editing it
  changes only fresh databases and silently forks the schema. Add a new one.
- **Prefer `IF NOT EXISTS` on creates**, so a partially-applied deploy can be
  retried rather than force-repaired.

Then: `make migrate` locally, `make test-integration` to prove it, and commit
the SQL with the code that needs it.

---

## 4. The baseline, and databases created before migrations existed

[`000001_baseline.up.sql`](../internal/database/migrations/000001_baseline.up.sql)
is a byte-for-byte reproduction of what `AutoMigrate` produced for the `users`
table on PostgreSQL 15 — captured by running it against an empty database and
reading the resulting catalog, not reconstructed from the struct tags.

Installations that predate versioned migrations already have that schema but no
`schema_migrations` row, so golang-migrate **will** run the baseline against
them. Every statement in it is `IF NOT EXISTS` and there is no `ALTER`, so on
such a database the migration is a no-op that only records version 1.

**Adoption therefore requires no manual step.** Upgrade the binary, boot it, and
the existing database is picked up untouched. This is covered end to end by
`TestMigrate_AdoptsLegacyAutoMigrateSchema`, which builds a database with the
original `AutoMigrate` DDL, puts a row in it, migrates, and asserts both the
schema and the row survive.

The baseline's `down` drops the `users` table. That exists so the set stays
reversible in development; it is not an operational rollback.

---

## 5. When a migration fails

A migration that dies part-way leaves `schema_migrations.dirty = true`. Every
subsequent `migrate` and every boot then fails immediately, on purpose: the
schema shape is unknown and nobody should be serving traffic against it.

```
database is dirty at version 2: a previous migration failed part-way.
Inspect the schema, then run `make migrate-force VERSION=<actual>` …
```

Recovery, in order:

1. **Look at the database.** Determine which version the schema actually
   matches. Do not guess.
2. **Finish or undo by hand** if it landed between versions, so it matches a
   real one.
3. `make migrate-force VERSION=<the version it really matches>` — this writes
   the version and clears the dirty flag **without running any SQL**. Forcing a
   version the schema does not match leaves the database permanently out of sync
   with the migration set, and every later migration will compound the error.
4. `make migrate` to continue.

---

## 6. Testing

| Suite | Command | Covers |
|---|---|---|
| Unit | `make test` | The embedded set loads, naming and up/down pairing, baseline idempotency and shape, CLI argument handling |
| Integration | `make test-integration` (needs postgres) | Empty-database bootstrap, adoption of an `AutoMigrate` database, idempotent re-runs, dirty-state failure and recovery, up/down/steps |

The integration tests each run in their own PostgreSQL schema (via
`search_path`), so they need no privileges beyond `CREATE SCHEMA` and leave
nothing behind.

---

## See also

- [ARCHITECTURE.md](ARCHITECTURE.md) — where the database sits in the stack
- [MODULES.md](MODULES.md) — the `internal/database` module reference
- [operations/PRODUCTION_DEPLOYMENT.md](operations/PRODUCTION_DEPLOYMENT.md) — deploying an upgrade
