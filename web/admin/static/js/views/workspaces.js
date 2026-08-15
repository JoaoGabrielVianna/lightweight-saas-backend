// workspaces.js (view) — the minimal workspace management surface.
//
// Scope decision (Phase 12): the console had NO UI for workspaces or
// connections, and without one this prototype cannot be operated without curl
// — an operator cannot even reach the identity views, because those need a
// workspace with an active connection. So this ships the smallest surface that
// makes the end-to-end journey possible, and deliberately stops there:
//
//   list · create · rename · archive
//
// No pagination, no search, no bulk actions, no per-workspace settings tabs.
// Those are a product, and this is the door to one.

import { h, mount, relativeTime } from "../lib/dom.js";
import { apiTry } from "../lib/api.js";
import { pageHeader, pill, spinner, emptyState, statusBadge } from "../components/common.js";
import { renderTable } from "../components/table.js";
import { openModal } from "../components/modal.js";
import { toastOk, toastBad } from "../components/toast.js";
import { navigate, wsRoute } from "../lib/router.js";
import { loadWorkspaces, getCurrentWorkspaceId, selectWorkspace } from "../lib/workspaces.js";
import { renderAPIError, describeFailure } from "../components/ws-states.js";

export default async function workspacesView({ container }) {
  mount(container,
    pageHeader("Workspaces", h("span", null,
      "A workspace connects this installation to one identity realm. Every identity view is scoped to the workspace you select. ",
      statusBadge("live"),
    ), [
      h("button", { class: "btn btn-primary", onclick: () => openCreateModal(container) }, "+ New workspace"),
    ]),
    h("div", { id: "ws-content" }, h("div", "row", spinner(), h("span", "muted", "loading…"))),
  );

  // Two lists, deliberately.
  //
  // `?status=all` for THIS table, because a management screen that hides
  // archived workspaces gives an operator no way to see that archiving worked,
  // and no way to find one again. `GET /v1/workspaces` defaults to active
  // only, which is right for the selector and wrong here.
  //
  // loadWorkspaces() in parallel keeps the shell — selector, sidebar, gates —
  // agreeing with the server about what is selectable.
  const [all] = await Promise.all([
    apiTry("/v1/workspaces?status=all"),
    loadWorkspaces(), // result intentionally unused; it refreshes shared state
  ]);

  const target = container.querySelector("#ws-content");
  if (!target) return;

  if (!all.ok) {
    mount(target, renderAPIError(all, {
      title: "Could not load workspaces",
      onRetry: () => workspacesView({ container }),
    }));
    return;
  }

  const workspaces = Array.isArray(all.data?.workspaces) ? all.data.workspaces : [];
  if (!workspaces.length) {
    mount(target, emptyState({
      icon: "◫",
      title: "No workspaces yet",
      body: "Create one, then give it a connection to a Keycloak realm. Until then there is no realm for the identity views to show.",
      action: h("button", { class: "btn btn-primary", onclick: () => openCreateModal(container) }, "Create the first workspace"),
    }));
    return;
  }

  const currentId = getCurrentWorkspaceId();

  renderTable(target, {
    toolbar: {
      actions: [h("button", { class: "btn btn-sm", onclick: () => workspacesView({ container }) }, "↻ refresh")],
    },
    columns: [
      { key: "name", title: "Name", render: (v, row) => h("span", null,
          h("strong", null, v),
          row.id === currentId ? h("span", { style: { marginLeft: "8px" } }, pill("selected", "accent")) : null,
        ),
      },
      { key: "slug",   title: "Slug",   render: (v) => h("code", null, v) },
      { key: "id",     title: "ID",     render: (v) => h("code", "text-xs", v) },
      { key: "status", title: "Status", render: (v) => pill(v, v === "active" ? "ok" : "neutral") },
      { key: "created_at", title: "Created", render: (v) => v ? relativeTime(v) : "—" },
      { key: "_actions", title: "", width: "300px", render: (_, row) => h("div", "row",
          h("button", {
            class: "btn btn-xs",
            disabled: row.status !== "active",
            title: row.status === "active" ? "Open this workspace's connections" : "Archived workspaces cannot be configured",
            onclick: (e) => { e.stopPropagation(); openWorkspace(row, "connections"); },
          }, "connections"),
          h("button", {
            class: "btn btn-xs",
            disabled: row.status !== "active",
            title: row.status === "active" ? "Switch to this workspace" : "Archived workspaces cannot be selected",
            onclick: (e) => { e.stopPropagation(); openWorkspace(row, "users"); },
          }, "open"),
          h("button", {
            class: "btn btn-xs",
            onclick: (e) => { e.stopPropagation(); openRenameModal(row, container); },
          }, "rename"),
          h("button", {
            class: "btn btn-xs btn-bad",
            disabled: row.status !== "active",
            title: row.status === "active" ? "Archive this workspace" : "Already archived",
            onclick: (e) => { e.stopPropagation(); confirmArchive(row, container); },
          }, "archive"),
        ),
      },
    ],
    rows: workspaces,
    empty: { title: "No workspaces", body: "" },
  });
}

