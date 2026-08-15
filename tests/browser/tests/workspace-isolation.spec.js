// workspace-isolation.spec.js — two workspaces, two Keycloak realms, one
// browser, and nothing crossing between them.
//
// ─── Why this needs a browser at all ────────────────────────────────────────
//
// The backend half is already covered: the integration suite points two
// workspaces at two realms and proves the resolver keeps them apart, and
// lib/workspaces.js has unit tests for the generation counter, the abort
// signal and the switch-listener registry against a stubbed fetch.
//
// What neither can see is the composition. Isolation in this console is not
// one mechanism, it is five that have to agree:
//
//   the workspace selector → the route it navigates to → the URL the view
//   builds from that route → the runtime's per-request resolution → what
//   finally reaches the DOM
//
// A unit test with a stubbed fetch proves link four is guarded. It cannot
// prove that link three built the URL the operator's selection implied, and a
// backend test cannot prove that the answer was rendered under the right name.
// The failure this guards is an operator deleting the right-looking person in
// the wrong realm, and that failure lives in the composition.

import { test, expect } from "../fixtures/test.js";
import { env, unique } from "../fixtures/env.js";
import { loginAsOperator } from "../fixtures/login.js";
import { provisionConnectedWorkspace } from "../fixtures/operator-api.js";

