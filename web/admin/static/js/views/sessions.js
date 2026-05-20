// sessions.js — REAL GET /admin/sessions + DELETE /admin/sessions/:id
// (Stage A + D). Bulk "Terminate all" is intentionally NOT a backend
// endpoint in v0.2 — surfaced as COMING SOON.

import { h, mount, relativeTime } from "../lib/dom.js";
import { apiTry } from "../lib/api.js";
import { pageHeader, pill, emptyState, spinner, statusBadge, disabledBtn } from "../components/common.js";
import { renderTable } from "../components/table.js";
import { openModal } from "../components/modal.js";
import { toastOk, toastBad } from "../components/toast.js";
import { navigate } from "../lib/router.js";

export default async function sessionsView({ container }) {
  mount(container,
    pageHeader("Active sessions", h("span", null,
      "Live session view aggregated across every enabled client in the realm. Per-session revocation is live. ",
      statusBadge("live"),
    ), [
      // Bulk terminate is not a v0.2 backend endpoint. Keep the button so
      // the IA stays consistent, but never let it fire — disabled + tooltip.
      disabledBtn(h("span", null, "Terminate all ", statusBadge("coming-soon")), {
        classes: ["btn-warn"],
        title: "Disponível em breve",
      }),
    ]),
    h("div", { id: "sess-content" }, h("div", "row", spinner(), h("span", "muted", "loading…"))),
  );

  const r = await apiTry("/admin/sessions");
  const target = container.querySelector("#sess-content");
  if (!target) return;

  if (!r.ok) {
    mount(target, renderError(r));
    return;
  }

  const rows = (r.data.sessions || []).map(s => ({
    ...s,
    client_names: s.clients ? Object.values(s.clients).join(", ") : "—",
  }));

  renderTable(target, {
    toolbar: {
      actions: [h("button", { class: "btn btn-sm", onclick: () => sessionsView({ container }) }, "↻ refresh")],
    },
    columns: [
      { key: "username", title: "User", render: (v) => h("strong", null, v || "—") },
      { key: "ip_address", title: "IP", render: (v) => v ? h("code", null, v) : "—" },
      { key: "client_names", title: "Clients", render: (v) => pill(v, "accent") },
      { key: "started_at",   title: "Started",       render: (v) => v ? relativeTime(v) : "—" },
      { key: "last_access",  title: "Last activity", render: (v) => v ? relativeTime(v) : "—" },
      { key: "_actions", title: "", width: "120px", render: (_, row) => h("button", {
          class: "btn btn-xs btn-bad",
          onclick: (e) => { e.stopPropagation(); confirmKill(row, container); },
        }, "terminate"),
      },
    ],
    rows,
    empty: { title: "No active sessions", body: "Sign in via the Playground to start one." },
  });
}

function renderError(r) {
  if (r.status === 401) {
    return emptyState({
      icon: "🔒",
      title: "Sign in required",
      body: "GET /admin/sessions requires a bearer token.",
      action: h("button", { class: "btn btn-primary", onclick: () => navigate("/playground") }, "Go to Playground"),
    });
  }
  if (r.status === 403) {
    return emptyState({ icon: "⛔", title: "Admin role required", body: "Your token lacks the `admin` realm role." });
  }
  return emptyState({ icon: "✗", title: "Request failed", body: `HTTP ${r.status}: ${r.error?.message || "unknown"}` });
}

function confirmKill(row, container) {
  let busy = false;
  let close;
  close = openModal({
    title: "Terminate session?",
    body: h("div", null,
      h("p", null, "Session ", h("code", null, row.id), " for ", h("strong", null, row.username || "(unknown user)"), "."),
      h("p", "muted text-xs", "Clients: ", row.client_names),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Terminate", bad: true, onClick: () => {
        if (busy) return false;
        busy = true;
        apiTry("/admin/sessions/" + encodeURIComponent(row.id), { method: "DELETE" })
          .then(({ ok, status, error }) => {
            if (ok) {
              toastOk("Session terminated.", "Revoked");
              if (close) close();
              if (container) sessionsView({ container });
            } else {
              toastBad(formatError(status, error), "Terminate failed");
              busy = false;
            }
          });
        return false;
      } },
    ],
  });
}

function formatError(status, error) {
  if (status === 400) return "Malformed session id.";
  if (status === 401) return "Sign in required.";
  if (status === 403) return "Your token lacks the admin role.";
  if (status === 404) return "Session already revoked or expired.";
  if (status === 502) return "Keycloak is unreachable.";
  return "HTTP " + status + ": " + (error?.message || "unknown");
}
