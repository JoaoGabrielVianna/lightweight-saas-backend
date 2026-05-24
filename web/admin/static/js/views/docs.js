// docs.js — Documentation mode view.
//
// Architecture:
//
//   - One view function handles every /docs/* route. The route's `params.page`
//     (or query `?p=`) selects which doc to load. A central DOC_MAP table
//     resolves the page slug to a source file under /admin/static/docs/, which
//     is a symlink to the repository's docs/ tree (see admin.go static
//     handler — it follows symlinks transparently via http.ServeFile).
//
//   - Markdown is fetched as plain text and rendered by lib/markdown.js. The
//     result is sandboxed: the renderer only emits a known tag set, and every
//     text node is HTML-escaped at the source. Source files are project-
//     shipped, not user input — XSS surface is zero in practice and zero by
//     construction even if that ever changed.
//
//   - In-page navigation: links inside rendered docs are intercepted. Same-doc
//     #anchors scroll smoothly; cross-doc relative paths (e.g.
//     "../validation/AUDIT_EVENTS.md") are translated into the corresponding
//     /docs/* route and navigated via the SPA router — no page reloads.
//
//   - When a route exists in DOC_MAP but the underlying file is missing
//     (404 from the static handler), the view renders the "Documentation
//     not available yet" placeholder. No mock content, no stubs.
//
// This module owns no global state. It is safe to mount, unmount, and
// re-enter without leaks; subscribers are cleaned up on each call.

import { h, mount } from "../lib/dom.js";
import { pageHeader, emptyState } from "../components/common.js";
import { render as renderMd, slugify } from "../lib/markdown.js";
import { highlight } from "../lib/highlight.js";
import { navigate } from "../lib/router.js";
import { getLocale, onLocaleChange, localizedDocFile } from "../lib/locale.js";

// DOC_MAP — every doc route the sidebar exposes, plus the route → file
// resolution. Keys mirror the path used by main.js. Adding a doc here is the
// only step needed to surface it; the renderer handles the rest. When the
// file column is null, the docs view shows "Documentation not available yet".
export const DOC_MAP = {
  "":                      { title: "Documentation Index", file: "INDEX.md",                                section: "DOCUMENTATION" },
  "quick-start":           { title: "Quick Start",         file: "getting-started/QUICKSTART.md",           section: "GETTING STARTED" },
  "bootstrap":             { title: "Bootstrap & Config",  file: "architecture/bootstrap.md",               section: "ARCHITECTURE" },
  "keycloak":              { title: "Keycloak Setup",      file: "getting-started/KEYCLOAK_SETUP.md",       section: "ARCHITECTURE" },
  "operations/backup":     { title: "Backup & Recovery",   file: "operations/BACKUP_AND_RECOVERY.md",       section: "OPERATIONS" },
  "operations/upgrade":    { title: "Upgrade & Rollback",  file: "operations/UPGRADE_AND_ROLLBACK.md",      section: "OPERATIONS" },
  "monitoring":            { title: "Monitoring",          file: "operations/MONITORING.md",                section: "MONITORING" },
  "security/secrets":      { title: "Secrets Management",  file: "security/SECRETS_MANAGEMENT.md",          section: "SECURITY" },
  "security/audit":        { title: "Audit Operations",    file: "audit/AUDIT_OPERATIONS.md",               section: "SECURITY" },
  "security/gaps":         { title: "Security Gaps",       file: "security/SECURITY_GAPS.md",               section: "SECURITY" },
  "security/gap1":         { title: "GAP-1 Remediation",   file: "security/SECURITY_REMEDIATION_GAP1.md",   section: "SECURITY" },
  "security/gap1-regress": { title: "GAP-1 Regression",    file: "security/SECURITY_REGRESSION_GAP1.md",    section: "SECURITY" },
  "security/final":        { title: "Final Security Gate", file: "security/FINAL_SECURITY.md",              section: "SECURITY" },
  "release/v0.2":          { title: "v0.2.0 Release",      file: "release/RELEASE_v0.2.md",                 section: "RELEASE NOTES" },
  "release/checklist":     { title: "Release Checklist",   file: "release/RELEASE_CHECKLIST.md",            section: "RELEASE NOTES" },
  "release/final-smoke":   { title: "Final Smoke",         file: "release/FINAL_SMOKE.md",                  section: "RELEASE NOTES" },
  "release/final-tag":     { title: "Final Tag Report",    file: "release/FINAL_TAG_REPORT.md",             section: "RELEASE NOTES" },
};

