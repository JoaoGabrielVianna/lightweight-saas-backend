# Projects and machine credentials

**Scope:** how a backend that is not a human operator authenticates to this API,
what it may do, and what confines it.

**Status:** shipped 2026-08-09. Packages
[`internal/project`](../internal/project/) and
[`internal/authz`](../internal/authz/).

The identity operations a credential reaches are
[WORKSPACE_IDENTITY_API.md](WORKSPACE_IDENTITY_API.md). The console that issues
credentials is [WORKSPACE_CONSOLE.md](WORKSPACE_CONSOLE.md).

---

## 1. The question this answers

Before this slice, every caller of `/v1` was a console operator holding a
Keycloak token with the realm `admin` role. That is the wrong shape for the
product's actual consumer: a backend, running unattended, that should reach one
workspace and only the operations it was granted.

```text
external backend
      ↓  Authorization: Bearer lw_sk_…
Project Credential
      ↓  workspace binding
Workspace
      ↓  active connection
Correct Keycloak realm
```

## 2. Project

A **Project** is one backend that consumes this API on behalf of **one
workspace**.

| Property | Decision |
|---|---|
| Workspace binding | Exactly one, **permanent** |
| Status | `active` · `archived` |
| Deletion | Never. Archived instead |
| Name | Unique per workspace, case-insensitive |
| Credentials | Zero or many; up to **10 active** |
| Public id | `prj_<uuid>` |

**The binding is the authorization boundary, not a convenience.** It is compared
against the workspace in the request path before any workspace, connection,
sealed credential or provider is touched — a string comparison against a value
already in memory. A project that could span workspaces would turn that into a
lookup, and turn this guarantee into a query result:

```text
one leaked credential → one project → one workspace → one realm
```

There is no endpoint that moves a project between workspaces, and `PATCH`
**refuses** a `workspace_id` in the body rather than ignoring it. A client that
believed it had moved a project and received a 200 would discover otherwise much
later, and every live credential would silently be pointing at a different realm.

**Archiving is the kill switch.** Authentication reads the project's status in
the same row fetch as the credential, so archiving stops every credential at
once, atomically, with no per-credential write. There is no loop that can
half-finish.

There is deliberately **no slug**, unlike workspaces. A slug exists to be a
stable handle other systems reference, and nothing references a project by name.
What the name has to solve is an operator picking the wrong row out of a list,
and case-insensitive uniqueness per workspace solves exactly that.

## 3. Credential format

```text
lw_sk_<lookup>_<secret>
       └ 16      └ 52          75 characters
```

- **`lw_sk_`** — a fixed, greppable prefix. Secret scanners match on it, a
  leaked value is recognisable in a log, and the authentication middleware
  discriminates on it without ever attempting to parse the value as a JWT.
- **`lookup`** — 10 bytes of CSPRNG (80 bits), base32. Stored **in clear** as
  `key_prefix` behind a unique index. It is not a secret: it exists so
  authentication is one indexed row fetch instead of a scan over hashes.
- **`secret`** — 32 bytes of CSPRNG, **256 bits**, base32. Never stored.

A redacted example, which is the only form that ever appears in a document, a
log or a test failure:

```text
lw_sk_m2ohxyc7cec3zrd3_REDACTED
```

**Base32 lowercase, unpadded** rather than base64url, for a parsing reason: the
separator is `_` and base64url's alphabet contains `_`, which would make the
token ambiguous to split. Base32's alphabet is `a-z2-7`. It also survives being
double-clicked, retyped from a screenshot, and used as a shell word unquoted.

### Storage

| Column | Holds |
|---|---|
| `key_prefix` | the lookup segment, in clear, `UNIQUE` |
| `key_hash` | `SHA-256(secret segment)`, 32 bytes |
| `key_hash_alg` | `sha256` |

**SHA-256 rather than bcrypt or Argon2, and that is an analysis rather than a
shortcut.** Memory-hard password hashes exist to make guessing low-entropy
human-chosen input expensive. This input is 256 bits of CSPRNG output generated
by this service: there is no search space to slow down. A memory-hard hash on
the authentication path of a machine-to-machine API would add tens of
milliseconds and megabytes per request — a self-inflicted denial of service — in
exchange for nothing.

**No pepper either.** An HMAC keyed by `SECRETS_MASTER_KEY` was considered and
rejected: it helps only if the entropy assumption above is wrong, and in
exchange every credential in the system would stop working the day that key is
rotated. `key_hash_alg` exists so revisiting this is a migration, not a redesign.

