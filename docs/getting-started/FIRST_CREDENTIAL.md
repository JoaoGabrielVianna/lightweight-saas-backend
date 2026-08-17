# Your first workspace, connection, project and credential

> **Where you are:** LIGHTWEIGHT is running and you can sign in to
> `http://<your-host>:8080/admin` as an operator holding the realm role
> `admin`. If not, go back to
> [I already have Keycloak](KEYCLOAK_EXISTING.md) or
> [I need a Keycloak](KEYCLOAK_BUNDLED.md).
>
> **Where you are going:** the three values a backend needs.

Everything on this page happens in the console. There is an equivalent REST
call for each step, and they are in [`../swagger.yaml`](../swagger.yaml), but
you do not need them to get through this.

```
1. Workspace          the tenant boundary
2. Connection         which Keycloak realm it routes to
3. Verify             prove the realm answers, and what it will allow
4. Activate           make it the one the workspace uses
5. Project            a backend that will consume the API
6. Scopes             what that backend may do
7. Credential         the token it authenticates with       ← shown once
```

---

## 1. Create a workspace

*Workspaces* → **New workspace**. It needs a name. The slug is derived and is
what appears in URLs.

A **workspace** is the tenant boundary. It owns connections, projects,
credentials and its own audit trail, and its id, `ws_<uuid>`, appears in every
API path a backend will ever call. Create one per tenant, or one per
environment, or one for the whole company. The product does not care; it cares
that everything below hangs off exactly one.

You can create more later. The selector in the top bar switches between them,
and every identity screen follows it.

---

## 2. Add a connection

*Connections* → **New connection**. Four values:

| Field | What it is |
|---|---|
| Base URL | Your Keycloak root, e.g. `https://sso.example.com`. Not `/realms/…`, not `/admin` |
| Realm | The realm name, e.g. `acme` |
| Client ID | The confidential client you created for LIGHTWEIGHT |
| Client secret | That client's secret, from *Credentials* in Keycloak |