// File-extension whitelist for the static handler — only .md files are
// fetched into the docs view; anything else would mean a config typo above.
const ALLOWED_EXT = /\.md$/i;

// Base URL the docs fetch from. /admin/docs/* is served by the Go handler
// in internal/server/admin.go, backed by a //go:embed of the docs/ tree
// declared in docs/markdown.go. No filesystem symlinks involved — the
// markdown corpus travels inside the binary, so this route works
// identically on macOS, Linux, Docker, and Windows clones (the prior
// /admin/static/docs symlink approach failed on Docker and on Windows
// because the symlink target wasn't present in the runtime tree).
const DOCS_BASE = "/admin/docs";

// Module-singleton render state. These three handles are reset at the top of
// every docsView call so each render starts from a clean slate.
//
//   _localeUnsub  — unsubscribe for the current onLocaleChange wrapper.
//   _tocObserver  — the IntersectionObserver wired by renderToc(). Without
//                   disconnecting before re-arming, every navigation leaked
//                   one observer holding the previous article's closures.
//   _renderToken  — monotonically increasing id. A deferred locale callback
//                   that resolves AFTER a newer docsView started bails out
//                   when its captured token no longer matches, so we never
//                   race two parallel renders against the same #docs-article.
let _localeUnsub = null;
let _tocObserver = null;
let _renderToken = 0;

export default async function docsView({ params, container, query }) {
  // Bump the render token first. Any deferred work from a prior render that
  // is still queued will see a stale token and short-circuit.
  const myToken = ++_renderToken;

  // Page key: rest of /docs/<page>. Router strips the leading /docs/ prefix
  // and stores the remainder under params.page (configured in main.js).
  const pageKey = (params && params.page) || "";
  const entry = DOC_MAP[pageKey];

  // Tear down side-effects from the previous render before we start.
  if (_localeUnsub) { try { _localeUnsub(); } catch {} _localeUnsub = null; }
  if (_tocObserver) { try { _tocObserver.disconnect(); } catch {} _tocObserver = null; }

  // Unknown sub-route — treat as a missing doc (not a router 404, since
  // /docs/* is intentionally a single registered pattern).
  if (!entry) {
    mount(container, renderNotAvailable({
      title: "Unknown documentation page",
      slug: pageKey || "(index)",
      body: "This page is not registered in the docs viewer. The sidebar lists every available doc.",
    }));
    return;
  }

  // Re-attach the locale subscriber so flipping EN ↔ PT-BR while a doc is
  // open re-renders the shell + re-fetches the markdown with the correct
  // sibling-file lookup.
  //
  // Two layers of recursion protection:
  //
  //   1. queueMicrotask defers the re-render so we cannot re-enter docsView
  //      synchronously inside setState's dispatch loop. state.js now also
  //      snapshots subscribers before iterating (which is the primary
  //      defense), but the microtask hop coalesces a burst of locale-
  //      affecting setState calls into one re-render and breaks any future
  //      synchronous re-entrancy that might be reintroduced.
  //   2. The renderToken check inside the microtask means a callback that
  //      was queued before a newer navigation took over silently drops on
  //      the floor — no duplicate fetch, no half-rendered article.
  _localeUnsub = onLocaleChange(() => {
    queueMicrotask(() => {
      if (myToken !== _renderToken) return;
      docsView({ params, container, query });
    });
  });

  await renderEntry(entry, pageKey, container, query);
}

