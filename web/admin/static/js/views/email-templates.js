// email-templates.js — Keycloak realm email message overrides.
// Covers: GET /admin/settings/email-templates,
//         PUT /admin/settings/email-templates/:key,
//         DELETE /admin/settings/email-templates/:key.

import { h, mount } from "../lib/dom.js";
import { apiTry } from "../lib/api.js";
import { pageHeader, card, spinner } from "../components/common.js";
import { toastOk, toastBad } from "../components/toast.js";

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

  renderTemplates(target, r.data?.templates || []);
}

function renderTemplates(target, templates) {
  const cards = templates.map(t => renderTemplateCard(t));

  mount(target,
    h("div", { class: "col", style: { gap: "1rem" } }, ...cards),
  );
}

function renderTemplateCard(template) {
  const isHtml = template.kind === "html";
  const textarea = h("textarea", {
    rows: isHtml ? 10 : 2,
    placeholder: template.override ? "" : "(usando padrão do Keycloak)",
    style: { width: "100%", fontFamily: isHtml ? "monospace" : "inherit", fontSize: "0.85rem", resize: "vertical" },
  });
  textarea.value = template.value || "";

  const statusBadge = h("span", { class: template.override ? "pill pill-success" : "pill pill-neutral" },
    template.override ? "personalizado" : "padrão",
  );

  const saveResult = h("span", "muted text-xs");

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

  const resetBtn = h("button", { class: "btn", style: { display: template.override ? "" : "none" } }, "Restaurar padrão");
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
            "Variáveis disponíveis: ",
            h("code", null, "${link}"),
            ", ",
            h("code", null, "${realmName}"),
            ", ",
            h("code", null, "${user.firstName}"),
            " — não remova as que existirem no template padrão.",
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
