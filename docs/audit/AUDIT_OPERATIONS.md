# Audit Operations Runbook

**Audience:** operators, SREs, IAM auditors investigating "who did what" on the admin surface (`/admin/*`).
**Scope:** v0.2.0+ — the audit subsystem documented in [AUDIT_EVENTS.md](AUDIT_EVENTS.md) (model) and [AUDIT_WIRING.md](AUDIT_WIRING.md) (call sites). Inspection only; not implementation, not the Sprint-4 `audit_log` table forward path.
**Companion docs:** [FINAL_SECURITY.md](../security/FINAL_SECURITY.md), [SECURITY_REMEDIATION_GAP1.md](../security/SECURITY_REMEDIATION_GAP1.md).

---

## 1. Where audit events live

Audit events go to the API's `stdout`, inline with the application log. No separate file, no sidecar, no syslog forwarder in v0.2.

```sh
docker compose logs saas-api                       # full history
docker compose logs -f saas-api                    # live tail
docker compose logs --since 1h saas-api            # last hour
```

### 1.1 Line shape

```
2026-05-21 09:00:00 [ INFO  ] [ audit      ] audit {"action":"user.deleted","actor":{"subject":"d8a1e2","email":"adminuser@test.com",...},"target":{"kind":"user","id":"6f3b…","name":"jane@example.com"},"ip":"10.0.0.1","ts":"2026-05-21T09:00:00Z"}
```

| Field            | Meaning |
|------------------|---------|
| `[ INFO  ]`      | Always `INFO` — successes and failures alike. |
| `[ audit      ]` | Fixed logger origin — `logger.New("audit")` in [audit_sink.go](../../internal/logging/audit_sink.go). |
| `audit ` (space) | Fixed `auditLogPrefix`. Downstream filters grep on this token. |
| `{ ... }`        | Single-line JSON of `audit.Event` (schema in §2). |

`ts` is UTC; the leading wall-clock timestamp is host-local. **Use `ts` for cross-system correlation.**

### 1.2 Filtering down to audit lines

The unique 9-char anchor `] audit {` is the primary grep (no other origin emits that sequence). Strip the preamble for clean JSON:

```sh
docker compose logs saas-api 2>&1 \
  | grep -F '] audit {' \
  | sed -E 's/^.*\] audit //'
```

Output is one `audit.Event` JSON per line — ready for `jq`.

---

## 2. Event schema

Source of truth: [internal/audit/event.go](../../internal/audit/event.go).

```json
{
  "action":  "user.deleted",
  "actor":   { "subject": "<uuid>", "email": "<...>", "username": "<...>" },
  "target":  { "kind": "user|role|session|invitation", "id": "<...>", "name": "<...>" },
  "ip":      "<client IP via gin.Context.ClientIP()>",
  "ts":      "<RFC3339 UTC>",
  "reason":  "<service error string — only on failures>",
  "extra":   { /* free-form per-event nuance — e.g. {"roles":["editor","support"]} */ }
}
```

### 2.1 Action vocabulary

Adding actions is backwards-compatible. **Renaming or removing breaks downstream consumers.**

| Action                          | Emitted by handler           | Target.Kind | Notes |
|---------------------------------|------------------------------|-------------|-------|
| `user.created`                  | `CreateInvitation`           | `user`      | Always paired with `invitation.created` — same `Target.ID` |
| `user.updated`                  | `UpdateUser`                 | `user`      | `Target.Name` = updated email on success |
| `user.deleted`                  | `DeleteUser`                 | `user`      | |
| `user.roles_granted`            | `AssignRolesToUser`          | `user`      | Granted role list in `extra.roles` |
| `user.role_revoked`             | `UnassignRoleFromUser`       | `user`      | `Target.Name` = role name being removed |
| `user.password_reset`           | `ResetUserPassword`          | `user`      | Reset email dispatched via Keycloak `executeActionsEmail` |
| `user.sessions_logged_out`      | `LogoutUserSessions`         | `user`      | Bulk revoke of all sessions for one user |
| `role.created`                  | `CreateRole`                 | `role`      | `Target.ID` = normalized role name |
| `role.updated`                  | `UpdateRole`                 | `role`      | Description-only; role name immutable |
| `role.deleted`                  | `DeleteRole`                 | `role`      | |
| `session.revoked`               | `DeleteSession`              | `session`   | Single session by Keycloak session UUID |
| `invitation.created`            | `CreateInvitation`           | `invitation`| Paired with `user.created` — see §2.2 |
| `invitation.resent`             | `ResendInvitation`           | `invitation`| `Target.Name` = invitee email on success |
| `invitation.revoked`            | `DeleteInvitation`           | `invitation`| |

