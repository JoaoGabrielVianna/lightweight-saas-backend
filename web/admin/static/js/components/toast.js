// toast.js — bottom-right notification stack.

import { h } from "../lib/dom.js";

const STACK = "#toast";
const TTL_MS = 4500;

export function toast(opts) {
  const stack = document.querySelector(STACK);
  if (!stack) return;

  // Allow toast("text") shorthand
  if (typeof opts === "string") opts = { body: opts };

  const variant = opts.kind || "info"; // info | ok | bad | warn
  const node = h("div", { class: "toast " + (variant === "info" ? "" : "toast-" + variant) },
    opts.title ? h("div", "toast-title", opts.title) : null,
    h("div", "toast-body", opts.body || ""),
  );
  stack.appendChild(node);

  const drop = () => { node.remove(); };
  const t = setTimeout(drop, opts.ttl || TTL_MS);
  node.addEventListener("click", () => { clearTimeout(t); drop(); });
}

export const toastOk  = (msg, title) => toast({ kind: "ok",  body: msg, title });
export const toastBad = (msg, title) => toast({ kind: "bad", body: msg, title });
export const toastWarn = (msg, title) => toast({ kind: "warn", body: msg, title });
