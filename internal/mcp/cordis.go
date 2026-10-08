package mcp

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// cordisItem is one (start, end, text) span of a top-level YAML list item,
// mirroring _split_cordis_patch_items' tuple return.
type cordisItem struct {
	start, end int
	text       string
}

var cordisItemStart = regexp.MustCompile(`(?m)^-[ \t]+`)

// splitCordisPatchItems mirrors _split_cordis_patch_items.
func splitCordisPatchItems(text string) []cordisItem {
	locs := cordisItemStart.FindAllStringIndex(text, -1)
	var starts []int
	for _, loc := range locs {
		starts = append(starts, loc[0])
	}
	if len(starts) == 0 {
		stripped := strings.TrimLeft(text, " \t\n\r\v\f")
		if strings.HasPrefix(stripped, "-") {
			starts = append(starts, strings.IndexByte(text, '-'))
		} else {
			return nil
		}
	}
	items := make([]cordisItem, 0, len(starts))
	for i, start := range starts {
		end := len(text)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		items = append(items, cordisItem{start, end, text[start:end]})
	}
	return items
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseCordisPluginItem mirrors _parse_cordis_plugin_item: a hand-rolled,
// indentation-sensitive line walker over one YAML list item. Not a general
// YAML parser — only understands the fixed shape Aikito itself writes.
func parseCordisPluginItem(itemText string) *OrderedObject {
	result := NewOrderedObject()
	config := NewOrderedObject()
	currentSection := ""
	currentKey := ""

	lines := strings.Split(itemText, "\n")
	for _, line := range lines {
		stripped := strings.TrimSpace(line)
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			continue
		}

		normLine := line
		if strings.HasPrefix(strings.TrimLeft(normLine, " \t"), "- ") {
			dashIdx := strings.Index(normLine, "- ")
			if dashIdx >= 0 {
				normLine = normLine[:dashIdx] + "  " + normLine[dashIdx+2:]
			}
		}
		indent := len(normLine) - len(strings.TrimLeft(normLine, " \t"))

		switch {
		case indent <= 2:
			currentSection = ""
			if i := strings.Index(stripped, ":"); i >= 0 {
				k := stripped[:i]
				v := strings.TrimSpace(stripped[i+1:])
				k = strings.TrimSpace(k)
				k = strings.TrimLeft(k, "- ")
				k = strings.TrimSpace(k)
				if k == "config" {
					currentSection = "config"
				} else {
					result.Set(k, strings.Trim(v, `'"`))
				}
			}
		case currentSection == "config" && indent == 4:
			if i := strings.Index(stripped, ":"); i >= 0 {
				k := strings.TrimSpace(stripped[:i])
				v := strings.TrimSpace(stripped[i+1:])
				if v == "" {
					currentKey = ""
					if k == "args" || k == "headers" || k == "env" {
						currentKey = k
						if k == "args" {
							config.Set(k, []any{})
						} else {
							config.Set(k, NewOrderedObject())
						}
					}
				} else {
					currentKey = ""
					switch {
					case isAllDigits(v):
						n, _ := strconv.ParseInt(v, 10, 64)
						config.Set(k, n)
					case v == "true" || v == "false":
						config.Set(k, v == "true")
					default:
						if !strings.HasPrefix(v, "!!js") {
							v = strings.Trim(v, `'"`)
						}
						config.Set(k, v)
					}
				}
			}
		case currentSection == "config" && indent >= 6 && currentKey != "":
			if currentKey == "args" && strings.HasPrefix(stripped, "- ") {
				val := strings.Trim(strings.TrimSpace(stripped[2:]), `'"`)
				argsVal, _ := config.Get("args")
				args, _ := argsVal.([]any)
				config.Set("args", append(args, val))
			} else if (currentKey == "headers" || currentKey == "env") && strings.Contains(stripped, ":") {
				i := strings.Index(stripped, ":")
				hk := strings.TrimSpace(stripped[:i])
				hv := strings.TrimSpace(stripped[i+1:])
				if !strings.HasPrefix(hv, "!!js") {
					hv = strings.Trim(hv, `'"`)
				}
				sub, _ := config.Get(currentKey)
				subObj, _ := sub.(*OrderedObject)
				if subObj != nil {
					subObj.Set(hk, hv)
				}
			}
		}
	}

	if config.Len() > 0 {
		result.Set("config", config)
	}
	return result
}

func plainString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// ParseDSHCordisEntries mirrors _parse_dsh_cordis_entries.
func ParseDSHCordisEntries(text string) (*OrderedObject, error) {
	entries := NewOrderedObject()
	if strings.TrimSpace(text) == "" {
		return entries, nil
	}
	for _, item := range splitCordisPatchItems(text) {
		plugin := parseCordisPluginItem(item.text)
		nameVal, _ := plugin.Get("name")
		name, _ := plainString(nameVal)
		name = strings.Trim(name, `'"`)
		if name != "@deepseek-ai/dsh-mcp-client" {
			continue
		}
		cfgVal, _ := plugin.Get("config")
		cfg, ok := cfgVal.(*OrderedObject)
		if !ok {
			continue
		}
		if serverNameVal, ok := cfg.Get("serverName"); ok {
			if serverName, ok := plainString(serverNameVal); ok {
				entries.Set(serverName, cfg)
			}
		}
	}
	return entries, nil
}

