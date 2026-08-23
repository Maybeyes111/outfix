package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	outfix "github.com/Maybeyes111/outfix"
)

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	Model       string    `json:"model"`
	Messages    []chatMsg `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature"`
}

type chatResp struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

var prompts = []struct {
	id        string
	text      string
	maxTokens int
}{
	{"toolcall", "Call the get_weather tool for city Jakarta with units metric and days 3. Respond with the tool call only, nothing else.", 400},
	{"person", "Return ONLY a JSON object (no explanation, no markdown) for a fictional person with keys: name (string), age (number), hobbies (array of 3 strings), active (boolean).", 400},
	{"trunc-bait", "Return ONLY a JSON config object with nested keys server{host,port,tags[5]}, database{hosts[3],replica}, cache{ttl,levels[4]} filled with plausible values. Be complete.", 80},
	{"math-json", "What is 17*23? Think about it, then answer with ONLY a JSON object {\"answer\": <number>} at the end.", 400},
	{"then-explain", "First output a JSON object {\"status\": \"ok\", \"code\": 200}. Then explain in one sentence why status codes matter.", 400},
	{"ndjson", "Give exactly 3 separate JSON objects, one per line, no array wrapper, each a user record with name and id fields. Nothing else.", 400},
	{"pydict", "Return this data as a Python dictionary literal (single quotes, True/False/None): user Maria, admin False, quota None, tags ['beta','gamma']. Output only the dict.", 300},
	{"nested", "Return ONLY a JSON object company with employees[2] each having name, skills[3], address{city,zip}, plus metadata{version,flags[2]}. Fill with realistic values.", 500},
}

func main() {
	key := os.Getenv("ROUTER_KEY")
	base := os.Getenv("ROUTER_BASE")
	if base == "" {
		base = "http://localhost:20128/v1"
	}
	models := []string{
		"xkiro/deepseek/deepseek-v4-pro",
		"xkiro/deepseek/deepseek-v4-flash",
		"xkiro/qwen/qwen3.8-max",
	}
	if len(os.Args) > 1 {
		models = os.Args[1:]
	}
	os.MkdirAll("testdata/live", 0o755)

	client := &http.Client{Timeout: 120 * time.Second}
	type stat struct {
		raw      int
		repaired int
		fallback int
		total    int
	}
	stats := map[string]*stat{}
	var fails []string

	for _, model := range models {
		short := filepath.Base(model)
		st := stats[model]
		if st == nil {
			st = &stat{}
			stats[model] = st
		}
		for _, p := range prompts {
			name := fmt.Sprintf("%s-%s", short, p.id)
			body, _ := json.Marshal(chatReq{
				Model:       model,
				Messages:    []chatMsg{{Role: "user", Content: p.text}},
				MaxTokens:   p.maxTokens,
				Temperature: 0.7,
			})
			req, _ := http.NewRequest("POST", base+"/chat/completions", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				fmt.Printf("[NETFAIL] %s: %v\n", name, err)
				continue
			}
			var cr chatResp
			dec := json.NewDecoder(resp.Body)
			derr := dec.Decode(&cr)
			resp.Body.Close()
			if derr != nil {
				fmt.Printf("[BADJSON] %s: %v\n", name, derr)
				continue
			}
			if cr.Error != nil {
				fmt.Printf("[APIERR ] %s: %s\n", name, cr.Error.Message)
				continue
			}
			content := cr.Choices[0].Message.Content
			if strings.TrimSpace(content) == "" {
				content = cr.Choices[0].Message.ReasoningContent
			}
			os.WriteFile(filepath.Join("testdata", "live", name+".raw.txt"), []byte(content), 0o644)

			st.total++
			wasValid := json.Valid([]byte(content))
			res, perr := outfix.New(outfix.Options{
				StripReasoning: true,
				RepairJSON:     true,
				RepairXML:      true,
			}).Process(content)
			nowValid := json.Valid([]byte(strings.TrimSpace(res.Output)))

			status := "?"
			switch {
			case wasValid:
				status = "CLEAN  "
			case nowValid && perr == nil:
				status = "REPAIRED"
				st.repaired++
			case nowValid && perr != nil:
				status = "REPAIRED*"
				st.repaired++
			default:
				status = "FAILED "
				st.fallback++
				fails = append(fails, fmt.Sprintf("%s | err=%v\nRAW: %q\nOUT: %q",
					name, perr, trunc(content, 300), trunc(res.Output, 300)))
			}
			fmt.Printf("[%s] %-28s finish=%s conf=%.2f guess=%s\n",
				status, name, cr.Choices[0].FinishReason, res.Confidence, res.ModelGuess)
		}
	}

	fmt.Println("\n==== SUMMARY ====")
	for m, st := range stats {
		fmt.Printf("%-40s total=%d repaired=%d failed=%d clean=%d\n",
			m, st.total, st.repaired, st.fallback, st.total-st.repaired-st.fallback)
	}
	if len(fails) > 0 {
		fmt.Println("\n==== FAILURES ====")
		for _, f := range fails {
			fmt.Println(f)
			fmt.Println("---")
		}
		os.Exit(1)
	}
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
