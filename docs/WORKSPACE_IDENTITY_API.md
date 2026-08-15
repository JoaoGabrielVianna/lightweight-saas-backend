# Workspace Identity API

**Scope:** the complete `/v1/workspaces/{workspace_id}/...` identity surface,
its error contract, and exactly what remains on legacy `/admin/*`.

**Status:** shipped 2026-08-09. Package
[`internal/identityruntime`](../internal/identityruntime/).

The runtime boundary itself — how a workspace resolves to a Keycloak realm per
request — is [WORKSPACE_IDENTITY_RUNTIME.md](WORKSPACE_IDENTITY_RUNTIME.md).
This document is the API built on it.

> **`/v1` is the product API. `/admin/*` is legacy compatibility.** New work
> targets `/v1`. No identity capability may be added only to `/admin/*`. The
> console moved onto this surface in Slice 6 — see
> [§8](#8-what-slice-6-must-do).

---

## 1. The surface

24 operations, all routed through the workspace's active Connection.

### Users

| Method | Path |
|---|---|
| `GET` | `/v1/workspaces/{workspace_id}/users` |
| `POST` | `/v1/workspaces/{workspace_id}/users` |
| `GET` | `/v1/workspaces/{workspace_id}/users/{user_id}` |
| `PATCH` | `/v1/workspaces/{workspace_id}/users/{user_id}` |
| `DELETE` | `/v1/workspaces/{workspace_id}/users/{user_id}` |

`POST /users` provisions with a **temporary password** the user must change on
first login. It is the existing direct-provisioning flow, promoted onto the
shared service — not a new one. The alternative is an invitation, which sends
email. Neither is "the" create: they have different prerequisites.

### User roles

| Method | Path |
|---|---|
| `GET` | `.../users/{user_id}/roles` |
| `POST` | `.../users/{user_id}/roles` |
| `DELETE` | `.../users/{user_id}/roles/{role_name}` |

### Passwords

| Method | Path | Needs SMTP |
|---|---|:--:|
| `POST` | `.../users/{user_id}/reset-password` | **yes** |
| `PUT` | `.../users/{user_id}/password` | no |

Two routes, not one with a flag. The first dispatches Keycloak's action email
and fails on a realm without SMTP; the second sets a credential directly and
fails on nothing. Collapsing them would make a `provider_unavailable` mean two
unrelated things.

### Sessions

| Method | Path |
|---|---|
| `GET` | `.../users/{user_id}/sessions` |
| `DELETE` | `.../users/{user_id}/sessions` |
| `GET` | `.../sessions` |
| `DELETE` | `.../sessions/{session_id}` |

### Roles

| Method | Path |
|---|---|
| `GET` · `POST` | `.../roles` |
| `GET` · `PATCH` · `DELETE` | `.../roles/{role_name}` |
| `GET` | `.../roles/{role_name}/users` |

`PATCH` changes `description` only. Renaming would require rewriting every
role-mapping that references the old name.

### Invitations

| Method | Path |
|---|---|
| `GET` · `POST` | `.../invitations` |
| `DELETE` | `.../invitations/{invitation_id}` |
| `POST` | `.../invitations/{invitation_id}/resend` |

> **An invitation is not a Keycloak resource.** It is a derived view over users
> in an invited-but-incomplete state. The `{invitation_id}` in these paths **is
> a user id**, and revoking an invitation deletes the user. Two consequences:
>
> - a user who completes their required actions stops appearing in the listing;
>   nothing was deleted.
> - `DELETE .../invitations/{id}` skips the last-admin guard (an invited user
>   cannot hold admin yet). For a user who has accepted, use
>   `DELETE .../users/{id}`, which is guarded.
>
> This model is preserved from `/admin/*` verbatim. Redesigning it into a
> first-class resource is a product decision, not a migration.

## 2. Pagination

`GET .../users` returns the **effective** `first` and `max` — what the server
actually used after clamping `max` to [1, 100] and `first` to ≥ 0.

This differs from legacy `GET /admin/users`, which echoes the caller's raw
input ([TD-020](TECH_DEBT.md#td-020)). A client paginating on the echoed `max`
there computes wrong offsets. The bug is fixed on `/v1` and deliberately
preserved on `/admin` until the frontend moves off it; both behaviours are
pinned by tests so the difference stays a decision.

`GET .../roles/{role_name}/users` returns the **complete** membership, not a
page, and reports `count` only.

## 3. Errors

Every failure uses the `/v1` envelope:

```json
{"error": {"code": "user_not_found", "message": "...", "request_id": "..."}}
```

The **code** is the contract. Statuses collide and messages get reworded.

| Code | Status | Meaning |
|---|:--:|---|
| `invalid_workspace_id` | 400 | Not in the form `ws_<uuid>` |
| `invalid_user_id` | 400 | Path user id is not a UUID |
| `invalid_role_name` | 400 | Path role name is outside the permitted charset |
| `invalid_session_id` | 400 | Path session id is not a UUID |
| `invalid_request` | 400 | Malformed body, or a field the service rejected |
| `workspace_not_found` | 404 | No such workspace |
| `user_not_found` | 404 | Not in this workspace's realm |
| `role_not_found` | 404 | Not in this workspace's realm |
| `session_not_found` | 404 | Not in this workspace's realm |
| `invitation_not_found` | 404 | Not in this workspace's realm |
| `caller_forbidden` | **403** | A product rule refused **you** |
| `workspace_archived` | 409 | Refused before the provider is contacted |
| `workspace_connection_missing` | 409 | Workspace routes nowhere |
| `workspace_connection_unusable` | 409 | Active connection cannot become a provider |
| `connection_read_only` | 409 | Connection lacks write access |
| `provider_forbidden` | **409** | Keycloak refused the **workspace's service account** |
| `role_already_exists` | 409 | Name taken in that realm |
| `role_reserved` | 409 | Platform- or Keycloak-managed name |
| `conflict` | 409 | Other state collision |
| `provider_credentials_unavailable` | 500 | Sealed credential could not be opened |
| `internal_error` | 500 | Cause logged with the request id |
| `provider_unavailable` | 502 | Provider reached, no useful answer |

### `caller_forbidden` vs `provider_forbidden`

The distinction is the point, and it decides which system an operator opens.

- **`caller_forbidden` (403)** — a product rule refused you: self-delete,
  self-disable, removing your own admin role, removing the realm's last enabled
  admin. Fix: ask someone else, or do something else.
- **`provider_forbidden` (409)** — Keycloak returned 403 to the *workspace's
  service account*. Fix: grant that account its realm-management roles and
  re-verify the connection.

409 rather than 403 for the second is deliberate. Every `/v1` route already
answers 403 from its own auth chain for "your token is insufficient". Reusing
that status for a provider-side problem would send operators to check their own
token for something that lives entirely in the connection's configuration.

**Limitation.** This distinction is only as sharp as the provider's. A Keycloak
403 is unambiguous. An endpoint that answers 404 for an unauthorized read is
indistinguishable from a genuinely absent resource, and will surface as
`*_not_found`. Sharpening that would mean probing permissions on every 404,
which costs a round trip per miss.

### Status deviation from the Slice 5 brief

The brief specifies **503** for provider-unavailable. This ships **502**, as it
did in Slice 4. LIGHTWEIGHT is up and serving; the *upstream* identity provider
is what failed, which is what 502 means. 503 would tell a client to retry
against this API as though it were overloaded. Changing it would also break the
contract Slice 4 published and tested. Flagged rather than silently applied.

## 4. Read-only connections

`connection_read_only` (409) refuses a mutation when the workspace's active
connection has `access_mode = limited`.

Enforced in **one place** — `Handler.write` — which every mutating route calls.
`TestHandler_EveryMutationGoesThroughTheWriteGuard` walks all 24 routes and
fails if a mutation reaches the provider through a limited connection, so this
cannot be defeated by forgetting.

> **Updated by Slice 6 — `access_mode` is now a capability model.**
> [TD-024](TECH_DEBT.md#td-024) is resolved. The set is
> `unknown | full | read_only | limited`, and **`full` is claimed only when
> write capability has been positively proven** from the grants Keycloak stamps
> into the service account's own access token.
>
> - `limited` — the verification probe's admin **reads** were refused. Such a
>   connection is under-privileged and may not be able to read either.
> - `read_only` — the reads succeeded and the provider reported no write grant.
>   This is the configuration the pre-Slice-6 three-value model labelled `full`.
> - `unknown` — the provider published no usable evidence either way. Writes are
>   still attempted; the authoritative answer arrives as `provider_forbidden`.
>
> `connection_read_only` now refuses `read_only` as well as `limited`, and
> `ConnectionResponse` carries a precomputed `can_write` so a client gates its
> mutation controls on one verdict rather than re-deriving the rule.
> See [CONNECTIONS.md §4](CONNECTIONS.md#health-vs-access_mode).

## 5. Self-protection across a workspace boundary

The service's self-protection guards — no self-delete, no self-disable, no
self-strip-admin — compare the caller's `sub` against the target's id.

The caller authenticated against the **installation's** realm; the target lives
in the **workspace's** realm. Where the same Keycloak backs both, the ids
coincide and the guards fire normally. Where they differ, the guards never
match and simply do not fire.

That is not a hole: the **last-admin guard is realm-local** and still protects
the target realm, so a workspace cannot be left admin-less. What is genuinely
absent is "you cannot delete yourself" when your account exists in a different
realm from the one you are administering — which is the normal case for a
control plane, and arguably correct.

## 6. Shared logic

`/v1` and `/admin/*` are two transports over **one** identity service. Neither
forks validation, reserved-role rules, self-protection guards, the last-admin
guard, pagination bounds, invitation compensation, or Keycloak error
translation. A bug fixed in the service is fixed for both.

One was, during this slice: the last-admin guard treated Keycloak's 404 for a
missing `admin` role as an enumeration failure, so **user deletion was
impossible in any realm without a role literally named `admin`**. Invisible on
`/admin/*`, whose realm is bootstrapped with one; reachable the moment a
workspace could point at a realm this product did not create. Found by the live
multi-realm suite, not by review.

## 7. Route parity with `/admin/*`

| Capability | `/admin/*` | `/v1/workspaces/*` | Status |
|---|:--:|:--:|---|
| List / get users | yes | yes | **parity** |
| Create user (temp password) | yes | yes | **parity** |
| Update / delete user | yes | yes | **parity** |
| User roles: list / grant / revoke | yes | yes | **parity** |
| Reset-password email | yes | yes | **parity** |
| Set password directly | yes | yes | **parity** |
| User sessions: list / revoke all | yes | yes | **parity** |
| Realm sessions: list / revoke one | yes | yes | **parity** |
| Roles: list / create / get / update / delete | yes | yes | **parity** |
| Role membership | yes | yes | **parity** |
| Invitations: list / create / revoke / resend | yes | yes | **parity** |
| `POST /admin/users/invite` | yes | **no** | **not reproduced** — legacy alias of `POST /invitations` |
| Audit events | yes | **no** | **control-plane** — installation-wide, not workspace-scoped |
| SMTP settings | yes | **no** | **deferred** |
| Email templates | yes | **no** | **deferred** |

### Why SMTP and email templates are deferred

Not because they are hard. Because they are a different kind of thing:
provider-specific **realm settings**, not identity administration. Their current
implementations call the concrete Keycloak provider directly, bypassing both
`IdentityProvider` and `identity.Service`, and they have no test coverage.
Migrating them means designing that seam — which is a slice, not a step.

Nothing in workspace identity administration depends on them.

## 8. What Slice 6 must do

**Done, 2026-08-09.** The console's six identity views consume this surface, the
workspace lives in the route, and TD-024 is resolved so `access_mode` can be
trusted to gate mutation controls. What shipped and why is
[WORKSPACE_CONSOLE.md](WORKSPACE_CONSOLE.md); the pre-slice assessment that
scoped it is [FRONTEND_READINESS.md](FRONTEND_READINESS.md).

SMTP and email templates remain on `/admin/*` as planned ([§7](#7-route-parity-with-admin)).

---

## See also

- [WORKSPACE_IDENTITY_RUNTIME.md](WORKSPACE_IDENTITY_RUNTIME.md) — how a request reaches a realm
- [WORKSPACE_CONSOLE.md](WORKSPACE_CONSOLE.md) — the console built on this surface
- [CONNECTIONS.md](CONNECTIONS.md) — what a workspace routes through
- [FRONTEND_READINESS.md](FRONTEND_READINESS.md) — the pre-migration assessment
- [TECH_DEBT.md](TECH_DEBT.md) — TD-020, TD-022, TD-023, TD-024
