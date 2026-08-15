// invitations.js — workspace-scoped invitations.
//
// Slice 6: migrated from /admin/invitations to
// /v1/workspaces/{workspace_id}/invitations (list, create, revoke, resend).
//
// One shape change beyond the path, flagged in FRONTEND_READINESS §4.3: the
// "set a temporary password" mode used to POST /admin/users/password. Its /v1
// equivalent is POST /v1/workspaces/{id}/users, same field names, but its
// validation now happens in the shared service and returns the structured
// envelope — so its inline errors go through describeFailure like everything
// else rather than reading `error.message` directly.
//
// An invitation id IS a user id (see WORKSPACE_IDENTITY_API.md §1): revoking
// deletes the underlying user. That model is preserved verbatim from /admin.

import { h, mount, relativeTime } from "../lib/dom.js";
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

const STATUS_VARIANT = {
  pending:  "warn",
  accepted: "ok",
  expired:  "bad",
  revoked:  "neutral",
};

export default async function invitationsView({ container, params }) {
  const workspaceId = params.workspace_id;

  mount(container,
    pageHeader("Invitations", h("span", null,
      "Pending invitations derived from users with required actions in this workspace's realm. ",
      statusBadge("live"),
    ), [
      h("button", {
        class: "btn btn-primary",
        id: "inv-new-btn",
        onclick: () => openInviteModal(container, workspaceId, params),
      }, "+ Invite user"),
    ]),
    h("div", { id: "inv-content" }, h("div", "row", spinner(), h("span", "muted", "loading…"))),
  );

  const gate = await enterWorkspace(workspaceId);
  let target = container.querySelector("#inv-content");
  if (!target) return;
  if (!gate.ok) {
    mount(target, renderGateState(gate, { onRetry: () => invitationsView({ container, params }) }));
    return;
  }

  const blocked = writeBlockedReason(gate.connectionState, gate.connection);
  const newBtn = container.querySelector("#inv-new-btn");
  if (newBtn && blocked) {
    newBtn.disabled = true;
    newBtn.title = blocked;
  }

  const token = captureWorkspaceToken();
  const r = await apiTry(wsPath(workspaceId, "/invitations"), { signal: token.signal });
  if (isWorkspaceStale(token)) return;

  target = container.querySelector("#inv-content");
  if (!target) return;
  if (!r.ok) {
    mount(target, renderAPIError(r, { onRetry: () => invitationsView({ container, params }) }));
    return;
  }

  const rows = r.data.invitations || [];

  mount(target,
    connectionBanner(gate.connectionState, gate.connection, { workspaceId }),
    h("div", { id: "inv-table" }),
  );

  renderTable(container.querySelector("#inv-table"), {
    toolbar: {
      actions: [h("button", { class: "btn btn-sm", onclick: () => invitationsView({ container, params }) }, "↻ refresh")],
    },
    columns: [
      { key: "email",            title: "Email",  render: (v, row) => h("strong", null, v || row.username || "—") },
      { key: "status",           title: "Status", render: (v) => pill(v, STATUS_VARIANT[v] || "neutral") },
      { key: "required_actions", title: "Required actions", render: (v) => (v && v.length) ? h("span", "muted text-xs", v.join(", ")) : h("span", "dim", "—") },
      { key: "invited_by",       title: "Invited by", render: (v) => v ? h("code", null, v) : h("span", "dim", "—") },
      { key: "created_at",       title: "Created", render: (v) => v ? relativeTime(v) : "—" },
      { key: "expires_at",       title: "Expires", render: (v) => v ? v : h("span", "dim", "—") },
      { key: "_actions", title: "", width: "180px", render: (_, row) => h("div", "row",
          h("button", {
            class: "btn btn-xs",
            disabled: row.status !== "pending" || !!blocked,
            title: blocked || (row.status === "pending" ? "Re-send the invitation email" : "Only pending invitations can be resent"),
            onclick: (e) => { e.stopPropagation(); resendInvitation(row, container, workspaceId, params, e.currentTarget); },
          }, "resend"),
          h("button", {
            class: "btn btn-xs btn-bad",
            disabled: row.status === "accepted" || !!blocked,
            title: blocked || (row.status === "accepted" ? "Accepted invitations cannot be revoked (use Users → Delete)" : "Revoke the invitation"),
            onclick: (e) => { e.stopPropagation(); confirmRevoke(row, container, workspaceId, params); },
          }, "revoke"),
        ),
      },
    ],
    rows,
    empty: {
      title: "No pending invitations",
      body: "Users with no required actions and no invited_by attribute are excluded from this list.",
    },
  });
}

