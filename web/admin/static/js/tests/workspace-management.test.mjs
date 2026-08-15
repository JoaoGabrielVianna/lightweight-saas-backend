// workspace-management.test.mjs — the minimal management surface (Phase 12)
// and the states it renders (Phases 5 and 11).
//
// Two things are worth pinning here above all others:
//
//   1. THE SECRET. The console must never render a stored client secret, and
//      must never blank one out by accident. The API makes the first
//      structurally impossible (ConnectionResponse has no such field); the
//      second is a client-side rule that a refactor could silently break.
//
//   2. THE DEGRADED STATES. "No workspaces", "not connected", "read-only" are
//      product states, not errors, and each must give the operator somewhere
//      to go rather than an opaque failure.

import { test } from "node:test";
import assert from "node:assert/strict";
import { makeStorage, fetchStub, installDOM } from "./helpers.mjs";

globalThis.localStorage = makeStorage();
globalThis.sessionStorage = makeStorage();
installDOM();
globalThis.window = { location: { hash: "" }, addEventListener() {}, scrollTo() {} };

const state = await import("../lib/state.js");
const ws = await import("../lib/workspaces.js");
const { buildConnectionBody } = await import("../views/connections.js");
const wsStates = await import("../components/ws-states.js");
const { APIError } = await import("../lib/api.js");

const A = "ws_aaaaaaaa-0000-4000-8000-000000000001";
const wsA = { id: A, name: "Alpha", slug: "alpha", status: "active" };

function reset() {
  globalThis.localStorage = makeStorage();
  state._resetForTests();
  ws._resetWorkspacesForTests();
  state.setState({ workspaces: [wsA], currentWorkspaceId: A });
}

// ─── The secret ─────────────────────────────────────────────────────────────

test("create requires a secret and sends it in exactly one field", () => {
  const r = buildConnectionBody({
    name: "Prod", baseURL: "http://keycloak:8080", realm: "saas",
    clientID: "lightweight-admin", secret: "s3cr3t", isEdit: false,
  });

  assert.equal(r.ok, true);
  assert.equal(r.body.client_secret, "s3cr3t");

  const occurrences = Object.entries(r.body).filter(([, v]) => v === "s3cr3t");
  assert.equal(occurrences.length, 1, "the secret must appear in exactly one field");
  assert.equal(occurrences[0][0], "client_secret");
});

test("create REFUSES without a secret", () => {
  const r = buildConnectionBody({
    name: "Prod", baseURL: "http://k", realm: "saas", clientID: "c", secret: "", isEdit: false,
  });
  assert.equal(r.ok, false);
  assert.equal(r.missing, "client_secret");
});

test("edit with a BLANK secret OMITS the key — it does not send an empty string", () => {
  // Sending "" would replace a working credential with an empty one, and the
  // operator would only find out at the next verify.
  const r = buildConnectionBody({
    name: "Prod", baseURL: "http://k", realm: "saas", clientID: "c", secret: "", isEdit: true,
  });

  assert.equal(r.ok, true);
  assert.equal("client_secret" in r.body, false,
    "an absent client_secret is what the PATCH contract reads as 'unchanged'");
});

test("edit with a typed secret replaces it", () => {
  const r = buildConnectionBody({
    name: "Prod", baseURL: "http://k", realm: "saas", clientID: "c", secret: "new-one", isEdit: true,
  });
  assert.equal(r.body.client_secret, "new-one");
});

test("every required coordinate is checked, and named when missing", () => {
  const base = { name: "n", baseURL: "http://k", realm: "r", clientID: "c", secret: "s", isEdit: false };
  const cases = { name: "name", baseURL: "base_url", realm: "realm", clientID: "client_id" };

  for (const [field, wantMissing] of Object.entries(cases)) {
    const r = buildConnectionBody({ ...base, [field]: "   " });
    assert.equal(r.ok, false, `${field} should be required`);
    assert.equal(r.missing, wantMissing);
  }
});

test("the connection the console STORES carries no secret", async () => {
  // Belt-and-braces on the API's structural guarantee: whatever the console
  // holds in state and renders from must have no credential in it.
  reset();
  globalThis.fetch = fetchStub([
    [`/v1/workspaces/${A}/connections`, {
      body: {
        connections: [{
          id: "conn_a", workspace_id: A, name: "Prod", provider: "keycloak", status: "active",
          base_url: "http://keycloak:8080", realm: "saas", client_id: "lightweight-admin",
          has_client_secret: true, health: "healthy", access_mode: "full", can_write: true,
        }],
      },
    }],
  ]);

  await ws.loadActiveConnection(A);
  const stored = state.getState().wsConnection;

  assert.equal(stored.has_client_secret, true, "the console knows a secret EXISTS");
  for (const key of Object.keys(stored)) {
    assert.ok(!/secret/i.test(key) || key === "has_client_secret",
      `state carries a secret-ish field: ${key}`);
  }
});

// ─── Management flow: create → verify → activate ────────────────────────────

test("the management flow hits the documented endpoints in order", async () => {
  reset();
  globalThis.fetch = fetchStub([
    [/\/connections$/, { status: 201, body: { id: "conn_new", status: "draft" } }],
    [/\/connections\/conn_new\/verify$/, {
      body: { connection: { id: "conn_new", status: "draft", verified: true }, report: { ok: true, access_mode: "full" } },
    }],
    [/\/connections\/conn_new\/activate$/, { body: { id: "conn_new", status: "active" } }],
  ]);

  await ws.wsMutate(A, "/connections", { method: "POST", body: "{}" });
  await ws.wsMutate(A, "/connections/conn_new/verify", { method: "POST" });
  await ws.wsMutate(A, "/connections/conn_new/activate", { method: "POST" });

  assert.deepEqual(globalThis.fetch.urls(), [
    `/v1/workspaces/${A}/connections`,
    `/v1/workspaces/${A}/connections/conn_new/verify`,
    `/v1/workspaces/${A}/connections/conn_new/activate`,
  ]);
  assert.deepEqual(globalThis.fetch.calls.map((c) => c.method), ["POST", "POST", "POST"]);
});

