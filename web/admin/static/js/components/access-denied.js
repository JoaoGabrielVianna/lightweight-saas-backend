// access-denied.js — the screen an operator sees instead of a console they
// cannot use.
//
// It replaces the pre-fix experience: a fully drawn console whose every panel
// failed with a bare 403, which reads as "the product is broken" rather than
// "your account lacks a role". Everything an operator needs to act is on the
// screen — which account they are signed in as, which roles that account
// actually carries, which role the console requires, and a way out.
//
// Every network-derived value reaches the DOM as a text node through `h`, so
// a `preferred_username` containing markup renders as characters. Same rule as
// ws-states.js; this screen is not an exception to it just because it is an
// error screen.

import { h } from "../lib/dom.js";
import { emptyState, kvList } from "./common.js";
import { ACCESS } from "../lib/access.js";

/**
 * renderAccessDenied — the denial screen for a non-granted decision.
 *
 * @param {object} decision the result of evaluateConsoleAccess
 * @param {{onSignOut?: Function, onRetry?: Function}} handlers
 */
export function renderAccessDenied(decision, handlers = {}) {
  const d = decision || {};
  const unverified = d.state === ACCESS.UNVERIFIED;

  // Signing out is offered in BOTH states, and it is the only action in the
  // missing-role state. The remedy there is a different account, and the
  // Keycloak session has to end before the operator can present one.
  const signOut = handlers.onSignOut
    ? h("button", { class: "btn btn-primary", onclick: handlers.onSignOut }, "Sign out")
    : null;

  // Retry is offered ONLY when the answer was unknown. Re-checking a token
  // that definitively lacks the role just redraws the same screen, and a
  // button that visibly does nothing invites the operator to keep pressing it.
  const retry = unverified && handlers.onRetry
    ? h("button", { class: "btn", onclick: handlers.onRetry }, "Retry")
    : null;

  return emptyState({
    icon:  unverified ? "⚠" : "⛔",
    title: unverified
      ? "Could not confirm your permissions"
      : "This account cannot use the console",
    body: unverified
      ? "Signing in worked, but the console could not read which roles your session carries, so it stopped rather than show you a screen it cannot trust."
      : `Signing in worked. Authorization did not: the console requires the "${d.role}" role, and this session does not carry it.`,
    action: h("div", "col",
      kvList([
        ["Signed in as",  d.email || d.subject || "unknown"],
        ["Roles on this session", d.roles && d.roles.length ? d.roles.join(", ") : "none"],
        ["Role required", d.role || ""],
      ]),
      d.reason ? h("p", "muted text-xs", d.reason) : null,
      // The console is not where this gets fixed, and saying so saves an
      // operator from hunting for a settings page that cannot exist: the
      // role lives in the identity provider this installation authenticates
      // against.
      h("p", "muted text-xs",
        "Roles are granted in the identity provider, not here. An operator with realm access can add the role to this account.",
      ),
      h("div", { class: "row", style: { marginTop: "12px", gap: "8px" } }, retry, signOut),
    ),
  });
}
