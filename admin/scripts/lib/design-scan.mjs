// Static scanners for the design layer (design/**). No dependencies. CONSERVATIVE on purpose: a false positive
// costs an AI retry, a false negative could ship hostile code. The scan is defence in depth; the real
// boundaries are the core (permissions, state rules, confirmations, no tokens in ctx), the safe DOM builder
// and a strict CSP (see docs/DESIGN-LAYER.md). Every violation is {rule, msg, file, line}.
import path from "node:path";
import { ASSET_EXTENSIONS, LIMITS } from "../../lib/design-schema.js";

// ───────────────────────────── JavaScript ─────────────────────────────

/** Identifiers that may not appear anywhere in design code, not even as a property name. */
export const BANNED_IDENTIFIERS = new Set([
  // network and messaging
  "fetch", "XMLHttpRequest", "WebSocket", "EventSource", "sendBeacon", "postMessage", "BroadcastChannel", "MessageChannel",
  "Worker", "SharedWorker", "ServiceWorker", "serviceWorker", "importScripts", "RTCPeerConnection", "WebTransport",
  // storage
  "localStorage", "sessionStorage", "indexedDB", "caches", "cookie", "cookieStore", "openDatabase",
  // dynamic code
  "eval", "Function", "AsyncFunction", "GeneratorFunction", "constructor", "__proto__", "__defineGetter__", "__defineSetter__",
  "__lookupGetter__", "__lookupSetter__", "Reflect", "Proxy", "WebAssembly", "Atomics",
  // ambient globals that reach everything else
  "window", "document", "globalThis", "navigator", "location",
  // raw HTML / element factories / attribute injection (the safe builder is ctx.ui.h)
  "innerHTML", "outerHTML", "insertAdjacentHTML", "insertAdjacentElement", "insertAdjacentText", "srcdoc", "createContextualFragment",
  "DOMParser", "XMLSerializer", "createElement", "createElementNS", "createDocumentFragment", "createRange", "setAttribute", "setAttributeNS", "setAttributeNode",
  "setHTMLUnsafe", "setHTML", "importNode", "adoptNode", "attachShadow", "execCommand", "write", "writeln",
  // things that send requests by being constructed
  "Image", "Audio", "Option", "Notification", "XSLTProcessor", "FileReader", "FormData", "RTCDataChannel",
]);
/** Bare references (not `.x` and not `x:`) that name a global window object. Renaming a local variable is the fix. */
export const BANNED_BARE = new Set(["self", "top", "parent", "frames", "opener"]);
/** String literals equal to one of these (whole value) are refused: they can only be used to reach the above by name. */
const BANNED_STRINGS = new Set([...BANNED_IDENTIFIERS, ...BANNED_BARE, "script", "iframe", "frame", "frameset", "embed", "object", "applet", "base", "meta", "portal", "action", "formaction", "formAction", "prototype", "defaultView", "ownerDocument", "contentWindow", "contentDocument", "parentNode", "parentElement"]);
const HTML_IN_STRING = /<\s*\/?\s*(script|iframe|frame|object|embed|link|meta|style|base|form|svg|math|template|noscript|portal)\b/i;
const SCHEME_IN_STRING = /(javascript|vbscript)\s*:|data\s*:\s*text\/html|:\/\//i;

const KEYWORDS_BEFORE_REGEX = new Set(["return", "typeof", "case", "in", "of", "delete", "void", "throw", "new", "else", "do", "yield", "await", "instanceof"]);
const PUNCT3 = ["...", "===", "!=="];
const PUNCT2 = ["?.", "=>", "==", "!=", "<=", ">=", "&&", "||", "??", "++", "--", "+=", "-=", "*=", "/=", "%=", "**", "<<", ">>"];

