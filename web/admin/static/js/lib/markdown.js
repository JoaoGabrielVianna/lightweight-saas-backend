// markdown.js — markdown → HTML renderer for the docs viewer.
//
// Output contract:
//
//   render(src) → { html, headings, anchors }
//
//     html      — string of HTML, safe to .innerHTML into a sandbox container.
//                 Every text node passes through escapeHtml; the renderer's
//                 own tag set is closed.
//     headings  — [{ level, text, slug }] for TOC generation; source order.
//     anchors   — Set<slug> used internally for slug deduplication.
//
// Features supported (every one is exercised by docs in this repo):
//
//   ATX headings (1-6, auto-slugged with permalink), fenced code with
//   language hint + always-visible copy button + language badge, GFM pipe
//   tables WITH alignment (`:---`, `:---:`, `---:`), GFM task lists
//   (`- [ ]`, `- [x]`), blockquotes (recursive), one level of nested lists,
//   inline images/links (relative paths preserved for SPA interception),
//   angle-bracket autolinks `<https://...>`, inline code, **bold** / *italic*
//   / __bold__ / _italic_ / ~~strike~~, horizontal rules.
//
// Bug fix history (against the prior implementation in this same path):
//
//   1. Link labels containing inline code rendered as the literal string
//      "undefined". Root cause: a recursive renderInline() call on the
//      label could not see the parent call's code-stash table, so the
//      placeholder restore returned undefined. Fixed by replacing the
//      recursion with a non-recursive applyEmphasis() helper that runs
//      against the same stash.
//   2. Table separator alignment hints (`:---:`) were parsed and then
//      thrown away. Now propagated to <th>/<td> via the
//      `md-align-{left,center,right}` class so docs.css can render them.
//   3. Task list items rendered as literal `[ ] Foo` text. Now emit a
//      disabled checkbox and a `md-li-task` class.
//   4. Angle-bracket autolinks `<https://x>` were HTML-escaped to
//      `&lt;https://x&gt;`. Now emit a real anchor.
//
// Deliberately unsupported (no doc uses them today; adding any of these is a
// 5-minute extension):
//
//   - Setext headings (`===` / `---` underlines).
//   - Reference-style links and link defs (`[label]: url "title"`).
//   - Footnotes, definition lists.
//   - Inline raw HTML — every <tag> emitted comes from this file.
//   - Math, mermaid, admonitions.
//
// What we WANT to preserve:
//
//   - Zero external dependencies. The project ships no package manager.
//   - A closed tag set. The renderer must not surface arbitrary HTML even
//     if a doc tries to inline it; doc authors get escaped text instead.

