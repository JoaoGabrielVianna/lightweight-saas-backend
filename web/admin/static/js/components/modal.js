// modal.js — focus-trapped overlay. Reusable.
//
//   openModal({ title, body, actions: [{label, primary?, onClick}] })
//
// Returns a `close()` function. Esc + clicking the backdrop also close.

import { h, mount } from "../lib/dom.js";

const ROOT = "#modal-root";

// _openModals tracks every modal currently on screen so the shell can tear
// them all down at once.
//
// This exists for one specific defect class (Slice 6, Phase 9): an operator
// opens "Delete user" in workspace A, switches to workspace B while the
// dialog is up, and clicks Delete. The dialog's closure still holds A's user
// id, but the mutation would be built for whatever workspace is current —
// deleting an unrelated account, or nothing, in B's realm. Neither outcome is
// acceptable, and disabling the button is not enough: the safe move is that a
// workspace switch dismisses every workspace-scoped dialog outright.
const _openModals = new Set();

/**
 * closeAllModals — dismiss every open modal.
 *
 * Called on workspace switch. Safe to call when nothing is open.
 */
export function closeAllModals() {
  for (const close of Array.from(_openModals)) {
    try { close(); } catch { /* a modal that fails to close must not block the rest */ }
  }
  _openModals.clear();
}

/** openModalCount — test seam; how many modals are currently registered. */
export function openModalCount() {
  return _openModals.size;
}

export function openModal({ title, body, actions, onClose }) {
  const root = document.querySelector(ROOT);
  if (!root) return () => {};

  const closeFns = [];
  const close = () => {
    _openModals.delete(close);
    closeFns.forEach(fn => { try { fn(); } catch {} });
    mount(root); // clear
    document.body.style.removeProperty("overflow");
    if (onClose) onClose();
  };
  _openModals.add(close);

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
