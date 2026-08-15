// project-credential.spec.js — JOURNEYS 3, 4, 5 and 6, which are one story.
//
//   an operator creates a Project in the browser
//     → issues it a Credential, and sees the secret exactly once
//       → an external backend uses that secret over plain HTTP
//         → its mutation appears in the console's Audit view, as a machine
//           → the operator revokes the credential in the browser
//             → the machine is refused on its very next request
//               → and that revocation appears in Audit, as a person
//
// Splitting this into four files would mean four ways to smuggle the secret
// between them, and the secret is the one thing that must stay in one test
// process's memory and nowhere else. It is one describe.serial, and the steps
// are named so a CI log reads as the story above.
//
// The workspace and its connection are provisioned over HTTP rather than
// clicked: Journey 2 already proves an operator can do that in the browser,
// and repeating it here would make this file a slower copy of that one. What
// is proven HERE is proven in the browser.

import { test, expect } from "../fixtures/test.js";
import { env, unique, recordSecret } from "../fixtures/env.js";
import { loginAsOperator } from "../fixtures/login.js";
import { expectSecretAbsent } from "../fixtures/secrets.js";
import { apiAs, provisionConnectedWorkspace } from "../fixtures/operator-api.js";

// actorCellOf — the Actor column of an audit row.
//
// Scoped rather than asserted against the whole row, and the reason is a trap
// worth naming: the revocation event's RESOURCE type is the literal string
// "project_credential", so a row-level `not.toContainText("project")` fails on
// a perfectly correct row. Every actor assertion below therefore looks at the
// one cell that is about the actor.
//
// Column order is Time · Event · Actor · Resource · Outcome, so index 2.
function actorCellOf(row) {
  return row.getByRole("cell").nth(2);
}