// formatDshCordisEntry mirrors _format_dsh_cordis_entry: desired's keys are
// emitted in iteration (insertion) order except serverName (skipped,
// already in the fixed preamble); headers/env sub-maps ARE sorted by key
// (unlike the top level).
func formatDshCordisEntry(serverName string, desired *OrderedObject) string {
	lines := []string{
		"- id: aikito-mcp-" + serverName,
		"  name: '@deepseek-ai/dsh-mcp-client'",
		"  config:",
		"    serverName: " + serverName,
	}
	for _, key := range desired.Keys() {
		if key == "serverName" {
			continue
		}
		val, _ := desired.Get(key)
		switch key {
		case "args":
			if list, ok := val.([]any); ok {
				lines = append(lines, "    args:")
				for _, arg := range list {
					var b strings.Builder
					writeJSONStringPy(&b, toPyStr(arg))
					lines = append(lines, "      - "+b.String())
				}
				continue
			}
		case "headers", "env":
			if obj, ok := val.(*OrderedObject); ok {
				lines = append(lines, "    "+key+":")
				keys := obj.Keys()
				sort.Strings(keys)
				for _, k := range keys {
					v, _ := obj.Get(k)
					vs, isStr := plainString(v)
					if isStr && (strings.HasPrefix(vs, "!!js") || strings.HasPrefix(vs, "`")) {
						lines = append(lines, "      "+k+": "+vs)
					} else {
						var b strings.Builder
						writeJSONStringPy(&b, toPyStr(v))
						lines = append(lines, "      "+k+": "+b.String())
					}
				}
				continue
			}
		}
		switch v := val.(type) {
		case bool:
			if v {
				lines = append(lines, "    "+key+": true")
			} else {
				lines = append(lines, "    "+key+": false")
			}
		case int64:
			lines = append(lines, "    "+key+": "+itoa(v))
		case float64:
			lines = append(lines, "    "+key+": "+formatPyFloat(v))
		default:
			lines = append(lines, "    "+key+": "+toPyStr(val))
		}
	}
	return strings.Join(lines, "\n")
}

// toPyStr approximates Python's str(value) for the scalar kinds that can
// legally appear here (this package only ever produces string/bool/int/
// float/list/dict values; a bare str() fallback is only exercised for a
// plain string value in formatDshCordisEntry's final "else" branch, which
// is just the string itself).
func toPyStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case int64:
		return itoa(x)
	case float64:
		return formatPyFloat(x)
	case nil:
		return "None"
	default:
		return ""
	}
}

func isDshCordisTarget(plugin *OrderedObject, serverName string) bool {
	idVal, _ := plugin.Get("id")
	id, _ := plainString(idVal)
	if id == "aikito-mcp-"+serverName {
		return true
	}
	nameVal, _ := plugin.Get("name")
	name, _ := plainString(nameVal)
	name = strings.Trim(name, `'"`)
	if name != "@deepseek-ai/dsh-mcp-client" {
		return false
	}
	cfgVal, _ := plugin.Get("config")
	cfg, ok := cfgVal.(*OrderedObject)
	if !ok {
		return false
	}
	snVal, _ := cfg.Get("serverName")
	sn, _ := plainString(snVal)
	return sn == serverName
}

// GetDSHCordisServer mirrors get_dsh_cordis_server.
func GetDSHCordisServer(text, serverName string) (*OrderedObject, error) {
	entries, err := ParseDSHCordisEntries(text)
	if err != nil {
		return nil, err
	}
	v, ok := entries.Get(serverName)
	if !ok {
		return nil, nil
	}
	obj, _ := v.(*OrderedObject)
	return obj, nil
}

// UpdateDSHCordisServer mirrors update_dsh_cordis_server.
func UpdateDSHCordisServer(text, serverName string, desired *OrderedObject) (string, error) {
	formatted := formatDshCordisEntry(serverName, desired)
	if strings.TrimSpace(text) == "" {
		return formatted + "\n", nil
	}
	items := splitCordisPatchItems(text)
	for _, item := range items {
		plugin := parseCordisPluginItem(item.text)
		if isDshCordisTarget(plugin, serverName) {
			trailing := ""
			if !strings.HasSuffix(item.text, "\n") {
				trailing = "\n"
			}
			return text[:item.start] + formatted + trailing + text[item.end:], nil
		}
	}
	var separator string
	if strings.HasSuffix(text, "\n\n") {
		separator = "\n"
	} else {
		separator = "\n\n"
	}
	return strings.TrimRight(text, " \t\n\r\v\f") + separator + formatted + "\n", nil
}

// RemoveDSHCordisServer mirrors remove_dsh_cordis_server.
func RemoveDSHCordisServer(text, serverName string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	items := splitCordisPatchItems(text)
	for _, item := range items {
		plugin := parseCordisPluginItem(item.text)
		if isDshCordisTarget(plugin, serverName) {
			newText := strings.TrimSpace(text[:item.start] + text[item.end:])
			if newText == "" {
				return "", nil
			}
			return newText + "\n", nil
		}
	}
	return text, nil
}