const RE_FENCE     = /^```(\s*([\w.+-]+))?\s*$/;
const RE_HR        = /^---+\s*$/;
const RE_HEAD      = /^(#{1,6})\s+(.+?)\s*#*\s*$/;
const RE_BQ        = /^>\s?(.*)$/;
const RE_OL        = /^(\s*)(\d+)\.\s+(.*)$/;
const RE_UL        = /^(\s*)([-*+])\s+(.*)$/;
const RE_TABLE     = /^\|(.+)\|\s*$/;
const RE_TABLE_SEP = /^\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)+\|?\s*$/;
const RE_TASK      = /^\[([ xX])\]\s+(.*)$/;

export function render(src) {
  const headings = [];
  const anchors = new Set();
  const lines = String(src).replace(/\r\n?/g, "\n").split("\n");
  const out = [];

  let i = 0;
  while (i < lines.length) {
    const line = lines[i];

    // Fenced code — preserve verbatim, escape HTML. Language tag is
    // reflected as data-lang AND a class so docs.js can hand the body to a
    // syntax highlighter without re-parsing.
    const fenceMatch = line.match(RE_FENCE);
    if (fenceMatch) {
      const lang = (fenceMatch[2] || "").toLowerCase();
      const body = [];
      i++;
      while (i < lines.length && !RE_FENCE.test(lines[i])) {
        body.push(lines[i]);
        i++;
      }
      if (i < lines.length) i++; // skip closing fence
      const safeBody = escapeHtml(body.join("\n"));

      // Mermaid fence — emit a render-target wrapper plus a collapsible
      // <details> carrying the original source. The post-render pass in
      // views/docs.js scans for `.md-mermaid` nodes; if any exist, it
      // lazily imports the Mermaid library (CDN ESM) and replaces each
      // target with the rendered SVG. The source <pre> reuses the same
      // .md-pre / .md-copy markup so copy-source works for free and any
      // failure path simply leaves the source visible — never breaks
      // the surrounding article.
      if (lang === "mermaid") {
        out.push(
          `<div class="md-mermaid" data-mermaid-block="1">` +
            `<div class="md-mermaid-render" aria-label="mermaid diagram"></div>` +
            `<details class="md-mermaid-source">` +
              `<summary>View source</summary>` +
              `<pre class="md-pre" data-lang="mermaid">` +
                `<span class="md-lang">mermaid</span>` +
                `<button class="md-copy" type="button" aria-label="copy">copy</button>` +
                `<code class="lang-mermaid">${safeBody}</code>` +
              `</pre>` +
            `</details>` +
          `</div>`
        );
        continue;
      }

      const langAttr = lang ? ` data-lang="${escapeAttr(lang)}"` : "";
      const langClass = lang ? ` class="lang-${escapeAttr(lang)}"` : "";
      const badge = lang ? `<span class="md-lang">${escapeHtml(lang)}</span>` : "";
      out.push(
        `<pre class="md-pre"${langAttr}>${badge}` +
        `<button class="md-copy" type="button" aria-label="copy">copy</button>` +
        `<code${langClass}>${safeBody}</code></pre>`
      );
      continue;
    }

    if (!line.trim()) { i++; continue; }

    if (RE_HR.test(line)) { out.push("<hr>"); i++; continue; }

    const h = line.match(RE_HEAD);
    if (h) {
      const level = h[1].length;
      const text  = h[2];
      const slug  = uniqueSlug(slugify(text), anchors);
      headings.push({ level, text, slug });
      out.push(
        `<h${level} id="${slug}" class="md-h md-h${level}">` +
        `<a class="md-anchor" href="#${slug}" aria-label="permalink to this section">#</a>` +
        `${renderInline(text)}</h${level}>`
      );
      i++;
      continue;
    }

    if (RE_BQ.test(line)) {
      const buf = [];
      while (i < lines.length && RE_BQ.test(lines[i])) {
        buf.push(lines[i].replace(RE_BQ, "$1"));
        i++;
      }
      const inner = render(buf.join("\n")).html;
      out.push(`<blockquote class="md-bq">${inner}</blockquote>`);
      continue;
    }

    if (RE_TABLE.test(line) && i + 1 < lines.length && RE_TABLE_SEP.test(lines[i + 1])) {
      const consumed = renderTable(lines, i);
      out.push(consumed.html);
      i = consumed.next;
      continue;
    }

    if (RE_UL.test(line) || RE_OL.test(line)) {
      const consumed = renderList(lines, i);
      out.push(consumed.html);
      i = consumed.next;
      continue;
    }

    // Paragraph — accumulate consecutive non-empty, non-block lines.
    const para = [line];
    i++;
    while (i < lines.length && lines[i].trim() && !isBlockStart(lines[i], lines[i + 1] || "")) {
      para.push(lines[i]);
      i++;
    }
    out.push(`<p class="md-p">${renderInline(para.join(" "))}</p>`);
  }

  return { html: out.join("\n"), headings, anchors };
}

function isBlockStart(line, next) {
  return RE_FENCE.test(line)
      || RE_HEAD.test(line)
      || RE_HR.test(line)
      || RE_BQ.test(line)
      || RE_UL.test(line)
      || RE_OL.test(line)
      || (RE_TABLE.test(line) && RE_TABLE_SEP.test(next));
}

// ─── Tables ─────────────────────────────────────────────────────────────