### 2.2 The two-event pairing on `CreateInvitation`

There is no standalone "create user" endpoint in v0.2 — the only path that provisions a Keycloak user is `CreateInvitation`. That handler emits **two events back-to-back** with the same Keycloak UUID in `Target.ID`: `invitation.created` then `user.created`. Joining logs by `Target.ID` will show both. v0.2 stopgap; a future direct-create handler emits only `user.created`. See [AUDIT_WIRING.md](AUDIT_WIRING.md#note-on-usercreated).

### 2.3 Success vs failure

Every mutation handler calls `logging.RecordMutation(c, action, target, err)` in **both branches**:

- Success → `reason` omitted (`omitempty`).
- Failure → `reason` = upstream `err.Error()`.

Failure events come from authorised admins whose service-tier call hit an error (last-admin guard, SMTP outage, upstream KC 5xx). Authentication / RBAC denials never reach the handler and never appear in audit — see [§4](#4-auth-vs-audit--two-distinct-streams).

```sh
# Failures only:
... | jq 'select((.reason // "") != "")'
```

---

## 3. Inspection cookbook

Every recipe below assumes the prefix-stripping pipeline from §1.2 aliased as:

```sh
LOGS='docker compose logs saas-api 2>&1'
AUDIT() { eval "$LOGS" | grep -F '] audit {' | sed -E "s/^.*\] audit //"; }
```

### 3.1 Firehose / live tail

```sh
AUDIT | jq .                          # all events
AUDIT | tail -n 50 | tac | jq .       # last 50, newest first

# Live tail (needs --line-buffered + sed -u to avoid stdio buffering):
docker compose logs -f saas-api 2>&1 \
  | grep --line-buffered -F '] audit {' \
  | sed -uE 's/^.*\] audit //' | jq -c .
```

### 3.2 Role changes

```sh
AUDIT | jq 'select(.action | startswith("role."))'                          # role definitions
AUDIT | jq 'select(.action == "user.roles_granted" or .action == "user.role_revoked")'  # role graph
AUDIT | jq 'select(.target.kind == "role" and .target.id == "support")'     # specific role lifecycle

# Granted roles per session (list is in extra.roles):
AUDIT | jq 'select(.action == "user.roles_granted") | {ts, actor: .actor.email, target: .target.name, roles: .extra.roles}'
```

**GAP-1 correlation.** Every successful `user.role_revoked` invalidates the live-admin cache for the affected subject. After a revoke, any subsequent `reason=live admin check denied: token role no longer present server-side` in the `auth` stream (§4) is the stale JWT being rejected — that's the GAP-1 mitigation firing.

### 3.3 Invites

```sh
AUDIT | jq 'select(.action | startswith("invitation."))'                    # all invite lifecycle

# Failed invite creations (typically SMTP outage or transient KC 5xx):
AUDIT | jq 'select(.action == "invitation.created" and (.reason // "") != "")'
```

A failed `invitation.created` should be matched within ~5s by a compensating-DELETE log line:

```sh
docker compose logs saas-api 2>&1 | grep -E '\] identity-k.* compensating delete'
# Expect: `compensating delete ok user_id=...` or `... failed`.
```

See [BUG_REPORT_CRUD.md §I14b](../validation/BUG_REPORT_CRUD.md#bug-i14b--orphan-user-left-after-smtp-failed-invitation) for the original bug; [INVITATION_RELIABILITY_v0.2.md](../validation/INVITATION_RELIABILITY_v0.2.md) for the contract.

### 3.4 Resends

```sh
AUDIT | jq 'select(.action == "invitation.resent")'

# Failures (often intentional 409s — accepted/disabled users):
AUDIT | jq 'select(.action == "invitation.resent" and (.reason // "") != "")'

# Frequency per invitee — who is being re-invited most:
AUDIT | jq -s '
  map(select(.action == "invitation.resent"))
  | group_by(.target.id)
  | map({invitee: .[0].target.name, kc_user_id: .[0].target.id, resends: length, last_ts: (max_by(.ts).ts)})
  | sort_by(-.resends)'
```

A non-empty `reason` is usually `identity: conflict ...` — the contract in [INVITATION_RELIABILITY_v0.2.md §2](../validation/INVITATION_RELIABILITY_v0.2.md#2-resendinvitation-respects-user-state). Intentional, not regression.

### 3.5 Password resets

```sh
AUDIT | jq 'select(.action == "user.password_reset")'

# Failed resets (almost always SMTP — KC executeActionsEmail returns 500):
AUDIT | jq 'select(.action == "user.password_reset" and (.reason // "") != "")'
```

When investigating "user says no email arrived":

1. Find the `user.password_reset` event — confirms an admin actually clicked.
2. No `reason` → request reached Keycloak. Check Mailpit / SMTP for the dispatched message.
3. `reason` present → dispatch failed; capture the string for post-mortem.

### 3.6 Session revocations

| Action                       | Endpoint                                  | Scope                              |
|------------------------------|-------------------------------------------|------------------------------------|
| `session.revoked`            | `DELETE /admin/sessions/{id}`             | One specific session UUID          |
| `user.sessions_logged_out`   | `DELETE /admin/users/{id}/sessions`       | All active sessions for one user   |

```sh
AUDIT | jq 'select(.action == "session.revoked" or .action == "user.sessions_logged_out")'
```

**Session revocation ≠ JWT revocation.** A revoked session blocks *future* refreshes; in-flight bearer JWTs still authenticate until `exp`. Tracked as GAP-2 in [SECURITY_GAPS.md §E1-E5](../security/SECURITY_GAPS.md#e-mass-revoke--gap-2--gap-3). The GAP-1 closure narrows blast radius for `/admin/*` *when the operator also unassigns the admin role* (live-admin check kicks in); non-admin surfaces continue to authorise post-revocation tokens. To verify the revocation actually stopped the user, correlate the audit event with subsequent gin/auth lines for `<target_sub>`.

### 3.7 By actor / target / IP

```sh
AUDIT | jq --arg a "<email>"  'select(.actor.email == $a)'                  # actor's history
AUDIT | jq --arg t "<id>"     'select(.target.id == $t)'                    # everything done to a subject
AUDIT | jq --arg i "<ip>"     'select(.ip == $i)'                           # IP-bound investigation
```

If the same IP appears against many *different* actor subjects in a short window, suspect a stolen admin credential or proxy mis-attribution — GAP-1 limits blast radius but doesn't detect this pattern on its own.

---

## 4. Auth vs audit — two distinct streams

The audit subsystem records what an **authorised admin** did. **Authentication failures and RBAC denials do NOT appear in the audit stream** — they're in the `auth` origin via `auth.AuthEvent` / `EventForbidden`. Use both streams together to answer "why did this request fail":

| Stream  | Origin tag        | Carries                                         | Recipe |
|---------|-------------------|-------------------------------------------------|--------|
| audit   | `[ audit      ]`  | Successful + service-failed admin mutations     | §3 |
| auth    | `[ auth       ]`  | 401 / 403 from middleware (incl. GAP-1 denials) | `... \| grep -F '] auth ] denied'` |

The GAP-1 fingerprint (stale-admin JWT rejected by the live-admin check) appears **only** in auth:

```sh
docker compose logs saas-api 2>&1 \
  | grep -F 'reason=live admin check denied: token role no longer present server-side'
```

Each line is a request that would have been admitted pre-v0.2; correlate `path=` / `method=` with the *preceding* `user.role_revoked` audit event.

---

## 5. Common investigations

### "Admin says they got logged out but audit shows nothing"

Expected. **Logout / session-end events are NOT audited** — audit records the admin *mutation* surface, not the authenticated-user lifecycle. End-of-session is observable in the `auth` stream (failed token validation on refresh) or in Keycloak's own admin events. What WOULD appear in audit: another admin deliberately revoking sessions (`user.sessions_logged_out`) or unassigning a role.

### "Did someone delete role X?"

```sh
ROLE="support"
AUDIT | jq --arg r "$ROLE" 'select(.action == "role.deleted" and .target.id == $r)'
```

Zero results + role still exists (`curl /admin/roles/$ROLE` → 200) → role wasn't deleted via the admin API. If 404 but no audit → operator went around the admin surface (direct Keycloak UI). Escalate.

### "Are we leaking duplicate password-reset emails?"

UI-003 pattern ([UI_BUGS.md §UI-003](../ui/UI_BUGS.md#ui-003--double-click-on-send-reset-email-dispatches-multiple-keycloak-emails)). Each click produces one audit event:

```sh
AUDIT | jq -s '
  map(select(.action == "user.password_reset"))
  | group_by(.target.id + "|" + (.ts[:19]))   # bucket per user per second
  | map(select(length > 1))
  | map({target: .[0].target.name, ts_bucket: (.[0].ts[:19]), clicks: length})'
```

Any output → double-click bug firing in the wild. Fix queued for v0.2.1 ([HARDENING_REPORT.md](../roadmap/HARDENING_REPORT.md#41-v021--patch-release)).

### "Reconstruct everything that happened to user `<sub>`"

```sh
SUB="6f3b25a8-..."
AUDIT | jq --arg s "$SUB" 'select(.target.id == $s) | {ts, by: .actor.email, action, reason: (.reason // "")}'
```

Canonical timeline against that Keycloak subject — creation (paired `invitation.created` + `user.created`), updates, role changes, password resets, session revocations, deletion. Missing expected steps → either out-of-band change (direct KC UI) or a panic before the deferred `RecordMutation` (rare).

---

## 6. Retention, rotation, shipping off-host

v0.2 ships audit events as part of the application log stream, not a separate persistent channel.

| Concern | v0.2 default | Production gap to close |
|---------|--------------|--------------------------|
| Retention | Whatever `docker compose logs` keeps (host driver default; typically `json-file`, no rotation cap) | Configure docker logging driver: `journald`, `fluentd`, `gelf`, CloudWatch |
| Tamper resistance | None — write access to the log file = ability to edit | Ship to append-only store (Loki, S3 object-lock, SIEM) before local rotation |
| Cross-host correlation | `ts` (UTC) + `ip` carried per event | Add deployment / pod / node identity at the log driver layer |
| Query latency | `docker logs \| grep` — fine on one host, slow at scale | Index `action`, `target.id`, `actor.subject` in your log platform |
| Schema evolution | Action vocabulary additive; renames breaking | Do not assume `extra` keys are stable across releases |

**Forward compatibility.** Sprint 4 ships an `audit_log` Postgres table fed by a parallel `Recorder` during rollout ([AUDIT_EVENTS.md "Forward compatibility"](AUDIT_EVENTS.md#forward-compatibility-sprint-4)). The JSON schema in §2 is the contract — anything built today against the log-line shape keeps working after the table ships, and you can switch from grep-pipelines to SQL when convenient.

---

## 7. Quick reference

```sh
# All audit events, raw JSON stream
docker compose logs saas-api 2>&1 | grep -F '] audit {' | sed -E 's/^.*\] audit //'

# By action prefix
... | jq 'select(.action | startswith("role."))'         # role definitions
... | jq 'select(.action | startswith("user."))'         # user-scoped events
... | jq 'select(.action | startswith("invitation."))'   # invite lifecycle
... | jq 'select(.action | startswith("session.") or .action == "user.sessions_logged_out")'

# By outcome
... | jq 'select((.reason // "") == "")'                 # successes only
... | jq 'select((.reason // "") != "")'                 # failures only

# By actor / target / ip
... | jq --arg a "<email>"  'select(.actor.email == $a)'
... | jq --arg t "<id>"     'select(.target.id == $t)'
... | jq --arg i "<ip>"     'select(.ip == $i)'

# Auth-side denials (separate stream — RBAC + GAP-1 fingerprint)
docker compose logs saas-api 2>&1 | grep -F '] auth ] denied'
docker compose logs saas-api 2>&1 | grep -F 'reason=live admin check denied'
```

---

## 8. Limits and footguns

- **Only mutations are audited.** Reads (`GET /admin/users`, etc.) are NOT. Raise as v0.3 request if your threat model needs read auditing.
- **Out-of-band changes bypass the trail.** Anything done via the Keycloak admin UI / Admin REST (not through `/admin/*`) doesn't appear. The GAP-1 cache also won't invalidate — [SECURITY_REMEDIATION_GAP1.md §6](../security/SECURITY_REMEDIATION_GAP1.md#6-remaining-risk).
- **Failures still emit.** A `reason` field isn't a defect — it's the audit-honest record of a denied or upstream-failed mutation.
- **Two events per `CreateInvitation`** (`invitation.created` + `user.created`) by design — §2.2. De-duplicate by `(target.id, ts[:19])` if a downstream consumer expects one row per request.
- **No bulk-realm-terminate trail.** The realm-wide "Terminate all sessions" doesn't exist in v0.2 (GAP-3) — nothing to audit. Per-user bulk revoke (`user.sessions_logged_out`) does emit.
- **Read scaling is grep-bound.** Index in your log platform before relying on these pipelines for forensic queries.
