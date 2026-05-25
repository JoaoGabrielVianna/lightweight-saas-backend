# Incident Response Process

This is the runbook the on-call operator follows when a security incident is suspected or confirmed. It is short on purpose — under pressure, long documents do not get read.

> **Scope:** confidentiality, integrity, or availability incidents affecting the API, Keycloak, the application database, or the secrets that protect them. For product bugs and feature failures, use the regular bug triage process.

---

## TL;DR — first 10 minutes

1. **Declare.** Post in `#ops-incident` (or your channel of record): *"INCIDENT DECLARED — <one-line description> — IC: <name>"*. Get a timestamp on the wire before anything else.
2. **Contain, don't fix.** Stop the bleeding (revoke a token, rotate a secret, block an IP). Do **not** start debugging root cause yet.
3. **Preserve.** Do not `docker compose down -v`. Do not delete logs. Snapshot the Postgres data volume and copy `journalctl -u saas-api --since '2 hours ago'` to a safe location.
4. **Notify.** If it touches user data: start the customer-comms clock. If it touches secrets: start the rotation clock ([SECRET_ROTATION.md § 10](../security/SECRET_ROTATION.md#-10--compromise-driven-emergency-rotation)).
5. **Continue with § 1 below.**

---

## § 1 — Severity classification

Pick the highest-applicable row. Severity drives notification scope, not technical response — always preserve and contain regardless.

| Sev    | Definition                                                                                                                                  | Notification target                              |
| ------ | ------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------ |
| **SEV-1** | Confirmed unauthorized access to admin endpoints, secret material exfiltrated, JWT signing key leaked, or user data exposed externally.     | Maintainer + all stakeholders + (if applicable) affected users within 24 h |
| **SEV-2** | Probable compromise: suspicious admin actions, sustained brute-force success rate, secret in CI log or git history.                          | Maintainer + ops channel within 1 h              |
| **SEV-3** | Suspicious activity: spike in 401/403, unusual `EventForbidden` patterns, denial-of-service attempt without a successful breach.             | Ops channel; investigate during business hours    |
| **SEV-4** | Configuration drift or near-miss (e.g. someone almost committed a secret, blocked by pre-commit hook).                                       | Backlog ticket                                    |

---

## § 2 — Roles

For any SEV-1 or SEV-2 incident:

- **Incident Commander (IC).** First responder. Owns the timeline, declares severity, and decides when the incident is closed. Does NOT do the technical fix — they coordinate.
- **Tech lead.** Owns the technical investigation and remediation.
- **Scribe.** Records every action taken (timestamp + actor + action) in a shared doc. Becomes the IR retrospective input.
- **Comms (SEV-1 only).** Owns external communication: customer notice, status page, regulator.

A single person can hold multiple roles in a small org, but the IC role must be explicit and named.

---

## § 3 — Detection sources

Where alerts come from. Confirm the alert against at least one independent source before declaring.

| Source                                                                | Signal                                                                                  |
| --------------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| Auth events (`auth` log origin, [internal/auth/events.go](../../internal/auth/events.go)) | Sustained `validation_failed`, `forbidden`, or `live admin check denied`.               |
| Audit log stream (`audit ` prefix, [internal/logging/audit_sink.go:21](../../internal/logging/audit_sink.go#L21)) | Admin mutations by unexpected actors, mass deletions, role grants outside change window. |
| Keycloak event log                                                    | `LOGIN_ERROR`, `USER_DISABLED_BY_PERMANENT_LOCKOUT`, `IDENTITY_PROVIDER_LINK_ACCOUNT`.   |
| Rate limiter ([internal/server/ratelimit.go](../../internal/server/ratelimit.go))         | Sustained 429s — possible attack or compromised credential being abused.                |
| Reverse proxy logs                                                    | 5xx spikes, unusual `User-Agent`, requests from unexpected geos.                         |
| Postgres                                                              | Connection exhaustion, slow query log, unexpected writes outside `users` table.          |
| External (user report, security researcher email, GitHub advisory)    | Treat as SEV-2 minimum until disproved.                                                  |

---

## § 4 — Contain (stop the bleeding)

Pick the actions that match the incident. Do not perform them all reflexively — `LogoutAllSessions` ejects every user, including the on-call, and is a hard tool.

### 4.1 Suspected stolen user token

```bash
# Find the user by sub or email in the audit log
journalctl -u saas-api --since '1 hour ago' | grep '^audit ' | jq -r 'select(.actor.email == "victim@x.com")'

# Revoke their sessions
curl -X DELETE -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://app.your-domain.com/admin/users/<keycloak-sub>/sessions
```

Token will fail validation within the access-token lifespan (default 10 m in our production profile) once Keycloak's session is gone.

### 4.2 Suspected stolen admin token

Same as above, plus invalidate the admin cache:

```bash
# Forces every admin verb to re-check Keycloak immediately
# (Today: requires API restart — InvalidateAll is not exposed via HTTP.
#  Tracked enhancement: POST /admin/_internal/cache/invalidate)
docker compose restart api
```

The live-admin check ([internal/auth/admin_check.go:204](../../internal/auth/admin_check.go#L204)) will then deny the stolen token even if it still has 9 minutes left on the access token, because Keycloak no longer reports the admin role.

### 4.3 Suspected leaked Keycloak client / admin client secret

Follow [SECRET_ROTATION.md § 10](../security/SECRET_ROTATION.md#-10--compromise-driven-emergency-rotation) — emergency rotation path.

### 4.4 Suspected leaked JWKS private key

```text
1. Keycloak Admin → Realm settings → Keys → Providers → add new RS256 with HIGHER priority
2. Disable the old key provider (do NOT delete yet — JWKS clients need it briefly)
3. Realm settings → Sessions → Logout all  (forces re-auth for every user)
4. Wait 1 × accessTokenLifespan
5. Delete the old key provider
```

The API picks up the new key automatically — the JWKS client refreshes on unknown `kid` within ≤ 30 s ([internal/auth/keycloak/jwks.go:59](../../internal/auth/keycloak/jwks.go#L59)).

### 4.5 Application DB credential compromise

`SECRET_ROTATION.md § 1` zero-downtime path. If you suspect the attacker still has DB access, **also**:

```sql
-- Lock the role immediately, even before rotating
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM postgres;
ALTER ROLE postgres NOLOGIN;
```

This takes the API down until rotation completes. Accepted cost in a real compromise.

### 4.6 Active DoS / sustained traffic spike

1. Confirm with rate-limiter 429 logs and reverse-proxy connection counts.
2. At the edge: drop the source IP/CIDR in your WAF or proxy.
3. If the attacker rotates IPs: lower the in-process rate limit ([internal/server/router.go:57](../../internal/server/router.go#L57)) by deploying with a lower rate, or scale horizontally.
4. The in-process limiter trusts `X-Forwarded-For` from any caller today — if the API has a host port published, attackers can bypass the limiter entirely. Confirm `ports:` is **not** set in your production compose ([PRODUCTION_DEPLOYMENT.md § 4](PRODUCTION_DEPLOYMENT.md#4-lock-down-docker-compose-for-production)).

---

## § 5 — Preserve evidence

Before you debug, snapshot. Recovery often requires re-running the system in a clean state — you cannot get the evidence back once it's overwritten.

```bash
# 1. Logs (last 24 h)
journalctl -u saas-api --since '24 hours ago' > /var/incidents/$(date -u +%Y%m%dT%H%M)/api.log
journalctl -u keycloak --since '24 hours ago' > /var/incidents/$(date -u +%Y%m%dT%H%M)/keycloak.log

# 2. Postgres snapshot (use your provider's snapshot if managed)
pg_dump -Fc lightweight_saas_backend_db > /var/incidents/$(date -u +%Y%m%dT%H%M)/app-db.dump

# 3. Keycloak event log
# Admin UI → Events → Login Events / Admin Events → Export

# 4. Audit ring buffer (last 500 events; lost on API restart)
curl -fsS -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://app.your-domain.com/admin/audit-events \
  > /var/incidents/$(date -u +%Y%m%dT%H%M)/audit-ring.json

# 5. Reverse proxy access log
cp /var/log/caddy/access.log /var/incidents/$(date -u +%Y%m%dT%H%M)/

# 6. Configuration as it was at incident time
git rev-parse HEAD > /var/incidents/$(date -u +%Y%m%dT%H%M)/git-sha.txt
cp .env /var/incidents/$(date -u +%Y%m%dT%H%M)/env-snapshot     # then chmod 600
```

Store under restricted permissions; this collection now contains secrets and likely PII.

---

## § 6 — Investigate

Only after containment + preservation. Work from the evidence pack, not from the running system.

**Standard questions for any incident:**

1. **What did the attacker access?** Map their authenticated `sub` to every audit event they emitted ([internal/logging/audit_sink.go](../../internal/logging/audit_sink.go)).
2. **Did they modify state?** Cross-reference audit `actor.subject` against the `users`, `roles`, `sessions`, and `invitations` Keycloak collections at incident time vs. now.
3. **How did they get in?** Stolen token? Replayed refresh? Compromised admin password? Misconfigured client?
4. **Was the attack vector novel or pre-existing?** Check [SECURITY_GAPS.md](../security/SECURITY_GAPS.md) — was this a known open gap?
5. **Are there other instances of the same vector active right now?** (Same attacker, different IP? Same exploit, different victim?)
6. **What detection gap let it run as long as it did?** Time-to-detect is the most important metric of the IR retrospective.

---

## § 7 — Eradicate + Recover

1. **Eradicate.** Apply the fix in code, config, or process. For a stolen-token incident the "fix" is "the token is invalidated"; for a config incident the fix is a deploy.
2. **Verify eradication.** Re-run the post-rotation verification ([SECRET_ROTATION.md § 9](../security/SECRET_ROTATION.md#-9--post-rotation-verification-required-for-every-rotation)). Re-run [scripts/security_live_check.sh](../../scripts/security_live_check.sh).
3. **Recover.** Restore any data that was modified. If you snapshotted Postgres before the attacker's mutations, this is straightforward; if not, replay audit events to identify and reverse the changes.
4. **Monitor.** Watch the auth event stream and audit log for the next 24 h with elevated attention. The same attacker often returns from a different IP.

---

## § 8 — Communicate

### Internal

- **Continuous:** updates in `#ops-incident` every 30 minutes during active response, even if the update is "no change."
- **At declaration, containment, eradication, closure:** explicit status announcement with timestamp.

### External (SEV-1 only)

If any of the following are true, external comms are required:

- User data was accessed by an unauthorized party.
- User authentication was bypassed.
- Service was degraded for > 1 hour.
- Regulatory notice obligations apply (GDPR Art. 33: 72 h to authorities; CCPA: without unreasonable delay; HIPAA: 60 d).

**Minimum content of a user-facing notice:**

- What happened, in plain language.
- What data was or may have been accessed.
- What you have done about it.
- What the user should do (rotate password, revoke sessions, etc.).
- How they can contact you with questions.

Draft the notice during the incident; send after the IC confirms containment is complete.

---

## § 9 — Close

The IC declares the incident closed when **all** of the following are true:

- [ ] Threat is contained — attacker no longer has access.
- [ ] Vulnerability is eradicated — the path that let them in is closed.
- [ ] Data integrity is verified — modifications are identified and either accepted, reversed, or restored from snapshot.
- [ ] Monitoring is in place — alerts will fire if the same vector is exploited again.
- [ ] Stakeholders + (if applicable) users have been notified.
- [ ] Evidence is preserved and access-controlled.

Post a closure message in `#ops-incident` with: timeline summary, root cause, fix, and the retrospective owner.

---

## § 10 — Retrospective (within 5 business days)

Blameless. The output is a written postmortem with:

1. **Timeline.** Every event with timestamp + actor + action, from detection to closure. Scribe's notes are the source.
2. **Root cause.** Not just "the bug" — also "why did this bug ship" and "why did this bug remain undetected."
3. **What worked.** What in the IR process actually helped — keep doing it.
4. **What didn't work.** What slowed us down, confused us, or didn't fire when it should have.
5. **Action items.** Each one named, owned, and dated. File them as issues immediately; an action item without an issue does not exist.
6. **Were any [known gaps](../security/SECURITY_GAPS.md) the proximate cause?** If yes, upgrade their priority.

Share the retrospective with the team. For SEV-1 with external impact, consider publishing a redacted version externally — it builds trust.

---

## § 11 — Drills

Schedule one IR drill per quarter. A drill is a tabletop exercise: the IC picks a scenario from the inventory below, walks the team through the response, and surfaces gaps in the runbook.

**Drill scenarios** (pick one per quarter, rotate):

1. *"A maintainer pushed `.env` to a public fork."* — Tests § 4.3 + SECRET_ROTATION.md.
2. *"An admin token is being used from an unusual IP, mid-business-day."* — Tests § 4.2.
3. *"Keycloak Postgres is down."* — Tests fail-closed behavior of the admin check + customer-comms cadence.
4. *"A penetration tester reports they exfiltrated a JWKS private key."* — Tests § 4.4 + customer comms for credential rotation.
5. *"Every API replica is returning 503 on `/admin/*` since 09:00."* — Tests detection (live-admin check fail-closed) and § 4.5.

After each drill, update this document with the gaps you found.

---

## Quick reference card

```
DECLARED → CONTAINED → PRESERVED → INVESTIGATED → ERADICATED → RECOVERED → CLOSED → POSTMORTEM
```

- **Containment ≠ fix.** Stop the bleeding first.
- **Preservation ≠ recovery.** Snapshot before you change anything.
- **Postmortem ≠ blame.** Process gaps are findings; people are not.
- **When in doubt, escalate.** A false alarm at 3am is cheaper than a missed real incident.
