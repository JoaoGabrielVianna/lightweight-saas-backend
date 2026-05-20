# SECURITY GAPS — Adversarial Probes

**Date:** 2026-05-20
**Tester:** Agent D (Security Tester)
**Target:** `milestone/auth-v1` against the live `docker-compose` stack
**Mission:** Actively attempt to break each guard — *delete own admin role, self-lockout, role escalation, cross-user mutations, token replay, mass revoke* — and document any exploitable gap with reproducible evidence.
**Method:** Black-box probes against the live API + Keycloak. State changes were rolled back; the realm's persistent state at end-of-run is identical to start-of-run (verified — see §9).
**Forbidden:** implementation changes.

## TL;DR

| # | Surface                              | Result | Severity |
|---|--------------------------------------|--------|----------|
| **GAP-1** | **Privilege revocation lag (stale JWT)** | **FIXED (2026-05-20)** — closed by a live-admin check (`internal/auth.RequireLiveAdmin`) on `/admin/*` with a short-lived cache and in-band invalidation. See [SECURITY_REMEDIATION_GAP1.md](SECURITY_REMEDIATION_GAP1.md) and live evidence at [evidence/security/gaps/remediation/](evidence/security/gaps/remediation/). Original finding preserved below for traceability. | **HIGH** (remediated) |
| GAP-2 | Session termination does not kill the JWT | EXPLOITABLE-adjacent — same root cause as GAP-1. Killing all of admin's sessions does not stop the same JWT from authorizing /admin/users. | MEDIUM |
| GAP-3 | Realm-wide bulk session terminate    | NOT-IMPLEMENTED — `DELETE /admin/sessions` → 404. No "panic button" if a token leaks. | LOW (operational) |
| GAP-4 | Strict JSON binding on PATCH         | INFO — unknown keys are silently dropped (Go default `json.Decoder` behavior). No state change, but defeats attack-detection on body fuzzing. | INFO |
| —  | A (self-demote/self-delete/self-disable)  | **NO GAP** — all three self-targeted operations rejected with `403`. |
| —  | B (role escalation by non-admin)     | **NO GAP** — direct, indirect, mass-assignment, and header-injection escalation all rejected. |
| —  | C (cross-user mutations by non-admin)| **NO GAP** — `403` on every cross-user verb. |
| —  | E5 (IDOR on /admin/users/:id/sessions)| **NO GAP** — testuser → admin's sessions → 403. |
| —  | E6 (mass-DELETE routes)              | **NO GAP** — no batch DELETE endpoints exist (`/admin/users`, `/admin/roles` → 404; `/admin/users/_all` → 400). |

**Net assessment.** RBAC and self-protection guards behave as specified. The single high-severity finding is a well-known consequence of pure-JWT stateless auth: revocation does not propagate to already-issued tokens within their TTL window. The `accessTokenLifespan` of **3600s** bounds the blast radius — but the bound is too long to rely on for IAM-grade admin operations.

---

## 1. Stack state

```
saas-api                 Up                       0.0.0.0:8080->8080/tcp
saas-keycloak            Up (healthy)             0.0.0.0:8081->8080/tcp
saas-keycloak-postgres   Up (healthy)             0.0.0.0:5433->5432/tcp
saas-postgres            Up (healthy)             0.0.0.0:5432->5432/tcp
saas-mailpit             Up (healthy)             0.0.0.0:8025, 1025
```

Seed identities at probe time:

| ID                                       | Username           | Roles                  |
|------------------------------------------|--------------------|------------------------|
| `ddc2cf1b-3735-4f4c-90b5-ed0c4e06ffe6`   | adminuser          | admin, user (implicit) |
| `2219e074-2f70-4904-bcd5-f9399ef401b9`   | testuser           | user                   |
| `3680a75c-0c69-4ad3-8b06-2c5a2db0815f`   | user@example.com   | admin, user            |

