// locale.test.mjs — unit tests for lib/locale.js + integration with lib/state.js.
//
// Run with: node --test web/admin/static/js/tests/
//
// These tests pin the contracts that, together with state.js's iteration
// safety, prevent the Docs-UI fetch storm:
//
//   - getLocale reads localStorage with a safe fallback;
//   - state.locale is seeded from localStorage at boot;
//   - onLocaleChange fires ONLY on real locale transitions;
//   - the docsView "callback re-registers itself" pattern emits exactly
//     one fire per setLocale call, never thousands.
//
// The two regressions at the bottom of this file are the exact scenarios
// that produced 74 655 duplicate fetches to QUICKSTART.pt-BR.md.

import { test } from "node:test";
import assert from "node:assert/strict";

function makeStorage(initial) {
  const data = { ...(initial || {}) };
  return {
    getItem(k) { return Object.prototype.hasOwnProperty.call(data, k) ? data[k] : null; },
    setItem(k, v) { data[k] = String(v); },
    removeItem(k) { delete data[k]; },
    clear() { for (const k of Object.keys(data)) delete data[k]; },
    _data: data,
  };
}

// Stub before any module import — state.js reads localStorage on load.
globalThis.localStorage = makeStorage();

const { setState, _resetForTests, getState } = await import("../lib/state.js");
const { getLocale, setLocale, onLocaleChange, DEFAULT_LOCALE, LOCALES } =
  await import("../lib/locale.js");

test("getLocale falls back to DEFAULT_LOCALE when localStorage is empty", () => {
  globalThis.localStorage = makeStorage({});
  assert.equal(getLocale(), DEFAULT_LOCALE);
  assert.equal(DEFAULT_LOCALE, "en");
});

test("getLocale returns persisted locale when valid", () => {
  globalThis.localStorage = makeStorage({ admin_docs_locale: "pt-BR" });
  assert.equal(getLocale(), "pt-BR");
});

test("getLocale rejects non-whitelisted values", () => {
  globalThis.localStorage = makeStorage({ admin_docs_locale: "klingon" });
  assert.equal(getLocale(), DEFAULT_LOCALE);
});

test("LOCALES is the canonical allow-list", () => {
  assert.deepEqual(LOCALES, ["en", "pt-BR"]);
});

test("boot: state.locale is seeded from localStorage", () => {
  globalThis.localStorage = makeStorage({ admin_docs_locale: "pt-BR" });
  _resetForTests();
  assert.equal(getState().locale, "pt-BR",
    "state.locale must match localStorage at boot — otherwise onLocaleChange's prev/next filter diverges");
});

test("onLocaleChange does NOT fire when an unrelated setState happens", () => {
  globalThis.localStorage = makeStorage({ admin_docs_locale: "pt-BR" });
  _resetForTests();
  let fires = 0;
  onLocaleChange(() => fires++);

  setState({ route: "/docs/quick-start" });
  setState({ theme: "light" });
  setState({ identity: { roles: ["admin"] } });

  assert.equal(fires, 0, "no fires when locale is unchanged");
});

test("onLocaleChange fires exactly once per actual locale transition", () => {
  globalThis.localStorage = makeStorage({ admin_docs_locale: "en" });
  _resetForTests();
  const observed = [];
  onLocaleChange((next) => observed.push(next));

  setLocale("pt-BR");
  setLocale("pt-BR");     // no-op transition
  setLocale("en");
  setLocale("pt-BR");

  assert.deepEqual(observed, ["pt-BR", "en", "pt-BR"]);
});

test("setLocale rejects values outside the allow-list", () => {
  globalThis.localStorage = makeStorage({ admin_docs_locale: "en" });
  _resetForTests();
  let fires = 0;
  onLocaleChange(() => fires++);

  setLocale("fr");   // not in LOCALES
  setLocale("xx");

  assert.equal(getState().locale, "en");
  assert.equal(fires, 0);
});

test("regression: PT-BR persisted + non-locale setStates do NOT loop", () => {
  // THE bug. Before the fix:
  //   prev = getLocale() = "pt-BR" (localStorage)
  //   next = state.locale || "en" = "en" (state.locale undefined)
  //   prev !== next on every setState → fires fn() forever
  //   fn re-registers itself → Set iterator visits new entry → recursion
  //
  // After the fix:
  //   state.js seeds state.locale = "pt-BR" at module load → no divergence;
  //   state.js snapshots subscribers → even a divergence would not recurse.
  globalThis.localStorage = makeStorage({ admin_docs_locale: "pt-BR" });
  _resetForTests();

  let fireCount = 0;
  let unsub = null;
  // Reproduce docsView's exact re-register pattern.
  const attach = () => {
    if (unsub) unsub();
    unsub = onLocaleChange(() => {
      fireCount++;
      attach();
    });
  };
  attach();

  // Router fires this on every navigation.
  setState({ route: "/docs/quick-start" });
  // Theme toggle fires this.
  setState({ theme: "light" });
  // Auth hydration fires this.
  setState({ identity: { roles: ["admin"] } });

  assert.equal(fireCount, 0,
    `expected 0 fires (pre-fix observed ~74 655); got ${fireCount}`);
});

test("regression: setLocale triggers exactly 1 callback even with re-register pattern", () => {
  globalThis.localStorage = makeStorage({ admin_docs_locale: "en" });
  _resetForTests();

  let fireCount = 0;
  let unsub = null;
  const attach = () => {
    if (unsub) unsub();
    unsub = onLocaleChange(() => {
      fireCount++;
      attach();
    });
  };
  attach();

  setLocale("pt-BR");
  assert.equal(fireCount, 1, "exactly one fire per real change");

  setLocale("en");
  assert.equal(fireCount, 2);

  setLocale("pt-BR");
  assert.equal(fireCount, 3);
});

test("regression: multiple subscribers all see the locale change exactly once each", () => {
  globalThis.localStorage = makeStorage({ admin_docs_locale: "en" });
  _resetForTests();

  let a = 0, b = 0, c = 0;
  onLocaleChange(() => a++);
  onLocaleChange(() => b++);
  onLocaleChange(() => c++);

  setLocale("pt-BR");
  assert.deepEqual([a, b, c], [1, 1, 1]);

  setLocale("en");
  assert.deepEqual([a, b, c], [2, 2, 2]);
});
