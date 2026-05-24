# Monitoring & Observability

**Audience:** operators running this stack in dev, staging, or as a reusable IAM foundation.
**Scope:** what the v0.2.0 binary already exposes (health, audit logs, auth events, container healthchecks), what to alert on today, and the seams a future Prometheus / OpenTelemetry layer plugs into without code changes.
**Sibling docs:** [UPGRADE_AND_ROLLBACK.md](UPGRADE_AND_ROLLBACK.md) · [../security/SECURITY_GAPS.md](../security/SECURITY_GAPS.md).

---

## 1. Signals at a glance

| Signal              | Where it comes from                         | Format                       | Today's consumer                     | v0.3+ consumer            |
|---------------------|---------------------------------------------|------------------------------|--------------------------------------|---------------------------|
| **Liveness**        | `GET /health`                               | `200 {"status":"ok"}`        | Container probe, smoke test          | Kubernetes liveness probe |
| **Container health**| `docker-compose` healthchecks               | `healthy / unhealthy`        | `docker ps`, `depends_on`            | Orchestrator probes       |
| **Auth events**     | `auth.AuthEvent` → `authEventLogger`        | logfmt-ish line, `[ auth ]`  | `docker logs`, grep                  | Prometheus counters       |
| **Audit events**    | `audit.Event` → `logging.AuditSink`         | `audit {…JSON…}`, `[ audit ]`| `docker logs`, grep, jq              | DB table + Loki + alerts  |
| **Live-admin check**| `auth.RequireLiveAdmin` → `auth.AuthEvent`  | `[ auth ] denied` line       | Same as auth events                  | Same as auth events       |
| **Gin access log**  | gin default middleware                      | `[GIN] method | code | dur`  | `docker logs`                        | Promtail → Loki           |
| **Application log** | `logger.Logger` per package origin          | `[ origin ] LEVEL msg`       | `docker logs`                        | Promtail → Loki           |

Everything below describes signals already wired in v0.2.0 unless tagged **future**.

---

## 2. Liveness — `GET /health`

