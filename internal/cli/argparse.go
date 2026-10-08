package cli

import (
	"fmt"
	"io"
	"strings"
)

// argparseResult is what a subcommand gets back from parseArgparse.
type argparseResult struct {
	flags       map[string]bool
	positionals []string
}

// subcommandUsage is the "usage: ..." block of a command's captured help.
func subcommandUsage(path string) string {
	text := helpTexts[path]
	i := strings.Index(text, "usage:")
	if i < 0 {
		return ""
	}
	text = text[i:]
	if j := strings.Index(text, "\n\n"); j >= 0 {
		text = text[:j+1]
	}
	return text
}

// rootUsage is the root parser's usage block (what argparse prints for
// "unrecognized arguments").
func rootUsage() string {
	text := noArgsUsage()
	if i := strings.Index(text, "aikito: error:"); i >= 0 {
		return text[:i]
	}
	return text
}

// parseArgparse emulates argparse for a subcommand made of store_true long
// flags and up to maxPositional optional positionals: unique-prefix
// abbreviations, "--" ending options, explicit "=value" rejected, and
// leftovers reported as unrecognized arguments. On a usage error it prints
// argparse's message to stderr and returns ok=false (exit status 2).
func parseArgparse(path string, args []string, flags []string, maxPositional int, stderr io.Writer) (argparseResult, bool) {
	res := argparseResult{flags: map[string]bool{}}
	var extras []string
	subError := func(msg string) (argparseResult, bool) {
		fmt.Fprintf(stderr, "%saikito %s: error: %s\n", subcommandUsage(path), path, msg)
		return res, false
	}
	onlyPositional := false
	for _, a := range args {
		if onlyPositional || a == "-" || !strings.HasPrefix(a, "-") {
			if len(res.positionals) < maxPositional {
				res.positionals = append(res.positionals, a)
			} else {
				extras = append(extras, a)
			}
			continue
		}
		if a == "--" {
			onlyPositional = true
			continue
		}
		name, value, hasValue := strings.Cut(a, "=")
		var matches []string
		for _, f := range flags {
			if f == name {
				matches = []string{f}
				break
			}
			if strings.HasPrefix(name, "--") && strings.HasPrefix(f, name) {
				matches = append(matches, f)
			}
		}
		switch {
		case len(matches) > 1:
			return subError(fmt.Sprintf("ambiguous option: %s could match %s", name, strings.Join(matches, ", ")))
		case len(matches) == 0:
			extras = append(extras, a)
		case hasValue:
			return subError(fmt.Sprintf("argument %s: ignored explicit argument '%s'", matches[0], value))
		default:
			res.flags[matches[0]] = true
		}
	}
	if len(extras) > 0 {
		fmt.Fprintf(stderr, "%saikito: error: unrecognized arguments: %s\n", rootUsage(), strings.Join(extras, " "))
		return res, false
	}
	return res, true
}
