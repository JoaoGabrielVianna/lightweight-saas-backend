// workspace-state.test.mjs — loading, selecting, persisting and resolving the
// current workspace.
//
// The interesting cases are all the ones where the persisted preference and
// reality disagree: a workspace that was archived, deleted, or belongs to
// another installation. Every one of those must resolve DETERMINISTICALLY,
// because the alternative is a console pointed at something that cannot answer
// and an operator who cannot tell why.

import { test } from "node:test";
import assert from "node:assert/strict";
import { makeStorage, fetchStub, makeResponse } from "./helpers.mjs";

globalThis.localStorage = makeStorage();
globalThis.sessionStorage = makeStorage();

const state = await import("../lib/state.js");
const ws = await import("../lib/workspaces.js");

const A = "ws_aaaaaaaa-0000-4000-8000-000000000001";
const B = "ws_bbbbbbbb-0000-4000-8000-000000000002";
const ARCHIVED = "ws_cccccccc-0000-4000-8000-000000000003";

const wsA = { id: A, name: "Alpha", slug: "alpha", status: "active" };
const wsB = { id: B, name: "Bravo", slug: "bravo", status: "active" };
const wsArchived = { id: ARCHIVED, name: "Gone", slug: "gone", status: "archived" };

function reset(storage) {
  globalThis.localStorage = makeStorage(storage);
  state._resetForTests();
  ws._resetWorkspacesForTests();
}

// ─── pickWorkspaceId — the resolution rules, in isolation ───────────────────

test("resolution: the route wins over the persisted preference", () => {
  // A deep link is an explicit instruction. Honouring persistence over it
  // would break "open workspace B in a second tab".
  assert.equal(ws.pickWorkspaceId([wsA, wsB], { routeId: B, persistedId: A }), B);
});

test("resolution: the persisted preference wins when the route names none", () => {
  assert.equal(ws.pickWorkspaceId([wsA, wsB], { routeId: null, persistedId: B }), B);
});

test("resolution: falls back to the first active workspace", () => {
  assert.equal(ws.pickWorkspaceId([wsA, wsB], {}), A);
});

test("resolution: a persisted workspace that no longer EXISTS falls through", () => {
  assert.equal(ws.pickWorkspaceId([wsA, wsB], { persistedId: "ws_deleted-0000-4000-8000-000000000009" }), A);
});

test("resolution: a persisted workspace that has been ARCHIVED falls through", () => {
  // Archived workspaces refuse every identity operation before the provider is
  // contacted, so restoring the console into one is restoring it into a dead
  // end.
  assert.equal(ws.pickWorkspaceId([wsArchived, wsA], { persistedId: ARCHIVED }), A);
});

test("resolution: a ROUTE naming an archived workspace also falls through", () => {
  assert.equal(ws.pickWorkspaceId([wsArchived, wsB], { routeId: ARCHIVED }), B);
});

test("resolution: zero selectable workspaces resolves to null, never to a guess", () => {
  assert.equal(ws.pickWorkspaceId([], {}), null);
  assert.equal(ws.pickWorkspaceId([wsArchived], {}), null, "an all-archived list has nothing to select");
  assert.equal(ws.pickWorkspaceId([wsArchived], { persistedId: ARCHIVED }), null);
});

test("resolution: a malformed list does not throw", () => {
  assert.equal(ws.pickWorkspaceId(null, { persistedId: A }), null);
  assert.equal(ws.pickWorkspaceId(undefined, {}), null);
});

// ─── loadWorkspaces ─────────────────────────────────────────────────────────

test("loadWorkspaces fills state from GET /v1/workspaces", async () => {
  reset();
  globalThis.fetch = fetchStub([["/v1/workspaces", { body: { workspaces: [wsA, wsB], count: 2 } }]]);

  const r = await ws.loadWorkspaces();

  assert.equal(r.ok, true);
  assert.equal(ws.getWorkspaces().length, 2);
  assert.equal(state.getState().workspacesLoading, false);
  assert.equal(state.getState().workspacesError, null);
});

test("loadWorkspaces records the error instead of throwing", async () => {
  // A console that cannot list workspaces must still render a shell that
  // explains why. Every caller is a boot path or a retry button.
  reset();
  globalThis.fetch = fetchStub([["/v1/workspaces", {
    status: 500,
    body: { error: { code: "internal_error", message: "Internal error", request_id: "rid-1" } },
  }]]);

  const r = await ws.loadWorkspaces();

  assert.equal(r.ok, false);
  assert.equal(ws.getWorkspaces().length, 0);
  assert.equal(state.getState().workspacesError.code, "internal_error");
  assert.equal(state.getState().workspacesError.requestId, "rid-1");
});

