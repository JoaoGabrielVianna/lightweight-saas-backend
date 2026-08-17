# Install with the bundled Keycloak

> **Who this is for:** you want to see LIGHTWEIGHT working before committing to
> anything, or you are setting up a development machine. One command, a
> throwaway Keycloak included, no questions asked.
>
> **Who this is not for:** if you already run a Keycloak, use
> [Connect an existing Keycloak](KEYCLOAK_EXISTING.md). It is not much longer
> and it is the path a real installation takes.

---

## What this is, and what it is not

The `dev-idp` Compose profile brings up a **complete evaluation stack**,
including a Keycloak that is pre-seeded with a realm, a client and an operator
account, so that the first thing you see is the product rather than a Keycloak
setup screen.

**This topology is for evaluation and development. It is not supported in
production.** Concretely:

| | Bundled (`--profile dev-idp`) | Production |
|---|---|---|
| Keycloak | started by this Compose file | yours, existing |
| Keycloak credentials | fixed, published in this document | yours |
| Realm contents | imported from `deploy/keycloak/realm-export.json` | yours |
| Keycloak upgrades | pinned to `26.0` here, you own the pin | your operational concern |
| Mail | Mailpit, catches everything, delivers nothing | your SMTP |
| Dev playground | enabled, unauthenticated token tool at `/dev/auth` | must be off |

