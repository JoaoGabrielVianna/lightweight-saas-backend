# FINAL SECURITY — Consolidated Security Gate

**Date:** 2026-05-20
**Tester:** Agent D (Security Tester)
**Target:** `milestone/auth-v1` against the live `docker-compose` stack
**Scope:** All security gates — auth guards (v0.2), advanced threat probes (v0.3), and re-validation under the v0.4 final QA pass.
**Verdict:** **GO**

This report consolidates [SECURITY_VALIDATION_v0.2.md](SECURITY_VALIDATION_v0.2.md) (17 guard probes), [SECURITY_VALIDATION_v0.3.md](SECURITY_VALIDATION_v0.3.md) (6-surface advanced probes), and the v0.4 re-runs Agent D executed today. No implementation was modified — only validation artifacts were produced.

---

## 1. Top-line numbers

| Category                  | Suite                              | Probes | PASS | FAIL | INFO |
|---------------------------|------------------------------------|-------:|-----:|-----:|-----:|
| **Auth & RBAC guards**    | `security_live_check.sh` (v0.2)    |    17  |  17  |   0  |   —  |
| **Advanced threats**      | `security_advanced_check.sh` (v0.3)|    11  |   5  |   0  |   6  |
| **Path traversal**        | covered in live (G13)              |    1   |   1  |   0  |   —  |
| **Concurrency / races**   | covered in advanced (T5)           |    10  |   1* |   0  |   —  |
| **Total**                 |                                     |   **28**| **22** | **0** | **6** |

\* T5 fires 10 concurrent requests; observed outcome = 1×201 + 9×409 = 1 verdict (race-safe).

**Zero FAILs** across all 28 probes. The 6 INFO entries are recorded findings (no claim of a guard exists for them) — not regressions.

---

## 2. Stack state at validation time

```
saas-api                 Up                       0.0.0.0:8080->8080/tcp
saas-keycloak            Up 25 minutes (healthy)  0.0.0.0:8081->8080/tcp
saas-keycloak-postgres   Up 25 minutes (healthy)  0.0.0.0:5433->5432/tcp
saas-postgres            Up 25 minutes (healthy)  0.0.0.0:5432->5432/tcp
```

Realm `saas` has `bruteForceProtected: true` (verified at runtime in T2). Allowed clients: `saas-backend,saas-dev-playground` (verified by T6 cross-client probe rejecting `saas-backend-admin`). Tokens issued via Direct Access Grants for the recon, PKCE for the SPA flows.

---

## 3. Auth & RBAC guards (v0.2 re-run) — 17/17 PASS

Evidence: [docs/evidence/final/security/live_check.log](../evidence/final/security/live_check.log), per-probe headers+body in [docs/evidence/final/security/live_checks/](../evidence/final/security/live_checks) (G01–G17).

| ID  | Probe                                                              | Expected | Actual | Result |
|-----|--------------------------------------------------------------------|---------:|-------:|--------|
| G01 | `GET /health` (public)                                             |      200 |    200 | PASS   |
| G02 | `GET /swagger/index.html` (public)                                 |      200 |    200 | PASS   |
| G03 | `GET /admin` HTML shell (public; actions still gated)              |      200 |    200 | PASS   |
| G04 | `GET /me` no Authorization                                         |      401 |    401 | PASS   |
| G05 | `GET /admin/users` no Authorization                                |      401 |    401 | PASS   |
| G06 | `GET /me` empty Bearer                                             |      401 |    401 | PASS   |
| G07 | `GET /me` Authorization w/o `Bearer ` prefix                       |      401 |    401 | PASS   |
| G08 | `GET /me` non-JWT garbage token                                    |      401 |    401 | PASS   |
| G09 | `GET /me` tampered JWT signature                                   |      401 |    401 | PASS   |
| G10 | `GET /me` valid user token                                         |      200 |    200 | PASS   |
| G11 | `GET /admin/users` user (no admin role)                            |      403 |    403 | PASS   |
| G12 | `GET /admin/users` admin token                                     |      200 |    200 | PASS   |
| G13 | `GET /admin/static/../../../etc/passwd` (`--path-as-is`)           |      403 |    403 | PASS   |
| G14 | `DELETE /admin/users/1` no Authorization                           |      401 |    401 | PASS   |
| G15 | `POST /admin/roles` no Authorization                               |      401 |    401 | PASS   |
| G16 | `POST /admin/roles` user (no admin role)                           |      403 |    403 | PASS   |
| G17 | `GET /me` single-segment token                                     |      401 |    401 | PASS   |