function openInviteModal(container, workspaceId, routeParams) {
  const email     = h("input", { type: "email",    placeholder: "person@example.com", autocomplete: "off" });
  const firstName = h("input", { type: "text",     placeholder: "Jane",              autocomplete: "off" });
  const lastName  = h("input", { type: "text",     placeholder: "Doe",               autocomplete: "off" });
  const role      = h("select", null, h("option", { value: "user" }, "user"));
  const expires   = h("input", { type: "datetime-local", placeholder: "(optional)" });
  const tempPass  = h("input", { type: "password", placeholder: "min. 8 characters", autocomplete: "new-password" });

  // "email" = invite-by-email (a provider action email).
  // "password" = provision directly with a temporary password.
  const modeEmail    = h("input", { type: "radio", name: "invite-mode", value: "email",    checked: true });
  const modePassword = h("input", { type: "radio", name: "invite-mode", value: "password" });

  const emailSection    = h("div", "col", h("label", null, h("div", "muted", "expires at (optional)"), expires), h("p", "muted text-xs", "The provider sends an email with UPDATE_PASSWORD + VERIFY_EMAIL links. Requires SMTP on that realm."));
  const passwordSection = h("div", "col", { style: { display: "none" } }, h("label", null, h("div", "muted", "temporary password *"), tempPass), h("p", "muted text-xs", "User must change this password on first login. No email is sent — share the password out-of-band."));

  function refreshMode() {
    const isPass = modePassword.checked;
    emailSection.style.display    = isPass ? "none" : "";
    passwordSection.style.display = isPass ? ""     : "none";
  }
  modeEmail.addEventListener("change",    refreshMode);
  modePassword.addEventListener("change", refreshMode);

  // Role list comes from the SAME workspace this invitation will be created
  // in. Reading it from anywhere else would offer role names that do not exist
  // in the target realm.
  const rolesToken = captureWorkspaceToken();
  apiTry(wsPath(workspaceId, "/roles"), { signal: rolesToken.signal }).then(({ ok, data }) => {
    if (isWorkspaceStale(rolesToken)) return;
    if (!ok || !Array.isArray(data?.roles)) return;
    role.innerHTML = "";
    for (const r of data.roles) {
      if (r.name === "offline_access" || r.name === "uma_authorization") continue;
      if (r.name.startsWith("default-roles-")) continue;
      role.appendChild(h("option", { value: r.name }, r.name + (r.description ? " — " + r.description : "")));
    }
    if ([...role.options].some(o => o.value === "user")) role.value = "user";
  });

  let busy = false;
  let close;

  function submit() {
    if (busy) return false;
    const rawEmail = email.value.trim();
    if (!rawEmail) { toastBad("Email is required.", "Missing email"); return false; }
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(rawEmail)) {
      toastBad("Enter a valid email address (e.g. person@example.com).", "Invalid email");
      return false;
    }

    const isPass = modePassword.checked;

    if (isPass) {
      const pw = tempPass.value;
      if (!pw) { toastBad("Temporary password is required.", "Missing field"); return false; }
      if (pw.length < 8) { toastBad("Password must be at least 8 characters.", "Too short"); return false; }
      busy = true;
      // FRONTEND_READINESS §4.3: POST /admin/users/password → POST .../users.
      // Same field names; the validation now lives in the shared service.
      wsMutate(workspaceId, "/users", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          email:              rawEmail,
          first_name:         firstName.value.trim(),
          last_name:          lastName.value.trim(),
          temporary_password: pw,
          roles:              [role.value],
        }),
      }).then((res) => {
        if (res.stale) { if (close) close(); return; }
        if (res.ok) {
          toastOk("User " + (res.data?.user?.email || res.data?.email || rawEmail) + " created with temporary password.", "User created");
          if (close) close();
          invitationsView({ container, params: routeParams });
        } else {
          toastBad(describeFailure(res), "Create failed");
          busy = false;
        }
      });
      return false;
    }

    busy = true;
    const body = {
      email:      rawEmail,
      first_name: firstName.value.trim(),
      last_name:  lastName.value.trim(),
      roles:      [role.value],
    };
    if (expires.value) {
      const d = new Date(expires.value);
      if (!isNaN(d.getTime())) body.expires_at = d.toISOString();
    }
    wsMutate(workspaceId, "/invitations", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }).then((res) => {
      if (res.stale) { if (close) close(); return; }
      if (res.ok) {
        toastOk("Invitation sent to " + (res.data?.email || body.email) + ".", "Invitation created");
        if (close) close();
        invitationsView({ container, params: routeParams });
      } else {
        toastBad(describeFailure(res), "Invite failed");
        busy = false;
      }
    });
    return false;
  }

  close = openModal({
    title: "Add user",
    body: h("div", "col",
      h("div", "col",
        h("div", "muted", "method"),
        h("div", "row",
          h("label", null, modeEmail,    h("span", null, " Send email invite")),
          h("label", null, modePassword, h("span", null, " Set temporary password")),
        ),
      ),
      h("hr", { style: { margin: "0.5rem 0", border: "none", borderTop: "1px solid var(--border, #333)" } }),
      h("label", null, h("div", "muted", "email *"), email),
      h("div", "row",
        h("label", { style: { flex: "1" } }, h("div", "muted", "first name"), firstName),
        h("label", { style: { flex: "1" } }, h("div", "muted", "last name"),  lastName),
      ),
      h("label", null, h("div", "muted", "initial role *"), role),
      emailSection,
      passwordSection,
    ),
    actions: [
      { label: "Cancel" },
      { label: "Confirm", primary: true, onClick: submit },
    ],
  });
}