// ─── initWorkspaces — boot ──────────────────────────────────────────────────

test("boot: with no persisted selection, the first active workspace is chosen and persisted", async () => {
  reset();
  globalThis.fetch = fetchStub([["/v1/workspaces", { body: { workspaces: [wsA, wsB] } }]]);

  const r = await ws.initWorkspaces({});

  assert.equal(r.workspaceId, A);
  assert.equal(ws.getCurrentWorkspaceId(), A);
  assert.equal(ws.readPersistedWorkspaceId(), A, "the choice survives a reload");
});

test("boot: a persisted selection is restored", async () => {
  reset({ lw_selected_workspace: B });
  globalThis.fetch = fetchStub([["/v1/workspaces", { body: { workspaces: [wsA, wsB] } }]]);

  await ws.initWorkspaces({});

  assert.equal(ws.getCurrentWorkspaceId(), B);
});

test("boot: an INVALID persisted selection is replaced AND cleaned out of storage", async () => {
  // Leaving the dead id in storage means every reload re-runs the same failed
  // lookup, and the operator never gets a stable answer.
  reset({ lw_selected_workspace: ARCHIVED });
  globalThis.fetch = fetchStub([["/v1/workspaces", { body: { workspaces: [wsArchived, wsB] } }]]);

  await ws.initWorkspaces({});

  assert.equal(ws.getCurrentWorkspaceId(), B);
  assert.equal(ws.readPersistedWorkspaceId(), B);
});

test("boot: a deep link overrides the persisted selection", async () => {
  reset({ lw_selected_workspace: A });
  globalThis.fetch = fetchStub([["/v1/workspaces", { body: { workspaces: [wsA, wsB] } }]]);

  await ws.initWorkspaces({ routeId: B });

  assert.equal(ws.getCurrentWorkspaceId(), B);
  assert.equal(ws.readPersistedWorkspaceId(), B, "the deep link becomes the new preference");
});

test("boot: ZERO workspaces leaves nothing selected and clears any stale preference", async () => {
  // Emphatically NOT a silent fallback to /admin/*, which would show a realm
  // nobody chose and let an operator mutate it believing otherwise.
  reset({ lw_selected_workspace: A });
  globalThis.fetch = fetchStub([["/v1/workspaces", { body: { workspaces: [] } }]]);

  const r = await ws.initWorkspaces({});

  assert.equal(r.workspaceId, null);
  assert.equal(ws.getCurrentWorkspaceId(), null);
  assert.equal(ws.readPersistedWorkspaceId(), null);
});

test("boot: the first request of a session is not stale to itself", async () => {
  // Seeding the selection must NOT bump the generation — otherwise the token
  // captured by the very first view would already fail isWorkspaceStale.
  reset();
  globalThis.fetch = fetchStub([["/v1/workspaces", { body: { workspaces: [wsA] } }]]);

  await ws.initWorkspaces({});
  const token = ws.captureWorkspaceToken();

  assert.equal(ws.isWorkspaceStale(token), false);
});

// ─── Selection ──────────────────────────────────────────────────────────────

test("selectWorkspace switches, persists, and bumps the generation", () => {
  reset();
  state.setState({ workspaces: [wsA, wsB], currentWorkspaceId: A });

  const before = ws.workspaceGeneration();
  const changed = ws.selectWorkspace(B);

  assert.equal(changed, true);
  assert.equal(ws.getCurrentWorkspaceId(), B);
  assert.equal(ws.readPersistedWorkspaceId(), B);
  assert.equal(ws.workspaceGeneration(), before + 1);
});

test("selectWorkspace to the SAME workspace is a no-op", () => {
  // Re-running the teardown would close a dialog the operator has open in the
  // workspace they are still in.
  reset();
  state.setState({ workspaces: [wsA], currentWorkspaceId: A });
  let switches = 0;
  ws.onWorkspaceSwitch(() => switches++);

  const changed = ws.selectWorkspace(A);

  assert.equal(changed, false);
  assert.equal(switches, 0);
});

test("selectWorkspace clears the outgoing workspace's connection state", () => {
  // Workspace A's connection health must never decorate workspace B.
  reset();
  state.setState({
    workspaces: [wsA, wsB],
    currentWorkspaceId: A,
    wsConnection: { id: "conn_a", status: "active", health: "healthy", can_write: true },
  });

  ws.selectWorkspace(B);

  assert.equal(state.getState().wsConnection, null);
});

