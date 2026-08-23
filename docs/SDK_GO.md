# Go SDK

The official Go client for LIGHTWEIGHT, at [`sdk/go`](../sdk/go).

Its own documentation — installation, the error model, pagination, secret
handling — lives in [`sdk/go/README.md`](../sdk/go/README.md). This page is the
part that concerns the SERVER: what the SDK is allowed to reach, how that is
enforced, and how it is released.

---

## Architecture

| Decision | Choice | Why |
| --- | --- | --- |
| Location | `sdk/go` | Outside `internal/`, in a directory that reads as "this is the external client". |
| Module | **separate `go.mod`** | An empty `go.sum` is mechanical proof of the zero-dependency claim, and a nested-module tag makes future extraction a rename rather than a migration. |
| Package | `lightweight` | Import as `lightweight ".../sdk/go"` — the path's last element is `go`, the package is not. |
| Workspace binding | **at client construction** | See below. |
| Dependencies | none | `net/http`, `encoding/json`, `context`, `net/url`, `time`, `iter`. |
| Go version | 1.23 | Chosen for the CONSUMERS who have to adopt it, not for the server's toolchain. `iter` is the floor. |

### Why the workspace is bound at construction

A Project Credential is bound to exactly one workspace, server-side, through its
project. The server compares that binding against the path on every request,
before any workspace is loaded.

So a `workspaceID` parameter on every method would add no safety whatsoever. What
it would add is a way to write a call that cannot succeed — and a reviewer would
have no way to tell a correct call from an incorrect one by reading it.

```go
client.Users.List(ctx, workspaceID)   // rejected: the extra argument buys nothing
client.Users.List(ctx)                // chosen: the boundary is the client
```

The consequence worth stating plainly: a client configured for workspace A cannot
express a request against workspace B. The mismatch is still reachable — a client
built from two environments' worth of configuration, which is how it happens in
practice — and the acceptance suite provokes exactly that and asserts
`workspace_mismatch`.

If projects ever span workspaces, this becomes a second constructor rather than a
redesign. That is not being pre-built: today's boundary is one workspace per
credential, and the API should say so.

---

## Parity matrix

Every route a Project Credential can reach, and the SDK method that serves it.
**24 of 24 supported.**

The authoritative copy is [`sdk/go/apicoverage.json`](../sdk/go/apicoverage.json).
This table is checked against it by
`TestSDKCoverage_DocumentedMatrixMatchesTheManifest`.

| SDK method | HTTP | Path | Scope | Status |
| --- | --- | --- | --- | --- |
| `Users.List` | `GET` | `/v1/workspaces/{workspace_id}/users` | `users:read` | supported |
| `Users.Create` | `POST` | `/v1/workspaces/{workspace_id}/users` | `users:write` | supported |
| `Users.Get` | `GET` | `/v1/workspaces/{workspace_id}/users/{user_id}` | `users:read` | supported |
| `Users.Update` | `PATCH` | `/v1/workspaces/{workspace_id}/users/{user_id}` | `users:write` | supported |
| `Users.Delete` | `DELETE` | `/v1/workspaces/{workspace_id}/users/{user_id}` | `users:write` | supported |
| `Users.SendPasswordReset` | `POST` | `/v1/workspaces/{workspace_id}/users/{user_id}/reset-password` | `users:write` | supported |
| `Roles.List` | `GET` | `/v1/workspaces/{workspace_id}/roles` | `roles:read` | supported |
| `Roles.Create` | `POST` | `/v1/workspaces/{workspace_id}/roles` | `roles:write` | supported |
| `Roles.Get` | `GET` | `/v1/workspaces/{workspace_id}/roles/{role_name}` | `roles:read` | supported |
| `Roles.Update` | `PATCH` | `/v1/workspaces/{workspace_id}/roles/{role_name}` | `roles:write` | supported |
| `Roles.Delete` | `DELETE` | `/v1/workspaces/{workspace_id}/roles/{role_name}` | `roles:write` | supported |
| `Roles.ListUsers` | `GET` | `/v1/workspaces/{workspace_id}/roles/{role_name}/users` | `roles:read` | supported |
| `Roles.ListForUser` | `GET` | `/v1/workspaces/{workspace_id}/users/{user_id}/roles` | `roles:read` | supported |
| `Roles.Grant` | `POST` | `/v1/workspaces/{workspace_id}/users/{user_id}/roles` | `roles:write` | supported |
| `Roles.Revoke` | `DELETE` | `/v1/workspaces/{workspace_id}/users/{user_id}/roles/{role_name}` | `roles:write` | supported |
| `Sessions.List` | `GET` | `/v1/workspaces/{workspace_id}/sessions` | `sessions:read` | supported |
| `Sessions.Revoke` | `DELETE` | `/v1/workspaces/{workspace_id}/sessions/{session_id}` | `sessions:revoke` | supported |
| `Sessions.ListForUser` | `GET` | `/v1/workspaces/{workspace_id}/users/{user_id}/sessions` | `sessions:read` | supported |
| `Sessions.RevokeAllForUser` | `DELETE` | `/v1/workspaces/{workspace_id}/users/{user_id}/sessions` | `sessions:revoke` | supported |
| `Invitations.List` | `GET` | `/v1/workspaces/{workspace_id}/invitations` | `invitations:read` | supported |
| `Invitations.Create` | `POST` | `/v1/workspaces/{workspace_id}/invitations` | `invitations:write` | supported |
| `Invitations.Resend` | `POST` | `/v1/workspaces/{workspace_id}/invitations/{invitation_id}/resend` | `invitations:write` | supported |
| `Invitations.Revoke` | `DELETE` | `/v1/workspaces/{workspace_id}/invitations/{invitation_id}` | `invitations:write` | supported |
| `Audit.List` | `GET` | `/v1/workspaces/{workspace_id}/audit` | `audit:read` | supported |