function resendInvitation(row, container, workspaceId, params, btn) {
  // UI-004: double-clicking used to dispatch N duplicate emails (the provider
  // does not dedupe action emails). Guarded on the DOM node so a stale closure
  // from a re-render cannot bypass the flag.
  if (!btn || btn.disabled) return;
  btn.disabled = true;
  const originalLabel = btn.textContent;
  btn.textContent = "sending…";
  wsMutate(workspaceId, "/invitations/" + encodeURIComponent(row.id) + "/resend", { method: "POST" })
    .then((res) => {
      if (res.stale) return;
      if (res.ok) {
        toastOk("Invitation email re-sent to " + (row.email || row.username || row.id) + ".", "Invitation resent");
        invitationsView({ container, params });
      } else {
        toastBad(describeFailure(res), "Resend failed");
        if (document.body.contains(btn)) {
          btn.disabled = false;
          btn.textContent = originalLabel;
        }
      }
    });
}

function confirmRevoke(row, container, workspaceId, params) {
  let busy = false;
  let close;
  close = openModal({
    title: "Revoke invitation?",
    body: h("div", null,
      h("p", null, "Revoke invitation for ", h("strong", null, row.email || row.username || row.id), "?"),
      h("p", "muted text-xs", "This deletes the underlying user in this workspace's realm. If they had already accepted, use Users → Delete instead."),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Revoke", bad: true, onClick: () => {
        if (busy) return false;
        busy = true;
        wsMutate(workspaceId, "/invitations/" + encodeURIComponent(row.id), { method: "DELETE" })
          .then((res) => {
            if (res.stale) { if (close) close(); return; }
            if (res.ok) {
              toastOk("Invitation revoked.", "Revoked");
              if (close) close();
              invitationsView({ container, params });
            } else {
              toastBad(describeFailure(res), "Revoke failed");
              busy = false;
            }
          });
        return false;
      } },
    ],
  });
}
