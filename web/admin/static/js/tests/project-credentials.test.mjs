// project-credentials.test.mjs — the console's half of the credential
// contract.
//
// Two properties are worth pinning here, and neither is visual:
//
//   1. the scope vocabulary the console offers is exactly the one the server
//      accepts. A console offering a scope the database rejects produces a
//      failed create an operator cannot act on; a console MISSING one silently
//      makes a capability unreachable.
//   2. nothing is pre-selected. A default scope set is the one nobody revisits,
//      and it would make a credential's power an accident rather than a choice.

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { makeStorage, installDOM } from "./helpers.mjs";

globalThis.localStorage = makeStorage();
globalThis.sessionStorage = makeStorage();
installDOM();
globalThis.window = { location: { hash: "" }, addEventListener() {}, scrollTo() {} };

const { _scopeGroupsForTests } = await import("../views/projects.js");

// The migrations are the language-neutral source of truth: they are where the
// database's CHECK constraint lives, and the Go vocabulary is pinned against
// the same files by TestScopes_MatchTheDatabaseConstraint.
//
// The EFFECTIVE constraint is the last definition across every up migration, in
// version order — not one named file. The vocabulary was introduced in 000005
// and widened by 000006 (`audit:read`), and it will move again; a gate pinned to
// one filename would either break on the next widening or, worse, keep passing
// while checking a definition the database has since replaced.
const MIGRATIONS_DIR = "internal/database/migrations";

function serverScopes() {
  const dir = new URL("../../../../../" + MIGRATIONS_DIR + "/", import.meta.url);
  const files = readdirSync(dir).filter((f) => f.endsWith(".up.sql")).sort();
  assert.ok(files.length > 0, "no up migrations found; this gate would pass vacuously");

  let last = null;
  for (const file of files) {
    const sql = readFileSync(new URL(file, dir), "utf8");
    const matches = [...sql.matchAll(/project_credentials_scopes_known[\s\S]*?\]::text\[\]/g)];
    if (matches.length > 0) last = matches[matches.length - 1][0];
  }
  assert.ok(last, "could not locate the scopes CHECK constraint in any migration");
  return [...last.matchAll(/'([a-z]+:[a-z]+)'/g)].map((m) => m[1]);
}

function consoleScopes() {
  return _scopeGroupsForTests().flatMap((g) => g.scopes.map((s) => s.id));
}

test("the console offers exactly the scopes the server accepts", () => {
  const server = serverScopes().sort();
  const ui = consoleScopes().sort();

  assert.deepEqual(ui, server,
    "the console's scope list and the database CHECK constraint disagree; one of them " +
    "would silently make a capability unreachable or unsavable");
});

test("no scope is selected by default", () => {
  // Every scope is presented as an unchecked option. There is no `default`,
  // `checked` or `recommended` flag anywhere in the vocabulary, so there is
  // nothing for a future edit to flip on.
  for (const group of _scopeGroupsForTests()) {
    for (const scope of group.scopes) {
      assert.equal(scope.checked, undefined, `${scope.id} carries a checked flag`);
      assert.equal(scope.default, undefined, `${scope.id} carries a default flag`);
    }
  }
});

test("every scope carries a hint describing what it permits", () => {
  for (const group of _scopeGroupsForTests()) {
    for (const scope of group.scopes) {
      assert.ok(scope.hint && scope.hint.length > 20,
        `${scope.id} has no usable description; an operator cannot grant least privilege blind`);
    }
  }
});

test("the scopes whose consequences are not obvious carry a warning", () => {
  const warned = _scopeGroupsForTests()
    .flatMap((g) => g.scopes)
    .filter((s) => s.sensitive)
    .map((s) => s.id)
    .sort();

  // roles:write — its bound (administrative roles are refused for machines) is
  // the reason it is safe to offer at all, and an operator should know it.
  // invitations:write — revoking an invitation DELETES a user, which the name
  // does not convey.
  // audit:read — reads EVERY actor's history in the workspace, not just this
  // credential's own, which "read the audit trail" does not convey either.
  assert.deepEqual(warned, ["audit:read", "invitations:write", "roles:write"]);
});

test("users:write does not claim to include setting a password", () => {
  // PUT .../password is operator-only. If the console implied otherwise, an
  // operator would grant users:write expecting a capability the server refuses.
  const usersWrite = _scopeGroupsForTests()
    .flatMap((g) => g.scopes)
    .find((s) => s.id === "users:write");

  assert.ok(usersWrite, "users:write is missing from the console vocabulary");
  assert.match(usersWrite.hint, /NOT include setting a password/i);
});

test("read scopes and write scopes are distinct entries, never merged", () => {
  // Least privilege only exists if an operator can grant read without write.
  const ids = consoleScopes();
  for (const resource of ["users", "roles"]) {
    assert.ok(ids.includes(`${resource}:read`), `${resource}:read is not offered`);
    assert.ok(ids.includes(`${resource}:write`), `${resource}:write is not offered`);
  }
  assert.ok(ids.includes("sessions:revoke"), "sessions:revoke is not offered");
  assert.ok(!ids.includes("sessions:write"), "sessions:write should not exist; a session is destroyed, not edited");
});

test("no scope in the vocabulary grants password setting", () => {
  // The absent scope. users:password was deliberately not created: including
  // the capability could never be walked back, because every key issued under
  // the looser rule would keep it.
  assert.ok(!consoleScopes().includes("users:password"));
  assert.ok(!serverScopes().includes("users:password"));
});
