# Security Validation v0.3 — Advanced Threat Probes

**Date:** 2026-05-20
**Status:** **5 PASS / 0 FAIL / 6 INFO** (3 findings worth tracking)
**Scope:** Black-box validation of six advanced threat surfaces (rate limiting, brute force, session fixation, token replay, concurrent admin actions, privilege escalation). Follows [SECURITY_VALIDATION_v0.2.md](SECURITY_VALIDATION_v0.2.md). Validation-only — no implementation touched.

Added artifacts (all outside `internal/**` and `web/**`):

- [scripts/security_advanced_check.sh](../scripts/security_advanced_check.sh) — runner.
- [docs/evidence/security/advanced/](evidence/security/advanced/) — per-probe evidence.
- [docs/evidence/security/advanced/summary.txt](evidence/security/advanced/summary.txt) — run roll-up.

---

## 1. How to read this report

| Result | Meaning |
|--------|---------|
| **PASS** | A guard exists, fired, and behaved as specified. |
| **FAIL** | A guard was expected, missing or weak. (None this run.) |
| **INFO** | An observed behavior with no claim of a guard — recorded so the next iteration can decide whether to harden. |

`INFO` is *not* a bug; it is a documented surface — e.g., the JWT model deliberately does not invalidate access tokens on logout. Three of this run's INFO entries are flagged in §10 as candidates for follow-up.

---

## 2. Stack state

```
saas-api                 Up                       0.0.0.0:8080->8080/tcp
saas-keycloak            Up 10 hours (healthy)    0.0.0.0:8081->8080/tcp
saas-keycloak-postgres   Up 10 hours (healthy)    0.0.0.0:5433->5432/tcp
saas-postgres            Up 10 hours (healthy)    0.0.0.0:5432->5432/tcp
```

`GET /health` → 200, realm discovery → 200, both `testuser`/`adminuser` tokens issued via Direct Access Grants.

---

## 3. T1 — Rate limiting *(INFO ×4)*

Bursts of identical requests fired in parallel; status-code histogram recorded.

| Probe | Count × parallelism | Dominant code | Evidence |
|-------|---------------------|---------------|----------|
| T1a `/me` with valid user token              | 100 × 50  | `100×200` | [T1a_burst_me_authed.txt](evidence/security/advanced/T1a_burst_me_authed.txt) |
| T1b `/admin/users` unauthenticated           | 50  × 50  | `50×401`  | [T1b_burst_admin_unauth.txt](evidence/security/advanced/T1b_burst_admin_unauth.txt) |
| T1c `/health`                                | 200 × 50  | `200×200` | [T1c_burst_health.txt](evidence/security/advanced/T1c_burst_health.txt) |
| T1d Keycloak token endpoint (bad creds)      | 50  × 50  | `50×401`  | [T1d_burst_kc_token.txt](evidence/security/advanced/T1d_burst_kc_token.txt) |

**Finding F1 (carried to §10).** No per-IP, per-token, or per-route rate limiting at the API tier — bursts of 100+ requests/sec are served without `429`, slowdowns, or backpressure. Keycloak's own token endpoint also did not throttle 50-parallel requests in this run. This is consistent with the current architecture (no rate-limit middleware claimed); recorded as a DoS surface.

---

## 4. T2 — Brute-force protection *(PASS ×2)*

The realm's `bruteForceProtected: true` is verified at runtime:

