package outfix

import (
	"encoding/json"
	"strings"
)

func Fix(input string) (string, error) {
	r, err := New(defaultFixOptions()).Process(input)
	if err != nil {
		return input, err
	}
	return r.Output, nil
}

type Processor struct {
	opts Options
}

func New(opts Options) *Processor {
	return &Processor{opts: opts.withDefaults()}
}

func (p *Processor) Process(input string) (res Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			res = Result{Output: input, Cleaned: false, Confidence: 0, ModelGuess: ModelGeneric}
			err = ErrRepairFailed
		}
	}()

	if input == "" {
		return Result{}, nil
	}

	rep := detect(input)
	guess := p.opts.ModelHint
	if guess == ModelGeneric {
		guess = rep.modelGuess
	}

	if p.fastPath(rep, input) {
		conf := 0.0
		ft := strings.TrimSpace(input)
		if rep.validJSON || (rep.xmlIntent && validXMLDoc(ft)) {
			conf = 1.0
		}
		return Result{Output: input, Cleaned: false, Confidence: conf, ModelGuess: guess}, nil
	}

	out := input
	var acts []RepairAction
	st := &stripper{repairs: &acts}

	if p.opts.StripReasoning {
		out = st.stripReasoning(out)
	}
	out = st.stripToolWrappers(out)
	out = st.stripFences(out)
	out = st.stripBoxLines(out)

	j := firstIndexAnyByte(out, '{', '[')
	x := firstXMLTagIndex(out)
	jsonShape := j >= 0 && (x < 0 || j < x)
	xmlOpenShape := x >= 0 && (j < 0 || x < j) &&
		(isASCIILetter(out[x+1]) || out[x+1] == '?' || out[x+1] == '!')
	structured := jsonShape || xmlOpenShape

	if p.opts.TargetFormat != FormatPlainText && structured {
		out = st.stripPreamble(out)
		out = st.stripTail(out)
		ft := strings.TrimSpace(out)
		switch {
		case (strings.HasPrefix(ft, "{") || strings.HasPrefix(ft, "[")) && p.opts.RepairJSON:
			out = jsonRepair(out, p.opts.MaxRepairDepth, &acts)
		case strings.HasPrefix(ft, "<") && p.opts.RepairXML:
			out = xmlRepair(out, &acts)
		}
	} else {
		out = stripOrphanCloseTags(out, &acts)
	}

	out = normalizeOutput(out, &acts)
	final := strings.TrimSpace(out)

	if final == "" && strings.TrimSpace(input) != "" {
		return Result{
			Output:     input,
			Cleaned:    false,
			Repairs:    acts,
			Confidence: 0,
			ModelGuess: guess,
		}, ErrRepairFailed
	}

	verified := (strings.HasPrefix(final, "{") && json.Valid([]byte(final))) ||
		(strings.HasPrefix(final, "[") && json.Valid([]byte(final))) ||
		(strings.HasPrefix(final, "<") && validXMLDoc(final))

	wantStructured := false
	switch p.opts.TargetFormat {
	case FormatJSON, FormatXML:
		wantStructured = true
	default:
		wantStructured = strings.HasPrefix(final, "{") ||
			strings.HasPrefix(final, "[") ||
			strings.HasPrefix(final, "<")
	}

	if wantStructured && !verified {
		return Result{
			Output:     input,
			Cleaned:    false,
			Repairs:    acts,
			Confidence: 0,
			ModelGuess: guess,
		}, ErrRepairFailed
	}

	conf := 0.0
	if verified {
		conf = 1.0
	}
	return Result{
		Output:     out,
		Cleaned:    out != input,
		Repairs:    acts,
		Confidence: conf,
		ModelGuess: guess,
	}, nil
}

func (p *Processor) fastPath(rep artifactReport, input string) bool {
	switch p.opts.TargetFormat {
	case FormatJSON:
		if !rep.validJSON {
			return false
		}
	case FormatXML:
		if !(rep.xmlIntent && validXMLDoc(strings.TrimSpace(input))) {
			return false
		}
	}
	if rep.hasOpenThink || rep.hasCloseThink || rep.hasToolWrapper || rep.hasTemplateBleed ||
		rep.hasCodeFence || rep.hasBoxDrawing ||
		rep.hasPreamble || rep.hasTail || rep.malformedJSON || rep.hasUnicodeEsc || rep.hasCR {
		return false
	}
	if rep.validJSON {
		return true
	}
	if rep.xmlIntent && validXMLDoc(strings.TrimSpace(input)) {
		return true
	}
	return !rep.jsonIntent && !rep.xmlIntent
}
