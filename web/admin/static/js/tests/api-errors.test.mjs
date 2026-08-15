// api-errors.test.mjs — the two error envelopes, and the defect that made
// this the first change in Slice 6.
//
// Before the fix, APIError's constructor did:
//
//   super(message || (typeof body === "object" ? body.error : body) || ...)
//
// For /admin/* that is a string and works. For /v1 that is an OBJECT, and
// passing it to super() renders the message as the literal "[object Object]"
// in every toast and every empty state. Migrating views first and fixing this
// after would have meant writing every view against a broken error path.
//
// Run with: node --test web/admin/static/js/tests/

import { test } from "node:test";
import assert from "node:assert/strict";
import { makeStorage, makeResponse } from "./helpers.mjs";

globalThis.localStorage = makeStorage();
globalThis.sessionStorage = makeStorage();

const { APIError, parseErrorBody, api, apiTry } = await import("../lib/api.js");

// ─── The regression ─────────────────────────────────────────────────────────

test("regression: /v1 structured envelope never renders as [object Object]", () => {
  const err = new APIError(409, {
    error: {
      code: "workspace_connection_missing",
      message: "Workspace has no active connection",
      request_id: "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
    },
  });

  assert.notEqual(err.message, "[object Object]");
  assert.equal(err.message, "Workspace has no active connection");
  assert.equal(err.code, "workspace_connection_missing");
  assert.equal(err.requestId, "3f2504e0-4f89-41d3-9a0c-0305e82c3301");
  assert.equal(err.status, 409);
});

test("legacy /admin string envelope still renders correctly", () => {
  const err = new APIError(404, { error: "not found" });

  assert.equal(err.message, "not found");
  assert.equal(err.code, null, "a legacy error has no stable code");
  assert.equal(err.requestId, null, "/admin does not emit a request id");
  assert.equal(err.status, 404);
});

test("String(error) never leaks an object for either envelope", () => {
  const legacy = new APIError(404, { error: "not found" });
  const structured = new APIError(404, { error: { code: "user_not_found", message: "User not found" } });

  for (const e of [legacy, structured]) {
    assert.ok(!String(e).includes("[object"), `String(${e.code}) leaked an object: ${String(e)}`);
    assert.ok(!`${e.message}`.includes("[object"));
  }
});

// ─── Malformed and degenerate bodies ────────────────────────────────────────

test("malformed and unexpected bodies degrade gracefully", () => {
  const cases = [
    [null,                       "HTTP 500"],
    [undefined,                  "HTTP 500"],
    ["<html>gateway error",      "<html>gateway error"], // non-JSON text
    ["",                         "HTTP 500"],
    [{},                         "HTTP 500"],
    [{ error: null },            "HTTP 500"],
    [{ error: 42 },              "HTTP 500"],
    [{ error: [] },              "HTTP 500"],
    [{ error: {} },              "HTTP 500"],
    [[1, 2, 3],                  "HTTP 500"],
    [{ message: "plain" },       "plain"],
    [{ error_description: "oauth style" }, "oauth style"],
  ];

  for (const [body, wantMessage] of cases) {
    const err = new APIError(500, body);
    assert.equal(err.message, wantMessage, `body ${JSON.stringify(body)}`);
    assert.ok(!err.message.includes("[object"), `body ${JSON.stringify(body)} leaked an object`);
    assert.ok(typeof err.message === "string" && err.message.length > 0);
  }
});

test("parseErrorBody returns string-or-null for every field, whatever the input", () => {
  const inputs = [null, undefined, 0, "", "text", [], {}, { error: {} },
    { error: { code: 5, message: {}, request_id: [] } }];

  for (const body of inputs) {
    const p = parseErrorBody(body);
    for (const key of ["message", "code", "requestId"]) {
      assert.ok(p[key] === null || typeof p[key] === "string",
        `${key} was ${typeof p[key]} for ${JSON.stringify(body)}`);
    }
  }
});

test("a non-string code or message in the envelope is dropped, not rendered", () => {
  // A provider or proxy that answers {"error":{"code":500,...}} must not put a
  // number where a stable code string belongs — views branch on `code`.
  const err = new APIError(500, { error: { code: 500, message: { nested: true } } });
  assert.equal(err.code, null);
  assert.equal(err.message, "HTTP 500");
});

// ─── Request id ─────────────────────────────────────────────────────────────

test("request id is preserved from the body", () => {
  const err = new APIError(500, {
    error: { code: "internal_error", message: "Internal error", request_id: "req-123" },
  });
  assert.equal(err.requestId, "req-123");
});

test("request id falls back to the X-Request-Id header when the body has none", () => {
  // A gin panic recovery, or a proxy 502, answers with no envelope — but /v1
  // still echoed the header, and that id is what ties the screen to the log.
  const err = new APIError(502, "Bad Gateway", null, "req-from-header");
  assert.equal(err.requestId, "req-from-header");
  assert.equal(err.message, "Bad Gateway");
});

test("a body request_id wins over the header", () => {
  const err = new APIError(409, { error: { code: "conflict", message: "x", request_id: "body-id" } }, null, "header-id");
  assert.equal(err.requestId, "body-id");
});

// ─── Through the real client ────────────────────────────────────────────────

test("apiTry surfaces the structured envelope AND the header id end to end", async () => {
  globalThis.fetch = async () => makeResponse({
    status: 409,
    body: { error: { code: "connection_read_only", message: "No write access", request_id: "rid-9" } },
    headers: { "X-Request-Id": "rid-9" },
  });

  const r = await apiTry("/v1/workspaces/ws_a/users", { method: "POST" });

  assert.equal(r.ok, false);
  assert.equal(r.status, 409);
  assert.equal(r.error.code, "connection_read_only");
  assert.equal(r.error.message, "No write access");
  assert.equal(r.error.requestId, "rid-9");
});

test("apiTry surfaces the legacy envelope end to end", async () => {
  globalThis.fetch = async () => makeResponse({ status: 404, body: { error: "not found" } });

  const r = await apiTry("/admin/users/nope");

  assert.equal(r.error.message, "not found");
  assert.equal(r.error.code, null);
});

test("api() throws an APIError carrying the code", async () => {
  globalThis.fetch = async () => makeResponse({
    status: 409,
    body: { error: { code: "workspace_archived", message: "Workspace is archived" } },
  });

  await assert.rejects(
    () => api("/v1/workspaces/ws_a/users"),
    (err) => {
      assert.equal(err.code, "workspace_archived");
      assert.equal(err.message, "Workspace is archived");
      return true;
    },
  );
});

test("a transport failure is status 0, not a fabricated HTTP status", async () => {
  globalThis.fetch = async () => { throw new TypeError("Failed to fetch"); };

  const r = await apiTry("/v1/workspaces");

  assert.equal(r.ok, false);
  assert.equal(r.status, 0, "no HTTP exchange happened; 0 is the marker for that");
  assert.equal(r.data, null);
});
