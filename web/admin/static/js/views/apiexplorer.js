// apiexplorer.js — interactive route execution against the live API.
// Every listed endpoint is wired to the real backend.

import { h, mount } from "../lib/dom.js";
import { apiTry } from "../lib/api.js";
import { pageHeader, pill, statusBadge } from "../components/common.js";

const ROUTES = [
  // Public + auth
  { method: "GET", path: "/health",                          needsAuth: false, desc: "Liveness probe" },
  { method: "GET", path: "/me",                              needsAuth: true,  desc: "Authenticated user's local row" },
  { method: "GET", path: "/auth/debug",                      needsAuth: true,  desc: "Token introspection (DEV-ONLY)" },

  // v0.2 Stage A — READ
  { method: "GET", path: "/admin/users",                     needsAuth: true,  desc: "List realm users (admin)" },
  { method: "GET", path: "/admin/users/:id",                 needsAuth: true,  desc: "Single user by sub UUID (admin)" },
  { method: "GET", path: "/admin/users/:id/roles",           needsAuth: true,  desc: "Realm roles assigned to a user (admin)" },
  { method: "GET", path: "/admin/users/:id/sessions",        needsAuth: true,  desc: "Active sessions for a user (admin)" },
  { method: "GET", path: "/admin/roles",                     needsAuth: true,  desc: "List realm roles (admin)" },
  { method: "GET", path: "/admin/roles/:name",               needsAuth: true,  desc: "Single realm role (admin)" },
  { method: "GET", path: "/admin/roles/:name/users",         needsAuth: true,  desc: "Users carrying a role (admin)" },
  { method: "GET", path: "/admin/sessions",                  needsAuth: true,  desc: "All sessions across the realm (admin)" },
  { method: "GET", path: "/admin/invitations",               needsAuth: true,  desc: "Pending invitations (admin)" },

  // v0.2 Stage B — CREATE (live)
  { method: "POST",   path: "/admin/roles",                  needsAuth: true,  desc: "Create role (Stage B)", sampleBody: { name: "support", description: "Support team" } },
  { method: "POST",   path: "/admin/users/invite",           needsAuth: true,  desc: "Invite user (Stage B) — alias of POST /admin/invitations", sampleBody: { email: "user@example.com", first_name: "Jane", last_name: "Doe", roles: ["user"] } },
  { method: "POST",   path: "/admin/invitations",            needsAuth: true,  desc: "Create invitation (Stage B)", sampleBody: { email: "user@example.com", first_name: "Jane", last_name: "Doe", roles: ["user"] } },

  // v0.2 Stage C — UPDATE (live)
  { method: "PATCH",  path: "/admin/users/:id",                needsAuth: true, desc: "Update user (partial — first/last/email/enabled)", sampleBody: { first_name: "Jane", enabled: true } },
  { method: "PATCH",  path: "/admin/roles/:name",              needsAuth: true, desc: "Update role description (Stage C)", sampleBody: { description: "Updated description" } },
  { method: "POST",   path: "/admin/users/:id/roles",          needsAuth: true, desc: "Assign realm roles (Stage C)", sampleBody: { roles: ["editor"] } },
  { method: "POST",   path: "/admin/users/:id/reset-password", needsAuth: true, desc: "Send UPDATE_PASSWORD action email (Stage C)" },
  { method: "POST",   path: "/admin/invitations/:id/resend",   needsAuth: true, desc: "Re-send invitation email (Stage C)" },

  // v0.2 Stage D — DELETE (live)
  { method: "DELETE", path: "/admin/users/:id",                needsAuth: true, desc: "Delete user (guards: self-delete, last-admin)" },
  { method: "DELETE", path: "/admin/users/:id/roles/:name",    needsAuth: true, desc: "Remove a role from a user (guards: self-strip admin, last-admin)" },
  { method: "DELETE", path: "/admin/users/:id/sessions",       needsAuth: true, desc: "Log a user out of every session" },
  { method: "DELETE", path: "/admin/roles/:name",              needsAuth: true, desc: "Delete a role (guards: protected/built-in)" },
  { method: "DELETE", path: "/admin/sessions/:id",             needsAuth: true, desc: "Revoke a single session" },
  { method: "DELETE", path: "/admin/invitations/:id",          needsAuth: true, desc: "Revoke an invitation (= delete the underlying user)" },
];

let selectedIdx = 0;
let pathInput = null;
let bodyInput = null;

