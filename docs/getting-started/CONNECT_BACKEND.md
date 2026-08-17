# Connect your backend

> **Where you are:** an operator has created a workspace, connected a Keycloak
> realm, made a project and issued you a credential. You hold three values.
>
> **Where you are going:** a first successful API call, then the four things
> every integration eventually needs.

```bash
export LIGHTWEIGHT_URL=https://identity.example.com
export LIGHTWEIGHT_WORKSPACE_ID=ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301
export LIGHTWEIGHT_API_KEY=lw_sk_…
```

Those three names are the contract. The Go SDK reads exactly them, and has
nowhere to read a fourth from.

If you do not have them, they came from
[step 8 of the previous page](FIRST_CREDENTIAL.md#8-copy-the-credential).

---

## The 30-second check

Before writing any code, prove the credential works:

```bash
curl -sS -H "Authorization: Bearer $LIGHTWEIGHT_API_KEY" \
  "$LIGHTWEIGHT_URL/v1/workspaces/$LIGHTWEIGHT_WORKSPACE_ID/users?max=10"
```

A `200` with a JSON page means everything upstream of you is correct: the
credential is valid, it carries `users:read`, the workspace has an active
connection, and that connection reaches its realm.

Keep this command. When something later fails, it is what tells an SDK problem
apart from a configuration one: if curl works and your code does not, the bug
is yours.

---

## Go

### Install

```bash
go get github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go@v0.1.0
```

> Ask for the **module version**, `@v0.1.0`. The git tag that publishes it is
> `sdk/go/v0.1.0`; those are different strings and only the first belongs in a
> `go get`.

The SDK is a separate module with **no dependencies**. Its `go.mod` has no
`require` block at all, so importing it cannot pull anything into your build.
It needs Go 1.23 or newer.

### First call

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	lightweight "github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go"
)

func main() {
	// Reads LIGHTWEIGHT_URL, LIGHTWEIGHT_WORKSPACE_ID, LIGHTWEIGHT_API_KEY.
	client, err := lightweight.NewClientFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	// Always pass a context with a deadline. The SDK adds no timeout of its
	// own on top of one you set.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	page, err := client.Users.List(ctx, lightweight.UserListOptions{Max: 10})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("%d user(s) on this page\n", page.Count)
	for _, u := range page.Users {
		fmt.Printf("  %s (enabled=%v)\n", u.Email, u.Enabled)
	}
}
```

Needs `users:read`.

`page.Count` is the number of users **on this page**, never a directory total.
There is no total: obtaining one would mean counting the whole realm on every
request. To walk everything, page with `First` and `Max`.

### Create a user

```go
user, err := client.Users.Create(ctx, lightweight.CreateUserRequest{
	Email:             "someone@example.com",
	FirstName:         "Some",
	LastName:          "One",
	TemporaryPassword: generatedPassword,   // at least 8 chars, forced change on first sign-in
})
```

Needs `users:write`. The temporary password is never echoed back, never logged
by the server, and never written to the audit trail. `CreateUserRequest`
redacts it in `String` and `GoString`, so printing the request cannot leak it.
What the SDK cannot control is how long that string lives in your process:
generate it, send it, hand it over, do not persist it.

### Assign a role

```go
err := client.Roles.Grant(ctx, user.ID, "billing-admin")
```

Needs `roles:write`, **in addition to** `users:write` if you are also creating
the user. Granting a role is a role operation, not a user operation, and the
scope model says so.

`CreateUserRequest` also has a `Roles` field to do both in one call. It needs
both scopes for the same reason.

### Errors

Three kinds, kept apart because you react to them differently:

| Type | Meaning |
|---|---|
| `*APIError` | LIGHTWEIGHT answered and refused. Carries `Code`, `StatusCode`, `Field`, `RequestID` |
| `*RequestError` | No answer was obtained: DNS, TCP, TLS, cancellation, deadline. Unwraps |
| `*ProtocolError` | An answer arrived that this client could not read |

```go
var apiErr *lightweight.APIError
if errors.As(err, &apiErr) {
	switch apiErr.Code {
	case lightweight.CodeInsufficientScope:
		// this credential lacks a scope; an operator must mint a new one
	case lightweight.CodeRateLimitExceeded:
		if wait, ok := apiErr.RetryAfter(); ok {
			time.Sleep(wait)
		}
	default:
		log.Printf("lightweight: %s (request %s)", apiErr.Code, apiErr.RequestID)
	}
}
```

`Code` is an **open string**, not a closed enum: a server newer than your build
can return a code this package has no constant for, and it still decodes and is
still readable.

**`RequestID` is the single most useful thing to put in your own logs.** It is
what ties a failure on your side to the server log line holding the real cause,
and it is what a support conversation will ask for.

📖 The complete SDK reference, including pagination, the audit iterator and
secret handling, is [`../../sdk/go/README.md`](../../sdk/go/README.md).
The coverage matrix and release process are in [`../SDK_GO.md`](../SDK_GO.md).

---

## Any other language

There is no second SDK. There is a plain HTTP API, an OpenAPI document
generated from the handlers, and the four rules below. That is enough to write
a client in an afternoon.

### Authentication

One header. No token exchange, no refresh, no expiry to manage unless the
operator set one.

```
Authorization: Bearer lw_sk_<lookup>_<secret>
```

Every path is scoped to your workspace:

```
{LIGHTWEIGHT_URL}/v1/workspaces/{LIGHTWEIGHT_WORKSPACE_ID}/...
```

The workspace id in the path must match the one your credential is bound to. It
is not a lookup key; it is a check. A mismatch is `workspace_mismatch`, not a
`404`, so a copy-paste from another environment fails loudly.

```bash
curl -sS \
  -H "Authorization: Bearer $LIGHTWEIGHT_API_KEY" \
  -H "Content-Type: application/json" \
  -X POST \
  -d '{"email":"someone@example.com","temporary_password":"a-generated-one"}' \
  "$LIGHTWEIGHT_URL/v1/workspaces/$LIGHTWEIGHT_WORKSPACE_ID/users"
