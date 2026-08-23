package outfix

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

func jsonRepair(src string, depth int, acts *[]RepairAction) string {
	s := src
	if json.Valid([]byte(s)) {
		return s
	}

	if strings.IndexByte(s, '\'') >= 0 && depth >= 2 {
		if v, pos, found := fixSingleQuotes(s); found {
			s = v
			addAct(acts, ActionFixedSingleQuotes,
				fmt.Sprintf("converted single-quoted key/value(s) starting at %d", pos), pos)
			if json.Valid([]byte(s)) {
				return s
			}
		}
	}

	if strings.Contains(s, "True") || strings.Contains(s, "False") || strings.Contains(s, "None") {
		if v, pos, found := fixPythonLiterals(s); found {
			s = v
			addAct(acts, ActionFixedPythonLiterals,
				fmt.Sprintf("replaced Python literal(s) starting at %d", pos), pos)
			if json.Valid([]byte(s)) {
				return s
			}
		}
	}

	if strings.IndexByte(s, ',') >= 0 {
		if v, pos, found := fixTrailingCommas(s); found {
			s = v
			addAct(acts, ActionFixedTrailingComma,
				fmt.Sprintf("removed trailing comma(s) starting at %d", pos), pos)
			if json.Valid([]byte(s)) {
				return s
			}
		}
	}

	if depth >= 2 {
		if (strings.IndexByte(s, '{') >= 0 || strings.IndexByte(s, ',') >= 0) &&
			strings.IndexByte(s, ':') >= 0 {
			if v, pos, found := quoteBareKeys(s); found {
				s = v
				addAct(acts, ActionQuotedBareKeys,
					fmt.Sprintf("quoted bare key(s) starting at %d", pos), pos)
				if json.Valid([]byte(s)) {
					return s
				}
			}
		}
		if strings.IndexByte(s, '\n') >= 0 || strings.IndexByte(s, ';') >= 0 ||
			strings.Contains(s, `\n`) {
			if v, found := mergeNDJSON(s); found {
				s = v
				addAct(acts, ActionMergedNDJSON,
					"merged newline-delimited JSON values into one array", 0)
				if json.Valid([]byte(s)) {
					return s
				}
			}
		}
		if v, pos, found := balanceTruncatedJSON(s, depth); found {
			s = v
			addAct(acts, ActionRepairedTruncatedJSON,
				fmt.Sprintf("closed truncated structure near %d", pos), pos)
		}
	}

	return s
}

