# Security Validation v0.2 — Live Guard Audit

**Date:** 2026-05-20
**Status:** **PASS** (17/17 guards)
**Scope:** Black-box validation of the live API's auth, RBAC, and path-traversal guards against the running `docker-compose` stack. No code under `internal/**` or `web/**` was modified — this report only adds the runner (`scripts/security_live_check.sh`) and its evidence (`docs/evidence/security/**`).

---

## 1. What this validation covers

Each guard is exercised against the live stack from outside the process — i.e. nothing in this run trusts source code, only HTTP responses. The runner:

1. Confirms `/health` and the Keycloak realm discovery doc are reachable.
2. Acquires a non-admin token (`testuser`) and an admin token (`adminuser`) via the realm's Direct Access Grants flow.
3. Issues 17 HTTP probes covering: public surfaces, missing/malformed Authorization headers, tampered JWT signatures, valid-token happy paths, role-gated routes, mutating verbs, and `/admin/static` path-traversal.
4. Records each probe's full response headers + body to `docs/evidence/security/checks/G<NN>.txt`.
5. Writes a roll-up to `docs/evidence/security/summary.txt` and exits non-zero on any mismatch.

The runner is idempotent — re-running it overwrites the evidence directory and re-fetches fresh tokens.

---

## 2. Stack state at validation time

```
saas-api                 Up 43 minutes           0.0.0.0:8080->8080/tcp
saas-keycloak            Up 10 hours (healthy)   0.0.0.0:8081->8080/tcp
saas-keycloak-postgres   Up 10 hours (healthy)   0.0.0.0:5433->5432/tcp
saas-postgres            Up 10 hours (healthy)   0.0.0.0:5432->5432/tcp
```

`GET /health` → `200`, `GET http://localhost:8081/realms/saas/.well-known/openid-configuration` → `200`. Tokens were issued by the `saas` realm via `saas-backend` (Direct Access Grants).

---

## 3. Guards exercised

| ID  | Probe                                                         | Expected | Actual | Result |
|-----|---------------------------------------------------------------|---------:|-------:|--------|
| G01 | `GET /health` — public                                        |      200 |    200 | PASS   |
| G02 | `GET /swagger/index.html` — public                            |      200 |    200 | PASS   |
| G03 | `GET /admin` — public HTML shell (actions still gated)        |      200 |    200 | PASS   |
| G04 | `GET /me` without Authorization                               |      401 |    401 | PASS   |
| G05 | `GET /admin/users` without Authorization                      |      401 |    401 | PASS   |
| G06 | `GET /me` with empty Bearer token                             |      401 |    401 | PASS   |
| G07 | `GET /me` without `Bearer ` prefix                            |      401 |    401 | PASS   |
| G08 | `GET /me` with non-JWT garbage token                          |      401 |    401 | PASS   |
| G09 | `GET /me` with tampered JWT signature                         |      401 |    401 | PASS   |
| G10 | `GET /me` with valid user token                               |      200 |    200 | PASS   |
| G11 | `GET /admin/users` with user (no admin role) → forbidden      |      403 |    403 | PASS   |
| G12 | `GET /admin/users` with admin token                           |      200 |    200 | PASS   |
| G13 | `GET /admin/static/../../../etc/passwd` (path traversal)      |      403 |    403 | PASS   |
| G14 | `DELETE /admin/users/1` without Authorization                 |      401 |    401 | PASS   |
| G15 | `POST /admin/roles` without Authorization                     |      401 |    401 | PASS   |
| G16 | `POST /admin/roles` with user (no admin role) → forbidden     |      403 |    403 | PASS   |
| G17 | `GET /me` with malformed single-segment token                 |      401 |    401 | PASS   |

**Totals: 17 PASS / 0 FAIL.** Raw per-probe evidence (headers + body) in [docs/evidence/security/checks/](evidence/security/checks/); roll-up in [docs/evidence/security/summary.txt](evidence/security/summary.txt).

---

