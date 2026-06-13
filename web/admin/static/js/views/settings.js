// settings.js — read-only display of runtime config + theme + storage controls.

import { h, mount } from "../lib/dom.js";
import { pageHeader, card, kvList } from "../components/common.js";
import { getState, setState, STORAGE_KEYS } from "../lib/state.js";
import { toastOk } from "../components/toast.js";

export default async function settingsView({ container }) {
  const cfg = getState().config || {};
  const id  = getState().identity || {};

  mount(container,
    pageHeader("Settings", "Runtime configuration the admin console has loaded. Read-only."),

    card({
      title: "Runtime",
      subtitle: "loaded from /admin/config.json",
      body: kvList([
        ["keycloak URL",   h("code", null, cfg.keycloakUrl || "—")],
        ["realm",          h("code", null, cfg.realm || "—")],
        ["OIDC client",    h("code", null, cfg.clientId || "—")],
        ["redirect URI",   h("code", null, cfg.redirectUri || "—")],
        ["api base",       h("code", null, cfg.apiBase || "(same-origin)")],
      ]),
    }),

    card({
      title: "API expectations",
      subtitle: "from /auth/debug — what the API checks on every token",
      body: id.valid !== undefined
        ? kvList([
            ["expected issuer",  h("code", null, id.issuer || "—")],
            ["allowed clients",  (id.allowed_clients || []).join(", ") || "—"],
          ])
        : h("p", "muted", "sign in via the Playground to populate this section"),
    }),

    card({
      title: "Appearance",
      body: h("div", "col",
        h("p", "muted", "Theme persists in localStorage. The toggle in the top bar does the same thing."),
        h("div", "row",
          h("button", { class: "btn", onclick: () => setTheme("dark") }, "Dark"),
          h("button", { class: "btn", onclick: () => setTheme("light") }, "Light"),
        ),
      ),
    }),

    card({
      title: "Session storage",
      subtitle: "tokens held by THIS browser tab only",
      body: h("div", "col",
        h("p", "muted", "Tokens live in sessionStorage and clear on tab close. Use this if you want to clear them immediately (e.g. before sharing a screenshot)."),
        h("div", "row",
          h("button", { class: "btn btn-warn", onclick: () => clearTokens() }, "Clear stored tokens"),
        ),
      ),
    }),

    card({
      title: "About",
      body: kvList([
        ["console version",   "v0.2 (IAM admin — read + mutations)"],
        ["auth foundation",   "v0.1.0-auth-foundation"],
        ["identity backend",  "/admin/users, /admin/roles, /admin/sessions, /admin/invitations"],
      ]),
    }),
  );
}

function setTheme(t) {
  document.body.classList.remove("theme-dark", "theme-light");
  document.body.classList.add("theme-" + t);
  localStorage.setItem(STORAGE_KEYS.theme, t);
  setState({ theme: t });
  toastOk("theme set to " + t);
}

function clearTokens() {
  Object.values(STORAGE_KEYS).forEach((k) => {
    if (k !== STORAGE_KEYS.theme) sessionStorage.removeItem(k);
  });
  setState({ token: null, identity: null });
  toastOk("session tokens cleared");
}
