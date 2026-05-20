// roles.js — REAL GET / POST / PATCH / DELETE /admin/roles (Stages A, B, C, D).

import { h, mount } from "../lib/dom.js";
import { apiTry } from "../lib/api.js";
import { pageHeader, pill, emptyState, spinner, statusBadge } from "../components/common.js";
import { renderTable } from "../components/table.js";
import { openModal } from "../components/modal.js";
import { toastOk, toastBad } from "../components/toast.js";
import { navigate } from "../lib/router.js";

export default async function rolesView({ container }) {
  mount(container,
    pageHeader("Roles", h("span", null,
      "Realm roles from Keycloak. Create, edit and delete are wired to the Admin API. ",
      statusBadge("live"),
    ), [
      h("button", { class: "btn btn-primary", onclick: () => openRoleModal(null, container) }, "+ New role"),
    ]),
    h("div", { id: "roles-content" }, h("div", "row", spinner(), h("span", "muted", "loading…"))),
  );

  const r = await apiTry("/admin/roles");
  const target = container.querySelector("#roles-content");
  if (!target) return;

  if (!r.ok) {
    mount(target, renderError(r));
    return;
  }

  const rows = r.data.roles || [];

  renderTable(target, {
    toolbar: {
      actions: [h("button", { class: "btn btn-sm", onclick: () => rolesView({ container }) }, "↻ refresh")],
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
            onclick: (e) => { e.stopPropagation(); openRoleModal(row, container); },
            title: row.builtin ? "Built-in roles: description-only edit" : "Edit",
          }, "edit"),
          h("button", {
            class: "btn btn-xs btn-bad",
            disabled: row.builtin,
            title: row.builtin ? "Built-in roles cannot be deleted" : "Delete",
            onclick: (e) => { e.stopPropagation(); confirmDelete(row, container); },
          }, "del"),
        ),
      },
    ],
    rows,
    onRowClick: (row) => openRoleModal(row, container),
    empty: { title: "No roles in this realm", body: "Click + New role to create one." },
  });
}

function renderError(r) {
  if (r.status === 401) {
    return emptyState({
      icon: "🔒",
      title: "Sign in required",
      body: "GET /admin/roles requires a valid bearer token.",
      action: h("button", { class: "btn btn-primary", onclick: () => navigate("/playground") }, "Go to Playground"),
    });
  }
  if (r.status === 403) {
    return emptyState({ icon: "⛔", title: "Admin role required", body: "Your token validated but lacks the `admin` realm role." });
  }
  return emptyState({
    icon: "✗",
    title: "Request failed",
    body: `HTTP ${r.status}: ${r.error?.message || "unknown"}`,
  });
}

function openRoleModal(existing, container) {
  const isEdit = !!existing;
  // For edit: name is read-only (rename is intentionally out of scope; see
  // service.UpdateRole). For create: caller picks the name.
  const name = h("input", {
    type: "text",
    value: existing?.name || "",
    placeholder: "e.g. editor",
    disabled: isEdit,
    title: isEdit ? "Role rename is not supported" : "",
  });
  const desc = h("input", { type: "text", value: existing?.description || "", placeholder: "Short description" });

  let busy = false;
  let close;
  close = openModal({
    title: isEdit ? "Edit role: " + existing.name : "New role",
    body: h("div", "col",
      h("label", null, h("div", "muted", "name"), name),
      h("label", null, h("div", "muted", "description"), desc),
      existing?.composite ? h("p", "muted text-xs", "This role is a composite (grants other roles transitively). Composition editing lands in v0.3.") : null,
      existing?.builtin ? h("p", "muted text-xs", "Built-in roles can have their description edited but not their name.") : null,
    ),
    actions: [
      { label: "Cancel" },
      { label: isEdit ? "Save changes" : "Create role", primary: true, onClick: () => {
        if (busy) return false;
        busy = true;
        const promise = isEdit
          ? updateRole(existing.name, { description: desc.value.trim() })
          : createRole({ name: name.value.trim(), description: desc.value.trim() });
        promise.then(({ ok, status, data, error }) => {
          if (ok) {
            const verb = isEdit ? "updated" : "created";
            toastOk(`Role "${data?.name || name.value}" ${verb}.`, "Role " + verb);
            if (close) close();
            if (container) rolesView({ container });
          } else {
            toastBad(formatError(status, error), (isEdit ? "Update" : "Create") + " failed");
            busy = false;
          }
        });
        return false;
      } },
    ],
  });
}

async function createRole(body) {
  return apiTry("/admin/roles", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

async function updateRole(name, body) {
  return apiTry("/admin/roles/" + encodeURIComponent(name), {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

async function deleteRole(name) {
  return apiTry("/admin/roles/" + encodeURIComponent(name), { method: "DELETE" });
}

function formatError(status, error) {
  if (status === 400) return "Invalid name. Use lowercase letters, digits, _ - . : (max 255).";
  if (status === 401) return "Sign in required.";
  if (status === 403) return "This role is protected — admin/user/built-ins cannot be modified or deleted.";
  if (status === 404) return "Role not found (already deleted?).";
  if (status === 409) return "A role with that name already exists.";
  if (status === 502) return "Keycloak is unreachable.";
  if (status === 503) return "Identity management not configured.";
  return "HTTP " + status + ": " + (error?.message || "unknown");
}

function confirmDelete(row, container) {
  let busy = false;
  let close;
  close = openModal({
    title: "Delete role?",
    body: h("div", null,
      h("p", null, "Delete realm role ", h("code", null, row.name), "?"),
      h("p", "muted text-xs", "Existing user → role assignments referencing this role will be removed by Keycloak."),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Delete", bad: true, onClick: () => {
        if (busy) return false;
        busy = true;
        deleteRole(row.name).then(({ ok, status, error }) => {
          if (ok) {
            toastOk(`Role "${row.name}" deleted.`, "Role deleted");
            if (close) close();
            if (container) rolesView({ container });
          } else {
            toastBad(formatError(status, error), "Delete failed");
            busy = false;
          }
        });
        return false;
      } },
    ],
  });
}
