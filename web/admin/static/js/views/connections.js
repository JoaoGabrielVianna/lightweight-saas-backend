// connections.js — a workspace's connections: the screen that makes the rest
// of the console possible.
//
// A workspace with no active connection routes nowhere, so every identity view
// can only say "not connected". This is where an operator fixes that, and it
// is the minimum surface for the Slice 6 journey:
//
//   list · create · edit draft · replace secret · verify · activate · retire
//
// ─── The secret ─────────────────────────────────────────────────────────────
//
// The stored client secret is NEVER rendered, and cannot be: the API has no
// endpoint that returns it, and ConnectionResponse has no field for it. This
// view shows only `has_client_secret` as the word "configured".
//
// On edit, a blank secret field means "keep the existing one". That is not a
// client-side invention — PATCH treats an ABSENT client_secret as unchanged
// (UpdateConnectionRequest uses a pointer field), so the view simply omits the
// key when the operator leaves it blank.
//
// ─── The lifecycle ──────────────────────────────────────────────────────────
//
//   draft ──verify──> (healthy) ──activate──> active ──retire──> retired
//
// Activation requires a verification that both PASSED and is still inside its
// validity window, which is why Verify sits next to Activate rather than being
// folded into it: an operator must be able to read the report before promoting
// a connection into service.

import { h, mount, relativeTime } from "../lib/dom.js";
import { apiTry, wsPath } from "../lib/api.js";
import { pageHeader, card, pill, spinner, emptyState, statusBadge, kvList } from "../components/common.js";
import { openModal } from "../components/modal.js";
import { toastOk, toastBad } from "../components/toast.js";
import { navigate, wsRoute } from "../lib/router.js";
import {
  loadActiveConnection, getWorkspaces, loadWorkspaces,
  captureWorkspaceToken, isWorkspaceStale, wsMutate,
  CONNECTION_STATE, classifyConnection, GATE,
} from "../lib/workspaces.js";
import { renderGateState, renderAPIError, describeFailure, connectionPill } from "../components/ws-states.js";

// ACCESS_MODE_LABEL renders the backend's four-value vocabulary. `read_only`
// is the value TD-024 added: reads work, writes provably do not.
const ACCESS_MODE_LABEL = {
  full:      { text: "full access",       kind: "ok" },
  read_only: { text: "read-only",         kind: "warn" },
  limited:   { text: "limited",           kind: "bad" },
  unknown:   { text: "capability unknown", kind: "neutral" },
};

/**
 * buildConnectionBody — turn the form fields into the request body.
 *
 * Extracted from the modal so the one rule that could leak a credential, and
 * the one rule that could silently wipe one, are both directly testable:
 *
 *   - the secret appears in exactly one place, `client_secret`, and only when
 *     the operator typed one;
 *   - a BLANK secret on edit OMITS the key entirely. That is not a
 *     client-side convention invented here — PATCH's UpdateConnectionRequest
 *     uses a pointer field, so an absent `client_secret` already means
 *     "unchanged". Sending `""` would replace a working credential with an
 *     empty one.
 *
 * @returns {{ok: true, body: object} | {ok: false, missing: string}}
 */
export function buildConnectionBody({ name, baseURL, realm, clientID, secret, isEdit }) {
  const body = {
    name: (name || "").trim(),
    base_url: (baseURL || "").trim(),
    realm: (realm || "").trim(),
    client_id: (clientID || "").trim(),
  };
  for (const [field, value] of Object.entries(body)) {
    if (!value) return { ok: false, missing: field };
  }
  if (secret) body.client_secret = secret;
  else if (!isEdit) return { ok: false, missing: "client_secret" };

  return { ok: true, body };
}