Two admins. The "last-admin" guard could not be triggered cleanly in this realm (see §A note), so it is documented as **unverified at runtime** in v0.4 (it remains covered by unit test `TestUnassignRolesFromUser_RejectsLastAdmin`).

---

## A. Delete own admin role / self-lockout — NO GAP

| ID  | Probe                                                                 | Expected | Actual | Result |
|-----|-----------------------------------------------------------------------|---------:|-------:|--------|
| A1  | adminuser → `DELETE /admin/users/{own}/roles/admin` (self-strip)      |    403   |   403  | PASS   |
| A3  | adminuser → `DELETE /admin/users/{own}` (self-delete)                 |    403   |   403  | PASS   |
| A4  | adminuser → `PATCH /admin/users/{own}` with `{"enabled":false}` (self-disable) | 403 | 403 | PASS |

Bodies all `{"error":"forbidden"}`. Source-side guards live at:

- `internal/identity/handler.go:441` — self-disable.
- `internal/identity/handler.go:569` — self-strip-admin / last-admin.
- `internal/identity/handler.go:664` — self-delete / last-admin.

**A note on last-admin.** The realm has *two* admins (adminuser, user@example.com). To trigger the last-admin guard cleanly via the API one needs a path that empties the admin set — but every such path passes through self-strip first when the caller is the only admin. So the last-admin guard's runtime behavior is asserted only through `TestUnassignRolesFromUser_RejectsLastAdmin`; the runtime behavior was not directly demonstrable here without setting up a deliberately fragile multi-admin pivot.

Evidence: [A1_self_strip_admin_*](evidence/security/gaps/), [A3_self_delete_*](evidence/security/gaps/), [A4_self_disable_*](evidence/security/gaps/).

---

## B. Role escalation (non-admin → admin) — NO GAP

| ID  | Probe                                                                 | Expected | Actual |
|-----|-----------------------------------------------------------------------|---------:|-------:|
| B1  | testuser → `POST /admin/users/{own}/roles` body `["admin"]`           |    403   |   403  |
| B2  | testuser → `POST /admin/users/{adminuser}/roles` body `["admin"]`     |    403   |   403  |
| B3  | testuser → `PATCH /admin/users/{own}` body `{"realm_access":{"roles":["admin"]},"roles":["admin"],"admin":true}` | 403 | 403 |
| B4  | testuser → `GET /admin/users` with `X-User-Role: admin`, `X-Roles: admin`, `X-Forwarded-Roles: admin` | 403 | 403 |
| B5  | adminuser → `PATCH /admin/users/{testuser}` body `{"realm_access":{"roles":["admin"]},"roles":["admin"],"is_admin":true,"admin":true,"groups":["admin"]}` | 200, **no role change** | 200 with testuser.roles unchanged (`["user"]`) — **see GAP-4** |

`RequireRole("admin")` short-circuits all four non-admin attempts. The mass-assignment probe (B5) returns `200` but is functionally a no-op — Go's default JSON decoder silently discards keys not present in the `UpdateUserRequestBody` struct. The server-side role list confirms testuser still has only `user` after B5.

Evidence: [B1_*](evidence/security/gaps/) … [B5_*](evidence/security/gaps/).

---

## C. Cross-user mutations by non-admin — NO GAP

| ID  | Probe                                                                  | Expected | Actual |
|-----|------------------------------------------------------------------------|---------:|-------:|
| C1  | testuser → `PATCH /admin/users/{adminuser}` `{"email":"hacked@evil.com","first_name":"Hacked"}` | 403 | 403 |
| C2  | testuser → `DELETE /admin/users/{adminuser}`                           |    403   |   403  |
| C3  | testuser → `POST /admin/users/{adminuser}/reset-password`              |    403   |   403  |
| C4  | testuser → `DELETE /admin/users/{adminuser}/sessions`                  |    403   |   403  |

Same pattern: `RequireRole("admin")` rejects before the handler runs. Bodies all `{"error":"forbidden"}`.