1. **Control.** Correct password before any failed attempts → `200`.
2. **Hammer.** 35 sequential POSTs to the token endpoint with `username=testuser` and `password=wrong-pw-1 … wrong-pw-35`. All 35 → `401 invalid_grant "Invalid user credentials"`. Evidence: [T2_brute_force_attempts.txt](evidence/security/advanced/T2_brute_force_attempts.txt).
3. **Lockout confirmed.** Immediately retry the **correct** password → `401 invalid_grant "Invalid user credentials"`. Same generic error as wrong-password (deliberate — Keycloak doesn't reveal the lockout state). Evidence: [T2_post_brute_correct_pw.txt](evidence/security/advanced/T2_post_brute_correct_pw.txt). **PASS T2.lockout.**
4. **Admin-recoverable.** `DELETE /admin/realms/saas/attack-detection/brute-force/users` via the master-realm admin token → `204`. Retry the correct password → `200`. **PASS T2.recovery.**

The runner unlocks all brute-force-tracked users on `EXIT` (trap), so an aborted run does not leave `testuser` disabled.

---

## 5. T3 — Session fixation / post-logout token reuse *(PASS ×1, INFO ×1)*

```
before_logout /me:    200    (expect 200)
logout endpoint http: 204    (RFC 7009 / OIDC end-session — refresh token presented)
after_logout  /me:    200    (JWT remains valid until 'exp')
refresh_token reuse:  has_access_token=no  error="Session not active"
```

Full evidence: [T3_post_logout_token_reuse.txt](evidence/security/advanced/T3_post_logout_token_reuse.txt).

- **PASS T3.refresh.** Post-logout, the refresh token is rejected by Keycloak with `Session not active`. An attacker who steals only a refresh token loses access at logout time.
- **INFO T3.access (Finding F2).** The bearer access token continues to authorize `/me` after logout — the API does not consult Keycloak's session store on each request. This is the standard JWT trade-off (stateless verification = no per-request revocation). The blast radius is bounded by `accessTokenLifespan` (3600 s in this realm). Defense-in-depth options for the next iteration: shorten the access-token lifespan, listen to Keycloak backchannel-logout events, or call the userinfo endpoint on sensitive verbs.

---

## 6. T4 — Token replay *(INFO ×1)*

A single valid bearer token used:

- 10 sequential `GET /me` calls → 10/10 `200` ([T4_replay_sequential.txt](evidence/security/advanced/T4_replay_sequential.txt)).
- 30 parallel `GET /me` calls → 30/30 `200` ([T4_replay_parallel.txt](evidence/security/advanced/T4_replay_parallel.txt)).

**Finding F3 (carried to §10).** Bearer JWTs have no `jti` revocation list, no per-request nonce, and no proof-of-possession (DPoP). A captured token is replayable for its full TTL by any holder, from any IP. This is expected for plain OAuth2 bearer tokens and matches the contract — recorded because it bounds what `RequireAuth` actually proves about the caller.

---

## 7. T5 — Concurrent admin actions *(PASS ×1)*

Fired 10 parallel `POST /admin/roles` with the same role name (`sec-adv-<epoch>-<pid>`). Expectation: exactly one creates, the rest collide on the unique `name` constraint.

```
## status-code distribution (count=10, parallelism=10):
   9 409
   1 201
```

Full evidence: [T5_concurrent_admin_roles.txt](evidence/security/advanced/T5_concurrent_admin_roles.txt). The runner deletes the role afterward (`DELETE /admin/roles/<name>` → `204`) to keep the realm clean.

**PASS T5.** Admin role creation is race-safe: no double-creates, no lost updates, no `500`s under contention. The same pattern would catch a logic-races regression introduced in the identity handler.

---

## 8. T6 — Privilege escalation *(PASS ×1)*

A non-admin user token (`testuser`, realm role `user`) was used to attempt every admin verb the router exposes, plus two escalation attacks. Every request was denied.

| Method  | Path                                                | Expected | Actual |
|---------|-----------------------------------------------------|----------|--------|
| GET     | /admin/roles                                        | 403      | 403    |
| GET     | /admin/users                                        | 403      | 403    |
| GET     | /admin/sessions                                     | 403      | 403    |
| GET     | /admin/invitations                                  | 403      | 403    |
| POST    | /admin/roles                                        | 403      | 403    |
| POST    | /admin/invitations                                  | 403      | 403    |
| PATCH   | /admin/users/&lt;uuid&gt;                           | 403      | 403    |
| PATCH   | /admin/roles/admin                                  | 403      | 403    |
| POST    | /admin/users/&lt;uuid&gt;/roles                     | 403      | 403    |
| POST    | /admin/users/&lt;uuid&gt;/reset-password            | 403      | 403    |
| DELETE  | /admin/users/&lt;uuid&gt;                           | 403      | 403    |
| DELETE  | /admin/users/&lt;uuid&gt;/roles/admin               | 403      | 403    |
| DELETE  | /admin/users/&lt;uuid&gt;/sessions                  | 403      | 403    |
| DELETE  | /admin/roles/admin                                  | 403      | 403    |
| GET     | /admin/users with `X-User-Role: admin` (+3 others)  | 403      | 403    |
| GET     | /admin/users with cross-client token (`saas-backend-admin`, client_credentials grant) | 401 | 401 |

Full matrix: [T6_privilege_escalation.txt](evidence/security/advanced/T6_privilege_escalation.txt).

Two notes worth recording:

- **Header injection is ignored.** `X-User-Role: admin`, `X-Forwarded-User: admin`, etc., have no effect — the gate consults only `realm_access.roles` from the verified JWT.
- **Cross-client tokens are rejected at auth, not at RBAC.** A `client_credentials` token minted for `saas-backend-admin` is refused with `401` (not `403`) because that client ID is not in `KEYCLOAK_ALLOWED_CLIENT_IDS=saas-backend,saas-dev-playground`. This means the API discriminates on `azp` *before* role checks — a stolen service-account token cannot impersonate a user-tier caller.

**PASS T6.**

---

## 9. Summary

```
TOTAL: 11   PASS: 5   FAIL: 0   INFO: 6
Result: PASS (no FAILs; 6 informational findings recorded)
```

| ID            | Surface              | Result | One-line |
|---------------|----------------------|--------|----------|
| T1a / T1b / T1c / T1d | rate limiting | INFO   | no rate-limit at API or KC token endpoint — Finding F1 |
| T2.lockout    | brute force          | PASS   | Keycloak locks the account after 30+ failures |
| T2.recovery   | brute force          | PASS   | admin can clear the lockout via attack-detection API |
| T3.refresh    | session/logout       | PASS   | refresh token invalidated post-logout |
| T3.access     | session/logout       | INFO   | access token still valid post-logout — Finding F2 |
| T4            | token replay         | INFO   | bearer JWT replayable until expiry — Finding F3 |
| T5            | concurrent admin     | PASS   | 1×201 / 9×409 on parallel role creation |
| T6            | privilege escalation | PASS   | 14 admin verbs + header injection + cross-client all denied |

---

## 10. Findings carried forward

| ID  | Severity | Surface          | Description | Suggested next step |
|-----|----------|------------------|-------------|---------------------|
| F1  | Medium   | Rate limiting    | No throttling on `/me`, `/admin/*`, `/health`, or the KC token endpoint from this caller's vantage. | Add an API-level rate-limit middleware (per-IP + per-`sub`) or front the API with one. |
| F2  | Low–Med  | Logout           | Access tokens remain valid for up to 3600 s after OIDC logout. | Shorten `accessTokenLifespan`, listen to Keycloak backchannel-logout, or hit `userinfo` on high-blast-radius verbs. |
| F3  | Low      | Token replay     | No DPoP / `jti` revocation; stolen token replayable until expiry. | Document explicitly as the OAuth2 bearer model; revisit if scope warrants DPoP or mTLS. |

These are *findings*, not failures — the runner exits `0` and the suite is `PASS`. They are tracked here so the next sprint can pick them up.

---

## 11. How to re-run

```sh
bash scripts/security_advanced_check.sh
```

Overrides (all optional):

```sh
API_URL=http://localhost:8080 \
KEYCLOAK_URL=http://localhost:8081 \
KEYCLOAK_REALM=saas \
KEYCLOAK_CLIENT_ID=saas-backend       KEYCLOAK_CLIENT_SECRET=saas-backend-secret \
KEYCLOAK_ADMIN_CLIENT_ID=saas-backend-admin \
KEYCLOAK_ADMIN_CLIENT_SECRET=saas-backend-admin-secret \
USER_USERNAME=testuser   USER_PASSWORD=password \
ADMIN_USERNAME=adminuser ADMIN_PASSWORD=password \
KEYCLOAK_ADMIN=admin     KEYCLOAK_ADMIN_PASSWORD=admin \
EVIDENCE_DIR=docs/evidence/security/advanced \
  bash scripts/security_advanced_check.sh
```

Exit codes: `0` = no FAILs (INFO entries allowed), `1` = at least one PASS-check failed, `2` = stack unreachable or master admin token not obtainable.

---

## 12. Safety notes

- **T2 will lock `testuser`.** The runner clears the lockout on `EXIT` (trap) via `DELETE /admin/realms/saas/attack-detection/brute-force/users`. If the runner is killed with `-9` before the trap fires, run `make realm-reset` or call that DELETE manually with the master admin token to recover.
- **T5 creates and deletes a role with a unique name** (`sec-adv-<epoch>-<pid>`). If the cleanup DELETE fails for any reason, the role remains in the realm — search by prefix `sec-adv-` and delete manually.
- **The evidence directory is overwritten on each run.** Token strings used in T6's cross-client probe are short-lived; the runner does not write them to disk. The advanced suite leaks no long-lived secrets.

---

## 13. Result

```
TOTAL: 11   PASS: 5   FAIL: 0   INFO: 6
Result: PASS
```

All five guards behave as specified. Three findings (F1, F2, F3) recorded for hardening consideration; none are regressions against v0.2's contract.
