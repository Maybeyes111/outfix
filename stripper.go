package outfix

import (
	"fmt"
	"regexp"
	"strings"
)

var thinkTagPattern = regexp.MustCompile(`(?i)<\s*/?\s*(thinking|reflection|reasoning|think)\s*>`)

var toolWrapperPattern = regexp.MustCompile(`(?i)<\s*/?\s*(tool_call|tool_calls|function_call|function_calls)\s*>`)

var chatTemplatePattern = regexp.MustCompile(`<\|[a-zA-Z_]+\|>`)

type stripper struct {
	repairs *[]RepairAction
}

type tagEvent struct {
	start   int
	end     int
	name    string
	isClose bool
}

type openTag struct {
	start int
	end   int
	name  string
}

func (st *stripper) stripReasoning(s string) string {
	matches := thinkTagPattern.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s
	}
	events := make([]tagEvent, 0, len(matches))
	for _, m := range matches {
		head := strings.HasPrefix(strings.TrimSpace(s[m[0]:m[1]]), "</")
		name := ""
		if m[2] >= 0 && m[3] >= 0 {
			name = strings.ToLower(strings.TrimSpace(s[m[2]:m[3]]))
		}
		events = append(events, tagEvent{start: m[0], end: m[1], name: name, isClose: head})
	}

	var stack []openTag
	var spans [][2]int
	paired := 0
	orphanCloses := 0
	firstOrphanPos := -1

	for _, ev := range events {
		if !ev.isClose {
			stack = append(stack, openTag{start: ev.start, end: ev.end, name: ev.name})
			continue
		}
		found := -1
		for i := len(stack) - 1; i >= 0; i-- {
			if stack[i].name == ev.name {
				found = i
				break
			}
		}
		if found >= 0 {
			spans = append(spans, [2]int{stack[found].start, ev.end})
			stack = stack[:found]
			paired++
		} else {
			orphanCloses++
			spans = append(spans, [2]int{ev.start, ev.end})
			if firstOrphanPos < 0 {
				firstOrphanPos = ev.start
			}
		}
	}

	if len(stack) > 0 {
		first := stack[0]
		last := stack[len(stack)-1]
		fs := firstStructuralIndex(s[last.end:])
		if fs >= 0 {
			spans = append(spans, [2]int{first.start, last.end + fs})
		} else {
			spans = append(spans, [2]int{first.start, len(s)})
		}
	}

	if len(spans) == 0 {
		return s
	}

	out := removeSpans(s, spans)
	addAct(st.repairs, ActionStrippedThinkBlock,
		fmt.Sprintf("removed %d paired reasoning block(s)", paired), spanStart(spans))
	if orphanCloses > 0 {
		addAct(st.repairs, ActionStrippedOrphanCloseTag,
			fmt.Sprintf("removed %d orphan close tag(s)", orphanCloses), maxInt(firstOrphanPos, 0))
	}
	return out
}

func (st *stripper) stripToolWrappers(s string) string {
	matches := toolWrapperPattern.FindAllStringSubmatchIndex(s, -1)
	if len(matches) > 0 {
		spans := make([][2]int, 0, len(matches))
		first := -1
		for _, m := range matches {
			spans = append(spans, [2]int{m[0], m[1]})
			if first < 0 {
				first = m[0]
			}
		}
		s = removeSpans(s, spans)
		addAct(st.repairs, ActionUnwrappedToolCall,
			fmt.Sprintf("unwrapped %d tool-call tag(s)", len(matches)), first)
	}
	if matches := chatTemplatePattern.FindAllStringSubmatchIndex(s, -1); len(matches) > 0 {
		spans := make([][2]int, 0, len(matches))
		first := -1
		for _, m := range matches {
			spans = append(spans, [2]int{m[0], m[1]})
			if first < 0 {
				first = m[0]
			}
		}
		s = removeSpans(s, spans)
		addAct(st.repairs, ActionStrippedChatTemplate,
			fmt.Sprintf("removed %d chat-template token(s)", len(matches)), first)
	}
	return s
}

