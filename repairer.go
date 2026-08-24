package outfix

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

func jsonRepair(src string, depth int, acts *[]RepairAction) string {
	s := src
	if depth >= 3 && strings.Contains(s, `"{`) {
		if v, pos, found := unwrapStringifiedJSON(s); found {
			s = v
			addAct(acts, ActionUnwrappedStringified,
				fmt.Sprintf("inlined stringified JSON at %d", pos), pos)
		}
	}
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
		if depth >= 3 {
			if v, pos, found := quoteBareValues(s); found {
				s = v
				addAct(acts, ActionQuotedBareValues,
					fmt.Sprintf("quoted bare value(s) starting at %d", pos), pos)
				if json.Valid([]byte(s)) {
					return s
				}
			}
			if v, pos, found := unwrapStringifiedJSON(s); found {
				s = v
				addAct(acts, ActionUnwrappedStringified,
					fmt.Sprintf("inlined stringified JSON at %d", pos), pos)
			}
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

var funcCallDenylist = map[string]bool{
	"def": true, "print": true, "len": true, "range": true, "int": true,
	"str": true, "float": true, "bool": true, "list": true, "dict": true,
	"set": true, "tuple": true, "open": true, "input": true, "type": true,
	"super": true, "isinstance": true, "getattr": true, "setattr": true,
	"if": true, "for": true, "while": true, "return": true, "lambda": true,
	"func": true, "return0": true,
}

func tryConvertFunctionCall(s string, acts *[]RepairAction) (string, bool) {
	t := strings.TrimSpace(s)
	if !funcCallFullRe.MatchString(t) {
		return s, false
	}
	open := strings.IndexByte(t, '(')
	name := strings.TrimSpace(t[:open])
	if funcCallDenylist[strings.ToLower(name)] {
		return s, false
	}
	close := strings.LastIndexByte(t, ')')
	argsBody := t[open+1 : close]

	var segs []string
	depth := 0
	inDQ, inSQ := false, false
	segStart := 0
	for i := 0; i < len(argsBody); i++ {
		c := argsBody[i]
		switch {
		case inDQ:
			if c == '\\' {
				i++
			} else if c == '"' {
				inDQ = false
			}
		case inSQ:
			if c == '\\' {
				i++
			} else if c == '\'' {
				inSQ = false
			}
		case c == '"':
			inDQ = true
		case c == '\'':
			inSQ = true
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		case c == ',' && depth == 0:
			segs = append(segs, argsBody[segStart:i])
			segStart = i + 1
		}
	}
	if strings.TrimSpace(argsBody[segStart:]) != "" {
		segs = append(segs, argsBody[segStart:])
	}

	var b strings.Builder
	b.WriteString(`{"name":`)
	b.Write(mustJSONString(name))
	b.WriteString(`,"arguments":{`)
	wrote := 0
	for _, seg := range segs {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		eq := top_level_eq_index(seg)
		if eq < 0 {
			return s, false
		}
		key := strings.TrimSpace(seg[:eq])
		key = strings.Trim(key, `"'`)
		if key == "" || !validIdent(key) {
			return s, false
		}
		val := strings.TrimSpace(seg[eq+1:])
		jv, ok := convertArgValue(val)
		if !ok {
			return s, false
		}
		if wrote > 0 {
			b.WriteByte(',')
		}
		b.Write(mustJSONString(key))
		b.WriteByte(':')
		b.WriteString(jv)
		wrote++
	}
	b.WriteString("}}")
	out := b.String()
	if !json.Valid([]byte(out)) {
		return s, false
	}
	addAct(acts, ActionConvertedFunctionCall,
		fmt.Sprintf("converted function call %s(...) to tool-call JSON", name), 0)
	return out, true
}

func top_level_eq_index(seg string) int {
	depth := 0
	inDQ, inSQ := false, false
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case inDQ:
			if c == '\\' {
				i++
			} else if c == '"' {
				inDQ = false
			}
		case inSQ:
			if c == '\\' {
				i++
			} else if c == '\'' {
				inSQ = false
			}
		case c == '"':
			inDQ = true
		case c == '\'':
			inSQ = true
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		case c == '=' && depth == 0:
			if i+1 < len(seg) && seg[i+1] == '=' {
				return -1
			}
			return i
		}
	}
	return -1
}

