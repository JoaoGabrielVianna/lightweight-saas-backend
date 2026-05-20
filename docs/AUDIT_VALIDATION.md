# Audit Validation — v0.2 Mutation Emission

**Verdict: PASS** — all 7 mutations listed in the validation mission
emit an audit event carrying the five required fields
(`actor`, `action`, `target`, `timestamp`, `ip`). The failure-path
invariant (events also carry `reason`) was independently verified.

- Date: 2026-05-20
- Method: end-to-end harness — real `logging.AuditSink` wired via
  `logging.WireDefault()`, stdout captured into a buffer, each emitted
  `audit {...}` line parsed back into `audit.Event` and field-checked.
- Test: [`internal/identity/handler_audit_validation_test.go`](../internal/identity/handler_audit_validation_test.go)
- Run command:
  ```sh
  go test ./internal/identity/... -run TestAudit_Validation -v -count=1
  ```

## Why this is "validation" not "another unit test"

The unit suite in
[`handler_audit_test.go`](../internal/identity/handler_audit_test.go)
uses a capturing in-memory recorder — that proves the call sites build
the right `Event` struct. But it does **not** prove the JSON ever
reaches stdout, nor that the JSON shape survives serialisation.

The validation harness here flips the recorder back to the production
`AuditSink` so the full chain runs:

```
handler  →  audit.Record  →  AuditSink  →  logger.Info  →  log.Println  →  stdout (captured)
                                                                                  │
                                                          parsed back  ←──────────┘
                                                          assert all 5 fields present
```

That is the closest faithful reproduction of a live HTTP probe we can
run without a Keycloak realm online — and it doesn't depend on
fixtures, network, or token issuance.

## Methodology

For each of the seven listed mutations:

1. Build a fresh in-memory `fakeProvider` (the one `service_test.go`
   already uses); stage admin-set when a guard needs to be cleared
   (`DeleteUser`).
2. Build a `Handler` from the fake.
3. Construct a `*gin.Context` via `httptest.NewRecorder` +
   `gin.CreateTestContext`, stash an admin `auth.Identity` via
   `auth.StoreIdentity`, set `RemoteAddr = 127.0.0.1:1234` so
   `c.ClientIP()` resolves.
4. Invoke the handler directly.
5. Read the bytes that landed in the captured stdout since the previous
   step, locate every line matching ` audit {`, `json.Unmarshal` it
   back to `audit.Event`.
6. Check `Actor.Subject || Email || Username` is non-empty (`who`),
   `Action` is the expected constant, `Target.Kind` is non-empty,
   `Timestamp` is non-zero, `IP` is non-empty.

A mutation FAILS validation if **any** of those checks fail.

## Results — PASS/FAIL table (machine-generated)

| Mutation            | Expected action          | Status | Detail                                                                                                                |
|---------------------|--------------------------|--------|-----------------------------------------------------------------------------------------------------------------------|
| Create role         | `role.created`           | PASS   | actor=aaaaaaaa-…  target=role/support-validation  ip=127.0.0.1  ts=10:24:50.997                                       |
| Delete role         | `role.deleted`           | PASS   | actor=aaaaaaaa-…  target=role/support-validation  ip=127.0.0.1  ts=10:24:50.998                                       |
| Delete user         | `user.deleted`           | PASS   | actor=aaaaaaaa-…  target=user/bbbbbbbb-…  ip=127.0.0.1  ts=10:24:50.998                                               |
| Assign role         | `user.roles_granted`     | PASS   | actor=aaaaaaaa-…  target=user/bbbbbbbb-…  ip=127.0.0.1  ts=10:24:50.998  (roles=[editor] in Extra)                    |
| Reset password      | `user.password_reset`    | PASS   | actor=aaaaaaaa-…  target=user/bbbbbbbb-…  ip=127.0.0.1  ts=10:24:50.998                                               |
| Delete invitation   | `invitation.revoked`     | PASS   | actor=aaaaaaaa-…  target=invitation/bbbbbbbb-…  ip=127.0.0.1  ts=10:24:50.998                                         |
| Revoke session      | `session.revoked`        | PASS   | actor=aaaaaaaa-…  target=session/cccccccc-…  ip=127.0.0.1  ts=10:24:50.998                                            |

`actor` rendered as `aaaaaaaa-…` is the test admin subject
`aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa`; the captured JSON below shows
the full UUID + email + username triple.

## Captured stdout lines (verbatim)

These are the exact lines that `AuditSink` wrote to the buffer standing
in for stdout. ANSI colour escapes from the project logger are visible
because we captured the byte stream directly:

