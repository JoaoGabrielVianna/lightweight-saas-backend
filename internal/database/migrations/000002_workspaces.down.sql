-- Down for 000002_workspaces.
--
-- DESTRUCTIVE: drops every workspace. Dropping the table also drops its
-- constraints and both indexes, so no explicit DROP INDEX is needed.
--
-- Scope is deliberately exactly one table. Reverting to version 1 must leave
-- the 000001 baseline — the `users` table and its indexes — completely intact;
-- `TestMigration000002_DownPreservesBaseline` asserts that.

DROP TABLE IF EXISTS workspaces;
