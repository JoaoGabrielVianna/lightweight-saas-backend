// test.js — the `test` every spec imports.
//
// Base Playwright plus two things every journey needs and none should have to
// remember: error capture that FAILS the test, and a written diagnostic log
// that replaces the traces and screenshots the artifact policy forbids.

import { test as base, expect } from "@playwright/test";
import { attachErrorCapture, writeErrorLog, redactUrl } from "./console-errors.js";

export const test = base.extend({
  /**
   * errors — the browser-error collector for this test.
   *
   * `auto: true` is load-bearing, not tidiness. A Playwright fixture is lazy:
   * without it, the collector only exists for tests that destructure `errors`,
   * so any test written as `async ({ page })` would run with NO error capture
   * at all — silently, and exactly like a test that captured errors and found
   * none. This suite exists to catch console-only defects; a capture that
   * quietly does not apply to two thirds of the tests is worse than none,
   * because it is believed.
   *
   * A spec that expects an error declares it:
   *   errors.allowStatus(400, /\/v1\/workspaces$/)
   *
   * The teardown half runs even when the body failed, so a failing test still
   * writes its log — that log is the only diagnostic this suite produces.
   */
  errors: [async ({ page }, use, testInfo) => {
    const collector = attachErrorCapture(page);
    await use(collector);

    let currentUrl = "(page closed)";
    try { currentUrl = page.url(); } catch { /* the page may be gone */ }

    const logFile = writeErrorLog(testInfo.title, collector, {
      status: testInfo.status,
      expected: testInfo.expectedStatus,
      "current url": currentUrl,
    });

    const unexpected = collector.unexpected();
    if (unexpected.length > 0) {
      const summary = unexpected
        .map((e) => `  ${e.kind}: ${redactUrl(e.text).slice(0, 400)}`)
        .join("\n");
      // Thrown rather than soft-asserted: an unexpected browser error means
      // the page did something nobody planned, and every assertion that
      // "passed" after it did so against an unknown state.
      throw new Error(
        `${unexpected.length} unexpected browser error(s) during "${testInfo.title}".\n` +
        `${summary}\n\nFull log: ${logFile}\n` +
        `If one of these is genuinely benign, allowlist it in ` +
        `fixtures/console-errors.js with the reason — not by widening a test.`,
      );
    }
  }, { auto: true }],
});

export { expect };
