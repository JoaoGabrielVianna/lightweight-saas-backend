// profile.js — current signed-in user: identity, token info, appearance,
// and session management.

import { h, mount } from "../lib/dom.js";
import { pageHeader, card, kvList, pill, emptyState } from "../components/common.js";
import { getState, setState, STORAGE_KEYS } from "../lib/state.js";
import { isAuthenticated, startLogin } from "../lib/auth.js";
import { toastOk } from "../components/toast.js";

export default function profileView({ container }) {
  const state = getState();
  const id    = state.identity;

  if (!isAuthenticated() || !id || !id.valid) {
    mount(container,
      pageHeader("My Profile", "Account information and preferences."),
      emptyState({
        icon:   "○",
        title:  "Not signed in",
        body:   "Sign in to view your profile.",
        action: h("button", { class: "btn btn-primary", onclick: () => startLogin() }, "Sign in"),
      }),
    );
    return;
  }

  const initials = (id.email || id.received_sub || "?").slice(0, 2).toUpperCase();
  const theme    = state.theme || "dark";

  mount(container,
    pageHeader("My Profile", "Account information and preferences for this session."),

    // Row 1: Identity + Token
    h("div", { class: "card-grid", style: { gridTemplateColumns: "1fr 1fr" } },

      card({
        title:    "Identity",
        subtitle: "claims from the current access token",
        body: h("div", "col",

          // Avatar + email
          h("div", { style: "display:flex;align-items:center;gap:16px;padding-bottom:16px;margin-bottom:16px;border-bottom:1px solid color-mix(in srgb,var(--fg) 10%,transparent)" },
            h("div", {
              style: "width:48px;height:48px;border-radius:50%;background:var(--accent);color:#fff;display:flex;align-items:center;justify-content:center;font-size:18px;font-weight:700;flex-shrink:0",
            }, initials),
            h("div", null,
              h("div", { style: "font-weight:600;font-size:var(--fs-lg)" }, id.email || id.received_sub || "—"),
              id.received_sub ? h("div", "muted", h("code", null, id.received_sub)) : null,
            ),
          ),

          // Status pills
          h("div", "row",
            pill(id.valid   ? "valid"   : "invalid", id.valid   ? "ok"  : "bad"),
            pill(id.expired ? "expired" : "live",    id.expired ? "bad" : "ok"),
          ),

          // Roles
          (id.roles || []).length
            ? h("div", { style: "margin-top:12px" },
                h("div", {
                  style: "font-size:var(--fs-xs);text-transform:uppercase;letter-spacing:0.06em;color:var(--fg-dim);margin-bottom:6px",
                }, "Roles"),
                h("div", "row",
                  ...(id.roles || []).map(r => pill(r, r === "admin" ? "accent" : "ok")),
                ),
              )
            : h("p", "muted", "No roles assigned."),
        ),
      }),

      card({
        title:    "Token",
        subtitle: "from /auth/debug",
        body: kvList([
          ["Email",           id.email          || "—"],
          ["Sub",             h("code", null, id.received_sub  || "—")],
          ["Issuer",          h("code", null, id.issuer        || "—")],
          ["AZP",             h("code", null, id.received_azp  || "—")],
          ["Allowed clients", (id.allowed_clients || []).join(", ") || "—"],
        ]),
      }),
    ),

    // Row 2: Appearance + Session
    h("div", { class: "card-grid", style: { gridTemplateColumns: "1fr 1fr" } },

      card({
        title: "Appearance",
        body: h("div", "col",
          h("p", "muted", "Theme persists in localStorage across sessions."),
          h("div", "row",
            h("button", {
              class:   "btn" + (theme === "dark"  ? " btn-primary" : ""),
              onclick: () => applyTheme("dark",  container),
            }, "Dark"),
            h("button", {
              class:   "btn" + (theme === "light" ? " btn-primary" : ""),
              onclick: () => applyTheme("light", container),
            }, "Light"),
          ),
        ),
      }),

      card({
        title:    "Session",
        subtitle: "tokens held in this browser tab only",
        body: h("div", "col",
          h("p", "muted", "Tokens live in sessionStorage and clear when the tab closes. Use this to clear them immediately."),
          h("button", { class: "btn btn-warn", onclick: () => clearSession(container) }, "Clear stored tokens"),
        ),
      }),
    ),
  );
}

function applyTheme(t, container) {
  document.body.classList.remove("theme-dark", "theme-light");
  document.body.classList.add("theme-" + t);
  localStorage.setItem(STORAGE_KEYS.theme, t);
  setState({ theme: t });
  toastOk("Theme set to " + t + ".");
  profileView({ container });
}

function clearSession(container) {
  Object.values(STORAGE_KEYS).forEach((k) => {
    if (k !== STORAGE_KEYS.theme) sessionStorage.removeItem(k);
  });
  setState({ token: null, identity: null });
  toastOk("Session tokens cleared.");
  profileView({ container });
}
