// user-detail.js — workspace-scoped user detail and every user mutation.
//
// Slice 6: migrated from /admin/users/:id to
// /v1/workspaces/{workspace_id}/users/{user_id} plus that user's roles,
// sessions and password operations.
//
// This is the view where cross-workspace mutation is most dangerous, and it
// is why Phase 9 exists. Every action here is built from TWO captured values:
// the user id AND the workspace id it belongs to. Neither is read from global
// state at click time. If the operator switches workspace while a dialog is
// open, the switch listener closes it; if a click somehow still lands,
// wsMutate refuses to send it. Two independent locks, because the failure mode
// is deleting a real person in the wrong realm.

import { h, mount, relativeTime } from "../lib/dom.js";
import {
  apiTry, wsPath, wsMutate, enterWorkspace, captureWorkspaceToken, isWorkspaceStale,
} from "../lib/workspaces.js";
import { pageHeader, card, kvList, pill, spinner, emptyState, statusBadge } from "../components/common.js";
import { navigate, wsRoute } from "../lib/router.js";
import { openModal } from "../components/modal.js";
import { toastOk, toastBad } from "../components/toast.js";
import {
  renderGateState, renderAPIError, connectionBanner, writeBlockedReason, describeFailure,
} from "../components/ws-states.js";

export default async function userDetailView({ container, params }) {
  const workspaceId = params.workspace_id;
  const userId = params.user_id;

  mount(container,
    pageHeader("User detail", h("span", null, h("code", null, userId), " ", statusBadge("live")), [
      h("button", { class: "btn", onclick: () => navigate(wsRoute(workspaceId, "users")) }, "← back to users"),
    ]),
    h("div", { id: "ud-content" }, h("div", "row", spinner(), h("span", "muted", "loading…"))),
  );

  const gate = await enterWorkspace(workspaceId);
  let target = container.querySelector("#ud-content");
  if (!target) return;
  if (!gate.ok) {
    mount(target, renderGateState(gate, { onRetry: () => userDetailView({ container, params }) }));
    return;
  }
  const blocked = writeBlockedReason(gate.connectionState, gate.connection);

  const token = captureWorkspaceToken();
  const [userR, rolesR] = await Promise.all([
    apiTry(wsPath(workspaceId, "/users/" + encodeURIComponent(userId)), { signal: token.signal }),
    apiTry(wsPath(workspaceId, "/users/" + encodeURIComponent(userId) + "/roles"), { signal: token.signal }),
  ]);
  if (isWorkspaceStale(token)) return;

  target = container.querySelector("#ud-content");
  if (!target) return;

  if (!userR.ok) {
    mount(target, userR.error?.code === "user_not_found" || userR.status === 404
      ? emptyState({
          icon: "?",
          title: "User not found",
          body: `No user with id ${userId} exists in this workspace's realm.`,
          action: h("button", { class: "btn", onclick: () => navigate(wsRoute(workspaceId, "users")) }, "Back to users"),
        })
      : renderAPIError(userR, { onRetry: () => userDetailView({ container, params }) }));
    return;
  }
  const u = userR.data;
  const userRoles = (rolesR.ok ? (rolesR.data.roles || []) : []);

  // ctx bundles the two values every mutation on this page must be built
  // from. Passing it around, rather than reading state, is what makes the
  // cross-workspace mutation impossible to write by accident.
  const ctx = { workspaceId, params, container, blocked };

  const writeBtn = (props, label) => h("button", {
    ...props,
    disabled: !!blocked,
    title: blocked || props.title || "",
  }, label);

  mount(target,
    connectionBanner(gate.connectionState, gate.connection, { workspaceId }),

    card({
      title: u.username || "user",
      subtitle: u.email,
      actions: [
        writeBtn({ class: "btn", onclick: () => openEditModal(u, ctx) }, "Edit"),
        writeBtn({ class: "btn", onclick: () => toggleEnabled(u, ctx) }, u.enabled ? "Disable" : "Enable"),
        writeBtn({ class: "btn btn-warn", id: "ud-reset-btn", onclick: (e) => sendResetEmail(u, ctx, e.currentTarget) }, "Send reset email"),
        writeBtn({ class: "btn btn-warn", onclick: () => confirmLogoutAll(u, ctx) }, "Logout all sessions"),
        writeBtn({ class: "btn btn-bad", onclick: () => confirmDelete(u, ctx) }, "Delete"),
      ],
      body: h("div", null,
        h("div", "row",
          pill(u.enabled ? "enabled" : "disabled", u.enabled ? "ok" : "warn"),
          pill(u.email_verified ? "email verified" : "email unverified", u.email_verified ? "ok" : "warn"),
        ),
        kvList([
          ["id",       h("code", null, u.id)],
          ["username", u.username || "—"],
          ["email",    u.email || "—"],
          ["first",    u.first_name || "—"],
          ["last",     u.last_name || "—"],
          ["created",  u.created_at ? `${u.created_at} (${relativeTime(u.created_at)})` : "—"],
        ]),
      ),
    }),

    card({
      title: h("span", null, "Realm roles ", statusBadge("live")),
      subtitle: "Assign or remove realm roles for this user",
      actions: [
        writeBtn({ class: "btn btn-sm", onclick: () => openAssignRoleModal(u, userRoles, ctx) }, "+ Assign role"),
      ],
      body: renderRoles(u, userRoles, ctx),
    }),

    card({
      title: "Attributes",
      subtitle: "Provider-side custom user attributes",
      body: renderAttributes(u.attributes),
    }),

    card({
      title: "Raw representation",
      body: h("details", "disclosure",
        h("summary", null, "show JSON"),
        h("pre", "pre", JSON.stringify(u, null, 2)),
      ),
    }),
  );
}