### Deliberately not exposed

Operator-only routes. A Project Credential cannot call any of them whatever
scopes it holds, so a method would only ever produce a `403` — and offering one
would send a developer looking for a scope that cannot exist.

* Workspaces, Connections, Projects, Project Credentials, `/v1/project-scopes`.
* `PUT /v1/workspaces/{workspace_id}/users/{user_id}/password`. Setting a
  password directly is a complete account-takeover primitive; `reset-password`
  covers the legitimate need without one. See
  [`internal/authz/registry.go`](../internal/authz/registry.go).

---

## Gates

The SDK is a separate module, so the root `go test ./...`, `go vet ./...` and
`golangci-lint run ./...` do **not** reach it — `./...` stops at module
boundaries. Only `gofmt -l .` does, because gofmt walks directories.

That is why `make ci` invokes `sdk-check` explicitly, and why these gates are
worth listing.

| Gate | Where | What it catches |
| --- | --- | --- |
| `TestSDK_ImportsNothingFromTheServer` | SDK | An import of the server module, or of any third-party module. |
| `TestSDK_HasNoProviderVocabulary` | SDK | An identifier or string literal naming the identity provider. Comments are exempt — explaining the guarantee is fine, modelling it is not. |
| `TestConfig_HasOnlyTheContractFields` | SDK | A `Config` field that would undo the three-variable claim. |
| `TestCoverage_*` | SDK | A manifest entry naming a method that does not exist, or one that documents the wrong scope. |
| `TestSDKCoverage_*` | `internal/authz` | A project-accessible route with no SDK decision; an entry for a route that is operator-only or does not exist; a supported route missing from the OpenAPI document. |
| `make sdk-deps-check` | Makefile | Any module dependency at all. |
| `make sdk-coverage-gate` | Makefile | SDK statement coverage below its own floor, reported separately from the server's. |
| `make sdk-mutation-check` | `scripts/` | A test that would not notice the behaviour it claims to pin. |

### The capability-completeness gate

The failure it exists to prevent is quiet:

```
someone adds a scoped route in a future slice
  → the registry classifies it, the OpenAPI document describes it
  → the SDK never hears about it
  → nothing fails, and the capability is invisible to every Go consumer
```

It is **not** a requirement that every new route get an SDK method. An entry with
`"status": "unsupported"` and a reason passes. What may not happen is silence.

The manifest is JSON rather than Go precisely because the two checkers live in
two modules that must not import each other.

### The mutation gate

