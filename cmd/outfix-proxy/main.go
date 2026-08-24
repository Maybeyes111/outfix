package main

import (
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
	upstream *url.URL
	client   *http.Client
	hint     outfix.ModelFamily
	verbose  bool
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

	if !isJSON || resp.StatusCode >= 400 {
		copyHeaders(w, resp.Header)
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "read upstream: "+err.Error(), http.StatusBadGateway)
		return
	}

	var cr chatResponse
	if json.Unmarshal(raw, &cr) != nil || len(cr.Choices) == 0 || cr.Error != nil {
		copyHeaders(w, resp.Header)
		w.WriteHeader(resp.StatusCode)
		w.Write(raw)
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
	out := raw
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

func main() {
	listen := flag.String("listen", ":8643", "listen address")
	upstream := flag.String("upstream", "", "upstream OpenAI-compatible base URL (required)")
	model := flag.String("model", "generic", "model hint: generic|qwen|deepseek|glm")
	timeout := flag.Int("timeout", 300, "upstream timeout seconds")
	verbose := flag.Bool("verbose", false, "log repairs to stderr")
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
		upstream: u,
		client:   &http.Client{Timeout: time.Duration(*timeout) * time.Second},
		hint:     hint,
		verbose:  *verbose,
	}
	log.Printf("outfix-proxy listening on %s -> %s", *listen, u.String())
	if err := http.ListenAndServe(*listen, p); err != nil {
		log.Fatal(err)
	}
}
