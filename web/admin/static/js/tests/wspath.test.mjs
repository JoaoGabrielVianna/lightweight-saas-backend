// wspath.test.mjs — the two path builders.
//
//   api.js  wsPath()  → "/v1/workspaces/<id>/users"    (API URL)
//   router  wsRoute()  → "/workspaces/<id>/users"       (hash route)
//
// Both exist so no view ever interpolates a workspace id into a string. The
// rule matters because "which workspace did this request go to?" must be
// answerable by reading one function, not by grepping thirty call sites — and
// because the missing-workspace case needs exactly one home.

import { test } from "node:test";
import assert from "node:assert/strict";
import { makeStorage } from "./helpers.mjs";

globalThis.localStorage = makeStorage();
globalThis.sessionStorage = makeStorage();

const { wsPath } = await import("../lib/api.js");

// router.js touches window at module load only inside init(), so importing it
// needs just a window object with a location.
globalThis.window = { location: { hash: "" }, addEventListener() {}, scrollTo() {} };
const { wsRoute, isWorkspaceRoute, workspaceIdFromPath, swapWorkspaceInPath } =
  await import("../lib/router.js");

const WS = "ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301";

// ─── wsPath ─────────────────────────────────────────────────────────────────

test("wsPath builds the /v1 workspace-scoped URL", () => {
  assert.equal(wsPath(WS, "/users"), `/v1/workspaces/${WS}/users`);
  assert.equal(wsPath(WS, "/roles/editor/users"), `/v1/workspaces/${WS}/roles/editor/users`);
  assert.equal(wsPath(WS, "/connections"), `/v1/workspaces/${WS}/connections`);
});

test("wsPath accepts a path with or without a leading slash, identically", () => {
  assert.equal(wsPath(WS, "users"), wsPath(WS, "/users"));
});

test("wsPath with no path addresses the workspace itself", () => {
  assert.equal(wsPath(WS, ""), `/v1/workspaces/${WS}`);
  assert.equal(wsPath(WS), `/v1/workspaces/${WS}`);
});

test("wsPath preserves a query string", () => {
  assert.equal(wsPath(WS, "/users?first=20&max=20"), `/v1/workspaces/${WS}/users?first=20&max=20`);
});

test("wsPath never produces a double slash", () => {
  for (const p of ["/users", "users", " /users "]) {
    const out = wsPath(WS, p);
    assert.ok(!out.slice(1).includes("//"), `"${p}" produced "${out}"`);
    assert.equal(out, `/v1/workspaces/${WS}/users`);
  }
});

test("wsPath rejects a protocol-relative path rather than collapsing it", () => {
  // "//host/x" is an ABSOLUTE url in a browser, not a path with a stray
  // slash. Silently collapsing it to "/host/x" would turn a caller's mistake
  // into a same-origin request against a wrong path; rejecting says so.
  assert.throws(() => wsPath(WS, "//users"), /absolute/);
  assert.throws(() => wsPath(WS, "///users"), /absolute/);
});

test("wsPath REJECTS a missing workspace id", () => {
  // Returning a degraded "/v1/workspaces//users" would 404 somewhere far from
  // the bug. Throwing fails at the call site that forgot the workspace.
  for (const bad of [undefined, null, "", "   ", 0, {}]) {
    assert.throws(() => wsPath(bad, "/users"), /workspace id is required|ws_<uuid>/,
      `wsPath(${JSON.stringify(bad)}) should throw`);
  }
});

test("wsPath REJECTS an id that is not a workspace public id", () => {
  assert.throws(() => wsPath("conn_123", "/users"), /ws_<uuid>/);
  assert.throws(() => wsPath("3f2504e0-4f89", "/users"), /ws_<uuid>/);
});

test("wsPath REJECTS an absolute URL — it is not a general URL builder", () => {
  assert.throws(() => wsPath(WS, "https://evil.example.com/users"), /absolute/);
  assert.throws(() => wsPath(WS, "//evil.example.com/users"), /absolute/);
});

test("wsPath REJECTS a path that already carries /v1", () => {
  // A caller passing a full path has built the URL elsewhere and is using this
  // helper as a rubber stamp.
  assert.throws(() => wsPath(WS, "/v1/workspaces/other/users"), /workspace-relative/);
});

// ─── wsRoute ────────────────────────────────────────────────────────────────

test("wsRoute builds the hash route", () => {
  assert.equal(wsRoute(WS, "users"), `/workspaces/${WS}/users`);
  assert.equal(wsRoute(WS, "/users"), `/workspaces/${WS}/users`);
  assert.equal(wsRoute(WS), `/workspaces/${WS}`);
});

test("wsRoute rejects a missing workspace id", () => {
  assert.throws(() => wsRoute("", "users"), /workspace id is required/);
  assert.throws(() => wsRoute(null, "users"), /workspace id is required/);
});

test("workspaceIdFromPath reads the workspace out of a route", () => {
  assert.equal(workspaceIdFromPath(`/workspaces/${WS}/users`), WS);
  assert.equal(workspaceIdFromPath(`/workspaces/${WS}/users/abc-def`), WS);
  assert.equal(workspaceIdFromPath(`/workspaces/${WS}`), WS);
  assert.equal(workspaceIdFromPath("/overview"), null);
  assert.equal(workspaceIdFromPath("/workspaces"), null);
  assert.equal(workspaceIdFromPath(""), null);
});

test("isWorkspaceRoute distinguishes scoped from installation-scoped pages", () => {
  assert.equal(isWorkspaceRoute(`/workspaces/${WS}/users`), true);
  assert.equal(isWorkspaceRoute("/workspaces"), false, "the workspace LIST is not workspace-scoped");
  assert.equal(isWorkspaceRoute("/email"), false, "legacy provider settings carry no workspace");
  assert.equal(isWorkspaceRoute("/overview"), false);
});

// ─── swapWorkspaceInPath ────────────────────────────────────────────────────

test("swapWorkspaceInPath keeps the operator on the same PAGE in the new workspace", () => {
  const other = "ws_00000000-0000-4000-8000-000000000000";
  assert.equal(swapWorkspaceInPath(`/workspaces/${WS}/roles`, other), `/workspaces/${other}/roles`);
  assert.equal(swapWorkspaceInPath(`/workspaces/${WS}/sessions`, other), `/workspaces/${other}/sessions`);
});

test("swapWorkspaceInPath DROPS entity ids — they name a record in the old realm", () => {
  // This is the whole point. A user uuid from workspace A either 404s in B or,
  // in a realm cloned from A's, resolves to a DIFFERENT person.
  const other = "ws_00000000-0000-4000-8000-000000000000";
  assert.equal(
    swapWorkspaceInPath(`/workspaces/${WS}/users/9c1e-user-from-A`, other),
    `/workspaces/${other}/users`,
  );
  assert.equal(
    swapWorkspaceInPath(`/workspaces/${WS}/roles/editor`, other),
    `/workspaces/${other}/roles`,
  );
});

test("swapWorkspaceInPath drops a query string with the page", () => {
  const other = "ws_00000000-0000-4000-8000-000000000000";
  // Pagination and search belong to the realm they were run against.
  assert.equal(
    swapWorkspaceInPath(`/workspaces/${WS}/users?first=40&search=alice`, other),
    `/workspaces/${other}/users`,
  );
});
