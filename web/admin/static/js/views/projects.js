// projects.js — Projects and their machine credentials.
//
// A Project is a backend that consumes the LIGHTWEIGHT API on behalf of ONE
// workspace. This screen is where an operator creates one, issues it a
// credential with explicit scopes, and revokes that credential when it should
// stop working.
//
// ─── The one-time secret ────────────────────────────────────────────────────
//
// The credential secret is returned by exactly one response and never again.
// There is no endpoint that can show it, because only a SHA-256 digest is
// stored. The UI has to make that unmissable rather than merely true, which is
// why the result gets its own modal instead of a toast: an operator needs time
// and focus to store a value they cannot ask for twice.
//
// ─── Operator only ──────────────────────────────────────────────────────────
//
// Every endpoint behind this view is operator-only in the server's
// authorization registry. A credential that could mint credentials would make
// revocation meaningless — revoke one, and it has already issued another.

import { h, mount, relativeTime } from "../lib/dom.js";
import { apiTry, wsPath } from "../lib/api.js";
import {
  wsMutate, enterWorkspace, captureWorkspaceToken, isWorkspaceStale,
} from "../lib/workspaces.js";
import { pageHeader, card, pill, spinner, emptyState, statusBadge, kvList } from "../components/common.js";
import { renderTable } from "../components/table.js";
import { openModal } from "../components/modal.js";
import { toastOk, toastBad } from "../components/toast.js";
import { navigate, wsRoute } from "../lib/router.js";
import {
  renderGateState, renderAPIError, describeFailure, connectionBanner,
} from "../components/ws-states.js";

// SCOPE_GROUPS renders the vocabulary the server advertises, grouped by
// resource so an operator reads "what can it do to users?" rather than an
// alphabetical list of sixteen words.
//
// `sensitive` marks the scopes whose consequences are not obvious from the
// name. They get an inline warning at selection time, which is the only moment
// the warning can change a decision.
const SCOPE_GROUPS = [
  {
    resource: "Users",
    scopes: [
      { id: "users:read",  label: "Read",  hint: "List and read users in this workspace's realm." },
      { id: "users:write", label: "Write", hint: "Create, update and delete users, and send password-reset emails. Does NOT include setting a password directly." },
    ],
  },
  {
    resource: "Roles",
    scopes: [
      { id: "roles:read",  label: "Read",  hint: "List realm roles and their membership." },
      {
        id: "roles:write", label: "Write",
        hint: "Create, update and delete realm roles, and grant or revoke them on users.",
        sensitive: "Administrative roles (admin, user, offline_access, default-roles-*) are refused for project credentials, so this cannot be used to make someone a realm admin.",
      },
    ],
  },
  {
    resource: "Sessions",
    scopes: [
      { id: "sessions:read",   label: "Read",   hint: "List active sessions." },
      { id: "sessions:revoke", label: "Revoke", hint: "Sign a user out of one session or all of them." },
    ],
  },
  {
    resource: "Invitations",
    scopes: [
      { id: "invitations:read",  label: "Read",  hint: "List pending invitations." },
      {
        id: "invitations:write", label: "Write",
        hint: "Create, resend and revoke invitations.",
        sensitive: "Revoking an invitation DELETES the underlying user, because an invitation is a user in an invited-but-incomplete state.",
      },
    ],
  },
  {
    resource: "Audit",
    scopes: [
      {
        id: "audit:read", label: "Read",
        hint: "Read the Workspace audit trail.",
        sensitive: "This shows EVERY actor's history in this workspace, including operators and other projects — not just this credential's own actions.",
      },
    ],
  },
];

// ─── List ───────────────────────────────────────────────────────────────────

