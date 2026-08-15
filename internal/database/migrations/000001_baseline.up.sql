-- 000001_baseline — the schema as it existed under gorm AutoMigrate.
--
-- This migration is a byte-for-byte reproduction of what
-- `db.AutoMigrate(&user.User{})` produced against PostgreSQL 15, captured by
-- running it against an empty database and reading the resulting catalog:
--
--   CREATE TABLE "users" ("id" bigserial,"keycloak_sub" text NOT NULL,
--     "email" text,"username" text NOT NULL,"created_at" timestamptz,
--     "updated_at" timestamptz,PRIMARY KEY ("id"))
--   CREATE INDEX IF NOT EXISTS "idx_users_email" ON "users" ("email")
--   CREATE UNIQUE INDEX IF NOT EXISTS "idx_users_keycloak_sub" ON "users" ("keycloak_sub")
--
-- `bigserial PRIMARY KEY` yields the same catalog objects AutoMigrate left
-- behind — sequence `users_id_seq` and constraint `users_pkey` — so a database
-- bootstrapped from this file is indistinguishable from one bootstrapped by
-- AutoMigrate.
--
-- IDEMPOTENCY IS LOAD-BEARING. Installations that predate versioned migrations
-- already have this schema but no `schema_migrations` row, so golang-migrate
-- WILL run this file against them. Every statement here is IF NOT EXISTS and
-- there is no ALTER, so on such a database the migration is a no-op that only
-- records version 1. Adoption needs no manual intervention.
--
-- Do not add tables here. New objects belong in a new numbered migration.

CREATE TABLE IF NOT EXISTS users (
    id           bigserial   PRIMARY KEY,
    keycloak_sub text        NOT NULL,
    email        text,
    username     text        NOT NULL,
    created_at   timestamptz,
    updated_at   timestamptz
);

CREATE INDEX IF NOT EXISTS idx_users_email ON users (email);

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_keycloak_sub ON users (keycloak_sub);
