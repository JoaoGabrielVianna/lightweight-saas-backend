# Authorization matrix

**Status:** current as of Slice 14 · closes [KI-018](../KNOWN_ISSUES.md#ki-018)

This document describes the authorization boundary for the public `/v1` surface
and the evidence that every layer of it fails closed.

It deliberately contains **no route table**. The route table lives in
[`internal/authz/registry.go`](../../internal/authz/registry.go), it is what the
server consults per request, and a copy of it here would be a second source of
truth that drifts silently. What is written down here is the part a table cannot
express: the shape of the pipeline, what each layer is responsible for, and how
each claim is proven.

---

## 1. The pipeline

Every `/v1` request passes through the same chain, in this order, and each step
can only ever narrow what the next one sees.

```
credential
   ↓  AuthenticatePrincipal   — WHO is calling
project status
   ↓  project.Authenticator   — may it authenticate at all
workspace boundary
   ↓  authz.Authorize         — is this the workspace it is bound to
scope authorization
   ↓  authz.Authorize         — does it hold the capability this route names
resource boundary
   ↓  identityruntime         — is the resource inside this workspace's realm
provider operation
   ↓  Keycloak
```

The ordering is the security property, not an optimisation. Both project checks
in `Authorize` are pure comparisons over values already in memory, and both run
**before** the handler — which means before a workspace row is read, before a
connection is loaded, before a sealed provider credential is opened, and before
any traffic reaches the provider.

Two consequences follow, and both are asserted mechanically:

- a rejected request leaves the provider untouched, so a 403 can never be a
  mutation that also happened;
- the workspace check cannot leak existence, because it compares the path id
  against the id on the principal and never consults storage.

### Middleware order, verbatim

```
requestid.Middleware
RateLimitEdge            per IP, before authentication
AuthenticatePrincipal    operator token or lw_sk_ credential
RateLimitPerCredential   per credential, keyed by an id that only exists
                         once authentication has SUCCEEDED
authz.Authorize          operator-only / workspace binding / scope
handler
```

---

## 2. The registry is the matrix

`internal/authz/registry.go` classifies every mounted `/v1` route as exactly one
of:

- **operator only** — no scope reaches it, ever;
- **scoped** — reachable by a project credential holding one named capability.

There is no third state and no default. A mounted route with no entry:

- fails `authz.ValidateRegistry` at **boot**, which panics the process
  ([`assertEveryV1RouteIsClassified`](../../internal/server/router.go));
- is denied at **runtime** by `Authorize` if it somehow escapes that.

Current shape, asserted by `TestMatrix_ClassificationCountsAreStable`:

| | count |
|---|---:|
| Project-reachable routes | 24 |
| Operator-only routes | 23 |
| Capability scopes | 9 |
| Capability families | 5 |

The families are `users`, `roles`, `sessions`, `invitations`, `audit`. Family
and read/write are **derived** from the scope string (`resource:verb`) by
[`internal/authz/matrix.go`](../../internal/authz/matrix.go) rather than
declared, so `sessions:revoke` cannot be classified as a write in one place and
a read in another.

### There is no super-scope

No `*`, `admin`, `all`, `full` or implication mechanism exists. A scope grants
exactly the routes that name it, and nothing else.
`TestMatrix_NoWildcardOrAdminScopeExists` fails if one is ever added, so
introducing one is a decision a reviewer sees rather than a line in a constant
block.

---

## 3. Lifecycle states, and what each one stops

Derived from the code, not from a design document.

| Object | States | Effect of the non-live state |
|---|---|---|
| Credential | usable · revoked · expired | 401 `credential_invalid`, before the workspace is read |
| Project | `active` · `archived` | 401 `credential_invalid` for **every** credential it owns, in the same row fetch |
| Workspace | `active` · `archived` | 409 `workspace_archived`, before Keycloak is contacted |
| Connection | `pending` · `active` · `retired` | 409 `workspace_connection_missing` — never a fallback |

There is no `disabled` state on a Project and no reactivation of an archived
Workspace. The brief for this slice speculated about both; neither exists, and
neither was invented to make a test symmetrical.

**Archiving a project is an atomic kill switch.** The authentication query reads
the project's status in the same row fetch as the credential, so nothing has to
walk a project's credentials and no loop can half-finish.

---

## 4. Caller-forbidden versus provider-forbidden

Two refusals that look alike and have completely different owners.

| | code | status | who fixes it |
|---|---|---:|---|
| The KEY lacks the capability | `insufficient_scope` | 403 | the developer, by asking their operator for a better key |
| The route is control-plane | `operator_only` | 403 | nobody — no key can satisfy it |
| A product rule refuses the caller | `caller_forbidden` | 403 | the caller, by not doing that |
| The workspace's SERVICE ACCOUNT lacks the Keycloak privilege | `provider_forbidden` | 409 | the operator, by granting realm-management roles |
| The connection was already known to be under-privileged | `connection_read_only` | 409 | the operator, by re-verifying after fixing the roles |

`provider_forbidden` is 409 rather than 403 deliberately: on this surface a 403
means "you may not do this", and reading a service-account misconfiguration as a
caller problem sends an operator to the wrong system entirely.

The distinction is proven against a real Keycloak that really refuses. Note the
subtlety the fixture had to work around: granting a service account only
`view-users` from the start produces `connection_read_only`, because
verification records the connection as read-only and the runtime's pre-flight
guard refuses locally. Reaching `provider_forbidden` requires the state a real
installation actually drifts into — privileges removed **after** verification,
so the recorded access mode is stale and the write is genuinely attempted.

---

## 5. Caches, and what each one can go stale about

Every cache on the authorization path, and the security consequence of
staleness.

| Cache | Key | Invalidation | Can it outlive a security transition? |
|---|---|---|---|
| Provider cache (`identityruntime`) | connection id + `updated_at` | none needed — the connection row is read on every request and IS the signal | **No.** Rotating a secret, editing a URL, activating another connection or retiring one all change the key or stop resolution entirely |
| Live-admin checker (`auth.AdminChecker`) | operator subject | TTL | Yes, bounded by the TTL. This is the documented GAP-1 window and applies to OPERATORS only |
| Credential / project state | — | not cached | **No.** Every request performs the indexed lookup |
| Workspace state | — | not cached | **No.** The resolver reads the row per request |

The decisions that matter for machine callers — is this credential still valid,
is its project still active, is this workspace still live, which connection does
it route through — are **read per request**. That is a deliberate trade: one
indexed read per request bought the property that revocation, archiving and
retirement take effect on the very next request with no restart, no TTL and no
invalidation protocol.

Proven, against a live process with the provider cache already warm, by
`scripts/negative-authz-e2e.sh` phases `warm` → operator acts → `matrix`.

### TOCTOU

The window between the authentication read and the provider call is real and is
not claimed to be zero. A request that authenticated microseconds before a
revocation committed will complete.

What IS guaranteed, and tested: **every request that begins after the revocation
commits is refused**, from every concurrent caller, with no per-connection or
per-goroutine warm-up. Requests already in flight are not cancelled, and no
claim is made that they are — an HTTP server that abandoned a response because a
row changed would be a different product.

---

## 6. Rate-limit ordering

The per-credential bucket is mounted **after** authentication and **before**
authorization. That is deliberate, and it answers the abuse question directly.

**Can an unauthenticated attacker exhaust a valid Project's budget by presenting
its identifiers?** No. The bucket is keyed by the credential id, which only
exists once authentication has succeeded, so a request that fails
authentication is refused before that middleware runs. Such an attacker can only
exhaust the per-IP edge bucket, which is that limiter's job.

**Does an insufficient-scope request consume the credential's budget?** Yes, and
deliberately. The caller is a known, attributable, revocable machine, and a
refused request still costs a credential lookup and a hash; leaving it unmetered
would make "spray every endpoint until one answers" the one traffic pattern with
no ceiling.

Both are pinned by tests, so reordering the chain to make denials free shows up
as a failure rather than as a quiet change.

The bucket is **per process** ([TD-027](../TECH_DEBT.md#td-027)). Two replicas
permit twice the rate. That is a documented limitation of the single-instance
topology, not a claim.

---

## 7. Audit semantics for rejected requests

Current behaviour, reported rather than changed:

| Refusal | Durable workspace audit | Security event channel | Domain event |
|---|---|---|---|
| 401 (any authentication failure) | no | yes (`EmitEvent`) | no |
| 403 `operator_only` / `workspace_mismatch` / `insufficient_scope` | no | yes, with project and credential id | no |
| 429 | no | no | no |
| A mutation that reached the handler and FAILED | no | — | yes, with `Reason` set |

Authentication failures deliberately do **not** produce durable audit rows.
Three reasons, and all three still hold:

- they are attacker-controlled in volume, and a durable row per attempt is a
  write amplification an unauthenticated caller controls;
- they have no workspace to attribute to — the credential was not resolved;
- the domain trail answers "what happened in this workspace", and filling it
  with things that did not happen makes it worse at that job.

Authenticated authorization failures (403) DO reach the security event channel
with the project and credential id, which is the signal an operator needs to
spot a misconfigured backend. Promoting them to durable rows was considered and
**not** done in this slice: it needs a retention and volume model that does not
exist yet, and doing it badly is worse than not doing it.

**Non-negotiable, and asserted over every mutating route:** a rejected mutation
emits no event at all. Never `user.created`, never `role.deleted`, never
`session.revoked`.

---

## 8. The evidence, in three layers

Deliberately three, because putting every failure into one end-to-end suite
would buy a slow CI and weaker per-case evidence.

### Layer A — mechanical, `internal/authz/matrix_test.go`

Table-driven over `authz.ProjectReachableRoutes()`. Milliseconds. Proves the
DECISION for every route × every scope:

- required scope present → allowed;
- required scope absent → refused, naming the scope in `WWW-Authenticate`;
- every other scope in the vocabulary → refused;
- read scope on a write route → refused;
- unknown, wildcard-looking or mis-cased scope → refused;
- duplicated scopes → identical decision;
- foreign workspace → refused as a mismatch, in preference to any scope
  complaint;
- operator-only route + the entire vocabulary → `operator_only`.

### Layer B — the assembled chain, `internal/server/authz_negative*_test.go`

The real `SetupRouter`, the real `AuthenticatePrincipal`, the real
`project.Authenticator`, the real `identityruntime` resolver, and two instrumented
seams:

```
workspaceLookups   the resolver's FIRST act on entering a handler
providerCalls      an actual call on the identity provider, tagged with realm
```

A rejected request must leave both at zero. The first is the stronger
statement — the refusal happened before the workspace row was read — and every
negative case is paired with a positive control on the same route with the same
body, where only the credential differs.

`nzRequests` is compared against the registry in both directions by
`TestNegative_TheRequestTableCoversEveryRoute`, which is the gate the slice asks
for: **a route added to the registry fails this package until someone writes the
request that exercises it.**

### Layer C — the real stack, `scripts/negative-authz-e2e.sh`

Real PostgreSQL, real Keycloak 26, six realms, five workspaces, three projects,
nine credentials, one long-lived process. `cmd/lwprobe -mode negative` is the
attacker: it imports nothing from this module and holds only URLs, workspace ids
and credentials.

It is **not** used to re-prove the scope matrix. It is used for the properties a
mock cannot have:

- a rejected mutation left the REALM unchanged, verified by reading Keycloak
  directly as the operator;
- a resource id from realm A cannot act through realm B, on eight routes;
- an archived workspace stops a credential that worked a second earlier, with
  the provider cache already warm and no restart;
- a workspace with no active connection does not fall back to the legacy
  configuration, a retired connection, or another workspace's;
- caller-forbidden and provider-forbidden stay distinguishable when a real
  Keycloak does the refusing;
- revocation lands on every one of six concurrent callers.

Runtime: about 15 seconds, inside the CI job that already runs Postgres and
Keycloak.

---

## 9. Mutation evidence

Authorization tests are especially prone to false confidence: a middleware that
denied everything would satisfy an exhaustive-looking negative suite.
`scripts/authz-mutation-check.sh` breaks the boundary eight ways in a scratch
copy and requires the matrix to go red each time.

All eight are caught. The table is what each mutation would have shipped, and
which assertions stand between it and production:

| # | Mutation | Caught by |
|---|---|---|
| 1 | `POST /users` requires `users:read` instead of `users:write` | `TestSDKCoverage_EveryProjectReachableRouteIsClassified`, `TestOpenAPI_EveryProjectReachableOperationNamesItsScope`, `TestNegative_AnAuthorizedMutationDoesEmitItsEvent` |
| 2 | Workspace binding not enforced | `TestMatrix_WrongWorkspaceIsRefusedOnEveryRoute`, `TestNegative_ForeignWorkspaceDoesNotDiscloseExistence`, + 6 more |
| 3 | Revocation ignored | `TestNegative_RevocationTakesEffectOnTheNextRequest`, `TestNegative_RevokingOneCredentialLeavesItsSiblingWorking`, `TestAuthenticate_EveryRejectionIsIndistinguishable` |
| 4 | Any scope satisfies any route | `TestMatrix_EveryOtherScopeIsRefused`, `TestMatrix_ReadScopeCannotWrite`, `TestNegative_ReadScopeCannotMutateThroughTheRealChain`, + 5 more |
| 5 | Handler runs, refusal written afterwards | `TestAuthorize_MissingScopeIsRefusedBeforeTheHandler`, `TestMatrix_EveryOtherScopeIsRefused`, + 6 more |
| 6 | `provider_forbidden` collapses into `caller_forbidden` | `TestHandler_ProviderFailuresMapToStableCodes`, `TestTranslate_SpecificSentinelsWinOverTheirBase` |
| 7 | `GET /audit` accepts `users:read` | `TestNegative_AuditRequiresItsOwnScope`, `TestMatrix_AuditReadIsIsolatedInBothDirections`, + 4 more |
| 8 | A workspace with no connection is served from the provider cache | `TestNegative_NoActiveConnectionDoesNotFallBackAnywhere`, `TestForWorkspace_RetiringTheOnlyConnectionStopsResolution` |

### Two mutations were wrong before they were right

Worth recording, because it is the finding that justifies the whole exercise.

The first version of mutation 5 appended `c.Next()` to `deny()`. It SURVIVED —
and not because the matrix is weak: `AbortWithStatusJSON` has already moved
gin's handler index past the end, so the extra call does nothing. The first
version of mutation 8 looked the connection up again under an empty workspace
id, which resolves to nothing and fell through to the same error.

Both changed no behaviour. Both would have been reported as caught. The script
now verifies that the **unmutated** scratch copy is green before running a
single mutation, which is what turned two false positives into two real ones.

### What a derived matrix cannot catch

Mutation 1 is instructive: it is caught by the OpenAPI and SDK drift gates, and
**not** by `TestMatrix_ReadScopeCannotWrite`. That is not a hole to plug — it is
a property of the design. The matrix derives read/write from the registry, so
mutating the registry moves the matrix with it. A derived matrix proves a
classification is **enforced**; it cannot prove the classification is **right**.

Proving the classification right needs an independently maintained document to
compare against, which is exactly what `sdk/go/apicoverage.json` and the OpenAPI
annotations are. The two mechanisms are complementary, and neither substitutes
for the other.

---

## 10. Known limits of this evidence

Stated plainly, because a security document that only lists what it proves is
half a document.

- **In-flight requests are not cancelled by revocation.** See §5.
- **Rate limiting is per process.** [TD-027](../TECH_DEBT.md#td-027).
- **Control-plane mutations and their durable audit rows are not atomic.**
  [TD-033](../TECH_DEBT.md#td-033). Nothing found here makes that worse, and
  nothing here depends on it.
- **The live-admin cache has a TTL window**, for operators only. Machine
  callers have no equivalent.
- **Layer C uses representatives, not every route.** Complete per-route coverage
  is Layers A and B; Layer C proves the assembly, once per capability family and
  once per boundary kind. That split is deliberate and is what keeps the
  real-stack suite at fifteen seconds.

---

## 11. Running it

```bash
make test                                   # Layers A and B, < 1s of the suite
make authz-mutation-check                   # does the matrix actually bite (~4 min)

DB_URL=postgres://…/throwaway \
  make e2e-negative-authz                   # Layer C (~15s + stack)

DB_URL=postgres://…/throwaway \
  make sdk-acceptance                       # the SDK's typed errors, real stack
```

Layer C also runs in CI, inside the `e2e` job, reusing the Postgres and Keycloak
that job already starts.
