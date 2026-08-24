/**
 * outfix — clean malformed/polluted LLM output.
 * Port of github.com/Maybeyes111/outfix (Go).
 * Heuristics-based; see repo README for status & limitations.
 */

export const ACTION = {
  STRIPPED_THINK_BLOCK: "stripped_think_block",
  STRIPPED_ORPHAN_CLOSE_TAG: "stripped_orphan_close_tag",
  UNWRAPPED_TOOL_CALL: "unwrapped_tool_call",
  STRIPPED_CHAT_TEMPLATE: "stripped_chat_template",
  STRIPPED_CODE_FENCE: "stripped_code_fence",
  STRIPPED_BOX_DRAWING: "stripped_box_drawing",
  REMOVED_PREAMBLE: "removed_preamble",
  REMOVED_POSTAMBLE: "removed_postamble",
  STRIPPED_XML_BLEED: "stripped_xml_bleed",
  FIXED_SINGLE_QUOTES: "fixed_single_quotes",
  FIXED_PYTHON_LITERALS: "fixed_python_literals",
  FIXED_TRAILING_COMMA: "fixed_trailing_comma",
  QUOTED_BARE_KEYS: "quoted_bare_keys",
  MERGED_NDJSON: "merged_ndjson",
  REPAIRED_TRUNCATED_JSON: "repaired_truncated_json",
  CONVERTED_FUNCTION_CALL: "converted_function_call",
  QUOTED_BARE_VALUES: "quoted_bare_values",
  UNWRAPPED_STRINGIFIED: "unwrapped_stringified_json",
  FIXED_XML_ORPHAN_CLOSE: "fixed_xml_orphan_close",
  NORMALIZED_UNICODE_ESC: "normalized_unicode_escapes",
  NORMALIZED_LINE_ENDINGS: "normalized_line_endings",
  COLLAPSED_WHITESPACE: "collapsed_whitespace",
};

export const Format = { AUTO: 0, JSON: 1, XML: 2, PLAIN_TEXT: 3 };
export const ModelFamily = { GENERIC: 0, QWEN: 1, DEEPSEEK: 2, GLM: 3 };

const THINK_RE = /<\s*\/?\s*(thinking|reflection|reasoning|think)\s*>/gi;
const TOOLWRAP_RE = /<\s*\/?\s*(tool_call|tool_calls|function_call|function_calls)\s*>/gi;
const CHATTEMPLATE_RE = /<\|[a-zA-Z_]+\|>/g;
const FUNCCALL_FULL_RE = /^\s*[A-Za-z_]\w*(?:\.\w+)*\s*\((?:[\S\s]*)\)\s*$/;
const FUNCCALL_ARGS_RE = /[A-Za-z_]\w*(?:\.\w+)*\s*\(/;
const WITH_LINES_RE = /^[A-Za-z_]\w*[ \t]+with[ \t]+[A-Za-z_][\w-]*[ \t]*=/m;
const FUNCCALL_DENYLIST = new Set(["def", "print", "len", "range", "int", "str",
  "float", "bool", "list", "dict", "set", "tuple", "open", "input", "type",
  "super", "isinstance", "getattr", "setattr", "if", "for", "while", "return",
  "lambda", "func"]);
const STRINGIFIED_RE = /"(\{(?:[^"\\]|\\.)*\})"/g;
const BOX_RUNES = new Set("╭╮╰╯│┃┌┐└┘├┤┬┴┼─━═·");
const ESC_TARGETS = [["\\u003c", "<"], ["\\u003C", "<"], ["\\u003e", ">"], ["\\u003E", ">"], ["\\u0026", "&"]];
const BLEED_TAGS = new Set(["content", "system", "prompt", "response", "assistant",
  "user", "message", "answer", "output", "result",
  "tool_response", "assistant_response", "tool_call", "function_result"]);

export class RepairAction {
  constructor(type, description, position) {
    this.type = type;
    this.description = description;
    this.position = position;
  }
}

export class Options {
  constructor(o = {}) {
    this.targetFormat = o.targetFormat ?? Format.AUTO;
    this.stripReasoning = o.stripReasoning ?? true;
    this.repairJson = o.repairJson ?? true;
    this.modelHint = o.modelHint ?? ModelFamily.GENERIC;
    this.maxRepairDepth = o.maxRepairDepth ?? 2;
  }
}

export class Result {
  constructor(o = {}) {
    this.output = o.output ?? "";
    this.cleaned = o.cleaned ?? false;
    this.repairs = o.repairs ?? [];
    this.confidence = o.confidence ?? 0;
    this.modelGuess = o.modelGuess ?? ModelFamily.GENERIC;
    this.error = o.error ?? null;
  }
}

const act = (acts, type, desc, pos) =>
  acts.push(new RepairAction(type, desc, Math.max(pos, 0)));

const validJson = (s) => {
  if (s !== s.trim()) return false;
  try { JSON.parse(s); return true; } catch { return false; }
};

function stringMask(s) {
  const mask = new Array(s.length).fill(false);
  let inside = false, esc = false;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (inside) {
      mask[i] = true;
      if (esc) esc = false;
      else if (c === "\\") esc = true;
      else if (c === '"') inside = false;
    } else if (c === '"') {
      inside = true;
      mask[i] = true;
    }
  }
  return mask;
}

const isIdentByte = (c) => /[A-Za-z0-9_-]/.test(c);

function nextNonSpace(s, i) {
  while (i < s.length && " \t\r\n".includes(s[i])) i++;
  return i < s.length ? i : -1;
}

function xmlTagIndex(s) {
  for (let i = 0; i < s.length; i++) {
    if (s[i] === "<" && i + 1 < s.length) {
      const n = s[i + 1];
      if (/[A-Za-z]/.test(n) || "/?!".includes(n)) return i;
    }
  }
  return -1;
}

