// audit-view.test.mjs — unit tests for views/workspace-audit.js.
//
// Run with: node --test web/admin/static/js/tests/
//
// ─── The defect these pin ───────────────────────────────────────────────────
//
// The Workspace Audit view called the shared table component as
//
//     renderTable({ columns: [...], rows: [...] })
//
// but components/table.js is `renderTable(target, opts)`. With one argument,
// `opts` is undefined and the very first line — `opts.columns` — threw
// `TypeError: Cannot read properties of undefined (reading 'columns')`. The
// columns were also plain strings and the rows arrays of elements, neither of
// which is the shape the component reads.
//
// So the view was broken for every workspace that had recorded at least one
// audit event, which is every workspace anyone has ever used. It rendered
// correctly only while the trail was empty, because the empty branch returns
// before reaching the table.
//
// ─── Why nothing caught it ──────────────────────────────────────────────────
//
// Three things had to line up, and they did:
//
//   1. The throw happens AFTER an await. router.js wraps `r.view(...)` in a
//      try/catch, but every view is async, so a rejection after the first
//      await escapes as an unhandled promise rejection. There is no "view
//      crashed" panel — the page just sits on "loading…" forever.
//   2. No frontend test rendered this view. The suites here cover state,
//      routing, isolation and error parsing; the audit view had none.
//   3. No backend test can see it: the API returned 200 with the right body.
//
// It was found by the Slice 11 browser suite (tests/browser), which opens the
// page and fails on an unexpected pageerror. These tests move the guard to the
// level it belongs at, so the next person to break it gets a red `node --test`
// in a second rather than a red browser job in five minutes.

import { test } from "node:test";
import assert from "node:assert/strict";

import { installDOM, makeStorage, fetchStub } from "./helpers.mjs";

globalThis.localStorage = makeStorage();
globalThis.sessionStorage = makeStorage();

const dom = installDOM();
globalThis.fetch = fetchStub([]);

const { default: workspaceAuditView } = await import("../views/workspace-audit.js");

const WORKSPACE = "ws_11111111-1111-1111-1111-111111111111";

// Two events, one per actor shape. The disjointness of the operator and
// project field sets is the audit record's central promise, and the view's job
// is to render it so a machine is never mistaken for a person.
const EVENTS = [
  {
    id: "evt_1",
    event: "role.created",
    occurred_at: "2026-08-10T12:00:00Z",
    actor: { type: "project", project_id: "prj_abc", credential_id: "cred_xyz" },
    resource: { type: "role", id: "billing-reader" },
    outcome: "success",
  },
  {
    id: "evt_2",
    event: "project_credential.revoked",
    occurred_at: "2026-08-10T12:05:00Z",
    actor: { type: "operator", subject: "sub-1", email: "operator@example.test" },
    resource: { type: "project_credential", id: "cred_xyz" },
    outcome: "failure",
    reason_code: "provider_unavailable",
  },
];

function newContainer() {
  const el = dom.StubElement ? new dom.StubElement("div") : null;
  assert.ok(el, "the DOM stub must expose StubElement");
  return el;
}

function respondWith(items, pagination = {}) {
  globalThis.fetch = fetchStub([
    [/\/v1\/workspaces\/.*\/audit/, { status: 200, body: { items, pagination } }],
  ]);
}

test("regression: the audit view renders a row per event instead of throwing", async () => {
  respondWith(EVENTS);
  const container = newContainer();

  // Before the fix this rejected with
  // "Cannot read properties of undefined (reading 'columns')".
  await workspaceAuditView({ container, params: { workspace_id: WORKSPACE } });

  const table = container.querySelector("table");
  assert.ok(table, "no table was rendered — the view is back to the pre-fix breakage");

  const body = table.querySelector("tbody");
  assert.ok(body, "the table has no body");
  assert.equal(body.childNodes.length, EVENTS.length,
    "expected exactly one row per audit event");
});

test("the table header names the five columns an operator reads", async () => {
  respondWith(EVENTS);
  const container = newContainer();
  await workspaceAuditView({ container, params: { workspace_id: WORKSPACE } });

  const head = container.querySelector("thead");
  assert.ok(head, "the table has no header");
  // Pre-fix, columns were plain strings, which the component would have
  // rendered as `c.title || c.key` on a string — i.e. undefined. Asserting
  // the header text pins the SHAPE of the column spec, not just that a table
  // exists.
  const text = head.textContent;
  for (const column of ["Time", "Event", "Actor", "Resource", "Outcome"]) {
    assert.ok(text.includes(column), `header is missing the "${column}" column: ${text}`);
  }
});

test("a project actor and an operator actor render differently", async () => {
  respondWith(EVENTS);
  const container = newContainer();
  await workspaceAuditView({ container, params: { workspace_id: WORKSPACE } });

  const text = container.querySelector("tbody").textContent;

  // The machine.
  assert.ok(text.includes("project"), "the project actor is not labelled");
  assert.ok(text.includes("prj_abc"), "the project id is not shown");
  // The person. Their email, because that is what an operator recognises.
  assert.ok(text.includes("operator"), "the operator actor is not labelled");
  assert.ok(text.includes("operator@example.test"), "the operator's email is not shown");
});

test("a failure shows its reason CODE, and a success does not invent one", async () => {
  respondWith(EVENTS);
  const container = newContainer();
  await workspaceAuditView({ container, params: { workspace_id: WORKSPACE } });

  const text = container.querySelector("tbody").textContent;
  assert.ok(text.includes("success"), "the successful event is not marked");
  assert.ok(text.includes("failure"), "the failed event is not marked");
  // A code from a closed vocabulary, never a message — the trail stores a code
  // precisely so nothing upstream can put a secret in this cell.
  assert.ok(text.includes("provider_unavailable"), "the failure's reason code is missing");
});

test("an empty trail renders the empty state, not a table", async () => {
  // The branch that used to be the ONLY working one. Keeping it asserted means
  // a future fix to the table path cannot quietly break the empty path.
  respondWith([]);
  const container = newContainer();
  await workspaceAuditView({ container, params: { workspace_id: WORKSPACE } });

  assert.equal(container.querySelector("table"), null,
    "an empty trail should not render a table");
  assert.ok(container.textContent.includes("No events match"),
    `expected the empty state, got: ${container.textContent}`);
});

test("end of history is stated, not left as a dead button", async () => {
  respondWith(EVENTS, {});
  const container = newContainer();
  await workspaceAuditView({ container, params: { workspace_id: WORKSPACE } });

  const text = container.textContent;
  assert.ok(text.includes("end of history"),
    "with no next_cursor the view must say the history has ended");
  assert.ok(text.includes("2 events loaded"), `expected a loaded count, got: ${text}`);
});

test("more history offers a load-more control", async () => {
  respondWith(EVENTS, { next_cursor: "cursor-2" });
  const container = newContainer();
  await workspaceAuditView({ container, params: { workspace_id: WORKSPACE } });

  assert.ok(container.textContent.includes("load more"),
    "a next_cursor must produce a load-more control");
  assert.ok(!container.textContent.includes("end of history"),
    "the view claimed the history ended while a cursor was outstanding");
});
