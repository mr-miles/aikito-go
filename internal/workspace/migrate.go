// Legacy (pre-v2) workspace layout migration: splits a monolithic
// agents.toml ([agents.<name>] tables in one file) and subagents.toml
// ([subagents.<name>] tables each referencing a subagents/<name>.md body
// file) into the current v2 layout (one agents/<name>.toml per agent,
// subagents/<name>.md carrying its own frontmatter). Python:
// src/aikito/workspace/layout.py build_migration_plan/apply_migration.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var legacyAgentHeaderRe = regexp.MustCompile(`(?m)^\[agents\.([a-z0-9][a-z0-9-]*)\][ \t]*$`)
var legacySubagentHeaderRe = regexp.MustCompile(`(?m)^\[subagents\.([a-z0-9][a-z0-9-]*)\][ \t]*$`)

// LegacyFiles are the two monolithic-registry files a pre-v2 workspace has
// instead of agents/ and subagents/ directories.
var LegacyFiles = []string{"agents.toml", "subagents.toml"}

// FileFragment is one planned file write: a workspace-root-relative path
// and its exact text content.
type FileFragment struct {
	Path    string
	Content string
}

// MigrationPlan mirrors layout.py's MigrationPlan: what a legacy-to-v2
// migration would create/update/remove, plus any findings that block it
// entirely (migration is all-or-nothing: any finding means zero files
// change) and informational notes.
type MigrationPlan struct {
	Root          string
	Creates       []FileFragment
	Updates       []FileFragment
	Removes       []string
	Findings      []string
	Notes         []string
	MarkerContent string
}

func (p MigrationPlan) Blocked() bool { return len(p.Findings) > 0 }