Notable confirmations:

- Auth failures return the same opaque `{"error":"unauthorized"}` — no leak of *why* a token failed validation.
- RBAC denials return `403 {"error":"forbidden"}` — distinguishable from auth failures, so SPA clients can branch "log in again" vs "you don't have access."
- `/admin/static/..` traversal is refused with `403 Content-Length: 0` *before* any disk I/O reaches `filepath.Join`.

---

## 4. Advanced threat probes (v0.3 re-run) — 5 PASS / 0 FAIL / 6 INFO

Evidence: [docs/evidence/final/security/advanced_check.log](../evidence/final/security/advanced_check.log), per-test detail in [docs/evidence/final/security/advanced_checks/](../evidence/final/security/advanced_checks) (T1a–T6).

### 4.1 T1 — Rate limiting *(INFO ×4)*

Bursts of identical requests against `/me`, `/admin/users`, `/health`, and Keycloak's token endpoint.

| Probe | Count × parallel | Dominant code |
|-------|------------------|---------------|
| T1a `/me` with valid token            | 100 × 50 | `100×200` |
| T1b `/admin/users` unauthenticated    | 50  × 50 | `50×401`  |
| T1c `/health`                         | 200 × 50 | `200×200` |
| T1d Keycloak token endpoint bad creds | 50  × 50 | `50×401`  |

**No `429`, no slowdown, no backpressure.** Documented as Finding F1 below.

### 4.2 T2 — Brute force *(PASS ×2)*

1. Control: correct password before brute force → `200`.
2. 35 sequential `password=wrong-pw-i` POSTs → all `401`.
3. Correct password retry **after** lockout → `401 invalid_grant "Invalid user credentials"` (same opaque error; no lockout leak).
4. `DELETE /admin/realms/saas/attack-detection/brute-force/users` via master admin → `204`. Correct password retry → `200`.

