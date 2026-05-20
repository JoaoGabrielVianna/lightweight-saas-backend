// playground.js — relocated /dev/auth flow.
//
// Same PKCE → /me → /auth/debug pattern as the standalone playground,
// rebuilt with the admin design system. The legacy /dev/auth page keeps
// working unchanged; this is a parallel host inside the console.

import { h, mount } from "../lib/dom.js";
import { apiTry } from "../lib/api.js";
import { startLogin, refreshToken, logout, refreshDebug, isAuthenticated, decodeJwtAll, maskToken } from "../lib/auth.js";
import { getState, STORAGE_KEYS } from "../lib/state.js";
import { pageHeader, card, kvList, pill, codeblock, emptyState } from "../components/common.js";
import { toast, toastOk, toastBad } from "../components/toast.js";

let countdownTimer = null;

export default async function playgroundView({ container }) {
  stopCountdown();

  mount(container,
    pageHeader("Playground", "Full Keycloak Authorization Code + PKCE flow with token introspection."),

    card({
      title: "Authentication",
      subtitle: (() => {
        const id = getState().identity;
        return id?.valid
          ? `signed in as ${id.email || id.received_sub}`
          : "not signed in";
      })(),
      actions: renderAuthActions(),
      body: renderAuthBody(),
    }),

    isAuthenticated() ? card({
      title: "Token introspection",
      subtitle: "source: GET /auth/debug",
      body: h("div", { id: "pg-introspect" }, h("span", "muted", "loading…")),
    }) : null,

    isAuthenticated() ? card({
      title: "API calls",
      subtitle: "exercises the protected endpoints with the active token",
      body: renderApiBody(container),
    }) : null,

    isAuthenticated() ? card({
      title: "Raw payloads",
      body: renderRawBody(),
    }) : null,
  );

  if (isAuthenticated()) {
    // populate introspection panel
    const r = await apiTry("/auth/debug");
    if (r.ok) {
      const introspectEl = document.querySelector("#pg-introspect");
      if (introspectEl) mount(introspectEl, renderIntrospection(r.data));
      // Track the live countdown if we have an exp
      if (r.data.exp) startCountdown(r.data.exp);
    } else {
      const introspectEl = document.querySelector("#pg-introspect");
      if (introspectEl) mount(introspectEl, h("div", "muted", "could not load /auth/debug: " + r.status));
    }
  }
}

function renderAuthActions() {
  if (!isAuthenticated()) {
    return [
      h("button", { class: "btn btn-primary", onclick: () => startLogin().catch(e => toastBad(e.message)) },
        "Login with Keycloak"),
    ];
  }
  return [
    sessionStorage.getItem(STORAGE_KEYS.refreshToken)
      ? h("button", { class: "btn", onclick: async () => {
          try { await refreshToken(); toastOk("token refreshed"); reload(); }
          catch (e) { toastBad(e.message); }
        }}, "Refresh token")
      : null,
    h("button", { class: "btn", onclick: async () => {
        const t = sessionStorage.getItem(STORAGE_KEYS.accessToken);
        if (t) { await navigator.clipboard.writeText(t); toastOk("token copied"); }
      }}, "Copy token"),
    h("button", { class: "btn btn-warn", onclick: () => logout() }, "Logout"),
  ].filter(Boolean);
}

function renderAuthBody() {
  if (!isAuthenticated()) {
    return emptyState({
      icon: "○",
      title: "Not signed in",
      body: "Click Login with Keycloak. You'll redirect to the realm login page and return here with a token.",
    });
  }
  const t = sessionStorage.getItem(STORAGE_KEYS.accessToken);
  const id = getState().identity || {};
  return kvList([
    ["access token (masked)", h("code", null, maskToken(t))],
    ["expires in", h("code", { id: "pg-countdown" }, "—")],
    ["sub",      h("code", null, id.received_sub || "—")],
    ["email",    id.email || "—"],
    ["azp",      h("code", null, id.received_azp || "—")],
    ["roles",    (id.roles || []).length ? (id.roles || []).join(", ") : "—"],
  ]);
}