func validIdent(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if !(isASCIILetter(c) || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
		if i == 0 && (c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func mustJSONString(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}

func convertArgValue(val string) (string, bool) {
	if val == "" {
		return "", false
	}
	switch val[0] {
	case '"':
		unq, err := strconv.Unquote(val)
		if err != nil {
			return marshalIfValidJSON(val)
		}
		return string(mustJSONString(unq)), true
	case '\'':
		if len(val) >= 2 && val[len(val)-1] == '\'' {
			inner := val[1 : len(val)-1]
			inner = strings.ReplaceAll(inner, `\'`, `'`)
			return string(mustJSONString(inner)), true
		}
		return "", false
	case '{', '[':
		return marshalIfValidJSON(val)
	}
	switch val {
	case "true", "True":
		return "true", true
	case "false", "False":
		return "false", true
	case "null", "None":
		return "null", true
	}
	if isNumericLiteral(val) {
		return val, true
	}
	if validIdent(val) {
		return string(mustJSONString(val)), true
	}
	return "", false
}

func marshalIfValidJSON(v string) (string, bool) {
	if json.Valid([]byte(v)) {
		return v, true
	}
	return "", false
}

func isNumericLiteral(v string) bool {
	if v == "" {
		return false
	}
	i := 0
	if v[0] == '-' {
		i = 1
	}
	digits, dot := 0, false
	for ; i < len(v); i++ {
		c := v[i]
		if c >= '0' && c <= '9' {
			digits++
		} else if c == '.' && !dot {
			dot = true
		} else {
			return false
		}
	}
	return digits > 0
}

func quoteBareValues(s string) (string, int, bool) {
	mask := buildStringMask(s)
	var b strings.Builder
	b.Grow(len(s))
	firstPos := -1
	i := 0
	for i < len(s) {
		c := s[i]
		if mask[i] || c != ':' {
			b.WriteByte(c)
			i++
			continue
		}
		b.WriteByte(c)
		j := nextNonWS(s, i+1)
		if j < 0 || !isASCIILetter(s[j]) {
			i++
			continue
		}
		k := j
		for k < len(s) && !mask[k] && s[k] != ',' && s[k] != '}' && s[k] != ']' && s[k] != '\n' {
			k++
		}
		valEnd := k
		for valEnd > j && (s[valEnd-1] == ' ' || s[valEnd-1] == '\t') {
			valEnd--
		}
		run := s[j:valEnd]
		if run == "" || isNumericLiteral(run) || run == "true" || run == "false" ||
			run == "null" || run == "True" || run == "False" || run == "None" {
			i++
			continue
		}
		b.WriteString(s[i+1 : j])
		b.WriteString(strconv.Quote(run))
		if firstPos < 0 {
			firstPos = j
		}
		i = valEnd
	}
	if firstPos < 0 {
		return s, -1, false
	}
	return b.String(), firstPos, true
}

func unwrapStringifiedJSON(s string) (string, int, bool) {
	re := regexp.MustCompile(`(?s)"(\{(?:(?:[^"\\]|\\.)*)\})"|"(\[(?:(?:[^"\\]|\\.)*?)\])"`)
	firstPos := -1
	out := re.ReplaceAllStringFunc(s, func(tok string) string {
		unq, err := strconv.Unquote(tok)
		if err != nil {
			return tok
		}
		trimmed := strings.TrimSpace(unq)
		if len(trimmed) < 2 || (trimmed[0] != '{' && trimmed[0] != '[') {
			return tok
		}
		if !json.Valid([]byte(trimmed)) {
			return tok
		}
		if firstPos < 0 {
			firstPos = strings.Index(s, tok)
		}
		return trimmed
	})
	if firstPos < 0 || out == s || !json.Valid([]byte(out)) {
		return s, -1, false
	}
	return out, firstPos, true
}

func tryConvertObjectArgCall(s string, acts *[]RepairAction) (string, bool) {
	t := strings.TrimSpace(s)
	if !funcCallFullRe.MatchString(t) {
		return s, false
	}
	open := strings.IndexByte(t, '(')
	name := strings.TrimSpace(t[:open])
	if funcCallDenylist[strings.ToLower(name)] {
		return s, false
	}
	close := strings.LastIndexByte(t, ')')
	body := strings.TrimSpace(t[open+1 : close])
	if len(body) < 2 || body[0] != '{' || body[len(body)-1] != '}' {
		return s, false
	}
	if !json.Valid([]byte(body)) {
		return s, false
	}
	out := fmt.Sprintf(`{"name":%s,"arguments":%s}`, mustJSONString(name), body)
	addAct(acts, ActionConvertedFunctionCall,
		fmt.Sprintf("converted object-argument call %s({...}) to tool-call JSON", name), 0)
	return out, true
}

var xmlAttrRe = regexp.MustCompile(`([\w.-]+)="([^"]*)"`)

func scanAttrCall(t string) (name string, keys []string, vals []string, selfClose bool, ok bool) {
	if len(t) < 3 || t[0] != '<' {
		return
	}
	i := 1
	start := i
	for i < len(t) && isTagNameByte(t[i]) {
		i++
	}
	name = t[start:i]
	if name == "" {
		return "", nil, nil, false, false
	}
	bad := func() (string, []string, []string, bool, bool) {
		return "", nil, nil, false, false
	}
	for {
		for i < len(t) && (t[i] == ' ' || t[i] == '\t' || t[i] == '\r' || t[i] == '\n') {
			i++
		}
		if i >= len(t) {
			return bad()
		}
		if t[i] == '/' {
			i++
			if i < len(t) && t[i] == '>' {
				selfClose = true
				i++
				break
			}
			return bad()
		}
		if t[i] == '>' {
			i++
			break
		}
		as := i
		for i < len(t) && isTagNameByte(t[i]) {
			i++
		}
		key := t[as:i]
		if key == "" || i >= len(t) || t[i] != '=' {
			return bad()
		}
		i++
		if i >= len(t) || t[i] != '"' {
			return bad()
		}
		i++
		vs := i
		for i < len(t) {
			if t[i] == '"' && (i+1 >= len(t) || t[i+1] == ' ' || t[i+1] == '/' || t[i+1] == '>') {
				break
			}
			i++
		}
		if i >= len(t) {
			return bad()
		}
		keys = append(keys, key)
		vals = append(vals, t[vs:i])
		i++
	}
	rest := strings.TrimSpace(t[i:])
	if !selfClose {
		if rest != "</"+name+">" {
			return bad()
		}
	} else if rest != "" {
		return bad()
	}
	return name, keys, vals, selfClose, true
}

func tryConvertXMLAttrCall(s string, acts *[]RepairAction) (string, bool) {
	name, keys, vals, _, ok := scanAttrCall(strings.TrimSpace(s))
	if !ok || funcCallDenylist[strings.ToLower(name)] {
		return s, false
	}
	var b strings.Builder
	b.WriteString(`{"name":`)
	b.Write(mustJSONString(name))
	b.WriteString(`,"arguments":{`)
	for i := 0; i < len(keys); i++ {
		jv, jok := convertArgValue(strings.TrimSpace(vals[i]))
		if !jok {
			return s, false
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(mustJSONString(keys[i]))
		b.WriteByte(':')
		b.WriteString(jv)
	}
	b.WriteString("}}")
	out := b.String()
	if !json.Valid([]byte(out)) {
		return s, false
	}
	addAct(acts, ActionConvertedFunctionCall,
		fmt.Sprintf("converted XML-attribute tool call <%s/> to JSON", name), 0)
	return out, true
}

var withLineRe = regexp.MustCompile(`^([A-Za-z_]\w*)\s+with\s+(.+)$`)

func tryConvertWithLines(s string, acts *[]RepairAction) (string, bool) {
	t := strings.TrimSpace(s)
	lines := strings.Split(t, "\n")
	type pair struct {
		name string
		args string
	}
	var calls []pair
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		m := withLineRe.FindStringSubmatch(ln)
		if m == nil {
			return s, false
		}
		calls = append(calls, pair{strings.TrimSpace(m[1]), strings.TrimSpace(m[2])})
	}
	if len(calls) == 0 {
		return s, false
	}

	buildOne := func(name, kv string) (string, bool) {
		if funcCallDenylist[strings.ToLower(name)] {
			return "", false
		}
		parts := []string{string(mustJSONString(name))}
		for _, seg := range splitTopLevelCommas(kv) {
			seg = strings.TrimSpace(seg)
			if seg == "" {
				continue
			}
			eq := top_level_eq_index(seg)
			if eq < 0 {
				return "", false
			}
			key := strings.Trim(strings.TrimSpace(seg[:eq]), `"'`)
			if key == "" || !validIdent(key) {
				return "", false
			}
			jv, ok := convertArgValue(strings.TrimSpace(seg[eq+1:]))
			if !ok {
				return "", false
			}
			parts = append(parts, string(mustJSONString(key))+":"+jv)
		}
		out := `{"name":` + parts[0] + `,"arguments":{` + strings.Join(parts[1:], ",") + `}}`
		if !json.Valid([]byte(out)) {
			return "", false
		}
		return out, true
	}

	var outs []string
	for _, c := range calls {
		o, ok := buildOne(c.name, c.args)
		if !ok {
			return s, false
		}
		outs = append(outs, o)
	}
	var out string
	if len(outs) == 1 {
		out = outs[0]
	} else {
		out = "[" + strings.Join(outs, ",") + "]"
	}
	if !json.Valid([]byte(out)) {
		return s, false
	}
	addAct(acts, ActionConvertedFunctionCall,
		fmt.Sprintf("converted %d prose-style call(s) to tool-call JSON", len(outs)), 0)
	return out, true
}

func splitTopLevelCommas(s string) []string {
	var segs []string
	depth := 0
	inDQ, inSQ := false, false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inDQ:
			if c == '\\' {
				i++
			} else if c == '"' {
				inDQ = false
			}
		case inSQ:
			if c == '\\' {
				i++
			} else if c == '\'' {
				inSQ = false
			}
		case c == '"':
			inDQ = true
		case c == '\'':
			inSQ = true
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		case c == ',' && depth == 0:
			segs = append(segs, s[start:i])
			start = i + 1
		}
	}
	segs = append(segs, s[start:])
	return segs
}

func lastResortToolConvert(s string, acts *[]RepairAction) (string, bool) {
	if v, ok := tryConvertXMLAttrCall(s, acts); ok {
		return v, true
	}
	if v, ok := tryConvertWithLines(s, acts); ok {
		return v, true
	}
	if v, ok := tryConvertObjectArgCall(strings.TrimSpace(s), acts); ok {
		return v, true
	}
	if v, ok := tryConvertFunctionCall(strings.TrimSpace(s), acts); ok {
		return v, true
	}
	return s, false
}

func extractSingleCallFromText(s string, acts *[]RepairAction) string {
	t := strings.TrimSpace(s)
	loc := funcCallArgsRe.FindStringIndex(t)
	if loc == nil {
		return s
	}
	start := loc[0]
	absOpen := start + strings.IndexByte(t[start:], '(')
	depth := 0
	inDQ, inSQ := false, false
	end := -1
scan:
	for i := absOpen; i < len(t); i++ {
		c := t[i]
		switch {
		case inDQ:
			if c == '\\' {
				i++
			} else if c == '"' {
				inDQ = false
			}
		case inSQ:
			if c == '\\' {
				i++
			} else if c == '\'' {
				inSQ = false
			}
		case c == '"':
			inDQ = true
		case c == '\'':
			inSQ = true
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
			if depth == 0 && c == ')' {
				end = i + 1
				break scan
			}
		}
	}
	if end < 0 {
		return s
	}
	candidate := t[start:end]
	callName := strings.TrimSpace(t[start:absOpen])
	argsBody := candidate[absOpen-start+1 : len(candidate)-1]
	if funcCallDenylist[strings.ToLower(callName)] || top_level_eq_index(argsBody) < 0 {
		return s
	}
	conv, ok := tryConvertFunctionCall(candidate, acts)
	if !ok {
		return s
	}
	if start > 0 && hasLetter(t[:start]) {
		addAct(acts, ActionRemovedPreamble,
			fmt.Sprintf("removed prose before extracted function call (%d bytes)", start), 0)
	}
	return conv
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
