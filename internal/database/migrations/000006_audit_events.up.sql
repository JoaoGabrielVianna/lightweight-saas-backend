-- 000006_audit_events — a durable, workspace-scoped audit trail.
--
-- Until now audit events went to two places: a structured log line (durable
-- only if someone is shipping logs) and a 500-entry in-process ring buffer that
-- a restart empties. For a product that administers IAM, that means the answer
-- to "who revoked that credential last week" is "nobody knows" ([TD-008]).
--
-- ─── What this table is, and is not ─────────────────────────────────────────
--
-- It is a record of ATTEMPTS TO CHANGE STATE, by an identified actor. It is not
-- a request log: reads are not here, health checks are not here, and neither is
-- traffic that never got past authentication. A row exists because somebody who
-- had been identified tried to change something.
--
-- That distinction is what keeps the table bounded by real activity rather than
-- by traffic. An anonymous flood produces zero rows.
--
-- ─── Append-only by use, not by grant ───────────────────────────────────────
--
-- Nothing in the application updates or deletes a row except the retention
-- sweep, which deletes only by age. There is deliberately no WORM enforcement,
-- no signing and no separate database role: those defend against an attacker
-- who already has database credentials, which is a threat model this product
-- does not otherwise defend against, and pretending otherwise would be worse
-- than being clear about it.
--
-- ─── Why every column is nullable-or-not the way it is ──────────────────────
--
-- The NOT NULLs are the fields without which a row cannot answer a question:
-- what happened, when, whether it worked. Everything else is genuinely absent
-- sometimes, and a placeholder would be a lie — an event with no workspace came
-- from the legacy unscoped surface, and writing 'unknown' would make that
-- indistinguishable from a bug.
--
-- Idempotent (IF NOT EXISTS throughout), matching 000001-000005.

