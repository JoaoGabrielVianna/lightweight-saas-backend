// secrets.js — "this value is not on the page, anywhere".
//
// Both secret journeys need the same assertion against different values: the
// identity-provider client secret the operator types into the connection form,
// and the one-time project credential the server shows once. Writing it twice
// would mean two definitions of "gone", and the weaker one would be the one
// that mattered.
//
// ─── What counts as an observable surface ───────────────────────────────────
//
// Deliberately NOT "search the browser's memory". A string typed into a form
// exists in the renderer's heap for as long as the renderer feels like keeping
// it, and no product can promise otherwise. What a product CAN promise is that
// the value is not in anything a person or a script can read:
//
//   1. the serialised page source (what View Source and any scraper sees);
//   2. the rendered text (what a screenshot or a human sees);
//   3. every form field's live value (what an autofill or an extension reads);
//   4. sessionStorage and localStorage (what survives a reload, and what any
//      script on the origin can read).
//
// (3) is the one that needs a live evaluate: an <input> value set by JavaScript
// never appears in the serialised HTML, so checking page source ALONE would
// pass on a form still holding the secret.

import { expect } from "@playwright/test";

/**
 * expectSecretAbsent — assert a value is on none of the observable surfaces.
 *
 * @param {import('@playwright/test').Page} page
 * @param {string} secret
 * @param {string} where a human label for the failure message ("after saving")
 */
export async function expectSecretAbsent(page, secret, where) {
  expect(secret, "expectSecretAbsent was given nothing to look for").toBeTruthy();

  const source = await page.content();
  expect(source, `the secret is in the page source ${where}`).not.toContain(secret);

  const surfaces = await page.evaluate(() => ({
    text: document.body ? document.body.innerText : "",
    inputs: Array.from(document.querySelectorAll("input, textarea"))
      .map((el) => el.value || ""),
    session: Object.keys(sessionStorage).map((k) => `${k}=${sessionStorage.getItem(k)}`),
    local: Object.keys(localStorage).map((k) => `${k}=${localStorage.getItem(k)}`),
  }));

  expect(surfaces.text, `the secret is rendered on screen ${where}`).not.toContain(secret);
  for (const value of surfaces.inputs) {
    expect(value, `the secret is still in a form field ${where}`).not.toContain(secret);
  }
  for (const entry of surfaces.session) {
    expect(entry, `the secret is in sessionStorage ${where}`).not.toContain(secret);
  }
  for (const entry of surfaces.local) {
    expect(entry, `the secret is in localStorage ${where}`).not.toContain(secret);
  }
}