test.describe.serial("Journeys 3–6 — project, credential, machine use, audit, revocation", () => {
  const projectName = unique("Billing worker");
  const credentialLabel = unique("billing staging");
  const roleName = unique("e2e-role").toLowerCase();

  let workspaceId = null;
  let projectId = null;

  // THE SECRET. Held in this process's memory for the length of the file and
  // written nowhere: not to disk, not to a fixture file, not to console.log.
  // recordSecret() registers it with the artifact scanner, which stores it
  // OUTSIDE the artifact tree precisely so the scanner is not itself the leak.
  let credentialSecret = null;
  let credentialKeyPrefix = null;

  test.beforeAll(async () => {
    const ws = await provisionConnectedWorkspace({
      name: unique("Credentials"),
      realm: env.alphaRealm,
      secret: env.alphaSecret,
    });
    workspaceId = ws.workspaceId;
  });

  // ── JOURNEY 3a ──────────────────────────────────────────────────────────
  test("an operator creates a project", async ({ page }) => {
    await loginAsOperator(page);
    await page.goto(`/admin#/workspaces/${workspaceId}/projects`);

    await page.getByRole("button", { name: "+ New project" }).click();
    const dialog = page.getByRole("dialog", { name: "New project" });
    await dialog.getByLabel("name *", { exact: true }).fill(projectName);

    const created = page.waitForResponse(
      (r) => /\/v1\/workspaces\/ws_[^/]+\/projects$/.test(r.url()) && r.request().method() === "POST",
    );
    await dialog.getByRole("button", { name: "Create", exact: true }).click();

    const res = await created;
    expect(res.status()).toBe(201);
    projectId = (await res.json()).id;
    expect(projectId).toMatch(/^prj_/);

    // A project with no credential does nothing, so the console goes straight
    // to the one screen that can fix that.
    await expect(page).toHaveURL(new RegExp(`projects/${projectId}$`));
    await expect(page.getByRole("button", { name: "+ New credential" })).toBeVisible();
  });

  // ── JOURNEY 3b ──────────────────────────────────────────────────────────
  test("an operator issues a credential with explicitly chosen scopes", async ({ page }) => {
    await loginAsOperator(page);
    await page.goto(`/admin#/workspaces/${workspaceId}/projects/${projectId}`);

    await page.getByRole("button", { name: "+ New credential" }).click();
    const dialog = page.getByRole("dialog", { name: new RegExp(`New credential for ${projectName}`) });
    await expect(dialog).toBeVisible();

    await dialog.getByLabel("label *", { exact: true }).fill(credentialLabel);

    // NOTHING is pre-selected, by design — a credential's power is always an
    // explicit choice. Asserting that first means a future default would fail
    // here rather than quietly widen every credential this console mints.
    const allBoxes = dialog.locator('input[type="checkbox"]');
    await expect(allBoxes.first()).not.toBeChecked();
    expect(await dialog.locator('input[type="checkbox"]:checked').count()).toBe(0);

    // Exactly what this backend needs to do its job: read users, and manage
    // realm roles. audit:read is deliberately NOT granted — the audit journey
    // below reads the trail as the OPERATOR, in the console, which is how an
    // operator reads it. Granting a machine the whole workspace's history
    // because a test found it convenient is how over-scoped credentials get
    // normalised.
    for (const scope of ["users:read", "roles:read", "roles:write"]) {
      await dialog.locator(`input[type="checkbox"][value="${scope}"]`).check();
    }
    await expect(dialog.locator('input[type="checkbox"][value="audit:read"]')).not.toBeChecked();

    const created = page.waitForResponse(
      (r) => /\/credentials$/.test(r.url()) && r.request().method() === "POST",
    );
    await dialog.getByRole("button", { name: "Create credential" }).click();
    expect((await created).status()).toBe(201);

    // ── THE ONE-TIME SECRET ───────────────────────────────────────────────
    //
    // Its own modal, not a toast: the operator needs time and focus for a
    // value they cannot ask for twice.
    const secretDialog = page.getByRole("dialog", { name: "Copy this credential now" });
    await expect(secretDialog).toBeVisible();
    await expect(secretDialog.getByText("one time")).toBeVisible();
    await expect(secretDialog).toContainText("will not be shown again");

    credentialSecret = await secretDialog.locator("input.secret-field").inputValue();
    expect(credentialSecret, "the one-time modal displayed no secret").toMatch(/^lw_sk_/);
    recordSecret(credentialSecret);

    // The scopes granted are restated on the same screen, so the operator can
    // see what they just handed out while they still have the key in hand.
    for (const scope of ["users:read", "roles:read", "roles:write"]) {
      await expect(secretDialog.getByText(scope, { exact: true })).toBeVisible();
    }
    await expect(secretDialog.getByText("audit:read", { exact: true })).toHaveCount(0);

    await secretDialog.getByRole("button", { name: "I have stored it" }).click();

    credentialKeyPrefix = credentialSecret.split("_")[2];
    expect(credentialKeyPrefix).toBeTruthy();
  });

  // ── THE ONE-TIME CONTRACT ───────────────────────────────────────────────
  //
  // "Shown once" is a promise about every later moment, so it is tested by
  // trying the things an operator would actually try to get it back.
  test("the secret is gone the moment the modal closes, and stays gone", async ({ page }) => {
    await loginAsOperator(page);
    await page.goto(`/admin#/workspaces/${workspaceId}/projects/${projectId}`);
    await expect(page.getByText(credentialLabel)).toBeVisible();

    // What the credential list SHOULD show: enough to recognise the key,
    // never enough to use it.
    await expect(page.getByText(`lw_sk_${credentialKeyPrefix}…`)).toBeVisible();
    await expectSecretAbsent(page, credentialSecret, "on the credential list");

    // Navigate away and come back — the console re-fetches, and the API has
    // no endpoint that could return the secret even if the view asked.
    await page.goto(`/admin#/workspaces/${workspaceId}/projects`);
    await expect(page.getByText(projectName)).toBeVisible();
    await page.goto(`/admin#/workspaces/${workspaceId}/projects/${projectId}`);
    await expect(page.getByText(credentialLabel)).toBeVisible();
    await expectSecretAbsent(page, credentialSecret, "after navigating away and back");

    // A reload cannot recover it either.
    await page.reload();
    await expect(page.getByText(credentialLabel)).toBeVisible();
    await expectSecretAbsent(page, credentialSecret, "after a reload");
  });

  // ── THE CREDENTIAL MUST REALLY WORK ─────────────────────────────────────
  //
  // Everything above is the console's claim. This is the claim being true:
  // the value a browser displayed, used by something that is not a browser,
  // over the public HTTP contract, with no knowledge of realms or Keycloak.
  test("the browser-created credential works from outside, over plain HTTP", async () => {
    const res = await apiAs(credentialSecret, "GET", `/v1/workspaces/${workspaceId}/users?max=50`);

    expect(res.status, `the credential the console minted was refused: ${JSON.stringify(res.body)}`).toBe(200);
    const usernames = (res.body.users || []).map((u) => u.username);
    // Resolved through the workspace's connection to the right realm — this
    // user exists only in the alpha realm.
    expect(usernames).toContain(env.alphaUser);
    expect(usernames).not.toContain(env.bravoUser);
  });

  test("the credential holds only the scopes the operator ticked", async () => {
    // audit:read was deliberately left unchecked in the browser. If this
    // returned 200 the console's scope checkboxes would be decoration.
    const res = await apiAs(credentialSecret, "GET", `/v1/workspaces/${workspaceId}/audit`);
    expect(res.status).toBe(403);
    expect(res.body?.error?.code).toBeTruthy();
  });

  // ── JOURNEY 4 — an audited mutation, performed by the machine ───────────
  let mutationRequestId = null;

  test("the machine performs a real mutation", async () => {
    const res = await apiAs(credentialSecret, "POST", `/v1/workspaces/${workspaceId}/roles`, {
      name: roleName,
      description: "created by the Slice 11 browser e2e",
    });

    expect(res.status, `role creation failed: ${JSON.stringify(res.body)}`).toBe(201);
    // The correlation id the /v1 envelope exists for. Captured so the audit
    // assertions below can be tied to THIS request rather than to whatever
    // else happens to be in the trail.
    mutationRequestId = res.requestId;
    expect(mutationRequestId).toBeTruthy();
  });

  // ── JOURNEY 5 — the operator sees it in the console ─────────────────────
  test("the console's Audit view shows the machine's mutation, attributed to the project", async ({ page }) => {
    await loginAsOperator(page);

    const audit = page.waitForResponse(
      (r) => /\/v1\/workspaces\/ws_[^/]+\/audit/.test(r.url()) && r.request().method() === "GET",
    );
    await page.goto(`/admin#/workspaces/${workspaceId}/audit`);
    expect((await audit).status()).toBe(200);

    // The row for the mutation the machine just made.
    const row = page.getByRole("row").filter({ hasText: "role.created" });
    await expect(row).toHaveCount(1);

    // ACTOR — a machine, and specifically THIS project. The audit record keeps
    // the operator and project field sets disjoint so a machine can never be
    // mistaken for a person at a glance; this is that promise on screen.
    const actor = actorCellOf(row);
    await expect(actor).toContainText("project");
    await expect(actor).toContainText(projectId.slice(0, 20));
    // Not an operator. Under a bug that collapsed the two actor shapes, the
    // row would still say "role.created" and still look plausible.
    await expect(actor).not.toContainText("operator");

    // RESOURCE and OUTCOME.
    await expect(row).toContainText("role");
    await expect(row).toContainText("success");

    // The console does not surface request_id in the table — it is in the
    // record, and correlating by it is an API question, not a UI one. Rather
    // than force a redesign for the test's convenience, confirm over the API
    // that the event the UI is showing is the one this run created.
    const viaApi = await apiAs(
      await (await import("../fixtures/operator-api.js")).operatorToken(),
      "GET",
      `/v1/workspaces/${workspaceId}/audit?event=role.created`,
    );
    expect(viaApi.status).toBe(200);
    const event = (viaApi.body.items || [])[0];
    expect(event, "the audit trail has no role.created event").toBeTruthy();
    expect(event.request_id).toBe(mutationRequestId);
    expect(event.actor.type).toBe("project");
    expect(event.actor.project_id).toBe(projectId);
  });

  // ── ONE high-value filter ───────────────────────────────────────────────
  //
  // One, not every combination. The point is that the filter control is wired
  // to the request and the result reaches the table — the repository's own
  // filtering is covered by its own tests, and reproducing that matrix in a
  // browser would be slow and would prove it a second time.
  test("the Audit view's actor filter is wired", async ({ page }) => {
    await loginAsOperator(page);
    await page.goto(`/admin#/workspaces/${workspaceId}/audit`);
    await expect(page.getByRole("row").filter({ hasText: "role.created" })).toHaveCount(1);

    // The workspace's trail contains operator events too (the connection was
    // created, verified and activated), so filtering to `project` must remove
    // them — which is only observable because both kinds are present.
    await expect(page.getByRole("row").filter({ hasText: "connection.activated" })).toHaveCount(1);

    const filtered = page.waitForResponse((r) => r.url().includes("actor_type=project"));
    await page.getByLabel("Actor").selectOption("project");
    expect((await filtered).status()).toBe(200);

    await expect(page.getByRole("row").filter({ hasText: "role.created" })).toHaveCount(1);
    await expect(page.getByRole("row").filter({ hasText: "connection.activated" })).toHaveCount(0);
  });

  // ── JOURNEY 6 — revocation, and its immediate effect ────────────────────
  test("an operator revokes the credential in the browser", async ({ page }) => {
    await loginAsOperator(page);
    await page.goto(`/admin#/workspaces/${workspaceId}/projects/${projectId}`);

    await page.getByRole("button", { name: "revoke" }).click();
    const confirm = page.getByRole("dialog", { name: "Revoke credential?" });
    await expect(confirm).toBeVisible();
    // The dialog names what is being revoked. An operator with several keys
    // needs that, and it is the difference between a safe click and an outage.
    await expect(confirm).toContainText(credentialLabel);

    const revoked = page.waitForResponse(
      (r) => /\/revoke$/.test(r.url()) && r.request().method() === "POST",
    );
    await confirm.getByRole("button", { name: "Revoke", exact: true }).click();
    expect((await revoked).status()).toBe(200);

    // State, not the toast.
    await expect(page.getByRole("row").filter({ hasText: credentialLabel })).toContainText("revoked");
    await expect(page.getByRole("button", { name: "revoke" })).toBeDisabled();
  });

  test("the machine is refused on its very next request", async () => {
    // The same secret this process has been holding all along. Immediacy is
    // the product claim — not "after the cache expires", not "on the next
    // deploy". A credential an operator has revoked is a credential that has
    // stopped working before they have finished reading the toast.
    const res = await apiAs(credentialSecret, "GET", `/v1/workspaces/${workspaceId}/users?max=50`);
    expect(res.status, "a revoked credential was still accepted").toBe(401);
  });

  test("the revocation appears in Audit, attributed to the operator", async ({ page }) => {
    await loginAsOperator(page);
    await page.goto(`/admin#/workspaces/${workspaceId}/audit`);

    const row = page.getByRole("row").filter({ hasText: "project_credential.revoked" });
    await expect(row).toHaveCount(1);

    // BOTH actor models, proven in one workspace's trail: the machine made
    // role.created, the person made this. An audit trail that cannot tell them
    // apart cannot answer the only question anyone brings to it.
    const actor = actorCellOf(row);
    await expect(actor).toContainText("operator");
    await expect(actor).toContainText(`${env.operatorUser}@example.test`);
    await expect(actor).not.toContainText("project");
    await expect(row).toContainText("success");
  });

  test("no artifact and no page ever showed the secret", async ({ page }) => {
    // A last sweep of the console with the credential now revoked. A revoked
    // credential is still a credential, and "we stopped honouring it" is not
    // the same promise as "we never showed it".
    await loginAsOperator(page);
    await page.goto(`/admin#/workspaces/${workspaceId}/projects/${projectId}`);
    await expect(page.getByText(credentialLabel)).toBeVisible();
    await expectSecretAbsent(page, credentialSecret, "after revocation");

    // scripts/scan-artifacts.sh runs after the suite and searches everything
    // published for this exact value; recordSecret() above is what puts it on
    // that list.
  });
});
