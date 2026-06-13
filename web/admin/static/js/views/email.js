// email.js — SMTP configuration for the Keycloak realm.
// Covers: GET/PUT /admin/settings/smtp, POST /admin/settings/smtp/test.

import { h, mount } from "../lib/dom.js";
import { apiTry } from "../lib/api.js";
import { pageHeader, card, spinner } from "../components/common.js";
import { toastOk, toastBad } from "../components/toast.js";

// Known provider presets. Matched against the host field as the operator types.
const PROVIDERS = [
  { name: "Gmail",      match: "smtp.gmail.com",           host: "smtp.gmail.com",       port: "587", ssl: "false", starttls: "true",  auth: "true",  hint: "Use an App Password, not your account password." },
  { name: "Zoho Mail",  match: "smtp.zoho",                host: "smtp.zoho.com",         port: "587", ssl: "false", starttls: "true",  auth: "true",  hint: "Generate a per-app password in Zoho Account Settings." },
  { name: "SendGrid",   match: "smtp.sendgrid",            host: "smtp.sendgrid.net",     port: "587", ssl: "false", starttls: "true",  auth: "true",  hint: "User is always 'apikey'; password is your SendGrid API key." },
  { name: "Brevo",      match: "smtp-relay.brevo",         host: "smtp-relay.brevo.com",  port: "587", ssl: "false", starttls: "true",  auth: "true",  hint: "User is your Brevo login; password is your SMTP key." },
  { name: "Mailgun",    match: "smtp.mailgun",             host: "smtp.mailgun.org",      port: "587", ssl: "false", starttls: "true",  auth: "true",  hint: "User is 'postmaster@yourdomain'; password from Mailgun dashboard." },
  { name: "AWS SES",    match: "email-smtp.us",            host: "email-smtp.us-east-1.amazonaws.com", port: "587", ssl: "false", starttls: "true", auth: "true", hint: "Create SMTP credentials in the SES console, not IAM access keys." },
  { name: "Resend",     match: "smtp.resend",              host: "smtp.resend.com",       port: "465", ssl: "true",  starttls: "false", auth: "true",  hint: "User is 'resend'; password is your Resend API key." },
  { name: "Office 365", match: "smtp.office365",           host: "smtp.office365.com",    port: "587", ssl: "false", starttls: "true",  auth: "true",  hint: "Use your Microsoft 365 email and password (or app password if MFA enabled)." },
  { name: "Outlook",    match: "smtp-mail.outlook",        host: "smtp-mail.outlook.com", port: "587", ssl: "false", starttls: "true",  auth: "true",  hint: "Use your Outlook.com email and password." },
];

function detectProvider(host) {
  if (!host) return null;
  const h = host.toLowerCase();
  return PROVIDERS.find(p => h.includes(p.match.toLowerCase())) || null;
}

export default async function emailView({ container }) {
  mount(container,
    pageHeader("Email / SMTP", "Configure the SMTP server Keycloak uses to send invitation and password-reset emails.", [
      h("span", { class: "pill pill-neutral" }, "realm setting"),
    ]),
    h("div", { id: "email-content" }, h("div", "row", spinner(), h("span", "muted", "loading…"))),
  );

  const r = await apiTry("/admin/settings/smtp");
  const target = container.querySelector("#email-content");
  if (!target) return;

  if (!r.ok) {
    mount(target, h("p", "muted", "Failed to load SMTP config: HTTP " + r.status));
    return;
  }

  renderSMTPForm(target, r.data?.smtp || {});
}