function renderRoles(u, roles, ctx) {
  if (!roles.length) {
    return h("p", "muted", "This user has no realm roles assigned.");
  }
  return h("div", "row",
    ...roles.map(r => h("span", { style: { display: "inline-flex", alignItems: "center", gap: "4px", marginRight: "8px" } },
      pill(r.name, r.name === "admin" ? "accent" : (r.builtin ? "neutral" : "ok")),
      // Built-ins cannot be unmapped cleanly through this surface; the service
      // guards would refuse anyway.
      r.builtin ? null : h("button", {
        class: "btn btn-xs btn-bad",
        style: { padding: "2px 6px", lineHeight: "1" },
        disabled: !!ctx.blocked,
        title: ctx.blocked || "Remove this role",
        onclick: () => confirmUnassign(u, r, ctx),
      }, "×"),
    )),
  );
}

function renderAttributes(attrs) {
  if (!attrs || Object.keys(attrs).length === 0) {
    return h("p", "muted", "No custom attributes set on this user.");
  }
  return kvList(Object.entries(attrs).map(([k, v]) => [k, Array.isArray(v) ? v.join(", ") : String(v)]));
}

// ─── Mutations ───────────────────────────────────────────────────────────────
//
// Every one takes ctx and uses ctx.workspaceId. None reads the current
// workspace.

function reload(ctx) {
  userDetailView({ container: ctx.container, params: ctx.params });
}