Comparison is `crypto/subtle.ConstantTimeCompare`, and it runs **even when the
lookup found nothing**, against a fixed dummy digest. Without that, an unknown
prefix would return measurably faster than a known prefix with a wrong secret,
which is a prefix-enumeration oracle.

### The secret is shown once

It is returned by exactly one response — `POST …/credentials` — and by nothing
else. There is no endpoint that can return it again, and none could be added
without changing what is stored. Losing it means creating a new credential and
revoking the old one.

The guarantee is structural rather than a review convention: the domain
`Credential` type has no field for a plaintext, so no listing, response, log
line or audit event can carry one by accident. Same shape as
`connection.Connection`'s guarantee about provider secrets.

## 4. Authentication

One header, `Authorization: Bearer`, and a **prefix test** rather than a
fallback chain:

```text
Bearer lw_sk_…  → ProjectAuthenticator   (never handed to the JWT parser)
Bearer <other>  → OIDC provider          (never handed to the authenticator)
```

The two spaces are disjoint by construction: a compact JWT is base64url of a
JSON header, so it always begins `eyJ`. Nothing that begins `lw_sk_` can be one.

```text
parse locally           malformed input never reaches the database
  ↓
one indexed SELECT      credential + project together, in one fetch
  ↓
constant-time compare   always, dummy digest when nothing matched
  ↓
state checks            revoked · expired · project archived
```

A project **never produces an `auth.Identity`**. Every operator-shaped check in
the system — the self-protection guards in `identity.Service`, the admin gates,
the legacy audit actor — asks `IdentityFrom`, and for a machine the answer is
"none". Absence is already handled everywhere as 401 or "unknown actor", so a
project reaching operator-shaped code fails closed by construction.

### Failure is one public answer

```text
401  credential_invalid
```

for **every** cause: unknown prefix, wrong secret, revoked, expired, project
archived. The real reason goes to the security event channel
(`auth.EmitEvent`), never to the response and never to the audit ring — a
scanner could otherwise flood that buffer until real history rolled out.

A credential that could not be *checked* — the database was unreachable — is
**503**, not 401. Telling a correctly configured backend that its credential is
invalid during an outage sends an operator to rotate keys that were never the
problem.

## 5. Authorization

Authentication answers *who*. Authorization answers *may they*. They are
separate middleware, in that order, and merging them was rejected: the operator
path carries checks the machine path must not, and vice versa.

```text
requestid
  ↓ edge rate limit            anonymous flood cannot burn CPU on authentication
  ↓ AuthenticatePrincipal      operator token OR project credential
  ↓ credential rate limit      needs the answer above
  ↓ Authorize                  ← binding, then scope
  ↓ handler                    ← resolver, connection, secret, provider
```

**Both project checks run before the handler**, which is where the resolver, the
connection, the sealed credential and the provider live. That ordering is the
central property of this slice:

```text
project bound to A + request for workspace B  ⇒  403, and B is never touched
```

The binding check cannot leak existence either: it compares the path id against
the id on the principal, so the answer is byte-identical whether B exists, is
archived, or was never created.

**Binding is checked before scope.** Both are free, so the ordering is about what
the answer reveals: a credential probing another workspace learns only "not
yours", never which scope would have been needed there.

### The registry

Every mounted `/v1` route has exactly one classification —
**operator-only** or **a required scope** — in
[`internal/authz/registry.go`](../internal/authz/registry.go). A route with no
entry is **denied at runtime and fails the boot**: the router walks its own
registered routes at startup and panics on a gap.

That turns "did we remember to secure the new endpoint?" from a review question
into a build failure. A route added in a future slice cannot reach a provider
without someone having made an explicit security decision about it.

## 6. Scopes

Eight, `resource:verb`, held by the **credential** and not by the project — which
is what lets one backend hold a read-only key and a read-write key at the same
time, with different blast radii.

| Scope | Grants |
|---|---|
| `users:read` | list and read users |
| `users:write` | create, update, delete users; send password-reset emails |
| `roles:read` | list roles and their membership |
| `roles:write` | create, update, delete roles; grant and revoke them |
| `sessions:read` | list realm and per-user sessions |
| `sessions:revoke` | revoke one session or all of a user's |
| `invitations:read` | list pending invitations |
| `invitations:write` | create, resend, revoke invitations |

The vocabulary is enforced twice: as Go constants and as a `CHECK` constraint in
migration `000005`. A scope reaching the table that Go does not know is a failed
`INSERT` rather than a permission granted forever. Adding one is a migration,
which is correct — a new scope is a change to the authorization contract.

