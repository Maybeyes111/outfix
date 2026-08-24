# outfix

[![CI](https://github.com/Maybeyes111/outfix/actions/workflows/ci.yml/badge.svg)](https://github.com/Maybeyes111/outfix/actions/workflows/ci.yml)

**Clean malformed / polluted LLM output before it hits your application logic.**

`outfix` is a Go library that repairs the messy things language models —
especially Chinese open models like **Qwen, DeepSeek, and GLM** — wrap around
(or mangle inside) their answers: reasoning blocks, code fences, tool-call
wrappers, chat-template bleed, chatty preambles/postambles, box-drawing
decoration, Python literals, truncated JSON, and more.

Zero external dependencies. Stdlib only. Thread-safe. Never panics.

---

## ⚠️ Status: early & honest

**This project works, but it is far from perfect.**

Real-world tested so far:

| Model | Route | Result |
|---|---|---|
| DeepSeek V4 Pro | 9router (`xkiro/...`) | **8/8 prompts pass** (4 clean, 4 repaired) |
| DeepSeek V4 Flash | 9router (`xkiro/...`) | **8/8 prompts pass** (5 clean, 3 repaired) |
| Qwen 3.8 Max | 9router | *pending* — upstream returned 503 during testing |
| GLM | — | not tested yet |

Real failing outputs captured from those runs live in `testdata/live/` and are
enforced as regression tests (`TestLiveCorpus`). Everything else is still
synthetic. Heuristics can misfire on unusual-but-valid content, and the public
API may change before v1.0.

If you run this against a real model and something breaks (or cleans when it
shouldn't), **please [open an issue](../../issues/new) with the raw input** —
real-world failing samples are the single most valuable contribution right now.

> Catatan (Bahasa Indonesia): proyek ini belum sempurna. Sudah diuji ke
> DeepSeek V4 (pro/flash) lewat 9router dengan hasil 16/16 pass, tapi Qwen
> belum (provider lagi down saat pengujian) dan GLM belum sama sekali. Kalau
> kamu menemukan output aneh yang gagal dibersihkan, tolong laporkan ke issue
> ya — sangat membantu. Terima kasih!

---

## Universal: use it from anywhere

| Language | How | Status |
|---|---|---|
| **Go** | `go get github.com/Maybeyes111/outfix` | reference implementation |
| **Python** | `pip install` from `ports/python/` (or copy the package) | ported, 25 tests pass |
| **JavaScript / Node** | `ports/javascript/outfix.mjs` (ESM, zero deps) | ported, 25 tests pass |
| **Any language (C ABI)** | `go build -buildmode=c-shared -o outfix.so ./capi` | planned |
| **Browser / WASM** | `GOOS=js GOARCH=wasm go build -o outfix.wasm .` | core builds clean |

The Python and JavaScript ports are line-for-line ports of the Go pipeline and
are validated against the same live-model corpus (`testdata/live/`). Action
type strings are identical across languages, so audit trails match.

## Cleaning proxy for chat gateways (Hermes Agent, Telegram bots)

Chat gateways like [Hermes Agent](https://github.com/NousResearch/hermes-agent)
(Telegram/Discord messaging gateway) speak the OpenAI-compatible protocol.
Instead of patching every bot, run **outfix-proxy** between your gateway and
any OpenAI-compatible provider — every assistant message gets cleaned before
it reaches the chat:

```bash
go install github.com/Maybeyes111/outfix/cmd/outfix-proxy@latest

# sit between Hermes and your router/provider
outfix-proxy -listen :8643 \
             -upstream http://localhost:20128/v1 \
             -model deepseek -verbose
```

Then point your gateway's provider at the proxy instead of the upstream:

```yaml
# ~/.hermes/config.yaml (Hermes Agent)
providers:
  custom:
    base_url: http://127.0.0.1:8643/v1
    api_key_env: ROUTER_API_KEY
```

Telegram users now see `{"result": 42}` instead of
`</think>Sure!\n```json\n{"result": 42}`. Non-streaming JSON responses are
cleaned; streaming (SSE) responses pass through untouched for now.

## Install

```bash
go get github.com/Maybeyes111/outfix
```

Requires Go 1.21+.

## Quick start

```go
clean, err := outfix.Fix(raw)
```

Input:

```
</think>
Here is your JSON:
{"result": 42}
```

Output:

```json
{"result": 42}
```

## Full control

```go
proc := outfix.New(outfix.Options{
    TargetFormat:   outfix.FormatJSON,
    StripReasoning: true,
    RepairJSON:     true,
    ModelHint:      outfix.ModelQwen,
})

res, err := proc.Process(raw)
if res.Cleaned {
    log.Printf("outfix repairs: %v", res.Repairs)
}
json.Unmarshal([]byte(res.Output), &v)
```

### Options

| Field | Meaning |
|---|---|
| `TargetFormat` | `FormatAuto`, `FormatJSON`, `FormatXML`, `FormatPlainText` |
| `StripReasoning` | remove `<think>` / `<reasoning>` / `<reflection>` blocks |
| `RepairJSON` | fix malformed JSON |
| `RepairXML` | fix malformed XML |
| `ModelHint` | skip detection, force rules for a model family |
| `MaxRepairDepth` | 1 = conservative, 2 = moderate (default), 3 = aggressive |

### Result

| Field | Meaning |
|---|---|
| `Output` | cleaned text (original input if repair failed — never empty) |
| `Cleaned` | true if anything changed |
| `Repairs` | audit trail: what was changed and where |
| `Confidence` | 1.0 = output verified valid JSON/XML, 0.0 = could not verify |
| `ModelGuess` | detected model family (`Qwen`, `DeepSeek`, ...) |

## What it cleans

| Artifact | Example | Action type |
|---|---|---|
| Reasoning blocks | `<think>...</think>` | `stripped_think_block` |
| Orphan close tag | leading `</think>` | `stripped_orphan_close_tag` |
| Tool-call wrappers | `<tool_call>{...}</tool_call>` | `unwrapped_tool_call` |
| Chat template bleed | `<\|im_start\|>assistant` | `stripped_chat_template` |
| Code fences | ```` ```json ... ``` ```` / `~~~` | `stripped_code_fence` |
| Preamble | `Sure! Here is your JSON:` | `removed_preamble` |
| Postamble | `Let me know if you need more!` | `removed_postamble` |
| XML bleed into text/JSON | trailing `</content>` | `stripped_xml_bleed` / `fixed_xml_orphan_close` |
| Box-drawing noise | `╭───╮ │ result │ ╰───╯` | `stripped_box_drawing` |
| Python literals | `{'a': True, 'b': None}` | `fixed_python_literals` |
| Single quotes | `{'city': "Jakarta"}` | `fixed_single_quotes` |
| Trailing commas | `[1, 2,]` | `fixed_trailing_comma` |
| Bare keys | `{name: "x"}` | `quoted_bare_keys` |
| Truncated JSON | `{"items": [1, 2` | `repaired_truncated_json` |
| NDJSON | two objects on two lines | `merged_ndjson` |
| Function-call written as code | `get_weather(city="X")` / `get_weather({...})` | `converted_function_call` |
| XML-attribute tool call | `<create_ticket title="x" urgent="false"/>` | `converted_function_call` |
| Prose-style calls | `book_hotel with city="X", nights=2` (one per line → array) | `converted_function_call` |
| Wrong param types | `{city: Jakarta}` bare values | `quoted_bare_values` (depth 3) |
| Stringified JSON args | `"arguments": "{...}"` as string | `unwrapped_stringified_json` (depth 3) |
| Unicode escapes | `\u003c` → `<` | `normalized_unicode_escapes` |
| CRLF / excess whitespace | `\r\n`, blank-line runs | `normalized_line_endings` |

Multiple sequential `<tool_call>` blocks are merged into one JSON array.

## CLI

```bash
go install github.com/Maybeyes111/outfix/cmd/outfix@latest

# pipe from anywhere
ollama run qwen2.5 "give me weather json" | outfix -verbose -model qwen

# from a saved response file
outfix -verbose -f response.txt

# strict JSON target, aggressive repairs
outfix -format json -depth 3 -f response.txt
```

Cleaned output goes to stdout; audit trail goes to stderr.

## Design guarantees

- **Never panics** — internal recovery returns the original input.
- **Never empty** — non-empty input never produces empty output.
- **Fallback** — if verification fails after repairs, you get the original
  input back with `ErrRepairFailed`, plus the audit trail of what was tried.
- **Thread-safe** — a `Processor` holds no mutable state; share it freely.
- **Fast** — full pipeline runs well under 1 ms for inputs under 4 KB
  (see `BenchmarkProcess4KB`).

## Limitations

- Heuristic string surgery, not a parser-grade transformer. Exotic edge cases
  exist (deeply nested unterminated strings, pathological quote soup).
- Model detection is intentionally simple: `<think>`-family tags map to
  Qwen/DeepSeek heuristics; GLM is currently indistinguishable from generic.
- XML repair targets fragments and common bleed patterns, not full documents
  with namespaces/DTD validation.

## Contributing

Failing raw outputs (as issue reports or test cases) are worth more than code
right now. If you contribute code, keep the zero-dependency rule and add
table-driven tests.

## Radical fuzzing (the dumbest AI we could build)

`chaos/` contains a hand-written x86-64 assembly garbage generator
(xorshift64* PRNG + raw binary character tables) that emits maximally
polluted "LLM" output. `cmd/radical-ai` feeds 50,000 of those through the
pipeline and enforces hard invariants:

- never panics (zero escaped panics across the full run)
- output is never empty for non-empty input; failures return the original
- `Confidence == 1.0` implies byte-verified valid JSON

The same corpus is replayed through the Python and JavaScript ports — all
three implementations must produce **byte-identical** output:

```bash
go run ./cmd/radical-ai -n 50000        # invariants + corpus generation
python ports/python/radical_check.py    # python: 300/300 identical to Go
node ports/javascript/radical_check.mjs # js:      300/300 identical to Go
```

## FAQ (things people actually ask)

**How do I remove `<think>` reasoning blocks from Qwen / DeepSeek output?**
`outfix.Fix(raw)` strips paired, nested, case-variant, and orphan `<think>` /
`<reasoning>` / `<reflection>` blocks automatically.

**The model hit max_tokens and my JSON is cut off. Can it be repaired?**
Yes — `repaired_truncated_json` closes unterminated strings, arrays, objects,
and even completes partial literals (`tru` → `true`) at depth 3.

**The model wrote a function call as text (`get_weather(city="X")`) instead of
calling the tool.**
outfix converts prose/Python-style invocations, object-argument calls,
XML-attribute calls (`<tool k="v"/>`), and `name with key=value` lines into
standard tool-call JSON — single call or array.

**My JSON is valid but arguments arrived as a string.**
At depth 3, stringified JSON values (`"arguments": "{...}"`) are inlined into
real objects.

**Chinese models return Python dicts with single quotes and `True/False/None`.**
Handled by `fixed_single_quotes` + `fixed_python_literals`.

**There's `</content>` junk after an otherwise good answer.**
Orphan template tags are removed from JSON tails *and* from plain-text/code
answers without touching surrounding content.

**I just want a CLI.**
`echo '<think>hmm</think>{"a":1}' | outfix -verbose`

---

## License

[MIT](LICENSE)
