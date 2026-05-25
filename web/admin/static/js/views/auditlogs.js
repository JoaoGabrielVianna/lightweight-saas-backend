// auditlogs.js — minimum-viable viewer over the in-process audit ring
// buffer (GET /admin/audit-events). The buffer is bounded, process-local,
// and volatile; the view labels each of those limitations explicitly so
// operators don't mistake it for a durable trail.

import { h, mount, relativeTime } from "../lib/dom.js";
import { apiTry } from "../lib/api.js";
import { pageHeader, pill, emptyState, spinner, statusBadge } from "../components/common.js";
import { renderTable } from "../components/table.js";
import { navigate } from "../lib/router.js";

export default async function auditLogsView({ container }) {
  mount(container,
    pageHeader("Audit Logs", h("span", null,
      "Recent identity-relevant events emitted by the admin layer. ",
      "Process-local, volatile, bounded ring buffer — the durable trail is the structured log stream. ",
      statusBadge("beta"),
    ), [
      h("button", {
        class: "btn btn-sm",
        onclick: () => auditLogsView({ container }),
      }, "↻ refresh"),
    ]),
    h("div", { id: "audit-content" },
      h("div", "row", spinner(), h("span", "muted", "loading…")),
    ),
  );

  const r = await apiTry("/admin/audit-events");
  const target = container.querySelector("#audit-content");
  if (!target) return; // user navigated away

  if (!r.ok) {
    mount(target, renderError(r));
    return;
  }

  const events = r.data.events || [];
  const capacity = r.data.capacity || 0;
  const dropped = r.data.dropped || 0;

  if (events.length === 0) {
    mount(target, emptyState({
      icon: "≣",
      title: "No events recorded yet",
      body: "Audit events appear here as soon as an admin mutates a user, role, session, or invitation. Try creating an invitation or assigning a role from the Users page.",
      action: h("button", { class: "btn", onclick: () => navigate("/users") }, "Go to Users"),
    }));
    return;
  }

  renderTable(target, {
    toolbar: {
      actions: [
        h("span", "muted text-xs", `${events.length} / ${capacity} in buffer` + (dropped > 0 ? ` · ${dropped} dropped` : "")),
      ],
    },
    columns: [
      { key: "ts", title: "When", width: "120px",
        render: (v) => h("span", { title: v }, relativeTime(v)) },
      { key: "action", title: "Action",
        render: (v) => h("code", null, v) },
      { key: "actor", title: "Actor",
        render: (_, row) => renderActor(row.actor) },
      { key: "target", title: "Target",
        render: (_, row) => renderTarget(row.target) },
      { key: "ip", title: "IP", width: "120px",
        render: (v) => v ? h("code", "muted", v) : "—" },
      { key: "reason", title: "Reason",
        render: (v) => v ? h("span", "muted text-xs", v) : pill("ok", "ok") },
    ],
    rows: events,
    empty: { title: "No events", body: "" }, // unreachable — guarded above
  });
}

function renderActor(actor) {
  if (!actor) return "—";
  const label = actor.email || actor.username || actor.subject;
  if (!label) return h("span", "muted", "unknown");
  return h("span", null,
    h("strong", null, label),
    actor.subject && actor.subject !== label
      ? h("div", "muted text-xs", h("code", null, actor.subject))
      : null,
  );
}

function renderTarget(target) {
  if (!target) return "—";
  const label = target.name || target.id;
  if (!label && !target.kind) return "—";
  return h("span", null,
    target.kind ? pill(target.kind, "accent") : null,
    " ",
    label ? h("span", null, label) : h("span", "muted", "—"),
  );
}

function renderError(r) {
  if (r.status === 401) {
    return emptyState({
      icon: "🔒",
      title: "Sign in required",
      body: "GET /admin/audit-events requires a bearer token.",
      action: h("button", { class: "btn btn-primary", onclick: () => navigate("/playground") }, "Go to Playground"),
    });
  }
  if (r.status === 403) {
    return emptyState({ icon: "⛔", title: "Admin role required", body: "Your token lacks the `admin` realm role." });
  }
  if (r.status === 404) {
    return emptyState({
      icon: "○",
      title: "Audit endpoint not mounted",
      body: "The /admin/audit-events route is only registered when identity management is configured.",
    });
  }
  return emptyState({ icon: "✗", title: "Request failed", body: `HTTP ${r.status}: ${r.error?.message || "unknown"}` });
}
