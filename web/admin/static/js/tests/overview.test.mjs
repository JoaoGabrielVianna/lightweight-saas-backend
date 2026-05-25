// overview.test.mjs — unit tests for views/overview.js.
//
// Run with: node --test web/admin/static/js/tests/
//
// Pins the UI-002 regression: a stale Overview render must not clobber the
// container after the user has navigated away or re-entered /overview.
//
// Pre-fix behaviour: overviewView mounted the placeholder shell, awaited
// /health + OIDC discovery + /admin/users, then unconditionally mounted the
// final markup. If the user clicked Users mid-await, the Users view
// rendered first, then the resumed Overview render overwrote it with its
// own stat cards — the page would flash back to Overview content under a
// "/users" URL until the next navigation. Same hazard if /overview was
// re-entered before the first awaits resolved: two concurrent renders
// raced to write the container, leaving the cards out-of-order or doubled.
//
// The fix: a module-level `_overviewGen` counter is bumped on every entry,
// captured into a local at the top of the function, and re-checked before
// the post-await mount. If the captured generation no longer matches the
// current one, OR the active route has moved away from /overview, the
// render bails before touching the DOM. This test pins the predicate.

import { test } from "node:test";
import assert from "node:assert/strict";

// Stub localStorage so state.js (imported transitively by overview.js)
// can seed _state.locale at module load.
globalThis.localStorage = makeStorage();

const {
  _isOverviewStaleForTests,
  _resetOverviewGenForTests,
  _bumpOverviewGenForTests,
} = await import("../views/overview.js");

function makeStorage(initial) {
  const data = { ...(initial || {}) };
  return {
    getItem(k) { return Object.prototype.hasOwnProperty.call(data, k) ? data[k] : null; },
    setItem(k, v) { data[k] = String(v); },
    removeItem(k) { delete data[k]; },
    clear() { for (const k of Object.keys(data)) delete data[k]; },
  };
}

test("staleness predicate: fresh render on /overview is NOT stale", () => {
  _resetOverviewGenForTests();
  const myGen = _bumpOverviewGenForTests(); // simulate entry → captures 1
  assert.equal(_isOverviewStaleForTests(myGen, "/overview"), false,
    "captured gen matches current gen and path is /overview → render");
});

test("regression UI-002: newer Overview render makes prior render stale", () => {
  // Simulates: user opens Overview, awaits /health; before the await
  // resolves, the user clicks back to Overview (re-entry). The first
  // render's captured gen no longer matches the bumped current gen, so
  // it must bail rather than racing the second render to mount.
  _resetOverviewGenForTests();
  const firstGen  = _bumpOverviewGenForTests(); // first entry → 1
  const secondGen = _bumpOverviewGenForTests(); // re-entry   → 2

  assert.equal(_isOverviewStaleForTests(firstGen,  "/overview"), true,
    "first render is stale once second render has started");
  assert.equal(_isOverviewStaleForTests(secondGen, "/overview"), false,
    "second render is the winner — not stale");
});

test("regression UI-002: navigation away from /overview makes render stale", () => {
  // Simulates: user opens Overview, awaits /admin/users; before the await
  // resolves, the user clicks Users. The resumed Overview render must bail
  // (NOT mount its stat cards on top of the Users view container).
  _resetOverviewGenForTests();
  const myGen = _bumpOverviewGenForTests();

  assert.equal(_isOverviewStaleForTests(myGen, "/users"), true,
    "active route has moved away from /overview → stale");
  assert.equal(_isOverviewStaleForTests(myGen, "/playground"), true,
    "any non-/overview path is stale");
  assert.equal(_isOverviewStaleForTests(myGen, null), true,
    "null route (pre-boot) is stale");
});

test("regression UI-002: both conditions together still classify as stale", () => {
  // Defensive: even if a newer render has bumped the gen AND the route
  // has moved away, the predicate still returns true. Either condition
  // alone is sufficient — but neither must mask the other.
  _resetOverviewGenForTests();
  const firstGen = _bumpOverviewGenForTests();
  _bumpOverviewGenForTests();

  assert.equal(_isOverviewStaleForTests(firstGen, "/users"), true,
    "stale by both gen mismatch AND route change");
});

test("regression UI-002: reset helper returns generation to zero", () => {
  // The reset helper exists so successive test cases start from a known
  // generation. Without it, this test file's earlier cases would leak
  // their bumps into later cases.
  _resetOverviewGenForTests();
  const g1 = _bumpOverviewGenForTests();
  _resetOverviewGenForTests();
  const g2 = _bumpOverviewGenForTests();
  assert.equal(g1, g2, "after reset, the next bump returns the same starting value");
});
