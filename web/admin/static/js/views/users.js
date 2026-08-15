// users.js — workspace-scoped user listing.
//
// Slice 6: migrated from GET /admin/users to
// GET /v1/workspaces/{workspace_id}/users. The realm shown is the one the
// selected workspace's active connection points at, resolved per request by
// the server — this view never learns the realm's name, base URL or
// credentials, and must not.
//
// Two things beyond the path changed, and both are correctness, not cosmetics:
//
//   1. PAGINATION. /admin echoed the caller's raw `max`; /v1 returns the
//      EFFECTIVE values after clamping (TD-020, fixed on /v1 only). The view
//      now paginates on what the server says it used, so a clamped page size
//      no longer produces skipped rows.
//   2. STALENESS. Every response is checked against the workspace context it
//      was requested in before it reaches the DOM. See lib/workspaces.js.

import { h, mount, relativeTime } from "../lib/dom.js";
import { apiTry, wsPath, enterWorkspace, captureWorkspaceToken, isWorkspaceStale } from "../lib/workspaces.js";
import { pageHeader, pill, spinner, statusBadge } from "../components/common.js";
import { renderTable } from "../components/table.js";
import { renderGateState, renderAPIError, connectionBanner } from "../components/ws-states.js";
import { navigate, wsRoute } from "../lib/router.js";

const PAGE_SIZE = 20;

let pageState = { search: "", first: 0, max: PAGE_SIZE };
let searchHandlerInstalled = false;

export default async function usersView({ container, params, query }) {
  const workspaceId = params.workspace_id;

  // Sync URL query → local state. Pagination and search are per-workspace by
  // construction: the workspace is in the path, so navigating to another
  // workspace produces a different route with its own (absent) query, and
  // A's page-3 offset cannot survive into B.
  pageState = {
    search: query.search || "",
    first:  Math.max(0, parseInt(query.first || "0", 10) || 0),
    max:    PAGE_SIZE,
  };

  if (!searchHandlerInstalled) {
    window.addEventListener("admin:search", (e) => {
      // Only react while a users LIST page is showing — not a user detail page.
      if (/^#\/workspaces\/ws_[^/]+\/users$/.test(location.hash.split("?")[0])) {
        pageState.search = e.detail;
        pageState.first = 0;
        renderInto(container, workspaceId);
      }
    });
    searchHandlerInstalled = true;
  }

  renderInto(container, workspaceId);
}

async function renderInto(container, workspaceId) {
  mount(container,
    pageHeader("Users", h("span", null,
      "Users in this workspace's realm. Click any row to edit, reset password, manage roles or delete. ",
      statusBadge("live"),
    ), [
      h("button", {
        class: "btn btn-primary",
        onclick: () => navigate(wsRoute(workspaceId, "invitations")),
        title: "Open the Invitations page",
      }, "+ Invite user"),
    ]),
    h("div", { id: "users-content" }, h("div", "row", spinner(), h("span", "muted", "loading…"))),
  );

  const gate = await enterWorkspace(workspaceId);
  const target = container.querySelector("#users-content");
  if (!target) return;
  if (!gate.ok) {
    mount(target, renderGateState(gate, { onRetry: () => renderInto(container, workspaceId) }));
    return;
  }

  const params = new URLSearchParams();
  if (pageState.search) params.set("search", pageState.search);
  if (pageState.first)  params.set("first",  pageState.first);
  if (pageState.max)    params.set("max",    pageState.max);

  const token = captureWorkspaceToken();
  const r = await apiTry(wsPath(workspaceId, "/users?" + params.toString()), { signal: token.signal });

  // THE isolation check. Without it, workspace A's user list can arrive after
  // the operator has switched to B and paint A's people under B's name — the
  // precise setup for deleting the wrong account.
  if (isWorkspaceStale(token)) return;

  const target2 = container.querySelector("#users-content");
  if (!target2) return;

  if (!r.ok) {
    mount(target2, renderAPIError(r, { onRetry: () => renderInto(container, workspaceId) }));
    return;
  }

  const rows = (r.data.users || []).map(u => ({
    ...u,
    _enabled: u.enabled,
    _verified: u.email_verified,
    _created: u.created_at,
  }));

  // /v1 reports the EFFECTIVE page window. Paginating on the requested values
  // instead would skip rows whenever the server clamped `max`.
  const effectiveFirst = Number.isFinite(r.data.first) ? r.data.first : pageState.first;
  const effectiveMax   = Number.isFinite(r.data.max)   ? r.data.max   : pageState.max;

  mount(target2,
    connectionBanner(gate.connectionState, gate.connection, { workspaceId }),
    h("div", { id: "users-table" }),
  );

  renderTable(container.querySelector("#users-table"), {
    toolbar: {
      search: true,
      placeholder: "Filter on the provider (search applied server-side)…",
      value: pageState.search,
      onSearch: (v) => {
        pageState.search = v;
        pageState.first = 0;
        clearTimeout(usersView._searchT);
        usersView._searchT = setTimeout(() => renderInto(container, workspaceId), 250);
      },
      actions: [
        h("button", { class: "btn btn-sm", onclick: () => renderInto(container, workspaceId) }, "↻ refresh"),
      ],
    },
    columns: [
      { key: "username", title: "Username", render: (v) => h("strong", null, v || "—") },
      { key: "email",    title: "Email" },
      { key: "first_name", title: "First" },
      { key: "last_name",  title: "Last" },
      { key: "_enabled", title: "Status", render: (v) => pill(v ? "enabled" : "disabled", v ? "ok" : "warn") },
      { key: "_verified",title: "Email verified", render: (v) => pill(v ? "yes" : "no", v ? "ok" : "warn") },
      { key: "_created", title: "Created", render: (v) => v ? relativeTime(v) : "—" },
    ],
    rows,
    onRowClick: (row) => navigate(wsRoute(workspaceId, "users/" + row.id)),
    pagination: {
      first: effectiveFirst,
      max:   effectiveMax,
      onChange: ({ first }) => {
        pageState.first = first;
        renderInto(container, workspaceId);
      },
    },
    empty: {
      title: pageState.search ? "No users match the filter" : "No users yet",
      body:  pageState.search ? "Try a different search." : "This workspace's realm has no users.",
    },
  });
}
