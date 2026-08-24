package outfix

import "errors"

type Format int

const (
	FormatAuto Format = iota
	FormatJSON
	FormatXML
	FormatPlainText
)

func (f Format) String() string {
	switch f {
	case FormatJSON:
		return "json"
	case FormatXML:
		return "xml"
	case FormatPlainText:
		return "plain_text"
	default:
		return "auto"
	}
}

type ModelFamily int

const (
	ModelGeneric ModelFamily = iota
	ModelQwen
	ModelDeepSeek
	ModelGLM
)

func (m ModelFamily) String() string {
	switch m {
	case ModelQwen:
		return "qwen"
	case ModelDeepSeek:
		return "deepseek"
	case ModelGLM:
		return "glm"
	default:
		return "generic"
	}
}

type Options struct {
	TargetFormat   Format
	StripReasoning bool
	RepairJSON     bool
	RepairXML      bool
	ModelHint      ModelFamily
	MaxRepairDepth int
}

func (o Options) withDefaults() Options {
	if o.MaxRepairDepth < 1 || o.MaxRepairDepth > 3 {
		o.MaxRepairDepth = 2
	}
	if o.TargetFormat < FormatAuto || o.TargetFormat > FormatPlainText {
		o.TargetFormat = FormatAuto
	}
	if o.ModelHint < ModelGeneric || o.ModelHint > ModelGLM {
		o.ModelHint = ModelGeneric
	}
	return o
}

type RepairAction struct {
	Type        string
	Description string
	Position    int
}

type Result struct {
	Output     string
	Cleaned    bool
	Repairs    []RepairAction
	Confidence float64
	ModelGuess ModelFamily
}

var ErrRepairFailed = errors.New("outfix: unable to repair input into a valid target format; original input returned")

const (
	ActionStrippedThinkBlock     = "stripped_think_block"
	ActionStrippedOrphanCloseTag = "stripped_orphan_close_tag"
	ActionUnwrappedToolCall      = "unwrapped_tool_call"
	ActionStrippedChatTemplate   = "stripped_chat_template"
	ActionStrippedCodeFence      = "stripped_code_fence"
	ActionStrippedBoxDrawing     = "stripped_box_drawing"
	ActionRemovedPreamble        = "removed_preamble"
	ActionRemovedPostamble       = "removed_postamble"
	ActionStrippedXMLBleed       = "stripped_xml_bleed"
	ActionFixedSingleQuotes      = "fixed_single_quotes"
	ActionFixedPythonLiterals    = "fixed_python_literals"
	ActionFixedTrailingComma     = "fixed_trailing_comma"
	ActionQuotedBareKeys         = "quoted_bare_keys"
	ActionMergedNDJSON           = "merged_ndjson"
	ActionRepairedTruncatedJSON  = "repaired_truncated_json"
	ActionConvertedFunctionCall  = "converted_function_call"
	ActionQuotedBareValues       = "quoted_bare_values"
	ActionUnwrappedStringified   = "unwrapped_stringified_json"
	ActionFixedXMLOrphanClose    = "fixed_xml_orphan_close"
	ActionFixedMismatchedXMLTags = "fixed_mismatched_xml_tags"
	ActionClosedUnclosedXMLTags  = "closed_unclosed_xml_tags"
	ActionNormalizedUnicodeEsc   = "normalized_unicode_escapes"
	ActionNormalizedLineEndings  = "normalized_line_endings"
	ActionCollapsedWhitespace    = "collapsed_whitespace"
)

func defaultFixOptions() Options {
	return Options{
		TargetFormat:   FormatAuto,
		StripReasoning: true,
		RepairJSON:     true,
		RepairXML:      true,
		ModelHint:      ModelGeneric,
		MaxRepairDepth: 2,
	}
}

func addAct(acts *[]RepairAction, typ, desc string, pos int) {
	*acts = append(*acts, RepairAction{Type: typ, Description: desc, Position: pos})
}