// BuildMigrationPlan mirrors build_migration_plan: read the legacy layout
// (if any) and report every target collision or validation problem before
// writing anything. home anchors path validation only; if empty, root is
// used (matching Python's "home = home if home is not None else root").
func BuildMigrationPlan(root, home string) (MigrationPlan, error) {
	resolvedRoot, err := ResolvePath(root)
	if err != nil {
		return MigrationPlan{}, err
	}
	root = resolvedRoot
	if home == "" {
		home = root
	}

	version, verr := marker_version(root)
	if verr != nil {
		return MigrationPlan{}, verr
	}

	var legacyPaths []string
	for _, name := range LegacyFiles {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			legacyPaths = append(legacyPaths, name)
		}
	}

	if version != nil && *version == 2 && len(legacyPaths) == 0 {
		var findings []string
		for _, name := range []string{"agents", "subagents"} {
			dir := filepath.Join(root, name)
			info, err := os.Lstat(dir)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				findings = append(findings, fmt.Sprintf("Workspace resource directory missing or unsafe: %s", dir))
			}
		}
		return MigrationPlan{Root: root, Findings: findings, MarkerContent: LayoutContent}, nil
	}
	if version != nil {
		return MigrationPlan{Root: root, Findings: []string{fmt.Sprintf("Unsupported or incomplete layout marker: %d", *version)}, MarkerContent: LayoutContent}, nil
	}
	if len(legacyPaths) != 2 {
		return MigrationPlan{Root: root, Findings: []string{"Both legacy configuration files are required"}, MarkerContent: LayoutContent}, nil
	}

	var findings []string
	var notes []string
	markerContent := LayoutContent
	var creates []FileFragment
	var updates []FileFragment

	for _, name := range legacyPaths {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			findings = append(findings, fmt.Sprintf("Unsafe legacy file: %s", name))
		}
	}

	agentsDir := filepath.Join(root, "agents")
	if adInfo, err := os.Lstat(agentsDir); err == nil {
		if adInfo.Mode()&os.ModeSymlink != 0 || !adInfo.IsDir() {
			findings = append(findings, "Unsafe Agent directory: agents")
		} else {
			entries, _ := os.ReadDir(agentsDir)
			for _, e := range entries {
				if e.Name() != ".DS_Store" && e.Name() != "Thumbs.db" && e.Name() != "desktop.ini" {
					findings = append(findings, fmt.Sprintf("Target already exists: agents/%s", e.Name()))
				}
			}
		}
	}

	// --- agents.toml ---
	agentsText, err := os.ReadFile(filepath.Join(root, "agents.toml"))
	if err != nil {
		findings = append(findings, fmt.Sprintf("agents.toml: %v", err))
	} else {
		agentsDoc, derr := DecodeTOML(agentsText)
		var agentsTable map[string]any
		if derr == nil {
			if len(agentsDoc) != 1 {
				derr = fmt.Errorf("Invalid legacy Agent registry")
			} else if t, ok := agentsDoc["agents"].(map[string]any); ok {
				agentsTable = t
			} else {
				derr = fmt.Errorf("Invalid legacy Agent registry")
			}
		}
		if derr != nil {
			findings = append(findings, fmt.Sprintf("agents.toml: %v", derr))
		} else {
			fragments, names, serr := splitAgentText(string(agentsText), agentsTable)
			if serr != nil {
				findings = append(findings, fmt.Sprintf("agents.toml: %v", serr))
			} else {
				// Document order, as Python's dict of sections keeps it.
				for _, name := range names {
					relative := "agents/" + name + ".toml"
					if _, err := os.Lstat(filepath.Join(root, relative)); err != nil {
						creates = append(creates, FileFragment{relative, fragments[name]})
					}
				}
				if len(agentsTable) == 0 && strings.Contains(string(agentsText), "#") {
					notes = append(notes, "Comments in empty agents.toml cannot be attached to an Agent")
				}
			}
		}
	}

	// --- subagents.toml ---
	subagentsText, err := os.ReadFile(filepath.Join(root, "subagents.toml"))
	if err != nil {
		findings = append(findings, fmt.Sprintf("subagents.toml: %v", err))
	} else if secs, serr := buildSubagentUpdates(root, string(subagentsText), &markerContent, &notes); serr != nil {
		findings = append(findings, fmt.Sprintf("subagents.toml: %v", serr))
	} else {
		updates = append(updates, secs.updates...)
		findings = append(findings, secs.findings...)
	}

	if _, err := os.Lstat(filepath.Join(root, "layout.toml")); err == nil {
		findings = append(findings, "Target already exists: layout.toml")
	}

	return MigrationPlan{
		Root: root, Creates: creates, Updates: updates, Removes: legacyPaths,
		Findings: findings, Notes: notes, MarkerContent: markerContent,
	}, nil
}

type subagentMigrationResult struct {
	updates  []FileFragment
	findings []string
}

