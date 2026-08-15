-- Down for 000001_baseline.
--
-- DESTRUCTIVE: this drops the users table and every row in it. The baseline's
-- down path exists so the migration set is complete and reversible in tests and
-- local development — it is not an operational rollback for production. There
-- is no version 0 of this application worth returning to.
--
-- Dropping the table also drops idx_users_email, idx_users_keycloak_sub, the
-- users_pkey constraint and the users_id_seq sequence, so no explicit DROP
-- INDEX / DROP SEQUENCE is needed.

DROP TABLE IF EXISTS users;
