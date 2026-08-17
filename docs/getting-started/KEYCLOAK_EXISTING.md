# Install against a Keycloak you already run

> **Who this is for:** you run a Keycloak, you have at least one realm, and you
> want LIGHTWEIGHT to manage users in it. This is the path most real
> installations take.
>
> **Who this is not for:** if you want to see the product working before
> committing anything, use [the bundled Keycloak](KEYCLOAK_BUNDLED.md) instead.

Read this once end to end before touching your realm. It is about twenty
minutes of clicking, and doing it in the wrong order costs more than reading.

---

## Table of contents

1. [Two realms, and why conflating them breaks the install](#1-two-realms-and-why-conflating-them-breaks-the-install)
2. [Supported Keycloak versions](#2-supported-keycloak-versions)
3. [What to create in the installation realm](#3-what-to-create-in-the-installation-realm)
4. [Install LIGHTWEIGHT](#4-install-lightweight)
5. [What to create in each managed realm](#5-what-to-create-in-each-managed-realm)
6. [Reading Verify](#reading-verify)
7. [Activate, and what happens to the previous connection](#7-activate-and-what-happens-to-the-previous-connection)
8. [Multiple realms and multiple Keycloak installations](#8-multiple-realms-and-multiple-keycloak-installations)
9. [TLS and certificates](#tls-and-certificates)
10. [Rotating the Keycloak client secret](#10-rotating-the-keycloak-client-secret)
11. [Error reference](#11-error-reference)

---

## 1. Two realms, and why conflating them breaks the install

LIGHTWEIGHT talks to Keycloak in **two unrelated ways**. Conflating them is the
single most common way a first install goes wrong, so it is the first thing on
this page rather than a footnote.

| | Installation realm | Managed realm |
|---|---|---|
| Answers | "who may administer LIGHTWEIGHT?" | "whose users am I managing?" |
| How many | exactly one, fixed at boot | one per workspace, added at runtime |
| Configured in | `.env`, before first start | the console, after first start |
| LIGHTWEIGHT writes to it | never | yes, that is the point |
| Called a Connection | no | yes |

You may point both at the same Keycloak **server**. They must not be the same
**realm**, or your operators become records that LIGHTWEIGHT administers, and
an operator could delete their own login.

### What is a "realm"?

A Keycloak realm is an isolated tenant inside a Keycloak installation: its own
users, its own roles, its own clients, its own signing keys. Nothing crosses
between realms. In the Keycloak admin console it is the dropdown in the top
left. The realm's **name** is the string in that dropdown and in every URL
under `/realms/<name>/`. That name is what LIGHTWEIGHT calls `realm`.

---

## 2. Supported Keycloak versions

| | |
|---|---|
| **Tested against** | Keycloak **26.0** (`quay.io/keycloak/keycloak:26.0`), in CI and in the live integration suite |
| **Expected to work** | Keycloak 23 and newer |
| **Not supported** | Keycloak 22 and older, and any Red Hat SSO / legacy WildFly distribution |

The floor is 23 because LIGHTWEIGHT depends on the modern Quarkus admin REST
layout (`/admin/realms/<realm>/…`) and on `realm-management` client roles
appearing under `resource_access` in the service account's token. Both have
been stable since 23. Nothing below that is tested, and nothing below that is
claimed.

If you run something newer than 26, it will very likely work. Only 26.0 is
verified by an automated suite.

---

## 3. What to create in the installation realm

Pick or create a realm for LIGHTWEIGHT's own operators. `lightweight` is a
reasonable name. In it, create two things.

### 3a. One public client, for the console's browser login

| Setting | Value |
|---|---|
| Client ID | anything you like. This is your `KEYCLOAK_CLIENT_ID` |
| Client authentication | **off** (this makes it a *public* client) |
| Authentication flow: Standard flow | **on** |
| Authentication flow: Direct access grants | off |
| Valid redirect URIs | `https://<your-lightweight-url>/admin` |
| Web origins | `https://<your-lightweight-url>` |

**Public, not confidential**, because the thing logging in is a browser. A
browser cannot keep a secret, so LIGHTWEIGHT's console uses Authorization Code
with PKCE. Keycloak accepts PKCE on any public client and there is nothing to
configure for it.

The redirect URI has **no wildcard and no trailing slash**. The console sends
exactly `<base>/admin`. If you are not sure what your installation will compute,
start it and open `<base>/admin/config.json`, which prints the `redirectUri` it
will send.

**Where do I find the client ID?** It is the value you typed in the *Client ID*
field. Keycloak shows it in *Clients*, first column.

### 3b. One user holding the realm role `admin`

*Realm roles* → create or find `admin` → *Users* → your user → *Role mapping* →
*Assign role* → filter by realm roles → `admin`.

That role name is **not configurable**. A token without it is rejected by every
`/v1` route. LIGHTWEIGHT has no second permission model for humans: scopes
describe what a *machine* may do, never an operator.

### 3c. Optional: a second client for live revocation checks

By default an operator's `admin` role is trusted for the lifetime of their
token. To have LIGHTWEIGHT re-ask Keycloak on every request instead, create a
second client in the **installation** realm:

| Setting | Value |
|---|---|
| Client authentication | **on** (confidential) |
| Service accounts roles | **on** |
| Service account roles to grant | `view-users` and `view-realm`, from `realm-management` |

Then set `KEYCLOAK_ADMIN_CLIENT_ID` and `KEYCLOAK_ADMIN_CLIENT_SECRET`.

This is fail-closed on purpose: if the check cannot reach Keycloak, requests get
`503` rather than falling back to the token's claim. Configure it only if your
API can genuinely reach Keycloak, and leave both empty otherwise.
`./scripts/init.sh` leaves them empty.

---

## 4. Install LIGHTWEIGHT

```bash
git clone https://github.com/JoaoGabrielVianna/lightweight-saas-backend.git
cd lightweight-saas-backend

./scripts/init.sh --keycloak-url https://sso.example.com \
                  --realm lightweight \
                  --console-client-id <the client id from 3a>

docker compose up -d          # no --profile: PostgreSQL + LIGHTWEIGHT only
curl -fsS localhost:8080/health/ready
```

`init.sh` asks nothing, writes `.env` with mode `600`, generates the secrets
keyring and a database password, and **refuses to touch an existing `.env`**.
Re-running it is a no-op, which is what an idempotent provisioning step needs.

### What you supplied, and what was generated

You typed **three** values. Everything else was derived or generated:

| You supplied | Generated for you | Left empty on purpose |
|---|---|---|
| `KEYCLOAK_URL` | `SECRETS_KEYRING`, `SECRETS_KEY_CURRENT` | `KEYCLOAK_JWKS_URL` (derived) |
| `KEYCLOAK_REALM` | `POSTGRES_PASSWORD`, `DB_URL` | `KEYCLOAK_ADMIN_BASE_URL` (derived) |
| `KEYCLOAK_CLIENT_ID` | `METRICS_TOKEN` | `KEYCLOAK_CLIENT_SECRET` (public client) |
| | `ADMIN_CONSOLE_CLIENT_ID` (= your client id) | `KEYCLOAK_ADMIN_CLIENT_ID` / `_SECRET` (see 3c) |
| | `DEV_PLAYGROUND_ENABLED=false` | `CORS_ALLOWED_ORIGINS` (same-origin console) |

You do **not** need to read all 41 lines of `.env.example`. See
[Configuration](../operations/RUNNING.md#2-configuration-contract) for the full
table, and [the minimum-input summary](#what-you-supplied-and-what-was-generated)
above for what actually matters.

> **Back up `SECRETS_KEYRING`, separately from the database.** Every provider
> client secret in your database is sealed under it. A `pg_dump` restored
> without the keyring contains credentials nobody can decrypt, and nothing
> reports the loss until a workspace tries to reach its realm.

### The variable that catches everyone

`KEYCLOAK_URL` must be the address **browsers** use, because it decides the
`iss` claim tokens must carry. An installation that puts an internal address
here starts cleanly and then rejects every token as `invalid issuer`.

If the API reaches Keycloak on a *different* address than browsers do (a
private network, a container gateway), set **both**:

```sh
KEYCLOAK_URL=https://sso.example.com          # public, drives `iss`
KEYCLOAK_JWKS_URL=http://keycloak:8080/realms/lightweight/protocol/openid-connect/certs
KEYCLOAK_ADMIN_BASE_URL=http://keycloak:8080
```

Setting only the first of the two overrides produces an installation that
starts, validates tokens, and then fails authorization checks it cannot route.

Open `http://localhost:8080/admin` and sign in as the user from step 3b.

`localhost:8080` is correct **on the host you just ran `docker compose` on**.
From anywhere else, substitute that host's address, and note that the redirect
URI you registered in step 3a must match the address you actually browse to.
If port 8080 is taken, set `API_HOST_PORT` in `.env` and restart; see
[Ports and collisions](KEYCLOAK_BUNDLED.md#ports-and-collisions).

---

## 5. What to create in each managed realm

This is per **Connection**, and it happens after LIGHTWEIGHT is running. Do it
once for every realm you want managed.

### Does LIGHTWEIGHT require a dedicated Keycloak client?

Yes, and it must be a different client from the console one in step 3a. The
console client is public and used by a browser; this one is confidential and
used by a server. They cannot be the same client because Keycloak cannot make a
client both.

### The client

| Setting | Value |
|---|---|
| Client authentication | **on** (confidential) |
| Service accounts roles | **on** (required) |
| Authentication flow: Standard flow | off |
| Authentication flow: Direct access grants | off |

Standard flow and direct access grants are off because nothing logs in as a
human through this client. LIGHTWEIGHT uses the **client credentials** grant,
which is what "Service accounts roles: on" enables.

### The service-account roles

*Clients* → your client → *Service accounts roles* → *Assign role* → filter by
**clients** → `realm-management`.

What you grant here decides what LIGHTWEIGHT reports the connection can do:

| Granted | Verify reports | A credential can |
|---|---|---|
| nothing | `healthy`, `access_mode=limited` | nothing useful |
| `view-users` + `view-realm` | `healthy`, `access_mode=read_only` | read users, roles, sessions |
| `realm-admin` | `healthy`, `access_mode=full` | everything its scopes allow |
| `manage-users` + `manage-realm` + the two `view-*` | `healthy`, `access_mode=full` | everything its scopes allow |

**Least privilege:** grant `view-users` and `view-realm` if your backends only
read. Grant the four-role set if they write. `realm-admin` is the simple answer
and implies the rest, at the cost of also granting client, group and identity
provider management that LIGHTWEIGHT never uses.

**What changes when privileges are limited:** LIGHTWEIGHT refuses writes
centrally, before contacting Keycloak, and the console disables mutation
controls and shows a banner. Reads keep working. You do not get a confusing
half-working realm.

### The client secret

*Clients* → your client → *Credentials* → *Client secret* → copy.

**Where do I regenerate it?** Same screen, *Regenerate*. See
[§10](#10-rotating-the-keycloak-client-secret) before you press it.

### Enter it in the console

*Workspaces* → create one → *Connections* → *New connection*. Four values:

```
Base URL       https://sso.example.com     (the Keycloak root, not /realms/...)
Realm          acme
Client ID      lightweight-acme
Client secret  <copied above>
```

**Which URL do I give LIGHTWEIGHT?** The Keycloak **root**, the same host you
open the Keycloak admin console on, with no `/realms/…` and no `/admin` suffix.
LIGHTWEIGHT appends the rest.

The secret is sealed with the installation's keyring the moment you submit it,
and there is no endpoint that can return it: the `Connection` type has no field
for secret material at all. The console shows `has_client_secret: true` and
nothing else.

Then press **Verify**.

---

## Reading Verify

### What Verify actually tests

Four probes, in order, against the realm you configured:

1. **Provider reachable.** Can this process open a connection to the base URL
   and get an answer from the realm's OIDC discovery endpoint?
2. **Client authenticates.** Does the client credentials grant return an access
   token for this client id and secret?
3. **Reads work.** Can the service account read realm settings, and list users?
4. **Write grant.** Does the returned token carry a `realm-management` role
   that permits writing?

### Does Verify modify the realm?

**No.** It is a read-only probe, and that is a hard rule in the code: it creates
no test user, writes nothing, and deletes nothing. Probe 4 reads the roles out
of the service account's own access token rather than attempting a write.

That has one honest consequence. If the client's scope does not include its
`realm-management` roles, the token does not list them, and LIGHTWEIGHT reports
`access_mode=unknown` rather than guessing. Writes are then permitted and the
authoritative answer arrives from Keycloak as a `403`, surfacing as
`provider_forbidden`. It never guesses upward.

### The two state axes

`health` and `access_mode` are separate and both matter:

| `health` | Meaning |
|---|---|
| `healthy` | The last probe reached the provider and authenticated. |
| `unhealthy` | The last probe failed. The `health_message` says how. |
| `unknown` | Never verified. |

| `access_mode` | Meaning | Writes |
|---|---|---|
| `full` | The service account provably holds a write grant. | allowed |
| `read_only` | Reads worked, and Keycloak positively reported no write grant. | refused |
| `limited` | The admin reads themselves were refused. Under-privileged. | refused |
| `unknown` | No usable evidence either way. | allowed, Keycloak decides |

**Verifications expire after one hour.** That is deliberate: "answered
correctly" is a perishable fact, and activating on a stale probe would let you
route production traffic through a realm that stopped answering yesterday.

---

## 7. Activate, and what happens to the previous connection

**Activate** promotes a verified draft to the workspace's live connection.

| Rule | |
|---|---|
| Requires a verification that passed **within the last hour** | re-Verify if it expired |
| **Retires the workspace's previous active connection, in one transaction** | there is never a moment with two, or with none |
| Exactly one active per workspace | enforced by a partial unique index, not by application code |
| `retired` is terminal | no reactivation. Create a new connection |
| Not idempotent | activating an already-active connection returns `connection_already_active` rather than succeeding |

That last row is deliberate. A caller retrying may believe they are switching
away from a *different* connection, and silently confirming that would be worse
than an error.

### Replacing an active connection safely

This is the supported procedure, and it is the same one used for rotating a
client secret:

```
1. Create a NEW connection with the new coordinates.   (the old one keeps serving)
2. Verify it.                                          (the old one keeps serving)
3. Activate it.       ← the switch. The old one is retired in the same transaction.
4. Delete the retired one, once you are satisfied.
```

There is no window in which the workspace has no active connection. In-flight
requests already resolved keep their provider until it is rebuilt; the next
request resolves the new one, because the provider cache is keyed on the
connection's id **and** its `updated_at`.

---

## 8. Multiple realms and multiple Keycloak installations

Both are supported, and neither needs any configuration.

**Multiple realms on the same Keycloak:** create one workspace per realm, and
give each a connection whose `realm` differs and whose base URL is the same.
Each realm needs its own confidential client (§5), because a client belongs to
exactly one realm in Keycloak.

**Completely different Keycloak installations:** identical, except the base URL
differs too. A Connection stores its own base URL, so nothing at the
installation level pins you to one Keycloak. Workspace A can point at
`sso-eu.example.com` and workspace B at `sso-us.example.com`.

What is **fixed at boot** is only the *installation* realm from §3, the one
your operators log into. That one is a single `KEYCLOAK_URL` + `KEYCLOAK_REALM`
pair and cannot vary per workspace.

Isolation is structural: each connection gets its own provider instance, and
every piece of state that could leak, the service-account token cache above all,
is a field on that instance.

---

## TLS and certificates

**LIGHTWEIGHT has no TLS configuration surface. This is a limitation, stated
here rather than discovered later.**

Outbound HTTPS to your Keycloak uses Go's default transport and the container's
system trust store. The image installs `ca-certificates`, so any Keycloak
presenting a certificate from a **publicly trusted CA** works with no
configuration.

| Your Keycloak's certificate | Result |
|---|---|
| Public CA (Let's Encrypt, DigiCert, …) | works, nothing to configure |
| Private or corporate CA | **fails**: `x509: certificate signed by unknown authority` |
| Self-signed | **fails**, same error |
| Plain HTTP | works. Only acceptable on a private network |

There is **no** `INSECURE_SKIP_VERIFY`, no custom CA environment variable, and
no per-connection certificate setting. Adding one is not planned for v0.4.

### The supported workaround

Add your CA to the container's trust store by mounting it and rebuilding the
bundle. In a `docker-compose.override.yml`:

```yaml
services:
  api:
    volumes:
      - ./my-ca.crt:/usr/local/share/ca-certificates/my-ca.crt:ro
    user: root
    entrypoint: ["/bin/sh", "-c", "update-ca-certificates && exec /app/api"]
```

This is standard container practice rather than a LIGHTWEIGHT feature, and it
is what an operator would do for any Go service. It is documented here because
the failure mode is otherwise a `x509` string with no obvious owner.

If you cannot do that, terminate TLS at a reverse proxy that LIGHTWEIGHT
reaches over plain HTTP on a private network, and point
`KEYCLOAK_ADMIN_BASE_URL` and `KEYCLOAK_JWKS_URL` at it.

---

## 10. Rotating the Keycloak client secret

A connection's client secret can only be edited while the connection is a
`draft`: `PATCH` on an active connection is refused, and it resets verification
when it does apply. So rotation is not an edit, it is a replacement, and it uses
exactly the procedure in [§7](#7-activate-and-what-happens-to-the-previous-connection):

```
1. Keycloak: Clients → your client → Credentials → Regenerate.
   Copy the new secret. The old one stops working immediately.

2. LIGHTWEIGHT: create a NEW connection in the same workspace,
   same base URL, same realm, same client id, NEW secret.

3. Verify it.  It should report healthy.

4. Activate it.  The old connection is retired in the same transaction.

5. Delete the retired connection.
```

Between steps 1 and 4 the old connection holds a secret Keycloak no longer
accepts, so identity operations for that workspace fail. Keep the gap short. If
you need a zero-gap rotation, create the new connection **before** regenerating
in Keycloak is not possible with a single client; use a second Keycloak client
instead, which the same procedure supports because step 2 lets you change the
client id too.

> This is the **provider** secret. Rotating LIGHTWEIGHT's own master keyring,
> which seals it, is a different and independent procedure:
> [`../SECRET_KEY_ROTATION.md`](../SECRET_KEY_ROTATION.md).
>
> Rotating a **project credential** (`lw_sk_…`) is a third thing again:
> [create a new one, deploy, revoke the old](FIRST_CREDENTIAL.md#rotating-or-losing-a-credential).

---

## 11. Error reference

Every way this can be wrong, and what is actually wrong. Verify distinguishes
all of them, so you should never have to guess.

| Message | What is actually wrong |
|---|---|
| `provider unreachable` | Base URL is wrong, or LIGHTWEIGHT cannot route to it. Also what a private-CA TLS failure looks like: check the API logs for `x509` |
| `realm not found` | Realm name is wrong, or the realm does not exist on that server |
| `admin client authentication failed` | Client id **or** client secret is wrong |
| `…lacks realm-management privileges` | Client id and secret are right; the service account has no roles |
| `…has no write privileges` | Read-only roles only. Grant `manage-users` and re-verify |

The third row covers both a wrong id and a wrong secret. Keycloak answers
`invalid_client` to both, and reporting which one was wrong would tell an
attacker whether a client id exists.

Runtime errors, after activation:

| Code | Meaning | Fix is in |
|---|---|---|
| `workspace_connection_missing` | No active connection on this workspace | LIGHTWEIGHT |
| `workspace_connection_unusable` | Active connection cannot be turned into a provider | LIGHTWEIGHT |
| `connection_read_only` | Service account has no write grant | Keycloak role mappings |
| `provider_forbidden` | Keycloak refused the service account | Keycloak role mappings |
| `provider_credentials_unavailable` | The sealed secret could not be opened. The keyring changed | your `SECRETS_KEYRING` |

`invalid issuer` on **every** token, right after install, is almost always
`KEYCLOAK_URL`: see [the variable that catches everyone](#the-variable-that-catches-everyone).

---

## Next

**→ [First workspace, connection, project and credential](FIRST_CREDENTIAL.md)**

Deeper reference: [`../CONNECTIONS.md`](../CONNECTIONS.md) for the full
lifecycle and invariants · [`../WORKSPACES.md`](../WORKSPACES.md) for workspace
semantics.
