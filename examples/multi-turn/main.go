package main

import (
	"encoding/json"
	"fmt"

	outfix "github.com/Maybeyes111/outfix"
)

var scripted = []struct {
	role    string
	content string
}{
	{"user", "What's the weather in Jakarta? Also, what does <think> mean in AI?"},
	{"assistant", `<think>
The user is asking two things at once: current weather in Jakarta, and what
"<think>" means in AI terminology. The weather part I can answer from the
cached forecast payload, so I should emit the JSON object directly. For the
terminology question, a one-line explanation after the JSON should be enough.
Keeping it short since their previous messages suggest they want terse answers.
</think>
Sure! Here is the weather:
` + "```json" + `
{"city": "Jakarta", "temp_c": 31, "humid": True}
` + "```" + `
Let me know if you want another city!`},
	{"tool", "{\"status\":\"ok\",\"payload\":\"{\\\"cached\\\": true}\"}\r\n"},
	{"assistant", `<think>
Follow-up request received: Bandung this time. Same shape as before, so I will
reuse the same object structure and only swap the values. Bandung sits in the
highlands, so a cooler temperature around 24 degrees makes sense, and humidity
is typically lower than Jakarta. The parser rejected markdown fences on the
previous turn, so this time I will output the bare JSON object without any
wrappers or trailing commentary.
</think>
{"city": "Bandung", "temp_c": 24, "humid": False}`},
}

func main() {
	sess := outfix.NewSession(outfix.Options{
		StripReasoning: true,
		RepairJSON:     true,
		RepairXML:      true,
	})

	for i, t := range scripted {
		res, err := sess.ProcessTurn(t.role, t.content)
		fmt.Printf("turn %d (%s) cleaned=%v err=%v actions=%d\n",
			i+1, t.role, res.Cleaned, err != nil, len(res.Repairs))

		if t.role == "user" {
			fmt.Printf("  user text preserved verbatim: %v\n", res.Output == t.content)
			continue
		}
		var probe any
		if e := json.Unmarshal([]byte(strings_Trim(res.Output)), &probe); e == nil {
			b, _ := json.Marshal(probe)
			fmt.Printf("  parseable payload: %s\n", string(b))
		} else {
			fmt.Printf("  text output: %.60q\n", res.Output)
		}
	}

	for _, tr := range sess.Turns() {
		fmt.Printf("history: #%d %-9s in=%3d out=%3d cleaned=%-5v err=%v\n",
			tr.Index, tr.Role, tr.Input, tr.Output, tr.Cleaned, tr.Error)
	}
}

func strings_Trim(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\n' || s[start] == '\t' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\n' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
