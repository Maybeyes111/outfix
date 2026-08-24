package outfix

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func hasAction(acts []RepairAction, typ string) bool {
	for _, a := range acts {
		if a.Type == typ {
			return true
		}
	}
	return false
}

func defaultProc() *Processor {
	return New(defaultFixOptions())
}

type tc struct {
	name    string
	input   string
	want    string
	wantErr bool
	cleaned *bool
	actions []string
	confOne bool
	guess   ModelFamily
}

func TestSpecCases(t *testing.T) {
	cleanedTrue := true
	cleanedFalse := false

	cases := []tc{
		{
			name:    "orphan close think tag",
			input:   "</think>\nHere is your JSON:\n{\"result\": 42}",
			want:    `{"result": 42}`,
			cleaned: &cleanedTrue,
			actions: []string{ActionStrippedOrphanCloseTag, ActionRemovedPreamble},
			confOne: true,
			guess:   ModelDeepSeek,
		},
		{
			name:    "full think block + preamble + code fence",
			input:   "<think>\nlet me think\n</think>\nSure!\n```json\n{\"ok\": true}\n```",
			want:    `{"ok": true}`,
			cleaned: &cleanedTrue,
			actions: []string{ActionStrippedThinkBlock, ActionStrippedCodeFence, ActionRemovedPreamble},
			confOne: true,
			guess:   ModelQwen,
		},
		{
			name:    "XML bleed from Go agent prompt template",
			input:   `{"code": "func main() {}"}</content>`,
			want:    `{"code": "func main() {}"}`,
			cleaned: &cleanedTrue,
			actions: []string{ActionStrippedXMLBleed},
			confOne: true,
		},
		{
			name:    "truncated JSON",
			input:   `{"items": [1, 2, 3`,
			want:    `{"items": [1, 2, 3]}`,
			cleaned: &cleanedTrue,
			actions: []string{ActionRepairedTruncatedJSON},
			confOne: true,
		},
		{
			name:    "python literals + single quotes",
			input:   "{'active': True, 'data': None, 'flag': False}",
			want:    `{"active": true, "data": null, "flag": false}`,
			cleaned: &cleanedTrue,
			actions: []string{ActionFixedPythonLiterals, ActionFixedSingleQuotes},
			confOne: true,
		},
		{
			name:    "box drawing noise",
			input:   "╭──────╮\n│ result │\n╰──────╯\n{\"val\": 1}",
			want:    `{"val": 1}`,
			cleaned: &cleanedTrue,
			actions: []string{ActionStrippedBoxDrawing, ActionRemovedPreamble},
			confOne: true,
		},
		{
			name:    "clean input fast path",
			input:   `{"ok": true}`,
			want:    `{"ok": true}`,
			cleaned: &cleanedFalse,
			actions: []string{},
			confOne: true,
		},
	}
	runTable(t, defaultProc(), cases)
}

func TestThinkVariants(t *testing.T) {
	cases := []tc{
		{
			name:    "uppercase think tags",
			input:   "<THINK>\nreasoning\n</THINK>\n{\"a\": 1}",
			want:    `{"a": 1}`,
			actions: []string{ActionStrippedThinkBlock},
			confOne: true,
		},
		{
			name:    "mixed case reasoning tag",
			input:   "<Reasoning>x</Reasoning>{\"a\": 1}",
			want:    `{"a": 1}`,
			actions: []string{ActionStrippedThinkBlock},
			confOne: true,
		},
		{
			name:    "nested think tags",
			input:   "<think>a<think>b</think>c</think>{\"n\": 1}",
			want:    `{"n": 1}`,
			actions: []string{ActionStrippedThinkBlock},
			confOne: true,
		},
		{
			name:    "unclosed think with payload after",
			input:   "<think>partial reasoning {\"kept\": 1}",
			want:    `{"kept": 1}`,
			actions: []string{ActionStrippedThinkBlock},
			confOne: true,
		},
	}
	runTable(t, defaultProc(), cases)
}