async function renderEntry(entry, pageKey, container, query) {
  // Single-source-of-navigation shell. The sidebar is the only persistent
  // navigation surface; the TOC is opt-in per-article via the "Sections"
  // button placed in the article HEADER (page-header actions slot). The
  // TOC region lives inline inside docs-body and is hidden by default;
  // toggling adds .open which animates max-height. No permanent
  // second-column nav anywhere.
  const sectionsBtn = h("button", {
    id: "docs-sections-btn",
    class: "btn btn-ghost btn-sm docs-sections-btn",
    type: "button",
    "aria-expanded": "false",
    "aria-controls": "docs-toc",
    onclick: () => toggleSections(),
  }, "Sections ▾");

  mount(container,
    h("div", "docs-shell",
      h("section", "docs-body",
        pageHeader(entry.title, entry.section, [sectionsBtn]),
        h("div", { id: "docs-search-wrap", class: "docs-search-wrap" },
          h("input", {
            type: "search",
            id: "docs-search",
            class: "docs-search",
            placeholder: "Search within this page",
            "aria-label": "search within doc",
            oninput: (e) => applySearchHighlight(e.target.value),
          }),
        ),
        h("aside", {
          class: "docs-toc",
          id: "docs-toc",
          "aria-label": "sections in this article",
          hidden: true,
        },
          h("div", "docs-toc-skeleton", ""),
        ),
        h("article", { id: "docs-article", class: "docs-article" },
          h("p", "muted", "Loading " + entry.file + " …"),
        ),
      ),
    ),
  );

  if (!ALLOWED_EXT.test(entry.file)) {
    showNotAvailableInArticle(entry, "non-markdown source");
    return;
  }

  // Locale-aware fetch with English fallback:
  //   1. If the active locale has a sibling file (e.g. *.pt-BR.md), try
  //      that first.
  //   2. On 404, silently fall back to the English original. Network
  //      errors propagate to the not-available state so users see a
  //      clear failure mode rather than a stale shell.
  //
  // This is the "hybrid" piece of the strategy: a missing translation
  // never breaks the page — the reader sees the English source instead.
  const locale = getLocale();
  const localized = localizedDocFile(entry.file, locale);
  let src;
  try {
    src = await fetchDoc(localized);
    if (src == null) src = await fetchDoc(entry.file);
    if (src == null) throw new Error("HTTP 404");
  } catch (e) {
    showNotAvailableInArticle(entry, e.message);
    return;
  }

  const { html, headings } = renderMd(src);
  const article = document.querySelector("#docs-article");
  if (!article) return; // user navigated away
  article.innerHTML = html;

  // Wire after-render behaviors:
  //   1. apply syntax highlighting (no-op for unknown languages)
  //   2. interpolate copy buttons on code blocks
  //   3. hijack internal links
  //   4. populate the sticky TOC
  //   5. honor any incoming #fragment from the URL
  //   6. render any Mermaid diagrams the article contains
  applySyntaxHighlight(article);
  wireCopyButtons(article);
  wireInternalLinks(article, entry);
  renderToc(headings);
  honorHash(query && query._fragment);
  // Awaited so subsequent navigations don't race a half-rendered diagram
  // back into view; failure is contained per-block so the surrounding
  // article never breaks.
  await renderMermaidBlocks(article);
}

// fetchDoc — return the body text for a docs path, or null on any HTTP
// error (treated as "not present"). Network errors throw, mirroring the
// previous behavior so they still surface in the not-available state.
async function fetchDoc(file) {
  if (!file) return null;
  const r = await fetch(DOCS_BASE + "/" + file, { cache: "no-store" });
  if (!r.ok) return null;
  return await r.text();
}

// toggleSections — open / close the article's section list. The TOC is the
// only place article-internal navigation appears; it never coexists with
// the sidebar as a second nav column.
function toggleSections() {
  const toc = document.querySelector("#docs-toc");
  const btn = document.querySelector("#docs-sections-btn");
  if (!toc || !btn) return;
  const willOpen = !toc.classList.contains("open");
  toc.classList.toggle("open", willOpen);
  if (willOpen) toc.removeAttribute("hidden"); else toc.setAttribute("hidden", "");
  btn.setAttribute("aria-expanded", String(willOpen));
  btn.textContent = willOpen ? "Sections ▴" : "Sections ▾";
}

// hideSectionsButton — articles with no h2/h3/h4 headings don't need a
// section list. Hiding the button avoids opening an empty drawer.
function hideSectionsButton() {
  const btn = document.querySelector("#docs-sections-btn");
  if (btn) btn.style.display = "none";
}

