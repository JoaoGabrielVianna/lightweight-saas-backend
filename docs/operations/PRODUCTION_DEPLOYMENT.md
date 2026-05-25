# Production Deployment Guide

This is the **V1 production-ready** procedure. Following every step is required — none of them are optional hardening. The project ships safe-for-localhost defaults; production requires the changes below.

> **Audience**: an operator deploying the API + Keycloak + Postgres stack into an environment exposed to the public internet (or any untrusted network).

---

## 0. Pre-flight checklist

Confirm before you start:

- [ ] You have a domain with DNS pointed at your edge.
- [ ] You have a TLS certificate (Let's Encrypt, ACM, or internal CA) issued for that domain.
- [ ] You have a managed PostgreSQL instance (or a hardened self-hosted one) with TLS enabled.
- [ ] You have an SMTP relay (SES, SendGrid, Postmark, or internal) with credentials.
- [ ] You have a secret store: AWS Secrets Manager, Vault, GCP Secret Manager, or 1Password Connect — **not** a `.env` file checked into git.
- [ ] You have a reverse proxy in front of the API: nginx, Caddy, Traefik, an L7 LB (ALB/GCLB), or an API gateway.
- [ ] You have outbound network egress to GitHub Container Registry / Docker Hub for image pulls (or a mirror).

---

## 1. Generate fresh secrets — do NOT use bootstrap defaults

The defaults in `internal/bootstrap/generate.go` (`admin/admin`, `saas-backend-secret`, `saas-backend-admin-secret`, `password`) are for local development only.

Generate fresh values:

```bash
# 32-byte URL-safe random for client secrets
openssl rand -base64 32

# 24-char alphanumeric for admin / DB passwords
openssl rand -base64 24 | tr -d '/+=' | head -c 32
```

Store each in your secret store. **Required secrets:**

| Logical name                  | Env var                          | Used by                |
| ----------------------------- | -------------------------------- | ---------------------- |
| App DB password               | `POSTGRES_PASSWORD`              | API → Postgres         |
| App DB user                   | `POSTGRES_USER`                  | API → Postgres         |
| Keycloak DB password          | `KC_DB_PASSWORD`                 | Keycloak → its Postgres|
| Keycloak bootstrap admin pwd  | `KEYCLOAK_ADMIN_PASSWORD`        | Keycloak boot          |
| API confidential client secret| `KEYCLOAK_CLIENT_SECRET`         | API → Keycloak         |
| Identity admin client secret  | `KEYCLOAK_ADMIN_CLIENT_SECRET`   | API admin client       |
| SMTP relay password           | (Keycloak realm SMTP config)     | Keycloak → SMTP relay  |

Full inventory: [SECRETS_MANAGEMENT.md](../security/SECRETS_MANAGEMENT.md).

---

## 2. Harden the Keycloak realm export

Take a copy of `deploy/keycloak/realm-export.json` named `realm-export.prod.json` and apply these changes before importing:

```diff
- "accessTokenLifespan": 3600,
+ "accessTokenLifespan": 600,
+ "ssoSessionIdleTimeout": 1800,
+ "ssoSessionMaxLifespan": 28800,

- "sslRequired": "none",
+ "sslRequired": "external",

+ "passwordPolicy": "length(12) and notUsername and specialChars(1) and upperCase(1) and digits(1) and passwordHistory(5)",

  "bruteForceProtected": true,
+ "permanentLockout": false,
+ "maxFailureWaitSeconds": 900,
+ "minimumQuickLoginWaitSeconds": 60,
+ "waitIncrementSeconds": 60,
+ "quickLoginCheckMilliSeconds": 1000,
+ "maxDeltaTimeSeconds": 43200,
+ "failureFactor": 10,
```

**Remove the seeded test users.** Delete the entire `users` array except the service account:

```json
"users": [
  {
    "clientRoles": { "realm-management": ["manage-users", "view-users", "manage-clients"] },
    "emailVerified": true,
    "enabled": true,
    "serviceAccountClientId": "saas-backend-admin",
    "username": "service-account-saas-backend-admin"
  }
]
```

> The default ships `realm-admin` for the service account — overly broad. The list above is the minimum the API needs for current admin endpoints. Tighten further if your endpoint set is smaller.

**Update the SMTP block** to point at your real relay with TLS + auth:

```json
"smtpServer": {
  "host": "smtp.your-relay.example.com",
  "port": "587",
  "from": "no-reply@your-domain.com",
  "fromDisplayName": "Your Product",
  "auth": "true",
  "user": "smtp-user",
  "starttls": "true",
  "ssl": "false",
  "envelopeFrom": "no-reply@your-domain.com"
}
```

The SMTP password is set via Keycloak Admin UI or `kcadm.sh update realms/<realm>` post-boot — **do not** put it in the realm export JSON.

**Restrict client redirect URIs and web origins** to your production hostnames:

```json
"redirectUris": ["https://app.your-domain.com/*"],
"webOrigins":   ["https://app.your-domain.com"]
```

**Disable direct access grants** on the confidential client unless the API needs ROPC:

```json
"directAccessGrantsEnabled": false
```

**Remove the dev playground client entirely** (`saas-dev-playground`) — it is a public PKCE client that ships with localhost redirects.

---

## 3. Run Keycloak in production mode

Replace the `keycloak` service in `docker-compose.yml` (or your equivalent orchestrator manifest):

```yaml
keycloak:
  image: quay.io/keycloak/keycloak:26.0
  restart: unless-stopped
  command: ["start", "--optimized", "--import-realm"]
  environment:
    KEYCLOAK_ADMIN: ${KEYCLOAK_ADMIN}
    KEYCLOAK_ADMIN_PASSWORD: ${KEYCLOAK_ADMIN_PASSWORD}
    KC_DB: postgres
    KC_DB_URL: jdbc:postgresql://keycloak-postgres:5432/${KC_DB_NAME}?ssl=true&sslmode=require
    KC_DB_USERNAME: ${KC_DB_USER}
    KC_DB_PASSWORD: ${KC_DB_PASSWORD}
    KC_HOSTNAME: auth.your-domain.com
    KC_HOSTNAME_STRICT: "true"
    KC_HTTP_ENABLED: "false"
    KC_PROXY_HEADERS: forwarded   # or 'xforwarded' if your LB uses XFF
    KC_HTTPS_CERTIFICATE_FILE: /run/secrets/kc-tls.crt
    KC_HTTPS_CERTIFICATE_KEY_FILE: /run/secrets/kc-tls.key
    KC_HEALTH_ENABLED: "true"
    KC_METRICS_ENABLED: "true"
  volumes:
    - ./deploy/keycloak:/opt/keycloak/data/import:ro
  secrets:
    - kc-tls.crt
    - kc-tls.key
  # NO ports: section — Keycloak is only reachable via the reverse proxy
```

Pre-build the optimized image once: `kc.sh build` against your custom theme/provider config. Cache the result.

---

## 4. Lock down docker-compose for production

Create `docker-compose.prod.yml` as an overlay (`docker compose -f docker-compose.yml -f docker-compose.prod.yml up`):

```yaml
services:
  postgres:
    ports: !reset []                  # remove the 5432 publish
    environment:
      POSTGRES_INITDB_ARGS: "--auth-host=scram-sha-256"

  keycloak-postgres:
    ports: !reset []

  mailpit: !reset null                # remove mailpit entirely

  api:
    environment:
      DB_URL: postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres:5432/${POSTGRES_DB}?sslmode=verify-full&sslrootcert=/etc/ssl/postgres/ca.crt
      KEYCLOAK_URL: https://auth.your-domain.com
      KEYCLOAK_JWKS_URL: https://auth.your-domain.com/realms/${KEYCLOAK_REALM}/protocol/openid-connect/certs
      KEYCLOAK_ADMIN_BASE_URL: https://auth.your-domain.com
      ADMIN_CONSOLE_ENABLED: "true"
      DEV_PLAYGROUND_ENABLED: "false"
      ADMIN_LIVE_CHECK_TTL_SECONDS: "30"
      GIN_ACCESS_LOG_ENABLED: "false"  # access logs come from your reverse proxy
    ports: !reset []
    # API only reachable from the reverse proxy via the docker network
```

---

## 5. Reverse-proxy configuration

The API itself does not terminate TLS, set security headers, or enforce CORS. Your reverse proxy must.

**Required headers** (Caddy example shown; equivalent in nginx/Traefik):

```caddy
app.your-domain.com {
  reverse_proxy api:8080 {
    header_up X-Forwarded-For   {remote_host}
    header_up X-Forwarded-Proto {scheme}
    header_up X-Real-IP         {remote_host}
  }

  header {
    Strict-Transport-Security  "max-age=63072000; includeSubDomains; preload"
    X-Content-Type-Options     "nosniff"
    X-Frame-Options            "DENY"
    Referrer-Policy            "strict-origin-when-cross-origin"
    Permissions-Policy         "geolocation=(), microphone=(), camera=()"
    Content-Security-Policy    "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' https://auth.your-domain.com; img-src 'self' data:; font-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'self'"
    -Server
  }

  # Rate limit at the edge — the in-process limiter is a floor, not a ceiling.
  rate_limit {
    zone admin {
      key {remote_host}
      events 60
      window 1m
    }
  }
}
```

**Trusted proxies on the Go side.** The in-process rate limiter trusts `X-Forwarded-For` from any source ([internal/server/ratelimit.go:131](../../internal/server/ratelimit.go#L131)). Until this is fixed in code, either:
- ensure the API listens **only** on the docker network (no host port publish), so direct callers cannot bypass the proxy, **and**
- configure Gin's `engine.SetTrustedProxies([...])` to the proxy IP.

---

## 6. Database hardening

- TLS required: `?sslmode=verify-full&sslrootcert=...` on every connection string.
- Run as a non-superuser role with grants limited to the app database.
- `pg_hba.conf` restricted to the API/Keycloak network segments only.
- Backups: enable PITR if available; otherwise daily `pg_dump` to encrypted object storage.
- `idle_in_transaction_session_timeout = '60s'` and `statement_timeout = '30s'` on the app role.
- Drop `GORM AutoMigrate` for the initial deployment migration: run a one-shot job and then disable. AutoMigrate at every boot ([internal/database/database.go:33](../../internal/database/database.go#L33)) becomes risky as the schema grows. Tracked for V1.x.

---

## 7. API process hardening (until fixed in code)

The Go binary today uses `gin.Default()` + `engine.Run()` ([internal/server/server.go:126,165](../../internal/server/server.go#L126)) — no HTTP timeouts. Until that ships:

- Front the API with a reverse proxy that imposes per-connection timeouts.
- Set OS-level limits in your unit file / pod spec:
  - `LimitNOFILE=65536` (file descriptors)
  - `MemoryMax=2G` (or appropriate)
  - `TasksMax=10000`
- Run as a non-root user (Dockerfile already does this — confirm `USER app` is intact).
- Read-only root filesystem: add `read_only: true` and `tmpfs: /tmp` in compose.

---

## 8. Observability

Required before launch:

- **Logs** shipped to a central store (Loki / Cloudwatch / Datadog). Grep on the `audit ` prefix ([internal/logging/audit_sink.go:21](../../internal/logging/audit_sink.go#L21)) for the audit trail.
- **Metrics** scraped from Keycloak's `/metrics` (after enabling `KC_METRICS_ENABLED`) and from your reverse proxy.
- **Alerts** on:
  - Spike in `EventValidationFailed` / `EventForbidden` events ([internal/auth/events.go](../../internal/auth/events.go)) — possible probe.
  - Sustained 429s from the rate limiter — possible attack or misconfigured client.
  - Keycloak `bruteForceProtected` lockouts.
  - Audit recorder `dropped` counter > 0 ([internal/audit/memory.go:29](../../internal/audit/memory.go#L29)) — events being lost.
  - Postgres connection failures.
  - Sustained Keycloak 5xx (the API fails closed on admin checks — [internal/auth/admin_check.go:197](../../internal/auth/admin_check.go#L197) — so a Keycloak outage = no admin verbs).

---

## 9. Pre-launch smoke tests

After deploy, before announcing the launch:

```bash
# 1. TLS posture
curl -sI https://app.your-domain.com/health | grep -E '(strict-transport|content-security|x-frame)'

# 2. Auth gate
curl -si https://app.your-domain.com/me                           # expect 401
curl -si -H 'Authorization: Bearer invalid' https://app.your-domain.com/me  # expect 401
curl -si -H 'Authorization: Bearer <real-token>' https://app.your-domain.com/me  # expect 200

# 3. Admin gate
curl -si -H 'Authorization: Bearer <non-admin-token>' https://app.your-domain.com/admin/users  # expect 403
curl -si -H 'Authorization: Bearer <admin-token>' https://app.your-domain.com/admin/users      # expect 200

# 4. Dev playground MUST be off
curl -si https://app.your-domain.com/dev/auth     # expect 404
curl -si https://app.your-domain.com/auth/debug   # expect 404

# 5. Swagger MUST be off (or auth-gated)
curl -si https://app.your-domain.com/swagger/index.html  # expect 404 or 401

# 6. Rate limit fires
for i in $(seq 1 50); do curl -so /dev/null -w '%{http_code}\n' https://app.your-domain.com/admin/users -H 'Authorization: Bearer <admin>'; done | sort | uniq -c
# expect a mix of 200 and 429
```

The project ships `scripts/security_live_check.sh` — extend it with the above and run it as a post-deploy gate.

---

## 10. Day-2 hygiene

- Patch cadence: Keycloak monthly, Go quarterly (or as security advisories require).
- Run `govulncheck ./...` weekly against `main` and against the deployed commit.
- Quarterly: rotate every secret per [SECRET_ROTATION.md](../security/SECRET_ROTATION.md).
- Quarterly: review Keycloak event logs and JWT issuance counts for drift.
- On suspected compromise: [INCIDENT_RESPONSE.md](INCIDENT_RESPONSE.md).
