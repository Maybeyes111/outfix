package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
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

var tools = []struct {
	name string
	args []string
}{
	{"get_weather", []string{"city", "units", "days"}},
	{"send_email", []string{"to", "subject", "priority", "html"}},
	{"search_flights", []string{"from", "to", "date", "direct"}},
	{"create_ticket", []string{"title", "tags", "urgent"}},
	{"book_hotel", []string{"city", "nights", "rating"}},
}

var cities = []string{"Jakarta", "Surabaya", "Bandung", "Tokyo", "Berlin", "São Paulo"}
var words = []string{"alert", "weekly-report", "q3-budget", "meeting-notes", "hotfix"}

type gen struct{ r *rand.Rand }

func (g gen) str() string     { return cities[g.r.Intn(len(cities))] }
func (g gen) word() string    { return words[g.r.Intn(len(words))] }
func (g gen) num() int        { return g.r.Intn(30) + 1 }
func (g gen) b() bool         { return g.r.Intn(2) == 0 }
func (g gen) date() string    { return fmt.Sprintf("2026-%02d-%02d", g.r.Intn(12)+1, g.r.Intn(28)+1) }
func (g gen) email() string   { return fmt.Sprintf("u%d@example.com", g.r.Intn(999)) }
func (g gen) rating() float64 { return float64(g.r.Intn(50)) / 10 }

func (g gen) argValues(toolIdx int) map[string]any {
	m := map[string]any{}
	t := tools[toolIdx]
	for _, a := range t.args {
		switch a {
		case "city":
			m[a] = g.str()
		case "units":
			m[a] = []string{"metric", "imperial"}[g.r.Intn(2)]
		case "days", "nights":
			m[a] = g.num()
		case "to":
			m[a] = g.email()
		case "subject", "title":
			m[a] = g.word()
		case "priority":
			m[a] = []string{"low", "normal", "high"}[g.r.Intn(3)]
		case "html":
			m[a] = g.b()
		case "from":
			m[a] = g.str()
		case "date":
			m[a] = g.date()
		case "direct":
			m[a] = g.b()
		case "tags":
			m[a] = []string{g.word(), g.word()}
		case "urgent":
			m[a] = g.b()
		case "rating":
			m[a] = g.rating()
		}
	}
	return m
}

func renderKV(m map[string]any, quoteKeys bool, pyStyle bool) string {
	var parts []string
	for k, v := range m {
		key := k
		if quoteKeys {
			key = "\"" + k + "\""
		} else if pyStyle {
			key = "'" + k + "'"
		}
		var val string
		switch x := v.(type) {
		case string:
			if pyStyle {
				val = "'" + x + "'"
			} else {
				b, _ := json.Marshal(x)
				val = string(b)
			}
		case bool:
			val = fmt.Sprintf("%v", x)
			if pyStyle {
				if x {
					val = "True"
				} else {
					val = "False"
				}
			}
		case nil:
			val = "null"
			if pyStyle {
				val = "None"
			}
		default:
			b, _ := json.Marshal(x)
			val = string(b)
		}
		parts = append(parts, key+"="+val)
	}
	return strings.Join(parts, ", ")
}

