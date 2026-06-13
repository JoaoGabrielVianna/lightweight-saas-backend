// settings.js — runtime configuration display (developer-facing, read-only).

import { h, mount } from "../lib/dom.js";
import { pageHeader, card, kvList } from "../components/common.js";
import { getState } from "../lib/state.js";

export default async function settingsView({ container }) {
  const cfg = getState().config || {};
  const id  = getState().identity || {};

  mount(container,
    pageHeader("Settings", "Runtime configuration loaded from /admin/config.json. Read-only."),

    h("div", { class: "card-grid", style: { gridTemplateColumns: "1fr 1fr" } },
      card({
        title: "Runtime",
        subtitle: "/admin/config.json",
        body: kvList([
          ["Keycloak URL",  h("code", null, cfg.keycloakUrl  || "—")],
          ["Realm",         h("code", null, cfg.realm        || "—")],
          ["OIDC client",   h("code", null, cfg.clientId     || "—")],
          ["Redirect URI",  h("code", null, cfg.redirectUri  || "—")],
          ["API base",      h("code", null, cfg.apiBase      || "(same-origin)")],
        ]),
      }),

      card({
        title: "API expectations",
        subtitle: "what the backend checks on every token",
        body: id.valid !== undefined
          ? kvList([
              ["Expected issuer",  h("code", null, id.issuer || "—")],
              ["Allowed clients",  h("code", null, (id.allowed_clients || []).join(", ") || "—")],
            ])
          : h("p", "muted", "Sign in to populate this section."),
      }),
    ),

    card({
      title: "About",
      body: kvList([
        ["Console version",  "v0.2 — IAM admin (read + mutations)"],
        ["Auth foundation",  "v0.1.0-auth-foundation"],
        ["Backend routes",   "/admin/users, /admin/roles, /admin/sessions, /admin/invitations"],
      ]),
    }),
  );
}
