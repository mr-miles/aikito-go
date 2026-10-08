package cli

import (
	"fmt"
	"io"
	"strings"
)

// argparseOpts is parseArgparse's result extended with options that take
// one value (repeated options keep the last value, as argparse does).
type argparseOpts struct {
	flags       map[string]bool
	values      map[string]string
	positionals []string
}

// parseArgparseOpts is parseArgparse plus value options: "--opt VALUE" and
// "--opt=VALUE"; a following argument that looks like an option is not
// taken as the value ("expected one argument"). aliases maps an alternative
// spelling to its canonical option.
func parseArgparseOpts(path string, args []string, boolFlags, valueFlags []string, aliases map[string]string, maxPositional int, stderr io.Writer) (argparseOpts, bool) {
	res := argparseOpts{flags: map[string]bool{}, values: map[string]string{}}
	isValue := map[string]bool{}
	all := append([]string{}, boolFlags...)
	for _, f := range valueFlags {
		isValue[f] = true
		all = append(all, f)
	}
	for alias, canon := range aliases {
		all = append(all, alias)
		if isValue[canon] {
			isValue[alias] = true
		}
	}
	canonical := func(f string) string {
		if c, ok := aliases[f]; ok {
			return c
		}
		return f
	}
	var extras []string
	subError := func(msg string) (argparseOpts, bool) {
		fmt.Fprintf(stderr, "%saikito %s: error: %s\n", subcommandUsage(path), path, msg)
		return res, false
	}
	looksLikeOption := func(a string) bool {
		return strings.HasPrefix(a, "-") && a != "-" && !isNegativeNumber(a)
	}
	onlyPositional := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if onlyPositional || !looksLikeOption(a) {
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
		seen := map[string]bool{}
		for _, f := range all {
			if f == name {
				matches = []string{f}
				break
			}
			if strings.HasPrefix(name, "--") && strings.HasPrefix(f, name) && !seen[f] {
				seen[f] = true
				matches = append(matches, f)
			}
		}
		switch {
		case len(matches) > 1:
			return subError(fmt.Sprintf("ambiguous option: %s could match %s", name, strings.Join(matches, ", ")))
		case len(matches) == 0:
			extras = append(extras, a)
		case isValue[matches[0]]:
			opt := canonical(matches[0])
			if hasValue {
				res.values[opt] = value
				continue
			}
			if i+1 >= len(args) || looksLikeOption(args[i+1]) {
				return subError(fmt.Sprintf("argument %s: expected one argument", argDisplay(opt, aliases)))
			}
			i++
			res.values[opt] = args[i]
		case hasValue:
			return subError(fmt.Sprintf("argument %s: ignored explicit argument '%s'", argDisplay(canonical(matches[0]), aliases), value))
		default:
			res.flags[canonical(matches[0])] = true
		}
	}
	if len(extras) > 0 {
		fmt.Fprintf(stderr, "%saikito: error: unrecognized arguments: %s\n", rootUsage(), strings.Join(extras, " "))
		return res, false
	}
	return res, true
}

// argDisplay is how argparse names an option in errors: all its spellings
// joined by "/", canonical first.
func argDisplay(opt string, aliases map[string]string) string {
	names := []string{opt}
	for alias, canon := range aliases {
		if canon == opt {
			names = append(names, alias)
		}
	}
	return strings.Join(names, "/")
}

// isNegativeNumber mirrors argparse's _negative_number_matcher
// ('^-\d+$|^-\d*\.\d+$').
func isNegativeNumber(a string) bool {
	if len(a) < 2 || a[0] != '-' {
		return false
	}
	digits := func(x string) bool {
		for _, c := range x {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	s := a[1:]
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return digits(s)
	}
	return digits(s[:dot]) && dot+1 < len(s) && digits(s[dot+1:])
}
