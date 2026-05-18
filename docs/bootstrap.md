# Bootstrap

The bootstrap layer turns a new clone of this repository into a working stack
with one command, and keeps the local environment, Keycloak realm, and Docker
configuration in lockstep with a single source of truth.

```
git clone
      |
      v
make init                    <- interactive prompts (or use -non-interactive)
      |
      v
config/project.json updated  <- canonical project description
      |
      v
.env, .env.example, realm-export.json regenerated
      |
      v
make up                      <- starts postgres + keycloak + api
```

## Source of truth

[config/project.json](../config/project.json) is the single, versioned
description of the project. It captures:

| Field                   | Used by                                 |
|-------------------------|-----------------------------------------|
| `project.name`          | DB name, Keycloak `displayName`, Docker container names |
| `project.environment`   | Future: gates production-only behavior  |
| `auth.provider`         | Selects which generator templates apply (today: `keycloak`) |
| `auth.realm`            | Keycloak realm + .env `KEYCLOAK_REALM`  |
| `auth.client.{id,secret}` | Keycloak client + .env `KEYCLOAK_CLIENT_*` |
| `auth.roles`            | Realm roles in `realm-export.json`     |
| `ports.*`               | Docker host port mappings + .env URLs   |
| `features.*`            | Feature flags consumed by generators (e.g. `seed_users`) |
| `seed_users`            | Pre-baked Keycloak users for local dev  |

Secrets that must never enter version control (admin password, end-user
passwords beyond seeds) stay in `.env` only. `.env` is gitignored;
`.env.example` is committed with placeholder values.

## Commands

```
make init        # interactive: prompts -> writes config/project.json -> regenerates
make regen       # non-interactive: re-runs generators against current config
go run ./cmd/bootstrap -non-interactive   # equivalent to `make regen`
```

Flags:

```
-config <path>          # alternate source-of-truth file (default: config/project.json)
-non-interactive        # skip prompts
-admin-password <val>   # Keycloak admin password to inject (non-interactive only)
```

## Stack lifecycle commands

Once config and env are generated, these are the Makefile targets you'll
use day-to-day. Pick by intent, not by force-of-habit — they sit on a
preservation spectrum:

```
                preserves data?
                |
+----------+    |    +-----------+
| make stop|--- Y -- | make start|     <- pause/resume; nothing changes
+----------+         +-----------+

+----------+
| make down|--- Y         <- remove containers, KEEP volumes (data survives `make up`)
+----------+

+-----------+
| make purge|--- N         <- WIPE everything: containers, volumes, networks, bin/, api image
+-----------+              (prompts y/N first; aborts safely on anything else)

+--------------+
| make reset-dev|-- N      <- one-command recovery: purge + rebuild + start in one shot
+--------------+           (prompts y/N first; same teeth as purge)
```

| Command          | Containers      | Volumes / Data  | API image | bin/  | Use when                                                  |
|------------------|-----------------|-----------------|-----------|-------|-----------------------------------------------------------|
| `make stop`      | stopped, kept   | preserved       | kept      | kept  | Pause work for the day; want to resume quickly tomorrow.  |
| `make start`     | resumed         | preserved       | kept      | kept  | Resume from `make stop`.                                  |
| `make down`      | removed         | preserved       | kept      | kept  | Clean container/network state but keep the DB rows you care about. |
| `make up`        | created         | preserved       | rebuilt   | kept  | Bring the stack up (works from any state).                |
| `make purge`     | removed         | **DELETED**     | removed   | removed | You want a guaranteed-clean baseline. Asks `y/N` first. |
| `make reset-dev` | removed → rebuilt | **DELETED** → fresh | rebuilt | removed → rebuilt | One-command rescue when Keycloak is wedged, JWKS is stale, a migration is broken, or a volume is corrupted. |

Notes:

- `purge` and `reset-dev` both show a `⚠️ This will DELETE all local data...`
  prompt and abort on anything other than `y` / `yes` / `Y` / `YES`.
- `reset-dev` is functionally `purge && make up`, but it only prompts
  once — internally it calls a private `_purge-run` target that skips
  the second prompt.
