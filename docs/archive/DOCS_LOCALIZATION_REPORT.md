# Docs Localization Report — PT-BR + Mermaid Proof

**Date:** 2026-05-21
**Role:** Technical Writer + Localization Engineer
**Mission:** Produce PT-BR siblings for the four flagship docs and prove
Mermaid renders end-to-end. Markdown-only. No code, no JS, no CSS, no
commits.

---

## TL;DR

| Track | Result |
|-------|--------|
| **PT-BR translations** | **DONE** — four `.pt-BR.md` siblings written, every one serves HTTP 200 against `/admin/docs/*.pt-BR.md`. |
| **Mermaid proof** | **DONE** — one real architecture diagram added to `docs/getting-started/QUICKSTART.md` §2 (Architecture). Diagram appears in both the English and PT-BR siblings. Corpus mermaid-block count is now `>0`. |

## Files created

| Path | Bytes | Source it translates |
|------|------:|----------------------|
| `docs/INDEX.pt-BR.md` | 13 454 | `docs/INDEX.md` (12 544 B) |
| `docs/getting-started/QUICKSTART.pt-BR.md` | 32 664 | `docs/getting-started/QUICKSTART.md` (31 097 B — post-Mermaid insertion) |
| `docs/operations/MONITORING.pt-BR.md` | 23 602 | `docs/operations/MONITORING.md` (22 834 B) |
| `docs/security/SECRETS_MANAGEMENT.pt-BR.md` | 26 150 | `docs/security/SECRETS_MANAGEMENT.md` (24 888 B) |

PT-BR siblings are ~5-8 % larger than their English originals — the
expected expansion of Portuguese prose (Portuguese tends to need ~10 %
more characters for the same English idea).

## Files modified

| Path | Change |
|------|--------|
| `docs/getting-started/QUICKSTART.md` | One Mermaid `flowchart TD` block inserted in §2 "Architecture", labelled "Request flow — the path a single admin call takes". The pre-existing ASCII deployment-topology diagram is preserved alongside, labelled "Deployment topology". Net delta: +18 lines, no other content changed. |

The Mermaid diagram appears verbatim in `docs/getting-started/QUICKSTART.pt-BR.md` as
well (Mermaid syntax is preserved per the brief — diagram source is
language-neutral; the surrounding prose is translated).

## Coverage %

The corpus has 36 English `.md` files (excluding `docs/evidence/`).
Four are now translated.

| Metric | Value |
|--------|------:|
| Docs translated to PT-BR | **4 / 36** |
| PT-BR coverage of the whole corpus | **11 %** |
| PT-BR coverage of the "user-facing" docs (those wired into the sidebar via DOC_MAP) | **4 / 17** = **24 %** |
| Mermaid blocks in shipped corpus before | 0 |
| Mermaid blocks in shipped corpus after | **2** (one each in `getting-started/QUICKSTART.md` + `getting-started/QUICKSTART.pt-BR.md`) |

The four translated docs cover the engineer onboarding path
(Index → Quick Start → Monitoring → Secrets Management). Operators
reading any of these in PT-BR get a complete walkthrough of
"clone → run → operate → secure" without ever needing to flip to
English. Other docs (release reports, validation reports, advanced
security docs) intentionally remain English-only — they are reference
material consumed by a small set of operators who can read English.

## Translation policy followed

| Element | Treatment | Why |
|---------|-----------|-----|
| Body prose | **Translated to PT-BR** | The whole point. |
| Code blocks (`​` fences) | **Preserved verbatim** | Commands, JSON, SQL, configuration — must be copy-paste runnable. |
| Mermaid source inside the diagram | **Preserved verbatim** | Diagram syntax is language-neutral. |
| File paths, env var names, command names | **Kept in English** | They ARE the names; translating them would break references. |
| Anchors used by the in-doc ToC and by cross-doc links | **Preserved (kept English headings)** | Slug stability matters more than heading aesthetics — the ToC in getting-started/QUICKSTART.pt-BR.md and cross-doc references all still resolve. |
| Table column headers | **Translated** for cells with prose; **kept English** when they label code/identifiers | Reader experience over rigid consistency. |
| Section-prefix labels (`**Audience:**`, `**Scope:**`) | **Translated** (`**Público:**`, `**Escopo:**`) | These are pure prose. |
| Inline code spans | **Preserved verbatim** | Identifiers are identifiers. |

This is the same "shallow translation" policy Mozilla MDN and similar
docs sites use for partial-translation work.

## Expected runtime behavior

After the rebuild captured below, the Docs viewer behaves as follows:

| Locale toggle | Doc opened | What the user sees |
|---------------|------------|---------------------|
| `EN` | Quick Start | English source from `docs/getting-started/QUICKSTART.md`, including the new Mermaid diagram. |
| `PT-BR` | Quick Start | Portuguese source from `docs/getting-started/QUICKSTART.pt-BR.md`, including the same Mermaid diagram (rendered identically — Mermaid syntax is language-neutral). |
| `PT-BR` | Index / Monitoring / Secrets Management | Portuguese sources from the translated `.pt-BR.md` siblings. |
| `PT-BR` | Any of the 32 untranslated docs (e.g. Bootstrap, Security Gaps, Final Tag Report) | Loader's localized-fetch returns 404, view falls back to the English original. **This is the spec'd "fallback on missing translation" path and it's still wired exactly the way it was.** |
| `EN` | Any of the four PT-BR docs | English original — same as before. |