export default async function projectsView({ container, params }) {
  const workspaceId = params.workspace_id;

  mount(container,
    pageHeader("Projects", h("span", null,
      "Backends that call this workspace's API with a machine credential instead of an operator token. ",
      statusBadge("live"),
    ), [
      h("button", {
        class: "btn btn-primary",
        id: "prj-new-btn",
        onclick: () => openCreateProjectModal(container, params),
      }, "+ New project"),
    ]),
    h("div", { id: "prj-content" }, h("div", "row", spinner(), h("span", "muted", "loading…"))),
  );

  const gate = await enterWorkspace(workspaceId);
  let target = container.querySelector("#prj-content");
  if (!target) return;
  if (!gate.ok) {
    mount(target, renderGateState(gate, { onRetry: () => projectsView({ container, params }) }));
    return;
  }

  const token = captureWorkspaceToken();
  const r = await apiTry(wsPath(workspaceId, "/projects"), { signal: token.signal });
  if (isWorkspaceStale(token)) return;

  target = container.querySelector("#prj-content");
  if (!target) return;
  if (!r.ok) {
    mount(target, renderAPIError(r, { onRetry: () => projectsView({ container, params }) }));
    return;
  }

  const projects = r.data.projects || [];
  if (!projects.length) {
    mount(target,
      connectionBanner(gate.connectionState, gate.connection, { workspaceId }),
      emptyState({
        icon: "⚿",
        title: "No projects yet",
        body: "A project represents one backend that consumes this workspace's API. Create one, then issue it a credential with only the scopes it needs.",
        action: h("button", {
          class: "btn btn-primary",
          onclick: () => openCreateProjectModal(container, params),
        }, "Create the first project"),
      }),
    );
    return;
  }

  mount(target,
    connectionBanner(gate.connectionState, gate.connection, { workspaceId }),
    h("div", { id: "prj-table" }),
  );

  renderTable(container.querySelector("#prj-table"), {
    toolbar: {
      actions: [h("button", { class: "btn btn-sm", onclick: () => projectsView({ container, params }) }, "↻ refresh")],
    },
    columns: [
      { key: "name", title: "Name", render: (v) => h("strong", null, v) },
      { key: "id",   title: "ID",   render: (v) => h("code", "text-xs", v) },
      { key: "status", title: "Status", render: (v) => pill(v, v === "active" ? "ok" : "neutral") },
      {
        key: "active_credentials", title: "Active credentials",
        render: (v) => v > 0 ? pill(String(v), "accent") : h("span", "dim", "none"),
      },
      { key: "created_at", title: "Created", render: (v) => v ? relativeTime(v) : "—" },
      { key: "_actions", title: "", width: "220px", render: (_, row) => h("div", "row",
          h("button", {
            class: "btn btn-xs",
            onclick: (e) => { e.stopPropagation(); openProject(workspaceId, row); },
          }, "open"),
          h("button", {
            class: "btn btn-xs",
            disabled: row.status !== "active",
            title: row.status === "active" ? "Rename" : "Archived projects cannot be renamed",
            onclick: (e) => { e.stopPropagation(); openRenameModal(row, container, params); },
          }, "rename"),
          h("button", {
            class: "btn btn-xs btn-bad",
            disabled: row.status !== "active",
            title: row.status === "active" ? "Archive" : "Already archived",
            onclick: (e) => { e.stopPropagation(); confirmArchive(row, container, params); },
          }, "archive"),
        ),
      },
    ],
    rows: projects,
    onRowClick: (row) => openProject(workspaceId, row),
    empty: { title: "No projects", body: "" },
  });
}

function openProject(workspaceId, row) {
  navigate(wsRoute(workspaceId, "projects/" + row.id));
}

// ─── Detail ─────────────────────────────────────────────────────────────────

