# Getting started

This is the entry point. Everything a new installation needs is reachable from
this page, in order, and nothing here asks you to read Go source.

The journey has five steps. It branches once, at step 2, and never again.

```
1. Understand what LIGHTWEIGHT is          ← ../../README.md
        │
2. Install it
        ├── I already have Keycloak  ──▶  KEYCLOAK_EXISTING.md
        └── I need a Keycloak        ──▶  KEYCLOAK_BUNDLED.md
        │
3. First workspace, connection, project, credential   ──▶  FIRST_CREDENTIAL.md
        │
4. Connect your backend                               ──▶  CONNECT_BACKEND.md
        │
5. Run it for real                                    ──▶  ../operations/RUNNING.md
```

---

## Step 1. Understand what this is

LIGHTWEIGHT is a self-hosted control plane that puts one or more Keycloak
realms behind a single workspace-scoped HTTP API.

It is **not** your application backend. It stores no product data. It owns the
boundary between your services and the identity provider behind them, so that a
service which needs to create a user does not need Keycloak admin rights.

The five nouns, and the whole product is these five:

| Concept | What it is |
|---|---|
| **Workspace** | A tenant boundary, `ws_<uuid>`. Everything hangs off it. |
| **Connection** | Coordinates for one Keycloak realm. A workspace may hold several; exactly one is active. |
| **Project** | A backend that consumes this API on behalf of one workspace, `prj_<uuid>`. |
| **Credential** | A machine token, `lw_sk_…`. Shown once. Revocable. |
| **Scope** | What a credential may do, from a fixed vocabulary. |