// applySyntaxHighlight — walk every <code data-lang> emitted by the
// renderer and replace its body with the highlighted variant. The body is
// already HTML-escaped (markdown.js does that during fence handling); the
// highlighter consumes that string and weaves in <span class="tok-*">
// wrappers. Unknown languages pass through untouched so the visual result
// is at worst plain text — never a crash.
function applySyntaxHighlight(article) {
  article.querySelectorAll("pre.md-pre > code[class^='lang-']").forEach(codeEl => {
    const lang = (codeEl.className.match(/lang-(\S+)/) || [])[1] || "";
    if (!lang) return;
    codeEl.innerHTML = highlight(codeEl.innerHTML, lang);
  });
}

// renderNotAvailable — the canonical "no content" state. Used both for
// unknown sub-routes and for known routes whose source file failed to load.
function renderNotAvailable({ title, slug, body }) {
  return emptyState({
    icon: "📄",
    title: title || "Documentation not available yet",
    body: body || `The page "${slug}" hasn't been written or bundled yet.`,
  });
}

function showNotAvailableInArticle(entry, reason) {
  const article = document.querySelector("#docs-article");
  if (!article) return;
  article.innerHTML = "";
  const wrap = renderNotAvailable({
    title: "Documentation not available yet",
    slug: entry.file,
    body: `${entry.title} (${entry.file}) could not be loaded — ${reason}. The page is registered in the sidebar but its source isn't reachable at /admin/static/docs/${entry.file}. See the docs-loading section of the integration report.`,
  });
  article.appendChild(wrap);
  const toc = document.querySelector("#docs-toc");
  if (toc) toc.innerHTML = "";
}

// wireCopyButtons — every fenced code block emitted by markdown.js carries
// a <button class="md-copy">. We delegate the click handler at the article
// root so the buttons survive future re-renders of the same article.
function wireCopyButtons(article) {
  article.addEventListener("click", (e) => {
    const btn = e.target.closest("button.md-copy");
    if (!btn) return;
    const code = btn.parentElement && btn.parentElement.querySelector("code");
    if (!code) return;
    const text = code.innerText;
    const finish = (ok) => {
      const original = btn.textContent;
      btn.textContent = ok ? "copied" : "failed";
      btn.classList.add(ok ? "md-copy-ok" : "md-copy-err");
      setTimeout(() => {
        btn.textContent = original;
        btn.classList.remove("md-copy-ok", "md-copy-err");
      }, 1200);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(() => finish(true), () => finish(false));
    } else {
      // Legacy fallback for non-secure contexts.
      try {
        const ta = document.createElement("textarea");
        ta.value = text;
        ta.style.position = "fixed";
        ta.style.top = "-1000px";
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
        finish(true);
      } catch { finish(false); }
    }
  });
}

// wireInternalLinks — intercept clicks on relative or anchor-only links.
// Same-page #anchors scroll smoothly. Cross-doc paths are resolved against
// the current doc's location in the docs/ tree, then matched against
// DOC_MAP. Anything we can't map stays a native link (and, for external
// URLs, opens in a new tab thanks to the renderer).
function wireInternalLinks(article, entry) {
  article.addEventListener("click", (e) => {
    const a = e.target.closest("a");
    if (!a) return;
    const href = a.getAttribute("href") || "";
    if (!href) return;

    // External — let the browser handle it (renderer already set target=_blank).
    if (/^https?:|^mailto:/i.test(href)) return;

    // Same-page anchor — smooth-scroll, update hash without router noise.
    if (href.startsWith("#")) {
      e.preventDefault();
      scrollToAnchor(href.slice(1));
      // Reflect the fragment in the route so reload preserves position.
      const base = window.location.hash.split("#fragment=")[0];
      window.history.replaceState(null, "", base + "#fragment=" + encodeURIComponent(href.slice(1)));
      return;
    }

    // Cross-doc link inside docs/. Resolve relative to the current file.
    const resolved = resolveDocPath(entry.file, href);
    if (resolved) {
      const { slug, fragment } = resolved;
      e.preventDefault();
      const target = slug === "" ? "/docs" : "/docs/" + slug;
      navigate(target + (fragment ? "?_fragment=" + encodeURIComponent(fragment) : ""));
    }
    // Else: native navigation (e.g. ../../README.md). Lets the user escape
    // the SPA intentionally if they want to inspect the file directly.
  });
}