function openEditModal(u, ctx) {
  const firstName = h("input", { type: "text", value: u.first_name || "", autocomplete: "off" });
  const lastName  = h("input", { type: "text", value: u.last_name  || "", autocomplete: "off" });
  const email     = h("input", { type: "email", value: u.email     || "", autocomplete: "off" });

  let busy = false;
  let close;
  close = openModal({
    title: "Edit user",
    body: h("div", "col",
      h("label", null, h("div", "muted", "first name"), firstName),
      h("label", null, h("div", "muted", "last name"),  lastName),
      h("label", null, h("div", "muted", "email"),      email),
      h("p", "muted text-xs", "Username is not editable. Enable/disable lives on the user card."),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Save changes", primary: true, onClick: () => {
        if (busy) return false;
        const newEmail = email.value.trim();
        const emailChanged = newEmail !== (u.email || "");
        if (emailChanged && newEmail !== "" && !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(newEmail)) {
          toastBad("Enter a valid email address (e.g. person@example.com).", "Invalid email");
          return false;
        }
        busy = true;
        const body = {};
        if (firstName.value.trim() !== (u.first_name || "")) body.first_name = firstName.value.trim();
        if (lastName.value.trim()  !== (u.last_name  || "")) body.last_name  = lastName.value.trim();
        if (emailChanged) body.email = newEmail;
        if (Object.keys(body).length === 0) {
          toastOk("No changes to save.", "Up to date");
          if (close) close();
          return false;
        }
        patchUser(ctx, u.id, body).then((res) => {
          if (res.stale) { if (close) close(); return; }
          if (res.ok) {
            toastOk("User updated.", "Saved");
            if (close) close();
            reload(ctx);
          } else {
            toastBad(describeFailure(res), "Update failed");
            busy = false;
          }
        });
        return false;
      } },
    ],
  });
}

function toggleEnabled(u, ctx) {
  const turningOff = u.enabled;
  if (!turningOff) {
    patchUser(ctx, u.id, { enabled: true }).then((res) => {
      if (res.stale) return;
      if (res.ok) {
        toastOk("User enabled.", "Saved");
        reload(ctx);
      } else {
        toastBad(describeFailure(res), "Enable failed");
      }
    });
    return;
  }
  let busy = false;
  let close;
  close = openModal({
    title: "Disable user?",
    body: h("p", null,
      "Disabling ", h("strong", null, u.username || u.email),
      " prevents them from signing in until you re-enable. Sessions stay alive until they expire — use ",
      h("em", null, "Logout all sessions"), " for an immediate cutoff.",
    ),
    actions: [
      { label: "Cancel" },
      { label: "Disable", bad: true, onClick: () => {
        if (busy) return false;
        busy = true;
        patchUser(ctx, u.id, { enabled: false }).then((res) => {
          if (res.stale) { if (close) close(); return; }
          if (res.ok) {
            toastOk("User disabled.", "Saved");
            if (close) close();
            reload(ctx);
          } else {
            toastBad(describeFailure(res), "Disable failed");
            busy = false;
          }
        });
        return false;
      } },
    ],
  });
}

function sendResetEmail(u, ctx, btn) {
  // UI-003: double-clicks used to send N action emails.
  if (!btn || btn.disabled) return;
  btn.disabled = true;
  const originalLabel = btn.textContent;
  btn.textContent = "Sending…";
  wsMutate(ctx.workspaceId, "/users/" + encodeURIComponent(u.id) + "/reset-password", { method: "POST" })
    .then((res) => {
      if (res.stale) return;
      if (res.ok) {
        toastOk("Password-reset email sent to " + (u.email || u.username) + ".", "Email queued");
      } else {
        toastBad(describeFailure(res), "Reset failed");
      }
    })
    .finally(() => {
      if (document.body.contains(btn)) {
        btn.disabled = false;
        btn.textContent = originalLabel;
      }
    });
}

function confirmLogoutAll(u, ctx) {
  let busy = false;
  let close;
  close = openModal({
    title: "Logout every session?",
    body: h("p", null,
      "This invalidates every active session for ", h("strong", null, u.username || u.email),
      " across every client in this workspace's realm.",
    ),
    actions: [
      { label: "Cancel" },
      { label: "Logout all", bad: true, onClick: () => {
        if (busy) return false;
        busy = true;
        wsMutate(ctx.workspaceId, "/users/" + encodeURIComponent(u.id) + "/sessions", { method: "DELETE" })
          .then((res) => {
            if (res.stale) { if (close) close(); return; }
            if (res.ok) {
              toastOk("All sessions terminated.", "Logged out everywhere");
              if (close) close();
            } else {
              toastBad(describeFailure(res), "Logout failed");
              busy = false;
            }
          });
        return false;
      } },
    ],
  });
}

