// workspace-selector.test.mjs — the control that answers "which realm am I
// about to change?".
//
// Placement is in the topbar, and the reason is testable only by reading the
// code, but three behaviours are not:
//
//   - the selected workspace is RENDERED, so an operator can see it without
//     opening the dropdown;
//   - archived workspaces are not selectable;
//   - zero workspaces produces an intentional state, and emphatically NOT a
//     silent fall back to the legacy /admin realm.

import { test } from "node:test";
import assert from "node:assert/strict";
import { makeStorage, installDOM } from "./helpers.mjs";

globalThis.localStorage = makeStorage();
globalThis.sessionStorage = makeStorage();
installDOM();

// The selector reaches the router for currentPath()/navigate(). The hash is a
// getter/setter that normalizes a leading "#", exactly as a real Location
// does — router.navigate assigns a bare path and relies on that.
let _hash = "";
globalThis.window = {
  location: {
    get hash() { return _hash; },
    set hash(v) { _hash = String(v).startsWith("#") ? String(v) : "#" + String(v); },
  },
  addEventListener() {},
  scrollTo() {},
};

const state = await import("../lib/state.js");
const ws = await import("../lib/workspaces.js");
const router = await import("../lib/router.js");
const { renderWorkspaceSelector } = await import("../components/workspace-selector.js");

// Capture navigation by watching the hash the router writes.
const originalHash = () => globalThis.window.location.hash;

const A = "ws_aaaaaaaa-0000-4000-8000-000000000001";
const B = "ws_bbbbbbbb-0000-4000-8000-000000000002";
const ARCHIVED = "ws_cccccccc-0000-4000-8000-000000000003";

const wsA = { id: A, name: "Alpha", slug: "alpha", status: "active" };
const wsB = { id: B, name: "Bravo", slug: "bravo", status: "active" };
const wsArchived = { id: ARCHIVED, name: "Gone", slug: "gone", status: "archived" };

const healthy = { id: "conn_a", status: "active", health: "healthy", access_mode: "full", can_write: true };

function reset(patch) {
  globalThis.localStorage = makeStorage();
  state._resetForTests();
  ws._resetWorkspacesForTests();
  globalThis.window.location.hash = "#/workspaces/" + A + "/users";
  if (patch) state.setState(patch);
}

function optionsOf(el) {
  return el.querySelectorAll("option").map((o) => ({
    value: o.getAttribute("value"),
    label: o.textContent,
    // lib/dom.js renders a true boolean prop as a valueless attribute, so
    // presence — not truthiness of the value — is what marks the selection.
    selected: Object.prototype.hasOwnProperty.call(o.attributes, "selected"),
  }));
}

// ─── Rendering ──────────────────────────────────────────────────────────────

test("the selected workspace is rendered by NAME, not by id", () => {
  reset({ workspaces: [wsA, wsB], currentWorkspaceId: B, wsConnection: healthy });

  const el = renderWorkspaceSelector();

  assert.match(el.textContent, /Bravo/, "the operator must see which realm they are in");
  const selected = optionsOf(el).find((o) => o.selected);
  assert.equal(selected.value, B);
});

test("connection health is rendered alongside the name", () => {
  // "Which realm" and "can I do anything to it" are the same question in
  // practice, so they share one control.
  reset({ workspaces: [wsA], currentWorkspaceId: A, wsConnection: healthy });

  const el = renderWorkspaceSelector();

  assert.match(el.textContent, /Healthy/);
});

test("a read-only connection is rendered as read-only, not as healthy", () => {
  reset({
    workspaces: [wsA],
    currentWorkspaceId: A,
    wsConnection: { ...healthy, access_mode: "read_only", can_write: false },
  });

  const el = renderWorkspaceSelector();

  assert.match(el.textContent, /Read-only/);
  assert.doesNotMatch(el.textContent, /Healthy/);
});