export async function projectDetailView({ container, params }) {
  const workspaceId = params.workspace_id;
  const projectId = params.project_id;

  mount(container,
    pageHeader("Project", h("span", null, h("code", null, projectId), " ", statusBadge("live")), [
      h("button", { class: "btn", onclick: () => navigate(wsRoute(workspaceId, "projects")) }, "← all projects"),
    ]),
    h("div", { id: "prj-detail" }, h("div", "row", spinner(), h("span", "muted", "loading…"))),
  );

  const gate = await enterWorkspace(workspaceId);
  let target = container.querySelector("#prj-detail");
  if (!target) return;
  if (!gate.ok) {
    mount(target, renderGateState(gate, { onRetry: () => projectDetailView({ container, params }) }));
    return;
  }

  const token = captureWorkspaceToken();
  const [projectR, credsR] = await Promise.all([
    apiTry(wsPath(workspaceId, "/projects/" + encodeURIComponent(projectId)), { signal: token.signal }),
    apiTry(wsPath(workspaceId, "/projects/" + encodeURIComponent(projectId) + "/credentials"), { signal: token.signal }),
  ]);
  if (isWorkspaceStale(token)) return;

  target = container.querySelector("#prj-detail");
  if (!target) return;
  if (!projectR.ok) {
    mount(target, renderAPIError(projectR, { onRetry: () => projectDetailView({ container, params }) }));
    return;
  }

  const project = projectR.data;
  const credentials = credsR.ok ? (credsR.data.credentials || []) : [];
  const archived = project.status !== "active";

  mount(target,
    card({
      title: h("span", null, project.name, " ", pill(project.status, archived ? "neutral" : "ok")),
      subtitle: project.id,
      actions: [
        h("button", {
          class: "btn btn-primary",
          disabled: archived,
          title: archived ? "Archived projects cannot issue credentials" : "Issue a new credential",
          onclick: () => openCreateCredentialModal(container, params, project),
        }, "+ New credential"),
      ],
      body: h("div", "col",
        archived
          ? h("div", { class: "ws-banner", role: "status" },
              pill("archived", "neutral"),
              h("span", null, "Every credential of this project stops authenticating immediately while it is archived."),
            )
          : null,
        kvList([
          ["workspace", h("code", null, project.workspace_id)],
          ["created",   project.created_at ? `${project.created_at} (${relativeTime(project.created_at)})` : "—"],
          ["active credentials", String(project.active_credentials ?? 0)],
        ]),
      ),
    }),

    card({
      title: h("span", null, "Credentials ", statusBadge("live")),
      subtitle: "The secret is shown once, at creation, and cannot be recovered afterwards.",
      body: credentials.length
        ? h("div", { id: "prj-cred-table" })
        : emptyState({
            icon: "⚿",
            title: "No credentials yet",
            body: archived
              ? "This project is archived. Restore it before issuing credentials."
              : "Issue one with only the scopes this backend needs.",
          }),
    }),
  );

  if (!credentials.length) return;

  renderTable(container.querySelector("#prj-cred-table"), {
    columns: [
      { key: "label", title: "Label", render: (v) => h("strong", null, v) },
      { key: "key_prefix", title: "Key prefix", render: (v) => h("code", "text-xs", "lw_sk_" + v + "…") },
      {
        key: "scopes", title: "Scopes",
        render: (v) => h("div", "row", ...(v || []).map((s) => pill(s, isSensitiveScope(s) ? "warn" : "neutral"))),
      },
      {
        key: "status", title: "Status",
        render: (v) => pill(v, v === "active" ? "ok" : v === "expired" ? "warn" : "bad"),
      },
      { key: "last_used_at", title: "Last used", render: (v) => v ? relativeTime(v) : h("span", "dim", "never") },
      { key: "expires_at",   title: "Expires",   render: (v) => v ? relativeTime(v) : h("span", "dim", "never") },
      { key: "created_at",   title: "Created",   render: (v) => v ? relativeTime(v) : "—" },
      { key: "_actions", title: "", width: "110px", render: (_, row) => h("button", {
          class: "btn btn-xs btn-bad",
          disabled: row.status === "revoked",
          title: row.status === "revoked" ? "Already revoked" : "Revoke this credential",
          onclick: (e) => { e.stopPropagation(); confirmRevoke(row, container, params, project); },
        }, "revoke"),
      },
    ],
    rows: credentials,
    empty: { title: "No credentials", body: "" },
  });
}

function isSensitiveScope(id) {
  return SCOPE_GROUPS.some((g) => g.scopes.some((s) => s.id === id && s.sensitive));
}

// ─── Project mutations ──────────────────────────────────────────────────────