// resolveDocPath — turn a relative .md link into a DOC_MAP slug. Returns
// null if the path doesn't resolve to a known doc.
function resolveDocPath(fromFile, href) {
  // Drop a trailing #fragment first.
  let fragment = "";
  const hashIdx = href.indexOf("#");
  if (hashIdx >= 0) {
    fragment = href.slice(hashIdx + 1);
    href = href.slice(0, hashIdx);
  }
  if (!href || !/\.md$/i.test(href)) return null;

  // Resolve path relative to fromFile's directory in the docs/ tree.
  const parts = fromFile.split("/").slice(0, -1);
  for (const seg of href.split("/")) {
    if (seg === "" || seg === ".") continue;
    if (seg === "..") { parts.pop(); continue; }
    parts.push(seg);
  }
  const target = parts.join("/");

  // Find the slug whose file matches.
  for (const [slug, entry] of Object.entries(DOC_MAP)) {
    if (entry.file === target) return { slug, fragment };
  }
  return null;
}

// renderToc — populate the sticky table of contents from the heading list
// emitted by markdown.js. We render h2 and deeper, indented by level. h1 is
// the doc title (already shown in the page header).
function renderToc(headings) {
  const root = document.querySelector("#docs-toc");
  if (!root) return;
  root.innerHTML = "";
  const items = headings.filter(h => h.level >= 2 && h.level <= 4);
  if (!items.length) {
    // No sections — hide the trigger so the page header doesn't show a
    // button that opens an empty drawer.
    hideSectionsButton();
    return;
  }

  const title = document.createElement("div");
  title.className = "docs-toc-title";
  title.textContent = "ON THIS PAGE";
  root.appendChild(title);

  const list = document.createElement("ul");
  list.className = "docs-toc-list";
  for (const it of items) {
    const li = document.createElement("li");
    li.className = "docs-toc-item docs-toc-level-" + it.level;
    const a = document.createElement("a");
    a.href = "#" + it.slug;
    a.textContent = it.text;
    a.className = "docs-toc-link";
    a.addEventListener("click", (e) => {
      e.preventDefault();
      scrollToAnchor(it.slug);
      const base = window.location.hash.split("#fragment=")[0];
      window.history.replaceState(null, "", base + "#fragment=" + encodeURIComponent(it.slug));
      // Auto-collapse after a pick — the drawer's purpose is "jump
      // somewhere," not "stay open as a second nav column."
      const toc = document.querySelector("#docs-toc");
      const btn = document.querySelector("#docs-sections-btn");
      if (toc) { toc.classList.remove("open"); toc.setAttribute("hidden", ""); }
      if (btn) { btn.setAttribute("aria-expanded", "false"); btn.textContent = "Sections ▾"; }
    });
    li.appendChild(a);
    list.appendChild(li);
  }
  root.appendChild(list);

  // Active-section highlighting on scroll. IntersectionObserver lets us mark
  // the topmost visible heading without a scroll-listener spam loop.
  const headEls = items
    .map(it => document.getElementById(it.slug))
    .filter(Boolean);
  if (!("IntersectionObserver" in window) || headEls.length === 0) return;
  const tocLinks = new Map(items.map(it => [it.slug, root.querySelector(`a[href="#${cssEscape(it.slug)}"]`)]));
  const io = new IntersectionObserver((entries) => {
    entries.forEach(e => {
      const link = tocLinks.get(e.target.id);
      if (!link) return;
      if (e.isIntersecting) {
        // Clear previous, mark new.
        root.querySelectorAll(".docs-toc-link.active").forEach(n => n.classList.remove("active"));
        link.classList.add("active");
      }
    });
  }, { rootMargin: "-80px 0px -70% 0px", threshold: 0 });
  headEls.forEach(el => io.observe(el));
  // Hand the observer to the module-level handle so the next docsView call
  // can disconnect it before re-arming. Without this every navigation
  // leaked one observer; under the prior recursion bug, hundreds leaked
  // per second.
  _tocObserver = io;
}

function cssEscape(s) {
  // Minimal escape for use in querySelector when slugs contain odd chars.
  if (window.CSS && window.CSS.escape) return window.CSS.escape(s);
  return String(s).replace(/[^\w-]/g, "\\$&");
}

function scrollToAnchor(id) {
  const el = document.getElementById(id);
  if (!el) return;
  el.scrollIntoView({ behavior: "smooth", block: "start" });
  el.classList.add("md-anchor-flash");
  setTimeout(() => el.classList.remove("md-anchor-flash"), 1500);
}

