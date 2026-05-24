// highlight.js — micro syntax highlighter for the docs viewer.
//
// Scope: just the languages this project's docs actually use.
// `grep -hE '^```[a-z]' docs/**/*.md | sort -u` returns:
//   bash, cron, dotenv, go, ini, json, jsonc, sh, text, yaml
//
// Why hand-rolled and not Prism / highlight.js:
//
//   - The project ships no package manager and no build step. Every JS
//     dependency is a checked-in file. The smallest practical Prism build
//     covering all of {sh, go, json, yaml} is ~30 KB minified + ~10 KB CSS.
//     This file is ~5 KB and covers the same languages well enough that a
//     reader can scan a snippet, copy it, and trust what they read.
//   - We don't need perfect tokenization — we need the eye to find the
//     comments, strings, and command names quickly. Pragmatic > pedantic.
//
// API:
//
//   highlight(rawCode: string, lang: string) → htmlString
//
//     `rawCode` MUST already be HTML-escaped (the renderer hands us
//     escapeHtml output). We treat it as a token stream and weave in
//     <span class="tok-*"> wrappers. Unrecognized languages pass through.
//
// Token classes emitted: tok-com (comment), tok-str (string), tok-num
// (number), tok-kw (keyword/builtin), tok-key (property key, JSON/YAML),
// tok-bool (true/false/null), tok-var (sh variable / env-var reference).

const KW = {
  go: new Set([
    "package","import","func","return","if","else","for","range","switch","case","default",
    "break","continue","defer","go","select","var","const","type","struct","interface",
    "map","chan","nil","true","false","make","new","len","cap","append","copy","delete","panic",
    "recover","string","int","int8","int16","int32","int64","uint","uint8","uint16","uint32",
    "uint64","float32","float64","bool","byte","rune","error",
  ]),
  js: new Set([
    "const","let","var","function","return","if","else","for","while","do","switch","case","default",
    "break","continue","new","class","extends","super","this","import","export","from","as","try","catch",
    "finally","throw","typeof","instanceof","in","of","async","await","null","undefined","true","false","yield",
  ]),
};

// String + comment patterns shared between many languages. Order matters:
// strings get stashed first so a `#` or `//` inside a string is not seen as
// a comment.
function tokeniseShared(code, opts) {
  const stash = [];
  let i = 0;
  let s = code;
  // 1. Stash strings.
  if (opts.strings !== false) {
    s = s.replace(/("(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*'|`(?:\\.|[^`\\\n])*`)/g, m => {
      const k = stash.push(`<span class="tok-str">${m}</span>`) - 1;
      return `\x00S${k}\x00`;
    });
  }
  // 2. Stash comments per language style.
  if (opts.line) {
    const re = opts.line === "#" ? /(^|[^&])(#[^\n]*)/g
              : opts.line === "//" ? /(^|[^:])(\/\/[^\n]*)/g
              : null;
    if (re) {
      s = s.replace(re, (_, pre, c) => {
        const k = stash.push(`<span class="tok-com">${c}</span>`) - 1;
        return pre + `\x00S${k}\x00`;
      });
    }
  }
  if (opts.block === "/* */") {
    s = s.replace(/\/\*[\s\S]*?\*\//g, m => {
      const k = stash.push(`<span class="tok-com">${m}</span>`) - 1;
      return `\x00S${k}\x00`;
    });
  }
  return { text: s, stash };
}

function restore({ text, stash }) {
  return text.replace(/\x00S(\d+)\x00/g, (_, k) => stash[Number(k)]);
}

function paintKeywords(text, set) {
  // \b is roughly word-boundary; OK for ASCII keywords.
  return text.replace(/\b([A-Za-z_][A-Za-z0-9_]*)\b/g, (m, w) =>
    set.has(w) ? `<span class="tok-kw">${m}</span>` : m
  );
}
function paintNumbers(text) {
  return text.replace(/\b(0x[0-9a-fA-F]+|\d+(?:\.\d+)?(?:e[+-]?\d+)?)\b/g, `<span class="tok-num">$1</span>`);
}

// ─── per-language ──────────────────────────────────────────────────────

