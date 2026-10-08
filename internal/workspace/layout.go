package workspace

import (
	"errors"
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

// RequireCurrentLayout mirrors layout.py require_current_layout: reject a
// workspace whose layout migration was interrupted (a pending "layout"-kind
// transaction journal — checked first, as in Python), one that still needs
// migrating from the legacy agents.toml/subagents.toml layout, one with an
// unsupported layout version, or one whose agents/subagents directories are
// missing or unsafe. `aikito migrate workspace-resources` (internal/cli
// migrate.go) recovers an interrupted migration and performs the migration.
func RequireCurrentLayout(root string) error {
	kinds, err := PendingTransactionKinds([]string{root})
	if err != nil {
		return layoutErrorf("Unsafe workspace transaction state: %v", err)
	}
	if _, ok := kinds["layout"]; ok {
		return layoutErrorf("Workspace migration is incomplete: %s\nRun: aikito migrate workspace-resources", root)
	}
	version, err := marker_version(root)
	if err != nil {
		return err
	}
	legacy := false
	for _, name := range LegacyFiles {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			legacy = true
		}
	}
	if version != nil && *version == CurrentLayoutVersion && !legacy {
		for _, name := range []string{"agents", "subagents"} {
			dir := filepath.Join(root, name)
			info, err := os.Lstat(dir)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return layoutErrorf("Workspace resource directory missing or unsafe: %s", dir)
			}
		}
		return nil
	}
	const command = "aikito migrate workspace-resources"
	if version == nil || legacy {
		return layoutErrorf("Workspace needs migration: %s\nRun: %s --dry-run\nThen: %s", root, command, command)
	}
	return layoutErrorf("Unsupported workspace layout version %d: %s", *version, root)
}

// ReadAgentDocuments mirrors layout.py's _read_agent_files: reads every
// <root>/agents/<name>.toml file (one file per agent, no exceptions) and
// returns name -> raw agent spec table (the parsed [agents.<name>] value,
// not yet validated/typed — that's the registry package's job). Each file
// must contain exactly one top-level key "agents" whose value is a table
// with exactly one key equal to the file's stem, whose value is itself a
// table.
func ReadAgentDocuments(root string) (map[string]map[string]any, error) {
	dir := filepath.Join(root, "agents")
	info, err := os.Lstat(dir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, layoutErrorf("Agents directory missing or unsafe: %s", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	agents := map[string]map[string]any{}
	for _, name := range names {
		if name == ".DS_Store" || name == "Thumbs.db" || name == "desktop.ini" {
			continue
		}
		path := filepath.Join(dir, name)
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		if filepath.Ext(name) != ".toml" || ValidateResourceName(stem, "agent") != "" {
			return nil, layoutErrorf("Unsupported Agent entry: %s", path)
		}
		fi, err := os.Lstat(path)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return nil, layoutErrorf("Unsafe Agent entry: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, layoutErrorf("Invalid Agent file: %s", path)
		}
		document, err := DecodeTOML(data)
		if err != nil {
			return nil, layoutErrorf("Invalid Agent file: %s", path)
		}
		if len(document) != 1 {
			return nil, layoutErrorf("Agent file must contain only [agents.%s]: %s", stem, path)
		}
		tableAny, ok := document["agents"]
		table, tableOK := tableAny.(map[string]any)
		if !ok || !tableOK || len(table) != 1 {
			return nil, layoutErrorf("Agent file must contain only [agents.%s]: %s", stem, path)
		}
		specAny, ok := table[stem]
		spec, specOK := specAny.(map[string]any)
		if !ok || !specOK {
			return nil, layoutErrorf("Agent file must contain only [agents.%s]: %s", stem, path)
		}
		agents[stem] = spec
	}
	return agents, nil
}

// --- Subagent strict-JSON-frontmatter Markdown (layout.py:146-299) ---

var frontmatterKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ParseSubagentText validates and parses the strict JSON-valued frontmatter
// Markdown format used by subagents/<name>.md, without touching the
// filesystem. Returns the decoded metadata (string keys; "description" is a
// string, "agents" is a []any of strings, every other key is a
// map[string]any platform-override table) and the instructions body.
func ParseSubagentText(content string) (map[string]any, string, error) {
	return parseSubagentTextAt("<subagent payload>", content)
}

// parseSubagentTextAt is layout.py's _parse_subagent_text: every error names
// path (a file path, or "<subagent payload>" for in-memory content).
func parseSubagentTextAt(path, content string) (map[string]any, string, error) {
	lines := pythonSplitLines(content)
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, "", layoutErrorf("Subagent frontmatter missing: %s", path)
	}
	closing := -1
	for idx := 1; idx < len(lines); idx++ {
		if strings.TrimSpace(lines[idx]) == "---" {
			closing = idx
			break
		}
	}
	if closing == -1 {
		return nil, "", layoutErrorf("Subagent frontmatter is incomplete: %s", path)
	}

	metadata := map[string]any{}
	for _, line := range lines[1:closing] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(strings.TrimLeftFunc(line, unicode.IsSpace), "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			return nil, "", layoutErrorf("Invalid subagent metadata: %s", path)
		}
		key := strings.TrimSpace(line[:idx])
		rawVal := strings.TrimSpace(line[idx+1:])
		if !frontmatterKeyPattern.MatchString(key) {
			return nil, "", layoutErrorf("Invalid or duplicate subagent key: %s", path)
		}
		if _, exists := metadata[key]; exists {
			return nil, "", layoutErrorf("Invalid or duplicate subagent key: %s", path)
		}
		val, err := DecodeStrictJSON(rawVal)
		var dupErr *DuplicateKeyError
		var constErr *ConstantError
		switch {
		case errors.As(err, &dupErr):
			return nil, "", layoutErrorf("Duplicate subagent object key: %s", path)
		case errors.As(err, &constErr):
			return nil, "", layoutErrorf("Unsupported subagent value %s: %s", constErr.Constant, path)
		case err != nil:
			return nil, "", layoutErrorf("Invalid subagent value for %s: %s", key, path)
		}
		metadata[key] = val
	}

	desc, ok := metadata["description"].(string)
	if !ok || strings.TrimSpace(desc) == "" {
		return nil, "", layoutErrorf("Subagent description missing: %s", path)
	}

	agentsRaw, ok := metadata["agents"].([]any)
	if !ok || len(agentsRaw) == 0 {
		return nil, "", layoutErrorf("Subagent agents list invalid: %s", path)
	}
	seen := map[string]bool{}
	for _, a := range agentsRaw {
		name, ok := a.(string)
		if !ok || ValidateResourceName(name, "agent") != "" {
			return nil, "", layoutErrorf("Subagent agents list invalid: %s", path)
		}
		if seen[name] {
			return nil, "", layoutErrorf("Subagent agents list invalid: %s", path)
		}
		seen[name] = true
	}

	for key, value := range metadata {
		if key == "description" || key == "agents" {
			continue
		}
		if ValidateResourceName(key, "agent") != "" {
			return nil, "", layoutErrorf("Subagent platform config invalid: %s", path)
		}
		if _, ok := value.(map[string]any); !ok {
			return nil, "", layoutErrorf("Subagent platform config invalid: %s", path)
		}
	}

	body := strings.Join(lines[closing+1:], "")
	if strings.TrimSpace(body) == "" {
		return nil, "", layoutErrorf("Subagent instructions missing: %s", path)
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
	return parseSubagentTextAt(path, string(data))
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