function honorHash(fragment) {
  if (!fragment) return;
  // Defer until the article DOM is settled.
  setTimeout(() => scrollToAnchor(fragment), 50);
}

// applySearchHighlight — naive but useful: mark every occurrence of the
// query string in the article body, scroll to the first one, count matches.
// Doesn't replace browser Ctrl/⌘+F; it's an additional aid for skimming.
function applySearchHighlight(qRaw) {
  const article = document.querySelector("#docs-article");
  if (!article) return;
  // Remove previous marks.
  article.querySelectorAll("mark.docs-search-hit").forEach(m => {
    const t = document.createTextNode(m.textContent);
    m.parentNode.replaceChild(t, m);
  });
  article.normalize();
  const q = qRaw.trim();
  if (q.length < 2) return; // avoid spamming single-char marks

  const re = new RegExp(escapeRegExp(q), "gi");
  const walker = document.createTreeWalker(article, NodeFilter.SHOW_TEXT);
  const toProcess = [];
  let node;
  while ((node = walker.nextNode())) {
    // Skip text inside <pre> / <code> — corrupting code samples is worse
    // than missing matches there. Users have Ctrl/⌘+F for those.
    if (node.parentElement.closest("pre, code")) continue;
    if (re.test(node.nodeValue)) toProcess.push(node);
  }
  let hits = 0, first = null;
  for (const n of toProcess) {
    const frag = document.createDocumentFragment();
    let last = 0;
    n.nodeValue.replace(re, (m, idx) => {
      frag.appendChild(document.createTextNode(n.nodeValue.slice(last, idx)));
      const mark = document.createElement("mark");
      mark.className = "docs-search-hit";
      mark.textContent = m;
      if (!first) first = mark;
      frag.appendChild(mark);
      last = idx + m.length;
      hits++;
    });
    frag.appendChild(document.createTextNode(n.nodeValue.slice(last)));
    n.parentNode.replaceChild(frag, n);
  }
  if (first) first.scrollIntoView({ behavior: "smooth", block: "center" });
}