export default async function connectionsView({ container, params }) {
  const workspaceId = params.workspace_id;

  if (!getWorkspaces().length) await loadWorkspaces();
  const workspace = getWorkspaces().find((w) => w.id === workspaceId) || null;

  mount(container,
    pageHeader(
      h("span", null, "Connections", workspace ? h("span", "muted", " · " + workspace.name) : null),
      h("span", null,
        "How this workspace reaches its identity provider. Exactly one connection can be active at a time. ",
        statusBadge("live"),
      ),
      [
        h("button", { class: "btn", onclick: () => navigate("/workspaces") }, "← all workspaces"),
        h("button", { class: "btn btn-primary", onclick: () => openConnectionModal(null, container, params) }, "+ New connection"),
      ],
    ),
    h("div", { id: "conn-content" }, h("div", "row", spinner(), h("span", "muted", "loading…"))),
  );

  // This view is reachable for a workspace with NO connection — that is its
  // whole purpose — so it must not go through the connection gate. It only
  // needs the workspace itself to exist and be active.
  if (!workspace) {
    mount(container.querySelector("#conn-content"), renderGateState({
      reason: getWorkspaces().length ? GATE.UNKNOWN_WORKSPACE : GATE.NO_WORKSPACES,
      workspaceId,
    }));
    return;
  }
  if (workspace.status === "archived") {
    mount(container.querySelector("#conn-content"), renderGateState({ reason: GATE.ARCHIVED, workspaceId }));
    return;
  }

  const token = captureWorkspaceToken();
  const r = await apiTry(wsPath(workspaceId, "/connections"), { signal: token.signal });
  if (isWorkspaceStale(token)) return;

  const target = container.querySelector("#conn-content");
  if (!target) return;

  if (!r.ok) {
    mount(target, renderAPIError(r, { onRetry: () => connectionsView({ container, params }) }));
    return;
  }

  const connections = r.data.connections || [];
  if (!connections.length) {
    mount(target, emptyState({
      icon: "⇄",
      title: "No connections yet",
      body: "Create a connection pointing at a Keycloak realm, verify it, then activate it. Until one is active this workspace cannot perform identity operations.",
      action: h("button", { class: "btn btn-primary", onclick: () => openConnectionModal(null, container, params) }, "Create the first connection"),
    }));
    return;
  }

  mount(target, ...connections.map((c) => renderConnectionCard(c, container, params)));
}

function renderConnectionCard(c, container, params) {
  const mode = ACCESS_MODE_LABEL[c.access_mode] || ACCESS_MODE_LABEL.unknown;
  const isDraft = c.status === "draft";
  const isActive = c.status === "active";
  const isRetired = c.status === "retired";

  return card({
    title: h("span", null,
      c.name, " ",
      pill(c.status, isActive ? "ok" : isRetired ? "neutral" : "warn"),
    ),
    subtitle: c.id,
    actions: [
      h("button", {
        class: "btn btn-sm",
        disabled: isRetired,
        title: isRetired ? "Retired connections cannot be verified" : "Probe the provider and record the verdict",
        onclick: (e) => verify(c, container, params, e.currentTarget),
      }, "Verify"),
      h("button", {
        class: "btn btn-sm",
        disabled: !isDraft,
        title: isDraft ? "Edit this draft" : "Only drafts can be edited — their coordinates are what verification was run against",
        onclick: () => openConnectionModal(c, container, params),
      }, "Edit"),
      h("button", {
        class: "btn btn-sm btn-primary",
        disabled: !isDraft || !c.verified,
        title: !isDraft
          ? "Only a draft can be activated"
          : !c.verified
            ? "Activation needs a verification that passed and has not expired. Press Verify first."
            : "Make this the connection this workspace routes through",
        onclick: () => confirmActivate(c, container, params),
      }, "Activate"),
      h("button", {
        class: "btn btn-sm btn-bad",
        disabled: isRetired,
        title: isRetired ? "Already retired" : "Take this connection out of service permanently",
        onclick: () => confirmRetire(c, container, params),
      }, "Retire"),
    ],
    body: h("div", "col",
      h("div", "row",
        connectionPill(
          isActive ? classifyConnection({ status: "active" }, c) : CONNECTION_STATE.UNKNOWN,
          c,
        ),
        pill(mode.text, mode.kind),
        c.verified ? pill("verification valid", "ok") : pill("needs verification", "warn"),
      ),
      kvList([
        ["provider",   c.provider],
        ["base url",   h("code", null, c.base_url)],
        ["realm",      h("code", null, c.realm)],
        ["client id",  h("code", null, c.client_id)],
        // Never the value. Only whether one exists.
        ["client secret", c.has_client_secret ? pill("configured", "ok") : pill("missing", "bad")],
        ["health",     c.health + (c.health_message ? " — " + c.health_message : "")],
        ["last verified", c.last_verified_at ? relativeTime(c.last_verified_at) : "never"],
        ["can write",  c.can_write ? pill("yes", "ok") : pill("no", "warn")],
      ]),
    ),
  });
}

