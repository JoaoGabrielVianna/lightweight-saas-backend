// roles.js — workspace-scoped realm roles.
//
// Slice 6: migrated from /admin/roles to
// /v1/workspaces/{workspace_id}/roles (list, create, update, delete).
//
// Mutations are gated twice: the buttons are disabled when the workspace's
// connection cannot write, and every request goes through wsMutate, which
// refuses to send an action composed for a workspace the operator has left.

import { h, mount } from "../lib/dom.js";
import {
  apiTry, wsPath, wsMutate, enterWorkspace, captureWorkspaceToken, isWorkspaceStale,
} from "../lib/workspaces.js";
import { pageHeader, pill, spinner, statusBadge } from "../components/common.js";
import { renderTable } from "../components/table.js";
import { openModal } from "../components/modal.js";
import { toastOk, toastBad } from "../components/toast.js";
import {
  renderGateState, renderAPIError, connectionBanner, writeBlockedReason, describeFailure,
} from "../components/ws-states.js";

export default async function rolesView({ container, params }) {
  const workspaceId = params.workspace_id;

  mount(container,
    pageHeader("Roles", h("span", null,
      "Realm roles in this workspace. Create, edit and delete are wired to the workspace API. ",
      statusBadge("live"),
    ), [
      h("button", {
        class: "btn btn-primary",
        id: "roles-new-btn",
        onclick: () => openRoleModal(null, container, workspaceId),
      }, "+ New role"),
    ]),
    h("div", { id: "roles-content" }, h("div", "row", spinner(), h("span", "muted", "loading…"))),
  );

  const gate = await enterWorkspace(workspaceId);
  let target = container.querySelector("#roles-content");
  if (!target) return;
  if (!gate.ok) {
    mount(target, renderGateState(gate, { onRetry: () => rolesView({ container, params }) }));
    return;
  }

  const blocked = writeBlockedReason(gate.connectionState, gate.connection);
  const newBtn = container.querySelector("#roles-new-btn");
  if (newBtn && blocked) {
    newBtn.disabled = true;
    newBtn.title = blocked;
  }

  const token = captureWorkspaceToken();
  const r = await apiTry(wsPath(workspaceId, "/roles"), { signal: token.signal });
  if (isWorkspaceStale(token)) return;

  target = container.querySelector("#roles-content");
  if (!target) return;
  if (!r.ok) {
    mount(target, renderAPIError(r, { onRetry: () => rolesView({ container, params }) }));
    return;
  }

  const rows = r.data.roles || [];

  mount(target,
    connectionBanner(gate.connectionState, gate.connection, { workspaceId }),
    h("div", { id: "roles-table" }),
  );

  renderTable(container.querySelector("#roles-table"), {
    toolbar: {
      actions: [h("button", { class: "btn btn-sm", onclick: () => rolesView({ container, params }) }, "↻ refresh")],
    },
    columns: [
      { key: "name", title: "Name", render: (v, row) => h("span", null,
          h("strong", null, v),
          row.builtin ? h("span", { style: { marginLeft: "8px" } }, pill("built-in", "neutral")) : null,
          row.composite ? h("span", { style: { marginLeft: "8px" } }, pill("composite", "accent")) : null,
        ),
      },
      { key: "description", title: "Description" },
      { key: "_actions", title: "", width: "180px", render: (_, row) => h("div", "row",
          h("button", {
            class: "btn btn-xs",
            disabled: !!blocked,
            onclick: (e) => { e.stopPropagation(); openRoleModal(row, container, workspaceId); },
            title: blocked || (row.builtin ? "Built-in roles: description-only edit" : "Edit"),
          }, "edit"),
          h("button", {
            class: "btn btn-xs btn-bad",
            disabled: row.builtin || !!blocked,
            title: blocked || (row.builtin ? "Built-in roles cannot be deleted" : "Delete"),
            onclick: (e) => { e.stopPropagation(); confirmDelete(row, container, workspaceId, params); },
          }, "del"),
        ),
      },
    ],
    rows,
    // Read-only workspaces still open the modal — it shows the role's
    // description. The save button inside is what refuses.
    onRowClick: (row) => openRoleModal(row, container, workspaceId, !!blocked),
    empty: { title: "No roles in this realm", body: "Click + New role to create one." },
  });
}

function openRoleModal(existing, container, workspaceId, readOnly = false) {
  const isEdit = !!existing;
  const name = h("input", {
    type: "text",
    value: existing?.name || "",
    placeholder: "e.g. editor",
    disabled: isEdit,
    title: isEdit ? "Role rename is not supported" : "",
  });
  const desc = h("input", {
    type: "text",
    value: existing?.description || "",
    placeholder: "Short description",
    disabled: readOnly,
  });

  let busy = false;
  let close;
  close = openModal({
    title: isEdit ? "Edit role: " + existing.name : "New role",
    body: h("div", "col",
      h("label", null, h("div", "muted", "name"), name),
      h("label", null, h("div", "muted", "description"), desc),
      existing?.composite ? h("p", "muted text-xs", "This role is a composite (grants other roles transitively). Composition editing is not in this release.") : null,
      existing?.builtin ? h("p", "muted text-xs", "Built-in roles can have their description edited but not their name.") : null,
      readOnly ? h("p", "muted text-xs", "This workspace's connection cannot write, so changes cannot be saved.") : null,
    ),
    actions: readOnly ? [{ label: "Close" }] : [
      { label: "Cancel" },
      { label: isEdit ? "Save changes" : "Create role", primary: true, onClick: () => {
        if (busy) return false;
        busy = true;
        // workspaceId is the one captured when this modal opened, NOT whatever
        // is selected now. wsMutate refuses to send it if they disagree.
        const promise = isEdit
          ? wsMutate(workspaceId, "/roles/" + encodeURIComponent(existing.name), {
              method: "PATCH",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ description: desc.value.trim() }),
            })
          : wsMutate(workspaceId, "/roles", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ name: name.value.trim(), description: desc.value.trim() }),
            });
        promise.then((res) => {
          if (res.stale) { if (close) close(); return; }
          if (res.ok) {
            const verb = isEdit ? "updated" : "created";
            toastOk(`Role "${res.data?.name || name.value}" ${verb}.`, "Role " + verb);
            if (close) close();
            refresh(container, workspaceId);
          } else {
            toastBad(describeFailure(res), (isEdit ? "Update" : "Create") + " failed");
            busy = false;
          }
        });
        return false;
      } },
    ],
  });
}

function confirmDelete(row, container, workspaceId, params) {
  let busy = false;
  let close;
  close = openModal({
    title: "Delete role?",
    body: h("div", null,
      h("p", null, "Delete realm role ", h("code", null, row.name), "?"),
      h("p", "muted text-xs", "Existing user → role assignments referencing this role will be removed by the provider."),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Delete", bad: true, onClick: () => {
        if (busy) return false;
        busy = true;
        wsMutate(workspaceId, "/roles/" + encodeURIComponent(row.name), { method: "DELETE" })
          .then((res) => {
            if (res.stale) { if (close) close(); return; }
            if (res.ok) {
              toastOk(`Role "${row.name}" deleted.`, "Role deleted");
              if (close) close();
              refresh(container, workspaceId);
            } else {
              toastBad(describeFailure(res), "Delete failed");
              busy = false;
            }
          });
        return false;
      } },
    ],
  });
}

function refresh(container, workspaceId) {
  rolesView({ container, params: { workspace_id: workspaceId } });
}
