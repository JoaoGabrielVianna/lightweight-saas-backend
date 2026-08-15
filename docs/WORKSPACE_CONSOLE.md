# Workspace console

**Scope:** how the admin console became multi-workspace — where the workspace
lives, how a switch is made safe, and what deliberately did not move.

**Status:** shipped 2026-08-09. Code under
[`web/admin/static/js`](../web/admin/static/js/).

The API it consumes is
[WORKSPACE_IDENTITY_API.md](WORKSPACE_IDENTITY_API.md). What a workspace routes
through is [CONNECTIONS.md](CONNECTIONS.md).

---

## 1. The problem

Before this slice the console had no workspace concept. Every request went to
`/admin/*`, implicitly scoped to the one realm in `KEYCLOAK_*`. The 24-operation
`/v1` surface existed and was tested, and nothing used it.

Migrating was not a matter of rewriting paths. Two workspaces point at two
different Keycloak realms, and once a console can switch between them the
failure mode stops being cosmetic: **showing workspace A's users under
workspace B's name is the operator deleting the wrong person.**

So the work is one third path replacement and two thirds isolation.

## 2. Where the workspace lives: the route

```text
#/workspaces/ws_3f25…/users
#/workspaces/ws_3f25…/users/9c1e…
#/workspaces/ws_3f25…/roles
```

The alternative was a workspace held in application state with flat paths
(`/users`). The route won, for reasons that are properties of this product
rather than general preference:

- **Two realms in two tabs** is the point of a control plane, and shared
  application state cannot express it.
- **Refresh, bookmark, back button and deep link** all become correct without a
  persistence rule for each.
- **The Phase 13 requirement becomes structural.** SMTP and email templates
  cannot accidentally follow the selected workspace, because their URLs have
  nowhere to put one.

The cost was one prefix rewrite in the sidebar, because the hash router already
supported multi-segment params (`/docs/:a/:b` predates this).

**Not every page carries a workspace**, and that split is the design:

| Carries a workspace | Does not |
|---|---|
| Users · Roles · Sessions · Invitations · Connections | Overview · Audit Logs · Swagger · Settings · Playground · API Explorer · **SMTP · Email templates** |

Selection is still persisted — `localStorage["lw_selected_workspace"]` — but
only as the fallback for a URL that names no workspace. The route always wins,
so a deep link is never overridden by what the last tab happened to be doing.

## 3. Resolution rules

`pickWorkspaceId` decides which workspace is current, in this order:

1. the id in the URL;
2. the persisted id;
3. the first **active** workspace;
4. none.

A candidate must exist in the list **and** be active. A persisted workspace that
has since been archived or deleted falls through deterministically rather than
leaving the console pointed at something that can only answer "this is
archived". The stale value is then removed from storage, so a reload does not
re-run the same failed lookup.

Zero workspaces is a product state, not an error, and it **never** falls back to
`/admin/*`. Falling back would show an operator a realm they did not choose and
let them mutate it believing otherwise.

## 4. Isolation

Three mechanisms, all in [`lib/workspaces.js`](../web/admin/static/js/lib/workspaces.js).
Views do not re-implement any of them.

### 4.1 The stale-response race

```text
workspace A selected
  → GET users A starts
    → operator switches to B
      → GET users B starts
        → B returns and renders
          → A returns LATER
```

Every switch bumps a **generation** counter and replaces an **AbortController**.
A view captures both before its request and re-checks before touching the DOM:

```js
const token = captureWorkspaceToken();
const r = await apiTry(wsPath(workspaceId, "/users"), { signal: token.signal });
if (isWorkspaceStale(token)) return;
```

The abort means most of those responses never arrive. The generation is what
makes the guarantee unconditional when the abort loses the race — and it is also
what catches **A → B → A**, where the workspace id matches again but the
in-flight response predates two switches and a possible mutation.

### 4.2 Mutation across a switch

```text
operator opens "Delete user <uuid>" in workspace A
  → switches to workspace B
    → clicks Delete
```

The dialog's closure still holds A's user id. Reading the workspace at click
time would send it to B, where it either 404s or — in a realm cloned from A's —
deletes a **different person**. Two independent locks:

1. a switch closes every open dialog (`closeAllModals`, wired to the switch
   listener at boot);
2. `wsMutate(workspaceId, …)` takes the workspace **captured with the entity**
   and refuses to send anything at all if it is no longer current. It does not
   retarget; it does not fall back.

### 4.3 Page state

Pagination, search and the selected entity live in the URL, which changes with
the workspace, so none of them can survive a switch. Switching keeps the
operator on the same *page* (`/roles` → `/roles`) but **drops entity segments**:
`/workspaces/A/users/<uuid>` becomes `/workspaces/B/users`.

## 5. Connection state in the UI

The selector shows the workspace name and one state pill, because "which realm"
and "can I do anything to it" are the same question in practice:

