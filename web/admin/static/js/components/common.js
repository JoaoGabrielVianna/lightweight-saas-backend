// common.js — small primitives that don't justify their own files.

import { h } from "../lib/dom.js";

export function pageHeader(title, subtitle, actions) {
  return h("div", "page-header",
    h("div", null,
      h("h1", "page-title", title),
      subtitle ? h("p", "page-subtitle", subtitle) : null,
    ),
    actions && actions.length ? h("div", "page-actions", ...actions) : null,
  );
}

export function card({ title, subtitle, actions, body }) {
  return h("section", "card",
    (title || actions)
      ? h("header", "card-header",
          h("div", null,
            title ? h("h3", "card-title", title) : null,
            subtitle ? h("p", "card-subtitle", subtitle) : null,
          ),
          actions && actions.length ? h("div", "card-actions", ...actions) : null,
        )
      : null,
    body,
  );
}

export function statCard({ label, value, hint }) {
  return h("div", "stat-card",
    h("div", "stat-label", label),
    h("p", "stat-value", value ?? "—"),
    hint ? h("div", "stat-hint", hint) : null,
  );
}

export function pill(text, kind) {
  return h("span", { class: "pill pill-" + (kind || "neutral") }, text);
}

// statusBadge surfaces feature-implementation status next to a section
// title or button. Variants reflect the v0.2 admin-console release matrix:
//
//   live         — backend integrated, fully usable
//   beta         — backend integrated but with documented caveats
//   coming-soon  — visible affordance, intentionally inactive
//
// We use this instead of removing not-yet-implemented buttons so the IA
// stays stable across releases — users learn the layout once.
const STATUS_LABEL = {
  "live":        "LIVE",
  "beta":        "BETA",
  "coming-soon": "EM BREVE",
};
const STATUS_KIND = {
  "live":        "ok",
  "beta":        "accent",
  "coming-soon": "neutral",
};
export function statusBadge(variant) {
  const v = variant || "live";
  return h("span", {
    class: "pill pill-" + (STATUS_KIND[v] || "neutral"),
    style: { fontSize: "9px", letterSpacing: "0.06em", fontWeight: 700, marginLeft: "8px" },
    title: variant === "coming-soon" ? "Disponível em breve" : "",
  }, STATUS_LABEL[v] || v.toUpperCase());
}

// disabledBtn renders a button whose click is intercepted with a "not yet"
// affordance. The native disabled attribute is set so screenreaders + form
// behavior align with the visual cue.
export function disabledBtn(label, opts) {
  const klass = ["btn"].concat(opts?.classes || []).filter(Boolean).join(" ");
  return h("button", {
    class: klass,
    disabled: true,
    title: opts?.title || "Sem funcionamento ainda",
  }, label);
}

export function spinner(size) {
  return h("span", { class: "spinner" + (size === "lg" ? " spinner-lg" : "") });
}

export function emptyState({ icon, title, body, action }) {
  return h("div", "empty",
    icon ? h("div", "empty-icon", icon) : null,
    h("h3", null, title || ""),
    body ? h("p", null, body) : null,
    action ? h("div", { style: { marginTop: "16px" } }, action) : null,
  );
}

export function codeblock(text) {
  return h("pre", { class: "pre" }, text);
}

// kvList renders a definition-list with our standard .kv styles.
export function kvList(pairs) {
  return h("dl", "kv",
    ...pairs.map(([k, v]) => h("div", null,
      h("dt", null, k),
      h("dd", null, v == null || v === "" ? "—" : v),
    )),
  );
}
