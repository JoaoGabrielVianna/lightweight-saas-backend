# The audit trail

**Last updated:** 2026-08-10

Who changed what, when, in which workspace, using which credential, and whether
it worked — kept in PostgreSQL, so a restart cannot erase it.

---

## 1. What it is, and what it is not

**Audit answers "who changed something".** Application logs answer "what is the
process doing". They are different questions and this is not a second log.

Deliberately absent from the trail:

- **reads.** `GET /users` is not history. Reading the audit page would otherwise
  create more history than the operations it displays.
- **health, readiness and metrics.**
- **traffic that never got past authentication.** An anonymous flood, or a
  scanner throwing invalid credentials, produces **zero rows** — otherwise the
  table would be a denial-of-service target.
- **rate-limit rejections** of unidentified callers, for the same reason.

What that buys: the table is bounded by real activity rather than by traffic,
and everything in it is attributable to somebody.

## 2. The event catalogue

Every mutating `/v1` route emits exactly one primary event. The registry in
[`internal/auditlog/coverage.go`](../internal/auditlog/coverage.go) declares
which, and a test fails the build if a mutating route has no entry — including a
route added in a future slice.

| Event | Producer | Actor | Workspace | Resource | Outcome |
|---|---|---|---|---|---|
| `workspace.created` | `internal/workspace` | operator | the new one (absent on failure) | workspace | ✅ |
| `workspace.renamed` | `internal/workspace` | operator | ✅ | workspace | ✅ |
| `workspace.archived` | `internal/workspace` | operator | ✅ | workspace | ✅ |
| `connection.created` | `internal/connection` | operator | ✅ | connection | ✅ |
| `connection.updated` | `internal/connection` | operator | ✅ | connection | ✅ |
| `connection.verified` | `internal/connection` | operator | ✅ | connection | ✅ |
| `connection.activated` | `internal/connection` | operator | ✅ | connection | ✅ |
| `connection.retired` | `internal/connection` | operator | ✅ | connection | ✅ |
| `connection.deleted` | `internal/connection` | operator | ✅ | connection | ✅ |
| `project.created` | `internal/project` | operator | ✅ | project | ✅ |
| `project.renamed` | `internal/project` | operator | ✅ | project | ✅ |
| `project.archived` | `internal/project` | operator | ✅ | project | ✅ |
| `project_credential.created` | `internal/project` | operator | ✅ | project_credential | ✅ |
| `project_credential.revoked` | `internal/project` | operator | ✅ | project_credential | ✅ |
| `user.created` | identity runtime | operator · project | ✅ | user | ✅ |
| `user.updated` | identity runtime | operator · project | ✅ | user | ✅ |
| `user.deleted` | identity runtime | operator · project | ✅ | user | ✅ |
| `user.password_reset` | identity runtime | operator · project | ✅ | user | ✅ |
| `user.roles_granted` | identity runtime | operator · project | ✅ | user | ✅ |
| `user.role_revoked` | identity runtime | operator · project | ✅ | user | ✅ |
| `user.sessions_logged_out` | identity runtime | operator · project | ✅ | user | ✅ |
| `role.created` | identity runtime | operator · project | ✅ | role | ✅ |
| `role.updated` | identity runtime | operator · project | ✅ | role | ✅ |
| `role.deleted` | identity runtime | operator · project | ✅ | role | ✅ |
| `session.revoked` | identity runtime | operator · project | ✅ | session | ✅ |
| `invitation.created` | identity runtime | operator · project | ✅ | invitation | ✅ |
| `invitation.resent` | identity runtime | operator · project | ✅ | invitation | ✅ |
| `invitation.revoked` | identity runtime | operator · project | ✅ | invitation | ✅ |


The invitation endpoints emit a second event (`user.created`) alongside their
primary one, because an invitation IS a user in an invited-but-incomplete state
and the pair is what makes that visible.

**Global events.** `/admin/*` mutations produce events with **no workspace** —
that surface acts on the process-level realm and belongs to no tenant. They are
durable, and they are unreachable through `GET /v1/workspaces/{id}/audit`,
because that query filters on `workspace_id = $1`. There is no global audit API.

