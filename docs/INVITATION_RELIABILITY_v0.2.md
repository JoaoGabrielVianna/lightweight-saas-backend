# Invitation Reliability — v0.2

Scope: `internal/identity` invitation flow (CreateInvitation, ListInvitations, ResendInvitation). This document captures the reliability contract that the v0.2 changes enforce, the failure modes they close, and what remains out of scope for a future iteration.

## What changed

### 1. Compensating delete on partial CreateInvitation failure

**Failure mode it closes.** Pre-v0.2, `CreateInvitation` walked four sequential calls against Keycloak:

1. `POST /users` — create the principal
2. `POST /users/:id/role-mappings/realm` — grant initial roles
3. `PUT /users/:id/execute-actions-email` — dispatch the invite email
4. `GET /users/:id` — capture the final representation

If step 2 or 3 failed (transient role-mapping error, SMTP misconfigured, network blip), the user from step 1 remained in Keycloak with no roles and no invite email. Every retry from the admin with the same email hit `409 Conflict` because the orphan still occupied the email. The provider docstring acknowledged this as a "Stage B known-limitation; a future Sprint adds compensating delete or transactional flow." This change is that future Sprint.

**Contract now.** If steps 2 or 3 fail, the provider best-effort deletes the half-provisioned user before returning the original error. The cleanup runs on a fresh `context.Background()` with a 5-second timeout — the caller's context may already be cancelled (often the reason we're rolling back), and we don't want a cancelled-ctx error to mask the original failure. Cleanup failures are intentionally swallowed: the caller already has an actionable error, and a failed cleanup leaves a recoverable state (the orphan can be re-deleted on retry).

**Implementation.** See [internal/identity/keycloak/invitations.go](internal/identity/keycloak/invitations.go) — `CreateInvitation` uses a deferred `compensateInvitationCreate` keyed off a `committed` boolean that flips to true only after step 3 (email dispatch) returns nil.

**Boundary.** Step 4 (the trailing GET) does NOT trigger rollback. By that point the invitation is provisioned end-to-end — the user can already see the email in their inbox — so failing the API call on a cosmetic GET would lose work the user can act on. Instead, the provider synthesizes a response from the request fields (see `synthesizeFreshInvitation`).

### 2. ResendInvitation respects user state

**Failure mode it closes.** Pre-v0.2, `ResendInvitation` always re-dispatched the full `[VERIFY_EMAIL, UPDATE_PASSWORD]` action set, regardless of what the user had pending. Two concrete bugs followed:

- A user who completed `VERIFY_EMAIL` but not `UPDATE_PASSWORD` would have `VERIFY_EMAIL` re-added on every resend, forcing them to verify their email again.
- A user who had fully accepted the invitation (no pending actions) would have BOTH actions re-added, effectively un-onboarding them. Same problem for revoked (disabled) users — their accounts would silently receive new required actions despite being disabled by admin policy.

**Contract now.** `ResendInvitation` GETs the user first and:

- Returns `ErrConflict` (mapped to HTTP 409 by the handler) when the user is **disabled** — invitations are terminal; the admin must re-enable the user before resending.
- Returns `ErrConflict` when the user has no pending **invite** actions — that is, no overlap between the user's `requiredActions` and `{VERIFY_EMAIL, UPDATE_PASSWORD}`. This covers both accepted invitations (no pending actions at all) and users who only have unrelated actions like `CONFIGURE_TOTP` queued by an admin.
- Otherwise PUTs only the **intersection** of the invite action set with the user's currently-pending actions. A user who completed `VERIFY_EMAIL` and still needs `UPDATE_PASSWORD` gets only `UPDATE_PASSWORD` in the resend.

**Implementation.** See `intersectInviteActions` in [internal/identity/keycloak/invitations.go](internal/identity/keycloak/invitations.go).

### 3. Status precedence: accepted is terminal

**Failure mode it closes.** Pre-v0.2, `deriveInvitationStatus` checked `expires_at` before checking whether the user had pending actions. An invitation that the user had already completed — but whose `expires_at` attribute later drifted into the past — would be reported as `expired` in the invitation listing. Admins auditing completion status saw false negatives.

**Contract now.** Precedence is explicit and totally ordered:

1. `enabled=false` → `revoked` (terminal, admin opt-out)
2. `enabled=true` AND no pending actions → `accepted` (terminal; `expires_at` is irrelevant)
3. `enabled=true` AND pending actions AND `expires_at` in the past → `expired`
4. else → `pending`