function firstStructuralIndex(s) {
  const j = Math.min(...["{", "["].map((ch) => {
    const k = s.indexOf(ch);
    return k < 0 ? Infinity : k;
  }));
  const jj = j === Infinity ? -1 : j;
  const x = xmlTagIndex(s);
  if (jj < 0) return x;
  if (x < 0) return jj;
  return Math.min(jj, x);
}

function jsonRootSpan(s) {
  let start = -1;
  for (const ch of ["{", "["]) {
    const k = s.indexOf(ch);
    if (k >= 0 && (start < 0 || k < start)) start = k;
  }
  if (start < 0) return null;
  const mask = stringMask(s);
  const openOf = { "}": "{", "]": "[" };
  const stack = [];
  for (let i = start; i < s.length; i++) {
    if (mask[i]) continue;
    const c = s[i];
    if (c === "{" || c === "[") stack.push(c);
    else if (c === "}" || c === "]") {
      if (!stack.length || stack[stack.length - 1] !== openOf[c]) return null;
      stack.pop();
      if (!stack.length) return [start, i + 1];
    }
  }
  return null;
}

const hasLetter = (s) => /\p{L}/u.test(s);

function removeSpans(s, spans) {
  spans = [...spans].sort((a, b) => a[0] - b[0]);
  let out = "", prev = 0;
  for (const [a, b] of spans) {
    if (a > prev) out += s.slice(prev, a);
    if (b > prev) prev = b;
  }
  return out + s.slice(prev);
}

function stripReasoning(s, acts) {
  const matches = [...s.matchAll(THINK_RE)];
  if (!matches.length) return s;
  const stack = [], spans = [];
  let paired = 0, orphan = 0, firstOrphan = -1;
  for (const m of matches) {
    const name = m[1].toLowerCase();
    if (!m[0].trimStart().startsWith("</")) {
      stack.push([m.index, name]);
      continue;
    }
    let found = -1;
    for (let i = stack.length - 1; i >= 0; i--) {
      if (stack[i][1] === name) { found = i; break; }
    }
    if (found >= 0) {
      spans.push([stack[found][0], m.index + m[0].length]);
      stack.length = found;
      paired++;
    } else {
      orphan++;
      spans.push([m.index, m.index + m[0].length]);
      if (firstOrphan < 0) firstOrphan = m.index;
    }
  }
  if (stack.length) {
    const tagEndOfLast = (() => {
      const close = s.indexOf(">", stack[stack.length - 1][0]);
      return close < 0 ? s.length : close + 1;
    })();
    const fs = firstStructuralIndex(s.slice(tagEndOfLast));
    if (fs >= 0) spans.push([stack[0][0], tagEndOfLast + fs]);
    else spans.push([stack[0][0], s.length]);
  }
  if (!spans.length) return s;
  const out = removeSpans(s, spans);
  act(acts, ACTION.STRIPPED_THINK_BLOCK,
    `removed ${paired} paired reasoning block(s)`, spans[0][0]);
  if (orphan) act(acts, ACTION.STRIPPED_ORPHAN_CLOSE_TAG,
    `removed ${orphan} orphan close tag(s)`, firstOrphan);
  return out;
}

function stripToolWrappers(s, acts) {
  let ms = [...s.matchAll(TOOLWRAP_RE)];
  if (ms.length) {
    s = removeSpans(s, ms.map((m) => [m.index, m.index + m[0].length]));
    act(acts, ACTION.UNWRAPPED_TOOL_CALL,
      `unwrapped ${ms.length} tool-call tag(s)`, ms[0].index);
  }
  ms = [...s.matchAll(CHATTEMPLATE_RE)];
  if (ms.length) {
    s = removeSpans(s, ms.map((m) => [m.index, m.index + m[0].length]));
    act(acts, ACTION.STRIPPED_CHAT_TEMPLATE,
      `removed ${ms.length} chat-template token(s)`, ms[0].index);
  }
  return s;
}

function parseFenceLine(tok) {
  if (!tok || !"`~".includes(tok[0])) return null;
  let n = 0;
  while (n < tok.length && tok[n] === tok[0]) n++;
  if (n < 3) return null;
  const rest = tok.slice(n);
  if (rest === "") return [tok[0], ""];
  if (/^[A-Za-z0-9_+\-.]+$/.test(rest)) return [tok[0], rest.toLowerCase()];
  return null;
}

function stripFences(s, acts) {
  const lines = s.split("\n");
  const regions = [];
  let offset = 0, i = 0;
  while (i < lines.length) {
    const fence = parseFenceLine(lines[i].trim());
    if (!fence) { offset += lines[i].length + 1; i++; continue; }
    const bodyLines = [];
    let j = i + 1, closed = false;
    while (j < lines.length) {
      const inner = parseFenceLine(lines[j].trim());
      if (inner && inner[0] === fence[0] && inner[1] === "") { closed = true; break; }
      bodyLines.push(lines[j]);
      j++;
    }
    let closeEnd;
    if (closed) {
      closeEnd = offset + lines.slice(i, j + 1).reduce((a, l) => a + l.length + 1, 0);
      regions.push({ opener: offset, closeEnd, body: bodyLines.join("\n"), lang: fence[1] });
      offset += lines.slice(i, j + 1).reduce((a, l) => a + l.length + 1, 0);
      i = j + 1;
    } else {
      closeEnd = s.length;
      regions.push({ opener: offset, closeEnd, body: bodyLines.join("\n"), lang: fence[1] });
      offset = s.length + 1;
      i = lines.length;
    }
  }
  if (regions.length) {
    let chosen = regions.find((r) => {
      const t = r.body.trim();
      return t && (t[0] === "{" || t[0] === "[" ||
        (t[0] === "<" && t.length > 1 && /[A-Za-z]/.test(t[1])));
    }) || regions.reduce((a, b) => (b.body.length > a.body.length ? b : a));
    s = s.slice(0, chosen.opener) + chosen.body.replace(/[\r\n]+$/, "") +
      s.slice(chosen.closeEnd);
    act(acts, ACTION.STRIPPED_CODE_FENCE,
      chosen.lang ? `stripped ${chosen.lang} code fence` : "stripped markdown code fence",
      chosen.opener);
  }
  const t = s.replace(/[ \t\r\n]+$/, "");
  if (t.endsWith("```") || t.endsWith("~~~")) {
    const li = t.lastIndexOf("\n");
    const candidate = li >= 0 ? t.slice(li + 1) : t;
    const pos = li >= 0 ? li + 1 : 0;
    const tok = candidate.trim();
    if (tok.length >= 3 && "`~".includes(tok[0]) &&
        [...tok].every((c) => c === tok[0])) {
      s = s.slice(0, pos);
      act(acts, ACTION.STRIPPED_CODE_FENCE, "removed stray trailing code fence", pos);
    }
  }
  return s;
}

