/**
 * auth-console — unified DEV-ONLY developer console.
 *
 * Sections:
 *   1. Connection status (auto-refreshes every 10s)
 *   2. Authentication   (PKCE login/logout/refresh, expiry countdown)
 *   3. Token introspection — driven entirely by /auth/debug
 *   4. Debugging — human "why" explanation when /auth/debug.valid=false
 *   5. API testing — /me, /health (+ placeholders)
 *   6. Raw payloads — collapsible JSON dumps
 *
 * Source-of-truth rule: this UI MUST NOT decide on its own whether a token
 * is valid. Every `valid`, `expired`, `roles` value rendered to the user
 * comes from /auth/debug. The local JWT decode in section 6 is cosmetic
 * only — it shows what's in the token without judging it.
 *
 * Migration path: swap the PKCE helpers for keycloak-js by replacing
 * startLogin/completeLogin/refreshToken/logout. Everything below the
 * sessionStorage layer would still work.
 */

(() => {
  "use strict";

  // ─────────────── Session storage keys ───────────────
  const KS = {
    accessToken:  "kc_dev_access_token",
    refreshToken: "kc_dev_refresh_token",
    idToken:      "kc_dev_id_token",
    pkceVerifier: "kc_dev_pkce_verifier",
    oauthState:   "kc_dev_oauth_state",
  };

  // ─────────────── DOM helpers ───────────────
  const $ = (id) => document.getElementById(id);
  const setText = (id, v) => { const el = $(id); if (el) el.textContent = v ?? "—"; };
  const show = (id, visible = true) => { const el = $(id); if (el) el.hidden = !visible; };

  function escapeHTML(s) {
    return String(s ?? "")
      .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;").replace(/'/g, "&#39;");
  }

  // ─────────────── State ───────────────
  let CONFIG = null;
  let DEBUG_RESPONSE = null;
  let countdownTimer = null;
  let diagTimer = null;

  // ─────────────── PKCE primitives ───────────────

  function base64UrlEncode(bytesOrStr) {
    let bytes = bytesOrStr instanceof Uint8Array
      ? bytesOrStr
      : new TextEncoder().encode(String(bytesOrStr));
    if (bytesOrStr instanceof ArrayBuffer) bytes = new Uint8Array(bytesOrStr);
    let bin = "";
    for (const b of bytes) bin += String.fromCharCode(b);
    return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  function randomB64Url(byteLen) {
    const buf = new Uint8Array(byteLen);
    crypto.getRandomValues(buf);
    return base64UrlEncode(buf);
  }

  async function sha256B64Url(input) {
    const buf = new TextEncoder().encode(input);
    const digest = await crypto.subtle.digest("SHA-256", buf);
    return base64UrlEncode(digest);
  }

  // ─────────────── Local JWT decode (cosmetic only) ───────────────

  function decodeJwtSegment(seg) {
    let p = (seg || "").replace(/-/g, "+").replace(/_/g, "/");
    while (p.length % 4) p += "=";
    try { return JSON.parse(atob(p)); } catch { return null; }
  }

  function decodeJwtAll(token) {
    if (!token) return null;
    const parts = token.split(".");
    if (parts.length !== 3) return null;
    return {
      header:    decodeJwtSegment(parts[0]),
      payload:   decodeJwtSegment(parts[1]),
      signature: `[${parts[2].length} base64url chars]`,
    };
  }

  function maskToken(t) {
    if (!t) return "—";
    if (t.length <= 24) return "…";
    return t.slice(0, 12) + "…" + t.slice(-8);
  }

  // ─────────────── Config ───────────────

  async function loadConfig() {
    const r = await fetch("/dev/auth/config.json", { cache: "no-store" });
    if (!r.ok) throw new Error(`config.json ${r.status}`);
    CONFIG = await r.json();
    setText("meta-realm",    CONFIG.realm);
    setText("meta-client",   CONFIG.clientId);
    setText("meta-redirect", CONFIG.redirectUri);
  }

  // ─────────────── Diagnostics (Section 1) ───────────────

  function setDiag(id, state, note) {
    const li = document.querySelector(`.diag li[data-id="${id}"]`);
    if (!li) return;
    li.querySelector(".dot").className = "dot dot-" + state;
    li.querySelector("[data-note]").textContent = note || "";
  }

  async function runDiagnostics() {
    setDiag("api",  "pending", "checking…");
    setDiag("kc",   "pending", "checking…");
    setDiag("oidc", "pending", "checking…");

    // API /health
    try {
      const r = await fetch("/health", { cache: "no-store" });
      setDiag("api", r.ok ? "ok" : "bad", `HTTP ${r.status}`);
    } catch (e) {
      setDiag("api", "bad", "unreachable");
    }

    if (!CONFIG?.keycloakUrl) {
      setDiag("kc", "bad", "config missing");
      setDiag("oidc", "bad", "config missing");
      return;
    }

    // Keycloak base — hit the bare realm URL; 200 with HTML is fine.
    const baseUrl = `${CONFIG.keycloakUrl.replace(/\/$/, "")}/realms/${CONFIG.realm}`;
    try {
      const r = await fetch(baseUrl, { cache: "no-store" });
      setDiag("kc", r.ok ? "ok" : "bad", `HTTP ${r.status}`);
    } catch (e) {
      setDiag("kc", "bad", "unreachable");
    }

    // OIDC discovery
    try {
      const r = await fetch(`${baseUrl}/.well-known/openid-configuration`, { cache: "no-store" });
      setDiag("oidc", r.ok ? "ok" : "bad", `HTTP ${r.status}`);
    } catch (e) {
      setDiag("oidc", "bad", "unreachable");
    }
  }

  function startDiagAutoRefresh() {
    if (diagTimer) clearInterval(diagTimer);
    diagTimer = setInterval(runDiagnostics, 10000);
  }

  // ─────────────── PKCE login (Section 2) ───────────────

  async function startLogin() {
    if (!CONFIG) return;
    const verifier  = randomB64Url(48);
    const challenge = await sha256B64Url(verifier);
    const state     = randomB64Url(16);

    sessionStorage.setItem(KS.pkceVerifier, verifier);
    sessionStorage.setItem(KS.oauthState,   state);

    const u = new URL(`${CONFIG.keycloakUrl.replace(/\/$/, "")}/realms/${CONFIG.realm}/protocol/openid-connect/auth`);
    u.searchParams.set("response_type",         "code");
    u.searchParams.set("client_id",             CONFIG.clientId);
    u.searchParams.set("redirect_uri",          CONFIG.redirectUri);
    u.searchParams.set("scope",                 "openid profile email");
    u.searchParams.set("state",                 state);
    u.searchParams.set("code_challenge",        challenge);
    u.searchParams.set("code_challenge_method", "S256");
    window.location.assign(u.toString());
  }

  async function completeLogin(code, returnedState) {
    const expectedState = sessionStorage.getItem(KS.oauthState);
    const verifier      = sessionStorage.getItem(KS.pkceVerifier);
    sessionStorage.removeItem(KS.oauthState);
    sessionStorage.removeItem(KS.pkceVerifier);

    if (!verifier) throw new Error("PKCE verifier missing");
    if (returnedState !== expectedState) throw new Error("OAuth state mismatch");

    const body = new URLSearchParams();
    body.set("grant_type",    "authorization_code");
    body.set("client_id",     CONFIG.clientId);
    body.set("code",          code);
    body.set("redirect_uri",  CONFIG.redirectUri);
    body.set("code_verifier", verifier);

    const tokenUrl = `${CONFIG.keycloakUrl.replace(/\/$/, "")}/realms/${CONFIG.realm}/protocol/openid-connect/token`;
    const r = await fetch(tokenUrl, {
      method:  "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body,
    });
    if (!r.ok) throw new Error(`token endpoint ${r.status}: ${await r.text()}`);
    storeTokens(await r.json());
  }

  function storeTokens(tok) {
    sessionStorage.setItem(KS.accessToken, tok.access_token);
    if (tok.refresh_token) sessionStorage.setItem(KS.refreshToken, tok.refresh_token);
    if (tok.id_token)      sessionStorage.setItem(KS.idToken,      tok.id_token);
  }

  async function refreshToken() {
    const refresh = sessionStorage.getItem(KS.refreshToken);
    if (!refresh) throw new Error("no refresh token in session");

    const body = new URLSearchParams();
    body.set("grant_type",    "refresh_token");
    body.set("client_id",     CONFIG.clientId);
    body.set("refresh_token", refresh);

    const r = await fetch(
      `${CONFIG.keycloakUrl.replace(/\/$/, "")}/realms/${CONFIG.realm}/protocol/openid-connect/token`,
      { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" }, body }
    );
    if (!r.ok) throw new Error(`refresh ${r.status}: ${await r.text()}`);
    storeTokens(await r.json());
  }

  function logout() {
    const idToken = sessionStorage.getItem(KS.idToken);
    Object.values(KS).forEach((k) => sessionStorage.removeItem(k));

    const u = new URL(`${CONFIG.keycloakUrl.replace(/\/$/, "")}/realms/${CONFIG.realm}/protocol/openid-connect/logout`);
    if (idToken) u.searchParams.set("id_token_hint", idToken);
    u.searchParams.set("post_logout_redirect_uri", CONFIG.redirectUri);
    window.location.assign(u.toString());
  }

  // ─────────────── /auth/debug consumer (Sections 3, 4) ───────────────
  // The single source of truth for token validity.

  async function refreshDebug() {
    const token = sessionStorage.getItem(KS.accessToken);
    if (!token) {
      DEBUG_RESPONSE = null;
      show("card-introspect", false);
      show("card-debug",      false);
      setText("meta-azp", "—");
      return;
    }

    const r = await fetch("/auth/debug", { headers: { Authorization: "Bearer " + token } });
    DEBUG_RESPONSE = await r.json();
    renderIntrospection(DEBUG_RESPONSE);
    renderDebugging(DEBUG_RESPONSE);
    renderRawDebug();
    setText("meta-azp", DEBUG_RESPONSE.received_azp || "—");
  }

  function renderIntrospection(d) {
    setText("d-iss",   d.issuer);
    setText("d-azp",   d.received_azp || "—");
    setText("d-sub",   d.received_sub || "—");
    setText("d-email", d.email || "—");
    setText("d-roles", (d.roles && d.roles.length) ? d.roles.join(", ") : "—");
    setText("d-aud",   (d.aud   && d.aud.length)   ? d.aud.join(", ")   : "—");
    setText("d-iat",   d.iat || "—");
    setText("d-exp",   d.exp || "—");
    setText("d-allowed", (d.allowed_clients || []).join(", ") || "—");

    const validPill = $("d-valid");
    validPill.textContent = d.valid ? "valid" : "invalid";
    validPill.className   = "pill " + (d.valid ? "ok" : "bad");

    const expPill = $("d-expired");
    if (d.exp) {
      expPill.textContent = d.expired ? "expired" : "live";
      expPill.className   = "pill " + (d.expired ? "bad" : "ok");
    } else {
      expPill.textContent = "no exp claim";
      expPill.className   = "pill warn";
    }

    show("card-introspect", true);
  }

  function renderDebugging(d) {
    // Hide the debug card on success or when there's no token at all.
    if (d.valid || !d.reason || d.reason.startsWith("no token supplied")) {
      show("card-debug", false);
      return;
    }
    $("d-explanation").innerHTML = explainReason(d);
    setText("d-reason-raw", d.reason);
    show("card-debug", true);
  }

  /**
   * Map a /auth/debug.reason string to a short human "why + fix" block.
   * This is purely translation — no new validation, no claim re-reading.
   * If the reason text changes server-side, the default case still renders
   * the raw provider message; we never silently swallow.
   */
  function explainReason(d) {
    const r = (d.reason || "").toLowerCase();

    if (r.includes("token is expired") || r.includes("token expired") || d.expired) {
      return `
        <p class="severity bad">token expired</p>
        <p>The token's <code>exp</code> claim (<code>${escapeHTML(d.exp || "?")}</code>) is in the past.</p>
        <p>Fix: click <strong>Refresh token</strong> if available, otherwise <strong>Logout</strong> then <strong>Login</strong>.</p>
      `;
    }
    if (r.includes("azp") && r.includes("allowed-client-set")) {
      const allowed = (d.allowed_clients || []).map(c => `<li><code>${escapeHTML(c)}</code></li>`).join("");
      return `
        <p class="severity bad">authorizing client not in whitelist</p>
        <p>Token was minted by <code>${escapeHTML(d.received_azp || "(unknown)")}</code> but this API only accepts tokens from:</p>
        <ul>${allowed}</ul>
        <p>Fix: re-issue the token from one of the allowed clients, or add <code>${escapeHTML(d.received_azp)}</code> to <code>KEYCLOAK_ALLOWED_CLIENT_IDS</code> if it's legitimate.</p>
      `;
    }
    if (r.includes("invalid issuer")) {
      return `
        <p class="severity bad">issuer mismatch</p>
        <p>Token's <code>iss</code> claim doesn't match what this API expects.</p>
        <p>API expects: <code>${escapeHTML(d.issuer)}</code></p>
        <p>Fix: tokens must be minted at the same URL the API has in <code>KEYCLOAK_URL</code>. Common cause: split docker/host URLs (token fetched at <code>localhost:8081</code>, API configured with <code>keycloak:8080</code>).</p>
      `;
    }
    if (r.includes("missing required claim: sub")) {
      return `
        <p class="severity bad">missing sub claim</p>
        <p>The token has no <code>sub</code> claim. Keycloak's <code>basic</code> client scope is what attaches it.</p>
        <p>Fix: Keycloak admin UI → Clients → ${escapeHTML(CONFIG?.clientId || "<client>")} → Client scopes → ensure <code>basic</code> is in Default.</p>
      `;
    }
    if (r.includes("signature") || r.includes("verification")) {
      return `
        <p class="severity bad">signature verification failed</p>
        <p>Token signature doesn't match the realm's public keys. Possible causes:</p>
        <ul>
          <li>Token was tampered with after issuance</li>
          <li>Keycloak signing keys rotated since the token was minted (any <code>make realm-reset</code> regenerates keys)</li>
          <li>Token was minted against a different realm</li>
        </ul>
        <p>Fix: log out and log back in to get a token signed with the current keys.</p>
      `;
    }
    if (r.includes("not a jwt") || r.includes("malformed") || r.includes("number of segments")) {
      return `
        <p class="severity bad">malformed token</p>
        <p>The bearer string isn't a valid JWT (three base64url segments separated by dots).</p>
      `;
    }
    if (r.startsWith("no token supplied")) {
      return `<p class="muted">no token supplied — log in to populate this section</p>`;
    }
    // Fallback: surface the raw provider message verbatim. Better to be
    // honest than to invent guidance for a message we don't recognise.
    return `
      <p class="severity bad">token rejected</p>
      <p>Provider message: <code>${escapeHTML(d.reason)}</code></p>
    `;
  }

  // ─────────────── Auth state rendering (Section 2) ───────────────

  function renderAuthState() {
    const token = sessionStorage.getItem(KS.accessToken);
    const authed = !!token;

    const pill = $("auth-pill");
    pill.textContent = authed ? "authenticated" : "unauthenticated";
    pill.className   = authed ? "pill ok" : "pill neutral";

    show("user-info",    authed);
    show("btn-login",    !authed);
    show("btn-refresh",  authed && !!sessionStorage.getItem(KS.refreshToken));
    show("btn-logout",   authed);
    show("btn-copy",     authed);

    if (authed) {
      // Pull user-info from the debug response when we have it, otherwise
      // fall back to local decode (purely cosmetic).
      const d = DEBUG_RESPONSE;
      const local = decodeJwtAll(token);
      const claims = d ? d : (local?.payload || {});
      setText("user-email", d?.email || claims.email || "—");
      setText("user-sub",   d?.received_sub || claims.sub || "—");
      const roles = d?.roles || (claims.realm_access?.roles ?? []);
      setText("user-roles", roles.length ? roles.join(", ") : "—");
      startCountdown();
    } else {
      stopCountdown();
      setText("user-email", "—");
      setText("user-sub", "—");
      setText("user-roles", "—");
      setText("user-countdown", "—");
    }

    renderRawJwt();
  }

  // ─────────────── Countdown (Section 2) ───────────────

  function startCountdown() {
    stopCountdown();
    tickCountdown();
    countdownTimer = setInterval(tickCountdown, 1000);
  }
  function stopCountdown() {
    if (countdownTimer) clearInterval(countdownTimer);
    countdownTimer = null;
  }
  function tickCountdown() {
    const exp = DEBUG_RESPONSE?.exp;
    if (!exp) { setText("user-countdown", "—"); return; }
    const ms = new Date(exp).getTime() - Date.now();
    setText("user-countdown", ms > 0 ? formatRemaining(ms) : "expired");
  }
  function formatRemaining(ms) {
    const s = Math.floor(ms / 1000);
    const h = Math.floor(s / 3600);
    const m = Math.floor((s % 3600) / 60);
    const r = s % 60;
    if (h) return `${h}h ${m}m ${r}s`;
    if (m) return `${m}m ${r}s`;
    return `${r}s`;
  }

  // ─────────────── API testing (Section 5) ───────────────

  async function callApi(label, path, withAuth) {
    setText("api-meta", `${label} → …`);
    setText("api-out",  "");
    const t0 = performance.now();
    try {
      const headers = {};
      if (withAuth) {
        const token = sessionStorage.getItem(KS.accessToken);
        if (token) headers.Authorization = "Bearer " + token;
      }
      const r = await fetch(path, { headers });
      const ms = Math.round(performance.now() - t0);
      const body = await r.text();
      setText("api-meta", `${label} → HTTP ${r.status}  ·  ${ms} ms`);
      let pretty = body;
      try { pretty = JSON.stringify(JSON.parse(body), null, 2); } catch {}
      setText("api-out", pretty || "(empty)");
      if (path === "/me") {
        setText("raw-me", pretty || body || "—");
      }
    } catch (e) {
      setText("api-meta", `${label} → fetch failed`);
      setText("api-out", String(e));
    }
  }

  // ─────────────── Raw panels (Section 6) ───────────────

  function renderRawJwt() {
    const token = sessionStorage.getItem(KS.accessToken);
    const decoded = decodeJwtAll(token);
    setText("raw-jwt", decoded ? JSON.stringify(decoded, null, 2) : "—");
  }
  function renderRawDebug() {
    setText("raw-debug", DEBUG_RESPONSE ? JSON.stringify(DEBUG_RESPONSE, null, 2) : "—");
  }

  // ─────────────── Wire events ───────────────

  function showError(msg) {
    const el = $("auth-error");
    el.textContent = msg;
    el.hidden = false;
  }
  function clearError() { $("auth-error").hidden = true; }

  function wireEvents() {
    $("btn-login").addEventListener("click", () => {
      clearError();
      startLogin().catch(e => showError("login: " + e.message));
    });

    $("btn-refresh").addEventListener("click", async () => {
      clearError();
      try {
        await refreshToken();
        await refreshDebug();
        renderAuthState();
      } catch (e) {
        showError("refresh: " + e.message);
      }
    });

    $("btn-logout").addEventListener("click", () => {
      clearError();
      logout();
    });

    $("btn-copy").addEventListener("click", async () => {
      const t = sessionStorage.getItem(KS.accessToken);
      if (!t) return;
      try {
        await navigator.clipboard.writeText(t);
        const c = $("copy-confirm");
        c.hidden = false;
        setTimeout(() => { c.hidden = true; }, 1500);
      } catch (e) {
        showError("copy: " + e.message);
      }
    });

    $("btn-me").addEventListener("click",     () => callApi("GET /me",     "/me",     true));
    $("btn-health").addEventListener("click", () => callApi("GET /health", "/health", false));

    $("btn-refresh-diag").addEventListener("click",  runDiagnostics);
    $("btn-refresh-debug").addEventListener("click", refreshDebug);

    // Delegated copy buttons on raw panels.
    document.addEventListener("click", async (ev) => {
      const target = ev.target;
      if (!(target instanceof HTMLElement)) return;
      const id = target.getAttribute("data-copy");
      if (!id) return;
      ev.preventDefault();
      const pre = $(id);
      if (!pre) return;
      try {
        await navigator.clipboard.writeText(pre.textContent || "");
        const orig = target.textContent;
        target.textContent = "copied";
        setTimeout(() => { target.textContent = orig; }, 1200);
      } catch (e) {
        target.textContent = "failed";
      }
    });
  }

  // ─────────────── Boot ───────────────

  async function main() {
    try {
      await loadConfig();
    } catch (e) {
      showError("could not load /dev/auth/config.json: " + e.message);
      return;
    }

    wireEvents();
    runDiagnostics();
    startDiagAutoRefresh();

    // PKCE callback handling
    const url   = new URL(window.location.href);
    const code  = url.searchParams.get("code");
    const state = url.searchParams.get("state");
    const err   = url.searchParams.get("error");
    if (err) {
      showError(`Keycloak: ${err}${url.searchParams.get("error_description") ? " — " + url.searchParams.get("error_description") : ""}`);
      history.replaceState(null, "", CONFIG.redirectUri);
    } else if (code) {
      try {
        await completeLogin(code, state);
      } catch (e) {
        showError("token exchange: " + e.message);
      } finally {
        history.replaceState(null, "", CONFIG.redirectUri);
      }
    }

    // After any login/no-login flow, fetch the debug state then render auth UI.
    await refreshDebug();
    renderAuthState();
  }

  document.addEventListener("DOMContentLoaded", main);
})();