An empty scope list is **rejected** at creation rather than stored as "no
permissions": a credential that authenticates and can do nothing is a
configuration mistake worth reporting once, not debugging later.

### Two deliberate placements

**Granting a role is `roles:write`, not `users:write`.** What is sensitive is the
privilege being handed out, not the user record. The split lets an operator give
a backend profile management without also giving it the ability to hand out
privileges.

**`sessions:revoke`, not `sessions:write`.** A session is never edited, only
destroyed, and a permission should say what it does.

## 7. What `roles:write` can and cannot do

`identity.Service` already refuses reserved role names on create, update and
delete, for every caller. It does **not** refuse role *assignment*, and
deliberately so: an operator reaching that endpoint is already a live realm
admin, and granting admin is a normal thing for them to do.

A project credential is a different principal on the same endpoint. Without a
bound, `roles:write` would be an escalation path — grant `admin` in the
workspace's realm to a user the backend controls. Where the workspace's realm and
the installation's realm are the same Keycloak realm, which is the ordinary
single-realm deployment, that is a grant of console-operator privilege to whoever
holds the key.

**CAN:**

- create, update and delete non-reserved realm roles;
- grant and revoke those roles on any user in the bound workspace's realm.

**CANNOT:**

- create, update or delete `admin`, `user`, `offline_access`,
  `uma_authorization`, `default-roles-*` — already refused for every caller;
- **grant or revoke** any of those names — refused for machines only, with
  `role_privileged` (403).

Revocation is guarded as well as granting: a machine able to strip `admin` could
lock every operator out of the realm it administers. The predicate is
`identity.IsProtectedRoleName`, the same one the service already uses, so the
list cannot drift into a second copy.

## 8. What no scope grants

**`PUT /v1/workspaces/{ws}/users/{id}/password` is operator-only.** It sets a
credential directly, with no email and no consent: a complete account-takeover
primitive. `POST …/reset-password` covers every legitimate backend flow, and the
user ends up choosing their own credential where a compromised key cannot read
the mailbox.

There is no `users:password` scope, and creating one later would be a decision
about new keys only — including the capability in `users:write` now could never
be walked back, because every key issued under the looser rule would keep it.

**The whole control plane is operator-only**, without exception:

| Surface | Why |
|---|---|
| Workspace management | A project administers identities *inside* a workspace; it never administers the workspace. Listing would also enumerate other tenants' workspaces |
| Connection management | These hold the provider's administrative credential and decide which realm a workspace routes through. A project able to create one would escalate to full control of any realm it could reach |
| Project management | A credential able to mint credentials makes revocation meaningless: revoke one, and it has already issued another |
| Audit events | The buffer is installation-wide and contains other workspaces' activity |

`/admin/*` never runs the principal middleware at all, so a project credential
there is rejected as an invalid bearer token, with the legacy body unchanged.

## 9. Credential lifecycle

**Create** — operator only, project must be active, label required, scopes
explicit and non-empty, `expires_at` optional and must be in the future, at most
10 active per project. Secret returned once.

**Rotate** — there is no rotate endpoint, and none is needed. With multiple
credentials per project, rotation is:

```text
create the new credential → deploy it → revoke the old one
```

Zero downtime, no grace-period state machine to get wrong, and the revocation is
an explicit act rather than a timer.

**Revoke** — operator only, **immediate**. Authentication reads the row on every
request and there is no cache to invalidate. A request already in flight
completes, because it was authorized when it started; the next one fails.
Revoking twice is a conflict rather than a silent success, so an operator learns
that someone else got there first.

**Expire** — optional. Checked at authentication, and indistinguishable from
every other rejection.

**Archive the project** — every credential stops at once. One `UPDATE`.

The 10-credential cap counts **live** credentials, so revoked history never
blocks a rotation.

## 10. Rate limiting

Two limiters, both in-process, no Redis.

| Limiter | Key | Default | Runs | Meters |
|---|---|---|---|---|
| Edge | client IP | 10 req/s, burst 20 | before authentication | unauthenticated + operator traffic |
| Credential | `key_<uuid>` | 20 req/s, burst 40 | after authentication | one machine consumer |

**A credential's limit is the credential limiter's, full stop.** 20 req/s is the
number a backend can actually reach from a single address, and the number
`RateLimit-Limit` advertises.

### Why the two do not compete

