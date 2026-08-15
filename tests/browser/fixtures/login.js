// login.js — the real Authorization Code + PKCE login, performed by a browser.
//
// ─── What is deliberately NOT done here ─────────────────────────────────────
//
// No token is written into sessionStorage. No callback URL is constructed by
// hand. No Keycloak endpoint is called directly. The console's own boot
// sequence issues the authorization request, Keycloak's own login page takes
// the credentials, and the console's own callback handler exchanges the code.
//
// That restraint IS the test. A helper that seeded a token would make every
// other journey pass while the login flow was broken — which is the exact
// failure mode TD-031 describes, reproduced inside the fix for TD-031.
//
// ─── How the console starts a login ─────────────────────────────────────────
//
// There is no login page and no sign-in button. web/admin/static/js/main.js
// boot() fetches /admin/config.json and, finding no token, calls startLogin()
// immediately, which navigates to Keycloak. So `goto('/admin')` IS the login
// trigger, and the first thing to assert is that we left our own origin.

import { expect } from "@playwright/test";
import { env, recordSecret } from "./env.js";

// Keycloak's login form. These three ids are Keycloak's, not ours — its
// login theme has used them for many major versions, and the image is pinned
// (quay.io/keycloak/keycloak:26.0) in both compose and CI, so they cannot
// change under us without a deliberate upgrade. Everything that IS ours is
// located by role or accessible name; see the specs.
const KC_USERNAME = "#username";
const KC_PASSWORD = "#password";
const KC_SUBMIT = "#kc-login, input[type=submit][name=login]";

/**
 * loginAsOperator — drive a complete PKCE login and return what was observed.
 *
 * @returns {Promise<{authorizeUrl: string, callbackUrl: string, navigations: string[]}>}
 */
export async function loginAsOperator(page, { username = env.operatorUser, password = env.operatorPassword } = {}) {
  const navigations = [];
  const onNav = (frame) => {
    if (frame === page.mainFrame()) navigations.push(frame.url());
  };
  page.on("framenavigated", onNav);

  try {
    // 1. Open the console. Nothing else — the console decides to log in.
    await page.goto("/admin");

    // 2. It must have left our origin for Keycloak's authorization endpoint.
    //    Waiting on the URL rather than on a form element means a console that
    //    silently failed to redirect fails HERE, saying so, instead of timing
    //    out later on a missing username field.
    const authorizePattern = new RegExp(
      `${escapeRegExp(env.keycloakURL)}/realms/${escapeRegExp(env.controlRealm)}/protocol/openid-connect/auth`,
    );
    await page.waitForURL(authorizePattern, { timeout: 30_000 });
    const authorizeUrl = page.url();

    // 3. The request must actually be a PKCE authorization-code request. A
    //    console that dropped the challenge would still log in successfully
    //    against a permissive Keycloak, and the flow would be materially
    //    weaker while every visible assertion stayed green.
    const authParams = new URL(authorizeUrl).searchParams;
    expect(authParams.get("response_type")).toBe("code");
    expect(authParams.get("client_id")).toBe(env.consoleClientId);
    expect(authParams.get("code_challenge_method")).toBe("S256");
    expect(authParams.get("code_challenge")).toBeTruthy();
    expect(authParams.get("state")).toBeTruthy();

    // 4. Keycloak's own login page, with the operator's own credentials.
    await page.locator(KC_USERNAME).fill(username);
    await page.locator(KC_PASSWORD).fill(password);
    await page.locator(KC_SUBMIT).first().click();

    // 5. Back on the console, authenticated. The console replaces the callback
    //    URL with history.replaceState as soon as the exchange finishes, so the
    //    ?code= URL is transient — it is captured from the navigation log
    //    below rather than from page.url().
    await page.waitForURL(/\/admin(\?|#|$)/, { timeout: 30_000 });

    // 6. The router's landing route. Reaching it means boot() got past the
    //    token exchange, past /auth/debug, and past the workspace load — all
    //    of which are awaited before initRouter runs.
    await page.waitForURL(/\/admin#\/overview$/, { timeout: 30_000 });

    const callbackUrl = navigations.find((u) => u.includes("code=")) || "";

    // The authorization code is single-use and by now spent, but it is still
    // credential material and must not reach an artifact. Registering it means
    // scan-artifacts.sh will fail the run if anything wrote it down.
    if (callbackUrl) {
      const code = new URL(callbackUrl).searchParams.get("code");
      if (code) recordSecret(code);
    }

    return { authorizeUrl, callbackUrl, navigations: navigations.slice() };
  } finally {
    page.off("framenavigated", onNav);
  }
}

/**
 * expectAuthenticatedShell — the console is showing an authenticated operator.
 *
 * This is the KI-001 assertion, and it is asserted on the SHELL rather than on
 * any one page because that is where the defect showed: the sidebar user card
 * reads `if (!id || !id.valid)` and renders the literal text "not signed in"
 * when /auth/debug's payload lacks the `valid` field. Under KI-001 the
 * operator was fully authenticated — every request they made succeeded — and
 * the console told them they were not signed in.
 *
 * So: assert the identity is RENDERED, and assert the not-signed-in state is
 * absent. Asserting only the first would pass on a card that shows both.
 */
export async function expectAuthenticatedShell(page, { email } = {}) {
  const sidebar = page.locator("#sidebar");
  await expect(sidebar.getByText("not signed in")).toHaveCount(0);
  await expect(sidebar.locator(".sidebar-user-name")).toBeVisible();
  if (email) {
    await expect(sidebar.locator(".sidebar-user-name")).toHaveText(email);
  }
}

function escapeRegExp(s) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