function renderSMTPForm(target, initial) {
  // ── form fields ────────────────────────────────────────────────────────
  const hostEl     = h("input", { type: "text",     placeholder: "smtp.example.com", value: initial.host     || "" });
  const portEl     = h("input", { type: "text",     placeholder: "587",              value: initial.port     || "" });
  const fromEl     = h("input", { type: "email",    placeholder: "noreply@example.com", value: initial.from  || "" });
  const fromNameEl = h("input", { type: "text",     placeholder: "Corsi Enterprise", value: initial.fromDisplayName || "" });
  const userEl     = h("input", { type: "text",     placeholder: "smtp-user",        value: initial.user     || "",    autocomplete: "off" });
  const passEl     = h("input", { type: "password", placeholder: initial.password ? "••••••••" : "smtp-password", value: initial.password || "", autocomplete: "new-password" });
  const replyToEl  = h("input", { type: "email",    placeholder: "(optional)",       value: initial.replyTo  || "" });

  const sslEl      = h("input", { type: "checkbox", id: "smtp-ssl",      checked: initial.ssl      === "true" });
  const starttlsEl = h("input", { type: "checkbox", id: "smtp-starttls", checked: initial.starttls !== "false" && initial.ssl !== "true" });
  const authEl     = h("input", { type: "checkbox", id: "smtp-auth",     checked: initial.auth !== "false" });

  // SSL and STARTTLS are mutually exclusive
  sslEl.addEventListener("change", () => { if (sslEl.checked) starttlsEl.checked = false; });
  starttlsEl.addEventListener("change", () => { if (starttlsEl.checked) sslEl.checked = false; });

  // ── provider hint banner ───────────────────────────────────────────────
  const hintBanner = h("div", { style: { display: "none" }, class: "card" });

  function updateHint() {
    const p = detectProvider(hostEl.value);
    if (p) {
      hintBanner.style.display = "";
      hintBanner.textContent = "";
      hintBanner.appendChild(h("span", { style: { fontWeight: "600" } }, p.name + " detected — "));
      hintBanner.appendChild(document.createTextNode(p.hint));
    } else {
      hintBanner.style.display = "none";
    }
  }

  // ── auto-fill on host blur ─────────────────────────────────────────────
  hostEl.addEventListener("input", updateHint);
  hostEl.addEventListener("change", () => {
    const p = detectProvider(hostEl.value);
    if (!p) return;
    if (!portEl.value)           portEl.value     = p.port;
    sslEl.checked                                 = p.ssl === "true";
    starttlsEl.checked                            = p.starttls === "true";
    authEl.checked                                = p.auth === "true";
  });

  // ── test button ────────────────────────────────────────────────────────
  const testResult = h("span", "muted text-xs");
  const testBtn    = h("button", { class: "btn" }, "Test connection");
  testBtn.addEventListener("click", async () => {
    testBtn.disabled = true;
    testBtn.textContent = "testing…";
    testResult.textContent = "";
    const r = await apiTry("/admin/settings/smtp/test", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(collectConfig()),
    });
    testBtn.disabled = false;
    testBtn.textContent = "Test connection";
    if (!r.ok) {
      testResult.textContent = "Request failed: HTTP " + r.status;
      return;
    }
    if (r.data?.ok) {
      testResult.textContent = "✓ Connected successfully";
      testResult.style.color = "var(--color-ok, #4ade80)";
    } else {
      testResult.textContent = "✗ " + (r.data?.error || "connection refused");
      testResult.style.color = "var(--color-bad, #f87171)";
    }
  });

  // ── save button ────────────────────────────────────────────────────────
  const saveBtn = h("button", { class: "btn btn-primary" }, "Save");
  saveBtn.addEventListener("click", async () => {
    if (!fromEl.value.trim()) { toastBad("From address is required.", "Missing field"); return; }
    saveBtn.disabled = true;
    saveBtn.textContent = "saving…";
    const r = await apiTry("/admin/settings/smtp", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(collectConfig()),
    });
    saveBtn.disabled = false;
    saveBtn.textContent = "Save";
    if (r.ok) {
      toastOk("SMTP configuration saved.", "Saved");
    } else {
      toastBad("HTTP " + r.status + ": " + (r.error?.message || "unknown"), "Save failed");
    }
  });

  function collectConfig() {
    const p = passEl.value;
    return {
      host:            hostEl.value.trim(),
      port:            portEl.value.trim() || "587",
      from:            fromEl.value.trim(),
      fromDisplayName: fromNameEl.value.trim(),
      user:            userEl.value.trim(),
      password:        p || "••••••••",
      replyTo:         replyToEl.value.trim(),
      ssl:             sslEl.checked      ? "true" : "false",
      starttls:        starttlsEl.checked ? "true" : "false",
      auth:            authEl.checked     ? "true" : "false",
    };
  }

  // ── layout ─────────────────────────────────────────────────────────────
  mount(target,
    hintBanner,

    card({
      title: "Server",
      body: h("div", "col",
        h("div", "row", { style: { gap: "1rem" } },
          h("label", { style: { flex: "2" } }, h("div", "muted", "SMTP host *"), hostEl),
          h("label", { style: { flex: "1" } }, h("div", "muted", "Port"),         portEl),
        ),
        h("div", "row",
          h("label", null, sslEl,      h("span", null, " Implicit TLS (SSL, port 465)")),
          h("label", null, starttlsEl, h("span", null, " STARTTLS (port 587)")),
        ),
      ),
    }),

    card({
      title: "Sender",
      body: h("div", "col",
        h("label", null, h("div", "muted", "From address *"), fromEl),
        h("label", null, h("div", "muted", "Display name"),   fromNameEl),
        h("label", null, h("div", "muted", "Reply-to (optional)"), replyToEl),
      ),
    }),

    card({
      title: "Authentication",
      body: h("div", "col",
        h("label", null, authEl, h("span", null, " Require SMTP authentication")),
        h("div", "row", { style: { gap: "1rem" } },
          h("label", { style: { flex: "1" } }, h("div", "muted", "Username"), userEl),
          h("label", { style: { flex: "1" } }, h("div", "muted", "Password"), passEl),
        ),
        h("p", "muted text-xs", "Password is write-only — it is never echoed back after saving."),
      ),
    }),

    card({
      title: "Test & Save",
      body: h("div", "col",
        h("p", "muted text-xs", "Test opens a connection and optionally authenticates — no email is sent."),
        h("div", "row",
          testBtn,
          testResult,
        ),
        h("div", "row", { style: { marginTop: "1rem" } },
          saveBtn,
        ),
      ),
    }),
  );

  updateHint();
}
