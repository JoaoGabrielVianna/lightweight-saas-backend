# Secret Rotation Guide

Every secret in this stack has a documented owner, a documented blast radius, and a documented rotation procedure. This file is the operator's runbook.

> Use [SECRETS_MANAGEMENT.md](SECRETS_MANAGEMENT.md) for the *what each secret is*. This file is the *how to change it*.

---

## Rotation cadence

| Type                              | Scheduled cadence | Emergency trigger                     |
| --------------------------------- | ----------------- | ------------------------------------- |
| Keycloak confidential client secrets | Quarterly      | Suspected leak, departed maintainer, log/CI exposure |
| Keycloak admin password           | Quarterly        | Same as above                         |
| Postgres passwords (app + KC)     | Semi-annually    | Same as above                         |
| Service account credentials       | Quarterly        | Same as above                         |
| TLS certificates                  | At expiry (≤90 d) | Compromised key, CA distrust          |
| JWKS signing keys (Keycloak realm) | Annually + on incident | Suspected private-key exposure  |
| SMTP relay credentials            | Annually         | Email-platform-side rotation          |

**Hard rules:**
1. Always rotate via "add new → cut over → remove old" — never by replacing in place. Atomic replacement breaks in-flight requests and burns the audit trail.
2. Record every rotation in an audit log entry (the audit subsystem records who/when via [internal/audit/event.go](../../internal/audit/event.go) — emit a `secret.rotated` action from your runbook script, or log it in your change-management system).
3. After rotation, run the post-rotation verification (§ 9) before considering the rotation complete.

---

## Secret inventory (rotation lookup)

| Secret                         | Stored in (prod)        | Read by                                   | Section |
| ------------------------------ | ----------------------- | ----------------------------------------- | ------- |
| `POSTGRES_PASSWORD`            | Secret store → env       | API → Postgres                            | § 1     |
| `KC_DB_PASSWORD`               | Secret store → env       | Keycloak → its Postgres                   | § 2     |
| `KEYCLOAK_ADMIN_PASSWORD`      | Secret store → env (boot)| Operator login to Keycloak Admin UI       | § 3     |
| `KEYCLOAK_CLIENT_SECRET`       | Secret store → env       | API token validation client (`saas-backend`) | § 4 |
| `KEYCLOAK_ADMIN_CLIENT_SECRET` | Secret store → env       | API → Keycloak Admin REST (`saas-backend-admin`) | § 5 |
| Realm signing keys (RS256/ES256) | Inside Keycloak DB      | Signs every JWT this API accepts          | § 6     |
| TLS certificate (edge)         | Secret store / cert mgr  | Reverse proxy                             | § 7     |
| TLS certificate (Keycloak)     | Secret store / cert mgr  | Keycloak HTTPS listener                   | § 7     |
| SMTP relay password            | Keycloak realm SMTP config | Keycloak → SMTP relay                    | § 8     |

---

## § 1 — Rotate `POSTGRES_PASSWORD` (app DB)

**Blast radius:** API → application Postgres. The API will 5xx on every DB call until the new password is loaded.

**Zero-downtime procedure:**

1. Pick a new password: `openssl rand -base64 32 | tr -d '/+=' | head -c 32`.
2. In Postgres, **create a second role** with the new password rather than alter the existing one:
   ```sql
   CREATE ROLE postgres_v2 WITH LOGIN PASSWORD '<new>' IN ROLE postgres;
   GRANT ALL PRIVILEGES ON DATABASE lightweight_saas_backend_db TO postgres_v2;
   ```
3. Push the new password + new user into your secret store as `POSTGRES_USER_NEXT` / `POSTGRES_PASSWORD_NEXT`.
4. Update `DB_URL` env on the API to use the new user. Roll the API (rolling restart, one replica at a time).
5. Verify all replicas are using the new credentials (`SELECT usename, application_name, state FROM pg_stat_activity WHERE datname = 'lightweight_saas_backend_db';`).
6. `DROP ROLE postgres;` (or rename `postgres_v2 → postgres` if you prefer the canonical name back).
7. Update the secret store: promote `POSTGRES_PASSWORD_NEXT` → `POSTGRES_PASSWORD`, delete the `_NEXT` entry.

