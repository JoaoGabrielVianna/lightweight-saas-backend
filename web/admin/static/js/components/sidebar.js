// sidebar.js — primary navigation. Reusable across projects: pass in the
// route table from main.js, get back a sidebar that highlights the active
// entry and reacts to state changes.

import { h, mount } from "../lib/dom.js";
import { getState, subscribe } from "../lib/state.js";
import { currentPath, navigate } from "../lib/router.js";
import { logout, isAuthenticated } from "../lib/auth.js";

/**
 * Render the sidebar into target.
 *
 * The second argument is either a flat nav array (legacy admin-only shape)
 * or a {admin, docs} pair. When given the pair, the sidebar selects which
 * set to render based on the current route: paths under /docs/* show the
 * docs nav, everything else shows the admin nav. The behaviour is identical
 * to the legacy shape when only `admin` is provided.
 *
 * @param {Element|string} target
 * @param {Array|{admin:Array, docs:Array}} navSpec
 */
export function renderSidebar(target, navSpec) {
  const el = typeof target === "string" ? document.querySelector(target) : target;
  if (!el) return;

  const isPair = navSpec && !Array.isArray(navSpec) && navSpec.admin;
  const adminNav = isPair ? (navSpec.admin || []) : (navSpec || []);
  const docsNav  = isPair ? (navSpec.docs  || []) : [];

  const draw = () => {
    const path = currentPath();
    const state = getState();
    const inDocs = path === "/docs" || path.startsWith("/docs/");
    const activeNav = inDocs && docsNav.length ? docsNav : adminNav;
    const brandLabel = inDocs ? "Docs" : "IAM Console";

    // Group nav items by section. Items without a section land under MAIN.
    const groups = new Map();
    for (const item of activeNav) {
      const key = item.section || "MAIN";
      if (!groups.has(key)) groups.set(key, []);
      groups.get(key).push(item);
    }

    const sections = [];
    for (const [name, items] of groups) {
      sections.push(
        h("div", "sidebar-section",
          h("div", "sidebar-section-title", name),
          h("nav", "sidebar-nav",
            ...items.map(it => h("a", {
              class: ["sidebar-link", path === it.path ? "active" : ""].filter(Boolean).join(" "),
              href: "#" + it.path,
              onclick: (e) => { e.preventDefault(); navigate(it.path); },
            },
              h("span", { class: "sidebar-link-icon", html: it.icon || "•" }),
              h("span", null, it.title),
            )),
          ),
        )
      );
    }

    mount(el,
      h("div", "sidebar-brand",
        h("h1", null, brandLabel),
        h("span", "brand-badge", inDocs ? "docs" : "v0.1"),
      ),
      ...sections,
      h("div", "sidebar-footer",
        renderUserCard(state),
      ),
    );
  };

  draw();
  subscribe(draw);
}

function renderUserCard(state) {
  const id = state.identity;
  if (!id || !id.valid) {
    return h("div", { class: "muted text-xs", style: { padding: "4px 8px" } }, "not signed in");
  }
  const initials = (id.email || id.received_sub || "?").slice(0, 2).toUpperCase();
  return h("div", "sidebar-user",
    h("div", "sidebar-user-avatar", initials),
    h("div", "sidebar-user-meta",
      h("div", "sidebar-user-name", id.email || id.received_sub || "—"),
      h("div", "sidebar-user-sub", (id.roles || []).join(", ") || "no roles"),
    ),
    h("button", {
      class: "btn btn-ghost btn-xs",
      title: "Logout",
      onclick: () => {
        if (confirm("Log out and end the Keycloak session?")) {
          if (isAuthenticated()) logout();
        }
      },
    }, "↪"),
  );
}
