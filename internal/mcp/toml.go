package mcp

import (
	"regexp"
	"strings"
)

// GetTOMLServer mirrors get_toml_server: reads mcp_servers.<name> from a
// Codex/Grok-style TOML document. Returns (nil, nil) if absent.
func GetTOMLServer(text, serverName string) (*OrderedObject, error) {
	document, err := LoadDocument("toml", text)
	if err != nil {
		return nil, err
	}
	doc, _ := document.(*OrderedObject)
	mcpServers, _ := doc.Get("mcp_servers")
	servers, _ := mcpServers.(*OrderedObject)
	if servers == nil {
		return nil, nil
	}
	server, ok := servers.Get(serverName)
	if !ok || server == nil {
		return nil, nil
	}
	obj, ok := server.(*OrderedObject)
	if !ok {
		return nil, configErrorf("Codex MCP server '%s' must be a table", serverName)
	}
	return obj, nil
}

// tomlValue mirrors _toml_value: strings as JSON-escaped double-quoted
// literals, bools as lowercase, ints/floats via their numeric text, dicts as
// recursively-rendered inline tables in iteration (insertion) order — never
// sorted. Any other type is an error.
func tomlValue(value any) (string, error) {
	switch x := value.(type) {
	case string:
		var b strings.Builder
		writeJSONStringPy(&b, x)
		return b.String(), nil
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case int:
		return formatPyFloat0OrInt(int64(x)), nil
	case int64:
		return formatPyFloat0OrInt(x), nil
	case float64:
		return formatPyFloat(x), nil
	case *OrderedObject:
		parts := make([]string, 0, x.Len())
		for _, k := range x.Keys() {
			var kb strings.Builder
			writeJSONStringPy(&kb, k)
			v, _ := x.Get(k)
			vs, err := tomlValue(v)
			if err != nil {
				return "", err
			}
			parts = append(parts, kb.String()+" = "+vs)
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	default:
		return "", configErrorf("Unsupported managed TOML value: %v", value)
	}
}

func formatPyFloat0OrInt(i int64) string {
	// int/float share str() formatting for whole numbers in the Python
	// source's str(value) call on an int: just the plain decimal digits.
	return itoa(i)
}

func itoa(i int64) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

var tomlSectionHeaderEnd = regexp.MustCompile(`(?m)^[ \t]*\[[^\]]+\][ \t]*(?:#.*)?$`)

func tomlHeaderPattern(header string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(header) + `[ \t]*(?:#.*)?$`)
}

// UpdateTOMLServer mirrors update_toml_server exactly, including its
// append-path separator quirk: when no existing [mcp_servers.<name>]
// section is found, the appended separator always ends up as "\n\n\n" (two
// blank lines) before the new section when text is non-empty, regardless of
// how many trailing newlines text already has — confirmed against the live
// Python function rather than re-derived, since the source's separator
// computation is easy to mis-trace by eye.
func UpdateTOMLServer(text, serverName string, desired *OrderedObject) (string, error) {
	header := "[mcp_servers." + serverName + "]"
	var bodyParts []string
	for _, k := range desired.Keys() {
		v, _ := desired.Get(k)
		vs, err := tomlValue(v)
		if err != nil {
			return "", err
		}
		bodyParts = append(bodyParts, k+" = "+vs)
	}
	section := header + "\n" + strings.Join(bodyParts, "\n") + "\n"

	pattern := tomlHeaderPattern(header)
	loc := pattern.FindStringIndex(text)
	if loc == nil {
		separator := ""
		if text != "" {
			if strings.HasSuffix(text, "\n") {
				separator = "\n"
			} else {
				separator = "\n\n"
			}
		}
		if text != "" && !strings.HasSuffix(text, "\n\n") {
			separator += "\n"
		}
		return text + separator + section, nil
	}

	rest := text[loc[1]:]
	nextLoc := tomlSectionHeaderEnd.FindStringIndex(rest)
	end := len(text)
	if nextLoc != nil {
		end = loc[1] + nextLoc[0]
	}
	return text[:loc[0]] + section + text[end:], nil
}

// RemoveTOMLServer mirrors remove_toml_server: locate and delete the whole
// [mcp_servers.<name>] section span, collapse 3+ consecutive newlines to 2,
// then re-parse the result as a correctness check.
func RemoveTOMLServer(text, serverName string) (string, error) {
	header := "[mcp_servers." + serverName + "]"
	pattern := tomlHeaderPattern(header)
	loc := pattern.FindStringIndex(text)
	if loc == nil {
		return text, nil
	}
	rest := text[loc[1]:]
	nextLoc := tomlSectionHeaderEnd.FindStringIndex(rest)
	end := len(text)
	if nextLoc != nil {
		end = loc[1] + nextLoc[0]
	}
	newText := text[:loc[0]] + text[end:]
	cleaned := collapseBlankLines(newText)
	if _, err := DecodeTOMLOrdered(cleaned); err != nil {
		return "", configErrorf("Failed to verify Codex TOML after removing server '%s': %v", serverName, err)
	}
	return cleaned, nil
}

var threeOrMoreNewlines = regexp.MustCompile(`\n{3,}`)

func collapseBlankLines(s string) string {
	return threeOrMoreNewlines.ReplaceAllString(s, "\n\n")
}
