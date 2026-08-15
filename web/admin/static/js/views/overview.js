// overview.js — dashboard. Real numbers when possible, placeholders otherwise.

import { h, mount } from "../lib/dom.js";
import { apiTry, wsPath } from "../lib/api.js";
import { getState } from "../lib/state.js";
import { pageHeader, card, statCard, pill, emptyState, codeblock } from "../components/common.js";
import { isAuthenticated } from "../lib/auth.js";
import { navigate, wsRoute } from "../lib/router.js";
import {
  getCurrentWorkspaceId, getCurrentWorkspace, getWorkspaces,
  captureWorkspaceToken, isWorkspaceStale, classifyConnection,
} from "../lib/workspaces.js";
import { connectionPill } from "../components/ws-states.js";

// Generation token to suppress stale renders. Each invocation captures its
// generation at entry; before the post-await mount, it verifies the captured
// generation still matches AND the active route is still /overview. Without
// this guard, rapid navigation (Overview → Users mid-await) lets the stale
// await chain rewrite the container with Overview HTML, clobbering the next
// view. Same hazard if /overview is re-entered before its first awaits
// resolve: two concurrent renders would race to mount.
let _overviewGen = 0;

// _isOverviewStaleForTests — exposed solely so the regression test in
// tests/overview.test.mjs can pin the staleness predicate without booting
// a DOM. The view itself uses the inline check.
export function _isOverviewStaleForTests(myGen, currentPath) {
  return myGen !== _overviewGen || currentPath !== "/overview";
}

// _resetOverviewGenForTests — test-only helper.
export function _resetOverviewGenForTests() {
  _overviewGen = 0;
}

// _bumpOverviewGenForTests — test-only helper. Simulates a newer Overview
// render starting before this one resumes.
export function _bumpOverviewGenForTests() {
  return ++_overviewGen;
}

export default async function overviewView({ container }) {
  const myGen = ++_overviewGen;
  const state = getState();
  const authed = isAuthenticated();

  // Render shell first so the user sees something immediately.
  mount(container,
    pageHeader("Overview", "Live status of your identity stack."),
    h("div", "card-grid",
      statCard({ label: "API",       value: "…", hint: "checking" }),
      statCard({ label: "Keycloak",  value: "…", hint: "checking" }),
      statCard({ label: "Users",     value: "—", hint: authed ? "fetching" : "sign in to see" }),
      statCard({ label: "Identity",  value: authed ? "signed in" : "anonymous", hint: state.identity?.received_azp || "" }),
    ),
    h("div", { id: "ov-recent" }),
  );

  // Probe /health
  const health = await apiTry("/health");
  // Probe OIDC discovery
  let oidcOk = false;
  try {
    const cfg = state.config;
    const r = await fetch(`${cfg.keycloakUrl.replace(/\/$/, "")}/realms/${cfg.realm}/.well-known/openid-configuration`, { cache: "no-store" });
    oidcOk = r.ok;
  } catch {}

  // User count for the SELECTED WORKSPACE.
  //
  // Overview itself stays installation-scoped — it is the health of this
  // LIGHTWEIGHT instance, not of one realm — but a bare "Users: 41" would be
  // meaningless without saying whose. Slice 6 moves the count onto
  // /v1/workspaces/{id}/users and labels the card with the workspace name.
  // With no workspace selected the card says so rather than falling back to
  // the legacy realm, which would show a number for a realm nobody chose.
  const wsToken = captureWorkspaceToken();
  const workspaceId = getCurrentWorkspaceId();
  let usersData = null, usersStatus = null;
  if (authed && workspaceId) {
    const u = await apiTry(wsPath(workspaceId, "/users?max=100"), { signal: wsToken.signal });
    if (u.ok) usersData = u.data; else usersStatus = u.status;
  }
  // A workspace switch mid-await must not paint A's count under B's name.
  if (isWorkspaceStale(wsToken)) return;

  // Bail if the user navigated away (or re-entered /overview) during the
  // awaits above. See `_overviewGen` comment for the race this guards.
  if (myGen !== _overviewGen) return;
  if (getState().route?.path !== "/overview") return;

  // Read the workspace AFTER the staleness checks, so the name rendered on the
  // card is the one the count was fetched for.
  const workspace = getCurrentWorkspace();

  // Re-render with real numbers
  mount(container,
    pageHeader("Overview", "Live status of your identity stack."),
    h("div", "card-grid",
      statCard({
        label: "API /health",
        value: health.ok ? "✓ ok" : "✗ down",
        hint:  health.ok ? "HTTP 200" : `HTTP ${health.status || "?"}`,
      }),
      statCard({
        label: "Keycloak OIDC",
        value: oidcOk ? "✓ ok" : "✗ down",
        hint:  oidcOk ? state.config.realm : "discovery failed",
      }),
      statCard({
        label: workspace ? `Users in ${workspace.name}` : "Users",
        value: usersData ? String(usersData.count) : "—",
        hint:  usersData
          ? `page size ${usersData.max || 20}`
          : !workspaceId ? "no workspace selected"
          : usersStatus === 409 ? "workspace not connected"
          : usersStatus === 403 ? "admin only"
          : authed ? "unavailable" : "sign in as admin",
      }),
      statCard({
        label: "Workspaces",
        value: String(getWorkspaces().length),
        hint:  workspace ? `current: ${workspace.name}` : "none selected",
      }),
    ),

    h("div", { class: "card-grid", style: { gridTemplateColumns: "1fr 1fr" } },
      card({
        title: "Identity snapshot",
        subtitle: "from /auth/debug — the OPERATOR's session, not a workspace realm",
        body: renderIdentitySnapshot(state.identity, authed),
      }),
      card({
        title: "Current workspace",
        subtitle: workspace ? workspace.id : "nothing selected",
        body: renderWorkspaceSnapshot(workspace, state),
      }),
    ),

    card({
      title: "Quick actions",
      body: h("div", "row",
        workspaceId
          ? h("button", { class: "btn", onclick: () => navigate(wsRoute(workspaceId, "users")) }, "Manage users")
          : h("button", { class: "btn btn-primary", onclick: () => navigate("/workspaces") }, "Create a workspace"),
        h("button", { class: "btn", onclick: () => navigate("/workspaces") }, "Workspaces"),
        h("button", { class: "btn", onclick: () => navigate("/playground") }, "Open Playground"),
        h("button", { class: "btn", onclick: () => navigate("/api-explorer") }, "API Explorer"),
        h("button", { class: "btn", onclick: () => navigate("/swagger") }, "Open Swagger"),
      ),
    }),
  );
}

