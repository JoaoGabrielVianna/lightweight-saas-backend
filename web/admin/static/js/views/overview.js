// overview.js — dashboard. Real numbers when possible, placeholders otherwise.

import { h, mount } from "../lib/dom.js";
import { apiTry } from "../lib/api.js";
import { getState } from "../lib/state.js";
import { pageHeader, card, statCard, pill, emptyState, codeblock } from "../components/common.js";
import { isAuthenticated } from "../lib/auth.js";
import { navigate } from "../lib/router.js";

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

  // /users (admin-only — silently fall back when 403)
  let usersData = null, usersStatus = null;
  if (authed) {
    const u = await apiTry("/admin/users?max=100");
    if (u.ok) usersData = u.data; else usersStatus = u.status;
  }

  // Bail if the user navigated away (or re-entered /overview) during the
  // awaits above. See `_overviewGen` comment for the race this guards.
  if (myGen !== _overviewGen) return;
  if (getState().route?.path !== "/overview") return;

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
        label: "Users",
        value: usersData ? String(usersData.count) : (authed ? "—" : "—"),
        hint:  usersData ? `page size ${usersData.max || 20}` : (usersStatus === 403 ? "admin only" : "sign in as admin"),
      }),
      statCard({
        label: "Your roles",
        value: (state.identity?.roles || []).length || "—",
        hint:  (state.identity?.roles || []).join(", ") || (authed ? "no roles" : "anonymous"),
      }),
    ),

    h("div", { class: "card-grid", style: { gridTemplateColumns: "1fr 1fr" } },
      card({
        title: "Identity snapshot",
        subtitle: "from /auth/debug",
        body: renderIdentitySnapshot(state.identity, authed),
      }),
      card({
        title: "Quick actions",
        body: h("div", "col",
          h("button", { class: "btn", onclick: () => navigate("/playground") }, "Open Playground"),
          h("button", { class: "btn", onclick: () => navigate("/users"), disabled: !authed }, "Manage Users"),
          h("button", { class: "btn", onclick: () => navigate("/api-explorer") }, "API Explorer"),
          h("button", { class: "btn", onclick: () => navigate("/swagger") }, "Open Swagger"),
        ),
      }),
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