## 3. The actor model

Two kinds, disjoint by construction and by a database CHECK:

```text
operator → actor_subject, actor_email        a Keycloak identity
project  → actor_project_id, actor_credential_id
```

**A project id never appears in `actor_subject`.** That column means "a Keycloak
sub", and a `prj_` value there would make a machine indistinguishable from a
person in exactly the records that exist to tell them apart. The constraint
`audit_events_actor_shape_check` refuses the mixed shape, so this is a guarantee
about the data rather than a property of whichever code path wrote it.

`credential_id` is stored because it is what an operator **revokes**: an audit
line names the exact key to pull, not just the project that held it. The
credential's secret and its hash are never stored — knowing which credential
acted needs neither.

## 4. Reading it

```http
GET /v1/workspaces/{workspace_id}/audit
```

**Operator** (realm `admin`), or a **project credential holding `audit:read`**.

`audit:read` is its own scope and is never implied by another. It shows *every*
actor's history in the workspace — including operators and other projects — so
bundling it into `users:read` would hand it to every integration that only
wanted a directory. Nothing selects it by default in the console.

### Filters

| Parameter | Values |
|---|---|
| `event` | exact event type, e.g. `user.created` |
| `actor_type` | `operator` \| `project` |
| `outcome` | `success` \| `failure` |
| `from`, `to` | RFC 3339, inclusive |
| `cursor` | opaque, from the previous page |
| `limit` | 1–200, default 50 |

There is **no `workspace_id` parameter**. The boundary is the path, and offering
a second way to name a workspace would create a second thing to authorize.

An out-of-range `limit` is **refused, not clamped**: answering `limit=100000`
with 200 makes a caller believe it has the whole history when it has one page,
and that belief is the bug — it stops paginating.

### Pagination

Cursor, on `(occurred_at, id)` descending. Offset pagination is not offered: the
table only grows at the head, so an offset shifts under a client between pages.

```jsonc
{ "items": [ … ], "pagination": { "count": 50, "limit": 50, "next_cursor": "MTc…" } }
```

**The absence of `next_cursor` is the end-of-history signal**, so a correct
client loops while it is present and makes exactly as many requests as there are
pages. The composite key matters: twenty mutations in one request burst can
share a microsecond, and a timestamp-only cursor would skip or repeat them at a
page boundary.

## 5. What the API never returns

No password, no credential secret or hash, no connection secret, no provider
token, no request body — **regardless of who is asking**. An operator does not
get more than a credential does: audit is a trail of actions, not a forensic
dump of payloads.

Three fields exist in the table and never in the response:

| Column | Why not |
|---|---|
| `source_ip` | An operator's network location, readable by any credential holding `audit:read`. The actor already answers "who". |
| `workspace_id` | Redundant — it is in the path the caller just used. |
| *(no `user_agent` column at all)* | It is free text the CALLER controls, and one caller must not be able to write arbitrary text into another reader's view. The per-request access log carries it, with the same `request_id`. |

Two fields are filled from runtime values, and both are **allowlisted rather
than sanitised**:

- **`reason_code`** comes from a closed vocabulary of `/v1` error codes. The
  underlying `err.Error()` — which can contain a Keycloak response body, a SQL
  fragment or a customer's email — never reaches the table. Anything
  unrecognised becomes `unclassified_error`, and the real cause is in the log
  line for the same `request_id`.
- **`metadata`** is allowlisted **per event type**. Today exactly one key
  qualifies: the scopes granted on `project_credential.created`, which is the
  single most useful fact for reconstructing what a leaked key could do. Nested
  maps are refused outright — that is how a whole request body gets in behind
  one allowlisted key.

A denylist ("strip anything that looks like a password") fails the first time an
upstream error phrases something nobody anticipated. An allowlist fails closed.

## 6. When the audit write fails

**The answer depends on where the mutation's state lives, and there are exactly
two cases.** Everything else in this section follows from that split.

