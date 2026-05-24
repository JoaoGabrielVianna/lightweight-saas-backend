// main.js — admin console entry point.
//
// Boot sequence:
//   1. Load theme from localStorage and apply to <body>
//   2. Fetch /admin/config.json
//   3. Handle PKCE callback (?code=...&state=...) if present
//   4. Hydrate /auth/debug into state
//   5. Wire sidebar + topbar
//   6. Initialize router (which fires the initial view render)

import { h, mount } from "./lib/dom.js";
import { setState, STORAGE_KEYS } from "./lib/state.js";
import { init as initRouter, navigate } from "./lib/router.js";
import { completeLogin, refreshDebug, isAuthenticated } from "./lib/auth.js";

import { renderSidebar } from "./components/sidebar.js";
import { renderTopbar }  from "./components/topbar.js";
import { toastBad } from "./components/toast.js";

import overviewView    from "./views/overview.js";
import playgroundView  from "./views/playground.js";
import usersView       from "./views/users.js";
import userDetailView  from "./views/user-detail.js";
import rolesView       from "./views/roles.js";
import sessionsView    from "./views/sessions.js";
import invitationsView from "./views/invitations.js";
import auditLogsView   from "./views/auditlogs.js";
import apiExplorerView from "./views/apiexplorer.js";
import swaggerView     from "./views/swagger.js";
import settingsView    from "./views/settings.js";
import docsView, { DOC_MAP } from "./views/docs.js";

// ADMIN_NAV — the existing admin nav, untouched by the docs mode. The
// sidebar renders this set when the active route does NOT start with
// /docs/. Mode preservation guarantee: nothing in the admin behavior
// depends on the docs view being present.
const ADMIN_NAV = [
  { path: "/overview",    title: "Overview",    icon: "▤", section: "MAIN" },
  { path: "/playground",  title: "Playground",  icon: "▷", section: "MAIN" },

  { path: "/users",       title: "Users",       icon: "◉", section: "IDENTITY" },
  { path: "/roles",       title: "Roles",       icon: "◇", section: "IDENTITY" },
  { path: "/sessions",    title: "Sessions",    icon: "◴", section: "IDENTITY" },
  { path: "/invitations", title: "Invitations", icon: "✉", section: "IDENTITY" },

  { path: "/audit-logs",  title: "Audit Logs",  icon: "≣", section: "OBSERVABILITY" },

  { path: "/api-explorer",title: "API Explorer",icon: "⌘", section: "DEVELOPER" },
  { path: "/swagger",     title: "Swagger",     icon: "≡", section: "DEVELOPER" },

  { path: "/settings",    title: "Settings",    icon: "⚙", section: "ADMIN" },
];

// Backward-compat alias — some external integrations may still reference
// the old name. Kept until grep'd out of the tree.
const NAV_ITEMS = ADMIN_NAV;

// DOCS_NAV — derived from views/docs.js DOC_MAP. Generated here (not in
// docs.js) so the sidebar shows the doc list whether or not the view module
// has been loaded yet, and so adding a doc to DOC_MAP is the only step
// required to expose it in the sidebar.
const DOCS_NAV = Object.entries(DOC_MAP).map(([slug, entry]) => ({
  path: "/docs" + (slug ? "/" + slug : ""),
  title: entry.title,
  icon: iconForSection(entry.section),
  section: entry.section,
}));

function iconForSection(section) {
  switch (section) {
    case "DOCUMENTATION":    return "ⓘ";
    case "GETTING STARTED":  return "▶";
    case "ARCHITECTURE":     return "◫";
    case "OPERATIONS":       return "⚙";
    case "MONITORING":       return "◉";
    case "SECURITY":         return "⛨";
    case "RELEASE NOTES":    return "✦";
    default:                 return "•";
  }
}