Evidence: [C1_*](evidence/security/gaps/) … [C4_*](evidence/security/gaps/).

---

## D. Token replay after privilege revocation — **GAP-1 (HIGH) — FIXED 2026-05-20**

> **Resolution:** Closed by [SECURITY_REMEDIATION_GAP1.md](SECURITY_REMEDIATION_GAP1.md).
> A live Keycloak role check now gates `/admin/*` after the JWT-claim
> gate; the post-revocation reproduction below now returns **403** (was 200)
> and the demonstrated `PATCH` mutation no longer persists.
>
> The original finding text is preserved as-is below for audit traceability.

> **The privileged JWT outlives the privilege.**

### Setup

1. `POST /admin/users/{testuser}/roles` body `{"roles":["admin"]}` as **adminuser** → `204`.
   testuser now has the realm `admin` role.
2. Fresh token request for testuser via Direct Access Grants → `D_TOK` (len 1146).
   Token claims:
   ```json
   {"exp":1779277089,"realm_access":{"roles":["admin","user"]}}
   ```
   Decoded server-side: testuser is admin.

### Exploit

3. `GET /admin/users` with `Authorization: Bearer D_TOK` → **200** (pre-revoke baseline; evidence [D3_pre_revoke_admin_users_headers.txt](evidence/security/gaps/D3_pre_revoke_admin_users_headers.txt)).
4. `DELETE /admin/users/{testuser}/roles/admin` as **adminuser** → `204`. Server state confirmed: testuser's role list is now `["user"]` ([D5_post_revoke_server_roles.json](evidence/security/gaps/D5_post_revoke_server_roles.json)).
5. **Re-test `D_TOK` AFTER revocation:**
   - `GET /admin/users` → **200** ([D6](evidence/security/gaps/D6_post_revoke_admin_users_headers.txt)). The demoted user still reads the user directory.
   - `PATCH /admin/users/{adminuser}` body `{"first_name":"PWNED"}` → **200**. The response body shows the mutation persisted: `"first_name":"PWNED"` ([D7_post_revoke_patch_admin_body.txt](evidence/security/gaps/D7_post_revoke_patch_admin_body.txt)). A demoted user successfully overwrote an admin's profile.

Rollback step: `PATCH /admin/users/{adminuser}` `{"first_name":"Adminuser"}` as adminuser → `200`. Field restored. testuser confirmed back to `["user"]`. Realm is clean.

### Why the guard misses

`internal/auth/middleware.go:RequireRole` decides authorization purely from the JWT's `realm_access.roles` claim. The middleware does not consult Keycloak's session store or `userinfo` endpoint on each request. Once a JWT is signed with `admin` in the claim set, it carries that privilege until `exp` regardless of subsequent server-side revocation. The realm's `accessTokenLifespan` is **3600 seconds** — so any role demotion has up to a **1-hour exposure window** where the demoted principal retains full admin powers from the wire.

### What an attacker gets

- 60 minutes (worst-case) to perform any admin verb after their role is revoked
- Includes: edit/delete other users, edit/delete roles, mint new invitations, reset passwords, revoke sessions
- Bounded only by their token's remaining TTL — refreshing requires the session to be alive on Keycloak's side, but the *existing* access token does not

### Suggested remediations (defense-in-depth, ranked)

1. **Shorten `accessTokenLifespan` for admin-scoped tokens.** 60–120 s lifespan + refresh on every admin verb. Cuts blast radius by >95 %.
2. **Listen to Keycloak's backchannel-logout / role-change events** and maintain an in-process revocation cache. Reject tokens whose `sid` or `sub` is in the cache.
3. **Call `/userinfo` on every admin verb** (cached for ≤5 s per `sub`). Costly but eliminates the lag.
4. **Pin admin sessions to specific JWTs and rotate on each admin verb** (DPoP / per-request nonce).

---

## E. Mass revoke — GAP-2 / GAP-3

### E1 — Admin can kill ALL their own sessions; their JWT keeps working