function stripBoxLines(s, acts) {
  if (![...s].some((c) => BOX_RUNES.has(c))) return s;
  const lines = s.split("\n");
  const kept = [];
  let dropped = 0, firstPos = -1, offset = 0;
  for (const ln of lines) {
    const body = [...ln.replace(/\r$/, "")].filter((c) => c !== " " && c !== "\t");
    if (body.length && body.every((c) => BOX_RUNES.has(c))) {
      dropped++;
      if (firstPos < 0) firstPos = offset;
    } else kept.push(ln);
    offset += ln.length + 1;
  }
  if (!dropped || !kept.length) return s;
  act(acts, ACTION.STRIPPED_BOX_DRAWING,
    `removed ${dropped} box-drawing line(s)`, firstPos);
  return kept.join("\n");
}

function stripPreamble(s, acts) {
  const idx = firstStructuralIndex(s);
  if (idx <= 0 || !hasLetter(s.slice(0, idx))) return s;
  act(acts, ACTION.REMOVED_PREAMBLE,
    `removed preamble before payload (${idx} chars)`, 0);
  return s.slice(idx);
}

function closeTagName(s) {
  const idx = s.indexOf("</");
  if (idx < 0) return "";
  const m = s.slice(idx + 2).match(/^([A-Za-z][\w.\-:]*)/);
  return m ? m[1] : "";
}