If you later want to keep the data and move to a real Keycloak, you do not
reinstall: you add a Connection to the real realm, verify it, activate it, and
the old one retires. That is [the same procedure as any connection
replacement](KEYCLOAK_EXISTING.md#replacing-an-active-connection-safely).

---

## Prerequisites

| Tool | Version | Check |
|---|---|---|
| Docker | 24+ | `docker version` |
| Docker Compose | **v2 plugin** | `docker compose version` |
| git | any | `git --version` |
| curl | any | `curl --version` |

No Go toolchain. No `make`. Those are for contributing, not installing.

> **`docker: unknown command: docker compose`** means you have the old
> standalone binary rather than the v2 plugin. Either install the plugin, or
> substitute `docker-compose` (with a hyphen) for `docker compose` in every
> command below. The Compose file itself works with both.

---

## Install

```bash
git clone https://github.com/JoaoGabrielVianna/lightweight-saas-backend.git
cd lightweight-saas-backend

./scripts/init.sh                          # writes .env. Asks nothing.
docker compose --profile dev-idp up -d     # starts everything
curl -fsS localhost:8080/health/ready      # {"status":"ready",...}
```

First boot takes a minute or two: Keycloak imports the realm, and the API waits
for it with a bounded retry rather than crash-looping.

Then open **`http://localhost:8080/admin`**.

---

## Credentials

Everything below is fixed and published, which is exactly why this topology is
not for production.

| What | Where | Username | Password |
|---|---|---|---|
| **LIGHTWEIGHT console** | `http://localhost:8080/admin` | `adminuser` | `password` |
| Keycloak admin console | `http://localhost:8081` | `admin` | see `KEYCLOAK_ADMIN_PASSWORD` in `.env` |
| Mailpit (catches all mail) | `http://localhost:8025` | none | none |

`adminuser` lives in the imported realm and holds the realm role `admin`, which
is what every `/v1` route requires of an operator.

Values that were **generated** rather than published live in `.env`, which
`init.sh` wrote with mode `600`: the secrets keyring, the database password,
the metrics token and the Keycloak admin password. Read them with
`grep '^KEYCLOAK_ADMIN_PASSWORD' .env`.

---

## What actually starts

Five containers, on the `dev-idp` profile:

| Service | Container | Host port | Purpose |
|---|---|---|---|
| `api` | `saas-api` | **8080** | LIGHTWEIGHT itself, and the console at `/admin` |
| `postgres` | `saas-postgres` | **5432** | LIGHTWEIGHT's own database |
| `keycloak` | `saas-keycloak` | **8081** | the throwaway identity provider |
| `keycloak-postgres` | `saas-keycloak-postgres` | **5433** | Keycloak's database |
| `mailpit` | `saas-mailpit` | **8025**, **1025** | catches invitation and reset mail |

Without `--profile dev-idp` you get two: `api` and `postgres`. That is the
self-hosted shape, and it expects a Keycloak you configured
[the other way](KEYCLOAK_EXISTING.md).

---

## Ports and collisions

`Bind for 0.0.0.0:8080 failed: port is already allocated` means something else
on your machine has that port. Every published port is configurable, in `.env`,
without touching the Compose file:

```sh
API_HOST_PORT=18080        # LIGHTWEIGHT              (default 8080)
KC_HOST_PORT=18081         # bundled Keycloak         (default 8081)
POSTGRES_HOST_PORT=15432   # LIGHTWEIGHT's database   (default 5432)
```

Then `docker compose --profile dev-idp up -d` again.

Two things to know when you change `API_HOST_PORT`:

- `PORT` is the port **inside** the container and does not need to change.
- `KEYCLOAK_URL` in `.env` points at `http://localhost:8081`. If you move
  `KC_HOST_PORT`, move `KEYCLOAK_URL` with it, or every token will be rejected
  as `invalid issuer`.

Ports `5433`, `8025` and `1025` are fixed in the Compose file. If one of those
collides, stop the conflicting process; they are only used by the evaluation
profile.

---

## What persists, and what does not

Two named Docker volumes:

| Volume | Holds |
|---|---|
| `postgres_data` | your workspaces, connections, projects, credentials, audit |
| `keycloak_postgres_data` | the bundled Keycloak's realm, users and sessions |

Everything else is disposable. `docker compose down` keeps both volumes;
`docker compose down -v` deletes them, and with them every workspace you made.

The `.env` file is **not** in a volume, is not committed, and holds the secrets
keyring. If you delete it you cannot decrypt the provider credentials in
`postgres_data`, and nothing will tell you until a workspace tries to reach its
realm. Back it up if the installation matters.

---

## Stop, start, upgrade

```bash
docker compose --profile dev-idp stop      # stop, keep data
docker compose --profile dev-idp start     # start again
docker compose --profile dev-idp down      # remove containers, keep volumes
docker compose --profile dev-idp down -v   # remove containers AND all data

docker compose --profile dev-idp logs -f api        # follow the API log
docker compose --profile dev-idp ps                 # what is running
```

**Upgrading LIGHTWEIGHT:**

```bash
git pull
docker compose --profile dev-idp up -d --build
```

Migrations run at boot (`DB_MIGRATE_ON_BOOT=true` by default), and readiness
does not report ready until they are applied. Your `.env` is untouched by
`git pull`, because it is not tracked.

**Upgrading the bundled Keycloak** is your call and your risk: the image is
pinned to `quay.io/keycloak/keycloak:26.0` in `docker-compose.yml`. This is the
evaluation stack, so there is no upgrade path maintained for it. A real
installation upgrades its own Keycloak on its own schedule, which is one of the
reasons the two topologies are separate.

---

## Turning this into something you would keep

If you decide to keep the installation, three things must change before it
faces anything real:

1. **`DEV_PLAYGROUND_ENABLED=false`** in `.env`. It is `true` here, and it
   exposes an unauthenticated token tool at `/dev/auth`.
2. **Replace the Keycloak.** Add a Connection to a real realm, verify, activate.
   The bundled one has a published admin password and a committed realm export.
3. **Read [`../operations/RUNNING.md`](../operations/RUNNING.md).** Backup,
   health probes, shutdown, and the production smoke procedure.

The honest summary: this profile exists so you can judge the product in one
command. It is not a deployment.

---

## Next

**→ [First workspace, connection, project and credential](FIRST_CREDENTIAL.md)**

The bundled Keycloak's realm `saas` is a perfectly good first Connection target.
The confidential client it needs is already in the realm export, so you can
practise the whole flow without opening the Keycloak console at all.
