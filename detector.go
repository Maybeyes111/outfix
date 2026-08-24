package outfix

import (
	"encoding/json"
	"regexp"
	"strings"
)

type artifactReport struct {
	hasOpenThink     bool
	hasCloseThink    bool
	hasFuncCall      bool
	hasLooseCall     bool
	hasStringified   bool
	hasToolWrapper   bool
	hasTemplateBleed bool
	hasCodeFence     bool
	hasPreamble      bool
	hasTail          bool
	tailIsXMLBleed   bool
	hasBoxDrawing    bool
	hasUnicodeEsc    bool
	hasCR            bool
	validJSON        bool
	jsonIntent       bool
	xmlIntent        bool
	malformedJSON    bool
	modelGuess       ModelFamily
}

var unicodeEscNeedles = []string{`\u003c`, `\u003e`, `\u0026`, `\u003C`, `\u003E`, `\u0026`}

var funcCallFullRe = regexp.MustCompile(`^\s*[A-Za-z_]\w*(?:\.\w+)*\s*\((?s:.*)\)\s*$`)

var funcCallArgsRe = regexp.MustCompile(`[A-Za-z_]\w*(?:\.\w+)*\s*\(`)

func detect(raw string) artifactReport {
	var rep artifactReport
	if raw == "" {
		return rep
	}
	lower := strings.ToLower(raw)
	rep.hasOpenThink = containsAny(lower, "<think", "<reasoning", "<reflection")
	rep.hasCloseThink = containsAny(lower, "</think", "</reasoning", "</reflection")
	rep.hasFuncCall = funcCallFullRe.MatchString(strings.TrimSpace(raw))
	hasLoose := funcCallArgsRe.MatchString(raw)
	rep.hasLooseCall = hasLoose && !rep.hasFuncCall
	rep.hasStringified = strings.Contains(raw, `"{`)
	rep.hasToolWrapper = containsAny(lower, "<tool_call", "</tool_call", "<function_call", "</function_call")
	rep.hasTemplateBleed = strings.Contains(lower, "<|")
	rep.hasCodeFence = strings.Contains(raw, "```") || strings.Contains(raw, "~~~")
	rep.hasBoxDrawing = strings.ContainsAny(raw, "╭╮╰╯│┌┐└┘├┤┬┴┼━═")
	rep.hasCR = strings.Contains(raw, "\r")
	for _, n := range unicodeEscNeedles {
		if strings.Contains(raw, n) {
			rep.hasUnicodeEsc = true
			break
		}
	}

	start := firstStructuralIndex(raw)
	if start > 0 && hasLetter(raw[:start]) {
		rep.hasPreamble = true
	}

	rep.validJSON = json.Valid([]byte(strings.TrimSpace(raw)))

	jsonStart := firstIndexAnyByte(raw, '{', '[')
	xmlStart := firstXMLTagIndex(raw)

	switch {
	case rep.hasFuncCall:
		rep.jsonIntent = false
		rep.xmlIntent = false
	case jsonStart >= 0 && (xmlStart < 0 || jsonStart < xmlStart):
		rep.jsonIntent = true
		rep.malformedJSON = !rep.validJSON
		if _, end, ok := jsonRootSpan(raw); ok {
			tail := raw[end:]
			if strings.TrimSpace(tail) != "" {
				rep.hasTail = true
				rep.tailIsXMLBleed = isXMLBleedTail(raw[:end], tail)
			}
		} else {
			rep.malformedJSON = true
		}
	case xmlStart >= 0:
		rep.xmlIntent = true
	}

	if rep.hasOpenThink {
		rep.modelGuess = ModelQwen
	} else if rep.hasCloseThink {
		rep.modelGuess = ModelDeepSeek
	} else if rep.hasBoxDrawing {
		rep.modelGuess = ModelGLM
	}
	return rep
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func firstIndexAnyByte(s string, b1, b2 byte) int {
	i := strings.IndexByte(s, b1)
	j := strings.IndexByte(s, b2)
	switch {
	case i < 0:
		return j
	case j < 0:
		return i
	default:
		return minInt(i, j)
	}
}

func firstStructuralIndex(s string) int {
	j := firstIndexAnyByte(s, '{', '[')
	x := firstXMLTagIndex(s)
	switch {
	case j < 0:
		return x
	case x < 0:
		return j
	default:
		return minInt(j, x)
	}
}

func firstXMLTagIndex(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != '<' || i+1 >= len(s) {
			continue
		}
		c := s[i+1]
		if isASCIILetter(c) || c == '/' || c == '?' || c == '!' {
			return i
		}
	}
	return -1
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func hasLetter(s string) bool {
	for i := 0; i < len(s); i++ {
		if isASCIILetter(s[i]) {
			return true
		}
	}
	for _, r := range s {
		if r > 127 && isUnicodeLetter(r) {
			return true
		}
	}
	return false
}

func isUnicodeLetter(r rune) bool {
	switch {
	case r >= 0x00C0 && r <= 0x024F,
		r >= 0x0370 && r <= 0x03FF,
		r >= 0x0400 && r <= 0x04FF,
		r >= 0x3040 && r <= 0x30FF,
		r >= 0x4E00 && r <= 0x9FFF,
		r >= 0xAC00 && r <= 0xD7AF:
		return true
	}
	return false
}

func buildStringMask(s string) []bool {
	mask := make([]bool, len(s))
	in := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if in {
			mask[i] = true
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				in = false
			}
		} else if c == '"' {
			in = true
			mask[i] = true
		}
	}
	return mask
}

func jsonRootSpan(s string) (start, end int, ok bool) {
	start = firstIndexAnyByte(s, '{', '[')
	if start < 0 {
		return -1, -1, false
	}
	mask := buildStringMask(s)
	openOf := map[byte]byte{'}': '{', ']': '['}
	var stack []byte
	for i := start; i < len(s); i++ {
		if mask[i] {
			continue
		}
		switch s[i] {
		case '{', '[':
			stack = append(stack, s[i])
		case '}', ']':
			if len(stack) == 0 || stack[len(stack)-1] != openOf[s[i]] {
				return start, -1, false
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return start, i + 1, true
			}
		}
	}
	return start, -1, false
}

func isXMLBleedTail(head, tail string) bool {
	t := strings.TrimSpace(tail)
	idx := strings.Index(t, "</")
	if idx < 0 {
		return false
	}
	name := tagNameOfClose(t[idx:])
	if name == "" {
		return false
	}
	return !strings.Contains(head, "<"+name)
}

func tagNameOfClose(s string) string {
	i := strings.Index(s, "</")
	if i < 0 {
		return ""
	}
	j := i + 2
	start := j
	if j >= len(s) || !isASCIILetter(s[j]) {
		return ""
	}
	for j < len(s) && isTagNameByte(s[j]) {
		j++
	}
	if j == start {
		return ""
	}
	return s[start:j]
}

func isTagNameByte(c byte) bool {
	return isASCIILetter(c) || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.' || c == ':'
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
