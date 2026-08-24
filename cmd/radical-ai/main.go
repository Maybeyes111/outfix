package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	outfix "github.com/Maybeyes111/outfix"
	"github.com/Maybeyes111/outfix/chaos"
)

type violation struct {
	seed   uint64
	input  string
	kind   string
	detail string
}

func main() {
	iters := flag.Int("n", 50000, "number of radical outputs")
	maxLen := flag.Int("maxlen", 512, "max input length")
	corpusDir := flag.String("corpus", "testdata/radical", "cross-language corpus dir")
	corpusN := flag.Int("corpus-n", 300, "utf8 samples saved for port cross-check")
	flag.Parse()

	os.MkdirAll(*corpusDir, 0o755)
	p := outfix.New(outfix.Options{
		StripReasoning: true,
		RepairJSON:     true,
		RepairXML:      true,
	})

	var viols []violation
	saved := 0
	panics := 0
	repairs := 0
	fallbacks := 0

	for i := 0; i < *iters; i++ {
		seed := uint64(i)*2654435761 + 0x9E3779B97F4A7C15
		n := (i % *maxLen) + 1
		buf := make([]byte, n)
		chaos.Fill(buf, seed)
		in := string(buf)

		res, perr := safeProcess(p, in, &panics)

		if i == 0 || string(in) != in {
			continue
		}
		res2, _ := p.Process(in)
		if res2.Output != res.Output || res2.Confidence != res.Confidence {
			viols = append(viols, violation{seed, clip(in), "NONDETERMINISTIC",
				fmt.Sprintf("pass1=%q pass2=%q", clip(res.Output), clip(res2.Output))})
			continue
		}

		switch {
		case perr != nil:
			fallbacks++
			if res.Output != in {
				viols = append(viols, violation{seed, clip(in), "FALLBACK_NOT_ORIGINAL",
					fmt.Sprintf("out=%q", clip(res.Output))})
			}
			if strings.TrimSpace(res.Output) == "" && strings.TrimSpace(in) != "" {
				viols = append(viols, violation{seed, clip(in), "EMPTY_OUTPUT", ""})
			}
		default:
			if strings.TrimSpace(res.Output) == "" && strings.TrimSpace(in) != "" {
				viols = append(viols, violation{seed, clip(in), "EMPTY_OUTPUT_NO_ERR", ""})
			}
			if res.Cleaned {
				repairs++
			}
			if res.Confidence == 1.0 {
				ft := strings.TrimSpace(res.Output)
				okJSON := ft != "" && json.Valid([]byte(ft))
				if !okJSON {
					viols = append(viols, violation{seed, clip(in), "CONF1_BUT_INVALID",
						fmt.Sprintf("out=%q", clip(res.Output))})
				}
			}
		}

		if saved < *corpusN && utf8Valid(in) {
			base := filepath.Join(*corpusDir, fmt.Sprintf("%05d", saved))
			os.WriteFile(base+".in", []byte(in), 0o644)
			os.WriteFile(base+".go.out", []byte(res.Output), 0o644)
			os.WriteFile(base+".err", []byte(errStr(perr)), 0o644)
			saved++
		}
	}

	fmt.Printf("iterations=%d cleaned=%d fallbacks=%d escaped_panics=%d corpus=%d\n",
		*iters, repairs, fallbacks, panics, saved)
	if len(viols) > 0 {
		fmt.Printf("VIOLATIONS=%d\n", len(viols))
		for i, v := range viols {
			if i >= 20 {
				fmt.Println("...")
				break
			}
			fmt.Printf("[%s] seed=%d input=%q detail=%s\n", v.kind, v.seed, v.input, v.detail)
		}
		os.Exit(1)
	}
	fmt.Println("ALL INVARIANTS HOLD")
}

func safeProcess(p *outfix.Processor, in string, panics *int) (res outfix.Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			*panics++
			res = outfix.Result{Output: in}
			err = fmt.Errorf("escaped panic: %v", r)
		}
	}()
	return p.Process(in)
}

func clip(s string) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

func utf8Valid(s string) bool { return utf8ValidStr(s) }

func errStr(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

func utf8ValidStr(s string) bool {
	for i := 0; i < len(s); {
		c := s[i]
		if c < 0x80 {
			i++
			continue
		}
		var n int
		switch {
		case c&0xE0 == 0xC0:
			n = 2
		case c&0xF0 == 0xE0:
			n = 3
		case c&0xF8 == 0xF0:
			n = 4
		default:
			return false
		}
		if i+n > len(s) {
			return false
		}
		for j := 1; j < n; j++ {
			if s[i+j]&0xC0 != 0x80 {
				return false
			}
		}
		i += n
	}
	return true
}
