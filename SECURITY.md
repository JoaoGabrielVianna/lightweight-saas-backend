# Security Policy

This project is an IAM foundation. A vulnerability here can impact every downstream consumer — please help us handle issues responsibly.

## Supported versions

Pre-1.0; security fixes are issued against the latest tagged release on `main`.

| Version          | Supported              |
| ---------------- | ---------------------- |
| `main` (latest)  | ✅                     |
| Older tags       | ❌ — please upgrade     |

## Reporting a vulnerability

**Do not open a public GitHub issue for security problems.** Public issues are indexed immediately and can be exploited before a fix ships.

Use one of these private channels:

1. **GitHub Private Vulnerability Reporting** (preferred)
   → [Open a private advisory](https://github.com/JoaoGabrielVianna/lightweight-saas-backend/security/advisories/new)
2. **Email the maintainer** at **joaogabrielvianna05@gmail.com** with subject `[SECURITY] lightweight-saas-backend: <short summary>`.

### What to include

- Description and impact (what an attacker can do).
- Affected version / commit (`git rev-parse --short HEAD`).
- Minimal reproduction — request payloads, configuration, or PoC.
- Suggested remediation, if you have one.
- Name / handle for advisory credit, if desired.

## Response SLO

| Step                                            | Target time   |
| ----------------------------------------------- | ------------- |
| Acknowledge the report                          | within **72 h** |
| Initial triage + severity assessment            | within **7 d**  |
| Coordinated disclosure / patch release          | within **30 d** for High; longer with reporter agreement for Critical that needs coordinated infra fixes |

We will keep you informed at each step and coordinate disclosure timing before publishing.

## Scope

**In scope**
- Authentication / RBAC bypass in the API surface (`/admin/*`, `/me`, `/dev/auth`, etc.).
- Audit-trail integrity (events not emitted, events forgeable, events lost silently).
- Secret leakage through logs, errors, responses, or container layers.
- Token validation flaws (algorithm confusion, JWKS handling, issuer/azp bypass).
- Insecure defaults that ship in `config/project.json`, `.env.example`, `deploy/keycloak/realm-export.json`, or `docker-compose.yml`.
- Misconfiguration of Keycloak that ships with the project's bootstrap defaults.
- Dependency vulnerabilities affecting the runtime that are exploitable in our usage.
- CI/release pipeline tampering (e.g., a way for a PR to read repository secrets).

**Out of scope**
- Findings that require pre-existing admin access ("an admin can delete users" is the intended behavior).
- Issues in third-party dependencies already fixed upstream that just need a version bump — please still tell us, treated as a normal PR.
- Social engineering of maintainers or users.
- Vulnerabilities only reachable when a documented `DEV-ONLY` feature is enabled (`DEV_PLAYGROUND_ENABLED=true`, `start-dev` Keycloak, mailpit). Production guidance explicitly prohibits these — see [PRODUCTION_DEPLOYMENT.md](docs/operations/PRODUCTION_DEPLOYMENT.md).

## Production security posture

V1 production deployments MUST:

1. Run Keycloak in `start --optimized` mode behind TLS (not `start-dev`).
2. Set realm `sslRequired: "external"` or stricter.
3. Rotate every bootstrap secret. The defaults in `internal/bootstrap/generate.go` (`admin/admin`, `saas-backend-secret`, etc.) are **for local development only**.
4. Use `sslmode=verify-full` (or at minimum `require`) on all Postgres connection strings.
5. Set `ADMIN_CONSOLE_ENABLED=true` and `DEV_PLAYGROUND_ENABLED=false`.
6. Front the API with a reverse proxy that terminates TLS, sets `X-Forwarded-For`, and enforces network-level rate limits. Configure `SetTrustedProxies` on Gin to only honor headers from the proxy IP.
7. Enable HTTP timeouts and security headers (the project ships safe defaults — verify they are not disabled).
8. Remove all `ports:` publishes from `docker-compose.yml` for Postgres, Keycloak's Postgres, and mailpit — these must not be reachable from the public network.

Full procedure: **[PRODUCTION_DEPLOYMENT.md](docs/operations/PRODUCTION_DEPLOYMENT.md)**.

## Operational runbooks

- **[Production deployment guide](docs/operations/PRODUCTION_DEPLOYMENT.md)** — what to change before going live.
- **[Secret rotation guide](docs/security/SECRET_ROTATION.md)** — scheduled + emergency rotation procedures for every secret in the system.
- **[Incident response process](docs/operations/INCIDENT_RESPONSE.md)** — what to do when you suspect a compromise.
- **[Secrets management](docs/security/SECRETS_MANAGEMENT.md)** — where each secret lives and who/what consumes it.
- **[Known gaps](docs/security/SECURITY_GAPS.md)** — open items tracked transparently.
- **[Validation evidence](docs/security/FINAL_SECURITY.md)** — black-box guard probes against the live stack.

## Acknowledged disclosures

Accepted, fixed, and disclosed vulnerabilities will be listed at [GitHub Security Advisories](https://github.com/JoaoGabrielVianna/lightweight-saas-backend/security/advisories) once any are published.

---

Thanks for helping keep the project and its users safe.