function confirmDelete(u, ctx) {
  let busy = false;
  let close;
  close = openModal({
    title: "Delete user?",
    body: h("div", null,
      h("p", null, "Permanently delete ", h("strong", null, u.username || u.email), "?"),
      h("p", "muted text-xs",
        "Self-delete and last-admin removal are refused by the API — you'll see a caller_forbidden if either applies.",
      ),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Delete", bad: true, onClick: () => {
        if (busy) return false;
        busy = true;
        wsMutate(ctx.workspaceId, "/users/" + encodeURIComponent(u.id), { method: "DELETE" })
          .then((res) => {
            if (res.stale) { if (close) close(); return; }
            if (res.ok) {
              toastOk("User deleted.", "Deleted");
              if (close) close();
              navigate(wsRoute(ctx.workspaceId, "users"));
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

function openAssignRoleModal(u, currentRoles, ctx) {
  const assigned = new Set(currentRoles.map(r => r.name));
  const select = h("select", null, h("option", { value: "" }, "Loading roles…"));

  // The role list comes from the same workspace the assignment will target.
  const token = captureWorkspaceToken();
  apiTry(wsPath(ctx.workspaceId, "/roles"), { signal: token.signal }).then(({ ok, data }) => {
    if (isWorkspaceStale(token)) return;
    if (!ok || !Array.isArray(data?.roles)) return;
    select.innerHTML = "";
    const available = data.roles.filter(r => !r.builtin && !assigned.has(r.name));
    if (!available.length) {
      select.appendChild(h("option", { value: "" }, "(no assignable roles)"));
      return;
    }
    for (const r of available) {
      select.appendChild(h("option", { value: r.name }, r.name + (r.description ? " — " + r.description : "")));
    }
  });

  let busy = false;
  let close;
  close = openModal({
    title: "Assign role",
    body: h("div", "col",
      h("label", null, h("div", "muted", "role"), select),
      h("p", "muted text-xs", "Granting `admin` is unrestricted — but removing your own admin later is refused."),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Assign", primary: true, onClick: () => {
        if (busy) return false;
        if (!select.value) {
          toastBad("Pick a role to assign.", "No role selected");
          return false;
        }
        busy = true;
        wsMutate(ctx.workspaceId, "/users/" + encodeURIComponent(u.id) + "/roles", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ roles: [select.value] }),
        }).then((res) => {
          if (res.stale) { if (close) close(); return; }
          if (res.ok) {
            toastOk(`Role "${select.value}" assigned.`, "Assigned");
            if (close) close();
            reload(ctx);
          } else {
            toastBad(describeFailure(res), "Assign failed");
            busy = false;
          }
        });
        return false;
      } },
    ],
  });
}

function confirmUnassign(u, role, ctx) {
  let busy = false;
  let close;
  close = openModal({
    title: "Remove role?",
    body: h("p", null, "Remove ", h("code", null, role.name), " from this user?"),
    actions: [
      { label: "Cancel" },
      { label: "Remove", bad: true, onClick: () => {
        if (busy) return false;
        busy = true;
        wsMutate(ctx.workspaceId,
          "/users/" + encodeURIComponent(u.id) + "/roles/" + encodeURIComponent(role.name),
          { method: "DELETE" },
        ).then((res) => {
          if (res.stale) { if (close) close(); return; }
          if (res.ok) {
            toastOk(`Role "${role.name}" removed.`, "Removed");
            if (close) close();
            reload(ctx);
          } else {
            toastBad(describeFailure(res), "Remove failed");
            busy = false;
          }
        });
        return false;
      } },
    ],
  });
}

function patchUser(ctx, id, body) {
  return wsMutate(ctx.workspaceId, "/users/" + encodeURIComponent(id), {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}
