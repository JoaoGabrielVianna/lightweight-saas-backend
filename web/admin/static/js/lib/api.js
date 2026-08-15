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

// ─── Error envelopes ────────────────────────────────────────────────────────
//
// The console now talks to two surfaces with two different error shapes, and
// both stay alive for the whole of Slice 6:
//
//   /admin/*  →  {"error": "not found"}                         (string)
//   /v1/*     →  {"error": {"code","message","request_id"}}      (object)
//
// The pre-Slice-6 constructor did `body.error` and handed the result to
// super(), which for the /v1 shape produced the literal string
// "[object Object]" in every toast and empty state. That is the defect this
// module fixes first, before any view is migrated: a view written against a
// broken error path bakes the breakage into its own error rendering.
//
// parseErrorBody is the single place that knows about either shape. It is
// deliberately total — every input, including malformed JSON, an array, a
// number, null, or an object with an `error` key of an unexpected type,
// returns the same {message, code, requestId} triple with string-or-null
// fields. Nothing else in the console inspects `body.error`.

// asString returns v when it is a non-empty string, otherwise null. This is
// what keeps `[object Object]` structurally impossible: no non-string value
// can reach the Error message or any rendered field.
function asString(v) {
  return typeof v === "string" && v !== "" ? v : null;
}

/**
 * Normalise either error envelope into {message, code, requestId}.
 *
 * @param {*} body parsed response body — object, string, null, or anything
 * @returns {{message: string|null, code: string|null, requestId: string|null}}
 */
export function parseErrorBody(body) {
  const none = { message: null, code: null, requestId: null };

  // Non-JSON response (HTML error page, plain text, empty body). The raw text
  // is the only thing available, and it is a string, so it is safe as a
  // message — but it is not a code and carries no request id.
  if (typeof body === "string") return { ...none, message: asString(body) };
  if (body == null || typeof body !== "object") return none;
  // An array is valid JSON but never an error envelope we emit.
  if (Array.isArray(body)) return none;

  const err = body.error;

  // Legacy /admin/* — {"error": "not found"}.
  if (typeof err === "string") return { ...none, message: asString(err) };

  // Structured /v1 — {"error": {"code","message","request_id"}}.
  if (err && typeof err === "object" && !Array.isArray(err)) {
    return {
      message:   asString(err.message),
      code:      asString(err.code),
      requestId: asString(err.request_id),
    };
  }

  // Neither shape. Some middlewares answer {"message": "..."} and a few
  // OAuth-flavoured endpoints answer {"error_description": "..."}; accepting
  // those costs nothing and keeps an unknown 4xx readable. Anything else
  // degrades to "no message", and the caller falls back to `HTTP <status>`.
  return { ...none, message: asString(body.message) || asString(body.error_description) };
}

/**
 * APIError — one error type for both surfaces.
 *
 * Fields, all of which views may read directly:
 *
 *   .status     HTTP status (0 for a transport failure)
 *   .message    human-readable prose, never "[object Object]", never empty
 *   .code       stable /v1 code (e.g. "workspace_connection_missing"), or null
 *   .requestId  /v1 correlation id, or null
 *   .body       the parsed body, untouched, for debugging
 *
 * `code` is what views branch on. `message` is what they render.
 */
export class APIError extends Error {
  constructor(status, body, message, requestId) {
    const parsed = parseErrorBody(body);
    super(asString(message) || parsed.message || `HTTP ${status}`);
    this.name = "APIError";
    this.status = status;
    this.body = body;
    this.code = parsed.code;
    // The header is the fallback: /v1 echoes X-Request-Id on every response,
    // including ones whose body never reached the error envelope (a panic
    // recovered by gin, a 502 from a proxy in front of us).
    this.requestId = parsed.requestId || asString(requestId);
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
    throw new APIError(res.status, body, null, requestIdOf(res));
  }
  return body;
}

// requestIdOf reads the /v1 correlation header off a response. Tolerates a
// Response-like object without a headers bag (test doubles, and the 401 path
// that never performs a fetch).
function requestIdOf(res) {
  try { return res?.headers?.get?.("X-Request-Id") || null; } catch { return null; }
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
      return {
        ok: false,
        status: res.status,
        data: null,
        error: new APIError(res.status, body, null, requestIdOf(res)),
      };
    }
    return { ok: true, status: res.status, data: body, error: null };
  } catch (e) {
    // A transport failure (offline, DNS, CORS, an aborted request) never
    // reached the server, so there is no envelope to parse. Status 0 is the
    // console's marker for "no HTTP exchange happened".
    return { ok: false, status: 0, data: null, error: e };
  }
}

// ─── Workspace-scoped path construction ─────────────────────────────────────

/**
 * wsPath — the ONE place a `/v1/workspaces/{id}/...` URL is built.
 *
 * Views never interpolate a workspace id into a template string. That rule is
 * what makes "which workspace did this request go to?" answerable by reading
 * one function instead of grepping thirty call sites, and it gives the
 * missing-workspace case a single home.
 *
 *   wsPath("ws_abc", "/users")          → "/v1/workspaces/ws_abc/users"
 *   wsPath("ws_abc", "users")           → "/v1/workspaces/ws_abc/users"
 *   wsPath("ws_abc", "")                → "/v1/workspaces/ws_abc"
 *   wsPath("ws_abc", "/users?max=20")   → "/v1/workspaces/ws_abc/users?max=20"
 *
 * It is deliberately NOT a URL builder. It refuses an absolute URL and refuses
 * a path that already carries the `/v1` prefix, because both mean the caller
 * has built the URL somewhere else and this helper is being used as a
 * rubber stamp.
 *
 * @param {string} workspaceId the public `ws_<uuid>` id
 * @param {string} path        workspace-relative path, with or without a leading slash
 * @returns {string}
 * @throws {Error} when the workspace id is missing or the path is not relative
 */
export function wsPath(workspaceId, path = "") {
  const id = typeof workspaceId === "string" ? workspaceId.trim() : "";
  if (!id) {
    // Thrown, not returned as a degraded URL. A request to
    // "/v1/workspaces//users" would 404 somewhere far from the bug; this
    // fails at the call site that forgot the workspace.
    throw new Error("wsPath: a workspace id is required");
  }
  if (!id.startsWith("ws_")) {
    throw new Error(`wsPath: workspace id must look like ws_<uuid>, got "${id}"`);
  }

  let rel = typeof path === "string" ? path.trim() : "";
  if (/^[a-z][a-z0-9+.-]*:/i.test(rel) || rel.startsWith("//")) {
    throw new Error("wsPath: absolute URLs are not workspace-relative");
  }
  if (rel === "/v1" || rel.startsWith("/v1/")) {
    throw new Error("wsPath: path must be workspace-relative, not a full /v1 path");
  }

  // Collapse leading slashes so both "/users" and "users" produce the same
  // URL, and an accidental "//users" cannot escape to a protocol-relative URL.
  rel = rel.replace(/^\/+/, "");
  const base = "/v1/workspaces/" + encodeURIComponent(id);
  return rel ? base + "/" + rel : base;
}