// ─── Error rendering ────────────────────────────────────────────────────────

test("errorMessage is always a non-empty string, for every envelope", () => {
  const cases = [
    new APIError(409, { error: { code: "conflict", message: "State collision" } }),
    new APIError(404, { error: "not found" }),
    new APIError(500, null),
    new Error("plain error"),
    null,
  ];
  for (const e of cases) {
    const msg = wsStates.errorMessage(e, 500);
    assert.equal(typeof msg, "string");
    assert.ok(msg.length > 0);
    assert.ok(!msg.includes("[object"));
  }
});

test("errorDetail surfaces the remedy AND the request id for a known /v1 code", () => {
  const err = new APIError(409, {
    error: {
      code: "connection_read_only",
      message: "No write access",
      request_id: "rid-42",
    },
  });

  const detail = wsStates.errorDetail(err);

  assert.match(detail, /manage-users/, "the remedy names the Keycloak role to grant");
  assert.match(detail, /connection_read_only/, "the stable code is quotable in a bug report");
  assert.match(detail, /rid-42/, "the request id ties this screen to the server log line");
});

test("errorDetail distinguishes provider_forbidden from a caller problem", () => {
  const err = new APIError(409, { error: { code: "provider_forbidden", message: "Refused" } });
  const detail = wsStates.errorDetail(err);
  assert.match(detail, /Keycloak/, "the operator must be sent to Keycloak, not to their own token");
});

test("errorDetail returns null when there is nothing to add", () => {
  assert.equal(wsStates.errorDetail(new APIError(404, { error: "not found" })), null);
  assert.equal(wsStates.errorDetail(null), null);
});

// ─── Connection state vocabulary ────────────────────────────────────────────

test("writeBlockedReason explains WHY writes are off, and is null when they are on", () => {
  const S = ws.CONNECTION_STATE;

  assert.equal(wsStates.writeBlockedReason(S.HEALTHY, {}), null);

  const readOnly = wsStates.writeBlockedReason(S.LIMITED, { access_mode: "read_only" });
  assert.match(readOnly, /manage-users/, "read_only has a specific, actionable remedy");

  const limited = wsStates.writeBlockedReason(S.LIMITED, { access_mode: "limited" });
  assert.match(limited, /reads were refused/i, "limited is a DIFFERENT problem from read_only");
  assert.notEqual(readOnly, limited, "two access modes, two remedies, two messages");

  assert.match(wsStates.writeBlockedReason(S.NONE, null), /no active connection/i);
  assert.match(wsStates.writeBlockedReason(S.ARCHIVED, null), /archived/i);
});

// ─── Rendered states are TEXT, never markup ─────────────────────────────────

test("a provider message containing markup renders as text, not as elements", () => {
  // Error prose can come from a provider's error page. lib/dom.js appends
  // strings as text nodes, so this is structural — but it is exactly the kind
  // of guarantee that a well-meaning `innerHTML` refactor breaks silently.
  const err = new APIError(502, {
    error: { code: "provider_unavailable", message: "<img src=x onerror=alert(1)>" },
  });

  const el = wsStates.renderAPIError({ status: 502, error: err });

  assert.equal(el.innerHTML, "", "nothing was assigned as raw HTML");
  assert.match(el.textContent, /<img src=x onerror=alert\(1\)>/,
    "the markup is visible to the operator as characters");
});

test("the legacy provider banner says the page is NOT workspace-scoped", () => {
  // Phase 13: the danger is not that SMTP is legacy — it is that an operator
  // reads the workspace selector above it and assumes the page follows it.
  const el = wsStates.legacyProviderBanner("SMTP settings");
  const text = el.textContent;

  assert.match(text, /SMTP settings/);
  assert.match(text, /NOT scoped to the selected workspace/i);
  assert.match(text, /installation/i);
});

test("every gate state renders a title and an action", () => {
  const reasons = [ws.GATE.NO_WORKSPACES, ws.GATE.UNKNOWN_WORKSPACE, ws.GATE.ARCHIVED, ws.GATE.NO_CONNECTION];

  for (const reason of reasons) {
    const el = wsStates.renderGateState({ reason, workspaceId: A });
    const text = el.textContent;
    assert.ok(text.length > 20, `${reason} rendered almost nothing: "${text}"`);
    // A dead end with an explanation is still a dead end: each state offers a
    // button that leads somewhere an operator can act.
    const buttons = el.querySelectorAll("button");
    assert.ok(buttons.length >= 1, `${reason} offers no way forward`);
  }
});

test("the no-connection state points at connections, not at the legacy surface", () => {
  const el = wsStates.renderGateState({ reason: ws.GATE.NO_CONNECTION, workspaceId: A });
  assert.match(el.textContent, /connection/i);
  assert.doesNotMatch(el.textContent, /\/admin/, "never offer the legacy realm as a fallback");
});

test("connectionBanner is absent when healthy and present when degraded", () => {
  const S = ws.CONNECTION_STATE;
  assert.equal(wsStates.connectionBanner(S.HEALTHY, {}, { workspaceId: A }), null,
    "a banner that is always there stops being read");

  const banner = wsStates.connectionBanner(S.LIMITED, { access_mode: "read_only" }, { workspaceId: A });
  assert.ok(banner);
  assert.match(banner.textContent, /Reads work here/);
});