func TestToolCallWrappers(t *testing.T) {
	cleanedTrue := true
	cases := []tc{
		{
			name:    "qwen single tool call unwrapped",
			input:   "<tool_call>\n{\"name\": \"get_weather\", \"arguments\": {\"city\": \"Jakarta\"}}\n</tool_call>",
			want:    `{"name": "get_weather", "arguments": {"city": "Jakarta"}}`,
			cleaned: &cleanedTrue,
			actions: []string{ActionUnwrappedToolCall},
			confOne: true,
		},
		{
			name:    "multiple tool calls merged into array",
			input:   `<tool_call>{"name": "a", "arguments": {"x": 1}}</tool_call>` + "\n" + `<tool_call>{"name": "b", "arguments": {"y": 2}}</tool_call>`,
			want:    `[{"name": "a", "arguments": {"x": 1}},{"name": "b", "arguments": {"y": 2}}]`,
			cleaned: &cleanedTrue,
			actions: []string{ActionUnwrappedToolCall, ActionMergedNDJSON},
			confOne: true,
		},
		{
			name:    "orphan close tag after json bleed",
			input:   `{"ok": true}</tool_call>`,
			want:    `{"ok": true}`,
			cleaned: &cleanedTrue,
			actions: []string{ActionUnwrappedToolCall},
			confOne: true,
		},
		{
			name:    "fenced payload inside wrapper",
			input:   "<function_call>\n```json\n{\"q\": 1}\n```\n</function_call>",
			want:    `{"q": 1}`,
			cleaned: &cleanedTrue,
			actions: []string{ActionUnwrappedToolCall, ActionStrippedCodeFence},
			confOne: true,
		},
		{
			name:    "chat template bleed stripped",
			input:   "<|im_start|>assistant\n{\"v\": 2}<|im_end|>",
			want:    `{"v": 2}`,
			cleaned: &cleanedTrue,
			actions: []string{ActionStrippedChatTemplate},
			confOne: true,
		},
		{
			name:    "wrapper + think block combined",
			input:   "<think>reasoning</think><|im_start|>assistant<tool_call>{\"z\": true}</tool_call>",
			want:    `{"z": true}`,
			cleaned: &cleanedTrue,
			actions: []string{ActionStrippedThinkBlock, ActionUnwrappedToolCall, ActionStrippedChatTemplate},
			confOne: true,
		},
	}
	runTable(t, defaultProc(), cases)
}

func TestXMLBleedInTextOutput(t *testing.T) {
	cleanedTrue := true
	p := defaultProc()

	t.Run("plain text with trailing template bleed", func(t *testing.T) {
		in := "Here is your answer.\nHope this helps!</assistant_response>"
		res, err := p.Process(in)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if res.Output != "Here is your answer.\nHope this helps!" {
			t.Fatalf("got %q", res.Output)
		}
		if !hasAction(res.Repairs, ActionFixedXMLOrphanClose) {
			t.Fatalf("missing %s in %+v", ActionFixedXMLOrphanClose, res.Repairs)
		}
	})

	t.Run("python code with content bleed kept intact", func(t *testing.T) {
		in := "def hi():\n    print('ok')\n</content>"
		res, err := p.Process(in)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		want := "def hi():\n    print('ok')"
		if res.Output != want {
			t.Fatalf("got %q want %q", res.Output, want)
		}
		if !res.Cleaned || res.Confidence != 0 {
			t.Fatalf("cleaned=%v conf=%v", res.Cleaned, res.Confidence)
		}
	})

	t.Run("legit html example untouched", func(t *testing.T) {
		in := "<div class=\"a\">x</div>\nEnjoy!"
		res, err := p.Process(in)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if res.Output != in || res.Cleaned {
			t.Fatalf("got %q cleaned=%v", res.Output, res.Cleaned)
		}
	})

	t.Run("plain text mode still cleans bleed tags", func(t *testing.T) {
		pt := New(Options{TargetFormat: FormatPlainText, StripReasoning: true})
		res, err := pt.Process("<think>r</think>answer text</prompt> more")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if res.Output != "answer text more" {
			t.Fatalf("got %q", res.Output)
		}
	})

	runTable(t, p, []tc{
		{
			name:    "bleed tag at start of text",
			input:   "</content>The summary goes here",
			want:    "The summary goes here",
			cleaned: &cleanedTrue,
			actions: []string{ActionFixedXMLOrphanClose},
		},
	})
}

