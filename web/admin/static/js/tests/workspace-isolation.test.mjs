// workspace-isolation.test.mjs — the tests this slice exists for.
//
// Two workspaces point at two different Keycloak realms. Showing A's users
// under B's name is not a cosmetic bug: it is the operator deleting the wrong
// person. Everything below pins one of the three mechanisms in
// lib/workspaces.js — the generation counter, the abort signal, and the switch
// listeners — against the exact race it was written for.
//
// The canonical race (Phase 8):
//
//   workspace A selected
//     → GET users A starts
//       → operator switches to B
//         → GET users B starts
//           → B returns and renders
//             → A returns LATER
//
// A must not overwrite B. Not "usually"; ever.

import { test } from "node:test";
import assert from "node:assert/strict";
import { makeStorage, fetchStub, deferred } from "./helpers.mjs";

globalThis.localStorage = makeStorage();
globalThis.sessionStorage = makeStorage();

const state = await import("../lib/state.js");
const ws = await import("../lib/workspaces.js");
const { apiTry, wsPath } = await import("../lib/api.js");

const A = "ws_aaaaaaaa-0000-4000-8000-000000000001";
const B = "ws_bbbbbbbb-0000-4000-8000-000000000002";
const wsA = { id: A, name: "Alpha", slug: "alpha", status: "active" };
const wsB = { id: B, name: "Bravo", slug: "bravo", status: "active" };

const healthy = (id) => ({
  id, status: "active", health: "healthy", access_mode: "full", can_write: true, realm: "r",
});

function reset() {
  globalThis.localStorage = makeStorage();
  state._resetForTests();
  ws._resetWorkspacesForTests();
  state.setState({ workspaces: [wsA, wsB], currentWorkspaceId: A });
}

// ─── The stale-response race ────────────────────────────────────────────────

test("PHASE 8: a slow response from workspace A is dropped after switching to B", async () => {
  reset();
  const gateA = deferred();

  globalThis.fetch = fetchStub([
    [`/v1/workspaces/${A}/users`, { gate: gateA.promise, body: { users: [{ id: "u1", username: "alice-in-A" }] } }],
    [`/v1/workspaces/${B}/users`, { body: { users: [{ id: "u2", username: "bob-in-B" }] } }],
  ]);

  // 1. A view in workspace A captures its context and starts a request.
  const tokenA = ws.captureWorkspaceToken();
  const requestA = apiTry(wsPath(A, "/users"), { signal: tokenA.signal });

  // 2. The operator switches to B before A answers.
  ws.selectWorkspace(B);

  // 3. B's request completes and would render.
  const tokenB = ws.captureWorkspaceToken();
  const responseB = await apiTry(wsPath(B, "/users"), { signal: tokenB.signal });
  assert.equal(ws.isWorkspaceStale(tokenB), false, "B is current, so B renders");
  assert.equal(responseB.data.users[0].username, "bob-in-B");

  // 4. NOW A answers.
  gateA.resolve();
  await requestA;

  // 5. The check that stops the defect. Whatever A returned — and it may have
  //    been aborted, which is the other half of the guard — its token is stale
  //    and the view must not commit it.
  assert.equal(ws.isWorkspaceStale(tokenA), true,
    "workspace A's in-flight response must be recognised as stale after the switch");
});

test("PHASE 8: switching ABORTS the outgoing workspace's in-flight requests", async () => {
  // The generation check is the correctness guarantee; the abort is what stops
  // the request being paid for at all. Both, because the abort can lose the
  // race with a response already on the wire.
  reset();
  const gate = deferred();
  globalThis.fetch = fetchStub([
    [`/v1/workspaces/${A}/users`, { gate: gate.promise, body: { users: [] } }],
  ]);

  const tokenA = ws.captureWorkspaceToken();
  const requestA = apiTry(wsPath(A, "/users"), { signal: tokenA.signal });

  ws.selectWorkspace(B);
  gate.resolve();
  const r = await requestA;

  assert.equal(r.ok, false, "the aborted request does not resolve as a success");
  assert.equal(r.status, 0, "no HTTP exchange completed");
});

test("PHASE 8: A → B → A is still stale for the FIRST A request", async () => {
  // The workspace id matches again, so an id-only check would wrongly accept
  // this. The generation is what catches it — and it matters, because a
  // mutation may have happened in B in between, or in A after the request
  // left.
  reset();
  const tokenA1 = ws.captureWorkspaceToken();

  ws.selectWorkspace(B);
  ws.selectWorkspace(A);

  assert.equal(ws.getCurrentWorkspaceId(), A, "we are back in A");
  assert.equal(ws.isWorkspaceStale(tokenA1), true,
    "the original A request predates two switches and must not be committed");
});

test("isWorkspaceStale rejects a missing or malformed token", () => {
  reset();
  assert.equal(ws.isWorkspaceStale(null), true);
  assert.equal(ws.isWorkspaceStale(undefined), true);
  assert.equal(ws.isWorkspaceStale({}), true);
});

test("a token captured for the current workspace is NOT stale", () => {
  reset();
  assert.equal(ws.isWorkspaceStale(ws.captureWorkspaceToken()), false);
});

// ─── Refetch on switch ──────────────────────────────────────────────────────

test("switching workspace forces the connection to be refetched, not reused", async () => {
  reset();
  globalThis.fetch = fetchStub([
    [`/v1/workspaces/${A}/connections`, { body: { connections: [healthy("conn_a")] } }],
    [`/v1/workspaces/${B}/connections`, { body: { connections: [healthy("conn_b")] } }],
  ]);

  await ws.loadActiveConnection(A);
  assert.equal(state.getState().wsConnection.id, "conn_a");

  ws.selectWorkspace(B);
  assert.equal(state.getState().wsConnection, null, "A's connection is cleared at the moment of the switch");

  await ws.loadActiveConnection(B);
  assert.equal(state.getState().wsConnection.id, "conn_b");
  assert.equal(globalThis.fetch.calls.length, 2, "each workspace was asked separately");
});

