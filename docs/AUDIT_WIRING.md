# Audit Wiring — v0.2 Mutation Coverage

This doc records how every admin-driven mutation is wired to the audit
subsystem ([`internal/audit`](../internal/audit)) via the
[`internal/logging`](../internal/logging) sink. It is the companion to
[`docs/AUDIT_EVENTS.md`](AUDIT_EVENTS.md) (which defines the model and
the action vocabulary). If you're adding a new mutation handler, the
checklist at the bottom is the contract you must satisfy.

## Status

| Concern                          | Status                                         |
|----------------------------------|------------------------------------------------|
| Event model + action constants   | ✅ shipped — [`internal/audit/event.go`](../internal/audit/event.go) |
| Recorder registry                | ✅ shipped — [`internal/audit/recorder.go`](../internal/audit/recorder.go) |
| Structured-log sink              | ✅ shipped — [`internal/logging/audit_sink.go`](../internal/logging/audit_sink.go) |
| Bootstrap registration           | ✅ wired in `cmd/api/main.go` via `logging.WireDefault()` |
| Mutation call-site emission      | ✅ all 13 handlers in [`internal/identity/handler.go`](../internal/identity/handler.go) |
| Required-fields invariant tests  | ✅ [`internal/identity/handler_audit_test.go`](../internal/identity/handler_audit_test.go) |
| Persisted `audit_log` table      | ⏳ Sprint 4 — out of v0.2 scope                |

## Invariants (per mission)

Every mutation MUST emit an audit event carrying:

- **who** — `Actor.Subject` / `Email` / `Username`, populated from the
  validated identity that `auth.RequireAuth` stashed on the gin context.
- **action** — one of the `audit.Action*` constants.
- **target** — `Target.Kind` (e.g. `"user"`, `"role"`, `"session"`,
  `"invitation"`) plus an `ID` (Keycloak sub UUID, role name, session
  UUID) and optionally `Name` for a human-readable label.
- **timestamp** — UTC, stamped by `audit.Record` if the caller leaves it
  zero.
- **ip** — captured from `gin.Context.ClientIP()`.

Failures MUST additionally emit:

- **reason** — `err.Error()` of whatever the service returned.

The helper [`logging.RecordMutation`](../internal/logging/gin_helpers.go)
encapsulates the success / failure branch so the 13 call sites can't
drift apart:

```go
err := h.service.DeleteUser(c.Request.Context(), callerSubject(c), targetID)
logging.RecordMutation(c, audit.ActionUserDeleted,
    audit.Target{Kind: "user", ID: targetID}, err)
if err != nil {
    handleError(c, err)
    return
}
c.Status(http.StatusNoContent)
```

## Handler → Action wiring map

| Handler                          | Action(s) emitted               | Target.Kind  | Notes |
|----------------------------------|---------------------------------|--------------|-------|
| `CreateRole`                     | `role.created`                  | role         | Target carries normalized role name |
| `CreateInvitation`               | `invitation.created` **and** `user.created` | invitation + user | See note below — invitation is the only path that provisions a user in this codebase |
| `UpdateUser`                     | `user.updated`                  | user         | Target.Name = updated email on success |
| `UpdateRole`                     | `role.updated`                  | role         | |
| `AssignRolesToUser`              | `user.roles_granted`            | user         | Roles list rides in `Extra["roles"]` |
| `UnassignRoleFromUser`           | `user.role_revoked`             | user         | Target.Name = role name being removed |
| `ResetUserPassword`              | `user.password_reset`           | user         | |
| `DeleteUser`                     | `user.deleted`                  | user         | |
| `DeleteRole`                     | `role.deleted`                  | role         | |
| `DeleteSession`                  | `session.revoked`               | session      | |
| `LogoutUserSessions`             | `user.sessions_logged_out`      | user         | Bulk session revoke for one user |
| `ResendInvitation`               | `invitation.resent`             | invitation   | Target.Name = invitee email on success |
| `DeleteInvitation`               | `invitation.revoked`            | invitation   | |

### Note on `user.created`

The v0.2 admin surface has no standalone "create user" endpoint — the
only path that provisions a Keycloak user is `CreateInvitation`. To keep
the audit semantics honest, that handler emits **two** events: one for
the invitation lifecycle (`invitation.created`) and one for the
underlying user provisioning (`user.created`). Downstream consumers can
join them on `Target.ID` (same Keycloak UUID).

If a future stage adds a direct `POST /admin/users` handler, it should
emit only `user.created` — the dual emission here is a v0.2 stopgap.

## Bootstrap

`cmd/api/main.go` calls `logging.WireDefault()` early, right after the
auth event hook is registered:

```go
provider := mustBuildAuthProvider(cfg)
auth.SetEventHook(authEventLogger)

// Wire the audit subsystem to the structured-log sink. Until this
// runs every audit.Record call is silently dropped by the package-
// level noop recorder.
logging.WireDefault()
```

Without this single line every `audit.Record` call is a no-op (this is
deliberate — it means test binaries don't have to register a sink they
don't care about).

## Test coverage

[`internal/identity/handler_audit_test.go`](../internal/identity/handler_audit_test.go)
exercises every mutation handler with:

- A capturing `audit.Recorder` swapped in via `audit.SetDefault`.
- The admin identity pre-stashed on the gin context via
  `auth.StoreIdentity`.
- An in-memory `fakeProvider` (the same one `service_test.go` uses) so
  the test stays close to the real call shape without touching Keycloak.

Each success case asserts the invariants via `assertRequiredFields`
(action, actor.subject, actor.email, target.kind, timestamp, ip). Three
failure cases additionally assert `Reason` is non-empty and contains the
upstream error message:

- `TestAudit_DeleteUser_Failure_CarriesReason`
- `TestAudit_AssignRoles_Failure_CarriesReason`
- `TestAudit_CreateRole_Failure_StillEmitsForBothInvitationAndUser`

Helper-level tests for the sink and dispatcher live in
[`internal/logging/audit_sink_test.go`](../internal/logging/audit_sink_test.go)
and [`internal/audit/recorder_test.go`](../internal/audit/recorder_test.go).

Run the audit slice alone with:

```sh
go test ./internal/audit/... ./internal/logging/... \
        ./internal/identity/... -run TestAudit -v
```

## Checklist — adding a new mutation handler

1. Pick (or add) an `audit.Action*` constant in
   [`internal/audit/event.go`](../internal/audit/event.go). Lowercase
   `kind.verb` form.
2. After calling the service, emit:
   ```go
   logging.RecordMutation(c, audit.ActionFoo,
       audit.Target{Kind: "foo", ID: id, Name: humanName}, err)
   ```
3. If extra context is essential (e.g. a list of roles), build the event
   inline:
   ```go
   event := logging.EventFromGin(c, audit.Event{
       Action: audit.ActionFoo,
       Target: audit.Target{Kind: "foo", ID: id},
       Extra:  map[string]any{"roles": rolesCopy},
   })
   if err != nil {
       event.Reason = err.Error()
   }
   audit.Record(c.Request.Context(), event)
   ```
4. Add a success-path and (when the failure mode is observable in tests)
   a failure-path test in
   [`internal/identity/handler_audit_test.go`](../internal/identity/handler_audit_test.go).
5. Update the handler→action table above.

## Forward path (Sprint 4)

When the persisted `audit_log` table lands the migration is purely
additive — `audit.SetDefault` swaps the recorder so logs *and* the
database receive every event during the rollout window. No call site
changes. See [`docs/AUDIT_EVENTS.md`](AUDIT_EVENTS.md) for the
forward-compatibility plan.