If that is not yet clear, [the README](../../README.md#core-concepts) has the
same five with a diagram. It is worth two minutes now, because every screen and
every error message from here on uses these words.

---

## Step 2. Install

Pick the one that describes you. They produce the same product; they differ
only in where the Keycloak comes from.

### → [I already have Keycloak](KEYCLOAK_EXISTING.md)

You run a Keycloak and have a realm you want LIGHTWEIGHT to manage. This is the
path most real installations take. The guide covers which client to create,
whether it is public or confidential, which service-account roles are required,
what least privilege costs you, what Verify actually tests, and what every
failure message means.

### → [I need a Keycloak](KEYCLOAK_BUNDLED.md)

You want to see the product working before deciding anything, or you are
setting up a development machine. The guide covers the bundled `dev-idp`
profile: what it starts, which ports it takes, what credentials it generates,
what survives a restart, and exactly where it stops being suitable.

**Prerequisites for both:** Docker with the Compose v2 plugin, `git`, and
`curl`. No Go toolchain is required to install; it is required only to
contribute.

---

## Step 3. Your first credential

**→ [First workspace, connection, project and credential](FIRST_CREDENTIAL.md)**

Both install paths end at a running console with an operator account. This
document takes you from there to the three values a backend needs, through the
console:

```
create a Workspace
  → add a Connection to a Keycloak realm
  → Verify it
  → Activate it
  → create a Project
  → choose scopes
  → create a Credential
```

It also explains what each state means, and what to do when a step refuses.

---

## Step 4. Connect your backend

**→ [Connect your backend](CONNECT_BACKEND.md)**

You now hold:

```bash
LIGHTWEIGHT_URL=…
LIGHTWEIGHT_WORKSPACE_ID=ws_…
LIGHTWEIGHT_API_KEY=lw_sk_…
```

The document covers the Go SDK and raw HTTP with equal weight: first call,
create a user, assign a role, and what to do about `insufficient_scope` and
`429`.

---

## Step 5. Run it for real

**→ [`../operations/RUNNING.md`](../operations/RUNNING.md)**

The full configuration table, health probes, graceful shutdown, backup, key
rotation and the production smoke procedure.

Two things to read before a production deployment:

- [Production topology](../../README.md#production-topology). Single instance
  is what is supported. Two replicas double your effective rate limit.
- [`../SECRET_KEY_ROTATION.md`](../SECRET_KEY_ROTATION.md). The keyring is not
  in your database dump. A restore without it produces provider credentials
  nobody can decrypt.

---

## Am I done? The first-success checklist

Six checkpoints. Each one is observable, and each one tells you which of the
previous ones is actually at fault when it fails. Work down the list; the first
failure is where the problem is, regardless of what the last step reported.

| # | Checkpoint | How you know | If it fails |
|---|---|---|---|
| 1 | **The server is running** | `curl -fsS localhost:8080/health/live` returns `200` | The container is not up. `docker compose ps`, then its logs |
| 2 | **The server can serve traffic** | `curl -fsS localhost:8080/health/ready` returns `{"status":"ready",...}` | `503` means a global dependency is down. The `checks` object names which |
| 3 | **You are an operator** | `http://localhost:8080/admin` loads the console *and the sidebar draws* | A screen saying the account cannot use the console means the account lacks the realm role `admin`, not that the install is broken |
| 4 | **A workspace has a live realm behind it** | Its connection shows **active** after a successful Verify | Verify names the cause. Verified and active are two different states — a verified connection still has to be activated |
| 5 | **A credential exists** | The creation modal showed a `lw_sk_…` secret once | If you closed it without copying, create another and revoke the first. It cannot be recovered |
| 6 | **The whole path works, from outside** | The [30-second check](CONNECT_BACKEND.md#the-30-second-check) returns `200` with a JSON page of users | This is the one that matters. It proves 1 through 5 at once, plus that the credential's scopes are right |

**Checkpoint 6 is the definition of "my installation is working."** It exercises
every layer in one request: the credential authenticates, its scope is checked,
its workspace binding is enforced, that workspace's active connection is
resolved, and a real Keycloak realm answers.

```bash
curl -sS -H "Authorization: Bearer $LIGHTWEIGHT_API_KEY" \
  "$LIGHTWEIGHT_URL/v1/workspaces/$LIGHTWEIGHT_WORKSPACE_ID/users?max=10"
```

Keep that command. Anything that breaks later is diagnosed by running it first.

> **What a green checklist does not mean.** It does not mean you are ready for
> production. Read [Step 5](#step-5-run-it-for-real) before you get there: the
> supported topology is a single instance, and the keyring is not in your
> database backup.

---

## If you are here to contribute, not to install

[`QUICKSTART.md`](QUICKSTART.md) is the contributor stack: Go toolchain, `make`
targets, the regeneration pipeline and the daily loop. It is not an
installation guide, and `make init` is a fork tool rather than an installer.
Installing uses `./scripts/init.sh`, which asks nothing and needs no Go.

See also [`../../CONTRIBUTING.md`](../../CONTRIBUTING.md) and
[`../QUALITY_GATE.md`](../QUALITY_GATE.md).

---

## Troubleshooting index

Symptoms an install actually produces, in roughly the order you would hit them.
Every row points at a document that explains the cause rather than just the fix.

**Getting the stack up**

| Symptom | Where |
|---|---|
| `docker: unknown command: docker compose` | [Prerequisites](KEYCLOAK_BUNDLED.md#prerequisites) — substitute `docker-compose`, or install the v2 plugin |
| `Bind for 0.0.0.0:8080 failed: port is already allocated` | [Ports and collisions](KEYCLOAK_BUNDLED.md#ports-and-collisions) |
| `./scripts/init.sh` says `.env already exists` and changes nothing | Intended. It refuses rather than mint a new keyring over your sealed data. Read the note it prints before deleting anything |
| The API container restarts, or exits at boot with a configuration error | [Fail-fast](../operations/RUNNING.md#3-fail-fast) — the message names the variable |
| `curl localhost:8080/health/ready` returns `503` with `"database":"..."` | [Health: liveness and readiness](../operations/RUNNING.md#4-health-liveness-and-readiness). PostgreSQL is unreachable; the API is up and correctly refusing traffic |
| `curl localhost:8080/health/ready` returns `503` with `"accepting":"draining"` | The process is shutting down. [Shutdown](../operations/RUNNING.md#5-shutdown) |

**Signing in**

| Symptom | Where |
|---|---|
| `invalid issuer` on every token | [The variable that catches everyone](KEYCLOAK_EXISTING.md#the-variable-that-catches-everyone) |
| Console says **"This account cannot use the console"** after a successful login | [What you see if you forget this step](KEYCLOAK_EXISTING.md#what-you-see-if-you-forget-this-step) — the account lacks the realm role `admin` |
| Console says **"Could not confirm your permissions"**, with Retry | Same section. The console could not read the session's roles and refused to guess |

**Connecting a realm**

| Symptom | Where |
|---|---|
| Verify fails, and the message is unclear | [Reading Verify](KEYCLOAK_EXISTING.md#reading-verify) and the [error reference](KEYCLOAK_EXISTING.md#11-error-reference) — it distinguishes unreachable, wrong realm, bad credentials and insufficient privileges |
| `x509: certificate signed by unknown authority` | [TLS and certificates](KEYCLOAK_EXISTING.md#tls-and-certificates) |
| The console shows "This workspace isn't connected yet" | [Activate](FIRST_CREDENTIAL.md#5-activate-the-connection) — a verified connection is still not an active one |
| `workspace_connection_missing` at runtime | [Error reference](KEYCLOAK_EXISTING.md#11-error-reference) |
| `connection_read_only` when writing | Same. The service account has read roles only |
| `provider_credentials_unavailable` | Same. The sealed secret cannot be opened: `SECRETS_KEYRING` changed. [What a missing key does not do](../SECRET_KEY_ROTATION.md#6-what-a-missing-key-does-not-do) |

**Using a credential**

| Symptom | Where |
|---|---|
| A credential gets `401` | Revoked, expired, or mistyped. Revocation is immediate |
| A credential gets `403 insufficient_scope` | [Scope failures](CONNECT_BACKEND.md#insufficient_scope) |
| A credential gets `403` naming a workspace mismatch | The key belongs to a different workspace. The binding is permanent — [PROJECTS.md](../PROJECTS.md) |
| A credential gets `429` | [Rate limiting](CONNECT_BACKEND.md#rate-limiting) |
| I lost a credential secret | [Rotating a credential](FIRST_CREDENTIAL.md#rotating-or-losing-a-credential) — it cannot be recovered |

**Still stuck?** The [30-second check](CONNECT_BACKEND.md#the-30-second-check) is
the fastest way to tell a configuration problem from a code problem: if that
`curl` succeeds and your code does not, the bug is yours.
