// topbar.js — breadcrumbs + search + theme toggle. Reusable.

import { h, mount } from "../lib/dom.js";
import { getState, subscribe, setState, STORAGE_KEYS } from "../lib/state.js";

/**
 * Render the topbar with breadcrumbs derived from the active route.
 * @param {Element|string} target
 * @param {Array<{path,title}>} navItems  — used to look up titles by path
 * @param {(query:string)=>void} onSearch — called when the search input changes
 */
export function renderTopbar(target, navItems, onSearch) {
  const el = typeof target === "string" ? document.querySelector(target) : target;
  if (!el) return;

  const draw = () => {
    const route = getState().route;
    const theme = getState().theme;
    const item  = (navItems || []).find(n => n.path === (route?.path || ""));

    mount(el,
      h("button", {
        class: "topbar-mobile-trigger",
        "aria-label": "toggle sidebar",
        onclick: () => document.body.classList.toggle("sidebar-open"),
      }, "≡"),
      h("nav", { class: "topbar-breadcrumbs", "aria-label": "breadcrumb" },
        h("span", "crumb", "Admin"),
        h("span", "crumb-sep", "/"),
        h("span", "crumb-current", item ? item.title : (route?.path || "—")),
      ),
      h("div", "topbar-search",
        h("input", {
          type: "search",
          placeholder: "Search this section…",
          "aria-label": "search",
          oninput: (e) => onSearch && onSearch(e.target.value),
        }),
      ),
      h("div", "topbar-actions",
        h("button", {
          class: "btn btn-ghost btn-sm",
          title: "Toggle theme",
          onclick: () => {
            const next = theme === "dark" ? "light" : "dark";
            document.body.classList.remove("theme-dark", "theme-light");
            document.body.classList.add("theme-" + next);
            localStorage.setItem(STORAGE_KEYS.theme, next);
            setState({ theme: next });
          },
        }, theme === "dark" ? "☾" : "☀"),
      ),
    );
  };

  draw();
  subscribe(draw);
}
