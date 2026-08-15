// console-errors.js — fail a journey on an unexpected browser error.
//
// ─── Why this is not optional ───────────────────────────────────────────────
//
// The class of defect this slice exists to catch is a console that renders
// something wrong while every server-side gate is green. Two of the three ways
// that happens announce themselves in the browser and nowhere else:
//
//   - an uncaught exception in a view (router.js catches SYNCHRONOUS throws;
//     every view is async, so a rejection after the first await escapes it
//     entirely and leaves the page on "loading…" forever);
//   - a console.error the code writes on purpose — lib/state.js logs
//     "subscriber error", router.js logs "view render failed".
//
// A browser suite that ignores those would watch the exact failure it was
// written to find scroll past in a log nobody reads.
//
// ─── Failed responses are recorded, not automatically fatal ─────────────────
//
// A 4xx is often the assertion. The revoked-credential journey REQUIRES a 401;
// the error-rendering journey REQUIRES a 400. So responses ≥ 400 are collected
// and reported, and a test declares the ones it expects with `errors.allow()`.
// Blanket-ignoring them would delete the diagnostic value; blanket-failing on
// them would make half the journeys unwritable.

import fs from "node:fs";
import path from "node:path";
import { env } from "./env.js";

// BENIGN — errors proven harmless, each with the reason it is here.
//
// This list is short on purpose. "Ignore all console errors" is the failure
// mode this whole fixture exists to prevent, and a long allowlist is that
// failure mode arrived at gradually.
const BENIGN = [
  {
    // The console shell has no <link rel="icon">, so Chromium requests
    // /favicon.ico and the Go router answers 404. Cosmetic, and unrelated to
    // any journey.
    match: /\/favicon\.ico\b/,
    why: "the console ships no favicon; Chromium asks anyway",
  },
  {
    // A full-page navigation (the PKCE redirect to Keycloak, the callback back,
    // the logout redirect) cancels whatever the previous page had in flight.
    // The browser reports the cancellation as a failed request. It is the
    // navigation working, not a request failing.
    match: /net::ERR_ABORTED/,
    why: "in-flight request cancelled by an intentional full-page navigation",
  },
  {
    // The Overview page's user-count probe against a workspace that has no
    // active connection.
    //
    // This is the product working. /v1 answers workspace_connection_missing
    // BEFORE contacting any provider, and views/overview.js has an explicit
    // branch for it: the card renders "workspace not connected" rather than a
    // number. Every login lands on Overview, and which workspace the console
    // auto-selects at boot depends on what else is in the installation — so a
    // suite that creates an unconnected workspace anywhere will see this.
    //
    // Narrow on purpose. `?max=100` is Overview's probe and nothing else's;
    // every other users request in this suite carries `first`/`max=20` or a
    // search term. A 409 on a workspace a test navigated to deliberately still
    // fails the run, which is the case worth catching.
    match: /^409 GET .*\/v1\/workspaces\/ws_[^/]+\/users\?max=100$/,
    why: "Overview's user-count probe on a workspace with no active connection — a designed, rendered product state",
  },
  {
    // Chromium's own text for the response above. It carries no URL, so it can
    // only be matched by status; it is paired with the narrow rule above
    // rather than standing alone.
    match: /Failed to load resource: the server responded with a status of 409 \(Conflict\)/,
    why: "Chromium's URL-less console line for the 409 immediately above",
  },
];

function isBenign(text) {
  return BENIGN.some((b) => b.match.test(text));
}

/**
 * attachErrorCapture — wire the collectors to a page and return the collector.
 *
 * Returned object:
 *   allow(pattern)   declare an expected error/response for THIS test
 *   entries()        everything seen, benign or not
 *   unexpected()     everything neither benign nor allowed
 *   note(text)       add a breadcrumb, so the written log says which journey
 *                    step the surrounding errors belong to
 */
