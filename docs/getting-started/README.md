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

## If you are here to contribute, not to install

[`QUICKSTART.md`](QUICKSTART.md) is the contributor stack: Go toolchain, `make`
targets, the regeneration pipeline and the daily loop. It is not an
installation guide, and `make init` is a fork tool rather than an installer.
Installing uses `./scripts/init.sh`, which asks nothing and needs no Go.

See also [`../../CONTRIBUTING.md`](../../CONTRIBUTING.md) and
[`../QUALITY_GATE.md`](../QUALITY_GATE.md).

---

## Troubleshooting index

| Symptom | Where |
|---|---|
| Port already allocated | [Ports and collisions](KEYCLOAK_BUNDLED.md#ports-and-collisions) |
| `docker: unknown command: docker compose` | [Prerequisites](KEYCLOAK_BUNDLED.md#prerequisites) |
| Verify fails, and the message is unclear | [Reading Verify](KEYCLOAK_EXISTING.md#reading-verify) |
| `invalid issuer` on every token | [The variable that catches everyone](KEYCLOAK_EXISTING.md#the-variable-that-catches-everyone) |
| `x509: certificate signed by unknown authority` | [TLS and certificates](KEYCLOAK_EXISTING.md#tls-and-certificates) |
| The console shows "This workspace isn't connected yet" | [Activate](FIRST_CREDENTIAL.md#5-activate-the-connection) |
| A credential gets `insufficient_scope` | [Scope failures](CONNECT_BACKEND.md#insufficient_scope) |
| A credential gets `429` | [Rate limiting](CONNECT_BACKEND.md#rate-limiting) |
| I lost a credential secret | [Rotating a credential](FIRST_CREDENTIAL.md#rotating-or-losing-a-credential) |
