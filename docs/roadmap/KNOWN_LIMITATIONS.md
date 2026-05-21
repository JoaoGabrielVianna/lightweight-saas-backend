# Known Limitations — v0.2.0-rc1

This document consolidates every limitation surfaced by the four RC1 validation agents. Each entry is intentionally a *limitation* (a documented gap), not a *defect* (a contract violation). RC1 ships with these gaps acknowledged; promotion to `v0.2.0` final follows the gates in [docs/RELEASE_CHECKLIST.md](../release/RELEASE_CHECKLIST.md).

Source reports:
- Agent A — [SECURITY_VALIDATION_v0.2.md](../security/SECURITY_VALIDATION_v0.2.md), [SECURITY_VALIDATION_v0.3.md](../security/SECURITY_VALIDATION_v0.3.md)
- Agent B — [SMOKE_TEST_v0.2.md](../validation/SMOKE_TEST_v0.2.md), [evidence/crud/CRUD_E2E_REPORT.md](../evidence/crud/CRUD_E2E_REPORT.md)
- Agent C — [INVITATION_RELIABILITY_v0.2.md](../validation/INVITATION_RELIABILITY_v0.2.md)
- Agent D — [AUDIT_EVENTS.md](../validation/AUDIT_EVENTS.md)

Severity scale: **Critical** = release-blocker · **High** = blocks final 0.2.0 tag · **Medium** = backlog for 0.3 · **Low** = noted, no action this milestone.

---

## 1. Security & hardening backlog (Agent A — `INFO` findings)

| ID | Severity | Surface          | Limitation | Notes |
|----|----------|------------------|------------|-------|
| F1 | Medium   | Rate limiting    | No throttling on `/me`, `/admin/*`, `/health`, or the Keycloak token endpoint from the caller's vantage. 100+ req/s burst is served without `429`. | DoS surface; not a confidentiality regression. Mitigation: per-IP + per-`sub` rate-limit middleware, or front the API with one. |
| F2 | Low–Med  | Logout           | Bearer access tokens remain valid for up to 3600s after OIDC logout — the API does not consult Keycloak's session store on each request. | Standard stateless-JWT trade-off. Blast radius bounded by `accessTokenLifespan`. Mitigations: shorten lifespan, listen to Keycloak backchannel-logout, or hit `userinfo` on sensitive verbs. |
| F3 | Low      | Token replay     | No DPoP, no `jti` revocation, no per-request nonce. A captured bearer token is replayable for its full TTL by any holder, from any IP. | Expected for plain OAuth2 bearer tokens. Revisit if scope warrants DPoP or mTLS. |

All three are recorded in Agent A's [SECURITY_VALIDATION_v0.3.md §10](../security/SECURITY_VALIDATION_v0.3.md#10-findings-carried-forward) and are consistent with v0.2's documented contract — none are regressions.

---

## 2. End-to-end CRUD gaps (Agent B — CRUD pass)