/** Minimal JS lexer: strings, templates (with nested expressions), comments, regex literals, numbers, identifiers, punctuators. */
export function lex(src) {
  const toks = [];
  let i = 0, line = 1;
  const n = src.length;
  const stack = []; // "{" | "tpl"
  const push = (t, v, extra = {}) => toks.push({ t, v, line, ...extra });
  const prevSig = () => toks[toks.length - 1];
  const idStart = /[\p{L}_$]/u, idPart = /[\p{L}\p{N}_$‌‍]/u;

  function readTemplate() { // after the opening backtick or a closing "}" of an expression
    let text = "";
    while (i < n) {
      const c = src[i];
      if (c === "\\") { text += src[i] + (src[i + 1] ?? ""); i += 2; continue; }
      if (c === "`") { i++; push("tpl", text, { end: true }); return; }
      if (c === "$" && src[i + 1] === "{") { i += 2; push("tpl", text, { end: false }); stack.push("tpl"); return; }
      if (c === "\n") line++;
      text += c; i++;
    }
    push("tpl", text, { end: true, unterminated: true });
  }

  while (i < n) {
    const c = src[i];
    if (c === "\n") { line++; i++; continue; }
    if (/\s/.test(c)) { i++; continue; }
    if (c === "/" && src[i + 1] === "/") { while (i < n && src[i] !== "\n") i++; continue; }
    if (c === "/" && src[i + 1] === "*") { const e = src.indexOf("*/", i + 2); const stop = e < 0 ? n : e + 2; for (let k = i; k < stop; k++) if (src[k] === "\n") line++; i = stop; continue; }
    if (c === '"' || c === "'") {
      let j = i + 1, val = "";
      while (j < n && src[j] !== c && src[j] !== "\n") {
        if (src[j] === "\\") {
          const d = src[j + 1];
          if (d === "u" && src[j + 2] === "{") { const e = src.indexOf("}", j); val += String.fromCodePoint(parseInt(src.slice(j + 3, e), 16) || 63); j = e + 1; continue; }
          if (d === "u") { val += String.fromCharCode(parseInt(src.slice(j + 2, j + 6), 16) || 63); j += 6; continue; }
          if (d === "x") { val += String.fromCharCode(parseInt(src.slice(j + 2, j + 4), 16) || 63); j += 4; continue; }
          val += { n: "\n", t: "\t", r: "\r", b: "\b", f: "\f", v: "\v", 0: "\0" }[d] ?? d; j += 2; continue;
        }
        val += src[j]; j++;
      }
      push("str", val, { raw: src.slice(i, j + 1) });
      i = j + 1; continue;
    }
    if (c === "`") { i++; readTemplate(); continue; }
    if (c === "{") { stack.push("{"); push("punct", "{"); i++; continue; }
    if (c === "}") {
      const top = stack.pop();
      if (top === "tpl") { i++; readTemplate(); continue; }
      push("punct", "}"); i++; continue;
    }
    if (/[0-9]/.test(c) || (c === "." && /[0-9]/.test(src[i + 1] || ""))) {
      let j = i + 1; while (j < n && /[0-9a-zA-Z_.]/.test(src[j])) j++;
      push("num", src.slice(i, j)); i = j; continue;
    }
    if (idStart.test(c) || c === "#") {
      let j = i + 1; while (j < n && idPart.test(src[j])) j++;
      push("id", src.slice(i, j)); i = j; continue;
    }
    if (c === "/") {
      const p = prevSig();
      const regexOk = !p || (p.t === "punct" && ![")", "]", "}"].includes(p.v)) || (p.t === "id" && KEYWORDS_BEFORE_REGEX.has(p.v));
      if (regexOk) {
        let j = i + 1, inClass = false;
        while (j < n && src[j] !== "\n") { if (src[j] === "\\") { j += 2; continue; } if (src[j] === "[") inClass = true; else if (src[j] === "]") inClass = false; else if (src[j] === "/" && !inClass) break; j++; }
        j++; while (j < n && /[a-z]/i.test(src[j])) j++;
        push("regex", src.slice(i, j)); i = j; continue;
      }
    }
    const three = src.slice(i, i + 3), two = src.slice(i, i + 2);
    const p = PUNCT3.includes(three) ? three : PUNCT2.includes(two) ? two : c;
    push("punct", p); i += p.length;
  }
  return toks;
}

