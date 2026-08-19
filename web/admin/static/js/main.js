// main.js — admin console entry point.
//
// Boot sequence:
//   1. Load theme from localStorage and apply to <body>
//   2. Fetch /admin/config.json
//   3. Handle PKCE callback (?code=...&state=...) if present
//   4. Hydrate /auth/debug into state, and stop here with an explicit denial
//      screen when the session does not carry the console's role
//   5. Wire sidebar + topbar
//   6. Initialize router (which fires the initial view render)

import { h, mount } from "./lib/dom.js";
import { setState, getState, STORAGE_KEYS } from "./lib/state.js";
import { init as initRouter, navigate, wsRoute, workspaceIdFromPath, currentPath } from "./lib/router.js";
import { completeLogin, refreshDebug, isAuthenticated, startLogin, logout } from "./lib/auth.js";
import { evaluateConsoleAccess } from "./lib/access.js";
import {
  initWorkspaces, selectWorkspace, getCurrentWorkspaceId, loadActiveConnection,
  onWorkspaceSwitch,
} from "./lib/workspaces.js";

import { renderSidebar } from "./components/sidebar.js";
import { renderTopbar }  from "./components/topbar.js";
import { toastBad } from "./components/toast.js";
import { renderAccessDenied } from "./components/access-denied.js";
import { closeAllModals } from "./components/modal.js";

import overviewView    from "./views/overview.js";
import workspacesView  from "./views/workspaces.js";
import connectionsView from "./views/connections.js";
import projectsView, { projectDetailView } from "./views/projects.js";
import workspaceAuditView from "./views/workspace-audit.js";
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
import emailView           from "./views/email.js";
import emailTemplatesView  from "./views/email-templates.js";
import docsView, { DOC_MAP } from "./views/docs.js";

// ADMIN_NAV_FULL — the maximal nav. The boot sequence prunes it down based
// on /admin/config.json flags (`devTools`, `apiExplorer`) so production
// deployments running with DEV_PLAYGROUND_ENABLED=false don't expose
// /playground or /api-explorer in the sidebar.
//
// The sidebar renders the result when the active route does NOT start with
// /docs/. Mode preservation guarantee: nothing in the admin behavior depends
// on the docs view being present.
// Entries carrying `ws: true` are WORKSPACE-SCOPED: their real path is
// /workspaces/<selected>/<path>, and the sidebar rewrites them at draw time
// against whichever workspace is current. Entries without it are
// installation-scoped and have no workspace in their URL — see lib/router.js
// for why that split is in the route rather than in application state.
//
// The WORKSPACE section is the control plane for workspaces themselves. It is
// deliberately above IDENTITY: an operator with no workspace has to go there
// first, and an operator debugging a broken realm goes there next.
const ADMIN_NAV_FULL = [
  { path: "/overview",    title: "Overview",    icon: "▤", section: "MAIN" },
  { path: "/playground",  title: "Playground",  icon: "▷", section: "MAIN",        devOnly: true },

  { path: "/workspaces",  title: "Workspaces",  icon: "◫", section: "WORKSPACE" },
  { path: "/connections", title: "Connections", icon: "⇄", section: "WORKSPACE", ws: true },
  { path: "/projects",    title: "Projects",    icon: "⚿", section: "WORKSPACE", ws: true },

  { path: "/users",       title: "Users",       icon: "◉", section: "IDENTITY", ws: true },
  { path: "/roles",       title: "Roles",       icon: "◇", section: "IDENTITY", ws: true },
  { path: "/sessions",    title: "Sessions",    icon: "◴", section: "IDENTITY", ws: true },
  { path: "/invitations", title: "Invitations", icon: "✉", section: "IDENTITY", ws: true },

  // Workspace-scoped and DURABLE, unlike /audit-logs below, which is the
  // process-local ring. Both exist because they answer different questions;
  // the titles say which.
  { path: "/audit",       title: "Audit",       icon: "≡", section: "OBSERVABILITY", ws: true },

  { path: "/audit-logs",  title: "Recent (all)",icon: "≣", section: "OBSERVABILITY" },

  { path: "/api-explorer",title: "API Explorer",icon: "⌘", section: "DEVELOPER",   apiExplorerOnly: true },
  { path: "/swagger",     title: "Swagger",     icon: "≡", section: "DEVELOPER" },

  // ── Legacy provider settings ─────────────────────────────────────────────
  // Slice 6, Phase 13: SMTP and email templates have NO /v1 equivalent — they
  // are realm settings, not identity administration, and migrating them means
  // designing that seam (a slice, not a step). They stay on /admin/*, and they
  // stay in their own clearly-named section so nobody reads them as applying
  // to the selected workspace. They act on the realm in this installation's
  // KEYCLOAK_* configuration, whichever workspace is selected.
  { path: "/email",            title: "Email / SMTP",      icon: "✉", section: "LEGACY PROVIDER SETTINGS" },
  { path: "/email-templates",  title: "Email templates",   icon: "✏", section: "LEGACY PROVIDER SETTINGS" },

  { path: "/settings",        title: "Settings",           icon: "⚙", section: "ADMIN" },
];