They meter different traffic. The edge limiter exists so an anonymous flood
cannot buy a credential lookup or a JWT signature check per request; the
credential limiter exists so one known consumer cannot crowd out another. Until
Slice 8 the edge bucket was charged for both, having no way to tell them apart
at the point where it runs — so a backend on one address was capped at 10 req/s
and the published 20 was unreachable ([TD-026]).

The fix is not a bigger edge number, which would hand every raised request to an
attacker as well. The edge limiter **reserves** a token from the IP bucket up
front, as it must, and **releases** it once the request turns out to have come
from an authenticated machine:

```text
unauthenticated / invalid credential  → charged     (unchanged)
operator                              → charged     (unchanged)
project credential                    → released
```

Anonymous-flood protection is therefore bit-for-bit what it was — no number
moved — and the console's throughput is unchanged, because an operator is still
metered at the edge exactly as before.

The release covers a credential's requests **even when its own bucket then
refuses them**. Otherwise a single runaway key would drain the shared IP bucket
and throttle its well-behaved siblings on the same host, which is the exact
coupling the per-credential bucket exists to avoid.

### Why the credential and not the project, or the IP

Revoking the key that is flooding must fix the flood. Keyed by project, a
runaway deployment would keep throttling its siblings until every one of the
project's keys was revoked.

Keyed by IP, a backend would multiply its quota by scaling out, and would lose
its history by moving. **The bucket follows the credential**: the same key used
from three addresses draws on one allowance.

### Headers

| Header | On | Meaning |
|---|---|---|
| `RateLimit-Limit` | every project response | the **sustained** quota, req/s |
| `RateLimit-Remaining` | every project response | whole requests still available |
| `Retry-After` | 429 | seconds to wait, a hint |

`RateLimit-Limit` advertises the sustained rate rather than the burst, so a
client pacing itself by the header cannot be refused for obeying it. Burst above
that number is a deliberate under-promise. Neither header appears on an edge
429: an unauthenticated caller has no quota, and publishing the anti-flood
tuning only helps it stay just under the threshold.

A `/v1` 429 is the standard envelope (`rate_limit_exceeded`, with `request_id`),
from either limiter. Which one refused is not distinguishable, deliberately: it
is internal tuning, and the client's correct reaction is the same. `/admin/*`
keeps its legacy body, untouched.

### Tuning

| Variable | Default | Effect |
|---|---|---|
| `RATE_LIMIT_EDGE_RPS` | 10 | per-IP allowance before authentication |
| `RATE_LIMIT_CREDENTIAL_RPS` | 20 | per-credential allowance |

Burst is derived as twice the rate and is not separately configurable: a burst
below the rate cannot be sustained, and one far above it turns the limiter into
an average with no ceiling. **0, negative and unparseable all mean "the
default"** — this is a tuning knob, never an off switch, so a typo cannot
silently remove the protection.

> **Known limitations.** The buckets are per process, so two replicas permit
> twice the rate ([TD-027]). And a credential over its own limit still costs one
> indexed lookup per request, since its token was released at the edge
> ([TD-028]). Both are recorded in [TECH_DEBT.md](TECH_DEBT.md).

[TD-026]: TECH_DEBT.md#td-026
[TD-027]: TECH_DEBT.md#td-027
[TD-028]: TECH_DEBT.md#td-028

## 11. Audit attribution

`audit.Actor` is a discriminated record. The field sets are disjoint by kind, and
**a project id never appears in `Subject`** — that field means "a Keycloak sub",
and every consumer reads it that way.

```json
{
  "action": "user.created",
  "actor": { "type": "project", "project_id": "prj_…", "credential_id": "key_…" },
  "workspace": "ws_…",
  "request_id": "…",
  "user_agent": "…"
}
```

`logging.ActorFromGin` is the only constructor in production code, so the
disjointness is a guarantee rather than a convention. The credential id is
included because it is what an operator revokes: an audit line names the exact
key to pull, not just the project that held it.

Failed *authentications* do not reach the audit ring — a scanner would flood it.
Failed *authorizations* do: those are a real, identified principal doing
something unexpected, they are low-volume, and they are the signal that a backend
is misconfigured.

## 12. Error contract