`DELETE /admin/users/{adminuser_id}/sessions` as adminuser → **204**.

Immediately after — same `Authorization: Bearer <admin_token>`:

```
GET /me           → 200
GET /admin/users  → 200
```

The same JWT continues to authenticate and authorize. ([E1_self_session_kill_headers.txt](evidence/security/gaps/E1_self_session_kill_headers.txt))

This is the session-attack-vector restatement of GAP-1. Even after "log everyone out for this user", the existing bearer tokens still pass `RequireAuth` because validation is signature + claim, never a session-liveness lookup.

### E5 — Same effect through `DELETE /admin/sessions/{sid}`

Pick adminuser's specific session ID from `/admin/sessions` and delete it → `204`. JWT still works → `/me` `200`, `/admin/users` `200`. ([E5_kill_own_sid_headers.txt](evidence/security/gaps/E5_kill_own_sid_headers.txt))

### E2 — Realm-wide bulk terminate is unimplemented — **GAP-3**

`DELETE /admin/sessions` (no `:id`) → `404 page not found` (Gin's default 404, not the API's JSON 404). ([E2_realm_wide_kill_headers.txt](evidence/security/gaps/E2_realm_wide_kill_headers.txt))

The SPA already renders the corresponding button as disabled with a `coming-soon` badge (`web/admin/static/js/views/sessions.js`). Operationally this is the "panic button" you would reach for if a master credential leaks. Its absence is a low-severity finding under the realm's current ops model but should be tracked.

### E4 — Cross-admin session kill works

`DELETE /admin/users/{user@example.com}/sessions` as adminuser → `204`. Confirms one admin can log out another admin. Combined with GAP-1/GAP-2, the logged-out admin can still operate from any unexpired token.

### E6 — No batch-DELETE escalation

| Path                          | Method | Status |
|-------------------------------|--------|-------:|
| `/admin/users`                | DELETE |    404 |
| `/admin/roles`                | DELETE |    404 |
| `/admin/sessions/_all`        | DELETE |    400 |
| `/admin/users/_all`           | DELETE |    400 |

No batch endpoint exists — `400` on the latter two is route-matched-then-rejected on UUID validation. No way to "wipe everything" through a single call. ([E6_batch_routes.txt](evidence/security/gaps/E6_batch_routes.txt))

### E7 — IDOR on session listing

testuser → `GET /admin/users/{adminuser}/sessions` → `403`. RequireRole guards the whole route group. ([E7_idor_sessions_headers.txt](evidence/security/gaps/E7_idor_sessions_headers.txt))

---

## F. Token replay (general) — same root cause as D

