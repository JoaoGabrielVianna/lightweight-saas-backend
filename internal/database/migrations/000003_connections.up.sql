-- 000003_connections — a Workspace's configured access to an identity provider.
--
-- A Connection holds the coordinates and credentials for reaching one identity
-- provider on behalf of one Workspace. Nothing in the running system consumes
-- it yet: the Identity API still uses the process-level Keycloak configuration.
-- Wiring the two together is a later slice. Nothing here touches `users`
-- (000001) or `workspaces` (000002) beyond the foreign key.
--
-- THE SECRET IS NEVER STORED IN PLAINTEXT. The provider's client secret is
-- sealed with AES-256-GCM by internal/secrets before it reaches this table, and
-- only the ciphertext and its nonce are persisted. `secret_key_version` and
-- `secret_alg` are stored alongside so a future key rotation can re-wrap rows
-- in place and tell wrapped-with-what from wrapped-with-what — rotation itself
-- is not implemented, but the format does not have to change to add it.
--
-- ONE ACTIVE CONNECTION PER WORKSPACE is enforced by a partial unique index,
-- not by application code. A service-level check cannot hold under two
-- concurrent activations; the index can, and makes the loser a deterministic
-- conflict rather than a second active row nobody notices until traffic is
-- being routed by it.
--
-- Idempotent (IF NOT EXISTS on the table and every index), matching 000001 and
-- 000002, so a retried deploy re-runs cleanly.

CREATE TABLE IF NOT EXISTS connections (
    id           uuid        PRIMARY KEY,

    -- ON DELETE RESTRICT, deliberately. Workspaces are archived, never deleted
    -- (there is no DELETE on the workspace API at all), so a cascade would only
    -- ever fire on a manual psql session — precisely when silently destroying
    -- provider credentials is least wanted.
    workspace_id uuid        NOT NULL REFERENCES workspaces (id) ON DELETE RESTRICT,

    name         text        NOT NULL,

    -- One provider exists. The column and its CHECK are here so adding a second
    -- is a migration with an explicit decision behind it, rather than an
    -- untyped string that silently accepts anything.
    provider     text        NOT NULL DEFAULT 'keycloak',

    status       text        NOT NULL DEFAULT 'draft',

    -- Provider coordinates. base_url is the URL this API uses to REACH the
    -- provider, which in docker is not the URL browsers use.
    base_url     text        NOT NULL,
    realm        text        NOT NULL,
    client_id    text        NOT NULL,

    -- Sealed credential. bytea, not text: this is ciphertext, and base64 in the
    -- database would only invite something to try to read it as a string.
    secret_ciphertext  bytea NOT NULL,
    secret_nonce       bytea NOT NULL,
    secret_key_version int   NOT NULL DEFAULT 1,
    secret_alg         text  NOT NULL DEFAULT 'aes-256-gcm',

    -- Result of the last verification probe. There is no background job: these
    -- change only when a verify runs.
    health         text        NOT NULL DEFAULT 'unknown',
    health_message text        NULL,
    access_mode    text        NOT NULL DEFAULT 'unknown',
    last_verified_at timestamptz NULL,

    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    activated_at timestamptz NULL,
    retired_at   timestamptz NULL,

    CONSTRAINT connections_provider_check
        CHECK (provider IN ('keycloak')),

    CONSTRAINT connections_status_check
        CHECK (status IN ('draft', 'active', 'retired')),

    CONSTRAINT connections_health_check
        CHECK (health IN ('unknown', 'healthy', 'unhealthy')),

    CONSTRAINT connections_access_mode_check
        CHECK (access_mode IN ('unknown', 'full', 'limited')),

    -- retired is terminal and always stamped; nothing else may carry the stamp.
    CONSTRAINT connections_retired_at_check
        CHECK (
            (status = 'retired' AND retired_at IS NOT NULL)
            OR
            (status <> 'retired' AND retired_at IS NULL)
        ),

    -- A draft has never been activated. active and retired both have been —
    -- retired keeps its activated_at, because when it went live is history
    -- worth having.
    CONSTRAINT connections_activated_at_check
        CHECK (
            (status = 'draft' AND activated_at IS NULL)
            OR
            (status = 'active' AND activated_at IS NOT NULL)
            OR
            (status = 'retired')
        ),

    -- health and last_verified_at move together: a verdict with no timestamp
    -- cannot be aged out, and a timestamp with no verdict says nothing.
    CONSTRAINT connections_verified_at_check
        CHECK (
            (health = 'unknown' AND last_verified_at IS NULL)
            OR
            (health <> 'unknown' AND last_verified_at IS NOT NULL)
        ),

    CONSTRAINT connections_name_not_blank_check
        CHECK (length(btrim(name)) > 0),

    CONSTRAINT connections_base_url_not_blank_check
        CHECK (length(btrim(base_url)) > 0),

    CONSTRAINT connections_realm_not_blank_check
        CHECK (length(btrim(realm)) > 0),

    CONSTRAINT connections_client_id_not_blank_check
        CHECK (length(btrim(client_id)) > 0),

    CONSTRAINT connections_secret_present_check
        CHECK (length(secret_ciphertext) > 0 AND length(secret_nonce) > 0),

    CONSTRAINT connections_secret_key_version_check
        CHECK (secret_key_version >= 1)
);

-- THE invariant of this table: at most one active connection per workspace.
--
-- Partial, so it constrains only active rows — a workspace may hold any number
-- of drafts and any amount of retired history, and those must not compete for
-- the slot. This is what makes activation safe to race: two concurrent
-- activations both try to insert into the same index slot and exactly one wins.
CREATE UNIQUE INDEX IF NOT EXISTS idx_connections_one_active_per_workspace
    ON connections (workspace_id)
    WHERE status = 'active';

-- Serves the per-workspace listing, which filters by workspace and orders by
-- (name, id).
CREATE INDEX IF NOT EXISTS idx_connections_workspace_name_id
    ON connections (workspace_id, name, id);