Status values are now constants on the public `identity` package — `identity.InvitationStatusPending`, `InvitationStatusAccepted`, `InvitationStatusExpired`, `InvitationStatusRevoked`. The string values are unchanged for backwards compatibility on the wire.

## Test coverage for the new contract

- `TestCreateInvitation_RoleMappingFails_CompensatesDelete` — DELETE fires when role-mapping returns 500
- `TestCreateInvitation_EmailDispatchFails_CompensatesDelete` — DELETE fires when SMTP returns 500
- `TestCreateInvitation_FinalGetFails_StillReturnsInvitation` — synthesized response, no DELETE
- `TestCreateInvitation_Success_NoCompensatingDelete` — happy path negative control
- `TestResendInvitation_OnlyResendsPendingActions` — PUT body contains only pending subset
- `TestResendInvitation_AlreadyAccepted_ReturnsConflict` — PUT short-circuited
- `TestResendInvitation_Revoked_ReturnsConflict` — PUT short-circuited
- `TestResendInvitation_IgnoresUnrelatedRequiredActions` — `CONFIGURE_TOTP`-only user treated as accepted
- `TestListInvitations_AcceptedWithPastExpiresAt_StaysAccepted` — status precedence fix

All existing tests pass unchanged except `TestResendInvitation_PutsBothActions_AndGetsCurrentState`, which was asserting the bug and is replaced by `TestResendInvitation_OnlyResendsPendingActions`.

## Pagination (added after the initial reliability pass)

`ListInvitations` and `ListUsersByRole` now page through Keycloak via `first`/`max` until a short page or a hard cap of 10,000 records. Page size is 200 — large enough that 1000 users complete in ~5 round-trips, small enough to bound per-page memory.

| Endpoint | Pre-pagination behavior | Post-pagination behavior |
|---|---|---|
| `ListInvitations` | Hardcoded `max=200`, silently truncated at 200 | Walks pages, hard cap at `invitationsHardCap = 10000` |
| `ListUsersByRole` | No `first`/`max`, Keycloak defaulted to 100 | Walks pages, hard cap at `usersByRoleHardCap = 10000` |

The `ListUsersByRole` fix matters specifically for `assertNotLastAdmin` in the service tier: a realm with >100 admins could lose its last admin because the guard only saw the first 100 — pagination closes that gap.

### Stress-test evidence

`internal/identity/keycloak/stress_test.go` exercises both endpoints at 100/500/1000 users against an httptest stub honoring `first`/`max`. Sample timings from a local run (loopback transport):

```
ListInvitations  100 users →  100 invitations in 1.37ms
ListInvitations  500 users →  500 invitations in 2.29ms
ListInvitations 1000 users → 1000 invitations in 3.54ms
ListUsersByRole  100 users →  100 returned in 0.87ms
ListUsersByRole  500 users →  500 returned in 1.20ms
ListUsersByRole 1000 users → 1000 returned in 2.80ms
```

Real-world Keycloak latency dominates (~50ms/round-trip), so a 1000-user realm should land near 250ms — well under the 10s per-request timeout, comfortably under interactive-admin expectations. `TestListInvitations_HardCap_PreventsRunaway` covers the defensive ceiling.

## Known limitations (still out of scope)

- **No automatic retry on transient transport failures.** The Admin API client retries exactly once on `401` (key rotation handling) but not on `5xx` or transport-level errors. Compensating delete makes the *failure mode* recoverable (no orphans), but a transient SMTP failure still surfaces as `ErrAdminAPIUnavailable` to the caller — the admin retries manually.
- **The compensating delete is best-effort.** A network partition that takes out both the create-call's email-dispatch step AND the cleanup DELETE leaves an orphan. The next admin retry will hit 409 and must manually delete the orphan via `DELETE /admin/users/:id` before re-inviting. This is a degenerate case (two consecutive Keycloak failures inside ~5 seconds) but operators should know it's possible.
- **No invitation-expiry renewal on resend.** Resending an `expired` invitation re-dispatches the email but does NOT update `expires_at`. The follow-up email will appear to the user with the same (stale) expiry attribute on the server. A future change would either reject resend on expired invitations or atomically extend the expiry.
- **Unparseable `expires_at` values are silently treated as "no expiry."** The provider tries `time.RFC3339` and `time.RFC3339Nano`. Anything else falls through to `pending`. Bad attribute data shouldn't poison the listing, but operators with truly malformed values won't be alerted from this code path.
