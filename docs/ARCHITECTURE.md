# Architecture

**Last updated:** 2026-08-16 · Companion to [PROJECT_STATUS.md](PROJECT_STATUS.md)

This document describes how the system is built and how a request travels
through it. It documents the architecture that **exists**, not an aspirational
one.

---

## 0. The shape of the system

Two planes, one binary.

**The control plane** is what an operator manages: workspaces, connections,
projects and credentials. It is local state in PostgreSQL, and it never talks
to an identity provider except to verify a connection.

**The data plane** is what a backend calls: users, roles, sessions,
invitations. It holds no state of its own. Every request on it is routed
through the calling workspace's active connection to a Keycloak realm.

```
                       ┌──────────────── control plane ────────────────┐
                       │  workspace · connection · project · credential │
                       │  PostgreSQL. Operator only, without exception. │
                       └────────────────────┬───────────────────────────┘
                                            │ a workspace's ACTIVE connection
                                            ▼
                       ┌───────────────── data plane ──────────────────┐
                       │  users · roles · sessions · invitations        │
                       │  No local state. Resolved per request.         │
                       └────────────────────┬───────────────────────────┘
                                            ▼
                                     Keycloak realm
```

### The workspace-scoped request path

This is the chain that makes a workspace mean something at request time, and
the one thing to understand before reading any handler:

```
HTTP router                          internal/server/router.go
  ↓
AuthenticatePrincipal                internal/auth        WHO is calling:
                                                          operator token, or lw_sk_ credential
  ↓
RateLimitPerCredential               internal/server      needs the answer above
  ↓
Authorize                            internal/authz       MAY this principal call THIS route:
                                                          route classification + scope,
                                                          or operator role + live admin check
  ↓
Resolver.ForWorkspace                internal/identityruntime
    ├─ workspace must exist and not be archived
    ├─ its ACTIVE connection must exist and be usable
    ├─ the sealed client secret is opened          internal/secrets
    └─ an identity.IdentityProvider is built for that connection's realm
  ↓
identity.IdentityProvider            internal/identity            (the port)
  ↓
Keycloak Admin REST adapter          internal/identity/keycloak   (the adapter)
  ↓
Keycloak realm
```

Three things are deliberately invisible above `identityruntime`: the connection
row, the encryption, and the credential. A handler receives an
`identity.IdentityProvider` and has **no way to ask which realm it points at**.
Isolation between workspaces is structural rather than disciplined: each
connection gets its own provider instance, and every piece of state that could
leak, the service-account token cache above all, is a field on that instance.

### Package roles

| Role | Packages |
|---|---|
| **Composition root** | `cmd/api` |
| **HTTP shell** | `internal/server` |
| **Control-plane features** | `internal/workspace`, `internal/connection`, `internal/project` |
| **Data-plane feature** | `internal/identityruntime` |
| **Audit feature** | `internal/auditlog` (durable, per workspace) |
| **Legacy single-realm** | `internal/identity`, `internal/user` |
| **Ports** | `internal/auth` (AuthProvider, AdminChecker, ProjectAuthenticator), `internal/identity` (IdentityProvider) |
| **Adapters** | `internal/auth/keycloak` (JWKS), `internal/identity/keycloak` (Admin REST) |
| **Cross-cutting** | `internal/authz`, `internal/audit`, `internal/logging`, `internal/requestid`, `internal/metrics` |
| **Platform** | `internal/config`, `internal/database`, `internal/secrets`, `internal/publicid`, `internal/logger` |

Each feature package owns its own `handler.go`, `service.go`, `repository.go`,
`dto.go` and `errors.go`, including a catalogue of stable error codes. There is
no shared `models/` or `services/` package; that is what makes this
package-by-feature rather than package-by-layer with feature-shaped folder
names.

The two surfaces the router mounts:

| Surface | Routes | Auth | Role |
|---|---:|---|---|
| `/v1/*` | 47 | operator token **or** `lw_sk_` credential | the product |
| `/admin/*` | 32 | operator token only | legacy single-realm, plus SMTP and email-template settings for the installation's own realm |

`/admin/*` predates workspaces. It is retained because the console still uses
it for provider settings that have no `/v1` equivalent, and because the
response bodies are a compatibility surface. It is not the product.

---

## 1. Architectural style

**Modular monolith, layered, with dependency inversion at the external
boundaries.**