func TestJSONRepairs(t *testing.T) {
	cases := []tc{
		{
			name:    "bare keys quoted",
			input:   "{name: \"x\", age: 30}",
			want:    `{"name": "x", "age": 30}`,
			actions: []string{ActionQuotedBareKeys},
			confOne: true,
		},
		{
			name:    "trailing comma removed",
			input:   `{"a": 1, "b": [2, 3,],}`,
			want:    `{"a": 1, "b": [2, 3]}`,
			actions: []string{ActionFixedTrailingComma},
			confOne: true,
		},
		{
			name:    "ndjson merged into array",
			input:   "{\"a\": 1}\n{\"b\": 2}",
			want:    `[{"a": 1},{"b": 2}]`,
			actions: []string{ActionMergedNDJSON},
			confOne: true,
		},
		{
			name:    "dangling colon completed at depth 2",
			input:   `{"a":`,
			want:    `{"a":null}`,
			actions: []string{ActionRepairedTruncatedJSON},
			confOne: true,
		},
		{
			name:    "unterminated string closed",
			input:   `{"msg": "hello`,
			want:    `{"msg": "hello"}`,
			actions: []string{ActionRepairedTruncatedJSON},
			confOne: true,
		},
		{
			name:    "postamble removed",
			input:   "{\"a\": 1}\nLet me know if you need more!",
			want:    `{"a": 1}`,
			actions: []string{ActionRemovedPostamble},
			confOne: true,
		},
		{
			name:    "apostrophe in prose not corrupted",
			input:   "{'v': 'it\\'s fine', 'w': \"ok's\"}",
			want:    `{"v": "it's fine", "w": "ok's"}`,
			actions: []string{ActionFixedSingleQuotes},
			confOne: true,
		},
	}
	runTable(t, defaultProc(), cases)
}

func TestXMLRepairs(t *testing.T) {
	cases := []tc{
		{
			name:    "mismatched close synthesized",
			input:   "<root><a>x</root>",
			want:    "<root><a>x</a></root>",
			actions: []string{ActionFixedMismatchedXMLTags},
			confOne: true,
		},
		{
			name:    "orphan closes dropped",
			input:   "<p>hi</p></content></system>",
			want:    "<p>hi</p>",
			actions: []string{ActionFixedXMLOrphanClose},
			confOne: true,
		},
		{
			name:    "unclosed tags appended",
			input:   "<root><item>x",
			want:    "<root><item>x</item></root>",
			actions: []string{ActionClosedUnclosedXMLTags},
			confOne: true,
		},
		{
			name:  "clean xml passthrough",
			input: "<root><a>1</a></root>",
			want:  "<root><a>1</a></root>",
		},
	}
	runTable(t, defaultProc(), cases)
}

func TestNormalization(t *testing.T) {
	cases := []tc{
		{
			name:    "unicode escapes decoded",
			input:   `{"html": "\u003cb\u003e hi"}`,
			want:    `{"html": "<b> hi"}`,
			actions: []string{ActionNormalizedUnicodeEsc},
			confOne: true,
		},
		{
			name:    "CRLF normalized",
			input:   "Sure!\r\n{\"ok\": true}\r\n",
			want:    `{"ok": true}`,
			actions: []string{ActionNormalizedLineEndings},
			confOne: true,
		},
		{
			name:    "tilde fence stripped",
			input:   "~~~\n{\"t\": 1}\n~~~",
			want:    `{"t": 1}`,
			actions: []string{ActionStrippedCodeFence},
			confOne: true,
		},
		{
			name:    "excess blank lines collapsed",
			input:   "{\"a\": 1}\n\n\n\n\nLet me know!",
			want:    `{"a": 1}`,
			actions: []string{ActionRemovedPostamble},
			confOne: true,
		},
	}
	runTable(t, defaultProc(), cases)
}

