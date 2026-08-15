# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
While the project is pre-1.0, minor version bumps may introduce breaking changes;
breaking changes are always called out under a **Breaking** subsection.

## [Unreleased]

Nothing yet.

---

## [0.4.0] — 2026-08-15

**LIGHTWEIGHT stops being an IAM foundation you build on and becomes an
identity control plane you install.**

Until now this served one Keycloak realm, and a backend that wanted to use it
was a fork of it. It now serves many: an operator attaches each workspace to
an identity provider through the console, issues a scoped credential to a
backend, and that backend talks to LIGHTWEIGHT knowing three environment
variables and nothing about the provider behind them.

If you are upgrading from v0.3.1, the short version:

- **Multi-workspace.** One installation, many tenants, each bound to its own
  Keycloak realm and resolved per request. Two workspaces pointed at two realms
  serve two disjoint sets of users from one process.
- **Connections.** A workspace's provider binding, with a verify step that says
  precisely what is wrong (unreachable / wrong realm / bad client / insufficient
  privileges), an activation lifecycle, and its client secret sealed at rest
  under a rotatable AES-GCM keyring.
- **Projects and credentials.** `lw_sk_` keys, hashed and never recoverable,
  carrying an explicit set of 9 scopes and bound to exactly one workspace.
- **A Go SDK**, zero-dependency, released independently as a nested module.
- **A durable audit trail**, written in the same transaction as the change it
  records.
- **An installation that works.** `./scripts/init.sh` and `docker compose up -d`
  from a clean machine, with `make product-acceptance` proving that path stays
  working.

Nothing was removed. The v0.3.1 `/admin/*` identity surface still exists and
still behaves the same way; it is now optional, and unset by default for a
self-hosted install. See **Changed** for the defaults that moved.

### Added — installation and first run

- **`./scripts/init.sh`** — the supported way to configure an installation.
  POSIX shell, no Go toolchain, no prompts, safe to re-run: it copies
  `.env.example`, generates the secrets keyring and a database password, writes
  the file `0600`, and refuses to touch an existing `.env` rather than mint a
  second keyring that would orphan every credential already sealed.

  `--keycloak-url`, `--realm` and `--console-client-id` produce a self-hosted
  configuration directly, which is what an unattended provisioning run needs.

- **`make product-acceptance`** — installs LIGHTWEIGHT from `.env.example` using
  the commands the README gives, then drives the whole journey: two workspaces,
  two Keycloak realms, two external Go consumers, a credential each, and a
  check at the provider that neither tenant's users reached the other's realm.
  It exercises only public surfaces — Docker, HTTP, the SDK — so it cannot pass
  by reaching around the product.

- **Configurable host ports** (`API_HOST_PORT`, `POSTGRES_HOST_PORT`), and
  `KC_HOST_PORT` now actually publishes the port it names.

- **`KEYCLOAK_SETUP.md` §0** — what to create in Keycloak, field by field, for
  the installation realm and for a workspace's realm. This did not exist, and
  it is the one thing an operator cannot derive from the product.

### Fixed — the installation path itself

- **`docker-compose.yml` ignored the operator's Keycloak configuration.**
  `KEYCLOAK_URL`, `KEYCLOAK_JWKS_URL` and `KEYCLOAK_ADMIN_BASE_URL` were written
  into the file, so editing `.env` did nothing and the API died fetching JWKS
  from `keycloak:8080` — a host that only exists with the evaluation profile
  running, and that the operator had never typed. **The documented self-hosting
  path could not work at all.** A gate now fails if compose passes a variable it
  does not interpolate.

- **`make regen` deleted configuration.** It emitted a hand-maintained list of
  25 variables while the contract had grown to 39, so regenerating dropped
  `SECRETS_KEYRING`, `ADMIN_CONSOLE_CLIENT_ID`, `CORS_ALLOWED_ORIGINS` and
  eleven others from a working `.env` — silently, leaving an installation that
  booted with its connection API and console login simply gone. The generator
  now reads the contract, and preserves every value an operator set.

- **A successful first boot printed eight to ten `FATAL` lines** while the
  bundled Keycloak imported its realm. The issuer is now waited for on a
  bounded retry, the same policy the database already had.

- **A self-hosted install rejected its own operators.** `--console-client-id`
  set only the console's client id, leaving `KEYCLOAK_ALLOWED_CLIENT_IDS`
  matching evaluation clients, so every valid token failed the `azp` check with
  a 401 explained only in the server log. It also left the evaluation
  identity-management client configured, and because that check is fail-closed,
  every `/v1` request answered `503` in a realm where that client does not
  exist.

### Added