A single Go module (`github.com/JoaoGabrielVianna/lightweight-saas-backend`,
Go 1.25.4), one deployable binary, 30 packages. Not microservices, not Clean
Architecture, not DDD — and the codebase never claims to be any of them.

What the design actually commits to:

1. **Two ports isolate the outside world.** Keycloak is reachable only through
   two interfaces. Swapping identity providers means writing new adapters, not
   touching handlers.
2. **Layers flow one way.** `handler → service → port → adapter`. No layer
   reaches backwards.
3. **Composition happens in exactly one place.** [cmd/api/main.go](../cmd/api/main.go)
   is the only file that knows how the graph is assembled.
4. **An anti-corruption layer keeps Keycloak's shapes out.** `kcUser`,
   `kcUserSession` and friends never escape `internal/identity/keycloak`.

### Package dependency graph

Derived from `go list -f '{{.Imports}}' ./...`, not maintained by hand.
Platform leaves (`logger`, `publicid`, `database`, `metrics`) are omitted from
the arrows to keep it readable; everything depends on them.

```mermaid
flowchart TD
    main["cmd/api<br/><i>composition root</i>"]
    server["internal/server<br/><i>HTTP shell + routing</i>"]

    subgraph control["control plane"]
      workspace["internal/workspace"]
      conn["internal/connection"]
      project["internal/project"]
    end

    subgraph dataplane["data plane"]
      idrt["internal/identityruntime<br/><i>resolve workspace → provider</i>"]
    end

    identity["internal/identity<br/><i>PORT: IdentityProvider</i>"]
    idkc["internal/identity/keycloak<br/><i>Admin REST adapter</i>"]
    auth["internal/auth<br/><i>PORTS: AuthProvider,<br/>AdminChecker, ProjectAuthenticator</i>"]
    authkc["internal/auth/keycloak<br/><i>JWKS adapter</i>"]
    authz["internal/authz<br/><i>route registry + scopes</i>"]
    auditlog["internal/auditlog<br/><i>durable trail</i>"]
    audit["internal/audit<br/><i>emit port</i>"]
    user["internal/user"]
    secretsPkg["internal/secrets<br/><i>AES-256-GCM keyring</i>"]
    logging["internal/logging"]
    cfg["internal/config"]

    main --> server
    main --> auth
    main --> authkc
    main --> cfg
    main --> logging
    main --> auditlog

    server --> workspace
    server --> conn
    server --> project
    server --> idrt
    server --> auditlog
    server --> authz
    server --> identity
    server --> idkc
    server --> user
    server --> auth
    server --> cfg

    conn --> workspace
    conn --> secretsPkg
    project --> workspace
    project --> authz

    idrt --> conn
    idrt --> workspace
    idrt --> identity
    idrt --> idkc
    idrt --> secretsPkg

    authz --> auth
    authz --> idrt

    identity --> auth
    idkc --> identity
    authkc --> auth

    workspace --> audit
    conn --> audit
    project --> audit
    auditlog --> audit
    logging --> audit
    logging --> auth

    classDef port fill:#1f6feb,stroke:#1f6feb,color:#fff
    class auth,identity,audit port
```

Two edges are worth explaining, because both look backwards at first glance:

- **`authz → identityruntime`** exists so the authorization registry can be
  validated against the route list `identityruntime` declares. The direction is
  deliberate and must not invert: `identityruntime` must not know how it is
  authorized, or the write guard and the capability check would become two
  halves of one tangled rule.
- **`project → authz`** is for the scope vocabulary. A scope is an
  authorization concept, so `authz` owns it and the feature consumes it.

There is no `auth → identity` edge, and there must not be. The one place that
needed the opposite direction, the live-admin check, is resolved with an
adapter in the composition root rather than an import
([server.go](../internal/server/server.go), `adminCheckerFromProvider`):

```go
// Adapts identity.IdentityProvider → auth.AdminChecker without an
// auth→identity import cycle. Both packages already depend on this layer.
func adminCheckerFromProvider(p identity.IdentityProvider) auth.AdminChecker {
	return auth.AdminCheckerFunc(func(ctx context.Context, subject string) (bool, error) {
		roles, err := p.ListUserRoles(ctx, subject)
		...
	})
}
```

`identity → auth`, never the reverse. Handlers call `auth.IdentityFrom(c)` to
read the authenticated principal.

---

## 2. The two ports

Everything provider-specific sits behind these interfaces.

### `auth.AuthProvider` — request-path token validation

