// state.js — minimal pub/sub store.
//
// Module-singleton: one shared store across the whole app. Subscribers are
// notified on every setState call with the merged state. Views that need to
// re-render on specific keys should subscribe and check `prev !== next` for
// their key of interest.

const _state = {
  config: null,          // /admin/config.json response
  token: null,           // active access token (mirrors sessionStorage)
  identity: null,        // /auth/debug response when authenticated
  theme: "dark",         // 'dark' | 'light'
  route: null,           // current parsed route { path, params }
};

const _subs = new Set();

export function getState() {
  return _state;
}

export function setState(patch) {
  Object.assign(_state, patch);
  for (const fn of _subs) {
    try { fn(_state); } catch (e) { console.error("subscriber error:", e); }
  }
}

export function subscribe(fn) {
  _subs.add(fn);
  return () => _subs.delete(fn);
}

// Convenience: persist + re-broadcast a token from sessionStorage. Used at
// boot and after PKCE callback completion.
export const STORAGE_KEYS = {
  accessToken:  "kc_admin_access_token",
  refreshToken: "kc_admin_refresh_token",
  idToken:      "kc_admin_id_token",
  pkceVerifier: "kc_admin_pkce_verifier",
  oauthState:   "kc_admin_oauth_state",
  theme:        "admin_theme",
};
