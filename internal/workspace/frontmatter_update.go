package workspace

import (
	"regexp"
	"strings"
)

// FrontmatterUpdate is one key of an _update_markdown_frontmatter update;
// a slice of them keeps Python's dict insertion order.
type FrontmatterUpdate struct{ Key, Value string }

// FormatYAMLScalar ports frontmatter.py's _format_yaml_scalar.
func FormatYAMLScalar(val string) string {
	if val == "" {
		return `""`
	}
	needsQuotes := strings.ContainsAny(val, ":#{}[]|>&*!%@`\"\n\r\t,?") ||
		PyStrip(val) != val ||
		strings.HasPrefix(val, "-") || strings.HasPrefix(val, "?") || strings.HasPrefix(val, "@") ||
		strings.HasPrefix(val, "`") || strings.HasPrefix(val, "%")
	switch strings.ToLower(val) {
	case "true", "false", "null", "yes", "no", "on", "off":
		needsQuotes = true
	}
	if !needsQuotes {
		return val
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`).Replace(val)
	return `"` + escaped + `"`
}

// UpdateMarkdownFrontmatter ports frontmatter.py's
// _update_markdown_frontmatter: rewrite only the given keys, keeping other
// keys, comments and the body.
func UpdateMarkdownFrontmatter(content string, updates []FrontmatterUpdate) string {
	const bomRune = "\ufeff"
	fmRaw, body, ok := splitMarkdownFrontmatter(content)
	if !ok {
		lines := []string{"---"}
		for _, u := range updates {
			lines = append(lines, u.Key+": "+FormatYAMLScalar(u.Value))
		}
		lines = append(lines, "---")
		rest := strings.TrimLeftFunc(strings.TrimLeft(content, bomRune), PyIsSpace)
		return strings.Join(lines, "\n") + "\n\n" + rest
	}
	bom := ""
	if strings.HasPrefix(content, bomRune) {
		bom = bomRune
	}

	fmLines := PySplitLines(fmRaw)
	remaining := append([]FrontmatterUpdate{}, updates...)
	indent := func(l string) bool { return strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t") }

	var newLines []string
	for i := 0; i < len(fmLines); {
		line := fmLines[i]
		matched := -1
		for j, u := range remaining {
			if regexp.MustCompile(`^` + regexp.QuoteMeta(u.Key) + `\s*:`).MatchString(line) {
				matched = j
				break
			}
		}
		if matched < 0 {
			newLines = append(newLines, line)
			i++
			continue
		}
		u := remaining[matched]
		remaining = append(remaining[:matched], remaining[matched+1:]...)
		newLines = append(newLines, u.Key+": "+FormatYAMLScalar(u.Value))
		i++
		// Skip the old value's indented continuation lines (and blank lines
		// inside it).
		for i < len(fmLines) {
			next := fmLines[i]
			if indent(next) {
				i++
				continue
			}
			if PyStrip(next) == "" {
				j := i + 1
				for j < len(fmLines) && PyStrip(fmLines[j]) == "" {
					j++
				}
				if j < len(fmLines) && indent(fmLines[j]) {
					i = j + 1
					continue
				}
			}
			break
		}
	}

	for _, u := range remaining {
		formatted := FormatYAMLScalar(u.Value)
		if u.Key == "name" {
			idx := 0
			for idx < len(newLines) && strings.HasPrefix(PyStrip(newLines[idx]), "#") {
				idx++
			}
			newLines = append(newLines[:idx], append([]string{"name: " + formatted}, newLines[idx:]...)...)
		} else {
			newLines = append(newLines, u.Key+": "+formatted)
		}
	}

	joined := strings.Trim(strings.Join(newLines, "\n"), "\n")
	bodyStripped := strings.TrimLeftFunc(body, PyIsSpace)
	if joined != "" {
		return bom + "---\n" + joined + "\n---\n\n" + bodyStripped
	}
	return bom + "---\n---\n\n" + bodyStripped
}
