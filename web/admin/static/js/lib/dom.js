// dom.js — vanilla DOM helpers. No framework, no virtual DOM.
//
// Pattern: `h(tag, props, ...children)` returns an HTMLElement. props can be
// an attribute map, a class string, or any property name supported by the
// element. Children are nodes, strings, numbers, arrays (flattened), or
// nullish (skipped).
//
// Example:
//   h("button", { class: "btn primary", onclick: doThing }, "Save")
//   h("div", { class: "card" }, h("h2", null, "Title"), bodyEl)

export function h(tag, props = null, ...children) {
  const el = document.createElement(tag);

  if (props && typeof props === "string") {
    el.className = props;
  } else if (props) {
    for (const [k, v] of Object.entries(props)) {
      if (v == null || v === false) continue;
      if (k === "class" || k === "className") {
        el.className = Array.isArray(v) ? v.filter(Boolean).join(" ") : v;
      } else if (k === "style" && typeof v === "object") {
        Object.assign(el.style, v);
      } else if (k === "dataset" && typeof v === "object") {
        for (const [dk, dv] of Object.entries(v)) el.dataset[dk] = String(dv);
      } else if (k === "html") {
        el.innerHTML = String(v);
      } else if (k.startsWith("on") && typeof v === "function") {
        el.addEventListener(k.slice(2).toLowerCase(), v);
      } else if (typeof v === "boolean") {
        if (v) el.setAttribute(k, "");
      } else {
        el.setAttribute(k, String(v));
      }
    }
  }

  appendChildren(el, children);
  return el;
}

function appendChildren(parent, children) {
  for (const c of children.flat(Infinity)) {
    if (c == null || c === false) continue;
    if (c instanceof Node) {
      parent.appendChild(c);
    } else {
      parent.appendChild(document.createTextNode(String(c)));
    }
  }
}

// Replace the entire content of an element with new children.
export function mount(target, ...children) {
  if (typeof target === "string") target = document.querySelector(target);
  if (!target) return;
  target.innerHTML = "";
  appendChildren(target, children);
}

// Escape user-controlled strings before interpolating into HTML.
export function esc(s) {
  return String(s ?? "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

// Format an ISO timestamp / Date / unix-seconds number into a relative
// string like "3m ago" or "2d ago". For absolute display, use toISOString().
export function relativeTime(input) {
  if (!input) return "—";
  const d = input instanceof Date
    ? input
    : typeof input === "number"
      ? new Date(input * (input > 1e12 ? 1 : 1000)) // detect ms vs s
      : new Date(input);
  if (isNaN(d.getTime())) return "—";
  const s = Math.floor((Date.now() - d.getTime()) / 1000);
  if (s < 60)         return s + "s ago";
  if (s < 3600)       return Math.floor(s / 60) + "m ago";
  if (s < 86400)      return Math.floor(s / 3600) + "h ago";
  if (s < 86400 * 30) return Math.floor(s / 86400) + "d ago";
  return d.toISOString().slice(0, 10);
}