`scripts/sdk-mutation-check.sh` copies the SDK to a scratch directory, breaks one
property at a time, and requires the suite to go red each time. The working tree
is never touched.

Sixteen mutations, all currently caught: the Authorization header dropped, the
workspace dropped from the path, path segments unescaped, typed errors collapsed
to strings, the request id dropped, non-2xx treated as success, unknown fields
rejected, the API key interpolated into an error, redaction removed from `Config`
and from `CreateUserRequest`, silent retries introduced, both body bounds removed,
a malformed success silently zero-valued, empty path arguments accepted, and the
coverage manifest quietly emptied.

---

## Acceptance

`scripts/sdk-acceptance.sh` stands up a real installation — PostgreSQL, a real
identity provider, a real `bin/api` — and runs
[`sdk/go/acceptance`](../sdk/go/acceptance) against it.

The split is the same one `scripts/m2m-harness.sh` uses: the script is the
**operator** and knows the provider exists; the Go suite is the **backend** and
receives three environment variables.

```bash
DB_URL=postgres://…  make sdk-acceptance
```

What it proves that unit tests cannot:

* the full consumer journey — list, create, read, search, patch, grant, revoke,
  read sessions, delete — against real provider state;
* the durable audit trail records the mutation, attributed to the **credential**
  rather than to a person, with a request id;
* two workspaces backed by two provider tenants stay isolated in both directions,
  including by direct id lookup;
* a client built from mismatched configuration is refused with
  `workspace_mismatch`;
* a `users:read`-only credential is refused with `insufficient_scope` across five
  endpoint families;
* a credential revoked **while the client is alive** stops working on the next
  call, with no restart — the revocation is performed by a watcher on the other
  side of a signal file, so the SDK process never holds an operator token;
* the rate limit is surfaced as `rate_limit_exceeded` with `Retry-After`, and is
  not silently retried;
* no credential, connection secret or temporary password appears in the server's
  process log.

The strongest of these is
`TestAcceptance_TheProgramNeedsNothingButTheThreeVariables`: it POISONS every
provider and database variable and re-runs the happy path. Absence is not
observable by running the happy path in an environment where those values happen
to be correct.

---

## Release

**`v0.1.0` is published.** The tag `sdk/go/v0.1.0` is on `origin`, the module
resolves through `proxy.golang.org`, `sum.golang.org` holds its hash and
pkg.go.dev renders it. The mechanics below are what published it, and are kept
because they are what the next release will follow.

### The tag and the version are different strings

This is the single fact the whole release design is arranged around, and getting
it wrong is silent rather than loud.

| | value | who types it |
|---|---|---|
| git tag | `sdk/go/v0.1.0` | the maintainer, once, at release |
| module version | `v0.1.0` | every consumer, in `go get` |

Go requires a nested module's tag to carry its directory as a prefix, and then
strips that prefix to obtain the version. A consumer therefore writes:

```bash
go get github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go@v0.1.0
```

Both halves are verified, not assumed. Resolving the module from a temporary Git
remote carrying one tag at a time gives:

| tag present | `go get …/sdk/go@v0.1.0` |
|---|---|
| `sdk/go/v0.1.0` | **resolves** to `v0.1.0` |
| `v0.1.0` | fails: *module `…/lightweight-saas-backend@v0.1.0` found, but does not contain package `…/sdk/go`* |
| `sdk/v0.1.0` | fails: *unknown revision `sdk/go/v0.1.0`* |
| `go/v0.1.0` | fails: *unknown revision `sdk/go/v0.1.0`* |
| `sdk/go/0.1.0` | fails: *unknown revision `sdk/go/v0.1.0`* |

The error message names the tag Go looked for, which is the clearest available
statement of the rule. Note the second row: the repository's existing `v0.3.1`
server tags are not merely unhelpful, they resolve to the *root* module and
report that it contains no such package.

`go get …@sdk/go/v0.1.0` — the form this document used to publish — does resolve,
but only because Go also accepts a bare revision name as a query. It is not the
canonical form, it drags the parent module into resolution, and it is not what
the module proxy or pkg.go.dev will ever show anyone. The release gate now
rejects it in the docs.

### Three versions that are not the same thing

```
LIGHTWEIGHT server release   v0.3.1, …   the deployable
HTTP API version             /v1          the wire contract
Go SDK release               v0.x.y       this module
```

