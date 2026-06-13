// email-templates.js — Keycloak realm email message overrides with tabbed UI.
// Covers: GET /admin/settings/email-templates,
//         PUT /admin/settings/email-templates/:key,
//         DELETE /admin/settings/email-templates/:key.
//
// Variable syntax: Keycloak message bundles use Java MessageFormat positional
// placeholders, NOT FreeMarker syntax. Use {0}, {2}, {3} — not ${link}.
//
//   {0} = link (action URL)
//   {2} = realmName
//   {3} = expiration formatted (e.g., "12 horas")
//   {4} = required actions text  (invite only)

import { h, mount } from "../lib/dom.js";
import { apiTry } from "../lib/api.js";
import { pageHeader, card, spinner } from "../components/common.js";
import { toastOk, toastBad } from "../components/toast.js";

const TABS = [
  {
    id: "invite",
    label: "Convite",
    keys: ["executeActionsEmailSubject", "executeActionsEmailBodyHtml"],
  },
  {
    id: "reset",
    label: "Reset de Senha",
    keys: ["passwordResetSubject", "passwordResetBodyHtml"],
  },
  {
    id: "verify",
    label: "Verificação de Email",
    keys: ["emailVerificationSubject", "emailVerificationBodyHtml"],
  },
];

// Preview substitution — replaces MessageFormat placeholders with readable
// sample values so the preview iframe looks like a real email.
const PREVIEW_VARS = {
  "{0}": "https://auth.corsienterprise.com/realms/corsi/login-actions/action?token=EXAMPLE",
  "{1}": "720",
  "{2}": "Corsi Enterprise",
  "{3}": "12 horas",
  "{4}": "Verificar Email, Redefinir Senha",
};

export default async function emailTemplatesView({ container }) {
  mount(container,
    pageHeader("Templates de Email", "Personalize os textos dos emails enviados pelo Keycloak.", [
      h("span", { class: "pill pill-neutral" }, "realm setting"),
    ]),
    h("div", { id: "et-content" }, h("div", { class: "row" }, spinner(), h("span", { class: "muted" }, "carregando…"))),
  );

  const r = await apiTry("/admin/settings/email-templates");
  const target = container.querySelector("#et-content");
  if (!target) return;

  if (!r.ok) {
    mount(target, h("p", { class: "muted" }, "Falha ao carregar templates: HTTP " + r.status));
    return;
  }

  const byKey = Object.fromEntries((r.data?.templates || []).map(t => [t.key, t]));
  renderTabs(target, byKey);
}

function renderTabs(target, byKey) {
  let activeTab = TABS[0].id;

  const tabPanels = {};
  const tabButtons = {};

  TABS.forEach(tab => {
    const templates = tab.keys.map(k => byKey[k]).filter(Boolean);
    tabPanels[tab.id] = h("div", { style: { display: "none" } },
      h("div", { class: "col", style: { gap: "1rem" } },
        ...templates.map(t => renderTemplateCard(t)),
      ),
    );

    tabButtons[tab.id] = h("button", {
      class: "btn",
      style: { borderRadius: "8px 8px 0 0", borderBottom: "none" },
    }, tab.label);

    tabButtons[tab.id].addEventListener("click", () => setTab(tab.id));
  });

  function setTab(id) {
    activeTab = id;
    TABS.forEach(tab => {
      const isActive = tab.id === id;
      tabPanels[tab.id].style.display = isActive ? "" : "none";
      tabButtons[tab.id].style.background = isActive ? "var(--color-surface-raised, #1e1e2e)" : "";
      tabButtons[tab.id].style.borderColor = isActive ? "var(--color-border, #333)" : "transparent";
      tabButtons[tab.id].style.color = isActive ? "var(--color-primary, #6366f1)" : "";
      tabButtons[tab.id].style.fontWeight = isActive ? "600" : "";
    });
  }

  const tabBar = h("div", {
    style: { display: "flex", gap: "0.25rem", borderBottom: "1px solid var(--color-border, #333)", marginBottom: "1rem" },
  }, ...TABS.map(tab => tabButtons[tab.id]));

  mount(target, tabBar, ...TABS.map(tab => tabPanels[tab.id]));
  setTab(activeTab);
}