test("switch listeners fire with {from, to} and a throwing listener does not block the rest", () => {
  reset();
  state.setState({ workspaces: [wsA, wsB], currentWorkspaceId: A });
  const seen = [];
  ws.onWorkspaceSwitch(() => { throw new Error("a listener blew up"); });
  ws.onWorkspaceSwitch((e) => seen.push(e));

  ws.selectWorkspace(B);

  assert.deepEqual(seen, [{ from: A, to: B }]);
});

test("onWorkspaceSwitch returns a working unsubscribe", () => {
  reset();
  state.setState({ workspaces: [wsA, wsB], currentWorkspaceId: A });
  let calls = 0;
  const off = ws.onWorkspaceSwitch(() => calls++);

  ws.selectWorkspace(B);
  off();
  ws.selectWorkspace(A);

  assert.equal(calls, 1);
});

test("a workspace archived under the operator stops being selectable", () => {
  assert.equal(ws.isWorkspaceSelectable(wsA), true);
  assert.equal(ws.isWorkspaceSelectable(wsArchived), false);
  assert.equal(ws.isWorkspaceSelectable(null), false);
});

// ─── Connection classification ──────────────────────────────────────────────

test("classifyConnection maps the backend's own vocabulary, never a re-derived one", () => {
  const S = ws.CONNECTION_STATE;
  const active = (over) => ({ id: "conn_1", status: "active", health: "healthy", can_write: true, ...over });

  assert.equal(ws.classifyConnection(wsA, active()), S.HEALTHY);
  assert.equal(ws.classifyConnection(wsA, active({ can_write: false, access_mode: "read_only" })), S.LIMITED);
  assert.equal(ws.classifyConnection(wsA, active({ can_write: false, access_mode: "limited" })), S.LIMITED);
  assert.equal(ws.classifyConnection(wsA, active({ health: "unhealthy" })), S.UNAVAILABLE);
  assert.equal(ws.classifyConnection(wsA, active({ status: "draft" })), S.NONE,
    "a draft is not what the workspace routes through");
  assert.equal(ws.classifyConnection(wsA, null), S.NONE);
  assert.equal(ws.classifyConnection(wsArchived, active()), S.ARCHIVED,
    "an archived workspace outranks a healthy connection");
});

test("canWriteHere is true ONLY for a healthy, write-capable connection", () => {
  const base = { workspaces: [wsA], currentWorkspaceId: A };
  const conn = (over) => ({ id: "c", status: "active", health: "healthy", can_write: true, ...over });

  assert.equal(ws.canWriteHere({ ...base, wsConnection: conn() }), true);
  assert.equal(ws.canWriteHere({ ...base, wsConnection: conn({ can_write: false }) }), false);
  assert.equal(ws.canWriteHere({ ...base, wsConnection: conn({ health: "unhealthy" }) }), false);
  assert.equal(ws.canWriteHere({ ...base, wsConnection: null }), false,
    "not loaded yet must not enable a Delete button");
  assert.equal(ws.canWriteHere({ workspaces: [wsArchived], currentWorkspaceId: ARCHIVED, wsConnection: conn() }), false);
});

test("the console reads can_write, NOT access_mode — one verdict, one owner", () => {
  // If the console re-derived writability from access_mode it would have to
  // know that `unknown` permits writes and `read_only` does not, in a second
  // place, and the two would eventually disagree. A hypothetical future mode
  // the console has never heard of must follow the backend's verdict.
  const conn = { id: "c", status: "active", health: "healthy", access_mode: "some_future_mode", can_write: true };
  assert.equal(ws.classifyConnection(wsA, conn), ws.CONNECTION_STATE.HEALTHY);

  const refused = { ...conn, access_mode: "full", can_write: false };
  assert.equal(ws.classifyConnection(wsA, refused), ws.CONNECTION_STATE.LIMITED);
});

// ─── Storage resilience ─────────────────────────────────────────────────────

test("a localStorage that throws does not take the console down", () => {
  // Safari private mode throws on access. Losing the console over a UI
  // preference is not a trade worth making.
  globalThis.localStorage = {
    getItem() { throw new Error("SecurityError"); },
    setItem() { throw new Error("SecurityError"); },
    removeItem() { throw new Error("SecurityError"); },
  };
  assert.equal(ws.readPersistedWorkspaceId(), null);
  assert.doesNotThrow(() => ws.persistWorkspaceId(A));
});

test("a persisted value that is not a workspace id is ignored", () => {
  globalThis.localStorage = makeStorage({ lw_selected_workspace: "conn_not-a-workspace" });
  assert.equal(ws.readPersistedWorkspaceId(), null);
});
