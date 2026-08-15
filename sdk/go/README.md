# LIGHTWEIGHT Go SDK

The official Go client for the LIGHTWEIGHT identity API.

It is written for one caller: a backend service that has been issued a **Project
Credential** and needs to administer identities inside one workspace.

```go
client, err := lightweight.NewClientFromEnv()
if err != nil {
    return err
}

page, err := client.Users.List(ctx, lightweight.UserListOptions{Max: 50})
if err != nil {
    return err
}
for _, u := range page.Users {
    fmt.Println(u.Email)
}
```

---

## The three variables

```
LIGHTWEIGHT_URL           where the API is
LIGHTWEIGHT_WORKSPACE_ID  which tenant this backend acts on
LIGHTWEIGHT_API_KEY       the Project Credential
```

That is the whole configuration surface, and there is deliberately nowhere to put
a fourth. A backend using this package never learns which identity provider sits
behind LIGHTWEIGHT, which tenant of it this workspace routes to, or what
credential opens it. An operator can repoint the workspace at a different
provider configuration and your service keeps working — unchanged, unrestarted —
because it was never told what it was pointing at.

That indirection is the product. This package is the smallest expression of it,
and a test fails the build if `Config` ever grows a field that would undo it.

---

## Getting a credential

The steps below are the **operator's**, done once. A backend developer needs none
of them — only the three values that fall out at the end.

1. Operator creates a **Workspace**.
2. Operator connects it to an identity provider (a **Connection**).
3. Operator creates a **Project** inside the workspace — one per consuming
   service.
4. Operator creates a **Project Credential** on that project, choosing its
   scopes. **The secret is shown once, at creation, and is never recoverable.**
5. The backend is configured with the three variables above.
6. The backend imports this package.

The scopes chosen in step 4 decide what your service can do. Each method below
documents the one it needs; ask for the narrowest set that covers your
integration, because a key cannot be widened later without being replaced.

---

## Install

The SDK is a **nested Go module**, so it is fetched by its own path:

```
go get github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go@v0.1.0
```

```go
import lightweight "github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go"
```

The import alias is worth writing out: the path's last element is `go`, while the
package is named `lightweight`.