| Pill | Means | Writes |
|---|---|:--:|
| **Healthy** | active connection, healthy, write capability proven | yes |
| **Read-only** | reads work; the service account provably cannot write | **no** |
| **Unavailable** | the last verification failed | attempted |
| **No connection** | the workspace routes nowhere | blocked before the request |
| **Archived** | the workspace is frozen | blocked before the request |

The console gates mutation controls on the API's own `can_write` field, never on
`access_mode` — one verdict, one owner. See §6.

**Doomed requests are not fired.** No active connection and archived workspace
are both refused by `/v1` before it contacts the provider, so the view renders a
state with a way forward instead. An `unhealthy` connection **is** still tried:
health is the verdict of the last verify, not a live fact, and refusing would
make the console less capable than curl.

## 6. TD-024: `full` now means what it says

The console needed to disable mutation controls when a workspace cannot write.
It could not: [TD-024](TECH_DEBT.md#td-024) recorded that a service account with
`view-users` but not `manage-users` passed both read probes and verified as
`full`. The API was claiming write capability it had only inferred from reads.

Resolved in this slice. `access_mode` gains a fourth value and `full` is now
claimed **only when write capability has been positively proven**:

| Value | Meaning | `can_write` |
|---|---|:--:|
| `full` | reads work **and** a write grant was proven | yes |
| `read_only` | reads work; the provider reported **no** write grant | **no** |
| `limited` | the admin reads themselves were refused | **no** |
| `unknown` | the provider gave no usable evidence either way | yes |

The proof costs **no extra request and mutates nothing**: Keycloak stamps a
service account's `realm-management` roles into the `client_credentials` access
token the probe already obtained, so the provider has already said what it will
allow. `realm-admin` or `manage-users` proves it.

`unknown` still permits the attempt, deliberately. It means the client's scope
does not publish its grants — refusing on absent evidence would break working
installations for a signal that was never promised, and the authoritative answer
still arrives as `provider_forbidden`.

`manage-realm` alone is **not** accepted: it permits realm-role writes while
leaving every user mutation refused, so treating it as write capability would
reproduce the same over-claim one endpoint over.

Verified against a live Keycloak by
`TestLiveVerify_AccessModeMatchesRealWriteOutcome`, which builds a read-write and
a genuinely read-only administrative client, asks Keycloak to perform a real
write with each, and asserts the verdict predicted the outcome.

## 7. What did not move

`/admin/*` is untouched and remains the compatibility surface.

**SMTP and email templates stay on it.** They are provider realm *settings*, not
identity administration, and their implementations call the concrete Keycloak
provider directly, past both `IdentityProvider` and `identity.Service`.
Migrating them means designing that seam — a slice, not a step
([WORKSPACE_IDENTITY_API.md §7](WORKSPACE_IDENTITY_API.md#why-smtp-and-email-templates-are-deferred)).

They sit under a **LEGACY PROVIDER SETTINGS** nav section, their routes carry no
workspace segment, and each page opens with a banner saying it applies to the
installation's own realm whichever workspace is selected. The risk being managed
is not that they are legacy — it is that an operator reads the workspace
selector at the top of the screen and assumes the page below follows it.

**Audit events** stay installation-wide, deliberately: they are control-plane
events, not realm events.

**Operator authentication is unchanged.** Three boundaries stay distinct:

```text
operator authentication  ≠  workspace  ≠  provider administrative connection
```

Selecting a workspace is not a second login. The console never authenticates the
operator against a workspace's realm — the connection's service account does
that, server-side, and its credentials never reach the browser.

## 8. The error envelope

`/admin/*` answers `{"error": "not found"}`; `/v1` answers
`{"error": {"code", "message", "request_id"}}`. The console's `APIError` used to
do `body.error` and hand the result to `super()`, which for `/v1` produced the
literal string `[object Object]` in every toast and empty state.

Fixed first, before any view was migrated, because a view written against a
broken error path bakes the breakage into its own rendering. `APIError` now
exposes `message`, `code`, `requestId` and `status` for both surfaces, degrades
gracefully on a malformed body, and falls back to the `X-Request-Id` header when
the body never reached the envelope.

The **code** is what views branch on. `connection_read_only` and
`provider_forbidden` render the specific remedy — which Keycloak role to grant —
rather than a generic toast, because those are the two an operator actually hits
while setting a workspace up.

## 9. Management surface

The console had no UI for workspaces or connections, which made the prototype
unusable without curl: an operator could not reach the identity views at all,
because those need a workspace with an active connection.

The smallest surface that closes that loop shipped, and stops there:

- **Workspaces** — list, create, rename, archive.
- **Connections** — list, create, edit draft, replace secret, verify, activate,
  retire.

A stored client secret is never rendered, and structurally cannot be: no
endpoint returns one. The UI shows only `configured`. On edit, a blank secret
field **omits** `client_secret`, which the PATCH contract already reads as
"unchanged" — sending `""` would replace a working credential with an empty one.

## 10. Tests

143 cases across 12 `node --test` suites, dependency-free (the DOM is stubbed in
`tests/helpers.mjs` rather than pulling in jsdom).

The ones that matter most:

| Suite | Pins |
|---|---|
| `api-errors` | both envelopes, malformed bodies, request-id preservation, no `[object Object]` |
| `wspath` | path/route construction; missing id rejected, absolute URL rejected |
| `workspace-state` | resolution precedence, invalid persisted selection, zero workspaces |
| `workspace-isolation` | the stale-response race, abort on switch, A → B → A, refetch on switch |
| `workspace-mutations` | switch closes dialogs; cross-workspace mutation refused and **not retargeted** |
| `workspace-management` | the secret is never sent blank and never stored; degraded states offer a way forward |
| `workspace-selector` | selected workspace rendered, archived not selectable, zero-workspace state |

## 11. Proving it with two real realms

Unit tests cannot prove the central claim — *two realms, one console, no
leakage* — because it needs two realms with recognisably different data.

[`scripts/two-realm-demo.sh`](../scripts/two-realm-demo.sh) builds them: three
disposable realms in a running Keycloak, an operator, two workspaces with active
connections, and a third workspace whose connection uses a deliberately
read-only administrative client. It then drives the **same `/v1` endpoints the
console calls** and asserts 27 properties: reads isolated, a role created in A
absent from B, a user created in B absent from A, `read_only` graded and its
write refused with `connection_read_only`, an archived workspace refusing reads,
and the connection listing carrying `has_client_secret` but no secret.

```sh
KEYCLOAK_VERIFY_URL=http://localhost:8081 \
DB_URL='postgres://…@localhost:5432/throwaway?sslmode=disable' \
  ./scripts/two-realm-demo.sh
```

Point `DB_URL` at a throwaway database: the script creates workspaces with fixed
slugs and does not drop anything for you.

### Why not a browser driver

The repository has no browser automation, and adding one would introduce its
**first runtime dependency** — into a console that is 7 500 lines of
dependency-free vanilla JS with a hand-written 80-line DOM stub in its tests.
That is a larger and more permanent decision than this slice should make on its
own.

What ships instead is the pair the brief asks for when that trade does not pay:
the API-level acceptance run above, plus automated frontend tests covering the
logic a browser driver would exercise — the stale-response race, the
cross-workspace mutation refusal, selector behaviour and every degraded state.
What remains genuinely unproven by automation is **rendering**: that the right
pixels appear. §12 is the deterministic walkthrough for that.

## 12. Manual walkthrough

Run the demo script with `--keep` and it leaves everything up:

```sh
DB_URL='postgres://…/throwaway?sslmode=disable' ./scripts/two-realm-demo.sh --keep
```

Then open **http://localhost:58080/admin** and sign in as `operator` /
`operator-pw`.

| # | Do | Expect |
|--:|---|---|
| 1 | Land on Overview | The topbar selector shows a workspace and a **Healthy** pill |
| 2 | Select **Alpha**, open Users | `alice-alpha`, `bob-alpha` — and nobody else |
| 3 | Open Roles | `alpha-only-role` is present |
| 4 | Create a role `demo-in-alpha` | Toast confirms; the row appears |
| 5 | Switch the selector to **Bravo** | You stay on Roles; the URL's `ws_` segment changes; `alpha-only-role` and `demo-in-alpha` are **gone** |
| 6 | Open Users | `carol-bravo`, `dave-bravo`, `erin-bravo` — no alpha users |
| 7 | Create a role `demo-in-bravo` | Appears in Bravo |
| 8 | Switch back to **Alpha** | `demo-in-alpha` present, `demo-in-bravo` absent |
| 9 | Open a user in Alpha, click Delete, and — with the dialog open — switch to Bravo | The dialog **closes on the switch**; nothing is deleted in either realm |
| 10 | Reload the page (F5) | Same workspace, same page — the URL carries it |
| 11 | Copy the URL into a second tab, switch that tab to Bravo | Two tabs, two realms, neither disturbs the other |
| 12 | Go to **Workspaces → Alpha read-only → connections** | The connection shows **read-only** and `can write: no` |
| 13 | Un-archive is not offered; open that workspace's Users anyway via its URL | An "archived" state with a way back, not an opaque error |
| 14 | Open **Email / SMTP** | A **legacy** banner saying the page is not workspace-scoped; switching workspace does not change it |

Step 9 is the one worth doing slowly: it is the defect class Phase 9 exists for,
and the only one whose absence is invisible when everything works.

---

## See also

- [WORKSPACE_IDENTITY_API.md](WORKSPACE_IDENTITY_API.md) — the API the console now consumes
- [CONNECTIONS.md](CONNECTIONS.md) — what a workspace routes through
- [WORKSPACES.md](WORKSPACES.md) — the workspace domain
- [TECH_DEBT.md](TECH_DEBT.md) — TD-024
