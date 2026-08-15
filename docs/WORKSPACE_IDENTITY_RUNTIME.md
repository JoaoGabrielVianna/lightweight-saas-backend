# Workspace identity runtime

**Scope:** how an identity request is routed to a Workspace's Keycloak realm at
request time, and why `/admin/*` still is not.

**Status:** shipped 2026-08-09. Package
[`internal/identityruntime`](../internal/identityruntime/).

Slices 2 and 3 made Workspaces and Connections real rows an operator manages.
This is the slice where they start deciding what a request does.

---

## 1. The flow

```
GET /v1/workspaces/ws_3f25…/users
  │
  ▼  publicid.Parse            invalid_workspace_id
workspace                      workspace_not_found · workspace_archived
  │
  ▼  active connection         workspace_connection_missing
connection row                 workspace_connection_unusable
  │
  ▼  OpenSecret + AES-256-GCM  provider_credentials_unavailable
plaintext client secret        (lives inside one function call)
  │
  ▼  identitykc.NewProvider
provider → that realm          provider_unavailable
```

Every step happens **per request**. There is no process-level "current realm",
which is what makes two workspaces on two realms possible in one process.

## 2. What the boundary hides

Three things never cross it, and none of them by convention:

| Hidden | Why it cannot leak |
|---|---|
| The Connection row | Handlers receive an `identity.IdentityProvider`. That interface has no accessor for base URL, realm or client id — there is nothing to call |
| Encryption | `OpenSecret` and `Keeper.Open` are called in exactly one function, `buildProvider`. Nothing above it imports `internal/secrets` |
| Credentials | The plaintext exists for the duration of one call and its buffer is wiped on the way out |

`connection.Connection` has no field for secret material at all, so no listing,
response or log line can carry one by accident. That is structural, not a rule
somebody has to remember in review.

## 3. Isolation

Each Connection gets its **own provider instance**. Every piece of state that
could leak between workspaces is a field on that instance — above all the
service-account token, which lives in an `atomic.Pointer` on `AdminClient`.
There is no package-level mutable state in `internal/identityruntime` or in
`internal/identity/keycloak`.

This is verified rather than asserted. `TestIsolation_*` in the integration
suite runs two live Keycloak realms and proves realm, credential and response
isolation both sequentially and under 80 concurrent requests.

## 4. Caching, and the measurement behind it

The resolver caches providers. The reason is **not** the one you would guess.

| Measured | Result |
|---|---|
| `identitykc.NewProvider` | 101 ns, 5 allocs, **0 HTTP requests** |
| A fresh provider's first admin call | **1 `client_credentials` round trip** |

Construction is free, so caching buys nothing there. What it buys is the
**token**: an uncached resolver builds a provider per request, and a provider
per request means a token grant per request — an extra round trip on the hot
path and a service-account session per request piling up in Keycloak.

So the cache's unit is the thing holding the token, and its key is the exact
configuration that token was minted from:

```
connection id  +  connections.updated_at
```

Never the workspace id alone. Keying on the workspace would survive a credential
rotation and keep presenting the revoked secret — the precise failure a rotation
exists to prevent.

**Invalidation needs no invalidation call.** The resolver reads the connection
row on every request, and that row *is* the signal:

| Event | Effect on the key |
|---|---|
| Secret rotated in place | `updated_at` moves → new key |
| Base URL / realm / client edited | `updated_at` moves → new key |
| Another connection activated | different connection id → new key |
| Connection retired | no active connection → not resolved at all |

The cache is bounded (LRU, 64 entries) and can be switched off entirely; the
resolver behaves identically without it, which is what makes it an optimization
rather than a dependency.

> **A rotation test that only reads is not a rotation test.** Rotating a client
> secret at Keycloak does not invalidate access tokens already issued under it —
> they stay valid until they expire, five minutes by default. A stale provider
> therefore keeps answering correctly for the entire life of a test. This was
> verified: a deliberately broken cache key passed a purely functional check.
> The assertion that discriminates is on provider **identity** — after the row
> changes, the resolver must hand back a different instance.

## 5. API

| Route | Purpose |
|---|---|
| `GET /v1/workspaces/{workspace_id}/users` | Users in that workspace's realm |
| `GET /v1/workspaces/{workspace_id}/roles` | Realm roles in that workspace's realm |
| `POST /v1/workspaces/{workspace_id}/roles` | Create a realm role in that realm |

Mounted only when `SECRETS_MASTER_KEY` is set — without a key there are no
Connections to route through, so the routes are absent (404) rather than
mounted-and-503. They carry the same gate chain as every other `/v1` route:
rate limit → `RequireAuth` → `RequireRole("admin")` → `RequireLiveAdmin`.

Pagination, id validation and role-name rules come from the same
`identity.Service` that backs `/admin/*`. One implementation, one set of tests,
two surfaces.

### Errors

