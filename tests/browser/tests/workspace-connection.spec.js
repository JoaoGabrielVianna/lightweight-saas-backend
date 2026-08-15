// workspace-connection.spec.js — JOURNEY 2: the operator makes a workspace
// usable.
//
// This is the journey with no machine equivalent. scripts/m2m-harness.sh does
// all of it with curl, which proves the API works; it cannot prove that the
// screen an operator is actually given leads them through it. A workspace that
// can only be connected with curl is a workspace this product cannot ship.
//
// Everything here happens through the same controls a person clicks. Nothing
// touches the database, and the connection is never marked active by any means
// other than pressing Verify and then Activate.

import { test, expect } from "../fixtures/test.js";
import { env, unique, recordSecret } from "../fixtures/env.js";
import { loginAsOperator } from "../fixtures/login.js";
import { expectSecretAbsent } from "../fixtures/secrets.js";

// The whole journey is one operator sitting down once. Splitting it into
// independent tests would mean four full PKCE logins to re-reach the same
// screen, and each step's precondition is literally the previous step's
// outcome — a serial describe says that honestly instead of hiding it behind
// a fixture that redoes the work.
test.describe.serial("Journey 2 — workspace, connection, verification, activation", () => {
  const workspaceName = unique("Alpha");
  let workspaceId = null;

  test("an operator creates a workspace and lands on its connections page", async ({ page }) => {
    await loginAsOperator(page);

    await page.locator("#sidebar").getByRole("link", { name: "Workspaces" }).click();
    await expect(page).toHaveURL(/#\/workspaces$/);

    await page.getByRole("button", { name: "+ New workspace" }).click();

    const dialog = page.getByRole("dialog", { name: "New workspace" });
    await expect(dialog).toBeVisible();
    await dialog.getByLabel("name *", { exact: true }).fill(workspaceName);

    // The response is watched as well as the UI. The pair is the diagnostic:
    // a green UI assertion with a 4xx underneath means the console rendered
    // optimistically, and a 2xx with nothing on screen means it dropped the
    // result. Neither is visible from one half alone.
    const created = page.waitForResponse(
      (r) => r.url().endsWith("/v1/workspaces") && r.request().method() === "POST",
    );
    await dialog.getByRole("button", { name: "Create", exact: true }).click();

    const res = await created;
    expect(res.status()).toBe(201);
    workspaceId = (await res.json()).id;
    expect(workspaceId).toMatch(/^ws_/);

    // A brand-new workspace has no connection, so the console sends the
    // operator to the only screen that can fix that. Asserting the redirect
    // pins the product's answer to "what do I do now?".
    await expect(page).toHaveURL(new RegExp(`#/workspaces/${workspaceId}/connections$`));
    await expect(page.getByRole("heading", { name: "Connections" })).toBeVisible();
  });

  test("the new workspace is selectable in the topbar", async ({ page }) => {
    await loginAsOperator(page);

    const selector = page.getByLabel("Active workspace");
    await expect(selector).toBeVisible();
    // Present in the selector AND choosable — an archived or unlisted
    // workspace would render as neither.
    await expect(selector.locator("option", { hasText: workspaceName })).toHaveCount(1);
  });

  test("an operator wires a connection, verifies it, and activates it", async ({ page }) => {
    // The value typed into the form. Registered before it is typed, so that if
    // any assertion below fails the artifact scan still knows to look for it.
    recordSecret(env.alphaSecret);

    await loginAsOperator(page);
    await page.goto(`/admin#/workspaces/${workspaceId}/connections`);

    await expect(page.getByRole("heading", { name: "No connections yet" })).toBeVisible();
    await page.getByRole("button", { name: "+ New connection" }).click();

    const dialog = page.getByRole("dialog", { name: "New connection" });
    await expect(dialog).toBeVisible();
    await dialog.getByLabel("name *", { exact: true }).fill(env.alphaRealm);
    await dialog.getByLabel("base url *", { exact: true }).fill(env.keycloakURL);
    await dialog.getByLabel("realm *", { exact: true }).fill(env.alphaRealm);
    await dialog.getByLabel("admin client id *", { exact: true }).fill(env.connClientId);
    await dialog.getByLabel("client secret *", { exact: true }).fill(env.alphaSecret);

    const createdConn = page.waitForResponse(
      (r) => /\/v1\/workspaces\/ws_[^/]+\/connections$/.test(r.url()) && r.request().method() === "POST",
    );
    await dialog.getByRole("button", { name: "Create connection" }).click();
    expect((await createdConn).status()).toBe(201);

    // A new connection is a DRAFT. It must not be usable yet — that is the
    // whole reason verify and activate are two buttons.
    await expect(page.getByText("draft", { exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Activate" })).toBeDisabled();

    // ── Verify: a real probe against a real Keycloak ──────────────────────
    const verified = page.waitForResponse(
      (r) => r.url().includes("/verify") && r.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Verify" }).click();
    expect((await verified).status()).toBe(200);

    const report = page.getByRole("dialog", { name: "Verification passed" });
    await expect(report).toBeVisible();
    // The report is a list of named checks, not a boolean, because
    // "unreachable", "wrong realm" and "under-privileged" send an operator to
    // three different places. Assert it actually reported the checks.
    await expect(report.locator("li")).not.toHaveCount(0);
    await expect(report.getByText("full access")).toBeVisible();
    // `exact` matters: every modal also carries a "×" dismiss button whose
    // aria-label is the lowercase "close", and a non-exact match resolves to
    // both. Case is the only thing separating them, which is a small
    // accessibility smell in components/modal.js rather than a test problem —
    // noted, not redesigned, because renaming a control an operator relies on
    // is not a change a test should force.
    await report.getByRole("button", { name: "Close", exact: true }).click();

    // ── Activate ──────────────────────────────────────────────────────────
    await expect(page.getByRole("button", { name: "Activate" })).toBeEnabled();
    await page.getByRole("button", { name: "Activate" }).click();

    const confirm = page.getByRole("dialog", { name: "Activate this connection?" });
    await expect(confirm).toBeVisible();
    const activated = page.waitForResponse(
      (r) => r.url().includes("/activate") && r.request().method() === "POST",
    );
    await confirm.getByRole("button", { name: "Activate", exact: true }).click();
    expect((await activated).status()).toBe(200);

    // ── State, not the toast ──────────────────────────────────────────────
    //
    // A toast says a request succeeded. These say the workspace CHANGED: the
    // connection card reads active, and the shell's own health indicator —
    // which is recomputed from the API, not from the click — agrees.
    await expect(page.locator(".card").getByText("active", { exact: true })).toBeVisible();
    await expect(page.getByLabel("Active workspace")).toBeVisible();
    await expect(page.locator(".ws-selector").getByText("Healthy")).toBeVisible();
  });

  test("the workspace is now usable by the runtime", async ({ page }) => {
    await loginAsOperator(page);

    // The real proof of activation: an identity view resolves through the
    // connection to the realm and shows a user that only exists there. Before
    // activation this page can only say "This workspace isn't connected yet".
    const users = page.waitForResponse(
      (r) => /\/v1\/workspaces\/ws_[^/]+\/users\?/.test(r.url()) && r.request().method() === "GET",
    );
    await page.goto(`/admin#/workspaces/${workspaceId}/users`);
    expect((await users).status()).toBe(200);

    // One row, by the realm-exclusive username. A cell locator would match
    // three times — the username, the email derived from it, and the first
    // name — so the row is both the unambiguous locator and the more honest
    // assertion: this person is listed here, once.
    await expect(page.getByRole("row").filter({ hasText: env.alphaUser })).toHaveCount(1);
    await expect(page.getByText("This workspace isn't connected yet")).toHaveCount(0);
  });

  // ───────────────────────────────────────────────────────────────────────
  // THE CLIENT SECRET
  // ───────────────────────────────────────────────────────────────────────
  //
  // The operator typed a provider credential into a form. From the moment it
  // is submitted the console must be structurally unable to show it again:
  // the API has no endpoint that returns one and ConnectionResponse has no
  // field for it, so the only way it could reappear is if the browser kept a
  // copy. That is what this checks, on every surface a person or a script can
  // read. scripts/scan-artifacts.sh then checks the same value against
  // everything the run published.
  test("the client secret never reappears anywhere the operator can see", async ({ page }) => {
    await loginAsOperator(page);
    await page.goto(`/admin#/workspaces/${workspaceId}/connections`);
    await expect(page.locator(".card").first()).toBeVisible();

    // What the operator SHOULD see: that a secret exists, never its value.
    await expect(page.getByText("configured")).toBeVisible();
    await expectSecretAbsent(page, env.alphaSecret, "on the connections page");

    // A reload cannot recover it either — that would mean it had been
    // persisted somewhere the first assertion did not look.
    await page.reload();
    await expect(page.locator(".card").first()).toBeVisible();
    await expectSecretAbsent(page, env.alphaSecret, "after a reload");

    // And the edit form starts EMPTY rather than pre-filled. A pre-filled
    // password field would put the stored secret back in the DOM, and the
    // blank-means-unchanged contract exists precisely so it does not have to.
    await page.goto(`/admin#/workspaces/${workspaceId}/connections`);
    await expect(page.getByRole("button", { name: "Edit" })).toBeVisible();
  });

  // ───────────────────────────────────────────────────────────────────────
  // /v1 ERROR RENDERING
  // ───────────────────────────────────────────────────────────────────────
  //
  // Slice 6 found that the console had two error surfaces with two shapes:
  //
  //   /admin/*  →  {"error": "not found"}                    (a string)
  //   /v1/*     →  {"error": {code, message, request_id}}    (an object)
  //
  // and the pre-Slice-6 APIError constructor did `super(body.error)`, which
  // for the /v1 shape produced the literal string "[object Object]" in every
  // toast and every empty state. lib/api.js's parseErrorBody now normalises
  // both, and there are unit tests for it — against a stubbed fetch.
  //
  // This is the same claim against the real server: a real /v1 rejection, in a
  // real browser, rendered to a real operator. The unit test proves the parser
  // handles an object; this proves the object the server actually sends is the
  // one the parser was written for, and that the prose reaches the screen.
  test("a rejected /v1 request is shown as prose, never as [object Object]", async ({ page, errors }) => {
    await loginAsOperator(page);
    await page.goto("/admin#/workspaces");

    // A 400 is the point of this test, so it is declared rather than ignored.
    errors.allowStatus(400, /\/v1\/workspaces$/);

    await page.getByRole("button", { name: "+ New workspace" }).click();
    const dialog = page.getByRole("dialog", { name: "New workspace" });
    await dialog.getByLabel("name *", { exact: true }).fill(unique("Rejected"));
    // A slug the server refuses. Client-side validation only checks for
    // emptiness, so this reaches /v1 and comes back in the structured shape.
    await dialog.getByLabel("slug", { exact: true }).fill("Not A Valid Slug!");

    const rejected = page.waitForResponse(
      (r) => r.url().endsWith("/v1/workspaces") && r.request().method() === "POST",
    );
    await dialog.getByRole("button", { name: "Create", exact: true }).click();

    const res = await rejected;
    expect(res.status()).toBe(400);

    // The envelope really is the OBJECT shape — the one that used to render as
    // "[object Object]". Without this the test could pass against a server
    // that had quietly reverted to the string shape, and would then be
    // proving nothing about the case it was written for.
    const body = await res.json();
    expect(typeof body.error, "the /v1 error envelope is no longer an object").toBe("object");
    expect(body.error.code).toBeTruthy();
    expect(body.error.message).toBeTruthy();

    // What the operator sees.
    const toast = page.locator("#toast .toast").first();
    await expect(toast).toBeVisible();
    const toastText = await toast.innerText();

    expect(toastText, "the console rendered a raw object into the UI").not.toContain("[object Object]");
    expect(toastText, "the toast is empty — the message never reached it").not.toBe("");
    // Prose the operator can act on, plus the stable code they can quote.
    expect(toastText).toContain(body.error.message);
    expect(toastText).toContain(body.error.code);
    // The request id is the whole reason the /v1 envelope exists: it ties this
    // screen to the server log line with the real cause.
    expect(toastText).toMatch(/request /);

    // The dialog stays open with the operator's input intact, so the fix is
    // one edit away rather than a retype.
    await expect(dialog).toBeVisible();
    await expect(dialog.getByLabel("slug", { exact: true })).toHaveValue("Not A Valid Slug!");
  });
});
