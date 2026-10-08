package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// WorkspaceLayoutError mirrors Python's WorkspaceLayoutError: the workspace
// needs migration or has an invalid resource layout.
type WorkspaceLayoutError struct{ Message string }

func (e *WorkspaceLayoutError) Error() string { return e.Message }

func layoutErrorf(format string, args ...any) error {
	return &WorkspaceLayoutError{Message: fmt.Sprintf(format, args...)}
}

const (
	LayoutFile           = "layout.toml"
	LayoutContent        = "version = 2\n"
	CurrentLayoutVersion = 2
)

// CheckLayoutMarker validates <root>/layout.toml is exactly the v2 marker
// file: a regular (non-symlink) file whose content is the literal string
// "version = 2\n". Any other shape is a hard error, matching Python's
// decision not to round-trip this file through a generic TOML encoder.
func CheckLayoutMarker(root string) error {
	path := filepath.Join(root, LayoutFile)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return layoutErrorf("Workspace needs migration: %s", root)
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return layoutErrorf("Unsafe workspace layout marker: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(data) != LayoutContent {
		return layoutErrorf("Invalid workspace layout marker: %s", path)
	}
	return nil
}

// RequireCurrentLayout rejects a workspace that needs migration or has an
// unsafe/missing agents|subagents directory. This is a simplified port of
// require_current_layout: legacy-format migration is out of scope for this
// Go port (phased-scope decision), so a legacy-looking workspace just gets a
// clear error pointing at the still-available Python tool, rather than a
// ported migration planner.
//
// TODO(phase2 transactions): also check for a pending "layout"-kind
// migration journal entry once the transaction engine exists, matching
// Python's pending_kinds((root,)) check that runs before the marker check.
func RequireCurrentLayout(root string) error {
	if err := CheckLayoutMarker(root); err != nil {
		return err
	}
	for _, legacy := range []string{"agents.toml", "subagents.toml"} {
		if _, err := os.Lstat(filepath.Join(root, legacy)); err == nil {
			return layoutErrorf(
				"Workspace needs migration: %s\nThis Go build does not port the legacy-layout migrator; "+
					"run the original Python aikito's 'migrate workspace-resources' first.", root)
		}
	}
	for _, name := range []string{"agents", "subagents"} {
		dir := filepath.Join(root, name)
		info, err := os.Lstat(dir)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return layoutErrorf("Workspace resource directory missing or unsafe: %s", dir)
		}
	}
	return nil
}

// --- Subagent strict-JSON-frontmatter Markdown (layout.py:146-299) ---

var frontmatterKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ParseSubagentText validates and parses the strict JSON-valued frontmatter
// Markdown format used by subagents/<name>.md, without touching the
// filesystem. Returns the decoded metadata (string keys; "description" is a
// string, "agents" is a []any of strings, every other key is a
// map[string]any platform-override table) and the instructions body.
func ParseSubagentText(content string) (map[string]any, string, error) {
	lines := pythonSplitLines(content)
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, "", layoutErrorf("Subagent frontmatter missing")
	}
	closing := -1
	for idx := 1; idx < len(lines); idx++ {
		if strings.TrimSpace(lines[idx]) == "---" {
			closing = idx
			break
		}
	}
	if closing == -1 {
		return nil, "", layoutErrorf("Subagent frontmatter is incomplete")
	}

	metadata := map[string]any{}
	for _, line := range lines[1:closing] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(strings.TrimLeftFunc(line, unicode.IsSpace), "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			return nil, "", layoutErrorf("Invalid subagent metadata")
		}
		key := strings.TrimSpace(line[:idx])
		rawVal := strings.TrimSpace(line[idx+1:])
		if !frontmatterKeyPattern.MatchString(key) {
			return nil, "", layoutErrorf("Invalid or duplicate subagent key")
		}
		if _, exists := metadata[key]; exists {
			return nil, "", layoutErrorf("Invalid or duplicate subagent key")
		}
		val, err := DecodeStrictJSON(rawVal)
		if err != nil {
			return nil, "", layoutErrorf("Invalid subagent value for %s", key)
		}
		metadata[key] = val
	}

	desc, ok := metadata["description"].(string)
	if !ok || strings.TrimSpace(desc) == "" {
		return nil, "", layoutErrorf("Subagent description missing")
	}

	agentsRaw, ok := metadata["agents"].([]any)
	if !ok || len(agentsRaw) == 0 {
		return nil, "", layoutErrorf("Subagent agents list invalid")
	}
	seen := map[string]bool{}
	for _, a := range agentsRaw {
		name, ok := a.(string)
		if !ok || ValidateResourceName(name, "agent") != "" {
			return nil, "", layoutErrorf("Subagent agents list invalid")
		}
		if seen[name] {
			return nil, "", layoutErrorf("Subagent agents list invalid")
		}
		seen[name] = true
	}

	for key, value := range metadata {
		if key == "description" || key == "agents" {
			continue
		}
		if ValidateResourceName(key, "agent") != "" {
			return nil, "", layoutErrorf("Subagent platform config invalid")
		}
		if _, ok := value.(map[string]any); !ok {
			return nil, "", layoutErrorf("Subagent platform config invalid")
		}
	}

	body := strings.Join(lines[closing+1:], "")
	if strings.TrimSpace(body) == "" {
		return nil, "", layoutErrorf("Subagent instructions missing")
	}
	return metadata, body, nil
}

// ParseSubagentFile reads and parses a subagent Markdown file, refusing a
// symlink or non-regular file at path.
func ParseSubagentFile(path string) (map[string]any, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, "", layoutErrorf("Unsafe subagent file: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	return ParseSubagentText(string(data))
}

// RenderSubagentText mirrors render_subagent_text: frontmatter keys are
// emitted "description", "agents", then all remaining keys sorted
// alphabetically, each value JSON-encoded with sort_keys=True,
// ensure_ascii=False (CanonicalJSON already matches that encoding exactly).
// comments, if non-empty, is inserted verbatim right after the opening "---"
// marker (used to preserve leading comment lines across a rewrite).
func RenderSubagentText(metadata map[string]any, body, comments string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(comments)

	rest := make([]string, 0, len(metadata))
	for k := range metadata {
		if k == "description" || k == "agents" {
			continue
		}
		rest = append(rest, k)
	}
	sort.Strings(rest)

	order := make([]string, 0, len(metadata))
	order = append(order, "description", "agents")
	order = append(order, rest...)

	for _, key := range order {
		val, ok := metadata[key]
		if !ok {
			continue
		}
		b.WriteString(key)
		b.WriteString(": ")
		b.WriteString(CanonicalJSON(val))
		b.WriteString("\n")
	}
	b.WriteString("---\n")
	b.WriteString(body)
	return b.String()
}

// pythonSplitLines mirrors Python's str.splitlines(keepends=True): splits on
// \n, \r, \r\n, and the additional line-boundary code points Python
// recognizes (\v, \f, \x1c-\x1e, NEL U+0085, LS U+2028, PS U+2029), keeping
// each terminator attached to its line so re-joining reproduces the
// original text exactly.
func pythonSplitLines(s string) []string {
	var lines []string
	lineStart := 0
	i := 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch r {
		case '\r':
			next := i + size
			if next < len(s) && s[next] == '\n' {
				lines = append(lines, s[lineStart:next+1])
				i = next + 1
				lineStart = i
				continue
			}
			lines = append(lines, s[lineStart:i+size])
			i += size
			lineStart = i
			continue
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			lines = append(lines, s[lineStart:i+size])
			i += size
			lineStart = i
			continue
		}
		i += size
	}
	if lineStart < len(s) {
		lines = append(lines, s[lineStart:])
	}
	return lines
}