| Code | Status | Meaning |
|---|---|---|
| `invalid_workspace_id` | 400 | Not in the form `ws_<uuid>` |
| `invalid_request` | 400 | Malformed body, or a field the identity service rejected |
| `workspace_not_found` | 404 | No such workspace |
| `resource_not_found` | 404 | The workspace resolved; the user or role is not in that realm |
| `workspace_archived` | 409 | Refused **before** the provider is contacted |
| `workspace_connection_missing` | 409 | Workspace exists, routes nowhere. Activate a connection |
| `workspace_connection_unusable` | 409 | Active connection cannot be turned into a provider, or its service account was refused by Keycloak |
| `resource_conflict` | 409 | The role name is already taken in that realm |
| `provider_credentials_unavailable` | 500 | The sealed credential could not be opened. Operator emergency |
| `internal_error` | 500 | Cause logged with the request id, never returned |
| `provider_unavailable` | 502 | The provider was reached and did not answer usefully |

`workspace_not_found` and `workspace_archived` deliberately reuse the codes
`/v1/workspaces` already publishes. That a workspace does not exist is one fact
with one name, whichever endpoint reported it.

Nothing from upstream reaches the client. Keycloak's error bodies quote realm
names, client ids and internal URLs; the envelope carries the catalogue's own
literal message and a request id pointing at the log line with the real cause.

## 6. Bootstrap: why `/admin/*` was left alone

**Decision: legacy `/admin/*` keeps using `KEYCLOAK_*` environment
configuration, untouched. No bootstrap Workspace or Connection is created.
Migrating `/admin/*` onto the runtime is deferred.**

The two authorities are **disjoint by construction**:

| Surface | Provider coordinates from |
|---|---|
| `/admin/*` | `KEYCLOAK_*` environment variables |
| `/v1/workspaces/{id}/*` | The workspace's active Connection |

`SetupWorkspaceIdentity` does not read `cfg.Keycloak*` at all, and
`SetupIdentity` does not read the connections table. There is no overlap, so
there is no precedence rule to specify — and therefore none to get wrong.

The alternative considered was seeding a Workspace and Connection from the
environment at boot. It was rejected for this slice on three counts:

1. **Dual authority with no defined precedence.** Once the same realm is
   described in two places, an operator editing one and not the other gets a
   system whose behaviour depends on which code path they hit. Defining
   precedence is a real design decision, and it belongs in the slice that
   actually migrates `/admin/*`, where it can be tested end to end.
2. **`SECRETS_MASTER_KEY` is optional today.** Seeding a Connection requires
   sealing a credential, which requires a key. Existing deployments without one
   must not break, so the seed would have to be conditional — leaving exactly
   the two-configuration split it was meant to remove, plus a new failure mode.
3. **Nothing needs it yet.** No existing deployment has a Workspace, so nothing
   is currently broken by its absence. Writing rows at boot to satisfy a
   consumer that will exist in a later slice is speculative work with a real
   blast radius.

Consequences, stated plainly:

- An existing deployment upgrading to this version sees **no change**. Same
  `/admin/*` routes, same responses, same headers.
- A deployment with no `SECRETS_MASTER_KEY` gets no `/v1` connection or
  workspace-identity routes — the same signal it already got in Slice 3.
- Using the workspace-scoped API requires creating a Workspace and activating a
  Connection through `/v1`. That is deliberate: it is the product's model.

`TestSetupWorkspaceIdentity_IgnoresTheProcessKeycloakConfiguration` pins the
disjointness, so a future change that quietly makes env config a fallback for
the runtime fails a test rather than passing review.

## 7. Testing

| Suite | Command | Covers |
|---|---|---|
| Unit | `make test` | Every refusal and its ordering, cache key semantics, LRU bound, error mapping, the full HTTP surface, and the construction/token measurements the cache design rests on |
| Integration (PostgreSQL + live Keycloak) | `DB_URL=… KEYCLOAK_VERIFY_URL=… make test-integration` | Two live realms: realm isolation, concurrent isolation, mutation isolation, connection rotation, in-place secret rotation, archived workspace, missing connection, retired connection, and secret isolation |

The live-Keycloak suite skips cleanly without `KEYCLOAK_VERIFY_URL`, so a
laptop with only a database still runs everything else. CI provides both.

```bash
docker run -d --name kc -e KC_BOOTSTRAP_ADMIN_USERNAME=admin \
  -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin -p 58080:8080 \
  quay.io/keycloak/keycloak:26.0 start-dev

KEYCLOAK_VERIFY_URL=http://localhost:58080 \
  DB_URL=... go test -tags=integration ./internal/identityruntime/
```

---

## See also

- [CONNECTIONS.md](CONNECTIONS.md) — the Connection this resolves to
- [WORKSPACES.md](WORKSPACES.md) — the routing boundary itself
- [ARCHITECTURE.md](ARCHITECTURE.md) — where this sits in the stack
- [QUALITY_GATE.md](QUALITY_GATE.md) — the two coverage measurements