| ID | Severity | Surface | Limitation | Suggested next pass |
|----|----------|---------|------------|---------------------|
| L1 | Medium   | Self-action guards (self-delete, last-admin) | Not negatively asserted by the smoke or CRUD pass. A regression that weakens these guards would not be caught by the current Playwright suite. | Add explicit negative tests in a separate integration suite. |
| L2 | Low      | Logout end-session round-trip | Smoke captured the session intentionally to keep it alive; OIDC end-session round-trip via the SPA was not exercised. | Add as a discrete browser test once write paths are stable. |
| L3 | Low      | Pre-existing pending invitation `user@example.com` | The CRUD pass revoked this invitation as part of its DELETE coverage. Future smokes that assume its presence will fail. | Future smoke runs must provision their own subjects (Agent B's CRUD pass already follows this pattern). |

---

## 3. Audit / observability handoff (Agent D)

| ID | Severity | Surface | Limitation | Mitigation now |
|----|----------|---------|------------|----------------|
| L4 | **High** for final 0.2.0 — Medium at RC1 | Audit event emission | The audit model + `AuditSink` log sink are shipped. Two wiring steps are pending: (a) `logging.WireDefault()` at bootstrap, and (b) `audit.Record(...)` calls in `internal/identity/handler.go` for the 13 mutation handlers. Until both ship, `audit.Record` is a silent no-op. | `auth.AuthEvent` continues to emit authn/RBAC events via the existing `auth.SetEventHook` channel (v0.1 behaviour). Authorization decisions and `EventForbidden` denials remain observable; what is *not* observable in RC1 is the post-success mutation trail (who deleted which user, who granted which role). |

Decision required before final 0.2.0 tag — see [RC1_REPORT.md §7](../release/RC1_REPORT.md#7-acceptance-gate-for-v020-final-not-rc1). If L4 slips to 0.2.1, this section is updated and the limitation called out explicitly in `CHANGELOG.md`.

---

## 4. SMTP-dependent endpoints (Agent B + Agent C)

| ID | Severity | Surface | Limitation | Mitigation |
|----|----------|---------|------------|------------|
| L5 | Medium   | `POST /admin/invitations`, `POST /admin/invitations/{id}/resend`, `POST /admin/users/{id}/reset-password` | The local dev Keycloak realm has no SMTP wired. All three endpoints return `502 Bad Gateway` on the wire because `executeActionsEmail` fails inside Keycloak. The handler is reached, auth + RBAC pass, and Agent C's compensating-DELETE contract is observed: no orphan user is left behind on a failed `CreateInvitation`. | Wire MailHog (or any catch-all SMTP) into `docker-compose.yml`; re-run Agent B's CRUD pass to flip these three PASS_GAPs to PASS. Alternative: document the three endpoints as "requires SMTP" in the README and accept the `502` as expected in a no-SMTP dev environment. |

---

## 5. Realm-wide bulk session termination (Agent B)

| ID | Severity | Surface | Limitation | Path forward |
|----|----------|---------|------------|--------------|
| L6 | Low      | "Terminate all sessions" (realm-wide) on the Sessions tab | Button is rendered disabled with a `coming-soon` badge by the admin UI. No backend endpoint exists in v0.2 (`DELETE /admin/sessions` without path param is not routed). Per-user "logout all sessions" works. | Add `DELETE /admin/sessions` (no path param) and lift the UI's `coming-soon` flag — planned for v0.3. |

---

## 6. Invitation reliability residual (Agent C)

These are explicitly enumerated by Agent C in [INVITATION_RELIABILITY_v0.2.md §"Known limitations"](../validation/INVITATION_RELIABILITY_v0.2.md#known-limitations-still-out-of-scope) and carried here for completeness.

| ID | Severity | Surface | Limitation | Operator action when it occurs |
|----|----------|---------|------------|--------------------------------|
| L7 | Low      | Transient transport failures | The Admin API client retries exactly once on `401` (key-rotation handling) but not on `5xx` or transport-level errors. | The admin retries manually. Compensating delete (L8) keeps the failure mode recoverable. |
| L8 | Low      | Compensating delete is best-effort | A network partition that takes out both the create's email-dispatch step AND the cleanup DELETE leaves an orphan user. Degenerate case (two consecutive Keycloak failures inside ~5 s). | Next admin retry hits `409`; operator deletes the orphan via `DELETE /admin/users/:id` before re-inviting. |
| L9 | Low      | No invitation-expiry renewal on resend | Resending an `expired` invitation re-dispatches the email but does NOT update `expires_at`. The follow-up email arrives with the same stale expiry attribute on the server. | Re-create the invitation instead of resending if expiry has elapsed. Future change: reject resend on expired, or atomically extend `expires_at`. |
| L10 | Low     | Unparseable `expires_at` silently treated as "no expiry" | Provider tries `time.RFC3339` and `time.RFC3339Nano`; anything else falls through to `pending`. Bad attribute data won't poison the listing but also won't alert. | Treat the listing as authoritative; if an invite appears stuck in `pending`, inspect the raw attribute on Keycloak. |

---

## 7. Summary by severity

| Severity | Count | IDs |
|----------|------:|-----|
| Critical | 0     | — |
| High     | 1 *(at final-tag tier; Medium at RC1)* | L4 |
| Medium   | 3     | F1, L1, L5 |
| Low–Med  | 1     | F2 |
| Low      | 6     | F3, L2, L3, L6, L7–L10 |

**Total: 11 documented limitations, 0 release-blocking at RC1 tier.**

---

## 8. What is explicitly *not* a limitation

For clarity, items reviewers sometimes flag that are by design in v0.2:

- **The HTML shell at `/admin` is intentionally public.** Every action it invokes goes through the gated `/admin/*` API surface. Confirmed by Agent A guards G03 + G05/G11/G12/G14/G15/G16.
- **Auth failures return a fixed `{"error":"unauthorized"}` body.** The specific reason is only emitted via the structured `AuthEvent` stream; the wire payload does not leak it. This is the documented middleware contract.
- **RBAC denials respond `403`, not `401`.** Clients can differentiate "log in again" from "you don't have access."
- **Cross-client tokens are rejected at the `azp` check (`401`), not at RBAC (`403`).** A `client_credentials` token minted for `saas-backend-admin` cannot impersonate a user-tier caller — verified by Agent A T6.
- **`POST /admin/roles` race-safety.** 10× parallel creates of the same role name → `1×201 / 9×409`. Confirmed by Agent A T5.

These behaviours are part of the v0.2 contract; if they ever regress they become defects, not limitations.

---

## 9. Maintenance

This document is updated when:

- A new validation pass surfaces a previously-unrecorded gap.
- A limitation is fixed (move it to a "Resolved in …" section or delete it).
- Severity changes because of an upstream decision (e.g. L4 promoted to **Critical** if final 0.2.0 must include audit wiring).

The canonical decision log for individual limitations lives in their source reports; this file is the index.
