// api.js — thin fetch wrapper for the admin console.
//
// Reusable across future projects. Responsibilities:
//   - inject Bearer token from sessionStorage when present
//   - throw a typed APIError on non-2xx so callers can switch on .status
//   - return parsed JSON on success
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

export async function api(path, options = {}) {
  const headers = {
    Accept: "application/json",
    ...(options.headers || {}),
  };
  const token = sessionStorage.getItem(STORAGE_KEYS.accessToken);
  if (token && !headers.Authorization) {
    headers.Authorization = "Bearer " + token;
  }

  const res = await fetch(path, { ...options, headers });
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
  const headers = {
    Accept: "application/json",
    ...(options.headers || {}),
  };
  const token = sessionStorage.getItem(STORAGE_KEYS.accessToken);
  if (token && !headers.Authorization) {
    headers.Authorization = "Bearer " + token;
  }
  try {
    const res = await fetch(path, { ...options, headers });
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