**PASS T2.lockout** (Keycloak's `bruteForceProtected` actively locks). **PASS T2.recovery** (admin-driven recovery works).

### 4.3 T3 — Session fixation / post-logout token reuse *(PASS ×1, INFO ×1)*

```
before_logout /me:    200    (expect 200)
logout endpoint http: 204    (RFC 7009 / end-session)
after_logout  /me:    200    (JWT remains valid until 'exp' — Finding F2)
refresh_token reuse:  has_access_token=no  error="Session not active"
```

- **PASS T3.refresh** — refresh token is invalidated server-side on logout.
- **INFO T3.access** — stateless JWT trade-off; bounded by `accessTokenLifespan=3600s`.

### 4.4 T4 — Token replay *(INFO)*

10 sequential + 30 parallel requests of the same valid token against `/me` — **all 200**. Documented as the expected OAuth2 bearer-token contract (no DPoP, no `jti` revocation). **Finding F3.**

### 4.5 T5 — Concurrent admin actions *(PASS)*

10 parallel `POST /admin/roles` with the same name (`sec-adv-<epoch>-<pid>`):

```
9 × 409
1 × 201
```

Exactly the expected race-safe outcome — one creator, nine conflict-rejected. Role cleaned up afterward (`DELETE` → 204).

### 4.6 T6 — Privilege escalation *(PASS)*

14 admin verbs attempted with a non-admin user token + header-injection + cross-client client_credentials token. **All denied:**

- 14 × `403 {"error":"forbidden"}` for the admin verbs.
- `GET /admin/users` with `X-User-Role: admin` (and 3 other claim-injection headers) → `403`.
- `GET /admin/users` with a `saas-backend-admin` service-account token (azp not in `KEYCLOAK_ALLOWED_CLIENT_IDS`) → `401` (rejected at auth, not RBAC — so a stolen service-account token cannot be silently downgraded to user-tier impersonation).

---

## 5. Findings carried forward (informational — NOT failures)

These three findings carry over from v0.3 and are confirmed re-observable in v0.4. None are regressions; none block ship.

| ID  | Severity | Surface       | Description | Suggested follow-up |
|-----|----------|---------------|-------------|---------------------|
| F1  | Medium   | Rate limiting | No per-IP, per-`sub`, or per-route throttling on `/me`, `/admin/*`, `/health`, or the Keycloak token endpoint. DoS surface. | Add an API-level rate-limit middleware (per-IP + per-`sub`), or front the API with one. |
| F2  | Low–Med  | Logout        | Access tokens remain valid for up to `accessTokenLifespan` (3600 s) after OIDC end-session. | Shorten `accessTokenLifespan`, subscribe to Keycloak backchannel-logout, or hit `/userinfo` on high-blast-radius verbs. |
| F3  | Low      | Token replay  | Bearer JWTs are replayable until `exp`; no DPoP / `jti` revocation. Matches the documented OAuth2 model. | Note explicitly in the contract; revisit if regulatory scope warrants DPoP or mTLS. |

A fourth finding worth carrying:

| ID  | Severity | Surface          | Description | Suggested follow-up |
|-----|----------|------------------|-------------|---------------------|
| F4  | Low      | Realm-wide bulk session terminate | UI shows a `coming-soon` badge on the `Terminate all sessions` button; no backend route. | Implement `DELETE /admin/sessions` (realm-wide) or remove the disabled placeholder. (Tracked in FINAL_SMOKE.md as FS-1.) |

---

## 6. What WAS NOT tested

For honest scope-keeping:

- **Token expiry / clock skew.** Requires waiting an hour or signing tokens externally. Out of v0.4 scope.
- **Cross-realm token acceptance.** Cannot mint a token from a different OIDC IdP from the local dev stack.
- **HTTP security headers** (`CSP`, `HSTS`, `X-Frame-Options`, secure cookies). Not exercised this run.
- **SAST / supply-chain audit.** Runtime-guard validation only; `govulncheck` and dependency review are separate gates.
- **Sustained DoS / long-duration brute force.** Each probe is bounded so the suite finishes in <2 min.

---

## 7. Evidence inventory

```
docs/evidence/final/security/
├── live_check.log          # 17/17 PASS summary
├── live_summary.txt        # same, written by the runner
├── live_checks/            # symlink → docs/evidence/security/checks/  (G01–G17 .txt)
├── advanced_check.log      # 5 PASS / 0 FAIL / 6 INFO
├── advanced_summary.txt    # same, written by the runner
└── advanced_checks/        # symlink → docs/evidence/security/advanced/  (T1a–T6 .txt)
```

Each per-probe file contains the exact `curl` arguments, the HTTP response headers, and the body (truncated to 2 KB). Tokens used in T6 / G09 / G10 / G11 / G12 are short-lived (1 h TTL) and against the local Keycloak whose master credentials are `admin/admin` from [.env.example](../../.env.example) — no production exposure. Re-running the suite overwrites the evidence with fresh tokens.

---

## 8. How to re-run

```sh
bash scripts/security_live_check.sh         # 17 probes, ~5 s, exit 0
bash scripts/security_advanced_check.sh     # 6 surfaces, ~60 s, exit 0
```

Both scripts exit `0` only when there are no FAILs. INFO findings do not flip the exit code. T2 deliberately locks `testuser`; the runner clears the lockout on EXIT via the master admin API.

---

## 9. Verdict

```
TOTAL PROBES: 28        PASS: 22        FAIL: 0       INFO: 6
GO/NO-GO:     GO
```

All guards behave as specified. The four findings (F1–F4) are tracked for hardening but do not block ship — they are either documented JWT trade-offs (F2, F3), a missing-but-known feature (F4), or an additive defense-in-depth measure (F1).

Pair with [FINAL_SMOKE.md](../release/FINAL_SMOKE.md) for the functional gate — combined verdict: **GO**.
