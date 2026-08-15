// ws-states.js — the product states a workspace-scoped view can be in, and
// one place that renders an API error.
//
// Two responsibilities, both of which used to be copy-pasted per view:
//
//   1. the empty/degraded states from Phase 11 — no workspaces, unknown
//      workspace, archived workspace, no active connection;
//   2. rendering an error from EITHER surface, using the structured /v1 code
//      when there is one and the legacy string when there is not.
//
// Nothing here injects HTML. Every value that came from the network reaches
// the DOM as a text node via lib/dom.js's `h`, so a provider error message
// containing markup renders as characters, not elements.

import { h } from "../lib/dom.js";
import { emptyState, pill } from "./common.js";
import { navigate, wsRoute } from "../lib/router.js";
import { CONNECTION_STATE, GATE } from "../lib/workspaces.js";

// ─── Error rendering ────────────────────────────────────────────────────────

// ERROR_ADVICE maps the /v1 codes an operator will actually hit onto the
// remedy, because "409 conflict" tells them nothing about which system to open.
//
// Only codes with a DIFFERENT remedy get an entry. A code whose message
// already says the right thing is better rendered from the message, which the
// server can reword without a frontend release.
const ERROR_ADVICE = {
  workspace_connection_missing:
    "This workspace has no active connection yet. Create one, verify it, then activate it.",
  workspace_connection_unusable:
    "The active connection cannot be turned into a working provider. Re-check its base URL, realm and client id, then verify again.",
  connection_read_only:
    "The connection's service account has no write permission. Grant it realm-management/manage-users in Keycloak and verify the connection again.",
  provider_forbidden:
    "Keycloak refused this workspace's service account. The fix is in Keycloak's role mappings, not in this console.",
  workspace_archived:
    "This workspace is archived. Identity operations are frozen until it is restored.",
  provider_credentials_unavailable:
    "The stored provider credential could not be opened. This needs an operator: the master key has changed or the row is damaged.",
};

/**
 * errorMessage — the one-line prose for an error from either surface.
 *
 * Never returns "[object Object]" and never returns an empty string: APIError
 * already guarantees a string message, and a non-APIError falls back to its
 * own message or the status.
 */
export function errorMessage(error, status) {
  const msg = error && typeof error.message === "string" ? error.message.trim() : "";
  if (msg) return msg;
  if (status) return `HTTP ${status}`;
  return "Unknown error";
}

/**
 * errorDetail — the small print under the message: the remedy for a known
 * code, then the code and request id an operator can quote in a bug report.
 *
 * Returns null when there is nothing worth adding, so callers can drop it.
 */
export function errorDetail(error) {
  if (!error) return null;
  const bits = [];
  if (error.code && ERROR_ADVICE[error.code]) bits.push(ERROR_ADVICE[error.code]);

  const meta = [];
  if (error.code) meta.push("code " + error.code);
  // The request id is the whole point of the /v1 envelope: it is what ties
  // this screen to the server log line holding the real cause.
  if (error.requestId) meta.push("request " + error.requestId);
  if (meta.length) bits.push(meta.join(" · "));

  return bits.length ? bits.join(" ") : null;
}

/**
 * describeFailure — one string for any failure a workspace-scoped mutation can
 * produce: a refused cross-workspace action, a structured /v1 error, or a
 * transport failure. Used for toasts, where there is room for one line.
 *
 * @param {{wrongWorkspace?: boolean, status?: number, error?: Error}} res a wsMutate result
 */
export function describeFailure(res) {
  if (!res) return "Unknown error";
  if (res.wrongWorkspace) return errorMessage(res.error);
  const msg = errorMessage(res.error, res.status);
  const detail = errorDetail(res.error);
  return detail ? `${msg} — ${detail}` : msg;
}

/**
 * renderAPIError — a full-panel error state for a failed request.
 *
 * @param {{status: number, error: Error}} r an apiTry result
 * @param {{onRetry?: Function, title?: string}} opts
 */
export function renderAPIError(r, opts = {}) {
  const error = r?.error || null;
  const status = r?.status ?? 0;

  // Status 0 means no HTTP exchange happened at all — offline, DNS, or an
  // aborted request. Saying "request failed" there sends an operator looking
  // at the server.
  const title = opts.title || (status === 0 ? "Could not reach the API" : "Request failed");
  const detail = errorDetail(error);

  return emptyState({
    icon: status === 401 ? "🔒" : status === 403 ? "⛔" : "✗",
    title,
    body: errorMessage(error, status),
    action: h("div", "col",
      detail ? h("p", "muted text-xs", detail) : null,
      opts.onRetry ? h("button", { class: "btn btn-primary", onclick: opts.onRetry }, "Retry") : null,
    ),
  });
}

// ─── Connection state ───────────────────────────────────────────────────────

// CONNECTION_LABEL renders the backend's vocabulary, not an invented one.
// `limited` covers both access modes the backend refuses writes on
// (`read_only` and `limited`); the pill's title distinguishes them, because
// they have different remedies and the same consequence.
const CONNECTION_LABEL = {
  [CONNECTION_STATE.HEALTHY]:     { text: "Healthy",            kind: "ok" },
  [CONNECTION_STATE.LIMITED]:     { text: "Read-only",          kind: "warn" },
  [CONNECTION_STATE.UNAVAILABLE]: { text: "Unavailable",        kind: "bad" },
  [CONNECTION_STATE.NONE]:        { text: "No connection",      kind: "neutral" },
  [CONNECTION_STATE.ARCHIVED]:    { text: "Archived",           kind: "neutral" },
  [CONNECTION_STATE.UNKNOWN]:     { text: "Connection unknown", kind: "neutral" },
};

