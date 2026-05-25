// user-detail.js — REAL GET /admin/users/:id + edit (PATCH) + send reset
// email (POST) + delete (DELETE) + per-user role mappings. Stage A/B/C/D wired.

import { h, mount, relativeTime } from "../lib/dom.js";
import { apiTry } from "../lib/api.js";
import { pageHeader, card, kvList, pill, spinner, emptyState, statusBadge } from "../components/common.js";
import { navigate } from "../lib/router.js";
import { openModal } from "../components/modal.js";
import { toastOk, toastBad } from "../components/toast.js";

export default async function userDetailView({ container, params }) {
  mount(container,
    pageHeader("User detail", h("span", null, h("code", null, params.id), " ", statusBadge("live")), [
      h("button", { class: "btn", onclick: () => navigate("/users") }, "← back to users"),
    ]),
    h("div", { id: "ud-content" }, h("div", "row", spinner(), h("span", "muted", "loading…"))),
  );

  // Fetch the user + their roles in parallel.
  const [userR, rolesR] = await Promise.all([
    apiTry("/admin/users/" + encodeURIComponent(params.id)),
    apiTry("/admin/users/" + encodeURIComponent(params.id) + "/roles"),
  ]);
  const target = container.querySelector("#ud-content");
  if (!target) return;

  if (!userR.ok) {
    mount(target, emptyState({
      icon: userR.status === 404 ? "?" : "✗",
      title: userR.status === 404 ? "User not found" : "Request failed",
      body: userR.status === 404
        ? `No user with id ${params.id}.`
        : `HTTP ${userR.status}: ${userR.error?.message || "unknown error"}`,
    }));
    return;
  }
  const u = userR.data;
  const userRoles = (rolesR.ok ? (rolesR.data.roles || []) : []);

  mount(target,
    card({
      title: u.username || "user",
      subtitle: u.email,
      actions: [
        h("button", { class: "btn", onclick: () => openEditModal(u, container) }, "Edit"),
        h("button", { class: "btn", onclick: () => toggleEnabled(u, container) },
          u.enabled ? "Disable" : "Enable"),
        h("button", { class: "btn btn-warn", id: "ud-reset-btn", onclick: (e) => sendResetEmail(u, e.currentTarget) }, "Send reset email"),
        h("button", { class: "btn btn-warn", onclick: () => confirmLogoutAll(u) }, "Logout all sessions"),
        h("button", { class: "btn btn-bad", onclick: () => confirmDelete(u) }, "Delete"),
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
        h("button", { class: "btn btn-sm", onclick: () => openAssignRoleModal(u, userRoles, container) }, "+ Assign role"),
      ],
      body: renderRoles(u, userRoles, container),
    }),

    card({
      title: "Attributes",
      subtitle: "Keycloak custom user attributes",
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

function renderRoles(u, roles, container) {
  if (!roles.length) {
    return h("p", "muted", "This user has no realm roles assigned.");
  }
  return h("div", "row",
    ...roles.map(r => h("span", { style: { display: "inline-flex", alignItems: "center", gap: "4px", marginRight: "8px" } },
      pill(r.name, r.name === "admin" ? "accent" : (r.builtin ? "neutral" : "ok")),
      // Built-ins (offline_access, uma_authorization, default-roles-*) can't
      // be removed cleanly via the API — Keycloak rejects unmapping them.
      // Hide the X for those; service guards will still 403 if abused.
      r.builtin ? null : h("button", {
        class: "btn btn-xs btn-bad",
        style: { padding: "2px 6px", lineHeight: "1" },
        title: "Remove this role",
        onclick: () => confirmUnassign(u, r, container),
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

// ─── Mutations ───────────────────────────────────────────────────────────

function openEditModal(u, container) {
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
        // SMTP validation: if the email field changed, enforce the same
        // pattern the server uses (identity.emailPattern). The server will
        // reject malformed emails with 400, but client-side rejection
        // avoids the round-trip AND prevents an SMTP-bound retry later
        // (verify-email action emails won't land if the address is bad).
        const newEmail = email.value.trim();
        const emailChanged = newEmail !== (u.email || "");
        if (emailChanged && newEmail !== "" && !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(newEmail)) {
          toastBad("Enter a valid email address (e.g. person@example.com).", "Invalid email");
          return false;
        }
        busy = true;
        // Only send fields that actually changed so the PATCH stays tight
        // and we don't accidentally re-write identical values into Keycloak.
        const body = {};
        if (firstName.value.trim() !== (u.first_name || "")) body.first_name = firstName.value.trim();
        if (lastName.value.trim()  !== (u.last_name  || "")) body.last_name  = lastName.value.trim();
        if (emailChanged) body.email = newEmail;
        if (Object.keys(body).length === 0) {
          toastOk("No changes to save.", "Up to date");
          if (close) close();
          return false;
        }
        patchUser(u.id, body).then(({ ok, status, error }) => {
          if (ok) {
            toastOk("User updated.", "Saved");
            if (close) close();
            if (container) userDetailView({ container, params: { id: u.id } });
          } else {
            toastBad(formatError(status, error), "Update failed");
            busy = false;
          }
        });
        return false;
      } },
    ],
  });
}

function toggleEnabled(u, container) {
  // No modal for enable; modal for disable (it's the irreversible-ish one
  // that can lock people out — confirmation is worth one click).
  const turningOff = u.enabled;
  if (!turningOff) {
    patchUser(u.id, { enabled: true }).then(({ ok, status, error }) => {
      if (ok) {
        toastOk("User enabled.", "Saved");
        if (container) userDetailView({ container, params: { id: u.id } });
      } else {
        toastBad(formatError(status, error), "Enable failed");
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
        patchUser(u.id, { enabled: false }).then(({ ok, status, error }) => {
          if (ok) {
            toastOk("User disabled.", "Saved");
            if (close) close();
            if (container) userDetailView({ container, params: { id: u.id } });
          } else {
            toastBad(formatError(status, error), "Disable failed");
            busy = false;
          }
        });
        return false;
      } },
    ],
  });
}

function sendResetEmail(u, btn) {
  // UI-003: double-clicks on this button used to send N action emails to the
  // user's inbox (Keycloak does not dedupe). The button is disabled while the
  // request is in flight; on completion (success or failure) we re-enable so
  // the operator can retry. Guards on the actual DOM node so a stale closure
  // from a re-render can't bypass the flag.
  if (!btn || btn.disabled) return;
  btn.disabled = true;
  const originalLabel = btn.textContent;
  btn.textContent = "Sending…";
  apiTry("/admin/users/" + encodeURIComponent(u.id) + "/reset-password", { method: "POST" })
    .then(({ ok, status, error }) => {
      if (ok) {
        toastOk("Password-reset email sent to " + (u.email || u.username) + ".", "Email queued");
      } else {
        toastBad(formatError(status, error), "Reset failed");
      }
    })
    .finally(() => {
      if (document.body.contains(btn)) {
        btn.disabled = false;
        btn.textContent = originalLabel;
      }
    });
}

function confirmLogoutAll(u) {
  let busy = false;
  let close;
  close = openModal({
    title: "Logout every session?",
    body: h("p", null,
      "This invalidates every active session for ", h("strong", null, u.username || u.email),
      " across every client. They'll be signed out everywhere immediately.",
    ),
    actions: [
      { label: "Cancel" },
      { label: "Logout all", bad: true, onClick: () => {
        if (busy) return false;
        busy = true;
        apiTry("/admin/users/" + encodeURIComponent(u.id) + "/sessions", { method: "DELETE" })
          .then(({ ok, status, error }) => {
            if (ok) {
              toastOk("All sessions terminated.", "Logged out everywhere");
              if (close) close();
            } else {
              toastBad(formatError(status, error), "Logout failed");
              busy = false;
            }
          });
        return false;
      } },
    ],
  });
}

function confirmDelete(u) {
  let busy = false;
  let close;
  close = openModal({
    title: "Delete user?",
    body: h("div", null,
      h("p", null, "Permanently delete ", h("strong", null, u.username || u.email), "?"),
      h("p", "muted text-xs",
        "Self-delete and last-admin removal are blocked at the API tier — you'll see a 403 if either applies.",
      ),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Delete", bad: true, onClick: () => {
        if (busy) return false;
        busy = true;
        apiTry("/admin/users/" + encodeURIComponent(u.id), { method: "DELETE" })
          .then(({ ok, status, error }) => {
            if (ok) {
              toastOk("User deleted.", "Deleted");
              if (close) close();
              navigate("/users");
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

function openAssignRoleModal(u, currentRoles, container) {
  // Build a name set of roles already assigned so we don't show duplicates.
  const assigned = new Set(currentRoles.map(r => r.name));
  const select = h("select", null, h("option", { value: "" }, "Loading roles…"));

  // Roles dropdown loads from GET /admin/roles, filtering out built-ins
  // (Keycloak rejects assigning offline_access / uma_authorization through
  // this surface) and roles already present.
  apiTry("/admin/roles").then(({ ok, data }) => {
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
      h("p", "muted text-xs", "Granting `admin` is unrestricted — but removing your own admin later is blocked."),
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
        apiTry("/admin/users/" + encodeURIComponent(u.id) + "/roles", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ roles: [select.value] }),
        }).then(({ ok, status, error }) => {
          if (ok) {
            toastOk(`Role "${select.value}" assigned.`, "Assigned");
            if (close) close();
            if (container) userDetailView({ container, params: { id: u.id } });
          } else {
            toastBad(formatError(status, error), "Assign failed");
            busy = false;
          }
        });
        return false;
      } },
    ],
  });
}

function confirmUnassign(u, role, container) {
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
        apiTry("/admin/users/" + encodeURIComponent(u.id) + "/roles/" + encodeURIComponent(role.name), {
          method: "DELETE",
        }).then(({ ok, status, error }) => {
          if (ok) {
            toastOk(`Role "${role.name}" removed.`, "Removed");
            if (close) close();
            if (container) userDetailView({ container, params: { id: u.id } });
          } else {
            toastBad(formatError(status, error), "Remove failed");
            busy = false;
          }
        });
        return false;
      } },
    ],
  });
}

async function patchUser(id, body) {
  return apiTry("/admin/users/" + encodeURIComponent(id), {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

function formatError(status, error) {
  if (status === 400) return "Invalid input. Check email format and field values.";
  if (status === 401) return "Sign in required.";
  if (status === 403) return "Blocked by a service-tier guard: self-action or last-admin protection.";
  if (status === 404) return "User or referenced role not found.";
  if (status === 409) return "Conflict — duplicate email or role.";
  if (status === 502) return "Keycloak unreachable (or SMTP not configured for email actions).";
  if (status === 503) return "Identity management not configured.";
  return "HTTP " + status + ": " + (error?.message || "unknown");
}
