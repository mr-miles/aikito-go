package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// checkWorkspaceInitialized mirrors add.py's _check_workspace_initialized.
func checkWorkspaceInitialized(aikitoDir string) string {
	info, err := os.Stat(aikitoDir)
	if err != nil || !info.IsDir() {
		return fmt.Sprintf("Aikito workspace directory not found: %s", aikitoDir)
	}
	if fi, err := os.Stat(filepath.Join(aikitoDir, "layout.toml")); err != nil || !fi.Mode().IsRegular() {
		return fmt.Sprintf("Aikito workspace is not initialized at: %s", aikitoDir)
	}
	return ""
}

// titleize mirrors add.py's _titleize: kebab/snake-case -> Title Case.
func titleize(name string) string {
	replaced := strings.NewReplacer("-", " ", "_", " ").Replace(name)
	fields := strings.Fields(replaced)
	for i, w := range fields {
		if w == "" {
			continue
		}
		r := []rune(w)
		fields[i] = strings.ToUpper(string(r[0])) + strings.ToLower(string(r[1:]))
	}
	return strings.Join(fields, " ")
}

func cmdAdd(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: aikito add skill|subagent|mcp ...")
		return 2
	}
	switch args[0] {
	case "skill", "skills":
		return cmdAddSkill(args[1:], stdout, stderr, env)
	case "subagent", "subagents":
		return cmdAddSubagent(args[1:], stdout, stderr, env)
	case "mcp", "mcps":
		return cmdAddMCP(args[1:], stdout, stderr, env)
	default:
		fmt.Fprintf(stderr, "[ERROR] Unknown add target: %s\n", args[0])
		return 2
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func stringListEquals(raw []any, want []string) bool {
	if len(raw) != len(want) {
		return false
	}
	for i, v := range raw {
		s, ok := v.(string)
		if !ok || s != want[i] {
			return false
		}
	}
	return true
}

// parseSimpleMarkdownFrontmatter is a simplified stand-in for
// frontmatter.py's _parse_markdown_frontmatter: handles the common case of
// "---\nkey: plain scalar value\n---\nbody", stripping surrounding quotes
// from quoted values. Does NOT implement YAML block scalars (|, >), nested
// lists/maps, or multi-line values — real-world SKILL.md frontmatter
// overwhelmingly uses simple name/description scalars, and this port's
// scope/time budget didn't extend to a full YAML-subset parser. If content
// has no "---" frontmatter block at all, returns an empty map and the whole
// trimmed content as body.
func parseSimpleMarkdownFrontmatter(content string) (map[string]any, string) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return map[string]any{}, strings.TrimSpace(content)
	}
	closing := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closing = i
			break
		}
	}
	if closing == -1 {
		return map[string]any{}, strings.TrimSpace(content)
	}
	meta := map[string]any{}
	for _, line := range lines[1:closing] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, `"'`)
		meta[key] = val
	}
	body := strings.Join(lines[closing+1:], "\n")
	return meta, strings.TrimLeft(body, "\n")
}

var defaultSubagentAgents = []string{"codex", "claude-code", "agy", "github-copilot"}
var defaultMCPAgents = []string{"codex", "claude-code", "opencode", "agy", "github-copilot"}

func toAnySlice(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func sortedKeysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}
