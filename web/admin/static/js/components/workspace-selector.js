// workspace-selector.js — the control that says which realm you are looking at.
//
// Placement: the topbar, leftmost in the actions cluster. The sidebar was the
// alternative, and it loses: the sidebar swaps its whole contents between
// Admin and Docs mode, so a selector living there would vanish exactly when an
// operator wants to check which workspace they left themselves in. The topbar
// is the only chrome present on every page.
//
// Scope: selection and state, nothing else. Creating, renaming and archiving
// live at /workspaces — a dropdown that can archive a realm's control plane is
// a dropdown someone archives a realm from by accident.

import { h } from "../lib/dom.js";
import { getState } from "../lib/state.js";
import { navigate, currentPath, isWorkspaceRoute, swapWorkspaceInPath, wsRoute } from "../lib/router.js";
import {
  getWorkspaces, getCurrentWorkspaceId, isWorkspaceSelectable,
  classifyConnection, CONNECTION_STATE,
} from "../lib/workspaces.js";
import { connectionPill } from "./ws-states.js";

/**
 * renderWorkspaceSelector — returns the selector element for the topbar.
 *
 * Called on every topbar draw, so it reads state fresh and holds none itself.
 */
export function renderWorkspaceSelector() {
  const state = getState();
  const workspaces = getWorkspaces();
  const currentId = getCurrentWorkspaceId();
  const current = workspaces.find((w) => w.id === currentId) || null;

  if (state.workspacesLoading && !workspaces.length) {
    return wrap(h("span", "muted text-xs", "loading workspaces…"));
  }

  // Zero workspaces is a product state, not an error, and it must not silently
  // fall back to the legacy /admin realm — that would show an operator a realm
  // they never selected and let them mutate it believing otherwise.
  if (!workspaces.length) {
    return wrap(
      h("button", {
        class: "btn btn-sm btn-primary",
        title: "Identity management needs a workspace before it can show you a realm",
        onclick: () => navigate("/workspaces"),
      }, "＋ Create a workspace"),
    );
  }

  // Archived workspaces are listed but never selectable: selecting one leads
  // to a view that can only say "this is archived".
  const selectable = workspaces.filter(isWorkspaceSelectable);

  const select = h("select", {
    class: "ws-select",
    "aria-label": "Active workspace",
    title: current ? `${current.name} (${current.id})` : "Select a workspace",
    onchange: (e) => switchTo(e.target.value),
  });

  if (!currentId) {
    select.appendChild(h("option", { value: "" }, "— select a workspace —"));
  }
  for (const w of selectable) {
    select.appendChild(h("option", { value: w.id, selected: w.id === currentId }, w.name || w.slug || w.id));
  }
  // A current workspace that is not selectable (archived while selected) still
  // needs to render, or the control would silently show someone else's name.
  if (current && !isWorkspaceSelectable(current)) {
    select.appendChild(h("option", { value: current.id, selected: true }, (current.name || current.id) + " (archived)"));
  }

  const connState = classifyConnection(current, state.wsConnection);

  return wrap(
    h("span", "ws-select-label", "Workspace"),
    select,
    // Connection health next to the name, because "which realm" and "can I do
    // anything to it" are the same question in practice.
    state.wsConnectionLoading
      ? h("span", "muted text-xs", "checking…")
      : connectionPill(connState, state.wsConnection),
    h("button", {
      class: "btn btn-ghost btn-xs",
      title: "Workspaces and connections",
      onclick: () => navigate(currentId ? wsRoute(currentId, "connections") : "/workspaces"),
    }, "⚙"),
  );
}

function wrap(...children) {
  return h("div", { class: "ws-selector", role: "group", "aria-label": "Workspace" }, ...children);
}

// switchTo changes workspace by NAVIGATING, not by mutating state directly.
//
// The route is the source of truth for the workspace (see lib/router.js), so
// driving the switch through the router keeps the URL, the back button and the
// selected state in agreement by construction. The actual state change happens
// in main.js's route handler, which is also what a deep link or a page reload
// goes through — one code path, not two.
function switchTo(nextId) {
  if (!nextId || nextId === getCurrentWorkspaceId()) return;

  const path = currentPath();
  // Stay on the same page in the new workspace where that makes sense; land on
  // its users page otherwise. swapWorkspaceInPath drops entity ids on purpose —
  // a user uuid from workspace A means nothing in B.
  navigate(isWorkspaceRoute(path) ? swapWorkspaceInPath(path, nextId) : wsRoute(nextId, "users"));
}

// _connectionStateForTests — exposed so the selector's state mapping can be
// pinned without a DOM.
export function _connectionStateForTests(workspace, connection) {
  const s = classifyConnection(workspace, connection);
  return { state: s, healthy: s === CONNECTION_STATE.HEALTHY };
}
