# Production Secrets Management

**Audience:** operators standing this stack up outside the dev `docker-compose` topology — primarily single-host VPS deployments.
**Scope:** every secret this project actually has. Specifically the variables in [.env.example](../../.env.example), the credentials inside [deploy/keycloak/realm-export.json](../../deploy/keycloak/realm-export.json), the SMTP block in the realm, and the Keycloak-managed JWT signing keys.
**Out of scope:** application logic, authn/authz behaviour (see [docs/security/FINAL_SECURITY.md](FINAL_SECURITY.md)), and cloud-native secret stores like AWS Secrets Manager / Vault — covered briefly under "future" only.

---

## 1. Secret inventory

Every named secret in the codebase today, in one table. If you add a new secret in code, add it here.

| ID  | Variable / location                                          | What it unlocks                                                                          | Source of truth          | Consumed by                              | Blast radius if leaked                                                                |
|-----|--------------------------------------------------------------|------------------------------------------------------------------------------------------|--------------------------|------------------------------------------|----------------------------------------------------------------------------------------|
| S1  | `POSTGRES_PASSWORD` (.env)                                   | App database superuser auth                                                              | `.env` (gitignored)      | docker-compose `postgres` + Go app via `DB_URL` | Full read/write of application data; lateral movement only if DB port is reachable    |
| S2  | `DB_URL` (.env)                                              | Same — but pre-rendered with credentials inline                                          | `.env`                   | Go app (DSN string)                      | Same as S1                                                                            |
| S3  | `KC_DB_PASSWORD` (.env)                                      | Keycloak's own database                                                                   | `.env`                   | docker-compose `keycloak-postgres` + `keycloak` | Full Keycloak state (users, realm config, signing-key material) — worst single secret |
| S4  | `KEYCLOAK_ADMIN_PASSWORD` (.env)                             | Bootstrap account on Keycloak's `master` realm                                            | `.env`                   | docker-compose `keycloak` startup        | Full control of every realm on the Keycloak instance                                  |
| S5  | `KEYCLOAK_CLIENT_SECRET` (.env)                              | `saas-backend` client credentials (token-validation client)                              | `.env` + `realm-export.json` | Go app                                | Token issuance impersonating the API client                                           |
| S6  | `KEYCLOAK_ADMIN_CLIENT_SECRET` (.env)                        | `saas-backend-admin` service account (calls Keycloak's Admin REST API)                   | `.env` + `realm-export.json` | Go app (`/admin/*` surface)            | Admin actions on the `saas` realm: read/create/delete users, sessions, roles          |
| S7  | `SEED_USER_PASSWORD` (.env)                                  | Initial passwords for users in `realm-export.json` (`testuser`, `adminuser`)              | `.env`                   | Realm import only                        | Login as seeded test users — but those should not exist in prod (see §6.1)            |
| S8  | Keycloak realm signing keys (HS256/RS256/EdDSA)              | JWT signatures — the only thing the API trusts when validating bearer tokens             | Keycloak DB (S3)         | Keycloak only; the app verifies via JWKS  | Forge any access token; impersonate any user including admins                         |
| S9  | Realm `smtpServer.password` (currently empty in dev)         | SMTP relay credentials for invitation / password-reset / verify-email                     | Realm config (Admin UI)  | Keycloak only                            | Outbound spam from your relay; bounced reputation; possible PII leakage via headers   |
| S10 | TLS certificates + private keys (reverse proxy / Keycloak)   | Encryption of every connection                                                            | Outside `.env` — see §6.3 | nginx / Caddy / Traefik in front of API + Keycloak | Passive eavesdropping; downgraded MITM                                              |

The current `.env.example` ships **dev defaults that must NOT be used in production** — see §6 for the hardening checklist.

---

## 2. The `.env` file

`.env` is the single canonical secret store at runtime. It is gitignored ([`.gitignore` line 2](../../.gitignore)). Treat it like the password to your house.

### 2.1 Generation, not editing

The file is **regenerated** by `cmd/bootstrap` from [config/project.json](../../config/project.json) plus existing secret values. The header banner says so:

```
# Auto-generated by `make init` / cmd/bootstrap. Edit config/project.json
# (and re-run `make regen`) rather than editing this file by hand.
# Secrets are sourced from this .env at regeneration time and preserved.
```

Practical consequence: **never put a secret only in `.env`** — also put it in your secure backup. The next `make regen` will preserve existing values, but a lost `.env` is a lost secret.

### 2.2 File-system hardening on a VPS

The default development `.env` lives next to the source tree, world-readable by whoever can `cat` it. On a production host:

```
# Run as the service user, NOT root.
sudo install -o saas -g saas -m 0600 .env /etc/saas/api.env

# Verify
stat -c '%U:%G %a' /etc/saas/api.env   # → saas:saas 600
```

Then point the unit / container at the protected location:

```ini
# /etc/systemd/system/saas-api.service
[Service]
User=saas
Group=saas
EnvironmentFile=/etc/saas/api.env
ExecStart=/usr/local/bin/saas-api
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
```

For Docker:

```bash
# Pass the file in — DO NOT bake secrets into the image.
docker run --env-file=/etc/saas/api.env --read-only ...
```

### 2.3 What the .env file MUST NOT do

- Be committed (verified — `.env` is in `.gitignore`).
- Be readable by anyone except the service user (`chmod 600`, not `644`).
- Appear in `ps`/`cmdline`. Pass via `EnvironmentFile=` or `--env-file`, not `--env KEY=VALUE` inline. Container runtimes leak inline values to anyone with `ps` on the host.
- Be backed up unencrypted. See §5.3.
- Be sent over chat / paste sites. Use `pass`, `age`, or `sops` for inter-operator handoff.

---

## 3. Keycloak secrets

Two distinct concerns:

### 3.1 The `master` realm root admin (`KEYCLOAK_ADMIN` / `KEYCLOAK_ADMIN_PASSWORD`)

**This is the highest-value credential in the stack.** A user with `master` realm admin can create new realms, grant themselves any role, exfiltrate signing keys, etc. The dev defaults are literally `admin` / `admin`.

- Rotate **before the first non-local boot**. Procedure: launch Keycloak with the dev defaults, log in once, create a new admin user with a fresh strong password, delete the bootstrap `admin` user, then delete `KEYCLOAK_ADMIN`/`KEYCLOAK_ADMIN_PASSWORD` from your production `.env`.
- The Keycloak container reads these env vars only on **first boot of a fresh database**. Subsequent boots ignore them. Leaving them in `.env` after first boot is mostly cosmetic — but it's also a free credential a process-list leak would hand over, so blanking them is safer.

### 3.2 Realm-export client secrets (`saas-backend`, `saas-backend-admin`)

`deploy/keycloak/realm-export.json` contains:

```json
{
  "clientId": "saas-backend",
  "secret": "saas-backend-secret"
},
{
  "clientId": "saas-backend-admin",
  "secret": "saas-backend-admin-secret"
}
```

These are **dev placeholders** that match `KEYCLOAK_CLIENT_SECRET` and `KEYCLOAK_ADMIN_CLIENT_SECRET` in `.env.example`. They are committed to git on purpose so the dev stack boots without prompting.

**The realm-export pattern does not survive contact with production.** Options for prod, in order of preference:

1. **Don't import the dev realm at all.** Export the dev realm, replace every `secret`/`password` with `${ENV_VAR}` style placeholders, import via `kc.sh import --override true` with envsubst pre-processed. The realm export file in git stays dev-only.
2. **Import dev once, then mutate.** Boot the realm from the committed export, then immediately rotate every client secret via the Admin REST API (or `kcadm.sh regenerate secret` per client). Save the new secrets to your prod `.env`.
3. **Maintain a parallel `realm-export.prod.json`** with placeholders, committed but unusable as-is. The deploy pipeline substitutes placeholders before passing to Keycloak.

Whichever you pick: **the values currently in `realm-export.json` must never be the values your prod realm uses.**

### 3.3 Service-account scope

The `saas-backend-admin` service account is what makes `/admin/*` work. It needs `realm-management` client roles (`manage-users`, `query-users`, `view-users`, `manage-realm`, `view-realm`, `manage-clients`, `view-clients`, `query-clients`, `view-events`). It does **not** need `master`-realm roles. Audit periodically:

```bash
kcadm.sh get-roles --uusername service-account-saas-backend-admin -r saas
```

If the role list ever drifts (someone added `realm-admin` for convenience), narrow it back.

---

## 4. JWT secrets

Important up-front: **this application does not sign JWTs**. Keycloak does. The app verifies via JWKS.

- Active signing key material lives in **Keycloak's database** (i.e. inside S3's blast radius). The app holds no private key.
- `KEYCLOAK_JWKS_URL` in `.env` points to Keycloak's public-key endpoint. This URL is public; it's not a secret.
- There is a vestigial reference to `JWT_SECRET` in [internal/config/config.go:138](../../internal/config/config.go#L138) as a doc-comment. **This is historical** — the field is not wired, and the configured Keycloak flow uses asymmetric RS256 + JWKS. If you see `JWT_SECRET=…` in a `.env`, it's doing nothing.

### 4.1 Key rotation (the part you DO own)

Keycloak supports key-pair rotation without user-visible disruption:

1. **Add a new active key** in `Realm Settings → Keys → Providers → rsa-generated → Add provider`. Set `priority` higher than the current key.
2. Keycloak starts signing **new** access tokens with the new key immediately. Existing tokens still verify against the old key because Keycloak keeps it `passive` (still served via JWKS) until you remove it.
3. After **accessTokenLifespan** has elapsed since rotation (currently `3600s` per `realm-export.json:2`), every token in circulation has been re-issued with the new key. The old key can be safely deleted.

If the rotation is **emergency** (suspected compromise), don't wait — disable the old key immediately. Every token signed with it instantly becomes invalid, forcing every active user to re-authenticate.

### 4.2 Token lifespan policy

Defaults from `deploy/keycloak/realm-export.json`:

```
"accessTokenLifespan": 3600,        // 1 h  — bearer token validity
"ssoSessionIdleTimeout": 1800,      // 30 m — idle → refresh required
"ssoSessionMaxLifespan": 36000      // 10 h — hard cap on a session
```

These are dev-comfortable. For prod, tighten to taste:

| Setting                   | Dev   | Suggested prod | Rationale                                                                          |
|---------------------------|------:|---------------:|------------------------------------------------------------------------------------|
| `accessTokenLifespan`     | 3600  | 300–900        | Smaller window for a stolen access token                                            |
| `ssoSessionIdleTimeout`   | 1800  | 900            | Idle → forced re-auth                                                               |
| `ssoSessionMaxLifespan`   | 36000 | 28800 (8h)     | Cap an unattended session                                                           |

Reducing `accessTokenLifespan` increases load on the token-refresh endpoint and on the live-admin re-check ([internal/auth/admin_check.go](../../internal/auth/admin_check.go)). The cache TTL is tunable via `ADMIN_LIVE_CHECK_TTL_SECONDS` ([internal/config/config.go:182](../../internal/config/config.go#L182)).

---

## 5. SMTP secrets

### 5.1 Current state (dev)

The dev stack uses [Mailpit](https://github.com/axllent/mailpit) on port 1025, no auth, no TLS — see [docker-compose.yml:45-65](../../docker-compose.yml). The realm SMTP block ([deploy/keycloak/realm-export.json:77-85](../../deploy/keycloak/realm-export.json#L77-L85)) reflects this:

```json
"smtpServer": {
  "host": "mailpit",
  "port": "1025",
  "from": "no-reply@saas.local",
  "fromDisplayName": "lightweight-saas-backend",
  "auth": "false",
  "starttls": "false",
  "ssl": "false"
}
```

**Mailpit is a catch-all. It must never run in production.** It accepts any auth, forwards nothing, and exposes a web UI on 8025 that anyone with network reach can read.

### 5.2 Prod SMTP

Pick a relay (SES, Postmark, Mailgun, SendGrid, your ISP, a self-hosted relay). Configure in the realm — via Admin UI or by editing the SMTP block before realm import:

```json
"smtpServer": {
  "host":            "smtp.example.com",
  "port":            "587",
  "from":            "noreply@yourdomain",
  "fromDisplayName": "your-product",
  "auth":            "true",
  "user":            "${SMTP_USER}",      // substituted at import / set via Admin UI
  "password":        "${SMTP_PASSWORD}",  // never literally in git
  "starttls":        "true",
  "ssl":             "false"
}
```

The `password` field, once set via the Admin UI, lives in Keycloak's database (S3 blast radius). It is **not** exposed via Admin REST API reads — only writes — so it can't leak via a misconfigured admin endpoint. But it CAN leak via realm-export. If you ever `kc.sh export` a prod realm, **scrub `smtpServer.password` from the export before committing or sharing**.

### 5.3 What an SMTP breach looks like

Outbound spam from your relay → ISP reputation hit → invitation emails start hitting recipients' spam folders → onboarding broken. The Keycloak-side compensating-delete work ([docs/INVITATION_RELIABILITY_v0.2.md](../validation/INVITATION_RELIABILITY_v0.2.md)) already handles SMTP-failure mid-invite, so a *temporary* outage is recoverable. A *breach* is not — rotate SMTP password and audit `/admin/invitations` for any unfamiliar entries.

### 5.4 SMTP rotation

The cheapest rotation in the stack: change credentials on the relay, update the realm SMTP block. There's no chain of consumers to coordinate — only Keycloak's mail thread reads it.

---

## 6. Backups (encrypted)

Whatever holds `.env` and the Keycloak DB volume must be backed up encrypted at rest. Options for a single-VPS deploy:

- `restic` to a remote object store, configured with `RESTIC_PASSWORD` held in a separate location (not in the same `.env` you're trying to back up).
- `pg_dump | age -r <key> > backup.age` for the Keycloak DB — keep the age recipient public on the host; the private key lives off-host.
- `sops` for `.env` itself in a separate git repo (private, with proper ACLs).

**Test restore at least quarterly.** A backup that's never been restored is a guess.

---

## 7. Rotation cadence

These are floors. Rotate sooner on any suspicion of compromise (departed admin, suspicious access-log entry, leaked screenshot, etc.).

| Secret                                                  | Routine cadence | Emergency trigger                                       | Procedure                                                                                          |
|---------------------------------------------------------|-----------------|----------------------------------------------------------|----------------------------------------------------------------------------------------------------|
| `KEYCLOAK_ADMIN_PASSWORD` (master realm)                | 90 d            | Admin departure; any leak suspicion                      | Log in → create new admin → delete old admin → blank var in `.env` after first boot                |
| `KEYCLOAK_CLIENT_SECRET` (saas-backend)                 | 90 d            | API host compromise; suspicious token issuance           | Keycloak Admin UI → Clients → saas-backend → Credentials → Regenerate → update `.env` → restart API |
| `KEYCLOAK_ADMIN_CLIENT_SECRET` (saas-backend-admin)     | 90 d            | API host compromise                                      | Same as above, for `saas-backend-admin`                                                            |
| Realm signing key (JWT)                                 | 180 d           | Any suspected key exfiltration                            | Admin UI → Realm Settings → Keys → Providers → add new rsa-generated → wait `accessTokenLifespan` → delete old. Or disable old immediately for emergency. |
| `POSTGRES_PASSWORD`                                     | 180 d           | DB host compromise                                       | `ALTER USER postgres WITH PASSWORD '…'` → update `.env` → restart API                              |
| `KC_DB_PASSWORD`                                        | 180 d           | KC host compromise                                       | `ALTER USER keycloak WITH PASSWORD '…'` → update `.env` → restart Keycloak (drops sessions; warn users) |
| `smtpServer.password`                                   | 180 d           | Spam complaints; reputation drop                          | Rotate at relay → update via Admin UI                                                              |
| `SEED_USER_PASSWORD`                                    | n/a — delete    | n/a                                                       | Seed users should not exist in prod. Verify with `kcadm.sh get users -r saas -q username=testuser` |
| TLS certs                                               | per CA          | Private key exposure                                      | Re-issue (Let's Encrypt auto-renew) → reload proxy                                                 |
| Backup passphrase (restic / age / sops)                 | yearly          | Operator departure with knowledge of passphrase           | Re-encrypt backups with new passphrase; retire old                                                  |

Rotating S5/S6 does **not** invalidate already-issued user JWTs — those are signed by S8. The order matters:

- Client-secret rotation forces only **service-to-service** re-auth (Go app ↔ Keycloak's token endpoint).
- Signing-key rotation invalidates **end-user** tokens after they expire.

---

## 8. Pre-deploy hardening checklist

Before the first non-local `make up`:

- [ ] `.env` exists at `/etc/saas/api.env` (or equivalent) with `chmod 600`, owned by the service user, NOT the source tree.
- [ ] `POSTGRES_PASSWORD` is not `postgres`.
- [ ] `KC_DB_PASSWORD` is not `keycloak`.
- [ ] `KEYCLOAK_ADMIN` / `KEYCLOAK_ADMIN_PASSWORD` are unset OR replaced (first-boot tradeoff — see §3.1).
- [ ] `KEYCLOAK_CLIENT_SECRET` is **not** `saas-backend-secret`. Regenerate via Admin UI.
- [ ] `KEYCLOAK_ADMIN_CLIENT_SECRET` is **not** `saas-backend-admin-secret`. Regenerate via Admin UI.
- [ ] `SEED_USER_PASSWORD` is removed; the realm import has been edited to drop `testuser` / `adminuser`, OR they have been deleted via `kcadm.sh` after import.
- [ ] Realm `smtpServer` points at a real relay; `password` is set; `starttls` is `true`.
- [ ] Realm `sslRequired` is `external` or `all` (currently `none` in `realm-export.json:86`).
- [ ] Realm `bruteForceProtected` is `true` (already so in dev — verified by [docs/security/FINAL_SECURITY.md](FINAL_SECURITY.md) §3 T2).
- [ ] `accessTokenLifespan` tightened per §4.2.
- [ ] TLS terminates in front of both the API and Keycloak. Direct TCP exposure of `5432`, `5433`, `8081`, `1025`, `8025` is OFF.
- [ ] `DEV_PLAYGROUND_ENABLED=false`. The `/dev/auth` playground and `/admin` console must not be reachable in prod.
- [ ] `features.identity_management` remains `true` if you need `/admin/*`, but the admin console UI behind it is dev-only — gate it at the proxy.
- [ ] Backups configured AND tested (§6).
- [ ] All operators with `.env` knowledge have rotation procedure (§7) bookmarked.

---

## 9. Threat model — what these controls do and don't cover

| Threat                                                   | Mitigated by                                | Residual risk                                                                                       |
|----------------------------------------------------------|----------------------------------------------|------------------------------------------------------------------------------------------------------|
| Lost VPS backup tape                                      | §6 encrypted backups + remote storage         | If passphrase + backup are in the same place, none                                                  |
| Disgruntled admin departure                               | §7 rotation; revoke their realm admin role    | Tokens they hold are valid until `accessTokenLifespan` expires unless signing key is rotated         |
| Process-list leak (`ps auxe`)                             | `EnvironmentFile=` / `--env-file`             | None if you don't pass `-e KEY=VALUE` inline                                                          |
| Git history scan for past `.env`                          | `.env` always gitignored                      | Pre-commit hook (e.g. `gitleaks`) catches future mistakes; nothing repairs past leaks short of history rewrite |
| Bearer-token theft via XSS / clipboard                    | Short `accessTokenLifespan`; PKCE on SPA flow  | Stolen-and-replayed token works for up to `accessTokenLifespan` seconds. Live-admin re-check ([admin_check.go](../../internal/auth/admin_check.go)) blocks stolen-admin tokens after demotion |
| Compromised Keycloak DB                                   | OS-level filesystem encryption; offline backup | Game over — see S3+S8                                                                                |
| Compromised reverse proxy                                 | TLS pinning at downstream (mTLS, optional)    | Currently none — single-host single-proxy is the design assumption                                  |
| Insider with shell on the API host                        | Run API as unprivileged user; `chmod 600 .env` | Memory inspection still possible (`gcore`); for that, you need disk-encryption + auditd             |

---

## 10. What's still gappy

These are not addressed by this document and would be the next things to tackle:

- **No central secret manager.** Single-VPS pattern keeps everything in `.env`. Migration to Vault / SOPS-encrypted git / a cloud KMS is a future iteration, not today's reality.
- **No secret-scanning hook in CI.** Adding `gitleaks` to the pre-commit chain would catch accidental commits before they hit history.
- **`realm-export.json` ships dev secrets in git.** Acceptable for dev, dangerous if someone hand-copies the file to a prod deploy. See §3.2 for the three mitigation patterns.
- **No automated rotation.** The cadence in §7 is manual. A future scheduled-job approach (cron + `kcadm.sh` for client secrets) would close the human-forgetting gap.
- **No secret-leak runbook.** This doc covers rotation; it does not specify **who** to call, **how** to revoke, **what** to communicate when a leak is confirmed. That belongs in an incident-response document not yet written.

---

## 11. Quick reference

```text
.env path on VPS                 /etc/saas/api.env (chmod 600, owned by service user)
JWT signing model                Keycloak-issued RS256 (or whatever realm is configured), validated via JWKS
Token rotation                   accessTokenLifespan (default 3600s) governs natural expiry
Forced re-auth                   Disable old signing key in Realm Settings → Keys
Client secret rotation           Admin UI → Clients → <id> → Credentials → Regenerate
Admin password rotation          Admin UI → master realm → Users → admin → Credentials → Reset
SMTP credentials                 Realm Settings → Email → Connection & Authentication
Backups encrypted                restic / age / sops, passphrase stored separately
Out-of-band handoff              pass / age / sops; never chat / paste
```