// ─── Actions ────────────────────────────────────────────────────────────────

function verify(c, container, params, btn) {
  // Verification is a network probe with a 20s server-side budget; without a
  // busy guard an impatient operator queues several against a slow provider.
  if (btn) { btn.disabled = true; btn.textContent = "Verifying…"; }
  wsMutate(params.workspace_id, "/connections/" + encodeURIComponent(c.id) + "/verify", { method: "POST" })
    .then((res) => {
      if (res.stale) return;
      if (!res.ok) {
        toastBad(describeFailure(res), "Verify failed");
        return;
      }
      showVerifyReport(res.data, container, params);
    })
    .finally(() => {
      if (btn && document.body.contains(btn)) { btn.disabled = false; btn.textContent = "Verify"; }
    });
}

// showVerifyReport renders the per-check report. The whole reason verification
// returns a list of checks rather than a boolean is that "unreachable", "wrong
// realm", "bad credentials" and "under-privileged" send an operator to four
// different screens.
function showVerifyReport(payload, container, params) {
  const report = payload?.report || {};
  const checks = Array.isArray(report.checks) ? report.checks : [];
  const mode = ACCESS_MODE_LABEL[report.access_mode] || ACCESS_MODE_LABEL.unknown;

  openModal({
    title: report.ok ? "Verification passed" : "Verification failed",
    body: h("div", "col",
      h("div", "row",
        pill(report.ok ? "ok" : "failed", report.ok ? "ok" : "bad"),
        pill(mode.text, mode.kind),
      ),
      h("p", null, report.summary || ""),
      h("ul", { class: "check-list" }, ...checks.map((c) => h("li", null,
        h("span", { class: "pill pill-" + (c.ok ? "ok" : "bad") }, c.ok ? "✓" : "✗"),
        " ",
        h("strong", null, c.name),
        " — ",
        c.detail,
      ))),
      report.access_mode === "read_only"
        ? h("p", "muted text-xs",
            "This service account can read the realm but holds no permission to change it. " +
            "Identity views will work read-only until it is granted realm-management/manage-users.")
        : null,
      report.access_mode === "unknown"
        ? h("p", "muted text-xs",
            "The provider did not publish this account's grants, so write capability could not be proven either way. " +
            "Writes will be attempted and may be refused by the provider.")
        : null,
    ),
    actions: [{ label: "Close", primary: true, onClick: () => { connectionsView({ container, params }); } }],
  });
}

function confirmActivate(c, container, params) {
  let busy = false;
  let close;
  close = openModal({
    title: "Activate this connection?",
    body: h("div", null,
      h("p", null, "Route ", h("strong", null, "this workspace"), " through ", h("code", null, c.name), " (realm ", h("code", null, c.realm), ")."),
      h("p", "muted text-xs", "Any connection currently active in this workspace is retired in the same operation — a workspace routes through exactly one."),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Activate", primary: true, onClick: () => {
        if (busy) return false;
        busy = true;
        wsMutate(params.workspace_id, "/connections/" + encodeURIComponent(c.id) + "/activate", { method: "POST" })
          .then((res) => {
            if (res.stale) { if (close) close(); return; }
            if (res.ok) {
              toastOk(`"${c.name}" is now this workspace's active connection.`, "Activated");
              if (close) close();
              // The shell caches the active connection per workspace; this
              // just changed it, so refresh rather than wait for a switch.
              loadActiveConnection(params.workspace_id).then(() => connectionsView({ container, params }));
            } else {
              toastBad(describeFailure(res), "Activate failed");
              busy = false;
            }
          });
        return false;
      } },
    ],
  });
}