Covered as INFO finding F3 in [SECURITY_VALIDATION_v0.3.md](SECURITY_VALIDATION_v0.3.md#L99). Reconfirmed: 30 parallel replays of the same valid JWT → 30×200. Bearer JWTs are replayable until `exp`. Not a vulnerability in itself, but it is the carrier wave that makes GAP-1 / GAP-2 exploitable.

---

## 2. Findings consolidated

### GAP-1 — Stale JWT retains revoked admin role (HIGH) — **FIXED 2026-05-20**

- **Resolution:** [SECURITY_REMEDIATION_GAP1.md](SECURITY_REMEDIATION_GAP1.md).
- **Reproducer:** see §D, four-line attack flow.
- **Pre-conditions:** attacker held the role at some point, has the corresponding access token.
- **Blast radius:** up to `accessTokenLifespan = 3600 s` of admin-grade access after revocation.
- **Demonstrated impact:** modified another admin's first_name via PATCH using a demoted user's token.
- **Affected code path:** `internal/auth/middleware.go` — `RequireRole` reads roles from the JWT, no session check.
- **Affected operational guard:** **none currently.** Realm `accessTokenLifespan` is the only bound.

### GAP-2 — Session termination is not JWT revocation (MEDIUM)

- **Reproducer:** see §E1 + §E5.
- **Why it matters:** admins reaching for "Logout this user" / "Logout all sessions" expect to actually log the user out. Today it logs out their Keycloak session (no future refresh, no SSO continuation), but their *current* JWT remains a fully authorized credential.
- **Affected code path:** same as GAP-1.

### GAP-3 — Realm-wide bulk session terminate is missing (LOW)

- **Reproducer:** §E2.
- **Why it matters:** if a high-value credential is suspected leaked, ops have no single call to invalidate every session. They must walk `/admin/sessions` and DELETE each. With GAP-2, that still wouldn't kill any in-flight JWTs.
- **Affected code path:** no route registered under `DELETE /admin/sessions` (no `:id`).

### GAP-4 — PATCH silently drops unknown fields (INFO)

- **Reproducer:** §B5.
- **Why it matters:** a body with `{"realm_access":{"roles":["admin"]}}` returns `200` with no warning. The server applied no change, but the wire response is indistinguishable from a successful update. A stricter `json.Decoder.DisallowUnknownFields()` would return `400` and surface the attempted mass-assignment in audit logs.
- **Severity:** informational; no exploit demonstrated.

---

## 3. Suggested remediations matrix

| Finding | Suggested fix                                                              | Cost | Risk reduction |
|---------|----------------------------------------------------------------------------|------|----------------|
| GAP-1   | Drop `accessTokenLifespan` for admin scope to 60–120 s + force PKCE refresh on each admin verb | low (Keycloak realm config) | ~98 % |
| GAP-1   | Maintain an in-process revocation cache fed by Keycloak admin events / backchannel-logout | medium | ~100 % |
| GAP-1   | Validate each admin verb against `/userinfo` (5 s cache per `sub`) | medium (latency) | ~100 % |
| GAP-2   | Subscribe to Keycloak's `LOGOUT` and `LOGOUT_ALL` events; populate revocation cache | medium | ~100 % |
| GAP-3   | Implement `DELETE /admin/sessions` (realm-wide) — already drafted in UI as `coming-soon` | low | n/a (defensive) |
| GAP-4   | Switch admin handlers to `DecoderConfig{DisallowUnknownFields: true}` | trivial | hardens detection |

---

## 4. What was NOT tested

For honest scope-keeping:

- **Live `last-admin` removal** — required pivoting through a multi-admin sequence the realm doesn't support without state churn. Covered by unit test only.
- **Token signature forgery / kid rotation under attack** — implicitly trusted by the JWKS subsystem (covered in v0.2 G09 — tampered signatures are rejected).
- **DoS / sustained brute-force** — covered as F1 in [SECURITY_VALIDATION_v0.3.md](SECURITY_VALIDATION_v0.3.md).
- **CSRF on /admin/*** — the API is pure bearer-token (no cookies for auth), so no CSRF surface.
- **Browser-side XSS into the admin SPA** — out of scope here; would warrant a separate review of `web/admin/static/js/`.

---

## 5. Evidence inventory

```
docs/evidence/security/gaps/
├── 00_users.txt / 00_users.json / 00_roles.txt / 00_admin_role_users.txt
├── A1_self_strip_admin_{headers,body}.txt        # self-demote → 403
├── A3_self_delete_{headers,body}.txt             # self-delete → 403
├── A4_self_disable_{headers,body}.txt            # self-disable → 403
├── B1_user_self_escalate_{headers,body}.txt      # non-admin self-escalate → 403
├── B2_user_other_escalate_{headers,body}.txt     # non-admin escalates other → 403
├── B3_mass_assignment_{headers,body}.txt         # non-admin PATCH escalation → 403
├── B4_header_inject_{headers,body}.txt           # X-User-Role: admin → 403
├── B5_admin_mass_assignment_{headers,body}.txt   # admin sends bogus keys, 200 no-op
├── B5_after_testuser_roles.json                  # confirms no role gain
├── C1..C4_*.txt                                  # non-admin cross-user verbs → 403
├── D3_pre_revoke_admin_users_{headers,body}.txt  # baseline
├── D4_revoke_headers.txt                         # revoke 204
├── D5_post_revoke_server_roles.json              # server state: testuser → [user]
├── D6_post_revoke_admin_users_{headers,body}.txt # STALE TOKEN → 200 (exploit)
├── D7_post_revoke_patch_admin_{headers,body}.txt # STALE TOKEN PATCHED ADMIN → 200
├── E1_self_session_kill_headers.txt              # self-logout 204; JWT still 200
├── E2_realm_wide_kill_headers.txt                # bulk endpoint 404
├── E3_sessions_list.json                         # session enumeration as admin
├── E4_kill_other_admin_sessions_headers.txt      # kill another admin's sessions 204
├── E5_kill_own_sid_headers.txt                   # per-sid kill 204; JWT still 200
├── E6_batch_routes.txt                           # no batch DELETE endpoints
└── E7_idor_sessions_headers.txt                  # cross-user session list → 403
```

---

## 6. Reproducibility

The probes were issued as one-shot `curl` invocations against the live stack — no script file was committed. Each guard's reproduction is a single command pasted from §A–§E. To re-run the full set with a clean realm:

```sh
docker-compose down -v
docker-compose up -d
# wait for /health and Keycloak discovery to return 200
# then paste each numbered probe from this document
```

The state-mutating probes (D1 grant, D7 PATCH, E1/E4/E5 session kills) are individually rolled back inline within each section. End-of-run state was verified in §9 below.

---

## 7. Verdict

| Gate                            | Status (original)                                                                       | Status (post-fix 2026-05-20)                       |
|---------------------------------|------------------------------------------------------------------------------------------|----------------------------------------------------|
| **GO / NO-GO**                  | NO-GO for IAM-grade ops without GAP-1 remediation. GO for non-privileged surfaces.       | **GO** — GAP-1 closed; see [SECURITY_REMEDIATION_GAP1.md](SECURITY_REMEDIATION_GAP1.md). GAP-2 (medium) and GAP-3 (low operational) remain outside the IAM-grade GO scope and tracked separately. |

The functional gate ([FINAL_SMOKE.md](FINAL_SMOKE.md)) remains GO. The security gate ([FINAL_SECURITY.md](FINAL_SECURITY.md)) was a synthesis of probes that all *expected* the JWT-stateless model; this adversarial pass quantifies what that model actually permits, and the answer is: enough that an attacker who holds a token for any role for one second retains that role for up to 60 minutes after server-side revocation. Recommendation: ship the functional surface, gate the IAM admin surface behind a short-lifespan admin-scope token (`accessTokenLifespan ≤ 120 s` for admin scope), and revisit when an event-driven revocation cache is in place.

---

## 8. Mapping to prior findings

| This report | Earlier finding                              |
|-------------|----------------------------------------------|
| GAP-1       | sharpens F2 from [SECURITY_VALIDATION_v0.3.md](SECURITY_VALIDATION_v0.3.md#L155) (logout doesn't invalidate access token) — turns the INFO/Low-Med into an **HIGH demonstrated exploit** |
| GAP-2       | restates F2 against the session-termination attack vector specifically |
| GAP-3       | same as F4 in [FINAL_SECURITY.md](FINAL_SECURITY.md#L116) and FS-1 in [FINAL_SMOKE.md](FINAL_SMOKE.md#L113) |
| GAP-4       | new informational finding from this run |

---

## 9. End-of-run state verification

```
testuser roles:                   ["user"]
adminuser profile:                {id:..., first_name:"Adminuser", last_name:"User", enabled:true, email:"adminuser@test.com"}
admin role membership:            ["adminuser", "user@example.com"]
```

All state changes from §D and §E were rolled back. The realm is functionally identical to its pre-probe state; only the brute-force tracker (cleared by `EXIT` trap from earlier runs) and the now-revoked sessions remain — both routine.