// ROUTES — admin routes are exactly the prior set; /docs and /docs/* are
// new. The docs route uses a generic ":page+" style by registering /docs
// for the index plus a wildcard route that resolves params.page from the
// remainder of the path. The hash router only honors one :name segment per
// pattern, so we register one route per depth (0, 1, 2) — three patterns
// cover every entry in DOC_MAP today and any future entry up to two
// segments deep.
const ROUTES = {
  "/":             ({ container }) => navigate("/overview"),

  // Admin (existing — untouched).
  "/overview":     overviewView,
  "/playground":   playgroundView,
  "/users":        usersView,
  "/users/:id":    userDetailView,
  "/roles":        rolesView,
  "/sessions":     sessionsView,
  "/invitations":  invitationsView,
  "/audit-logs":   auditLogsView,
  "/api-explorer": apiExplorerView,
  "/swagger":      swaggerView,
  "/settings":     settingsView,

  // Docs.
  "/docs":             (ctx) => docsView({ ...ctx, params: { ...ctx.params, page: "" } }),
  "/docs/:page":       docsView,
  "/docs/:a/:b":       (ctx) => docsView({ ...ctx, params: { ...ctx.params, page: `${ctx.params.a}/${ctx.params.b}` } }),
};

async function boot() {
  applyTheme();

  // 1. Load config
  let config;
  try {
    const r = await fetch("/admin/config.json", { cache: "no-store" });
    if (!r.ok) throw new Error("config.json HTTP " + r.status);
    config = await r.json();
  } catch (e) {
    showBootError("Cannot load /admin/config.json. Is the admin console enabled? (DEV_PLAYGROUND_ENABLED=true)", e);
    return;
  }
  setState({ config });

  // 2. Handle PKCE callback if we landed on a redirect URL
  const url = new URL(window.location.href);
  const code  = url.searchParams.get("code");
  const state = url.searchParams.get("state");
  const err   = url.searchParams.get("error");
  if (err) {
    toastBad((url.searchParams.get("error_description") || err), "Keycloak");
    history.replaceState(null, "", config.redirectUri);
  } else if (code) {
    try {
      await completeLogin(code, state);
    } catch (e) {
      toastBad("token exchange: " + e.message);
    } finally {
      history.replaceState(null, "", config.redirectUri);
    }
  }

  // 3. Restore identity from existing token if any
  if (isAuthenticated()) {
    try { await refreshDebug(); } catch {}
  }

  // 4. Mount sidebar + topbar — both are mode-aware. The sidebar renders
  //    NAV_ITEMS or DOCS_NAV depending on whether the active route lives
  //    under /docs/*. The topbar exposes a permanent Admin/Docs toggle.
  //    The renderers subscribe to state changes, so a route transition
  //    triggers a re-render with the correct nav set automatically.
  renderSidebar("#sidebar", { admin: ADMIN_NAV, docs: DOCS_NAV });
  renderTopbar("#topbar", { admin: ADMIN_NAV, docs: DOCS_NAV }, (q) => {
    // Topbar search currently broadcasts via a custom event; views that
    // care subscribe to it. Keeps the search-vs-view contract tiny.
    window.dispatchEvent(new CustomEvent("admin:search", { detail: q }));
  });

  // 5. Start the router
  document.body.removeAttribute("data-route-loading");
  initRouter({
    routes: ROUTES,
    container: "#main",
  });
}

function applyTheme() {
  const stored = localStorage.getItem(STORAGE_KEYS.theme);
  const theme = stored || "dark";
  document.body.classList.remove("theme-dark", "theme-light");
  document.body.classList.add("theme-" + theme);
  setState({ theme });
}

function showBootError(message, err) {
  const main = document.querySelector("#main");
  mount(main,
    h("div", "empty",
      h("div", "empty-icon", "⚠"),
      h("h3", null, "boot failed"),
      h("p", null, message),
      err ? h("pre", { class: "pre", style: { textAlign: "left" } }, String(err)) : null,
    ),
  );
}

boot();
