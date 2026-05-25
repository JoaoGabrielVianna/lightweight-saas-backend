// auth.test.mjs — unit tests for lib/auth.js.
//
// Run with: node --test web/admin/static/js/tests/
//
// Pins the UI-001 regression: a double-click on the Login button must not
// corrupt the PKCE handshake. Before the fix, two concurrent startLogin
// invocations each generated their own (verifier, challenge) pair; the
// second call overwrote the verifier in sessionStorage while either
// navigation could win, so the verifier in storage no longer matched the
// challenge sent to Keycloak. The token exchange then failed with
// invalid_grant.

import { test } from "node:test";
import assert from "node:assert/strict";

// Stub localStorage before importing state.js (which reads it at load).
globalThis.localStorage = makeStorage();
// sessionStorage stub — auth.js writes the PKCE verifier and OAuth state here.
globalThis.sessionStorage = makeStorage();
// crypto stub — auth.js uses crypto.getRandomValues + crypto.subtle.digest.
// Node 19+ exposes webcrypto on globalThis.crypto as a read-only getter,
// so plain assignment throws. defineProperty bypasses that; the assignment
// is also a no-op when the global is already the same webcrypto instance.
const { webcrypto } = await import("node:crypto");
if (globalThis.crypto !== webcrypto) {
  Object.defineProperty(globalThis, "crypto", {
    value: webcrypto, configurable: true, writable: true,
  });
}
// window.location.assign stub — record every navigation attempt.
const _assigns = [];
globalThis.window = { location: { assign: (u) => _assigns.push(String(u)) } };

const { setState, _resetForTests } = await import("../lib/state.js");
const { startLogin, _resetLoginInFlightForTests } = await import("../lib/auth.js");

function makeStorage(initial) {
  const data = { ...(initial || {}) };
  return {
    getItem(k) { return Object.prototype.hasOwnProperty.call(data, k) ? data[k] : null; },
    setItem(k, v) { data[k] = String(v); },
    removeItem(k) { delete data[k]; },
    clear() { for (const k of Object.keys(data)) delete data[k]; },
    _data: data,
  };
}

function resetAll() {
  _resetForTests();
  _resetLoginInFlightForTests();
  globalThis.sessionStorage = makeStorage();
  _assigns.length = 0;
  setState({
    config: {
      keycloakUrl: "https://kc.example.test",
      realm:       "saas",
      clientId:    "saas-dev-playground",
      redirectUri: "https://app.example.test/admin/",
    },
  });
}

// sha256 of a base64url string, returned as base64url. Mirrors the helper
// inside auth.js so the test can independently verify that the verifier in
// storage hashes to the challenge embedded in the navigation URL.
async function sha256B64Url(input) {
  const buf = new TextEncoder().encode(input);
  const digest = await crypto.subtle.digest("SHA-256", buf);
  const bytes = new Uint8Array(digest);
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return Buffer.from(bin, "binary").toString("base64")
    .replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

test("startLogin: single click navigates with verifier matching challenge", async () => {
  resetAll();
  await startLogin();
  assert.equal(_assigns.length, 1, "exactly one navigation");

  const verifier = globalThis.sessionStorage.getItem("kc_admin_pkce_verifier");
  assert.ok(verifier, "verifier stored");

  const url = new URL(_assigns[0]);
  const challenge = url.searchParams.get("code_challenge");
  assert.equal(url.searchParams.get("code_challenge_method"), "S256");
  assert.equal(await sha256B64Url(verifier), challenge,
    "stored verifier must hash to the challenge in the navigation URL");
});

test("regression UI-001: double-click does NOT corrupt PKCE handshake", async () => {
  resetAll();

  // Two concurrent invocations — the double-click race. Both promises are
  // awaited together; without the in-flight guard, this is exactly the
  // path that produced a verifier/challenge mismatch.
  const [a, b] = await Promise.all([startLogin(), startLogin()]);

  // The guard makes both calls share a single promise → one navigation.
  assert.equal(_assigns.length, 1,
    `expected 1 navigation, got ${_assigns.length} (pre-fix: 2 with mismatched challenges)`);

  // The two promises must be the same in-flight handle.
  assert.equal(a, b, "concurrent startLogin calls must share the in-flight promise");

  // The invariant that actually defines PKCE correctness: the verifier
  // sitting in sessionStorage must hash to the challenge embedded in the
  // URL Keycloak will see. If this assertion ever fires again, login is
  // broken end-to-end.
  const verifier  = globalThis.sessionStorage.getItem("kc_admin_pkce_verifier");
  const url       = new URL(_assigns[0]);
  const challenge = url.searchParams.get("code_challenge");
  assert.equal(await sha256B64Url(verifier), challenge,
    "PKCE invariant: SHA256(stored_verifier) === url.code_challenge");

  // OAuth state must likewise match between storage and URL.
  const storedState = globalThis.sessionStorage.getItem("kc_admin_oauth_state");
  assert.equal(storedState, url.searchParams.get("state"),
    "OAuth state in storage must match the state in the navigation URL");
});

test("regression UI-001: rapid serial clicks also produce one consistent navigation", async () => {
  // Some browsers fire two click events from a fast double-click as two
  // separate microtasks rather than truly concurrent — the second click
  // re-enters startLogin AFTER the first awaited sha256 has resolved but
  // BEFORE window.location.assign has actually navigated the page. Same
  // guard, same outcome: one nav, one consistent (verifier, challenge).
  resetAll();
  await startLogin();
  await startLogin();
  // The second call must reuse the existing in-flight handle (which has
  // already resolved after the first nav) and NOT issue a fresh navigation.
  // After the first call resolves, _loginInFlight is still set — so the
  // second call returns the same resolved promise without re-running the
  // body. This is intentional: the page is in the middle of navigating away.
  assert.equal(_assigns.length, 1,
    "second click must not trigger a second navigation");
});

test("startLogin: on config error, in-flight guard resets so user can retry", async () => {
  resetAll();
  setState({ config: null }); // force the early throw

  await assert.rejects(() => startLogin(), /config not loaded/);

  // Restore config and verify a fresh attempt now works.
  setState({
    config: {
      keycloakUrl: "https://kc.example.test",
      realm:       "saas",
      clientId:    "saas-dev-playground",
      redirectUri: "https://app.example.test/admin/",
    },
  });
  await startLogin();
  assert.equal(_assigns.length, 1, "retry after failure must succeed");
});
