// locale.js — minimal locale switch for the Docs viewer.
//
// Scope is deliberately tiny:
//
//   - Two locales: "en" (default) and "pt-BR".
//   - One persisted setting (localStorage admin_docs_locale).
//   - One file-naming convention for translated markdown:
//
//         docs/FOO.md          ← English (canonical)
//         docs/FOO.pt-BR.md    ← Portuguese sibling (optional)
//
//     If the sibling is missing the viewer falls back to the English
//     original — no JS, no Go, no config change is required to add a
//     translation, and no coverage gates exist.
//
// What this module does NOT do, by design:
//
//   - No UI dictionary. Admin chrome and Docs chrome stay in English.
//   - No global i18n framework. PT-BR exists only so the maintainer can
//     read long-form prose comfortably in their native language.
//   - No translation layer for the Admin views.
//
// The whole module is one screenful so future-me can grep it in one go.

import { setState, subscribe } from "./state.js";

export const LOCALES        = ["en", "pt-BR"];
export const DEFAULT_LOCALE = "en";
export const STORAGE_KEY    = "admin_docs_locale";

// Labels rendered on the toggle buttons. Short and language-native.
export const LOCALE_LABEL = {
  "en":    "EN",
  "pt-BR": "PT-BR",
};

// getLocale — read the persisted choice, validate it. Falls back to EN
// when the slot is empty or holds an unknown value.
export function getLocale() {
  let stored;
  try { stored = localStorage.getItem(STORAGE_KEY); } catch { stored = null; }
  return LOCALES.includes(stored) ? stored : DEFAULT_LOCALE;
}

// setLocale — persist + broadcast through the shared state store. The
// sidebar and topbar subscribe to state, so they re-render on every
// flip. The Docs view subscribes through onLocaleChange (below) so it
// can also re-fetch the markdown sibling.
export function setLocale(locale) {
  if (!LOCALES.includes(locale)) return;
  try { localStorage.setItem(STORAGE_KEY, locale); } catch {}
  setState({ locale });
}

// onLocaleChange — convenience filter over the state subscriber. Only
// fires when the locale value actually changes (state.js notifies on
// every setState, regardless of key). Returns the unsubscribe function.
export function onLocaleChange(fn) {
  let prev = getLocale();
  return subscribe((state) => {
    const next = state.locale || DEFAULT_LOCALE;
    if (next !== prev) {
      prev = next;
      try { fn(next); } catch (e) { console.error("locale subscriber error:", e); }
    }
  });
}

// localizedDocFile — derive the locale-suffixed sibling for a markdown
// path. Returns null for the default locale (no suffix needed) so the
// caller can skip the speculative fetch.
//
//   "QUICKSTART.md",            "pt-BR" → "QUICKSTART.pt-BR.md"
//   "operations/MONITORING.md", "pt-BR" → "operations/MONITORING.pt-BR.md"
//   any path,                   "en"    → null
export function localizedDocFile(file, locale) {
  if (!file || !locale || locale === DEFAULT_LOCALE) return null;
  return file.replace(/\.md$/i, "." + locale + ".md");
}