// pruneNav drops Playground when devTools is false, and API Explorer when
// apiExplorer is false. Defaults are conservative (false = hide) so a
// missing config field never accidentally exposes a dev surface.
function pruneNav(items, config) {
  const showDevTools = !!config?.devTools;
  const showApiExplorer = !!config?.apiExplorer;
  return items.filter((it) => {
    if (it.devOnly && !showDevTools) return false;
    if (it.apiExplorerOnly && !showApiExplorer) return false;
    return true;
  });
}

// ADMIN_NAV is computed once at boot from ADMIN_NAV_FULL + the loaded
// config. Kept as a `let` so the sidebar can re-read it after boot if the
// flags ever change at runtime; today they don't.
let ADMIN_NAV = ADMIN_NAV_FULL;

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
// gateDevToolView wraps a view function so direct navigation to a hidden
// dev surface (e.g. someone typing #/playground in production) bounces to
// /overview instead of rendering the surface. Belt-and-braces with the
// pruned nav — the SPA still ships the view module, so the route guard is
// what actually hides it.
function gateDevToolView(view, flagName) {
  return (ctx) => {
    if (!getState().config?.[flagName]) {
      navigate("/overview");
      return;
    }
    return view(ctx);
  };
}

// legacyIdentityRedirect — the pre-Slice-6 flat identity paths (/users,
// /roles, …) now redirect into the selected workspace.
//
// They are kept as routes rather than deleted because they are what is in
// every operator's browser history and in the docs written before this slice.
// A 404-ish "view crashed" screen for a bookmark that worked yesterday is a
// worse answer than landing them on the same page in their current workspace.
//
// With no workspace selectable the redirect goes to /workspaces, which
// explains what to do — NOT to /admin/*, which would silently show a realm
// nobody chose.
function legacyIdentityRedirect(section) {
  return () => {
    const id = getCurrentWorkspaceId();
    navigate(id ? wsRoute(id, section) : "/workspaces");
  };
}