const v = (rule, msg, file, line) => ({ rule, msg, file, line });

/** Data files (design/copy.js, design/routes.js) may contain only literals. */
export function scanDataJs(src, file) {
  const out = [];
  const toks = lex(src);
  const ALLOWED_PUNCT = new Set(["{", "}", "[", "]", ",", ":", ";", "-"]);
  let sawExport = false;
  toks.forEach((tk, k) => {
    if (tk.t === "str" || tk.t === "num") return;
    if (tk.t === "tpl") { if (!tk.end || tk.unterminated) out.push(v("js/data-not-pure", "template literals with ${} are not allowed in data files", file, tk.line)); return; }
    if (tk.t === "punct") { if (!ALLOWED_PUNCT.has(tk.v)) out.push(v("js/data-not-pure", `"${tk.v}" is not allowed in a data file (literals only: no calls, operators, spreads or functions)`, file, tk.line)); return; }
    if (tk.t === "id") {
      if (tk.v === "export" || tk.v === "default") { sawExport = sawExport || tk.v === "export"; return; }
      if (["true", "false", "null"].includes(tk.v)) return;
      const next = toks[k + 1];
      if (next && next.t === "punct" && next.v === ":") return; // unquoted key
      out.push(v("js/data-not-pure", `identifier "${tk.v}" is not allowed in a data file`, file, tk.line));
      return;
    }
    out.push(v("js/data-not-pure", `unexpected ${tk.t} token in a data file`, file, tk.line));
  });
  if (!sawExport) out.push(v("js/data-not-pure", 'a data file must `export default { ... }`', file, 1));
  return out;
}

const SIMPLE_INDEX = (inner) => {
  // allowed bracket contents: number | identifier | string literal | ident.ident | ident +/- number | ident.length
  if (inner.length === 1) return inner[0].t === "num" || inner[0].t === "id" || inner[0].t === "str";
  if (inner.length === 3) {
    const [a, op, b] = inner;
    if (a.t === "id" && op.t === "punct" && (op.v === "." || op.v === "?.") && b.t === "id") return true;
    if ((a.t === "id" || a.t === "num") && op.t === "punct" && (op.v === "+" || op.v === "-") && (b.t === "num" || b.t === "id")) return a.t !== "str" && b.t !== "str";
  }
  if (inner.length === 5) { // ident . ident . ident  (e.g. [p.kind.x]) is fine too
    return inner[0].t === "id" && inner[1].v === "." && inner[2].t === "id" && inner[3].v === "." && inner[4].t === "id";
  }
  return false;
};

/**
 * Code files (views, parts). `api` is lib/public-api.json (or null to skip the ctx-name check).
 * `designRelPath` is the path of this file relative to the repo root (e.g. "design/views/home.js").
 */