They move independently and are deliberately not in lockstep. The API is `/v1`
and changes rarely; the SDK may release many times against it. A future `/v2`
would not force an SDK `v2.0.0` — one SDK release could speak both. An SDK
`v1.0.0` would say the *Go surface* has settled, and nothing about the server.

Compatibility is expressed in one line, because a matrix would be inventing
precision nobody has yet: **SDK `v0.x` targets the LIGHTWEIGHT HTTP API `/v1`.**
The SDK does not interrogate or pin a server version; the HTTP contract is the
boundary, and premature version negotiation would only add a way to fail.

### SemVer policy, pre-v1

The SDK starts at `v0`, which is a statement rather than modesty.

| change | release |
|---|---|
| bug fix, doc fix, internal refactor | patch — `v0.1.0` → `v0.1.1` |
| new method, new field, new error code | minor — `v0.1.1` → `v0.2.0` |
| removal, rename, signature or behaviour change | minor — `v0.1.1` → `v0.2.0`, and it is called out in the release notes |

While the major version is zero, a breaking change is *permitted* by SemVer and
therefore cannot be prevented — but it is not permitted to be **silent**. Every
one of them shows up as a `<` line in [`sdk/go/api.txt`](../sdk/go/api.txt) and
has to be re-committed by hand before the gate goes green.

The SDK reaches `v1.0.0` when its exported surface has survived more than one
real integration without needing a breaking change. From `v2` onward Go's import
compatibility rule applies: the module path itself must end in `/v2`, every
consumer changes their import line, and `check_major_suffix` in
`scripts/lib/sdk-release.sh` fails a `v2.0.0` tag on an unsuffixed path — which
is worth having, because Go does not error on that mistake, it just silently
never resolves.

### The release gate

Two layers, because they answer different questions.

**Full branch CI** (`.github/workflows/ci.yml`) is unchanged and remains the
requirement before tagging: integration, e2e, browser, coverage floors. It takes
tens of minutes and proves the repository works.

**SDK release gate** (`.github/workflows/sdk-release.yml`, on `sdk/go/v*` tags)
proves the *module* is releasable, in a couple of minutes: tag format, module
path agreement, tidiness, zero dependencies, the API snapshot, vet, tests, race,
coverage, a real Go 1.23 toolchain, external-consumer compilation, and the
install command in the docs.

A narrow gate must not be able to bless what the wide one never saw, so the tag
job additionally requires the tagged commit to be **an ancestor of `origin/main`**.
That is not proof CI was green; it is proof the commit went through the branch
where green is enforced, which is exactly the step a tag can otherwise skip. It
is a trust model, not a release-management platform, and it is one `git
merge-base --is-ancestor` call.

The workflow holds `permissions: contents: read`. It validates and refuses; it
does not publish. Should a GitHub Release step ever be added it would need
`contents: write`, and that is the moment to grant it and not before.

### Dry run

The question a maintainer actually has is *"if I tagged this, would it be
valid?"*, and it is answerable without creating anything:

```bash
make sdk-release-check VERSION=v0.1.0    # would a tag on HEAD be a valid release?
make sdk-release-dev   VERSION=v0.1.0    # same content gates, against the working tree
```

The two modes exist because a tag captures a **commit**, never a working tree.
`sdk-release-dev` checks the files on disk and says so; `sdk-release-check`
checks what a tag would actually contain and enforces the preconditions. Blurring
them would let the tooling report "ready to release" about code that is not in
any commit. When that happens the check says so rather than blessing it:

```
Release content
  x sdk/go/go.mod does NOT exist in commit e3c4e22 via HEAD
      This tag would publish nothing.
```

That output is an example of the failure mode, not the current state: the
module has been committed since `dd2bc5f` and `sdk/go/v0.1.0` shipped from it.

Neither command tags, pushes, or contacts a remote. Nothing in `scripts/` does.

### The first release

**Shipped as `v0.1.0` on 2026-08-15**, from the tag `sdk/go/v0.1.0` pointing at
`303ff18`. The reasoning that chose that number, and the sequence that published
it, are recorded below as written; the sequence is still the procedure for the
next release, with the version substituted.

