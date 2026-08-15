// operator-api.js — the public HTTP contract, called as an operator.
//
// ─── What this is for, and what it is NOT for ───────────────────────────────
//
// PRECONDITIONS only. A journey that proves "an operator can create a
// connection in the browser" is written in the browser; a journey that proves
// "workspace state does not leak when switching" needs two connected
// workspaces to already exist, and building both through the UI would make
// that test a re-run of the previous one plus the thing it actually tests.
//
// The line is: whatever a spec is proving happens in the browser. Whatever it
// merely NEEDS is provisioned here, over the same public HTTP surface an
// operator's curl would use — never by touching the database, never by
// importing an internal package. This is what scripts/m2m-harness.sh does with
// curl, in JavaScript.
//
// The operator token comes from Keycloak's direct-access grant. The BROWSER
// never uses it; it exists in this file only.

import { env, recordSecret } from "./env.js";

let _tokenPromise = null;

/**
 * operatorToken — a bearer token for the fixture's own HTTP calls.
 *
 * Cached for the process: minting one per call would put a Keycloak token
 * request in front of every provisioning step for no benefit, and the suite
 * runs well inside a token's lifetime.
 */
export function operatorToken() {
  if (_tokenPromise) return _tokenPromise;
  _tokenPromise = (async () => {
    const body = new URLSearchParams({
      grant_type: "password",
      client_id: env.consoleClientId,
      username: env.operatorUser,
      password: env.operatorPassword,
    });
    const r = await fetch(
      `${env.keycloakURL}/realms/${env.controlRealm}/protocol/openid-connect/token`,
      { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" }, body },
    );
    if (!r.ok) {
      throw new Error(`operator token: HTTP ${r.status} — is the fixture realm built?`);
    }
    const tok = await r.json();
    // The fixture's own token is credential material like any other. If it
    // ever reaches an artifact the scan must fail, and it can only do that if
    // the value is registered.
    recordSecret(tok.access_token);
    return tok.access_token;
  })();
  return _tokenPromise;
}

/**
 * apiAs — one HTTP call against /v1 with a chosen credential.
 *
 * `credential` is either an operator bearer token or a project credential
 * secret. The contract is deliberately the same for both, because that is the
 * product's claim: a project credential is used exactly like a bearer token by
 * a caller that knows nothing else.
 *
 * @returns {Promise<{status: number, body: any, requestId: string|null}>}
 */
export async function apiAs(credential, method, path, body) {
  const r = await fetch(env.baseURL + path, {
    method,
    headers: {
      Authorization: "Bearer " + credential,
      ...(body ? { "Content-Type": "application/json" } : {}),
    },
    ...(body ? { body: JSON.stringify(body) } : {}),
  });
  const text = await r.text();
  let parsed = text;
  try { parsed = text ? JSON.parse(text) : null; } catch { /* keep the text */ }
  return { status: r.status, body: parsed, requestId: r.headers.get("X-Request-Id") };
}

/** api — apiAs with the operator token. */
export async function api(method, path, body) {
  return apiAs(await operatorToken(), method, path, body);
}

/**
 * provisionConnectedWorkspace — a workspace with an active connection to a
 * realm, ready for a spec that needs one but is not testing how it got there.
 *
 * Verify-then-activate, in that order, through the same two endpoints the
 * console's buttons call. The connection is NOT marked active by any other
 * means: activation requires a verification that passed and has not expired,
 * and short-circuiting that would make the fixture prove something the product
 * does not do.
 */
export async function provisionConnectedWorkspace({ name, realm, secret }) {
  const ws = await api("POST", "/v1/workspaces", { name });
  if (ws.status !== 201 && ws.status !== 200) {
    throw new Error(`provision workspace ${name}: HTTP ${ws.status} ${JSON.stringify(ws.body)}`);
  }
  const workspaceId = ws.body.id;

  const conn = await api("POST", `/v1/workspaces/${workspaceId}/connections`, {
    name: realm,
    base_url: env.keycloakURL,
    realm,
    client_id: env.connClientId,
    client_secret: secret,
  });
  if (conn.status !== 201 && conn.status !== 200) {
    throw new Error(`provision connection for ${name}: HTTP ${conn.status} ${JSON.stringify(conn.body)}`);
  }
  const connectionId = conn.body.id;

  const verified = await api("POST", `/v1/workspaces/${workspaceId}/connections/${connectionId}/verify`);
  if (verified.status !== 200) {
    throw new Error(`verify connection for ${name}: HTTP ${verified.status} ${JSON.stringify(verified.body)}`);
  }
  const activated = await api("POST", `/v1/workspaces/${workspaceId}/connections/${connectionId}/activate`);
  if (activated.status !== 200) {
    throw new Error(`activate connection for ${name}: HTTP ${activated.status} ${JSON.stringify(activated.body)}`);
  }

  return { workspaceId, connectionId, name, realm };
}
