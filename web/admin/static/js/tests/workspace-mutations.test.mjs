// workspace-mutations.test.mjs — Phase 9: a mutation composed in one
// workspace must never reach another.
//
// The defect class, concretely:
//
//   operator opens "Delete user <uuid>" in workspace A
//     → switches to workspace B
//       → clicks Delete
//
// The dialog's closure still holds A's user id. If the request were built from
// whatever workspace is current at click time, it would go to B — where it
// either 404s or, in a realm cloned from A's, deletes a DIFFERENT person.
//
// Two independent locks are tested here:
//
//   1. a switch CLOSES every open dialog (components/modal.js + the switch
//      listener registered in main.js);
//   2. wsMutate REFUSES to send an action whose captured workspace is no
//      longer current, even if a click somehow lands.

import { test } from "node:test";
import assert from "node:assert/strict";
import { makeStorage, fetchStub, deferred, installDOM } from "./helpers.mjs";

globalThis.localStorage = makeStorage();
globalThis.sessionStorage = makeStorage();
installDOM();

const state = await import("../lib/state.js");
const ws = await import("../lib/workspaces.js");
const { openModal, closeAllModals, openModalCount } = await import("../components/modal.js");

const A = "ws_aaaaaaaa-0000-4000-8000-000000000001";
const B = "ws_bbbbbbbb-0000-4000-8000-000000000002";
const wsA = { id: A, name: "Alpha", slug: "alpha", status: "active" };
const wsB = { id: B, name: "Bravo", slug: "bravo", status: "active" };

function reset() {
  globalThis.localStorage = makeStorage();
  state._resetForTests();
  ws._resetWorkspacesForTests();
  closeAllModals();
  state.setState({ workspaces: [wsA, wsB], currentWorkspaceId: A });
}

// ─── Lock 1: the switch tears the dialog down ───────────────────────────────

test("PHASE 9: switching workspace closes every open dialog", () => {
  reset();
  // This is the wiring main.js installs at boot.
  ws.onWorkspaceSwitch(() => closeAllModals());

  openModal({ title: "Delete user?", body: null, actions: [{ label: "Delete" }] });
  openModal({ title: "Assign role", body: null, actions: [{ label: "Assign" }] });
  assert.equal(openModalCount(), 2);

  ws.selectWorkspace(B);

  assert.equal(openModalCount(), 0,
    "a confirmation left open across a switch is the setup for mutating the wrong realm");
});

test("PHASE 9: a dialog's onClose still runs when the switch closes it", () => {
  reset();
  ws.onWorkspaceSwitch(() => closeAllModals());
  let closed = 0;
  openModal({ title: "x", body: null, onClose: () => closed++ });

  ws.selectWorkspace(B);

  assert.equal(closed, 1, "views rely on onClose to clear their own pending state");
});

test("closeAllModals is safe with nothing open, and idempotent", () => {
  reset();
  assert.doesNotThrow(() => closeAllModals());
  openModal({ title: "x", body: null });
  closeAllModals();
  closeAllModals();
  assert.equal(openModalCount(), 0);
});

test("a modal closed normally deregisters itself", () => {
  reset();
  const close = openModal({ title: "x", body: null });
  assert.equal(openModalCount(), 1);
  close();
  assert.equal(openModalCount(), 0, "a closed modal must not linger in the registry");
});

// ─── Lock 2: wsMutate refuses a cross-workspace action ──────────────────────

test("PHASE 9: wsMutate REFUSES an action composed for a workspace we have left", async () => {
  reset();
  globalThis.fetch = fetchStub([[/./, { status: 204 }]]);

  // The dialog captured workspace A and a user id from A's realm.
  const capturedWorkspace = A;
  const capturedUserId = "user-uuid-from-A";

  ws.selectWorkspace(B);

  const res = await ws.wsMutate(capturedWorkspace, "/users/" + capturedUserId, { method: "DELETE" });

  assert.equal(res.ok, false);
  assert.equal(res.wrongWorkspace, true);
  assert.equal(globalThis.fetch.calls.length, 0,
    "NOTHING may be sent — not to A, and above all not to B");
  assert.match(res.error.message, /workspace you have since left/i);
});

