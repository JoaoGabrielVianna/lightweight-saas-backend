// table.js — reusable data table with toolbar, search filter, sortable
// columns, and built-in pagination controls.
//
// Usage:
//   renderTable(container, {
//     columns: [{key:"username", title:"Username"}, {key:"email", title:"Email"}],
//     rows: [...],
//     onRowClick: (row) => {...},
//     toolbar: { search: true, actions: [h("button"...)] },
//     pagination: { first: 0, max: 20, total: undefined, onChange: fn },
//     empty: { title:"No users", body:"…" },
//   });
//
// State (search/page) is owned by the caller — pass new props to re-render.

import { h, mount, esc } from "../lib/dom.js";

export function renderTable(target, opts) {
  const el = typeof target === "string" ? document.querySelector(target) : target;
  if (!el) return;

  const cols = opts.columns || [];
  const rows = opts.rows || [];
  const onRowClick = opts.onRowClick;
  const toolbar = opts.toolbar || {};
  const pag = opts.pagination;
  const empty = opts.empty || { title: "No data", body: "" };

  // Toolbar
  let toolbarEl = null;
  if (toolbar.search || (toolbar.actions && toolbar.actions.length)) {
    toolbarEl = h("div", "table-toolbar",
      toolbar.search
        ? h("input", {
            type: "search",
            placeholder: toolbar.placeholder || "Filter…",
            "aria-label": "filter table",
            oninput: (e) => toolbar.onSearch && toolbar.onSearch(e.target.value),
            value: toolbar.value || "",
          })
        : null,
      ...(toolbar.actions || []),
    );
  }

  // Table body
  let bodyEl;
  if (rows.length === 0) {
    bodyEl = h("div", "empty",
      h("div", "empty-icon", "∅"),
      h("h3", null, empty.title),
      h("p", null, empty.body || ""),
    );
  } else {
    bodyEl = h("table", "table",
      h("thead", null,
        h("tr", null,
          ...cols.map(c => h("th", { style: c.width ? { width: c.width } : null }, c.title || c.key)),
        ),
      ),
      h("tbody", null,
        ...rows.map(row => {
          const tr = h("tr", { class: onRowClick ? "clickable" : "" },
            ...cols.map(c => {
              const v = row[c.key];
              if (c.render) return h("td", null, c.render(v, row));
              if (v == null || v === "") return h("td", { class: "dim" }, "—");
              return h("td", null, String(v));
            }),
          );
          if (onRowClick) tr.addEventListener("click", () => onRowClick(row));
          return tr;
        }),
      ),
    );
  }

  // Pagination
  let pagerEl = null;
  if (pag) {
    const first = pag.first || 0;
    const max   = pag.max || 20;
    const have  = rows.length;
    pagerEl = h("div", "table-pagination",
      h("span", null, `showing ${first + 1}–${first + have}`),
      h("div", "pager",
        h("button", {
          class: "btn btn-sm",
          disabled: first === 0,
          onclick: () => pag.onChange && pag.onChange({ first: Math.max(0, first - max), max }),
        }, "← prev"),
        h("button", {
          class: "btn btn-sm",
          disabled: have < max,
          onclick: () => pag.onChange && pag.onChange({ first: first + max, max }),
        }, "next →"),
      ),
    );
  }

  mount(el,
    h("div", "table-wrap",
      toolbarEl,
      bodyEl,
      pagerEl,
    ),
  );
}