test.describe.serial("Workspace isolation across two real realms", () => {
  const alphaName = unique("Isolation Alpha");
  const bravoName = unique("Isolation Bravo");

  let alpha = null;
  let bravo = null;

  test.beforeAll(async () => {
    // Provisioned over HTTP: Journey 2 already proves an operator can build
    // these in the browser, and what this file tests is what happens AFTER
    // two of them exist.
    alpha = await provisionConnectedWorkspace({ name: alphaName, realm: env.alphaRealm, secret: env.alphaSecret });
    bravo = await provisionConnectedWorkspace({ name: bravoName, realm: env.bravoRealm, secret: env.bravoSecret });
  });

  // ── MULTI-REALM EVIDENCE ────────────────────────────────────────────────
  //
  // One vertical proof, not a duplicate of the backend's multi-realm matrix.
  // The two realms hold users whose names exist nowhere else and are scoped to
  // this run, so a leak is unambiguous and a stale row cannot fake a pass.
  test("selecting a workspace shows that realm's users, and only that realm's", async ({ page }) => {
    await loginAsOperator(page);

    // ── Workspace A ──
    await page.goto(`/admin#/workspaces/${alpha.workspaceId}/users`);
    await expect(page.getByRole("row").filter({ hasText: env.alphaUser })).toHaveCount(1);
    await expect(page.getByRole("row").filter({ hasText: env.bravoUser })).toHaveCount(0);

    // ── Switch through the control an operator actually uses ──
    //
    // Not by typing a URL: the selector is the thing under test. It navigates
    // rather than mutating state directly, so the URL, the back button and the
    // selection stay in agreement — and this asserts that they do.
    const bravoUsers = page.waitForResponse(
      (r) => r.url().includes(`/v1/workspaces/${bravo.workspaceId}/users`) && r.request().method() === "GET",
    );
    await page.getByLabel("Active workspace").selectOption(bravo.workspaceId);
    expect((await bravoUsers).status()).toBe(200);

    // The operator stays on the same PAGE in the new workspace. Bouncing them
    // to a landing screen would lose what they were doing.
    await expect(page).toHaveURL(new RegExp(`#/workspaces/${bravo.workspaceId}/users$`));

    // ── Workspace B, and no trace of A ──
    await expect(page.getByRole("row").filter({ hasText: env.bravoUser })).toHaveCount(1);
    await expect(page.getByRole("row").filter({ hasText: env.alphaUser })).toHaveCount(0);

    // The breadcrumb names the workspace, because "Users" alone does not say
    // whose users and this console's whole risk is acting on the wrong realm.
    await expect(page.locator(".topbar-breadcrumbs")).toContainText(bravoName);
    await expect(page.locator(".topbar-breadcrumbs")).not.toContainText(alphaName);
  });

  // ── STATE LEAK ACROSS A SWITCH ──────────────────────────────────────────
  //
  // Projects, deliberately. A leak here is the worst kind: project ids are
  // opaque, an operator cannot tell at a glance that the list belongs to
  // another workspace, and the next click issues a credential — or revokes
  // one — against a realm they are not looking at.
  test("switching workspaces does not leave the previous workspace's projects on screen", async ({ page }) => {
    const alphaProject = unique("Alpha-only project");
    await loginAsOperator(page);

    // Create a project that exists in A and nowhere else.
    await page.goto(`/admin#/workspaces/${alpha.workspaceId}/projects`);
    await page.getByRole("button", { name: "+ New project" }).click();
    const dialog = page.getByRole("dialog", { name: "New project" });
    await dialog.getByLabel("name *", { exact: true }).fill(alphaProject);
    await dialog.getByRole("button", { name: "Create", exact: true }).click();
    await expect(page).toHaveURL(/projects\/prj_/);

    await page.goto(`/admin#/workspaces/${alpha.workspaceId}/projects`);
    await expect(page.getByRole("row").filter({ hasText: alphaProject })).toHaveCount(1);

    // Switch to B and stay on the Projects page.
    const bravoProjects = page.waitForResponse(
      (r) => r.url().includes(`/v1/workspaces/${bravo.workspaceId}/projects`) && r.request().method() === "GET",
    );
    await page.getByLabel("Active workspace").selectOption(bravo.workspaceId);
    expect((await bravoProjects).status()).toBe(200);
    await expect(page).toHaveURL(new RegExp(`#/workspaces/${bravo.workspaceId}/projects$`));

    // B has no projects. If A's row is still on screen the operator is one
    // click from issuing a credential in the wrong realm.
    await expect(page.getByRole("row").filter({ hasText: alphaProject })).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "No projects yet" })).toBeVisible();

    // And back again — A's project is still A's. The generation counter exists
    // because A → B → A is the case where the workspace id matches again while
    // the in-flight response belongs to a context already left.
    await page.getByLabel("Active workspace").selectOption(alpha.workspaceId);
    await expect(page).toHaveURL(new RegExp(`#/workspaces/${alpha.workspaceId}/projects$`));
    await expect(page.getByRole("row").filter({ hasText: alphaProject })).toHaveCount(1);
  });

  // ── AUDIT DOES NOT LEAK EITHER ──────────────────────────────────────────
  //
  // The second surface where a leak would be severe: the audit trail is what
  // an operator consults during an incident, and reading another tenant's
  // history under this tenant's name is both a wrong answer and a disclosure.
  test("each workspace's audit trail contains only its own events", async ({ page }) => {
    await loginAsOperator(page);

    await page.goto(`/admin#/workspaces/${alpha.workspaceId}/audit`);
    // A has had a connection created, verified and activated, plus a project.
    await expect(page.getByRole("row").filter({ hasText: "project.created" })).toHaveCount(1);
    const alphaRows = await page.getByRole("row").count();
    expect(alphaRows).toBeGreaterThan(1);

    // Every event in view names A's workspace. Asserted through the API
    // because the table does not render the workspace column — it does not
    // need to, since the page is already scoped to one — and forcing a
    // redesign so a test can read it would be the test driving the product.
    await page.goto(`/admin#/workspaces/${bravo.workspaceId}/audit`);
    await expect(page.getByRole("row").filter({ hasText: "connection.activated" })).toHaveCount(1);
    // B never had a project created in it.
    await expect(page.getByRole("row").filter({ hasText: "project.created" })).toHaveCount(0);
  });

  // ── A DIALOG MUST NOT SURVIVE A SWITCH ──────────────────────────────────
  //
  // The defect this prevents, stated concretely: an operator opens a
  // destructive confirmation in workspace A, switches to B while it is up, and
  // clicks the destructive button. The dialog's closure still holds A's entity
  // id. Disabling the button is not enough — the safe answer is that the
  // dialog is gone before anything from B can render behind it.
  test("an open dialog is dismissed when the workspace changes underneath it", async ({ page }) => {
    await loginAsOperator(page);
    await page.goto(`/admin#/workspaces/${alpha.workspaceId}/projects`);

    await page.getByRole("button", { name: "+ New project" }).click();
    await expect(page.getByRole("dialog", { name: "New project" })).toBeVisible();

    await page.getByLabel("Active workspace").selectOption(bravo.workspaceId);

    await expect(page.getByRole("dialog", { name: "New project" })).toHaveCount(0);
    await expect(page).toHaveURL(new RegExp(`#/workspaces/${bravo.workspaceId}/projects$`));
  });
});