func spanStart(spans [][2]int) int {
	best := -1
	for _, sp := range spans {
		if best < 0 || sp[0] < best {
			best = sp[0]
		}
	}
	return maxInt(best, 0)
}

func removeSpans(s string, spans [][2]int) string {
	sortSpans(spans)
	var b strings.Builder
	b.Grow(len(s))
	prev := 0
	for _, sp := range spans {
		if sp[0] > prev {
			b.WriteString(s[prev:sp[0]])
		}
		if sp[1] > prev {
			prev = sp[1]
		}
	}
	b.WriteString(s[prev:])
	return b.String()
}

func sortSpans(spans [][2]int) {
	for i := 1; i < len(spans); i++ {
		for j := i; j > 0 && spans[j][0] < spans[j-1][0]; j-- {
			spans[j], spans[j-1] = spans[j-1], spans[j]
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (st *stripper) stripFences(s string) string {
	regions := findFencedRegions(s)
	if len(regions) > 0 {
		r := regions[pickFenceRegion(regions)]
		body := strings.TrimRight(r.body, "\r\n")
		s = s[:r.openerStart] + body + s[r.closeEnd:]
		addAct(st.repairs, ActionStrippedCodeFence, fenceDesc(r.lang), r.openerStart)
	}

	t := strings.TrimRight(s, " \t\r\n")
	if strings.HasSuffix(t, "```") || strings.HasSuffix(t, "~~~") {
		li := strings.LastIndexByte(t, '\n')
		candidate := t
		pos := 0
		if li >= 0 {
			candidate = t[li+1:]
			pos = li + 1
		}
		if isPureFence(strings.TrimSpace(candidate)) {
			s = s[:pos]
			addAct(st.repairs, ActionStrippedCodeFence, "removed stray trailing code fence", pos)
		}
	}
	return s
}

type fenceRegion struct {
	openerStart int
	closeEnd    int
	body        string
	lang        string
}

func isPureFence(tok string) bool {
	ch, _, lang, ok := parseFenceLine(tok)
	return ok && lang == "" && (ch == '`' || ch == '~')
}

func parseFenceLine(tok string) (ch byte, runLen int, lang string, ok bool) {
	if tok == "" {
		return 0, 0, "", false
	}
	c := tok[0]
	if c != '`' && c != '~' {
		return 0, 0, "", false
	}
	n := 0
	for n < len(tok) && tok[n] == c {
		n++
	}
	if n < 3 {
		return 0, 0, "", false
	}
	rest := tok[n:]
	if rest == "" {
		return c, n, "", true
	}
	for i := 0; i < len(rest); i++ {
		r := rest[i]
		if !isASCIILetter(r) && !(r >= '0' && r <= '9') &&
			r != '-' && r != '+' && r != '.' && r != '_' {
			return 0, 0, "", false
		}
	}
	return c, n, strings.ToLower(rest), true
}

func pickFenceRegion(regions []fenceRegion) int {
	for i, r := range regions {
		t := strings.TrimSpace(r.body)
		if t != "" && (t[0] == '{' || t[0] == '[' || (t[0] == '<' && len(t) > 1 && isASCIILetter(t[1]))) {
			return i
		}
	}
	best := 0
	longest := -1
	for i, r := range regions {
		if len(r.body) > longest {
			longest = len(r.body)
			best = i
		}
	}
	return best
}

func findFencedRegions(s string) []fenceRegion {
	var regions []fenceRegion
	lineStart := 0
	for lineStart < len(s) {
		if len(regions) > 0 && lineStart < regions[len(regions)-1].closeEnd {
			lineStart = regions[len(regions)-1].closeEnd
			continue
		}
		nl := strings.IndexByte(s[lineStart:], '\n')
		lineEnd := len(s)
		nextLine := len(s)
		if nl >= 0 {
			lineEnd = lineStart + nl
			nextLine = lineEnd + 1
		}
		fc, _, lang, ok := parseFenceLine(strings.TrimSpace(s[lineStart:lineEnd]))
		if !ok {
			lineStart = nextLine
			continue
		}
		bodyStart := nextLine
		bodyTo := -1
		closeEnd := -1
		scan := bodyStart
		for scan < len(s) {
			nl2 := strings.IndexByte(s[scan:], '\n')
			le2 := len(s)
			nl3 := len(s)
			if nl2 >= 0 {
				le2 = scan + nl2
				nl3 = le2 + 1
			}
			cc, _, cl, cok := parseFenceLine(strings.TrimSpace(s[scan:le2]))
			if cok && cl == "" && cc == fc {
				bodyTo = scan
				closeEnd = nl3
				break
			}
			scan = nl3
		}
		var body string
		if closeEnd >= 0 {
			body = s[bodyStart:bodyTo]
		} else {
			body = s[bodyStart:]
			closeEnd = len(s)
		}
		regions = append(regions, fenceRegion{
			openerStart: lineStart,
			closeEnd:    closeEnd,
			body:        body,
			lang:        lang,
		})
		lineStart = closeEnd
	}
	return regions
}

func fenceDesc(lang string) string {
	if lang == "" {
		return "stripped markdown code fence"
	}
	return fmt.Sprintf("stripped %s code fence", lang)
}

var boxRuneSet = func() map[rune]bool {
	m := map[rune]bool{}
	for _, r := range "╭╮╰╯│┃┌┐└┘├┤┬┴┼─━═·" {
		m[r] = true
	}
	return m
}()

func (st *stripper) stripBoxLines(s string) string {
	if !strings.ContainsAny(s, "╭╮╰╯│┌┐└┘├┤┬┴┼━═·") {
		return s
	}
	lines := strings.Split(s, "\n")
	kept := make([]string, 0, len(lines))
	dropped := 0
	firstPos := -1
	offset := 0
	for _, ln := range lines {
		if isPureBoxLine(ln) {
			dropped++
			if firstPos < 0 {
				firstPos = offset
			}
		} else {
			kept = append(kept, ln)
		}
		offset += len(ln) + 1
	}
	if dropped == 0 || len(kept) == 0 {
		return s
	}
	addAct(st.repairs, ActionStrippedBoxDrawing,
		fmt.Sprintf("removed %d box-drawing line(s)", dropped), maxInt(firstPos, 0))
	return strings.Join(kept, "\n")
}

func isPureBoxLine(ln string) bool {
	hasBox := false
	for _, r := range strings.TrimRight(ln, "\r") {
		if r == ' ' || r == '\t' {
			continue
		}
		if boxRuneSet[r] {
			hasBox = true
		} else {
			return false
		}
	}
	return hasBox
}

func (st *stripper) stripPreamble(s string) string {
	idx := firstStructuralIndex(s)
	if idx <= 0 {
		return s
	}
	if !hasLetter(s[:idx]) {
		return s
	}
	addAct(st.repairs, ActionRemovedPreamble,
		fmt.Sprintf("removed preamble before payload (%d bytes)", idx), 0)
	return s[idx:]
}

func (st *stripper) stripTail(s string) string {
	if start, end, ok := jsonRootSpan(s); ok {
		tail := s[end:]
		tt := strings.TrimSpace(tail)
		if tt == "" {
			return s
		}
		switch {
		case tt[0] == '{' || tt[0] == '[':
			return s
		case tt[0] == '<' && (len(tt) == 1 || tt[1] != '/'):
			return s
		case tt[0] == '<':
			name := tagNameOfClose(tt)
			if name != "" && !strings.Contains(s[start:end], "<"+name) {
				addAct(st.repairs, ActionStrippedXMLBleed,
					fmt.Sprintf("stripped trailing XML bleed </%s>", name), end)
				return s[:end]
			}
			return s
		default:
			addAct(st.repairs, ActionRemovedPostamble,
				fmt.Sprintf("removed postamble after JSON payload (%d bytes)", len(tail)), end)
			return s[:end]
		}
	}

	return s
}