function renderApiBody(container) {
  return h("div", "col",
    h("div", "row",
      h("button", { class: "btn", onclick: () => callApi("/me",     "pg-out-me") }, "GET /me"),
      h("button", { class: "btn", onclick: () => callApi("/auth/debug", "pg-out-debug") }, "GET /auth/debug"),
      h("button", { class: "btn", onclick: () => callApi("/health", "pg-out-health") }, "GET /health"),
    ),
    h("details", "disclosure",
      h("summary", null, "/me output"),
      h("pre", { class: "pre", id: "pg-out-me" }, "—"),
    ),
    h("details", "disclosure",
      h("summary", null, "/auth/debug output"),
      h("pre", { class: "pre", id: "pg-out-debug" }, "—"),
    ),
    h("details", "disclosure",
      h("summary", null, "/health output"),
      h("pre", { class: "pre", id: "pg-out-health" }, "—"),
    ),
  );
}

function renderRawBody() {
  const t = sessionStorage.getItem(STORAGE_KEYS.accessToken);
  const decoded = decodeJwtAll(t);
  return h("div", "col",
    h("details", "disclosure",
      h("summary", null, "decoded JWT (local decode — cosmetic only, NOT validation)"),
      h("pre", { class: "pre" }, decoded ? JSON.stringify(decoded, null, 2) : "—"),
    ),
    h("details", "disclosure",
      h("summary", null, "/auth/debug response"),
      h("pre", { class: "pre" }, JSON.stringify(getState().identity || {}, null, 2)),
    ),
  );
}

async function callApi(path, outId) {
  const out = document.getElementById(outId);
  if (out) out.textContent = "…";
  const r = await apiTry(path);
  if (out) {
    out.textContent = r.ok
      ? JSON.stringify(r.data, null, 2)
      : `HTTP ${r.status}\n${JSON.stringify(r.error?.body, null, 2)}`;
  }
}

function renderIntrospection(d) {
  return h("div", null,
    h("div", "row",
      pill(d.valid ? "valid" : "invalid", d.valid ? "ok" : "bad"),
      d.exp ? pill(d.expired ? "expired" : "live", d.expired ? "bad" : "ok") : pill("no exp", "warn"),
    ),
    kvList([
      ["issuer",          h("code", null, d.issuer || "—")],
      ["azp",             h("code", null, d.received_azp || "—")],
      ["sub",             h("code", null, d.received_sub || "—")],
      ["email",           d.email || "—"],
      ["roles",           (d.roles || []).join(", ") || "—"],
      ["aud",             (d.aud || []).join(", ") || "—"],
      ["iat",             h("code", null, d.iat || "—")],
      ["exp",             h("code", null, d.exp || "—")],
      ["allowed clients", (d.allowed_clients || []).join(", ")],
    ]),
    !d.valid && d.reason
      ? h("div", { class: "muted", style: { marginTop: "12px" } },
          h("strong", null, "reason: "),
          h("code", null, d.reason),
        )
      : null,
  );
}

function reload() {
  refreshDebug().then(() => {
    const route = getState().route;
    if (route?.path === "/playground") {
      playgroundView({ container: document.querySelector("#main") });
    }
  });
}

function startCountdown(expISO) {
  stopCountdown();
  const target = new Date(expISO).getTime();
  const tick = () => {
    const el = document.querySelector("#pg-countdown");
    if (!el) return;
    const ms = target - Date.now();
    el.textContent = ms > 0 ? formatRemaining(ms) : "expired";
  };
  tick();
  countdownTimer = setInterval(tick, 1000);
}
function stopCountdown() {
  if (countdownTimer) { clearInterval(countdownTimer); countdownTimer = null; }
}
function formatRemaining(ms) {
  const s = Math.floor(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const r = s % 60;
  if (h) return `${h}h ${m}m ${r}s`;
  if (m) return `${m}m ${r}s`;
  return `${r}s`;
}