export function scanCode(src, file, { api = null, designRelPath = file } = {}) {
  const out = [];
  const toks = lex(src);
  const at = (k) => toks[k];
  const isP = (tk, val) => tk && tk.t === "punct" && tk.v === val;

  for (let k = 0; k < toks.length; k++) {
    const tk = toks[k], prev = at(k - 1), next = at(k + 1);
    if (tk.t === "str") {
      if (BANNED_STRINGS.has(tk.v.trim())) out.push(v("js/banned-string", `the string "${tk.v}" names a forbidden API`, file, tk.line));
      if (HTML_IN_STRING.test(tk.v) || SCHEME_IN_STRING.test(tk.v)) out.push(v("js/html-or-url-in-string", `a string contains markup, a script scheme or an external URL: ${JSON.stringify(tk.v.slice(0, 60))}`, file, tk.line));
      if (prev && prev.t === "punct" && prev.v === "+" && toks[k - 2] && toks[k - 2].t === "str") out.push(v("js/string-concat", "concatenating string literals is refused (write the string whole, or use ctx.t)", file, tk.line));
      if (/^on[a-z]+$/i.test(tk.v) && isP(next, ":") && at(k + 2) && (at(k + 2).t === "str" || at(k + 2).t === "tpl")) out.push(v("js/event-handler-string", `string event handler "${tk.v}"`, file, tk.line));
      continue;
    }
    if (tk.t === "tpl") {
      if (HTML_IN_STRING.test(tk.v) || SCHEME_IN_STRING.test(tk.v)) out.push(v("js/html-or-url-in-string", `a template contains markup, a script scheme or an external URL: ${JSON.stringify(tk.v.slice(0, 60))}`, file, tk.line));
      continue;
    }
    if (tk.t !== "id") continue;
    const name = tk.v;
    const afterDot = prev && (isP(prev, ".") || isP(prev, "?."));

    if (BANNED_IDENTIFIERS.has(name)) { out.push(v("js/forbidden-identifier", `"${name}" is not allowed in design code`, file, tk.line)); continue; }
    if (BANNED_BARE.has(name) && !afterDot && !isP(next, ":")) { out.push(v("js/forbidden-identifier", `bare "${name}" refers to a window object (rename the variable)`, file, tk.line)); continue; }

    if (name === "import") {
      if (isP(next, "(")) { out.push(v("js/dynamic-import", "dynamic import() is not allowed", file, tk.line)); continue; }
      if (isP(next, ".")) { out.push(v("js/forbidden-identifier", "import.meta is not allowed", file, tk.line)); continue; }
      checkSpecifier(k, true);
      continue;
    }
    if (name === "export") {
      // "export ... from 'x'" re-exports; plain exports are fine
      for (let j = k + 1; j < toks.length && j < k + 40; j++) {
        if (isP(toks[j], ";") || (toks[j].t === "id" && ["function", "const", "let", "var", "class", "default", "async"].includes(toks[j].v))) break;
        if (toks[j].t === "id" && toks[j].v === "from") { checkSpecifier(j - 1, false, j); break; }
      }
      continue;
    }

    // setTimeout("code") / setInterval("code")
    if ((name === "setTimeout" || name === "setInterval") && isP(next, "(") && at(k + 2) && (at(k + 2).t === "str" || at(k + 2).t === "tpl")) {
      out.push(v("js/timer-string", `${name} with a string argument is code injection`, file, tk.line));
    }
    // form submission targets are not the design layer's business
    if ((name === "action" || name === "formAction" || name === "formaction") && isP(next, ":")) out.push(v("js/form-action", `"${name}" attribute is not allowed (screens talk to the core, not to URLs)`, file, tk.line));
    // x.onfoo = ...   /   { onfoo: "string" }
    if (/^on[a-z]+$/.test(name)) {
      if (afterDot && isP(next, "=")) out.push(v("js/event-handler-assignment", `assigning .${name} is refused (use h({ ${name}: fn }) or addEventListener)`, file, tk.line));
      if (!afterDot && isP(next, ":") && at(k + 2) && (at(k + 2).t === "str" || at(k + 2).t === "tpl")) out.push(v("js/event-handler-string", `string event handler "${name}"`, file, tk.line));
    }
    // computed member access
    // handled below via brackets
  }

  // computed member access: x[ ... ]
  for (let k = 1; k < toks.length; k++) {
    if (!isP(toks[k], "[")) continue;
    const p = toks[k - 1];
    const isMember = (p.t === "id" && !KEYWORDS_BEFORE_REGEX.has(p.v) && !["return", "const", "let", "var", "export", "default", "in", "of"].includes(p.v)) || isP(p, ")") || isP(p, "]") || p.t === "str" || p.t === "tpl";
    if (!isMember) continue; // array literal
    let depth = 1, j = k + 1;
    while (j < toks.length && depth) { if (isP(toks[j], "[")) depth++; else if (isP(toks[j], "]")) depth--; j++; }
    const inner = toks.slice(k + 1, j - 1);
    if (!inner.length || !SIMPLE_INDEX(inner)) out.push(v("js/computed-access", "computed member access with a non-trivial expression is refused (use a plain index, identifier or string literal)", file, toks[k].line));
  }

  // imports must stay inside design/
  function checkSpecifier(k, isImport, fromIdx) {
    let spec = null;
    if (isImport) {
      for (let j = k + 1; j < toks.length && j < k + 80; j++) {
        if (toks[j].t === "str" && (j === k + 1 || (toks[j - 1].t === "id" && toks[j - 1].v === "from"))) { spec = toks[j]; break; }
        if (isP(toks[j], ";")) break;
      }
    } else spec = toks[fromIdx + 1] && toks[fromIdx + 1].t === "str" ? toks[fromIdx + 1] : null;
    if (!spec) { out.push(v("js/import", "could not read the import specifier", file, toks[k].line)); return; }
    const s = spec.v;
    if (!/^\.{1,2}\/[A-Za-z0-9_\/.-]+\.js$/.test(s)) { out.push(v("js/import", `import "${s}" is not allowed: design code may only import other files under design/ by relative path (everything else comes from ctx)`, file, spec.line)); return; }
    const resolved = path.posix.normalize(path.posix.join(path.posix.dirname(designRelPath), s));
    const root = designRelPath.split("/")[0];
    if (!resolved.startsWith(root + "/") || resolved.includes("..")) out.push(v("js/import", `import "${s}" leaves ${root}/`, file, spec.line));
  }

  if (api) checkCtx(toks, api, file, out);
  return out;
}