[internal/auth/provider.go](../internal/auth/provider.go)

```go
type AuthProvider interface {
	ValidateToken(ctx context.Context, raw string) (*Identity, error)
}
```

One method, called on every authenticated request. Implemented by
[`auth/keycloak.Provider`](../internal/auth/keycloak/provider.go) against the
realm JWKS.

### `identity.IdentityProvider` — admin operations

[internal/identity/provider.go](../internal/identity/provider.go)

23 methods across users, roles, sessions and invitations. Implemented by
[`identity/keycloak.Provider`](../internal/identity/keycloak/provider.go)
against the Keycloak Admin REST API.

The interface carries an explicit non-growth policy in its doc comment:
*"Adding a method here is a breaking change for every implementation — keep
the surface curated."*

**The two use different Keycloak clients with independent secrets**
(`KEYCLOAK_CLIENT_ID` vs `KEYCLOAK_ADMIN_CLIENT_ID`) so the low-privilege
token validator and the privileged service account never share a credential.

---

## 3. Layers and responsibilities

| Layer | Location | Owns | Must not |
|---|---|---|---|
| **Composition root** | [cmd/api/main.go](../cmd/api/main.go) | Building every dependency; fail-fast on misconfiguration | Contain business logic |
| **HTTP shell** | [internal/server/](../internal/server/) | Gin engine, route groups, middleware order, static asset mounting | Know Keycloak specifics |
| **Handler** | `*/handler.go` | Binding, request-shape validation, error → HTTP status, building the audit event from the request | Contain business rules; own a transaction |
| **Service** | `*/service.go` | Business rules: validation, normalization, self-protection guards, pagination clamping. For control-plane mutations, the TRANSACTION BOUNDARY | Know about HTTP or Gin |
| **Port** | `provider.go` interfaces | The contract | Have an implementation |
| **Adapter** | `*/keycloak/`, `repository.go` | Talking to the external system, translating its shapes | Leak external types upward |

The separation is real, not nominal. Example — the self-delete guard lives in
the service, not the handler ([identity/service.go](../internal/identity/service.go)):

```go
func (s *Service) DeleteUser(ctx context.Context, callerSubject, targetID string) error {
	if !uuidPattern.MatchString(targetID) { return ErrBadRequest }
	if callerSubject != "" && callerSubject == targetID {
		return fmt.Errorf("%w: cannot delete your own account", ErrForbidden)
	}
	...
}
```

The handler's only job is turning `ErrForbidden` into a 403.

---

## 4. Bootstrap and startup

Startup is deliberately fail-fast: every misconfiguration that can be detected
without traffic terminates the process before the listener opens.

```mermaid
sequenceDiagram
    participant M as cmd/api/main
    participant C as config
    participant DB as database
    participant KC as Keycloak
    participant S as server

    M->>C: LoadConfig()
    C->>C: read .env + env vars
    C->>C: Validate() — log.Fatal on missing required var
    M->>DB: Connect(cfg.DBUrl, WithMigrations(cfg.DBMigrateOnBoot))
    DB->>DB: apply embedded SQL migrations — Fatal on failure
    M->>KC: keycloak.NewProvider() → blocking JWKS fetch
    KC-->>M: Fatal if unreachable
    M->>M: auth.SetEventHook(authEventLogger)
    M->>M: logging.WireDefaultWithMemory(500)
    M->>S: SetupIdentity(cfg)
    Note over S: returns (nil,nil,nil,nil) when admin<br/>client unset → /admin/* not mounted.<br/>Error only on half-configured client.
    M->>S: NewServer(db, cfg) → Gin engine + CORS
    M->>S: SetupRoutes(...)
    M->>S: Start(port) — blocks
```

Three distinct failure policies, chosen per case:

| Condition | Behaviour | Why |
|---|---|---|
| Missing `DB_URL`, `KEYCLOAK_URL`, `KEYCLOAK_REALM`, `KEYCLOAK_CLIENT_ID` | `log.Fatal` | Serving traffic with a half-configured auth stack is worse than not starting |
| JWKS unreachable at boot | `log.Fatal` | Surfacing this at boot beats serving 401s in production |
| Admin client credentials **both** unset | Warn, continue, omit `/admin/*` | Valid deployment shape: auth-only, no admin surface |
| Admin client **half** set (id without secret) | Return error → `log.Fatal` | Always an operator mistake |

### Configuration source of truth

