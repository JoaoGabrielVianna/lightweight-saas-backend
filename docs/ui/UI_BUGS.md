# Admin UI Bug Catalog

**Scope:** `web/admin/*`. Static hostile-input analysis — no implementation changes made.

**Methodology:** read every view/component/lib with adversarial scenarios in mind — double-click on action buttons, fast click (3+ clicks before first promise resolves), empty/whitespace inputs, invalid payloads in modals/API Explorer, network interruption, expired bearer mid-session, tab switching (timer throttling). Traced ambiguous cases top-down from `onclick` → handler → API → state update → re-render.

**Severity:** **P0** security/data-loss · **P1** user-visible misbehavior with no manual recovery · **P2** confusing UX, recoverable with reload · **P3** cosmetic / inefficient.

---

## P0 — critical

### UI-001 · Double-click on "Login with Keycloak" corrupts PKCE state

**Where:** [playground.js:69-71](../../web/admin/static/js/views/playground.js#L69-L71); [lib/auth.js:41-60](../../web/admin/static/js/lib/auth.js#L41-L60).

**Trigger:** Click "Login with Keycloak" twice fast (≤ ~200ms) on Playground while signed out.

**Path:** `onclick: () => startLogin().catch(...)` has no busy guard.
1. Click A: `startLogin()` generates verifier-A + state-A, writes both to sessionStorage, schedules `window.location.assign(URL with challenge-A & state-A)`.
2. Click B (before A's assign settles): generates verifier-B + state-B, **overwrites** verifier-A + state-A in sessionStorage, schedules a new assign with challenge-B & state-B.
3. Browser serializes — last write wins. Whichever URL loads has challenge-A or challenge-B, but sessionStorage always has verifier/state from click B.
4. After KC redirect, `completeLogin(code, state)` either throws `"OAuth state mismatch"` (returnedState ≠ expected state-B), OR token endpoint rejects with `invalid_grant: PKCE verification failed`.

**Observed:** Bad-toast `"token exchange: invalid_grant"` or `"OAuth state mismatch"`. URL silently rewritten back to redirectUri. User must re-click Login (works second time).

**Suggested fix:** `loginInProgress` flag in module scope; disable button while true; reset on PKCE callback completion.

---

### UI-002 · Late async resolve in Overview view overwrites the next view

**Where:** [overview.js:44-67](../../web/admin/static/js/views/overview.js#L44-L67).

**Trigger:** Navigate to /overview, then immediately (before `/health` + `/admin/users` finish) click /users.

**Path:**
1. `overviewView` does initial `mount(container, ...)` with placeholder cards.
2. Awaits `/health`, OIDC discovery, `/admin/users?max=100` — ~100–600ms.
3. User navigates away. Router calls `usersView({container: #main})` which mounts into `#main`.
4. Overview's awaits resolve. Line 44–67 calls `mount(container, ...)` **without scoping to a sub-container ID** — wipes `#main` and replaces with Overview's re-rendered cards.

**Observed:** User on /users sees the users table briefly, then it's replaced by Overview's stat cards. URL still says `#/users`. Visual state lies about the route.

**Fix:** Wrap the second render in a sub-container (`mount(container.querySelector("#ov-grid"), ...)`) — mirrors how `users.js`, `roles.js`, `sessions.js`, `invitations.js`, `user-detail.js` all scope post-await mounts to an ID inside the container (null-safe after route change). Overview is the **only** view doing the un-scoped pattern; single point fix.

---

## P1 — high impact

### UI-003 · Double-click on "Send reset email" dispatches multiple Keycloak emails

**Where:** [user-detail.js:48](../../web/admin/static/js/views/user-detail.js#L48), handler [214-223](../../web/admin/static/js/views/user-detail.js#L214-L223).

**Trigger:** On `/users/:id`, double-click "Send reset email".

**Path:** Button has no busy state. Each click fires `apiTry("/admin/users/:id/reset-password", {method:"POST"})`. Each successful call dispatches a fresh action email via Keycloak's execute-actions-email endpoint. Keycloak does not deduplicate.

**Observed:** N clicks → N emails.

**Fix:** Confirmation modal (like Logout all / Delete) OR per-button busy ref pattern with visible disabled state during in-flight call.

---

### UI-004 · Double-click on "resend" invitation dispatches duplicate emails

**Where:** [invitations.js:57](../../web/admin/static/js/views/invitations.js#L57), handler [181-193](../../web/admin/static/js/views/invitations.js#L181-L193).

**Trigger:** Same shape as UI-003 — click "resend" twice on an invitation row.

**Note:** Docstring on `resendInvitation` says "No confirmation modal — resend is idempotent (Keycloak just sends a second copy of the same action email). Toast feedback only." That comment was written *before* the ResendInvitation reliability work (see [../validation/INVITATION_RELIABILITY_v0.2.md](../validation/INVITATION_RELIABILITY_v0.2.md)) — backend now returns 409 on accepted/revoked invitations, but UI still allows runaway clicks against pending ones.

**Observed:** N clicks → N emails.

**Fix:** Disable row button during call (mutate DOM ref), re-enable on completion.

---

### UI-005 · Double-click on "Refresh token" hits Keycloak's refresh-token rotation

**Where:** [playground.js:76-79](../../web/admin/static/js/views/playground.js#L76-L79); [lib/auth.js:90-107](../../web/admin/static/js/lib/auth.js#L90-L107).

**Trigger:** With refresh-token rotation enabled in KC (default in recent versions), double-click "Refresh token".

**Path:** Click A POSTs `grant_type=refresh_token`; KC issues new pair and invalidates the old refresh token. Click B starts a second refresh before A resolves — old refresh token now invalid → `invalid_grant`. Second toast says `refresh 400: invalid_grant`; user may assume the session is broken despite the FIRST refresh having succeeded.

**Fix:** Disable button during in-flight call, OR wrap `refreshToken()` in a module-level promise-cache so concurrent calls return the same promise.

---

### UI-006 · Double-click on API Explorer "Send" — result panel shows whichever finishes last

**Where:** [apiexplorer.js:97](../../web/admin/static/js/views/apiexplorer.js#L97); `execute()` [110-149](../../web/admin/static/js/views/apiexplorer.js#L110-L149).

**Trigger:** Pick endpoint, click Send. Before the spinner disappears, edit path/body and click Send again.

**Path:** `execute()` writes spinner, awaits `apiTry()`, writes result via `mount(out, ...)`. No request id, no abort. Second call's result overwrites first only if it finishes later — if the first finishes later (heavier request), user sees FIRST request's response despite having edited the form.

**Fix:** `requestSeq` counter incremented per Send; capture seq before await, only mount the result if seq === current counter.

---

## P2 — medium impact

### UI-007 · No client-side input validation in user-edit modal

**Where:** [user-detail.js:122-167](../../web/admin/static/js/views/user-detail.js#L122-L167).

**Trigger:** Open Edit modal, clear email, click Save.

**Path:** "Save changes" handler diffs and includes `body.email = ""` because `"" !== (u.email || "")` when current email is non-empty. Server returns 400 `identity: bad request: email is malformed`. `formatError(400, ...)` produces generic *"Invalid input. Check email format and field values."* — accurate but doesn't pinpoint which field. The `<input type="email">` would normally trigger browser validation, but the button isn't inside a `<form>` — click goes through modal's framework; no form-submit lifecycle, no `:invalid` UI.

**Fix:** Validate email/required fields client-side before `patchUser`. Red-border class on offending input, scroll into view, focus.

---

### UI-008 · Empty role name on create returns generic 400 with no field hint

**Where:** [roles.js:85-131](../../web/admin/static/js/views/roles.js#L85-L131).

**Trigger:** Click + New role, leave name blank, click Create role.

**Path:** Sends `{name:"", description:"..."}`. Service returns 400 `"role name is required"`. UI toasts generic format-rule message. Same UX as UI-007.

**Fix:** Reject empty name client-side, highlight field. Add checks for reserved names (admin/user/offline_access/…) — well-known rejection rule.

---

### UI-009 · Token expires mid-session → state drift (sidebar says "signed in", API says 401)

**Where:** [lib/api.js:32-41](../../web/admin/static/js/lib/api.js#L32-L41), [sidebar.js:66-87](../../web/admin/static/js/components/sidebar.js#L66-L87), [lib/auth.js:133-145](../../web/admin/static/js/lib/auth.js#L133-L145).

**Trigger:** Log in. Wait for access token to expire (default ~5 min). Click any data view.

**Path:** `apiTry()` sends bearer; KC returns 401; view renders "Sign in required" empty state. But `state.identity` is NOT cleared — sidebar `renderUserCard()` reads `state.identity` and still shows email + roles + logout button. Two views of the world in the same DOM.

**Fix:** Centralize 401 path in `api.js`. On 401, call `refreshDebug()` (returns null, clears identity state). Optionally try a single refresh-token swap first.

---

### UI-010 · Refresh-token failure leaves stale token in sessionStorage

**Where:** [lib/auth.js:90-107](../../web/admin/static/js/lib/auth.js#L90-L107).

**Trigger:** Let both access + refresh tokens expire. Click "Refresh token" in Playground.

**Path:** `refreshToken()` POSTs, gets 400 `invalid_grant`, throws. Playground catches + toasts. Neither token in sessionStorage is removed. Subsequent calls keep failing with 401. `isAuthenticated()` still returns true because the access-token key is still populated.

**Fix:** In `refreshToken()` catch path (or before throw), call `clearTokens()` (same logic settings.js's "Clear stored tokens" button uses) when failure looks unrecoverable (400 `invalid_grant`, 401, etc.).

---

### UI-011 · Modal Esc-during-action: action proceeds; success/failure toast appears with no modal

**Where:** [components/modal.js:51-53](../../web/admin/static/js/components/modal.js#L51-L53).

**Trigger:** Open a destructive modal (e.g. Delete user). Click primary action. While network call is in flight, press Esc.

**Path:** Modal's primary `onClick` returns false (modal stays open), but the network call is already in flight. Esc triggers `close()`, mounts empty modal-root, removes keydown listener. The in-flight `apiTry().then(...)` eventually resolves — handler calls `if (close) close();` (modal already gone) and either `toastOk(...)` (delete succeeded behind user's back) or `toastBad(...)`. User thought they cancelled.

**Fix:** Modal accepts `cancellable: false` (or `busy: true` flag) configurable per action — when set, Esc and backdrop click are ignored.

---

### UI-012 · Stacked modals collide: opening modal B wipes modal A's DOM but leaves A's keydown handler

**Where:** [components/modal.js:11-58](../../web/admin/static/js/components/modal.js#L11-L58).

**Trigger:** Edge case — open one modal, while open trigger a flow that calls `openModal()` again (e.g. cascading-confirm prompt — plausible in future, not v0.2).

**Path:** Second `openModal()` calls `mount(root, backdrop)` which clears the root DOM. First modal's keydown handler still bound to `document` (only first modal's `close()` removes it; that hasn't fired). Pressing Esc now triggers the FIRST modal's close → mounts empty root again → wipes second modal.

**Observed:** Esc on second modal closes both. Backdrop click on second correctly closes only the second.

**Fix:** Modal manager maintains a stack; only topmost modal's keydown handler is active. Or forbid stacking and assert no open modal when `openModal()` is called.

---

### UI-013 · Module-level state leaks across navigations

**Where:** [apiexplorer.js:46-48](../../web/admin/static/js/views/apiexplorer.js#L46-L48); [users.js:12](../../web/admin/static/js/views/users.js#L12); [playground.js:14](../../web/admin/static/js/views/playground.js#L14).

**Trigger:** API Explorer: pick non-default endpoint, edit path, navigate to /users, navigate back. Edited path preserved (stale DOM node ref). Same for `users.js`'s `pageState`.

**Path:** Globals survive route changes — views are functions, not classes; each re-call re-uses module-level state.

**Observed:** Some state survives (filters, last endpoint); some doesn't (modals, in-flight calls). Inconsistent.

**Fix:** Decide per-view whether state should persist. API Explorer persistence is probably useful; users.js URL already encodes search/first/max — module-level state is redundant.

---

## P3 — low impact

### UI-014 · Refresh buttons have no busy guards (parallel-fetch flicker)

**Where:** Most list views — [users.js:87](../../web/admin/static/js/views/users.js#L87), [roles.js:35](../../web/admin/static/js/views/roles.js#L35), [sessions.js:45](../../web/admin/static/js/views/sessions.js#L45), [invitations.js:43](../../web/admin/static/js/views/invitations.js#L43).

Click "↻ refresh" three times — each click re-fetches; whichever resolves last wins; UI flickers. **Fix:** busy flag, disable button until resolved.

### UI-015 · Pagination "next" enabled even when no more rows

**Where:** [components/table.js:79-99](../../web/admin/static/js/components/table.js#L79-L99). Pager uses `disabled: have < max`. When `have === max`, "next" is enabled even if next page is empty. Click → empty-state flash → user clicks "← prev" to recover. **Fix:** if `rows.length === max && rows.length > 0` leave "next" enabled (can't know); on empty next page render "no more results, back to start" with `← back` link.

### UI-016 · Token countdown drifts when tab is backgrounded

**Where:** [playground.js:192-215](../../web/admin/static/js/views/playground.js#L192-L215). `setInterval(tick, 1000)` throttled by browser when tab hidden. On foreground, jumps to correct value at next tick; brief stale display. **Fix:** `document.addEventListener("visibilitychange", () => { if (!document.hidden) tick(); })`.

### UI-017 · Toast stack unbounded — spam-clicking accumulates many toasts

**Where:** [components/toast.js:8-25](../../web/admin/static/js/components/toast.js#L8-L25). 15+ spam clicks each produce a 4.5s-lived toast; stack consumes screen real estate; no manual dismiss. **Fix:** cap at 5 visible toasts, drop oldest when over cap.

### UI-018 · `table.js` claims "sortable columns" but doesn't sort

**Where:** [components/table.js:1-15](../../web/admin/static/js/components/table.js#L1-L15). Header comment says "sortable columns" but `<th>` elements have no click handlers. **Fix:** remove comment OR add per-column `sortable: true` flag + onclick toggling sort state.

### UI-019 · "admin:search" event listener never removed (topbar search inert on most views)

**Where:** [users.js:24-33](../../web/admin/static/js/views/users.js#L24-L33). Listener registered with `searchHandlerInstalled` flag (once-only). Inner guard `if (location.hash.startsWith("#/users") && !.../users/...)` keeps it inert elsewhere — leaked but inert. But other views (sessions, roles, invitations, overview, settings, playground, api-explorer, audit-logs) ignore the topbar search entirely without affordance — the input pretends to be functional everywhere. **Fix:** topbar exposes "search: enabled|disabled" per route; views opt in via route-level flag.

### UI-020 · Fetch on view enter is never cancelled when navigating away

**Where:** Pattern-wide — no `AbortController` anywhere in `web/admin/`. Navigate to /sessions (fans out across all clients — can take seconds at scale). Immediately navigate away. Fetch continues; response parsed; `container.querySelector("#sess-content")` returns null after view replacement; render skipped but work done. **Fix:** router maintains an `AbortController` per route; replaces on each `handleChange`; `apiTry` accepts a `signal` option.

---

## Caveats

- **Static-analysis only.** P0/P1 entries trace concrete code paths — high confidence. P2/P3 describe behavior under the stated trigger but not visually confirmed.
- **No race-condition tooling.** Some races are timing-dependent. Recommend Playwright with deliberate `await page.click()` calls (no waits) for double-click bugs.
- **Backend behavior assumed from contract.** KC refresh-token rotation, action-email idempotency, PKCE state behavior as documented. If your realm is configured differently (rotation off, etc.), some P0/P1 bugs may not reproduce.
- **Scope-bound.** Backend changes that would address state drift (e.g. server-driven token-refresh hint headers) are out of scope here.

## Suggested triage order

| Bug | Severity | Effort | Priority |
|---|---|---|---|
| UI-002 (overview late-mount race) | P0 | ~5 lines | Fix first — easy and dangerous |
| UI-001 (login double-click PKCE) | P0 | ~10 lines | Fix immediately |
| UI-003, UI-004 (duplicate email dispatch) | P1 | ~5 lines each | Reuse existing busy pattern; high-confidence wins |
| UI-005 (refresh token double-click) | P1 | ~10 lines | Same pattern |
| UI-006 (API Explorer stale result) | P1 | ~5 lines (seq number) | Easy |
| UI-009, UI-010 (token state drift) | P2 | ~20 lines (centralize 401) | Worth a small refactor |
| UI-011 (Esc cancels modal but not action) | P2 | ~5 lines (modal busy flag) | Bundle with UI-007 |
| UI-007, UI-008 (client-side validation) | P2 | ~15 lines per modal | Polish |
| UI-013–UI-020 | P3 | varies | Backlog |
