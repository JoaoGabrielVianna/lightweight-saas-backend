// workspace-audit.js — the durable audit trail for one workspace.
//
// Distinct from views/auditlogs.js, and the distinction is the point. That view
// reads /admin/audit-events: a process-local, volatile ring buffer with no
// workspace, which answers "what just happened on this box". This one reads
// GET /v1/workspaces/{id}/audit: durable, workspace-scoped history that
// survives a restart.
//
// Deliberately a table and three filters. No charts, no aggregation, no
// timeline: the question an operator brings here is "who changed this, and
// when", and every pixel spent on anything else is a pixel not spent on the
// answer.
//
// Pagination is cursor-based because the underlying table only grows at the
// head — see docs/AUDIT.md. The consequence for the UI is that there is no page
// count and no "jump to page": there is a "load more" button, and its absence
// means there is no more history.

import { h, mount, relativeTime } from "../lib/dom.js";
import { apiTry } from "../lib/api.js";
import { pageHeader, pill, emptyState, spinner, statusBadge } from "../components/common.js";
import { renderTable } from "../components/table.js";

// The filters offered. Deliberately the three the server supports as indexed
// predicates, not everything it accepts: a filter that scans is a filter that
// times out on the workspace that most needs it.
const OUTCOMES = ["", "success", "failure"];
const ACTOR_TYPES = ["", "operator", "project"];

export default async function workspaceAuditView({ container, params }) {
  const workspaceId = params.workspace_id;

  // Filter state lives here rather than in the URL.
  //
  // The workspace IS in the URL, because it is an authorization boundary and a
  // deep link must resolve to the right realm. A filter is a transient view
  // preference: putting it in the hash would make every dropdown change a
  // history entry, and the back button would walk filters instead of pages.
  const state = { event: "", actorType: "", outcome: "", items: [], cursor: null, loading: false };

  mount(container,
    pageHeader("Audit", h("span", null,
      "Durable history of every change made in this workspace, newest first. ",
      "Survives restarts. ",
      statusBadge("live"),
    ), [
      h("button", {
        class: "btn btn-sm",
        onclick: () => reload(),
      }, "↻ refresh"),
    ]),
    filterBar(state, () => reload()),
    h("div", { id: "audit-content" },
      h("div", "row", spinner(), h("span", "muted", "loading…")),
    ),
  );

  async function reload() {
    state.items = [];
    state.cursor = null;
    await loadMore({ replace: true });
  }

  async function loadMore({ replace = false } = {}) {
    if (state.loading) return;
    state.loading = true;

    const target = container.querySelector("#audit-content");
    if (!target) return; // navigated away
    if (replace) {
      mount(target, h("div", "row", spinner(), h("span", "muted", "loading…")));
    }

    const r = await apiTry(auditPath(workspaceId, state));
    state.loading = false;

    const stillHere = container.querySelector("#audit-content");
    if (!stillHere) return;

    if (!r.ok) {
      mount(stillHere, renderError(r));
      return;
    }

    state.items = state.items.concat(r.data.items || []);
    state.cursor = (r.data.pagination && r.data.pagination.next_cursor) || null;
    mount(stillHere, renderEvents(state, () => loadMore()));
  }

  await reload();
}

// auditPath builds the request. Filters are omitted when empty rather than sent
// as blanks, so the server sees the query an operator actually asked for.
function auditPath(workspaceId, state) {
  const q = new URLSearchParams();
  if (state.event) q.set("event", state.event);
  if (state.actorType) q.set("actor_type", state.actorType);
  if (state.outcome) q.set("outcome", state.outcome);
  if (state.cursor) q.set("cursor", state.cursor);

  const qs = q.toString();
  return `/v1/workspaces/${workspaceId}/audit${qs ? "?" + qs : ""}`;
}

function filterBar(state, onChange) {
  const select = (label, key, options) =>
    h("label", "filter",
      h("span", "muted", label),
      h("select", {
        class: "input input-sm",
        onchange: (e) => { state[key] = e.target.value; onChange(); },
      }, ...options.map((v) =>
        h("option", { value: v, selected: state[key] === v || undefined }, v || "any"))),
    );

  return h("div", "row filters",
    select("Outcome", "outcome", OUTCOMES),
    select("Actor", "actorType", ACTOR_TYPES),
    h("label", "filter",
      h("span", "muted", "Event"),
      h("input", {
        class: "input input-sm",
        placeholder: "e.g. user.created",
        value: state.event,
        // On change rather than on input: filtering on every keystroke would
        // issue a request per character against a table that only grows.
        onchange: (e) => { state.event = e.target.value.trim(); onChange(); },
      })),
  );
}

