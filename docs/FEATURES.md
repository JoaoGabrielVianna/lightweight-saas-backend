# Features

**Last updated:** 2026-07-26 · Companion to [PROJECT_STATUS.md](PROJECT_STATUS.md)

Complete feature inventory with a code reference for every claim. If a feature
is not listed here with a file path, it does not exist.

**Status legend**

| | Meaning |
|:--:|---|
| ✅ | **Implemented** — works, has code, exercised by tests or a live run |
| 🟡 | **Partial** — works with documented gaps or missing coverage |
| 🔴 | **Not started** — no code exists |
| ⚪ | **Planned only** — mentioned somewhere but no implementation |

---

## 1. Authentication

| Feature | Status | Implementation | Limitations |
|---|:--:|---|---|
| OIDC login (Authorization Code + PKCE) | ✅ | Keycloak + [web/admin/static/js/lib/auth.js](../web/admin/static/js/lib/auth.js) | No login endpoint in Go — by design (AD-001) |
| JWT signature validation via JWKS | ✅ | [auth/keycloak/provider.go](../internal/auth/keycloak/provider.go) | JWKS fetched at boot; no runtime refresh interval configured |
| Asymmetric-only algorithms | ✅ | `allowedAlgs` in [provider.go](../internal/auth/keycloak/provider.go) | `HS*` deliberately rejected |
| `iss` claim enforcement | ✅ | `jwt.WithIssuer` | Must match `KEYCLOAK_URL` + realm exactly |
| `exp` required | ✅ | `jwt.WithExpirationRequired` | No clock leeway configured |
| `azp` allow-list | ✅ | `allowedClients` in [config.go](../internal/auth/keycloak/config.go) | Missing `azp` is tolerated per OIDC Core §2 |
| Role extraction (realm + client) | ✅ | `extractRoles` in [provider.go](../internal/auth/keycloak/provider.go) | Only the primary client's `resource_access` is read |
| Bearer token extraction | ✅ | `extractBearer` in [auth/middleware.go](../internal/auth/middleware.go) | — |
| Opaque error responses | ✅ | [auth/middleware.go](../internal/auth/middleware.go) | Reason goes to `AuthEvent`, never the wire |
| Token introspection (authenticated) | ✅ | `GET /auth/debug` — [server.go](../internal/server/server.go) | Cannot diagnose invalid tokens (middleware rejects first) |
| Token introspection (dev, unauthenticated) | ✅ | `GET /dev/auth/debug` — [playground.go](../internal/server/playground.go) | `DEV_PLAYGROUND_ENABLED=true` only |
| Automatic token refresh on 401 | ✅ | [web/admin/static/js/lib/api.js](../web/admin/static/js/lib/api.js) | Frontend only |
| Refresh token handling | ✅ | Keycloak + SPA | Backend is stateless and does not participate |
| Backchannel logout | 🔴 | — | F2: a bearer token stays valid until `exp` after logout |
| API keys | 🔴 | — | — |
| Social OAuth (Google) | ⚪ | `google_login` prompt in [bootstrap/prompt.go](../internal/bootstrap/prompt.go#L97) | Flag collected, **never read** |
| MFA | ⚪ | `mfa` prompt in [bootstrap/prompt.go](../internal/bootstrap/prompt.go#L97) | Flag collected, never read. Configurable directly in Keycloak |
| DPoP / mTLS / token binding | 🔴 | — | F3: captured tokens are replayable for their TTL |

## 2. Authorization

| Feature | Status | Implementation | Limitations |
|---|:--:|---|---|
| Realm-role gating | ✅ | `RequireRole` — [auth/middleware.go](../internal/auth/middleware.go) | Role name `admin` is a hardcoded constant |
| Any-of-roles gating | ✅ | `RequireAnyRole` — [auth/middleware.go](../internal/auth/middleware.go) | Defined but not currently mounted on any route |
| Live server-side admin check | ✅ | `RequireLiveAdmin` — [auth/admin_check.go](../internal/auth/admin_check.go) | `/admin/*` only |
| TTL cache with negative caching | ✅ | `CachedAdminChecker` — [auth/admin_check.go](../internal/auth/admin_check.go) | In-process; not shared across replicas |
| In-band cache invalidation | ✅ | `SetAdminInvalidator` — [identity/handler.go](../internal/identity/handler.go) | Out-of-band changes bounded by TTL (30 s) |
| Fail-closed on authz backend error | ✅ | 503 in [auth/admin_check.go](../internal/auth/admin_check.go) | Admin surface depends on Keycloak availability |
| Group-level single gate | ✅ | [router.go](../internal/server/router.go) | — |
| Route omission when unconfigured | ✅ | `SetupIdentity` — [server.go](../internal/server/server.go) | 404 rather than 403 (AD-004) |
| Self-disable guard | ✅ | [identity/service.go](../internal/identity/service.go) | — |
| Self-delete guard | ✅ | [identity/service.go](../internal/identity/service.go) | — |
| Self-strip-admin guard | ✅ | [identity/service.go](../internal/identity/service.go) | — |
| Last-admin guard | ✅ | `assertNotLastAdmin` — [identity/service.go](../internal/identity/service.go) | Not negatively asserted end-to-end (L1) |
| Reserved / protected role names | ✅ | `reservedRoleNames` — [identity/service.go](../internal/identity/service.go) | — |
| Granular permissions | 🔴 | — | Roles are the only unit of authorization |
| ABAC / policies | 🔴 | — | — |
| Resource-level ownership checks | 🔴 | — | Not needed yet: no user-owned resources exist |

## 3. Identity management (admin API)

All 32 routes below sit behind `RateLimitPerIP → RequireAuth → RequireRole("admin") → RequireLiveAdmin`.
Registered in [internal/server/router.go](../internal/server/router.go).

### Users — 13 routes

| Method | Path | Handler | Status |
|---|---|---|:--:|
| GET | `/admin/users` | `ListUsers` | ✅ |
| GET | `/admin/users/:id` | `GetUser` | ✅ |
| GET | `/admin/users/:id/roles` | `ListUserRoles` | ✅ |
| GET | `/admin/users/:id/sessions` | `ListUserSessions` | ✅ |
| PATCH | `/admin/users/:id` | `UpdateUser` | ✅ |
| DELETE | `/admin/users/:id` | `DeleteUser` | ✅ |
| POST | `/admin/users/:id/roles` | `AssignRolesToUser` | ✅ |
| DELETE | `/admin/users/:id/roles/:name` | `UnassignRoleFromUser` | ✅ |
| POST | `/admin/users/:id/reset-password` | `ResetUserPassword` | ✅ |
| PUT | `/admin/users/:id/password` | `SetUserPassword` | ✅ |
| DELETE | `/admin/users/:id/sessions` | `LogoutUserSessions` | ✅ |
| POST | `/admin/users/invite` | `CreateInvitation` (alias) | ✅ |
| POST | `/admin/users/password` | `CreateUserWithPassword` | ✅ |

`PATCH /admin/users/:id` accepts `first_name`, `last_name`, `email`, `enabled`,
`email_verified` — all optional, read-modify-write so omitted fields are
preserved.

### Roles — 6 routes

| Method | Path | Handler | Status |
|---|---|---|:--:|
| GET | `/admin/roles` | `ListRoles` | ✅ |
| GET | `/admin/roles/:name` | `GetRole` | ✅ |
| GET | `/admin/roles/:name/users` | `ListRoleUsers` | ✅ |
| POST | `/admin/roles` | `CreateRole` | ✅ |
| PATCH | `/admin/roles/:name` | `UpdateRole` | ✅ |
| DELETE | `/admin/roles/:name` | `DeleteRole` | ✅ |

Role rename is intentionally unsupported — it would require rewriting every
role mapping referencing the old name. `UpdateRole` mutates description only.

### Sessions — 2 routes

| Method | Path | Handler | Status |
|---|---|---|:--:|
| GET | `/admin/sessions` | `ListSessions` | ✅ |
| DELETE | `/admin/sessions/:id` | `DeleteSession` | ✅ |
| DELETE | `/admin/sessions` (realm-wide) | — | 🔴 [KI-006](KNOWN_ISSUES.md#ki-006) |

`ListSessions` is N+1 — one request per enabled realm client
([TD-007](TECH_DEBT.md#td-007)).

### Invitations — 4 routes

| Method | Path | Handler | Status |
|---|---|---|:--:|
| GET | `/admin/invitations` | `ListInvitations` | ✅ |
| POST | `/admin/invitations` | `CreateInvitation` | ✅ |
| POST | `/admin/invitations/:id/resend` | `ResendInvitation` | ✅ |
| DELETE | `/admin/invitations/:id` | `DeleteInvitation` | ✅ |

Invitations are **derived**, not stored — Keycloak has no invitation resource.
Status (`pending`/`accepted`/`expired`/`revoked`) is computed from user state in
[identity/keycloak/invitations.go](../internal/identity/keycloak/invitations.go).

### Settings — 6 routes

| Method | Path | Handler | Status |
|---|---|---|:--:|
| GET | `/admin/settings/smtp` | `GetSMTP` | ✅ |
| PUT | `/admin/settings/smtp` | `UpdateSMTP` | ✅ |
| POST | `/admin/settings/smtp/test` | `TestSMTP` | ✅ |
| GET | `/admin/settings/email-templates` | `GetEmailTemplates` | ✅ |
| PUT | `/admin/settings/email-templates/:key` | `UpdateEmailTemplate` | ✅ |
| DELETE | `/admin/settings/email-templates/:key` | `ResetEmailTemplate` | ✅ |

The SMTP password is redacted to `••••••••` on read and carried forward on
write when the placeholder is sent back
([smtp_handler.go](../internal/server/smtp_handler.go)).

### Observability — 1 route

| Method | Path | Handler | Status |
|---|---|---|:--:|
| GET | `/admin/audit-events` | `ListEvents` | ✅ |

Reads the in-process ring buffer. Volatile, capped at 500 events
([TD-008](TECH_DEBT.md#td-008)).

## 4. Non-admin routes

| Method | Path | Auth | Status | Implementation |
|---|---|---|:--:|---|
| GET | `/me` | Required | ✅ | [user/handler.go](../internal/user/handler.go) |
| GET | `/auth/debug` | Required | ✅ | [server.go](../internal/server/server.go) |
| GET | `/health` | None | ✅ | [server.go](../internal/server/server.go) — liveness only, no dependency checks |
| GET | `/swagger/*any` | None | ✅ | `gin-swagger` |
| GET | `/` | None | ✅ | [landing.go](../internal/server/landing.go) |

## 5. Audit

| Feature | Status | Implementation | Limitations |
|---|:--:|---|---|
| Canonical event model | ✅ | [audit/event.go](../internal/audit/event.go) | — |
| 14 canonical actions | ✅ | [audit/event.go](../internal/audit/event.go) | All 14 declared **and** emitted |
| Structured-log sink (durable) | ✅ | [logging/audit_sink.go](../internal/logging/audit_sink.go) | Log-based; no query interface |
| In-memory ring buffer | ✅ | [audit/memory.go](../internal/audit/memory.go) | Volatile, capped at 500 |
| Fan-out to multiple sinks | ✅ | [audit/multi.go](../internal/audit/multi.go) | — |
| Emission on success **and** failure | ✅ | `RecordMutation` — [logging/gin_helpers.go](../internal/logging/gin_helpers.go) | Failure adds `reason` |
| Actor + IP capture | ✅ | `ActorFromGin`/`IPFromGin` | IP via `c.ClientIP()` |
| Admin console viewer | ✅ | [views/auditlogs.js](../web/admin/static/js/views/auditlogs.js) | Shows the volatile buffer only |
| Database persistence | 🔴 | — | [TD-008](TECH_DEBT.md#td-008) |
| Retention / archival policy | 🔴 | — | — |
| Read-model filtering & search | 🔴 | — | Endpoint returns the raw buffer |

## 6. Security controls

| Control | Status | Implementation | Notes |
|---|:--:|---|---|
| Per-IP rate limiting | ✅ | [ratelimit.go](../internal/server/ratelimit.go) | `/admin/*` only, 10 req/s burst 20, in-process |
| Rate limit before auth | ✅ | [router.go](../internal/server/router.go) | Unauthenticated floods cannot force JWT verification |
| CORS allow-list | ✅ | [server.go](../internal/server/server.go) | Disabled when `CORS_ALLOWED_ORIGINS` is empty |
| SQL injection protection | ✅ | GORM parameterized queries | Only one user-influenced query exists |
| Path traversal defence | ✅ | [admin.go](../internal/server/admin.go) | Rejects `..`, `path.Clean`, `.md` whitelist, `embed.FS` |
| Input validation | ✅ | [identity/service.go](../internal/identity/service.go) | UUID + role-name regex, page clamping, min password length |
| Mass-assignment protection | ✅ | Explicit request structs in [identity/dto.go](../internal/identity/dto.go) | Unknown JSON keys silently dropped (GAP-4, INFO) |
| Secret redaction in responses | ✅ | [smtp_handler.go](../internal/server/smtp_handler.go) | SMTP password |
| No secrets in logs | ✅ | Verified by grep across `internal/` | — |
| `.env` git-ignored | ✅ | [.gitignore](../.gitignore) | Verified: `.env` is untracked |
| Non-root container | ✅ | [Dockerfile](../Dockerfile) | `app` user |
| Static analysis (CodeQL) | ✅ | [.github/workflows/codeql.yml](../.github/workflows/codeql.yml) | `security-and-quality`, weekly + PR |
| Trusted-proxy validation | 🔴 | — | [KI-004](KNOWN_ISSUES.md#ki-004): rate limit bypassable via forged `X-Forwarded-For` |
| Security headers (CSP, HSTS, …) | 🔴 | — | [KI-003](KNOWN_ISSUES.md#ki-003) |
| XSS review of the SPA | 🔴 | — | [KI-005](KNOWN_ISSUES.md#ki-005): never audited |
| CSRF protection | 🔴 (N/A) | — | No cookie-based sessions; auth is `Authorization: Bearer` |
| Dependency scanning (Dependabot etc.) | 🔴 | — | — |
| Secret management integration | 🔴 | — | Manual per [security/SECRETS_MANAGEMENT.md](security/SECRETS_MANAGEMENT.md) |

## 7. Email

| Feature | Status | Implementation | Limitations |
|---|:--:|---|---|
| Realm SMTP configuration via API | ✅ | [smtp_handler.go](../internal/server/smtp_handler.go) | — |
| SMTP connection test | ✅ | `dialSMTP` — [smtp_handler.go](../internal/server/smtp_handler.go) | — |
| Invitation email | ✅ | Keycloak `execute-actions-email` | Requires SMTP configured |
| Password-reset email | ✅ | `SendResetPasswordEmail` | Requires SMTP configured |
| Email verification | ✅ | Keycloak required action | — |
| Template customization via API | ✅ | [email_templates_handler.go](../internal/server/email_templates_handler.go) | Uses Keycloak's localization API |
| Custom FTL theme (`corsi`, PT-BR) | 🟡 | [deploy/keycloak/themes/corsi/](../deploy/keycloak/themes/corsi/) | **Not persisted across container rebuild** — [KI-011](KNOWN_ISSUES.md#ki-011) |
| Dev SMTP catch-all (Mailpit) | ✅ | [docker-compose.yml](../docker-compose.yml) | UI at `http://localhost:8025` |
| Asynchronous / queued sending | 🔴 | — | Sending is synchronous; blocks the request |
| Bounce / delivery tracking | 🔴 | — | — |
| Go tests for the email surface | 🔴 | — | Neither handler has dedicated tests |

## 8. Admin console (SPA)

| Feature | Status | Implementation |
|---|:--:|---|
| PKCE login + auto-redirect | ✅ | [lib/auth.js](../web/admin/static/js/lib/auth.js) |
| Users view + detail | ✅ | [views/users.js](../web/admin/static/js/views/users.js), [views/user-detail.js](../web/admin/static/js/views/user-detail.js) |
| Roles view | ✅ | [views/roles.js](../web/admin/static/js/views/roles.js) |
| Sessions view | ✅ | [views/sessions.js](../web/admin/static/js/views/sessions.js) |
| Invitations view | ✅ | [views/invitations.js](../web/admin/static/js/views/invitations.js) |
| Overview / dashboard | ✅ | [views/overview.js](../web/admin/static/js/views/overview.js) |
| Settings + SMTP | ✅ | [views/settings.js](../web/admin/static/js/views/settings.js), [views/email.js](../web/admin/static/js/views/email.js) |
| Email templates editor | ✅ | [views/email-templates.js](../web/admin/static/js/views/email-templates.js) |
| Audit logs viewer | ✅ | [views/auditlogs.js](../web/admin/static/js/views/auditlogs.js) |
| Embedded docs viewer (Markdown + Mermaid + TOC) | ✅ | [views/docs.js](../web/admin/static/js/views/docs.js) |
| Swagger UI embed | ✅ | [views/swagger.js](../web/admin/static/js/views/swagger.js) |
| API explorer (dev-only) | ✅ | [views/apiexplorer.js](../web/admin/static/js/views/apiexplorer.js) |
| Playground (dev-only) | ✅ | [views/playground.js](../web/admin/static/js/views/playground.js) |
| EN / PT-BR localization | ✅ | [lib/locale.js](../web/admin/static/js/lib/locale.js) |
| Theme toggle | ✅ | [components/topbar.js](../web/admin/static/js/components/topbar.js) |
| Dev-tools gating in production | ✅ | `devTools`/`apiExplorer` in `/admin/config.json` |
| Realm-wide "terminate all sessions" | 🔴 | Rendered disabled with a `coming-soon` badge — no backend route |

## 9. Developer experience

| Feature | Status | Implementation |
|---|:--:|---|
| Interactive project bootstrap | ✅ | `make init` — [cmd/bootstrap](../cmd/bootstrap/) |
| Regenerate config without prompts | ✅ | `make regen` |
| Toolchain / port diagnostics | ✅ | `make doctor` |
| One-command environment reset | ✅ | `make reset-dev` |
| Local CI gate | ✅ | `make ci` — fmt-check + vet + build + test + swagger-check |
| Swagger drift gate | ✅ | `make swagger-check` |
| Auth smoke test | ✅ | `make auth-test` — [scripts/auth-test.sh](../scripts/auth-test.sh) |
| E2E shell script | 🟡 | [scripts/e2e.sh](../scripts/e2e.sh) — smoke only, not in CI |
| Security check scripts | 🟡 | [scripts/](../scripts/) — 3 scripts, manual, not in CI |
| Dev auth playground | ✅ | [web/dev/](../web/dev/) |
| Embedded docs in the console | ✅ | `/admin/docs/*` |
| `golangci-lint` in CI | 🔴 | `make lint` runs it if installed; the workflow does not install it |

## 10. Infrastructure & operations

| Feature | Status | Implementation | Notes |
|---|:--:|---|---|
| Multi-stage Dockerfile | ✅ | [Dockerfile](../Dockerfile) | Static binary, non-root, cache mounts |
| Docker Compose stack (5 services) | ✅ | [docker-compose.yml](../docker-compose.yml) | Healthchecks + `service_healthy` ordering |
| Keycloak realm import | ✅ | [deploy/keycloak/](../deploy/keycloak/) | `--import-realm` |
| CI pipeline | ✅ | [.github/workflows/ci.yml](../.github/workflows/ci.yml) | Runs `make ci` |
| CodeQL analysis | ✅ | [.github/workflows/codeql.yml](../.github/workflows/codeql.yml) | Weekly + PR |
| Liveness probe | ✅ | `GET /health` | No dependency checks — see [KI-012](KNOWN_ISSUES.md#ki-012) |
| Readiness probe | 🔴 | — | `/health` does not check DB or Keycloak |
| Operator runbooks | ✅ | [operations/](operations/) | Backup, upgrade, monitoring, incident response |
| Compose env completeness | 🟡 | [docker-compose.yml](../docker-compose.yml) | 4 variables not propagated — [TD-004](TECH_DEBT.md#td-004) |
| Kubernetes manifests | 🔴 | — | — |
| Terraform / IaC | 🔴 | — | — |
| CD pipeline | 🔴 | — | Deployment is manual per runbook |
| Graceful shutdown | 🔴 | — | `router.Run` with no signal handling or connection draining |
| Structured logging | ✅ | [internal/logger](../internal/logger/) | Named loggers, plain text |
| Metrics endpoint | 🔴 | — | [TD-009](TECH_DEBT.md#td-009) |
| Distributed tracing | 🔴 | — | [TD-009](TECH_DEBT.md#td-009) |

## 11. Data & persistence

| Feature | Status | Implementation | Notes |
|---|:--:|---|---|
| PostgreSQL connection (GORM) | ✅ | [database.go](../internal/database/database.go) | `TranslateError` enabled |
| Local user projection | ✅ | [user/model.go](../internal/user/model.go) | 1 table, 6 columns |
| Unique constraint on identity | ✅ | `keycloak_sub uniqueIndex` | Survives concurrent first-login races |
| JIT user provisioning | ✅ | `EnsureUser` — [user/service.go](../internal/user/service.go) | — |
| Workspaces | 🟡 | [`internal/workspace`](../internal/workspace/) | Create, read, list, rename, archive under `/v1`. No Connection, no Keycloak binding, no default workspace — [WORKSPACES.md](WORKSPACES.md) |
| Connections (identity-provider config) | 🟡 | [`internal/connection`](../internal/connection/) | Draft/active/retired lifecycle, read-only verify probe, one active per workspace. **Nothing consumes it yet** — [CONNECTIONS.md](CONNECTIONS.md) |
| Secrets at rest (AES-256-GCM) | ✅ | [`internal/secrets`](../internal/secrets/aesgcm.go) | Sealed with per-row AAD and a stored key version. Key rotation not implemented — [TD-019](TECH_DEBT.md#td-019) |
| Prefixed public ids | ✅ | [`internal/publicid`](../internal/publicid/publicid.go) | `ws_<uuid>` on the wire; bare UUID also accepted on input |
| Request correlation id | 🟡 | [`internal/requestid`](../internal/requestid/requestid.go) | `X-Request-Id` on `/v1` only; `/admin/*` deliberately unchanged |
| Schema migration | ✅ | [`internal/database/migrate.go`](../internal/database/migrate.go) | Versioned SQL via `golang-migrate`, embedded with `go:embed`, applied at boot — [MIGRATIONS.md](MIGRATIONS.md) |
| Versioned migrations | 🔴 | — | — |
| Seed data | 🔴 | — | Seed users come from the Keycloak realm export, not the app DB |
| Connection pool tuning | 🔴 | — | GORM defaults |
| Read replicas / sharding | 🔴 | — | — |
| Soft delete / audit columns | 🔴 | — | — |

## 12. Not started — product domain

Everything in this section has **zero lines of implementation**. Listed
explicitly so no reader infers otherwise.

| Feature | Status | Evidence of absence |
|---|:--:|---|
| Multi-tenancy | ⚪ | `grep -rni tenant --include='*.go'` → 3 hits: an unread bootstrap prompt, its test fixture, and a comment. No `tenant_id`, no resolution middleware, no query scoping |
| Organizations / Teams | 🔴 | No package, no model, no route |
| Billing / subscriptions | 🔴 | No package, no Stripe dependency in [go.mod](../go.mod) |
| File upload / object storage | 🔴 | No package, no S3 dependency |
| Job queue | 🔴 | No package, no queue dependency |
| Background workers | 🔴 | No worker process; `cmd/` has only `api` and `bootstrap` |
| Outbound webhooks | 🔴 | No package, no delivery or retry logic |
| Scheduler / cron | 🔴 | No package |
| Runtime feature flags | ⚪ | Only build-time flags in [bootstrap/prompt.go](../internal/bootstrap/prompt.go) |
| Distributed cache (Redis) | 🔴 | No dependency in [go.mod](../go.mod) |
| Notifications (in-app, push) | 🔴 | — |
| Full-text search | 🔴 | — |
| GraphQL API | 🔴 | REST + OpenAPI only |
| WebSocket / SSE | 🔴 | — |
| i18n on the API | 🔴 | Localization exists only in the SPA and Keycloak email templates |

---

## Verification

Every ✅ in this document is backed by a file path. To re-verify the counts:

```bash
# Admin routes by verb (should total 32)
for m in GET POST PUT PATCH DELETE; do
  echo -n "$m "; grep -c "admin\.$m(" internal/server/router.go
done

# Audit actions declared vs emitted (should both be 14)
grep -cE '^\tAction[A-Za-z]+ +Action = ' internal/audit/event.go
grep -rhoE 'audit\.Action[A-Za-z]+' internal/identity/handler.go | sort -u | wc -l

# Confirm the product domain really is absent
grep -rni "tenant\|billing\|webhook\|queue" --include='*.go' internal/ | grep -v _test
```