| Code | Status | Meaning |
|---|:--:|---|
| `credential_invalid` | 401 | Any authentication failure. One answer for all causes |
| `authorization_unavailable` | 503 | Could not check the credential |
| `workspace_mismatch` | 403 | The credential is not bound to that workspace |
| `insufficient_scope` | 403 | Bound correctly, missing the capability. `WWW-Authenticate` names it |
| `operator_only` | 403 | No scope grants this; do not go looking for one |
| `role_privileged` | 403 | A machine tried to touch an administrative role |
| `project_not_found` | 404 | Also for a project in another workspace |
| `project_archived` | 409 | Frozen |
| `project_name_taken` | 409 | Name collision within the workspace |
| `credential_not_found` | 404 | Also for a credential on another project |
| `credential_already_revoked` | 409 | Someone got there first |
| `credential_limit_reached` | 409 | 10 active already |
| `invalid_scope` | 400 | Unknown or empty scope list |
| `rate_limit_exceeded` | 429 | Either limiter. `Retry-After`, and the quota headers when it was the credential's |
| `provider_unavailable` | 502 | The workspace's realm could not be reached |

Every one of these carries `request_id`, in the body and echoed as
`X-Request-Id`, from every layer that can refuse — authentication, the rate
limiters, authorization, the resolver and the handler. The matrix is exercised
end-to-end by `scripts/m2m-harness.sh` against a real installation, and layer by
layer by `TestV1_EveryRefusingLayerCarriesTheRequestID`.

`workspace_mismatch` is 403 rather than 404, and the reasoning is the reverse of
the usual one: hiding existence behind a 404 would be pointless, because the
check never consults the database and the response is identical whether the
workspace exists. Nothing leaks, and 403 tells a developer the truth.

## 13. Using a credential

```sh
curl -H "Authorization: Bearer lw_sk_…" \
     https://lightweight.example.com/v1/workspaces/ws_…/users
```

The workspace in the path must be the one the credential is bound to. Everything
else is the API documented in
[WORKSPACE_IDENTITY_API.md](WORKSPACE_IDENTITY_API.md); a credential simply
reaches a subset of it.

### What a consuming backend has to be configured with

Three values. This is an architectural commitment, not a summary:

```text
LIGHTWEIGHT_URL           where the API is
LIGHTWEIGHT_WORKSPACE_ID  which tenant to act on
LIGHTWEIGHT_API_KEY       lw_sk_…
```

A consumer never needs — and is never told — the Keycloak base URL, the realm
name, a Keycloak client id or secret, or a connection id. **The backend talks to
LIGHTWEIGHT; LIGHTWEIGHT knows the provider.** That is what makes connection
rotation invisible: an operator can point the workspace at a different realm and
the credential keeps working, unchanged, with no restart.

The workspace id is in the path even though the credential already determines it
server-side. A URL whose meaning changes with the key presented is a URL nobody
can read, log or cache — and the mismatch check is what proves the binding is
enforced rather than assumed.

### Proving it

`cmd/lwprobe` is a backend that imports **nothing** from this module —
`TestLwprobe_ImportsNothingInternal` fails the build if that ever changes — and
its client type has exactly the three fields above and nowhere for a fourth to
hide. `scripts/m2m-harness.sh` stands up realms, workspaces, connections,
projects and credentials the way an operator would, then hands over to it:

```sh
DB_URL=postgres://…  ./scripts/m2m-harness.sh           # contract
DB_URL=postgres://…  ./scripts/m2m-harness.sh --bench   # + measurements
```

It exercises the flow, the full error matrix, the effective rate limit,
credential isolation, immediate revocation, connection rotation, multi-realm
isolation, and that no secret reaches a response or the process log.

## 14. Proving it

[`scripts/two-realm-demo.sh`](../scripts/two-realm-demo.sh) builds two realms,
two workspaces, a project with a read-only and a read-write credential, and
asserts 66 properties against real Keycloak and real PostgreSQL — including that
a credential reads its own workspace and is refused on the other with zero
provider traffic, that a read-only key cannot write, that `admin` cannot be
granted, that revocation and archiving take effect on the next request, that the
control plane is unreachable, and that no secret appears in the audit trail.

```sh
KEYCLOAK_VERIFY_URL=http://localhost:8081 \
DB_URL='postgres://…/throwaway?sslmode=disable' \
  ./scripts/two-realm-demo.sh
```

---

## See also

- [WORKSPACE_IDENTITY_API.md](WORKSPACE_IDENTITY_API.md) — the operations a credential reaches
- [WORKSPACE_CONSOLE.md](WORKSPACE_CONSOLE.md) — where credentials are issued
- [CONNECTIONS.md](CONNECTIONS.md) — what a workspace routes through
- [TECH_DEBT.md](TECH_DEBT.md) — TD-026, TD-027