test("a connection response arriving after a switch does not overwrite the new workspace", async () => {
  reset();
  const gateA = deferred();
  globalThis.fetch = fetchStub([
    [`/v1/workspaces/${A}/connections`, { gate: gateA.promise, body: { connections: [healthy("conn_a")] } }],
    [`/v1/workspaces/${B}/connections`, { body: { connections: [healthy("conn_b")] } }],
  ]);

  const loadA = ws.loadActiveConnection(A);
  ws.selectWorkspace(B);
  await ws.loadActiveConnection(B);
  assert.equal(state.getState().wsConnection.id, "conn_b");

  gateA.resolve();
  await loadA;

  assert.equal(state.getState().wsConnection.id, "conn_b",
    "A's connection must not land on top of B's");
});

test("concurrent loads for the SAME workspace share one request", async () => {
  // Without dedup, the shell's load and the view's enterWorkspace both fire —
  // and worse, the view can see wsConnection still null and render
  // 'not connected' over a perfectly healthy workspace.
  reset();
  globalThis.fetch = fetchStub([
    [`/v1/workspaces/${A}/connections`, { delayMs: 5, body: { connections: [healthy("conn_a")] } }],
  ]);

  const [r1, r2] = await Promise.all([ws.loadActiveConnection(A), ws.loadActiveConnection(A)]);

  assert.equal(globalThis.fetch.calls.length, 1, "one request, not two");
  assert.equal(r1.connection.id, "conn_a");
  assert.equal(r2.connection.id, "conn_a");
});

// ─── enterWorkspace — the gate ──────────────────────────────────────────────

test("enterWorkspace resolves a DEEP LINK into the selected workspace", async () => {
  reset();
  state.setState({ currentWorkspaceId: A });
  globalThis.fetch = fetchStub([
    [`/v1/workspaces/${B}/connections`, { body: { connections: [healthy("conn_b")] } }],
  ]);

  const gate = await ws.enterWorkspace(B);

  assert.equal(gate.ok, true);
  assert.equal(ws.getCurrentWorkspaceId(), B, "the URL is the source of truth");
  assert.equal(gate.connectionState, ws.CONNECTION_STATE.HEALTHY);
});

test("enterWorkspace BLOCKS a workspace with no active connection", async () => {
  // Phase 11: identity views must not fire requests that /v1 refuses before it
  // contacts the provider. Firing them on every render is pure noise.
  reset();
  globalThis.fetch = fetchStub([
    [`/v1/workspaces/${A}/connections`, { body: { connections: [] } }],
  ]);

  const gate = await ws.enterWorkspace(A);

  assert.equal(gate.ok, false);
  assert.equal(gate.reason, ws.GATE.NO_CONNECTION);
  assert.equal(gate.connectionState, ws.CONNECTION_STATE.NONE);
});

test("enterWorkspace BLOCKS an archived workspace", async () => {
  reset();
  const archived = { id: A, name: "Alpha", slug: "alpha", status: "archived" };
  state.setState({ workspaces: [archived, wsB], currentWorkspaceId: A });
  globalThis.fetch = fetchStub([]);

  const gate = await ws.enterWorkspace(A);

  assert.equal(gate.ok, false);
  assert.equal(gate.reason, ws.GATE.ARCHIVED);
  assert.equal(globalThis.fetch.calls.length, 0, "archived is decided without touching the network");
});

test("enterWorkspace BLOCKS an unknown workspace id", async () => {
  reset();
  globalThis.fetch = fetchStub([]);

  const gate = await ws.enterWorkspace("ws_99999999-0000-4000-8000-000000000009");

  assert.equal(gate.ok, false);
  assert.equal(gate.reason, ws.GATE.UNKNOWN_WORKSPACE);
});

test("enterWorkspace reports NO_WORKSPACES on a fresh installation", async () => {
  reset();
  state.setState({ workspaces: [], currentWorkspaceId: null });
  globalThis.fetch = fetchStub([["/v1/workspaces", { body: { workspaces: [] } }]]);

  const gate = await ws.enterWorkspace(null);

  assert.equal(gate.ok, false);
  assert.equal(gate.reason, ws.GATE.NO_WORKSPACES);
});

test("enterWorkspace ALLOWS an unhealthy-but-active connection through", async () => {
  // Health is the verdict of the LAST verify, not a live fact. Refusing to try
  // would make the console strictly less capable than curl; the view proceeds
  // and surfaces the real /v1 error with a retry.
  reset();
  globalThis.fetch = fetchStub([
    [`/v1/workspaces/${A}/connections`, {
      body: { connections: [{ ...healthy("conn_a"), health: "unhealthy" }] },
    }],
  ]);

  const gate = await ws.enterWorkspace(A);

  assert.equal(gate.ok, true, "a stale unhealthy verdict must not lock the operator out");
  assert.equal(gate.connectionState, ws.CONNECTION_STATE.UNAVAILABLE);
});

test("enterWorkspace ALLOWS a read-only connection through, for reads", async () => {
  // Phase 11: reads stay usable; only mutation controls are disabled, which is
  // what writeBlockedReason drives.
  reset();
  globalThis.fetch = fetchStub([
    [`/v1/workspaces/${A}/connections`, {
      body: { connections: [{ ...healthy("conn_a"), access_mode: "read_only", can_write: false }] },
    }],
  ]);

  const gate = await ws.enterWorkspace(A);

  assert.equal(gate.ok, true);
  assert.equal(gate.connectionState, ws.CONNECTION_STATE.LIMITED);
});
