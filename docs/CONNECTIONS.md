# Connections

**Scope:** the Connection domain — a Workspace's configured access to an
identity provider — and its `/v1` API.

**Status:** shipped 2026-07-28. Migration `000003_connections`, packages
[`internal/connection`](../internal/connection/) and
[`internal/secrets`](../internal/secrets/).

> **A Connection now routes traffic.** `GET /v1/workspaces/{id}/users` and the
> other workspace-scoped identity routes resolve a Workspace's active Connection
> per request and talk to that realm — see
> [WORKSPACE_IDENTITY_RUNTIME.md](WORKSPACE_IDENTITY_RUNTIME.md).
>
> Legacy `/admin/*` deliberately still uses the process-level `KEYCLOAK_*`
> configuration and is unchanged. The two sources are disjoint: neither reads
> the other. Migrating `/admin/*` onto the runtime is a later slice, and the
> reasoning for deferring it is in
> [§6 of that document](WORKSPACE_IDENTITY_RUNTIME.md#6-bootstrap-why-admin-was-left-alone).

---

## 1. Enabling it

The connection API is mounted **only** when `SECRETS_MASTER_KEY` is set:

```bash
openssl rand -base64 32     # → put in SECRETS_MASTER_KEY
```

Without a key the routes do not exist (404, as with `/admin/*` without admin
credentials). That is deliberate rather than degraded: a Connection exists to
hold a provider credential, and there is no acceptable way to store one
unsealed. A key that IS set but unusable — wrong length, not base64 — **fails
the boot**, because silently running without the API would be discovered much
later by someone wondering where the routes went.

> **Losing the master key makes every stored client secret unreadable.** They
> cannot be recovered, only re-entered. Back it up with the same care as a
> database credential, and note that it is not in the database — a `pg_dump`
> alone does not restore a working installation.

---

## 2. Lifecycle

```
   create
     │
     ▼
 ┌────────┐   verify ✓   ┌────────┐   activate    ┌────────┐
 │ draft  │ ───────────▶ │ draft  │ ────────────▶ │ active │
 │        │  (≤ 1 hour)  │verified│               │        │
 └────────┘              └────────┘               └────────┘
     │                                                 │
     │ retire                        retire ───────────┤
     ▼                                                 ▼
                        ┌──────────┐  ◀────────────────┘
                        │ retired  │   (also: automatically, when another
                        └──────────┘    connection in the workspace activates)
                             │
                             ▼  delete ✓          delete on active ✗
```

| Rule | |
|---|---|
| Creation | always `draft` |
| Activation | requires a verification that **passed within the last hour** |
| Activation | retires the workspace's previous active connection, in one transaction |
| One active per workspace | enforced by a **partial unique index**, not by application code |
| `retired` | terminal — no reactivation. Create a new connection |
| `DELETE` | `draft` and `retired` only. An active connection must be retired first |
| `PATCH` | `draft` only, and it resets the verification (see §4) |
| Archived workspace | blocks create and activate; reads still work |

**Activation is deliberately not idempotent.** Unlike archiving a workspace, a
repeat call returns `connection_already_active` rather than succeeding: a caller
retrying may believe they are switching away from a *different* connection, and
silently confirming that would be worse than an error.

### Replacing an active connection, and rotating the provider secret

`PATCH` edits a **draft** only, and resets its verification when it applies. So
changing anything about a live connection, including its client secret, is a
replacement rather than an edit:

```
1. Create a NEW connection with the new coordinates.   (the old one keeps serving)
2. Verify it.                                          (the old one keeps serving)
3. Activate it.       ← the switch. The old one is retired in the same transaction.
4. Delete the retired one, once you are satisfied.
```

There is no window with two active connections, and none with zero. The
provider cache is keyed on the connection's id **and** its `updated_at`, so the
next request after step 3 resolves the new one; a rotated credential can never
be served from a provider built against the old one.

For a Keycloak client-secret rotation specifically, the gap between
regenerating in Keycloak (which invalidates the old secret immediately) and
step 3 is a window in which that workspace's identity operations fail. Keep it
short, or use a second Keycloak client so step 1 can change the client id too.
Step-by-step:
[KEYCLOAK_EXISTING.md §10](getting-started/KEYCLOAK_EXISTING.md#10-rotating-the-keycloak-client-secret).

### TLS

Outbound HTTPS to a provider uses Go's default transport and the container's
system trust store. A publicly-trusted certificate works with no
configuration; a **private or self-signed CA does not**, and there is no
configuration surface for one. Verify reports it as `provider unreachable`,
with an `x509` line in the API log. The supported workaround, and the reasoning,
are in
[KEYCLOAK_EXISTING.md § TLS and certificates](getting-started/KEYCLOAK_EXISTING.md#tls-and-certificates).

---

## 3. Secrets at rest

The provider's client secret is sealed with **AES-256-GCM** before it reaches
the database ([`internal/secrets`](../internal/secrets/aesgcm.go)). Only the
ciphertext, the nonce, the key version and the algorithm are stored.

| Choice | Why |
|---|---|
| Authenticated encryption (GCM) | An altered ciphertext fails to open rather than decrypting to garbage that would then be sent to a provider as a credential |
| Fresh random 96-bit nonce per seal | GCM's security collapses under nonce reuse. The same secret never produces the same ciphertext twice |
| Additional authenticated data (AAD) | `connection:<id>:client_secret` binds a ciphertext to its row. Without it, someone with database write access could move connection A's secret onto B and make B authenticate as A |
| Key version stored per row | Each row records which master key opens it, and is opened with that key and **no other** — there is no try-every-key fallback. This is what makes rotation possible: see [SECRET_KEY_ROTATION.md](SECRET_KEY_ROTATION.md) |

**Threat model, stated plainly.** This protects a credential at rest: a stolen
dump, a leaked backup, a readable replica. It does **not** protect against an
attacker who can read the master key, because the running process must be able
to open what it sealed. Moving the key to a KMS is the next step and the stored
format already accommodates it.

**Rotating the master key.** `SECRETS_KEYRING` holds every version the process
can decrypt with; `SECRETS_KEY_CURRENT` names the one that encrypts. Adding a
version, running `make secrets-rotate`, and removing the old version retires a
key with no downtime and without re-entering a single credential. The full
procedure — including what a missing key does and does not break, and why a
`pg_dump` alone is not a backup — is in
[SECRET_KEY_ROTATION.md](SECRET_KEY_ROTATION.md).

**The API never returns the secret.** Not the plaintext, not the ciphertext, not
the nonce. The guarantee is structural rather than a matter of remembering: the
`Connection` domain type has no field for secret material at all, so no change
to the response conversion can expose it. The sealed value is loaded only by an
explicit `OpenSecret` call, by the one code path that uses it. Responses carry
`has_client_secret: true` and nothing else.

---

## 4. Verify

```
POST /v1/workspaces/{workspace_id}/connections/{connection_id}/verify
```

Six checks, in order:

| Check | Probe |
|---|---|
| `reachable` | the realm's OIDC discovery document responds |
| `realm_exists` | …and is not a 404 |
| `client_authenticated` | `client_credentials` grant succeeds |
| `realm_readable` | `GET /admin/realms/{realm}` |
| `users_listable` | `GET /admin/realms/{realm}/users?max=1` |
| `write_capable` | the granted `realm-management` roles inside the token just obtained |

**The probe is strictly read-only.** It creates no test user, no throwaway role,
and modifies nothing, so an operator can press Verify against production without
wondering what it left behind. Asserted against real Keycloak by
`TestLiveVerify_IsReadOnly`, which compares the realm's user count before and
after.

The sixth check keeps that promise while still answering "may this account
mutate?". Keycloak stamps a service account's `realm-management` roles into the
access token the third check already obtained, so the provider has **already
said** what it will allow — no extra request, nothing written. Its token
signature is not verified, because this is not authentication: the probe is
reading its own credential's grant sheet, minted seconds earlier by the provider
it just authenticated to.

### health vs access_mode

The first three checks decide `health`. The remaining three decide
`access_mode`:

| Outcome | health | access_mode | writes |
|---|---|---|:--:|
| Reads pass, `realm-admin` or `manage-users` granted | `healthy` | `full` | yes |
| Reads pass, no write grant in the token | `healthy` | `read_only` | **no** |
| Reads pass, token does not publish its grants | `healthy` | `unknown` | attempted |
| Authenticates, cannot read | `healthy` | `limited` | **no** |
| Cannot authenticate / reach / find realm | `unhealthy` | `unknown` | n/a |

A service account that authenticates but cannot list users is **correctly
configured and under-privileged** — it needs `realm-management` roles, which is
a different fix from a wrong URL. Collapsing both into "unhealthy" would tell an
operator to look in the wrong place.

`read_only` and `limited` are also different fixes, which is why they are
different values: the first needs `manage-users` granted, the second needs the
read roles it never had. Both refuse writes, and the response carries the
verdict as a precomputed `can_write` so a client does not re-derive the rule —
notably, that `unknown` permits the attempt while `read_only` does not.

> **`full` is claimed only when write capability was PROVEN.** Before
> [TD-024](TECH_DEBT.md#td-024) was resolved, `full` meant "both reads
> succeeded", so a `view-users`-only account was labelled write-capable. The
> invariant now: this API never reports write capability it has only inferred
> from reads. `manage-realm` alone is not accepted — it permits realm-role
> writes while leaving every user mutation refused.

`verify` returns **200 even when the probe fails**: the verification *ran*, and
its verdict is in the body. A 4xx would claim this API malfunctioned, which is
not what "your provider refused our credentials" means.

### Expiry

A successful verification authorizes activation for **one hour**
(`connection.VerifyValidity`). Long enough to read the report and decide; short
enough that activating on last week's verdict is impossible. "This provider
answered correctly" is a perishable fact — credentials rotate, realms get
deleted — and activation is where acting on a stale one costs most.

Changing `base_url`, `realm`, `client_id` or `client_secret` **resets the
verification**, because the stored verdict referred to coordinates that no
longer apply.

---

## 5. API

All routes carry the same chain as `/admin/*`:
`RateLimitPerIP → RequireAuth → RequireRole("admin") → RequireLiveAdmin`.

| Method | Path |
|---|---|
| `GET` | `/v1/workspaces/{workspace_id}/connections` — `?status=draft\|active\|retired\|all`, default **all** |
| `POST` | `/v1/workspaces/{workspace_id}/connections` |
| `GET` | `/v1/workspaces/{workspace_id}/connections/{connection_id}` |
| `PATCH` | `…/{connection_id}` — draft only |
| `DELETE` | `…/{connection_id}` — draft or retired only |
| `POST` | `…/{connection_id}/verify` |
| `POST` | `…/{connection_id}/activate` |
| `POST` | `…/{connection_id}/retire` |

The default listing filter is `all`, unlike workspaces where it is `active`: the
whole operator workflow is draft → verify → activate → the previous one retires,
and hiding two thirds of that would make the endpoint unusable.

Connections are **nested under their workspace** and every handler confirms the
connection actually belongs to it. A connection addressed under the wrong
workspace is `connection_not_found`, not a distinct error — from that caller's
position it does not exist, and saying more would confirm it exists elsewhere.

Public ids are `conn_<uuid>`; a bare UUID is accepted on input as a development
convenience. Full schemas in [`swagger.yaml`](swagger.yaml), tag `connections`.

### Error codes

| Code | Status | Meaning |
|---|---|---|
| `connection_not_found` | 404 | No such connection under that workspace |
| `invalid_connection_id` | 400 | Not `conn_<uuid>` or a bare UUID |
| `connection_not_verified` | 409 | Activation without a passing verification |
| `connection_verification_expired` | 409 | Verification older than an hour |
| `connection_already_active` | 409 | Repeat activation |
| `workspace_has_active_connection` | 409 | Lost an activation race; retry |
| `connection_retired` | 409 | Terminal state |
| `connection_active_cannot_delete` | 409 | Retire first |
| `connection_not_draft` | 409 | Configuration is editable only while draft |
| `connection_name_required`, `connection_base_url_invalid`, `connection_realm_required`, `connection_client_id_required`, `connection_client_secret_required`, `connection_provider_unsupported` | 400 | Field validation |
| `invalid_status_filter`, `invalid_workspace_id`, `invalid_request` | 400 | |
| `workspace_not_found` / `workspace_archived` | 404 / 409 | Same codes the workspace API uses |
| `internal_error` | 500 | Cause logged with the request id, never returned |

---

## 6. Invariants, and where each lives

| Invariant | Enforced by |
|---|---|
| **At most one active connection per workspace** | `idx_connections_one_active_per_workspace` — partial unique index. The authority under concurrency, which no service check can be |
| `provider`, `status`, `health`, `access_mode` are closed sets | CHECK constraints |
| `retired_at` set **iff** retired; `activated_at` absent on drafts | CHECK constraints |
| `health` and `last_verified_at` move together | CHECK constraint — a verdict with no timestamp cannot be aged out |
| A secret is always present and non-empty | CHECK constraint |
| Deleting a workspace with connections | Foreign key `ON DELETE RESTRICT` — a cascade would silently destroy credentials |

---

## 7. Testing

| Suite | Command | Covers |
|---|---|---|
| Unit | `make test` | AES seal/open, wrong key, wrong AAD, tampered ciphertext, nonce freshness; every state transition; slug of validation rules; the full HTTP surface; the probe's classification of each provider status |
| Integration (PostgreSQL) | `make test-integration` | The migration's real schema, every CHECK driven by raw SQL, the partial index under 6 concurrent activations, `000003` down preserving `000002`+`000001`, and `ON DELETE RESTRICT` |
| Integration (live Keycloak) | `KEYCLOAK_VERIFY_URL=… make test-integration` | Verification against real Keycloak 26: full access, limited access, a genuinely read-only admin client, wrong secret, unknown client, unknown realm, probe read-only-ness, and that the recorded `access_mode` predicts a real write's outcome |

The live-Keycloak suite is gated on `KEYCLOAK_VERIFY_URL` and **skips** without
it, so a machine with only a database still runs everything else. CI's
integration job starts both PostgreSQL and Keycloak, so it does run there. Run
it locally:

```bash
docker run -d --name kc -e KC_BOOTSTRAP_ADMIN_USERNAME=admin \
  -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin -p 58080:8080 \
  quay.io/keycloak/keycloak:26.0 start-dev

KEYCLOAK_VERIFY_URL=http://localhost:58080 \
  DB_URL=... go test -tags=integration ./internal/connection/
```

---

## See also

- [WORKSPACES.md](WORKSPACES.md) — what a Connection belongs to
- [MIGRATIONS.md](MIGRATIONS.md) — how `000003` is applied and rolled back
- [WORKSPACE_IDENTITY_RUNTIME.md](WORKSPACE_IDENTITY_RUNTIME.md) — what consumes a Connection at request time
- [ARCHITECTURE.md](ARCHITECTURE.md) — where this sits in the stack
