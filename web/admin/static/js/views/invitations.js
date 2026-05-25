// invitations.js — REAL GET / POST / DELETE /admin/invitations and
// POST /admin/invitations/:id/resend. Full Stage B/C/D wiring.

import { h, mount, relativeTime } from "../lib/dom.js";
import { apiTry } from "../lib/api.js";
import { pageHeader, pill, emptyState, spinner, statusBadge } from "../components/common.js";
import { renderTable } from "../components/table.js";
import { openModal } from "../components/modal.js";
import { toastOk, toastBad } from "../components/toast.js";
import { navigate } from "../lib/router.js";

const STATUS_VARIANT = {
  pending:  "warn",
  accepted: "ok",
  expired:  "bad",
  revoked:  "neutral",
};

export default async function invitationsView({ container }) {
  mount(container,
    pageHeader("Invitations", h("span", null,
      "Pending invitations derived from Keycloak users with required actions. Invite, resend and revoke are all live. ",
      statusBadge("live"),
    ), [
      h("button", { class: "btn btn-primary", onclick: () => openInviteModal(container) }, "+ Invite user"),
    ]),
    h("div", { id: "inv-content" }, h("div", "row", spinner(), h("span", "muted", "loading…"))),
  );

  const r = await apiTry("/admin/invitations");
  const target = container.querySelector("#inv-content");
  if (!target) return;

  if (!r.ok) {
    mount(target, renderError(r));
    return;
  }

  const rows = r.data.invitations || [];

  renderTable(target, {
    toolbar: {
      actions: [h("button", { class: "btn btn-sm", onclick: () => invitationsView({ container }) }, "↻ refresh")],
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
            disabled: row.status !== "pending",
            title: row.status === "pending" ? "Re-send the invitation email" : "Only pending invitations can be resent",
            onclick: (e) => { e.stopPropagation(); resendInvitation(row, container, e.currentTarget); },
          }, "resend"),
          h("button", {
            class: "btn btn-xs btn-bad",
            disabled: row.status === "accepted",
            title: row.status === "accepted" ? "Accepted invitations cannot be revoked (use Users → Delete)" : "Revoke the invitation",
            onclick: (e) => { e.stopPropagation(); confirmRevoke(row, container); },
          }, "revoke"),
        ),
      },
    ],
    rows,
    empty: {
      title: "No pending invitations",
      body: "Users with no required actions and no invited_by attribute are excluded from this list. Once Stage B ships POST /admin/users/invite, new invitations show up here.",
    },
  });
}

function renderError(r) {
  if (r.status === 401) {
    return emptyState({
      icon: "🔒",
      title: "Sign in required",
      action: h("button", { class: "btn btn-primary", onclick: () => navigate("/playground") }, "Go to Playground"),
    });
  }
  if (r.status === 403) {
    return emptyState({ icon: "⛔", title: "Admin role required" });
  }
  return emptyState({ icon: "✗", title: "Request failed", body: `HTTP ${r.status}: ${r.error?.message || "unknown"}` });
}

