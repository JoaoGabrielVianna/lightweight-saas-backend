-- 000004_connection_read_only_access_mode (down)
--
-- Restores the three-value CHECK.
--
-- Rows already carrying 'read_only' would violate it, so they are folded to
-- 'unknown' first. That is the honest target: the old vocabulary has no way to
-- say "reads work, writes provably do not", and 'full' would re-introduce the
-- exact over-claim this migration exists to remove. 'unknown' means "not
-- determined", which after a rollback is true — the running code can no longer
-- determine it.

UPDATE connections
    SET access_mode = 'unknown'
    WHERE access_mode = 'read_only';

ALTER TABLE connections
    DROP CONSTRAINT IF EXISTS connections_access_mode_check;

ALTER TABLE connections
    ADD CONSTRAINT connections_access_mode_check
    CHECK (access_mode IN ('unknown', 'full', 'limited'));