func renderJSON(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func main() {
	n := flag.Int("n", 200, "total requests")
	intervalMs := flag.Int("interval-ms", 3000, "ms between request starts")
	base := flag.String("base", "http://localhost:20128/v1", "router base URL")
	key := os.Getenv("ROUTER_KEY")
	dir := flag.String("dir", "testdata/toolloop", "output dir")
	flag.Parse()

	models := []string{
		"xkiro/deepseek/deepseek-v4-flash",
		"xkiro/deepseek/deepseek-v4-pro",
		"xkiro/qwen/qwen3.8-max",
	}

	os.MkdirAll(*dir, 0o755)
	summaryFile, _ := os.Create(filepath.Join(*dir, "progress.log"))
	defer summaryFile.Close()
	logf := func(format string, a ...any) {
		fmt.Printf(format+"\n", a...)
		fmt.Fprintf(summaryFile, format+"\n", a...)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	proc := outfix.New(outfix.Options{
		StripReasoning: true,
		RepairJSON:     true,
		RepairXML:      true,
	})

	start := time.Now()
	stats := map[string]int{}
	var fails []string

	for i := 0; i < *n; i++ {
		target := start.Add(time.Duration(i) * time.Duration(*intervalMs) * time.Millisecond)
		if d := time.Until(target); d > 0 {
			time.Sleep(d)
		}
		g := gen{r: rand.New(rand.NewSource(int64(i)*7919 + 13))}
		ti := i % len(tools)
		args := g.argValues(ti)
		style := i % 6
		maxTokens := 400
		var prompt string
		expectTool := tools[ti].name

		switch style {
		case 0:
			prompt = fmt.Sprintf("Call the %s tool with exactly these arguments: %s. Respond with the tool call only.",
				expectTool, renderKV(args, false, false))
		case 1:
			prompt = fmt.Sprintf("Emit a tool call for %s as %s. Tool call only.", expectTool, renderKV(args, true, false))
		case 2:
			prompt = fmt.Sprintf("You cannot execute tools here. Write the %s invocation instead: %s. Output only the invocation.",
				expectTool, renderKV(args, false, true))
		case 3:
			prompt = fmt.Sprintf(`Return ONLY compact JSON: {"name":"%s","arguments":%s}`, expectTool, renderJSON(args))
			maxTokens = 40
		case 4:
			other := tools[(ti+1)%len(tools)].name
			a2 := g.argValues((ti + 1) % len(tools))
			prompt = fmt.Sprintf("Output two separate tool calls, one per line, nothing else:\n%s with %s\n%s with %s",
				expectTool, renderKV(args, false, false), other, renderKV(a2, false, false))
		case 5:
			prompt = fmt.Sprintf("As a Python dict literal (single quotes), express calling %s with %s. Only the dict.",
				expectTool, renderKV(args, false, true))
		}

		model := models[i%len(models)]
		body, _ := json.Marshal(chatReq{
			Model:       model,
			Messages:    []chatMsg{{Role: "user", Content: prompt}},
			MaxTokens:   maxTokens,
			Temperature: 0.8,
		})
		req, _ := http.NewRequest("POST", *base+"/chat/completions", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			stats["neterr"]++
			logf("[NETERR ] %04d %s: %v", i, model, err)
			continue
		}
		var cr chatResp
		derr := json.NewDecoder(resp.Body).Decode(&cr)
		resp.Body.Close()
		if derr != nil {
			stats["badjson"]++
			logf("[BADJSON] %04d %s", i, model)
			continue
		}
		if cr.Error != nil {
			stats["apierr"]++
			logf("[APIERR ] %04d %s: %.80s", i, model, cr.Error.Message)
			continue
		}
		content := cr.Choices[0].Message.Content
		if strings.TrimSpace(content) == "" {
			content = cr.Choices[0].Message.ReasoningContent
		}
		short := filepath.Base(model)

		res, perr := safe(proc, content)
		verdict := "CLEAN"
		switch {
		case perr != nil:
			verdict = "FALLBACK"
			stats["fallback"]++
		case res.Cleaned && res.Confidence == 1.0:
			verdict = "REPAIRED"
			stats["repaired"]++
		case !res.Cleaned && res.Confidence == 1.0:
			verdict = "CLEAN"
			stats["clean"]++
		default:
			verdict = "WEIRD"
			stats["weird"]++
		}
		if perr == nil && res.Confidence == 1.0 {
			ft := strings.TrimSpace(res.Output)
			isArray := strings.HasPrefix(ft, "[")
			var probe any
			if json.Unmarshal([]byte(ft), &probe) != nil {
				verdict = "VIOLATION"
				stats["violation"]++
				fails = append(fails, fmt.Sprintf("%04d %s conf1-invalid: %q", i, short, clip(ft)))
			} else if isArray {
				var arr []map[string]any
				if json.Unmarshal([]byte(ft), &arr) == nil {
					for _, e := range arr {
						if nm, _ := e["name"].(string); nm != "" && !strings.Contains(nm, expectTool) &&
							nm != tools[0].name && nm != tools[1].name && nm != tools[2].name &&
							nm != tools[3].name && nm != tools[4].name {
							fails = append(fails, fmt.Sprintf("%04d array entry odd name=%q", i, nm))
						}
					}
				}
			} else {
				var obj map[string]any
				if json.Unmarshal([]byte(ft), &obj) == nil {
					if nm, ok := obj["name"].(string); ok && !strings.Contains(nm, expectTool) {
						fails = append(fails, fmt.Sprintf("%04d wrong tool name=%q want~%q raw=%q",
							i, nm, expectTool, clip(content)))
					}
				}
			}
		}
		if verdict == "FALLBACK" || verdict == "VIOLATION" || verdict == "WEIRD" {
			os.WriteFile(filepath.Join(*dir, fmt.Sprintf("fail-%04d.raw.txt", i)), []byte(content), 0o644)
			fails = append(fails, fmt.Sprintf("%04d %s %s err=%v raw=%.120s", i, short, verdict, perr, clip(content)))
		}
		logf("[%8s] %04d %-22s finish=%-7s conf=%.2f", verdict, i, short, cr.Choices[0].FinishReason, res.Confidence)
	}

	el := time.Since(start).Round(time.Second)
	logf("==== DONE in %s ====", el)
	logf("stats: %+v", stats)
	if len(fails) > 0 {
		logf("FAILURES=%d", len(fails))
		for _, f := range fails[:min(30, len(fails))] {
			logf("  " + f)
		}
		os.Exit(1)
	}
	logf("NO FAILURES")
}

func safe(p *outfix.Processor, s string) (r outfix.Result, e error) {
	defer func() {
		if rec := recover(); rec != nil {
			r = outfix.Result{Output: s}
			e = fmt.Errorf("escaped panic: %v", rec)
		}
	}()
	return p.Process(s)
}

func clip(s string) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}
