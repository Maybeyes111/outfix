package outfix

import (
	"fmt"
	"strings"
)

func normalizeOutput(s string, acts *[]RepairAction) string {
	out := s

	if idx, found := findUnicodeEscape(out); found {
		out = replaceUnicodeEscapes(out)
		addAct(acts, ActionNormalizedUnicodeEsc,
			fmt.Sprintf("decoded unicode escape(s) starting at %d", maxInt(idx, 0)), maxInt(idx, 0))
	}

	if strings.Contains(out, "\r") {
		pos := strings.IndexByte(out, '\r')
		out = strings.ReplaceAll(out, "\r\n", "\n")
		out = strings.ReplaceAll(out, "\r", "\n")
		addAct(acts, ActionNormalizedLineEndings,
			fmt.Sprintf("normalized CRLF/CR to LF at %d", pos), pos)
	}

	out = collapseWhitespace(out, acts)
	return out
}

var escTargets = []struct {
	seq string
	re  string
}{
	{`\u003c`, "<"},
	{`\u003C`, "<"},
	{`\u003e`, ">"},
	{`\u003E`, ">"},
	{`\u0026`, "&"},
}

func findUnicodeEscape(s string) (int, bool) {
	best := -1
	for _, t := range escTargets {
		if i := strings.Index(s, t.seq); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best, best >= 0
}

func replaceUnicodeEscapes(s string) string {
	for _, t := range escTargets {
		s = strings.ReplaceAll(s, t.seq, t.re)
	}
	return s
}

func collapseWhitespace(s string, acts *[]RepairAction) string {
	mask := buildStringMask(s)
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	nlRun := 0
	spStart := -1
	prevEmittedNL := true

	flushNL := func() {
		n := nlRun
		if n > 2 {
			n = 2
		}
		for k := 0; k < n; k++ {
			b.WriteByte('\n')
		}
		if n > 0 {
			prevEmittedNL = true
		}
		nlRun = 0
	}

	for i < len(s) {
		c := s[i]
		if mask[i] {
			flushNL()
			if spStart >= 0 {
				if prevEmittedNL {
					b.WriteString(s[spStart:i])
				} else {
					b.WriteByte(' ')
				}
				spStart = -1
			}
			b.WriteByte(c)
			prevEmittedNL = false
			i++
			continue
		}
		switch {
		case c == '\n':
			spStart = -1
			nlRun++
			i++
		case c == ' ' || c == '\t':
			if spStart < 0 {
				spStart = i
			}
			i++
		default:
			flushNL()
			if spStart >= 0 {
				if prevEmittedNL {
					b.WriteString(s[spStart:i])
				} else {
					b.WriteByte(' ')
				}
				spStart = -1
			}
			prevEmittedNL = false
			b.WriteByte(c)
			i++
		}
	}
	flushNL()

	res := strings.Trim(b.String(), "\n")
	if res != s {
		addAct(acts, ActionCollapsedWhitespace, "collapsed excess whitespace", 0)
	}
	return res
}