function renderEvents(state, onLoadMore) {
  if (state.items.length === 0) {
    return emptyState({
      icon: "≣",
      title: "No events match",
      body: anyFilterActive(state)
        ? "No audit events in this workspace match these filters."
        : "Nothing has been changed in this workspace yet. Events appear here as soon as " +
          "someone creates, updates or deletes something.",
    });
  }

  // renderTable's contract is renderTable(target, {columns, rows}) — the
  // target first, columns as {key, title, render} objects, rows as the raw
  // records. This view previously called it with a single argument and with
  // columns as plain strings and rows as arrays of elements, which threw
  // `Cannot read properties of undefined (reading 'columns')` on the first
  // event the workspace ever recorded. Because the throw happened after an
  // await, router.js's try/catch never saw it: the page simply stayed on
  // "loading…" forever, and the only evidence was in the browser console.
  //
  // Found by the Slice 11 browser suite. See tests/audit-view.test.mjs for the
  // regression guard at this level.
  const tableEl = h("div");
  renderTable(tableEl, {
    columns: [
      { key: "occurred_at", title: "Time",     render: (v) => h("span", { title: v || "" }, relativeTime(v)) },
      { key: "event",       title: "Event",    render: (v) => h("code", null, v) },
      { key: "actor",       title: "Actor",    render: (v) => actorCell(v) },
      { key: "resource",    title: "Resource", render: (v) => resourceCell(v) },
      // Reads two fields (outcome and reason_code), so it takes the row.
      { key: "outcome",     title: "Outcome",  render: (_, row) => outcomeCell(row) },
    ],
    rows: state.items,
    empty: { title: "No events", body: "" },
  });

  return h("div", null,
    tableEl,
    h("div", "row muted",
      h("span", null, `${state.items.length} event${state.items.length === 1 ? "" : "s"} loaded`),
      // The button's ABSENCE is the end-of-history signal, which is what the
      // API's absent next_cursor means. Showing a disabled button instead would
      // leave an operator wondering whether it failed.
      state.cursor
        ? h("button", { class: "btn btn-sm", onclick: onLoadMore }, "load more")
        : h("span", "muted", "· end of history"),
    ),
  );
}

// actorCell renders the two disjoint actor shapes differently, so a machine is
// never mistaken for a person at a glance — which is the whole reason the
// underlying record keeps their fields apart.
function actorCell(actor) {
  if (!actor) return h("span", "muted", "—");

  if (actor.type === "project") {
    return h("span", null,
      pill("project"), " ",
      h("code", { title: `credential ${actor.credential_id || "?"}` },
        shortId(actor.project_id)),
    );
  }
  return h("span", null,
    pill("operator"), " ",
    h("span", { title: actor.subject || "" }, actor.email || shortId(actor.subject)),
  );
}

function resourceCell(resource) {
  if (!resource) return h("span", "muted", "—");
  return h("span", null,
    h("span", "muted", resource.type), " ",
    h("code", { title: resource.id }, shortId(resource.id)),
  );
}

function outcomeCell(e) {
  if (e.outcome === "success") return pill("success");
  // The reason CODE, never a message: the trail stores a code from a closed
  // vocabulary precisely so nothing upstream can put a secret here.
  return h("span", null,
    pill("failure"), " ",
    e.reason_code ? h("code", "muted", e.reason_code) : null,
  );
}

function anyFilterActive(state) {
  return Boolean(state.event || state.actorType || state.outcome);
}

// shortId keeps a table cell readable. The full value is in the title, so it is
// still copyable — an operator investigating needs the whole id.
function shortId(id) {
  if (!id) return "—";
  return id.length > 20 ? id.slice(0, 20) + "…" : id;
}

function renderError(r) {
  // audit_unavailable is the one an operator can act on: the trail is stored in
  // PostgreSQL, so it means the database is unreachable rather than that
  // anything about audit is misconfigured.
  const code = (r.data && r.data.error && r.data.error.code) || "";
  if (code === "audit_unavailable") {
    return emptyState({
      icon: "⚠",
      title: "Audit history is temporarily unavailable",
      body: "The audit trail lives in this installation's PostgreSQL database. " +
        "This usually means the database is unreachable — check /health/ready.",
    });
  }
  return emptyState({
    icon: "⚠",
    title: "Could not load the audit trail",
    body: (r.data && r.data.error && r.data.error.message) || r.error || "Unexpected error.",
  });
}
