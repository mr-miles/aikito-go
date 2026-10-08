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
