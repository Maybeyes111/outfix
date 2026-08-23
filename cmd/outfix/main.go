package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Maybeyes111/outfix"
)

func main() {
	file := flag.String("f", "", "read input from file instead of stdin")
	model := flag.String("model", "generic", "model hint: generic|qwen|deepseek|glm")
	format := flag.String("format", "auto", "target format: auto|json|xml|plain")
	depth := flag.Int("depth", 2, "repair depth: 1=conservative 2=moderate 3=aggressive")
	verbose := flag.Bool("verbose", false, "print audit trail to stderr")
	noStrip := flag.Bool("no-strip-reasoning", false, "keep reasoning blocks")
	flag.Parse()

	var src []byte
	var err error
	if *file != "" {
		src, err = os.ReadFile(*file)
	} else {
		src, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "outfix:", err)
		os.Exit(1)
	}

	opts := outfix.Options{
		TargetFormat:   parseFormat(*format),
		ModelHint:      parseModel(*model),
		MaxRepairDepth: *depth,
		StripReasoning: !*noStrip,
		RepairJSON:     true,
		RepairXML:      true,
	}

	res, procErr := outfix.New(opts).Process(string(src))
	if procErr != nil {
		fmt.Fprintln(os.Stderr, "outfix:", procErr)
	}
	if *verbose {
		fmt.Fprintf(os.Stderr, "# cleaned: %v\n# confidence: %.2f\n# model guess: %s\n",
			res.Cleaned, res.Confidence, res.ModelGuess)
		for _, r := range res.Repairs {
			fmt.Fprintf(os.Stderr, "# repair: %-28s pos=%-5d %s\n", r.Type, r.Position, r.Description)
		}
		fmt.Fprintln(os.Stderr, "# ---")
	}
	fmt.Print(res.Output)
}

func parseFormat(s string) outfix.Format {
	switch s {
	case "json":
		return outfix.FormatJSON
	case "xml":
		return outfix.FormatXML
	case "plain":
		return outfix.FormatPlainText
	default:
		return outfix.FormatAuto
	}
}

func parseModel(s string) outfix.ModelFamily {
	switch s {
	case "qwen":
		return outfix.ModelQwen
	case "deepseek":
		return outfix.ModelDeepSeek
	case "glm":
		return outfix.ModelGLM
	default:
		return outfix.ModelGeneric
	}
}