function stripTail(s, acts) {
  const span = jsonRootSpan(s);
  if (span) {
    const [start, end] = span;
    const tt = s.slice(end).trim();
    if (!tt) return s;
    if (tt[0] === "{" || tt[0] === "[") return s;
    if (tt[0] === "\\" && tt.length > 2 && "ntr".includes(tt[1]) &&
        /[{[]/.test(tt.slice(2))) return s;
    if (tt[0] === "<") {
      const name = closeTagName(tt);
      if (name && !s.slice(start, end).includes("<" + name)) {
        act(acts, ACTION.STRIPPED_XML_BLEED,
          `stripped trailing XML bleed </${name}>`, end);
        return s.slice(0, end);
      }
      return s;
    }
    act(acts, ACTION.REMOVED_POSTAMBLE,
      `removed postamble after JSON payload (${s.slice(end).length} chars)`, end);
    return s.slice(0, end);
  }
  return s;
}

function fixPythonLiterals(s) {
  const mask = stringMask(s);
  const repl = { True: "true", False: "false", None: "null" };
  let out = "", first = -1, i = 0;
  while (i < s.length) {
    if (mask[i] || !/[A-Za-z]/.test(s[i])) { out += s[i]; i++; continue; }
    const matched = ["True", "False", "None"].find((w) => s.startsWith(w, i));
    if (!matched) { out += s[i]; i++; continue; }
    const end = i + matched.length;
    const prevOk = i === 0 || !isIdentByte(s[i - 1]);
    const nextOk = end >= s.length || !isIdentByte(s[end]);
    if (prevOk && nextOk) {
      if (first < 0) first = i;
      out += repl[matched];
      i = end;
      continue;
    }
    out += s[i];
    i++;
  }
  return first < 0 ? [s, -1, false] : [out, first, true];
}

function fixTrailingCommas(s) {
  const mask = stringMask(s);
  let out = "", first = -1;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (c === "," && !mask[i]) {
      const j = nextNonSpace(s, i + 1);
      if (j >= 0 && (s[j] === "}" || s[j] === "]")) {
        if (first < 0) first = i;
        continue;
      }
    }
    out += c;
  }
  return first < 0 ? [s, -1, false] : [out, first, true];
}

function findSingleQuoteClose(s, from) {
  for (let k = from; k < s.length; k++) {
    const c = s[k];
    if (c === "\n" || c === '"') return -1;
    if (c === "\\") { k++; continue; }
    if (c === "'") return k - from > 4096 ? -1 : k;
  }
  return -1;
}

function fixSingleQuotes(s) {
  const mask = stringMask(s);
  let out = "", first = -1, i = 0;
  while (i < s.length) {
    const c = s[i];
    if (c !== "'" || mask[i]) { out += c; i++; continue; }
    let p = i;
    while (p > 0 && " \t\n\r".includes(s[p - 1])) p--;
    const openOk = p > 0 && "{[:,".includes(s[p - 1]);
    if (!openOk) { out += c; i++; continue; }
    const closeIdx = findSingleQuoteClose(s, i + 1);
    if (closeIdx < 0) { out += c; i++; continue; }
    const j = nextNonSpace(s, closeIdx + 1);
    const closeOk = j >= 0 && ":,}]".includes(s[j]);
    if (!closeOk) { out += c; i++; continue; }
    if (first < 0) first = i;
    out += '"';
    for (let k = i + 1; k < closeIdx; k++) {
      if (s[k] === "\\" && k + 1 < closeIdx && s[k + 1] === "'") {
        out += "'";
        k++;
        continue;
      }
      out += s[k];
    }
    out += '"';
    i = closeIdx + 1;
  }
  return first < 0 ? [s, -1, false] : [out, first, true];
}

function quoteBareKeys(s) {
  const mask = stringMask(s);
  let out = "", first = -1, i = 0;
  while (i < s.length) {
    const c = s[i];
    if (mask[i] || (c !== "{" && c !== ",")) { out += c; i++; continue; }
    const j = nextNonSpace(s, i + 1);
    if (j < 0 || (!/[A-Za-z]/.test(s[j]) && s[j] !== "_")) { out += c; i++; continue; }
    let k = j;
    while (k < s.length && isIdentByte(s[k])) k++;
    const m = nextNonSpace(s, k);
    if (m < 0 || s[m] !== ":") { out += c; i++; continue; }
    if (first < 0) first = j;
    out += s.slice(i + 1, j) + '"' + s.slice(j, k) + '"';
    i = k;
  }
  return first < 0 ? [s, -1, false] : [out, first, true];
}

function matchBracketFrom(s, mask, start) {
  const open = s[start];
  const clo = { "{": "}", "[": "]" }[open];
  if (!clo) return -1;
  let depth = 0;
  for (let i = start; i < s.length; i++) {
    if (mask[i]) continue;
    if (s[i] === open) depth++;
    else if (s[i] === clo) {
      depth--;
      if (depth === 0) return i;
    }
  }
  return -1;
}

function mergeNDJSON(s) {
  const mask = stringMask(s);
  const vals = [];
  let i = 0;
  while (i < s.length) {
    const c = s[i];
    if (mask[i]) { i++; continue; }
    if (c === "{" || c === "[") {
      const end = matchBracketFrom(s, mask, i);
      if (end < 0) return [s, false];
      vals.push(s.slice(i, end + 1).trim());
      i = end + 1;
      continue;
    }
    if (" \t\r\n,;".includes(c) ||
        (c === "\\" && i + 1 < s.length && "ntr".includes(s[i + 1]))) {
      i += c === "\\" ? 2 : 1;
      continue;
    }
    return [s, false];
  }
  if (vals.length < 2 || !vals.every(validJson)) return [s, false];
  const merged = "[" + vals.join(",") + "]";
  return validJson(merged) ? [merged, true] : [s, false];
}

function completePartialLiteral(t) {
  let k = t.length;
  while (k > 0 && /[A-Za-z]/.test(t[k - 1])) k--;
  const run = t.slice(k);
  const table = { t: "true", tr: "true", tru: "true",
    f: "false", fa: "false", fal: "false", fals: "false",
    n: "null", nu: "null", nul: "null" };
  if (table[run] && table[run].length > run.length) return [table[run], k];
  return [null, -1];
}

function balanceTruncatedJSON(s, depth) {
  const mask = stringMask(s);
  const stack = [];
  for (let i = 0; i < s.length; i++) {
    if (mask[i]) continue;
    const c = s[i];
    if (c === "{" || c === "[") stack.push(c);
    else if (c === "}" || c === "]") {
      if (!stack.length) return [s, -1, false];
      const wantOpen = c === "}" ? "{" : "[";
      if (stack[stack.length - 1] !== wantOpen) return [s, -1, false];
      stack.pop();
    }
  }
  const inStr = mask.length > 0 && mask[mask.length - 1];
  let trailing = s.length;
  while (trailing > 0 && " \t\r\n".includes(s[trailing - 1])) trailing--;
  const origT = s.slice(0, trailing);
  let t = origT;
  if (inStr) t += '"';
  if (t.endsWith(":") && depth >= 2) t += "null";
  if (depth >= 3) {
    const [comp, k] = completePartialLiteral(t);
    if (comp) t = t.slice(0, k) + comp;
  }
  if (t.endsWith(",")) t = t.slice(0, -1);
  if (!stack.length) {
    if (t !== origT && validJson(t)) return [t, Math.max(t.length - 1, 0), true];
    return [s, -1, false];
  }
  const closers = [...stack].reverse().map((c) => (c === "{" ? "}" : "]")).join("");
  const cand = t + closers;
  return validJson(cand) ? [cand, t.length, true] : [s, -1, false];
}

function topEqIndex(seg) {
  let depth = 0, inDQ = false, inSQ = false;
  for (let i = 0; i < seg.length; i++) {
    const c = seg[i];
    if (inDQ) {
      if (c === "\\") i++;
      else if (c === '"') inDQ = false;
    } else if (inSQ) {
      if (c === "\\") i++;
      else if (c === "'") inSQ = false;
    } else if (c === '"') inDQ = true;
    else if (c === "'") inSQ = true;
    else if (c === "(" || c === "[") depth++;
    else if (c === ")" || c === "]") depth--;
    else if (c === "=" && depth === 0) {
      if (seg[i + 1] === "=") return -1;
      return i;
    }
  }
  return -1;
}

const jstr = (s) => JSON.stringify(s);

const validIdent = (k) =>
  k.length > 0 && !/[0-9]/.test(k[0]) && /^[A-Za-z0-9_\-]+$/.test(k);

function convertArgValue(val) {
  if (!val) return null;
  if (val[0] === '"') {
    try { return jstr(JSON.parse(val)); } catch { return validJson(val) ? val : null; }
  }
  if (val[0] === "'") {
    if (val.length >= 2 && val.endsWith("'")) {
      const inner = val.slice(1, -1).replace(/\\'/g, "'");
      return jstr(inner);
    }
    return null;
  }
  if (val[0] === "{" || val[0] === "[") {
    return validJson(val) ? val : null;
  }
  if (val === "true" || val === "True") return "true";
  if (val === "false" || val === "False") return "false";
  if (val === "null" || val === "None") return "null";
  const numchk = val.startsWith("-") ? val.slice(1) : val;
  if (/^[0-9]+(\.[0-9]+)?$/.test(numchk)) return val;
  if (validIdent(val)) return jstr(val);
  return null;
}

function splitCallArgs(body) {
  const segs = [];
  let depth = 0, inDQ = false, inSQ = false, start = 0;
  for (let i = 0; i < body.length; i++) {
    const c = body[i];
    if (inDQ) {
      if (c === "\\") i++;
      else if (c === '"') inDQ = false;
    } else if (inSQ) {
      if (c === "\\") i++;
      else if (c === "'") inSQ = false;
    } else if (c === '"') inDQ = true;
    else if (c === "'") inSQ = true;
    else if ("([{".includes(c)) depth++;
    else if (")]}" .includes(c)) depth--;
    else if (c === "," && depth === 0) {
      segs.push(body.slice(start, i));
      start = i + 1;
    }
  }
  const tail = body.slice(start);
  if (tail.trim()) segs.push(tail);
  return segs;
}

export function tryConvertFunctionCall(s, acts) {
  const t = s.trim();
  if (!FUNCCALL_FULL_RE.test(t)) return [s, false];
  const openIdx = t.indexOf("(");
  const name = t.slice(0, openIdx).trim();
  if (FUNCCALL_DENYLIST.has(name.toLowerCase())) return [s, false];
  const closeIdx = t.lastIndexOf(")");
  const argsBody = t.slice(openIdx + 1, closeIdx);
  const parts = [jstr(name)];
  for (let seg of splitCallArgs(argsBody)) {
    seg = seg.trim();
    if (!seg) continue;
    const eq = topEqIndex(seg);
    if (eq < 0) return [s, false];
    const key = seg.slice(0, eq).trim().replace(/^["']|["']$/g, "");
    if (!key || !validIdent(key)) return [s, false];
    const jv = convertArgValue(seg.slice(eq + 1).trim());
    if (jv === null) return [s, false];
    parts.push(jstr(key) + ":" + jv);
  }
  const out = '{"name":' + parts[0] + ',"arguments":{' + parts.slice(1).join(",") + "}}";
  if (!validJson(out)) return [s, false];
  act(acts, ACTION.CONVERTED_FUNCTION_CALL,
    `converted function call ${name}(...) to tool-call JSON`, 0);
  return [out, true];
}

export function tryConvertObjectArgCall(s, acts) {
  const t = s.trim();
  if (!FUNCCALL_FULL_RE.test(t)) return [s, false];
  const openIdx = t.indexOf("(");
  const name = t.slice(0, openIdx).trim();
  if (FUNCCALL_DENYLIST.has(name.toLowerCase())) return [s, false];
  const body = t.slice(openIdx + 1, t.lastIndexOf(")")).trim();
  if (body.length < 2 || body[0] !== "{" || body[body.length - 1] !== "}" || !validJson(body))
    return [s, false];
  const out = '{"name":' + jstr(name) + ',"arguments":' + body + "}";
  act(acts, ACTION.CONVERTED_FUNCTION_CALL,
    `converted object-argument call ${name}({...}) to tool-call JSON`, 0);
  return [out, true];
}

function scanAttrCall(t) {
  if (t.length < 3 || t[0] !== "<") return null;
  let i = 1;
  const start = i;
  while (i < t.length && /[A-Za-z0-9_.\-]/.test(t[i])) i++;
  const name = t.slice(start, i);
  if (!name) return null;
  const keys = [], vals = [];
  let selfClose = false;
  while (true) {
    while (i < t.length && " \t\r\n".includes(t[i])) i++;
    if (i >= t.length) return null;
    if (t[i] === "/") {
      i++;
      if (i < t.length && t[i] === ">") { selfClose = true; i++; break; }
      return null;
    }
    if (t[i] === ">") { i++; break; }
    const as = i;
    while (i < t.length && /[A-Za-z0-9_.\-]/.test(t[i])) i++;
    const key = t.slice(as, i);
    if (!key || i >= t.length || t[i] !== "=") return null;
    i++;
    if (i >= t.length || t[i] !== '"') return null;
    i++;
    const vs = i;
    while (i < t.length) {
      if (t[i] === '"' && (i + 1 >= t.length || " />".includes(t[i + 1]))) break;
      i++;
    }
    if (i >= t.length) return null;
    keys.push(key);
    vals.push(t.slice(vs, i));
    i++;
  }
  const rest = t.slice(i).trim();
  if (!selfClose) {
    if (rest !== "</" + name + ">") return null;
  } else if (rest) return null;
  return { name, keys, vals };
}

export function tryConvertXMLAttrCall(s, acts) {
  const parsed = scanAttrCall(s.trim());
  if (!parsed) return [s, false];
  const { name, keys, vals } = parsed;
  if (FUNCCALL_DENYLIST.has(name.toLowerCase())) return [s, false];
  const parts = [];
  for (let i = 0; i < keys.length; i++) {
    const jv = convertArgValue(vals[i].trim());
    if (jv === null) return [s, false];
    parts.push(jstr(keys[i]) + ":" + jv);
  }
  const out = '{"name":' + jstr(name) + ',"arguments":{' + parts.join(",") + "}}";
  if (!validJson(out)) return [s, false];
  act(acts, ACTION.CONVERTED_FUNCTION_CALL,
    `converted XML-attribute tool call <${name}/> to JSON`, 0);
  return [out, true];
}

function splitTopCommas(s) {
  const segs = [];
  let depth = 0, inDQ = false, inSQ = false, start = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (inDQ) {
      if (c === "\\") i++;
      else if (c === '"') inDQ = false;
    } else if (inSQ) {
      if (c === "\\") i++;
      else if (c === "'") inSQ = false;
    } else if (c === '"') inDQ = true;
    else if (c === "'") inSQ = true;
    else if ("([{".includes(c)) depth++;
    else if (")]}" .includes(c)) depth--;
    else if (c === "," && depth === 0) { segs.push(s.slice(start, i)); start = i + 1; }
  }
  segs.push(s.slice(start));
  return segs;
}

export function tryConvertWithLines(s, acts) {
  const t = s.trim();
  const calls = [];
  for (let ln of t.split("\n")) {
    ln = ln.trim();
    if (!ln) continue;
    const m = ln.match(/^([A-Za-z_]\w*)[ \t]+with[ \t]+(.+)$/);
    if (!m) return [s, false];
    calls.push([m[1], m[2].trim()]);
  }
  if (!calls.length) return [s, false];
  const buildOne = (name, kv) => {
    if (FUNCCALL_DENYLIST.has(name.toLowerCase())) return null;
    const parts = [jstr(name)];
    for (let seg of splitTopCommas(kv)) {
      seg = seg.trim();
      if (!seg) continue;
      const eq = topEqIndex(seg);
      if (eq < 0) return null;
      const key = seg.slice(0, eq).trim().replace(/^["']|["']$/g, "");
      if (!key || !validIdent(key)) return null;
      const jv = convertArgValue(seg.slice(eq + 1).trim());
      if (jv === null) return null;
      parts.push(jstr(key) + ":" + jv);
    }
    const out = '{"name":' + parts[0] + ',"arguments":{' + parts.slice(1).join(",") + "}}";
    return validJson(out) ? out : null;
  };
  const outs = [];
  for (const [name, kv] of calls) {
    const o = buildOne(name, kv);
    if (o === null) return [s, false];
    outs.push(o);
  }
  const out = outs.length === 1 ? outs[0] : "[" + outs.join(",") + "]";
  if (!validJson(out)) return [s, false];
  act(acts, ACTION.CONVERTED_FUNCTION_CALL,
    `converted ${outs.length} prose-style call(s) to tool-call JSON`, 0);
  return [out, true];
}

export function extractSingleCallFromText(s, acts) {
  const t = s.trim();
  const loc = FUNCCALL_ARGS_RE.exec(t);
  if (!loc) return s;
  const start = loc.index;
  const absOpen = t.indexOf("(", start);
  let depth = 0, inDQ = false, inSQ = false, end = -1, i = absOpen;
  while (i < t.length) {
    const c = t[i];
    if (inDQ) {
      if (c === "\\") i++;
      else if (c === '"') inDQ = false;
    } else if (inSQ) {
      if (c === "\\") i++;
      else if (c === "'") inSQ = false;
    } else if (c === '"') inDQ = true;
    else if (c === "'") inSQ = true;
    else if ("([{".includes(c)) depth++;
    else if (")]}" .includes(c)) {
      depth--;
      if (depth === 0 && c === ")") { end = i + 1; break; }
    }
    i++;
  }
  if (end < 0) return s;
  const candidate = t.slice(start, end);
  const callName = t.slice(start, absOpen).trim();
  const argsBody = candidate.slice(absOpen - start + 1, candidate.length - 1);
  if (FUNCCALL_DENYLIST.has(callName.toLowerCase()) || topEqIndex(argsBody) < 0) return s;
  const [conv, ok] = tryConvertFunctionCall(candidate, acts);
  if (!ok) return s;
  if (start > 0 && hasLetter(t.slice(0, start))) {
    act(acts, ACTION.REMOVED_PREAMBLE,
      `removed prose before extracted function call (${start} chars)`, 0);
  }
  return conv;
}

export function quoteBareValues(s) {
  const mask = stringMask(s);
  let out = "", first = -1, i = 0;
  while (i < s.length) {
    const c = s[i];
    if (mask[i] || c !== ":") { out += c; i++; continue; }
    out += c;
    const j = nextNonSpace(s, i + 1);
    if (j < 0 || !/[A-Za-z]/.test(s[j])) { i++; continue; }
    let k = j;
    while (k < s.length && !mask[k] && !",}]\n".includes(s[k])) k++;
    let valEnd = k;
    while (valEnd > j && " \t".includes(s[valEnd - 1])) valEnd--;
    const run = s.slice(j, valEnd);
    if (!run || ["true", "false", "null", "True", "False", "None"].includes(run) ||
        /^-?[0-9]+(\.[0-9]+)?$/.test(run)) { i++; continue; }
    out += s.slice(i + 1, j) + jstr(run);
    if (first < 0) first = j;
    i = valEnd;
  }
  return first < 0 ? [s, -1, false] : [out, first, true];
}

export function unwrapStringifiedJSON(s) {
  let firstPos = -1;
  const out = s.replace(STRINGIFIED_RE, (tok, inner) => {
    try {
      const decoded = JSON.parse('"' + inner + '"');
      if (typeof decoded === "string") {
        const stripped = decoded.trim();
        if (stripped.startsWith("{")) {
          JSON.parse(stripped);
          if (firstPos < 0) firstPos = s.indexOf(tok);
          return stripped;
        }
      }
      return tok;
    } catch {
      return tok;
    }
  });
  if (firstPos < 0 || out === s || !validJson(out)) return [s, -1, false];
  return [out, firstPos, true];
}

export function jsonRepair(src, depth, acts) {
  let s = src;
  if (depth >= 3 && s.includes('"{')) {
    const [v, pos, found] = unwrapStringifiedJSON(s);
    if (found) {
      s = v;
      act(acts, ACTION.UNWRAPPED_STRINGIFIED,
        `inlined stringified JSON at ${pos}`, pos);
    }
  }
  if (validJson(s)) return s;
  if (s.includes("'") && depth >= 2) {
    const [v, pos, found] = fixSingleQuotes(s);
    if (found) {
      s = v;
      act(acts, ACTION.FIXED_SINGLE_QUOTES,
        `converted single-quoted key/value(s) starting at ${pos}`, pos);
      if (validJson(s)) return s;
    }
  }
  if (["True", "False", "None"].some((w) => s.includes(w))) {
    const [v, pos, found] = fixPythonLiterals(s);
    if (found) {
      s = v;
      act(acts, ACTION.FIXED_PYTHON_LITERALS,
        `replaced Python literal(s) starting at ${pos}`, pos);
      if (validJson(s)) return s;
    }
  }
  if (s.includes(",")) {
    const [v, pos, found] = fixTrailingCommas(s);
    if (found) {
      s = v;
      act(acts, ACTION.FIXED_TRAILING_COMMA,
        `removed trailing comma(s) starting at ${pos}`, pos);
      if (validJson(s)) return s;
    }
  }
  if (depth >= 2) {
    if ((s.includes("{") || s.includes(",")) && s.includes(":")) {
      const [v, pos, found] = quoteBareKeys(s);
      if (found) {
        s = v;
        act(acts, ACTION.QUOTED_BARE_KEYS,
          `quoted bare key(s) starting at ${pos}`, pos);
        if (validJson(s)) return s;
      }
    }
    if (s.includes("\n") || s.includes(";") || s.includes("\\n")) {
      const [v, found] = mergeNDJSON(s);
      if (found) {
        s = v;
        act(acts, ACTION.MERGED_NDJSON,
          "merged newline-delimited JSON values into one array", 0);
        if (validJson(s)) return s;
      }
    }
    const [v, pos, found] = balanceTruncatedJSON(s, depth);
    if (found) {
      s = v;
      act(acts, ACTION.REPAIRED_TRUNCATED_JSON,
        `closed truncated structure near ${pos}`, pos);
    }
    if (depth >= 3) {
      const [v2, pos2, found2] = quoteBareValues(s);
      if (found2) {
        s = v2;
        act(acts, ACTION.QUOTED_BARE_VALUES,
          `quoted bare value(s) starting at ${pos2}`, pos2);
        if (validJson(s)) return s;
      }
      const [v3, pos3, found3] = unwrapStringifiedJSON(s);
      if (found3) {
        s = v3;
        act(acts, ACTION.UNWRAPPED_STRINGIFIED,
          `inlined stringified JSON at ${pos3}`, pos3);
      }
    }
  }
  return s;
}

export function stripOrphanCloseTags(s, acts) {
  const out = [];
  const stack = [];
  let dropped = 0, firstPos = -1, i = 0;
  while (i < s.length) {
    const c = s[i];
    if (c !== "<") { out.push(c); i++; continue; }
    const rest = s.slice(i);
    const closeM = rest.match(/^<\/\s*([A-Za-z][\w.\-:]*)\s*>/);
    if (closeM) {
      const name = closeM[1];
      let found = false;
      for (let k = stack.length - 1; k >= 0; k--) {
        if (stack[k] === name) { stack.length = k; found = true; break; }
      }
      if (found) { out.push(closeM[0]); i += closeM[0].length; continue; }
      if (BLEED_TAGS.has(name.toLowerCase())) {
        dropped++;
        if (firstPos < 0) firstPos = i;
        i += closeM[0].length;
        continue;
      }
      out.push(closeM[0]);
      i += closeM[0].length;
      continue;
    }
    const openM = rest.match(/^<([A-Za-z][\w.\-:]*)/);
    if (!openM) { out.push(c); i++; continue; }
    const gt = rest.indexOf(">");
    if (gt < 0) { out.push(c); i++; continue; }
    const inner = rest.slice(1, gt).replace(/\s+$/, "");
    out.push(rest.slice(0, gt + 1));
    if (!inner.endsWith("/")) stack.push(openM[1]);
    i += gt + 1;
  }
  if (dropped) {
    act(acts, ACTION.FIXED_XML_ORPHAN_CLOSE,
      `removed ${dropped} orphan template tag(s) from text output`, firstPos);
    return out.join("");
  }
  return s;
}

function normalizeOutput(s, acts) {
  let out = s;
  if (ESC_TARGETS.some(([seq]) => out.includes(seq))) {
    let pos = -1;
    for (const [seq] of ESC_TARGETS) {
      const k = out.indexOf(seq);
      if (k >= 0 && (pos < 0 || k < pos)) pos = k;
    }
    for (const [seq, rep] of ESC_TARGETS) out = out.split(seq).join(rep);
    act(acts, ACTION.NORMALIZED_UNICODE_ESC,
      `decoded unicode escape(s) starting at ${Math.max(pos, 0)}`, pos);
  }
  if (out.includes("\r")) {
    const pos = out.indexOf("\r");
    out = out.split("\r\n").join("\n").split("\r").join("\n");
    act(acts, ACTION.NORMALIZED_LINE_ENDINGS,
      `normalized CRLF/CR to LF at ${pos}`, pos);
  }
  return collapseWhitespace(out, acts);
}

function collapseWhitespace(s, acts) {
  const mask = stringMask(s);
  const out = [];
  let nlRun = 0, spStart = -1, prevNl = true, i = 0;
  const flushNl = () => {
    const n = Math.min(nlRun, 2);
    out.push("\n".repeat(n));
    if (n > 0) prevNl = true;
    nlRun = 0;
  };
  while (i < s.length) {
    const c = s[i];
    if (mask[i]) {
      flushNl();
      if (spStart >= 0) {
        if (prevNl) out.push(s.slice(spStart, i));
        else out.push(" ");
        spStart = -1;
      }
      out.push(c);
      prevNl = false;
      i++;
      continue;
    }
    if (c === "\n") { spStart = -1; nlRun++; i++; }
    else if (c === " " || c === "\t") {
      if (spStart < 0) spStart = i;
      i++;
    } else {
      flushNl();
      if (spStart >= 0) {
        if (prevNl) out.push(s.slice(spStart, i));
        else out.push(" ");
        spStart = -1;
      }
      prevNl = false;
      out.push(c);
      i++;
    }
  }
  flushNl();
  const res = out.join("").replace(/^\n+|\n+$/g, "");
  if (res !== s) {
    act(acts, ACTION.COLLAPSED_WHITESPACE, "collapsed excess whitespace", 0);
  }
  return res;
}

function detect(raw) {
  const lower = raw.toLowerCase();
  const hasOpenThink = ["<think", "<reasoning", "<reflection"].some((n) => lower.includes(n));
  const hasCloseThink = ["</think", "</reasoning", "</reflection"].some((n) => lower.includes(n));
  const trimmedRaw = raw.trim();
  const fullCall = FUNCCALL_FULL_RE.test(trimmedRaw);
  const looseCall = !fullCall && FUNCCALL_ARGS_RE.test(raw);
  const withLines = WITH_LINES_RE.test(raw);
  const hasStringified = raw.includes('"{');
  const hasTool = ["<tool_call", "</tool_call", "<function_call", "</function_call"].some((n) => lower.includes(n));
  const hasTmpl = lower.includes("<|");
  const hasFence = raw.includes("```") || raw.includes("~~~");
  const hasBox = [...raw].some((c) => BOX_RUNES.has(c));
  const hasCr = raw.includes("\r");
  const hasEsc = ESC_TARGETS.some(([seq]) => raw.includes(seq));
  const start = firstStructuralIndex(raw);
  const hasPreamble = start > 0 && hasLetter(raw.slice(0, start));
  const validJ = validJson(raw.trim());
  let j = -1;
  for (const ch of ["{", "["]) {
    const k = raw.indexOf(ch);
    if (k >= 0 && (j < 0 || k < j)) j = k;
  }
  const x = xmlTagIndex(raw);
  const jsonIntent = j >= 0 && (x < 0 || j < x) && !fullCall;
  const xmlIntent = !jsonIntent && x >= 0 && !fullCall;
  const malformed = jsonIntent && !validJ;
  let guess = ModelFamily.GENERIC;
  if (hasOpenThink) guess = ModelFamily.QWEN;
  else if (hasCloseThink) guess = ModelFamily.DEEPSEEK;
  else if (hasBox) guess = ModelFamily.GLM;
  return {
    artifact: hasOpenThink || hasCloseThink || hasTool || hasTmpl || hasFence ||
      hasBox || hasCr || hasEsc || hasPreamble || fullCall || looseCall ||
      withLines || hasStringified,
    validJson: validJ, jsonIntent, xmlIntent,
    malformed: malformed, guess, fullCall,
  };
}

export function process(input, opts = new Options()) {
  if (!input) return new Result();
  const info = detect(input);
  const guess = opts.modelHint !== ModelFamily.GENERIC ? opts.modelHint : info.guess;

  let fast = !info.artifact && !info.malformed &&
    (info.validJson || (!info.jsonIntent && !info.xmlIntent));
  if (fast && opts.targetFormat === Format.JSON && !info.validJson) fast = false;
  if (fast && opts.targetFormat === Format.XML) fast = info.xmlIntent;
  if (fast) {
    const conf = info.validJson ? 1.0 : 0.0;
    return new Result({ output: input, confidence: conf, modelGuess: guess });
  }

  const acts = [];
  let out = input;
  if (opts.stripReasoning) out = stripReasoning(out, acts);
  out = stripToolWrappers(out, acts);
  out = stripFences(out, acts);
  out = stripBoxLines(out, acts);

  let j = -1;
  for (const ch of ["{", "["]) {
    const k = out.indexOf(ch);
    if (k >= 0 && (j < 0 || k < j)) j = k;
  }
  const x = xmlTagIndex(out);
  const jsonShape = j >= 0 && (x < 0 || j < x);
  const xmlShape = x >= 0 && (j < 0 || x < j) &&
    (/[A-Za-z]/.test(out[x + 1]) || "?!".includes(out[x + 1]));
  const structured = (jsonShape || xmlShape) && !info.fullCall;

  if (opts.targetFormat !== Format.PLAIN_TEXT && structured) {
    out = stripPreamble(out, acts);
    out = stripTail(out, acts);
    const ft = out.trim();
    if (opts.repairJson && (ft.startsWith("{") || ft.startsWith("["))) {
      const depth = Math.max(1, Math.min(opts.maxRepairDepth, 3));
      out = jsonRepair(out, depth, acts);
    }
  } else {
    const tstrip = out.trim();
    let converted = null;
    {
      const [v, ok] = tryConvertObjectArgCall(tstrip, acts);
      if (ok) converted = v;
    }
    if (converted === null) {
      const [v, ok] = tryConvertFunctionCall(tstrip, acts);
      if (ok) converted = v;
    }
    if (converted === null && FUNCCALL_ARGS_RE.test(tstrip)) {
      const out2 = extractSingleCallFromText(out, acts);
      if (out2 !== out) converted = out2;
    }
    if (converted === null) {
      const [v, ok] = tryConvertWithLines(out, acts);
      if (ok) converted = v;
    }
    if (converted !== null) out = converted;
    out = stripOrphanCloseTags(out, acts);
  }

  out = normalizeOutput(out, acts);
  let final = out.trim();

  if (!final && input.trim()) {
    return new Result({
      output: input, repairs: acts, modelGuess: guess,
      error: "unable to repair input into a valid target format",
    });
  }

  let verified =
    ((final.startsWith("{") || final.startsWith("[")) && validJson(final));
  const wantStructured = opts.targetFormat === Format.JSON ||
    opts.targetFormat === Format.XML ||
    final.startsWith("{") || final.startsWith("[") || final.startsWith("<");

  if (wantStructured && !verified) {
    let conv = null, ok = false;
    if (final.startsWith("<")) {
      [conv, ok] = tryConvertXMLAttrCall(out.trim(), acts);
    }
    if (!ok) [conv, ok] = tryConvertWithLines(out, acts);
    if (!ok) [conv, ok] = tryConvertObjectArgCall(out.trim(), acts);
    if (ok && validJson(conv)) {
      out = conv;
      final = out.trim();
      verified = (final.startsWith("{") || final.startsWith("[")) && validJson(final);
    }
  }

  if (wantStructured && !verified) {
    return new Result({
      output: input, repairs: acts, modelGuess: guess,
      error: "unable to repair input into a valid target format",
    });
  }

  return new Result({
    output: out, cleaned: out !== input, repairs: acts,
    confidence: verified ? 1.0 : 0.0, modelGuess: guess,
  });
}

export function fix(input, opts) {
  return process(input, opts).output;
}

export default { fix, process, Options, Result, RepairAction, Format, ModelFamily, ACTION };