[internal/bootstrap](../internal/bootstrap/) generates both `.env` and the
Keycloak realm export from `config/project.json`, so the API's configuration
and the realm's configuration cannot drift apart.

```mermaid
flowchart LR
    PJ["config/project.json<br/><i>source of truth</i>"]
    PJ -->|generate.go| ENV[".env"]
    PJ -->|generate.go| RE["deploy/keycloak/<br/>realm-export.json"]
    ENV --> API["API process"]
    RE --> KC["Keycloak<br/>--import-realm"]
```

Validated against a JSON Schema in
[internal/bootstrap/schema/](../internal/bootstrap/schema/).

---

## 5. Request lifecycle

### Middleware chain by route group

```mermaid
flowchart TD
    REQ([HTTP request]) --> GIN[Gin engine]
    GIN --> CORS{CORS configured?}
    CORS -->|yes| CORSMW[cors middleware]
    CORS -->|no| ROUTE
    CORSMW --> ROUTE{route group}

    ROUTE -->|"/health, /swagger, /,<br/>/admin shell, /dev/auth"| PUB[no auth] --> H1([handler])

    ROUTE -->|"/me, /auth/debug"| RA1[RequireAuth] --> H2([handler])

    ROUTE -->|"/admin/*"| RL[RateLimitPerIP<br/>10 req/s, burst 20]
    RL --> RA2[RequireAuth]
    RA2 --> RR[RequireRole admin]
    RR --> RLA[RequireLiveAdmin]
    RLA --> H3([handler])

    RL -.->|over limit| E429[429]
    RA2 -.->|invalid token| E401[401]
    RR -.->|claim missing role| E403[403]
    RLA -.->|role revoked server-side| E403b[403]
    RLA -.->|Keycloak unreachable| E503[503 fail closed]
```

**The middleware order is load-bearing:**

- **Rate limit sits before auth** so an unauthenticated flood cannot force
  JWT signature verification. Cheapest check first.
- **`RequireRole` sits before `RequireLiveAdmin`** so a non-admin is rejected
  from the JWT claim alone, with no Keycloak round-trip. The expensive live
  check only runs for tokens that claim they *should* pass.
- **One gate at the group level** rather than per-route. There is no route
  where someone can forget to add `RequireRole`.

### A mutating admin request, end to end

`DELETE /admin/users/:id`:

```mermaid
sequenceDiagram
    actor A as Admin (SPA)
    participant G as Gin
    participant RL as RateLimitPerIP
    participant AU as RequireAuth
    participant RR as RequireRole
    participant LA as RequireLiveAdmin
    participant H as identity.Handler
    participant S as identity.Service
    participant P as keycloak.Provider
    participant K as Keycloak
    participant AD as audit

    A->>G: DELETE /admin/users/:id + Bearer
    G->>RL: token bucket for client IP
    RL->>AU: allowed
    AU->>AU: ValidateToken — JWKS, iss, azp, exp
    AU->>AU: StoreIdentity(c, id) + emit AuthEvent
    AU->>RR: identity in context
    RR->>RR: id.HasRole("admin")
    RR->>LA: claim ok
    LA->>LA: cache hit? (TTL 30s)
    alt cache miss
        LA->>K: GET /users/{sub}/role-mappings/realm
        K-->>LA: roles
    end
    LA->>H: live admin confirmed
    H->>S: DeleteUser(ctx, callerSub, targetID)
    S->>S: uuid format · no self-delete · not last admin
    S->>P: DeleteUser(ctx, targetID)
    P->>K: DELETE /users/{id}
    K-->>P: 204
    P-->>S: nil
    S-->>H: nil
    H->>AD: RecordMutation(user.deleted, target, err)
    H->>LA: adminInvalidator.Invalidate(targetID)
    H-->>A: 204 No Content
```

Two details worth internalising:

