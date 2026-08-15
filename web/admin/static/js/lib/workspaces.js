// workspaces.js — the console's Workspace context.
//
// Before Slice 6 the console had no Workspace concept: every request went to
// /admin/*, implicitly scoped to the one realm in KEYCLOAK_*. This module is
// what makes "which realm am I looking at?" a first-class, switchable answer.
//
// ─── Design constraints ─────────────────────────────────────────────────────
//
// No state library. The console already has lib/state.js (a pub/sub store) and
// adding Redux or Zustand for four keys would be a larger change than the
// feature. Workspace state lives in that store under `ws*` keys and this
// module is the only writer.
//
// No DOM. Everything here is testable under `node --test` with a stubbed
// fetch, sessionStorage and localStorage. The visual parts live in
// components/workspace-selector.js and views/workspaces.js.
//
// ─── The isolation problem this module exists to solve ──────────────────────
//
// Two workspaces point at two different Keycloak realms. Reading A's users
// into B's table is not a cosmetic bug — it is the operator deleting the wrong
// person. Three mechanisms guard it, and all three live here:
//
//   1. a GENERATION counter, bumped on every switch, so an in-flight response
//      from A can be recognised and dropped after B is selected;
//   2. an ABORT signal per workspace, so those responses mostly never arrive;
//   3. a SWITCH LISTENER registry, so open dialogs, selected entities and
//      pending destructive actions belonging to A are torn down at the moment
//      B becomes current.
//
// Views never re-implement any of it: they capture a token, and check it.

import { api, apiTry, wsPath } from "./api.js";
import { getState, setState } from "./state.js";

// WORKSPACE_STORAGE_KEY — the selected workspace id, per browser, per
// installation. localStorage rather than sessionStorage so a closed tab does
// not lose the operator's context; it is a UI preference, not a credential.
//
// NOTHING SECRET IS EVER PERSISTED HERE. Connection secrets never reach the
// browser at all (the API has no endpoint that returns one), and the id stored
// is the same public `ws_` value that appears in every URL.
export const WORKSPACE_STORAGE_KEY = "lw_selected_workspace";

// ─── Generation + abort ─────────────────────────────────────────────────────

let _generation = 0;
let _controller = null;

// _switchListeners are invoked synchronously on every workspace switch, before
// any new data is requested. Registered by the shell (close modals) and by
// views that hold entity state.
const _switchListeners = new Set();

/**
 * onWorkspaceSwitch registers a teardown callback. Returns an unsubscribe fn.
 *
 * The callback receives {from, to} — the previous and next workspace ids,
 * either of which may be null.
 */
export function onWorkspaceSwitch(fn) {
  _switchListeners.add(fn);
  return () => _switchListeners.delete(fn);
}

/** workspaceGeneration — how many switches have happened. Test seam. */
export function workspaceGeneration() {
  return _generation;
}

/**
 * captureWorkspaceToken — take a snapshot of the workspace context before an
 * async call, so the caller can tell afterwards whether it is still current.
 *
 * Usage in a view:
 *
 *   const tok = captureWorkspaceToken();
 *   const r = await apiTry(wsPath(tok.workspaceId, "/users"));
 *   if (isWorkspaceStale(tok)) return;   // operator switched; drop the result
 *
 * @returns {{generation: number, workspaceId: string|null, signal: AbortSignal|undefined}}
 */
export function captureWorkspaceToken() {
  // The controller is created lazily rather than at boot so that a token is
  // never abort-less. Depending on a boot step to have run first would mean
  // any request issued before it — or in a test that seeded state directly —
  // silently loses the abort half of the isolation guarantee, while still
  // LOOKING guarded.
  if (!_controller && typeof AbortController === "function") _controller = new AbortController();
  return {
    generation: _generation,
    workspaceId: getCurrentWorkspaceId(),
    signal: _controller ? _controller.signal : undefined,
  };
}

/**
 * isWorkspaceStale — true when the token no longer describes the current
 * workspace context, and its result must NOT be committed to the DOM.
 *
 * Both halves are checked, and neither is redundant:
 *
 *   - the generation catches a switch A → B → A, where the id matches again
 *     but the in-flight request belongs to a context the operator has already
 *     left and returned to (its data may be older than a mutation performed in
 *     between);
 *   - the id catches a request issued for a workspace that is no longer
 *     selected even when no counted switch occurred (initial resolution,
 *     a workspace disappearing from the list).
 */
