package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/mr-miles/aikito-go/internal/registry"
)

type missingFields struct {
	agent  string
	fields []string // dotted paths, in bundled template order
}

// leafPaths flattens a decoded TOML table into dotted leaf paths.
func leafPaths(v map[string]any, prefix string, out map[string]bool) {
	for k, child := range v {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		if m, ok := child.(map[string]any); ok {
			leafPaths(m, p, out)
		} else {
			out[p] = true
		}
	}
}

// templateLeafOrder lists the dotted leaf paths of one bundled agent's
// [agents.<name>...] tables in the order they appear in the template text,
// which is the order Python's dict-based _leaf_fields yields. Only
// "key = value" lines under table headers are handled, which is all the
// bundled templates use.
func templateLeafOrder(text, agent string) []string {
	prefix := "agents." + agent
	table := ""
	var out []string
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") && !strings.HasPrefix(t, "[[") {
			name := strings.TrimSpace(strings.Trim(t, "[]"))
			switch {
			case name == prefix:
				table = ""
			case strings.HasPrefix(name, prefix+"."):
				table = strings.TrimPrefix(name, prefix+".")
			default:
				table = "\x00"
			}
			continue
		}
		if table == "\x00" {
			continue
		}
		if i := strings.Index(t, "="); i > 0 {
			key := strings.Trim(strings.TrimSpace(t[:i]), `"`)
			if table != "" {
				key = table + "." + key
			}
			out = append(out, key)
		}
	}
	return out
}

// missingAgentFields ports registry.py missing_agent_fields for the
// registered agents/<name>.toml files.
func missingAgentFields(agentsDir string) []missingFields {
	matches, _ := filepath.Glob(filepath.Join(agentsDir, "*.toml"))
	sort.Strings(matches)
	var out []missingFields
	for _, path := range matches {
		name := strings.TrimSuffix(filepath.Base(path), ".toml")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var doc map[string]any
		if err := toml.Unmarshal(data, &doc); err != nil {
			return nil
		}
		agents, _ := doc["agents"].(map[string]any)
		current, ok := agents[name].(map[string]any)
		if !ok {
			continue
		}
		bundled := registry.BundledAgentSpec(name)
		if bundled == nil {
			continue
		}
		text, err := registry.BundledAgentTemplateText(name)
		if err != nil {
			continue
		}
		have := map[string]bool{}
		leafPaths(current, "", have)
		want := map[string]bool{}
		leafPaths(bundled, "", want)
		var absent []string
		for _, p := range templateLeafOrder(text, name) {
			if want[p] && !have[p] {
				absent = append(absent, p)
			}
		}
		if len(absent) > 0 {
			out = append(out, missingFields{name, absent})
		}
	}
	return out
}

func sortedPlatformKeys(m map[string]map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
