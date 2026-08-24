package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