**Single-node procedure** (acceptable downtime ≤ 30 s):

1. `ALTER ROLE postgres WITH PASSWORD '<new>';`
2. Update env, restart API.

---

## § 2 — Rotate `KC_DB_PASSWORD` (Keycloak DB)

**Blast radius:** Keycloak → its Postgres. While stale, Keycloak rejects every token (because it can't read its own DB). Schedule a maintenance window or use the zero-downtime procedure.

Same shape as § 1 but against the Keycloak Postgres role and the Keycloak container env.

---

## § 3 — Rotate `KEYCLOAK_ADMIN_PASSWORD`

**Blast radius:** breaks the bootstrap admin login until rotated. Does NOT affect tokens, the API, or end users.

**Procedure:**

1. Log into Keycloak Admin UI as the current admin.
2. Create a second admin user, log out, log back in as the second admin.
3. Reset the password of the original `admin` user (or delete it if you'd rather operate as named humans).
4. Update the secret-store entry. Note: `KEYCLOAK_ADMIN_PASSWORD` is only honored on **first boot** of a fresh Keycloak instance — once Keycloak has bootstrapped, the env var is ignored. The secret-store entry exists for disaster recovery / re-bootstrap only.

---

## § 4 — Rotate `KEYCLOAK_CLIENT_SECRET` (`saas-backend`)

**Blast radius:** API → Keycloak for any client_credentials or refresh-token flow on this client. Token *validation* uses JWKS, not the client secret, so existing user JWTs continue to validate.

> Note: today the API only uses this client for `iss`/`azp` matching and does not perform client_credentials grants on it. Verify by grepping for `KeycloakClientSecret` in `internal/` — at audit time, this is reserved for future Admin API calls ([internal/auth/keycloak/config.go:21](../../internal/auth/keycloak/config.go#L21)).

**Procedure:**

1. Keycloak Admin → Clients → `saas-backend` → Credentials → **Regenerate Secret**.
2. Copy the new secret into your secret store as `KEYCLOAK_CLIENT_SECRET_NEXT`.
3. Roll the API with `KEYCLOAK_CLIENT_SECRET=<new>`.
4. Promote `_NEXT` → primary, delete `_NEXT`.

Keycloak supports **client secret rotation with grace period**: in the Credentials tab, you can hold both the new and rotated-out secret valid simultaneously for a configurable window — use this if you have many API replicas.

---

## § 5 — Rotate `KEYCLOAK_ADMIN_CLIENT_SECRET` (`saas-backend-admin`)

**Blast radius:** every `/admin/*` endpoint in this API goes 5xx until rotated. The live-admin check ([internal/auth/admin_check.go:197](../../internal/auth/admin_check.go#L197)) fails closed on Keycloak Admin REST errors, so admin verbs cannot run with stale credentials — by design.

**Procedure:**

1. Keycloak Admin → Clients → `saas-backend-admin` → Credentials → **Regenerate Secret**.
2. Push new secret to secret store.
3. **Drain admin traffic** if any operator is mid-session; broadcast a 1-minute heads-up.
4. Roll the API; observe `/admin/users` returns 200 once a replica picks up the new secret.
5. Expire any admin sessions that started before the rotation (Keycloak Admin → Sessions → Logout all).

The cached admin checker ([internal/auth/admin_check.go:79](../../internal/auth/admin_check.go#L79)) holds positive answers for 30 s. The rotation will cause one cycle of `IsAdmin` failures (cache misses go to the live upstream); after the new secret is loaded, the next call repopulates.

---

## § 6 — Rotate Keycloak realm signing keys

**Blast radius:** every JWT issued under the old key continues to validate (Keycloak publishes both keys in JWKS during overlap), then becomes invalid once the old key is dropped. The API's JWKS client picks up the rotation automatically:
- scheduled refresh: hourly ([internal/auth/keycloak/jwks.go:38](../../internal/auth/keycloak/jwks.go#L38))
- on-demand refresh on unknown `kid`: throttled to 1 / 30 s, burst 2 ([internal/auth/keycloak/jwks.go:59](../../internal/auth/keycloak/jwks.go#L59))

**Scheduled procedure:**

1. Keycloak Admin → Realm settings → Keys → Providers → **Add provider** (rsa-generated or rsa-enc-generated, RS256).
2. Set priority **higher** than the current active key. The new key becomes the default signer; the old key stays in JWKS for verification.
3. Wait until `accessTokenLifespan` (default 10 m in our production profile, see [PRODUCTION_DEPLOYMENT.md](../operations/PRODUCTION_DEPLOYMENT.md#2-harden-the-keycloak-realm-export)) has elapsed twice — every in-flight token has been refreshed.
4. Disable the old key (do not delete yet). Wait 24 h.
5. Delete the old key provider.

**Emergency (key suspected leaked):**

Same as above, but at step 3 do **not** wait — at step 4, immediately revoke all sessions (Realm settings → Sessions → Logout all). Every client must re-authenticate.

---

## § 7 — Rotate TLS certificates

**Edge cert (reverse proxy):** Use ACME (`cert-manager`, Caddy auto-TLS, Let's Encrypt + certbot). These rotate automatically before the 30-day-to-expiry threshold. Verify the automation works by setting alerts on expiry < 21 d.

**Keycloak TLS:** if you're terminating TLS at Keycloak (rare in our recommended topology — usually edge proxy handles it), mount certs from a managed secret store and `kc.sh` reads them at startup. On rotation, restart Keycloak.

---

## § 8 — Rotate SMTP relay password

**Blast radius:** Keycloak's invitation, password-reset, and verify-email mails fail silently until rotated. Users mid-invitation flow get no email.

**Procedure:**

1. Rotate the password in your SMTP relay's console (SES, SendGrid, etc.).
2. Keycloak Admin → Realm settings → Email → update password → **Test connection**.
3. Send a real invitation through the API (`POST /admin/invitations`) and confirm receipt.

---

## § 9 — Post-rotation verification (required for every rotation)

Run these after **every** secret rotation, regardless of which one:

```bash
# 1. API still serves /health
curl -fsS https://app.your-domain.com/health

# 2. A real bearer token still validates
TOKEN=$(./scripts/get-token.sh prod-admin)   # your token script
curl -fsS -H "Authorization: Bearer $TOKEN" https://app.your-domain.com/me

# 3. Admin endpoint still validates AND live-admin check still passes
curl -fsS -H "Authorization: Bearer $TOKEN" https://app.your-domain.com/admin/users | jq '.[] | .username' | head

# 4. JWKS endpoint is reachable from the API process (for key rotations)
docker exec saas-api wget -qO- "$KEYCLOAK_JWKS_URL" | jq '.keys[].kid'

# 5. Audit trail recorded the rotation
journalctl -u saas-api --since '5 minutes ago' | grep '^audit ' | tail

# 6. No new authentication failures (compare against baseline)
journalctl -u saas-api --since '15 minutes ago' | grep 'denied kind=validation_failed' | wc -l
```

If any check fails, **roll back** by re-deploying with the previous secret from your secret-store version history, then investigate.

---

## § 10 — Compromise-driven (emergency) rotation

If you suspect a secret has leaked (committed to git, in a CI log, on a departed maintainer's machine):

1. **Within 10 minutes:** rotate the affected secret using the procedure above, in fast-cutover mode (not zero-downtime).
2. **Within 30 minutes:** rotate every secret in the same trust boundary:
   - Keycloak client secret leaked → also rotate the admin client secret (they sit on the same Keycloak instance).
   - DB password leaked → also rotate any service-account credential with DB access.
   - JWKS private key leaked → rotate keys **and** revoke all sessions.
3. **Within 60 minutes:** open the incident in [INCIDENT_RESPONSE.md](../operations/INCIDENT_RESPONSE.md) workflow.
4. **Within 24 hours:** complete the IR retrospective and post the root-cause to the team.

---

## Rotation checklist (paste into your ticket)

```
[ ] New secret generated (provenance: openssl rand / vault / KMS)
[ ] New secret stored in <secret store> with version annotation
[ ] Stakeholders notified (#ops, on-call)
[ ] Change window scheduled (if not zero-downtime)
[ ] Rotation executed
[ ] Post-rotation verification (§ 9) — all checks PASS
[ ] Old secret revoked / removed from store
[ ] Audit log entry recorded
[ ] Runbook updated if procedure changed
```