| | control plane | provider |
|---|---|---|
| Where the state is | this PostgreSQL | a Keycloak realm |
| Examples | workspace, connection, project, credential | users, roles, sessions, invitations |
| Audit row | same transaction as the mutation | written after the fact |
| If the audit write fails | **the mutation is rolled back**, caller gets 500 | the response still succeeds |
| Guarantee | atomic | best-effort, loudly |
| Count | 14 routes | 15 routes |

The classification is not prose. Every mutating `/v1` route carries it in
[`internal/auditlog/coverage.go`](../internal/auditlog/coverage.go), and
`TestCoverage_DurabilityMatchesWhereTheStateLives` fails the build if a route
claims the wrong one — including the dangerous direction, a provider mutation
claiming an atomicity no transaction can deliver.

### 6.1 Control plane — atomic (Slice 15, [TD-033](TECH_DEBT.md#td-033) closed)

> **LIGHTWEIGHT does not commit a PostgreSQL control-plane mutation without its
> required durable audit record.**

The domain rows and the audit row are written in ONE transaction, owned by the
service:

```
BEGIN
    domain mutation          (one row, or two — activation retires and promotes)
    audit insert
COMMIT
```

If the audit insert fails, PostgreSQL rolls back everything and the caller
receives `500 internal_error`. There is no window between the two writes,
because there are not two writes.

**What this means for an operator, stated plainly.** A 500 from a control-plane
mutation means *the state did not change*. That is deliberately the honest
direction and not the convenient one:

- **Revoking a credential** and receiving a 500 means the credential is **still
  live**. Retry it. The alternative — committing the revocation behind an error
  — would tell an operator their kill switch failed while it had in fact fired,
  and acting on a false negative is recoverable where acting on a false positive
  is not.
- **Archiving a workspace or project** and receiving a 500 means it is still
  active.
- **Activating a connection** and receiving a 500 means the previous connection
  is still the active one. Both row updates are undone together.

