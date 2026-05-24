// state.js — minimal pub/sub store.
//
// Module-singleton: one shared store across the whole app. Subscribers are
// notified on every setState call with the merged state. Views that need to
// re-render on specific keys should subscribe and check `prev !== next` for
// their key of interest.
//
// Iteration safety (added 2026-05-22 after the Docs-UI freeze investigation):
//
//   1. Subscribers are snapshotted with Array.from before each notification
//      cycle. New subscribers added during the cycle do NOT fire in that
//      cycle — they get the next setState. Without this, a subscriber that
//      re-registered itself synchronously caused the `for…of` over the Set
//      to visit the freshly-added entry, which re-registered, which fired
//      again — an unbounded synchronous recursion. The Docs view's
//      onLocaleChange wrapper combined with an uninitialised _state.locale
//      hit this path and shipped ~75 000 duplicate fetches in seconds.
//
//   2. Re-entrant setState calls inside a notification cycle are collapsed:
//      the patch is still merged into _state synchronously, but no nested
//      dispatch is started. Subscribers in the outer cycle see the merged
//      state on their remaining visits (they read _state by reference).
//      This prevents an O(N²) cascade if multiple subscribers each setState
//      in reaction to a change.
//
//   3. _state.locale is seeded from localStorage at module load so the
//      in-memory store agrees with the persisted value from the moment
//      any subscriber attaches. The prior implementation left _state.locale
//      undefined and let onLocaleChange fall back to a default that
//      disagreed with localStorage — that divergence is what armed the
//      recursion described above.

// _readPersistedLocale — read the locale from localStorage with the same
// allow-list locale.js uses. Inlined (not imported) so state.js has no
// import dependency on locale.js, which itself imports from state.js.
// The two-string duplication (storage key + default) is a tolerated
// trade: the seed MUST happen at module load and cannot wait for any
// other module to finish initialising.
function _readPersistedLocale() {
  try {
    if (typeof localStorage === "undefined") return "en";
    const v = localStorage.getItem("admin_docs_locale");
    return v === "pt-BR" ? "pt-BR" : "en";
  } catch {
    return "en";
  }
}

const _state = {
  config: null,
  token: null,
  identity: null,
  theme: "dark",
  route: null,
  // Seeded from localStorage at module load. See header comment §3.
  locale: _readPersistedLocale(),
};

const _subs = new Set();

// True while a setState dispatch is iterating subscribers. Used to collapse
// re-entrant setState calls into a single observable update.
let _dispatching = false;

export function getState() {
  return _state;
}

export function setState(patch) {
  Object.assign(_state, patch);
  // Re-entrant setState inside a subscriber callback: merge the patch and
  // return without dispatching. The outer dispatch sees the merged state
  // on its remaining subscriber visits (they read _state by reference),
  // so no observable update is lost.
  if (_dispatching) return;
  _dispatching = true;
  try {
    // Snapshot subscribers so that callbacks which register new
    // subscribers do NOT cause those new subscribers to fire in this
    // same cycle. See header comment §1.
    const snapshot = Array.from(_subs);
    for (const fn of snapshot) {
      // A prior callback may have unsubscribed an entry still in our
      // snapshot. Skip it so the unsubscribe takes effect immediately
      // for the remainder of this cycle.
      if (!_subs.has(fn)) continue;
      try { fn(_state); } catch (e) { console.error("subscriber error:", e); }
    }
  } finally {
    _dispatching = false;
  }
}

export function subscribe(fn) {
  _subs.add(fn);
  return () => _subs.delete(fn);
}

// _resetForTests — test-only helper. Production code does NOT call this;
// it exists so the unit tests in tests/ can reset the module singleton
// between cases without inventing module-isolation tricks. `initialPatch`
// lets a test simulate a specific boot state (e.g. locale already set).
export function _resetForTests(initialPatch) {
  _subs.clear();
  _dispatching = false;
  _state.config   = null;
  _state.token    = null;
  _state.identity = null;
  _state.theme    = "dark";
  _state.route    = null;
  _state.locale   = _readPersistedLocale();
  if (initialPatch) Object.assign(_state, initialPatch);
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
