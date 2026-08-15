// apiexplorer.js — interactive route execution against the live API.
// Every listed endpoint is wired to the real backend.

import { h, mount } from "../lib/dom.js";
import { apiTry } from "../lib/api.js";
import { pageHeader, pill, statusBadge } from "../components/common.js";
import { getCurrentWorkspaceId } from "../lib/workspaces.js";

// ROUTES is a CATALOGUE, not a router: it documents the surface and prefills
// the request panel. Slice 6 reordered it around the split the product now
// makes, because an explorer that lists the legacy surface first teaches the
// legacy surface.
//
//   1. /v1/workspaces — the workspace control plane
//   2. /v1/workspaces/{id}/… — workspace-scoped identity, THE product API
//   3. /admin/* — legacy compatibility, kept and clearly labelled
//
// `:ws` in a path is prefilled with the SELECTED workspace id when the panel
// loads, so the explorer is usable without copy-pasting ids.
const WS = ":ws";

const ROUTES = [
  // Public + auth
  { method: "GET", path: "/health",     needsAuth: false, desc: "Liveness probe" },
  { method: "GET", path: "/me",         needsAuth: true,  desc: "Authenticated user's local row" },
  { method: "GET", path: "/auth/debug", needsAuth: true,  desc: "Token introspection (DEV-ONLY)" },

  // ── /v1 — workspaces ────────────────────────────────────────────────────
  { method: "GET",    path: "/v1/workspaces",     needsAuth: true, desc: "List workspaces" },
  { method: "POST",   path: "/v1/workspaces",     needsAuth: true, desc: "Create a workspace", sampleBody: { name: "Production" } },
  { method: "GET",    path: `/v1/workspaces/${WS}`, needsAuth: true, desc: "Get a workspace" },
  { method: "PATCH",  path: `/v1/workspaces/${WS}`, needsAuth: true, desc: "Rename a workspace", sampleBody: { name: "Production EU" } },
  { method: "POST",   path: `/v1/workspaces/${WS}/archive`, needsAuth: true, desc: "Archive a workspace (freezes identity operations)" },

  // ── /v1 — connections ───────────────────────────────────────────────────
  { method: "GET",    path: `/v1/workspaces/${WS}/connections`, needsAuth: true, desc: "List connections (?status=active for the live one)" },
  { method: "POST",   path: `/v1/workspaces/${WS}/connections`, needsAuth: true, desc: "Create a connection (draft)", sampleBody: { name: "Production Keycloak", base_url: "http://keycloak:8080", realm: "saas", client_id: "lightweight-admin", client_secret: "…" } },
  { method: "PATCH",  path: `/v1/workspaces/${WS}/connections/:conn`, needsAuth: true, desc: "Edit a draft (omit client_secret to keep it)", sampleBody: { realm: "saas" } },
  { method: "POST",   path: `/v1/workspaces/${WS}/connections/:conn/verify`,   needsAuth: true, desc: "Probe the provider — read-only, records health + access_mode" },
  { method: "POST",   path: `/v1/workspaces/${WS}/connections/:conn/activate`, needsAuth: true, desc: "Promote a verified draft (retires the previous active)" },
  { method: "POST",   path: `/v1/workspaces/${WS}/connections/:conn/retire`,   needsAuth: true, desc: "Take a connection out of service (terminal)" },
  { method: "DELETE", path: `/v1/workspaces/${WS}/connections/:conn`,          needsAuth: true, desc: "Delete a non-active connection" },

  // ── /v1 — workspace identity (THE product API) ──────────────────────────
  { method: "GET",    path: `/v1/workspaces/${WS}/users`, needsAuth: true, desc: "List users — returns the EFFECTIVE first/max" },
  { method: "POST",   path: `/v1/workspaces/${WS}/users`, needsAuth: true, desc: "Provision a user with a temporary password", sampleBody: { email: "user@example.com", first_name: "Jane", last_name: "Doe", temporary_password: "change-me-please", roles: ["user"] } },
  { method: "GET",    path: `/v1/workspaces/${WS}/users/:id`, needsAuth: true, desc: "Single user" },
  { method: "PATCH",  path: `/v1/workspaces/${WS}/users/:id`, needsAuth: true, desc: "Update a user", sampleBody: { first_name: "Jane", enabled: true } },
  { method: "DELETE", path: `/v1/workspaces/${WS}/users/:id`, needsAuth: true, desc: "Delete a user (guards: self-delete, last-admin)" },
  { method: "GET",    path: `/v1/workspaces/${WS}/users/:id/roles`, needsAuth: true, desc: "A user's realm roles" },
  { method: "POST",   path: `/v1/workspaces/${WS}/users/:id/roles`, needsAuth: true, desc: "Grant realm roles", sampleBody: { roles: ["editor"] } },
  { method: "DELETE", path: `/v1/workspaces/${WS}/users/:id/roles/:name`, needsAuth: true, desc: "Revoke a realm role" },
  { method: "POST",   path: `/v1/workspaces/${WS}/users/:id/reset-password`, needsAuth: true, desc: "Send an UPDATE_PASSWORD action email (needs SMTP on that realm)" },
  { method: "PUT",    path: `/v1/workspaces/${WS}/users/:id/password`, needsAuth: true, desc: "Set a password directly (no email)", sampleBody: { password: "new-password", temporary: true } },
  { method: "GET",    path: `/v1/workspaces/${WS}/users/:id/sessions`, needsAuth: true, desc: "A user's sessions" },
  { method: "DELETE", path: `/v1/workspaces/${WS}/users/:id/sessions`, needsAuth: true, desc: "Log a user out everywhere" },
  { method: "GET",    path: `/v1/workspaces/${WS}/sessions`, needsAuth: true, desc: "Every session in the realm" },
  { method: "DELETE", path: `/v1/workspaces/${WS}/sessions/:id`, needsAuth: true, desc: "Revoke one session" },
  { method: "GET",    path: `/v1/workspaces/${WS}/roles`, needsAuth: true, desc: "List realm roles" },
  { method: "POST",   path: `/v1/workspaces/${WS}/roles`, needsAuth: true, desc: "Create a realm role", sampleBody: { name: "support", description: "Support team" } },
  { method: "GET",    path: `/v1/workspaces/${WS}/roles/:name`, needsAuth: true, desc: "Single realm role" },
  { method: "PATCH",  path: `/v1/workspaces/${WS}/roles/:name`, needsAuth: true, desc: "Update a role's description", sampleBody: { description: "Updated description" } },
  { method: "DELETE", path: `/v1/workspaces/${WS}/roles/:name`, needsAuth: true, desc: "Delete a realm role" },
  { method: "GET",    path: `/v1/workspaces/${WS}/roles/:name/users`, needsAuth: true, desc: "Complete role membership (not a page)" },
  { method: "GET",    path: `/v1/workspaces/${WS}/invitations`, needsAuth: true, desc: "Pending invitations" },
  { method: "POST",   path: `/v1/workspaces/${WS}/invitations`, needsAuth: true, desc: "Invite by email", sampleBody: { email: "user@example.com", first_name: "Jane", last_name: "Doe", roles: ["user"] } },
  { method: "POST",   path: `/v1/workspaces/${WS}/invitations/:id/resend`, needsAuth: true, desc: "Re-send an invitation email" },
  { method: "DELETE", path: `/v1/workspaces/${WS}/invitations/:id`, needsAuth: true, desc: "Revoke an invitation (= delete the underlying user)" },

  // ── Installation-scoped ─────────────────────────────────────────────────
  { method: "GET", path: "/admin/audit-events", needsAuth: true, desc: "Audit events — installation-wide, deliberately not workspace-scoped" },

  // ── Legacy /admin/* — compatibility surface ─────────────────────────────
  // Kept, not removed. Acts on THIS INSTALLATION's KEYCLOAK_* realm, never on
  // the selected workspace's realm.
  { method: "GET",    path: "/admin/users",                     needsAuth: true, desc: "LEGACY: list users (installation realm)" },
  { method: "GET",    path: "/admin/roles",                     needsAuth: true, desc: "LEGACY: list roles (installation realm)" },
  { method: "GET",    path: "/admin/sessions",                  needsAuth: true, desc: "LEGACY: realm sessions (installation realm)" },
  { method: "GET",    path: "/admin/invitations",               needsAuth: true, desc: "LEGACY: invitations (installation realm)" },
  { method: "GET",    path: "/admin/settings/smtp",             needsAuth: true, desc: "LEGACY: SMTP settings — no /v1 equivalent (deferred)" },
  { method: "GET",    path: "/admin/settings/email-templates",  needsAuth: true, desc: "LEGACY: email templates — no /v1 equivalent (deferred)" },
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
  // Prefill `:ws` with the selected workspace so the /v1 routes are one click
  // from usable. Everything else stays a visible placeholder the operator
  // fills in — this panel documents the shape as much as it sends requests.
  const workspaceId = getCurrentWorkspaceId();
  const prefilled = workspaceId ? route.path.split(WS).join(workspaceId) : route.path;

  pathInput = h("input", { type: "text", value: prefilled, style: { width: "100%", fontFamily: "var(--font-mono)" } });

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