func TestFallbackAndModes(t *testing.T) {
	p := New(Options{TargetFormat: FormatJSON, StripReasoning: true, RepairJSON: true, MaxRepairDepth: 2})
	res, err := p.Process("hello world nothing structural")
	if err == nil {
		t.Fatalf("expected ErrRepairFailed for forced JSON on prose")
	}
	if !errors.Is(err, ErrRepairFailed) {
		t.Fatalf("wrong error: %v", err)
	}
	if res.Output != "hello world nothing structural" {
		t.Fatalf("original input not preserved on failure: %q", res.Output)
	}
	if res.Confidence != 0 {
		t.Fatalf("confidence should be 0 on failure, got %v", res.Confidence)
	}

	res, err = New(Options{TargetFormat: FormatJSON, RepairJSON: true, MaxRepairDepth: 1}).
		Process(`{"items": [1, 2`)
	if !errors.Is(err, ErrRepairFailed) {
		t.Fatalf("depth=1 must not attempt truncation repair, err=%v", err)
	}
	if res.Output != `{"items": [1, 2` {
		t.Fatalf("depth=1 must return original, got %q", res.Output)
	}

	fixed, err := New(Options{MaxRepairDepth: 3, RepairJSON: true}).Process(`{"done": tru`)
	if err != nil {
		t.Fatalf("depth=3 partial literal completion failed: %v", err)
	}
	if fixed.Output != `{"done": true}` {
		t.Fatalf("depth=3 completion got %q", fixed.Output)
	}

	pt := New(Options{TargetFormat: FormatPlainText, StripReasoning: true})
	res, err = pt.Process("<think>secret</think>The answer is forty two.")
	if err != nil {
		t.Fatalf("plain text mode errored: %v", err)
	}
	if res.Output != "The answer is forty two." {
		t.Fatalf("plain text output %q", res.Output)
	}

	res, err = defaultProc().Process("")
	if err != nil || res.Output != "" {
		t.Fatalf("empty input handling: %v %q", err, res.Output)
	}

	res, err = defaultProc().Process("Just a friendly hello.")
	if err != nil {
		t.Fatalf("prose fast path errored: %v", err)
	}
	if res.Cleaned || res.Confidence != 0 || len(res.Repairs) != 0 {
		t.Fatalf("prose fast path flags wrong: %+v", res)
	}
}

func TestModelGuess(t *testing.T) {
	r1, _ := defaultProc().Process("</think>\n{\"a\": 1}")
	if r1.ModelGuess != ModelDeepSeek {
		t.Fatalf("orphan close should guess deepseek, got %v", r1.ModelGuess)
	}
	r2, _ := defaultProc().Process("<think>x</think>{\"a\": 1}")
	if r2.ModelGuess != ModelQwen {
		t.Fatalf("full block should guess qwen, got %v", r2.ModelGuess)
	}
	hinted := New(Options{ModelHint: ModelGLM})
	r3, _ := hinted.Process("<think>x</think>{\"a\": 1}")
	if r3.ModelGuess != ModelGLM {
		t.Fatalf("hint must win, got %v", r3.ModelGuess)
	}
}

func TestFixConvenience(t *testing.T) {
	out, err := Fix("{'active': True}")
	if err != nil {
		t.Fatalf("Fix error: %v", err)
	}
	if out != `{"active": true}` {
		t.Fatalf("Fix output %q", out)
	}
	out, err = Fix(`{"pristine": [1, 2, 3]}`)
	if err != nil {
		t.Fatalf("Fix error: %v", err)
	}
	if out != `{"pristine": [1, 2, 3]}` {
		t.Fatalf("Fix changed clean input: %q", out)
	}
}

func TestNeverPanic(t *testing.T) {
	nasty := []string{
		"",
		"<think>",
		"</think>",
		"```",
		"~~~",
		"<<<>>>",
		"}{",
		"[''",
		"\x00\xff",
		"╭╮╰╯",
		`{"a":`,
		"'unclosed",
		strings.Repeat("<think>", 50),
		strings.Repeat("[", 200),
		"{\"k\": \"" + strings.Repeat("\\", 100),
		"</content>",
		"<a b c d",
		"\r\r\r",
		"`{'x': True}` trailing ```",
	}
	p := defaultProc()
	for i, in := range nasty {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic on input %d (%q): %v", i, truncate(in), r)
				}
			}()
			res, err := p.Process(in)
			if in == "" {
				return
			}
			if err != nil && res.Output != in {
				t.Fatalf("input %d: error path must return original, got %q", i, res.Output)
			}
			if strings.TrimSpace(res.Output) == "" && strings.TrimSpace(in) != "" {
				t.Fatalf("input %d: empty output forbidden (err=%v)", i, err)
			}
		}()
	}
}