// buildSubagentUpdates mirrors the subagents.toml half of
// build_migration_plan: split the legacy table into one subagents/<name>.md
// rewrite per entry (reusing the already-rendered body + metadata through
// RenderSubagentText, verified by a full round-trip re-parse), preserving
// comments attached to each table header. Deliberately simplified from
// Python: per-platform option *type* validation (validate_platform_opts,
// which needs an AgentDefinition built from the in-memory legacy document —
// not exposed by internal/registry without reading real agents/*.toml files
// from disk, which don't exist yet at plan time) is NOT performed here;
// the round-trip re-parse below already catches structural problems
// (platform key must be a valid agent name, value must be a table) via
// ParseSubagentText's own validation, which is the correctness-critical
// part. A workspace relying on deep per-field platform-option validation
// specifically during migration (as opposed to after, when `aikito doctor`
// or a subsequent sync would catch it) is the known gap.
func buildSubagentUpdates(root, subagentsText string, markerContent *string, notes *[]string) (subagentMigrationResult, error) {
	doc, err := DecodeTOML([]byte(subagentsText))
	if err != nil {
		return subagentMigrationResult{}, err
	}
	if len(doc) != 1 {
		return subagentMigrationResult{}, fmt.Errorf("Invalid legacy subagent registry")
	}
	table, ok := doc["subagents"].(map[string]any)
	if !ok {
		return subagentMigrationResult{}, fmt.Errorf("Invalid legacy subagent registry")
	}

	sections := resourceSections(subagentsText, legacySubagentHeaderRe)
	firstMatch := legacySubagentHeaderRe.FindStringIndex(subagentsText)
	sectionStart := len(subagentsText)
	if firstMatch != nil && len(sections) > 0 {
		sectionStart = firstMatch[0] - len(sections[0].Comments)
	}
	prefixComments := standaloneComments(subagentsText[:sectionStart])

	commentsByName := map[string]string{}
	for _, s := range sections {
		commentsByName[s.Name] = s.Comments + standaloneComments(s.TableText)
	}
	if len(commentsByName) != len(sections) {
		return subagentMigrationResult{}, fmt.Errorf("Unsupported subagent table header")
	}
	tableNames := map[string]bool{}
	for n := range table {
		tableNames[n] = true
	}
	commentNames := map[string]bool{}
	for n := range commentsByName {
		commentNames[n] = true
	}
	if !stringSetEqual(commentNames, tableNames) {
		return subagentMigrationResult{}, fmt.Errorf("Unsupported subagent table header")
	}
	if len(sections) > 0 {
		firstName := sections[0].Name
		commentsByName[firstName] = prefixComments + commentsByName[firstName]
	} else if prefixComments != "" {
		*markerContent += prefixComments
		*notes = append(*notes, "Subagent registry comments preserved in layout.toml")
	}

	var findings []string
	var updates []FileFragment
	names := make([]string, 0, len(table))
	for n := range table {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		metadataAny := table[name]
		metadata, isMap := metadataAny.(map[string]any)
		if ValidateResourceName(name, "subagent") != "" || !isMap {
			findings = append(findings, fmt.Sprintf("Invalid legacy subagent: %s", name))
			continue
		}
		path := filepath.Join(root, "subagents", name+".md")
		info, serr := os.Lstat(path)
		if serr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			findings = append(findings, fmt.Sprintf("Missing or unsafe subagent instructions: %s", path))
			continue
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			findings = append(findings, fmt.Sprintf("Missing or unsafe subagent instructions: %s", path))
			continue
		}
		body := string(data)
		newText := RenderSubagentText(metadata, body, commentsByName[name])
		parsedMeta, parsedBody, perr := ParseSubagentText(newText)
		if perr != nil || !subagentMetadataEqual(parsedMeta, metadata) || parsedBody != body {
			findings = append(findings, fmt.Sprintf("Subagent cannot round-trip: %s", name))
			continue
		}
		updates = append(updates, FileFragment{"subagents/" + name + ".md", newText})
	}

	subDir := filepath.Join(root, "subagents")
	entries, _ := os.ReadDir(subDir)
	for _, e := range entries {
		n := e.Name()
		if n == ".DS_Store" || n == "Thumbs.db" || n == "desktop.ini" {
			continue
		}
		stem := strings.TrimSuffix(n, filepath.Ext(n))
		if !strings.HasSuffix(n, ".md") || !tableNames[stem] {
			findings = append(findings, fmt.Sprintf("Unregistered subagent entry: %s", filepath.Join(subDir, n)))
		}
	}

	return subagentMigrationResult{updates, findings}, nil
}

func subagentMetadataEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok {
			return false
		}
		if CanonicalJSON(av) != CanonicalJSON(bv) {
			return false
		}
	}
	return true
}

func stringSetEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

type resourceSection struct {
	Name      string
	Comments  string
	TableText string
}

