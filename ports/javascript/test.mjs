import { strict as assert } from "node:assert";
import test from "node:test";
import { readFileSync, readdirSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { fix, process, Options, Format } from "./outfix.mjs";

test("orphan close think tag", () => {
  assert.equal(fix('</think>\nHere is your JSON:\n{"result": 42}'), '{"result": 42}');
});

test("think block + preamble + fence", () => {
  assert.equal(
    fix('<think>\nlet me think\n</think>\nSure!\n```json\n{"ok": true}\n```'),
    '{"ok": true}'
  );
});

test("xml bleed after json", () => {
  assert.equal(fix('{"code": "func main() {}"}</content>'), '{"code": "func main() {}"}');
});

test("truncated json", () => {
  assert.equal(fix('{"items": [1, 2, 3'), '{"items": [1, 2, 3]}');
});

test("truncated nested json", () => {
  assert.equal(fix('{"a": [{"b": 1}, {"c": 2}'), '{"a": [{"b": 1}, {"c": 2}]}');
});

test("python dict", () => {
  assert.equal(
    fix("{'active': True, 'data': None, 'flag': False}"),
    '{"active": true, "data": null, "flag": false}'
  );
});

test("box drawing noise", () => {
  assert.equal(fix('╭──────╮\n│ result │\n╰──────╯\n{"val": 1}'), '{"val": 1}');
});

test("clean passthrough", () => {
  const res = process('{"ok": true}');
  assert.equal(res.cleaned, false);
  assert.equal(res.repairs.length, 0);
  assert.equal(res.confidence, 1.0);
});

test("tool call unwrap", () => {
  assert.equal(
    fix('<tool_call>\n{"name": "get_weather", "arguments": {"city": "Jakarta"}}\n</tool_call>'),
    '{"name": "get_weather", "arguments": {"city": "Jakarta"}}'
  );
});

test("multiple tool calls merge to array", () => {
  const out = fix('<tool_call>{"name": "a"}</tool_call>\n<tool_call>{"name": "b"}</tool_call>');
  JSON.parse(out);
  assert.ok(out.startsWith("[{"), out);
});

test("chat template bleed", () => {
  assert.equal(fix('<|im_start|>assistant\n{"v": 2}<|im_end|>'), '{"v": 2}');
});

test("literal escape ndjson", () => {
  const sep = String.fromCharCode(92) + "n";
  assert.equal(fix(`{"a": 1}${sep}{"b": 2}`), '[{"a": 1},{"b": 2}]');
});

test("text with template bleed", () => {
  const res = process("Here is your answer.\nHope this helps!</assistant_response>");
  assert.equal(res.output, "Here is your answer.\nHope this helps!");
  assert.equal(res.error, null);
});

test("python code bleed kept intact", () => {
  const res = process("def hi():\n    print('ok')\n</content>");
  assert.equal(res.output, "def hi():\n    print('ok')");
});

test("forced json on prose fails with original", () => {
  const res = process("hello world nothing structural", new Options({ targetFormat: Format.JSON }));
  assert.ok(res.error);
  assert.equal(res.output, "hello world nothing structural");
});

test("plain text mode strips reasoning only", () => {
  const res = process("<think>secret</think>The answer is forty two.",
    new Options({ targetFormat: Format.PLAIN_TEXT }));
  assert.equal(res.output, "The answer is forty two.");
});

test("empty input", () => {
  assert.equal(fix(""), "");
});

test("never throws on nasty inputs", () => {
  const nasty = ["<think>", "</think>", "```", "<<<>>>", "}{", "[''",
    "\x00ÿ", "╭╮╰╯", '{"a":', "'unclosed", "<think>".repeat(50), "[".repeat(200)];
  for (const s of nasty) {
    const res = process(s);
    if (s) assert.ok(res.output.trim().length > 0 || res.error, JSON.stringify(s));
  }
});

test("function call conversion", () => {
  assert.equal(
    fix(`get_weather(city="Jakarta", units='metric', days=3, verbose=True)`),
    '{"name":"get_weather","arguments":{"city":"Jakarta","units":"metric","days":3,"verbose":true}}'
  );
});

test("empty args call", () => {
  assert.equal(fix("list_tools()"), '{"name":"list_tools","arguments":{}}');
});

test("call inside prose", () => {
  assert.equal(
    fix('Sure! Here is the call:\nget_weather(city="Jakarta")'),
    '{"name":"get_weather","arguments":{"city":"Jakarta"}}'
  );
});

test("nested dict arg survives", () => {
  const out = fix('create(filter={"status": "open"}, limit=5)');
  JSON.parse(out);
  assert.ok(out.includes('"limit":5'), out);
});

test("bare values quoted at depth 3", () => {
  const out = fix('{"city": Jakarta, "zip": 12345}',
    new Options({ maxRepairDepth: 3 }));
  assert.equal(out, '{"city": "Jakarta", "zip": 12345}');
});

test("stringified json unwrapped at depth 3", () => {
  const raw = '{"arguments": "{\\"city\\": \\"Jakarta\\"}", "n": 1}';
  const parsed = JSON.parse(fix(raw, new Options({ maxRepairDepth: 3 })));
  assert.equal(parsed.arguments.city, "Jakarta");
  assert.equal(parsed.n, 1);
});

test("live corpus all valid json", () => {
  const base = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "testdata", "live");
  let files;
  try {
    files = readdirSync(base).filter((f) => f.endsWith(".raw.txt"));
  } catch {
    files = [];
  }
  if (!files.length) return; // corpus not present
  for (const f of files) {
    const raw = readFileSync(join(base, f), "utf8");
    const res = process(raw);
    assert.equal(res.error, null, `${f}: ${res.error}`);
    assert.doesNotThrow(() => JSON.parse(res.output), `${f}`);
  }
});
