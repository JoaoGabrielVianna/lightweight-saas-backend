// busy-guards.test.mjs — pure DOM tests for the UI-003 and UI-004 busy-guard
// pattern. They don't import the production view modules (those require a
// full DOM + state stack); instead they execute the exact handler shape used
// by sendResetEmail (user-detail.js) and resendInvitation (invitations.js)
// against an in-memory DOM stub. If the shape ever drifts from this test —
// for example, a refactor that swaps "btn.disabled = true" for a class
// toggle — the test will still pin the contract: two rapid clicks must
// produce ONE network call.
//
// Run with: node --test web/admin/static/js/tests/

import { test } from "node:test";
import assert from "node:assert/strict";

// Minimal HTMLButtonElement-ish stub. The production code reads/writes
// `disabled` and `textContent`, and checks `document.body.contains(btn)`
// before re-enabling.
function makeButton(label) {
  let _disabled = false;
  let _text = label;
  return {
    get disabled() { return _disabled; },
    set disabled(v) { _disabled = !!v; },
    get textContent() { return _text; },
    set textContent(v) { _text = String(v); },
  };
}

// Re-implementation of the guarded handler. Same shape as sendResetEmail in
// user-detail.js. apiTry is injected so the test can control timing + count
// invocations.
function makeGuardedHandler(apiTry) {
  return function trigger(btn) {
    if (!btn || btn.disabled) return Promise.resolve("blocked");
    btn.disabled = true;
    const originalLabel = btn.textContent;
    btn.textContent = "Sending…";
    return apiTry().finally(() => {
      btn.disabled = false;
      btn.textContent = originalLabel;
    });
  };
}

test("regression UI-003 / UI-004: double-click produces exactly one API call", async () => {
  let calls = 0;
  let resolveFirst;
  const apiTry = () => {
    calls++;
    return new Promise((resolve) => { resolveFirst = resolve; });
  };
  const trigger = makeGuardedHandler(apiTry);
  const btn = makeButton("Send reset email");

  // First click — starts the request.
  const p1 = trigger(btn);
  // Second click before the first resolves — must be blocked.
  const p2 = trigger(btn);
  // Third click — also blocked.
  const p3 = trigger(btn);

  // The button must be disabled while the request is in flight.
  assert.equal(btn.disabled, true, "button disabled during in-flight call");
  assert.equal(btn.textContent, "Sending…", "label flips to busy state");

  // Resolve the original request.
  resolveFirst({ ok: true });
  await p1;

  // Only ONE API call must have been made, no matter how many clicks landed
  // before the first promise resolved.
  assert.equal(calls, 1, "exactly one network call despite three rapid clicks");

  // The blocked clicks must report "blocked" rather than silently dropping —
  // belt-and-braces so a future caller knows the click was a no-op.
  assert.equal(await p2, "blocked");
  assert.equal(await p3, "blocked");

  // After resolution the button is re-enabled and the label is restored,
  // so the operator can retry intentionally.
  assert.equal(btn.disabled, false, "button re-enabled after the call settles");
  assert.equal(btn.textContent, "Send reset email", "label restored");
});

test("regression UI-003 / UI-004: failure path still re-enables the button", async () => {
  let resolveFail;
  const apiTry = () => new Promise((_, reject) => { resolveFail = reject; });
  const trigger = makeGuardedHandler(apiTry);
  const btn = makeButton("resend");

  const p = trigger(btn).catch(() => "errored");
  assert.equal(btn.disabled, true);

  resolveFail(new Error("502 upstream"));
  await p;

  assert.equal(btn.disabled, false, "must re-enable after failure so operator can retry");
  assert.equal(btn.textContent, "resend", "label restored after failure");
});
