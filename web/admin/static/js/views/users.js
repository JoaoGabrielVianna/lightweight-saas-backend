// users.js — REAL backend integration. GET /users + GET /users/:id.

import { h, mount, esc, relativeTime } from "../lib/dom.js";
import { apiTry } from "../lib/api.js";
import { pageHeader, emptyState, pill, spinner, statusBadge } from "../components/common.js";
import { renderTable } from "../components/table.js";
import { navigate } from "../lib/router.js";
import { toastBad } from "../components/toast.js";

const PAGE_SIZE = 20;

let pageState = { search: "", first: 0, max: PAGE_SIZE };
let searchHandlerInstalled = false;

export default async function usersView({ container, query }) {
  // Sync URL query → local state
  pageState = {
    search: query.search || "",
    first:  Math.max(0, parseInt(query.first || "0", 10) || 0),
    max:    PAGE_SIZE,
  };

  // Wire topbar search once
  if (!searchHandlerInstalled) {
    window.addEventListener("admin:search", (e) => {
      if (location.hash.startsWith("#/users") && !location.hash.includes("/users/")) {
        pageState.search = e.detail;
        pageState.first = 0;
        renderInto(container);
      }
    });
    searchHandlerInstalled = true;
  }

  renderInto(container);
}

async function renderInto(container) {
  mount(container,
    pageHeader("Users", h("span", null,
      "Realm users from Keycloak. Click any row to edit, reset password, manage roles or delete. ",
      statusBadge("live"),
    ), [
      h("button", {
        class: "btn btn-primary",
        onclick: () => navigate("/invitations"),
        title: "Open the Invitations modal",
      }, "+ Invite user"),
    ]),
    h("div", { id: "users-content" }, h("div", "row", spinner(), h("span", "muted", "loading…"))),
  );

  const params = new URLSearchParams();
  if (pageState.search) params.set("search", pageState.search);
  if (pageState.first)  params.set("first",  pageState.first);
  if (pageState.max)    params.set("max",    pageState.max);
  const r = await apiTry("/admin/users?" + params.toString());

  const target = container.querySelector("#users-content");
  if (!target) return;

  if (!r.ok) {
    mount(target, renderError(r));
    return;
  }

  const rows = (r.data.users || []).map(u => ({
    ...u,
    _enabled: u.enabled,
    _verified: u.email_verified,
    _created: u.created_at,
  }));

  renderTable(target, {
    toolbar: {
      search: true,
      placeholder: "Filter on Keycloak (search applied server-side)…",
      value: pageState.search,
      onSearch: (v) => {
        pageState.search = v;
        pageState.first = 0;
        // debounce-ish: small delay so we don't fire per keystroke
        clearTimeout(usersView._searchT);
        usersView._searchT = setTimeout(() => renderInto(container), 250);
      },
      actions: [
        h("button", { class: "btn btn-sm", onclick: () => renderInto(container) }, "↻ refresh"),
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
    onRowClick: (row) => navigate("/users/" + row.id),
    pagination: {
      first: pageState.first,
      max:   pageState.max,
      onChange: ({ first }) => {
        pageState.first = first;
        renderInto(container);
      },
    },
    empty: {
      title: pageState.search ? "No users match the filter" : "No users yet",
      body:  pageState.search ? "Try a different search." : "Realm has no users.",
    },
  });
}

function renderError(r) {
  if (r.status === 401) {
    return emptyState({
      icon: "🔒",
      title: "Sign in required",
      body: "GET /admin/users requires a valid bearer token. Use the Playground to authenticate.",
      action: h("button", { class: "btn btn-primary", onclick: () => navigate("/playground") }, "Go to Playground"),
    });
  }
  if (r.status === 403) {
    return emptyState({
      icon: "⛔",
      title: "Admin role required",
      body: "Your token validated, but lacks the realm `admin` role. Sign in as adminuser/password to see this view.",
    });
  }
  if (r.status === 503) {
    return emptyState({
      icon: "⚠",
      title: "Identity management not configured",
      body: "The API was started without KEYCLOAK_ADMIN_CLIENT_ID / SECRET. Set features.identity_management=true and re-run make regen.",
    });
  }
  if (r.status === 502) {
    return emptyState({
      icon: "↯",
      title: "Upstream Keycloak unreachable",
      body: "The API couldn't reach the Keycloak Admin REST endpoint.",
    });
  }
  return emptyState({
    icon: "✗",
    title: "Request failed",
    body: `HTTP ${r.status}: ${r.error?.message || "unknown error"}`,
  });
}
