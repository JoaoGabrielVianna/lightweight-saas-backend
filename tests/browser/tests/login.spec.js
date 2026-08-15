// login.spec.js — JOURNEY 1: a real operator logs in with a real browser.
//
// The most important file in this suite. Every other journey assumes an
// authenticated console; this one is the only thing that proves the console
// can become authenticated, and it is the only thing in the whole repository
// that exercises the PKCE flow against a real Keycloak in a real browser.
//
// Nothing here is simulated: no token is injected, no callback is constructed,
// no Keycloak endpoint is called directly. See fixtures/login.js.

import { test, expect } from "../fixtures/test.js";
import { env } from "../fixtures/env.js";
import { loginAsOperator, expectAuthenticatedShell } from "../fixtures/login.js";

test.describe("Journey 1 — Authorization Code + PKCE login", () => {
  test("an operator logs in through Keycloak and lands on an authenticated console", async ({ page, errors }) => {
    // The workspace list is the first authenticated API call the console makes
    // after the token exchange. Watching for it proves the session is real at
    // the network layer, not merely that some UI rendered.
    const workspacesResponse = page.waitForResponse(
      (r) => r.url().includes("/v1/workspaces") && r.request().method() === "GET",
      { timeout: 30_000 },
    );

    errors.note("opening /admin — the console itself decides to start a login");
    const { authorizeUrl, callbackUrl } = await loginAsOperator(page);

    // ── The redirect reached Keycloak ──────────────────────────────────────
    expect(authorizeUrl).toContain(env.keycloakURL);
    expect(authorizeUrl).toContain(`/realms/${env.controlRealm}/protocol/openid-connect/auth`);

    // ── The callback returned to LIGHTWEIGHT ───────────────────────────────
    //
    // Asserted on the recorded navigation, because the console calls
    // history.replaceState the instant the exchange completes — by the time
    // any assertion could read page.url(), the ?code= is gone. That
    // replaceState is itself worth pinning: leaving the code in the address
    // bar puts it in browser history and in every screenshot the operator
    // ever takes of the console.
    expect(callbackUrl, "the browser never came back with an authorization code").toBeTruthy();
    expect(callbackUrl).toContain(env.baseURL + "/admin");
    expect(new URL(callbackUrl).searchParams.get("code")).toBeTruthy();
    expect(page.url(), "the authorization code was left in the address bar").not.toContain("code=");

    // ── The console is authenticated ───────────────────────────────────────
    const res = await workspacesResponse;
    expect(res.status(), "GET /v1/workspaces did not succeed for the logged-in operator").toBe(200);

    // ── The authenticated UI renders ───────────────────────────────────────
    await expectAuthenticatedShell(page, { email: `${env.operatorUser}@example.test` });
    await expect(page).toHaveURL(/#\/overview$/);
  });

  // ─────────────────────────────────────────────────────────────────────────
  // KI-001 REGRESSION
  // ─────────────────────────────────────────────────────────────────────────
  //
  // KI-001 Defect B: with the dev playground OFF — the production recipe — a
  // second, degraded /auth/debug handler answered without the `valid`,
  // `issuer` and `allowed_clients` fields. The operator was fully
  // authenticated and every request they made worked, but:
  //
  //   components/sidebar.js  `if (!id || !id.valid)`      → "not signed in"
  //   views/overview.js      identity pill                → "invalid"
  //   views/settings.js      `id.valid !== undefined`     → the whole
  //                                                         "API expectations"
  //                                                         card disappeared
  //
  // It shipped and survived six weeks because the only SetupRoutes test ran
  // with the playground ENABLED, and because a silently degraded UI has no
  // failing assertion anywhere.
  //
  // The test below is written as the operator's experience, not as a payload
  // check: log in, and look at the three places the console tells you who you
  // are. Under KI-001 all three are wrong while everything else is green,
  // which is exactly the situation no gate in this repository could see.
  //
  // Note the fixture runs with DEV_PLAYGROUND_ENABLED=false and
  // ADMIN_CONSOLE_ENABLED=true — the configuration KI-001 broke, and the one
  // the pre-existing unit test did not cover.
  test("the console shows the operator as signed in, with the API's expectations [KI-001]", async ({ page }) => {
    await loginAsOperator(page);

    // 1. The sidebar identity card. This is the literal symptom KI-001 was
    //    reported as: "admin console rendered signed-in admins as signed-out".
    const sidebar = page.locator("#sidebar");
    await expect(sidebar.getByText("not signed in")).toHaveCount(0);
    await expect(sidebar.locator(".sidebar-user-name")).toHaveText(`${env.operatorUser}@example.test`);
    // The roles line reads from the same payload and is empty when the
    // degraded handler omits it.
    await expect(sidebar.locator(".sidebar-user-sub")).toHaveText(/admin/);

    // 2. The Overview identity snapshot. Under KI-001 this pill read
    //    "invalid" for a perfectly valid session.
    const snapshot = page.locator(".card", { hasText: "Identity snapshot" });
    await expect(snapshot.getByText("valid", { exact: true })).toBeVisible();
    await expect(snapshot.getByText("invalid", { exact: true })).toHaveCount(0);
    // `iss` is one of the fields the degraded payload dropped.
    await expect(snapshot).toContainText(`${env.keycloakURL}/realms/${env.controlRealm}`);

    // 3. Settings — "API expectations". Under KI-001 this card rendered the
    //    placeholder prose instead of the issuer and allowed clients, because
    //    the view gates on `id.valid !== undefined`.
    await page.goto("/admin#/settings");
    const expectations = page.locator(".card", { hasText: "API expectations" });
    await expect(expectations).toBeVisible();
    await expect(expectations).toContainText("expected issuer");
    await expect(expectations).toContainText(`${env.keycloakURL}/realms/${env.controlRealm}`);
    await expect(expectations).toContainText(env.consoleClientId);
    await expect(expectations.getByText(/sign in via the Playground/i)).toHaveCount(0);
  });

  test("a reload keeps the session without a second trip to Keycloak", async ({ page }) => {
    await loginAsOperator(page);

    // Tokens live in sessionStorage, so a reload in the SAME tab must restore
    // the session from storage rather than bounce through Keycloak again. If
    // this ever starts redirecting, the console has lost its token between
    // page loads and every operator sees a login round-trip per navigation.
    const navigations = [];
    page.on("framenavigated", (f) => { if (f === page.mainFrame()) navigations.push(f.url()); });

    await page.reload();
    await expect(page).toHaveURL(/#\/overview$/);
    await expectAuthenticatedShell(page);

    expect(
      navigations.filter((u) => u.includes("/protocol/openid-connect/auth")),
      "the reload bounced through Keycloak — the session did not survive it",
    ).toHaveLength(0);
  });
});
