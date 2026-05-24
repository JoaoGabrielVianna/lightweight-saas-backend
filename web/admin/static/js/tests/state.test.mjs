// state.test.mjs — unit tests for lib/state.js.
//
// Run with: node --test web/admin/static/js/tests/
//
// These tests pin the iteration-safety invariants that prevent the
// Docs-UI runaway fetch loop:
//
//   - subscribers added during dispatch do not fire in the same cycle;
//   - subscribers unsubscribed during dispatch are skipped immediately;
//   - re-entrant setState calls collapse into one observable update;
//   - the "callback re-registers itself" pattern terminates after one fire.
//
// Each `t.beforeEach` resets the module singleton via the explicit
// `_resetForTests` helper — the alternative (per-test process forks)
// would be needlessly heavy for a 200-line module.

import { test } from "node:test";
import assert from "node:assert/strict";

// Stub localStorage BEFORE importing modules that read it at top level.
// state.js seeds _state.locale from localStorage.admin_docs_locale at
// module load; without this stub the import would throw under Node.
globalThis.localStorage = makeStorage();

const { setState, subscribe, getState, _resetForTests } =
  await import("../lib/state.js");

function makeStorage(initial) {
  const data = { ...(initial || {}) };
  return {
    getItem(k) { return Object.prototype.hasOwnProperty.call(data, k) ? data[k] : null; },
    setItem(k, v) { data[k] = String(v); },
    removeItem(k) { delete data[k]; },
    clear() { for (const k of Object.keys(data)) delete data[k]; },
    _data: data,
  };
}

test("setState merges patch into state", () => {
  _resetForTests();
  setState({ theme: "light" });
  assert.equal(getState().theme, "light");
  setState({ route: "/x" });
  assert.equal(getState().theme, "light");
  assert.equal(getState().route, "/x");
});

test("subscribers fire on every setState", () => {
  _resetForTests();
  let calls = 0;
  subscribe(() => calls++);
  setState({ theme: "light" });
  setState({ theme: "dark" });
  setState({ route: "/y" });
  assert.equal(calls, 3);
});

test("unsubscribe removes the listener", () => {
  _resetForTests();
  let calls = 0;
  const unsub = subscribe(() => calls++);
  setState({ theme: "light" });
  unsub();
  setState({ theme: "dark" });
  assert.equal(calls, 1);
});

test("snapshot: subscribers added during dispatch do NOT fire in the same cycle", () => {
  _resetForTests();
  let aCalls = 0;
  let bCalls = 0;
  subscribe(() => {
    aCalls++;
    if (aCalls === 1) {
      // Register B inside A's first invocation.
      subscribe(() => bCalls++);
    }
  });
  setState({ route: "/x" });
  assert.equal(aCalls, 1, "A fires once for the outer dispatch");
  assert.equal(bCalls, 0, "B does NOT fire — it was added after the snapshot was taken");
  setState({ route: "/y" });
  assert.equal(aCalls, 2);
  assert.equal(bCalls, 1, "B fires on the next dispatch (it's in the new snapshot)");
});

test("snapshot: subscriber unsubscribed during dispatch is skipped immediately", () => {
  _resetForTests();
  let aCalls = 0;
  let bCalls = 0;
  let unsubB = null;
  subscribe(() => { aCalls++; if (unsubB) { unsubB(); unsubB = null; } });
  unsubB = subscribe(() => bCalls++);
  setState({ route: "/x" });
  // A unsubscribed B before B's slot in the snapshot was visited.
  assert.equal(aCalls, 1);
  assert.equal(bCalls, 0, "B was unsubscribed mid-cycle and must not fire");
});

test("reentrancy: setState inside a callback merges but does NOT spawn nested dispatch", () => {
  _resetForTests();
  let aCalls = 0;
  let bCalls = 0;
  subscribe(() => {
    aCalls++;
    if (aCalls === 1) {
      // Re-entrant setState: patch should merge, but no nested dispatch.
      setState({ theme: "light" });
    }
  });
  subscribe(() => bCalls++);
  setState({ route: "/x" });
  assert.equal(aCalls, 1, "A fires once — re-entrant setState did not re-fire A");
  assert.equal(bCalls, 1, "B fires once — re-entrant setState did not re-fire B either");
  assert.equal(getState().route, "/x", "outer patch merged");
  assert.equal(getState().theme, "light", "inner patch merged");
});

test("regression: callback re-registering itself does NOT cause runaway recursion", () => {
  // This is the exact pattern docsView used:
  //   _localeUnsub = onLocaleChange(() => { docsView({...}); });
  // where docsView synchronously re-registers _localeUnsub. Without the
  // snapshot in setState, the Set iterator visited the newly-added
  // subscriber, which fired again, which re-registered, … unbounded.
  _resetForTests();
  let fireCount = 0;
  let currentUnsub = null;
  const attach = () => {
    if (currentUnsub) currentUnsub();
    currentUnsub = subscribe(() => {
      fireCount++;
      attach();
    });
  };
  attach();
  setState({ route: "/x" });
  // Snapshot semantics: exactly ONE fire, the original subscriber. The
  // self-re-registered subscriber is added AFTER the snapshot and does
  // not fire in this cycle. The next setState would visit it once.
  assert.equal(fireCount, 1, `expected 1 fire, got ${fireCount} (pre-fix: 74 655)`);
});

test("regression: locale seeded from localStorage at module load", () => {
  // The seed is what makes onLocaleChange's prev (localStorage) and
  // next (state.locale) agree from boot. Without it, every non-locale
  // setState wasted one docsView call even AFTER the snapshot fix.
  globalThis.localStorage = makeStorage({ admin_docs_locale: "pt-BR" });
  _resetForTests(); // re-reads localStorage
  assert.equal(getState().locale, "pt-BR");

  globalThis.localStorage = makeStorage({ admin_docs_locale: "garbage" });
  _resetForTests();
  assert.equal(getState().locale, "en", "unknown locale value falls back to en");

  globalThis.localStorage = makeStorage({}); // empty
  _resetForTests();
  assert.equal(getState().locale, "en");
});
