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
import { installDOM } from "./helpers.mjs";

// Stub localStorage so state.js (imported transitively by overview.js)
// can seed _state.locale at module load.
globalThis.localStorage = makeStorage();
globalThis.sessionStorage = makeStorage();
installDOM();
globalThis.window = { location: { hash: "", origin: "https://lw.example.com" }, addEventListener() {}, scrollTo() {} };

const {
  _isOverviewStaleForTests,
  _resetOverviewGenForTests,
  _bumpOverviewGenForTests,
  quickActions,
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

// labels renders quickActions and returns the button captions, which is the
// only part of it a user can act on.
function labels(workspaceId, config) {
  return quickActions(workspaceId, config).map((el) => el.textContent);
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

// ─── Quick actions must not offer what this deployment does not serve ───────
//
// The dev tools are gated on the server's /admin/config.json flags in two
// places already (pruneNav hides the nav entries, gateDevToolView bounces the
// routes). Overview's Quick actions card rendered them unconditionally, so on
// a production console — ADMIN_CONSOLE_ENABLED=true with
// DEV_PLAYGROUND_ENABLED=false, exactly what `init.sh --keycloak-url` writes —
// the first screen offered two buttons that silently returned the operator to
// the same screen.

test("production config offers no dev-tool actions", () => {
  const production = { devTools: false, apiExplorer: false };
  const got = labels("ws_1", production);

  assert.ok(!got.includes("Open Playground"),
    "Playground is route-gated in production; offering it is a dead button");
  assert.ok(!got.includes("API Explorer"),
    "API Explorer is route-gated in production; offering it is a dead button");
  assert.ok(got.includes("Open Swagger"),
    "Swagger is always mounted and must stay");
  assert.ok(got.includes("Getting started"),
    "a first-run operator needs a route into the onboarding docs");
});

test("dev config offers the dev-tool actions", () => {
  const dev = { devTools: true, apiExplorer: true };
  const got = labels("ws_1", dev);

  assert.ok(got.includes("Open Playground"), "Playground is mounted when devTools is on");
  assert.ok(got.includes("API Explorer"), "API Explorer is mounted when apiExplorer is on");
});

test("the two dev flags are independent", () => {
  const onlyPlayground = labels("ws_1", { devTools: true, apiExplorer: false });
  assert.ok(onlyPlayground.includes("Open Playground"));
  assert.ok(!onlyPlayground.includes("API Explorer"));

  const onlyExplorer = labels("ws_1", { devTools: false, apiExplorer: true });
  assert.ok(!onlyExplorer.includes("Open Playground"));
  assert.ok(onlyExplorer.includes("API Explorer"));
});

test("absent or unloaded config hides both dev tools", () => {
  // Fail closed: a config.json that failed to load must not expose a dev tool.
  for (const config of [undefined, null, {}]) {
    const got = labels("ws_1", config);
    assert.ok(!got.includes("Open Playground"), `devTools absent (${JSON.stringify(config)}) → hidden`);
    assert.ok(!got.includes("API Explorer"), `apiExplorer absent (${JSON.stringify(config)}) → hidden`);
  }
});

test("with no workspace the primary action is to create one", () => {
  const got = labels(null, { devTools: false, apiExplorer: false });
  assert.ok(got.includes("Create a workspace"),
    "an operator with no workspace has exactly one useful next step");
  assert.ok(!got.includes("Manage users"),
    "identity screens are workspace-scoped; offering them without one is a dead end");
});
