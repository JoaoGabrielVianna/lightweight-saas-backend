// playwright.config.js — the operator-journey browser suite.
//
// The process under test is NOT started here. scripts/browser-e2e.sh builds
// the realms, boots ./bin/api against a real PostgreSQL, and waits for
// /health/ready before this config is ever read. That split is deliberate:
// a `webServer` block here would put half the fixture in YAML-adjacent config
// and half in a shell script, and the readiness wait — the thing that stops
// Chromium opening on ERR_CONNECTION_REFUSED and hiding the real cause —
// belongs next to the process it is waiting for.
//
// ─── ARTIFACT POLICY (read this before changing anything below) ─────────────
//
// trace: off · screenshot: off · video: off. Not a performance choice.
//
// This suite handles three values that must not outlive the run:
//
//   1. the identity-provider client secret the operator TYPES INTO A FORM;
//   2. the project credential the server displays exactly once;
//   3. the operator's bearer token, on every request.
//
// A Playwright trace stores DOM snapshots including input values, and the
// request/response log. A failure screenshot stores whatever was on screen —
// and the highest-value screenshot, the one taken when the credential modal is
// open, is precisely the one that contains a live credential. The HTML report
// embeds both.
//
// Three options were on the table. (A) trace only for specs without secrets:
// rejected, because it makes the safety of the artifact depend on a per-file
// judgement that will be wrong the first time a spec grows. (C) redact traces
// after the fact: rejected, because the trace is a zip of DOM snapshots and
// network bodies, and "we redacted it" is only true if you can prove the
// PUBLISHED copy is the redacted one. That leaves (B), the smallest thing that
// is safe by construction: capture nothing that can hold a secret, and get
// diagnostics from sources that are text and can be checked.
//
// What replaces them, and it is enough to debug a CI failure:
//
//   - every page error, console.error and failed request, per test, written to
//     a plain-text log by fixtures/console-errors.js;
//   - the current URL and the test title on failure;
//   - the LIGHTWEIGHT API log and the Keycloak container log, both passed
//     through scripts/redact-logs.sh before upload;
//   - a JSON result file listing which step failed.
//
// scripts/scan-artifacts.sh then searches everything published for the exact
// secret values the run used, plus lw_sk_/JWT/Bearer shapes. That gate is what
// makes this comment a fact rather than an intention.
//
// Local debugging may enable tracing with LW_BROWSER_TRACE=1. Artifacts from
// such a run are for your machine only and must never be uploaded — the scan
// will fail them, which is the intended outcome.

import { defineConfig, devices } from "@playwright/test";

const BASE_URL = process.env.LW_E2E_BASE_URL || "http://localhost:58095";
const ARTIFACT_DIR = process.env.LW_E2E_ARTIFACT_DIR || "./artifacts";

// Opt-in tracing for local debugging ONLY. Off in CI by construction: the CI
// job does not set this variable, and the artifact scan fails a run whose
// artifacts contain a credential regardless of how they got there.
const LOCAL_TRACE = process.env.LW_BROWSER_TRACE === "1";

export default defineConfig({
  testDir: "./tests",
  outputDir: `${ARTIFACT_DIR}/test-results`,

  // ─── Retries: zero, on purpose ────────────────────────────────────────────
  //
  // This is a new toolchain against a real browser, a real Keycloak and a real
  // database. A retry policy adopted before we know the suite's stability
  // converts flakiness into a slower green build, and we would find out about
  // it from a bug report instead of from CI. If a journey flakes, that is the
  // finding, and the fix is in the test or the product — not in a retry count.
  retries: 0,

  // ─── One worker ───────────────────────────────────────────────────────────
  //
  // The journeys share one Keycloak, one database and one LIGHTWEIGHT process.
  // Workspace-scoped state is isolated by run-scoped names, but the Keycloak
  // SSO session and the audit trail are installation-wide, and an audit
  // assertion racing another spec's mutations is a fixture-management project.
  // That project is not this slice. Parallelism comes after isolation is
  // proven, not before.
  workers: 1,
  fullyParallel: false,

  // A journey that hangs must fail inside the time budget rather than at the
  // CI job's 20-minute wall, where the log is truncated and nothing says which
  // step stopped.
  timeout: 90_000,
  expect: { timeout: 15_000 },

  // `list` prints each journey step as it happens, which is what a CI log
  // needs. `json` gives a machine-readable record of which step failed. No
  // `html` reporter: it embeds traces and screenshots, and even with both off
  // it is a directory of assets nobody reads in CI.
  reporter: [
    ["list"],
    ["json", { outputFile: `${ARTIFACT_DIR}/results.json` }],
  ],

  use: {
    baseURL: BASE_URL,
    // See the ARTIFACT POLICY block above before changing any of these three.
    trace: LOCAL_TRACE ? "retain-on-failure" : "off",
    screenshot: "off",
    video: "off",

    // Auto-waiting is the whole point of using this tool; an explicit action
    // timeout keeps a missing element from consuming the test timeout and
    // reporting as "test timed out" instead of "button not found".
    actionTimeout: 15_000,
    navigationTimeout: 30_000,

    // The console and Keycloak are both served over plain HTTP on loopback.
    ignoreHTTPSErrors: true,
  },

  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        // A fixed viewport keeps the console out of its mobile layout, where
        // the sidebar collapses behind a hamburger and role-based locators
        // would resolve differently on a laptop than in CI.
        viewport: { width: 1440, height: 900 },
      },
    },
  ],
});
