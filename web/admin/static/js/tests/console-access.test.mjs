// console-access.test.mjs — the boot-time authorization gate.
//
// Before this gate, authentication was the console's only question: any
// account in the realm completed the PKCE flow, the shell mounted, and the
// operator learned about the missing role only from a screenful of 403s.
//
// What is pinned here is the DECISION and the SCREEN, not main.js's boot
// sequence — main.js calls boot() at module load and cannot be imported by a
// test. Keeping the rule in lib/access.js is what makes it testable at the
// level it lives at.
//
// The load-bearing case is `valid: false`: /auth/debug fills `roles` from an
// unverified base64 decode of whatever token it was handed, so a token the
// server rejected must never grant access on the strength of its own claims.

import { test } from "node:test";
import assert from "node:assert/strict";
import { makeStorage, installDOM } from "./helpers.mjs";

globalThis.localStorage = makeStorage();
globalThis.sessionStorage = makeStorage();
installDOM();

const { evaluateConsoleAccess, ACCESS, CONSOLE_ROLE } = await import("../lib/access.js");
const { renderAccessDenied } = await import("../components/access-denied.js");

// A realistic /auth/debug body. Only the fields the gate reads are filled in.
function debugResponse(patch) {
  return {
    issuer:       "https://kc.example.test/realms/saas",
    received_sub: "5f0c1f4e-0000-4000-8000-000000000001",
    email:        "operator@example.test",
    roles:        ["admin", "user"],
    valid:        true,
    reason:       "",
    ...patch,
  };
}

// ─── The decision ───────────────────────────────────────────────────────────

test("admin role on the session is granted", () => {
  const d = evaluateConsoleAccess(debugResponse());
  assert.equal(d.allowed, true);
  assert.equal(d.state, ACCESS.GRANTED);
  assert.equal(d.reason, "");
});

test("the required role is the one the server enforces", () => {
  // internal/authz/authorize.go's operatorRole and the literal passed to
  // RequireRole in internal/server/router.go. If the server's role is ever
  // renamed, this test is the reminder that the console has a copy.
  assert.equal(CONSOLE_ROLE, "admin");
});

test("a session with other roles is denied, not merely unknown", () => {
  const d = evaluateConsoleAccess(debugResponse({ roles: ["user", "editor"] }));
  assert.equal(d.allowed, false);
  assert.equal(d.state, ACCESS.MISSING_ROLE);
  assert.deepEqual(d.roles, ["user", "editor"]);
});

test("a session with no roles at all is denied", () => {
  const d = evaluateConsoleAccess(debugResponse({ roles: [] }));
  assert.equal(d.allowed, false);
  assert.equal(d.state, ACCESS.MISSING_ROLE);
  assert.deepEqual(d.roles, []);
});

test("a role that merely contains 'admin' does not pass", () => {
  const d = evaluateConsoleAccess(debugResponse({ roles: ["admin-readonly", "not-admin"] }));
  assert.equal(d.allowed, false);
  assert.equal(d.state, ACCESS.MISSING_ROLE);
});

test("no identity at all is unverified, and still denied", () => {
  for (const identity of [null, undefined, "", 42]) {
    const d = evaluateConsoleAccess(identity);
    assert.equal(d.allowed, false, String(identity));
    assert.equal(d.state, ACCESS.UNVERIFIED, String(identity));
    assert.deepEqual(d.roles, []);
    assert.equal(d.role, CONSOLE_ROLE);
  }
});

test("a payload with no role list is unverified, not denied", () => {
  const noRoles = debugResponse();
  delete noRoles.roles;
  assert.equal(evaluateConsoleAccess(noRoles).state, ACCESS.UNVERIFIED);
  assert.equal(evaluateConsoleAccess(debugResponse({ roles: "admin" })).state, ACCESS.UNVERIFIED);
});

test("valid:false never grants, even when the claims say admin", () => {
  const d = evaluateConsoleAccess(debugResponse({
    valid:  false,
    reason: "token expired",
    roles:  ["admin"],
  }));
  assert.equal(d.allowed, false);
  assert.equal(d.state, ACCESS.UNVERIFIED);
  assert.equal(d.reason, "token expired");
});

test("non-string entries in the role list cannot pass the gate", () => {
  const d = evaluateConsoleAccess(debugResponse({ roles: [null, 7, { name: "admin" }] }));
  assert.equal(d.allowed, false);
  assert.deepEqual(d.roles, []);
});

test("identity fields are normalized for rendering", () => {
  const d = evaluateConsoleAccess(debugResponse({ email: null, received_sub: 12345, roles: ["user"] }));
  assert.equal(d.email, "");
  assert.equal(d.subject, "");
});

// ─── The screen ─────────────────────────────────────────────────────────────

test("missing-role screen names the account, its roles, and the role required", () => {
  const el = renderAccessDenied(evaluateConsoleAccess(debugResponse({ roles: ["user"] })), {});
  const text = el.textContent;
  assert.match(text, /cannot use the console/);
  assert.match(text, /operator@example\.test/);
  assert.match(text, /user/);
  assert.match(text, /admin/);
});

test("a session with no roles reads 'none', not an empty gap", () => {
  const el = renderAccessDenied(evaluateConsoleAccess(debugResponse({ roles: [] })), {});
  assert.match(el.textContent, /none/);
});

test("the subject stands in when the token carries no email", () => {
  const el = renderAccessDenied(
    evaluateConsoleAccess(debugResponse({ email: "", roles: ["user"] })), {},
  );
  assert.match(el.textContent, /5f0c1f4e-0000-4000-8000-000000000001/);
});

test("markup in an identity field renders as text, never as elements", () => {
  const el = renderAccessDenied(
    evaluateConsoleAccess(debugResponse({ email: "<img src=x onerror=alert(1)>", roles: ["user"] })), {},
  );
  assert.match(el.textContent, /<img src=x onerror=alert\(1\)>/);
  assert.equal(el.querySelector("IMG"), null);
});

test("missing role offers sign-out and NOT retry", () => {
  let signedOut = 0;
  const el = renderAccessDenied(
    evaluateConsoleAccess(debugResponse({ roles: ["user"] })),
    { onSignOut: () => { signedOut++; }, onRetry: () => { throw new Error("retry must not be offered"); } },
  );
  const buttons = el.querySelectorAll("button");
  assert.equal(buttons.length, 1, "exactly one action");
  assert.equal(buttons[0].textContent, "Sign out");
  buttons[0].dispatch("click");
  assert.equal(signedOut, 1);
});

test("unverified offers both retry and sign-out", () => {
  let retried = 0, signedOut = 0;
  const el = renderAccessDenied(
    evaluateConsoleAccess(null),
    { onSignOut: () => { signedOut++; }, onRetry: () => { retried++; } },
  );
  assert.match(el.textContent, /Could not confirm your permissions/);
  const labels = el.querySelectorAll("button").map((b) => b.textContent);
  assert.deepEqual(labels, ["Retry", "Sign out"]);
  el.querySelectorAll("button")[0].dispatch("click");
  assert.equal(retried, 1);
  assert.equal(signedOut, 0);
});

test("the screen renders with no handlers wired", () => {
  const el = renderAccessDenied(evaluateConsoleAccess(debugResponse({ roles: ["user"] })));
  assert.equal(el.querySelectorAll("button").length, 0);
  assert.match(el.textContent, /cannot use the console/);
});
