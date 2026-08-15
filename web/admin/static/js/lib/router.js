// router.js — hash-based SPA router.
//
// Routes are declared as a flat object: path → view module. Path can include
// :param tokens (e.g. "/users/:id"). The matched view receives a context
// object: { params, query, container }.

import { setState } from "./state.js";

let _routes = [];
let _onChange = null;
let _container = null;
let _notFoundView = null;

export function init({ routes, container, onChange, notFound }) {
  _routes = Object.entries(routes).map(([pattern, view]) => ({
    pattern,
    view,
    regex: patternToRegex(pattern),
    params: extractParams(pattern),
  }));
  _container = typeof container === "string" ? document.querySelector(container) : container;
  _onChange = onChange;
  _notFoundView = notFound;

  window.addEventListener("hashchange", handleChange);
  window.addEventListener("DOMContentLoaded", handleChange);

  // If the DOM is already ready (script loaded async), fire once.
  if (document.readyState !== "loading") {
    handleChange();
  }
}

export function navigate(path) {
  if (window.location.hash === "#" + path) {
    handleChange();
  } else {
    window.location.hash = path;
  }
}

export function currentPath() {
  return window.location.hash.replace(/^#/, "") || "/";
}

// ─── Workspace-scoped routes ────────────────────────────────────────────────
//
// Slice 6 decision: the workspace lives in the ROUTE for every view whose
// content depends on it, and nowhere in the route for views that do not.
//
//   /workspaces/ws_x/users        ← workspace-scoped identity
//   /overview, /audit-logs, /email ← installation-scoped, deliberately not
//
// Why route over application state:
//
//   - the hash router already supports multi-segment params (/docs/:a/:b), so
//     the churn is one prefix on nav paths, not a router rewrite;
//   - two realms in two browser tabs is the product's whole point, and shared
//     application state cannot express it;
//   - refresh, bookmark, back-button and deep link all become correct for
//     free, instead of each needing a persistence rule;
//   - it makes the Phase 13 requirement structural rather than a convention:
//     the SMTP and email-template pages CANNOT be accidentally workspace-
//     scoped, because their URLs have nowhere to put a workspace.
//
// wsRoute is the single builder, the routing counterpart to api.js's wsPath.
// Views never interpolate a workspace id into a hash path by hand.
export function wsRoute(workspaceId, path = "") {
  const id = typeof workspaceId === "string" ? workspaceId.trim() : "";
  if (!id) throw new Error("wsRoute: a workspace id is required");

  const rel = (typeof path === "string" ? path.trim() : "").replace(/^\/+/, "");
  const base = "/workspaces/" + id;
  return rel ? base + "/" + rel : base;
}

// isWorkspaceRoute — whether a path carries a workspace segment. Used by the
// shell to decide whether the workspace selector is meaningful on this page.
export function isWorkspaceRoute(path) {
  return typeof path === "string" && /^\/workspaces\/ws_[^/]+/.test(path);
}

// workspaceIdFromPath — read the workspace id out of a path, or null. The
// route params already carry it for the matched view; this is for the shell,
// which runs before and across route matches.
export function workspaceIdFromPath(path) {
  const m = typeof path === "string" ? path.match(/^\/workspaces\/(ws_[^/?]+)/) : null;
  return m ? m[1] : null;
}

// swapWorkspaceInPath — keep the operator on the same PAGE while changing
// workspace. Switching from A's Roles page should land on B's Roles page, not
// bounce everyone to a landing screen: the page is what the operator was
// doing, and the workspace is the context they were doing it in.
//
// Entity-level segments are deliberately dropped. `/workspaces/A/users/<uuid>`
// becomes `/workspaces/B/users`, because that uuid names a user in A's realm
// and would 404 — or, far worse in a product where realms can be clones of one
// another, resolve to a DIFFERENT person in B.
export function swapWorkspaceInPath(path, nextWorkspaceId) {
  const rest = typeof path === "string" ? path.replace(/^\/workspaces\/ws_[^/]+/, "") : "";
  const section = (rest.split("?")[0].split("/").filter(Boolean)[0]) || "";
  return wsRoute(nextWorkspaceId, section);
}

function handleChange() {
  const raw = currentPath();
  const [pathPart, queryStr] = raw.split("?");
  const query = parseQuery(queryStr);

  for (const r of _routes) {
    const m = r.regex.exec(pathPart);
    if (m) {
      const params = {};
      r.params.forEach((name, i) => { params[name] = decodeURIComponent(m[i + 1] || ""); });

      const route = { path: pathPart, pattern: r.pattern, params, query };
      setState({ route });

      // Reset main scroll position on every nav.
      window.scrollTo({ top: 0, behavior: "instant" });

      if (_onChange) _onChange(route);
      try {
        r.view({ params, query, container: _container });
      } catch (err) {
        console.error("view render failed:", err);
        renderError(err);
      }
      return;
    }
  }
  if (_notFoundView) _notFoundView({ container: _container });
}

function patternToRegex(pattern) {
  const escaped = pattern.replace(/[.+*?^${}()|[\]\\]/g, "\\$&");
  const withParams = escaped.replace(/:([a-zA-Z_]\w*)/g, "([^/]+)");
  return new RegExp("^" + withParams + "/?$");
}

function extractParams(pattern) {
  const names = [];
  pattern.replace(/:([a-zA-Z_]\w*)/g, (_, n) => names.push(n));
  return names;
}

function parseQuery(s) {
  const out = {};
  if (!s) return out;
  for (const pair of s.split("&")) {
    const [k, v] = pair.split("=");
    if (!k) continue;
    out[decodeURIComponent(k)] = decodeURIComponent(v || "");
  }
  return out;
}

function renderError(err) {
  _container.innerHTML = "";
  const wrap = document.createElement("div");
  wrap.className = "empty";
  wrap.innerHTML = `
    <div class="empty-icon">⚠</div>
    <h3>view crashed</h3>
    <pre class="pre" style="text-align:left">${(err && err.stack || String(err))
      .replace(/&/g, "&amp;").replace(/</g, "&lt;")}</pre>`;
  _container.appendChild(wrap);
}
