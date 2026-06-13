// api.js — thin fetch wrapper for the admin console.
//
// Reusable across future projects. Responsibilities:
//   - inject Bearer token from sessionStorage when present
//   - throw a typed APIError on non-2xx so callers can switch on .status
//   - return parsed JSON on success
//   - on 401: attempt one silent token refresh, then retry; redirect to login
//     if the refresh itself fails (handlers registered by auth.js via
//     setAuthHandlers to avoid a circular import cycle)
//
// As of Stage 5.2C/D the console DOES mutate via this layer (PATCH/DELETE/
// POST). The header doc above used to claim GETs only — kept for history
// in git; do not re-introduce that restriction.

import { STORAGE_KEYS } from "./state.js";

export class APIError extends Error {
  constructor(status, body, message) {
    super(message || (typeof body === "object" ? body.error : body) || `HTTP ${status}`);
    this.status = status;
    this.body = body;
  }
}

// Handlers registered by auth.js at module-init time to break the circular-
// import cycle (auth.js already imports api.js, so api.js must not import
// auth.js in return).
let _onRefresh  = null; // () => Promise<void>  — exchanges refresh token for new access token
let _onAuthFail = null; // () => void           — called when refresh itself fails; should redirect to login

export function setAuthHandlers({ onRefresh, onAuthFail }) {
  _onRefresh  = onRefresh;
  _onAuthFail = onAuthFail;
}

// Single shared in-flight promise so concurrent 401s don't fire multiple
// refresh requests to Keycloak simultaneously.
let _refreshInFlight = null;

async function attemptRefresh() {
  if (!_onRefresh) return false;
  if (!_refreshInFlight) {
    _refreshInFlight = _onRefresh().finally(() => { _refreshInFlight = null; });
  }
  try {
    await _refreshInFlight;
    return true;
  } catch {
    return false;
  }
}

// Build headers for each attempt so the refreshed token is picked up on retry.
function buildHeaders(options) {
  const headers = { Accept: "application/json", ...(options.headers || {}) };
  const token = sessionStorage.getItem(STORAGE_KEYS.accessToken);
  if (token && !headers.Authorization) headers.Authorization = "Bearer " + token;
  return headers;
}

export async function api(path, options = {}) {
  let res = await fetch(path, { ...options, headers: buildHeaders(options) });

  if (res.status === 401) {
    const refreshed = await attemptRefresh();
    if (refreshed) {
      res = await fetch(path, { ...options, headers: buildHeaders(options) });
    } else {
      if (_onAuthFail) _onAuthFail();
      throw new APIError(401, "session expired");
    }
  }

  const text = await res.text();
  let body = text;
  try { body = text ? JSON.parse(text) : null; } catch { /* keep as text */ }

  if (!res.ok) {
    throw new APIError(res.status, body);
  }
  return body;
}

// apiTry: convenience for views that want to handle missing-auth / 403 /
// 404 / 502 differently. Returns { ok, status, data, error }.
//
// Implemented as a direct fetch (rather than wrapping api()) so we can
// surface the REAL HTTP status on success — important for distinguishing
// 200 (PATCH/GET) from 201 (POST CREATE) from 204 (POST/DELETE no-content)
// in the API Explorer and for callers that check status === 204.
export async function apiTry(path, options = {}) {
  try {
    let res = await fetch(path, { ...options, headers: buildHeaders(options) });

    if (res.status === 401) {
      const refreshed = await attemptRefresh();
      if (refreshed) {
        res = await fetch(path, { ...options, headers: buildHeaders(options) });
      } else {
        if (_onAuthFail) _onAuthFail();
        return { ok: false, status: 401, data: null, error: new APIError(401, "session expired") };
      }
    }

    const text = await res.text();
    let body = text;
    try { body = text ? JSON.parse(text) : null; } catch { /* keep as text */ }
    if (!res.ok) {
      return { ok: false, status: res.status, data: null, error: new APIError(res.status, body) };
    }
    return { ok: true, status: res.status, data: body, error: null };
  } catch (e) {
    return { ok: false, status: 0, data: null, error: e };
  }
}