function confirmRetire(c, container, params) {
  let busy = false;
  let close;
  close = openModal({
    title: "Retire this connection?",
    body: h("div", null,
      h("p", null, "Retire ", h("code", null, c.name), "?"),
      h("p", "muted text-xs",
        "Retiring is permanent: a retired connection never returns to service. ",
        c.status === "active"
          ? "This workspace is currently routing through it, and will have NO active connection afterwards — identity views will stop working until another is activated."
          : "",
      ),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Retire", bad: true, onClick: () => {
        if (busy) return false;
        busy = true;
        wsMutate(params.workspace_id, "/connections/" + encodeURIComponent(c.id) + "/retire", { method: "POST" })
          .then((res) => {
            if (res.stale) { if (close) close(); return; }
            if (res.ok) {
              toastOk(`"${c.name}" retired.`, "Retired");
              if (close) close();
              loadActiveConnection(params.workspace_id).then(() => connectionsView({ container, params }));
            } else {
              toastBad(describeFailure(res), "Retire failed");
              busy = false;
            }
          });
        return false;
      } },
    ],
  });
}

function openConnectionModal(existing, container, params) {
  const isEdit = !!existing;

  const name     = h("input", { type: "text", value: existing?.name || "", placeholder: "Production Keycloak", autocomplete: "off" });
  const baseURL  = h("input", { type: "url",  value: existing?.base_url || "", placeholder: "http://keycloak:8080", autocomplete: "off" });
  const realm    = h("input", { type: "text", value: existing?.realm || "", placeholder: "saas", autocomplete: "off" });
  const clientID = h("input", { type: "text", value: existing?.client_id || "", placeholder: "lightweight-admin", autocomplete: "off" });
  const secret   = h("input", {
    type: "password",
    placeholder: isEdit ? "leave blank to keep the stored secret" : "the service-account secret",
    autocomplete: "new-password",
  });

  let busy = false;
  let close;
  close = openModal({
    title: isEdit ? "Edit connection: " + existing.name : "New connection",
    body: h("div", "col",
      h("label", null, h("div", "muted", "name *"), name),
      h("label", null, h("div", "muted", "base url *"), baseURL),
      h("p", "muted text-xs", "The URL THIS API uses to reach the provider — inside docker that is usually the service name, not the URL your browser uses."),
      h("label", null, h("div", "muted", "realm *"), realm),
      h("label", null, h("div", "muted", "admin client id *"), clientID),
      h("label", null,
        h("div", "muted", isEdit ? "client secret" : "client secret *"),
        secret,
      ),
      isEdit
        ? h("p", "muted text-xs",
            "Stored secret: ",
            existing.has_client_secret ? h("strong", null, "configured") : h("strong", null, "missing"),
            ". It is never displayed. Leave the field blank to keep it, or type a new one to replace it.")
        : h("p", "muted text-xs", "Sealed with AES-256-GCM before it reaches the database, and never returned by any endpoint."),
      h("p", "muted text-xs", "A new connection starts as a draft. Verify it, then activate it."),
    ),
    actions: [
      { label: "Cancel" },
      { label: isEdit ? "Save draft" : "Create connection", primary: true, onClick: () => {
        if (busy) return false;

        const built = buildConnectionBody({
          name: name.value,
          baseURL: baseURL.value,
          realm: realm.value,
          clientID: clientID.value,
          secret: secret.value,
          isEdit,
        });
        if (!built.ok) {
          toastBad(`${built.missing.replace(/_/g, " ")} is required.`, "Missing field");
          return false;
        }
        const body = built.body;

        busy = true;
        const req = isEdit
          ? wsMutate(params.workspace_id, "/connections/" + encodeURIComponent(existing.id), {
              method: "PATCH",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify(body),
            })
          : wsMutate(params.workspace_id, "/connections", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify(body),
            });

        req.then((res) => {
          if (res.stale) { if (close) close(); return; }
          if (res.ok) {
            toastOk(isEdit ? "Draft saved. Verify it again before activating." : "Connection created as a draft. Verify it next.",
              isEdit ? "Saved" : "Created");
            if (close) close();
            connectionsView({ container, params });
          } else {
            toastBad(describeFailure(res), (isEdit ? "Save" : "Create") + " failed");
            busy = false;
          }
        });
        return false;
      } },
    ],
  });
}