function renderTable(lines, start) {
  const header = parseRow(lines[start]);
  const aligns = tableAlignments(lines[start + 1]);
  let i = start + 2;
  const body = [];
  while (i < lines.length && RE_TABLE.test(lines[i])) {
    body.push(parseRow(lines[i]));
    i++;
  }
  const cls = (idx) => aligns[idx] ? ` class="md-align-${aligns[idx]}"` : "";
  const headHtml = "<tr>" + header.map((c, idx) => `<th${cls(idx)}>${renderInline(c)}</th>`).join("") + "</tr>";
  const bodyHtml = body.map(r => "<tr>" + r.map((c, idx) => `<td${cls(idx)}>${renderInline(c)}</td>`).join("") + "</tr>").join("");
  return {
    html: `<div class="md-table-wrap"><table class="md-table"><thead>${headHtml}</thead><tbody>${bodyHtml}</tbody></table></div>`,
    next: i,
  };
}

function parseRow(line) {
  return line.replace(/^\|/, "").replace(/\|\s*$/, "").split("|").map(s => s.trim());
}

function tableAlignments(sep) {
  return sep.replace(/^\|/, "").replace(/\|\s*$/, "").split("|").map(s => {
    const t = s.trim();
    const left = t.startsWith(":");
    const right = t.endsWith(":");
    if (left && right) return "center";
    if (right)         return "right";
    if (left)          return "left";
    return "";
  });
}

// ─── Lists (with task-list support) ─────────────────────────────────────

function renderList(lines, start) {
  const ordered = RE_OL.test(lines[start]);
  const items = [];
  let i = start;
  while (i < lines.length) {
    const m = ordered ? lines[i].match(RE_OL) : lines[i].match(RE_UL);
    if (!m) break;
    const indent = m[1].length;
    let content = m[3];

    // Task-list detection runs BEFORE renderInline so the leading `[ ]`
    // never has a chance to be HTML-escaped into the rendered output.
    const tm = content.match(RE_TASK);
    const isTask = !!tm;
    const checked = isTask && tm[1].toLowerCase() === "x";
    if (isTask) content = tm[2];

    i++;
    const childLines = [];
    while (i < lines.length) {
      const nxt = lines[i];
      if (!nxt.trim()) { childLines.push(""); i++; continue; }
      const nestedUl = nxt.match(RE_UL);
      const nestedOl = nxt.match(RE_OL);
      if ((nestedUl && nestedUl[1].length > indent) || (nestedOl && nestedOl[1].length > indent)) {
        childLines.push(nxt);
        i++;
        continue;
      }
      if (nxt.startsWith(" ".repeat(Math.max(2, indent + 2)))) {
        childLines.push(nxt.replace(new RegExp(`^ {${indent + 2}}`), ""));
        i++;
        continue;
      }
      break;
    }
    let childHtml = "";
    if (childLines.length) {
      const trimmed = childLines.join("\n").replace(/^\n+|\n+$/g, "");
      if (trimmed) childHtml = render(trimmed).html;
    }
    const taskHtml = isTask
      ? `<input type="checkbox" class="md-task"${checked ? " checked" : ""} disabled> `
      : "";
    const liClass = isTask ? "md-li md-li-task" : "md-li";
    items.push(`<li class="${liClass}">${taskHtml}${renderInline(content)}${childHtml ? "\n" + childHtml : ""}</li>`);
  }
  const tag = ordered ? "ol" : "ul";
  const ulClass = items.some(s => s.includes("md-li-task")) ? "md-list md-list-task" : "md-list";
  return { html: `<${tag} class="${ulClass}">${items.join("")}</${tag}>`, next: i };
}

// ─── Inline pass — code spans, autolinks, images, links, emphasis ──────