export default async function apiExplorerView({ container }) {
  mount(container,
    pageHeader("API Explorer", h("span", null,
      "Send requests against this API. Authentication uses the currently-active token; sign in via Playground first. ",
      statusBadge("live"),
    )),
    h("div", { class: "card", style: { padding: 0 } },
      h("div", { style: { display: "grid", gridTemplateColumns: "260px 1fr" } },
        renderEndpointList(container),
        renderRequestPanel(container),
      ),
    ),
  );
}

function renderEndpointList(container) {
  return h("aside", { style: { borderRight: "1px solid var(--line-soft)", padding: "12px", maxHeight: "70vh", overflowY: "auto" } },
    h("h4", { class: "muted", style: { margin: "0 0 8px", textTransform: "uppercase", fontSize: "10px", letterSpacing: "0.08em" } }, "endpoints"),
    ...ROUTES.map((r, i) => h("button", {
      class: ["sidebar-link", i === selectedIdx ? "active" : ""].filter(Boolean).join(" "),
      style: { width: "100%", marginBottom: "2px", fontFamily: "var(--font-mono)", fontSize: "12px", textAlign: "left", justifyContent: "flex-start" },
      onclick: () => { selectedIdx = i; apiExplorerView({ container }); },
    },
      h("span", { style: { color: methodColor(r.method), marginRight: "8px", fontWeight: 700 } }, r.method),
      h("span", { class: "grow" }, r.path),
    )),
  );
}

function renderRequestPanel(container) {
  const route = ROUTES[selectedIdx];
  pathInput = h("input", { type: "text", value: route.path, style: { width: "100%", fontFamily: "var(--font-mono)" } });

  const hasBody = route.method !== "GET" && route.method !== "DELETE";
  bodyInput = hasBody
    ? h("textarea", {
        rows: 6,
        spellcheck: "false",
        style: { width: "100%", fontFamily: "var(--font-mono)", fontSize: "12px" },
        placeholder: '{}',
      }, route.sampleBody ? JSON.stringify(route.sampleBody, null, 2) : "{}")
    : null;

  return h("div", { style: { padding: "16px" } },
    h("div", "row",
      h("span", { class: "pill pill-accent", style: { color: methodColor(route.method) } }, route.method),
      pathInput,
      h("button", { class: "btn btn-primary", onclick: () => execute(container, route) }, "Send"),
    ),
    h("p", "muted", route.desc, route.needsAuth ? " · requires bearer token" : " · no auth required"),
    hasBody
      ? h("div", { style: { marginTop: "12px" } },
          h("div", "muted", "request body (JSON)"),
          bodyInput,
        )
      : null,
    h("div", { id: "ax-result", style: { marginTop: "16px" } }),
  );
}

async function execute(container, route) {
  const url = pathInput.value;
  const out = container.querySelector("#ax-result");
  out.innerHTML = "";
  mount(out, h("div", "row", h("span", "spinner"), h("span", "muted", "calling…")));
  const t0 = performance.now();

  const opts = { method: route.method };
  if (bodyInput && bodyInput.value.trim()) {
    try {
      // Validate JSON before sending so the error surfaces here, not as
      // an opaque 400 from the server. Re-stringify to normalize.
      const parsed = JSON.parse(bodyInput.value);
      opts.body = JSON.stringify(parsed);
      opts.headers = { "Content-Type": "application/json" };
    } catch (e) {
      mount(out,
        h("div", "row", pill("client error", "bad")),
        h("pre", "pre", "Body is not valid JSON: " + e.message),
      );
      return;
    }
  }
  const r = await apiTry(url, opts);
  const ms = Math.round(performance.now() - t0);
  const result = {
    status: r.status || (r.ok ? 200 : 0),
    duration_ms: ms,
    body: r.ok ? r.data : (r.error?.body ?? null),
    ok: r.ok,
  };

  mount(out,
    h("div", "row",
      pill("HTTP " + result.status, result.ok ? "ok" : "bad"),
      h("span", "muted", result.duration_ms + " ms"),
    ),
    h("pre", "pre", JSON.stringify(result.body, null, 2)),
  );
}

function methodColor(m) {
  return ({
    GET:    "var(--ok)",
    POST:   "var(--accent)",
    PATCH:  "var(--warn)",
    DELETE: "var(--bad)",
  })[m] || "var(--fg)";
}