> **No release has been published yet.** The command above is the verified
> installation form — it is what will work, unchanged, from the first release
> onward — but `v0.1.0` does not exist on GitHub yet, so running it today fails.
> See [Versioning](#versioning).

Ask for the **version**, `@v0.1.0`, and not for the git tag. Those are different
strings for a nested module: the tag that publishes this version is
`sdk/go/v0.1.0`, carrying the directory as a prefix because that is how Go finds
a module that is not at the root of its repository. Go derives one from the
other, so a consumer never types the prefix.

### Requirements

* Go 1.23 or later.
* **No dependencies.** `go.sum` is empty and a CI gate keeps it that way — a
  dependency here would become a dependency of every backend that imports this,
  along with its transitive graph and its advisories.

---

## Errors

Three kinds, kept apart because a caller reacts to them differently.

| Type             | Meaning                                                           |
| ---------------- | ----------------------------------------------------------------- |
| `*APIError`      | LIGHTWEIGHT answered, and refused. Carries `Code`, `StatusCode`, `Field`, `RequestID`. |
| `*RequestError`  | No answer was obtained: DNS, TCP, TLS, cancellation, deadline. Unwraps. |
| `*ProtocolError` | An answer arrived that this client could not read.                |

```go
var apiErr *lightweight.APIError
if errors.As(err, &apiErr) {
    switch apiErr.Code {
    case lightweight.CodeInsufficientScope:
        // the credential is missing a scope; an operator must mint a new one
    case lightweight.CodeRateLimitExceeded:
        if wait, ok := apiErr.RetryAfter(); ok {
            time.Sleep(wait)
        }
    default:
        log.Printf("lightweight: %s (request %s)", apiErr.Code, apiErr.RequestID)
    }
}
```

`Code` is an **open string**, never a closed enum: a server newer than your build
can return a code this package has no constant for, and it will still decode and
still be readable. `errors.Is(err, context.Canceled)` and
`errors.Is(err, context.DeadlineExceeded)` keep working through `*RequestError`.

**`RequestID` is the single most useful thing to carry into your own logs.** It
is what ties a failure here to the server-side log line holding the real cause,
and it is what a support conversation will ask for.

---

## What this package will not do

* **It never retries.** Not even a GET. A client that silently retries a mutation
  it believes idempotent eventually creates two users, and this API has no
  idempotency keys to make that safe. Retry policy belongs to you, because only
  you know which of your operations tolerate it.
* **It never caches authorization.** A credential revoked by an operator stops
  working on the very next call through a live client. There is nothing between
  the call and the server to remember that it used to work.
* **It never logs, meters or traces.** No logger, no metrics, no hooks. Supply
  `Config.HTTPClient` with your own `http.RoundTripper` — that is where tracing,
  metrics and retries already belong, and it means this package does not have to
  invent a second abstraction for each of them.
* **It never touches `http.DefaultClient` or `http.DefaultTransport`.**

A `Client` is immutable once constructed and safe for concurrent use. Construct
one per credential and share it. To rotate to a new key, construct a new client —
there is deliberately no mutable secret state.

---

## Secret handling

The API key is sent as a bearer token and nowhere else: never in a URL, never in
a query parameter, never in the User-Agent, and never in any error this package
produces.

`Config`, `Client` and `CreateUserRequest` implement `String` **and** `GoString`,
so `%v`, `%+v`, `%s` and `%#v` all render a redacted form. That is a security
control, not a convenience: without it, Go's default struct formatting prints the
credential, and the places a config gets printed are exactly the places a secret
must not reach.

`CreateUserRequest.TemporaryPassword` is never echoed by the server, never
logged by it, and never recorded in the audit trail. What this package cannot do
is control the lifetime of the string in your process: generate it, send it, hand
it to the user, do not persist it.

---

## Pagination

Three endpoints families, three genuinely different models. This package exposes
all three rather than pretending they are one.

| Collection                       | Model      | Shape                                    |
| -------------------------------- | ---------- | ---------------------------------------- |
| Users                            | offset     | `UserPage{Users, First, Max, Count}`     |
| Roles, Sessions, Invitations     | complete   | a plain slice                            |
| Audit                            | cursor     | `AuditPage{Items, Pagination}`           |

For the audit trail, loop while the cursor is present. The **absence** of
`NextCursor` is the end-of-history signal — an empty page is not the terminator,
and a full page can be the last one:

```go
opts := lightweight.AuditListOptions{Outcome: lightweight.AuditOutcomeFailure}
for {
    page, err := client.Audit.List(ctx, opts)
    if err != nil {
        return err
    }
    for _, ev := range page.Items {
        handle(ev)
    }
    if !page.HasMore() {
        break
    }
    opts.Cursor = page.Pagination.NextCursor
}
```

`client.Audit.All(ctx, opts)` is a range-over-func convenience over the same
thing. It is not the primary API: a caller that needs to checkpoint, resume after
a restart, or stop after N pages needs the cursor in its own hands.

---

## Rate limiting

Each credential has its own allowance. Over it, the server answers `429` with
`rate_limit_exceeded` and a `Retry-After` header, which
[`APIError.RetryAfter`](#errors) surfaces:

```go
if wait, ok := apiErr.RetryAfter(); ok {
    time.Sleep(wait)
}
```

`ok` is false when the response carried no hint. That is reported separately from
a zero duration on purpose: "retry immediately" and "the server said nothing" call
for different behaviour, and a caller that cannot tell them apart will hammer a
server that never asked to be hammered.

This package does not back off for you, and does not retry rate-limited calls.

---

## Verifying with curl

For the minimal example, the equivalent request:

```bash
curl -sS -H "Authorization: Bearer $LIGHTWEIGHT_API_KEY" \
  "$LIGHTWEIGHT_URL/v1/workspaces/$LIGHTWEIGHT_WORKSPACE_ID/users?max=10"
```

If curl works and the SDK does not, the bug is in the SDK. If neither works, the
credential or the workspace is wrong, and the error body will say which. That
distinction is worth the four lines; the rest of the API is not duplicated here.

---

## Coverage of the API

Every route a Project Credential can reach is served by a method here — 24 of 24.
The classification lives in [`apicoverage.json`](apicoverage.json) and is gated
from both sides: a test in the server module checks it against the authorization
registry and the OpenAPI document, and a test in this module checks that each
entry names a method that exists and documents the right scope.

Operator-only routes (workspaces, connections, projects, credentials, and
`PUT .../users/{id}/password`) are **not** exposed. A Project Credential cannot
call them whatever scopes it holds, so a method for one could only ever produce a
`403` — and offering it would suggest a scope exists that would make it work.

See [docs/SDK_GO.md](../../docs/SDK_GO.md) for the full matrix.

---

## Versioning

The SDK's version and the HTTP API's version are **different things**.

* The HTTP API is `/v1`. That is the wire contract, and it changes rarely.
  **SDK v0.x targets LIGHTWEIGHT HTTP API `/v1`** — that one line is the whole
  compatibility statement, and it will stay that way until there is a second
  thing to say.
* This module versions independently. The tag that publishes a version is
  `sdk/go/vX.Y.Z`; the version you ask for is `vX.Y.Z`. Go requires the
  directory prefix on the tag for a nested module and strips it to get the
  version, so the two strings are never the same and only one of them is yours.

The SDK starts at **v0.x**, which is a statement rather than modesty: while the
major version is zero, the Go API may change between minor versions.

| change | release |
|---|---|
| bug fix, doc fix, internal refactor | patch |
| new method, field, or error code | minor |
| removal, rename, or signature change | minor, and called out in the release notes |

A breaking change pre-v1 is permitted, but not permitted to be *silent*: every
exported declaration is recorded in [`api.txt`](api.txt), so a removal or a
renamed field shows up as a `-` line in review and the release gate fails until
someone re-records it deliberately.

It reaches v1.0.0 when the exported surface has been used by more than one real
integration without needing a breaking change. From v2 onward Go's import
compatibility rule applies and the module path itself would gain a `/v2` suffix.

An SDK v1.0.0 would not imply the server API had changed, and a future `/v2`
would not force an SDK v2.0.0 — a single SDK release could speak both.

**No release has been published.** Publishing is a separate decision from
building; the mechanics are proven and documented in
[docs/SDK_GO.md](../../docs/SDK_GO.md#release).

---

## Development

```bash
make sdk-test             # unit and contract suites
make sdk-test-race        # concurrent-client tests under -race
make sdk-vet              # including the acceptance build tag
make sdk-deps-check       # fails if the SDK acquires any dependency
make sdk-coverage-gate    # separate floor, reported separately from the server's
make sdk-api-check        # fails if the exported API drifted from api.txt
make sdk-consumer-check   # an external module compiles against the SDK alone
make sdk-check            # all of the above
make sdk-mutation-check   # proves the tests fail when the behaviour breaks

DB_URL=postgres://…  make sdk-acceptance   # against a real stack
```

Changing the exported API on purpose:

```bash
make sdk-api-update      # re-record api.txt, then review the diff
```

Release mechanics — none of these tag, push, or contact a remote:

```bash
make sdk-release-check VERSION=v0.1.0    # would a tag on HEAD be a valid release?
make sdk-release-dev   VERSION=v0.1.0    # same gates against the working tree
make sdk-release-simulate                # publish to a throwaway remote, consume it externally
make sdk-release-mutation-check          # proves the release gates catch broken releases
```