function escapeRegExp(s) {
  return String(s).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

// ─── Mermaid (lazy-loaded) ─────────────────────────────────────────────
//
// Strategy:
//
//   1. The markdown renderer emits `<div class="md-mermaid">` wrappers for
//      ```mermaid``` fences (see lib/markdown.js). Each wrapper contains an
//      empty `.md-mermaid-render` target and a <details> with the original
//      source in a `<pre><code class="lang-mermaid">…</code></pre>` block.
//   2. `renderMermaidBlocks(article)` scans the article AFTER markdown
//      rendering. If zero blocks are found, nothing is loaded — Admin mode
//      and Mermaid-free docs never touch the Mermaid library.
//   3. The first time a Mermaid block IS found, `loadMermaid()` dynamically
//      imports the library from the jsDelivr CDN (ESM). The import URL is
//      pinned to a major (Mermaid 11.x) so a future minor doesn't surprise
//      us in production. The module promise is memoised so subsequent
//      doc navigations within the same session re-use it.
//   4. Each block is rendered into a unique element id via
//      `mermaid.render(id, source)` which returns `{ svg }`. The SVG is
//      injected as innerHTML — Mermaid produces self-contained, sanitised
//      SVG (no <script>, no inline events) when run with
//      `securityLevel: 'strict'`, which we configure at initialize time.
//   5. Any failure (library load failed, network blocked by CSP, malformed
//      diagram syntax) marks the affected block with `md-mermaid-failed`
//      and expands the source <details>, leaving the article otherwise
//      untouched. This is the "Diagram failed to render" + raw source
//      fallback the spec requires.
//
// Security:
//
//   - `securityLevel: 'strict'`  → Mermaid HTML-encodes node labels and
//     refuses to execute inline JS callbacks (no `click X call myFn()`).
//   - `htmlLabels: false`        → Mermaid renders text as plain SVG
//     <text> nodes; label content cannot inject HTML or script tags.
//   - Source text is HTML-escaped before being stored in the source <pre>
//     by lib/markdown.js, so the un-rendered source is also safe.
//
// What this code never does:
//
//   - Modify, query, or import anything in admin/auth/CRUD/API paths.
//   - Load Mermaid eagerly. The dynamic import only fires when the
//     CURRENTLY-RENDERED doc has at least one mermaid fence.
//   - Inject anything other than Mermaid's own SVG output into the DOM.

const MERMAID_CDN = "https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs";
let _mermaidPromise = null;
let _mermaidThemeApplied = null;

function loadMermaid() {
  // One in-flight load, regardless of how many blocks the article has.
  if (_mermaidPromise) return _mermaidPromise;
  _mermaidPromise = import(/* @vite-ignore */ MERMAID_CDN).then(mod => {
    const mermaid = mod.default || mod.mermaid || mod;
    if (!mermaid || typeof mermaid.initialize !== "function") {
      throw new Error("Mermaid export shape unexpected");
    }
    initializeMermaid(mermaid);
    return mermaid;
  }).catch(err => {
    // Reset on failure so a subsequent navigation can retry (e.g. after
    // the network comes back online). A persistent failure simply means
    // every block falls through to the visible fallback below.
    _mermaidPromise = null;
    throw err;
  });
  return _mermaidPromise;
}

function initializeMermaid(mermaid) {
  const theme = document.body.classList.contains("theme-light") ? "default" : "dark";
  mermaid.initialize({
    startOnLoad: false,
    theme,
    // Strict security: no HTML labels, no click-callback evaluation, no
    // foreignObject, no font-family injection. Diagrams rendered under
    // this setting cannot escape into the document.
    securityLevel: "strict",
    htmlLabels: false,
    flowchart: { htmlLabels: false, useMaxWidth: true },
    sequence:  { useMaxWidth: true },
    er:        { useMaxWidth: true },
    class:     { useMaxWidth: true },
    state:     { useMaxWidth: true },
    journey:   { useMaxWidth: true },
    pie:       { useMaxWidth: true },
    fontFamily: 'ui-sans-serif, -apple-system, BlinkMacSystemFont, "Inter", "Segoe UI", Roboto, sans-serif',
  });
  _mermaidThemeApplied = theme;
}

async function renderMermaidBlocks(article) {
  const blocks = Array.from(article.querySelectorAll(".md-mermaid[data-mermaid-block]"));
  if (blocks.length === 0) return; // Mermaid library never loaded for this doc.

  let mermaid;
  try {
    mermaid = await loadMermaid();
  } catch (e) {
    blocks.forEach(b => failMermaidBlock(b, "library could not be loaded"));
    console.warn("docs: failed to load Mermaid:", e && e.message);
    return;
  }

  // Re-initialise if the user toggled themes between document loads.
  const wantTheme = document.body.classList.contains("theme-light") ? "default" : "dark";
  if (_mermaidThemeApplied !== wantTheme) initializeMermaid(mermaid);

  for (let i = 0; i < blocks.length; i++) {
    const block = blocks[i];
    const codeEl = block.querySelector("code.lang-mermaid");
    const target = block.querySelector(".md-mermaid-render");
    if (!codeEl || !target) continue;

    const source = codeEl.textContent;
    const renderId = `mmd-${Date.now().toString(36)}-${i}`;
    try {
      const { svg } = await mermaid.render(renderId, source);
      // Mermaid returns sanitised SVG when securityLevel is strict.
      target.innerHTML = svg;
      target.setAttribute("data-rendered", "1");
    } catch (e) {
      failMermaidBlock(block, e && e.message ? e.message.split("\n")[0] : null);
    }
  }
}

function failMermaidBlock(block, reason) {
  block.classList.add("md-mermaid-failed");
  const target = block.querySelector(".md-mermaid-render");
  if (target) {
    // The reason is appended as a tiny line under the headline so the
    // reader can self-diagnose typos without opening DevTools, but is
    // intentionally short — full diagnostics belong in the console.
    const safe = reason ? String(reason).replace(/[<&>]/g, c => ({"<":"&lt;","&":"&amp;",">":"&gt;"}[c])) : "";
    target.innerHTML =
      `<div class="md-mermaid-error">` +
        `<strong>Diagram failed to render</strong>` +
        (safe ? `<div class="md-mermaid-error-reason">${safe}</div>` : "") +
        `<div class="md-mermaid-error-hint">Showing source instead.</div>` +
      `</div>`;
  }
  const det = block.querySelector("details.md-mermaid-source");
  if (det) det.setAttribute("open", "");
}
