# Known Issues

**Last updated:** 2026-08-24 · Companion to [PROJECT_STATUS.md](PROJECT_STATUS.md) and [TECH_DEBT.md](TECH_DEBT.md)

Defects, runtime limitations, workarounds, and temporary decisions. Structural
shortcuts that raise the cost of change live in
[TECH_DEBT.md](TECH_DEBT.md) instead.

This file supersedes [roadmap/KNOWN_LIMITATIONS.md](roadmap/KNOWN_LIMITATIONS.md),
which is retained for the v0.2 RC1 historical record. Original IDs (F1–F3,
L1–L10, GAP-1–GAP-4) are cross-referenced so older reports remain traceable.

**Severity**
- **Critical** — service does not start, or data/security integrity is
  compromised
- **High** — a documented capability does not work
- **Medium** — degraded behaviour, exploitable under specific conditions, or
  silently incorrect
- **Low** — cosmetic, or an accepted trade-off with a bounded blast radius

---

## Summary

| ID | Issue | Severity | Status |
|---|---|:--:|---|
| [KI-001](#ki-001) | `/auth/debug` registered twice → boot panic; degraded console payload | Critical | ✅ **Fixed 2026-07-26** |
| [KI-002](#ki-002) | Bearer token remains valid after OIDC logout | Medium | Accepted (F2) |
| [KI-003](#ki-003) | No security headers on the `/admin` shell | Medium | Open |
| [KI-004](#ki-004) | Rate limit bypassable via forged `X-Forwarded-For` | Medium | Open |
| [KI-005](#ki-005) | SPA never reviewed for XSS | Medium | Open — unassessed |
| [KI-006](#ki-006) | No realm-wide session revocation | Low | Open (GAP-3 / L6) |
| [KI-007](#ki-007) | Bootstrap collects three feature flags nothing reads | Low | Open |
| [KI-008](#ki-008) | Tag `v0.3.1` has no CHANGELOG entry | Low | ✅ **Fixed 2026-07-26** |
| [KI-009](#ki-009) | `internal/config` documents a removed `JWTSecret` field | Low | Open |
| [KI-010](#ki-010) | Admin API client retries only on 401, not 5xx | Low | Accepted (L7) |
| [KI-011](#ki-011) | Keycloak email theme does not survive container recreation | Medium | Open — workaround documented |
| [KI-012](#ki-012) | `/health` performs no dependency checks | Low | ✅ **Fixed 2026-08-09** |
| [KI-013](#ki-013) | List endpoints silently truncate at a hard cap | Medium | Open |
| [KI-014](#ki-014) | Token replay possible for a token's full TTL | Low | Accepted (F3) |
| [KI-015](#ki-015) | Unknown JSON keys silently dropped on PATCH | Low | Accepted (GAP-4) |
| [KI-016](#ki-016) | Invitation lifecycle residuals | Low | Accepted (L8–L10) |
| [KI-017](#ki-017) | Keycloak email theme doc references version 24.0 | Low | Open |
| [KI-018](#ki-018) | Guards not asserted end-to-end | Medium | **Closed** (Slice 14) |
| [KI-019](#ki-019) | OAuth authorization code written to the access log | Medium | ✅ **Fixed 2026-08-10** |
| [KI-020](#ki-020) | Workspace Audit view crashed on any workspace with events | High | ✅ **Fixed 2026-08-10** |

**20 entries · Open: 14 · Fixed or closed: 6.** Five of the fourteen are
**accepted trade-offs** (KI-002, KI-010, KI-014, KI-015, KI-016) rather than
work waiting to be done. **No Critical and no High is open.**

Open entries are KI-002, KI-003, KI-004, KI-005, KI-006, KI-007, KI-009,
KI-010, KI-011, KI-013, KI-014, KI-015, KI-016, KI-017.

KI-019 and KI-020 were found by the browser end-to-end suite
([TD-031](TECH_DEBT.md#td-031), Slice 11) on its first runs against a real
stack, and neither was visible to any pre-existing gate.

> **KI-008 and KI-012 were closed on 2026-08-24 by re-derivation, not by new
> work.** Both had been fixed weeks earlier — the `[0.3.1]` changelog section on
> 2026-07-26, the readiness split on 2026-08-09 — and neither entry was marked.
> Counts here are maintained by hand; re-derive from the table before editing
> them.

---

## KI-001

### `/auth/debug` registered twice → boot panic; degraded console payload — **FIXED**

**Severity: Critical** · Reported and fixed 2026-07-26 · Introduced 2026-06-13 (`c4e8329`)

Two distinct defects with one root cause.

**Defect A — process panic at startup.** `GET /auth/debug` was registered in two
places on the same Gin engine:

- `internal/server/router.go` — inside the `private` group, unconditionally
- `internal/server/playground.go` — gated by `DevPlaygroundEnabled`

`SetupRoutes` calls `SetupRouter` and then `mountPlayground`, so with the
playground enabled both ran. Gin panics on duplicate registration
(`gin@v1.12.0/tree.go:243`).

Because [.env.example](../.env.example) ships `DEV_PLAYGROUND_ENABLED=true` and
`make setup` copies it to `.env`, **the documented onboarding path produced a
container that would not start.**

**Defect B — admin console rendered signed-in admins as signed-out.** With the
playground *off* — the production recipe — the surviving `router.go` handler
returned only `received_sub`, `received_azp`, `email`, `username`, `roles`,
`expires_at`. It omitted `valid`, `issuer`, `allowed_clients`, `exp`, `expired`,
`iat`, `aud`. The SPA reads those:

| Location | Consequence |
|---|---|
| `components/sidebar.js` — `if (!id \|\| !id.valid)` | Rendered **"not signed in"** for an authenticated admin |
| `views/overview.js` | Rendered an **"invalid"** token pill |
| `views/settings.js` — `id.valid !== undefined` | Hid the issuer / allowed-clients card entirely |
| `views/playground.js` | "signed in as …" never appeared |

So the console was broken in **both** configurations: crash with the playground
on, visual corruption with it off.

**Why it survived six weeks.** The only `SetupRoutes` test in the suite ran with
`DevPlaygroundEnabled: false`, so Defect A was never exercised. Defect B is a
silent UI degradation with no failing assertion anywhere. All 59 evidence
artifacts under [evidence/](evidence/) predate 2026-06-13. See
[TD-003](TECH_DEBT.md#td-003).

**Runtime confirmation.**

```
AUDIT RESULT: PANIC CONFIRMED -> handlers are already registered for path '/auth/debug'
```

Note that `go run ./cmd/api` does **not** reveal this without a live stack:
`config.Validate`, `database.Connect`, and the JWKS fetch all `log.Fatal` before
`SetupRoutes` is reached. Reproduction requires either the full stack or a
package-level test.

**Resolution.** One handler, two purposefully-scoped routes:

| Route | Auth | Mounted | Purpose |
|---|---|---|---|
| `GET /auth/debug` | Required | Always, by `SetupRoutes` | Full payload for the admin console |
| `GET /dev/auth/debug` | None | `DEV_PLAYGROUND_ENABLED=true` | Explains *why* a bad token was rejected |

The split is necessary, not cosmetic: an authenticated route cannot diagnose an
invalid token because `RequireAuth` rejects it first. Both are served by
`authDebugHandler`. `web/dev/auth.js` was repointed to the dev route.

**Regression guards added** to
[internal/server/server_test.go](../internal/server/server_test.go):

- `TestServerSetupRoutes_FlagMatrixDoesNotPanic` — all four flag combinations
  construct without panicking
- `TestServerSetupRoutes_FlagMatrixRouteVisibility` — asserts gating via Gin's
  route table, and pins `/auth/debug` to **exactly one** registration
- `TestAuthDebug_RequiresAuth` — the always-on route is gated
- `TestAuthDebug_ReturnsSPAContract` — pins all 12 fields the console reads

**Lesson.** A degraded reimplementation of an existing handler is worse than
reusing it. The commit message read *"implement /auth/debug endpoint used by
admin SPA"* — the endpoint already existed and already served the SPA correctly.

---

## KI-002

### Bearer token remains valid after OIDC logout

**Severity: Medium** · Status: **Accepted trade-off** · Originally F2

After an OIDC logout, the access token stays valid until `exp` (up to 3600 s in
the default realm). The API does not consult Keycloak's session store on every
request.

**Impact.** A token captured before logout remains usable for the remainder of
its lifetime. Blast radius is bounded by `accessTokenLifespan`.

**Why accepted.** This is the standard stateless-JWT trade-off. Checking the
session store per request would eliminate the performance benefit of stateless
validation.

**Partial mitigation already in place.** For the surface that matters most —
`/admin/*` — [`RequireLiveAdmin`](../internal/auth/admin_check.go) *does*
consult Keycloak, so a logged-out or demoted admin loses admin access within the
cache TTL (30 s) even though their token still authenticates.

**Options if requirements change.** Shorten `accessTokenLifespan`; implement
backchannel logout; call `userinfo` on sensitive verbs.

---

## KI-003

### No security headers on the `/admin` shell

**Severity: Medium** · Status: Open

`/admin` sets `Content-Type` and `Cache-Control: no-store`, but no
`Content-Security-Policy`, `Strict-Transport-Security`, `X-Frame-Options`, or
`X-Content-Type-Options` ([internal/server/admin.go](../internal/server/admin.go)).

**Impact.** No CSP means no defence-in-depth against an XSS vector in the SPA —
which compounds [KI-005](#ki-005). No `X-Frame-Options` permits clickjacking of
the console. No HSTS permits protocol downgrade on first contact.

**Recommendation.** Add a headers middleware for the console routes. A strict
CSP is feasible here because the SPA loads no external resources — no CDN, no
external fonts.

**Roadmap:** [V1-09](ROADMAP.md#v1-09--security-hardening-pass)

---

## KI-004

### Rate limit bypassable via forged `X-Forwarded-For`

**Severity: Medium** · Status: Open

`clientIP` in [internal/server/ratelimit.go](../internal/server/ratelimit.go)
returns the leftmost `X-Forwarded-For` entry whenever the header is present,
with no verification that the request came from a trusted proxy.

**Impact.** Any client that can reach the API directly defeats the rate limit
entirely by varying the header:

```bash
for i in $(seq 1 1000); do
  curl -H "X-Forwarded-For: 10.0.0.$((RANDOM % 255))" \
       -H "Authorization: Bearer $TOKEN" \
       http://api/admin/users
done
```

Every request lands in a fresh bucket. This nullifies the only rate limit in the
system — the control added specifically to close finding F1.

**Preconditions.** The API must be reachable without a proxy that overwrites the
header. Behind an ALB or nginx that *sets* rather than appends XFF, the header is
trustworthy and the issue does not apply.

**Recommendation.** Use Gin's `SetTrustedProxies` and only honour XFF from
configured CIDRs; otherwise use `RemoteAddr`. Add a `TRUSTED_PROXIES` config
value.

**Roadmap:** [V1-04](ROADMAP.md#v1-04--close-the-rate-limit-bypass)

---

## KI-005

### SPA never reviewed for XSS

**Severity: Medium** · Status: **Open — risk unassessed**

The admin console is 5,809 lines of vanilla JavaScript that builds DOM through
an `h()` hyperscript helper and template strings, and renders server-supplied
data: usernames, emails, role names and descriptions, session user-agents,
audit event fields, and Markdown from the docs viewer.

**No XSS review has ever been performed.** This entry records an *unknown*, not
a confirmed vulnerability. Do not read it as either safe or exploitable.

**Why it warrants attention.** Several rendered values are attacker-influenceable
— an invited user controls their own first/last name, and a session's user-agent
is client-supplied. If any path uses `innerHTML` without escaping, a stored XSS
in an admin console is a privilege-escalation vector. The absence of CSP
([KI-003](#ki-003)) removes the mitigating layer.

**Recommendation.** Audit every DOM-construction path for `innerHTML` /
`insertAdjacentHTML` on non-constant input; confirm `h()` escapes text nodes;
review the Markdown renderer specifically, since Markdown-to-HTML is a classic
injection surface. Then add CSP.

**Roadmap:** [V1-09](ROADMAP.md#v1-09--security-hardening-pass)

---

## KI-006

### No realm-wide session revocation

**Severity: Low** · Status: Open · Originally GAP-3 / L6

`DELETE /admin/sessions` (no path parameter) is not routed. Per-user
(`DELETE /admin/users/:id/sessions`) and per-session
(`DELETE /admin/sessions/:id`) revocation both work.

**Impact.** Operational, not a vulnerability: there is no single "panic button"
if a token is suspected leaked at scale. An operator must revoke user by user, or
go directly to the Keycloak admin UI.

**Current state in the UI.** The console renders the button disabled with a
`coming-soon` badge — honest, but the capability is absent.

**Roadmap:** [V1-10](ROADMAP.md#v1-10--performance-and-consistency-cleanup)

---

## KI-007

### Bootstrap collects three feature flags that nothing reads

**Severity: Low** · Status: Open

[bootstrap/prompt.go](../internal/bootstrap/prompt.go#L97) prompts for
`google_login`, `mfa`, `multi_tenant`, `swagger` and `seed_users`. Only
`dev_playground` and `seed_users` are read anywhere in the codebase.

**Impact.** The tool implies capabilities that do not exist. Answering "yes" to
`multi_tenant` writes `"multi_tenant": true` into `project.json` for a system
with **no tenancy whatsoever** — see
[FEATURES.md §12](FEATURES.md#12-not-started--product-domain). Anyone reading
that file, human or AI, would reasonably conclude multi-tenancy is enabled.

**Recommendation.** Remove the inert prompts, or label them `not yet
implemented` in the CLI output. Do not leave them silently inert.

Tracked as debt in [TD-015](TECH_DEBT.md#td-015).

---

## KI-008

### Tag `v0.3.1` has no CHANGELOG entry — **FIXED**

**Severity: Low** · Fixed 2026-07-26 · **Entry closed 2026-08-24**

**What it said.** `v0.3.1` (`06a5bd6`, 2026-05-25, *"add admin-aligned landing
and minimal IAM boot experience"*) was tagged in git, but
[CHANGELOG.md](../CHANGELOG.md) jumped from `[Unreleased]` to `[0.3.0]`, so the
changelog was incomplete as a release record.

**What fixed it.** The `[0.3.1]` section was written retroactively on
2026-07-26 and says so in its own preamble, naming this issue.

**Why it stayed listed as Open for a month.** The fix landed in the CHANGELOG
and nobody came back to mark the entry. That is the same class of rot the
v0.4.2 documentation reconciliation exists to clear, and it is the reason
[RELEASE_CHECKLIST.md](RELEASE_CHECKLIST.md) gate 2.9 exists.

**Regression guard.** `scripts/check_docs_links.py` would fail on a dangling
`#ki-008` anchor, but nothing asserts that every tag has a changelog section.
That gate does not exist and is not proposed here: the failure is caught by
Phase 2 of the release checklist, by a human, once per release.

---

## KI-009

### `internal/config` documents a removed `JWTSecret` field

**Severity: Low** · Status: Open

The package doc comment and `LoadConfig`'s comment in
[internal/config/config.go](../internal/config/config.go) both describe a
`JWTSecret` field and a `JWT_SECRET` environment variable, including *"Default
JWT_SECRET 'secret' is for development ONLY"*. Neither exists — the field was
removed when Keycloak took over token signing.

**Impact.** Actively misleading. A reader may search for JWT-signing
configuration that cannot exist, or believe this service signs tokens — the exact
opposite of AD-001.

Tracked as debt in [TD-012](TECH_DEBT.md#td-012).

---

## KI-010

### Admin API client retries only on 401, not on 5xx or transport errors

**Severity: Low** · Status: **Accepted trade-off** · Originally L7

[`AdminClient`](../internal/identity/keycloak/admin.go) retries exactly once on
401 — which correctly handles Keycloak key rotation invalidating a cached
service-account token. It does not retry 5xx or transport failures.

**Impact.** A transient Keycloak blip surfaces to the caller as a 502. The admin
retries manually.

**Why accepted.** Blind retries on mutating endpoints risk duplicate side
effects. The compensating-delete contract on invitation creation keeps the
failure mode recoverable, and the caller is a human at a console who can retry.

**If revisited.** Retry only idempotent reads (GET), with backoff and a bounded
budget. Never blanket-retry POST/PUT/DELETE against the Admin API.

---

## KI-011

### Keycloak email theme does not survive container recreation

**Severity: Medium** · Status: Open — workaround documented

The custom `corsi` email theme lives in
[deploy/keycloak/themes/corsi/](../deploy/keycloak/themes/corsi/). In the
production environment (EasyPanel / Docker Swarm), files under
`/opt/keycloak/themes/corsi/` are lost on every deploy or container restart.
The compose file mounts `./deploy/keycloak` to
`/opt/keycloak/data/import` — the realm-import path — **not** the themes path.

**Impact.** After any Keycloak restart, invitation, password-reset and
verification emails silently revert to Keycloak's default templates. Nothing
fails; the branding just disappears. Users receive unbranded, English-default
mail.

**Current workaround** (from
[operations/KEYCLOAK_EMAIL_THEME.md](operations/KEYCLOAK_EMAIL_THEME.md)) —
manual copy after each restart:

```bash
CONTAINER=$(docker ps | grep keycloak | grep -v db | awk '{print $NF}')
docker exec $CONTAINER mkdir -p \
  /opt/keycloak/themes/corsi/email/html \
  /opt/keycloak/themes/corsi/email/text
# then copy each .ftl from deploy/keycloak/themes/corsi/
```

**Permanent fix** — bake the theme into a custom image:

```dockerfile
FROM quay.io/keycloak/keycloak:26.0
COPY deploy/keycloak/themes/corsi /opt/keycloak/themes/corsi
```

Then publish it to a registry and point the Keycloak service at it. Note the
existing runbook shows `24.0` in this snippet — see [KI-017](#ki-017).

---

## KI-012

### `/health` performs no dependency checks — **FIXED 2026-08-09**

**Severity: Low** · Fixed by the readiness split · **Entry closed 2026-08-24**

**What it said.** `healthHandler` returned `{"status":"ok"}` unconditionally, so
there was no readiness signal: an orchestrator could not distinguish "process
running" from "process able to serve", and a pod with a broken database kept
receiving traffic.

**What fixed it.** The probes were split, exactly as this entry recommended
([`internal/server/health.go`](../internal/server/health.go)):

| Route | Answers | Checks |
|---|---|---|
| `/health/live` | should this process be restarted? | nothing — no I/O, no lock, no dependency |
| `/health/ready` | should this instance receive traffic? | the database, and whether a drain has begun |
| `/health` | unchanged | liveness, kept byte-compatible for existing monitors |

`/health/ready` answers `{"status":"ready", "checks": {…}}` with `200`, or `503`
with the same shape while draining or when the database is unreachable, and sets
`Cache-Control: no-store` so a cached "ready" cannot outlive the state it
described.

**One deliberate deviation from the recommendation.** Readiness does **not**
check Keycloak. This entry asked for it; it was rejected with a reason worth
keeping: connections are per workspace, so one tenant's Keycloak going down
would take the instance out of rotation and every other tenant with it. A
workspace whose provider is unreachable answers `provider_unavailable` on its
own requests and nobody else's.

**Related:** [TD-009](TECH_DEBT.md#td-009), whose operational half this was.

---

## KI-013

### List endpoints silently truncate at a hard cap

**Severity: Medium** · Status: Open

`ListInvitations` and `ListUsersByRole`
([invitations.go](../internal/identity/keycloak/invitations.go),
[roles.go](../internal/identity/keycloak/roles.go)) page internally up to a
defensive hard cap and return whatever they collected. Realms larger than the cap
get a **silently incomplete** result with a 200 status.

Separately, `ListRoles`, `ListSessions`, `ListUserRoles` and `ListUserSessions`
do not paginate at all.

**Impact.** Worse than a failure, because it looks like success. An admin
searching for an invitation beyond the cap concludes it does not exist. The
provider comment says *"For realms above the cap, adopt the v0.3 local-mirror
approach"* — v0.3 shipped without that approach.

**Recommendation.** Make truncation explicit in the response (a `truncated: true`
flag or a next-page cursor), and apply one pagination convention to every
`List*`.

Tracked as debt in [TD-007](TECH_DEBT.md#td-007).

---

## KI-014

### Token replay possible for a token's full TTL

**Severity: Low** · Status: **Accepted trade-off** · Originally F3

No DPoP, no `jti` revocation list, no per-request nonce. A captured bearer token
is replayable by any holder from any IP until `exp`.

**Why accepted.** Expected behaviour for plain OAuth2 bearer tokens. Mitigating
it requires DPoP or mTLS, which is a substantial change to every client.

**If revisited.** DPoP (RFC 9449) is the lower-friction option; mTLS suits
machine-to-machine only.

---

## KI-015

### Unknown JSON keys silently dropped on PATCH

**Severity: Low** · Status: **Accepted trade-off** · Originally GAP-4

Go's default `json.Decoder` ignores keys absent from the target struct. A
mass-assignment probe such as
`PATCH /admin/users/:id {"roles":["admin"],"admin":true}` returns **200** while
changing nothing.

**Impact.** No security consequence — the probe is functionally a no-op and was
verified as such (probe B5). But it returns success for a request that did
nothing, and it defeats attack detection: body fuzzing produces no signal.

**Why accepted.** Strict binding (`DisallowUnknownFields`) would break forward
compatibility for clients sending extra fields.

**If revisited.** Log unknown keys as a warning without rejecting the request —
detection without a breaking change.

---

## KI-016

### Invitation lifecycle residuals

**Severity: Low** · Status: **Accepted trade-offs** · Originally L8–L10

| # | Behaviour | Operator action |
|---|---|---|
| L8 | Compensating delete is best-effort. A partition killing both the email dispatch *and* the cleanup DELETE leaves an orphan user | The next invite attempt returns 409; delete the orphan via `DELETE /admin/users/:id` and re-invite |
| L9 | Resending an expired invitation re-sends the email but does **not** extend `expires_at` | Recreate the invitation instead of resending once expiry has passed |
| L10 | An unparseable `expires_at` attribute is treated as "no expiry" and the invite shows as `pending` | If an invite appears stuck in `pending`, inspect the raw attribute in Keycloak |

All three require two near-simultaneous failures or manually corrupted attribute
data. The contracts are documented in
[identity/provider.go](../internal/identity/provider.go).

---

## KI-017

### Keycloak email theme doc references version 24.0

**Severity: Low** · Status: Open

[operations/KEYCLOAK_EMAIL_THEME.md](operations/KEYCLOAK_EMAIL_THEME.md) shows
`FROM quay.io/keycloak/keycloak:24.0` in its custom-image snippet and refers to
switching the service from `24.0`. [docker-compose.yml](../docker-compose.yml)
pins `quay.io/keycloak/keycloak:26.0`.

**Impact.** Following the runbook verbatim would downgrade Keycloak by two major
versions. Small change, potentially large consequence.

**Recommendation.** Update the snippet to `26.0` and reference the compose pin
rather than hardcoding a version in prose.

---

## KI-018

### Authorization guards not asserted end-to-end — **CLOSED**

**Severity: Medium** · Opened as L1 · **Closed 2026-08-13 (Slice 14)**

**What it said.** The self-protection guards — self-delete, self-disable,
self-strip-admin, last-admin — were covered by unit tests in `internal/identity`
but never asserted against a live stack. The last-admin guard in particular
([security/SECURITY_GAPS.md](security/SECURITY_GAPS.md) §A) could not be
triggered cleanly during adversarial probing, because the test realm happened to
have two admins — so the one guard standing between an operator and locking
their organization out of its own realm had never been demonstrated at runtime.

**What closed it.** Slice 14 built a three-layer negative authorization matrix
and, separately, the single-admin realm fixture this issue asked for by name.
The full model and its evidence are in
[security/AUTHORIZATION_MATRIX.md](security/AUTHORIZATION_MATRIX.md); the short
version:

- **Layer A** ([`internal/authz/matrix_test.go`](../internal/authz/matrix_test.go))
  sweeps every project-reachable route against every scope in the vocabulary,
  driven by the same registry the server consults. Milliseconds.
- **Layer B** ([`internal/server/authz_negative_test.go`](../internal/server/authz_negative_test.go))
  runs the real router and proves each refusal lands **before the workspace row
  is read**, and therefore before any provider traffic. A completeness gate
  fails the package when a route is added to the registry without a
  corresponding request.
- **Layer C** ([`scripts/negative-authz-e2e.sh`](../scripts/negative-authz-e2e.sh))
  runs against real PostgreSQL and real Keycloak with seven realms, and proves
  what a mock cannot: rejected mutations leave the realm unchanged, a resource
  id from one realm cannot act through another, archiving and retirement take
  effect with the provider cache already warm, and caller-forbidden stays
  distinguishable from provider-forbidden when a real Keycloak does the
  refusing.

**The guards this issue was actually about** are exercised in Layer C against a
realm built with exactly one enabled admin, with the operator token, and the
realm is then read directly to confirm the target survived:

```
the realm's last enabled admin cannot be deleted        403 caller_forbidden
the realm's last enabled admin cannot be disabled       403 caller_forbidden
the realm's last enabled admin cannot have admin stripped  403 caller_forbidden
an operator cannot delete their own account             403 caller_forbidden
an operator cannot disable their own account            403 caller_forbidden
an operator cannot strip their own admin role           403 caller_forbidden
```

**One finding worth keeping.** These guards are unreachable from the Project
surface by construction: the three self guards compare the caller's Keycloak
subject against the target, and a project credential has no Keycloak subject at
all. The last-admin guard does apply to machines, but on role operations the
protected-role guard refuses `admin` first with `role_privileged`, so it is
shadowed there. That is why the fixture needs an operator token and a workspace
pointing at the installation's own realm — the single-realm deployment is the
only shape in which the caller and the target can be the same person.

**Regression protection.** `scripts/authz-mutation-check.sh` breaks the
authorization boundary eight ways and requires the matrix to go red each time;
all eight are caught. Layer C runs in CI inside the existing `e2e` job.

**Related:** [TD-003](TECH_DEBT.md#td-003) · [R-01](RISKS.md#r-01)

---

## KI-019

### OAuth authorization code written to the access log — **FIXED**

**Severity: Medium** · Found and fixed 2026-08-10 · Present since the admin
console shipped

`NewServer` used `gin.Default()`, which installs `gin.Logger()`, whose default
formatter prints `param.Path` — and `param.Path` is the request URI *including
the raw query*. The admin console's PKCE callback is an ordinary browser
navigation to

```
GET /admin?code=<authorization code>&state=<csrf state>
```

so **every operator login wrote a live authorization code and a CSRF state
token into the application log**, in plaintext.

**How bad, honestly.** Not critical. The code is single-use, short-lived, and
bound to a PKCE verifier that never leaves the browser, so an attacker holding
only the log cannot exchange it. It is fixed anyway because:

- the protection is PKCE, not the log. Any deployment that puts a confidential
  or non-PKCE client on this surface has a directly usable credential sitting in
  a file;
- `state` is a CSRF token, and logging it is simply wrong;
- the same formatter printed **every** query string, so the exposure was
  "whatever any client ever puts in a query parameter", and the next such
  parameter would not have been found by a test;
- logs get shipped. Once they are, the blast radius is not one laptop.

**How it was found.** By the browser e2e suite's artifact scan
([`scripts/scan-artifacts.sh`](../scripts/scan-artifacts.sh)) on its first
otherwise-green run. The scan searches everything a run publishes for the exact
secret values the run used; the API log is copied into CI diagnostics, and the
authorization code was in it. No existing gate could see this: the login
succeeded, every status code was correct, and nothing failed.

**Resolution.** [`internal/logging/access_log.go`](../internal/logging/access_log.go)
adds a formatter that redacts the *values* of a known-sensitive parameter set
(`code`, `state`, `session_state`, `code_verifier`, `id_token_hint`, the token
family, `client_secret`, `password`, `api_key`) and keeps everything else
byte-identical to gin's layout. `NewServer` now uses `gin.New()` plus that
logger and `gin.Recovery()`.

The parameter **name** is deliberately kept: `/admin?code=REDACTED` still tells
an operator that the line is a login callback, and dropping the query entirely
would make a login indistinguishable from a shell load during an incident.

**Regression guards** in
[internal/logging/access_log_test.go](../internal/logging/access_log_test.go):

- `TestRedactRequestURI` — twelve cases including the callback, an unparseable
  query (redacted wholesale, never passed through), and a similarly-named
  parameter that must *not* be redacted;
- `TestAccessLogFormatter_KeepsGinsLayout` — the line shape is an interface
  (somebody's grep, somebody's log shipper) and only the field's content
  changed;
- `TestAccessLogger_DoesNotLogAuthorizationCodes` — a real gin engine, a real
  request, and the bytes that reach the writer. The unit test proves the
  function; this proves the server uses it, which is the half that regressed.

[`scripts/redact-logs.sh`](../scripts/redact-logs.sh) also gained the pattern,
as defence in depth for logs produced by binaries predating this fix.

---

## KI-020

### Workspace Audit view crashed on any workspace with events — **FIXED**

**Severity: High** · Found and fixed 2026-08-10 · Introduced with the Audit view
(Slice 10)

[`views/workspace-audit.js`](../web/admin/static/js/views/workspace-audit.js)
called the shared table component as

```js
renderTable({ columns: [...], rows: [...] })       // one argument
```

but [`components/table.js`](../web/admin/static/js/components/table.js) is
`renderTable(target, opts)`. With one argument `opts` is `undefined`, and the
component's first line — `opts.columns` — threw
`TypeError: Cannot read properties of undefined (reading 'columns')`. The
`columns` were also plain strings and the `rows` arrays of elements, neither of
which is the shape the component reads.

**So the durable Audit view was broken for every workspace that had ever
recorded an event** — which is every workspace anyone has used. It rendered
correctly only while the trail was empty, because the empty-state branch returns
before reaching the table.

**Why nothing caught it.** Three things lined up:

1. the throw happens **after an `await`**. `router.js` wraps `r.view(...)` in a
   `try/catch`, but every view is `async`, so a rejection after the first await
   escapes as an unhandled promise rejection. There is no "view crashed" panel —
   the page simply sits on "loading…" forever;
2. no frontend suite rendered this view. The existing suites cover state,
   routing, isolation and error parsing;
3. no backend test can see it. The API returned 200 with a correct body.

This is precisely the [KI-001](#ki-001) shape: a console-only defect, invisible
to every server-side gate, that a person would report as "the page never
loads".

**How it was found.** Journey 5 of the browser suite opened the Audit view after
a machine had made an audited mutation, and failed on both the missing row and
the captured `pageerror`.

**Resolution.** The view now calls `renderTable(tableEl, {...})` with the
component's real contract: `{key, title, render}` columns and the raw event
records as rows.

**Regression guards** in
[web/admin/static/js/tests/audit-view.test.mjs](../web/admin/static/js/tests/audit-view.test.mjs)
— seven cases at the level the rule lives at, verified to fail 6-of-7 against
the pre-fix code (the one that passes is the empty-trail case, exactly as the
defect's shape predicts): a row per event, the five column headers, the
project-vs-operator actor rendering, the failure reason code, the empty state,
and both ends of the cursor pagination.

---

## Explicitly *not* issues

Behaviours reviewers sometimes flag that are intentional. Carried forward from
[roadmap/KNOWN_LIMITATIONS.md](roadmap/KNOWN_LIMITATIONS.md) §8 and re-verified
against the code on 2026-07-26.

| Behaviour | Why it is correct |
|---|---|
| The `/admin` HTML shell is unauthenticated | It contains no secrets; every action it performs goes through the gated API (AD-005) |
| Auth failures return a fixed `{"error":"unauthorized"}` | The specific reason goes to the `AuthEvent` stream, never the wire — deliberate, so validation internals are not disclosed |
| RBAC denials return 403, not 401 | Lets clients distinguish "log in again" from "you lack access" |
| Cross-client tokens are rejected at `azp` with 401, not 403 | A `client_credentials` token minted for the admin client cannot impersonate a user-tier caller |
| `POST /admin/roles` is race-safe | 10 parallel creates of the same name → 1×201, 9×409 |
| There is no `/login` or `/register` endpoint | Keycloak owns identity (AD-001) |
| `/admin/*` returns 404 when the admin client is unconfigured | A 403 would confirm the feature exists (AD-004) |
| Two Keycloak hostnames in docker-compose | `KEYCLOAK_URL` drives `iss` matching (browser-facing); `KEYCLOAK_ADMIN_BASE_URL`/`JWKS_URL` are server-to-server |

## Maintenance

Add new issues with an ID, severity, reproduction, and either a fix or an
explicit acceptance rationale. When one is fixed, keep the entry, mark it
**Fixed** with the date, and record the regression guard that prevents
recurrence — as done for [KI-001](#ki-001). An issue removed without that record
is an issue that can return unnoticed.
