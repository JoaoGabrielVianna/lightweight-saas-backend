# Frontend readiness for the `/v1` Workspace Identity API

> **Superseded 2026-08-09 — this migration has happened.** Kept as the record of
> what was assessed and predicted before Slice 6, which is worth having when the
> next surface migrates. For what actually shipped, and where the predictions
> below turned out to be incomplete, read
> [WORKSPACE_CONSOLE.md](WORKSPACE_CONSOLE.md).
>
> The largest thing this assessment under-weighted: it treated six views as
> "mechanical". The path replacement was mechanical; the *isolation* work it
> implies — stale responses, cross-workspace mutations, per-workspace page state
> — was the majority of the slice and is not mentioned here at all.

**Purpose:** make Slice 6 implementation-ready. Nothing in the frontend was
changed by Slice 5; this is an assessment of what it would take.

**Assessed:** 2026-08-09, against the 24-operation `/v1` surface documented in
[WORKSPACE_IDENTITY_API.md](WORKSPACE_IDENTITY_API.md).

---

## 1. Verdict

The identity views can migrate. **The blocker is not missing API capability —
it is that the console has no concept of a workspace.** Every request it makes
today is implicitly scoped to the one realm in `KEYCLOAK_*`, and every `/v1`
path requires a `{workspace_id}` the app has nowhere to get.

That, plus one bug in the shared API client, is the real work.

## 2. Per-view assessment

| View | Endpoints used | Migrates? | What it needs |
|---|---|:--:|---|
| `users.js` | `GET /admin/users` | **mechanical** | path + workspace id |
| `user-detail.js` | user CRUD, roles, sessions, reset-password | **mechanical** | path + workspace id |
| `roles.js` | full role CRUD | **mechanical** | path + workspace id |
| `sessions.js` | `GET`/`DELETE` sessions | **mechanical** | path + workspace id |
| `overview.js` | `GET /admin/users` | **mechanical** | path + workspace id |
| `invitations.js` | invitations + `POST /admin/users/password` | **near-mechanical** | the password path becomes `POST /v1/…/users`, whose body differs (see §4) |
| `apiexplorer.js` | a hardcoded catalogue of 15 admin routes | **rewrite the catalogue** | it *documents* routes; the list is data, so this is an edit, not a redesign |
| `auditlogs.js` | `GET /admin/audit-events` | **stays** | control-plane, installation-wide, deliberately unscoped |
| `settings.js` | mentions endpoints in prose only | **stays** | no calls to change |
| `email.js` | SMTP settings | **blocked** | no `/v1` equivalent — deferred by design |
| `email-templates.js` | email templates | **blocked** | no `/v1` equivalent — deferred by design |

**Six views migrate mechanically. Two are blocked on deliberately deferred
capability. Three stay where they are.**

## 3. The blocking work: workspace state

The console has no workspace concept at all. Slice 6 must add:

1. **A workspace list call** — `GET /v1/workspaces` already exists.
2. **A selected-workspace in app state.** `lib/state.js` is the store; this is
   a new key plus a `STORAGE_KEYS` entry so a reload does not lose it.
3. **A selector in the shell.** Header or sidebar; out of scope here.
4. **Path construction.** Today views hardcode `"/admin/users"`. They will need
   the workspace id woven in. The lowest-risk shape is a helper in `lib/api.js`
   (`wsPath("/users")` reading the selected workspace) so no view builds a
   `/v1` path by hand and the "no workspace selected" case has one home.
5. **An empty state.** A fresh installation has no workspaces, and every
   identity view must say so rather than erroring.

Also needed: the console currently assumes an identity API is always present.
`/v1` identity routes are **absent (404)** when `SECRETS_MASTER_KEY` is unset.
The shell should detect that once and route to an explanation, not let each
view discover it separately.

## 4. API shape differences beyond path replacement

Four, and the first is a real bug that will bite immediately.

### 4.1 The error envelope — `lib/api.js` needs fixing first

```js
// lib/api.js, APIError constructor
super(message || (typeof body === "object" ? body.error : body) || `HTTP ${status}`);
```

`/admin/*` returns `{"error": "not found"}` — a **string**. `/v1` returns
`{"error": {"code", "message", "request_id"}}` — an **object**. Passing the
object to `super()` renders the message as `[object Object]`, which is exactly
the class of defect KI-001 was.

Fix it **before** migrating any view, and make `APIError` carry the `code` as a
field. The codes are the contract — views branching on
`workspace_connection_missing` or `connection_read_only` are the whole reason
the envelope is structured. Migrating views first and fixing this after means
every view gets written against a broken error path.

### 4.2 Pagination echo changed

`/admin/users` echoes the caller's raw `first`/`max`; `/v1` returns the
**effective** values ([TD-020](TECH_DEBT.md#td-020) is fixed on `/v1` only). Any
view computing the next page from the echoed `max` gets *more correct* results
after migrating, but the values will differ — check `users.js` pagination
arithmetic rather than assuming it carries over.

### 4.3 Direct user creation body

`invitations.js` posts to `/admin/users/password` with
`{email, first_name, last_name, temporary_password, roles}`. The `/v1`
equivalent is `POST /v1/workspaces/{id}/users` with the **same field names** —
but validation now happens in the service and returns
`{"error":{"code":"invalid_request"}}` rather than
`{"error":{"message":"..."}}`. The inline error rendering in that modal needs
updating.

### 4.4 New failure states views must handle

These have no `/admin/*` equivalent and will otherwise surface as unexplained
409s:

| Code | What the UI should say |
|---|---|
| `workspace_connection_missing` | "This workspace isn't connected yet" → link to connections |
| `workspace_connection_unusable` | "Connection needs attention" → link to verify |
| `connection_read_only` | Disable write controls, explain the service account needs realm-management roles |
| `workspace_archived` | Read-only banner |
| `provider_forbidden` | Same as read-only — the fix is in Keycloak, not in the console |

`connection_read_only` and `provider_forbidden` are worth handling properly
rather than as a generic toast: they are the two an operator will actually hit
while setting a workspace up, and both have a specific remedy.

## 5. What is NOT blocked

- No missing identity capability. Every operation the console performs today
  exists on `/v1`, except SMTP and email templates.
- Response **success** shapes are identical — `/v1` renders users, roles,
  sessions and invitations with internal/identity's own wire types, so table
  and detail rendering carries over untouched.
- Auth is unchanged: same bearer token, same realm-`admin` requirement, same
  middleware chain.

## 6. Suggested Slice 6 order

1. Fix `APIError` (§4.1). Standalone, testable, unblocks everything.
2. Add workspace state + `wsPath()` helper + empty state.
3. Add the workspace selector.
4. Migrate the six mechanical views.
5. Migrate `invitations.js`, including the create-user body.
6. Rewrite the API Explorer catalogue.
7. Add the five failure states from §4.4.
8. Leave `email.js` / `email-templates.js` on `/admin/*` and say so in the UI.

Steps 1 and 2 are the ones that decide whether the rest is mechanical.

---

## See also

- [WORKSPACE_CONSOLE.md](WORKSPACE_CONSOLE.md) — what actually shipped
- [WORKSPACE_IDENTITY_API.md](WORKSPACE_IDENTITY_API.md) — the surface being migrated to
- [TECH_DEBT.md](TECH_DEBT.md) — TD-020, TD-022
