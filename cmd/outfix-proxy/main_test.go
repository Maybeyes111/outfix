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
