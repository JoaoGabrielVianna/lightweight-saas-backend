// email-templates.js — Keycloak realm email message overrides with tabbed UI.
// Covers: GET /admin/settings/email-templates,
//         PUT /admin/settings/email-templates/:key,
//         DELETE /admin/settings/email-templates/:key.

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

export default async function emailTemplatesView({ container }) {
  mount(container,
    pageHeader("Templates de Email", "Personalize os textos dos emails enviados pelo Keycloak.", [
      h("span", { class: "pill pill-neutral" }, "realm setting"),
    ]),
    h("div", { id: "et-content" }, h("div", "row", spinner(), h("span", "muted", "carregando…"))),
  );

  const r = await apiTry("/admin/settings/email-templates");
  const target = container.querySelector("#et-content");
  if (!target) return;

  if (!r.ok) {
    mount(target, h("p", "muted", "Falha ao carregar templates: HTTP " + r.status));
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
      h("div", "col", { style: { gap: "1rem" } },
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
    rows: isHtml ? 12 : 2,
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

  const saveResult = h("span", "muted text-xs");

  const resetBtn = h("button", {
    class: "btn",
    style: { display: template.override ? "" : "none" },
  }, "Restaurar padrão");

  const saveBtn = h("button", { class: "btn btn-primary" }, "Salvar");

  saveBtn.addEventListener("click", async () => {
    saveBtn.disabled = true;
    saveBtn.textContent = "salvando…";
    saveResult.textContent = "";

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
    } else {
      toastBad("HTTP " + r.status, "Erro ao restaurar");
    }
  });

  return card({
    title: h("div", "row", { style: { gap: "0.5rem", alignItems: "center" } },
      h("span", null, template.label),
      statusBadge,
    ),
    body: h("div", "col",
      h("p", "muted text-xs", template.description),
      isHtml
        ? h("p", { class: "muted text-xs", style: { marginTop: "0.25rem" } },
            "Variáveis obrigatórias: ",
            h("code", null, "${link}"),
            ", ",
            h("code", null, "${linkExpirationFormatter(linkExpiration,'MINUTES')}"),
            " — não remova.",
          )
        : null,
      textarea,
      h("div", "row", { style: { gap: "0.5rem", marginTop: "0.5rem" } },
        saveBtn,
        resetBtn,
        saveResult,
      ),
    ),
  });
}
