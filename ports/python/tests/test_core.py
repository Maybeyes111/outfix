import glob
import json
import os
import unittest

from outfix import Options, fix, process


class SpecCases(unittest.TestCase):
    def test_orphan_close_think(self):
        out = fix('</think>\nHere is your JSON:\n{"result": 42}')
        self.assertEqual(out, '{"result": 42}')

    def test_think_preamble_fence(self):
        out = fix('<think>\nlet me think\n</think>\nSure!\n```json\n{"ok": true}\n```')
        self.assertEqual(out, '{"ok": true}')

    def test_xml_bleed(self):
        out = fix('{"code": "func main() {}"}</content>')
        self.assertEqual(out, '{"code": "func main() {}"}')

    def test_truncated_json(self):
        out = fix('{"items": [1, 2, 3')
        self.assertEqual(out, '{"items": [1, 2, 3]}')

    def test_truncated_nested(self):
        out = fix('{"a": [{"b": 1}, {"c": 2}')
        self.assertEqual(out, '{"a": [{"b": 1}, {"c": 2}]}')

    def test_python_dict(self):
        out = fix("{'active': True, 'data': None, 'flag': False}")
        self.assertEqual(out, '{"active": true, "data": null, "flag": false}')

    def test_box_drawing(self):
        out = fix('╭──────╮\n│ result │\n╰──────╯\n{"val": 1}')
        self.assertEqual(out, '{"val": 1}')

    def test_clean_passthrough(self):
        res = process('{"ok": true}')
        self.assertFalse(res.cleaned)
        self.assertEqual(len(res.repairs), 0)
        self.assertEqual(res.confidence, 1.0)

    def test_tool_call_unwrap(self):
        out = fix('<tool_call>\n{"name": "get_weather", "arguments": {"city": "Jakarta"}}\n</tool_call>')
        self.assertEqual(out, '{"name": "get_weather", "arguments": {"city": "Jakarta"}}')

    def test_multiple_tool_calls_merge(self):
        out = fix('<tool_call>{"name": "a"}</tool_call>\n<tool_call>{"name": "b"}</tool_call>')
        json.loads(out)  # valid
        self.assertTrue(out.startswith('[{'), out)

    def test_chat_template_bleed(self):
        out = fix('<|im_start|>assistant\n{"v": 2}<|im_end|>')
        self.assertEqual(out, '{"v": 2}')

    def test_literal_escape_ndjson(self):
        sep = chr(92) + "n"
        out = fix(f'{{"a": 1}}{sep}{{"b": 2}}')
        self.assertEqual(out, '[{"a": 1},{"b": 2}]')

    def test_text_with_template_bleed(self):
        res = process("Here is your answer.\nHope this helps!</assistant_response>")
        self.assertEqual(res.output, "Here is your answer.\nHope this helps!")
        self.assertIsNone(res.error)

    def test_python_code_bleed(self):
        res = process("def hi():\n    print('ok')\n</content>")
        self.assertEqual(res.output, "def hi():\n    print('ok')")

    def test_forced_json_on_prose_fails(self):
        res = process("hello world nothing structural",
                      Options(target_format=1))
        self.assertIsNotNone(res.error)
        self.assertEqual(res.output, "hello world nothing structural")

    def test_plain_text_mode(self):
        from outfix.core import FORMAT_PLAIN_TEXT
        res = process("<think>secret</think>The answer is forty two.",
                      Options(target_format=FORMAT_PLAIN_TEXT))
        self.assertEqual(res.output, "The answer is forty two.")

    def test_empty(self):
        self.assertEqual(fix(""), "")

    def test_never_panic_nasty_inputs(self):
        nasty = ["<think>", "</think>", "```", "<<<>>>", "}{", "[''",
                 "\x00\xff", "╭╮╰╯", '{"a":', "'unclosed",
                 "<think>" * 50, "[" * 200]
        for i, s in enumerate(nasty):
            try:
                res = process(s)
                if s:
                    self.assertTrue(res.output.strip() or res.error)
            except Exception as e:  # pragma: no cover
                self.fail(f"exception on input {i}: {e}")

    def test_function_call_full(self):
        from outfix.core import ACTION_CONVERTED_FUNCTION_CALL
        import outfix as pkg
        res = process('get_weather(city="Jakarta", units=\'metric\', days=3, verbose=True)')
        self.assertIsNone(res.error)
        self.assertEqual(
            res.output,
            '{"name":"get_weather","arguments":{"city":"Jakarta","units":"metric","days":3,"verbose":true}}')
        self.assertTrue(any(a.type == ACTION_CONVERTED_FUNCTION_CALL for a in res.repairs))

    def test_function_call_empty_args(self):
        self.assertEqual(fix("list_tools()"),
                         '{"name":"list_tools","arguments":{}}')

    def test_function_call_in_prose(self):
        out = fix('Sure! Here is the call:\nget_weather(city="Jakarta")')
        self.assertEqual(out, '{"name":"get_weather","arguments":{"city":"Jakarta"}}')

    def test_nested_dict_arg(self):
        out = fix('create(filter={"status": "open"}, limit=5)')
        json.loads(out)
        self.assertIn('"limit":5', out)

    def test_bare_values_depth3(self):
        import outfix as pkg
        out = fix('{"city": Jakarta, "zip": 12345}',
                  pkg.Options(max_repair_depth=3))
        self.assertEqual(out, '{"city": "Jakarta", "zip": 12345}')

    def test_stringified_json_unwrap(self):
        import outfix as pkg
        raw = '{"arguments": "{\\"city\\": \\"Jakarta\\"}", "n": 1}'
        out = fix(raw, pkg.Options(max_repair_depth=3))
        parsed = json.loads(out)
        self.assertEqual(parsed["arguments"]["city"], "Jakarta")
        self.assertEqual(parsed["n"], 1)

    def test_real_object_arg_call(self):
        out = fix('get_weather({"units": "imperial", "days": 30, "city": "Tokyo"})')
        parsed = json.loads(out)
        self.assertEqual(parsed["name"], "get_weather")
        self.assertEqual(parsed["arguments"]["city"], "Tokyo")

    def test_real_xml_attr_call(self):
        raw = '<create_ticket tags="["alert","alert"]" urgent="false" title="weekly-report"/>'
        res = process(raw)
        self.assertIsNone(res.error)
        parsed = json.loads(res.output)
        self.assertEqual(parsed["name"], "create_ticket")
        self.assertEqual(parsed["arguments"]["urgent"], False)
        self.assertEqual(parsed["arguments"]["title"], "weekly-report")

    def test_real_with_lines(self):
        raw = ('book_hotel with city="Surabaya", nights=30, rating=1.7\n'
               'get_weather with city="Bandung", units="metric", days=3')
        res = process(raw)
        arr = json.loads(res.output)
        self.assertEqual(len(arr), 2)
        self.assertEqual(arr[0]["name"], "book_hotel")
        self.assertEqual(arr[1]["name"], "get_weather")


class LiveCorpus(unittest.TestCase):
    def test_corpus(self):
        base = os.path.join(os.path.dirname(__file__),
                            "..", "..", "..", "testdata", "live")
        files = sorted(glob.glob(os.path.join(base, "*.raw.txt")))
        if not files:
            self.skipTest("no live corpus")
        for f in files:
            with self.subTest(os.path.basename(f)):
                raw = open(f, encoding="utf-8", errors="replace").read()
                res = process(raw)
                self.assertIsNone(res.error, f"{f}: {res.error}")
                self.assertTrue(json.loads(res.output), f)


if __name__ == "__main__":
    unittest.main()