```

### The error envelope

Every `/v1` failure has the same shape, whatever produced it:

```json
{
  "error": {
    "code": "insufficient_scope",
    "message": "This credential does not carry users:write",
    "request_id": "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
  }
}
```

| Field | |
|---|---|
| `code` | **Branch on this.** A stable identifier. Statuses collide (three different things return `400`) and messages are prose that will be reworded |
| `message` | Human-readable. Never contains a database error, a SQL fragment or a constraint name |
| `request_id` | Log it |
| `field` | Present only on validation failures, naming the offending request field |

### Request IDs

Every `/v1` response carries `X-Request-Id`. If you send that header, it is
honoured, so a correlation id assigned upstream in your own system survives
into LIGHTWEIGHT's logs.

### `insufficient_scope`

```
HTTP 403
WWW-Authenticate: Bearer error="insufficient_scope", scope="users:write"
{"error":{"code":"insufficient_scope", ...}}
```

Your credential does not carry the scope this route requires. The
`WWW-Authenticate` header names the missing scope (RFC 6750 §3.1), so you do
not have to look it up to report the problem.

**This is not retryable and not fixable in code.** Scopes are immutable after
creation, so an operator must create a new credential with the right scopes and
revoke yours.

Which scope each route needs is stated in that route's description in the
OpenAPI document, and summarised in
[`../PROJECTS.md`](../PROJECTS.md).

Its sibling, `operator_only`, means the route is a control-plane route that no
credential may ever reach, whatever its scopes: workspaces, connections,
projects and credentials are operator-only without exception.

### Rate limiting

```
HTTP 429
Retry-After: 1
{"error":{"code":"rate_limit_exceeded", ...}}
```

Each credential has its own allowance, `RATE_LIMIT_CREDENTIAL_RPS`, default 20
requests per second with a burst of twice that. Honour `Retry-After`.

Note that "no `Retry-After`" and "`Retry-After: 0`" are different: the first
means the server gave no hint, the second means retry now. A client that cannot
tell them apart will hammer a server that never asked to be hammered.

### Retries

LIGHTWEIGHT does **not** implement mutation idempotency
([TD-036](../TECH_DEBT.md#td-036)), and the Go SDK never retries, not even a
`GET`. Write your client the same way: a retried `POST /users` that actually
succeeded the first time creates a second user.

Retrying a `GET` after a `429` or a transport failure is fine. Retrying a
mutation is your risk to take, knowingly.

### The full endpoint reference

[`../swagger.yaml`](../swagger.yaml), also served at `/swagger` by any running
installation, and browsable in the console under *Developer → Swagger*.

It is generated from the handler annotations by `make docs`, and CI fails if
the committed copy drifts, so it is the one API document that cannot be stale.
This page deliberately does not duplicate it.

---

## Next

- Running it in production: [`../operations/RUNNING.md`](../operations/RUNNING.md)
- Reading your workspace's audit trail: [`../AUDIT.md`](../AUDIT.md)
- Rotating this credential: [Rotating, or losing, a credential](FIRST_CREDENTIAL.md#rotating-or-losing-a-credential)
