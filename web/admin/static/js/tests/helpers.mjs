// helpers.mjs — shared test scaffolding. NOT a test file: `node --test` only
// runs files matching *.test.*, so this is imported, never executed directly.
//
// Three stubs, in the order a module needs them:
//
//   makeStorage  — localStorage / sessionStorage
//   installDOM   — the sliver of the DOM that lib/dom.js and components/modal.js
//                  actually touch
//   fetchStub    — a scriptable fetch, including a controllable delay so the
//                  workspace-isolation races can be driven deterministically
//
// The DOM stub is deliberately small. A full jsdom dependency would be the
// first runtime dependency this repo has, for a console that is 6 000 lines of
// dependency-free vanilla JS; the stub below is ~80 lines and covers every DOM
// operation the code under test performs. If a future component needs more, add
// it here rather than reaching for a framework.

export function makeStorage(initial) {
  const data = { ...(initial || {}) };
  return {
    getItem(k) { return Object.prototype.hasOwnProperty.call(data, k) ? data[k] : null; },
    setItem(k, v) { data[k] = String(v); },
    removeItem(k) { delete data[k]; },
    clear() { for (const k of Object.keys(data)) delete data[k]; },
    _data: data,
  };
}

// ─── DOM ────────────────────────────────────────────────────────────────────

class StubNode {
  constructor() {
    this.childNodes = [];
    this.parentNode = null;
  }
}

class StubText extends StubNode {
  constructor(text) { super(); this.nodeValue = String(text); }
  get textContent() { return this.nodeValue; }
}

class StubElement extends StubNode {
  constructor(tag) {
    super();
    this.tagName = String(tag).toUpperCase();
    this.attributes = {};
    this.style = {};
    this.dataset = {};
    this.className = "";
    this.listeners = {};
    this._innerHTML = "";
    this.disabled = false;
  }

  setAttribute(k, v) { this.attributes[k] = String(v); }
  getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attributes, k) ? this.attributes[k] : null; }
  removeAttribute(k) { delete this.attributes[k]; }

  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  removeEventListener(type, fn) {
    this.listeners[type] = (this.listeners[type] || []).filter((f) => f !== fn);
  }
  // dispatch is test-only sugar; production code never calls it.
  dispatch(type, event = {}) {
    for (const fn of this.listeners[type] || []) fn({ target: this, currentTarget: this, ...event });
  }

  appendChild(node) {
    node.parentNode = this;
    this.childNodes.push(node);
    return node;
  }

  set innerHTML(v) {
    // The production code only ever assigns "" (to clear) or trusted icon
    // markup. Clearing must actually drop children, which is what mount()
    // relies on.
    this._innerHTML = String(v);
    if (this._innerHTML === "") this.childNodes = [];
  }
  get innerHTML() { return this._innerHTML; }

  get textContent() {
    return this.childNodes.map((c) => c.textContent ?? "").join("");
  }

  // querySelector supports "#id" and a bare tag name — everything the code
  // under test uses.
  querySelector(sel) {
    for (const child of this.childNodes) {
      if (!(child instanceof StubElement)) continue;
      if (matches(child, sel)) return child;
      const nested = child.querySelector(sel);
      if (nested) return nested;
    }
    return null;
  }

  querySelectorAll(sel) {
    const out = [];
    for (const child of this.childNodes) {
      if (!(child instanceof StubElement)) continue;
      if (matches(child, sel)) out.push(child);
      out.push(...child.querySelectorAll(sel));
    }
    return out;
  }

  contains(node) {
    if (node === this) return true;
    return this.childNodes.some((c) => c === node || (c instanceof StubElement && c.contains(node)));
  }
}

function matches(el, sel) {
  if (sel.startsWith("#")) return el.attributes.id === sel.slice(1);
  if (sel.startsWith(".")) return String(el.className).split(/\s+/).includes(sel.slice(1));
  return el.tagName === sel.toUpperCase();
}

/**
 * installDOM — put a minimal document/Node on globalThis.
 *
 * Returns the root element so a test can inspect what was rendered, plus the
 * `#modal-root` element components/modal.js mounts into.
 */
export function installDOM() {
  const root = new StubElement("div");
  const body = new StubElement("body");
  const modalRoot = new StubElement("div");
  modalRoot.setAttribute("id", "modal-root");
  body.appendChild(modalRoot);

  const doc = {
    body,
    createElement: (tag) => new StubElement(tag),
    createTextNode: (t) => new StubText(t),
    querySelector: (sel) => (matches(modalRoot, sel) ? modalRoot : body.querySelector(sel)),
    addEventListener() {},
    removeEventListener() {},
  };
  // body.style must support removeProperty — modal.js calls it on close.
  body.style.removeProperty = () => {};

  globalThis.Node = StubNode;
  globalThis.document = doc;
  return { root, body, modalRoot, doc, StubElement };
}

// ─── fetch ──────────────────────────────────────────────────────────────────

/**
 * makeResponse builds a fetch Response-alike.
 *
 * `headers` is the real contract point for the request-id tests: /v1 echoes
 * X-Request-Id on every response, and APIError falls back to it when the body
 * never reached the error envelope.
 */
export function makeResponse({ status = 200, body = null, headers = {} } = {}) {
  const text = typeof body === "string" ? body : body == null ? "" : JSON.stringify(body);
  const lower = Object.fromEntries(Object.entries(headers).map(([k, v]) => [k.toLowerCase(), v]));
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: { get: (k) => lower[String(k).toLowerCase()] ?? null },
    text: async () => text,
  };
}

/**
 * fetchStub — a scriptable fetch.
 *
 * `routes` maps a matcher (string prefix or RegExp) to either a response spec
 * or a function (url, options) => spec. A spec may carry `delayMs`, which is
 * what lets a test start request A, switch workspace, let B finish first, and
 * then release A — the exact ordering Phase 8 is about.
 */
export function fetchStub(routes) {
  const calls = [];
  const fn = async (url, options = {}) => {
    calls.push({ url: String(url), options, method: options.method || "GET" });

    for (const [matcher, handler] of routes) {
      const hit = matcher instanceof RegExp ? matcher.test(url) : String(url).startsWith(matcher);
      if (!hit) continue;

      const spec = typeof handler === "function" ? handler(String(url), options) : handler;
      if (spec && spec.delayMs) await new Promise((r) => setTimeout(r, spec.delayMs));
      if (spec && spec.gate) await spec.gate;
      // An aborted request rejects, exactly as the platform does — apiTry
      // turns that into {ok:false, status:0}.
      if (options.signal?.aborted) {
        const err = new Error("The operation was aborted.");
        err.name = "AbortError";
        throw err;
      }
      return makeResponse(spec || {});
    }
    return makeResponse({ status: 404, body: { error: "no stub route for " + url } });
  };
  fn.calls = calls;
  fn.urls = () => calls.map((c) => c.url);
  return fn;
}

/** deferred — a promise plus its resolver, for driving request ordering. */
export function deferred() {
  let resolve;
  const promise = new Promise((r) => { resolve = r; });
  return { promise, resolve };
}
