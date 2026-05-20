# Release v0.2.0 — Identity Management

**Date:** 2026-05-20
**Codename:** `identity-management`
**Predecessor:** [v0.1.0-auth-foundation](https://github.com/joaogabrielvianna/lightweight-saas-backend/releases/tag/v0.1.0-auth-foundation)
**Tracking changelog entry:** [CHANGELOG.md §0.2.0](../CHANGELOG.md#020--2026-05-20)

---

## 1. Summary

v0.1 made the API able to **trust** Keycloak-issued tokens. v0.2 makes it
able to **administer** the Keycloak realm those tokens come from — list and
edit users, manage realm roles, revoke sessions, and invite new users —
through a first-class HTTP surface under `/admin/*`, guarded by RBAC.

The Admin API is a deliberate, narrow wrapper over the Keycloak Admin
REST API. It exists so that downstream applications and a future
console UI don't need to learn Keycloak's URL layout, attribute model,
or `RequiredActions` enum to perform everyday operator tasks.

---

## 2. What's new

### 2.1 Admin HTTP surface

All routes are mounted under `/admin/*`, require a valid Keycloak Bearer
token, and require the realm role `admin`. Group-level enforcement —
`RequireAuth + RequireRole("admin")` — means "forgot a role check on a
handler" is structurally impossible inside the group.

| Area         | Method · Path                                       | Purpose                                                                 |
|--------------|-----------------------------------------------------|-------------------------------------------------------------------------|
| Users        | `GET    /admin/users`                               | List realm users.                                                       |
|              | `GET    /admin/users/{id}`                          | Get a single user by Keycloak id.                                       |
|              | `PATCH  /admin/users/{id}`                          | Update mutable user fields.                                             |
|              | `DELETE /admin/users/{id}`                          | Delete a user from the realm.                                           |
|              | `POST   /admin/users/{id}/reset-password`           | Dispatch the password-reset action email.                               |
|              | `GET    /admin/users/{id}/roles`                    | List the user's realm roles.                                            |
|              | `POST   /admin/users/{id}/roles`                    | Assign one or more realm roles.                                         |
|              | `DELETE /admin/users/{id}/roles/{name}`             | Remove a realm role from the user.                                      |
|              | `GET    /admin/users/{id}/sessions`                 | List the user's active sessions.                                        |
|              | `DELETE /admin/users/{id}/sessions`                 | Revoke every active session for the user.                               |
| Invitations  | `GET    /admin/invitations`                         | List pending invitations (synthesized from Keycloak state).             |
|              | `POST   /admin/invitations`                         | Create an invited-but-incomplete user; dispatches the action email.     |
|              | `DELETE /admin/invitations/{id}`                    | Revoke a pending invitation.                                            |
|              | `POST   /admin/invitations/{id}/resend`             | Re-send the invitation email.                                           |
| Roles        | `GET    /admin/roles`                               | List realm roles.                                                       |
|              | `POST   /admin/roles`                               | Create a realm role.                                                    |
|              | `GET    /admin/roles/{name}`                        | Fetch one realm role.                                                   |
|              | `PATCH  /admin/roles/{name}`                        | Update a role's description.                                            |
|              | `DELETE /admin/roles/{name}`                        | Delete a realm role.                                                    |
|              | `GET    /admin/roles/{name}/users`                  | List users carrying the role.                                           |
| Sessions     | `GET    /admin/sessions`                            | List active sessions realm-wide.                                        |
|              | `DELETE /admin/sessions/{id}`                       | Revoke a single session by id.                                          |

Full request/response schemas are in the regenerated Swagger spec at
`/swagger/index.html` (or [docs/swagger.yaml](swagger.yaml)).

### 2.2 RBAC primitives

`internal/auth` adds two new middleware helpers:

- `auth.RequireRole(role string) gin.HandlerFunc` — gates a group on a
  single realm role.
- `auth.RequireAnyRole(roles ...string) gin.HandlerFunc` — gates on
  *any* of the listed roles (disjunction).

Both depend on `RequireAuth` having already populated `Identity` in
`gin.Context`. Denials emit a structured `AuthEvent{Kind: EventForbidden}`
so RBAC failures surface in the same observability hook as authentication
failures.

### 2.3 Admin console (preview)

A minimal static admin UI ships under `web/admin/` (`index.html` + a
small `static/` tree). It's a thin, dependency-free client over the
Admin API and is intended for local development and break-glass ops.
It is **not** a production console.

### 2.4 Feature flag

`config/project.json` gains a new boolean under `features`:

```json
"features": {
  "identity_management": true
}
```

The server only mounts the `/admin/*` group when this flag is true.
Disable it in environments where you intentionally don't want the
admin surface live.

### 2.5 New environment variables

A dedicated Keycloak service-account client is used for Admin API calls
so that user-token scopes never bleed into administrative operations:

| Variable                        | Purpose                                                                 |
|---------------------------------|-------------------------------------------------------------------------|
| `KEYCLOAK_ADMIN_CLIENT_ID`      | Client id for the admin-API service account.                            |
| `KEYCLOAK_ADMIN_CLIENT_SECRET`  | Secret for the admin-API service account (lives in `.env`, gitignored). |
| `KEYCLOAK_ADMIN_BASE_URL`       | In-network Keycloak URL used by the admin client (defaults to `http://keycloak:8080` under `docker-compose`). Intentionally distinct from `KEYCLOAK_URL`, which still drives `iss` matching on incoming tokens. |

`make regen` writes the new client into `deploy/keycloak/realm-export.json`
and seeds the new keys into `.env` / `.env.example`. No hand-editing
required.

---

## 3. Breaking changes

**None.** v0.2 is purely additive on top of v0.1:

- `/health`, `/me`, `/swagger/*`, request shapes, and JWT handling are unchanged.
- No DB migrations; the `users` table schema is the v0.1 schema.
- API `info.version` in the Swagger spec stays at `1.0`.

If you upgrade *without* enabling `features.identity_management`, the
runtime surface is byte-for-byte identical to v0.1 plus the
`KEYCLOAK_ADMIN_*` env vars being read once at startup (and tolerated
when empty).

---

## 4. Upgrade guide

From a v0.1 checkout on the same machine:

```bash
git fetch --tags
git checkout v0.2.0
make regen           # writes new env keys + admin client into realm-export
make reset-dev       # rebuilds the api image, re-imports the realm
make auth-test       # sanity: token → /me  (should still be 200)
```

Then promote a user to `admin` in Keycloak (or use the seeded
`adminuser`) and exercise the new surface:

```bash
TOKEN=$(curl -fsS -X POST http://localhost:8081/realms/saas/protocol/openid-connect/token \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d 'client_id=saas-backend' -d "client_secret=$KEYCLOAK_CLIENT_SECRET" \
  -d 'grant_type=password' -d 'username=adminuser' -d "password=$SEED_USER_PASSWORD" \
  | jq -r .access_token)

curl -fsS http://localhost:8080/admin/users -H "Authorization: Bearer $TOKEN" | jq
```

A non-admin token against the same endpoint returns `403 forbidden`
and emits an `EventForbidden` `AuthEvent`.

### Disabling the admin surface

Set `features.identity_management` to `false` in `config/project.json`,
run `make regen`, restart the API. The `/admin/*` group will not be
mounted; routes return `404`.

---

## 5. Operational notes

- **Why a second Keycloak client?** The Admin API talks to Keycloak's
  privileged endpoints. Using a dedicated service-account client
  isolates that authority from end-user tokens and lets the admin
  client be rotated independently.
- **Why a separate `KEYCLOAK_ADMIN_BASE_URL`?** Inside the compose
  network the admin client reaches Keycloak at `http://keycloak:8080`,
  but tokens issued to user agents carry `iss=http://localhost:8081/realms/saas`
  (the host-facing URL). Sharing one variable would either break `iss`
  matching or force user agents through the in-network DNS name. They
  are kept separate by design.
- **Observability.** All admin actions still flow through
  `auth.AuthEvent`. RBAC denials emit `EventForbidden`; successful
  authn emits `EventAuthorized`; integrate via `auth.SetEventHook` once
  per process.
- **Production hardening.** Treat `/admin/*` as power. The dev-only
  caveats already enumerated in
  [docs/KEYCLOAK_SETUP.md §10](KEYCLOAK_SETUP.md#10-production-considerations)
  apply in full — TLS in front of the API, real secrets store for
  `KEYCLOAK_ADMIN_CLIENT_SECRET`, network-level restriction of the
  `/admin/*` prefix, and audit logging hooked via `SetEventHook`.

---

## 6. Known limitations

- The admin UI under `web/admin/` is intentionally minimal — it covers
  the v0.2 surface but has no auth UX, no pagination polish, no
  i18n, and no theming. It is shipped as a developer affordance, not
  a product surface.
- Invitations are synthesized from Keycloak state (required actions /
  `invited_by` attribute) rather than stored in a first-class table.
  This keeps Keycloak as the source of truth but means that
  "list invitations" is a query, not a read of stored rows.
- No bulk operations yet (bulk role assignment, bulk invite). The
  endpoints handle one user at a time.

---

## 7. Verification checklist

Before tagging:

- [ ] `make ci` passes (`fmt-check + vet + build + test + swagger-check`).
- [ ] `make auth-test` returns `200` on `/me`.
- [ ] `GET /admin/users` as `adminuser` returns the seeded users.
- [ ] `GET /admin/users` as `testuser` returns `403`.
- [ ] `features.identity_management: false` → `/admin/users` returns `404`.
- [ ] `docs/swagger.{json,yaml}` contain every `/admin/*` path listed in §2.1.
- [ ] `CHANGELOG.md` `[0.2.0]` entry matches this document.