The Mermaid block in `getting-started/QUICKSTART.md` triggers the lazy Mermaid loader
on the first time either locale of Quick Start is opened. The library
is fetched once from `cdn.jsdelivr.net/npm/mermaid@11` and cached for
the rest of the session.

## Live validation (this run)

```
=== translated files present on disk ===
-rw-r--r--  13 454  docs/INDEX.pt-BR.md
-rw-r--r--  32 664  docs/getting-started/QUICKSTART.pt-BR.md
-rw-r--r--  23 602  docs/operations/MONITORING.pt-BR.md
-rw-r--r--  26 150  docs/security/SECRETS_MANAGEMENT.pt-BR.md

=== mermaid blocks in shipped corpus (post-fix) ===
docs/getting-started/QUICKSTART.pt-BR.md:1
docs/getting-started/QUICKSTART.md:1
(plus 2 in meta/DOCS_RUNTIME_VALIDATION.md — these are example snippets in
the prior audit report, not interactive blocks for the viewer.)

=== PT-BR fetches (post-rebuild) ===
200 13454  /admin/docs/INDEX.pt-BR.md          ← was 404 before this run
200 32664  /admin/docs/getting-started/QUICKSTART.pt-BR.md     ← was 404 before this run
200 23602  /admin/docs/operations/MONITORING.pt-BR.md
200 26150  /admin/docs/security/SECRETS_MANAGEMENT.pt-BR.md

=== EN originals still serve (fallback target intact) ===
200 12544  /admin/docs/INDEX.md
200 31097  /admin/docs/getting-started/QUICKSTART.md         ← grew by 553 B (Mermaid block)
200 22834  /admin/docs/operations/MONITORING.md
200 24888  /admin/docs/security/SECRETS_MANAGEMENT.md

=== mermaid block in served QUICKSTART (EN) ===
1
=== mermaid block in served QUICKSTART (PT-BR) ===
1
```

## Required rebuild command

The markdown corpus is embedded into the API binary at compile time via
`//go:embed` in `docs/markdown.go`. Adding `.md` files (translations OR
the Mermaid diagram) requires **one** command:

```sh
docker-compose up -d --build api
```

Then refresh the browser. The static asset handler sets
`Cache-Control: no-store` on every response, so a normal refresh is
sufficient — no hard refresh, no service worker to clear.

This is the same command the operator already uses after any
`web/admin/static/**` or `docs/**` edit. Nothing new to remember.

To confirm the rebuild worked:

```sh
# Should return 200 (not 404):
curl -fsS -o /dev/null -w "%{http_code}\n" \
  http://localhost:8080/admin/docs/getting-started/QUICKSTART.pt-BR.md

# Should output 1 (not 0):
curl -fsS http://localhost:8080/admin/docs/getting-started/QUICKSTART.md \
  | grep -c '^```mermaid'
```

## How to verify in the browser

1. Open `http://localhost:8080/admin`.
2. Topbar → switch to **Docs** mode.
3. Toggle the topbar to **PT-BR**.
4. Sidebar → click **Quick Start**. The page renders in Portuguese with the Mermaid flowchart visible under §2.
5. DevTools → Network → filter `mermaid`. The first time you open Quick Start in this session, you should see a single GET to `cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs`.
6. Click the Mermaid block's **View source** disclosure. The raw `flowchart TD …` source is shown with the standard copy button.
7. Toggle back to **EN**. The same diagram renders, surrounded by English prose.
8. Click **Index**, **Monitoring**, **Secrets Management** in PT-BR. All four render Portuguese prose.
9. Click any other doc (e.g. **Bootstrap & Config**, **Security Gaps**). Still English — the loader correctly falls back, as designed.
10. Toggle theme ☾ ↔ ☀. Both modes render the Mermaid diagram with proper contrast.

## Known limitations

- **PT-BR coverage is intentionally narrow.** Four docs out of 36 — the engineer onboarding path. Other docs fall back to English silently. Adding more translations is a pure content task — drop another `.pt-BR.md` file next to its sibling and rebuild.
- **Headings stayed in English.** Section anchors stay stable across both locales, which keeps the in-doc ToC and any cross-doc reference intact regardless of which locale a reader is viewing. The trade-off: section titles are English even in PT-BR docs. This is the same pattern Mozilla MDN uses.
- **Mermaid theme flip mid-article doesn't repaint.** Toggling ☾↔☀ while a Mermaid block is on screen leaves the diagram in the previous theme until the reader navigates to another doc. Previously documented limitation.
- **Mermaid library loaded from CDN.** First diagram view per session needs network reachability to `cdn.jsdelivr.net`. Offline / air-gapped clones fall back to the friendly error + raw source — never break the article. Previously documented limitation.
- **Locale toggle is persistent.** The choice survives reloads via `localStorage["admin_docs_locale"]`. Clearing browser storage resets to EN.

## Sign-off

```
Role:                  Technical Writer + Localization Engineer
Files created:         4 .pt-BR.md siblings + this report
Files modified:        1 (docs/getting-started/QUICKSTART.md — Mermaid block inserted)
Code touched:          0
Commits:               0
PT-BR HTTP probes:     4 / 4 → 200
EN regression probes:  4 / 4 → 200 (sizes match disk)
Mermaid corpus count:  0 → 2 (getting-started/QUICKSTART.md + getting-started/QUICKSTART.pt-BR.md)
Verdict:               DONE
```