export function isWorkspaceStale(token) {
  if (!token) return true;
  if (token.generation !== _generation) return true;
  return token.workspaceId !== getCurrentWorkspaceId();
}

// ─── Reading state ──────────────────────────────────────────────────────────

/** getWorkspaces — the loaded list, always an array. */
export function getWorkspaces() {
  return getState().workspaces || [];
}

/** getCurrentWorkspaceId — the selected `ws_` id, or null. */
export function getCurrentWorkspaceId() {
  return getState().currentWorkspaceId || null;
}

/** getCurrentWorkspace — the selected workspace object, or null. */
export function getCurrentWorkspace() {
  const id = getCurrentWorkspaceId();
  if (!id) return null;
  return getWorkspaces().find((w) => w.id === id) || null;
}

/** isWorkspaceSelectable — archived workspaces cannot serve identity operations. */
export function isWorkspaceSelectable(ws) {
  return !!ws && ws.status === "active";
}

// ─── Persistence ────────────────────────────────────────────────────────────

// readPersistedWorkspaceId / persistWorkspaceId wrap localStorage in try/catch
// because Safari's private mode throws on access, and losing the console
// entirely over a preference is not a trade worth making.

export function readPersistedWorkspaceId() {
  try {
    if (typeof localStorage === "undefined") return null;
    const v = localStorage.getItem(WORKSPACE_STORAGE_KEY);
    return typeof v === "string" && v.startsWith("ws_") ? v : null;
  } catch {
    return null;
  }
}

export function persistWorkspaceId(id) {
  try {
    if (typeof localStorage === "undefined") return;
    if (id) localStorage.setItem(WORKSPACE_STORAGE_KEY, id);
    else localStorage.removeItem(WORKSPACE_STORAGE_KEY);
  } catch { /* preference storage is best-effort */ }
}

// ─── Selection resolution ───────────────────────────────────────────────────

/**
 * pickWorkspaceId — decide which workspace should be current.
 *
 * Pure: no state reads, no storage, no network. This is the function the
 * "invalid persisted selection" requirement lives in, and having it pure is
 * what makes every branch of it directly testable.
 *
 * Precedence, highest first:
 *
 *   1. the id in the URL — a deep link is an explicit instruction, and honouring
 *      persistence over it would break "open workspace B in a second tab";
 *   2. the persisted id;
 *   3. the first selectable workspace;
 *   4. null.
 *
 * At every level the candidate must exist in the list AND be selectable
 * (active). A persisted id naming a workspace that has since been archived or
 * deleted falls through deterministically rather than leaving the console
 * pointed at something that cannot answer.
 *
 * @param {Array} workspaces
 * @param {{routeId?: string|null, persistedId?: string|null}} opts
 * @returns {string|null}
 */
export function pickWorkspaceId(workspaces, { routeId = null, persistedId = null } = {}) {
  const list = Array.isArray(workspaces) ? workspaces : [];
  const usable = (id) => !!id && list.some((w) => w.id === id && isWorkspaceSelectable(w));

  if (usable(routeId)) return routeId;
  if (usable(persistedId)) return persistedId;

  const firstActive = list.find(isWorkspaceSelectable);
  return firstActive ? firstActive.id : null;
}

// ─── Mutating state ─────────────────────────────────────────────────────────

/**
 * selectWorkspace — make `id` the current workspace.
 *
 * Everything that must happen exactly once per switch happens here, in this
 * order, and the order is the contract:
 *
 *   1. abort in-flight requests belonging to the outgoing workspace;
 *   2. bump the generation, so any response that still lands is recognisably
 *      stale even if the abort lost the race;
 *   3. clear workspace-scoped state (connection, cached entity selections);
 *   4. notify switch listeners, so dialogs and pending actions are torn down
 *      BEFORE any new data can render behind them;
 *   5. publish the new id.
 *
 * Selecting the already-current workspace is a no-op — re-running the teardown
 * would close a modal the operator has open in the workspace they are still in.
 *
 * @param {string|null} id
 * @returns {boolean} whether the current workspace changed
 */
