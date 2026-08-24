"""outfix core — faithful port of github.com/Maybeyes111/outfix (Go).

Cleans malformed/polluted LLM output before it hits application logic.
Positions in RepairAction are character offsets (Go uses byte offsets).
"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass, field

ACTION_STRIPPED_THINK_BLOCK = "stripped_think_block"
ACTION_STRIPPED_ORPHAN_CLOSE_TAG = "stripped_orphan_close_tag"
ACTION_UNWRAPPED_TOOL_CALL = "unwrapped_tool_call"
ACTION_STRIPPED_CHAT_TEMPLATE = "stripped_chat_template"
ACTION_STRIPPED_CODE_FENCE = "stripped_code_fence"
ACTION_STRIPPED_BOX_DRAWING = "stripped_box_drawing"
ACTION_REMOVED_PREAMBLE = "removed_preamble"
ACTION_REMOVED_POSTAMBLE = "removed_postamble"
ACTION_STRIPPED_XML_BLEED = "stripped_xml_bleed"
ACTION_FIXED_SINGLE_QUOTES = "fixed_single_quotes"
ACTION_FIXED_PYTHON_LITERALS = "fixed_python_literals"
ACTION_FIXED_TRAILING_COMMA = "fixed_trailing_comma"
ACTION_QUOTED_BARE_KEYS = "quoted_bare_keys"
ACTION_MERGED_NDJSON = "merged_ndjson"
ACTION_REPAIRED_TRUNCATED_JSON = "repaired_truncated_json"
ACTION_CONVERTED_FUNCTION_CALL = "converted_function_call"
ACTION_QUOTED_BARE_VALUES = "quoted_bare_values"
ACTION_UNWRAPPED_STRINGIFIED = "unwrapped_stringified_json"
ACTION_FIXED_XML_ORPHAN_CLOSE = "fixed_xml_orphan_close"
ACTION_NORMALIZED_UNICODE_ESC = "normalized_unicode_escapes"
ACTION_NORMALIZED_LINE_ENDINGS = "normalized_line_endings"
ACTION_COLLAPSED_WHITESPACE = "collapsed_whitespace"

FORMAT_AUTO, FORMAT_JSON, FORMAT_XML, FORMAT_PLAIN_TEXT = range(4)
MODEL_GENERIC, MODEL_QWEN, MODEL_DEEPSEEK, MODEL_GLM = range(4)

_MODEL_NAMES = {"generic": MODEL_GENERIC, "qwen": MODEL_QWEN,
                "deepseek": MODEL_DEEPSEEK, "glm": MODEL_GLM}

_THINK_RE = re.compile(r"<\s*/?\s*(thinking|reflection|reasoning|think)\s*>", re.I)
_TOOLWRAP_RE = re.compile(r"<\s*/?\s*(tool_call|tool_calls|function_call|function_calls)\s*>", re.I)
_CHATTEMPLATE_RE = re.compile(r"<\|[a-zA-Z_]+\|>")
_FUNC_CALL_FULL_RE = re.compile(r"^\s*[A-Za-z_]\w*(?:\.\w+)*\s*\((?s:.*)\)\s*$")
_FUNC_CALL_ARGS_RE = re.compile(r"[A-Za-z_]\w*(?:\.\w+)*\s*\(")
_FUNC_CALL_DENYLIST = {"def", "print", "len", "range", "int", "str", "float",
                       "bool", "list", "dict", "set", "tuple", "open", "input",
                       "type", "super", "isinstance", "getattr", "setattr",
                       "if", "for", "while", "return", "lambda", "func"}
_BOX_RUNES = set("╭╮╰╯│┃┌┐└┘├┤┬┴┼─━═·")
_ESC_TARGETS = [("\\u003c", "<"), ("\\u003C", "<"), ("\\u003e", ">"),
                ("\\u003E", ">"), ("\\u0026", "&")]
_BLEED_TAGS = {"content", "system", "prompt", "response", "assistant",
               "user", "message", "answer", "output", "result",
               "tool_response", "assistant_response", "tool_call",
               "function_result"}


@dataclass
class RepairAction:
    type: str
    description: str
    position: int


@dataclass
class Options:
    target_format: int = FORMAT_AUTO
    strip_reasoning: bool = True
    repair_json: bool = True
    model_hint: int = MODEL_GENERIC
    max_repair_depth: int = 2


@dataclass
class Result:
    output: str = ""
    cleaned: bool = False
    repairs: list = field(default_factory=list)
    confidence: float = 0.0
    model_guess: int = MODEL_GENERIC
    error: str | None = None


class OutfixRepairFailed(Exception):
    """Raised by process(strict=True) when repair cannot verify."""


def _act(acts, typ, desc, pos):
    acts.append(RepairAction(typ, desc, max(pos, 0)))


def _valid_json(s):
    if s != s.strip():
        return False
    try:
        json.loads(s)
        return True
    except ValueError:
        return False


def _string_mask(s):
    mask = [False] * len(s)
    inside = esc = False
    for i, ch in enumerate(s):
        if inside:
            mask[i] = True
            if esc:
                esc = False
            elif ch == "\\":
                esc = True
            elif ch == '"':
                inside = False
        elif ch == '"':
            inside = True
            mask[i] = True
    return mask


def _ident_byte(c):
    return c.isascii() and (c.isalnum() or c in "_-")


def _next_nonspace(s, i):
    while i < len(s) and s[i] in " \t\r\n":
        i += 1
    return i if i < len(s) else -1


def _first_structural_index(s):
    j = min((k for k in (s.find("{"), s.find("[")) if k >= 0), default=-1)
    x = _xml_tag_index(s)
    if j < 0:
        return x
    if x < 0:
        return j
    return min(j, x)


def _xml_tag_index(s):
    for i, ch in enumerate(s):
        if ch == "<" and i + 1 < len(s):
            nxt = s[i + 1]
            if nxt.isalpha() or nxt in "/?!":
                return i
    return -1


def _json_root_span(s):
    start = min((k for k in (s.find("{"), s.find("[")) if k >= 0), default=-1)
    if start < 0:
        return None
    mask = _string_mask(s)
    open_of = {"}": "{", "]": "["}
    stack = []
    for i in range(start, len(s)):
        if mask[i]:
            continue
        c = s[i]
        if c in "{[":
            stack.append(c)
        elif c in "}]":
            if not stack or stack[-1] != open_of[c]:
                return None
            stack.pop()
            if not stack:
                return (start, i + 1)
    return None


def _has_letter(s):
    return any(ch.isalpha() for ch in s)


def _remove_spans(s, spans):
    spans = sorted(spans)
    out = []
    prev = 0
    for a, b in spans:
        if a > prev:
            out.append(s[prev:a])
        if b > prev:
            prev = b
    out.append(s[prev:])
    return "".join(out)


# ---------------------------------------------------------------- stripper

def strip_reasoning(s, acts):
    matches = list(_THINK_RE.finditer(s))
    if not matches:
        return s
    stack = []  # (start, name)
    spans = []
    paired = orphan = 0
    first_orphan = -1
    for m in matches:
        tok = m.group(0)
        name = m.group(1).lower()
        if not tok.lstrip().startswith("</"):
            stack.append((m.start(), name))
            continue
        found = -1
        for i in range(len(stack) - 1, -1, -1):
            if stack[i][1] == name:
                found = i
                break
        if found >= 0:
            spans.append((stack[found][0], m.end()))
            del stack[found:]
            paired += 1
        else:
            orphan += 1
            spans.append((m.start(), m.end()))
            if first_orphan < 0:
                first_orphan = m.start()
    if stack:
        last_open_end = _tag_end(s, stack[-1][0])
        fs = _first_structural_index(s[last_open_end:])
        if fs >= 0:
            spans.append((stack[0][0], last_open_end + fs))
        else:
            spans.append((stack[0][0], len(s)))
    if not spans:
        return s
    out = _remove_spans(s, spans)
    _act(acts, ACTION_STRIPPED_THINK_BLOCK,
         f"removed {paired} paired reasoning block(s)", spans[0][0])
    if orphan:
        _act(acts, ACTION_STRIPPED_ORPHAN_CLOSE_TAG,
             f"removed {orphan} orphan close tag(s)", first_orphan)
    return out


def _tag_end(s, tag_start):
    close = s.find(">", tag_start)
    return close + 1 if close >= 0 else len(s)


def strip_tool_wrappers(s, acts):
    ms = list(_TOOLWRAP_RE.finditer(s))
    if ms:
        s2 = _remove_spans(s, [(m.start(), m.end()) for m in ms])
        _act(acts, ACTION_UNWRAPPED_TOOL_CALL,
             f"unwrapped {len(ms)} tool-call tag(s)", ms[0].start())
        s = s2
    ms = list(_CHATTEMPLATE_RE.finditer(s))
    if ms:
        s2 = _remove_spans(s, [(m.start(), m.end()) for m in ms])
        _act(acts, ACTION_STRIPPED_CHAT_TEMPLATE,
             f"removed {len(ms)} chat-template token(s)", ms[0].start())
        s = s2
    return s


def _parse_fence_line(tok):
    if not tok or tok[0] not in "`~":
        return None
    n = 0
    while n < len(tok) and tok[n] == tok[0]:
        n += 1
    if n < 3:
        return None
    rest = tok[n:]
    if rest == "":
        return (tok[0], "")
    if all(c.isalnum() or c in "+-._" for c in rest):
        return (tok[0], rest.lower())
    return None


def _find_fence_regions(s):
    regions = []
    lines = s.split("\n")
    offset = 0
    i = 0
    while i < len(lines):
        line = lines[i]
        stripped = line.strip()
        fence = _parse_fence_line(stripped)
        if fence is None:
            offset += len(line) + 1
            i += 1
            continue
        body_lines = []
        j = i + 1
        closed = False
        while j < len(lines):
            inner = _parse_fence_line(lines[j].strip())
            if inner is not None and inner[0] == fence[0] and inner[1] == "":
                closed = True
                break
            body_lines.append(lines[j])
            j += 1
        body = "\n".join(body_lines)
        opener_start = offset
        if closed:
            close_end = offset + sum(len(l) + 1 for l in lines[i:j + 1])
            regions.append({"opener": opener_start, "close_end": close_end,
                            "body": body, "lang": fence[1]})
            consumed = lines[i:j + 1]
        else:
            close_end = len(s)
            regions.append({"opener": opener_start, "close_end": close_end,
                            "body": body, "lang": fence[1]})
            consumed = lines[i:]
        offset += sum(len(l) + 1 for l in consumed)
        i += len(consumed)
    return regions


def strip_fences(s, acts):
    regions = _find_fence_regions(s)
    if regions:
        chosen = None
        for r in regions:
            t = r["body"].strip()
            if t and (t[0] in "{[" or (t[0] == "<" and len(t) > 1 and t[1].isalpha())):
                chosen = r
                break
        if chosen is None:
            chosen = max(regions, key=lambda r: len(r["body"]))
        s = s[:chosen["opener"]] + chosen["body"].rstrip("\r\n") + s[chosen["close_end"]:]
        lang = chosen["lang"]
        _act(acts, ACTION_STRIPPED_CODE_FENCE,
             f"stripped {lang} code fence" if lang else "stripped markdown code fence",
             chosen["opener"])
    t = s.rstrip(" \t\r\n")
    if t.endswith("```") or t.endswith("~~~"):
        li = t.rfind("\n")
        candidate = t[li + 1:] if li >= 0 else t
        pos = li + 1 if li >= 0 else 0
        tok = candidate.strip()
        if len(tok) >= 3 and tok[0] in "`~" and tok.count(tok[0]) == len(tok):
            s = s[:pos]
            _act(acts, ACTION_STRIPPED_CODE_FENCE,
                 "removed stray trailing code fence", pos)
    return s


def strip_box_lines(s, acts):
    if not any(c in _BOX_RUNES for c in s):
        return s
    lines = s.split("\n")
    kept, dropped, first_pos, offset = [], 0, -1, 0
    for ln in lines:
        body = ln.rstrip("\r").replace(" ", "").replace("\t", "")
        if body and all(c in _BOX_RUNES for c in body):
            dropped += 1
            if first_pos < 0:
                first_pos = offset
        else:
            kept.append(ln)
        offset += len(ln) + 1
    if dropped == 0 or not kept:
        return s
    _act(acts, ACTION_STRIPPED_BOX_DRAWING,
         f"removed {dropped} box-drawing line(s)", first_pos)
    return "\n".join(kept)


def strip_preamble(s, acts):
    idx = _first_structural_index(s)
    if idx <= 0 or not _has_letter(s[:idx]):
        return s
    _act(acts, ACTION_REMOVED_PREAMBLE,
         f"removed preamble before payload ({idx} chars)", 0)
    return s[idx:]


def strip_tail(s, acts):
    span = _json_root_span(s)
    if span:
        start, end = span
        tt = s[end:].strip()
        if not tt:
            return s
        if tt[0] in "{[":
            return s
        if tt[0] == "\\" and len(tt) > 2 and tt[1] in "ntr" and any(c in tt[2:] for c in "{["):
            return s
        if tt[0] == "<":
            name = _close_tag_name(tt)
            if name and f"<{name}" not in s[start:end]:
                _act(acts, ACTION_STRIPPED_XML_BLEED,
                     f"stripped trailing XML bleed </{name}>", end)
                return s[:end]
            return s
        _act(acts, ACTION_REMOVED_POSTAMBLE,
             f"removed postamble after JSON payload ({len(s[end:])} chars)", end)
        return s[:end]
    return s


def _close_tag_name(s):
    idx = s.find("</")
    if idx < 0:
        return ""
    rest = s[idx + 2:]
    name = re.match(r"[A-Za-z][\w.\-:]*", rest)
    return name.group(0) if name else ""


# ---------------------------------------------------------------- repairer

def _fix_python_literals(s):
    mask = _string_mask(s)
    repl = {"True": "true", "False": "false", "None": "null"}
    out, first, i = [], -1, 0
    while i < len(s):
        if mask[i] or not s[i].isalpha():
            out.append(s[i])
            i += 1
            continue
        matched = next((lit for lit in ("True", "False", "None")
                        if s.startswith(lit, i)), None)
        if not matched:
            out.append(s[i])
            i += 1
            continue
        end = i + len(matched)
        prev_ok = i == 0 or not _ident_byte(s[i - 1])
        next_ok = end >= len(s) or not _ident_byte(s[end])
        if prev_ok and next_ok:
            if first < 0:
                first = i
            out.append(repl[matched])
            i = end
            continue
        out.append(s[i])
        i += 1
    if first < 0:
        return s, -1, False
    return "".join(out), first, True


def _fix_trailing_commas(s):
    mask = _string_mask(s)
    out, first = [], -1
    i = 0
    while i < len(s):
        c = s[i]
        if c == "," and not mask[i]:
            j = _next_nonspace(s, i + 1)
            if j >= 0 and s[j] in "}]":
                if first < 0:
                    first = i
                i += 1
                continue
        out.append(c)
        i += 1
    if first < 0:
        return s, -1, False
    return "".join(out), first, True


def _find_single_quote_close(s, frm):
    k = frm
    while k < len(s):
        c = s[k]
        if c in "\n\"":
            return -1
        if c == "\\":
            k += 2
            continue
        if c == "'":
            return -1 if k - frm > 4096 else k
        k += 1
    return -1


def _fix_single_quotes(s):
    mask = _string_mask(s)
    out, first, i = [], -1, 0
    while i < len(s):
        c = s[i]
        if c != "'" or mask[i]:
            out.append(c)
            i += 1
            continue
        p = i
        while p > 0 and s[p - 1] in " \t\n\r":
            p -= 1
        open_ok = p > 0 and s[p - 1] in "{[:,"
        if not open_ok:
            out.append(c)
            i += 1
            continue
        close_idx = _find_single_quote_close(s, i + 1)
        if close_idx < 0:
            out.append(c)
            i += 1
            continue
        j = _next_nonspace(s, close_idx + 1)
        close_ok = j >= 0 and s[j] in ":,}]"
        if not close_ok:
            out.append(c)
            i += 1
            continue
        if first < 0:
            first = i
        out.append('"')
        k = i + 1
        while k < close_idx:
            if s[k] == "\\" and k + 1 < close_idx and s[k + 1] == "'":
                out.append("'")
                k += 2
                continue
            out.append(s[k])
            k += 1
        out.append('"')
        i = close_idx + 1
    if first < 0:
        return s, -1, False
    return "".join(out), first, True


def _quote_bare_keys(s):
    mask = _string_mask(s)
    out, first, i = [], -1, 0
    while i < len(s):
        c = s[i]
        if mask[i] or c not in "{,":
            out.append(c)
            i += 1
            continue
        j = _next_nonspace(s, i + 1)
        if j < 0 or not (s[j].isalpha() and s[j].isascii()) and s[j] != "_":
            out.append(c)
            i += 1
            continue
        k = j
        while k < len(s) and _ident_byte(s[k]):
            k += 1
        m = _next_nonspace(s, k)
        if m < 0 or s[m] != ":":
            out.append(c)
            i += 1
            continue
        if first < 0:
            first = j
        out.append(s[i + 1:j])
        out.append('"' + s[j:k] + '"')
        i = k
    if first < 0:
        return s, -1, False
    return "".join(out), first, True


def _match_bracket_from(s, mask, start):
    opn = s[start]
    clo = {"{": "}", "[": "]"}.get(opn)
    if clo is None:
        return -1
    depth = 0
    for i in range(start, len(s)):
        if mask[i]:
            continue
        if s[i] == opn:
            depth += 1
        elif s[i] == clo:
            depth -= 1
            if depth == 0:
                return i
    return -1


def _merge_ndjson(s):
    mask = _string_mask(s)
    vals = []
    i = 0
    while i < len(s):
        c = s[i]
        if mask[i]:
            i += 1
            continue
        if c in "{[":
            end = _match_bracket_from(s, mask, i)
            if end < 0:
                return s, False
            vals.append(s[i:end + 1].strip())
            i = end + 1
            continue
        if c in " \t\r\n,;" or (c == "\\" and i + 1 < len(s) and s[i + 1] in "ntr"):
            i += 2 if c == "\\" else 1
            continue
        return s, False
    if len(vals) < 2:
        return s, False
    if not all(_valid_json(v) for v in vals):
        return s, False
    merged = "[" + ",".join(vals) + "]"
    return (merged, True) if _valid_json(merged) else (s, False)


def _complete_partial_literal(t):
    k = len(t)
    while k > 0 and t[k - 1].isalpha():
        k -= 1
    run = t[k:]
    table = {"t": "true", "tr": "true", "tru": "true",
             "f": "false", "fa": "false", "fal": "false", "fals": "false",
             "n": "null", "nu": "null", "nul": "null"}
    if run in table and len(table[run]) > len(run):
        return table[run], k
    return None, -1


def _balance_truncated_json(s, depth):
    mask = _string_mask(s)
    stack = []
    for i, c in enumerate(s):
        if mask[i]:
            continue
        if c in "{[":
            stack.append(c)
        elif c in "}]":
            if not stack:
                return s, -1, False
            want_open = "{" if c == "}" else "["
            if stack[-1] != want_open:
                return s, -1, False
            stack.pop()
    in_str = bool(mask) and mask[-1]

    trailing = len(s)
    while trailing > 0 and s[trailing - 1] in " \t\r\n":
        trailing -= 1
    orig_t = t = s[:trailing]
    if in_str:
        t += '"'
    if t.endswith(":") and depth >= 2:
        t += "null"
    if depth >= 3:
        comp, k = _complete_partial_literal(t)
        if comp:
            t = t[:k] + comp
    if t.endswith(","):
        t = t[:-1]

    if not stack:
        if t != orig_t and _valid_json(t):
            return t, max(len(t) - 1, 0), True
        return s, -1, False
    closers = "".join("}" if c == "{" else "]" for c in reversed(stack))
    cand = t + closers
    if _valid_json(cand):
        return cand, len(t), True
    return s, -1, False


def json_repair(src, depth, acts):
    s = src
    if depth >= 3 and '"{' in s:
        v, pos, found = _unwrap_stringified_json(s)
        if found:
            s = v
            _act(acts, ACTION_UNWRAPPED_STRINGIFIED,
                 f"inlined stringified JSON at {pos}", pos)
    if _valid_json(s):
        return s
    if "'" in s and depth >= 2:
        v, pos, found = _fix_single_quotes(s)
        if found:
            s = v
            _act(acts, ACTION_FIXED_SINGLE_QUOTES,
                 f"converted single-quoted key/value(s) starting at {pos}", pos)
            if _valid_json(s):
                return s
    if any(w in s for w in ("True", "False", "None")):
        v, pos, found = _fix_python_literals(s)
        if found:
            s = v
            _act(acts, ACTION_FIXED_PYTHON_LITERALS,
                 f"replaced Python literal(s) starting at {pos}", pos)
            if _valid_json(s):
                return s
    if "," in s:
        v, pos, found = _fix_trailing_commas(s)
        if found:
            s = v
            _act(acts, ACTION_FIXED_TRAILING_COMMA,
                 f"removed trailing comma(s) starting at {pos}", pos)
            if _valid_json(s):
                return s
    if depth >= 2:
        if (("{" in s or "," in s) and ":" in s):
            v, pos, found = _quote_bare_keys(s)
            if found:
                s = v
                _act(acts, ACTION_QUOTED_BARE_KEYS,
                     f"quoted bare key(s) starting at {pos}", pos)
                if _valid_json(s):
                    return s
        if "\n" in s or ";" in s or "\\n" in s:
            v, found = _merge_ndjson(s)
            if found:
                s = v
                _act(acts, ACTION_MERGED_NDJSON,
                     "merged newline-delimited JSON values into one array", 0)
                if _valid_json(s):
                    return s
        v, pos, found = _balance_truncated_json(s, depth)
        if found:
            s = v
            _act(acts, ACTION_REPAIRED_TRUNCATED_JSON,
                 f"closed truncated structure near {pos}", pos)
        if depth >= 3:
            v, pos, found = _quote_bare_values(s)
            if found:
                s = v
                _act(acts, ACTION_QUOTED_BARE_VALUES,
                     f"quoted bare value(s) starting at {pos}", pos)
                if _valid_json(s):
                    return s
            v, pos, found = _unwrap_stringified_json(s)
            if found:
                s = v
                _act(acts, ACTION_UNWRAPPED_STRINGIFIED,
                     f"inlined stringified JSON at {pos}", pos)
    return s


def _top_level_eq_index(seg):
    depth = 0
    in_dq = in_sq = False
    i = 0
    while i < len(seg):
        c = seg[i]
        if in_dq:
            if c == "\\":
                i += 1
            elif c == '"':
                in_dq = False
        elif in_sq:
            if c == "\\":
                i += 1
            elif c == "'":
                in_sq = False
        elif c == '"':
            in_dq = True
        elif c == "'":
            in_sq = True
        elif c in "([":
            depth += 1
        elif c in ")]":
            depth -= 1
        elif c == "=" and depth == 0:
            if i + 1 < len(seg) and seg[i + 1] == "=":
                return -1
            return i
        i += 1
    return -1


def _valid_ident(k):
    if not k or k[0].isdigit():
        return False
    return all(c.isascii() and (c.isalnum() or c in "_-") for c in k)


def _jstr(s):
    return json.dumps(s, ensure_ascii=False)


def _convert_arg_value(val):
    if not val:
        return None
    if val[0] == '"':
        try:
            return _jstr(json.loads(val))
        except ValueError:
            return val if _valid_json(val) else None
    if val[0] == "'":
        if len(val) >= 2 and val.endswith("'"):
            inner = val[1:-1].replace("\\'", "'")
            return _jstr(inner)
        return None
    if val[0] in "{[":
        return val if _valid_json(val) else None
    if val in ("true", "True"):
        return "true"
    if val in ("false", "False"):
        return "false"
    if val in ("null", "None"):
        return "null"
    stripped = val[1:] if val.startswith("-") else val
    if stripped.isdigit() or (val.count(".") == 1 and all(
            p.isdigit() for p in val.split(".")) and val[0].isdigit()):
        return val
    if _valid_ident(val):
        return _jstr(val)
    return None


def _split_call_args(body):
    segs, depth, in_dq, in_sq, start = [], 0, False, False, 0
    i = 0
    while i < len(body):
        c = body[i]
        if in_dq:
            if c == "\\":
                i += 1
            elif c == '"':
                in_dq = False
        elif in_sq:
            if c == "\\":
                i += 1
            elif c == "'":
                in_sq = False
        elif c == '"':
            in_dq = True
        elif c == "'":
            in_sq = True
        elif c in "([{":
            depth += 1
        elif c in ")]}":
            depth -= 1
        elif c == "," and depth == 0:
            segs.append(body[start:i])
            start = i + 1
        i += 1
    tail = body[start:]
    if tail.strip():
        segs.append(tail)
    return segs


def _try_convert_function_call(s, acts):
    t = s.strip()
    if not _FUNC_CALL_FULL_RE.match(t):
        return s, False
    open_idx = t.find("(")
    name = t[:open_idx].strip()
    if name.lower() in _FUNC_CALL_DENYLIST:
        return s, False
    close_idx = t.rfind(")")
    args_body = t[open_idx + 1:close_idx]

    parts = [_jstr(name)]
    for seg in _split_call_args(args_body):
        seg = seg.strip()
        if not seg:
            continue
        eq = _top_level_eq_index(seg)
        if eq < 0:
            return s, False
        key = seg[:eq].strip().strip("\"'")
        if not key or not _valid_ident(key):
            return s, False
        jv = _convert_arg_value(seg[eq + 1:].strip())
        if jv is None:
            return s, False
        parts.append(_jstr(key) + ":" + jv)
    out = '{"name":' + parts[0] + ',"arguments":{' + ",".join(parts[1:]) + "}}"
    if not _valid_json(out):
        return s, False
    _act(acts, ACTION_CONVERTED_FUNCTION_CALL,
         f"converted function call {name}(...) to tool-call JSON", 0)
    return out, True


def _extract_single_call_from_text(s, acts):
    t = s.strip()
    loc = _FUNC_CALL_ARGS_RE.search(t)
    if not loc:
        return s
    start = loc.start()
    abs_open = t.find("(", start)
    depth = 0
    in_dq = in_sq = False
    end = -1
    i = abs_open
    while i < len(t):
        c = t[i]
        if in_dq:
            if c == "\\":
                i += 1
            elif c == '"':
                in_dq = False
        elif in_sq:
            if c == "\\":
                i += 1
            elif c == "'":
                in_sq = False
        elif c == '"':
            in_dq = True
        elif c == "'":
            in_sq = True
        elif c in "([":
            depth += 1
        elif c in ")]}":
            depth -= 1
            if depth == 0 and c == ")":
                end = i + 1
                break
        i += 1
    if end < 0:
        return s
    candidate = t[start:end]
    call_name = t[start:abs_open].strip()
    args_body = candidate[abs_open - start + 1:len(candidate) - 1]
    if call_name.lower() in _FUNC_CALL_DENYLIST or _top_level_eq_index(args_body) < 0:
        return s
    conv, ok = _try_convert_function_call(candidate, acts)
    if not ok:
        return s
    if start > 0 and _has_letter(t[:start]):
        _act(acts, ACTION_REMOVED_PREAMBLE,
             f"removed prose before extracted function call ({start} chars)", 0)
    return conv


def _quote_bare_values(s):
    mask = _string_mask(s)
    out, first, i = [], -1, 0
    while i < len(s):
        c = s[i]
        if mask[i] or c != ":":
            out.append(c)
            i += 1
            continue
        out.append(c)
        j = _next_nonspace(s, i + 1)
        if j < 0 or not s[j].isalpha():
            i += 1
            continue
        k = j
        while k < len(s) and not mask[k] and s[k] not in ",}]\n":
            k += 1
        val_end = k
        while val_end > j and s[val_end - 1] in " \t":
            val_end -= 1
        run = s[j:val_end]
        if run == "" or run in ("true", "false", "null", "True", "False", "None"):
            i += 1
            continue
        numchk = run[1:] if run.startswith("-") else run
        if numchk.replace(".", "", 1).isdigit() and numchk != "":
            i += 1
            continue
        out.append(s[i + 1:j])
        out.append(_jstr(run))
        if first < 0:
            first = j
        i = val_end
    if first < 0:
        return s, -1, False
    return "".join(out), first, True


_STRINGIFIED_RE = re.compile(r'"(\{(?:[^"\\]|\\.)*\})"')


def _unwrap_stringified_json(s):
    first_pos = -1

    def repl(m):
        nonlocal first_pos
        tok = m.group(0)
        try:
            inner = json.loads(tok)
        except ValueError:
            return tok
        if isinstance(inner, str):
            stripped = inner.strip()
            if stripped.startswith(("{", "[")):
                try:
                    json.loads(stripped)
                except ValueError:
                    return tok
                if first_pos < 0:
                    first_pos = s.find(tok)
                return stripped
        return tok

    out = _STRINGIFIED_RE.sub(repl, s)
    if first_pos < 0 or out == s or not _valid_json(out):
        return s, -1, False
    return out, first_pos, True


def strip_orphan_close_tags(s, acts):
    out, stack = [], []
    dropped, first_pos, i = 0, -1, 0
    changed = False
    while i < len(s):
        c = s[i]
        if c != "<":
            out.append(c)
            i += 1
            continue
        rest = s[i:]
        if rest.startswith("</"):
            m = re.match(r"</\s*([A-Za-z][\w.\-:]*)\s*>", rest)
            if not m:
                out.append(c)
                i += 1
                continue
            name = m.group(1)
            found = False
            for k in range(len(stack) - 1, -1, -1):
                if stack[k] == name:
                    del stack[k:]
                    found = True
                    break
            if found:
                out.append(rest[:m.end()])
                i += m.end()
                continue
            if name.lower() in _BLEED_TAGS:
                dropped += 1
                if first_pos < 0:
                    first_pos = i
                changed = True
                i += m.end()
                continue
            out.append(rest[:m.end()])
            i += m.end()
            continue
        m = re.match(r"<([A-Za-z][\w.\-:]*)", rest)
        if not m:
            out.append(c)
            i += 1
            continue
        gt = rest.find(">")
        if gt < 0:
            out.append(c)
            i += 1
            continue
        inner = rest[1:gt].rstrip()
        out.append(rest[:gt + 1])
        if not inner.endswith("/"):
            stack.append(m.group(1))
        i += gt + 1
    if dropped:
        _act(acts, ACTION_FIXED_XML_ORPHAN_CLOSE,
             f"removed {dropped} orphan template tag(s) from text output", first_pos)
        return "".join(out)
    return s


# ---------------------------------------------------------------- normalizer

def normalize_output(s, acts):
    out = s
    if any(seq in out for seq, _ in _ESC_TARGETS):
        pos = min((out.find(seq) for seq, _ in _ESC_TARGETS if seq in out))
        for seq, rep in _ESC_TARGETS:
            out = out.replace(seq, rep)
        _act(acts, ACTION_NORMALIZED_UNICODE_ESC,
             f"decoded unicode escape(s) starting at {pos}", pos)
    if "\r" in out:
        pos = out.find("\r")
        out = out.replace("\r\n", "\n").replace("\r", "\n")
        _act(acts, ACTION_NORMALIZED_LINE_ENDINGS,
             f"normalized CRLF/CR to LF at {pos}", pos)
    out = _collapse_whitespace(out, acts)
    return out


def _collapse_whitespace(s, acts):
    mask = _string_mask(s)
    out = []
    nl_run = 0
    sp_start = -1
    prev_nl = True
    i = 0

    def flush_nl():
        nonlocal nl_run, prev_nl
        n = min(nl_run, 2)
        out.append("\n" * n)
        if n > 0:
            prev_nl = True
        nl_run = 0

    while i < len(s):
        c = s[i]
        if mask[i]:
            flush_nl()
            if sp_start >= 0:
                if prev_nl:
                    out.append(s[sp_start:i])
                else:
                    out.append(" ")
                sp_start = -1
            out.append(c)
            prev_nl = False
            i += 1
            continue
        if c == "\n":
            sp_start = -1
            nl_run += 1
            i += 1
        elif c in " \t":
            if sp_start < 0:
                sp_start = i
            i += 1
        else:
            flush_nl()
            if sp_start >= 0:
                if prev_nl:
                    out.append(s[sp_start:i])
                else:
                    out.append(" ")
                sp_start = -1
            prev_nl = False
            out.append(c)
            i += 1
    flush_nl()
    res = "".join(out).strip("\n")
    if res != s:
        _act(acts, ACTION_COLLAPSED_WHITESPACE, "collapsed excess whitespace", 0)
    return res


# ---------------------------------------------------------------- detector

def _detect(raw):
    lower = raw.lower()
    has_open_think = any(n in lower for n in ("<think", "<reasoning", "<reflection"))
    has_close_think = any(n in lower for n in ("</think", "</reasoning", "</reflection"))
    trimmed = raw.strip()
    full_call = bool(_FUNC_CALL_FULL_RE.match(trimmed))
    loose_call = (not full_call) and bool(_FUNC_CALL_ARGS_RE.search(raw))
    has_stringified = '"{' in raw
    has_tool = any(n in lower for n in ("<tool_call", "</tool_call",
                                        "<function_call", "</function_call"))
    has_tmpl = "<|" in raw
    has_fence = "```" in raw or "~~~" in raw
    has_box = any(c in _BOX_RUNES for c in raw)
    has_cr = "\r" in raw
    has_esc = any(seq in raw for seq, _ in _ESC_TARGETS)
    start = _first_structural_index(raw)
    has_preamble = start > 0 and _has_letter(raw[:start])
    valid = _valid_json(raw.strip())
    j = min((k for k in (raw.find("{"), raw.find("[")) if k >= 0), default=-1)
    x = _xml_tag_index(raw)
    json_intent = j >= 0 and (x < 0 or j < x) and not full_call
    xml_intent = (not json_intent) and x >= 0 and not full_call
    malformed = json_intent and not valid
    guess = MODEL_GENERIC
    if has_open_think:
        guess = MODEL_QWEN
    elif has_close_think:
        guess = MODEL_DEEPSEEK
    elif has_box:
        guess = MODEL_GLM
    return dict(has_artifact=any([has_open_think, has_close_think, has_tool,
                                  has_tmpl, has_fence, has_box, has_cr,
                                  has_esc, has_preamble,
                                  full_call, loose_call, has_stringified]),
                valid_json=valid, json_intent=json_intent,
                xml_intent=xml_intent, malformed=malformed, guess=guess,
                full_call=full_call)


# ---------------------------------------------------------------- public API

def process(inp: str, opts: Options | None = None) -> Result:
    opts = opts or Options()
    if not inp:
        return Result()
    info = _detect(inp)
    guess = opts.model_hint if opts.model_hint != MODEL_GENERIC else info["guess"]

    clean_fast = (not info["has_artifact"] and not info["malformed"]) and \
                 (info["valid_json"] or (not info["json_intent"] and not info["xml_intent"]))
    if clean_fast and opts.target_format == FORMAT_JSON and not info["valid_json"]:
        clean_fast = False
    if clean_fast and opts.target_format == FORMAT_XML:
        clean_fast = info["xml_intent"]
    if clean_fast:
        conf = 1.0 if info["valid_json"] else 0.0
        return Result(output=inp, cleaned=False, confidence=conf, model_guess=guess)

    acts: list = []
    out = inp
    if opts.strip_reasoning:
        out = strip_reasoning(out, acts)
    out = strip_tool_wrappers(out, acts)
    out = strip_fences(out, acts)
    out = strip_box_lines(out, acts)

    j = min((k for k in (out.find("{"), out.find("[")) if k >= 0), default=-1)
    x = _xml_tag_index(out)
    json_shape = j >= 0 and (x < 0 or j < x)
    xml_shape = x >= 0 and (j < 0 or x < j) and (
        out[x + 1].isalpha() or out[x + 1] in "?!")
    structured = (json_shape or xml_shape) and not info["full_call"]

    if opts.target_format != FORMAT_PLAIN_TEXT and structured:
        out = strip_preamble(out, acts)
        out = strip_tail(out, acts)
        ft = out.strip()
        if opts.repair_json and (ft.startswith("{") or ft.startswith("[")):
            out = json_repair(out, max(1, min(opts.max_repair_depth, 3)), acts)
    else:
        conv, ok = _try_convert_function_call(out.strip(), acts) \
            if _FUNC_CALL_FULL_RE.match(out.strip()) else (out, False)
        if ok:
            out = conv
        elif _FUNC_CALL_ARGS_RE.search(out.strip()):
            out = _extract_single_call_from_text(out, acts)
        out = strip_orphan_close_tags(out, acts)

    out = normalize_output(out, acts)
    final = out.strip()

    if not final and inp.strip():
        return Result(output=inp, cleaned=False, repairs=acts,
                      confidence=0.0, model_guess=guess,
                      error="unable to repair input into a valid target format")

    verified = (final.startswith(("{", "[")) and _valid_json(final))
    want_structured = (opts.target_format in (FORMAT_JSON, FORMAT_XML)) or \
                      final.startswith(("{", "[", "<"))
    if want_structured and not verified:
        return Result(output=inp, cleaned=False, repairs=acts,
                      confidence=0.0, model_guess=guess,
                      error="unable to repair input into a valid target format")

    return Result(output=out, cleaned=out != inp, repairs=acts,
                  confidence=1.0 if verified else 0.0, model_guess=guess)


def fix(inp: str, opts: Options | None = None) -> str:
    """One-liner: always returns a usable string (original on failure)."""
    return process(inp, opts).output