**Endpoint.** `GET http://<api-host>:8080/health` — [`internal/server/server.go:143`](../../internal/server/server.go#L143).

- No auth, no DB ping, no upstream check.
- Returns `200 {"status":"ok"}` whenever the Gin process accepts connections.
- *Liveness*, not *readiness* — 200 means "process up", not "DB/Keycloak reachable".

For dependency health use the Docker healthchecks (§5) and the audit/auth log signals (§3, §4).

```sh
curl -fsS -o /dev/null -w "%{http_code}\n" http://localhost:8080/health  # 200
```

---

## 3. Audit logs — the mutation trail

Every mutation through `/admin/*` emits exactly one structured audit event, success or failure. The v0.2 invariant:

> every mutation MUST emit `who / action / target / timestamp / ip`; failures MUST also emit `reason`.

### 3.1 Event shape

Defined in [`internal/audit/event.go`](../../internal/audit/event.go):

```jsonc
{
  "action": "user.role_revoked",
  "actor":  {"subject":"ddc2cf1b-…","email":"adminuser@test.com","username":"adminuser"},
  "target": {"kind":"user","id":"2219e074-…","name":"testuser"},
  "ip":     "10.0.0.1",
  "ts":     "2026-05-21T03:14:15Z",
  "reason": "live admin check denied: token role no longer present server-side",
  "extra":  {"roles":["admin"]}
}
```

| Field    | Required | Notes |
|----------|:--------:|-------|
| `action` |  ✓       | Canonical verb (see §3.2). Renaming is a breaking change. |
| `actor`  |  ✓       | At least one of `subject` / `email` / `username` populated. From the verified `Identity`. |
| `target` |  ✓       | `kind` short (`user`, `role`, `session`, `invitation`). `id` canonical (UUID or role name). `name` optional. |
| `ts`     |  ✓       | UTC, stamped by `audit.Record` if zero. |
| `ip`     |  ✓       | `gin.Context.ClientIP()` under the current `TrustedProxies`. |
| `reason` | failures only | `err.Error()` from the service layer. |
| `extra`  | optional | Per-event nuance (e.g. `{"roles":["editor","support"]}`). |

### 3.2 Canonical actions

Stable over a major version. Adding values is safe; renaming/removing breaks downstream.

```
user.created              user.updated              user.deleted
user.roles_granted        user.role_revoked         user.password_reset
role.created              role.updated              role.deleted
session.revoked           user.sessions_logged_out
invitation.created        invitation.resent         invitation.revoked
```

Source of truth: [`internal/audit/event.go:27-60`](../../internal/audit/event.go#L27-L60).

### 3.3 Wiring

[`cmd/api/main.go:44`](../../cmd/api/main.go#L44) calls `logging.WireDefault()`, which installs [`logging.AuditSink`](../../internal/logging/audit_sink.go#L23) as the package-level `audit.Recorder`. Each event becomes one stdout line with `origin="audit"`:

```
2026-05-21 03:14:15 INFO [ audit ] audit {"action":"user.role_revoked","actor":{…},"target":{…},…}
```

The `audit ` prefix is fixed in [`audit_sink.go:20`](../../internal/logging/audit_sink.go#L20) — every downstream filter greps on it.

### 3.4 Useful greps

```sh
# Audit events in the last hour, pretty-printed:
docker logs --since 1h saas-api | grep -F '[ audit      ]' | sed 's/.*audit //' | jq -c .

# Only failures (events that carried a reason):
docker logs saas-api | grep -F '[ audit      ]' | grep -F '"reason":'
```

### 3.5 What is NOT audited

- Read-only `GET /admin/*` (listing users, roles, sessions) — use the gin access line.
- `/me` and the user-facing surface — out of IAM admin scope.
- `RequireAuth` / `RequireRole` failures — emit an `AuthEvent` (§4), not an `Event`.

---

## 4. Auth events — the access-control trail

[`internal/auth/middleware.go`](../../internal/auth/middleware.go) emits a structured `AuthEvent` for **every** protected request, success and failure. Hook registered at [`cmd/api/main.go:39`](../../cmd/api/main.go#L39).

### 4.1 Event kinds

From [`internal/auth/events.go`](../../internal/auth/events.go):

| Kind                   | When |
|------------------------|------|
| `token_validated`      | JWT signature + issuer + audience checked OK; identity stored on the request context. |
| `missing_header`       | No `Authorization` header. |
| `malformed_header`     | Header missing `Bearer ` prefix or empty after it. |
| `validation_failed`    | Signature / issuer / audience / kid-missing / `sub` claim missing — `reason` carries detail. |
| `forbidden`            | Token valid but caller lacks required realm role (`RequireRole`) OR live-admin check denied (`RequireLiveAdmin`). |

### 4.2 Line format

```
[ auth ] ok     kind=token_validated sub=<uuid> method=GET path=/me dur=146.374µs
[ auth ] denied kind=forbidden       method=PATCH path=/admin/users/<uuid> reason=live admin check denied: token role no longer present server-side dur=14ms
```

Logfmt-shaped, not JSON. The hook is provider-agnostic (`auth.SetEventHook`); rewriting `authEventLogger` to fan out to Prometheus / OpenTelemetry needs no middleware change (see §8).

### 4.3 GAP-1 live-admin denials

The live-admin check ([`admin_check.go:174`](../../internal/auth/admin_check.go)) emits `kind=forbidden` with `reason=live admin check denied: token role no longer present server-side`. **Treat one in the wild as an active exploit attempt** — a token whose claims still said `admin` was rejected because the upstream Keycloak no longer shows the subject as admin (user was demoted; attacker tried the pre-revocation token before `exp`). See [SECURITY_REMEDIATION_GAP1.md](../security/SECURITY_REMEDIATION_GAP1.md).

A related signal is `reason=live admin check failed: <upstream-error>`. That one is *not* an attack — API couldn't reach Keycloak. Alert on rate, but read as availability, not security.

---

## 5. Container health (docker-compose)

Five healthchecks ship in [`docker-compose.yml`](../../docker-compose.yml):

| Service                  | Probe                                                                                          | Interval / timeout / retries |
|--------------------------|------------------------------------------------------------------------------------------------|------------------------------|
| `saas-postgres`          | `pg_isready -U $POSTGRES_USER -d $POSTGRES_DB`                                                 | 5s / 5s / 10                 |
| `saas-keycloak-postgres` | `pg_isready -U $KC_DB_USER -d $KC_DB_NAME`                                                     | 5s / 5s / 10                 |
| `saas-mailpit`           | `wget -q --spider http://127.0.0.1:8025/readyz`                                                | 5s / 3s / 10                 |
| `saas-keycloak`          | TCP fetch of `http://127.0.0.1:9000/health/ready` (KC management port)                         | 10s / … / …                  |
| `saas-api`               | *(no docker-level healthcheck)* — covered by `depends_on: saas-keycloak: condition: service_healthy` |                              |

`saas-api` skips its own healthcheck because `/health` is covered by external probes; boot order is enforced via `depends_on … condition: service_healthy` on Keycloak + Postgres.

```sh
docker ps --format 'table {{.Names}}\t{{.Status}}'
# saas-postgres            Up 35 minutes (healthy)
# saas-keycloak            Up 35 minutes (healthy)
# …
```

A service `(unhealthy)` for >3 consecutive checks is your first page.

---

## 6. Reading the logs in dev

All services log to stdout/stderr; `docker logs` is the universal interface.

```sh
# Tail the API:
docker logs -f saas-api

# Last 5 minutes of Keycloak WARN/ERROR:
docker logs --since 5m saas-keycloak 2>&1 | grep -E " WARN | ERROR "

# Every 4xx/5xx response from gin in the last hour:
docker logs --since 1h saas-api | grep -E '\[GIN\][^|]*\|\s*[45][0-9]{2}'

# Watch audit + auth events live:
docker logs -f saas-api 2>&1 | grep -E '\[ (audit|auth) +\]'
```

**Per-origin filtering.** Every line begins with `[ <origin>     ]`:

```
main       server.go banner + boot
auth       RequireAuth / RequireRole / RequireLiveAdmin
audit      the audit-event sink
identity   identity service (admin handlers)
database   gorm + connection lifecycle
keycloak   auth provider (JWKS / token validation)
```

**Log rotation.** Container logs grow forever by default. In production wire a rotation driver in `docker-compose.yml`:

```yaml
services:
  saas-api:
    logging:
      driver: json-file
      options:
        max-size: "50m"
        max-file: "5"
```

Not set in dev compose to keep boot fast; **set it before shipping.**

---

## 7. Metrics (current state)

**v0.2.0 ships no `/metrics` endpoint and no metric counters.** Middleware emits structured events that *would* feed metrics, but there is no Prometheus collector yet.

Practical consequence: "how many `/admin/users` 5xxs in the last hour?", "p95 of `/me`?", "live-admin denials per minute?" — all answered today by `docker logs | grep`, not aggregated counters. Deliberate decision: ship the events first, the collector second. Event shapes (§3.1, §4.1) are stable; the future layer (§8) is additive.

---

## 8. Future Prometheus / OpenTelemetry (seams)

Three seams exist; future collectors plug in without touching the hot path:

| Seam | Where | What plugs in |
|------|-------|---------------|
| `auth.SetEventHook(h)` | [`internal/auth/events.go:51`](../../internal/auth/events.go#L51) | Fan auth events to counters/histograms (e.g. `saas_auth_requests_total{kind,method,path_template}`, `saas_auth_validation_duration_seconds{kind}`, `saas_auth_live_admin_check_denied_total`) |
| `audit.SetDefault(r)` | [`internal/audit/recorder.go:42`](../../internal/audit/recorder.go#L42) | Composite recorder fan-out: keep log line + bump counters (`saas_audit_events_total{action}`, `saas_audit_failures_total{action}`) + persist to DB |
| gin middleware mount | `internal/server/server.go` | `r.Use(prommiddleware.New())` + `r.GET("/metrics", gin.WrapH(promhttp.Handler()))` for per-request histograms and `/metrics` |

The `EventHook` and `Recorder` interfaces are stable contracts; replacing the implementations is the entire integration. OpenTelemetry spans use the same fields (`Path`, `Method`, `Subject`, `Duration`) — same seams, different consumer.

---

## 9. Alerts — what to page on today

Without Prometheus, alerts are log-based. Each row notes the future Prometheus query that will replace the `grep`.

| # | Condition                                                                                             | Today's detector                                                                                                            | Severity | Future Prom query                                       |
|---|-------------------------------------------------------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------|----------|---------------------------------------------------------|
| 1 | `saas-api` down or `/health` ≠ 200 for >1 min                                                         | external probe + `docker ps` shows missing / Restart loop                                                                   | page     | `up{job="saas-api"} == 0`                                |
| 2 | Any compose service `(unhealthy)` for ≥3 checks                                                       | `docker ps` status                                                                                                          | page     | container_state                                          |
| 3 | `live admin check denied` events seen *at all*                                                        | `docker logs saas-api \| grep -F 'live admin check denied'`                                                                 | page     | `rate(saas_auth_live_admin_check_denied_total[5m]) > 0` |
| 4 | `live admin check failed` events at rate >0.1/s sustained (Keycloak unreachable from API)             | `docker logs --since 5m saas-api \| grep -c 'live admin check failed' > 30`                                                 | page     | `rate(saas_auth_live_admin_check_failed_total[5m]) > 0.1` |
| 5 | Burst of `kind=validation_failed` (>10/s) — token-forgery probing                                     | `docker logs --since 1m saas-api \| grep -c 'kind=validation_failed' > 600`                                                  | warn     | `rate(saas_auth_requests_total{kind="validation_failed"}[1m]) > 10` |
| 6 | `kind=forbidden` rate spike from a single IP — RBAC probing                                           | per-IP grep (no aggregation today)                                                                                          | warn     | `topk(5, rate(saas_auth_requests_total{kind="forbidden"}[5m])) by (ip)` |
| 7 | Brute-force lockouts in Keycloak (covered in [SECURITY_VALIDATION_v0.3.md](../security/SECURITY_VALIDATION_v0.3.md)) | KC admin event log + `Invalid user credentials` repetition pattern                                                          | warn     | counter on KC admin events                              |
| 8 | Audit events ceased for >5 min while traffic continues                                                | `docker logs --since 5m saas-api \| grep -c '\[ audit'  == 0` AND  gin lines > 0                                            | page     | `absent_over_time(saas_audit_events_total[5m])`         |
| 9 | gin 5xx rate >1/s for 5 min                                                                           | `docker logs --since 5m saas-api \| grep -cE '\[GIN\][^|]*\| 5[0-9]{2}'`                                                     | page     | `rate(http_requests_total{status=~"5.."}[5m]) > 1`      |

Until #1, #2, #3 have programmatic detectors, treat them as **runbook MUST monitor**.

---

## 10. Smoke / synthetic checks

Two probes in tree double as health signals:

- [`scripts/security_live_check.sh`](../../scripts/security_live_check.sh) — 17 guard probes. Exit non-zero on any drift from the 401/403/200 contract. Safe on a 1-minute cron in staging.
- [`scripts/security_advanced_check.sh`](../../scripts/security_advanced_check.sh) — 6 advanced threat probes. Heavier (≈60 s, triggers a brute-force that locks the test user). Suitable for a 30–60 min cron.

Both write evidence under `docs/evidence/security/`.

---

## 11. Privacy / log hygiene

- The audit `Actor` carries `email` and `username`. If treated as PII, configure a PII-aware log destination, or wrap `AuditSink` (via `audit.SetDefault`) with a redactor.
- The auth-event line carries the JWT `sub` (KC UUID — not PII alone).
- The auth-event line **does not** carry the bearer token, the user's email, or the request body.
- The audit event's `extra` map may carry sensitive context if a handler stuffs it in — review each `RecordMutation` call site for compliance.

---

## 12. Quick reference card

```
liveness            curl -sS http://localhost:8080/health
container health    docker ps --format 'table {{.Names}}\t{{.Status}}'
audit events        docker logs saas-api | grep -F '[ audit      ]' | sed 's/.*audit //' | jq -c
auth events         docker logs saas-api | grep -F '[ auth       ]'
live-admin denials  docker logs saas-api | grep -F 'live admin check denied'
guard smoke         bash scripts/security_live_check.sh
adversarial smoke   bash scripts/security_advanced_check.sh
```