function hlShell(code) {
  // Strings + # comments. Then sh variables ($FOO / ${FOO}). Then a small
  // set of common command names so the eye anchors quickly.
  const cmds = new Set([
    "curl","docker","docker-compose","make","go","sh","bash","env","cat","echo","grep","awk","sed",
    "ls","cd","cp","mv","rm","mkdir","touch","tee","chmod","chown","tar","gzip","gunzip","xargs",
    "sort","uniq","wc","head","tail","find","jq","openssl","kubectl","helm","git","npm","node","python","python3",
  ]);
  let t = tokeniseShared(code, { line: "#" });
  // Variables AFTER strings (which are stashed) so $FOO inside "..." isn't double-tagged.
  t.text = t.text.replace(/(\$\{[^}]+\}|\$[A-Za-z_][A-Za-z0-9_]*)/g, `<span class="tok-var">$1</span>`);
  // Command names — only when they appear at the very start of a line (after
  // optional leading whitespace) or after `|`, `;`, `&&`, `||`. Heuristic.
  t.text = t.text.replace(/(^|[\n|;]|&&|\|\|)\s*([a-zA-Z][\w-]*)/g,
    (m, lead, w) => cmds.has(w) ? `${lead}<span class="tok-kw">${w}</span>` : m);
  return restore(t);
}

function hlGo(code) {
  let t = tokeniseShared(code, { line: "//", block: "/* */" });
  t.text = paintKeywords(t.text, KW.go);
  t.text = paintNumbers(t.text);
  return restore(t);
}

function hlJs(code) {
  let t = tokeniseShared(code, { line: "//", block: "/* */" });
  t.text = paintKeywords(t.text, KW.js);
  t.text = paintNumbers(t.text);
  return restore(t);
}

function hlJson(code) {
  // JSON: key strings (followed by :), value strings, numbers, true/false/null.
  // jsonc additionally allows // and /* */ comments.
  let t = tokeniseShared(code, { line: "//", block: "/* */" });
  // Re-stash strings then promote the "key" ones (those followed by a colon).
  // Simpler: paint a "key" highlight after the fact by finding stashed string
  // immediately before optional whitespace + colon.
  t.text = t.text.replace(/\x00S(\d+)\x00(\s*):/g, (_, k, ws) => {
    const span = t.stash[Number(k)].replace('class="tok-str"', 'class="tok-key"');
    t.stash[Number(k)] = span;
    return `\x00S${k}\x00${ws}:`;
  });
  t.text = t.text.replace(/\b(true|false|null)\b/g, `<span class="tok-bool">$1</span>`);
  t.text = paintNumbers(t.text);
  return restore(t);
}

function hlYaml(code) {
  // YAML / INI / cron / dotenv — comment-and-string focus is enough.
  let t = tokeniseShared(code, { line: "#" });
  // Highlight keys (foo: bar) at line start. Apply on the non-stashed text.
  t.text = t.text.replace(/(^|\n)(\s*)([A-Za-z_][\w.-]*)(\s*:)/g,
    (_, br, sp, key, colon) => `${br}${sp}<span class="tok-key">${key}</span>${colon}`);
  t.text = paintNumbers(t.text);
  t.text = t.text.replace(/\b(true|false|null|yes|no|on|off)\b/gi, `<span class="tok-bool">$1</span>`);
  return restore(t);
}

function hlDotenv(code) {
  // KEY=value with optional # comment.
  let t = tokeniseShared(code, { line: "#" });
  t.text = t.text.replace(/(^|\n)([A-Za-z_][A-Za-z0-9_]*)(=)/g,
    (_, br, k, eq) => `${br}<span class="tok-key">${k}</span>${eq}`);
  return restore(t);
}

const HIGHLIGHTERS = {
  sh: hlShell, bash: hlShell, shell: hlShell,
  go: hlGo, golang: hlGo,
  js: hlJs, javascript: hlJs, ts: hlJs, typescript: hlJs,
  json: hlJson, jsonc: hlJson,
  yaml: hlYaml, yml: hlYaml, ini: hlYaml, toml: hlYaml, cron: hlYaml,
  dotenv: hlDotenv, env: hlDotenv,
};

/**
 * Highlight an already-HTML-escaped code body. Unknown languages return the
 * input verbatim so callers can blindly route everything through here.
 */
export function highlight(escapedCode, lang) {
  const fn = HIGHLIGHTERS[(lang || "").toLowerCase()];
  return fn ? fn(escapedCode) : escapedCode;
}

export function knownLanguages() {
  return Object.keys(HIGHLIGHTERS);
}
