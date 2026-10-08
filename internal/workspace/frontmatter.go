package workspace

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// pyJSONLoads is json.loads for frontmatter values: exactly one JSON
// document (trailing data is an error), numbers kept as json.Number.
func pyJSONLoads(text string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("Extra data")
	}
	return v, nil
}

// PyIsSpace is Python's str.isspace for one rune: Go's unicode.IsSpace plus
// the ASCII information separators U+001C..U+001F.
func PyIsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// PyStrip is Python's str.strip() with no arguments.
func PyStrip(s string) string { return strings.TrimFunc(s, PyIsSpace) }

// PyRStrip is Python's str.rstrip() with no arguments.
func PyRStrip(s string) string { return strings.TrimRightFunc(s, PyIsSpace) }

// PySplitLines is Python's str.splitlines() (no keepends).
func PySplitLines(s string) []string {
	lines := pythonSplitLines(s)
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = trimLineTerminator(l)
	}
	return out
}

func trimLineTerminator(l string) string {
	if strings.HasSuffix(l, "\r\n") {
		return l[:len(l)-2]
	}
	r, size := utf8.DecodeLastRuneInString(l)
	switch r {
	case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return l[:len(l)-size]
	}
	return l
}

// splitMarkdownFrontmatter ports frontmatter.py's
// _split_markdown_frontmatter: (frontmatter text, body, ok).
func splitMarkdownFrontmatter(content string) (string, string, bool) {
	raw := strings.TrimPrefix(content, string(rune(0xFEFF)))
	lines := pythonSplitLines(raw)
	if len(lines) == 0 {
		return "", "", false
	}
	isFence := func(line string) bool {
		stripped := strings.TrimRight(line, "\r\n")
		return PyRStrip(stripped) == "---" && !strings.HasPrefix(stripped, " ") && !strings.HasPrefix(stripped, "\t")
	}
	if !isFence(lines[0]) {
		return "", "", false
	}
	for i := 1; i < len(lines); i++ {
		if isFence(lines[i]) {
			return strings.Join(lines[1:i], ""), strings.Join(lines[i+1:], ""), true
		}
	}
	return "", "", false
}

// parseYAMLValue ports frontmatter.py's _parse_yaml_value.
func parseYAMLValue(val string) any {
	val = PyStrip(val)
	if val == "" {
		return ""
	}
	if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
		if v, err := pyJSONLoads(val); err == nil {
			return v
		}
		inner := PyStrip(val[1 : len(val)-1])
		if inner == "" {
			return []any{}
		}
		var items []any
		for _, item := range strings.Split(inner, ",") {
			items = append(items, parseYAMLValue(PyStrip(item)))
		}
		return items
	}
	if strings.HasPrefix(val, "{") && strings.HasSuffix(val, "}") {
		if v, err := pyJSONLoads(val); err == nil {
			return v
		}
		inner := PyStrip(val[1 : len(val)-1])
		res := map[string]any{}
		if inner == "" {
			return res
		}
		for _, pair := range strings.Split(inner, ",") {
			if k, v, ok := strings.Cut(pair, ":"); ok {
				res[strings.Trim(PyStrip(k), `"'`)] = parseYAMLValue(PyStrip(v))
			}
		}
		return res
	}
	switch strings.ToLower(val) {
	case "true":
		return true
	case "false":
		return false
	case "null", "~":
		return nil
	}
	return strings.Trim(val, `"'`)
}

func indented(line string) bool {
	return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
}

// ParseMarkdownFrontmatter ports frontmatter.py's
// _parse_markdown_frontmatter: the YAML-subset frontmatter of agent-native
// markdown files (block scalars, "- " lists, platform sub-tables).
func ParseMarkdownFrontmatter(content string, platformNames []string) (map[string]any, string) {
	fm, body, ok := splitMarkdownFrontmatter(content)
	if !ok {
		fm, body, ok = splitMarkdownFrontmatter(PyStrip(content))
	}
	if !ok {
		return map[string]any{}, PyStrip(content)
	}
	platforms := map[string]bool{}
	for _, p := range platformNames {
		platforms[p] = true
	}

	meta := map[string]any{}
	lines := PySplitLines(fm)
	for i := 0; i < len(lines); {
		line := lines[i]
		stripped := PyStrip(line)
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			i++
			continue
		}
		if indented(line) || !strings.Contains(line, ":") {
			i++
			continue
		}
		k, v, _ := strings.Cut(line, ":")
		key, valStr := PyStrip(k), PyStrip(v)
		isBlock := false
		switch valStr {
		case "|", ">", "|-", ">-", "|+", ">+":
			isBlock = true
		}
		isFolded := strings.HasPrefix(valStr, ">")
		if valStr != "" && !isBlock {
			meta[key] = parseYAMLValue(valStr)
			i++
			continue
		}
		i++
		var children []string
		for i < len(lines) {
			next := lines[i]
			if PyStrip(next) == "" || indented(next) {
				children = append(children, next)
				i++
				continue
			}
			break
		}
		var nonEmpty []string
		for _, c := range children {
			if s := PyStrip(c); s != "" && !strings.HasPrefix(s, "#") {
				nonEmpty = append(nonEmpty, c)
			}
		}
		stripAll := func() []string {
			out := make([]string, len(nonEmpty))
			for j, c := range nonEmpty {
				out[j] = PyStrip(c)
			}
			return out
		}
		anyColon := false
		anyDash := false
		for _, c := range nonEmpty {
			anyColon = anyColon || strings.Contains(c, ":")
			anyDash = anyDash || strings.HasPrefix(PyStrip(c), "- ")
		}
		switch {
		case isBlock && isFolded:
			meta[key] = strings.Join(stripAll(), " ")
		case isBlock:
			meta[key] = strings.Join(stripAll(), "\n")
		case len(nonEmpty) == 0:
			meta[key] = ""
		case platforms[key] && anyColon:
			sub := map[string]any{}
			for _, c := range nonEmpty {
				if sk, sv, ok := strings.Cut(c, ":"); ok {
					sub[PyStrip(sk)] = parseYAMLValue(sv)
				}
			}
			meta[key] = sub
		case anyDash:
			items := []any{}
			for _, s := range stripAll() {
				if strings.HasPrefix(s, "- ") {
					items = append(items, parseYAMLValue(s[2:]))
				}
			}
			meta[key] = items
		default:
			meta[key] = strings.Join(stripAll(), " ")
		}
	}
	return meta, PyStrip(body)
}
