-- Down for 000006_audit_events.
--
-- DESTRUCTIVE: drops the entire durable audit trail. There is no other copy —
-- the structured log stream carries the same events only if someone is shipping
-- logs, and the in-process ring holds the last few hundred at best. Rolling
-- back past this version discards history that cannot be reconstructed.
--
-- Dropping the table drops its indexes and constraints with it.
DROP TABLE IF EXISTS audit_events;

-- Restore the pre-000006 scope vocabulary.
--
-- This can FAIL, and failing is correct. If any live credential was granted
-- `audit:read` while this migration was applied, the narrower CHECK will not
-- validate and the rollback stops with a constraint violation naming the table.
--
-- The alternative — stripping the scope from those rows to make the constraint
-- fit — would silently revoke a capability an operator granted, during a
-- rollback, with no record that it happened. A rollback that refuses and says
-- why is recoverable; one that quietly changes authorization is not.
--
-- To proceed deliberately: remove 'audit:read' from the affected credentials
-- through the API (or re-issue them without it), then re-run the rollback.
ALTER TABLE project_credentials
    DROP CONSTRAINT IF EXISTS project_credentials_scopes_known;

ALTER TABLE project_credentials
    ADD CONSTRAINT project_credentials_scopes_known
        CHECK (scopes <@ ARRAY[
            'users:read', 'users:write',
            'roles:read', 'roles:write',
            'sessions:read', 'sessions:revoke',
            'invitations:read', 'invitations:write'
        ]::text[]);
