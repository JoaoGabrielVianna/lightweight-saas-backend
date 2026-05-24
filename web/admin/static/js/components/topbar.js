// topbar.js — breadcrumbs + search + theme toggle + Admin/Docs mode toggle
// + Docs-mode language toggle (EN / PT-BR).
//
// The language toggle is the ONLY i18n-aware element here. The rest of
// the topbar chrome stays in English regardless of the active locale —
// translation exists solely to make long-form docs prose readable for
// the maintainer.

import { h, mount } from "../lib/dom.js";
import { getState, subscribe, setState, STORAGE_KEYS } from "../lib/state.js";
import { currentPath, navigate } from "../lib/router.js";
import { LOCALES, LOCALE_LABEL, getLocale, setLocale } from "../lib/locale.js";

// STORAGE_KEY for "last admin route visited", so toggling Admin→Docs→Admin
// returns the user to the same admin page they were last on. Same shape for
// the docs side. Kept here (not in state.js) because they're topbar-internal.
const LAST_ADMIN_KEY = "admin_last_admin_path";
const LAST_DOCS_KEY  = "admin_last_docs_path";

/**
 * Render the topbar with breadcrumbs derived from the active route.
 *
 * The second argument is either a flat nav array (legacy shape) or a
 * {admin, docs} pair (new shape). When the pair is passed, the topbar
 * shows a permanent mode toggle ([Admin] [Docs]); when only an array is
 * passed, the toggle is hidden and behavior is unchanged from before.
 *
 * @param {Element|string} target
 * @param {Array|{admin:Array, docs:Array}} navSpec
 * @param {(query:string)=>void} onSearch
 */
export function renderTopbar(target, navSpec, onSearch) {
  const el = typeof target === "string" ? document.querySelector(target) : target;
  if (!el) return;

  const isPair = navSpec && !Array.isArray(navSpec) && navSpec.admin;
  const adminNav = isPair ? (navSpec.admin || []) : (navSpec || []);
  const docsNav  = isPair ? (navSpec.docs  || []) : [];
  const hasToggle = isPair && docsNav.length > 0;

  const draw = () => {
    const route = getState().route;
    const theme = getState().theme;
    const path  = currentPath();
    const inDocs = path === "/docs" || path.startsWith("/docs/");
    const activeNav = inDocs ? docsNav : adminNav;
    const item  = activeNav.find(n => n.path === (route?.path || "")) ||
                  // Fall back to nearest-prefix match for parametric routes.
                  activeNav.find(n => (route?.path || "").startsWith(n.path));

    // Persist the latest path per mode so toggling back is non-destructive.
    try {
      if (inDocs) localStorage.setItem(LAST_DOCS_KEY, path);
      else        localStorage.setItem(LAST_ADMIN_KEY, path);
    } catch {}

    mount(el,
      h("button", {
        class: "topbar-mobile-trigger",
        "aria-label": "toggle sidebar",
        onclick: () => document.body.classList.toggle("sidebar-open"),
      }, "≡"),
      h("nav", { class: "topbar-breadcrumbs", "aria-label": "breadcrumb" },
        h("span", "crumb", inDocs ? "Docs" : "Admin"),
        h("span", "crumb-sep", "/"),
        h("span", "crumb-current", item ? item.title : (route?.path || "—")),
      ),
      h("div", "topbar-search",
        h("input", {
          type: "search",
          placeholder: inDocs ? "Search this doc…" : "Search this section…",
          "aria-label": "search",
          oninput: (e) => onSearch && onSearch(e.target.value),
        }),
      ),
      h("div", "topbar-actions",
        // Docs-only language toggle. The toggle stays in admin chrome's
        // English regardless of the active locale — it's a control, not
        // translated content. Rendered FIRST in the actions cluster so
        // it sits at the most-scanned position on the page.
        inDocs ? renderLangToggle() : null,
        hasToggle ? renderModeToggle(inDocs) : null,
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

// renderModeToggle — segmented [Admin] [Docs]. Clicking the inactive side
// switches mode and restores the user's last-visited path in that mode,
// falling back to a sensible landing page when none was recorded.
function renderModeToggle(inDocs) {
  return h("div", { class: "mode-toggle", role: "group", "aria-label": "Mode" },
    h("button", {
      class: "mode-toggle-btn" + (!inDocs ? " active" : ""),
      "aria-pressed": String(!inDocs),
      title: "Switch to Admin",
      onclick: () => {
        if (!inDocs) return;
        let target = "/overview";
        try { target = localStorage.getItem(LAST_ADMIN_KEY) || "/overview"; } catch {}
        if (target.startsWith("/docs")) target = "/overview";
        navigate(target);
      },
    }, "Admin"),
    h("button", {
      class: "mode-toggle-btn" + (inDocs ? " active" : ""),
      "aria-pressed": String(inDocs),
      title: "Switch to Docs",
      onclick: () => {
        if (inDocs) return;
        let target = "/docs";
        try { target = localStorage.getItem(LAST_DOCS_KEY) || "/docs"; } catch {}
        if (!target.startsWith("/docs")) target = "/docs";
        navigate(target);
      },
    }, "Docs"),
  );
}

// renderLangToggle — segmented [EN] [PT-BR], visible only in Docs mode.
// Selecting the inactive side persists the choice (localStorage via
// setLocale) and broadcasts it through shared state, which re-renders
// this topbar and the Docs view (the latter subscribes separately so it
// re-fetches the markdown sibling for the new locale).
//
// Visibility choices:
//   - A small "Lang" prefix sits inside the same border as the buttons,
//     so the control reads as a labelled language picker rather than
//     two anonymous letter chips that could be mistaken for tabs.
//   - The wrapper carries its own .docs-lang-toggle class so docs.css
//     can style it independently of the [Admin]/[Docs] mode toggle
//     (higher contrast, never collapses on small viewports).
//   - Placed first in the topbar-actions cluster, so it's the leftmost
//     of the three controls on the right edge of the topbar.
function renderLangToggle() {
  const active = getLocale();
  return h("div", {
    class: "mode-toggle docs-lang-toggle",
    role: "group",
    "aria-label": "Documentation language",
    title: "Documentation language",
  },
    h("span", "docs-lang-toggle-label", "Lang"),
    ...LOCALES.map(loc =>
      h("button", {
        class: "mode-toggle-btn" + (active === loc ? " active" : ""),
        "aria-pressed": String(active === loc),
        title: "Switch documentation to " + (LOCALE_LABEL[loc] || loc),
        onclick: () => { if (active !== loc) setLocale(loc); },
      }, LOCALE_LABEL[loc] || loc),
    ),
  );
}
