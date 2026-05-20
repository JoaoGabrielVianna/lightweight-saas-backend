# Admin UI Bug Catalog

**Scope:** `web/admin/*`. Static hostile-input analysis — no implementation changes made.

**Methodology:** Read every view, component, and lib file with the following adversarial scenarios in mind:

- double click on every action button
- fast click (3+ clicks before the first promise resolves)
- empty / whitespace-only inputs in forms
- invalid payloads in API Explorer / modals
- network interruption (transport failure, status 0)
- expired bearer token mid-session
- tab switching (browser throttles timers and pauses some I/O)

Where ambiguous, I traced the code path top-down (button onclick → handler → API → state update → re-render) to find the failure mode.

**Severity legend:**

- **P0** — security-relevant or causes data loss / corrupted user state
- **P1** — user-visible misbehavior with no manual recovery (e.g. duplicate emails sent)
- **P2** — confusing UX, recoverable with a page reload
- **P3** — cosmetic / inefficient but not user-blocking

---

## P0 — critical

### UI-001 · Double-click on "Login with Keycloak" corrupts PKCE state

**Where:** [web/admin/static/js/views/playground.js:69-71](web/admin/static/js/views/playground.js#L69-L71); [web/admin/static/js/lib/auth.js:41-60](web/admin/static/js/lib/auth.js#L41-L60).

**Trigger:** Click the "Login with Keycloak" button twice fast (≤ ~200ms apart) on the Playground view while not signed in.

**Path:**

1. `onclick: () => startLogin().catch(e => toastBad(e.message))` has no busy guard.
2. Click A: `startLogin()` generates verifier-A + state-A, writes both to sessionStorage, schedules `window.location.assign(URL with challenge-A & state-A)`.
3. Click B (before A's assign settles): generates verifier-B + state-B, **overwrites** verifier-A and state-A in sessionStorage, schedules `window.location.assign(URL with challenge-B & state-B)`.
4. Browser serializes the two navigations — last write wins. Whichever URL the browser actually loads has either challenge-A or challenge-B, but the sessionStorage always has verifier/state from click B.
5. After Keycloak redirect, `completeLogin(code, state)` either:
   - throws `"OAuth state mismatch"` (returnedState ≠ expected verifier-B's state-B), OR
   - the token endpoint rejects with `invalid_grant: PKCE verification failed` if challenges don't match.

**Observed:** Bad-toast appears with `"token exchange: invalid_grant"` or `"OAuth state mismatch"`. URL is silently rewritten back to redirectUri. User has no in-place "click again" affordance — they have to re-click Login, which works the second time.

**Expected:** Login button disabled (or guarded) for the duration of the redirect prep. Only one PKCE handshake in flight at a time.

**Suggested fix:** Track a `loginInProgress` flag in module scope, disable the button while true, reset on PKCE callback completion.

---

### UI-002 · Late async resolve in Overview view overwrites the next view

**Where:** [web/admin/static/js/views/overview.js:44-67](web/admin/static/js/views/overview.js#L44-L67).

**Trigger:** Navigate to /overview, then immediately (before /health and /admin/users finish) click /users (or any other sidebar item).

**Path:**

1. `overviewView` does an initial `mount(container, …)` with placeholder cards. Synchronous.
2. Awaits `/health`, OIDC discovery, and `/admin/users?max=100` — collectively ~100–600ms depending on network.
3. User navigates away. Router calls e.g. `usersView({ container: #main })`. `usersView` mounts its own content into `#main`.
4. Overview's awaits resolve. Line 44–67 calls `mount(container, …)` **without** scoping to a sub-container ID. This wipes `#main`'s contents and replaces them with the Overview's re-rendered cards.

**Observed:** The user is on /users, sees the users table briefly, then it's replaced by the Overview's stat cards. URL still says `#/users`. Visual state is now lying about the route.

**Expected:** Either cancel the pending fetches when navigating away, or scope the second mount to a sub-container ID (the pattern used by users.js, roles.js, sessions.js, invitations.js, user-detail.js — all use `container.querySelector("#xxx-content")` which safely returns null after a route change).

**Suggested fix:** Wrap the second render in a sub-container, e.g. `mount(container.querySelector("#ov-grid"), …)`, mirroring how every other paginated view does it. Or carry an AbortController that the router invalidates on route change.

**Note:** Overview is the *only* view in the codebase doing `mount(container, …)` after an `await`. Every other view scopes its post-await mount to an ID inside the container, which is null-safe. This is a single point fix.

---

## P1 — high impact

### UI-003 · Double-click on "Send reset email" dispatches multiple Keycloak emails

**Where:** [web/admin/static/js/views/user-detail.js:48](web/admin/static/js/views/user-detail.js#L48), handler at [web/admin/static/js/views/user-detail.js:214-223](web/admin/static/js/views/user-detail.js#L214-L223).

**Trigger:** On /users/:id, double-click the "Send reset email" button.

**Path:** The button has no busy state — it fires `apiTry("/admin/users/:id/reset-password", { method: "POST" })` per click. Each successful call dispatches a fresh action email via Keycloak's execute-actions-email endpoint. Keycloak does not deduplicate.

**Observed:** Two (or N) reset emails arrive in the user's inbox.

**Expected:** One click → one email. Re-clicks should either be ignored while a previous call is in flight or require waiting.

**Suggested fix:** Either route through a confirmation modal (same as Logout all / Delete) or add a per-button busy ref pattern with the button visually disabled during the in-flight call.

---

### UI-004 · Double-click on "resend" invitation dispatches duplicate emails

**Where:** [web/admin/static/js/views/invitations.js:57](web/admin/static/js/views/invitations.js#L57), handler at [web/admin/static/js/views/invitations.js:181-193](web/admin/static/js/views/invitations.js#L181-L193).

**Trigger:** Same shape as UI-003 — click "resend" twice on an invitation row.

**Path:** Direct row-action `onclick` → `resendInvitation(row, container)` → `apiTry(…/resend, POST)` per click. No busy guard.

**Note:** The docstring on `resendInvitation` says "No confirmation modal — resend is idempotent (Keycloak just sends a second copy of the same action email). Toast feedback only." This explicitly accepts the duplicate-email outcome by design. But that comment was written *before* the ResendInvitation reliability work (see [docs/INVITATION_RELIABILITY_v0.2.md](docs/INVITATION_RELIABILITY_v0.2.md)) — the backend now returns 409 on accepted/revoked invitations, but the UI still allows runaway clicks against pending ones.

**Observed:** N clicks → N emails.

**Expected:** A click→reply→toast cycle should be atomic. Subsequent clicks during the call should no-op.

**Suggested fix:** Disable the row button during the call (mutate the DOM ref) and re-enable on completion. Or move to per-row state with a re-render.

---

### UI-005 · Double-click on "Refresh token" hits Keycloak's refresh-token rotation

**Where:** [web/admin/static/js/views/playground.js:76-79](web/admin/static/js/views/playground.js#L76-L79); [web/admin/static/js/lib/auth.js:90-107](web/admin/static/js/lib/auth.js#L90-L107).

**Trigger:** With refresh-token rotation enabled in Keycloak (default in recent versions), double-click "Refresh token".

**Path:**

1. Click A: `refreshToken()` POSTs `grant_type=refresh_token` with the current refresh token. Keycloak issues a new pair and **invalidates the old refresh token**.
2. Click B: starts a second refresh call before click A's promise resolves. The old refresh token is now invalid → `invalid_grant`.
3. `refreshToken()` throws. The caller toasts the error.

**Observed:** First refresh succeeds (you can see "token refreshed"); second toast says `refresh 400: invalid_grant`. The user may not realize the FIRST refresh worked and assume the session is broken.

**Expected:** Only one refresh in flight at a time.

**Suggested fix:** Same pattern — disable the button during in-flight call, or wrap refreshToken() in a module-level promise-cache so concurrent calls return the same promise.

---

### UI-006 · Double-click on API Explorer "Send" — result panel shows whichever finishes last

**Where:** [web/admin/static/js/views/apiexplorer.js:97](web/admin/static/js/views/apiexplorer.js#L97); execute() at [web/admin/static/js/views/apiexplorer.js:110-149](web/admin/static/js/views/apiexplorer.js#L110-L149).

**Trigger:** Pick an endpoint, click Send. Before the spinner disappears, edit the path or body and click Send again.

**Path:** `execute()` writes the spinner via `mount(out, …)`, then awaits `apiTry()`, then writes the final result via `mount(out, …)`. No request id, no abort. The second call's result eventually overwrites the first only if it finishes later — but if the first finishes later (e.g. it was a heavier request), the user sees the FIRST request's response despite having edited the form.

**Observed:** Response panel can show stale data not corresponding to the most recent click. Especially confusing when the user is debugging timing issues against the real backend.

**Expected:** Latest click wins. Earlier responses should be ignored.

**Suggested fix:** Maintain a `requestSeq` counter that increments per Send; capture seq before await, and only mount the result if seq === current counter.

---

## P2 — medium impact

### UI-007 · No client-side input validation in user-edit modal

**Where:** [web/admin/static/js/views/user-detail.js:122-167](web/admin/static/js/views/user-detail.js#L122-L167).

**Trigger:** Open Edit modal, clear the email field, click Save.

**Path:**

1. The "Save changes" handler computes a diff and includes `body.email = ""` because `"" !== (u.email || "")` when the current email is non-empty.
2. Server returns 400 (`identity: bad request: email is malformed`).
3. `formatError(400, …)` produces the generic message *"Invalid input. Check email format and field values."* — accurate but doesn't pinpoint *which* field is wrong.

The `<input type="email">` would normally trigger browser validation, but the button is not inside a `<form>` and click goes through the modal's framework — no form-submit lifecycle, so the browser's `:invalid` UI never fires.

**Observed:** Generic error toast, no field-level highlight, modal stays open without indicating which field is bad.

**Expected:** Field-level validation feedback (red border + helper text) before the API roundtrip.

**Suggested fix:** Validate email/required-fields in the onClick before calling patchUser. Add red-border CSS class on offending input, scroll into view, focus.

---

### UI-008 · Empty role name on create returns generic 400 with no field hint

**Where:** [web/admin/static/js/views/roles.js:85-131](web/admin/static/js/views/roles.js#L85-L131).

**Trigger:** Click + New role, leave name blank, click Create role.

**Path:** Sends `{name: "", description: "…"}`. Service returns 400 with `"role name is required"`. UI toasts the generic format-rule message.

**Observed:** Same as UI-007 — generic toast, no field highlight, modal stays open. User has to read the toast to understand which field.

**Suggested fix:** Reject empty name client-side and highlight the field. Add similar checks for the reserved names (admin/user/offline_access/...) since the rejection rule is well-known and predictable.

---

### UI-009 · Token expires mid-session → state drift (sidebar says "signed in", API says 401)

**Where:** [web/admin/static/js/lib/api.js:32-41](web/admin/static/js/lib/api.js#L32-L41), [web/admin/static/js/components/sidebar.js:66-87](web/admin/static/js/components/sidebar.js#L66-L87), [web/admin/static/js/lib/auth.js:133-145](web/admin/static/js/lib/auth.js#L133-L145).

**Trigger:** Log in. Wait for the access token to expire (default Keycloak token lifetime ~5 min). Click any data view.

**Path:**

1. `apiTry()` sends the bearer header; Keycloak returns 401.
2. View renders the "Sign in required" empty state.
3. But `state.identity` is NOT cleared. The sidebar `renderUserCard()` reads `state.identity` and shows the user's email + roles + "↪ logout" button.
4. Sub views show "Sign in required"; sidebar shows the user is signed in. Two views of the world, in the same DOM.

**Expected:** Either (a) refresh the token on 401 automatically, or (b) on the first 401, clear `state.identity` so the sidebar reflects the same reality the views show.

**Suggested fix:** Centralize the 401 path in `api.js`. On 401, call `refreshDebug()` (which will return null and clear identity state). Optionally try a single refresh-token swap first.

---

### UI-010 · Refresh-token failure leaves stale token in sessionStorage

**Where:** [web/admin/static/js/lib/auth.js:90-107](web/admin/static/js/lib/auth.js#L90-L107).

**Trigger:** Let both access and refresh tokens expire. Click "Refresh token" in Playground.

**Path:** `refreshToken()` POSTs, gets 400 `invalid_grant`, throws. The Playground catches and toasts. Neither the access token nor the refresh token in sessionStorage is removed. Subsequent calls keep failing with 401. `isAuthenticated()` still returns true because the access-token key is still populated.

**Expected:** A failed refresh that returns `invalid_grant` should clear both tokens and `state.identity`, putting the UI back to the signed-out empty state.

**Suggested fix:** In the `refreshToken()` catch path (or right before the throw), call something like `clearTokens()` (same logic settings.js's "Clear stored tokens" button uses) when the failure looks unrecoverable (400 `invalid_grant`, 401, etc.).

---

### UI-011 · Modal Esc-during-action: action proceeds; success/failure toast appears with no modal

**Where:** [web/admin/static/js/components/modal.js:51-53](web/admin/static/js/components/modal.js#L51-L53).

**Trigger:** Open a destructive modal (e.g. Delete user). Click the primary action. While the network call is in flight, press Esc.

**Path:**

1. Modal's primary `onClick` returns false (modal stays open), but the network call is already in flight.
2. Esc triggers `close()`, which mounts an empty modal-root and removes the keydown listener.
3. The in-flight `apiTry().then(…)` eventually resolves. The handler calls `if (close) close();` — but the modal is already gone — and either `toastOk(…)` (delete succeeded behind the user's back) or `toastBad(…)`.

**Observed:** A "User deleted" or "Delete failed" toast appears with no visible modal. The user thought they cancelled.

**Expected:** Either (a) Esc is suppressed while an action is in flight, or (b) the action is cancelled when the modal closes.

**Suggested fix:** Modal should accept a `cancellable: false` (or `busy: true` flag) configurable per action — when set, Esc and backdrop click are ignored. Wire `busy = true` to also set this flag.

---

### UI-012 · Stacked modals collide: opening modal B wipes modal A's DOM but leaves A's keydown handler

**Where:** [web/admin/static/js/components/modal.js:11-58](web/admin/static/js/components/modal.js#L11-L58).

**Trigger:** Edge case — open one modal, while it's open, trigger a flow that calls `openModal()` again (e.g. a chained "this user has dependencies, confirm cascade?" prompt — not in v0.2 but plausible).

**Path:** Second `openModal()` calls `mount(root, backdrop)` which clears the root DOM. The first modal's keydown handler is still bound to `document` (only the first modal's close() removes it, and that close() hasn't fired). Pressing Esc now triggers the FIRST modal's close, which mounts empty root again — wiping the second modal.

**Observed:** Esc on the second modal closes both modals at once. Backdrop click on the second modal correctly closes only the second.

**Expected:** Modal stack should be FIFO or LIFO consistent — Esc closes the topmost only.

**Suggested fix:** Modal manager should maintain a stack and only the topmost modal's keydown handler should be active. Alternatively, forbid stacking and assert there's no open modal when openModal() is called.

---

### UI-013 · Module-level state leaks across navigations

**Where:** [web/admin/static/js/views/apiexplorer.js:46-48](web/admin/static/js/views/apiexplorer.js#L46-L48); [web/admin/static/js/views/users.js:12](web/admin/static/js/views/users.js#L12); [web/admin/static/js/views/playground.js:14](web/admin/static/js/views/playground.js#L14).

**Trigger:** API Explorer: select a non-default endpoint, edit the path, navigate away to /users, then back to /api-explorer. The edited path is preserved (because `pathInput` is a stale DOM node reference). Same for users.js's `pageState`.

**Path:** These globals survive route changes because views are functions, not classes; each re-call re-uses the module-level state.

**Observed:** Some state survives navigation (filters, last-selected endpoint); some doesn't (modals, in-flight calls). Inconsistent behavior between views.

**Expected:** Either deliberate (document it) or transient (clear on view init). The mixing is the confusion.

**Suggested fix:** Decide per-view whether state should persist. For API Explorer the persistence is probably useful; for users.js the URL already encodes search/first/max so module-level state is redundant.

---

## P3 — low impact

### UI-014 · Refresh buttons have no busy guards (parallel-fetch flicker)

**Where:** Most list views — [users.js:87](web/admin/static/js/views/users.js#L87), [roles.js:35](web/admin/static/js/views/roles.js#L35), [sessions.js:45](web/admin/static/js/views/sessions.js#L45), [invitations.js:43](web/admin/static/js/views/invitations.js#L43).

**Trigger:** Click the "↻ refresh" button in the table toolbar three times in a row.

**Path:** Each click re-runs the view function which does its own fetch. With pagination on the backend, each fetch can take 100–500ms. Race between responses: whichever resolves last wins. UI may flicker.

**Suggested fix:** Same pattern — set a busy flag, disable the button until resolved.

---

### UI-015 · Pagination "next" enabled even when there are no more rows

**Where:** [web/admin/static/js/components/table.js:79-99](web/admin/static/js/components/table.js#L79-L99).

**Trigger:** A users page that has exactly `max` rows (e.g. realm has 20 users, page size is 20).

**Path:** The pager uses `disabled: have < max`. When `have === max`, "next" is enabled even though there's nothing on the next page. Clicking it fetches an empty page → empty-state UI flashes. User must click "← prev" to get back.

**Expected:** Either fetch total count from server (not currently returned by [internal/identity/handler.go ListUsers response](../internal/identity/handler.go) — outside this audit's scope) or detect short-page on render and disable "next" then.

**Suggested fix:** If `rows.length === max && rows.length > 0`, leave "next" enabled (we can't know). On empty next page, render "no more results, back to start" with a `← back` link in the empty state.

---

### UI-016 · Token countdown drifts when tab is backgrounded

**Where:** [web/admin/static/js/views/playground.js:192-215](web/admin/static/js/views/playground.js#L192-L215).

**Trigger:** Go to Playground while signed in. Switch to another tab. Wait 30+ seconds. Return.

**Path:** `setInterval(tick, 1000)` is throttled by browsers to ~1Hz minimum and may be paused entirely on backgrounded tabs. When the tab is foregrounded, the countdown jumps to the correct value at the next tick (because `tick()` always reads `Date.now()`), but in between the displayed value is stale.

**Observed:** Minor flicker on tab return.

**Expected:** Either pause/resume on visibilitychange or just live with the drift (it's cosmetic).

**Suggested fix:** Add `document.addEventListener("visibilitychange", () => { if (!document.hidden) tick(); })` to immediately refresh on focus return. Out-of-scope for v0.2 polish.

---

### UI-017 · Toast stack unbounded — spam-clicking accumulates many toasts

**Where:** [web/admin/static/js/components/toast.js:8-25](web/admin/static/js/components/toast.js#L8-L25).

**Trigger:** Spam-click "Send reset email" (or any button that toasts on completion) 15+ times. Each will eventually produce a toast that lives 4.5s.

**Observed:** Toast stack grows long, screen real estate consumed. The user has no way to dismiss except waiting or clicking each individually.

**Expected:** Cap at e.g. 5 visible toasts, drop oldest when over cap.

**Suggested fix:** In `toast()`, check `stack.children.length` and remove the oldest before appending.

---

### UI-018 · `table.js` claims "sortable columns" but doesn't sort

**Where:** [web/admin/static/js/components/table.js:1-15](web/admin/static/js/components/table.js#L1-L15).

**Trigger:** Read the header comment, click a column header.

**Observed:** Header comment says "sortable columns" but `<th>` elements have no click handlers and there's no sort plumbing. Clicking does nothing.

**Expected:** Either remove the misleading comment or implement sort.

**Suggested fix:** Drop the comment line OR add a per-column `sortable: true` flag and an onclick that toggles sort state.

---

### UI-019 · "admin:search" event listener never removed

**Where:** [web/admin/static/js/views/users.js:24-33](web/admin/static/js/views/users.js#L24-L33).

**Trigger:** Visit /users once. Navigate away. Search in the topbar from /roles.

**Path:** The listener is registered with `searchHandlerInstalled` flag — once-only. The inner guard `if (location.hash.startsWith("#/users") && !…/users/…)` keeps the handler inert on other pages. So it's leaked-but-inert. Functional, but other views (sessions, roles, invitations) ignore the topbar search entirely without affordance — the input pretends to be functional everywhere.

**Observed:** Topbar search has no effect on roles / sessions / invitations / overview / settings / playground / api-explorer / audit-logs. Only /users responds.

**Expected:** Either disable the topbar search on those views or wire each view to handle it.

**Suggested fix:** Topbar exposes "search: enabled|disabled" per route. Views opt in via a route-level flag.

---

### UI-020 · Fetch on view enter is never cancelled when navigating away

**Where:** Pattern-wide — no `AbortController` is created anywhere in `web/admin/`.

**Trigger:** Navigate to /sessions (which fans out across all clients in the realm — can take a few seconds at scale). Immediately navigate away.

**Path:** The fetch continues to completion. The response is parsed. The `container.querySelector("#sess-content")` returns null (because the new view replaced it) so the render is skipped, but the work was still done.

**Observed:** Wasted bandwidth and CPU. Not user-visible except as a slight delay on the new view (network contention).

**Expected:** Outbound requests cancelled on route change.

**Suggested fix:** Router maintains an AbortController per route. Replace the controller on each `handleChange`. `apiTry` accepts a `signal` option.

---

## Caveats

- **Static-analysis only.** I did not execute the UI. P0/P1 entries trace concrete code paths and are high-confidence. P2/P3 entries describe behavior that would manifest under the stated trigger but I have not visually confirmed.
- **No race-condition tooling.** Browsers don't expose perfect concurrent execution semantics; some races are timing-dependent and hard to reproduce manually. Recommend playwright with deliberate `await page.click()` calls (no waits) for the double-click bugs.
- **Backend behavior assumed from server contract.** Keycloak's refresh-token rotation, action-email idempotency, and PKCE state behavior referenced as documented; if your Keycloak realm is configured differently (rotation off, e.g.) some P0/P1 bugs may not reproduce.
- **Scope-bound.** Forbidden directories (`internal/*`, `auth/*`, `identity/*`, `bootstrap/*`, `server/*`, `scripts/*`) were not inspected. Backend changes that would address some state-drift issues (e.g. server-driven token refresh hint headers) are out of scope here.

## Suggested triage order

| Bug | Severity | Effort | Priority |
|---|---|---|---|
| UI-002 (overview late-mount race) | P0 | 5 lines | Fix first — easy and dangerous |
| UI-001 (login double-click PKCE) | P0 | ~10 lines | Fix immediately |
| UI-003, UI-004 (duplicate email dispatch) | P1 | ~5 lines each | Reuse existing busy pattern; high-confidence wins |
| UI-005 (refresh token double-click) | P1 | ~10 lines | Same pattern |
| UI-006 (API Explorer stale result) | P1 | ~5 lines (sequence number) | Easy |
| UI-009, UI-010 (token state drift) | P2 | ~20 lines (centralize 401 handling) | Worth a small refactor |
| UI-011 (Esc cancels modal but not action) | P2 | ~5 lines (modal busy flag) | Bundle with UI-007 fixes |
| UI-007, UI-008 (client-side validation) | P2 | ~15 lines per modal | Polish |
| UI-013–UI-020 | P3 | varies | Backlog |
