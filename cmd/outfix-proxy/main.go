package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	outfix "github.com/Maybeyes111/outfix"
)

type chatChoice struct {
	Message struct {
		Content          string `json:"content"`
		ReasoningContent string `json:"reasoning_content,omitempty"`
	} `json:"message"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type proxy struct {
	upstream   *url.URL
	client     *http.Client
	hint       outfix.ModelFamily
	verbose    bool
	streamMode string
	maxStream  int
}

func copyHeaders(w http.ResponseWriter, src http.Header) {
	for k, vs := range src {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	upstreamURL := p.upstream.String() + r.URL.Path
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for k, vs := range r.Header {
		if strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Host = p.upstream.Host

	resp, err := p.client.Do(req)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	isJSON := strings.Contains(ct, "application/json")
	isSSE := strings.Contains(ct, "text/event-stream")
	isAnthropic := strings.HasSuffix(r.URL.Path, "/messages")

	switch {
	case isSSE && p.streamMode == "clean":
		if isAnthropic {
			p.serveCleanedSSEAnthropic(w, resp)
		} else {
			p.serveCleanedSSE(w, resp)
		}
	case !isJSON || resp.StatusCode >= 400 || (isSSE && p.streamMode == "pass"):
		copyHeaders(w, resp.Header)
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	default:
		if isAnthropic {
			p.serveCleanedJSONAnthropic(w, resp)
		} else {
			p.serveCleanedJSON(w, resp)
		}
	}
}

type anthropicResp struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	} `json:"content"`
	Role    string `json:"role,omitempty"`
	Model   string `json:"model,omitempty"`
	StopRsn string `json:"stop_reason,omitempty"`
}

func cleanTextWith(p *proxy, s string) (string, bool) {
	opts := outfix.Options{
		StripReasoning: true,
		RepairJSON:     true,
		RepairXML:      true,
		ModelHint:      p.hint,
	}
	res, err := outfix.New(opts).Process(s)
	if err != nil || !res.Cleaned {
		return s, false
	}
	if p.verbose {
		for _, a := range res.Repairs {
			log.Printf("[outfix] %s: %s", a.Type, a.Description)
		}
	}
	return res.Output, true
}

func (p *proxy) serveCleanedJSONAnthropic(w http.ResponseWriter, resp *http.Response) {
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "read upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	var top map[string]any
	if json.Unmarshal(rawBody, &top) != nil {
		copyHeaders(w, resp.Header)
		w.WriteHeader(resp.StatusCode)
		w.Write(rawBody)
		return
	}
	blocks, ok := top["content"].([]any)
	if !ok || len(blocks) == 0 {
		copyHeaders(w, resp.Header)
		w.WriteHeader(resp.StatusCode)
		w.Write(rawBody)
		return
	}
	cleanedAny := false
	for _, b := range blocks {
		m, ok := b.(map[string]any)
		if !ok || m["type"] != "text" {
			continue
		}
		txt, _ := m["text"].(string)
		if txt == "" {
			continue
		}
		if v, changed := cleanTextWith(p, txt); changed {
			m["text"] = v
			cleanedAny = true
		}
	}
	out := rawBody
	if cleanedAny {
		if nb, e := json.Marshal(&top); e == nil {
			out = nb
		}
	}
	copyHeaders(w, resp.Header)
	w.Header().Set("Content-Length", fmt.Sprint(len(out)))
	w.WriteHeader(resp.StatusCode)
	w.Write(out)
}

func (p *proxy) serveCleanedSSEAnthropic(w http.ResponseWriter, resp *http.Response) {
	flusher, canFlush := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	if canFlush {
		flusher.Flush()
	}

	var lines []string
	var acc strings.Builder
	textSeen := false
	overflow := false

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		lines = append(lines, line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var evt struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if json.Unmarshal([]byte(data), &evt) != nil || evt.Type != "content_block_delta" ||
			evt.Delta.Type != "text_delta" {
			continue
		}
		textSeen = true
		acc.WriteString(evt.Delta.Text)
		if !overflow && acc.Len() > p.maxStream {
			overflow = true
			if p.verbose {
				log.Printf("[outfix][sse-anthropic] exceeded %d bytes; pass-through", p.maxStream)
			}
		}
	}
	_ = textSeen

	var cleanedFull string
	injected := false
	if !overflow {
		v, _ := cleanTextWith(p, acc.String())
		cleanedFull = v
	}

	for _, line := range lines {
		outLine := line
		if !overflow && strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var evt map[string]any
			if json.Unmarshal([]byte(data), &evt) == nil && evt["type"] == "content_block_delta" {
				if d, ok := evt["delta"].(map[string]any); ok && d["type"] == "text_delta" {
					if !injected {
						d["text"] = cleanedFull
						injected = true
					} else {
						d["text"] = ""
					}
					if nb, e := json.Marshal(evt); e == nil {
						outLine = "data: " + string(nb)
					}
				}
			}
		}
		if _, err := io.WriteString(w, outLine+"\n\n"); err != nil {
			return
		}
		if canFlush {
			flusher.Flush()
		}
	}
}

func (p *proxy) serveCleanedJSON(w http.ResponseWriter, resp *http.Response) {
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "read upstream: "+err.Error(), http.StatusBadGateway)
		return
	}

	var cr chatResponse
	if json.Unmarshal(rawBody, &cr) != nil || len(cr.Choices) == 0 || cr.Error != nil {
		copyHeaders(w, resp.Header)
		w.WriteHeader(resp.StatusCode)
		w.Write(rawBody)
		return
	}

	cleanedAny := false
	for i := range cr.Choices {
		msg := &cr.Choices[i].Message
		content := msg.Content
		if strings.TrimSpace(content) == "" && msg.ReasoningContent != "" {
			continue
		}
		opts := outfix.Options{
			StripReasoning: true,
			RepairJSON:     true,
			RepairXML:      true,
			ModelHint:      p.hint,
		}
		res, perr := outfix.New(opts).Process(content)
		if perr == nil && res.Cleaned {
			msg.Content = res.Output
			cleanedAny = true
			if p.verbose {
				for _, a := range res.Repairs {
					log.Printf("[outfix] %s: %s", a.Type, a.Description)
				}
			}
		}
	}
	out := rawBody
	if cleanedAny {
		if nb, err := json.Marshal(&cr); err == nil {
			out = nb
		}
	}
	copyHeaders(w, resp.Header)
	w.Header().Set("Content-Length", fmt.Sprint(len(out)))
	w.WriteHeader(resp.StatusCode)
	w.Write(out)
}

type sseChunk struct {
	ID      string `json:"id,omitempty"`
	Object  string `json:"object,omitempty"`
	Model   string `json:"model,omitempty"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role    string `json:"role,omitempty"`
			Content string `json:"content,omitempty"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

func (p *proxy) serveCleanedSSE(w http.ResponseWriter, resp *http.Response) {
	flusher, canFlush := w.(http.Flusher)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(200)
	if canFlush {
		flusher.Flush()
	}

	emit := func(payload any) bool {
		b, err := json.Marshal(payload)
		if err != nil {
			return true
		}
		if _, err := io.WriteString(w, "data: "+string(b)+"\n\n"); err != nil {
			return false
		}
		if canFlush {
			flusher.Flush()
		}
		return true
	}

	var acc strings.Builder
	overflow := false
	firstMeta := &sseChunk{}
	haveMeta := false
	finish := ""

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	doneSeen := false

	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			doneSeen = true
			break
		}
		var ch sseChunk
		if json.Unmarshal([]byte(data), &ch) != nil || len(ch.Choices) == 0 {
			if overflow {
				if _, err := io.WriteString(w, "data: "+data+"\n\n"); err != nil {
					return
				}
				if canFlush {
					flusher.Flush()
				}
			}
			continue
		}
		if !haveMeta {
			*firstMeta = ch
			firstMeta.Choices = nil
			haveMeta = true
		}
		c := ch.Choices[0]
		acc.WriteString(c.Delta.Content)
		if c.FinishReason != nil && *c.FinishReason != "" {
			finish = *c.FinishReason
		}
		if !overflow && acc.Len() > p.maxStream {
			overflow = true
			if p.verbose {
				log.Printf("[outfix] stream exceeded %d bytes; switching to pass-through", p.maxStream)
			}
			io.WriteString(w, "data: "+firstJSONWith(firstMeta, c.Delta.Content)+"\n\n")
			if canFlush {
				flusher.Flush()
			}
		}
		if overflow {
			if _, err := io.WriteString(w, "data: "+data+"\n\n"); err != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
	}

	if overflow {
		if finish != "" {
			fr := finish
			emit(map[string]any{
				"id": firstMeta.ID, "object": firstMeta.Object, "model": firstMeta.Model,
				"choices": []map[string]any{{
					"index":         0,
					"delta":         map[string]any{},
					"finish_reason": fr,
				}},
			})
		}
		io.WriteString(w, "data: [DONE]\n\n")
		if canFlush {
			flusher.Flush()
		}
		return
	}

	opts := outfix.Options{
		StripReasoning: true,
		RepairJSON:     true,
		RepairXML:      true,
		ModelHint:      p.hint,
	}
	res, perr := outfix.New(opts).Process(acc.String())
	cleaned := res.Output
	if perr != nil {
		cleaned = acc.String()
	}
	if p.verbose && perr == nil && res.Cleaned {
		for _, a := range res.Repairs {
			log.Printf("[outfix][sse] %s: %s", a.Type, a.Description)
		}
	}

	if strings.TrimSpace(cleaned) != "" {
		var content any = cleaned
		emit(map[string]any{
			"id": firstMeta.ID, "object": firstMeta.Object, "model": firstMeta.Model,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         map[string]any{"role": "assistant", "content": content},
				"finish_reason": nil,
			}},
		})
	}
	fr := finish
	if fr == "" {
		fr = "stop"
	}
	emit(map[string]any{
		"id": firstMeta.ID, "object": firstMeta.Object, "model": firstMeta.Model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": fr,
		}},
	})
	io.WriteString(w, "data: [DONE]\n\n")
	if canFlush {
		flusher.Flush()
	}
	_ = doneSeen
}

func firstJSONWith(meta *sseChunk, content string) string {
	b, _ := json.Marshal(map[string]any{
		"id": meta.ID, "object": meta.Object, "model": meta.Model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{"role": "assistant", "content": content},
			"finish_reason": nil,
		}},
	})
	return string(b)
}

func main() {
	listen := flag.String("listen", ":8643", "listen address")
	upstream := flag.String("upstream", "", "upstream OpenAI-compatible base URL (required)")
	model := flag.String("model", "generic", "model hint: generic|qwen|deepseek|glm")
	timeout := flag.Int("timeout", 300, "upstream timeout seconds")
	verbose := flag.Bool("verbose", false, "log repairs to stderr")
	streamMode := flag.String("stream-mode", "clean", "streaming behavior: clean (buffer, flush at finish) | pass (verbatim)")
	maxStream := flag.Int("max-stream-buffer", 65536, "bytes to buffer in clean mode before pass-through fallback")
	flag.Parse()

	if *upstream == "" {
		log.Fatal("outfix-proxy: -upstream is required (e.g. http://localhost:20128/v1)")
	}
	u, err := url.Parse(strings.TrimRight(*upstream, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		log.Fatalf("outfix-proxy: invalid -upstream %q", *upstream)
	}

	hint := outfix.ModelGeneric
	switch *model {
	case "qwen":
		hint = outfix.ModelQwen
	case "deepseek":
		hint = outfix.ModelDeepSeek
	case "glm":
		hint = outfix.ModelGLM
	}

	p := &proxy{
		upstream:   u,
		client:     &http.Client{Timeout: time.Duration(*timeout) * time.Second},
		hint:       hint,
		verbose:    *verbose,
		streamMode: *streamMode,
		maxStream:  *maxStream,
	}
	log.Printf("outfix-proxy listening on %s -> %s (stream-mode=%s)", *listen, u.String(), p.streamMode)
	if err := http.ListenAndServe(*listen, p); err != nil {
		log.Fatal(err)
	}
}