export function attachErrorCapture(page) {
  const entries = [];
  const allowed = [];

  const push = (kind, text) => {
    entries.push({ kind, text, at: new Date().toISOString() });
  };

  page.on("pageerror", (err) => {
    // The message AND the stack: a "view crashed" in this console is usually a
    // TypeError whose message alone does not say which view.
    push("pageerror", `${err.message}\n${err.stack || ""}`.trim());
  });

  page.on("console", (msg) => {
    if (msg.type() !== "error") return;
    push("console.error", msg.text());
  });

  page.on("requestfailed", (req) => {
    const failure = req.failure();
    push("requestfailed", `${req.method()} ${req.url()} — ${failure ? failure.errorText : "unknown"}`);
  });

  page.on("response", (res) => {
    if (res.status() < 400) return;
    push("response", `${res.status()} ${res.request().method()} ${res.url()}`);
  });

  return {
    allow(pattern) {
      allowed.push(pattern instanceof RegExp ? pattern : new RegExp(escapeRegExp(String(pattern))));
    },

    /**
     * allowStatus — declare that THIS test deliberately provokes an HTTP
     * failure, and allow both records the browser produces for it.
     *
     * A rejected fetch shows up twice, in two different vocabularies:
     *
     *   response:      "400 POST http://…/v1/workspaces"
     *   console.error: "Failed to load resource: the server responded with a
     *                   status of 400 (Bad Request)"
     *
     * The second is Chromium's own text and carries NO URL, so it can only be
     * allowed by status. That is broader than the first — within one test, any
     * request failing with that status is tolerated — and the narrow
     * URL-scoped pattern is registered alongside it so the response record
     * still has to match the intended endpoint.
     *
     * Kept as one call rather than two allow() lines at each call site,
     * because the two-record shape is a fact about the browser that every
     * error-provoking test would otherwise have to rediscover.
     */
    allowStatus(status, urlPattern) {
      const url = urlPattern instanceof RegExp ? urlPattern.source : escapeRegExp(String(urlPattern));
      allowed.push(new RegExp(`^${status}\\s+[A-Z]+\\s+.*${url}`));
      allowed.push(new RegExp(`Failed to load resource: the server responded with a status of ${status}\\b`));
    },
    note(text) {
      entries.push({ kind: "note", text, at: new Date().toISOString() });
    },
    entries() {
      return entries.slice();
    },
    unexpected() {
      return entries.filter((e) => {
        if (e.kind === "note") return false;
        if (isBenign(e.text)) return false;
        return !allowed.some((p) => p.test(e.text));
      });
    },
  };
}

function escapeRegExp(s) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * writeErrorLog — persist what the browser said, per test.
 *
 * This is the diagnostic that REPLACES traces and screenshots. It is plain
 * text, so scripts/scan-artifacts.sh can read it, and so a reader can see what
 * happened without downloading a zip and opening a viewer.
 *
 * URLs are written verbatim EXCEPT for OAuth callback parameters. A failed
 * login's URL carries `code=` and `state=`, and an authorization code in a
 * published artifact is a credential in a published artifact even though it is
 * single-use and already spent.
 */
export function writeErrorLog(testTitle, collector, extra = {}) {
  const dir = path.join(env.artifactDir, "browser-logs");
  fs.mkdirSync(dir, { recursive: true });
  const safe = testTitle.replace(/[^a-zA-Z0-9]+/g, "-").slice(0, 80);
  const file = path.join(dir, `${safe}.log`);

  const lines = [
    `test: ${testTitle}`,
    ...Object.entries(extra).map(([k, v]) => `${k}: ${redactUrl(String(v))}`),
    "",
    ...collector.entries().map((e) => `[${e.at}] ${e.kind}: ${redactUrl(e.text)}`),
  ];
  fs.writeFileSync(file, lines.join("\n") + "\n", "utf8");
  return file;
}

/**
 * redactUrl — strip OAuth material out of anything about to be written down.
 *
 * Belt and braces with the artifact scan: the scan is the gate that catches a
 * leak, and this is what stops the most predictable one from happening in the
 * first place. Both, because a gate with nothing upstream of it fails builds
 * instead of preventing leaks.
 */
export function redactUrl(text) {
  return String(text)
    .replace(/([?&](?:code|state|session_state|id_token_hint|code_verifier|code_challenge)=)[^&\s"']+/gi, "$1REDACTED")
    .replace(/(eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.)[A-Za-z0-9_-]+/g, "$1REDACTED")
    .replace(/lw_sk_[A-Za-z0-9_]{8,}/g, "lw_sk_REDACTED");
}