1. **The audit event is emitted on both success and failure.**
   [`logging.RecordMutation`](../internal/logging/gin_helpers.go) centralises
   the branch: on error it attaches `err.Error()` as `Reason`. Exactly one
   event per mutation attempt, always.

   **Control-plane mutations differ, and the difference is the point** (Slice 15,
   [TD-033](TECH_DEBT.md#td-033)). Their domain rows and their audit row live in
   the same PostgreSQL, so the SERVICE opens one transaction and writes both:

   ```
   handler   builds the event from the request (who, from where, request id)
   service   BEGIN → domain write → complete the event → audit insert → COMMIT
   handler   emits to the log and the ring; the durable row is already in
   ```

   A failed audit write rolls the mutation back. The handler's job shrinks to
   building the event and deciding what to do with the outcome — and the third
   outcome is the subtle one: when the mutation was rolled back BECAUSE the audit
   store failed, nothing is emitted, because the store that would receive it is
   the one that just failed.

   Provider mutations keep the original shape. A Keycloak write cannot be rolled
   back by a PostgreSQL transaction, so their audit row is best-effort and the
   response still succeeds — see [AUDIT.md §6](AUDIT.md#6-when-the-audit-write-fails)
   and [TD-038](TECH_DEBT.md#td-038).
2. **Cache invalidation is in-band.** Mutations that could change admin status
   call `Invalidate(subject)` immediately, so the 30 s TTL only ever bounds
   *out-of-band* changes made directly in the Keycloak admin UI.

---

## 6. Authentication

```mermaid
sequenceDiagram
    actor U as User
    participant SPA as /admin SPA
    participant KC as Keycloak
    participant API as API

    U->>SPA: open /admin
    SPA->>API: GET /admin/config.json
    API-->>SPA: keycloakUrl, realm, clientId, redirectUri
    SPA->>KC: Authorization Code + PKCE redirect
    U->>KC: credentials
    KC-->>SPA: code
    SPA->>KC: exchange code + verifier
    KC-->>SPA: access_token (JWT)
    SPA->>API: GET /auth/debug + Bearer
    API-->>SPA: {valid, issuer, allowed_clients, roles, ...}
    SPA->>API: GET /admin/users + Bearer
```

Validation steps in
[`auth/keycloak.Provider.ValidateToken`](../internal/auth/keycloak/provider.go):

| # | Check | Failure |
|---|---|---|
| 1 | Signature verified against realm JWKS | `ErrInvalidToken` |
| 2 | Algorithm ∈ `{RS,PS,ES}{256,384,512}` — **`HS*` excluded** | `ErrInvalidToken` |
| 3 | `iss` equals the configured realm issuer | `ErrInvalidToken` |
| 4 | `exp` present and in the future | `ErrTokenExpired` |
| 5 | `azp`, if present, is in the allow-list | `ErrInvalidToken` |
| 6 | `sub` present | `ErrMissingClaim` |

Roles are flattened from `realm_access.roles` and
`resource_access.<clientID>.roles` into `Identity.Roles`.

**Why `HS*` is excluded** — with a symmetric algorithm the verification key is
also the signing key, so anyone who can read it can mint tokens. Keycloak
always publishes asymmetric keys, so the restriction costs nothing.

**Error responses never leak the reason.** The wire gets
`401 {"error":"unauthorized"}`; the specific cause goes to the `AuthEvent`
stream only.

### Token introspection — two routes, one handler

| Route | Auth | Mounted | Purpose |
|---|---|---|---|
| `GET /auth/debug` | Required | Always, by `SetupRoutes` | Consumed by the admin console: `valid`, `issuer`, `allowed_clients`, `roles` |
| `GET /dev/auth/debug` | **None** | `DEV_PLAYGROUND_ENABLED=true` | Explains *why* a token was rejected |

The split exists because an authenticated route cannot diagnose an invalid
token — `RequireAuth` rejects it before the handler runs. Collapsing them into
one path caused [KI-001](KNOWN_ISSUES.md#ki-001).

---

## 7. Authorization

Four independent mechanisms, layered:

```mermaid
flowchart TD
    R([/admin/* request]) --> L1["1 · RequireAuth<br/>valid token?"]
    L1 -->|no| D401[401]
    L1 -->|yes| L2["2 · RequireRole admin<br/>JWT claim — no network"]
    L2 -->|no| D403[403]
    L2 -->|yes| L3["3 · RequireLiveAdmin<br/>ask Keycloak, 30s cache"]
    L3 -->|error| D503[503 — fail closed]
    L3 -->|not admin| D403b[403 — stale token]
    L3 -->|admin| L4["4 · Service guards<br/>self-action · last-admin · reserved names"]
    L4 -->|violated| D403c[403]
    L4 -->|ok| OK([execute])
```

### Layer 3 — why it exists

`RequireRole` reads a claim that was frozen when the token was signed. Revoke
someone's admin role and their existing token stays privileged until `exp` —
up to an hour in a default realm. That was **GAP-1**.

[`CachedAdminChecker`](../internal/auth/admin_check.go) closes it:

- Positive **and** negative results cached, same TTL (30 s default,
  `ADMIN_LIVE_CHECK_TTL_SECONDS`). Negative caching stops a stale-admin token
  from triggering one Keycloak call per request.
- **On upstream error it returns the error, never a cached or claim-derived
  guess.** `RequireLiveAdmin` maps that to **503**. Fail closed is the whole
  point of the layer.
- In-band mutations invalidate immediately, so the TTL only bounds changes
  made directly in Keycloak's own admin UI.

### Layer 4 — business guards

In [identity/service.go](../internal/identity/service.go):

| Guard | Rule |
|---|---|
| Self-disable | `PATCH {enabled:false}` on yourself → 403 |
| Self-delete | `DELETE` your own user → 403 |
| Self-strip-admin | Removing `admin` from yourself → 403 |
| Last admin | Any operation emptying the enabled-admin set → 403 |
| Reserved roles | Cannot create/modify `admin`, `user`, `offline_access`, `uma_authorization`, `default-roles-*` |

`assertNotLastAdmin` fails closed: if it cannot enumerate admins because the
Admin API is unavailable, it refuses the operation rather than risk wiping the
last one.

---

## 8. Data layer

```mermaid
flowchart LR
    subgraph app["Application Postgres"]
        U["users<br/>id · keycloak_sub UNIQUE<br/>email · username<br/>created_at · updated_at"]
        W["workspaces<br/>id uuid · slug UNIQUE<br/>name · status<br/>created_at · updated_at · archived_at"]
        C["connections<br/>id uuid · workspace_id FK<br/>provider · status · base_url · realm<br/>secret_ciphertext · secret_nonce<br/>health · access_mode"]
        P["projects<br/>id uuid · workspace_id FK<br/>name · status<br/>created_at · updated_at · archived_at"]
        PC["project_credentials<br/>id uuid · project_id FK<br/>key_prefix · key_hash · key_hash_alg<br/>scopes text[] · expires_at · revoked_at<br/>created_by · last_used_at"]
        AE["audit_events<br/>id uuid · workspace_id FK NULL<br/>event_type · outcome · actor_*<br/>resource_* · request_id · reason_code<br/>metadata jsonb · occurred_at"]
    end
    subgraph kcdb["Keycloak Postgres"]
        K["users · credentials · roles<br/>sessions · attributes<br/>role_mappings"]
    end
    API["API"] -->|GORM| U
    API -->|GORM| W
    API -->|GORM| C
    API -->|GORM| P
    API -->|GORM| PC
    API -->|GORM| AE
    C -.->|workspace_id| W
    P -.->|workspace_id| W
    PC -.->|project_id| P
    AE -.->|workspace_id| W
    API -->|Admin REST| KCS["Keycloak"] --> K
    U -.->|keycloak_sub| K
```

The service owns **6 tables**, and none holds identity data: everything
identity-related lives in Keycloak and is reachable only over HTTP. They arrive
in six versioned migrations, `000001_baseline` through `000006_audit_events`.

`connections` holds each workspace's identity-provider configuration. The
provider's client secret is sealed with AES-256-GCM before it is written and is
never returned by the API; at most one connection per workspace may be active,
enforced by a partial unique index. **A connection is what a `/v1` identity
request routes through**: `internal/identityruntime` resolves the calling
workspace's active connection per request and builds a provider from it, so two
workspaces on two realms are served by one process. See
[CONNECTIONS.md](CONNECTIONS.md) and
[WORKSPACE_IDENTITY_RUNTIME.md](WORKSPACE_IDENTITY_RUNTIME.md). Legacy
`/admin/*` still uses the process-level `KEYCLOAK_*` configuration instead, and
that split is [TD-022](TECH_DEBT.md#td-022).

`workspaces` is the root of the product domain: `connections`, `projects` and
`audit_events` all reference it, each `ON DELETE RESTRICT`, because workspaces
are archived and never deleted. There is no foreign key between it and `users` —
a Workspace holds a Connection to an identity provider rather than a set of
people. Its invariants are enforced by four CHECK constraints rather than by
application code alone; see [WORKSPACES.md](WORKSPACES.md).

`projects` and `project_credentials` are the machine-authentication half.
`project_credentials.key_hash` is a SHA-256 **digest**, not a sealed value:
unlike `connections.secret_ciphertext` there is no operation that needs the
secret back. `projects.workspace_id` is the authorization boundary itself, and
is compared against the workspace in the request path before any workspace,
connection or provider is touched. See [PROJECTS.md](PROJECTS.md).

`audit_events` is the durable, workspace-scoped trail. `workspace_id` is
nullable and NULL means "not workspace-scoped", which is how the legacy
`/admin/*` surface records; the workspace audit API filters on
`workspace_id = $1`, so those rows are unreachable through it by construction.
Control-plane mutations write their row in the same transaction as the change
([TD-033](TECH_DEBT.md#td-033)); provider mutations cannot, and that is
[TD-038](TECH_DEBT.md#td-038). See [AUDIT.md](AUDIT.md).

- **`keycloak_sub` is the canonical key**, with a unique index enforcing it at
  the database level even under concurrent first-login races.
- **`FindByEmail` deliberately does not exist** — email is not a stable
  identity in OIDC; users can change it in Keycloak.
- Schema changes go through **versioned SQL migrations** embedded in the binary
  (`go:embed`) and applied at boot via `golang-migrate`. `AutoMigrate` is gone
  ([TD-005](TECH_DEBT.md#td-005), resolved) — see [MIGRATIONS.md](MIGRATIONS.md).

---

## 9. Audit subsystem

Provider-agnostic by construction: `internal/audit` imports neither Gin nor
Keycloak.

```mermaid
flowchart LR
    H["identity.Handler<br/><i>15 mutation sites</i>"] --> RM["logging.RecordMutation"]
    RM --> EV["audit.Event<br/>who · action · target<br/>ts · ip · reason"]
    EV --> REC["audit.Record"]
    REC --> MULTI["multi recorder"]
    MULTI --> SINK1["AuditSink<br/><i>structured log — durable</i>"]
    MULTI --> SINK2["MemoryRecorder<br/><i>ring buffer, 500 — volatile</i>"]
    SINK2 --> EP["GET /admin/audit-events"]
```

- **14 canonical actions**, all declared in
  [audit/event.go](../internal/audit/event.go) and all emitted. Declared count
  and emitted count match — verify with the commands in
  [PROJECT_STATUS.md](PROJECT_STATUS.md#metrics).
- The active recorder sits behind an `atomic.Pointer`, so `SetDefault` is safe
  concurrently with in-flight `Record` calls.
- A no-op recorder is installed at package init, so `audit.Record` is always
  safe to call — even before wiring, and in tests.
- **The ring buffer is a recency window, not history.** It is volatile and
  capped. The structured log is the durable trail. See
  [TD-008](TECH_DEBT.md#td-008).

---

## 10. Observability hooks

Two extension points exist and are currently wired only to the logger:

| Hook | Set in | Currently does | Designed for |
|---|---|---|---|
| [`auth.SetEventHook`](../internal/auth/events.go) | [main.go](../cmd/api/main.go) | Writes to the structured logger | Prometheus counters, OTel spans |
| [`audit.SetDefault`](../internal/audit/recorder.go) | [main.go](../cmd/api/main.go) | Fan-out: log sink + ring buffer | Database sink, SIEM shipper |

`AuthEvent` kinds: `token_validated`, `validation_failed`, `missing_header`,
`malformed_header`, `forbidden`. Denials from the live-admin check carry the
marker `"live admin check denied"` so stale-token rejections are
distinguishable from ordinary RBAC denials.

Adding metrics means writing a collector and registering it at these two
points — no middleware changes. See [TD-009](TECH_DEBT.md#td-009).

---

## 11. Frontend architecture

Two separate surfaces, both dependency-free vanilla JavaScript with **no build
step** — served straight from disk.

### Admin console — [web/admin/](../web/admin/)

```
static/js/
├── main.js            boot: router, PKCE callback, state hydration
├── lib/
│   ├── auth.js        PKCE flow, token storage, refreshDebug()
│   ├── api.js         fetch wrapper + 401 interceptor
│   ├── router.js      hash-based client-side routing
│   ├── state.js       minimal observable store
│   ├── markdown.js    docs renderer
│   ├── highlight.js   syntax highlighting
│   ├── locale.js      EN / PT-BR
│   └── dom.js         h() hyperscript helper
├── components/        sidebar · topbar · table · modal · toast · common
└── views/             14 views: users · user-detail · roles · sessions ·
                       overview · settings · email · email-templates ·
                       auditlogs · docs · swagger · playground · apiexplorer
```

The SPA depends on a **response contract** from `GET /auth/debug` —
`valid`, `issuer`, `allowed_clients`, `received_sub`, `received_azp`, `roles`,
`exp`, `expired`. Breaking it degrades the console silently rather than
loudly: this is exactly how [KI-001](KNOWN_ISSUES.md#ki-001)'s second symptom
went unnoticed. The contract is now pinned by
`TestAuthDebug_ReturnsSPAContract` in
[internal/server/server_test.go](../internal/server/server_test.go).

`devTools` and `apiExplorer` in `/admin/config.json` are bound to
`DEV_PLAYGROUND_ENABLED`, letting production serve the console while hiding
the dev-only views.

### Dev playground — [web/dev/](../web/dev/)

Standalone six-section token debugger. **Rule enforced in its own source:** the
UI must never decide token validity itself — every `valid` / `expired` /
`roles` value rendered comes from `/dev/auth/debug`.

---

## 12. Deployment topology

```mermaid
flowchart TD
    subgraph compose["docker-compose — 5 services"]
        API["api :8080<br/><i>Go, distroless-ish alpine, non-root</i>"]
        PG[("postgres :5432<br/>application data")]
        KCPG[("keycloak-postgres :5433<br/>identity data")]
        KC["keycloak :8081<br/><i>--import-realm</i>"]
        MP["mailpit :8025/:1025<br/><i>dev SMTP catch-all</i>"]
    end
    API --> PG
    API -->|Admin REST + JWKS| KC
    KC --> KCPG
    KC -->|SMTP| MP
```

**Two hostnames for one Keycloak, deliberately:**

| Variable | Value in compose | Used for |
|---|---|---|
| `KEYCLOAK_URL` | `http://localhost:8081` | Expected `iss` claim — must match what browsers use to obtain tokens |
| `KEYCLOAK_JWKS_URL` / `KEYCLOAK_ADMIN_BASE_URL` | `http://keycloak:8080` | Server-to-server calls over the Docker network |

Getting these backwards is the single most common setup failure: tokens
minted at `localhost:8081` carry that issuer, and the API must expect exactly
that string.

**Dockerfile:** multi-stage, `CGO_ENABLED=0`, `-trimpath -ldflags="-s -w"`,
build cache mounts, runs as a non-root `app` user.

**Known gap:** the compose `api` service does not pass
`ADMIN_CONSOLE_ENABLED`, `ADMIN_CONSOLE_CLIENT_ID`, `CORS_ALLOWED_ORIGINS`, or
`ADMIN_LIVE_CHECK_TTL_SECONDS`, so the documented production recipe is not
reproducible with the shipped file — [TD-004](TECH_DEBT.md#td-004).

---

## 13. Extension guide

Where to add things, so the next change lands in the right layer.

| To add… | Do this |
|---|---|
| **A new admin endpoint** | Method on `IdentityProvider` → implement in `identity/keycloak` → business rules in `Service` → handler in `identity/handler.go` → route in [router.go](../internal/server/router.go) → `logging.RecordMutation` if it mutates → swagger annotation → `make docs` |
| **A new audit action** | Constant in [audit/event.go](../internal/audit/event.go) → emit via `RecordMutation`. Never rename an existing one — consumers depend on the string |
| **A different identity provider** | Implement both ports in a new subpackage. Do not touch handlers or services |
| **Metrics / tracing** | Register collectors at `auth.SetEventHook` and `audit.SetDefault`. No middleware changes needed |
| **A new business entity** | New package under `internal/` following `model → repository → service → handler`. **Resolve multi-tenancy first** — retrofitting `tenant_id` later touches every query |
| **A new config value** | Field on `Config` → read in `LoadConfig` → add to `Validate()` if required → **add to docker-compose `api.environment`** (see TD-004) → document in `.env.example` |

### Invariants — do not break these

1. `internal/audit` must not import Gin, Keycloak, or any transport package.
2. `internal/auth` must not import `internal/identity`. Use the
   `AdminChecker` seam in the composition root.
3. Adapters must not leak provider types (`kcUser`, …) above the port.
4. Every mutation handler emits exactly one audit event, on success **and**
   failure.
5. `RequireLiveAdmin` fails closed. Never add a fallback to the JWT claim.
6. `/auth/debug` is registered in exactly one place. A second registration
   panics Gin at boot — pinned by a test.