- **The Go SDK is releasable through standard Go module tooling**
  ([TD-035](docs/TECH_DEBT.md#td-035) closed). Nothing is published — this is
  release-*ready*, not released — but the mechanics are proven rather than
  assumed, and the remaining step is an operator pushing a tag.

  - **The tag format, established by experiment.** A nested module publishes
    from a tag carrying its directory as a prefix. Resolving the SDK from a
    throwaway Git remote, one tag shape at a time, gives exactly one that works:

    | tag | `go get …/sdk/go@v0.1.0` |
    |---|---|
    | `sdk/go/v0.1.0` | resolves |
    | `v0.1.0` | *module found, but does not contain package …/sdk/go* |
    | `sdk/v0.1.0`, `go/v0.1.0`, `sdk/go/0.1.0` | *unknown revision sdk/go/v0.1.0* |

  - **Breaking: the documented install command was wrong.** Both READMEs said
    `go get …/sdk/go@sdk/go/v0.1.0`, which asks for the **tag** where Go expects
    the **version**. It happens to resolve — Go also accepts a bare revision name
    as a query — but it is not canonical, it pulls the parent module into
    resolution, and it is not what the proxy or pkg.go.dev show. The correct form
    is `go get …/sdk/go@v0.1.0`. No consumer can have been affected, because
    nothing was ever published.

  - **One authoritative source.** `scripts/lib/sdk-release.sh` holds a single
    literal — the directory — and derives the module path, tag prefix and install
    command from the two `go.mod` files, failing if they disagree. Renaming the
    module is now a loud event instead of a silently unresolvable release.

  - **A release gate that validates the commit, not the desk.**
    `make sdk-release-check VERSION=vX.Y.Z` answers "would a tag on HEAD be a
    valid release?" — tag prefix, SemVer, the `/vN` major rule, whether the
    tagged **commit** actually contains `sdk/go/go.mod`, tidiness, zero
    dependencies, exported-API drift, vet, tests, race, coverage, a real Go 1.23
    toolchain, and the install command in the docs. Nothing in `scripts/` tags,
    pushes, or contacts a remote.

  - **`.github/workflows/sdk-release.yml`**, on `sdk/go/v*` tags, with
    `permissions: contents: read`. It also requires the tagged commit to be an
    ancestor of `main`, so a narrow release gate cannot bless code the full
    branch CI never ran.

  - **`sdk/go/api.txt`** — every exported declaration, including the *values* of
    wire-visible constants, since a consumer comparing against
    `CodeInsufficientScope` depends on the string and not the identifier. Pre-v1
    a breaking change is allowed; making one unnoticed is not.

  - **`make sdk-consumer-check`** compiles a module from outside the repository
    against the SDK, and **`make sdk-release-simulate`** proves tag resolution
    with no `replace` directive. Two different claims, two scripts, neither
    overstating what it ran.

  - **Executable examples** (`sdk/go/example_test.go`) so pkg.go.dev renders
    working code. None makes a network call.

  - **`sdk/go/CHANGELOG.md`**, separate from this file because the SDK versions
    independently and because the module zip carries it to pkg.go.dev.

  - **19 release mutations, all caught** (`make sdk-release-mutation-check`),
    each verified against a pristine copy first.

### Fixed

- **The zero-dependency gate could report a dependency as absent.**
  `go list -m all` exits non-zero and prints nothing when a `require` cannot be
  downloaded, so a `sdk/go/go.mod` declaring an unresolvable dependency read as
  "no dependencies" — precisely the offline case, which is the CI cache-miss
  case. `make sdk-deps-check` now also reads the declaration via
  `go mod edit -json`. Found by a mutation that was expected to be caught and
  was not.

- **Control-plane mutations are now atomic with their audit record**
  ([TD-033](docs/TECH_DEBT.md#td-033) closed). The guarantee, stated exactly:

  > LIGHTWEIGHT does not commit a PostgreSQL control-plane mutation without its
  > required durable audit record.

  Not "audit is reliable", and not "everything is atomic". The guarantee is
  narrow, mechanically tested, and true. Full model in
  [docs/AUDIT.md §6](docs/AUDIT.md#6-when-the-audit-write-fails).

  - **One transaction, owned by the service.** All fourteen control-plane
    mutations — workspace, connection, project, credential — write their domain
    rows and their audit row inside one `BEGIN … COMMIT`. If the audit insert
    fails, PostgreSQL rolls the mutation back and the caller gets a 500. There is
    no window between the two writes because there are not two writes.

  - **The transaction handle is `*gorm.DB`, deliberately.** The textbook move is
    a `DBTX` interface over `ExecContext`/`QueryContext`, and it would have been
    wrong here: every repository is written against GORM's fluent API and none of
    it goes through `ExecContext`, so the abstraction would have meant rewriting
    the persistence layer in raw SQL to avoid naming a library already in use.
    `*gorm.DB` IS the "either the pool or a transaction" type the pattern is
    reaching for. [`internal/database/tx.go`](internal/database/tx.go) names it
    `Tx` and says why.

  - **One SQL implementation per statement.** `Repository.WithTx` returns a copy
    bound to the caller's transaction rather than a parallel set of `…Tx`
    methods. The repositories' own internal transactions become savepoints and
    roll back with the outer one, so no method body changed.

  - **`Recorder.RecordTx` returns the error that `Record` absorbs.** That
    difference is the whole slice in one signature: an audit row a service cannot
    write is a mutation it must not commit. `Record`'s
    succeed-the-response-and-log policy stays exactly as it was for provider
    mutations, where it is correct.

  - **No recursion on failure.** A mutation rolled back because its audit row
    would not write does NOT then record a failure event — that would be a second
    write to the store that just failed. It is logged and counted instead, and
    `logging.RecordControlPlaneOutcome` is where that rule lives so fourteen call
    sites cannot each get it wrong.

  - **17 acceptance cases against real PostgreSQL**
    ([`atomicity_integration_test.go`](internal/auditlog/atomicity_integration_test.go)).
    Every rollback case asserts three things in order: the domain row was
    **visible inside the transaction** when the audit write was refused, the
    write then failed, and neither row exists afterwards. Without the middle
    observation, "the row is absent" would be equally consistent with a write
    that never ran.

  - **Connection activation is the case worth naming.** It retires the incumbent
    AND promotes the successor, so a failed audit write must undo two row
    updates; the test asserts the workspace still has exactly one active
    connection and that it is the original one.

  - **Credential revocation has a consequence, stated plainly.** If the audit
    write fails, the revocation is rolled back — so a 500 means *the credential
    is still live* and the operator must retry. That is deliberately the honest
    direction: committing the revocation behind an error would tell an operator
    their kill switch failed while it had in fact fired.

  - **`make audit-mutation-check`** breaks the guarantee nine ways in a scratch
    copy — including moving the audit write back after the commit, which is
    exactly the code this closed — and requires the suite to go red each time.
    All nine are caught, against a baseline verified green first.

  - **Measured faster, not slower.** Median control-plane mutation latency went
    from ~4.5 ms to ~2.5 ms against a local PostgreSQL, consistently across every
    sample pair: one transaction is one round trip fewer than a transaction plus
    a separate insert. The correctness work removed a write, it did not add one.

### Changed

- **`internal/auditlog`'s coverage registry now carries durability**, not just
  which event a route emits. Every mutating `/v1` route declares whether this
  database owns its state (`atomic`) or a Keycloak realm does (`audited`), and
  `TestCoverage_DurabilityMatchesWhereTheStateLives` fails the build in BOTH
  directions — including the dangerous one, a provider mutation claiming an
  atomicity no transaction can deliver.

- **Control-plane services require their transaction runner and audit writer.**
  `NewService` returns nil without them, which omits the routes. A service that
  fell back to a non-transactional path when they were absent would make
  atomicity conditional on wiring nobody checks, and the failure mode would be a
  production deployment that is silently non-atomic while every test passes.
  `workspace.NewHandler` and `connection.NewHandler` now guard nil services for
  the same reason.

- **A mechanically complete negative authorization matrix, in three layers.**
  Positive-path coverage proved an authorized caller can do what it should.
  Nothing proved the opposite question — that every other caller is prevented
  from doing what it must not — and that gap was
  [KI-018](docs/KNOWN_ISSUES.md#ki-018), now closed. The whole model and its
  evidence live in
  [docs/security/AUTHORIZATION_MATRIX.md](docs/security/AUTHORIZATION_MATRIX.md).

  - **The matrix derives from the registry, not from a list.**
    [`internal/authz/matrix.go`](internal/authz/matrix.go) computes the
    project-reachable route set, each route's capability family and its
    read/write character from the same table `Authorize` consults per request.
    A hand-kept copy would drift, and would drift silently — which is precisely
    the failure this exists to catch: a route added in a future slice appears in
    every sweep on the next run whether or not anyone remembered it.

  - **Layer A — every route against every scope**
    ([`internal/authz/matrix_test.go`](internal/authz/matrix_test.go)).
    Required scope present → allowed. Absent → refused, naming the scope in
    `WWW-Authenticate`. Every OTHER scope in the vocabulary → refused. Read
    scope on a write route → refused. Unknown, wildcard-shaped or mis-cased
    scope → refused. Duplicated scopes → identical decision. Foreign workspace
    → refused as a mismatch, in preference to any scope complaint, so a
    credential probing another tenant never learns which scopes would have
    worked there. Milliseconds, and it is the layer that would notice a
    twenty-fifth route.

  - **Layer B — the refusal lands before anything is touched**
    ([`internal/server/authz_negative_test.go`](internal/server/authz_negative_test.go)).
    The real router, the real `AuthenticatePrincipal`, the real
    `project.Authenticator`, the real resolver, and two instrumented seams: the
    workspace lookup (the resolver's first act) and the provider call itself,
    tagged with the realm it went through. A rejected request must leave both at
    zero, and the weaker-looking counter is the stronger evidence — it means the
    refusal happened before the workspace row was read, and therefore before a
    connection was loaded, a sealed credential opened, or any provider traffic
    sent. Every negative case is paired with a positive control on the same
    route with the same body, where only the credential differs.

  - **A completeness gate, which is the point.**
    `TestNegative_TheRequestTableCoversEveryRoute` compares the concrete request
    table against the registry in both directions. A route added to the registry
    fails the package until someone writes the request that exercises it. The
    sequence this breaks is: new route added → developer forgets the negative
    test → CI stays green → the route ships unproven.

  - **Layer C — the real stack**
    ([`scripts/negative-authz-e2e.sh`](scripts/negative-authz-e2e.sh),
    `cmd/lwprobe -mode negative`). Real PostgreSQL, real Keycloak 26, seven
    realms, seven workspaces, three projects, nine credentials, one long-lived
    process. It does NOT re-prove the scope matrix — that would buy twenty
    minutes of CI for weaker per-case evidence. It proves what a mock cannot: a
    rejected mutation left the REALM unchanged, verified by reading Keycloak
    directly; a resource id from one realm cannot act through another, on eight
    routes; an archived workspace and a retired connection cut off a credential
    that worked a second earlier, with the provider cache already warm and no
    restart; caller-forbidden stays distinguishable from provider-forbidden when
    a real Keycloak does the refusing; revocation lands on all six concurrent
    callers. 78 checks in about 20 seconds, inside the CI job that already runs
    Postgres and Keycloak.

  - **The last-admin guard, finally triggered.** KI-018 asked for "a deliberate
    single-admin realm fixture so the last-admin path can actually be
    exercised", because adversarial probing had never managed to trigger it —
    the test realm happened to have two admins. Layer C builds that realm and
    exercises all four self-protection guards with an operator token, then reads
    the realm directly to confirm the target survived.

  - **`make authz-mutation-check`**
    ([`scripts/authz-mutation-check.sh`](scripts/authz-mutation-check.sh))
    breaks the boundary eight ways in a scratch copy and requires the matrix to
    go red each time. All eight are caught. Two of the eight were WRONG on their
    first writing — they changed no behaviour and were reported as caught — and
    the script now refuses to run until the unmutated copy is green, which is
    what turned two false positives into two real ones.

  - **SDK negative acceptance.** Three states a consumer meets in production and
    which no earlier fixture produced: an archived workspace (reported as a
    workspace state, not a bad credential), a provider refusal (not blamed on
    the caller's key), and a resource id from another realm (indistinguishable
    from one that exists nowhere).

- **The official Go SDK (`sdk/go`).** LIGHTWEIGHT's abstraction was, until now,
  an HTTP API a developer had to hand-roll a client for — which meant every
  consumer reinvented error decoding, pagination and secret hygiene, and got a
  slightly different answer each time. [docs/SDK_GO.md](docs/SDK_GO.md) ·
  [sdk/go/README.md](sdk/go/README.md).

  - **Three environment variables, and nowhere to put a fourth.**
    `LIGHTWEIGHT_URL`, `LIGHTWEIGHT_WORKSPACE_ID`, `LIGHTWEIGHT_API_KEY`.
    `NewClientFromEnv()` reads exactly those. A consuming backend never learns
    which identity provider is behind the API, which tenant of it the workspace
    routes to, or what credential opens it — so an operator can repoint the
    workspace and the backend keeps working, unchanged and unrestarted.

  - **A separate Go module with no dependencies.** `sdk/go/go.mod` has no
    `require` block and no `go.sum`. That is not tidiness: a dependency here
    becomes a dependency of every backend that imports the SDK, along with its
    transitive graph and its advisories. `make sdk-deps-check` fails the build if
    one appears, and `TestSDK_ImportsNothingFromTheServer` fails it if the SDK
    reaches back into `internal/`.

  - **The workspace is bound at client construction**, not passed per call. A
    Project Credential is already bound to one workspace server-side, so a
    `workspaceID` argument would add no safety and would add a way to write a
    call that cannot succeed. The reasoning, and what is given up, is in
    [docs/SDK_GO.md](docs/SDK_GO.md#why-the-workspace-is-bound-at-construction).

  - **Typed errors, three kinds, kept apart.** `*APIError` (the server refused —
    stable `Code`, `StatusCode`, `Field`, `RequestID`), `*RequestError` (no
    answer was obtained; unwraps, so `errors.Is(err, context.Canceled)` keeps
    working) and `*ProtocolError` (an answer arrived that could not be read). A
    caller that cannot tell "the user does not exist" from "the network is down"
    deletes the wrong things. Error codes are open strings, never a closed enum,
    so a server newer than the client still decodes.

  - **Five services covering all 24 project-accessible routes**: `Users`,
    `Roles`, `Sessions`, `Invitations`, `Audit`. Operator-only routes are
    deliberately absent, including `PUT .../users/{id}/password` — a Project
    Credential cannot call it whatever scopes it holds, so a method could only
    ever produce a 403. Every method documents the scope it needs.

  - **The three pagination models are exposed as three shapes**, because they
    are three models: offset for users, complete slices for roles/sessions/
    invitations, cursor for audit. `Audit.All` is a range-over-func convenience
    over the page API rather than a replacement for it.

  - **No retries, ever, including on GETs.** This API has no idempotency keys
    ([TD-036](docs/TECH_DEBT.md#td-036)), so a retried mutation can create a
    second user. `Retry-After` is surfaced on `*APIError`; acting on it is the
    caller's decision. No logger, no metrics, no hooks: supply
    `Config.HTTPClient` with your own `RoundTripper`.

  - **The API key is sent as a bearer token and nowhere else.** `Config`,
    `Client` and `CreateUserRequest` implement `String` *and* `GoString`, so
    `%v`, `%+v`, `%s` and `%#v` all redact. Without that, Go's default struct
    formatting prints the credential, and the places a config gets printed are
    exactly the places a secret must not reach.

- **A capability-completeness gate for the SDK.** `sdk/go/apicoverage.json`
  records a decision per project-accessible route, checked from both sides:
  `internal/authz/sdk_coverage_test.go` compares it against the authorization
  registry and the OpenAPI document, and `sdk/go/coverage_test.go` checks each
  entry names a real method documenting the right scope. It does **not** require
  every new route to get an SDK method — `"unsupported"` with a reason passes.
  What it makes impossible is a route being added and nobody deciding.

- **`scripts/sdk-acceptance.sh`** — the SDK against a real API, PostgreSQL and
  identity provider. Same operator/backend split as `scripts/m2m-harness.sh`: the
  script knows the provider exists, the Go suite receives three variables. It
  proves the full consumer journey, two-tenant isolation in both directions,
  `workspace_mismatch` from mismatched configuration, `insufficient_scope` across
  five endpoint families, rate limiting with `Retry-After`, and — the one that
  needed a design rather than an assertion — that a credential revoked **while
  the client is alive** fails on the next call, with the revocation performed by
  a watcher on the other side of a signal file so the SDK process never holds an
  operator token.

- **`scripts/sdk-mutation-check.sh`** — breaks the SDK sixteen ways in a scratch
  copy and requires the suite to go red each time. A green suite proves the code
  passes the tests; it does not prove the tests would notice if the code were
  wrong. The first run found a real hole: the error-body bound was asserted with
  a payload that failed to parse whether or not it had been truncated, so the
  bound could be deleted with no test failing. The assertion was rewritten
  around an observable consequence.

- **`make` targets** `sdk-test`, `sdk-test-race`, `sdk-vet`, `sdk-deps-check`,
  `sdk-coverage`, `sdk-coverage-gate`, `sdk-check`, `sdk-mutation-check`,
  `sdk-acceptance`. `sdk-check` is wired into `make ci` because the root
  `go test ./...` and `golangci-lint run ./...` stop at the module boundary and
  would otherwise never see the SDK at all.

- **Master-key rotation for credentials at rest**
  ([TD-019](docs/TECH_DEBT.md#td-019)). Provider client secrets could be
  encrypted but not re-keyed: the sealer held exactly one master key, stamped a
  hard-coded version on everything, and a row sealed under any other version
  simply failed to open. Retiring a key meant re-entering every stored
  credential by hand, which made the standard response to a suspected key
  compromise — rotate now, re-wrap later — unavailable.
  [docs/SECRET_KEY_ROTATION.md](docs/SECRET_KEY_ROTATION.md).

  - **A bounded keyring.** `SECRETS_KEYRING=1:<key>,2:<key>` lists every version
    the process can DECRYPT with; `SECRETS_KEY_CURRENT` names the single version
    that ENCRYPTS. Versions are explicit, start at 1, may not repeat, and a
    current version outside the ring refuses the boot.
  - **Every row is opened with the key its own version names, and no other.**
    There is deliberately no try-every-key fallback: it would turn a missing key
    from a diagnosable error into a silent one, give a forged ciphertext N
    authentication attempts instead of one, and destroy the only signal that
    says rotation has not finished. `TestKeyring_DoesNotTryOtherKeys` configures
    a key that *would* open the row under a different number, and requires the
    refusal.
  - **`secrets rotate`** re-seals every persisted credential under the current
    key — one row per transaction, under `SELECT … FOR UPDATE`. Idempotent
    (already-current rows are counted and skipped, never re-encrypted),
    resumable after an interruption or a failed row, and safe to run twice at
    once. The lock is the database's rather than the process's because the
    interleaving that would actually destroy data is a rotation racing a
    credential *replacement*, and a mutex is worth nothing to a second process.
  - **`secrets status`** reports how many credentials each key version still
    opens and which configured keys are safe to remove — the question that
    decides whether an old key can be destroyed. `rotate --dry-run` reports the
    same from metadata alone, and says out loud that it cannot detect wrong key
    material without decrypting.
  - **Exit codes are the interface**: `0` success including nothing to do, `1` a
    row failed or a persisted version is unconfigured, `2` bad invocation. Built
    to `bin/secrets` rather than run through `go run`, which collapses the three
    into two.
  - **A missing key degrades its own workspaces and nothing else.** Readiness
    stays a global-dependency check: failing it would take every other tenant
    out of the load balancer over one stranded connection. The condition is loud
    instead — an `ERROR` at boot and every five minutes naming the versions and
    counts, a `lightweight_secret_key_version_rows{version}` gauge, a
    `lightweight_secret_open_failures_total{reason}` counter, and a non-zero
    `secrets status`.
  - **Master-key rotation is not credential rotation**, and the provider cache
    now proves it. Rotation deliberately does not touch `updated_at`, so the
    cached provider — holding the same plaintext and a live service-account
    token — survives; a genuine `client_secret` change still evicts it, because
    that one holds a credential Keycloak has revoked.
  - **No schema change.** `secret_key_version` and `secret_alg` have been
    carried since migration 000003 for exactly this.
  - **Evidence.** `TestKeyRotation_ConnectionSurvivesTheWholeLifecycle` takes a
    connection from v1, through a mixed keyring, through rotation, to a process
    holding only v2, performing real Keycloak admin reads and writes at every
    stage. `make secrets-check` drives the compiled binary through the same
    lifecycle and scans everything it produced for the keys and secrets it used.

- **Browser end-to-end coverage of the operator journey**
  ([TD-031](docs/TECH_DEBT.md#td-031)). A real Chromium, driving the real admin
  console, against a real LIGHTWEIGHT process, a real PostgreSQL and a real
  Keycloak 26. No mocked API, no injected token, no fake realm.
  [docs/testing/BROWSER_E2E.md](docs/testing/BROWSER_E2E.md).

  This is the boundary nothing tested. `scripts/m2m-harness.sh` proves a machine
  can *use* the product; everything that machine consumes has to be created by
  an operator first, and a console that renders but cannot complete that
  sequence is a product nobody can install.

  - **A real Authorization Code + PKCE login.** The console has no sign-in
    button — boot() finds no token and redirects to Keycloak itself — so opening
    `/admin` *is* the login trigger. The test types the operator's credentials
    into Keycloak's own form and asserts the authorization request carried
    `code_challenge_method=S256`. Nothing writes a token into `sessionStorage`:
    a helper that did would make every other journey pass while login was
    broken, which is the exact failure mode TD-031 describes.
  - **The whole operator journey, by clicking.** Workspace → connection →
    verify → activate → project → credential → the one-time secret → the
    credential used from outside over plain HTTP → its mutation in the console's
    Audit view attributed to the project → revocation in the browser → the
    machine refused on its next request → that revocation attributed to the
    operator. 27 tests, five spec files, ~14 s of browser time.
  - **Both actor models proven in one workspace's trail**, and workspace-state
    and multi-realm isolation proven across two live realms.
  - **Unexpected browser errors fail the run.** Every view is `async`, so a
    rejection after the first `await` escapes `router.js`'s `try/catch` and
    leaves the page on "loading…" with the only evidence in the console. The
    allowlist is short and every entry carries the reason it is benign.
  - **Artifacts are proved secret-free, not assumed.** Traces, screenshots and
    video are off — a trace stores input values, and the most valuable
    screenshot is the one taken while the one-time credential modal is open.
    `scripts/scan-artifacts.sh` then searches everything published for the exact
    values the run used plus `lw_sk_`/JWT/Bearer shapes, and fails the build on
    a hit.
  - **CI job `browser-e2e`**, separate from `e2e` so "backend green, browser
    red" is a diagnosis rather than a bisect. Playwright is pinned by a
    committed lockfile; Chromium only.
  - **`make e2e-browser`** locally, plus `make e2e-m2m` for the machine
    boundary, which had no target before.
  - `scripts/lib/keycloak-fixture.sh` is now sourced by **both** harnesses, so
    there are two e2e suites and one definition of a fixture realm.

- **A durable, workspace-scoped audit trail** ([TD-008](docs/TECH_DEBT.md#td-008)).
  Events now live in PostgreSQL (migration `000006`) instead of a 500-entry ring
  a restart emptied. [docs/AUDIT.md](docs/AUDIT.md).

  - **`GET /v1/workspaces/{id}/audit`** — newest first, cursor-paginated,
    filterable by event, actor type, outcome and time range. Readable by an
    operator, or by a project credential holding the new **`audit:read`** scope,
    which is never implied by another and is unchecked by default in the
    console.
  - **Eleven control-plane mutations that emitted nothing now emit.** The
    durable trail's first finding was that every workspace and connection
    operation — including `connection.activated`, which silently redirects a
    whole workspace to a different Keycloak realm — had been shipping with no
    audit event for three slices. Nobody decided that.
  - **A completeness gate.** Every mutating `/v1` route is classified in
    `internal/auditlog/coverage.go` as `audited(<event>)` or explicitly
    not-audited-because, checked against the authorization registry. A future
    mutation cannot merge without someone writing down what it records.
  - **The actor model is enforced by the database.** Operator and project rows
    are disjoint by CHECK constraint, so "a project id never appears in
    `actor_subject`" is a guarantee about the data rather than a property of
    whichever code path wrote it.
  - **Nothing secret is stored.** The failure reason is mapped onto a closed
    vocabulary of `/v1` error codes — never `err.Error()`, which can carry a
    provider response body — and metadata is allowlisted per event type. There
    is no `user_agent` column at all: it is free text the caller controls, and
    one caller must not be able to write into another reader's view of history.
  - **90-day retention** (`AUDIT_RETENTION_DAYS`), swept at boot and daily.
    There is deliberately no value meaning "keep forever", and `0` is refused
    rather than defaulted.
  - **A console view** at Workspace → Audit: time, event, actor, resource,
    outcome, with three filters and a load-more button whose absence is the
    end-of-history signal.

  Audit is **not** a request log: reads, health checks and traffic that never
  passed authentication produce zero rows, which keeps the table bounded by real
  activity rather than by traffic.

  When an audit write fails after a mutation has already happened, the response
  still succeeds — failing it would invite a retry of a change that already
  landed — and the failure is logged loudly and counted as
  `lightweight_audit_persist_failures_total`. The residual non-atomicity is
  [TD-033](docs/TECH_DEBT.md#td-033).

  `GET /admin/audit-events` and its in-process ring are unchanged. The two
  surfaces answer different questions and neither is authority for the other's.

- **Projects and machine credentials** — an external backend can now
  authenticate to this API without an operator token, reach exactly one
  workspace, and perform only the operations it was explicitly granted.
  [docs/PROJECTS.md](docs/PROJECTS.md), migration `000005`.

  - **A Project belongs to exactly one workspace, permanently.** That binding is
    the authorization boundary, not a convenience: it is compared against the
    workspace in the request path before any workspace, connection, sealed
    credential or provider is touched. `PATCH` refuses a `workspace_id` rather
    than ignoring it. One leaked credential reaches one project, one workspace,
    one realm.
  - **Opaque credentials**, `lw_sk_<lookup>_<secret>`: 80-bit lookup stored in
    clear behind a unique index, 256-bit secret stored only as a SHA-256 digest.
    The plaintext is returned by one response and by nothing else — no endpoint
    can return it, because it is not stored. SHA-256 rather than a memory-hard
    hash is an analysis, not a shortcut: there is no search space to slow down
    for CSPRNG output, and Argon2 on a machine-to-machine authentication path
    would be a self-inflicted denial of service.
  - **One header, a prefix test, no fallback chain.** A `lw_sk_` token is never
    handed to the JWT parser and a JWT is never handed to the credential
    authenticator. A project never produces an `auth.Identity`, so every
    operator-shaped check downstream fails closed for a machine by construction.
  - **Eight scopes**, held by the credential rather than the project, so one
    backend can hold a read-only key and a read-write key at once. Enforced
    twice: Go constants and a `CHECK` constraint. Granting a role is
    `roles:write`, not `users:write` — what is sensitive is the privilege, not
    the user record.
  - **A central authorization registry.** Every mounted `/v1` route carries
    exactly one classification, operator-only or a required scope. An
    unclassified route is denied at runtime and **panics the boot**, so a route
    added in a future slice cannot reach a provider without an explicit security
    decision.
  - **The control plane is operator-only** without exception: workspaces,
    connections, projects and `PUT …/password`. A credential able to mint
    credentials would make revocation meaningless.
  - **Revocation is immediate** — no cache to invalidate — and archiving a
    project stops every one of its credentials in a single `UPDATE`, because
    authentication reads the project's status in the same row fetch.
  - Per-credential rate limiting (20 req/s, burst 40) keyed on the credential
    rather than the project, with the modern `/v1` 429 envelope.
  - Console: **Workspace → Projects → Project → Credentials**, with explicit
    scope selection (nothing pre-selected), inline warnings on the two scopes
    whose consequences their names do not convey, and a dedicated one-time
    secret modal.

- **Multi-workspace admin console** — the console's identity views moved from
  legacy `/admin/*` onto the public `/v1/workspaces/{id}/…` API, the same
  surface external consumers use. One LIGHTWEIGHT install can now administer
  several Keycloak realms from one browser session.
  [docs/WORKSPACE_CONSOLE.md](docs/WORKSPACE_CONSOLE.md).

  - **The workspace lives in the route** — `#/workspaces/ws_x/users` — so
    refresh, bookmarks, the back button, deep links and two realms in two tabs
    all work without a persistence rule. Installation-scoped pages carry no
    workspace segment, which makes "this page is not workspace-scoped"
    structural rather than a convention.
  - **Workspace switching is isolated three ways**: a generation counter, an
    abort signal per workspace, and switch listeners that tear down open
    dialogs. A response for workspace A can never render under workspace B, and
    an action composed in A is refused rather than retargeted if the operator
    has moved to B.
  - **Workspace and connection management** — list, create, rename, archive;
    and per workspace: create, edit draft, replace secret, verify, activate,
    retire. Stored secrets are never rendered; a blank secret on edit means
    "keep the existing one", which is the PATCH contract rather than a
    client-side invention.
  - **Intentional degraded states** for no workspaces, unknown workspace,
    archived workspace, no active connection, provider unavailable and
    read-only connection. None of them falls back to `/admin/*`, which would
    show an operator a realm they never chose.
  - `scripts/two-realm-demo.sh` stands up two realms and asserts 27 isolation
    properties through the same endpoints the console calls; `--keep` leaves it
    running for a manual walkthrough.

- **`cmd/lwprobe` — a consumer that is genuinely external.** It imports nothing
  from this module, and `TestLwprobe_ImportsNothingInternal` fails the build if
  that changes. Its client type has exactly three fields — base URL, workspace
  id, API key — and no room for a Keycloak URL, a realm, a client secret or a
  connection id, so the architectural claim that a backend needs none of those
  is expressed as a type rather than as prose.

  Two modes: `-mode contract` runs the flow and the full error matrix, checking
  status, `error.code`, `request_id` and headers on every documented failure;
  `-mode bench` measures representative HTTP latency and throughput per path.

  Everything inside `internal/` can build a principal, reach a provider or read
  the database. Each of those proves a component works; none proves the public
  contract works, because none is restricted to it.

- **`scripts/m2m-harness.sh`** stands up realms, workspaces, connections,
  projects and seven credentials the way an operator would, then hands over to
  `lwprobe`. It covers the effective rate limit, credential isolation,
  cross-address bucket sharing, immediate revocation, connection rotation,
  multi-realm isolation, console regression, and that no credential or client
  secret reaches a response or the process log.

- **`RATE_LIMIT_EDGE_RPS` and `RATE_LIMIT_CREDENTIAL_RPS`.** How much machine
  traffic is normal is a property of the installation, not of the software.
  Burst is derived as twice the rate rather than configured separately; 0,
  negative and unparseable all mean "the default", so a typo cannot silently
  switch the protection off.

- **Graceful shutdown** ([TD-013](docs/TECH_DEBT.md#td-013)). `SIGTERM`/`SIGINT`
  start an ordered drain: readiness reports 503, three seconds pass so a load
  balancer notices, the listener closes, in-flight requests finish within
  `SHUTDOWN_TIMEOUT_SECONDS` (default 20), the database pool closes, exit 0.
  A second signal skips the delay.

  Marking not-ready **before** closing the listener is the load-bearing part: a
  balancer polls, so closing first turns that window into refused connections.

  HTTP timeouts arrived with it — `ReadHeaderTimeout` 10s, `ReadTimeout` 30s,
  `WriteTimeout` 60s, `IdleTimeout` 120s, `MaxHeaderBytes` 64KB. `router.Run`
  set none, so a client that opened a connection and never sent a request held a
  goroutine indefinitely.

- **`GET /health/live` and `GET /health/ready`**, which answer different
  questions. Liveness does no I/O — the only remedy for a failed liveness probe
  is a restart, so nothing a restart cannot fix may fail it. Readiness checks the
  database and whether a drain has begun.

  Readiness deliberately does **not** consult workspace connections: one tenant's
  Keycloak going down must not take the instance out of rotation and every other
  tenant with it. That request answers `provider_unavailable` and everything else
  keeps working.

  `GET /health` is unchanged and still answers liveness, for existing monitors.

- **Fail-fast configuration validation.** The process reports **every** problem
  at once and refuses to start, rather than one per restart. Required values,
  URL schemes, number ranges, master-key length, CORS origin shape, and
  combinations that cannot be honoured. A message names the variable and never
  prints a secret.

  Present-but-unparseable is now an error rather than a silent fallback:
  `RATE_LIMIT_CREDENTIAL_RPS=2O` used to run as 20 and say nothing. Absent still
  means the default.

- **A configuration contract** in `internal/config/contract.go` declaring every
  variable's consumer, requirement, default, secrecy and purpose — and the gate
  that keeps `.env.example` and `docker-compose.yml` in step with it
  ([TD-004](docs/TECH_DEBT.md#td-004)). Published as a generated matrix in
  [docs/operations/RUNNING.md](docs/operations/RUNNING.md).

- **Minimal metrics** at `/metrics`, off by default
  ([TD-009](docs/TECH_DEBT.md#td-009)): request counts by
  method/route-pattern/status, a duration histogram, authentication failures and
  authorization denials. No new dependency — the trade against
  `prometheus/client_golang` is recorded in the package.

  Loopback-only without `METRICS_TOKEN`; bearer token for remote scraping;
  **404** rather than 401 when unauthorized, since there is no flow to
  authenticate into. No identifying value is ever a label.

- **Per-request access logs** carrying `request_id`, route pattern, status,
  duration and, for a machine caller, `project_id` / `credential_id` /
  `workspace_id`. Reads emit no audit event, so this is the only record that a
  credential performed one.

- **`error.field` on validation failures** ([TD-029](docs/TECH_DEBT.md#td-029)).
  `invalid_request` now names the offending field. `omitempty`, so an error that
  is not about a field carries no key at all and a client written before this
  decodes every response exactly as it did.

- **An end-to-end job in CI** ([TD-003](docs/TECH_DEBT.md#td-003), the half that
  mattered). PostgreSQL, Keycloak 26, migrations, the real binary, readiness,
  then the product driven through `scripts/m2m-harness.sh --smoke`: workspace,
  connection, project, credential, real read and write, wrong workspace,
  insufficient scope, immediate revocation, connection rotation, multi-realm
  isolation checked through Keycloak's own API, no secret in the log, graceful
  shutdown. Diagnostics are uploaded after `scripts/redact-logs.sh` strips
  anything credential-shaped.

- **`RUNNING.md`** — the operational reference: configuration matrix, fail-fast
  behaviour, probes, shutdown, metrics, log correlation, the recovery unit, the
  production smoke procedure, and what retries at startup versus what fails
  fast.

- **A bounded database connect retry** — 10 attempts over ~15s, then exit.
  Covers a database a few seconds behind on a host reboot without becoming a
  process that runs forever without serving. Migration failure is still
  immediate: it already connected, so retrying is how a half-applied migration
  becomes a corrupted one.

- **The migration CLI ships in the container image**, so
  `DB_MIGRATE_ON_BOOT=false` has a supported path:
  `docker compose run --rm api /app/migrate up`.

### Changed

- **`SECRETS_MASTER_KEY` is now the legacy spelling of `SECRETS_KEYRING=1:<key>`.**
  It still works, unchanged, and maps to version 1 — the version every existing
  row already carries, since the schema has defaulted `secret_key_version` to 1
  since migration 000003. Existing installations upgrade with no data migration
  and no configuration change. Setting both variables is refused: there is no
  ordering of the two that is obviously right, and guessing produces exactly the
  failure the keyring exists to prevent.

- **`docker-compose.yml` is the reference deployment, and Keycloak is behind a
  profile.** `docker compose up -d` starts PostgreSQL and the API against
  *your* Keycloak; `--profile dev-idp` adds a throwaway one. The API no longer
  `depends_on` Keycloak.

  This is architectural, not cosmetic: LIGHTWEIGHT connects workspaces to realms
  that already exist, wherever they are. A hard dependency in this file stated
  the opposite.

  **Breaking for anyone relying on `docker compose up` to start Keycloak** — add
  `--profile dev-idp`.

- **The container healthcheck is readiness**, run by the binary itself
  (`/app/api -healthcheck`) rather than by a tool the base image happens to
  ship. `stop_grace_period: 30s` so Docker's default 10s does not cut the drain
  short.

- **`temporary_password`, `email`, `name` and `roles` are marked `required`** in
  the OpenAPI document. They always were at runtime; the document said
  otherwise, so a generated client had the wrong signature.

- **A project credential can now reach the rate limit it is documented to have**
  ([TD-026](docs/TECH_DEBT.md#td-026)). The `/v1` edge limiter (10 req/s per IP)
  sat in front of the per-credential limiter (20 req/s) and, because it runs
  before authentication, was charged for both anonymous and machine traffic. A
  backend on one address was capped at 10 req/s and the published number was
  unreachable.

  The edge limiter now **reserves** a token and **releases** it once the request
  turns out to have authenticated as a project credential. Measured
  before/after with `scripts/m2m-harness.sh --bench`: 20 requests admitted
  before the first 429, then 44 — the credential's own burst instead of the
  edge's.

  **No number changed**, which was the point: raising the edge limit would have
  handed every extra request to an unauthenticated attacker too. Anonymous-flood
  protection and console throughput are bit-for-bit what they were.

  The release deliberately covers requests the credential limiter then refuses,
  so one runaway key cannot drain the shared IP bucket and throttle its siblings
  on the same host. What that leaves is recorded as
  [TD-028](docs/TECH_DEBT.md#td-028).

- **`RateLimit-Limit` now advertises the sustained rate, not the burst**, and
  `RateLimit-Remaining` is emitted on every project response instead of only as
  a constant `0` on the refusal. The header said `40` while the sustained rate
  was `20`: a client pacing itself by it would have been refused half its
  traffic for obeying it. **Breaking for any client reading that header as its
  quota** — the new value is lower and is the one that is true.

- **The `/v1` edge 429 now uses the `/v1` error envelope.** It answered the
  legacy `{"error":"rate limit exceeded"}` — no `error.code`, no `request_id` —
  which made it the one `/v1` response an SDK could not decode with the same
  type as every other error. `/admin/*` keeps the legacy body, unchanged.

- **`/v1` authentication and authorization are now separate middleware.** The
  group used to carry `RequireAuth → RequireRole → RequireLiveAdmin`: one kind
  of caller, authenticated and authorized in one run. A second kind cannot be
  served that way, so the chain is now `AuthenticatePrincipal` (who is calling)
  followed by `Authorize` (may they do this). **Operator behaviour is
  unchanged** — the same admin-role check, the same live check, in the same
  order so the cheap denial still short-circuits.

- **`/v1` authentication failures now use the `/v1` error envelope.** They
  answered the legacy `{"error":"unauthorized"}`, inherited from reusing
  `RequireAuth`, which contradicted this surface's own documented contract that
  every failure carries a code and a request id. `/admin/*` is unchanged.

- **`audit.Actor` is a discriminated record.** New `type`, `project_id` and
  `credential_id` fields, all `omitempty`; a project id never appears in
  `subject`. Events also carry `request_id` and `user_agent`. Additive on the
  wire; consumers matching the Go struct exactly need a recompile.

### Fixed

- **`make test-race-integration` could not pass** ([TD-030](docs/TECH_DEBT.md#td-030)).
  The `-p 1` in that target serialises the test PACKAGES, which does nothing
  about a package that exhausts PostgreSQL's connections on its own — and
  `internal/identityruntime` did. Its fixture was the only one of the five
  integration suites that never closed a pool, so three pools per test across
  nineteen tests held their idle connections for the life of the test binary,
  and the eighty-goroutine burst in
  `TestIsolation_ConcurrentRequestsDoNotCrossContaminate` then got
  `too many clients already` reported as `internal_error` from a resolver that
  was working perfectly. The fixture now closes and bounds its pools.

- **The Workspace Audit view crashed on any workspace that had events**
  ([KI-020](docs/KNOWN_ISSUES.md#ki-020)). `views/workspace-audit.js` called
  `renderTable({columns, rows})` with one argument, but the component is
  `renderTable(target, opts)` — so `opts.columns` threw on the first event a
  workspace ever recorded. The durable trail shipped above was therefore
  unreadable in the console for every real workspace; it rendered only while
  empty, because that branch returns before the table.

  Nothing caught it because the throw happens *after* an `await`: `router.js`
  catches synchronous view errors, but every view is `async`, so the rejection
  escaped and the page simply sat on "loading…" forever. The API returned 200
  with a correct body throughout. Found by the new browser suite; guarded now by
  `web/admin/static/js/tests/audit-view.test.mjs`, verified to fail 6-of-7
  against the pre-fix code.

- **OAuth authorization codes are no longer written to the access log**
  ([KI-019](docs/KNOWN_ISSUES.md#ki-019)). `gin.Default()` installs a formatter
  that prints the request URI *including the raw query*, and the console's PKCE
  callback is a browser navigation to `GET /admin?code=…&state=…` — so every
  operator login logged a live authorization code and a CSRF state token.

  Not critical (the code is single-use and PKCE-bound), but the protection was
  PKCE rather than the log, `state` is a CSRF token, and the same formatter
  printed *every* query string. `internal/logging/access_log.go` now redacts the
  values of a known-sensitive parameter set while keeping gin's layout
  byte-identical and keeping the parameter *name* visible, so a callback is
  still identifiable in a log. Found by the new suite's artifact scanner on its
  first otherwise-green run.

- **`roles:write` cannot be used to become a realm admin.** `identity.Service`
  protects reserved role names on create, update and delete, but not on
  *assignment* — correctly, since an operator reaching that endpoint is already
  a live realm admin. A machine credential is a different principal on the same
  endpoint, so a project holding `roles:write` may not grant or revoke `admin`,
  `user`, `offline_access`, `uma_authorization` or `default-roles-*`
  (`role_privileged`, 403). Revocation is guarded as well as granting: a machine
  able to strip `admin` could lock every operator out of the realm.

- **`access_mode` no longer claims write capability it has not proven**
  ([TD-024](docs/TECH_DEBT.md#td-024), migration `000004`). A service account
  with `view-users` but not `manage-users` passed both read probes and was
  recorded as `full`, so the API told clients writes were supported when it had
  only proven reads.

  `access_mode` gains a fourth value, `read_only`, and `full` is now claimed
  only when a write grant was positively proven — read from the
  `realm-management` roles Keycloak stamps into the service account's own access
  token, at **no extra request and with nothing written**. `ConnectionResponse`
  gains `can_write` so a client gates mutation controls on one verdict.
  `connection_read_only` now refuses `read_only` as well as `limited`.

  `unknown` still permits the write attempt: it means the provider published no
  evidence either way, and refusing on absent evidence would break working
  installations. Verified against a live Keycloak by
  `TestLiveVerify_AccessModeMatchesRealWriteOutcome`, which performs a real
  write with each fixture and asserts the verdict predicted the outcome.

- **The console rendered `/v1` errors as `[object Object]`.** `APIError`
  assumed `body.error` was a string, which holds for `/admin/*` and not for the
  structured `/v1` envelope. It now normalises both into `message`, `code`,
  `requestId` and `status`, degrades gracefully on a malformed body, and falls
  back to the `X-Request-Id` header when the body never reached the envelope.

### Added

- **Connection domain** (`internal/connection`, `internal/secrets`, migration
  `000003_connections`) — a Workspace's configured access to an identity
  provider. **Nothing consumes it yet:** the Identity API still uses the
  process-level `KEYCLOAK_*` configuration, and wiring the two together is a
  later slice.

  Eight routes under `/v1/workspaces/{workspace_id}/connections`, gated exactly
  as `/admin/*`. Lifecycle is `draft → active → retired`, with `retired`
  terminal and no reactivation.

  - **Credentials are sealed with AES-256-GCM before they reach the database**
    (`SECRETS_MASTER_KEY`, base64 32 bytes). A fresh random nonce per seal, and
    additional authenticated data binding each ciphertext to its row so a
    credential cannot be moved between connections. Only the ciphertext, nonce,
    key version and algorithm are stored. **The API never returns the secret** —
    the domain type has no field for it, so the guarantee is structural rather
    than a review convention; responses carry only `has_client_secret`.
  - **`SECRETS_MASTER_KEY` is optional**: without it the connection routes are
    not mounted, so existing deployments upgrade unchanged. A key that is set
    but unusable fails the boot rather than silently disabling the feature.
  - **Verify** (`POST …/verify`) runs a strictly read-only probe — reach the
    provider, confirm the realm, authenticate the service account, read the
    realm, list one user. It creates no test user and modifies nothing. The
    first three checks decide `health`; the two admin reads decide `access_mode`
    (`full`/`limited`), because a service account that authenticates but cannot
    read is under-privileged rather than misconfigured. A failed probe returns
    **200** with the verdict in the body.
  - **Activate** requires a verification that passed within the last hour, and
    retires the workspace's previous active connection in the same transaction.
    **One active connection per workspace is enforced by a partial unique
    index**, not by application code, so it holds under concurrency. Verified by
    an integration test that races six activations.
  - `DELETE` accepts draft and retired connections only; an active one must be
    retired first. `PATCH` accepts drafts only and resets the verification when
    a probed field changes.
  - Six checks against **real Keycloak 26** cover the probe end to end
    (full access, limited access, wrong secret, unknown client, unknown realm,
    read-only-ness); they skip when `KEYCLOAK_VERIFY_URL` is unset, as in CI.

  Documented in [docs/CONNECTIONS.md](docs/CONNECTIONS.md). New debt recorded:
  [TD-018](docs/TECH_DEBT.md#td-018) (duplicated token acquisition) and
  [TD-019](docs/TECH_DEBT.md#td-019) (no master-key rotation).
- **Workspace domain and the `/v1` API** (`internal/workspace`, migration
  `000002_workspaces`) — the first persistent concept of the Identity Control
  Plane. A Workspace is an isolated administrative context that will later own
  a Connection to an identity provider; it owns nothing yet and performs **no
  Keycloak operation**.

  Five routes, all behind the same chain as `/admin/*`
  (`RateLimitPerIP → RequireAuth → RequireRole("admin") → RequireLiveAdmin`):
  `GET`/`POST /v1/workspaces`, `GET`/`PATCH /v1/workspaces/{workspace_id}`,
  `POST /v1/workspaces/{workspace_id}/archive`. There is no `DELETE`.

  - **Public ids** (`internal/publicid`): `ws_<uuid>` on the wire, UUID
    generated by the application before the INSERT. A bare UUID is accepted on
    input as a development convenience; a wrong prefix is `invalid_workspace_id`
    (400) and fails before any query, never `workspace_not_found`.
  - **Slugs** are immutable, globally unique, and never released by archiving.
    A client-supplied slug is trimmed and lowercased but never slugified;
    slugification applies only when deriving one from `name`. Eight slugs are
    reserved for the platform.
  - **Archive** is idempotent and atomic, and is not a delete: the row stays
    individually readable, and a retried call returns it unchanged rather than
    conflicting.
  - **Stable error envelope** for `/v1`:
    `{"error":{"code","message","request_id"}}` with ten catalogued codes. No
    database message, constraint name or SQL fragment can reach a client.
  - **`X-Request-Id`** (`internal/requestid`) on `/v1` responses, honouring a
    validated inbound header. Scoped to the `/v1` group so `/admin/*` responses
    are unchanged.
  - Four CHECK constraints (`status`, the `archived_at` biconditional, slug
    format, non-blank name) make the invariants true of the data rather than of
    one code path. Slug collisions arrive as `gorm.ErrDuplicatedKey`, so the
    translation is deterministic and never parses PostgreSQL's message text.

  **No behaviour change to any existing surface.** `SetupRouter` gained a
  variadic `RouterOption` rather than a ninth positional parameter, so all
  fourteen existing call sites are untouched; a test builds the router with and
  without `/v1` and asserts the `/admin` route table, response and headers are
  identical. No default workspace is created — that belongs with Connection.
  Documented in [docs/WORKSPACES.md](docs/WORKSPACES.md).
- **Versioned database migrations** (`internal/database/migrations/`,
  `internal/database/migrate.go`) — replaces gorm `AutoMigrate`. SQL migrations
  are embedded in the binary with `go:embed` and applied by
  [golang-migrate](https://github.com/golang-migrate/migrate) at boot, gated by
  the new `DB_MIGRATE_ON_BOOT` (default `true`). A failed or inconsistent
  migration is fatal — the process exits rather than serve traffic against a
  schema of unknown shape. New `cmd/migrate` CLI behind `make migrate`,
  `make migrate-version`, `make migrate-steps`, `make migrate-down`,
  `make migrate-force` and `make migrate-new`. Closes
  [TD-005](docs/TECH_DEBT.md#td-005) and
  [V1-07](docs/ROADMAP.md#v1-07--versioned-migrations); documented in
  [docs/MIGRATIONS.md](docs/MIGRATIONS.md).

  **No action required on upgrade.** `000001_baseline` reproduces the exact
  schema `AutoMigrate` produced and is written idempotently, so an existing
  database is adopted in place — the migration runs, changes nothing, and
  records version 1. Covered by `TestMigrate_AdoptsLegacyAutoMigrateSchema`,
  which builds a database with the original `AutoMigrate` DDL, seeds a row, and
  asserts both schema and data survive.
- **SMTP management** (`internal/server/smtp_handler.go`): `GET`/`PUT
  /admin/settings/smtp` for the realm's SMTP block, `POST
  /admin/settings/smtp/test` to dial the server and verify credentials, and
  `POST /admin/users/password` to provision a user with a temporary password
  instead of the email-invite flow. The stored password is redacted to
  `••••••••` on read and carried forward on write when the placeholder is
  echoed back.
- **Email template customization** (`internal/server/email_templates_handler.go`):
  `GET /admin/settings/email-templates`, `PUT`/`DELETE
  /admin/settings/email-templates/:key`, backed by Keycloak's localization API.
  Admin console gains per-type tabs (invitation, reset, verification) and an
  HTML preview.
- **Custom Keycloak email theme** `corsi` (`deploy/keycloak/themes/corsi/`) —
  branded FTL templates in PT-BR for invitation, password reset and email
  verification. **Does not persist across container recreation** — see
  [KI-011](docs/KNOWN_ISSUES.md#ki-011).
- **`PUT /admin/users/:id/password`** — set a user's password directly via the
  Admin API, with an optional `temporary` flag.
- **`email_verified`** is now a settable field on `PATCH /admin/users/:id`.
- **Configurable CORS** via `CORS_ALLOWED_ORIGINS` (`internal/config`,
  `internal/server/server.go`). Empty means CORS stays disabled. **Not yet
  propagated through docker-compose** — see [TD-004](docs/TECH_DEBT.md#td-004).
- **`ADMIN_CONSOLE_CLIENT_ID`** lets the console's PKCE client be set
  independently of the dev playground's.
- **Canonical documentation set** under `docs/`: `PROJECT_STATUS.md`,
  `ARCHITECTURE.md`, `MODULES.md`, `FEATURES.md`, `ROADMAP.md`,
  `TECH_DEBT.md`, `KNOWN_ISSUES.md`.
- **Quality system** (2026-07-27) — enforced gates against regression:
  - **Blocking lint.** [`.golangci.yml`](.golangci.yml) with 9 linters green
    and enforced; `golangci-lint` pinned to `v2.12.2` and installed in CI.
    `errcheck` (20 findings) and `staticcheck` (13) deferred with a documented
    promotion procedure. Closes TD-011 — linting had silently never run.
  - **Coverage floor** at 73% via
    [`scripts/check_coverage.sh`](scripts/check_coverage.sh) (current 73.2%).
  - **Integration tests in CI** against a real PostgreSQL service container,
    and **admin console tests in CI** (30 cases) — both previously ran nowhere.
  - **Documentation gates.**
    [`scripts/check_docs_links.py`](scripts/check_docs_links.py) fails on any
    broken Markdown link or anchor;
    [`scripts/check_doc_metrics.py`](scripts/check_doc_metrics.py) fails when a
    number published in the docs stops matching the code (17 claims across four
    documents). Makes the TD-001 class of drift mechanically impossible.
  - **Git hooks** via `make hooks-install`: pre-commit (gofmt · vet · `.env`
    guard, ~3 s) and pre-push (`make ci` + `make check-docs`, ~60 s).
  - **New make targets:** `lint-install`, `test-frontend`, `coverage-gate`,
    `check-links`, `check-metrics`, `check-docs`, `ci-full`, `hooks-install`.
  - **CI split into 4 jobs** — `gate`, `coverage`, `frontend`, `integration`.
- **Process documentation:** `QUALITY_GATE.md`, `CONTRIBUTION_CHECKLIST.md`,
  `HEALTH_CHECK.md`, `RELEASE_CHECKLIST.md` (canonical, superseding the
  v0.2-specific one under `docs/release/`), `RISKS.md` (ten scored risks) and
  `MILESTONE_v0.4.md`.

### Fixed

- **Dead code and a false-positive-prone pattern**, both surfaced by the first
  real lint run: removed the unused `newProviderWithKeyfunc` "test seam" in
  `internal/auth/keycloak` that no test used, removed two redundant loop-variable
  copies (Go 1.22+), and made the deliberate `nilerr` suppression in
  `identity/keycloak/realm.go` explicit — the informational read-back there must
  not roll back a successfully provisioned user.
- **15 broken documentation anchors** across the historical `security/`,
  `release/` and `roadmap/` documents, including `#L<n>` fragments that never
  worked in rendered Markdown. `make check-links` now blocks on these.
- **`/auth/debug` was registered twice**, so the process panicked at startup
  whenever `DEV_PLAYGROUND_ENABLED=true` — the default in `.env.example`, and
  therefore the documented onboarding path. With the playground off, the
  surviving handler omitted `valid`, `issuer` and `allowed_clients`, causing the
  admin console to render an authenticated admin as "not signed in". Now one
  handler serves two scoped routes: `GET /auth/debug` (authenticated, always
  mounted) and `GET /dev/auth/debug` (unauthenticated, dev-only, the only one
  that can explain *why* a token was rejected). Regression guards added for all
  four flag combinations and for the console's payload contract. Full analysis:
  [KI-001](docs/KNOWN_ISSUES.md#ki-001).
- Admin console derives `redirectUri` from the request host instead of assuming
  `localhost`, so it works behind a proxy or on a real domain.
- Admin console auto-redirects to Keycloak login when unauthenticated rather
  than rendering an empty shell.
- Automatic single-flight token refresh on `401` in the SPA's API client,
  preventing a stuck session after the access token expires.
- Email templates no longer render `[object Object]`; realm i18n is enabled
  automatically when a template is customized.
- Invitation email template filename corrected to `executeActions.ftl`.
- Server tests realigned with the current `SetupIdentity` (4 return values) and
  `SetupRouter` (8 parameters) signatures; CI restored to green.
- Swagger artifacts regenerated for `PUT /admin/users/:id/password`.

### Changed

- README corrected: the admin surface is **32** routes, not 22, and the audit
  subsystem has **14** canonical actions, not 13. Added an explicit scope
  statement clarifying that this is an IAM foundation, not a complete SaaS
  backend.
- `docs/roadmap/KNOWN_LIMITATIONS.md` marked **L4** (audit wiring) and **L5**
  (dev SMTP) as resolved — both had been implemented since `v0.3.0` but were
  still listed as open.
- `make seed` no longer claims a `database.seedDefaultUser` function; no such
  function exists.

### Reverted

- `feat(admin): profile page + settings redesign` (`aa0c6ab`) was reverted in
  `0d5da8a` on the same day. Net effect on this release: none.

### Removed

- `CLAUDE_FIX_TESTS.md` — described a broken test suite that commit `e2a3bcd`
  had already fixed.

---

## [0.3.1] — 2026-05-25

Retroactively documented on 2026-07-26; this tag shipped without a changelog
entry ([KI-008](docs/KNOWN_ISSUES.md#ki-008)).

### Added

- **Landing page** at `GET /` (`internal/server/landing.go`,
  `web/landing/index.html`) — an admin-aligned root page so a bare deployment
  presents something coherent instead of a 404.

## [0.3.0] — 2026-05-25

Production Hardening release. No new product surface on top of v0.2.0 — this
milestone closes the operational and production-readiness gaps identified in
the v0.3 security validation and the post-v0.2 UI reliability catalog, and
adds the repo metadata + runbooks needed to defend a real deployment.

### Added

- **Per-IP rate limit on `/admin/*`** (`internal/server/ratelimit.go`):
  in-process token bucket, defaults 10 req/s with burst 20, mounted **before**
  auth so unauthenticated floods cannot burn CPU on JWT validation. Closes
  Finding **F1** from `docs/security/SECURITY_VALIDATION_v0.3.md` and
  `docs/security/FINAL_SECURITY.md`.
- **In-memory audit ring buffer** (`internal/audit/memory.go`,
  `internal/audit/multi.go`) and read-only **`GET /admin/audit-events`**
  endpoint. Bounded, process-local, volatile — explicitly labeled as such in
  the UI; the durable trail remains the structured-log `AuditSink`.
- **Audit Logs admin view** now reads the live buffer (was a "coming soon"
  placeholder in v0.2).
- **`ADMIN_CONSOLE_ENABLED`** config flag (`internal/config/config.go`)
  splits the admin console from the dev playground. Production recipe:
  `ADMIN_CONSOLE_ENABLED=true` + `DEV_PLAYGROUND_ENABLED=false`.
- **`/admin/config.json`** now advertises `devTools` / `apiExplorer` flags so
  the SPA can hide `/playground` and `/api-explorer` in production deployments.
- **Repo metadata**: `LICENSE` (MIT), `CONTRIBUTING.md`, `SECURITY.md` with
  private-advisory reporting channel and supported-versions matrix.
- **Operations runbooks**: `docs/operations/PRODUCTION_DEPLOYMENT.md`,
  `docs/operations/INCIDENT_RESPONSE.md`, `docs/security/SECRET_ROTATION.md`.
- **GitHub Actions**: `.github/workflows/ci.yml` (`make ci` on every push/PR)
  and `.github/workflows/codeql.yml` (weekly Go analysis + PR scans).
- **Test coverage expansion**:
  - `internal/server/server_test.go` — full router / admin-console /
    auth-debug / admin-checker coverage, including path-traversal rejection
    on the embedded docs viewer.
  - `internal/config/config_test.go` — full `LoadConfig` env-var matrix
    incl. `ADMIN_CONSOLE_ENABLED`.
  - `internal/database/database_test.go` — reflection pin on the `User`
    migration contract.
  - `internal/database/database_integration_test.go` — `AutoMigrate`
    happy-path against the docker-compose Postgres (build tag: `integration`).
  - `internal/audit/memory_test.go`, `internal/server/ratelimit_test.go` —
    pin the new audit-buffer and rate-limit contracts.
  - `web/admin/static/js/tests/{auth,busy-guards,overview}.test.mjs` —
    Node `--test` suites that pin UI-001..004 regressions.

### Fixed

- **UI-001** — `startLogin` is now idempotent. A double-click on Login used
  to generate two PKCE `(verifier, challenge)` pairs and corrupt
  `sessionStorage`, causing the subsequent `/token` exchange to fail with
  `invalid_grant`. Concurrent calls now share a single in-flight promise.
- **UI-002** — Overview view bails on stale generation / route change before
  the post-await mount, so a slow render can no longer clobber a view the
  user has already navigated to.
- **UI-003** — "Send reset email" on user-detail disables itself in-flight;
  stops N duplicate `VERIFY_EMAIL` emails on double-click.
- **UI-004** — Invitations "Resend" disables per-row while in-flight; stops
  N duplicate action emails. Invite + edit modals reject malformed email
  client-side (mirrors server `identity.emailPattern`).

### Security

- **F1 closed**: per-IP rate-limit middleware on `/admin/*`.
- **SECURITY.md** establishes a private vulnerability reporting channel
  (GitHub Security Advisory + maintainer email) with embargo policy.
- **CodeQL** workflow runs weekly + on every PR.
- **Production posture**: admin console can now be served in production
  **without** mounting `/playground` or `/api-explorer`; SPA nav is pruned at
  boot from server-advertised flags and direct deep-links to hidden dev
  surfaces are bounced to `/overview`.

### Operations

- Pre-flight checklist and TLS / managed-DB / secret-store guidance in
  `docs/operations/PRODUCTION_DEPLOYMENT.md`.
- 10-minute TL;DR runbook + severity classification in
  `docs/operations/INCIDENT_RESPONSE.md`.
- Per-secret rotation cadence + step-by-step procedures (incl. emergency
  compromise path) in `docs/security/SECRET_ROTATION.md`.

### Breaking

- `server.SetupRoutes` / `server.SetupRouter` signatures take an additional
  `*server.AuditHandler` parameter. Internal-only call sites
  (`cmd/api/main.go`, tests) are updated; external embedders must pass
  `nil` to preserve v0.2 behavior (the `/admin/audit-events` route is then
  omitted).

## [0.2.0] — 2026-05-20

Identity Management milestone. Adds an admin-only HTTP surface that wraps the
Keycloak Admin API for user, role, session, and invitation administration,
plus role-based access control middleware and a minimal admin web UI.

Full release notes: [docs/RELEASE_v0.2.md](docs/release/RELEASE_v0.2.md).

### Added

- **Admin API** (`/admin/*`, all `Bearer` + `admin` realm role):
  - `/admin/users` — list, get, update, delete; password-reset email;
    per-user roles (list / assign / remove); per-user sessions (list / revoke-all).
  - `/admin/invitations` — list pending, create (dispatches `VERIFY_EMAIL`
    + `UPDATE_PASSWORD` action emails), revoke, resend.
  - `/admin/roles` — list, create, get, update description, delete; list
    users carrying a role.
  - `/admin/sessions` — list realm-wide active sessions; revoke individual
    session.
- **RBAC middleware**: `auth.RequireRole(role)` and `auth.RequireAnyRole(...)`
  for declarative role gates at the route-group level. Denials emit a
  structured `EventForbidden` `AuthEvent` for observability parity with
  authn failures.
- **Admin web UI** under `web/admin/` (static `index.html` + assets) —
  thin client over the Admin API for local development and ops.
- **`features.identity_management`** flag in `config/project.json` —
  gates mounting of the `/admin/*` group at server startup.
- **`auth.RequireLiveAdmin` middleware** (GAP-1 remediation): per-request
  re-check that the calling subject still carries the realm `admin` role,
  read live from the Keycloak admin API rather than trusted from the
  bearer token's role claim. Mounted as the third gate on `/admin/*`
  after `RequireAuth` + `RequireRole("admin")`.
- **`auth.CachedAdminChecker`** with `Invalidate(subject)` and
  `InvalidateAll()` (and the `auth.AdminInvalidator` interface) — bounds
  Keycloak load for the steady-state admin workflow while letting
  identity mutations evict cached entries immediately.
- **New environment variables** (consumed by the service-account client
  that calls the Keycloak Admin API):
  - `KEYCLOAK_ADMIN_CLIENT_ID`
  - `KEYCLOAK_ADMIN_CLIENT_SECRET`
  - `KEYCLOAK_ADMIN_BASE_URL` (defaults to in-network
    `http://keycloak:8080` in `docker-compose`; deliberately distinct from
    `KEYCLOAK_URL` so issuer matching is unaffected).
  - `ADMIN_LIVE_CHECK_TTL_SECONDS` — operator knob for the
    `CachedAdminChecker` TTL (surfaced as `Config.AdminLiveCheckTTL()`).
- **Bootstrap regen** writes the new admin client into
  `deploy/keycloak/realm-export.json` and seeds the new env keys into
  `.env` / `.env.example`.

### Changed

- `docs/swagger.{json,yaml,docs.go}` regenerated to cover the new
  `/admin/*` endpoints. API `info.version` stays at `1.0` (additive,
  non-breaking surface change).
- `internal/server/router.go` adds the `admin` route group with
  `RequireAuth + RequireRole("admin") + RequireLiveAdmin(checker)`
  applied at the group level. The live-admin checker is wired in by
  `server.SetupIdentity`, which also calls `identity.Handler.SetAdminInvalidator`
  so mutations (`AssignRolesToUser`, `UnassignRolesFromUser`, `DeleteUser`,
  `UpdateUser`) evict cached admin status for the affected subject before
  returning to the client.

### Fixed

- **CRUD reliability — compensating-delete made observable.**
  `keycloak.compensateInvitationCreate` previously discarded the cleanup
  DELETE result with `_ = …`. Under SMTP outage (when Keycloak's
  `executeActionsEmail` returns 500), any failure of the rollback was
  invisible and orphan users could accumulate. The cleanup path now
  reports both success and failure through the `identity-kc` logger,
  and a destructive stress run (5 consecutive SMTP-failed invites)
  observes zero orphans. Repro and verification: `docs/BUG_REPORT_CRUD.md`
  (case `I14b`).

### Security

- **GAP-1 closed — stale-admin-JWT replay against `/admin/*`.** Prior to
  this release, a token issued while the subject held the `admin` realm
  role could be replayed against the admin surface after the role was
  revoked, for the remainder of the token's lifetime — the gate only
  consulted the claim baked into the JWT at issue time. The remediation
  combines:
  - `auth.RequireLiveAdmin` — re-reads the subject's live realm roles
    on every admin request via the identity provider;
  - `auth.CachedAdminChecker` — bounds upstream load with a TTL the
    operator controls via `ADMIN_LIVE_CHECK_TTL_SECONDS`;
  - immediate cache invalidation from the identity handler on every
    role/user mutation, so revocations take effect on the next request
    rather than waiting for the TTL to roll;
  - **fail-closed on checker error** — an upstream Keycloak failure
    returns `503` rather than admitting the request on the token claim
    alone (`TestRequireLiveAdmin_UpstreamError_FailsClosed`).
  Regression coverage: **7 / 7 PASS** across the GAP-1 scenarios —
  see `docs/SECURITY_REGRESSION_GAP1.md` and the design rationale at
  `docs/SECURITY_REMEDIATION_GAP1.md`.
- **Audit-event coverage validated.** Every admin mutation handler now
  has a paired audit-record assertion in
  `internal/identity/handler_audit_validation_test.go`, ensuring the
  structured `AuditEvent` (actor + target + action) is emitted on every
  mutating verb the admin surface exposes.

### Notes

- No data migrations are required; the `users` table schema is unchanged
  from `0.1.0`.
- The Admin API is **dev-only by default** in the same sense as the rest
  of the stack — see [docs/operations/PRODUCTION_DEPLOYMENT.md](docs/operations/PRODUCTION_DEPLOYMENT.md)
  before exposing it. (This originally linked to a README section that no
  longer exists; corrected 2026-07-26.)
- Milestone outcome: the `/admin/*` surface — the `auth` + `identity`
  packages, the realm-import workflow, the regen pipeline, and the
  GAP-1 live-admin remediation — is intended as a **reusable IAM
  foundation** for other Go services that delegate identity to Keycloak.
  No service-specific business logic leaks into either package.

## [0.1.0] — 2026-05-17

Initial tagged release — **Authentication foundation** (`v0.1.0-auth-foundation`).

### Added

- Keycloak-delegated authentication: JWKS-validated RS256 tokens via
  `github.com/MicahParks/keyfunc/v3` with kid-miss refresh.
- `auth.AuthProvider` interface (Keycloak today, provider-agnostic by design).
- `auth.RequireAuth(provider)` Gin middleware; `Identity` propagated via
  `gin.Context`.
- Idempotent just-in-time user provisioning on first protected request
  (`/me`); race-safe via DB unique index on `keycloak_sub`.
- Structured `auth.AuthEvent` + `auth.SetEventHook` for observability.
- Config-as-source-of-truth bootstrap (`config/project.json` →
  `make regen` → `.env`, `realm-export.json`, `project.schema.json`).
- Categorized `make help`; `make doctor` toolchain probe;
  `make reset-dev` one-command rescue.
- Swagger / OpenAPI documentation via `swaggo`; CI gate
  `make swagger-check` blocks drift between handlers and committed specs.
- 41 unit tests, including a 50-goroutine race on JIT user provisioning.

[Unreleased]: https://github.com/joaogabrielvianna/lightweight-saas-backend/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/joaogabrielvianna/lightweight-saas-backend/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/joaogabrielvianna/lightweight-saas-backend/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/joaogabrielvianna/lightweight-saas-backend/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/joaogabrielvianna/lightweight-saas-backend/compare/v0.1.0-auth-foundation...v0.2.0
[0.1.0]: https://github.com/joaogabrielvianna/lightweight-saas-backend/releases/tag/v0.1.0-auth-foundation