CREATE TABLE IF NOT EXISTS audit_events (
    id uuid PRIMARY KEY,

    -- The workspace whose state was touched, as a UUID foreign key.
    --
    -- NULL is legal and means "not workspace-scoped": the legacy /admin/*
    -- surface acts on the process-level realm and belongs to no workspace. The
    -- workspace audit API filters on `workspace_id = $1`, so a NULL row can
    -- never be returned by it — global events are unreachable through a
    -- workspace-scoped endpoint by construction rather than by a WHERE clause
    -- someone has to remember.
    --
    -- ON DELETE RESTRICT, like every other workspace reference here. Workspaces
    -- are archived and never deleted; a cascade could only fire from a manual
    -- psql session, which is exactly when silently destroying the history of a
    -- tenant is least wanted.
    workspace_id uuid NULL REFERENCES workspaces (id) ON DELETE RESTRICT,

    -- The canonical verb, e.g. 'credential.revoked'. Deliberately NOT a CHECK
    -- constraint: the vocabulary is owned by internal/audit and grows with
    -- every slice, and a CHECK would make adding an event a migration while
    -- buying nothing — an unknown value here is a Go bug that a completeness
    -- test catches at build time, not a data-integrity problem.
    --
    -- Contrast with project_credentials.scopes, which IS constrained: a scope
    -- is an authorization grant, and one that reached the database unrecognised
    -- would be honoured forever.
    event_type text NOT NULL,

    -- 'success' or 'failure'. Two values, closed set, so a CHECK is free and
    -- stops a third spelling appearing in a dashboard six months from now.
    outcome text NOT NULL,

    -- ── Actor ───────────────────────────────────────────────────────────────
    --
    -- Disjoint by type, exactly as audit.Actor is in Go:
    --
    --   operator → actor_subject, actor_email
    --   project  → actor_project_id, actor_credential_id
    --
    -- A PROJECT ID MUST NEVER APPEAR IN actor_subject. That column means "a
    -- Keycloak sub", and every reader treats it that way; a `prj_` value there
    -- would make a machine indistinguishable from a person in precisely the
    -- records that exist to tell them apart. The CHECK below enforces it in the
    -- database rather than leaving it to the mapper.
    actor_type text NOT NULL,
    actor_subject text NULL,
    actor_email text NULL,

    -- Public ids (`prj_…`, `key_…`) as text, not foreign keys.
    --
    -- Not a reference on purpose: audit history must survive the thing it
    -- describes. A credential is revoked and eventually its project archived,
    -- and the record of what that credential did has to outlive both. A foreign
    -- key would make deleting a project either impossible or history-destroying.
    actor_project_id text NULL,
    actor_credential_id text NULL,

    -- ── Resource ────────────────────────────────────────────────────────────
    --
    -- What was acted upon. A closed set of kinds, as text with a CHECK rather
    -- than a polymorphic join table: there is nothing to join TO — a Keycloak
    -- user id is not a row here — and the only operations are equality and
    -- display.
    resource_type text NULL,
    resource_id text NULL,

    -- ── Correlation ─────────────────────────────────────────────────────────
    request_id text NULL,

    -- The address the server observed, as gin resolves it (honouring the
    -- forwarded headers a deployment behind a proxy needs). Validated to parse
    -- as an IP before it is written, so this column holds an address or NULL
    -- and never caller-supplied free text.
    source_ip text NULL,

    -- Machine-readable failure reason, drawn from the /v1 error vocabulary
    -- ('provider_unavailable', 'role_privileged', …). NEVER an exception
    -- string: an upstream message can contain a token, a SQL fragment or a
    -- customer's email, and this table is readable by anyone with audit:read.
    reason_code text NULL,

    -- Per-event detail, allowlisted per event type by the Go layer. Never a
    -- request body.
    metadata jsonb NULL,

    occurred_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT audit_events_outcome_check
        CHECK (outcome IN ('success', 'failure')),

    CONSTRAINT audit_events_actor_type_check
        CHECK (actor_type IN ('operator', 'project')),

    -- The actor discriminant, enforced. An operator row carries no project
    -- fields and a project row carries no subject — so the disjointness is a
    -- guarantee about the data rather than a property of whichever mapper
    -- happened to write it.
    CONSTRAINT audit_events_actor_shape_check
        CHECK (
            (actor_type = 'operator'
                AND actor_project_id IS NULL
                AND actor_credential_id IS NULL)
            OR
            (actor_type = 'project'
                AND actor_subject IS NULL
                AND actor_email IS NULL
                AND actor_project_id IS NOT NULL)
        ),

    CONSTRAINT audit_events_resource_type_check
        CHECK (resource_type IS NULL OR resource_type IN (
            'workspace', 'connection', 'project', 'project_credential',
            'user', 'role', 'session', 'invitation'
        )),

    CONSTRAINT audit_events_event_type_not_blank
        CHECK (length(btrim(event_type)) > 0)
);

-- ─── Indexes ────────────────────────────────────────────────────────────────
--
-- Designed against the queries that exist, not against the columns that exist.
--
-- There is exactly ONE listing query, and every filter narrows it:
--
--   WHERE workspace_id = $1  [AND event_type = …] [AND actor_type = …]
--                            [AND outcome = …] [AND occurred_at BETWEEN …]
--     AND (occurred_at, id) < ($cursor_ts, $cursor_id)
--   ORDER BY occurred_at DESC, id DESC
--   LIMIT $n
--
-- The composite below is declared ASCENDING, and that is deliberate rather than
-- an oversight.
--
-- The obvious spelling is `(workspace_id, occurred_at DESC, id DESC)`, matching
-- the ORDER BY. It was measured and it is WRONG: with DESC columns PostgreSQL
-- would not turn the row-value comparison `(occurred_at, id) < (…)` into an
-- index condition, fell back to the occurred_at-only index, and filtered by
-- workspace afterwards. On a twenty-workspace dataset that meant touching ~1000
-- rows and running an Incremental Sort to return a 50-row page — degrading
-- linearly with tenant count, which is precisely what this index exists to
-- prevent.
--
-- A btree can be scanned in either direction, so an ASC index serves
-- `ORDER BY occurred_at DESC, id DESC` by a backward scan, and the ASC ordering
-- is what lets the row-value comparison become a range condition. The result is
-- an index-only walk that stops at LIMIT rows: no sort node, and cost bounded
-- by the page rather than by the history or the number of tenants.
--
-- TestIntegration_ListUsesTheCompositeIndex asserts both properties against a
-- real planner, which is the only way this stays true.
--
-- One index, not three. A partial index per filter would each serve a fraction
-- of queries while every INSERT pays for all of them, and audit is
-- append-heavy: writes are the common case and reads are an operator opening a
-- page. The filters are additional predicates on rows this index already
-- narrowed to one workspace's page — cheap, because there are at most `limit`
-- of them under consideration.
--
-- If a workspace ever accumulates enough history that filtering a rare
-- event_type has to scan far, THAT is when a second index earns its place, and
-- the EXPLAIN in docs/AUDIT.md is the baseline to compare against.
CREATE INDEX IF NOT EXISTS audit_events_workspace_time_idx
    ON audit_events (workspace_id, occurred_at, id);

-- Retention sweeps delete by age across ALL workspaces, which the composite
-- above cannot serve: its leading column is workspace_id, so a sweep without
-- this index is a sequential scan of the whole table.
--
-- BRIN, not btree, and the reason was measured rather than assumed.
--
-- A plain `CREATE INDEX ... (occurred_at)` works for the sweep and ACTIVELY
-- HARMS the listing query. The planner preferred it for
-- `WHERE workspace_id = ? AND occurred_at <= ? ORDER BY occurred_at DESC`,
-- scanned it backwards, filtered the other tenants out afterwards and added an
-- Incremental Sort: on a twenty-workspace dataset that was 969 rows discarded
-- to return 51, degrading linearly with tenant count. Two indexes, and the
-- cheap one for the daily job was stealing the hot query.
--
-- BRIN is the right shape for this data and cannot be stolen for that plan:
--
--   it fits    audit_events is append-only and occurred_at is monotonic, which
--              is exactly the physical/logical correlation BRIN summarises.
--   it is tiny a handful of pages regardless of table size, so the write cost
--              on every INSERT is negligible.
--   it cannot  BRIN is lossy and provides no ordering, so the planner will not
--   compete    choose it for an ordered LIMIT — leaving the composite index to
--              serve the listing, which is what it is for.
--
-- Measured after the change: the listing is an Index Scan Backward on the
-- composite with no sort node, and the DELETE is a Bitmap Index Scan on this
-- one. Both are asserted by integration tests against a real planner.
CREATE INDEX IF NOT EXISTS audit_events_occurred_at_brin
    ON audit_events USING brin (occurred_at);

-- ─── The audit:read scope ───────────────────────────────────────────────────
--
-- Adding a scope is a migration, deliberately: a scope is a change to the
-- authorization contract, and making it cost a migration is what stops the set
-- drifting into dozens of half-meant permissions.
--
-- The constraint is dropped and recreated rather than altered, because
-- PostgreSQL has no ALTER CONSTRAINT for a CHECK expression. Both statements
-- are guarded so a retried deploy re-runs cleanly.
--
-- This is safe on a live table: the new set is a strict superset of the old, so
-- every existing row satisfies it. The validation scan is over
-- project_credentials, which holds at most ten live rows per project.
ALTER TABLE project_credentials
    DROP CONSTRAINT IF EXISTS project_credentials_scopes_known;

ALTER TABLE project_credentials
    ADD CONSTRAINT project_credentials_scopes_known
        CHECK (scopes <@ ARRAY[
            'users:read', 'users:write',
            'roles:read', 'roles:write',
            'sessions:read', 'sessions:revoke',
            'invitations:read', 'invitations:write',
            'audit:read'
        ]::text[]);