// resourceSections mirrors layout.py's _resource_sections: assign each
// contiguous pre-table comment block (tolerating blank lines above it, but
// stopping at the first non-blank non-comment line, or immediately at any
// blank line once a comment block has started) to the section header that
// follows it.
func resourceSections(content string, headerPattern *regexp.Regexp) []resourceSection {
	matches := headerPattern.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return nil
	}
	starts := make([]int, len(matches))
	for i, m := range matches {
		matchStart := m[0]
		before := pythonSplitLines(content[:matchStart])
		start := matchStart
		hasComment := false
		for j := len(before) - 1; j >= 0; j-- {
			line := before[j]
			trimmed := strings.TrimLeft(line, " \t\n\v\f\r")
			switch {
			case strings.HasPrefix(trimmed, "#"):
				hasComment = true
			case strings.TrimSpace(line) != "" || hasComment:
				goto doneBacktrack
			}
			start -= len(line)
		}
	doneBacktrack:
		starts[i] = start
	}
	sections := make([]resourceSection, len(matches))
	for i, m := range matches {
		name := content[m[2]:m[3]]
		matchStart := m[0]
		comments := content[starts[i]:matchStart]
		end := len(content)
		if i+1 < len(matches) {
			end = starts[i+1]
		}
		tableText := content[matchStart:end]
		sections[i] = resourceSection{Name: name, Comments: comments, TableText: tableText}
	}
	return sections
}

// standaloneComments mirrors _standalone_comments: every line (keeping its
// terminator) whose left-stripped content starts with "#".
func standaloneComments(content string) string {
	var b strings.Builder
	for _, line := range pythonSplitLines(content) {
		if strings.HasPrefix(strings.TrimLeft(line, " \t\n\v\f\r"), "#") {
			b.WriteString(line)
		}
	}
	return b.String()
}

// splitAgentText mirrors _split_agent_text: split a monolithic agents.toml
// document into one TOML fragment per [agents.<name>] table (each
// fragment's own comments + table text, trimmed and newline-terminated),
// verified to parse back to exactly the expected value.
func splitAgentText(content string, expected map[string]any) (map[string]string, []string, error) {
	for name := range expected {
		if ValidateResourceName(name, "agent") != "" {
			return nil, nil, fmt.Errorf("Unsupported Agent name in agents.toml")
		}
	}
	sections := resourceSections(content, legacyAgentHeaderRe)
	fragments := map[string]string{}
	var order []string
	for _, s := range sections {
		if _, exists := fragments[s.Name]; exists {
			return nil, nil, fmt.Errorf("Duplicate Agent table: %s", s.Name)
		}
		fragment := strings.TrimSpace(s.Comments+s.TableText) + "\n"
		parsedDoc, err := DecodeTOML([]byte(fragment))
		if err != nil {
			return nil, nil, fmt.Errorf("Cannot preserve Agent table text: %s", s.Name)
		}
		parsed, _ := parsedDoc["agents"].(map[string]any)
		if len(parsed) != 1 {
			return nil, nil, fmt.Errorf("Cannot preserve Agent table text: %s", s.Name)
		}
		val, ok := parsed[s.Name]
		if !ok || CanonicalJSON(val) != CanonicalJSON(expected[s.Name]) {
			return nil, nil, fmt.Errorf("Cannot preserve Agent table text: %s", s.Name)
		}
		fragments[s.Name] = fragment
		order = append(order, s.Name)
	}
	if len(fragments) != len(expected) {
		return nil, nil, fmt.Errorf("Unsupported Agent table header in agents.toml")
	}
	return fragments, order, nil
}

// marker_version mirrors _marker_version: nil (no error) if layout.toml is
// absent; the parsed version if present and well-formed; an error for an
// unsafe/invalid marker file. Named with Python's underscore convention
// deliberately kept visible in this one spot since it is a direct structural
// counterpart to _marker_version, unlike the rest of this file's CamelCase.
func marker_version(root string) (*int, error) {
	path := filepath.Join(root, LayoutFile)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, layoutErrorf("Unsafe workspace layout marker: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, derr := DecodeTOML(data)
	if derr != nil {
		return nil, layoutErrorf("Invalid workspace layout marker: %s", path)
	}
	if len(doc) != 1 {
		return nil, layoutErrorf("Invalid workspace layout marker: %s", path)
	}
	v, ok := doc["version"]
	if !ok {
		return nil, layoutErrorf("Invalid workspace layout marker: %s", path)
	}
	var version int
	switch x := v.(type) {
	case int64:
		version = int(x)
	default:
		return nil, layoutErrorf("Invalid workspace layout marker: %s", path)
	}
	return &version, nil
}