function renderInline(src) {
  const stash = [];
  let text = String(src);

  // 1. Stash inline code spans behind \x00C<idx>\x00 placeholders. Done
  //    first so emphasis/link regex cannot see backticks.
  text = text.replace(/`([^`\n]+)`/g, (_, code) => {
    const i = stash.push(`<code class="md-code">${escapeHtml(code)}</code>`) - 1;
    return `\x00C${i}\x00`;
  });

  // 2. Stash angle-bracket autolinks <https://...> / <mailto:...>. Done
  //    before HTML escaping so the angle brackets don't leak as &lt;.
  text = text.replace(/<((?:https?|mailto):[^>\s]+)>/g, (_, url) => {
    const safe = escapeAttr(url);
    const isMail = /^mailto:/i.test(url);
    const display = escapeHtml(isMail ? url.replace(/^mailto:/i, "") : url);
    const cls = "md-link md-link-ext";
    const target = isMail ? "" : ' target="_blank" rel="noopener noreferrer"';
    const i = stash.push(`<a class="${cls}" href="${safe}"${target}>${display}</a>`) - 1;
    return `\x00L${i}\x00`;
  });

  // 3. Now safe to escape the rest. \x00 control chars survive verbatim.
  text = escapeHtml(text);

  // 4. Images first (`![alt](url)`) so the link regex below doesn't catch
  //    the `[alt]` portion.
  text = text.replace(/!\[([^\]]*)\]\(([^)\s]+)(?:\s+"([^"]*)")?\)/g,
    (_, alt, url, title) => {
      const t = title ? ` title="${escapeAttr(title)}"` : "";
      return `<img class="md-img" src="${escapeAttr(url)}" alt="${escapeAttr(alt)}"${t} loading="lazy">`;
    });

  // 5. Links `[text](url)` — label may contain inline code placeholders
  //    and/or emphasis markup; both are handled by applyEmphasis, NOT by
  //    a recursive renderInline (which was the root cause of the prior
  //    "undefined" bug — recursion lost the stash table).
  text = text.replace(/\[([^\]]+)\]\(([^)\s]+)(?:\s+"([^"]*)")?\)/g,
    (_, label, url, title) => {
      const t = title ? ` title="${escapeAttr(title)}"` : "";
      const external = /^https?:|^mailto:/i.test(url);
      const target = external ? ' target="_blank" rel="noopener noreferrer"' : "";
      const cls = external ? "md-link md-link-ext" : "md-link";
      return `<a class="${cls}" href="${escapeAttr(url)}"${t}${target}>${applyEmphasis(label)}</a>`;
    });

  // 6. Emphasis on whatever is left at the outer level.
  text = applyEmphasis(text);

  // 7. Restore both code spans and autolink anchors from the single stash.
  text = text.replace(/\x00[CL](\d+)\x00/g, (_, i) => stash[Number(i)]);
  return text;
}

// applyEmphasis — bold / italic / strikethrough, NO code / link logic.
// Safe to call repeatedly on partial text (link labels, etc.) without
// disturbing the outer code-span stash.
function applyEmphasis(s) {
  let t = s;
  t = t.replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>");
  t = t.replace(/__([^_\n]+)__/g,     "<strong>$1</strong>");
  t = t.replace(/(?<![*\w])\*([^*\n]+)\*(?!\w)/g, "<em>$1</em>");
  t = t.replace(/(?<![_\w])_([^_\n]+)_(?!\w)/g,   "<em>$1</em>");
  t = t.replace(/~~([^~\n]+)~~/g,    "<del>$1</del>");
  return t;
}

function escapeHtml(s) {
  return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

function escapeAttr(s) {
  return String(s).replace(/&/g, "&amp;").replace(/"/g, "&quot;");
}

// slugify — GitHub-flavored: lowercase, strip punctuation, hyphenate
// whitespace. Stable across docs so cross-doc fragment links resolve.
export function slugify(text) {
  return String(text)
    .toLowerCase()
    .replace(/[`*_~]/g, "")
    .replace(/[^\w\s-]/g, "")
    .trim()
    .replace(/\s+/g, "-")
    .replace(/-+/g, "-");
}

function uniqueSlug(base, set) {
  if (!set.has(base) && base) { set.add(base); return base; }
  let n = 1;
  while (set.has(`${base}-${n}`)) n++;
  const slug = `${base}-${n}`;
  set.add(slug);
  return slug;
}