func nextNonWS(s string, i int) int {
	for i < len(s) {
		switch s[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return -1
}

func isIdentByte(c byte) bool {
	return isASCIILetter(c) || (c >= '0' && c <= '9') || c == '_' || c == '-'
}

func fixPythonLiterals(s string) (string, int, bool) {
	mask := buildStringMask(s)
	lits := []string{"True", "False", "None"}
	repl := map[string]string{"True": "true", "False": "false", "None": "null"}
	var b strings.Builder
	b.Grow(len(s))
	firstPos := -1
	i := 0
	for i < len(s) {
		if mask[i] || !isASCIILetter(s[i]) {
			b.WriteByte(s[i])
			i++
			continue
		}
		matched := ""
		for _, lit := range lits {
			if i+len(lit) <= len(s) && s[i:i+len(lit)] == lit {
				matched = lit
				break
			}
		}
		if matched == "" {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := i + len(matched)
		prevOK := i == 0 || !isIdentByte(s[i-1])
		nextOK := end >= len(s) || !isIdentByte(s[end])
		if prevOK && nextOK {
			if firstPos < 0 {
				firstPos = i
			}
			b.WriteString(repl[matched])
			i = end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	if firstPos < 0 {
		return s, -1, false
	}
	return b.String(), firstPos, true
}

func fixTrailingCommas(s string) (string, int, bool) {
	mask := buildStringMask(s)
	var b strings.Builder
	b.Grow(len(s))
	firstPos := -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ',' && !mask[i] {
			j := nextNonWS(s, i+1)
			if j >= 0 && (s[j] == '}' || s[j] == ']') {
				if firstPos < 0 {
					firstPos = i
				}
				continue
			}
		}
		b.WriteByte(c)
	}
	if firstPos < 0 {
		return s, -1, false
	}
	return b.String(), firstPos, true
}

func fixSingleQuotes(s string) (string, int, bool) {
	mask := buildStringMask(s)
	var b strings.Builder
	b.Grow(len(s))
	firstPos := -1
	i := 0
	for i < len(s) {
		c := s[i]
		if c != '\'' || mask[i] {
			b.WriteByte(c)
			i++
			continue
		}
		p := i
		for p > 0 {
			ch := s[p-1]
			if ch != ' ' && ch != '\t' && ch != '\n' && ch != '\r' {
				break
			}
			p--
		}
		openOK := p > 0 && (s[p-1] == '{' || s[p-1] == '[' || s[p-1] == ',' || s[p-1] == ':')
		if !openOK {
			b.WriteByte(c)
			i++
			continue
		}
		closeIdx := findSingleQuoteClose(s, i+1)
		if closeIdx < 0 {
			b.WriteByte(c)
			i++
			continue
		}
		j := nextNonWS(s, closeIdx+1)
		closeOK := j >= 0 && (s[j] == ':' || s[j] == ',' || s[j] == '}' || s[j] == ']')
		if !closeOK {
			b.WriteByte(c)
			i++
			continue
		}
		if firstPos < 0 {
			firstPos = i
		}
		b.WriteByte('"')
		for k := i + 1; k < closeIdx; k++ {
			if s[k] == '\\' && k+1 < closeIdx && s[k+1] == '\'' {
				b.WriteByte('\'')
				k++
				continue
			}
			b.WriteByte(s[k])
		}
		b.WriteByte('"')
		i = closeIdx + 1
	}
	if firstPos < 0 {
		return s, -1, false
	}
	return b.String(), firstPos, true
}

func findSingleQuoteClose(s string, from int) int {
	for k := from; k < len(s); k++ {
		c := s[k]
		switch c {
		case '\n', '"':
			return -1
		case '\\':
			k++
		case '\'':
			if k-from > 4096 {
				return -1
			}
			return k
		}
	}
	return -1
}

func quoteBareKeys(s string) (string, int, bool) {
	mask := buildStringMask(s)
	var b strings.Builder
	b.Grow(len(s) + 16)
	firstPos := -1
	i := 0
	for i < len(s) {
		c := s[i]
		if mask[i] || (c != '{' && c != ',') {
			b.WriteByte(c)
			i++
			continue
		}
		j := nextNonWS(s, i+1)
		if j < 0 || (!isASCIILetter(s[j]) && s[j] != '_') {
			b.WriteByte(c)
			i++
			continue
		}
		k := j
		for k < len(s) && isIdentByte(s[k]) {
			k++
		}
		m := nextNonWS(s, k)
		if m < 0 || s[m] != ':' {
			b.WriteByte(c)
			i++
			continue
		}
		if firstPos < 0 {
			firstPos = j
		}
		b.WriteByte(c)
		b.WriteString(s[i+1 : j])
		b.WriteByte('"')
		b.WriteString(s[j:k])
		b.WriteByte('"')
		i = k
	}
	if firstPos < 0 {
		return s, -1, false
	}
	return b.String(), firstPos, true
}

func mergeNDJSON(s string) (string, bool) {
	mask := buildStringMask(s)
	var vals []string
	i := 0
	for i < len(s) {
		c := s[i]
		if mask[i] {
			i++
			continue
		}
		if c == '{' || c == '[' {
			end := matchBracketFrom(s, mask, i)
			if end < 0 {
				return s, false
			}
			vals = append(vals, strings.TrimSpace(s[i:end+1]))
			i = end + 1
			continue
		}
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == ',' || c == ';' {
			i++
			continue
		}
		if c == '\\' && i+1 < len(s) && (s[i+1] == 'n' || s[i+1] == 't' || s[i+1] == 'r') {
			i += 2
			continue
		}
		return s, false
	}
	if len(vals) < 2 {
		return s, false
	}
	for _, v := range vals {
		if !json.Valid([]byte(v)) {
			return s, false
		}
	}
	merged := "[" + strings.Join(vals, ",") + "]"
	if json.Valid([]byte(merged)) {
		return merged, true
	}
	return s, false
}

func matchBracketFrom(s string, mask []bool, start int) int {
	open, close := s[start], byte(0)
	switch open {
	case '{':
		close = '}'
	case '[':
		close = ']'
	default:
		return -1
	}
	depth := 0
	for i := start; i < len(s); i++ {
		if mask[i] {
			continue
		}
		c := s[i]
		if c == open {
			depth++
		} else if c == close {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func balanceTruncatedJSON(s string, depth int) (string, int, bool) {
	mask := buildStringMask(s)
	var stack []byte
	for i := 0; i < len(s); i++ {
		if mask[i] {
			continue
		}
		switch s[i] {
		case '{', '[':
			stack = append(stack, s[i])
		case '}', ']':
			if len(stack) == 0 {
				return s, -1, false
			}
			top := stack[len(stack)-1]
			open := byte('{')
			if s[i] == ']' {
				open = '['
			}
			if top != open {
				return s, -1, false
			}
			stack = stack[:len(stack)-1]
		}
	}
	inStr := len(mask) > 0 && mask[len(mask)-1]

	trailing := len(s)
	for trailing > 0 {
		c := s[trailing-1]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			trailing--
			continue
		}
		break
	}
	origT := s[:trailing]
	t := origT
	if inStr {
		t += `"`
	}

	if len(t) > 0 && t[len(t)-1] == ':' && depth >= 2 {
		t += "null"
	}
	if depth >= 3 {
		if comp, k, ok := completePartialLiteral(t); ok {
			t = t[:k] + comp
		}
	}
	if len(t) > 0 && t[len(t)-1] == ',' {
		t = t[:len(t)-1]
	}

	if len(stack) == 0 {
		if t != origT && json.Valid([]byte(t)) {
			return t, maxInt(len(t)-1, 0), true
		}
		return s, -1, false
	}

	cand := t + closersFor(stack)
	if json.Valid([]byte(cand)) {
		return cand, len(t), true
	}
	return s, -1, false
}

func closersFor(stack []byte) string {
	out := make([]byte, len(stack))
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			out[len(stack)-1-i] = '}'
		} else {
			out[len(stack)-1-i] = ']'
		}
	}
	return string(out)
}

func completePartialLiteral(t string) (string, int, bool) {
	n := len(t)
	k := n
	for k > 0 && isASCIILetter(t[k-1]) {
		k--
	}
	run := t[k:n]
	if run == "" || len(run) >= 5 {
		return "", -1, false
	}
	switch run {
	case "t", "tr", "tru":
		return "true", k, true
	case "f", "fa", "fal", "fals":
		return "false", k, true
	case "n", "nu", "nul":
		return "null", k, true
	}
	return "", -1, false
}

func xmlRepair(src string, acts *[]RepairAction) string {
	s := src
	var b strings.Builder
	b.Grow(len(s) + 32)
	type openTagX struct {
		name string
		pos  int
	}
	var stack []openTagX

	i := 0
	for i < len(s) {
		c := s[i]
		if c != '<' {
			b.WriteByte(c)
			i++
			continue
		}
		rest := s[i:]
		switch {
		case strings.HasPrefix(rest, "<![CDATA["):
			j := strings.Index(rest, "]]>")
			if j < 0 {
				b.WriteString(rest)
				i = len(s)
			} else {
				b.WriteString(rest[:j+3])
				i += j + 3
			}
		case strings.HasPrefix(rest, "<!--"):
			j := strings.Index(rest, "-->")
			if j < 0 {
				b.WriteString(rest)
				i = len(s)
			} else {
				b.WriteString(rest[:j+3])
				i += j + 3
			}
		case strings.HasPrefix(rest, "<?"):
			j := strings.Index(rest, "?>")
			if j < 0 {
				b.WriteString(rest)
				i = len(s)
			} else {
				b.WriteString(rest[:j+2])
				i += j + 2
			}
		case strings.HasPrefix(rest, "<!"):
			j := strings.IndexByte(rest, '>')
			if j < 0 {
				b.WriteString(rest)
				i = len(s)
			} else {
				b.WriteString(rest[:j+1])
				i += j + 1
			}
		case strings.HasPrefix(rest, "</"):
			name, end := parseXMLCloseTag(rest)
			if name == "" {
				b.WriteByte(c)
				i++
				continue
			}
			found := -1
			for k := len(stack) - 1; k >= 0; k-- {
				if stack[k].name == name {
					found = k
					break
				}
			}
			if found < 0 {
				addAct(acts, ActionFixedXMLOrphanClose,
					fmt.Sprintf("removed orphan closing tag </%s>", name), i)
				i += end
				continue
			}
			if found < len(stack)-1 {
				addAct(acts, ActionFixedMismatchedXMLTags,
					fmt.Sprintf("closed %d unclosed tag(s) before </%s>", len(stack)-found-1, name), i)
				for k := len(stack) - 1; k > found; k-- {
					b.WriteString("</" + stack[k].name + ">")
				}
			}
			stack = stack[:found]
			b.WriteString(rest[:end])
			i += end
		default:
			name, end := parseXMLOpenTag(rest)
			if name == "" {
				b.WriteByte(c)
				i++
				continue
			}
			inner := strings.TrimRight(rest[1:end-1], " \t\r\n")
			selfClose := strings.HasSuffix(inner, "/")
			b.WriteString(rest[:end])
			if !selfClose {
				stack = append(stack, openTagX{name: name, pos: i})
			}
			i += end
		}
	}

	if len(stack) > 0 {
		addAct(acts, ActionClosedUnclosedXMLTags,
			fmt.Sprintf("appended %d missing closing tag(s)", len(stack)), b.Len())
		for k := len(stack) - 1; k >= 0; k-- {
			b.WriteString("</" + stack[k].name + ">")
		}
	}
	return b.String()
}

func parseXMLCloseTag(s string) (string, int) {
	j := 2
	start := j
	if j >= len(s) || !isASCIILetter(s[j]) {
		return "", -1
	}
	for j < len(s) && isTagNameByte(s[j]) {
		j++
	}
	name := s[start:j]
	for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	if j >= len(s) || s[j] != '>' {
		return "", -1
	}
	return name, j + 1
}

func parseXMLOpenTag(s string) (string, int) {
	if len(s) < 2 || s[0] != '<' || !isASCIILetter(s[1]) {
		return "", -1
	}
	j := 1
	start := j
	for j < len(s) && isTagNameByte(s[j]) {
		j++
	}
	name := s[start:j]
	inDQ := false
	inSQ := false
	for j < len(s) {
		c := s[j]
		if inDQ {
			if c == '"' {
				inDQ = false
			}
			j++
			continue
		}
		if inSQ {
			if c == '\'' {
				inSQ = false
			}
			j++
			continue
		}
		switch c {
		case '"':
			inDQ = true
		case '\'':
			inSQ = true
		case '<':
			return "", -1
		case '>':
			return name, j + 1
		}
		j++
	}
	return "", -1
}

var bleedTagNames = map[string]bool{
	"content":            true,
	"system":             true,
	"prompt":             true,
	"response":           true,
	"assistant":          true,
	"user":               true,
	"message":            true,
	"answer":             true,
	"output":             true,
	"result":             true,
	"tool_response":      true,
	"assistant_response": true,
	"tool_call":          true,
	"function_result":    true,
}

func stripOrphanCloseTags(src string, acts *[]RepairAction) string {
	s := src
	var b strings.Builder
	b.Grow(len(s))
	type openName struct {
		name string
	}
	var stack []openName
	dropped := 0
	firstPos := -1

	i := 0
	for i < len(s) {
		c := s[i]
		if c != '<' {
			b.WriteByte(c)
			i++
			continue
		}
		rest := s[i:]
		switch {
		case strings.HasPrefix(rest, "<![CDATA["):
			j := strings.Index(rest, "]]>")
			if j < 0 {
				b.WriteString(rest)
				i = len(s)
			} else {
				b.WriteString(rest[:j+3])
				i += j + 3
			}
		case strings.HasPrefix(rest, "<!--"):
			j := strings.Index(rest, "-->")
			if j < 0 {
				b.WriteString(rest)
				i = len(s)
			} else {
				b.WriteString(rest[:j+3])
				i += j + 3
			}
		case strings.HasPrefix(rest, "</"):
			name, end := parseXMLCloseTag(rest)
			if name == "" {
				b.WriteByte(c)
				i++
				continue
			}
			found := false
			for k := len(stack) - 1; k >= 0; k-- {
				if stack[k].name == name {
					stack = stack[:k]
					found = true
					break
				}
			}
			if found {
				b.WriteString(rest[:end])
				i += end
				continue
			}
			if bleedTagNames[name] {
				dropped++
				if firstPos < 0 {
					firstPos = i
				}
				i += end
				continue
			}
			b.WriteString(rest[:end])
			i += end
		default:
			name, end := parseXMLOpenTag(rest)
			if name == "" {
				b.WriteByte(c)
				i++
				continue
			}
			inner := strings.TrimRight(rest[1:end-1], " \t\r\n")
			if !strings.HasSuffix(inner, "/") {
				stack = append(stack, openName{name: name})
			}
			b.WriteString(rest[:end])
			i += end
		}
	}

	if dropped > 0 {
		addAct(acts, ActionFixedXMLOrphanClose,
			fmt.Sprintf("removed %d orphan template tag(s) from text output", dropped), maxInt(firstPos, 0))
		return b.String()
	}
	return s
}

func validXMLDoc(s string) bool {
	dec := xml.NewDecoder(strings.NewReader(s))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return true
		}
		if err != nil {
			return false
		}
	}
}