**Retry safety.** Because nothing committed, retrying a control-plane mutation
after a 500 is safe in the sense that matters here: it cannot produce a
half-applied change. It is not idempotent in general — that is
[TD-036](TECH_DEBT.md#td-036) — so a retried *create* can still produce a second
object if the first one actually committed and only the response was lost.

**No recursion.** When a mutation is rolled back because its audit row would not
write, the system does **not** then try to record a failure event: that would be
a second write to the store that just failed, which is how a transient error
becomes an outage. The failure is logged and counted instead.

### 6.2 Provider — best-effort, loudly

The mutation has already happened in a Keycloak realm and no PostgreSQL
transaction can undo it. Three answers were available:

| | |
|---|---|
| **A. succeed the response, log loudly, count it** | **chosen** |
| B. fail the response | dangerous |
| C. outbox and retry | out of scope |

**B is actively harmful here.** A Keycloak user has been created; telling the
caller it failed invites a retry that either creates a second user or answers
409 for a user the caller believes does not exist. Losing an audit row is bad;
corrupting the caller's model of what exists is worse.

**A is only acceptable because the failure is loud:** an ERROR log carrying the
event type, workspace and `request_id`, and the metric
`lightweight_audit_persist_failures_total{event}`. A silently incomplete trail
invites trust it has not earned.

The residual window — a provider mutation succeeds and its audit row does not —
is [TD-038](TECH_DEBT.md#td-038). It is genuinely narrower than it sounds (both
writes share a database and a connection pool, so the common failure fails the
audit write only after the provider call succeeded) and it is real.

**An outbox was considered and not built.** For the control plane it would be
strictly worse: the state and the audit row share a database, so one transaction
gives a stronger guarantee than eventual consistency. For the provider plane it
would help, and it is a table, a worker, a retry policy and a poison-message
story — worth building once the failure has been observed rather than imagined.

### 6.3 What is NOT claimed

- **Not exactly-once.** The guarantee is *atomic with its PostgreSQL domain
  transaction*, which is narrower and true. A client that retries after an
  ambiguous failure can produce a second mutation with a second audit row, and
  both rows are correct records of what happened.
- **Not immune to commit ambiguity.** A client whose connection drops during
  `COMMIT` cannot know whether PostgreSQL committed. That is a property of the
  protocol, not of this design, and it is distinct from application-level
  atomicity: whatever the outcome, the domain row and the audit row share it.
- **Not atomic across systems.** Nothing here makes a Keycloak write and a
  PostgreSQL write atomic. No 2PC, no XA, no distributed coordinator.

## 7. Retention

`AUDIT_RETENTION_DAYS`, default **90**. Swept 30 seconds after boot and once a
day thereafter, deleting events **strictly older** than the window.

There is **no value meaning "keep forever"**. An audit table that only grows is
a disk-exhaustion outage scheduled for whenever the installation gets busy, and
an operator who wants indefinite history needs an export rather than an
unbounded table. `0` is refused rather than defaulted: someone who typed it
meant "keep nothing", and silently giving them 90 days would be the opposite of
what they asked for.

The startup sweep exists so an installation that has been down for a week does
not carry a week of expired events until the first tick.

## 8. Storage and query plans

One table, two indexes, and the second one is not the obvious shape.

```sql
audit_events_workspace_time_idx  btree (workspace_id, occurred_at, id)
audit_events_occurred_at_brin    brin  (occurred_at)
```

**The composite is ASCENDING although the query orders DESC.** A btree can be
scanned either direction, and the ASC declaration is what lets the cursor
predicate become an index condition — measured: with `DESC` columns PostgreSQL
would not use it, fell back to the time index, and filtered by workspace
afterwards.

**The cursor predicate is split** into `occurred_at <= t` (index condition) and
`occurred_at < t OR id < i` (a filter that can only discard rows sharing the
boundary timestamp). The elegant row-value form `(occurred_at, id) < (t, i)` is
correct and was measured to be slow: PostgreSQL only recognises a row comparison
as an index condition when it starts at the index's *first* column, and here
that column is `workspace_id`, pinned by equality.

**Retention uses BRIN, not btree**, and that is the interesting one. A plain
btree on `occurred_at` serves the sweep and *steals the listing query*: the
planner preferred it, scanned it backwards, filtered other tenants out and added
an Incremental Sort — 969 rows discarded to return 51, degrading linearly with
tenant count. BRIN fits append-only monotonic data, is a handful of pages at any
size, and is lossy with no ordering, so the planner will not choose it for an
ordered `LIMIT`.

Measured on 10,000 events across 20 workspaces:

```text
Limit  (cost=0.29..113.84 rows=51) (actual time=0.008..0.024 rows=51 loops=1)
  Buffers: shared hit=29
  ->  Index Scan Backward using audit_events_workspace_time_idx
        Index Cond: ((workspace_id = $1) AND (occurred_at <= $2))
        Filter: ((occurred_at < $3) OR (id < $4))
Execution Time: 0.043 ms
```

No sort node, 29 buffers, work bounded by the **page** rather than by history or
tenant count. And the sweep, over 40,000 rows:

```text
Delete on audit_events
  ->  Bitmap Heap Scan on audit_events  (actual rows=20000)
        ->  Bitmap Index Scan on audit_events_occurred_at_brin
Execution Time: 5.582 ms
```

Both are asserted by integration tests against a real planner, so neither can
silently regress.

## 9. Backup

The trail is in PostgreSQL, so **the database backup already includes it**. The
recovery unit is unchanged — see
[RUNNING.md §8](operations/RUNNING.md#8-backup-and-recovery): the dump plus
`SECRETS_MASTER_KEY`. Audit rows hold no sealed value, so they restore from the
dump alone.

## 10. Deliberate non-goals

No WORM enforcement, no cryptographic signing, no append-only grant, no separate
database role. Those defend against an attacker who already holds database
credentials — a threat model this product does not otherwise defend against, and
pretending otherwise would be worse than being clear about it.

No streaming, no webhooks, no SIEM export, no full-text search, no per-workspace
retention, no global audit API.
