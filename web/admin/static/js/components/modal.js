// modal.js — focus-trapped overlay. Reusable.
//
//   openModal({ title, body, actions: [{label, primary?, onClick}] })
//
// Returns a `close()` function. Esc + clicking the backdrop also close.

import { h, mount } from "../lib/dom.js";

const ROOT = "#modal-root";

export function openModal({ title, body, actions, onClose }) {
  const root = document.querySelector(ROOT);
  if (!root) return () => {};

  const closeFns = [];
  const close = () => {
    closeFns.forEach(fn => { try { fn(); } catch {} });
    mount(root); // clear
    document.body.style.removeProperty("overflow");
    if (onClose) onClose();
  };

  const backdrop = h("div", { class: "modal-backdrop", onclick: (e) => { if (e.target === backdrop) close(); } },
    h("div", { class: "modal", role: "dialog", "aria-modal": "true", "aria-label": title || "dialog" },
      h("div", "modal-header",
        h("h3", "modal-title", title || ""),
        h("button", { class: "modal-close", "aria-label": "close", onclick: close }, "×"),
      ),
      h("div", "modal-body", body),
      actions && actions.length
        ? h("div", "modal-footer",
            ...actions.map(a => h("button", {
              class: ["btn", a.primary ? "btn-primary" : "", a.warn ? "btn-warn" : "", a.bad ? "btn-bad" : ""].filter(Boolean).join(" "),
              onclick: () => {
                const r = a.onClick && a.onClick();
                if (r === false) return; // returning false keeps modal open
                close();
              },
            }, a.label))
          )
        : null,
    ),
  );

  // Focus the first input or the close button
  setTimeout(() => {
    const focusable = backdrop.querySelector("input, textarea, select, button:not(.modal-close)");
    if (focusable) focusable.focus();
  }, 0);

  const onKey = (e) => { if (e.key === "Escape") close(); };
  document.addEventListener("keydown", onKey);
  closeFns.push(() => document.removeEventListener("keydown", onKey));

  document.body.style.overflow = "hidden";
  mount(root, backdrop);
  return close;
}