test("a workspace with no active connection says so in the selector", () => {
  reset({ workspaces: [wsA], currentWorkspaceId: A, wsConnection: null });

  const el = renderWorkspaceSelector();

  assert.match(el.textContent, /No connection/);
});

// ─── Selectability ──────────────────────────────────────────────────────────

test("archived workspaces are not offered as options", () => {
  reset({ workspaces: [wsA, wsArchived, wsB], currentWorkspaceId: A, wsConnection: healthy });

  const values = optionsOf(renderWorkspaceSelector()).map((o) => o.value);

  assert.deepEqual(values, [A, B]);
  assert.ok(!values.includes(ARCHIVED), "selecting an archived workspace leads only to a dead end");
});

test("a workspace archived WHILE selected still renders, labelled", () => {
  // Dropping it would silently show someone else's name in the control.
  reset({ workspaces: [wsArchived, wsB], currentWorkspaceId: ARCHIVED, wsConnection: null });

  const opts = optionsOf(renderWorkspaceSelector());
  const current = opts.find((o) => o.value === ARCHIVED);

  assert.ok(current, "the current workspace is always represented");
  assert.match(current.label, /archived/i);
});

test("with a single workspace the selector still renders it", () => {
  reset({ workspaces: [wsA], currentWorkspaceId: A, wsConnection: healthy });

  const el = renderWorkspaceSelector();

  assert.match(el.textContent, /Alpha/);
  assert.equal(optionsOf(el).length, 1);
});

// ─── Zero workspaces ────────────────────────────────────────────────────────

test("ZERO workspaces renders an intentional call to action, never a silent fallback", () => {
  reset({ workspaces: [], currentWorkspaceId: null });

  const el = renderWorkspaceSelector();

  assert.match(el.textContent, /Create a workspace/i);
  // The failure this guards: falling back to /admin/* would show a realm
  // nobody chose and let an operator mutate it believing otherwise.
  assert.doesNotMatch(el.textContent, /admin/i);
  assert.equal(el.querySelectorAll("select").length, 0, "there is nothing to select");
});

test("a loading list renders a loading state rather than an empty dropdown", () => {
  reset({ workspaces: [], currentWorkspaceId: null, workspacesLoading: true });

  const el = renderWorkspaceSelector();

  assert.match(el.textContent, /loading/i);
  assert.doesNotMatch(el.textContent, /Create a workspace/i,
    "'no workspaces' must not flash before the list has arrived");
});

// ─── Switching drives the ROUTE ─────────────────────────────────────────────

test("choosing a workspace navigates, keeping the operator on the same page", () => {
  // The route is the source of truth for the workspace, so the selector must
  // navigate rather than mutate state. One code path serves the selector, a
  // deep link, the back button and a second tab.
  reset({ workspaces: [wsA, wsB], currentWorkspaceId: A, wsConnection: healthy });
  globalThis.window.location.hash = "#/workspaces/" + A + "/roles";

  const el = renderWorkspaceSelector();
  const select = el.querySelector("select");
  select.dispatch("change", { target: { value: B } });

  assert.equal(globalThis.window.location.hash, "#" + router.wsRoute(B, "roles"),
    "same page, new workspace");
});

test("choosing a workspace from an installation-scoped page lands on its users", () => {
  reset({ workspaces: [wsA, wsB], currentWorkspaceId: A, wsConnection: healthy });
  globalThis.window.location.hash = "#/overview";

  const el = renderWorkspaceSelector();
  el.querySelector("select").dispatch("change", { target: { value: B } });

  assert.equal(globalThis.window.location.hash, "#" + router.wsRoute(B, "users"));
});

test("re-choosing the CURRENT workspace does nothing", () => {
  reset({ workspaces: [wsA, wsB], currentWorkspaceId: A, wsConnection: healthy });
  const before = originalHash();

  const el = renderWorkspaceSelector();
  el.querySelector("select").dispatch("change", { target: { value: A } });

  assert.equal(originalHash(), before);
});