**Why `v0.1.0`.** The repository's existing tags (`v0.1.0-auth-foundation`,
`v0.2.0`, `v0.3.0`, `v0.3.1`) are all server releases in the root module's
namespace and are unreachable from `sdk/go/`, so the SDK's history starts empty
and `v0.1.0` collides with nothing. `v0.1.0` rather than `v1.0.0` because no
external integration has yet depended on this surface, and `v0` is the honest way
to say the API may still move.

The exact sequence, run by a human who has decided to publish:

```bash
# 1. the release content is committed and merged to main
git checkout main && git pull

# 2. the full branch CI is green for that commit  (check GitHub Actions)

# 3. it would be a valid release
make sdk-release-check VERSION=v0.1.0

# 4. tag — the PREFIXED form, and only this form
git tag -a sdk/go/v0.1.0 -m "Go SDK v0.1.0"

# 5. publish
git push origin sdk/go/v0.1.0

# 6. the tag workflow runs and must pass

# 7. smoke-test the real proxy, from an empty directory outside this repository
./scripts/first-publish-smoke.sh v0.1.0
```

Step 7 is the first moment `proxy.golang.org` is involved at all, and it cannot
be run earlier — see below.

### What was proven before the tag, and what the tag proved

The distinction mattered because it was easy to overclaim, and it is kept
because it is the shape of the evidence for any future release.

**Proven, offline, with no GitHub involvement.** A snapshot of the working tree
was committed into a throwaway Git repository, tagged, published through a local
bare remote, and consumed by a module outside this repository's tree. That
established the tag-format table above; that `go mod tidy` and `go build` succeed
with **no `replace` directive**; that the consumer's `go.sum` contains the SDK and
nothing else; that Go includes the repository-root `LICENSE` in the nested
module's zip automatically; and that `lightweight.Version()` reports `v0.1.0` from
inside a released dependency. The temporary repository was deleted afterwards.
Nothing was pushed.

**Not provable before a real tag existed.** `proxy.golang.org` cannot serve a
version nobody has published, and `sum.golang.org` cannot have recorded a hash
for it. Local resolution used `GOPRIVATE`/`GONOSUMDB` and direct VCS access
precisely to bypass both, so it was evidence about *Go module semantics* and not
about the public infrastructure. Three things stayed open until the tag was
pushed: public proxy resolution, checksum-database entry, and pkg.go.dev
rendering.

**Closed by the tag.** `scripts/first-publish-smoke.sh` tests exactly those three
against the real proxy from an empty directory outside this repository, and
refuses to run against a version that does not exist. Against `v0.1.0`:

```
+ tag 'sdk/go/v0.1.0' exists on origin
+ go get github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go@v0.1.0
+ sum.golang.org recorded a hash for v0.1.0
+ the published module still pulls in nothing else
+ lightweight.Version() reports v0.1.0 (User-Agent: lightweight-go/v0.1.0)
+ pkg.go.dev is serving .../sdk/go@v0.1.0
```

Re-runnable at any time; it contacts the public proxy and creates nothing.

### A bad release

Published module versions are **immutable**, and the fix for a broken one is a
new version:

```
bad v0.1.0  →  release v0.1.1
```

Deleting and re-pushing a tag is not a rollback. The proxy and the checksum
database may already hold the old content, and a consumer whose `go.sum` records
that hash gets a security error rather than an update — a worse failure than the
bug being fixed. The only case where re-tagging is safe is a tag that was purely
local and never pushed anywhere.

Go's `retract` directive can mark a published version as withdrawn, so `go get`
stops selecting it and `go list -m -u` reports it. It is added to the *SDK's own*
`go.mod` in a later release. None is present now and none should be added
speculatively; the directive exists for a version that shipped and should not
have, and `v0.1.0` is not that version.

---

## Related

* [`sdk/go/README.md`](../sdk/go/README.md) — the developer-facing guide.
* [PROJECTS.md](PROJECTS.md) — Project Credentials, scopes, rate limits.
* [WORKSPACE_IDENTITY_API.md](WORKSPACE_IDENTITY_API.md) — the HTTP surface the SDK wraps.
* [AUDIT.md](AUDIT.md) — the durable trail `Audit.List` reads.