function openCreateProjectModal(container, params) {
  const name = h("input", { type: "text", placeholder: "Billing worker", autocomplete: "off" });

  let busy = false;
  let close;
  close = openModal({
    title: "New project",
    body: h("div", "col",
      h("label", null, h("div", "muted", "name *"), name),
      h("p", "muted text-xs",
        "A project is bound to THIS workspace permanently. The binding cannot be changed later: ",
        "it is what confines every credential of this project to one realm. If another workspace ",
        "needs API access, create a project there.",
      ),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Create", primary: true, onClick: () => {
        if (busy) return false;
        if (!name.value.trim()) { toastBad("Name is required.", "Missing name"); return false; }
        busy = true;
        wsMutate(params.workspace_id, "/projects", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name: name.value.trim() }),
        }).then((res) => {
          if (res.stale) { if (close) close(); return; }
          if (res.ok) {
            toastOk(`Project "${res.data?.name}" created.`, "Project created");
            if (close) close();
            // Straight to the detail page: a project with no credential does
            // nothing, so issuing one is the only useful next step.
            if (res.data?.id) navigate(wsRoute(params.workspace_id, "projects/" + res.data.id));
            else projectsView({ container, params });
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

function openRenameModal(row, container, params) {
  const name = h("input", { type: "text", value: row.name || "", autocomplete: "off" });

  let busy = false;
  let close;
  close = openModal({
    title: "Rename project",
    body: h("div", "col",
      h("label", null, h("div", "muted", "name"), name),
      h("p", "muted text-xs", "Renaming does not affect credentials: they are bound to the project's id, not its name."),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Save", primary: true, onClick: () => {
        if (busy) return false;
        const next = name.value.trim();
        if (!next) { toastBad("Name is required.", "Missing name"); return false; }
        if (next === row.name) { if (close) close(); return false; }
        busy = true;
        wsMutate(params.workspace_id, "/projects/" + encodeURIComponent(row.id), {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name: next }),
        }).then((res) => {
          if (res.stale) { if (close) close(); return; }
          if (res.ok) {
            toastOk("Project renamed.", "Saved");
            if (close) close();
            projectsView({ container, params });
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

function confirmArchive(row, container, params) {
  let busy = false;
  let close;
  close = openModal({
    title: "Archive project?",
    body: h("div", null,
      h("p", null, "Archive ", h("strong", null, row.name), "?"),
      h("p", "muted text-xs",
        "EVERY credential of this project stops authenticating immediately. Any backend using one ",
        "will start receiving 401 on its next request. Nothing is deleted, and the credential history ",
        "stays on record.",
      ),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Archive", bad: true, onClick: () => {
        if (busy) return false;
        busy = true;
        wsMutate(params.workspace_id, "/projects/" + encodeURIComponent(row.id) + "/archive", { method: "POST" })
          .then((res) => {
            if (res.stale) { if (close) close(); return; }
            if (res.ok) {
              toastOk(`Project "${row.name}" archived.`, "Archived");
              if (close) close();
              projectsView({ container, params });
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

// ─── Credential mutations ───────────────────────────────────────────────────

function openCreateCredentialModal(container, params, project) {
  const label = h("input", { type: "text", placeholder: "billing worker (staging)", autocomplete: "off" });
  const expires = h("input", { type: "datetime-local" });

  // NOTHING is pre-selected. A credential's power is always an explicit
  // choice, and a default set would be the one nobody revisits.
  const checkboxes = new Map();
  const scopeSection = h("div", "col",
    ...SCOPE_GROUPS.map((group) => h("div", "col",
      h("div", { class: "muted", style: { fontWeight: 600 } }, group.resource),
      ...group.scopes.map((scope) => {
        const box = h("input", { type: "checkbox", value: scope.id });
        checkboxes.set(scope.id, box);
        return h("label", { class: "scope-option" },
          box,
          h("div", "col",
            h("span", null, h("code", null, scope.id), " — ", scope.label),
            h("span", "muted text-xs", scope.hint),
            scope.sensitive ? h("span", { class: "scope-warning" }, "⚠ " + scope.sensitive) : null,
          ),
        );
      }),
    )),
  );

  let busy = false;
  let close;
  close = openModal({
    title: "New credential for " + project.name,
    body: h("div", "col",
      h("label", null, h("div", "muted", "label *"), label),
      h("p", "muted text-xs", "The label is how you will recognise this key later. Name the deployment that will hold it."),
      h("div", "muted", "scopes *"),
      scopeSection,
      h("label", null, h("div", "muted", "expires at (optional)"), expires),
      h("p", "muted text-xs",
        "The secret is shown ONCE, on the next screen. It cannot be recovered afterwards — ",
        "only a digest is stored, so no endpoint can return it.",
      ),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Create credential", primary: true, onClick: () => {
        if (busy) return false;

        if (!label.value.trim()) { toastBad("Label is required.", "Missing label"); return false; }
        const scopes = [...checkboxes.entries()].filter(([, box]) => box.checked).map(([id]) => id);
        if (!scopes.length) {
          toastBad("Select at least one scope. A credential with none can authenticate and do nothing.", "No scopes selected");
          return false;
        }

        const body = { label: label.value.trim(), scopes };
        if (expires.value) {
          const d = new Date(expires.value);
          if (!isNaN(d.getTime())) body.expires_at = d.toISOString();
        }

        busy = true;
        wsMutate(params.workspace_id, "/projects/" + encodeURIComponent(project.id) + "/credentials", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        }).then((res) => {
          if (res.stale) { if (close) close(); return; }
          if (res.ok) {
            if (close) close();
            showSecretOnce(res.data, container, params);
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

// integrationEnv renders the three variables a backend needs, as a .env block.
//
// These three names ARE the integration contract: the Go SDK's
// NewClientFromEnv reads exactly LIGHTWEIGHT_URL, LIGHTWEIGHT_WORKSPACE_ID and
// LIGHTWEIGHT_API_KEY, and has nowhere to read a fourth from. Naming them
// anything else here would produce a block that looks copy-pasteable and is
// not, which is worse than showing nothing.
//
// The base URL is location.origin rather than a configured value because the
// console is served BY the API: the origin the operator is reading this on is,
// by construction, the origin their backend must call. A separate setting would
// be a second source of truth that nothing validates.
export function integrationEnv(workspaceId, secret, origin) {
  return [
    "LIGHTWEIGHT_URL=" + origin,
    "LIGHTWEIGHT_WORKSPACE_ID=" + workspaceId,
    "LIGHTWEIGHT_API_KEY=" + secret,
  ].join("\n");
}

// showSecretOnce is the one screen in this console that displays a credential.
//
// A dedicated modal rather than a toast, and deliberately hard to dismiss by
// accident: the operator has to acknowledge that the value is not recoverable.
// Closing it drops the only copy this browser ever had.
//
// ─── Why the whole .env block, and not just the key ─────────────────────────
//
// This is the only moment the secret exists. An operator standing here holds
// all three values a backend needs — the origin they are looking at, the
// workspace in the path, and the key — and handing over one of the three sends
// them to the README to look up the names of the other two, at the exact
// moment they are least able to come back for a second attempt.
//
// The plaintext lives in this modal's DOM and nowhere else: it is never
// written to storage, never logged, never put in a URL, and cannot be
// reconstructed after the modal closes, because the server stores only a
// digest.
function showSecretOnce(payload, container, params) {
  const secret = payload?.secret || "";
  const credential = payload?.credential || {};
  const workspaceId = params.workspace_id;
  const origin = window.location.origin;

  const envBlock = integrationEnv(workspaceId, secret, origin);

  // readonly, not disabled: a disabled field cannot be selected, and manual
  // selection is the fallback whenever the clipboard API is refused.
  const envField = h("textarea", {
    readonly: true,
    rows: 3,
    class: "secret-field secret-env",
    spellcheck: "false",
    onclick: (e) => e.target.select(),
  });
  envField.value = envBlock;

  const copyEnvBtn = h("button", {
    class: "btn btn-primary",
    onclick: () => copyInto(copyEnvBtn, envField, envBlock, "Copy all three"),
  }, "Copy all three");

  const keyField = h("input", {
    type: "text",
    value: secret,
    readonly: true,
    class: "secret-field",
    onclick: (e) => e.target.select(),
  });

  const copyKeyBtn = h("button", {
    class: "btn",
    onclick: () => copyInto(copyKeyBtn, keyField, secret, "Copy key only"),
  }, "Copy key only");

  openModal({
    title: "Copy this credential now",
    body: h("div", "col",
      h("div", { class: "ws-banner ws-banner-secret", role: "alert" },
        pill("one time", "warn"),
        h("span", null, "This credential will not be shown again. There is no way to recover it: ",
          "only a digest is stored. If it is lost, create a new credential and revoke this one."),
      ),

      h("p", "muted text-xs", "Your backend needs these three values, and nothing else:"),
      envField,
      h("div", "row", copyEnvBtn, copyKeyBtn),

      kvList([
        ["label",  credential.label || "—"],
        ["id",     h("code", null, credential.id || "—")],
        ["scopes", h("div", "row", ...(credential.scopes || []).map((s) => pill(s, "neutral")))],
        ["expires", credential.expires_at || "never"],
      ]),

      h("details", { class: "muted text-xs" },
        h("summary", null, "Just the key"),
        h("div", "row", keyField),
        h("p", null,
          "Send it as ", h("code", null, "Authorization: Bearer <credential>"),
          " to ", h("code", null, "/v1/workspaces/" + workspaceId + "/…"),
        ),
      ),

      h("p", "muted text-xs",
        "Next: ",
        h("a", { href: "#/docs/getting-started/connect-backend" }, "connect your backend"),
        " · ",
        h("a", {
          href: "https://pkg.go.dev/github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go",
          target: "_blank", rel: "noopener noreferrer",
        }, "Go SDK reference"),
        " · ",
        h("a", { href: "#/swagger" }, "HTTP API"),
      ),
    ),
    actions: [
      { label: "I have stored it", primary: true, onClick: () => {
        projectDetailView({ container, params });
      } },
    ],
  });
}

// copyInto writes `text` to the clipboard and reports the outcome on the
// button itself.
//
// The clipboard API is refused in an insecure context and by some permission
// policies, and an operator who cannot copy the ONE value they will never see
// again needs to be told what to do instead rather than left with a button
// that silently did nothing. Selecting the field is the fallback that always
// works.
function copyInto(button, field, text, restoreLabel) {
  const done = (label) => {
    button.textContent = label;
    setTimeout(() => { button.textContent = restoreLabel; }, 2000);
  };
  if (!navigator.clipboard?.writeText) {
    field.select();
    done("Select and copy manually");
    return;
  }
  navigator.clipboard.writeText(text).then(
    () => done("Copied"),
    () => { field.select(); done("Select and copy manually"); },
  );
}

function confirmRevoke(row, container, params, project) {
  let busy = false;
  let close;
  close = openModal({
    title: "Revoke credential?",
    body: h("div", null,
      h("p", null, "Revoke ", h("strong", null, row.label), " (", h("code", null, row.key_prefix), ")?"),
      h("p", "muted text-xs",
        "Effective immediately: the next request using this credential fails with 401. ",
        "A request already in flight completes. This cannot be undone — issue a new credential instead.",
      ),
      row.last_used_at
        ? h("p", "muted text-xs", "Last used ", relativeTime(row.last_used_at), ". Something may still be using it.")
        : h("p", "muted text-xs", "This credential has never been used."),
    ),
    actions: [
      { label: "Cancel" },
      { label: "Revoke", bad: true, onClick: () => {
        if (busy) return false;
        busy = true;
        wsMutate(params.workspace_id,
          "/projects/" + encodeURIComponent(project.id) + "/credentials/" + encodeURIComponent(row.id) + "/revoke",
          { method: "POST" },
        ).then((res) => {
          if (res.stale) { if (close) close(); return; }
          if (res.ok) {
            toastOk(`Credential "${row.label}" revoked.`, "Revoked");
            if (close) close();
            projectDetailView({ container, params });
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

// _scopeGroupsForTests — exposed so the scope vocabulary rendered by the
// console can be pinned against the server's without booting a DOM.
export function _scopeGroupsForTests() {
  return SCOPE_GROUPS;
}
