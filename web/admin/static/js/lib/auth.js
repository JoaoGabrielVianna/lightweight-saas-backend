// auth.js — Authorization Code + PKCE flow for the admin console.
//
// Same Keycloak client (saas-dev-playground) and same protocol as the
// /dev/auth playground. Migration path to keycloak-js: replace startLogin /
// completeLogin / refreshToken / logout — every consumer above only sees
// the sessionStorage tokens and the `/auth/debug` response.

import { STORAGE_KEYS, getState, setState } from "./state.js";
import { apiTry } from "./api.js";

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

// ─────────────── Public API ───────────────

export function isAuthenticated() {
  return !!sessionStorage.getItem(STORAGE_KEYS.accessToken);
}

// Idempotency guard for startLogin. A double-click would otherwise interleave
// two async runs: each generates its own (verifier, challenge) pair, the
// second overwrites the verifier in sessionStorage, but the navigation that
// actually wins may carry the FIRST challenge — so the verifier in storage
// no longer hashes to the challenge Keycloak holds, and the subsequent
// /token exchange fails with invalid_grant. Returning the in-flight promise
// makes every concurrent call share a single verifier/challenge/redirect.
let _loginInFlight = null;

export function startLogin() {
  if (_loginInFlight) return _loginInFlight;
  const promise = (async () => {
    const cfg = getState().config;
    if (!cfg) throw new Error("config not loaded");
    const verifier  = randomB64Url(48);
    const challenge = await sha256B64Url(verifier);
    const state     = randomB64Url(16);

    sessionStorage.setItem(STORAGE_KEYS.pkceVerifier, verifier);
    sessionStorage.setItem(STORAGE_KEYS.oauthState,   state);

    const u = new URL(`${cfg.keycloakUrl.replace(/\/$/, "")}/realms/${cfg.realm}/protocol/openid-connect/auth`);
    u.searchParams.set("response_type",         "code");
    u.searchParams.set("client_id",             cfg.clientId);
    u.searchParams.set("redirect_uri",          cfg.redirectUri);
    u.searchParams.set("scope",                 "openid profile email");
    u.searchParams.set("state",                 state);
    u.searchParams.set("code_challenge",        challenge);
    u.searchParams.set("code_challenge_method", "S256");
    window.location.assign(u.toString());
  })();
  _loginInFlight = promise;
  // Clear the guard on rejection so the user can retry after a transient
  // failure (e.g. config not loaded yet). On success we keep the resolved
  // promise: a successful call is followed by a real page navigation, so
  // concurrent clicks should share the same resolved promise and NOT
  // re-run the body. The identity check protects against the unlikely
  // case where a different in-flight promise has already replaced this
  // one — we only null out our own slot.
  promise.catch(() => { if (_loginInFlight === promise) _loginInFlight = null; });
  return promise;
}

// _resetLoginInFlightForTests — test-only helper. Clears the in-flight guard
// so successive test cases can start fresh without leaking state.
export function _resetLoginInFlightForTests() {
  _loginInFlight = null;
}

export async function completeLogin(code, returnedState) {
  const cfg = getState().config;
  const expectedState = sessionStorage.getItem(STORAGE_KEYS.oauthState);
  const verifier      = sessionStorage.getItem(STORAGE_KEYS.pkceVerifier);
  sessionStorage.removeItem(STORAGE_KEYS.oauthState);
  sessionStorage.removeItem(STORAGE_KEYS.pkceVerifier);

  if (!verifier) throw new Error("PKCE verifier missing from sessionStorage");
  if (returnedState !== expectedState) throw new Error("OAuth state mismatch");

  const body = new URLSearchParams();
  body.set("grant_type",    "authorization_code");
  body.set("client_id",     cfg.clientId);
  body.set("code",          code);
  body.set("redirect_uri",  cfg.redirectUri);
  body.set("code_verifier", verifier);

  const tokenUrl = `${cfg.keycloakUrl.replace(/\/$/, "")}/realms/${cfg.realm}/protocol/openid-connect/token`;
  const r = await fetch(tokenUrl, {
    method:  "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body,
  });
  if (!r.ok) throw new Error(`token endpoint ${r.status}: ${await r.text()}`);
  storeTokens(await r.json());
  await refreshDebug();
}

export async function refreshToken() {
  const cfg = getState().config;
  const refresh = sessionStorage.getItem(STORAGE_KEYS.refreshToken);
  if (!refresh) throw new Error("no refresh token in session");

  const body = new URLSearchParams();
  body.set("grant_type",    "refresh_token");
  body.set("client_id",     cfg.clientId);
  body.set("refresh_token", refresh);

  const r = await fetch(
    `${cfg.keycloakUrl.replace(/\/$/, "")}/realms/${cfg.realm}/protocol/openid-connect/token`,
    { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" }, body }
  );
  if (!r.ok) throw new Error(`refresh ${r.status}: ${await r.text()}`);
  storeTokens(await r.json());
  await refreshDebug();
}

export function logout() {
  const cfg = getState().config;
  const idToken = sessionStorage.getItem(STORAGE_KEYS.idToken);
  Object.values(STORAGE_KEYS).forEach((k) => {
    if (k !== STORAGE_KEYS.theme) sessionStorage.removeItem(k);
  });
  setState({ token: null, identity: null });

  const u = new URL(`${cfg.keycloakUrl.replace(/\/$/, "")}/realms/${cfg.realm}/protocol/openid-connect/logout`);
  if (idToken) u.searchParams.set("id_token_hint", idToken);
  u.searchParams.set("post_logout_redirect_uri", cfg.redirectUri);
  window.location.assign(u.toString());
}

function storeTokens(tok) {
  sessionStorage.setItem(STORAGE_KEYS.accessToken, tok.access_token);
  if (tok.refresh_token) sessionStorage.setItem(STORAGE_KEYS.refreshToken, tok.refresh_token);
  if (tok.id_token)      sessionStorage.setItem(STORAGE_KEYS.idToken,      tok.id_token);
  setState({ token: tok.access_token });
}

// refreshDebug pulls /auth/debug and stores it in state — every view that
// renders identity info reads from state, so refreshing this once after
// login is enough.
export async function refreshDebug() {
  if (!isAuthenticated()) {
    setState({ identity: null });
    return null;
  }
  const r = await apiTry("/auth/debug");
  if (r.ok) {
    setState({ identity: r.data });
    return r.data;
  }
  setState({ identity: null });
  return null;
}

// Decodes a JWT payload (cosmetic only — no signature verification). Used by
// the Playground view's "raw decoded JWT" section.
export function decodeJwtAll(token) {
  if (!token) return null;
  const parts = token.split(".");
  if (parts.length !== 3) return null;
  const decode = (seg) => {
    let p = seg.replace(/-/g, "+").replace(/_/g, "/");
    while (p.length % 4) p += "=";
    try { return JSON.parse(atob(p)); } catch { return null; }
  };
  return {
    header:    decode(parts[0]),
    payload:   decode(parts[1]),
    signature: `[${parts[2].length} base64url chars]`,
  };
}

export function maskToken(t) {
  if (!t) return "—";
  if (t.length <= 24) return "…";
  return t.slice(0, 12) + "…" + t.slice(-8);
}
