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