- After `reset-dev`, the Go API image is rebuilt from source (`docker-compose up --build`),
  so picks up any uncommitted code changes.

## Files generated (today)

| Target                                  | Generator                       |
|-----------------------------------------|---------------------------------|
| `.env`                                  | `bootstrap.writeEnv`            |
| `.env.example`                          | `bootstrap.writeEnv` (annotated)|
| `deploy/keycloak/realm-export.json`     | `bootstrap.writeRealmExport`    |

## Files NOT generated yet (planned)

Honest TODO list — these are designed for but not implemented in this iteration:

| Target                                  | Why deferred                    |
|-----------------------------------------|---------------------------------|
| `docker-compose.override.yml`           | Override layer needs careful merge semantics with the base file. Will be added when environment != local needs to override volumes/ports. |
| `README.md` quickstart snippet          | README is hand-curated; needs a clearly demarcated `<!-- bootstrap:start -->...<!-- bootstrap:end -->` block before generation can be safely automated. |
| Frontend `.env`                         | No frontend in this repo yet; will land alongside frontend scaffolding. |

Each will be added as a new function in [internal/bootstrap/generate.go](../internal/bootstrap/generate.go).
Adding a new generated file is a 3-step change:
1. Add a write function.
2. Wire it into `GenerateAll`.
3. Add it to this table.

## Customization

To add a new field to the project config:

1. Add the field to the relevant struct in [internal/bootstrap/config.go](../internal/bootstrap/config.go).
2. If it must be prompted, add an `out.X = p.ask(...)` line in [internal/bootstrap/prompt.go](../internal/bootstrap/prompt.go).
3. Consume the field in the generator(s) that need it.
4. Bump [config/project.json](../config/project.json) with the new default.

## Regeneration semantics

- Generators **overwrite** their target files. Hand-editing a generated file
  and then running `make regen` will lose those edits. Edit
  `config/project.json` instead and regenerate.
- The `.env` working file is overwritten too — including the
  `KEYCLOAK_ADMIN_PASSWORD`. Use `-admin-password` in non-interactive mode to
  preserve a chosen value across regenerations, or set it once via your secret
  store in non-local environments.
- `realm-export.json` is only re-imported by Keycloak on container startup
  with `--import-realm`. After `make regen`, run `make keycloak-import` (which
  restarts Keycloak) or `make realm-reset` (which wipes Keycloak's DB so the
  fresh realm is honored end-to-end).

## Migration path

When extending the bootstrap to a new auth provider (Auth0, Supabase, Clerk):

1. Add a discriminator value: `auth.provider = "auth0"` becomes valid in `Validate()`.
2. Add a new generator package `internal/auth/auth0/` mirroring the keycloak one.
3. Add a generator dispatch in `bootstrap.GenerateAll` that selects the right
   generator set based on `cfg.Auth.Provider`.
4. Add a corresponding realm/tenant export generator if the provider supports
   declarative tenant setup; otherwise emit a setup runbook.

The `auth.AuthProvider` interface is provider-agnostic; the new provider only
implements `ValidateToken` and wires itself into `main.go`. No business code
changes.

## Validation strategy

After every regeneration the bootstrap CLI:

1. Parses `config/project.json` and runs `ProjectConfig.Validate()` (fails on
   missing required fields).
2. Generates each file. JSON outputs are produced via `encoding/json` so
   syntax errors are impossible by construction.
3. Exits non-zero on any error — never half-writes.

For end-to-end validation (Keycloak boots, API serves `/me`), use:

```
make up        # bring the stack up
make auth-test # acquires a token via Direct Access Grants and calls /me
make e2e       # combines the above with readiness waits
```

## Anti-patterns to avoid

- Editing `.env`, `.env.example`, or `realm-export.json` by hand. Run
  `make regen` instead.
- Committing real secrets to `config/project.json`. The `auth.client.secret`
  there is a DEV-ONLY default; production secrets belong in a secret store.
- Coupling new generators to Keycloak. Generators should branch on
  `cfg.Auth.Provider` and stay portable.