Those four come from [§5 of the existing-Keycloak guide](KEYCLOAK_EXISTING.md#5-what-to-create-in-each-managed-realm).
**If you are on the bundled stack**, the realm export already contains a
suitable client, so you can use it as-is:

```
Base URL       http://keycloak:8080          (as the API reaches it, inside the network)
Realm          saas
Client ID      saas-backend-admin
Client secret  saas-backend-admin-secret
```

Those values are committed in this repository, which is precisely why the
bundled stack is not a deployment.

The secret is sealed with AES-256-GCM before it reaches the database, and there
is no endpoint that can return it. Responses carry `has_client_secret: true`
and nothing else. If you lose it, you replace the connection; you do not read
it back.

A new connection is always a **draft**. Nothing routes through it yet.

---

## 3. Verify the connection

Press **Verify**.

Verify is a **read-only probe**. It creates no test user and writes nothing. It
answers two separate questions:

**Is it healthy?** Can LIGHTWEIGHT reach the provider, and does the client
authenticate?

**What will it allow?** `access_mode` reports what the service account can do:

| `access_mode` | What it means | Writes |
|---|---|---|
| `full` | The service account provably holds a write grant | allowed |
| `read_only` | Reads work; Keycloak reported no write grant | refused |
| `limited` | Even the admin reads were refused | refused |
| `unknown` | No usable evidence either way | allowed, Keycloak decides |

If it fails, the message names the cause rather than making you guess. The full
table is in [Reading Verify](KEYCLOAK_EXISTING.md#reading-verify); the short
version:

| Message | Fix |
|---|---|
| `provider unreachable` | Base URL, routing, or [a private CA](KEYCLOAK_EXISTING.md#tls-and-certificates) |
| `realm not found` | Realm name |
| `admin client authentication failed` | Client id or client secret |
| `…lacks realm-management privileges` | Grant the service account roles in Keycloak |
| `…has no write privileges` | Grant `manage-users`, then Verify again |

**A verification expires after one hour.** If you get distracted, Verify again
before activating.

---

## 4. Understand what you just proved

Nothing routes yet. `health` says the realm answered; `access_mode` says what
it will permit. Both are recorded on the connection and both are visible in the
console.

This matters because the failure you are avoiding is the one where a workspace
is activated against a realm that authenticates fine and then refuses every
write, and you find out from a support ticket.

---

## 5. Activate the connection

Press **Activate**.

| What happens | |
|---|---|
| This connection becomes the one the workspace routes through | immediately |
| The workspace's **previous** active connection is retired | in the same transaction |
| Exactly one active connection per workspace | enforced by a database index |

Requires a verification that passed **within the last hour**, and activating an
already-active connection returns `connection_already_active` rather than
silently succeeding.

Retired is terminal. To go back, create a new connection.

From now on, every identity screen in the console, and every `/v1` call a
credential makes, resolves through this connection. If the console says
**"This workspace isn't connected yet"**, you have a workspace with no active
connection: you are between steps 2 and 5.

---

## 6. Create a project

*Projects* → **New project**. It needs a name.

A **project** is a backend that consumes this API on behalf of this workspace:
your billing worker, your web app, your admin tool. One project per deployable
thing, because that is the unit you will want to revoke independently.

Projects are archived, never deleted, so the `prj_` ids in audit history never
become dangling references.

---

## 7. Choose scopes and create a credential

Open the project → **New credential**. Give it a label, pick its scopes, and
optionally an expiry.

The label is for you; it is what you will read in six months when deciding
whether something still uses this key.

### The scope vocabulary

There are **9**, and this is all of them:

| Scope | Grants |
|---|---|
| `users:read` | list, get users |
| `users:write` | create, update, delete users; set and reset passwords |
| `roles:read` | list, get roles; read a user's roles |
| `roles:write` | create, update, delete roles; assign and unassign |
| `sessions:read` | list sessions, realm-wide or per user |
| `sessions:revoke` | delete sessions, realm-wide or per user |
| `invitations:read` | list invitations |
| `invitations:write` | create, resend, delete invitations |
| `audit:read` | read this workspace's audit trail |

Grant the narrowest set that works. A credential that only reads users gets
`users:read` and nothing else; if it is ever leaked, that is the whole blast
radius.

**Scopes are not editable after creation.** To change what a credential may do,
create a new one with the right scopes and revoke the old one. That is
deliberate: a credential whose powers can grow is a credential whose audit
history stops meaning anything.

### What a credential can never do

No credential, whatever its scopes, can manage workspaces, connections,
projects or other credentials. Those routes are operator-only, without
exception. A credential able to mint credentials would make revocation
meaningless, because revoking one would find it had already issued another.

---

## 8. Copy the credential

The secret is shown **once**, in a modal, at creation. It is stored only as a
digest, so there is no endpoint that could return it even if someone added one.

The modal gives you the three values your backend needs, ready to paste:

```bash
LIGHTWEIGHT_URL=https://identity.example.com
LIGHTWEIGHT_WORKSPACE_ID=ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301
LIGHTWEIGHT_API_KEY=lw_sk_…
```

Copy them somewhere durable before you close it. Closing the modal drops the
only copy that browser ever had.

That is the whole handoff. There is deliberately nowhere to put a fourth
variable: the backend never learns which provider sits behind LIGHTWEIGHT,
which realm this workspace routes to, or what credential opens it. An operator
can repoint the workspace at a different realm and the backend keeps working,
unchanged and unrestarted.

---

## Rotating, or losing, a credential

There is no rotate endpoint, and that is not an omission. Rotation is:

```
1. Create a new credential with the same scopes.
2. Deploy it to your backend.
3. Revoke the old one.
```

No new state machine, no overlap window to reason about, and the two keys are
independently visible in the audit trail while both exist.

**If you lost a secret**, the procedure is the same: it cannot be recovered, so
create a replacement and revoke the lost one. Revocation is effective
immediately; the next request using it fails with `401`, though a request
already in flight completes.

The console shows each credential's `last_used_at`, which is what tells you
whether something still depends on a key you are about to revoke.

---

## Next

**→ [Connect your backend](CONNECT_BACKEND.md)**

Deeper reference: [`../WORKSPACES.md`](../WORKSPACES.md) ·
[`../CONNECTIONS.md`](../CONNECTIONS.md) · [`../PROJECTS.md`](../PROJECTS.md)
for the scope-by-route matrix and the credential format.