export function selectWorkspace(id) {
  const next = id || null;
  const prev = getCurrentWorkspaceId();
  if (prev === next) return false;

  if (_controller) {
    try { _controller.abort(); } catch { /* older browsers, or already aborted */ }
  }
  _controller = typeof AbortController === "function" ? new AbortController() : null;
  _generation++;
  // The in-flight connection load belonged to the outgoing workspace and has
  // just been aborted; forget it so the incoming workspace starts its own.
  _connectionInFlight = null;

  // Workspace-scoped derived state. Anything here describes the OUTGOING
  // workspace and must not survive into the incoming one.
  setState({
    currentWorkspaceId: next,
    wsConnection: null,
    wsConnectionLoading: false,
    wsConnectionError: null,
  });
  persistWorkspaceId(next);

  for (const fn of Array.from(_switchListeners)) {
    try { fn({ from: prev, to: next }); } catch (e) { console.error("workspace switch listener failed:", e); }
  }
  return true;
}

/**
 * loadWorkspaces — GET /v1/workspaces into state.
 *
 * Failure is recorded, not thrown: a console that cannot list workspaces must
 * still render a shell explaining why, and every caller here is a boot path or
 * a refresh button.
 *
 * @returns {Promise<{ok: boolean, workspaces: Array, error: (Error|null)}>}
 */
export async function loadWorkspaces() {
  setState({ workspacesLoading: true, workspacesError: null });
  const r = await apiTry("/v1/workspaces");

  if (!r.ok) {
    setState({ workspacesLoading: false, workspacesError: r.error, workspaces: [] });
    return { ok: false, workspaces: [], error: r.error };
  }
  const workspaces = Array.isArray(r.data?.workspaces) ? r.data.workspaces : [];
  setState({ workspacesLoading: false, workspacesError: null, workspaces });
  return { ok: true, workspaces, error: null };
}

/**
 * initWorkspaces — the boot sequence: load the list, then resolve which one is
 * current from the route, the persisted preference, or the list itself.
 *
 * Returns the resolved id so the caller can redirect a workspace-less URL.
 */
export async function initWorkspaces({ routeId = null } = {}) {
  const { ok, workspaces, error } = await loadWorkspaces();
  if (!ok) return { ok: false, workspaceId: null, error };

  const picked = pickWorkspaceId(workspaces, {
    routeId,
    persistedId: readPersistedWorkspaceId(),
  });

  // Seed rather than switch: at boot there is no outgoing workspace whose
  // dialogs need tearing down, and bumping the generation here would make the
  // very first request of the session look stale to itself.
  if (picked !== getCurrentWorkspaceId()) {
    if (getCurrentWorkspaceId() === null) {
      setState({ currentWorkspaceId: picked });
      persistWorkspaceId(picked);
      if (!_controller && typeof AbortController === "function") _controller = new AbortController();
    } else {
      selectWorkspace(picked);
    }
  }
  // A persisted id that did not survive resolution must not linger in storage,
  // or every reload re-runs the same failed lookup.
  if (readPersistedWorkspaceId() !== picked) persistWorkspaceId(picked);

  return { ok: true, workspaceId: picked, error: null };
}

// ─── Connection state for the current workspace ─────────────────────────────

// CONNECTION_STATE values — the console's vocabulary for "can this workspace
// actually do identity work?". Each maps to something the backend genuinely
// reports; none is invented.
export const CONNECTION_STATE = {
  // No connection has ever been activated. /v1 answers
  // workspace_connection_missing on every identity call.
  NONE: "none",
  // An active connection whose last probe failed, or whose provider is
  // unreachable now.
  UNAVAILABLE: "unavailable",
  // Active and healthy, but the service account provably cannot write
  // (access_mode read_only) or could not even read (limited).
  LIMITED: "limited",
  // Active, healthy, and write capability proven.
  HEALTHY: "healthy",
  // The workspace itself is frozen.
  ARCHIVED: "archived",
  // We have not asked yet, or the ask failed.
  UNKNOWN: "unknown",
};

/**
 * classifyConnection — turn a workspace + its active connection into one of
 * CONNECTION_STATE.
 *
 * Pure, so the mapping is testable without a network. It deliberately reads
 * only fields the API documents: `status`, `health`, `access_mode` and the
 * precomputed `can_write`.
 */
export function classifyConnection(workspace, connection) {
  if (workspace && workspace.status === "archived") return CONNECTION_STATE.ARCHIVED;
  if (!connection) return CONNECTION_STATE.NONE;
  if (connection.status !== "active") return CONNECTION_STATE.NONE;
  if (connection.health !== "healthy") return CONNECTION_STATE.UNAVAILABLE;
  // can_write is the backend's own verdict (see connection.AccessMode.CanWrite).
  // The console does not re-derive it from access_mode — that duplication is
  // exactly how the two would drift.
  if (connection.can_write === false) return CONNECTION_STATE.LIMITED;
  return CONNECTION_STATE.HEALTHY;
}