```
[Create role]
2026-05-20 07:24:50 [44m[97m INFO  [0m [ audit      ] audit {"action":"role.created","actor":{"subject":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","email":"admin@example.com","username":"adminuser"},"target":{"kind":"role","id":"support-validation","name":"support-validation"},"ip":"127.0.0.1","ts":"2026-05-20T10:24:50.997706Z"}

[Delete role]
2026-05-20 07:24:50 [44m[97m INFO  [0m [ audit      ] audit {"action":"role.deleted","actor":{"subject":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","email":"admin@example.com","username":"adminuser"},"target":{"kind":"role","id":"support-validation","name":"support-validation"},"ip":"127.0.0.1","ts":"2026-05-20T10:24:50.998154Z"}

[Delete user]
2026-05-20 07:24:50 [44m[97m INFO  [0m [ audit      ] audit {"action":"user.deleted","actor":{"subject":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","email":"admin@example.com","username":"adminuser"},"target":{"kind":"user","id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},"ip":"127.0.0.1","ts":"2026-05-20T10:24:50.99819Z"}

[Assign role]
2026-05-20 07:24:50 [44m[97m INFO  [0m [ audit      ] audit {"action":"user.roles_granted","actor":{"subject":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","email":"admin@example.com","username":"adminuser"},"target":{"kind":"user","id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},"ip":"127.0.0.1","ts":"2026-05-20T10:24:50.998248Z","extra":{"roles":["editor"]}}

[Reset password]
2026-05-20 07:24:50 [44m[97m INFO  [0m [ audit      ] audit {"action":"user.password_reset","actor":{"subject":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","email":"admin@example.com","username":"adminuser"},"target":{"kind":"user","id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},"ip":"127.0.0.1","ts":"2026-05-20T10:24:50.998284Z"}

[Delete invitation]
2026-05-20 07:24:50 [44m[97m INFO  [0m [ audit      ] audit {"action":"invitation.revoked","actor":{"subject":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","email":"admin@example.com","username":"adminuser"},"target":{"kind":"invitation","id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},"ip":"127.0.0.1","ts":"2026-05-20T10:24:50.9983Z"}

[Revoke session]
2026-05-20 07:24:50 [44m[97m INFO  [0m [ audit      ] audit {"action":"session.revoked","actor":{"subject":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","email":"admin@example.com","username":"adminuser"},"target":{"kind":"session","id":"cccccccc-cccc-cccc-cccc-cccccccccccc"},"ip":"127.0.0.1","ts":"2026-05-20T10:24:50.998348Z"}
```

## Field-by-field verification

Re-projecting the captured payloads to show every required field is
populated on every event:

| Mutation            | who (`actor.subject` / `email`)             | action               | target (`kind` / `id`)               | timestamp                    | ip          |
|---------------------|---------------------------------------------|----------------------|--------------------------------------|------------------------------|-------------|
| Create role         | aaaaaaaa-…  / admin@example.com             | role.created         | role / support-validation            | 2026-05-20T10:24:50.997706Z  | 127.0.0.1   |
| Delete role         | aaaaaaaa-…  / admin@example.com             | role.deleted         | role / support-validation            | 2026-05-20T10:24:50.998154Z  | 127.0.0.1   |
| Delete user         | aaaaaaaa-…  / admin@example.com             | user.deleted         | user / bbbbbbbb-…                    | 2026-05-20T10:24:50.99819Z   | 127.0.0.1   |
| Assign role         | aaaaaaaa-…  / admin@example.com             | user.roles_granted   | user / bbbbbbbb-…                    | 2026-05-20T10:24:50.998248Z  | 127.0.0.1   |
| Reset password      | aaaaaaaa-…  / admin@example.com             | user.password_reset  | user / bbbbbbbb-…                    | 2026-05-20T10:24:50.998284Z  | 127.0.0.1   |
| Delete invitation   | aaaaaaaa-…  / admin@example.com             | invitation.revoked   | invitation / bbbbbbbb-…              | 2026-05-20T10:24:50.9983Z    | 127.0.0.1   |
| Revoke session      | aaaaaaaa-…  / admin@example.com             | session.revoked      | session / cccccccc-…                 | 2026-05-20T10:24:50.998348Z  | 127.0.0.1   |

All seven rows: every required field populated. **No missing events.**

## Failure-path invariant

`TestAudit_Validation_FailurePathEmitsReason` ran an additional probe:
forced the fake provider to return `errors.New("provider rejected
revoke")` on `DeleteSession`, then validated the emitted event still
carries the five required fields **plus** `reason`:

```
2026-05-20 07:24:50 [44m[97m INFO  [0m [ audit      ] audit {"action":"session.revoked","actor":{"subject":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","email":"admin@example.com","username":"adminuser"},"target":{"kind":"session","id":"cccccccc-cccc-cccc-cccc-cccccccccccc"},"ip":"127.0.0.1","ts":"2026-05-20T10:24:50.998629Z","reason":"provider rejected revoke"}
```

PASS — `reason` field present, contains the upstream error.

## Missing events

**None.** Every mutation in the validation list emitted an event in the
captured stdout segment that ran after the handler returned. The
expected `Action` was present in every case; the five required fields
were all populated.

## Pre-existing observation (not a validation failure)

`CreateInvitation` (not in this validation list) emits **two** events
by design — `invitation.created` followed by `user.created` — because
in this codebase invitations are the only path that provisions a
Keycloak user. The dual emission is documented in
[`docs/AUDIT_WIRING.md`](AUDIT_WIRING.md#note-on-usercreated) and
covered by `TestAudit_CreateInvitation_EmitsBothInvitationAndUser` in
the unit suite.

## Final verdict

| Aspect                                              | Result |
|-----------------------------------------------------|--------|
| Every listed mutation emits at least one event      | ✅ PASS |
| Every event has `actor` (subject / email / username)| ✅ PASS |
| Every event has the expected `action`               | ✅ PASS |
| Every event has `target.kind`                       | ✅ PASS |
| Every event has non-zero `timestamp` (UTC, RFC3339) | ✅ PASS |
| Every event has `ip`                                | ✅ PASS |
| Failure path additionally carries `reason`          | ✅ PASS |
| JSON shape round-trips back into `audit.Event`      | ✅ PASS |

**Audit validation: PASS.**

## Reproducing the run

```sh
go test ./internal/identity/... \
        -run TestAudit_Validation \
        -v -count=1
```

Expected: both tests `PASS`, with the table and stdout-capture blocks
above appearing under `=== RUN   TestAudit_Validation_AllRequiredMutationsEmit`.