// renderWorkspaceSnapshot — what the selected workspace routes through, so an
// operator landing on Overview can tell at a glance whether identity work is
// possible before clicking into a view that would say the same thing.
function renderWorkspaceSnapshot(workspace, state) {
  if (!workspace) {
    return emptyState({
      icon: "◫",
      title: "No workspace selected",
      body: "Identity management is scoped to a workspace. Create or select one to begin.",
    });
  }
  const conn = state.wsConnection;
  return h("div", "col",
    h("div", "row", connectionPill(classifyConnection(workspace, conn), conn)),
    h("dl", "kv",
      h("div", null, h("dt", null, "workspace"), h("dd", null, workspace.name)),
      h("div", null, h("dt", null, "id"),        h("dd", null, h("code", null, workspace.id))),
      h("div", null, h("dt", null, "status"),    h("dd", null, workspace.status)),
      h("div", null, h("dt", null, "connection"),h("dd", null, conn ? conn.name : "none active")),
      h("div", null, h("dt", null, "realm"),     h("dd", null, conn ? h("code", null, conn.realm) : "—")),
    ),
  );
}

function renderIdentitySnapshot(id, authed) {
  if (!authed || !id) {
    return emptyState({
      icon: "○",
      title: "Anonymous",
      body: "Sign in via the Playground to populate this card.",
    });
  }
  return h("div", "col",
    h("div", "row", pill(id.valid ? "valid" : "invalid", id.valid ? "ok" : "bad"), pill(id.expired ? "expired" : "live", id.expired ? "bad" : "ok")),
    h("dl", "kv",
      h("div", null, h("dt", null, "sub"),  h("dd", null, h("code", null, id.received_sub || "—"))),
      h("div", null, h("dt", null, "email"),h("dd", null, id.email || "—")),
      h("div", null, h("dt", null, "azp"),  h("dd", null, h("code", null, id.received_azp || "—"))),
      h("div", null, h("dt", null, "iss"),  h("dd", null, h("code", null, id.issuer || "—"))),
    ),
  );
}