function renderTemplateCard(template) {
  const isHtml = template.kind === "html";

  const textarea = h("textarea", {
    rows: isHtml ? 14 : 2,
    placeholder: "(usando padrão do Keycloak)",
    style: {
      width: "100%",
      fontFamily: isHtml ? "monospace" : "inherit",
      fontSize: "0.85rem",
      resize: "vertical",
    },
  });
  textarea.value = template.value || "";

  const statusBadge = h("span", {
    class: template.override ? "pill pill-success" : "pill pill-neutral",
  }, template.override ? "personalizado" : "padrão");

  const resetBtn = h("button", {
    class: "btn",
    style: { display: template.override ? "" : "none" },
  }, "Restaurar padrão");

  const saveBtn = h("button", { class: "btn btn-primary" }, "Salvar");

  saveBtn.addEventListener("click", async () => {
    saveBtn.disabled = true;
    saveBtn.textContent = "salvando…";

    const r = await apiTry("/admin/settings/email-templates/" + encodeURIComponent(template.key), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ value: textarea.value }),
    });

    saveBtn.disabled = false;
    saveBtn.textContent = "Salvar";

    if (r.ok) {
      toastOk("Template salvo.", template.label);
      statusBadge.textContent = "personalizado";
      statusBadge.className = "pill pill-success";
      resetBtn.style.display = "";
    } else {
      toastBad("HTTP " + r.status + ": " + (r.error?.message || "erro desconhecido"), "Erro ao salvar");
    }
  });

  resetBtn.addEventListener("click", async () => {
    if (!confirm("Restaurar o texto padrão do Keycloak para \"" + template.label + "\"?")) return;
    resetBtn.disabled = true;
    resetBtn.textContent = "restaurando…";

    const r = await apiTry("/admin/settings/email-templates/" + encodeURIComponent(template.key), {
      method: "DELETE",
    });

    resetBtn.disabled = false;
    resetBtn.textContent = "Restaurar padrão";

    if (r.ok) {
      toastOk("Padrão restaurado.", template.label);
      textarea.value = "";
      statusBadge.textContent = "padrão";
      statusBadge.className = "pill pill-neutral";
      resetBtn.style.display = "none";
      updatePreviewEl?.();
    } else {
      toastBad("HTTP " + r.status, "Erro ao restaurar");
    }
  });

  let updatePreviewEl = null;
  const previewSection = isHtml ? renderPreview(textarea, fn => { updatePreviewEl = fn; }) : null;

  const varHint = isHtml
    ? h("p", { class: "muted text-xs", style: { marginTop: "0.25rem" } },
        "Variáveis: ",
        h("code", null, "{0}"),
        " = link, ",
        h("code", null, "{2}"),
        " = nome da plataforma, ",
        h("code", null, "{3}"),
        " = expiração. Não use ${...}, use {0} {2} {3}.",
      )
    : null;

  return card({
    title: template.label,
    actions: [statusBadge],
    body: h("div", { class: "col" },
      h("p", { class: "muted text-xs" }, template.description),
      varHint,
      textarea,
      previewSection,
      h("div", { class: "row", style: { gap: "0.5rem", marginTop: "0.5rem" } },
        saveBtn,
        resetBtn,
      ),
    ),
  });
}

function renderPreview(textarea, registerUpdater) {
  const iframe = h("iframe", {
    sandbox: "allow-same-origin",
    style: {
      width: "100%",
      height: "320px",
      border: "1px solid var(--color-border, #333)",
      borderRadius: "8px",
      background: "#fff",
      marginTop: "0.75rem",
      display: "none",
    },
  });

  function update() {
    let html = textarea.value || "";
    for (const [k, v] of Object.entries(PREVIEW_VARS)) {
      html = html.split(k).join(v);
    }
    iframe.srcdoc = [
      "<style>",
      "  body { font-family: -apple-system, sans-serif; padding: 32px; color: #111; max-width: 600px; margin: 0 auto; line-height: 1.6; }",
      "  a { color: #6366f1; }",
      "  p { margin: 0 0 1rem; }",
      "</style>",
      html,
    ].join("\n");
  }

  registerUpdater(update);
  textarea.addEventListener("input", update);
  update();

  const toggle = h("button", { class: "btn", style: { fontSize: "0.8rem", marginTop: "0.5rem" } }, "▶ Mostrar prévia");
  let open = false;
  toggle.addEventListener("click", () => {
    open = !open;
    iframe.style.display = open ? "" : "none";
    toggle.textContent = open ? "▼ Ocultar prévia" : "▶ Mostrar prévia";
    if (open) update();
  });

  return h("div", null, toggle, iframe);
}
