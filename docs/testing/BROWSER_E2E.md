# Browser end-to-end tests

**The operator boundary.** A real Chromium, driving the real admin console,
against a real LIGHTWEIGHT process, a real PostgreSQL and a real Keycloak 26.
No mocked API, no injected token, no fake realm.

Companion to [`scripts/m2m-harness.sh`](../../scripts/m2m-harness.sh), which
tests the *machine* boundary. Neither replaces the other:

| | What it proves |
|---|---|
| `scripts/m2m-harness.sh` | an external backend with an API key can **use** the product |
| `scripts/browser-e2e.sh` | a person with a browser can **configure** the product so that key can exist |

Everything a machine consumes has to be created by an operator first — the
workspace, the connection to a realm, the project, the credential. Until this
suite existed a regression in that path shipped with every gate green, which is
[TD-031](../TECH_DEBT.md#td-031).

---

## Running it

```sh
# 1. Bring up PostgreSQL + Keycloak (the dev-idp compose profile)
make up-infra

# 2. Create a throwaway database, then run
DB_URL=postgres://saas:saas@localhost:5432/saas_browser?sslmode=disable make e2e-browser
```

The suite must start from an **empty installation** and says so if it does not.
That is not fussiness: the console auto-selects the first active workspace at
boot, so rows left by an earlier run decide what the first screen shows — and
those rows point at Keycloak realms this script deletes and recreates on every
run. CI never hits this (its PostgreSQL is a fresh service container per job).
Locally, re-run with:

```sh
DB_URL=…  ./scripts/browser-e2e.sh --reset-db     # drops + recreates schema "public"
```

Other flags:

```sh
./scripts/browser-e2e.sh --headed                 # watch it happen
./scripts/browser-e2e.sh --keep                   # leave the stack up afterwards
./scripts/browser-e2e.sh -- --grep "Journey 2"    # pass through to Playwright
```

`--grep` isolates a test from the serial steps it depends on, so a single step
of a journey will usually fail on its own precondition. Grep by journey, not by
step.

---

## What it covers

Twenty-seven tests across five spec files, in `tests/browser/tests/`.

| Spec | Journey |
|---|---|
| `login.spec.js` | Authorization Code + PKCE against Keycloak's own login form; the [KI-001](../KNOWN_ISSUES.md#ki-001) regression; session survives a reload |
| `workspace-connection.spec.js` | create a workspace · create a connection · verify · activate · the workspace becomes usable by the runtime · the client secret never reappears · a `/v1` error renders as prose |
| `project-credential.spec.js` | create a project · issue a credential with chosen scopes · the one-time secret · the credential works from outside over plain HTTP · an audited M2M mutation · the Audit view attributes it to the project · one audit filter · revoke in the browser · immediate refusal · the revocation attributed to the operator |
| `workspace-isolation.spec.js` | two workspaces on two realms: users, projects and audit stay apart; an open dialog is dismissed on switch |
| `logout.spec.js` | RP-initiated logout, session cleared, console no longer usable as authenticated |

### The login is real

There is no login page and no sign-in button: `main.js` boot() finds no token
and redirects to Keycloak itself. So `page.goto('/admin')` *is* the login
trigger. The helper then asserts the authorization request carries
`response_type=code` and `code_challenge_method=S256`, types the operator's
credentials into Keycloak's own form, and waits for the console to come back
authenticated.

Nothing writes a token into `sessionStorage`. A helper that did would make every
other journey pass while the login flow was broken — which is the failure mode
TD-031 describes, reproduced inside the fix for it.

---

## Artifact policy

**`trace: off` · `screenshot: off` · `video: off`.** This is a security
decision, not a performance one, and
[`playwright.config.js`](../../tests/browser/playwright.config.js) explains it
at length.

The suite handles three values that must not outlive the run: the
identity-provider client secret the operator **types into a form**, the project
credential the server displays **once**, and the operator's bearer token. A
Playwright trace stores DOM snapshots including input values; a failure
screenshot stores whatever was on screen — and the most valuable screenshot,
the one taken while the credential modal is open, is precisely the one holding a
live credential.

Three options were considered:

| | Verdict |
|---|---|
| A. trace only for specs without secrets | rejected — safety becomes a per-file judgement that will be wrong the first time a spec grows |
| C. redact traces after the fact | rejected — a trace is a zip of DOM snapshots and network bodies; "we redacted it" is only true if you can prove the *published* copy is the redacted one |
| **B. capture nothing that can hold a secret** | **chosen** — the smallest thing that is safe by construction |

`PLAYWRIGHT_NO_COPY_PROMPT=1` is exported by the runner for the same reason: it
suppresses the ARIA page snapshot Playwright otherwise writes into
`error-context.md` on failure, and an ARIA snapshot renders a textbox as
`- textbox "client secret *": <value>`.

### What replaces them

Enough to debug a CI failure, all of it plain text:

- every page error, `console.error` and failed request, per test, written by
  `fixtures/console-errors.js`;
- the current URL and the test title, with OAuth parameters redacted;
- the failing locator and Playwright's call log;
- the LIGHTWEIGHT and Keycloak logs, both through
  [`scripts/redact-logs.sh`](../../scripts/redact-logs.sh) before upload.

### And it is proved, not asserted

[`scripts/scan-artifacts.sh`](../../scripts/scan-artifacts.sh) runs after every
suite — pass or fail, because a failing run produces the most artifacts and is
therefore the likeliest to leak. It searches everything published for:

1. the **exact values** the run used. Tests call `recordSecret()` the moment a
   secret becomes visible, writing it to a sentinel directory that lives
   **outside** the artifact tree — a list of secrets stored among the artifacts
   would be both the leak and the thing detecting it;
2. **shapes**: `lw_sk_…`, JWTs, `Authorization: Bearer …` — the same vocabulary
   `redact-logs.sh` strips, so the two halves of the rule cannot drift.

This gate has already earned its place: on its first green run it failed the
build because the OAuth authorization code was appearing in the application
access log. See [KI-019](../KNOWN_ISSUES.md#ki-019).

---

## Console errors fail the test

Every test gets an error collector (`auto: true` — a lazy fixture would silently
not apply to tests that do not destructure it). Unhandled page errors and
`console.error` calls fail the run.

This matters because of how the console fails: `router.js` catches *synchronous*
throws from a view, but every view is `async`, so a rejection after the first
`await` escapes entirely and leaves the page on "loading…" forever, with the
only evidence in the browser console. That is exactly how
[KI-020](../KNOWN_ISSUES.md#ki-020) hid.

A test that deliberately provokes an HTTP failure declares it:

```js
errors.allowStatus(400, /\/v1\/workspaces$/);
```

The permanent allowlist in `fixtures/console-errors.js` is short and every entry
carries the reason it is benign. **Do not widen a test to make an error go
away** — allowlist it with a justification, or fix the product.

---

## Fixture architecture

```
scripts/browser-e2e.sh
  ├── scripts/lib/keycloak-fixture.sh   shared with scripts/m2m-harness.sh
  │     realms · users · service-account clients · the browser PKCE client
  ├── builds three realms:  lw-e2e-control · lw-e2e-alpha · lw-e2e-bravo
  ├── boots ./bin/api on port 58095 against DB_URL
  ├── waits for /health/ready          ← BEFORE Chromium opens
  ├── refuses to start if the installation is not empty
  ├── npx playwright test              ← tests/browser
  └── scripts/scan-artifacts.sh
```

`scripts/lib/keycloak-fixture.sh` is shared with the m2m harness rather than
copied. Two implementations of "how this project builds a fixture realm" would
mean the copy is the one that does not get the fix when Keycloak changes an
admin-API shape.

**Test isolation is by naming, not by truncation.** Every workspace, project,
credential and role carries a per-run id. Nothing deletes rows it did not
create, and no container is removed except by the explicit name this script gave
it.

### Preconditions vs. journeys

`fixtures/operator-api.js` provisions preconditions over the **public HTTP
contract** with an operator token — exactly what the m2m harness does with curl.
The rule is: *whatever a spec is proving happens in the browser; whatever it
merely needs is provisioned.* `workspace-connection.spec.js` clicks through
creating a connection because that is its subject;
`workspace-isolation.spec.js` provisions two of them because its subject is what
happens once two exist.

Nothing touches the database, and no connection is ever marked active except by
pressing Verify and then Activate.

---

## Settings, and why

| Setting | Value | Why |
|---|---|---|
| Browser | Chromium only | cross-browser is not the risk for a self-hosted admin tool at this stage; every extra browser is ~200 MB per CI run for coverage nobody asked for |
| `retries` | `0` | a retry policy adopted before the suite's stability is known turns flakiness into a slower green build, discovered later from a bug report |
| `workers` | `1` | the journeys share one Keycloak, one database and one process; the audit trail is installation-wide. Parallelism comes after isolation is proven |
| Playwright | pinned `1.62.1` via committed lockfile | `npm ci` installs exactly the lockfile, so the toolchain belongs to the repository rather than to whoever ran it last |
| `RATE_LIMIT_EDGE_RPS` | `50` in the fixture | the browser AND the fixture drive the same loopback address at machine speed; the limiter is covered by `ratelimit_v1_test.go` and by the m2m harness, which paces deliberately |

**Time budget: under 5 minutes in CI.** Observed browser time locally is ~14
seconds for 27 tests; the job's wall time is dominated by Keycloak boot
(~15 s), `npm ci` and the Chromium download.

---

## Selectors

Role and accessible name, in that order of preference:

```js
page.getByRole("button", { name: "+ New workspace" })
page.getByRole("dialog", { name: "New connection" })
page.getByLabel("client secret *", { exact: true })
page.getByLabel("Active workspace")               // the workspace <select>
```

The console needed **no `data-testid` at all**. Its markup was already semantic:
real `<button>`s with real text, `role="dialog"` with the title as its
accessible name, real `<table>`s, and form inputs wrapped in `<label>` so the
implicit association resolves.

Two notes for anyone extending the suite:

- `{ exact: true }` on `"Close"` is load-bearing — every modal also carries a
  "×" dismiss button whose `aria-label` is the lowercase `"close"`, and case is
  the only thing separating them. A small accessibility smell, recorded rather
  than redesigned: renaming a control an operator relies on is not a change a
  test should force.
- Prefer `getByRole("row").filter({ hasText: … })` over a cell locator. A
  username appears in the username cell, the email cell *and* the first-name
  cell, so a cell match resolves three times.

Two things Keycloak owns are located by id — `#username`, `#password`,
`#kc-login`. That page is not ours to make accessible, the ids have been stable
across many major versions, and the image is pinned.
