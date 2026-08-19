// access.js — may this signed-in operator use the console at all?
//
// Authentication and authorization are two different questions, and until now
// the console only asked the first one. Any account in the realm could
// complete the PKCE flow, so the shell mounted, the sidebar drew, and then
// every request came back 403 — a working console showing a wall of failures
// with no statement of the cause. The gate below asks the second question
// once, at boot, and turns that into one sentence.
//
// This changes NO privilege. The server already refuses every /admin/* and
// /v1/* call without the role (internal/server/router.go, RequireRole →
// RequireLiveAdmin) and remains the only thing enforcing it — a browser check
// is a courtesy to the person reading the screen, never a security boundary.
// What it removes is the misleading UI in front of that refusal.
//
// The identity object is the /auth/debug response, already in state.identity
// by the time this runs: `roles` there is the same flattening the token
// validator performs (realm_access.roles + resource_access.<client>.roles,
// see extractRolesFromClaims in internal/server/playground.go), so agreeing
// with the server means checking membership in that one list.

// CONSOLE_ROLE mirrors `operatorRole` in internal/authz/authorize.go and the
// literal passed to RequireRole in internal/server/router.go. It is
// deliberately a hardcoded constant and NOT configuration: a second source of
// truth for "which role runs this installation" is a worse problem than the
// one this file fixes.
export const CONSOLE_ROLE = "admin";

// The three answers, kept apart because they have different remedies.
//
//   GRANTED       — the role is on the token; boot continues.
//   MISSING_ROLE  — we know the roles and the required one is absent. The
//                   account is the problem; another account is the fix.
//   UNVERIFIED    — we could not establish the roles at all. The account may
//                   well be fine; retrying is the fix.
//
// UNVERIFIED still denies. Failing closed here costs an admin one Retry click
// on a flaky /auth/debug; failing open would put the pre-fix wall of 403s back
// on screen for exactly the users this exists to inform.
export const ACCESS = {
  GRANTED:      "granted",
  MISSING_ROLE: "missing_role",
  UNVERIFIED:   "unverified",
};

/**
 * evaluateConsoleAccess — the boot-time decision, as data.
 *
 * Returns a plain object so the caller renders and the test asserts without
 * either of them re-deriving the rule:
 *
 *   { allowed, state, role, roles, email, subject, reason }
 *
 * `roles`, `email` and `subject` are normalized (never null, never a
 * non-string) because they are rendered on the denial screen, and a screen
 * explaining a permission problem is the worst place to throw a TypeError.
 *
 * @param {object|null} identity the /auth/debug response, or null
 */
export function evaluateConsoleAccess(identity) {
  const who = {
    role:    CONSOLE_ROLE,
    roles:   [],
    email:   "",
    subject: "",
  };

  // No identity at all: /auth/debug failed, or was never reached. A 401 has
  // already been turned into a re-login by api.js's interceptor, so what lands
  // here is a transport failure or a 5xx — unknown, not denied.
  if (!identity || typeof identity !== "object") {
    return {
      ...who,
      allowed: false,
      state:   ACCESS.UNVERIFIED,
      reason:  "the console could not read your session from /auth/debug",
    };
  }

  who.email   = asText(identity.email);
  who.subject = asText(identity.received_sub);

  const roles = Array.isArray(identity.roles)
    ? identity.roles.filter((r) => typeof r === "string" && r !== "")
    : null;
  if (roles) who.roles = roles;

  // `valid: false` means the endpoint's own validation rejected the token it
  // was handed. Its `roles` came from an UNVERIFIED base64 decode in that
  // case, so they describe what the token claims, not what the realm granted.
  // Never grant on those, even when `admin` is among them.
  if (identity.valid === false) {
    return {
      ...who,
      allowed: false,
      state:   ACCESS.UNVERIFIED,
      reason:  asText(identity.reason) || "the server did not accept this token",
    };
  }

  // A payload with no roles array is a shape we do not recognize. Same
  // reasoning as above: absence of evidence is not evidence of denial.
  if (!roles) {
    return {
      ...who,
      allowed: false,
      state:   ACCESS.UNVERIFIED,
      reason:  "the session response carried no role list",
    };
  }

  if (!roles.includes(CONSOLE_ROLE)) {
    return {
      ...who,
      allowed: false,
      state:   ACCESS.MISSING_ROLE,
      reason:  `this account does not carry the "${CONSOLE_ROLE}" role`,
    };
  }

  return { ...who, allowed: true, state: ACCESS.GRANTED, reason: "" };
}

function asText(v) {
  return typeof v === "string" ? v : "";
}