func TestConcurrentUse(t *testing.T) {
	p := defaultProc()
	inputs := []string{
		"</think>\nHere:\n{\"result\": 42}",
		"<think>r</think>Sure!\n```json\n{\"ok\": true}\n```",
		"{'active': True, 'data': None}",
		`{"items": [1, 2, 3`,
		`{"code": "func main() {}"}</content>`,
	}
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				in := inputs[(g+i)%len(inputs)]
				res, err := p.Process(in)
				if err != nil {
					t.Errorf("goroutine %d: unexpected error on %q: %v", g, in, err)
					return
				}
				if strings.TrimSpace(res.Output) == "" {
					t.Errorf("goroutine %d: empty output for %q", g, in)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestFunctionCallConversion(t *testing.T) {
	cleanedTrue := true
	cases := []tc{
		{
			name:    "full python-style call",
			input:   `get_weather(city="Jakarta", units='metric', days=3, verbose=True)`,
			want:    `{"name":"get_weather","arguments":{"city":"Jakarta","units":"metric","days":3,"verbose":true}}`,
			cleaned: &cleanedTrue,
			actions: []string{ActionConvertedFunctionCall},
			confOne: true,
		},
		{
			name:    "empty args",
			input:   `list_tools()`,
			want:    `{"name":"list_tools","arguments":{}}`,
			actions: []string{ActionConvertedFunctionCall},
			confOne: true,
		},
		{
			name:    "bare word value becomes string",
			input:   `send_message(to=budi, text="halo")`,
			want:    `{"name":"send_message","arguments":{"to":"budi","text":"halo"}}`,
			actions: []string{ActionConvertedFunctionCall},
			confOne: true,
		},
		{
			name:    "none and false literals",
			input:   `search(q="x", cache=None, strict=False)`,
			want:    `{"name":"search","arguments":{"q":"x","cache":null,"strict":false}}`,
			actions: []string{ActionConvertedFunctionCall},
			confOne: true,
		},
		{
			name:    "nested dict arg survives",
			input:   `create(filter={"status": "open"}, limit=5)`,
			want:    `{"name":"create","arguments":{"filter":{"status": "open"},"limit":5}}`,
			actions: []string{ActionConvertedFunctionCall},
			confOne: true,
		},
	}
	runTable(t, defaultProc(), cases)

	res, err := defaultProc().Process("Sure! Here is the call:\nget_weather(city=\"Jakarta\")")
	if err != nil || !strings.HasPrefix(res.Output, "{\"name\":\"get_weather\"") {
		t.Fatalf("call inside prose: out=%q err=%v repairs=%+v", res.Output, err, res.Repairs)
	}
}

func TestAggressiveParamFixes(t *testing.T) {
	out, err := New(Options{MaxRepairDepth: 3, RepairJSON: true}).Process(`{"city": Jakarta, "zip": 12345}`)
	if err != nil || out.Output != `{"city": "Jakarta", "zip": 12345}` {
		t.Fatalf("bare values depth3: %q err=%v", out.Output, err)
	}
	res2, err2 := New(Options{MaxRepairDepth: 2, RepairJSON: true}).Process(`{"city": Jakarta}`)
	if err2 == nil && json.Valid([]byte(res2.Output)) && strings.Contains(res2.Output, `"Jakarta"`) {
		t.Fatalf("depth2 must not quote bare values, got %q", res2.Output)
	}

	out3, err3 := New(Options{MaxRepairDepth: 3, RepairJSON: true}).
		Process(`{"arguments": "{\"city\": \"Jakarta\"}", "n": 1}`)
	if err3 != nil {
		t.Fatalf("stringified unwrap errored: %v", err3)
	}
	var parsed struct {
		Arguments map[string]string `json:"arguments"`
		N         int               `json:"n"`
	}
	if e := json.Unmarshal([]byte(out3.Output), &parsed); e != nil ||
		parsed.Arguments["city"] != "Jakarta" || parsed.N != 1 {
		t.Fatalf("stringified unwrap got %q", out3.Output)
	}
}

func TestLiteralEscapeNDJSON(t *testing.T) {
	literalSep := `\` + "n"
	in := `{"name": "Alice", "id": 1}` + literalSep + `{"name": "Bob", "id": 2}` + literalSep + `{"name": "Charlie", "id": 3}`
	want := `[{"name": "Alice", "id": 1},{"name": "Bob", "id": 2},{"name": "Charlie", "id": 3}]`

	out, err := Fix(in)
	if err != nil {
		t.Fatalf("literal-escape NDJSON must repair, err=%v in=%q", err, in)
	}
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}

	single := `{"a": 1}` + literalSep + `{"b": 2}`
	out2, err2 := Fix(single)
	if err2 != nil || out2 != `[{"a": 1},{"b": 2}]` {
		t.Fatalf("two-value variant: out=%q err=%v", out2, err2)
	}
}

func TestLiveCorpus(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "live", "*.raw.txt"))
	if err != nil || len(files) == 0 {
		t.Skipf("no live corpus files: %v", err)
	}
	p := defaultProc()
	for _, f := range files {
		raw, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatalf("read %s: %v", f, rerr)
		}
		name := filepath.Base(f)
		t.Run(name, func(t *testing.T) {
			res, perr := p.Process(string(raw))
			if perr != nil {
				t.Fatalf("repair failed: %v\nraw=%q", perr, truncate(string(raw)))
			}
			out := strings.TrimSpace(res.Output)
			if !json.Valid([]byte(out)) {
				t.Fatalf("output not valid JSON: %q\nraw=%q", truncate(out), truncate(string(raw)))
			}
			if res.Confidence != 1.0 {
				t.Fatalf("confidence=%v want 1.0", res.Confidence)
			}
		})
	}
}

func runTable(t *testing.T, p *Processor, cases []tc) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := p.Process(c.input)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got none; result %+v", res)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v (repairs=%+v)", err, res.Repairs)
			}
			if res.Output != c.want {
				t.Fatalf("output mismatch:\n got: %q\nwant: %q\nrepairs=%+v", res.Output, c.want, res.Repairs)
			}
			if c.cleaned != nil && res.Cleaned != *c.cleaned {
				t.Fatalf("cleaned=%v want %v", res.Cleaned, *c.cleaned)
			}
			for _, a := range c.actions {
				if !hasAction(res.Repairs, a) {
					t.Fatalf("missing action %q; repairs=%+v", a, res.Repairs)
				}
			}
			if c.confOne && res.Confidence != 1.0 {
				t.Fatalf("confidence=%v want 1.0", res.Confidence)
			}
			if c.guess != ModelGeneric && res.ModelGuess != c.guess {
				t.Fatalf("guess=%v want %v", res.ModelGuess, c.guess)
			}
		})
	}
}

func truncate(s string) string {
	if len(s) > 32 {
		return s[:32] + "..."
	}
	return s
}

var benchInput = buildBenchInput()

func buildBenchInput() string {
	var b strings.Builder
	b.WriteString("<think>\nThe user wants a list of items, I should produce valid JSON.\n")
	b.WriteString(strings.Repeat("Considering edge cases... ", 40))
	b.WriteString("\n</think>\nSure! Here is your JSON:\n```json\n")
	b.WriteString(`{"items": [`)
	for b.Len() < 3400 {
		b.WriteString(`{"id": 1234, "name": "widget", "tags": ["a", "b"], "active": True},`)
	}
	b.WriteString(`{"id": 99, "name": "last"}`)
	b.WriteString("]}\n```\nLet me know if you need anything else!")
	s := b.String()
	if len(s) >= 4096 {
		panic("benchmark input must stay under 4KB")
	}
	return s
}

func BenchmarkProcess4KB(b *testing.B) {
	p := defaultProc()
	b.SetBytes(int64(len(benchInput)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res, err := p.Process(benchInput)
		if err != nil {
			b.Fatal(err)
		}
		_ = res
	}
}

func BenchmarkFix(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := Fix(benchInput)
		if err != nil {
			b.Fatal(err)
		}
	}
}
