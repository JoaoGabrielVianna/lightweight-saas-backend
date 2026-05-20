# SECURITY REMEDIATION — GAP-1 (Stale JWT retains revoked admin role)

**Date:** 2026-05-20
**Source finding:** [SECURITY_GAPS.md §D / GAP-1](SECURITY_GAPS.md#d-token-replay-after-privilege-revocation--gap-1-high) (HIGH)
**Status:** Fixed.

---

## 1. Root cause

Authorization on `/admin/*` was decided exclusively from the JWT claim set.
`internal/auth/middleware.go:RequireRole("admin")` inspected the validated
token's `realm_access.roles` array and let the request through if `admin`
was present. The middleware never consulted Keycloak's authoritative state.

Consequence: once Keycloak signed a token containing `admin`, the token
carried admin authority until `exp` regardless of any subsequent role
revocation. With realm `accessTokenLifespan = 3600 s`, an admin who was
demoted retained full admin powers for up to one hour from the wire.

Demonstrated in [docs/SECURITY_GAPS.md §D](SECURITY_GAPS.md#d-token-replay-after-privilege-revocation--gap-1-high)
by a demoted user mutating `adminuser.first_name → "PWNED"` with a stale token.

---

## 2. Chosen mitigation

A two-layer authorization gate on `/admin/*`:

```
RequireAuth        → JWT validation (signature, iss, azp, exp)         (unchanged)
RequireRole(admin) → JWT claim check                                   (unchanged — cheap short-circuit for non-admins)
RequireLiveAdmin   → live Keycloak lookup w/ short TTL cache           (NEW — GAP-1 closure)
```

The third middleware consults an `auth.AdminChecker` for the caller's live
admin status on every `/admin/*` request. The check is backed by an
in-process TTL cache (default 30 s) to keep the per-request cost flat, and
the identity handlers that mutate role/user state invalidate cache entries
immediately so in-band changes take effect on the next request.

### Why this option (vs. the alternatives in §D)

| Option (from SECURITY_GAPS.md §3) | Why not chosen |
|-----------------------------------|----------------|
| Shorten `accessTokenLifespan` realm-wide | Breaks normal user sessions across the whole app for a problem that only matters on admin verbs. The realm export is the wrong layer for a route-specific guard. |
| `/userinfo` per request, cached ≤5 s | Adds an extra Keycloak hop and an HTTP call already implicitly resolved through the Admin API we use for everything else. |
| Backchannel-logout + event-driven cache | Highest-quality fix but largest blast radius — requires standing up an event listener, new env, new failure modes. Worth doing later; not the minimum safe fix. |
| **Live check + short TTL cache + in-band invalidation (chosen)** | Closes GAP-1 with one new middleware and no new infrastructure. Latency overhead bounded by the cache. Mutations through this API are instant; out-of-band changes (operator going straight to Keycloak Admin UI) close within the TTL. |

### Defense-in-depth properties

- **Fails closed.** When the Keycloak Admin API can't be reached, the
  middleware returns 503 rather than falling back to the JWT claim.
  Admin verbs never run on stale authorization during infra failures.
- **JWT short-circuit preserved.** RequireRole still runs first — a flood
  of non-admin probes does not become a flood of Keycloak round trips.
- **Negative caching.** A demoted-admin JWT bouncing against `/admin/*`
  hits the upstream once per TTL, not once per request.
- **Distinct denial telemetry.** Live-check denials emit
  `EventForbidden` with the reason marker
  `"live admin check denied: token role no longer present server-side"`
  so dashboards can tell GAP-1 rejections from plain RBAC ones.

### Layering / circular-dependency

- `internal/auth` defines `AdminChecker`, `AdminInvalidator`,
  `CachedAdminChecker`, and `RequireLiveAdmin`. **No new imports from
  `internal/identity`.**
- `internal/identity` already imports `internal/auth` (handlers use
  `auth.IdentityFrom`). The handler gains an optional
  `auth.AdminInvalidator` field — same direction, no cycle.
- The adapter that turns `identity.IdentityProvider.ListUserRoles` into an
  `auth.AdminChecker` lives in `internal/server` (the composition root),
  which already depends on both packages.

---

## 3. Files changed

| File | Change |
|------|--------|
| [internal/auth/admin_check.go](../internal/auth/admin_check.go) | **NEW.** `AdminChecker` interface, `AdminInvalidator` interface, `NoopAdminInvalidator`, `CachedAdminChecker` (TTL cache, concurrent-safe, injectable clock), `RequireLiveAdmin` middleware. |
| [internal/auth/admin_check_test.go](../internal/auth/admin_check_test.go) | **NEW.** 16 unit tests covering middleware behavior, cache TTL semantics, fail-closed on upstream error, distinct denial telemetry, and the composed `RequireRole + RequireLiveAdmin` gate (GAP-1 attack reproduced in-process). |
| [internal/identity/handler.go](../internal/identity/handler.go) | Handler gains `adminInvalidator` field + `SetAdminInvalidator(nil-safe)`. `UpdateUser`, `AssignRolesToUser`, `UnassignRoleFromUser`, `DeleteUser`, `DeleteInvitation` invalidate the target subject after success; `DeleteRole` calls `InvalidateAll` (role-graph change). |
| [internal/identity/handler_admin_invalidation_test.go](../internal/identity/handler_admin_invalidation_test.go) | **NEW.** Asserts each mutation handler invokes the invalidator (and that failure paths do NOT). |
| [internal/server/router.go](../internal/server/router.go) | `SetupRouter` accepts an `auth.AdminChecker`; mounts `RequireLiveAdmin` on the `/admin/*` group after `RequireRole("admin")`. |
| [internal/server/server.go](../internal/server/server.go) | `SetupIdentity` returns `(*identity.Handler, *auth.CachedAdminChecker, error)`. Builds the cached checker from an in-tier adapter over `IdentityProvider.ListUserRoles` and wires it both into the router and into the handler's invalidator slot. |
| [internal/config/config.go](../internal/config/config.go) | New optional `ADMIN_LIVE_CHECK_TTL_SECONDS` env var (default 30s). |
| [cmd/api/main.go](../cmd/api/main.go) | Threads the new return value through `SetupRoutes`. |
| [scripts/security_gap1_check.sh](../scripts/security_gap1_check.sh) | **NEW.** Focused live-stack validation: 11 probes covering baseline / grant / promoted-token-works / revoke / **stale-token-now-rejected (G1.6 + G1.7)** / current-admin-still-works / normal-user-still-denied / `/me`-unaffected. |
| [docs/SECURITY_GAPS.md](SECURITY_GAPS.md) | GAP-1 status updated to **FIXED** with cross-link to this document. |

---

## 4. Test evidence

### 4.1 Unit tests

```
$ go test ./internal/auth/... ./internal/identity/... -count=1 -v | grep -E '^(=== RUN|--- (PASS|FAIL))'
...
=== RUN   TestRequireLiveAdmin_NoIdentity_Returns401
--- PASS: TestRequireLiveAdmin_NoIdentity_Returns401 (0.00s)
=== RUN   TestRequireLiveAdmin_EmptySubject_Returns401
--- PASS: TestRequireLiveAdmin_EmptySubject_Returns401 (0.00s)
=== RUN   TestRequireLiveAdmin_DemotedAdmin_Returns403           ← GAP-1 in-process repro
--- PASS: TestRequireLiveAdmin_DemotedAdmin_Returns403 (0.00s)
=== RUN   TestRequireLiveAdmin_CurrentAdmin_PassesThrough
--- PASS: TestRequireLiveAdmin_CurrentAdmin_PassesThrough (0.00s)
=== RUN   TestRequireLiveAdmin_UpstreamError_FailsClosed         ← 503 on KC down (never falls back to claim)
--- PASS: TestRequireLiveAdmin_UpstreamError_FailsClosed (0.00s)
=== RUN   TestRequireLiveAdmin_EmitsForbiddenEventWithMarker
--- PASS: TestRequireLiveAdmin_EmitsForbiddenEventWithMarker (0.00s)
=== RUN   TestCachedAdminChecker_CachesPositiveResult
--- PASS
=== RUN   TestCachedAdminChecker_CachesNegativeResult
--- PASS
=== RUN   TestCachedAdminChecker_TTLExpires
--- PASS
=== RUN   TestCachedAdminChecker_ErrorsNotCached
--- PASS
=== RUN   TestCachedAdminChecker_Invalidate_RefetchesOnNextCall
--- PASS
=== RUN   TestCachedAdminChecker_InvalidateAll_RefetchesEveryone
--- PASS
=== RUN   TestCachedAdminChecker_ZeroTTLFallsBackToDefault
--- PASS
=== RUN   TestCachedAdminChecker_EmptySubject_NoUpstreamCall
--- PASS
=== RUN   TestRequireRole_then_RequireLiveAdmin_DemotedJWT_Denied  ← full chain: claim says admin, KC says no → 403
--- PASS
=== RUN   TestRequireRole_then_RequireLiveAdmin_NonAdmin_ShortCircuits  ← non-admin never hits KC
--- PASS
=== RUN   TestRequireRole_then_RequireLiveAdmin_CurrentAdmin_Allowed
--- PASS
=== RUN   TestUnassignRoleFromUser_InvalidatesTarget
--- PASS
=== RUN   TestAssignRolesToUser_InvalidatesTarget
--- PASS
=== RUN   TestUpdateUser_InvalidatesTarget
--- PASS
=== RUN   TestDeleteUser_InvalidatesTarget
--- PASS
=== RUN   TestDeleteRole_InvalidatesAll
--- PASS
=== RUN   TestSetAdminInvalidator_NilFallsBackToNoop
--- PASS
=== RUN   TestUnassignRoleFromUser_FailurePath_DoesNotInvalidate  ← failure must NOT invalidate
--- PASS
```

### 4.2 Full test suite

```
$ make ci
  + fmt-check passed
  + vet passed
  + built bin/api
  + every package PASS (audit, auth, auth/keycloak, bootstrap, identity,
    identity/keycloak, logging, user)
  + swagger.{json,yaml,docs.go} match annotations
  + CI checks passed
```

### 4.3 Live-stack validation

Reproduces the original GAP-1 attack flow against the running stack (see
[scripts/security_gap1_check.sh](../scripts/security_gap1_check.sh) for the
host-runnable version; the canonical results below were captured with a
docker-network sidecar because the host port 8080 was held by an unrelated
local process during the run):

```
## tokens: admin=1161 testuser-pre=1145
## ids: admin=ddc2cf1b-3735-4f4c-90b5-ed0c4e06ffe6 test=2219e074-2f70-4904-bcd5-f9399ef401b9
## snapshot adminuser.first_name=Adminuser

PASS  G1.1  exp=200 actual=200   ← admin baseline
PASS  G1.9  exp=403 actual=403   ← normal user denied (no admin role)
PASS  G1.2  exp=204 actual=204   ← grant testuser admin
## post-grant testuser token len=1155
PASS  G1.3  exp=200 actual=200   ← freshly-promoted token lists /admin/users
PASS  G1.4  exp=200 actual=200   ← freshly-promoted token PATCHes adminuser
PASS  G1.5  exp=204 actual=204   ← revoke testuser admin
PASS  G1.6  exp=403 actual=403   ← STALE token denied on GET /admin/users  ★ GAP-1 closed
PASS  G1.7  exp=403 actual=403   ← STALE token denied on PATCH /admin/users/{adminuser}  ★ GAP-1 closed
PASS  G1.7b adminuser.first_name unchanged (Adminuser)                       ★ PATCH did NOT mutate
PASS  G1.8  exp=200 actual=200   ← current admin still works
PASS  G1.10 exp=200 actual=200   ← /me still works for the demoted user (auth unaffected)

TOTAL: 11   PASS: 11   FAIL: 0
Result: PASS — GAP-1 closed
```

Evidence files: [docs/evidence/security/gaps/remediation/](evidence/security/gaps/remediation/).

API-side audit log lines corresponding to the GAP-1 denials (the
`live admin check denied` marker is the GAP-1 fingerprint that ops
dashboards can grep for):

```
auth ] denied kind=forbidden method=GET   path=/admin/users
       reason=live admin check denied: token role no longer present server-side
auth ] denied kind=forbidden method=PATCH path=/admin/users/ddc2cf1b-…
       reason=live admin check denied: token role no longer present server-side
```

Pre-fix, these requests returned **200** and (for PATCH) successfully
mutated `adminuser.first_name`; post-fix they return **403** with no state
change.

---

## 5. Requirements compliance

| Requirement | Status | Evidence |
|-------------|--------|----------|
| Demoted admin token must fail on /admin/* after role removal | ✅ | G1.6, G1.7 above; `TestRequireRole_then_RequireLiveAdmin_DemotedJWT_Denied` |
| Existing admin tokens must continue to work if the user still has admin server-side | ✅ | G1.8; `TestRequireLiveAdmin_CurrentAdmin_PassesThrough` |
| Non-admin users remain 403 | ✅ | G1.9; `TestRequireRole_then_RequireLiveAdmin_NonAdmin_ShortCircuits` (and no upstream call — preserves perf) |
| Auth failures remain 401 | ✅ | RequireAuth unchanged; `TestRequireLiveAdmin_NoIdentity_Returns401`, `TestRequireLiveAdmin_EmptySubject_Returns401` |
| Must not break /me | ✅ | G1.10; `/me` is on the `private` group, not `/admin/*` |
| Must not break public routes | ✅ | `/health` and `/swagger/*` are outside `/admin/*`; verified live |
| No circular dependency between auth and identity | ✅ | `internal/auth` has no `internal/identity` import (verifiable: `go list -deps ./internal/auth | grep identity` → empty) |

---

## 6. Remaining risk

- **Out-of-band changes still have a TTL-bounded lag.** If an operator
  edits role membership directly in the Keycloak Admin UI (not through
  `/admin/*`), the cache won't know to invalidate immediately. With the
  default 30 s TTL the worst-case exposure window shrinks from 3600 s
  (the realm `accessTokenLifespan`) to 30 s — a **120× improvement**.
  Operators who need zero-lag can set `ADMIN_LIVE_CHECK_TTL_SECONDS=1`
  at the cost of one Keycloak round trip per admin verb.
- **GAP-2 (session terminate ≠ JWT revocation) is unchanged** by this
  remediation. Killing sessions still doesn't invalidate in-flight access
  tokens. The mitigation here happens to also help GAP-2 *for the admin
  surface specifically*: if the operator's intent in killing sessions
  was to revoke admin powers, they should ALSO unassign the admin role —
  which now propagates immediately. Standalone GAP-2 fix (backchannel
  logout / token revocation list) is tracked separately.
- **Keycloak unavailability fails closed.** On Keycloak outage every
  admin verb returns 503 until Keycloak recovers. This is by design —
  the alternative (silently fall back to the JWT claim) would re-open
  GAP-1 every time Keycloak hiccups. Acceptable for an IAM-grade
  surface; operators should monitor `EventForbidden` with the
  `live admin check failed:` reason marker.

---

## 7. Release gate

| Gate | Pre-fix | Post-fix |
|------|---------|----------|
| Functional ([FINAL_SMOKE.md](FINAL_SMOKE.md)) | GO | GO |
| Security ([SECURITY_GAPS.md](SECURITY_GAPS.md)) | **NO-GO for IAM-grade ops** | **GO** for IAM admin surface |

The single HIGH-severity finding that gated NO-GO has been closed,
verified by both unit tests and a live-stack reproduction of the
original attack flow.
