-- Down for 000005_projects.
--
-- DESTRUCTIVE: drops every project and every credential. Credentials cannot be
-- recovered from a schema rollback — their secrets were never stored, only
-- digests, so every backend holding one has to be issued a new key by hand.
--
-- Order matters: project_credentials references projects with ON DELETE
-- RESTRICT, so the child table goes first. Dropping the tables drops their
-- constraints and indexes with them.
--
-- Scope is exactly these two tables. Reverting to version 4 must leave
-- connections (000003), workspaces (000002) and the users baseline (000001)
-- intact.

DROP TABLE IF EXISTS project_credentials;
DROP TABLE IF EXISTS projects;