/**
 * canWriteHere — whether mutation controls should be enabled.
 *
 * Conservative by construction: anything other than a healthy, write-capable
 * connection in an active workspace disables writes. `unknown` (we have not
 * loaded the connection yet) also disables, because enabling a Delete button
 * on an assumption is the failure mode this whole slice is about.
 */
export function canWriteHere(state = getState()) {
  const ws = (state.workspaces || []).find((w) => w.id === state.currentWorkspaceId) || null;
  return classifyConnection(ws, state.wsConnection) === CONNECTION_STATE.HEALTHY;
}

// _connectionInFlight dedupes concurrent loads for the SAME workspace.
//
// Without it there is a real race: the shell kicks off a load on navigation,
// and the view's enterWorkspace runs a moment later, sees wsConnection still
// null and wsConnectionLoading true, and would either fire a second request or
// — worse — proceed as though the workspace had no connection and render the
// "not connected" state over a workspace that is perfectly healthy.
let _connectionInFlight = null; // { workspaceId, promise }

/**
 * loadActiveConnection — fetch the current workspace's active connection.
 *
 * Uses the `status=active` filter so the answer is one row or none, rather
 * than the console picking a winner out of a list and possibly disagreeing
 * with the resolver about which connection is serving it.
 *
 * Concurrent calls for the same workspace share one request and one promise.
 */
export function loadActiveConnection(workspaceId = getCurrentWorkspaceId()) {
  if (!workspaceId) return Promise.resolve({ ok: false, connection: null, error: null });
  if (_connectionInFlight && _connectionInFlight.workspaceId === workspaceId) {
    return _connectionInFlight.promise;
  }
  const promise = _fetchActiveConnection(workspaceId).finally(() => {
    if (_connectionInFlight && _connectionInFlight.workspaceId === workspaceId) {
      _connectionInFlight = null;
    }
  });
  _connectionInFlight = { workspaceId, promise };
  return promise;
}

async function _fetchActiveConnection(workspaceId) {
  const token = captureWorkspaceToken();
  setState({ wsConnectionLoading: true, wsConnectionError: null });

  const r = await apiTry(wsPath(workspaceId, "/connections?status=active"), { signal: token.signal });

  // The operator may have switched while this was in flight. Committing now
  // would show workspace A's connection health under workspace B's name.
  if (isWorkspaceStale(token)) return { ok: false, connection: null, error: null, stale: true };

  if (!r.ok) {
    setState({ wsConnectionLoading: false, wsConnectionError: r.error, wsConnection: null });
    return { ok: false, connection: null, error: r.error };
  }
  const list = Array.isArray(r.data?.connections) ? r.data.connections : [];
  const active = list.find((c) => c.status === "active") || null;
  setState({ wsConnectionLoading: false, wsConnectionError: null, wsConnection: active });
  return { ok: true, connection: active, error: null };
}

// ─── Mutations ──────────────────────────────────────────────────────────────

/**
 * wsMutate — perform a workspace-scoped mutation, refusing to send it if the
 * workspace it was composed for is no longer the current one.
 *
 * Every destructive control in the console goes through this. The workspace id
 * is passed in explicitly — captured when the dialog was opened, alongside the
 * entity id it targets — rather than read from state at click time. That
 * pairing is the point:
 *
 *   operator opens "Delete user <uuid>" in workspace A
 *     → switches to workspace B
 *       → clicks Delete
 *
 * Reading the workspace at click time would send A's user id to B, where it
 * either 404s or, in a realm cloned from A's, names a DIFFERENT person.
 * Refusing outright is the only safe answer, and it is a bug in the caller if
 * it ever fires: the switch listener should already have closed that dialog.
 * This is the second lock on the same door.
 *
 * @returns {Promise<{ok, status, data, error, wrongWorkspace?: boolean, stale?: boolean}>}
 */
export async function wsMutate(workspaceId, path, options = {}) {
  if (!workspaceId || workspaceId !== getCurrentWorkspaceId()) {
    return {
      ok: false,
      status: 0,
      data: null,
      wrongWorkspace: true,
      error: new Error(
        "This action belonged to a workspace you have since left, and was not sent. Re-open it in the current workspace.",
      ),
    };
  }

  const token = captureWorkspaceToken();
  const r = await apiTry(wsPath(workspaceId, path), { ...options, signal: token.signal });

  // A switch DURING the request. The mutation may or may not have been applied
  // to the outgoing workspace; what must not happen is this result refreshing
  // or toasting over the incoming one.
  if (isWorkspaceStale(token)) return { ...r, stale: true };
  return r;
}

