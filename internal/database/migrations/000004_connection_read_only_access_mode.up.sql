-- 000004_connection_read_only_access_mode (up)
--
-- Widens connections.access_mode to admit 'read_only', the fourth value
-- introduced when TD-024 was resolved.
--
-- Before this, access_mode was {unknown, full, limited} and 'full' meant "both
-- admin READS succeeded". A service account with view-users but not
-- manage-users passed both reads and was recorded as 'full', so the API
-- claimed write capability it had never proven. The verifier now proves the
-- write grant from the provider's own token and records 'read_only' for the
-- account that provably cannot mutate.
--
-- Data: no rows are rewritten. An existing 'full' row keeps its value until
-- the connection is verified again, at which point the probe re-grades it
-- honestly. Rewriting stored verdicts here would be inventing a verification
-- that never ran.

ALTER TABLE connections
    DROP CONSTRAINT IF EXISTS connections_access_mode_check;

ALTER TABLE connections
    ADD CONSTRAINT connections_access_mode_check
    CHECK (access_mode IN ('unknown', 'full', 'read_only', 'limited'));