test("PHASE 9: the refusal never rewrites the action onto the CURRENT workspace", async () => {
  reset();
  globalThis.fetch = fetchStub([[/./, { status: 204 }]]);
  ws.selectWorkspace(B);

  await ws.wsMutate(A, "/users/user-uuid-from-A", { method: "DELETE" });

  const touchedB = globalThis.fetch.urls().some((u) => u.includes(B));
  assert.equal(touchedB, false, "silently retargeting the action at B is the catastrophic outcome");
});

test("wsMutate sends to the workspace it was given, and only that one", async () => {
  reset();
  globalThis.fetch = fetchStub([[/./, { status: 204 }]]);

  const res = await ws.wsMutate(A, "/users/u1", { method: "DELETE" });

  assert.equal(res.ok, true);
  assert.equal(globalThis.fetch.calls.length, 1);
  assert.equal(globalThis.fetch.urls()[0], `/v1/workspaces/${A}/users/u1`);
  assert.equal(globalThis.fetch.calls[0].method, "DELETE");
});

test("wsMutate refuses a missing workspace id rather than guessing", async () => {
  reset();
  globalThis.fetch = fetchStub([[/./, { status: 204 }]]);

  const res = await ws.wsMutate(null, "/users/u1", { method: "DELETE" });

  assert.equal(res.wrongWorkspace, true);
  assert.equal(globalThis.fetch.calls.length, 0);
});

test("a switch DURING a mutation marks the result stale, so it cannot refresh the new workspace", async () => {
  // The mutation may or may not have been applied to the outgoing workspace.
  // What must not happen is its success toast and its refetch landing on the
  // workspace the operator is now looking at.
  reset();
  const gate = deferred();
  globalThis.fetch = fetchStub([[/./, { gate: gate.promise, status: 204 }]]);

  const pending = ws.wsMutate(A, "/users/u1", { method: "DELETE" });
  ws.selectWorkspace(B);
  gate.resolve();
  const res = await pending;

  assert.equal(res.stale, true, "the view must skip its refresh and its toast");
});

// ─── Write gating from the connection's proven capability ───────────────────

test("a read-only connection blocks writes at the CONSOLE, before the network", async () => {
  // TD-024 made access_mode trustworthy; this is the console consuming it.
  // The button being disabled is the operator-facing half; can_write is the
  // fact it is derived from.
  reset();
  globalThis.fetch = fetchStub([
    [`/v1/workspaces/${A}/connections`, {
      body: {
        connections: [{
          id: "conn_a", status: "active", health: "healthy",
          access_mode: "read_only", can_write: false, realm: "r",
        }],
      },
    }],
  ]);

  const gate = await ws.enterWorkspace(A);

  assert.equal(gate.ok, true, "reads still work");
  assert.equal(gate.connectionState, ws.CONNECTION_STATE.LIMITED);
  assert.equal(ws.canWriteHere(), false, "every mutation control must be disabled");
});

test("a write-capable connection enables writes", async () => {
  reset();
  globalThis.fetch = fetchStub([
    [`/v1/workspaces/${A}/connections`, {
      body: {
        connections: [{
          id: "conn_a", status: "active", health: "healthy",
          access_mode: "full", can_write: true, realm: "r",
        }],
      },
    }],
  ]);

  await ws.enterWorkspace(A);

  assert.equal(ws.canWriteHere(), true);
});

test("an unloaded connection does NOT enable writes", async () => {
  // Enabling a Delete button on an assumption is the failure mode this whole
  // slice is about.
  reset();
  assert.equal(state.getState().wsConnection, null);
  assert.equal(ws.canWriteHere(), false);
});
