package subagent

import (
	"regexp"
	"strings"
	"unicode"
)

// cordisItemStartPattern mirrors subagent_adapters.py's
// _split_cordis_patch_items regex: a top-level YAML list item start.
var cordisItemStartPattern = regexp.MustCompile(`(?m)^-[ \t]+`)

type cordisItem struct {
	start, end int
	text       string
}

// splitCordisPatchItems mirrors _split_cordis_patch_items: split a
// cordis.patch.yml-shaped YAML list into (start, end, itemText) spans for
// each top-level list item. This is intentionally a separate, independent
// implementation from any MCP-side Cordis splitter (Python keeps the two
// copy-pasted rather than shared; this package doesn't depend on
// internal/mcp, which may not exist yet at build time).
func splitCordisPatchItems(text string) []cordisItem {
	locs := cordisItemStartPattern.FindAllStringIndex(text, -1)
	var starts []int
	for _, l := range locs {
		starts = append(starts, l[0])
	}
	if len(starts) == 0 {
		stripped := strings.TrimLeftFunc(text, unicode.IsSpace)
		if strings.HasPrefix(stripped, "-") {
			idx := strings.Index(text, "-")
			if idx < 0 {
				return nil
			}
			starts = append(starts, idx)
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

// parseCordisSubagentPluginItem mirrors _parse_cordis_subagent_plugin_item:
// pulls only the id/name/toolName/provider scalar fields out of a single
// Cordis plugin item's YAML text (no nested `config:` walk — the MCP side's
// equivalent parser is heavier; this one is deliberately lighter per the
// Python source).
func parseCordisSubagentPluginItem(itemText string) map[string]string {
	result := map[string]string{}
	for _, line := range strings.Split(itemText, "\n") {
		stripped := strings.TrimSpace(line)
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			continue
		}
		idx := strings.Index(stripped, ":")
		if idx < 0 {
			continue
		}
		k := stripped[:idx]
		v := stripped[idx+1:]
		k = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(k), "- "))
		v = strings.Trim(strings.TrimSpace(v), `'"`)
		switch k {
		case "id", "name", "toolName", "provider":
			result[k] = v
		}
	}
	return result
}

func cordisItemMatches(info map[string]string, name string) bool {
	pluginID := info["id"]
	pluginName := info["name"]
	toolName := info["toolName"]
	return pluginID == "aikito-subagent-"+name ||
		(pluginName == "@deepseek-ai/dsh-tool-subagent" && toolName == name)
}

// GetDSHCordisSubagentItem mirrors get_dsh_cordis_subagent_item.
func GetDSHCordisSubagentItem(text, name string) (string, bool) {
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	for _, item := range splitCordisPatchItems(text) {
		info := parseCordisSubagentPluginItem(item.text)
		if cordisItemMatches(info, name) {
			return item.text, true
		}
	}
	return "", false
}

// GetAllDSHCordisSubagents mirrors get_all_dsh_cordis_subagents.
func GetAllDSHCordisSubagents(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var names []string
	for _, item := range splitCordisPatchItems(text) {
		if !HasAikitoMarkerText(item.text) {
			continue
		}
		info := parseCordisSubagentPluginItem(item.text)
		pluginID := info["id"]
		if strings.HasPrefix(pluginID, "aikito-subagent-") {
			names = append(names, strings.TrimPrefix(pluginID, "aikito-subagent-"))
		} else if info["name"] == "@deepseek-ai/dsh-tool-subagent" {
			if toolName, ok := info["toolName"]; ok {
				names = append(names, toolName)
			}
		}
	}
	return names
}

// UpdateDSHCordisSubagent mirrors update_dsh_cordis_subagent: update the
// matching item in place (preserving the item's own trailing-newline
// presence), or append a new item with blank-line separation matching the
// existing file's ending.
func UpdateDSHCordisSubagent(text, name, renderedBlock string) string {
	if strings.TrimSpace(text) == "" {
		return renderedBlock + "\n"
	}
	for _, item := range splitCordisPatchItems(text) {
		info := parseCordisSubagentPluginItem(item.text)
		if cordisItemMatches(info, name) {
			trailing := ""
			if !strings.HasSuffix(item.text, "\n") {
				trailing = "\n"
			}
			return text[:item.start] + renderedBlock + trailing + text[item.end:]
		}
	}
	var separator string
	switch {
	case strings.HasSuffix(text, "\n\n"):
		separator = "\n"
	case strings.HasSuffix(text, "\n"):
		separator = "\n\n"
	default:
		separator = "\n\n"
	}
	return strings.TrimRightFunc(text, unicode.IsSpace) + separator + renderedBlock + "\n"
}

// RemoveDSHCordisSubagent mirrors remove_dsh_cordis_subagent.
func RemoveDSHCordisSubagent(text, name string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	for _, item := range splitCordisPatchItems(text) {
		info := parseCordisSubagentPluginItem(item.text)
		if cordisItemMatches(info, name) {
			newText := strings.TrimSpace(text[:item.start] + text[item.end:])
			if newText == "" {
				return ""
			}
			return newText + "\n"
		}
	}
	return text
}