function openInviteModal(container) {
  const email     = h("input", { type: "email", placeholder: "person@example.com", autocomplete: "off" });
  const firstName = h("input", { type: "text",  placeholder: "Jane", autocomplete: "off" });
  const lastName  = h("input", { type: "text",  placeholder: "Doe",  autocomplete: "off" });
  // Roles are loaded live from GET /admin/roles. Fallback to the canonical
  // built-in `user` role if the lookup fails so the modal stays usable.
  const role  = h("select", null, h("option", { value: "user" }, "user"));
  const expires = h("input", { type: "datetime-local", placeholder: "(optional)" });

  // Fire role lookup once the modal is open. The select repopulates when
  // the response lands; until then the user can still pick `user`.
  apiTry("/admin/roles").then(({ ok, data }) => {
    if (!ok || !Array.isArray(data?.roles)) return;
    role.innerHTML = "";
    for (const r of data.roles) {
      // Skip built-ins that aren't meaningful as initial roles.
      if (r.name === "offline_access" || r.name === "uma_authorization") continue;
      if (r.name.startsWith("default-roles-")) continue;
      const opt = h("option", { value: r.name }, r.name + (r.description ? " — " + r.description : ""));
      role.appendChild(opt);
    }
    // Default selection to `user` if present.
    role.value = "user";
  });

  let busy = false;
  let close;
  close = openModal({
    title: "Invite user",
    body: h("div", "col",
      h("label", null, h("div", "muted", "email *"), email),
      h("div", "row",
        h("label", { style: { flex: "1" } }, h("div", "muted", "first name"), firstName),
        h("label", { style: { flex: "1" } }, h("div", "muted", "last name"),  lastName),
      ),
      h("label", null, h("div", "muted", "initial role *"), role),
      h("label", null, h("div", "muted", "expires at (optional)"), expires),
      h("p", "muted text-xs", "Keycloak creates the user with UPDATE_PASSWORD + VERIFY_EMAIL required actions and sends the action email."),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Send invitation", primary: true, onClick: () => {
        if (busy) return false;
        // SMTP validation: reject malformed emails client-side. Keycloak's
        // execute-actions-email endpoint hits the realm SMTP server, and a
        // malformed local-part or missing domain produces a generic 502 by
        // the time it reaches the operator — wasting an admin round-trip
        // AND tying up Keycloak's SMTP socket for the timeout window.
        // Pattern mirrors the server's identity.emailPattern verbatim so the
        // two layers agree on what "valid" means.
        const rawEmail = email.value.trim();
        if (!rawEmail) {
          toastBad("Email is required.", "Missing email");
          return false;
        }
        if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(rawEmail)) {
          toastBad("Enter a valid email address (e.g. person@example.com).", "Invalid email");
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
          // datetime-local is local-time without zone; turn it into RFC3339
          // by appending the local offset via the Date constructor.
          const d = new Date(expires.value);
          if (!isNaN(d.getTime())) body.expires_at = d.toISOString();
        }
        createInvitation(body).then(({ ok, status, data, error }) => {
          if (ok) {
            toastOk("Invitation sent to " + (data?.email || body.email) + ".", "Invitation created");
            if (close) close();
            if (container) invitationsView({ container });
          } else {
            toastBad(formatError(status, error), "Invite failed");
            busy = false;
          }
        });
        return false;
      } },
    ],
  });
}

async function createInvitation(body) {
  return apiTry("/admin/invitations", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

function formatError(status, error) {
  if (status === 400) return "Invalid input. Check email format and expires_at is in the future.";
  if (status === 401) return "Sign in required.";
  if (status === 403) return "Action blocked by a service-tier guard (self-protection or admin role required).";
  if (status === 404) return "Invitation or referenced role not found.";
  if (status === 409) return "A user with that email already exists.";
  if (status === 502) return "Keycloak is unreachable (this often means SMTP is not configured).";
  if (status === 503) return "Identity management not configured.";
  return "HTTP " + status + ": " + (error?.message || "unknown");
}

function resendInvitation(row, container, btn) {
  // UI-004: double-clicking the row's resend button used to dispatch N
  // duplicate invitation emails (Keycloak does not dedupe action emails).
  // Disable the per-row button while the request is in flight; re-enable on
  // failure so the operator can retry. On success the parent view re-renders
  // and the button is recreated fresh — no manual re-enable needed.
  if (!btn || btn.disabled) return;
  btn.disabled = true;
  const originalLabel = btn.textContent;
  btn.textContent = "sending…";
  apiTry("/admin/invitations/" + encodeURIComponent(row.id) + "/resend", { method: "POST" })
    .then(({ ok, status, error }) => {
      if (ok) {
        toastOk("Invitation email re-sent to " + (row.email || row.username || row.id) + ".", "Invitation resent");
        if (container) invitationsView({ container });
      } else {
        toastBad(formatError(status, error), "Resend failed");
        if (document.body.contains(btn)) {
          btn.disabled = false;
          btn.textContent = originalLabel;
        }
      }
    });
}

function confirmRevoke(row, container) {
  let busy = false;
  let close;
  close = openModal({
    title: "Revoke invitation?",
    body: h("div", null,
      h("p", null, "Revoke invitation for ", h("strong", null, row.email || row.username || row.id), "?"),
      h("p", "muted text-xs", "This deletes the underlying Keycloak user. If they had already accepted, use Users → Delete instead."),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Revoke", bad: true, onClick: () => {
        if (busy) return false;
        busy = true;
        apiTry("/admin/invitations/" + encodeURIComponent(row.id), { method: "DELETE" })
          .then(({ ok, status, error }) => {
            if (ok) {
              toastOk("Invitation revoked.", "Revoked");
              if (close) close();
              if (container) invitationsView({ container });
            } else {
              toastBad(formatError(status, error), "Revoke failed");
              busy = false;
            }
          });
        return false;
      } },
    ],
  });
}