## 4. What the evidence shows

A few responses worth highlighting (see the linked evidence files for full headers + body):

- **Auth failures return a single, fixed error body.** Both missing headers (G04) and forged signatures (G09) respond with `{"error":"unauthorized"}` and status `401`. The middleware does *not* leak the specific reason to the wire — the precise cause is only emitted via the structured `AuthEvent` stream observed at the server, which is the documented contract in [internal/auth/middleware.go](../internal/auth/middleware.go#L26-L27).
- **RBAC denials are distinct from auth failures.** A valid-but-underprivileged token (G11, G16) is answered with `403 {"error":"forbidden"}`, not `401`. Clients can therefore differentiate "log in again" from "you don't have access."
- **Path traversal is short-circuited before disk I/O.** G13's response is a `403` with `Content-Length: 0` — the handler refuses the request as soon as `..` is detected, never reaching `filepath.Join`. The check uses `curl --path-as-is` so the traversal segments actually leave the client.
- **The HTML shell at `/admin` is intentionally public (G03).** It is a single-page bootstrap; every action it invokes goes through the gated `/admin/*` API surface, which G05/G11/G12/G14/G15/G16 cover.

---

## 5. How to re-run

```sh
# stack must already be up (docker compose up -d)
bash scripts/security_live_check.sh

# overrides (all optional):
API_URL=http://localhost:8080 \
KEYCLOAK_URL=http://localhost:8081 \
KEYCLOAK_REALM=saas \
KEYCLOAK_CLIENT_ID=saas-backend \
KEYCLOAK_CLIENT_SECRET=saas-backend-secret \
USER_USERNAME=testuser     USER_PASSWORD=password \
ADMIN_USERNAME=adminuser   ADMIN_PASSWORD=password \
EVIDENCE_DIR=docs/evidence/security \
  bash scripts/security_live_check.sh
```

The runner exits `0` on PASS, `1` on any guard mismatch, `2` if the stack is unreachable or token acquisition fails. Suitable for wiring into CI once the stack is reachable from the runner.

---

## 6. What this validation does **not** cover

Out of scope for v0.2 (recorded so the next iteration can pick them up):

- **Token expiration and clock skew.** Forging an expired token requires either waiting an hour or signing one — neither was performed here. The Keycloak `accessTokenLifespan` is 3600s.
- **Token-`aud` / `azp` enforcement** against unexpected client IDs. The middleware accepts the listed `KEYCLOAK_ALLOWED_CLIENT_IDS`; cross-client tokens were not tested in this run.
- **Replay / nonce semantics.** No `nonce`/`jti` invalidation is implemented (none is claimed); a stolen bearer token is valid until expiry.
- **Rate limiting / brute-force.** Keycloak's `bruteForceProtected` is enabled at the realm level (see [docs/VALIDATION_PHASE3.md](VALIDATION_PHASE3.md#L42)), but the API has no per-IP throttling and that surface was not probed.
- **CORS, CSP, secure cookies, HSTS.** No HTTP security headers were inspected here.
- **Dependency / supply-chain audit.** This is a runtime-guard validation only; static analysis (`govulncheck`, etc.) is separate.

---

## 7. Notes on evidence hygiene

`docs/evidence/security/checks/G07.txt`, `G09.txt`, `G10.txt`, `G11.txt`, `G12.txt`, `G16.txt`, `G17.txt` contain the access tokens used during the probe in their `curl_args:` lines. These are short-lived (1h TTL) tokens issued for the dev seed users `testuser` / `adminuser` against a local Keycloak whose admin credentials are themselves `admin/admin` (see [.env.example](../.env.example)) — i.e. there is no production credential exposure. Re-running the script overwrites this directory with fresh tokens; if the evidence is checked into a public repo, rotate the seed users (`make realm-reset` or `docker volume rm`) before publishing.

---

## 8. Result

```
TOTAL: 17   PASS: 17   FAIL: 0
Result: PASS
```

All 17 guards behave as specified.
