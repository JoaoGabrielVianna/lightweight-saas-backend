// logout.spec.js — JOURNEY 7: the operator leaves.
//
// ─── Tested against what the product promises, not against a standard ───────
//
// The console's logout() clears sessionStorage and then navigates to
// Keycloak's end-session endpoint with id_token_hint and
// post_logout_redirect_uri. So this installation DOES perform RP-initiated
// logout, and the test asserts that because it is what the code does — not
// because a specification says it should.
//
// What is deliberately NOT asserted: that the previously-issued access token
// stops being accepted by the API. It does not, and that is a documented,
// accepted trade-off (KI-002): the API validates a JWT's signature and expiry
// and does not consult Keycloak's session store per request, so a bearer token
// outlives the OIDC session until `exp`. Writing a test that expects otherwise
// would be writing a test against a decision this project has already made and
// recorded. If that decision changes, the test to add is the one that fails
// today — and it belongs next to the token validator, not in a browser.

import { test, expect } from "../fixtures/test.js";
import { env } from "../fixtures/env.js";
import { loginAsOperator, expectAuthenticatedShell } from "../fixtures/login.js";

test.describe("Journey 7 — logout", () => {
  test("logging out clears the session and the console is no longer authenticated", async ({ page }) => {
    await loginAsOperator(page);
    await expectAuthenticatedShell(page);

    // The session really is in this tab before we start.
    const before = await page.evaluate(() => ({
      access: sessionStorage.getItem("kc_admin_access_token"),
      refresh: sessionStorage.getItem("kc_admin_refresh_token"),
    }));
    expect(before.access, "no access token in sessionStorage before logout").toBeTruthy();

    // The logout control asks for confirmation with window.confirm, which
    // blocks the page until something answers it. Accepting the dialog is the
    // operator saying yes — not a bypass of the control.
    page.once("dialog", (d) => d.accept());

    // Observed on the REQUEST, not on framenavigated.
    //
    // Keycloak's end-session endpoint answers with a 302 straight back to
    // post_logout_redirect_uri, and a browser commits a redirect chain once,
    // at the final URL — so the navigation log never contains the logout URL
    // even though the browser certainly went there. Watching document requests
    // sees every hop.
    const documentRequests = [];
    page.on("request", (req) => {
      if (req.resourceType() === "document") documentRequests.push(req.url());
    });

    await page.locator("#sidebar").getByTitle("Logout").click();

    // ── The console treats itself as logged out ───────────────────────────
    //
    // The end state is Keycloak's LOGIN page, and the route there is the whole
    // proof: Keycloak's end-session endpoint redirects back to /admin, the
    // console boots, finds no token, and starts a new authorization request.
    // Landing here means the protected console is no longer usable as an
    // authenticated surface — asserted as behaviour rather than by poking at
    // storage.
    await page.waitForURL(/\/protocol\/openid-connect\/auth/, { timeout: 30_000 });

    // ── The browser really went to Keycloak's end-session endpoint ────────
    expect(
      documentRequests.some((u) => u.includes("/protocol/openid-connect/logout")),
      "the console did not perform an RP-initiated logout at Keycloak",
    ).toBeTruthy();
    // With the id token as a hint, so Keycloak knows WHICH session to end
    // rather than being asked to guess or to prompt.
    const logoutUrl = documentRequests.find((u) => u.includes("/protocol/openid-connect/logout"));
    expect(new URL(logoutUrl).searchParams.get("id_token_hint")).toBeTruthy();
    expect(new URL(logoutUrl).searchParams.get("post_logout_redirect_uri")).toContain("/admin");

    // ── Local authenticated state is gone ─────────────────────────────────
    //
    // sessionStorage is per-origin-per-tab and survives a round trip to
    // another origin and back — which is exactly why "we redirected" is not
    // by itself evidence that anything was cleared, and why this has to be
    // read rather than inferred.
    //
    // Read from /admin/config.json rather than from /admin: the console
    // restarts a login the moment it boots without a token, so evaluating on
    // the SPA races a navigation it is guaranteed to lose. config.json is the
    // same origin — so the same sessionStorage — and runs no script.
    await page.goto("/admin/config.json");
    const after = await page.evaluate(() => ({
      access: sessionStorage.getItem("kc_admin_access_token"),
      refresh: sessionStorage.getItem("kc_admin_refresh_token"),
      id: sessionStorage.getItem("kc_admin_id_token"),
    }));
    expect(after.access, "the access token survived logout").toBeNull();
    expect(after.refresh, "the refresh token survived logout").toBeNull();
    expect(after.id, "the id token survived logout").toBeNull();
  });

  test("after logout, opening the console starts a fresh login rather than resuming", async ({ page }) => {
    await loginAsOperator(page);

    page.once("dialog", (d) => d.accept());
    await page.locator("#sidebar").getByTitle("Logout").click();
    await page.waitForURL(/localhost/, { timeout: 30_000 });

    // The real question an operator has: is the console usable by whoever
    // sits down at this machine next? Asserting on storage says the token is
    // gone; this says the CONSOLE behaves as logged out — it refuses to render
    // an authenticated surface and sends the next person to Keycloak.
    await page.goto("/admin");
    await page.waitForURL(
      new RegExp(`${env.keycloakURL.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}/realms/`),
      { timeout: 30_000 },
    );

    // Keycloak's own login form, i.e. credentials are being asked for again.
    // Whether the SSO session also ended is Keycloak's business; what matters
    // here is that this browser is no longer holding a usable console session.
    await expect(page.locator("#username")).toBeVisible({ timeout: 15_000 });
  });
});