// ─── The gate every workspace-scoped view passes through ────────────────────

// GATE_BLOCKED values — why a workspace-scoped view must not fire its request.
export const GATE = {
  OK: "ok",
  NO_WORKSPACES: "no_workspaces",
  UNKNOWN_WORKSPACE: "unknown_workspace",
  ARCHIVED: "archived",
  NO_CONNECTION: "no_connection",
  LIST_FAILED: "list_failed",
};

/**
 * enterWorkspace — resolve the route's workspace, load its connection once,
 * and decide whether the view may proceed.
 *
 * Called by every workspace-scoped view before its first request. It is what
 * turns a deep link into selected state, and what stops identity views firing
 * requests that are structurally guaranteed to fail.
 *
 * ─── What blocks, and what deliberately does not ───────────────────────────
 *
 * Blocked: no workspaces at all, an unknown/archived workspace, and a
 * workspace with no active connection. Each of those makes /v1 refuse BEFORE
 * it contacts the provider, so the request carries no information — firing it
 * on every render is just noise in the operator's network tab.
 *
 * NOT blocked: an active connection whose recorded health is `unhealthy`.
 * Health is the verdict of the last verify, not a live fact — the provider may
 * have come back up since. Refusing to try would make the console strictly
 * less capable than curl, so the view proceeds and surfaces the real /v1 error
 * with a retry, which is what Phase 11 asks for.
 *
 * @param {string|null} routeWorkspaceId the id from the URL
 * @returns {Promise<{ok: boolean, reason: string, workspace: object|null,
 *                    workspaceId: string|null, connection: object|null,
 *                    connectionState: string, token: object}>}
 */
export async function enterWorkspace(routeWorkspaceId) {
  // A deep link, a second tab, or the browser's back button can name a
  // workspace the store does not currently consider selected. The URL wins —
  // see pickWorkspaceId — and switching here is what makes it so.
  if (routeWorkspaceId && routeWorkspaceId !== getCurrentWorkspaceId()) {
    selectWorkspace(routeWorkspaceId);
  }

  if (!getWorkspaces().length && !getState().workspacesLoading) {
    await loadWorkspaces();
  }

  const id = routeWorkspaceId || getCurrentWorkspaceId();
  const list = getWorkspaces();
  const workspace = id ? list.find((w) => w.id === id) || null : null;

  const fail = (reason) => ({
    ok: false,
    reason,
    workspace,
    workspaceId: id,
    connection: null,
    connectionState: CONNECTION_STATE.UNKNOWN,
    token: captureWorkspaceToken(),
  });

  if (getState().workspacesError) return fail(GATE.LIST_FAILED);
  if (!list.length) return fail(GATE.NO_WORKSPACES);
  if (!id || !workspace) return fail(GATE.UNKNOWN_WORKSPACE);
  if (workspace.status === "archived") return fail(GATE.ARCHIVED);

  // Load the connection at most once per workspace: selectWorkspace clears
  // wsConnection, so a switch refetches and a re-render does not. A load the
  // shell already started is awaited rather than duplicated — see
  // _connectionInFlight for why "already loading" must not be read as
  // "no connection".
  if (!getState().wsConnection) {
    await loadActiveConnection(id);
  }
  const connection = getState().wsConnection;
  const connectionState = classifyConnection(workspace, connection);

  if (connectionState === CONNECTION_STATE.NONE) {
    return { ...fail(GATE.NO_CONNECTION), connectionState };
  }

  return {
    ok: true,
    reason: GATE.OK,
    workspace,
    workspaceId: id,
    connection,
    connectionState,
    token: captureWorkspaceToken(),
  };
}

// _resetWorkspacesForTests — test-only. Returns the module singletons to a
// known state between cases, the same seam lib/state.js already provides.
export function _resetWorkspacesForTests() {
  _generation = 0;
  _controller = null;
  _connectionInFlight = null;
  _switchListeners.clear();
  setState({
    workspaces: [],
    currentWorkspaceId: null,
    workspacesLoading: false,
    workspacesError: null,
    wsConnection: null,
    wsConnectionLoading: false,
    wsConnectionError: null,
  });
}

// Re-exported so views import one module for "everything workspace".
export { api, apiTry, wsPath };
