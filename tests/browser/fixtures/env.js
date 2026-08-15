// env.js — everything scripts/browser-e2e.sh handed this process.
//
// One module reads process.env, so a missing variable fails once with a
// sentence about which fixture step did not run, rather than as `undefined`
// interpolated into a URL three files away.

import fs from "node:fs";
import path from "node:path";

function required(name) {
  const v = process.env[name];
  if (!v) {
    throw new Error(
      `${name} is not set. The browser suite is started by scripts/browser-e2e.sh, ` +
      `which builds the realms and exports this. Running \`npx playwright test\` ` +
      `directly will not work — see docs/testing/BROWSER_E2E.md.`,
    );
  }
  return v;
}

export const env = {
  baseURL:        required("LW_E2E_BASE_URL"),
  keycloakURL:    required("LW_E2E_KEYCLOAK_URL"),
  controlRealm:   required("LW_E2E_CONTROL_REALM"),
  consoleClientId: required("LW_E2E_CONSOLE_CLIENT_ID"),
  operatorUser:   required("LW_E2E_OPERATOR_USER"),
  operatorPassword: required("LW_E2E_OPERATOR_PASSWORD"),

  alphaRealm:     required("LW_E2E_ALPHA_REALM"),
  bravoRealm:     required("LW_E2E_BRAVO_REALM"),
  connClientId:   required("LW_E2E_CONN_CLIENT_ID"),
  alphaSecret:    required("LW_E2E_ALPHA_SECRET"),
  bravoSecret:    required("LW_E2E_BRAVO_SECRET"),
  alphaUser:      required("LW_E2E_ALPHA_USER"),
  bravoUser:      required("LW_E2E_BRAVO_USER"),

  runId:          required("LW_E2E_RUN_ID"),
  artifactDir:    required("LW_E2E_ARTIFACT_DIR"),
  sentinelDir:    required("LW_E2E_SENTINEL_DIR"),
};

/**
 * unique — a name no other run, and no other call, can collide with.
 *
 * Test isolation here is by NAMING, not by truncation. The alternative — wipe
 * the workspaces table between specs — would mean a test issuing destructive
 * SQL against a database it did not create, which is exactly the move the
 * slice brief rules out. Run-scoped names cost nothing and cannot delete
 * somebody's data.
 */
let _seq = 0;
export function unique(prefix) {
  _seq += 1;
  return `${prefix}-${env.runId}-${_seq}`;
}

// ─── Secret recording ───────────────────────────────────────────────────────

const SENTINEL_FILE = path.join(env.sentinelDir, "secrets.txt");

/**
 * recordSecret — register a value that must NOT appear in any published
 * artifact, so scripts/scan-artifacts.sh can search for it afterwards.
 *
 * The sentinel directory lives OUTSIDE the artifact tree. That is not a
 * detail: a list of secrets stored among the artifacts would be both the leak
 * and the thing that detects it.
 *
 * Call this the moment a secret becomes visible to the test — a value the
 * suite handled but never registered is one the scan cannot look for, and the
 * scan reporting "zero matches" would then be true and meaningless.
 */
export function recordSecret(value) {
  if (!value || typeof value !== "string" || value.length < 8) return;
  fs.mkdirSync(env.sentinelDir, { recursive: true });
  fs.appendFileSync(SENTINEL_FILE, value + "\n", "utf8");
}
