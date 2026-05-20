// sidebar.js — primary navigation. Reusable across projects: pass in the
// route table from main.js, get back a sidebar that highlights the active
// entry and reacts to state changes.

import { h, mount } from "../lib/dom.js";
import { getState, subscribe } from "../lib/state.js";
import { currentPath, navigate } from "../lib/router.js";
import { logout, isAuthenticated } from "../lib/auth.js";

/**
 * Render the sidebar into target.
 * @param {Element|string} target
 * @param {Array<{path,title,icon,section?}>} navItems
 */
export function renderSidebar(target, navItems) {
  const el = typeof target === "string" ? document.querySelector(target) : target;
  if (!el) return;

  const draw = () => {
    const path = currentPath();
    const state = getState();

    // Group nav items by section. Items without a section land under MAIN.
    const groups = new Map();
    for (const item of navItems) {
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
        h("h1", null, "IAM Console"),
        h("span", "brand-badge", "v0.1"),
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
