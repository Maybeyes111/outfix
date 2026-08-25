package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	outfix "github.com/Maybeyes111/outfix"
)

func testProxy(t *testing.T, base string) *proxy {
	t.Helper()
	u, err := url.Parse(stringsTrimRight(base))
	if err != nil {
		t.Fatal(err)
	}
	return &proxy{upstream: u, client: &http.Client{}}
}

func stringsTrimRight(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func TestProxyCleansContent(t *testing.T) {
	polluted := map[string]any{
		"id":    "chatcmpl-1",
		"model": "deepseek-v4-flash",
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "</think>\nSure! Here is your JSON:\n```json\n{'active': True, 'n': None}\n```\nLet me know!",
				},
			},
		},
	}
	upstreamJSON, _ := json.Marshal(polluted)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(upstreamJSON)
	}))
	defer up.Close()

	pr := testProxy(t, up.URL)
	pr.hint = outfix.ModelGeneric
	srv := httptest.NewServer(pr)
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/v1/chat/completions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Choices) != 1 {
		t.Fatalf("choices=%d", len(got.Choices))
	}
	content := got.Choices[0].Message.Content
	want := `{"active": true, "n": null}`
	if content != want {
		t.Fatalf("content=%q want %q", content, want)
	}
}

func TestProxyCleanSSE(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		``,
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"<think>reasoning "},"finish_reason":null}]}`,
		``,
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"spans chunks</think>\nSure! Here is your JSON:\n` + "```json" + `\n{'active': True, 'n': None}\n` + "```" + `"},"finish_reason":null}]}`,
		``,
		`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse)
	}))
	defer up.Close()

	pr := testProxy(t, up.URL)
	pr.streamMode = "clean"
	pr.maxStream = 1 << 16
	srv := httptest.NewServer(pr)
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/v1/chat/completions",
		"application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := string(body)

	if !strings.Contains(out, `"content":"{\"active\": true, \"n\": null}"`) &&
		!strings.Contains(out, `{"active\": true, \"n\": null}`) {
		t.Fatalf("cleaned delta missing in SSE out=%s", out)
	}
	if strings.Contains(out, "<think>") || strings.Contains(out, "```") || strings.Contains(out, "Sure!") {
		t.Fatalf("pollution leaked: %s", out)
	}
	if !strings.Contains(out, "[DONE]") || !strings.Contains(out, `"finish_reason":"stop"`) {
		t.Fatalf("terminators missing: %s", out)
	}
}

func TestProxyPassModeKeepsRaw(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"</think>raw"},"finish_reason":null}}]}`+"\n\n"+`data: [DONE]`+"\n\n")
	}))
	defer up.Close()
	pr := testProxy(t, up.URL)
	pr.streamMode = "pass"
	srv := httptest.NewServer(pr)
	defer srv.Close()
	resp, err := srv.Client().Post(srv.URL+"/v1/x", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "</think>raw") {
		t.Fatalf("pass mode must be verbatim, got %s", b)
	}
}

func TestAnthropicJSONClean(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant",
			"content":[
				{"type":"text","text":"</think>\nSure! Here is your JSON:\n`+"```json"+`\n{'active': True}\n`+"```"+`\nLet me know!"},
				{"type":"tool_use","id":"tu_1","name":"get_weather","input":{"city":"Jakarta"}},
				{"type":"text","text":"clean already"}
			],
			"stop_reason":"end_turn"}`)
	}))
	defer up.Close()
	pr := testProxy(t, up.URL)
	srv := httptest.NewServer(pr)
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/v1/messages", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var ar struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Name string `json:"name"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		t.Fatal(err)
	}
	if len(ar.Content) != 3 {
		t.Fatalf("blocks=%d", len(ar.Content))
	}
	if strings.Contains(ar.Content[0].Text, "think") || !strings.Contains(ar.Content[0].Text, `{"active": true}`) {
		t.Fatalf("block0=%q", ar.Content[0].Text)
	}
	if ar.Content[1].Type != "tool_use" || ar.Content[1].Name != "get_weather" {
		t.Fatalf("tool_use mangled: %+v", ar.Content[1])
	}
	if ar.Content[2].Text != "clean already" {
		t.Fatalf("block2=%q", ar.Content[2].Text)
	}
}

func TestAnthropicSSEClean(t *testing.T) {
	events := []string{
		`{"type":"message_start","message":{"id":"msg_9"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"<think>deep "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"reasoning</think>\nAnswer:\n` + "```json" + `\n{\"v\": True}\n` + "```" + `"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		`{"type":"message_stop"}`,
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			io.WriteString(w, "event: x\ndata: "+e+"\n\n")
		}
	}))
	defer up.Close()
	pr := testProxy(t, up.URL)
	pr.streamMode = "clean"
	pr.maxStream = 1 << 16
	srv := httptest.NewServer(pr)
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/v1/messages", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := string(body)

	if got := strings.Count(out, "data: "); got != len(events) {
		t.Fatalf("event count=%d want %d\n%s", got, len(events), out)
	}
	if strings.Contains(out, "<think>") || strings.Contains(out, "```") {
		t.Fatalf("pollution leaked:\n%s", out)
	}
	if !strings.Contains(out, `{\"v\": true}`) && !strings.Contains(out, `{"v": true}`) {
		t.Fatalf("cleaned text missing:\n%s", out)
	}
	if !strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Fatalf("stop_reason lost:\n%s", out)
	}
}

func TestProxyPassesThroughErrors(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		w.Write([]byte(`{"error":{"message":"down"}}`))
	}))
	defer up.Close()
	pr := testProxy(t, up.URL)
	srv := httptest.NewServer(pr)
	defer srv.Close()
	resp, err := srv.Client().Post(srv.URL+"/v1/x", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("status=%d want 503", resp.StatusCode)
	}
}