function checkCtx(toks, api, file, out) {
  const isP = (tk, val) => tk && tk.t === "punct" && tk.v === val;
  const known = (nsPath) => api[nsPath];
  const aliases = new Map(); // alias name -> namespace path ("ctx.ui")

  // chain: starting at index k (the identifier), return {segments, end}
  const readChain = (k) => {
    const segs = [];
    let j = k + 1;
    while (j < toks.length && (isP(toks[j], ".") || isP(toks[j], "?.")) && toks[j + 1] && toks[j + 1].t === "id") { segs.push({ name: toks[j + 1].v, line: toks[j + 1].line }); j += 2; }
    return { segs, end: j };
  };
  const checkSegs = (base, segs, line) => {
    let ns = base;
    for (const s of segs) {
      const list = known(ns);
      if (!list) return; // plain data below this point (e.g. ctx.route.params.x)
      if (!list.includes(s.name)) { out.push(v("js/ctx-unknown", `${ns}.${s.name} is not in lib/public-api.json`, file, s.line || line)); return; }
      ns = `${ns}.${s.name}`;
    }
  };

  // pass 1: destructuring  const { a, b: c } = ctx[.ns]
  for (let k = 0; k < toks.length; k++) {
    if (!(toks[k].t === "id" && toks[k].v === "ctx") || !isP(toks[k - 1], "=")) continue;
    if (!isP(toks[k - 2], "}")) continue;
    let j = k - 2, depth = 0;
    for (; j >= 0; j--) { if (isP(toks[j], "}")) depth++; else if (isP(toks[j], "{")) { depth--; if (depth === 0) break; } }
    const pattern = toks.slice(j + 1, k - 2);
    const { segs } = readChain(k);
    const base = ["ctx", ...segs.map((s) => s.name)].join(".");
    if (segs.length && !known("ctx." + segs.map((s) => s.name).join("."))) { checkSegs("ctx", segs, toks[k].line); }
    if (pattern.some((p) => isP(p, "{") || isP(p, "["))) { out.push(v("js/ctx-unknown", "nested destructuring of ctx is refused (destructure one level)", file, toks[k].line)); continue; }
    let idx = 0;
    while (idx < pattern.length) {
      const key = pattern[idx];
      if (key.t === "id") {
        const list = known(base);
        if (!list) {
          // plain data below a known namespace (ctx.route.params / query / state): any keys are fine
          const parentNs = base.slice(0, base.lastIndexOf("."));
          if (known(parentNs) && known(parentNs).includes(base.slice(base.lastIndexOf(".") + 1))) break;
          out.push(v("js/ctx-unknown", `cannot destructure from ${base}`, file, key.line)); break;
        }
        if (!list.includes(key.v)) out.push(v("js/ctx-unknown", `${base}.${key.v} is not in lib/public-api.json`, file, key.line));
        let alias = key.v;
        if (isP(pattern[idx + 1], ":") && pattern[idx + 2] && pattern[idx + 2].t === "id") { alias = pattern[idx + 2].v; idx += 2; }
        if (known(`${base}.${key.v}`)) aliases.set(alias, `${base}.${key.v}`);
      }
      while (idx < pattern.length && !isP(pattern[idx], ",")) idx++;
      idx++;
    }
  }

  // pass 2: every chain that starts with ctx or an alias
  for (let k = 0; k < toks.length; k++) {
    const tk = toks[k];
    if (tk.t !== "id") continue;
    if (isP(toks[k - 1], ".") || isP(toks[k - 1], "?.")) continue;
    if (tk.v === "ctx") {
      if (isP(toks[k + 1], "[")) { out.push(v("js/ctx-unknown", "computed access on ctx is refused", file, tk.line)); continue; }
      const { segs } = readChain(k);
      checkSegs("ctx", segs, tk.line);
    } else if (aliases.has(tk.v) && !isP(toks[k + 1], ":")) {
      if (isP(toks[k + 1], "[")) { out.push(v("js/ctx-unknown", `computed access on ${tk.v} is refused`, file, tk.line)); continue; }
      const { segs } = readChain(k);
      checkSegs(aliases.get(tk.v), segs, tk.line);
    }
  }
}

