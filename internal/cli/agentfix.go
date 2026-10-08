package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/mr-miles/aikito-go/internal/registry"
)

// loadAgentsTemplate is templating.py load_agents_template: the header plus
// every bundled agent template, joined as _join_agent_templates does.
func loadAgentsTemplate() string {
	parts := []string{}
	if h, err := registry.BundledAgentTemplateText("_header"); err == nil {
		parts = append(parts, strings.TrimRight(h, " \t\r\n"))
	}
	for _, name := range registry.BuiltinAgents {
		if t, err := registry.BundledAgentTemplateText(name); err == nil {
			parts = append(parts, strings.TrimSpace(t))
		}
	}
	return strings.Join(parts, "\n\n") + "\n"
}

var nextAgentHeaderRE = regexp.MustCompile(`(?m)^\[agents\.[^.\]]+\]\s*$`)
var nextHeaderRE = regexp.MustCompile(`(?m)^\[`)

func headerLineRE(header string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(header) + `\s*$`)
}

// agentTemplateFragment is registry.py _agent_template_fragment.
func agentTemplateFragment(template, agent string) (string, bool) {
	start := headerLineRE("[agents." + agent + "]").FindStringIndex(template)
	if start == nil {
		return "", false
	}
	end := len(template)
	if next := nextAgentHeaderRE.FindStringIndex(template[start[1]:]); next != nil {
		end = start[1] + next[0]
	}
	return strings.TrimSpace(template[start[0]:end]) + "\n", true
}

// pyTOMLValue is registry.py _toml_value: strings go through json.dumps,
// which escapes everything outside printable ASCII.
func pyTOMLValue(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "true"
		}
		return "false"
	case string:
		var b strings.Builder
		b.WriteByte('"')
		for _, r := range x {
			switch {
			case r == '"':
				b.WriteString(`\"`)
			case r == '\\':
				b.WriteString(`\\`)
			case r == '\n':
				b.WriteString(`\n`)
			case r == '\r':
				b.WriteString(`\r`)
			case r == '\t':
				b.WriteString(`\t`)
			case r == '\b':
				b.WriteString(`\b`)
			case r == '\f':
				b.WriteString(`\f`)
			case r >= 0x10000:
				r -= 0x10000
				fmt.Fprintf(&b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
			case r < 0x20 || r > 0x7e:
				fmt.Fprintf(&b, `\u%04x`, r)
			default:
				b.WriteRune(r)
			}
		}
		b.WriteByte('"')
		return b.String()
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = pyTOMLValue(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case int64:
		return strconv.FormatInt(x, 10)
	default:
		return fmt.Sprint(x)
	}
}

func leafValue(m map[string]any, dotted string) any {
	var cur any = m
	for _, p := range strings.Split(dotted, ".") {
		t, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = t[p]
	}
	return cur
}

// addMissingAgentFields is registry.py add_missing_agent_fields, called as
// run_doctor_fixes does with the agents installed on this host. Any read or
// decode failure abandons the step without reporting fixes, like Python's
// except clause (files already written stay written).
func addMissingAgentFields(agentsDir string, installed []string) []string {
	template := loadAgentsTemplate()
	matches, _ := filepath.Glob(filepath.Join(agentsDir, "*.toml"))
	sort.Strings(matches)
	current := map[string]map[string]any{}
	var names []string
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
		def, _ := agents[name].(map[string]any)
		if len(doc) != 1 || len(agents) != 1 || def == nil {
			return nil
		}
		current[name] = def
		names = append(names, name)
	}
	for _, n := range installed {
		if _, ok := current[n]; !ok {
			names = append(names, n)
		}
	}

	var fixes []string
	for _, name := range names {
		bundled := registry.BundledAgentSpec(name)
		text, err := registry.BundledAgentTemplateText(name)
		if bundled == nil || err != nil {
			continue
		}
		have := map[string]bool{}
		if def, ok := current[name]; ok {
			leafPaths(def, "", have)
		}
		var absent []string
		for _, p := range templateLeafOrder(text, name) {
			if !have[p] && leafValue(bundled, p) != nil {
				absent = append(absent, p)
			}
		}
		if len(absent) == 0 {
			continue
		}
		path := filepath.Join(agentsDir, name+".toml")
		if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
			frag, ok := agentTemplateFragment(template, name)
			if !ok {
				return nil
			}
			if err := os.WriteFile(path, []byte(frag), 0o644); err != nil {
				return nil
			}
			fixes = append(fixes, fmt.Sprintf("Added agents/%s.toml", name))
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		content := string(data)
		type kv struct {
			key string
			val any
		}
		var tables []string
		grouped := map[string][]kv{}
		for _, field := range absent {
			parts := strings.Split(field, ".")
			table := strings.Join(append([]string{"agents", name}, parts[:len(parts)-1]...), ".")
			if _, seen := grouped[table]; !seen {
				tables = append(tables, table)
			}
			grouped[table] = append(grouped[table], kv{parts[len(parts)-1], leafValue(bundled, field)})
		}
		for _, table := range tables {
			header := "[" + table + "]"
			var additions strings.Builder
			for _, e := range grouped[table] {
				additions.WriteString(e.key + " = " + pyTOMLValue(e.val) + "\n")
			}
			if m := headerLineRE(header).FindStringIndex(content); m != nil {
				insertion := len(content)
				if next := nextHeaderRE.FindStringIndex(content[m[1]:]); next != nil {
					insertion = m[1] + next[0]
				}
				prefix := "\n"
				if strings.HasSuffix(content[:insertion], "\n") {
					prefix = ""
				}
				content = content[:insertion] + prefix + additions.String() + content[insertion:]
			} else {
				content += "\n" + header + "\n" + additions.String()
			}
			for _, e := range grouped[table] {
				fixes = append(fixes, fmt.Sprintf("Added %s.%s to agents/%s.toml", table, e.key, name))
			}
		}
		var check map[string]any
		if err := toml.Unmarshal([]byte(content), &check); err != nil {
			return nil
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return nil
		}
	}
	return fixes
}
