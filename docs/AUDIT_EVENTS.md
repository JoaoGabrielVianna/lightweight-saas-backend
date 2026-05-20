# Audit Events — v0.2 Observability Foundation

Status: **model + log sink shipped**, mutation-site wiring **pending**
(owned by the identity package — see "Wiring" below).

## Scope

The audit subsystem records every admin-driven mutation against the
identity surface so operators can answer "who did what, to whom, from
where, and when" without a database query. The v0.2 deliverable is the
event model plus a structured-log sink; the v0.3 deliverable (Sprint 4)
will swap the sink for a persisted `audit_log` table.

## Required fields

Per the v0.2 scope every event MUST carry:

| Field      | Source                                   | Notes                                              |
|------------|------------------------------------------|----------------------------------------------------|
| who        | `audit.Actor` (Subject / Email / Username)| Extracted from `auth.IdentityFrom(c)`              |
| action     | `audit.Action` enum                       | Canonical verb, see vocabulary below               |
| target     | `audit.Target` (Kind / ID / Name)         | The user/role/session/invitation being mutated     |
| timestamp  | `audit.Event.Timestamp` (UTC)             | Stamped automatically when left zero               |
| ip         | `audit.Event.IP`                          | Resolved via `gin.Context.ClientIP()`              |

Two optional fields exist for nuance:

- `Reason` — populated on failure events (e.g. "last admin guard")
- `Extra` — free-form `map[string]any` for per-event detail
  (e.g. `{"roles":["editor","support"]}`)

## Action vocabulary

Defined as `audit.Action` constants in
[`internal/audit/event.go`](../internal/audit/event.go).

### User mutations
- `user.created`
- `user.updated`
- `user.deleted`
- `user.roles_granted`
- `user.role_revoked`
- `user.password_reset`

### Role mutations
- `role.created`
- `role.updated`
- `role.deleted`

### Session revokes
- `session.revoked`           — single session, `DELETE /admin/sessions/:id`
- `user.sessions_logged_out`  — all of a user's sessions, `DELETE /admin/users/:id/sessions`

### Invitation lifecycle
- `invitation.created`
- `invitation.resent`
- `invitation.revoked`

New actions are additive — adding a constant is backwards-compatible;
renaming or removing one is breaking for downstream log/metric consumers.

## Architecture

```
   identity handler  ─┐
                      │ audit.Record(ctx, event)
   identity service  ─┤
                      ▼
              internal/audit  (model + dispatcher)
                      │
                      ▼
            internal/logging.AuditSink
                      │
                      ▼
              project logger → stdout
                      │
                      ▼
            (Sprint 4) audit_log DB table
```

- `internal/audit` is provider-agnostic — no gin, no Keycloak, no logger
  import. Only the event shape and a swappable `Recorder` registry.
- `internal/logging` is the bridge. It depends on the project logger and
  knows how to extract Actor/IP from a `*gin.Context`.
- The default recorder is a no-op until bootstrap calls
  `logging.WireDefault()`. Tests can swap recorders freely via
  `audit.SetDefault`.

## Output format

`AuditSink` emits one log line per event, prefixed with the literal
`audit ` (note trailing space) so downstream filters can grep cheaply:

```
2026-05-20 12:00:00 [ INFO  ] [ audit      ] audit {"action":"user.updated","actor":{"subject":"<uuid>","email":"admin@example.com"},"target":{"kind":"user","id":"<uuid>","name":"jane@example.com"},"ip":"10.0.0.1","ts":"2026-05-20T12:00:00Z"}
```

The JSON payload is the exact serialised form of `audit.Event`, so
consumers can `json.Unmarshal` straight back into the Go struct.

## Wiring (pending — out of scope for this agent)

Two integration steps remain. Both touch files outside this agent's
ownership and are listed here for the identity-package owner:

### 1. Bootstrap

In the application bootstrap (likely `cmd/api/main.go` or
`internal/server/server.go`) call:

```go
import "github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logging"

logging.WireDefault()
```

This swaps the no-op recorder for `AuditSink`. Without it `audit.Record`
calls are silently dropped.

### 2. Call sites in `internal/identity/handler.go`

Each mutation handler should call `audit.Record` after the service
returns success. The helper `logging.EventFromGin` fills in
who/ip automatically:

```go
import (
    "github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/audit"
    "github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/logging"
)

func (h *Handler) DeleteUser(c *gin.Context) {
    targetID := c.Param("id")
    if err := h.service.DeleteUser(c.Request.Context(), callerSubject(c), targetID); err != nil {
        handleError(c, err)
        return
    }
    audit.Record(c.Request.Context(), logging.EventFromGin(c, audit.Event{
        Action: audit.ActionUserDeleted,
        Target: audit.Target{Kind: "user", ID: targetID},
    }))
    c.Status(http.StatusNoContent)
}
```

Mapping of handler → action:

| Handler                          | Action                          | Target.Kind  |
|----------------------------------|---------------------------------|--------------|
| `CreateRole`                     | `role.created`                  | role         |
| `UpdateRole`                     | `role.updated`                  | role         |
| `DeleteRole`                     | `role.deleted`                  | role         |
| `UpdateUser`                     | `user.updated`                  | user         |
| `DeleteUser`                     | `user.deleted`                  | user         |
| `AssignRolesToUser`              | `user.roles_granted`            | user         |
| `UnassignRoleFromUser`           | `user.role_revoked`             | user         |
| `ResetUserPassword`              | `user.password_reset`           | user         |
| `DeleteSession`                  | `session.revoked`               | session      |
| `LogoutUserSessions`             | `user.sessions_logged_out`      | user         |
| `CreateInvitation`               | `invitation.created`            | invitation   |
| `ResendInvitation`               | `invitation.resent`             | invitation   |
| `DeleteInvitation`               | `invitation.revoked`            | invitation   |

## Testing

Unit tests in [`internal/audit/recorder_test.go`](../internal/audit/recorder_test.go)
cover the dispatcher (timestamp stamping, recorder swap, concurrent
emission). Sink tests in
[`internal/logging/audit_sink_test.go`](../internal/logging/audit_sink_test.go)
verify the on-disk JSON shape round-trips.

Integration tests for mutation handlers should register a capturing
`RecorderFunc` in `TestMain` and assert on the captured events — example
pattern in `recorder_test.go`.

## Forward compatibility (Sprint 4)

When the `audit_log` table lands, the migration is:

1. Implement a new `Recorder` that inserts each event into the table.
2. Wrap it around `AuditSink` so logs *and* the table receive every
   event during the rollout window.
3. Swap via `audit.SetDefault` from bootstrap; no call site changes.

The model (`audit.Event`) is the contract — keep its JSON shape stable
and the migration stays low-risk.
