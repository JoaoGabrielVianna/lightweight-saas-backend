-- Down for 000003_connections.
--
-- DESTRUCTIVE: drops every connection, including the sealed provider
-- credentials. Those cannot be recovered from a schema rollback — the operator
-- has to re-enter them. Dropping the table also drops its constraints, both
-- indexes, and the foreign key to workspaces.
--
-- Scope is exactly one table. Reverting to version 2 must leave `workspaces`
-- (000002) and the `users` baseline (000001) intact;
-- TestMigration000003_DownPreservesEarlierMigrations asserts that.

DROP TABLE IF EXISTS connections;