const ROUTES = {
  "/":             () => navigate("/overview"),

  // Installation-scoped. No workspace in the URL, deliberately.
  "/overview":     overviewView,
  "/playground":   gateDevToolView(playgroundView, "devTools"),
  "/audit-logs":   auditLogsView,
  "/api-explorer": gateDevToolView(apiExplorerView, "apiExplorer"),
  "/swagger":      swaggerView,
  "/settings":     settingsView,

  // Legacy provider settings — /admin/*, NOT workspace-scoped. See the nav
  // comment above and Phase 13.
  "/email":            emailView,
  "/email-templates":  emailTemplatesView,

  // Workspace control plane.
  "/workspaces":                            workspacesView,
  "/workspaces/:workspace_id":              ({ params }) => navigate(wsRoute(params.workspace_id, "users")),
  "/workspaces/:workspace_id/connections":  connectionsView,

  // Projects and their machine credentials. Operator-only on the server, so
  // these views are reachable only by a console session — a project credential
  // gets operator_only from every endpoint behind them.
  "/workspaces/:workspace_id/projects":              projectsView,
  "/workspaces/:workspace_id/projects/:project_id":  projectDetailView,

  // Workspace-scoped identity. The workspace id is a route param, so a deep
  // link, a reload, and a second browser tab all resolve to the right realm
  // without consulting any stored preference.
  "/workspaces/:workspace_id/users":           usersView,
  "/workspaces/:workspace_id/users/:user_id":  userDetailView,
  "/workspaces/:workspace_id/roles":           rolesView,
  "/workspaces/:workspace_id/sessions":        sessionsView,
  "/workspaces/:workspace_id/invitations":     invitationsView,

  // The durable, workspace-scoped audit trail. Operator-only in practice
  // (the console holds an operator token), though the same endpoint serves a
  // project credential carrying audit:read.
  "/workspaces/:workspace_id/audit":           workspaceAuditView,

  // Pre-Slice-6 bookmarks.
  "/users":        legacyIdentityRedirect("users"),
  "/users/:id":    ({ params }) => {
    // The user id is dropped on purpose: it named a user in whichever realm
    // the legacy /admin surface pointed at, and that is not necessarily the
    // selected workspace's realm. Resolving it into the current workspace
    // could open a DIFFERENT person's record.
    const id = getCurrentWorkspaceId();
    navigate(id ? wsRoute(id, "users") : "/workspaces");
  },
  "/roles":        legacyIdentityRedirect("roles"),
  "/sessions":     legacyIdentityRedirect("sessions"),
  "/invitations":  legacyIdentityRedirect("invitations"),

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

  // Prune the sidebar nav based on the server's devTools / apiExplorer
  // flags. In production deployments (ADMIN_CONSOLE_ENABLED=true,
  // DEV_PLAYGROUND_ENABLED=false) both flags are false → Playground and
  // API Explorer disappear from the sidebar. Belt-and-braces with the
  // gateDevToolView route guards above.
  ADMIN_NAV = pruneNav(ADMIN_NAV_FULL, config);

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
  } else {
    await startLogin();
    return;
  }

  // 3b. Authorization gate.
  //
  // Being authenticated is not the same as being allowed in, and the console
  // used to conflate the two: any account in the realm reached a fully drawn
  // console where every request then failed with 403. The server was never
  // fooled — RequireRole → RequireLiveAdmin refuses those calls and still
  // does, and it remains the boundary — but the operator was, so we say it
  // once, here, instead of scattering it across a dozen failed panels.
  //
  // BEFORE the workspace load, which is the first thing that would 403, and
  // before the sidebar exists, because a nav to pages that cannot answer is
  // part of the misleading UI being removed.
  const access = evaluateConsoleAccess(getState().identity);
  if (!access.allowed) {
    showAccessDenied(access);
    return;
  }

  // 4. Workspace context.
  //
  // AFTER authentication, because /v1/workspaces requires a bearer token, and
  // BEFORE the router starts, so the first view already knows which workspace
  // it is in rather than rendering an empty state and then correcting itself.
  //
  // Note the boundary this respects: the operator authenticated once, against
  // the installation's realm. Selecting a workspace is NOT a second login, and
  // the console never authenticates the operator against a workspace's realm —
  // the connection's service account does that, server-side, and its
  // credentials never reach this browser.
  //
  // Phase 9: a workspace switch closes every open dialog before anything from
  // the new workspace can render behind it. A "Delete user" confirmation left
  // open across a switch is the exact defect this prevents.
  onWorkspaceSwitch(() => closeAllModals());

  await initWorkspaces({ routeId: workspaceIdFromPath(currentPath()) });
  if (getCurrentWorkspaceId()) {
    // Not awaited: the connection's health decorates the shell and gates
    // mutation controls, but no view should wait on it to paint. Views that
    // need it call enterWorkspace, which awaits the same in-flight load.
    loadActiveConnection(getCurrentWorkspaceId());
  }

  // 5. Mount sidebar + topbar — both are mode-aware. The sidebar renders
  //    NAV_ITEMS or DOCS_NAV depending on whether the active route lives
  //    under /docs/*. The topbar exposes a permanent Admin/Docs toggle and
  //    the workspace selector. The renderers subscribe to state changes, so a
  //    route transition or a workspace switch re-renders them automatically.
  renderSidebar("#sidebar", { admin: ADMIN_NAV, docs: DOCS_NAV });
  renderTopbar("#topbar", { admin: ADMIN_NAV, docs: DOCS_NAV }, (q) => {
    // Topbar search currently broadcasts via a custom event; views that
    // care subscribe to it. Keeps the search-vs-view contract tiny.
    window.dispatchEvent(new CustomEvent("admin:search", { detail: q }));
  });

  // 6. Start the router
  document.body.removeAttribute("data-route-loading");
  initRouter({
    routes: ROUTES,
    container: "#main",
    onChange: syncWorkspaceFromRoute,
  });
}

// syncWorkspaceFromRoute — the route is the source of truth for which
// workspace is current, so every navigation reconciles state to it.
//
// This runs BEFORE the view renders (router.js calls onChange first), which is
// what guarantees a view never briefly renders workspace A's context under
// workspace B's URL. It is also the single place a switch happens, whether it
// came from the selector, a deep link, the back button, or a second tab —
// one code path, so the teardown in selectWorkspace cannot be skipped by
// arriving through a different door.
function syncWorkspaceFromRoute(route) {
  const routeWorkspaceId = workspaceIdFromPath(route?.path || "");
  if (!routeWorkspaceId) return; // installation-scoped page; leave selection alone
  if (routeWorkspaceId === getCurrentWorkspaceId()) return;

  selectWorkspace(routeWorkspaceId);
  loadActiveConnection(routeWorkspaceId);
}

function applyTheme() {
  const stored = localStorage.getItem(STORAGE_KEYS.theme);
  const theme = stored || "dark";
  document.body.classList.remove("theme-dark", "theme-light");
  document.body.classList.add("theme-" + theme);
  setState({ theme });
}

// showAccessDenied paints the denial screen and stops the boot. Sidebar,
// topbar and router are never initialized: there is nothing to navigate to,
// and a nav rail around an access error suggests otherwise.
//
// Retry is a full reload rather than a re-run of the boot sequence. The
// unverified state means we do not know what half-loaded state we are in, and
// reload is the one recovery path with no such doubt.
function showAccessDenied(decision) {
  document.body.removeAttribute("data-route-loading");
  mount(document.querySelector("#main"),
    renderAccessDenied(decision, {
      onSignOut: () => logout(),
      onRetry:   () => window.location.reload(),
    }),
  );
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