/** connectionPill — the compact state indicator used in the topbar selector. */
export function connectionPill(state, connection) {
  const spec = CONNECTION_LABEL[state] || CONNECTION_LABEL[CONNECTION_STATE.UNKNOWN];
  const el = pill(spec.text, spec.kind);
  el.title = connectionTitle(state, connection);
  return el;
}

/** connectionTitle — the hover/aria explanation for a connection state. */
export function connectionTitle(state, connection) {
  switch (state) {
    case CONNECTION_STATE.HEALTHY:
      return "The active connection reached its provider and its service account was proven able to write.";
    case CONNECTION_STATE.LIMITED:
      return connection?.access_mode === "read_only"
        ? "The service account can read this realm but holds no write permission. Grant it realm-management/manage-users and verify again."
        : "The service account's admin reads were refused. Grant it realm-management roles and verify again.";
    case CONNECTION_STATE.UNAVAILABLE:
      return connection?.health_message ||
        "The last verification of this connection failed. Re-verify it to find out whether the provider is back.";
    case CONNECTION_STATE.NONE:
      return "This workspace has no active connection, so it cannot perform identity operations.";
    case CONNECTION_STATE.ARCHIVED:
      return "This workspace is archived. Identity operations are refused before the provider is contacted.";
    default:
      return "The connection state has not been loaded.";
  }
}

/**
 * writeBlockedReason — why mutation controls are disabled here, or null when
 * they are not.
 *
 * The console asks this instead of reading access_mode, so "may I write?" has
 * exactly one answer derived from the backend's own `can_write`.
 */
export function writeBlockedReason(state, connection) {
  if (state === CONNECTION_STATE.HEALTHY) return null;
  return connectionTitle(state, connection);
}

/**
 * connectionBanner — an in-page strip explaining a degraded but usable state.
 *
 * Returns null when the connection is healthy: a banner that is always there
 * stops being read.
 */
export function connectionBanner(state, connection, { workspaceId } = {}) {
  if (state === CONNECTION_STATE.HEALTHY || state === CONNECTION_STATE.UNKNOWN) return null;

  const isReadOnly = state === CONNECTION_STATE.LIMITED;
  return h("div", { class: "ws-banner", role: "status" },
    connectionPill(state, connection),
    h("span", null,
      isReadOnly
        ? "Reads work here. Everything that changes data is disabled until this connection can write."
        : "Identity operations through this workspace may fail.",
    ),
    workspaceId
      ? h("button", {
          class: "btn btn-xs",
          onclick: () => navigate(wsRoute(workspaceId, "connections")),
        }, "Open connections")
      : null,
  );
}

/**
 * legacyProviderBanner — the marker on a page that is NOT workspace-scoped
 * while the workspace selector is visible above it.
 *
 * Phase 13: SMTP and email templates stay on /admin/* because they are
 * provider realm settings with no /v1 equivalent. The danger is not that they
 * are legacy — it is that an operator reads the selector at the top of the
 * screen and assumes this page follows it. It does not, and saying so costs
 * one strip of text.
 *
 * @param {string} what the surface's name, e.g. "SMTP settings"
 */
export function legacyProviderBanner(what) {
  return h("div", { class: "ws-banner ws-banner-legacy", role: "note" },
    pill("legacy", "neutral"),
    h("span", null,
      `${what} are NOT scoped to the selected workspace. They apply to this installation's own Keycloak realm, whichever workspace is chosen above. `,
      "There is no /v1 equivalent yet — these remain on the legacy /admin surface by design.",
    ),
  );
}

// ─── Gate states ────────────────────────────────────────────────────────────

/**
 * renderGateState — the panel shown INSTEAD of a view's content when
 * enterWorkspace refused.
 *
 * Every branch offers the operator somewhere to go. A dead end with an
 * explanation is still a dead end.
 */
export function renderGateState(gate, { onRetry } = {}) {
  switch (gate.reason) {
    case GATE.NO_WORKSPACES:
      return emptyState({
        icon: "◫",
        title: "No workspaces yet",
        body: "Identity management is scoped to a workspace: a workspace connects this installation to one Keycloak realm. Create one to begin.",
        action: h("button", {
          class: "btn btn-primary",
          onclick: () => navigate("/workspaces"),
        }, "Manage workspaces"),
      });

    case GATE.UNKNOWN_WORKSPACE:
      return emptyState({
        icon: "?",
        title: "Workspace not found",
        body: gate.workspaceId
          ? `No workspace with id ${gate.workspaceId} is available to you. It may have been deleted, or this link came from another installation.`
          : "No workspace is selected.",
        action: h("button", {
          class: "btn btn-primary",
          onclick: () => navigate("/workspaces"),
        }, "Choose a workspace"),
      });

    case GATE.ARCHIVED:
      return emptyState({
        icon: "▤",
        title: "Workspace is archived",
        body: "Archived workspaces are frozen: identity operations are refused before the provider is contacted. Select an active workspace, or restore this one.",
        action: h("button", {
          class: "btn btn-primary",
          onclick: () => navigate("/workspaces"),
        }, "Manage workspaces"),
      });

    case GATE.NO_CONNECTION:
      return emptyState({
        icon: "⇄",
        title: "This workspace isn't connected yet",
        body: "A workspace reaches its Keycloak realm through an active connection. Create a connection, verify it, then activate it — until then there is no realm to show.",
        action: h("button", {
          class: "btn btn-primary",
          onclick: () => navigate(wsRoute(gate.workspaceId, "connections")),
        }, "Configure connection"),
      });

    case GATE.LIST_FAILED:
      return renderAPIError(
        { status: 0, error: gate.error || null },
        { title: "Could not load workspaces", onRetry },
      );

    default:
      return emptyState({ icon: "✗", title: "Workspace unavailable" });
  }
}
