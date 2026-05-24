# Docs UI Performance & Firefox Freeze — Investigation Report

> **Document history**
> - **2026-05-22 (initial pass, pre-fix):** investigation-only audit per rules.
>   Identified the root cause; sketched a fix at §9. Veredict: SAFE_FOR_DAILY_USE = false.
> - **2026-05-22 (re-run, post-fix):** see [§0 Update](#0--update-2026-05-22-post-fix-state) at the top.
>   Fix from §9 has been applied and validated by automated tests (19/19 green).
>   Updated evidence: log now shows **701 750** fetches over a ~10h40m window
>   (loop continued after the initial report was written, before the fix
>   landed). Veredict re-stated.
>
> **Scope:** the new Docs UI under `/admin#/docs` after the markdown + syntax
> highlight + PT-BR + Mermaid + TOC + search + scrollspy + locale-toggle +
> docs-reorg + DOC_MAP rollouts.
>
> **Rules honoured during BOTH passes:** no backend/auth changes, no file
> moves, no commits created. The fix touches only `lib/state.js` (modified)
> and `views/docs.js` (hardening); `lib/locale.js` and `main.js` are
> untouched. Tests added under `web/admin/static/js/tests/`. None of these
> changes have been committed.

---

## 0 — Update 2026-05-22 (post-fix state)

This section was added after the §9 fix sketch was applied. It supersedes
the original TL;DR. The pre-fix investigation (§1–§9) is preserved below
as historical evidence.

### What changed in the tree

| File | Status (vs HEAD) | What changed |
|---|---|---|
| `web/admin/static/js/lib/state.js` | **Modified** | Seed `_state.locale` from localStorage at module load (kills the prev/next divergence). Snapshot subscribers via `Array.from(_subs)` before iterating (kills the Set-iterator-visits-new-entries trap). Reentrancy guard via `_dispatching` (collapses re-entrant `setState` into one observable update). |
| `web/admin/static/js/views/docs.js` | **Untracked (was uncommitted)** | Added `_renderToken`, `_tocObserver` cleanup, and `queueMicrotask` defer in the `onLocaleChange` callback — defense-in-depth on top of the state.js fixes. IO leak from `renderToc` is now cured (handle stored in `_tocObserver`, disconnected on next render). |
| `web/admin/static/js/lib/locale.js` | **Untracked, NOT modified** | The seed in state.js makes its existing `prev = getLocale()` filter correct — no edit needed. |
| `web/admin/static/js/main.js` | **NOT modified** | Seed lives in state.js's module-load, so main.js has nothing to add. |
| `web/admin/static/js/tests/` | **New** | 19 tests across `state.test.mjs` and `locale.test.mjs` pinning the iteration invariants and the regression scenario. See [tests/README.md](web/admin/static/js/tests/README.md). |

### Current docker / log state (post-fix observation window)

```
$ docker stats --no-stream
saas-api    0.00% CPU   15.16 MiB / 3.81 GiB   (healthy, no restart loop)

$ docker logs saas-api 2>&1 | grep -c "QUICKSTART.pt-BR.md"
701 750

$ docker logs saas-api 2>&1 | grep "QUICKSTART.pt-BR.md" \
    | grep -oE '[0-9]{2}:[0-9]{2}' | sort | uniq -c | sort -rn | head -3
  93 985 08:10
  72 123 08:07
  71 905 08:03
```

- Peak burst: **93 985 req/min ≈ 1 566 req/s** — the loop's natural cadence
  with no caching, no debounce, no event-loop pressure.
- Total storm: 701 750 fetches of QUICKSTART.pt-BR.md across **~10h40m**
  (2026-05-21 21:35 → 2026-05-22 08:15). The original report saw 74 655
  because it was written mid-storm; the storm continued until the user
  killed the tab.
- Other docs in the log: 1–6 hits each. **Nothing else is contended.**
- No 5xx, no panics, no restart loops in saas-api.
- Six legacy `/admin/docs/...` URLs return 404 (the docs reorg broke a few
  cross-references — see §0 follow-ups below).

### Tests

```
$ node --test web/admin/static/js/tests/
# tests 19
# pass  19
# fail  0
# duration_ms 44.666
```

The two regression tests reproduce the exact bug scenario:
- `regression: PT-BR persistido + non-locale setStates do NOT loop` (locale.test.mjs)
- `regression: callback re-registering itself does NOT cause runaway recursion` (state.test.mjs)

Both would have hung indefinitely against the pre-fix code; both complete
in < 1 ms against the patched code.

### Updated verdict

```
SAFE_FOR_DAILY_USE (post-fix) = true
                                ^^^^
                                conditional on:
                                  - manual checklist in §0 follow-ups below
                                  - the fix being committed before deploy
                                  - the 404 references in the reorg being resolved
```

The pre-fix verdict (`false`) still stands as the historical correct
assessment for code that was actually on disk at the time of the original
report.

### Post-fix follow-ups (NOT blocking deploy, but flagged)

1. **Six broken doc links from the reorg.** The access log shows persistent
   404s at:
   - `/admin/docs/QUICKSTART.md` → moved to `/admin/docs/getting-started/QUICKSTART.md`
   - `/admin/docs/KEYCLOAK_SETUP.md` → moved to `/admin/docs/getting-started/KEYCLOAK_SETUP.md`
   - `/admin/docs/bootstrap.md` → moved to `/admin/docs/architecture/bootstrap.md`
   - `/admin/docs/migrations/PHASE3_BREAKING_CHANGE.md` → moved to `/admin/docs/architecture/PHASE3_BREAKING_CHANGE.md`
   - `/admin/docs/validation/AUDIT_EVENTS.md` → moved to `/admin/docs/audit/AUDIT_EVENTS.md`
   - `/admin/docs/security/AUDIT_OPERATIONS.md` → moved to `/admin/docs/audit/AUDIT_OPERATIONS.md`

   Likely culprits: stale entries in DOC_MAP, hard-coded paths in some
   markdown files, or external bookmarks. Not a regression of the fix —
   pre-existed. Worth a sweep before commit.

2. **§4–§6 P1/P2 items below remain valid post-fix.** The IO leak in
   `renderToc` was cured by my docs.js hardening; the click-listener
   stacking on `#docs-article` was only dangerous **under the loop** and
   is now inert; the search debounce is still missing; Mermaid still
   renders all blocks unconditionally on open.

3. **`_localeUnsub` is still not cleaned up when the user navigates AWAY
   from /docs/\*.** Today this is harmless (locale toggle is only visible
   inside /docs/\*, so the subscriber never fires outside it), but if the
   toggle ever moves to the global topbar it will need a teardown hook
   on route-change. See risks audit at the end of this conversation
   transcript.

### What stayed the same (still correct)

The §4 mechanism of the loop, the §3 render-stage ballpark, the §5
Mermaid analysis, and the §7 ranking are all unchanged. Read on for the
original write-up.

---

## Veredict (original, pre-fix)

```
SAFE_FOR_DAILY_USE = false
```

**Root cause of the Firefox freeze (confirmed):**
A synchronous infinite loop inside `setState()` in
[lib/state.js:22-27](web/admin/static/js/lib/state.js#L22-L27),
triggered whenever the Docs view is mounted **with `localStorage.admin_docs_locale === "pt-BR"`** and **any subsequent
`setState` is called** (route change, theme toggle, anything). The loop fires
one `fetch("/admin/docs/.../QUICKSTART.pt-BR.md")` per iteration. Backend
served **74 655 identical requests** to that one URL during the original
observation window (logs continued to grow to 701 750 by the time the
fix landed — see §0). Firefox eventually stalls and is killed.

The smoking gun is the combination of:

1. `_state.locale` is **never initialised** in [lib/state.js:8-14](web/admin/static/js/lib/state.js#L8-L14)
   (only `config / token / identity / theme / route`).
2. [`onLocaleChange`](web/admin/static/js/lib/locale.js#L58-L67) initialises
   `prev = getLocale()` **from localStorage** and then compares against
   `next = state.locale || DEFAULT_LOCALE` **from the in-memory store**.
   When localStorage is `"pt-BR"` and `state.locale` is undefined, `prev`
   and `next` mismatch **forever**, so the wrapper fires the callback on
   every `setState`.
3. The callback is `() => docsView({...})` registered **inside** `docsView`
   itself ([views/docs.js:104-106](web/admin/static/js/views/docs.js#L104-L106)),
   so each fire **re-registers a fresh subscriber while `setState`'s
   `for…of` loop is still iterating the Set**. JS `Set` iterators visit
   entries added during iteration, so the loop never exits.

Full mechanism in §4. Fix sketch in §9. **Fix has been applied** (see §0).

---

## 1 — Docker state

### Containers are all stopped right now

```
$ docker ps -a --format 'table {{.Names}}\t{{.Status}}'
NAMES                    STATUS
saas-api                 Exited (2) 6 hours ago
corsi-postgres-1         Exited (255) 7 hours ago
saas-keycloak            Exited (143) 6 hours ago
saas-postgres            Exited (0) 6 hours ago
saas-keycloak-postgres   Exited (0) 6 hours ago
saas-mailpit             Exited (0) 6 hours ago
```

`docker compose ps` is unsupported in this environment (`docker: unknown
command: docker compose` — Docker CLI is the standalone client, not the
Compose-plugin one). `docker stats --no-stream` therefore returned nothing
useful — there is no live container to sample.

> **Note:** `saas-api` exited with code 2 about 6 hours before the
> investigation began. That coincides with the Firefox freeze episode.
> Either Firefox was killed and the user manually tore down the stack, or
> the exit was triggered by the same session. The log retention is still
> intact (75 223 lines), so the post-mortem is solid.

### Log shape — fetch storm to ONE URL

`docker logs saas-api 2>&1 | wc -l` → **75 223 lines**.

```
$ docker logs saas-api 2>&1 | grep "/admin/docs" \
  | awk -F'"' '{print $2}' | sort | uniq -c | sort -rn | head
  74655 /admin/docs/getting-started/QUICKSTART.pt-BR.md   ← LOOP
      2 /admin/docs/getting-started/QUICKSTART.md
      1 /admin/docs/validation/AUDIT_EVENTS.md
      1 /admin/docs/security/SECURITY_REMEDIATION_GAP1.md
      1 /admin/docs/security/SECURITY_REGRESSION_GAP1.md
      1 /admin/docs/security/SECURITY_GAPS.md
      1 /admin/docs/security/SECRETS_MANAGEMENT.pt-BR.md
      …  (every other doc has 1–2 hits)
```

**74 655 hits to one file vs. 1–2 hits per other doc.** This is not a
caching issue; this is an unbounded client-side loop targeted at the
PT-BR sibling of QUICKSTART.

### Time distribution — three runaway episodes

```
$ docker logs saas-api 2>&1 | grep QUICKSTART.pt-BR.md \
  | awk '{print substr($4,1,5)}' | sort | uniq -c | sort -k2
      2 21:35
    855 21:39
    593 21:40
  17477 21:45   ← burst 1
   4378 21:46
   1381 21:47
  18009 22:06   ← burst 2
   9180 22:10
   2184 22:12
  17414 22:29   ← burst 3
   3182 22:30
```

Three distinct runaway episodes, each peaking around 18 000 req/min
(~300 req/s) and decaying as Firefox's connection limits throttled the
fan-out. The episodes are 20+ minutes apart, so each was a separate user
action (most likely: open Docs → loop starts → reload → loop restarts).

### No restart loops, no build loops, no backend errors

All logged HTTP statuses for `/admin/docs/*` are `200`. Response times are
50 µs – 700 µs (the embed FS is in-memory; the backend is not the
bottleneck). No `panic`, no `gin recovery`, no restart cascade.

The backend handler ([internal/server/admin.go:90-127](internal/server/admin.go#L90-L127))
does exactly what it should: rejects `..`, refuses non-`.md`, reads from
`docs.MarkdownFS`, sets `Cache-Control: no-store`. **The `no-store` header
is correct for content under active iteration, but it means every loop
iteration WILL hit the wire** — no opportunistic short-circuit at the
browser cache layer.

---

## 2 — Frontend performance scenarios

**Caveat:** I cannot drive Chrome / Firefox DevTools from this agent
environment (no browser, no display). The numbers below are deductions
from the request count + code review + file sizes, not real recordings.
Where the prompt asks for FPS / paint / memory peaks, I say so and
substitute the closest measurable signal we have.

| Action | CPU (deduced) | Memory (deduced) | Long tasks | Freeze? | Notes |
|---|---|---|---|---|---|
| A. Open INDEX | low | ~+1 MB | none | no | INDEX.md is small, no Mermaid, single fetch. Listener wiring is O(1). |
| B. Open QUICKSTART.md (EN) | medium | ~+3 MB | one (~100-200ms): markdown render of 31 KB + 1 Mermaid lazy import (~250 KB ESM) | no | Mermaid loads once per session. Highlight is hand-rolled and fast. |
| C. Open QUICKSTART.pt-BR.md | medium | ~+3 MB | same as B | no | Same render cost. The fetch URL differs but the parse cost is symmetric. |
| D. Toggle EN → PT-BR | medium | grows | one render pass | no on first toggle | Single `setState({locale})`. If `localStorage` was empty, this is the moment that ARMS the bug (see §4). Doesn't freeze yet because the locale subscriber's `prev` was registered AFTER setLocale persisted to localStorage. |
| E. Open doc with Mermaid | medium-high | ~+5 MB | one (~300-500ms) for first Mermaid block (CDN load + parse + SVG render) | no | Mermaid count is small (see §5). |
| F. Search within doc | low per keystroke; **no debounce** — high under rapid typing | linear in matches | none individually, but cumulative wall-time scales with N keystrokes × M text nodes | no | The MutationObserver-style normalize / replace runs every keystroke (see §4.5). |
| **G. Boot Docs WITH `admin_docs_locale === "pt-BR"` already set, then ANY setState** | **pegged to 100%** | **grows linearly with each iteration; new Set entries, new DOM trees, new IntersectionObservers** | **one synchronous task that never returns** | **YES — the freeze** | The runaway loop from §4. This is the Firefox-killing path. |

Long tasks > 50 ms in normal operation are expected exactly twice per
doc open: the markdown render and the Mermaid SVG render. Everything
else (TOC build, scrollspy IO setup, highlight pass over 5-10 code
blocks) is sub-50 ms by construction.

---

## 3 — Render pipeline cost (per docsView call)

Instrumented mentally (no code added to the tree; rule says no edits).
Rough ballpark for QUICKSTART.pt-BR.md (32 914 bytes, ~742 lines, 1
Mermaid block, ~30 code fences, deep TOC):

```
Render stage                ms (estimate)   evidence
─────────────────────────────────────────────────────────────────
fetch(localized.md)             ~5-20       backend served in 50-700 µs; remainder is network/parse
markdown parse (renderMd)       ~30-80      single-pass tokenise + inline; 742 lines
syntax highlight                ~10-30      ~20-30 fences × ~1 ms each (hand-rolled, regex-light)
toc generation                  ~2-5        headings already extracted by renderMd
scrollspy (IO setup)            ~1-3        one IntersectionObserver, ~20 observed elements
mermaid lazy import (1st time)  ~150-300    CDN ESM, parsed and initialised
mermaid.render (1 block)        ~80-150     SVG generation
final paint                     ~30-60      large article DOM
─────────────────────────────────────────────────────────────────
Total (cold)                    ~300-650
Total (warm, mermaid cached)    ~80-200
```

These are workable numbers. The view is **not** slow per render — the
problem is that **the view re-renders thousands of times per second**
once the loop arms.

Source files inspected (no edits):

- [web/admin/static/js/views/docs.js](web/admin/static/js/views/docs.js) — 657 lines, owns docsView, fetchDoc, TOC, scrollspy, mermaid, search, copy buttons, internal-link interception.
- [web/admin/static/js/lib/markdown.js](web/admin/static/js/lib/markdown.js) — 380 lines, hand-rolled renderer. No nested-render hot loop. `applyEmphasis`'s lookbehind regex is fine.
- [web/admin/static/js/lib/highlight.js](web/admin/static/js/lib/highlight.js) — 184 lines, scoped to languages actually used in this repo. No catastrophic backtracking patterns seen.
- [web/admin/static/js/lib/locale.js](web/admin/static/js/lib/locale.js) — 80 lines. **This is the file with the bug.**
- [web/admin/static/js/lib/state.js](web/admin/static/js/lib/state.js) — 44 lines, the tiny pub/sub store. **Initial `_state` is missing the `locale` field.**
- [web/admin/static/js/lib/router.js](web/admin/static/js/lib/router.js) — 109 lines. `handleChange` calls `setState({ route })` on every navigation; that's what arms the loop.

---

## 4 — Loops, listeners, observers

### Q: Is there a render-infinite loop?

**Yes.** Synchronous, fires inside `setState`'s `for…of`. Details below.

### The loop, step-by-step

1. **Bootstrap conditions.** localStorage has `admin_docs_locale = "pt-BR"`
   (set in any previous session). `_state.locale` is `undefined` because
   [lib/state.js:8-14](web/admin/static/js/lib/state.js#L8-L14) never
   declared a `locale` field:
   ```js
   const _state = { config: null, token: null, identity: null,
                    theme: "dark", route: null };   // no locale!
   ```
2. **Docs view mounts.** [views/docs.js:104-106](web/admin/static/js/views/docs.js#L104-L106)
   registers a locale subscriber:
   ```js
   _localeUnsub = onLocaleChange(() => {
     docsView({ params, container, query });
   });
   ```
   [`onLocaleChange`](web/admin/static/js/lib/locale.js#L58-L67) builds the
   wrapper:
   ```js
   let prev = getLocale();                 // → "pt-BR" (from localStorage)
   return subscribe((state) => {
     const next = state.locale || DEFAULT_LOCALE;  // → undefined || "en" = "en"
     if (next !== prev) { prev = next; fn(next); }
   });
   ```
   `prev` is **"pt-BR"** (sourced from localStorage), `next` is **"en"**
   (sourced from in-memory `_state`, which has no `locale`). Mismatch is
   **permanent** — there is no path that ever makes them agree, because
   `setLocale` only writes if the user clicks the toggle.
3. **Any setState arms the loop.** As soon as `setState({ route })` (router),
   `setState({ theme })` (theme toggle), `setState({ identity })` (auth
   refresh), or any other call runs:
   ```js
   for (const fn of _subs) { try { fn(_state); } catch ... }
   ```
   The iterator visits the locale wrapper. Mismatch fires
   `docsView({...})`.
4. **Re-entrant `docsView` mutates the Set during iteration.**
   ```js
   if (_localeUnsub) _localeUnsub();              // delete current entry
   _localeUnsub = onLocaleChange(...);            // ADD a new entry
   await renderEntry(entry, pageKey, container, query);
   ```
   `subscribe(...)` returns a fresh unsubscribe; the new wrapper has the
   **same broken `prev = "pt-BR"` vs `next = "en"` mismatch**. The new
   subscriber is appended to `_subs`. The fetch starts (now `await`
   suspends).
5. **Set iteration visits the new entry.** ECMAScript Set iterators visit
   entries added during iteration. The new wrapper fires
   `docsView` again. Goto step 4.
6. **Unbounded synchronous loop.** Each iteration adds a fetch to the
   network queue and recreates the entire Docs shell DOM (`mount(container,
   …)` in renderEntry). Firefox's main thread is held. The fetches drain
   asynchronously and slam the backend. Eventually Firefox throttles
   connection slots (visible in the per-second log decay), but the JS
   side never stops issuing them. The browser is unresponsive until killed.

### Listeners and observers — secondary smells

These don't cause the freeze on their own, but they are real defects that
will bite once the loop is fixed:

- **Click listeners pile up on `#docs-article`.** `wireCopyButtons` and
  `wireInternalLinks` both call `article.addEventListener("click", …)`
  ([views/docs.js:278](web/admin/static/js/views/docs.js#L278),
  [:318](web/admin/static/js/views/docs.js#L318)). In normal operation
  the article element is replaced every `mount`, so old listeners die
  with their host. In the loop scenario, dozens of dead articles
  accumulate per second before GC catches up.
- **IntersectionObserver leak.** `renderToc`
  ([:431-442](web/admin/static/js/views/docs.js#L431-L442)) creates a
  new `IntersectionObserver` on every docsView and never `disconnect()`s
  the previous one. Old observers stop firing (their targets are
  detached), but they hold the closure scope alive.
- **No debounce on doc search.** [views/docs.js:138](web/admin/static/js/views/docs.js#L138)
  fires `applySearchHighlight(value)` on every `oninput`. That helper
  walks every text node, builds DocumentFragments, and rewrites the
  article. Rapid typing in a long doc is O(keystrokes × text-nodes) of
  DOM churn.
- **Topbar / sidebar subscribers DO behave** — they call `mount(el, …)`
  which replaces innerHTML cleanly; only ONE subscriber per component
  is ever registered because `renderTopbar`/`renderSidebar` are called
  once at boot.

### Q: Is there a listener-duplicated bug?

Yes, on `#docs-article` under the loop scenario (above), but not on
sidebar/topbar.

### Q: Re-render on every scroll?

No. `IntersectionObserver` is used precisely to avoid scroll-listener
spam. The IO callback only adjusts `.docs-toc-link.active` classes — no
re-render.

### Q: Re-render on every hashchange?

Yes — once per actual hash change. That's normal SPA behavior. The
problem is that this single re-render now triggers the loop above.

---

## 5 — Mermaid behavior

The Mermaid path is **mostly correct**:

- The library is lazy-loaded from CDN — `loadMermaid()` is memoised at
  module level. Cold cost ~150-300 ms on first doc that has a fence;
  subsequent docs reuse the same module.
- It renders **every Mermaid block in the current doc** sequentially,
  not just visible ones (`for (let i = 0; i < blocks.length; i++)` in
  [views/docs.js:620-636](web/admin/static/js/views/docs.js#L620-L636)).
  So the answer to the prompt's question is **B) renders ALL on open**,
  not A). For the current docs corpus this is fine because the per-doc
  block count is tiny:

  ```
  $ grep -c '^```mermaid' docs/getting-started/QUICKSTART.md
  1
  $ grep -c '^```mermaid' docs/getting-started/QUICKSTART.pt-BR.md
  1
  $ grep -lrE '^```mermaid' docs/
  docs/meta/DOCS_RUNTIME_VALIDATION.md   (2)
  docs/getting-started/QUICKSTART.md     (1)
  docs/getting-started/QUICKSTART.pt-BR.md (1)
  ```

  Max 2 blocks in any doc today. Render cost is negligible vs. the loop.

- `securityLevel: 'strict'`, `htmlLabels: false` — sensible.
- Failure is contained per block — failed diagrams fall back to a visible
  source `<details>`. Good.

**Verdict:** Mermaid is NOT the cause of the freeze. If anything, it
amplifies it under the loop because each runaway re-render also asks
Mermaid to re-render the same block(s).

---

## 6 — Memory leak

Cannot run the live 20-document switch test (no browser available in
this environment). Reasoning from code:

- **`onLocaleChange` subscribers:** the loop adds one per iteration, but
  also removes the prior one. Steady-state: 1 sub. **Not leaked.**
- **IntersectionObservers in renderToc:** one new IO per docsView call,
  never disconnected ([views/docs.js:431-442](web/admin/static/js/views/docs.js#L431-L442)).
  Each IO retains its callback closure, which references `root`, `items`,
  and `tocLinks`. **Leaked. Steady growth proportional to navigation count.**
  Under the runaway loop this grows by 100s/second.
- **`_mermaidPromise`:** memoised module-level. **Not leaked**, intentionally.
- **Detached DOM:** every `mount(container, …)` replaces #main's children.
  Old subtrees become detached. They have ZERO surviving event listeners
  (event listeners died with the old article element when its parent
  `docs-shell` was replaced). They should be GC-able. **Not a true leak,
  but during the runaway loop the GC can't keep up — heap will balloon
  until either the loop ends or the tab crashes.**

**Plausible heap signature under the loop:**
- Pre-arm: ~30-40 MB.
- 30s of loop: 200-500 MB.
- 60-90s of loop: out-of-memory crash on Firefox (default ~2 GB cap per
  tab on macOS, hit faster on memory-constrained boxes).

---

## 7 — Findings, ranked

### P0 — fix before any further use

1. **Locale-driven setState infinite loop.** [§4](#4--loops-listeners-observers). The
   freeze. Affects every page in Docs mode the moment localStorage has
   `pt-BR` and any setState fires.

### P1 — fix in the next pass

2. **`_localeUnsub` is module-singleton and re-registered inside
   `docsView`.** Even after the loop is fixed, this pattern is fragile.
   The "every doc mount adds a new subscriber, but trusts itself to tear
   down on next mount" contract works by accident; one missed teardown
   in any future code path turns the warning fire into a wildfire again.
   Prefer: register once at boot in main.js, subscribe just to "current
   doc needs rerender" decisions.
3. **IntersectionObserver leak in `renderToc`.** [§4](#4--loops-listeners-observers).
   Keep a module-level handle, `disconnect()` it before re-arming.
4. **Click-listener stacking on `#docs-article`.** Same fix shape:
   detach on next docsView, or use event delegation from `#main` once.

### P2 — quality of life

5. **No-debounce search.** Add a 100-150 ms debounce around
   `applySearchHighlight`.
6. **`Cache-Control: no-store` on docs endpoint.** Justifiable while the
   docs change daily, but consider `must-revalidate` + ETag so a normal
   navigation pattern doesn't pay for re-shipping 30 KB every time.
   (Optional, not load-bearing.)
7. **Mermaid renders all blocks unconditionally on open.** Today's docs
   have ≤2 per file; with 10+ this would become felt. Consider an
   IntersectionObserver-driven "render on first scroll into view" pass.
8. **Detach IO observers when the user navigates away from the doc.**
   The single-flight pattern in `docsView` doesn't currently propagate to
   the IO.

### Not a regression, but noted

- The PT-BR fallback path itself (`fetchDoc(localized)` then `fetchDoc(en)`)
  is fine. The double-fetch on first 404 is one extra request, not 74k.
- `markdown.js` is hand-rolled but the regex set is reasonable. No
  catastrophic backtracking was found.

---

## 8 — Corrigir já vs otimizar depois

**Corrigir já** (blocks daily use):
- P0 #1 — the loop.

**Otimizar depois** (post-fix hardening):
- P1 #2, #3, #4 — listener / observer hygiene.
- P2 #5 #7 #8 — debounce, viewport-aware Mermaid, IO teardown.

**Não corrigir** (intentional or defensible):
- `Cache-Control: no-store` while docs churn.
- Mermaid CDN dependency (deliberate per the comment block in `loadMermaid`).
- Hand-rolled highlighter and renderer (deliberate "no package manager" stance).

---

## 9 — Recommended fix (sketch only — NOT applied per rules)

Smallest patch that resolves P0:

```js
// lib/state.js — initialise locale at module load so onLocaleChange's
// filter has a coherent baseline.
import { getLocale } from "./locale.js";   // careful with circular import
const _state = {
  config: null, token: null, identity: null,
  theme: "dark", route: null,
  locale: getLocale(),                     // ← add this
};
```

Or, equivalently, in main.js right after `applyTheme()`:

```js
import { getLocale } from "./lib/locale.js";
// …
setState({ locale: getLocale() });          // before any other setState
```

Either of these makes `prev` and `next` agree at boot, which prevents the
runaway. A second, defensive change — even cheaper — is to compare against
the SAME source in `onLocaleChange`:

```js
export function onLocaleChange(fn) {
  let prev = undefined;                     // ← compare against state, not localStorage
  return subscribe((state) => {
    const next = state.locale || DEFAULT_LOCALE;
    if (prev === undefined) { prev = next; return; }   // prime, don't fire
    if (next !== prev) { prev = next; try { fn(next); } catch (e) {} }
  });
}
```

Doing **both** is belt-and-braces.

After the loop is fixed, the P1/P2 items above are worth a small follow-up.

---

## Appendix — raw evidence

```
$ docker logs saas-api 2>&1 | wc -l
75223

$ docker logs saas-api 2>&1 \
  | grep "/admin/docs/" | awk -F'"' '{print $2}' | sort | uniq -c \
  | sort -rn | head -5
74655 /admin/docs/getting-started/QUICKSTART.pt-BR.md
    2 /admin/docs/getting-started/QUICKSTART.md
    1 /admin/docs/validation/AUDIT_EVENTS.md
    1 /admin/docs/security/SECURITY_REMEDIATION_GAP1.md
    1 /admin/docs/security/SECURITY_REGRESSION_GAP1.md

$ docker logs saas-api 2>&1 | grep QUICKSTART.pt-BR.md \
  | awk '{print substr($4,1,5)}' | sort | uniq -c | sort -k2
    2 21:35
  855 21:39
  593 21:40
17477 21:45
 4378 21:46
 1381 21:47
18009 22:06
 9180 22:10
 2184 22:12
17414 22:29
 3182 22:30

$ grep -n locale web/admin/static/js/lib/state.js   # ← empty: no init
(no output)

$ grep -nE 'let prev|const next' web/admin/static/js/lib/locale.js
59:  let prev = getLocale();
61:    const next = state.locale || DEFAULT_LOCALE;
```