// ───────────────────────────── CSS ─────────────────────────────

const stripCssComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, " "));
const stripCssStrings = (s) => s.replace(/"(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*'/g, (m) => '"' + " ".repeat(Math.max(0, m.length - 2)) + '"');
const HIDING = /display\s*:\s*none|visibility\s*:\s*hidden|opacity\s*:\s*0(?![.\d])|pointer-events\s*:\s*none|(?:^|[;{\s])(?:width|height|max-width|max-height|font-size)\s*:\s*0(?:px|rem|em|%)?\s*(?:[;}!]|$)|clip-path\s*:\s*(?:inset|circle|polygon)|transform\s*:[^;}]*scale\(\s*0/i;

/** cssRel: the file's path relative to the repo root, e.g. "design/theme.css". */
export function scanCss(src, file, cssRel = file) {
  const out = [];
  const noComments = stripCssComments(src);
  const lineOf = (idx) => noComments.slice(0, idx).split("\n").length;
  if (/@import/i.test(noComments)) out.push(v("css/import", "@import is not allowed (put the rules in the file)", file, lineOf(noComments.search(/@import/i))));
  for (const m of noComments.matchAll(/url\s*\(\s*(["']?)([^)]*?)\1\s*\)/gi)) {
    const target = m[2].trim();
    const ok = /^data:image\/(png|jpe?g|webp|gif|avif|svg\+xml)[;,]/i.test(target) ||
      (!/^[a-z][a-z0-9+.-]*:/i.test(target) && !target.startsWith("//") && !target.startsWith("\\") &&
        path.posix.normalize(path.posix.join(path.posix.dirname(cssRel), target)).startsWith(cssRel.split("/")[0] + "/assets/"));
    if (!ok) out.push(v("css/url", `url(${target.slice(0, 60)}) must be a file under design/assets/ (or a data:image URI)`, file, lineOf(m.index)));
  }
  if (/image-set\s*\(\s*["']/i.test(noComments) || /\bsrc\s*\(/i.test(noComments)) out.push(v("css/url", "image-set(\"...\") / src() string URLs are refused; use url(assets/...)", file, 1));
  if (/expression\s*\(/i.test(noComments)) out.push(v("css/expression", "expression() is not allowed", file, lineOf(noComments.search(/expression\s*\(/i))));
  if (/behavior\s*:|-moz-binding|javascript\s*:|vbscript\s*:/i.test(noComments)) out.push(v("css/behavior", "behaviour hacks (behavior:, -moz-binding, javascript:) are not allowed", file, 1));
  if (/\\[0-9a-fA-F]{1,6}/.test(stripCssStrings(noComments))) out.push(v("css/escape", "CSS escapes outside strings can hide forbidden tokens", file, 1));
  if (/<\s*\/?\s*(script|style|iframe)/i.test(noComments)) out.push(v("css/markup", "markup inside CSS", file, 1));
  // rules that make confirmation dialogs invisible or unclickable
  const flat = noComments;
  for (const m of flat.matchAll(/([^{}@]+)\{([^{}]*)\}/g)) {
    const sel = m[1], decl = m[2];
    if (/dialog|\.dlg|confirm|::backdrop|data-testid\s*[~|^$*]?=\s*["']?confirm/i.test(sel) && HIDING.test(decl)) {
      out.push(v("css/hides-dialog", `a rule on "${sel.trim().slice(0, 50)}" hides or disables a dialog / confirmation control`, file, lineOf(m.index)));
    }
  }
  return out;
}

// ───────────────────────────── Assets ─────────────────────────────

const SIGNATURES = {
  png: (b) => b.length > 8 && b[0] === 0x89 && b.slice(1, 4).toString() === "PNG",
  jpg: (b) => b.length > 3 && b[0] === 0xff && b[1] === 0xd8,
  jpeg: (b) => b.length > 3 && b[0] === 0xff && b[1] === 0xd8,
  gif: (b) => b.slice(0, 3).toString() === "GIF",
  webp: (b) => b.length > 12 && b.slice(0, 4).toString() === "RIFF" && b.slice(8, 12).toString() === "WEBP",
  woff2: (b) => b.slice(0, 4).toString() === "wOF2",
  ico: (b) => b.length > 4 && b[0] === 0 && b[1] === 0 && b[2] === 1 && b[3] === 0,
  avif: (b) => b.length > 12 && b.slice(4, 8).toString() === "ftyp",
};

export function scanAsset(buf, file) {
  const out = [];
  const ext = path.extname(file).slice(1).toLowerCase();
  if (!ASSET_EXTENSIONS.includes(ext)) { out.push(v("asset/type", `.${ext} files are not allowed in design/assets/ (allowed: ${ASSET_EXTENSIONS.join(", ")})`, file, 1)); return out; }
  if (buf.length > LIMITS.assetMaxBytes) out.push(v("asset/size", `${(buf.length / 1024).toFixed(0)} KiB exceeds the ${LIMITS.assetMaxBytes / 1024} KiB cap`, file, 1));
  if (ext === "svg") {
    const text = buf.toString("utf8");
    if (/<\s*(script|foreignObject|iframe|object|embed|animate|set|use|image|a)\b/i.test(text) || /\son[a-z]+\s*=/i.test(text) || /javascript\s*:/i.test(text) ||
      /(?:xlink:)?href\s*=\s*["'](?!#)/i.test(text) || /<!ENTITY|<!DOCTYPE/i.test(text) || /url\s*\(\s*["']?(?!#)/i.test(text) || /@import/i.test(text)) {
      out.push(v("asset/svg-active", "SVG must be passive: no script, foreignObject, event handlers, external references, entities or url()/@import", file, 1));
    }
  } else if (SIGNATURES[ext] && !SIGNATURES[ext](buf)) out.push(v("asset/magic", `the content is not a real .${ext} file`, file, 1));
  return out;
}

// ───────────────────────────── Scope ─────────────────────────────

/** Fails every path outside design/**. `paths` use forward slashes relative to the repo root. */
export function scopeViolations(paths) {
  const out = [];
  for (const raw of paths) {
    const p = String(raw).replace(/\\/g, "/").replace(/^\.\//, "");
    const norm = path.posix.normalize(p);
    if (!norm.startsWith("design/") || norm.includes("..") || path.posix.isAbsolute(norm)) out.push(v("scope/outside-design", `${p} is outside design/ (only design/** may change)`, p, 0));
  }
  return out;
}
