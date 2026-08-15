// sessions.js — workspace-scoped realm sessions.
//
// Slice 6: migrated from /admin/sessions to
// GET /v1/workspaces/{workspace_id}/sessions and
// DELETE /v1/workspaces/{workspace_id}/sessions/{session_id}.
//
// Bulk "Terminate all" is still not a backend capability on either surface —
// surfaced as COMING SOON, unchanged.

import { h, mount, relativeTime } from "../lib/dom.js";
import {
  apiTry, wsPath, wsMutate, enterWorkspace, captureWorkspaceToken, isWorkspaceStale,
} from "../lib/workspaces.js";
import { pageHeader, pill, spinner, statusBadge, disabledBtn } from "../components/common.js";
import { renderTable } from "../components/table.js";
import { openModal } from "../components/modal.js";
import { toastOk, toastBad } from "../components/toast.js";
import {
  renderGateState, renderAPIError, connectionBanner, writeBlockedReason, describeFailure,
} from "../components/ws-states.js";

export default async function sessionsView({ container, params }) {
  const workspaceId = params.workspace_id;

  mount(container,
    pageHeader("Active sessions", h("span", null,
      "Live sessions aggregated across every enabled client in this workspace's realm. Per-session revocation is live. ",
      statusBadge("live"),
    ), [
      disabledBtn(h("span", null, "Terminate all ", statusBadge("coming-soon")), {
        classes: ["btn-warn"],
        title: "Disponível em breve",
      }),
    ]),
    h("div", { id: "sess-content" }, h("div", "row", spinner(), h("span", "muted", "loading…"))),
  );

  const gate = await enterWorkspace(workspaceId);
  let target = container.querySelector("#sess-content");
  if (!target) return;
  if (!gate.ok) {
    mount(target, renderGateState(gate, { onRetry: () => sessionsView({ container, params }) }));
    return;
  }
  const blocked = writeBlockedReason(gate.connectionState, gate.connection);

  const token = captureWorkspaceToken();
  const r = await apiTry(wsPath(workspaceId, "/sessions"), { signal: token.signal });
  if (isWorkspaceStale(token)) return;

  target = container.querySelector("#sess-content");
  if (!target) return;
  if (!r.ok) {
    mount(target, renderAPIError(r, { onRetry: () => sessionsView({ container, params }) }));
    return;
  }

  const rows = (r.data.sessions || []).map(s => ({
    ...s,
    client_names: s.clients ? Object.values(s.clients).join(", ") : "—",
  }));

  mount(target,
    connectionBanner(gate.connectionState, gate.connection, { workspaceId }),
    h("div", { id: "sess-table" }),
  );

  renderTable(container.querySelector("#sess-table"), {
    toolbar: {
      actions: [h("button", { class: "btn btn-sm", onclick: () => sessionsView({ container, params }) }, "↻ refresh")],
    },
    columns: [
      { key: "username", title: "User", render: (v) => h("strong", null, v || "—") },
      { key: "ip_address", title: "IP", render: (v) => v ? h("code", null, v) : "—" },
      { key: "client_names", title: "Clients", render: (v) => pill(v, "accent") },
      { key: "started_at",   title: "Started",       render: (v) => v ? relativeTime(v) : "—" },
      { key: "last_access",  title: "Last activity", render: (v) => v ? relativeTime(v) : "—" },
      { key: "_actions", title: "", width: "120px", render: (_, row) => h("button", {
          class: "btn btn-xs btn-bad",
          disabled: !!blocked,
          title: blocked || "Terminate this session",
          onclick: (e) => { e.stopPropagation(); confirmKill(row, container, workspaceId, params); },
        }, "terminate"),
      },
    ],
    rows,
    empty: { title: "No active sessions", body: "Nobody is signed in to this workspace's realm right now." },
  });
}

function confirmKill(row, container, workspaceId, params) {
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
        // The session id belongs to workspaceId's realm; wsMutate refuses to
        // send it anywhere else.
        wsMutate(workspaceId, "/sessions/" + encodeURIComponent(row.id), { method: "DELETE" })
          .then((res) => {
            if (res.stale) { if (close) close(); return; }
            if (res.ok) {
              toastOk("Session terminated.", "Revoked");
              if (close) close();
              sessionsView({ container, params });
            } else {
              toastBad(describeFailure(res), "Terminate failed");
              busy = false;
            }
          });
        return false;
      } },
    ],
  });
}