// openWorkspace navigates INTO a workspace. Navigation is what changes the
// selected workspace (the route is the source of truth), so this does not call
// selectWorkspace itself.
function openWorkspace(row, section) {
  navigate(wsRoute(row.id, section));
}

function openCreateModal(container) {
  const name = h("input", { type: "text", placeholder: "Production", autocomplete: "off" });
  const slug = h("input", { type: "text", placeholder: "(derived from the name)", autocomplete: "off" });

  let busy = false;
  let close;
  close = openModal({
    title: "New workspace",
    body: h("div", "col",
      h("label", null, h("div", "muted", "name *"), name),
      h("label", null, h("div", "muted", "slug"), slug),
      h("p", "muted text-xs", "The slug is derived from the name when left blank. It is permanent: a slug cannot be changed after creation."),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Create", primary: true, onClick: () => {
        if (busy) return false;
        if (!name.value.trim()) { toastBad("Name is required.", "Missing name"); return false; }
        busy = true;
        const body = { name: name.value.trim() };
        if (slug.value.trim()) body.slug = slug.value.trim();
        apiTry("/v1/workspaces", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        }).then((res) => {
          if (res.ok) {
            toastOk(`Workspace "${res.data?.name || body.name}" created.`, "Workspace created");
            if (close) close();
            // A brand-new workspace has no connection; sending the operator
            // straight to its connections page is the only useful next step.
            if (res.data?.id) navigate(wsRoute(res.data.id, "connections"));
            else workspacesView({ container });
          } else {
            toastBad(describeFailure(res), "Create failed");
            busy = false;
          }
        });
        return false;
      } },
    ],
  });
}

function openRenameModal(row, container) {
  const name = h("input", { type: "text", value: row.name || "", autocomplete: "off" });

  let busy = false;
  let close;
  close = openModal({
    title: "Rename workspace",
    body: h("div", "col",
      h("label", null, h("div", "muted", "name"), name),
      h("p", "muted text-xs", "The slug (", h("code", null, row.slug), ") is immutable and does not change with the name."),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Save", primary: true, onClick: () => {
        if (busy) return false;
        const next = name.value.trim();
        if (!next) { toastBad("Name is required.", "Missing name"); return false; }
        if (next === row.name) { if (close) close(); return false; }
        busy = true;
        apiTry("/v1/workspaces/" + encodeURIComponent(row.id), {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name: next }),
        }).then((res) => {
          if (res.ok) {
            toastOk("Workspace renamed.", "Saved");
            if (close) close();
            workspacesView({ container });
          } else {
            toastBad(describeFailure(res), "Rename failed");
            busy = false;
          }
        });
        return false;
      } },
    ],
  });
}

function confirmArchive(row, container) {
  let busy = false;
  let close;
  close = openModal({
    title: "Archive workspace?",
    body: h("div", null,
      h("p", null, "Archive ", h("strong", null, row.name), "?"),
      h("p", "muted text-xs",
        "Archiving freezes the workspace: every identity operation through it is refused before the provider is contacted. ",
        "Nothing is deleted in the provider's realm, and the workspace's connections stay on record.",
      ),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Archive", bad: true, onClick: () => {
        if (busy) return false;
        busy = true;
        apiTry("/v1/workspaces/" + encodeURIComponent(row.id) + "/archive", { method: "POST" })
          .then((res) => {
            if (res.ok) {
              toastOk(`Workspace "${row.name}" archived.`, "Archived");
              if (close) close();
              // If the operator archived the workspace they were in, the
              // console must not stay pointed at it — every view there can
              // now only say "archived".
              if (getCurrentWorkspaceId() === row.id) selectWorkspace(null);
              workspacesView({ container });
            } else {
              toastBad(describeFailure(res), "Archive failed");
              busy = false;
            }
          });
        return false;
      } },
    ],
  });
}
