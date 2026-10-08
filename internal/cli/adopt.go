// Port of adopt.py: scan agent-native configuration (global instructions,
// MCP servers, subagents), plan workspace files and any built-in Agent
// registrations they need, block on findings, back up the sources, and
// write each file atomically. Structure and names follow adopt.py so the two
// can be compared function by function.
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/pelletier/go-toml/v2"

	"github.com/mr-miles/aikito-go/internal/linkplan"
	"github.com/mr-miles/aikito-go/internal/mcp"
	"github.com/mr-miles/aikito-go/internal/registry"
	"github.com/mr-miles/aikito-go/internal/subagent"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// --- Model ---

type instructionSource struct {
	Agent   string
	Path    string
	Content string
}

type instructionsAdoption struct {
	Sources       []instructionSource
	HasConflict   bool
	MergedContent *string
	TargetPath    string
}

type mcpServerAdoption struct {
	Name        string
	Agents      []string
	Config      *mcp.OrderedObject // canonical keys only, in _canonical_config order
	SourceAgent string
	SourceFile  string
}

type subagentAdoption struct {
	Name         string
	Description  string
	SystemPrompt string
	TargetAgents []string
	SourceFile   string
	// PlatformConfigs keeps insertion order (agent names), like the dict.
	PlatformOrder   []string
	PlatformConfigs map[string]map[string]any
}

type adoptFilePlan struct {
	Path             string
	ExpectedPreImage *string
	Desired          string
	Kind             string // "agent", "instructions", "mcp", "subagent_prompt"
	Name             string
	Action           string // "CREATE", "UPDATE", "NOOP"
	Log              string
}

type agentRegistrationAdoption struct {
	Agent   string
	Display string
	Sources []string
}

type findingAction struct{ Label, Command string }

// adoptFinding is diagnostics.Finding with its actions.
type adoptFinding struct {
	Finding
	Actions []findingAction
}

type adoptDefinitions struct {
	Names []string // _agent_order
	Defs  map[string]registry.AgentDefinition
}

type adoptPlan struct {
	Workspace, Home    string
	Instructions       instructionsAdoption
	MCPServers         []mcpServerAdoption
	Subagents          []subagentAdoption
	FilePlans          []adoptFilePlan
	Findings           []adoptFinding
	Errors             []adoptFinding
	Skipped            []string
	BuiltinMCPs        [][2]string
	BackupSources      []string
	SourceFingerprints [][2]string
	CanApply           bool
	Registrations      []agentRegistrationAdoption
	Registered         map[string]bool // nil: no valid workspace registry
	Definitions        adoptDefinitions
}

func (p adoptPlan) hasConflicts() bool { return p.Instructions.HasConflict }

type adoptSummary struct {
	AgentRegistrations, InstructionUpdates, MCPImports, SubagentImports int
	Conflicts, Errors, Skipped                                          int
}

func (s adoptSummary) totalChanges() int {
	return s.AgentRegistrations + s.InstructionUpdates + s.MCPImports + s.SubagentImports
}

// --- Python value formatting ---

// pyReprStr is Python's repr() of a str.
func pyReprStr(s string) string {
	quote := '\''
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteRune(quote)
	for _, r := range s {
		switch {
		case r == quote || r == '\\':
			b.WriteRune('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case !unicode.IsPrint(r) && r != ' ':
			switch {
			case r < 0x100:
				fmt.Fprintf(&b, `\x%02x`, r)
			case r < 0x10000:
				fmt.Fprintf(&b, `\u%04x`, r)
			default:
				fmt.Fprintf(&b, `\U%08x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteRune(quote)
	return b.String()
}

// pyStr is Python's str() for decoded JSON/TOML/frontmatter values.
func pyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return pyRepr(v)
}

func pyRepr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return pyReprStr(x)
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number, int, int64, float64:
		return workspace.CanonicalJSON(x)
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = pyRepr(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *mcp.OrderedObject:
		parts := make([]string, 0, x.Len())
		for _, k := range x.Keys() {
			val, _ := x.Get(k)
			parts = append(parts, pyReprStr(k)+": "+pyRepr(val))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = pyReprStr(k) + ": " + pyRepr(x[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return fmt.Sprint(x)
	}
}

// pyAlnumUnderscore is "".join(c if c.isalnum() else "_" for c in s).
func pyAlnumUnderscore(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// formatTOMLKey is adopt.py's _format_toml_key.
func formatTOMLKey(key string) string {
	if key == "" {
		return workspace.CanonicalJSON(key)
	}
	for _, r := range key {
		if !(unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '-') {
			return workspace.CanonicalJSON(key)
		}
	}
	return key
}

// formatTOMLValue is adopt.py's _format_toml_value.
func formatTOMLValue(v any) string {
	switch x := v.(type) {
	case string:
		return workspace.CanonicalJSON(x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case json.Number, int, int64, float64:
		return workspace.CanonicalJSON(x)
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = formatTOMLValue(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *mcp.OrderedObject:
		keys := x.Keys()
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			val, _ := x.Get(k)
			parts[i] = formatTOMLKey(k) + " = " + formatTOMLValue(val)
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = formatTOMLKey(k) + " = " + formatTOMLValue(x[k])
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	default:
		return workspace.CanonicalJSON(pyStr(v))
	}
}

// renderMCPServerFile is adopt.py's render_mcp_server_file: the file
// content, or "" and the reason it can't be adopted.
func renderMCPServerFile(srv mcpServerAdoption) (string, bool, string) {
	name := srv.Name
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") || strings.Contains(name, `\`) {
		return "", false, "[ERROR] Invalid MCP server name: " + pyReprStr(name)
	}
	agents := make([]any, len(srv.Agents))
	for i, a := range srv.Agents {
		agents[i] = a
	}
	lines := []string{"agents = " + formatTOMLValue(agents)}
	for _, key := range []string{"command", "url", "args", "env", "transport", "headers"} {
		if val, ok := srv.Config.Get(key); ok && val != nil {
			lines = append(lines, key+" = "+formatTOMLValue(val))
		}
	}
	block := strings.Join(lines, "\n") + "\n"
	var check map[string]any
	if err := toml.Unmarshal([]byte(block), &check); err != nil {
		return "", false, fmt.Sprintf("[SKIP MCP] Skipping invalid server name or config '%s': %v", name, err)
	}
	return block, true, fmt.Sprintf("[ADOPT MCP] Server '%s' (agents: [%s])", name, strings.Join(srv.Agents, ", "))
}

// --- Sources and definitions ---

func isDirectory(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func claudeDesktopSources(home string) []string {
	return []string{
		filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"),
		filepath.Join(home, ".claude", "claude_desktop_config.json"),
	}
}

// externalMCPSources is _EXTERNAL_MCP_SOURCES: third-party import sources
// sharing an Agent's MCP shape (Claude Desktop for Claude Code).
func externalMCPSourcesFor(agent, home string) []string {
	if agent == "claude-code" {
		return claudeDesktopSources(home)
	}
	return nil
}

func externalMCPSources(home string) []string { return claudeDesktopSources(home) }

var mcpSourcePrecedence = []string{"claude-code", "codex", "github-copilot"}

func agentOrderLess(a, b string) bool {
	ia, ib := builtinIndex(a), builtinIndex(b)
	if ia != ib {
		return ia < ib
	}
	return a < b
}

func builtinIndex(name string) int {
	for i, n := range registry.BuiltinAgents {
		if n == name {
			return i
		}
	}
	return len(registry.BuiltinAgents)
}

// mcpSourceOrderLess is _mcp_source_order.
func mcpSourceOrderLess(a, b string, defs map[string]registry.AgentDefinition) bool {
	key := func(name string) (int, int) {
		for i, n := range mcpSourcePrecedence {
			if n == name {
				return i, 0
			}
		}
		underscore := 0
		if d := defs[name]; d.MCP != nil && d.MCP.NameStyle == "underscore" {
			underscore = 1
		}
		return len(mcpSourcePrecedence), underscore
	}
	pa, ua := key(a)
	pb, ub := key(b)
	if pa != pb {
		return pa < pb
	}
	if pa < len(mcpSourcePrecedence) {
		return false
	}
	if ua != ub {
		return ua < ub
	}
	return agentOrderLess(a, b)
}

func recordScanError(errs *[]adoptFinding, message, source, resource string) {
	*errs = append(*errs, adoptFinding{Finding: Finding{
		Status:   "FAIL",
		Code:     "adopt.source_invalid",
		Resource: resource,
		Source:   source,
		Message:  fmt.Sprintf("Cannot inspect adoption source '%s'", resource),
		Reason:   message,
		FixHint:  "Repair or remove the invalid source: " + source,
	}})
}

// adoptionDefinitions is _adoption_definitions: workspace Agents plus
// bundled definitions for unregistered built-ins.
func adoptionDefinitions(aikitoDir, home string, errs *[]adoptFinding) (adoptDefinitions, map[string]bool, bool, error) {
	registered := map[string]registry.AgentDefinition{}
	valid := true
	agentsPath := filepath.Join(aikitoDir, "agents")
	if isDirectory(agentsPath) {
		defs, err := registry.LoadAgentDefinitions(aikitoDir, home)
		if err != nil {
			valid = false
			recordScanError(errs, err.Error(), agentsPath, "agents")
		} else {
			registered = defs
		}
	}
	out := adoptDefinitions{Defs: map[string]registry.AgentDefinition{}}
	regNames := map[string]bool{}
	for name, def := range registered {
		out.Defs[name] = def
		regNames[name] = true
	}
	for _, name := range registry.BuiltinAgents {
		if _, ok := out.Defs[name]; !ok {
			def, err := registry.BundledAgentDefinition(name, home)
			if err != nil {
				return out, nil, false, err
			}
			out.Defs[name] = def
		}
	}
	for name := range out.Defs {
		out.Names = append(out.Names, name)
	}
	sort.Slice(out.Names, func(i, j int) bool { return agentOrderLess(out.Names[i], out.Names[j]) })
	return out, regNames, valid, nil
}

// homeAnchored is _home_anchored: re-anchor a resolved path under home.
func homeAnchored(path, home string) string {
	resolvedHome, err := workspace.ResolvePath(home)
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(resolvedHome, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return filepath.Join(home, rel)
}

func mcpBackupSources(defs adoptDefinitions, home string) []string {
	paths := append([]string{}, externalMCPSources(home)...)
	for _, name := range defs.Names {
		c := defs.Defs[name].MCP
		if c == nil || !c.IsSupported() {
			continue
		}
		a, err := mcp.GetMCPAdapter(c.Adapter)
		if err == nil && a.ImportEntry != nil && !a.MaterializesSecrets {
			paths = append(paths, c.ConfigPath)
		}
	}
	return paths
}

func adoptionSourceFiles(defs adoptDefinitions, home string) []string {
	set := map[string]bool{}
	for _, p := range externalMCPSources(home) {
		set[p] = true
	}
	for _, name := range defs.Names {
		def := defs.Defs[name]
		if c := def.MCP; c != nil && c.IsSupported() {
			if a, err := mcp.GetMCPAdapter(c.Adapter); err == nil && a.ImportEntry != nil {
				set[c.ConfigPath] = true
			}
		}
		if s := def.Subagents; s != nil {
			if a, err := subagent.GetSubagentAdapter(s.ConfigFormat); err == nil && a.ImportFields != nil {
				files, _ := a.ListUnmanaged(homeAnchored(s.ConfigPath, home))
				for _, p := range files {
					set[p] = true
				}
			}
		}
	}
	var out []string
	for p := range set {
		if isRegularFile(p) {
			out = append(out, p)
		}
	}
	return out
}

// pathLess orders paths like pathlib (component by component).
func pathLess(a, b string) bool {
	pa := strings.Split(filepath.ToSlash(a), "/")
	pb := strings.Split(filepath.ToSlash(b), "/")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}

func sortedUniquePaths(paths []string) []string {
	set := map[string]bool{}
	var out []string
	for _, p := range paths {
		if !set[p] {
			set[p] = true
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return pathLess(out[i], out[j]) })
	return out
}

func collectSourcesForBackup(home string, inst instructionsAdoption, subs []subagentAdoption, defs adoptDefinitions) []string {
	var files []string
	for _, s := range inst.Sources {
		if isRegularFile(s.Path) {
			files = append(files, s.Path)
		}
	}
	for _, p := range mcpBackupSources(defs, home) {
		if isRegularFile(p) {
			files = append(files, p)
		}
	}
	for _, s := range subs {
		if isRegularFile(s.SourceFile) {
			files = append(files, s.SourceFile)
		}
	}
	return sortedUniquePaths(files)
}

// --- Instructions ---

func normalizeInstructionsContent(text string) string {
	lines := workspace.PySplitLines(workspace.PyStrip(text))
	for i, l := range lines {
		lines[i] = workspace.PyRStrip(l)
	}
	return strings.Join(lines, "\n")
}

func globalAgentsTemplate() (string, error) { return loadTemplate("global/AGENTS.md") }

func appendDefaultMemoryInstruction(content string) (string, error) {
	tmpl, err := globalAgentsTemplate()
	if err != nil {
		return "", err
	}
	defaultInstruction := workspace.PyRStrip(tmpl)
	normalized := normalizeInstructionsContent(content)
	if strings.Contains(normalized, normalizeInstructionsContent(defaultInstruction)) {
		return normalized + "\n", nil
	}
	if normalized == "" {
		return defaultInstruction, nil
	}
	return normalized + "\n\n" + defaultInstruction, nil
}

// mergeAdoptedInstructions is _merge_adopted_instructions: (conflict, merged).
func mergeAdoptedInstructions(imported, targetPath string) (bool, *string, error) {
	if !isRegularFile(targetPath) {
		return false, &imported, nil
	}
	data, err := os.ReadFile(targetPath)
	if err != nil {
		return false, nil, err
	}
	canonicalContent := string(data)
	canonical := normalizeInstructionsContent(canonicalContent)
	tmpl, err := globalAgentsTemplate()
	if err != nil {
		return false, nil, err
	}
	if canonical == normalizeInstructionsContent(imported) {
		return false, &canonicalContent, nil
	}
	merged, err := appendDefaultMemoryInstruction(imported)
	if err != nil {
		return false, nil, err
	}
	if canonical == normalizeInstructionsContent(tmpl) {
		return false, &merged, nil
	}
	if canonical == normalizeInstructionsContent(merged) {
		return false, &canonicalContent, nil
	}
	return true, nil, nil
}

// legacyInstructionSources is _LEGACY_INSTRUCTION_SOURCES.
var legacyInstructionSources = map[string][]string{"agy": {".gemini/config/AGENTS.md"}}

func scanInstructions(aikitoDir, home string, errs *[]adoptFinding, defs adoptDefinitions) (instructionsAdoption, error) {
	type candidate struct{ agent, path string }
	var candidates []candidate
	var seen []string
	for _, name := range defs.Names {
		var paths []string
		if p := defs.Defs[name].InstructionPath; p != nil {
			paths = append(paths, *p)
		}
		for _, rel := range legacyInstructionSources[name] {
			paths = append(paths, filepath.Join(home, rel))
		}
		for _, p := range paths {
			dup := false
			for _, other := range seen {
				if linkplan.IsSameTargetLocation(p, other) {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			seen = append(seen, p)
			candidates = append(candidates, candidate{name, p})
		}
	}

	target := filepath.Join(aikitoDir, "global", "AGENTS.md")
	var sources []instructionSource
	for _, c := range candidates {
		if !isRegularFile(c.path) {
			continue
		}
		data, err := os.ReadFile(c.path)
		if err != nil {
			recordScanError(errs, "Unable to read instructions: "+err.Error(), c.path, "instructions")
			continue
		}
		if content := workspace.PyStrip(string(data)); content != "" {
			sources = append(sources, instructionSource{c.agent, c.path, content})
		}
	}
	if len(sources) == 0 {
		return instructionsAdoption{TargetPath: target}, nil
	}
	first := normalizeInstructionsContent(sources[0].Content)
	for _, s := range sources[1:] {
		if normalizeInstructionsContent(s.Content) != first {
			return instructionsAdoption{Sources: sources, HasConflict: true, TargetPath: target}, nil
		}
	}
	conflict, merged, err := mergeAdoptedInstructions(sources[0].Content, target)
	if err != nil {
		return instructionsAdoption{}, err
	}
	return instructionsAdoption{Sources: sources, HasConflict: conflict, MergedContent: merged, TargetPath: target}, nil
}

// --- MCP servers ---

// sanitizeAdoptedMCPEntry is adopt.py's _sanitize: env values and
// credential headers become environment references before import, so no
// plaintext secret reaches the workspace.
func sanitizeAdoptedMCPEntry(name string, entry *mcp.OrderedObject) *mcp.OrderedObject {
	out := entry.Clone()
	if env, ok := out.GetOr("env", nil).(*mcp.OrderedObject); ok {
		clean := mcp.NewOrderedObject()
		for _, k := range env.Keys() {
			v, _ := env.Get(k)
			text := pyStr(v)
			if strings.HasPrefix(text, "$") {
				clean.Set(k, text)
			} else {
				clean.Set(k, "${"+k+"}")
			}
		}
		out.Set("env", clean)
	}
	if headers, ok := out.GetOr("headers", nil).(*mcp.OrderedObject); ok {
		safeServer := pyAlnumUnderscore(strings.ToUpper(name))
		clean := mcp.NewOrderedObject()
		for _, k := range headers.Keys() {
			v, _ := headers.Get(k)
			text := pyStr(v)
			isReference := strings.Contains(text, "${") || strings.HasPrefix(text, "$")
			if mcp.IsCredentialHeader(k) && !isReference {
				text = "${AIKITO_" + safeServer + "_" + pyAlnumUnderscore(strings.ToUpper(k)) + "}"
			}
			clean.Set(k, text)
		}
		out.Set("headers", clean)
	}
	return out
}

// canonicalMCPConfig is scan_mcp_servers' _canonical_config.
func canonicalMCPConfig(config *mcp.OrderedObject) *mcp.OrderedObject {
	out := mcp.NewOrderedObject()
	for _, k := range []string{"command", "url", "args", "env", "transport", "headers"} {
		if v, ok := config.Get(k); ok && v != nil {
			out.Set(k, v)
		}
	}
	if out.Has("url") && !out.Has("transport") {
		out.Set("transport", "remote")
	}
	return out
}

func existingMCPNames(aikitoDir string) []string {
	set := map[string]bool{}
	if entries, err := os.ReadDir(filepath.Join(aikitoDir, "mcps")); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".toml") && isRegularFile(filepath.Join(aikitoDir, "mcps", e.Name())) {
				set[strings.TrimSuffix(e.Name(), ".toml")] = true
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(aikitoDir, "mcps.toml")); err == nil {
		var doc map[string]any
		if toml.Unmarshal(data, &doc) == nil {
			if servers, ok := doc["servers"].(map[string]any); ok {
				for k := range servers {
					set[k] = true
				}
			}
		}
	}
	var out []string
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func scanMCPServers(aikitoDir, home string, errs *[]adoptFinding, builtin *[][2]string, defs adoptDefinitions, stderr io.Writer) []mcpServerAdoption {
	agentBuiltin := map[string]map[string]bool{}
	for _, name := range defs.Names {
		if c := defs.Defs[name].MCP; c != nil && len(c.BuiltinServers) > 0 {
			agentBuiltin[name] = map[string]bool{}
			for _, s := range c.BuiltinServers {
				agentBuiltin[name][s] = true
			}
		}
	}
	existing := existingMCPNames(aikitoDir)
	existingSet := map[string]bool{}
	for _, e := range existing {
		existingSet[e] = true
	}

	targetName := func(agent, canonical string) string {
		if d, ok := defs.Defs[agent]; ok && d.MCP != nil && d.MCP.NameStyle == "underscore" {
			return strings.ReplaceAll(canonical, "-", "_")
		}
		return canonical
	}
	var adopted []*mcpServerAdoption
	byName := map[string]*mcpServerAdoption{}
	resolveCanonical := func(name, agent string) string {
		if existingSet[name] {
			return name
		}
		for _, canon := range existing {
			if targetName(agent, canon) == name {
				return canon
			}
		}
		if _, ok := byName[name]; ok {
			return name
		}
		for _, srv := range adopted {
			if targetName(agent, srv.Name) == name {
				return srv.Name
			}
		}
		return name
	}
	register := func(rawName, agent string, config *mcp.OrderedObject, source string) {
		canon := resolveCanonical(rawName, agent)
		cfg := canonicalMCPConfig(config)
		if srv, ok := byName[canon]; ok {
			a, _ := srv.Config.Get("url")
			b, _ := cfg.Get("url")
			if !reflect.DeepEqual(a, b) {
				*errs = append(*errs, adoptFinding{Finding: Finding{
					Status:   "FAIL",
					Code:     "adopt.mcp_conflict",
					Resource: "mcp/" + srv.Name,
					Source:   srv.SourceFile + ", " + source,
					Message:  fmt.Sprintf("MCP server '%s' cannot be merged", srv.Name),
					Reason:   fmt.Sprintf("MCP server '%s' has different URLs in %s and %s", srv.Name, srv.SourceAgent, agent),
					FixHint:  "Align the Agent configurations or rename one MCP server",
				}})
				return
			}
			for _, a := range srv.Agents {
				if a == agent {
					return
				}
			}
			srv.Agents = append(srv.Agents, agent)
			return
		}
		srv := &mcpServerAdoption{Name: canon, Agents: []string{agent}, Config: cfg, SourceAgent: agent, SourceFile: source}
		byName[canon] = srv
		adopted = append(adopted, srv)
	}

	type nativeSource struct {
		agent, path string
		adapter     mcp.Adapter
	}
	names := append([]string{}, defs.Names...)
	sort.SliceStable(names, func(i, j int) bool { return mcpSourceOrderLess(names[i], names[j], defs.Defs) })
	var sources []nativeSource
	for _, agent := range names {
		c := defs.Defs[agent].MCP
		if c == nil || !c.IsSupported() {
			continue
		}
		adapter, err := mcp.GetMCPAdapter(c.Adapter)
		if err != nil || adapter.ImportEntry == nil {
			continue
		}
		for _, p := range append([]string{c.ConfigPath}, externalMCPSourcesFor(agent, home)...) {
			if isRegularFile(p) {
				sources = append(sources, nativeSource{agent, p, adapter})
			}
		}
	}
	for _, src := range sources {
		data, err := os.ReadFile(src.path)
		if err != nil {
			recordScanError(errs, "Failed to read MCP config file: "+err.Error(), src.path, "mcp")
			continue
		}
		entries, err := src.adapter.ReadAllEntries(string(data))
		if err != nil {
			recordScanError(errs, fmt.Sprintf("Failed to parse %s: %v", src.adapter.SyntaxName, err), src.path, "mcp")
			continue
		}
		for _, name := range entries.Keys() {
			v, _ := entries.Get(name)
			entry, _ := v.(*mcp.OrderedObject)
			converted := src.adapter.ImportEntry(sanitizeAdoptedMCPEntry(name, entry))
			if converted == nil {
				fmt.Fprintf(stderr, "[WARN] Skipping unsupported local %s MCP server '%s'\n", defs.Defs[src.agent].DisplayName, name)
				continue
			}
			register(name, src.agent, converted, src.path)
		}
	}

	var out []mcpServerAdoption
	for _, srv := range adopted {
		isBuiltin := true
		for _, ag := range srv.Agents {
			if !agentBuiltin[ag][targetName(ag, srv.Name)] {
				isBuiltin = false
				break
			}
		}
		if isBuiltin {
			for _, ag := range srv.Agents {
				*builtin = append(*builtin, [2]string{srv.Name, ag})
			}
			continue
		}
		out = append(out, *srv)
	}
	return out
}

// --- Subagents ---

func scanSubagents(home string, errs *[]adoptFinding, defs adoptDefinitions) []subagentAdoption {
	var subs []*subagentAdoption
	for _, agent := range defs.Names {
		def := defs.Defs[agent]
		c := def.Subagents
		if c == nil {
			continue
		}
		adapter, err := subagent.GetSubagentAdapter(c.ConfigFormat)
		if err != nil || adapter.ImportFields == nil {
			continue
		}
		files, _ := adapter.ListUnmanaged(homeAnchored(c.ConfigPath, home))
		names := make([]string, 0, len(files))
		for n := range files {
			names = append(names, n)
		}
		sort.Slice(names, func(i, j int) bool { return files[names[i]] < files[names[j]] })
		fields := make([]string, 0, len(adapter.ImportFields))
		for f := range adapter.ImportFields {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		for _, name := range names {
			path := files[name]
			data, err := os.ReadFile(path)
			if err != nil {
				recordScanError(errs, "Failed to read subagent file: "+err.Error(), path, "subagent/"+name)
				continue
			}
			meta, body := workspace.ParseMarkdownFrontmatter(string(data), nil)
			platform := map[string]any{}
			for _, f := range fields {
				if v, ok := meta[f]; ok {
					platform[f] = v
				}
			}
			var existing *subagentAdoption
			for _, s := range subs {
				if s.Name == name {
					existing = s
					break
				}
			}
			if existing != nil {
				found := false
				for _, a := range existing.TargetAgents {
					found = found || a == agent
				}
				if !found {
					existing.TargetAgents = append(existing.TargetAgents, agent)
				}
				if len(adapter.ImportFields) > 0 {
					if _, ok := existing.PlatformConfigs[agent]; !ok {
						existing.PlatformOrder = append(existing.PlatformOrder, agent)
					}
					existing.PlatformConfigs[agent] = platform
				}
				continue
			}
			description := fmt.Sprintf("Adopted subagent %s from %s", name, def.DisplayName)
			if v, ok := meta["description"]; ok {
				description = pyStr(v)
			}
			s := &subagentAdoption{
				Name: name, Description: description, SystemPrompt: body,
				TargetAgents: []string{agent}, SourceFile: path,
				PlatformConfigs: map[string]map[string]any{},
			}
			if len(adapter.ImportFields) > 0 {
				s.PlatformOrder = []string{agent}
				s.PlatformConfigs[agent] = platform
			}
			subs = append(subs, s)
		}
	}
	out := make([]subagentAdoption, len(subs))
	for i, s := range subs {
		out[i] = *s
	}
	return out
}

// --- File plans ---

func readOptional(path string) (*string, error) {
	if !isRegularFile(path) {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := string(data)
	return &s, nil
}

func buildFilePlans(aikitoDir string, inst instructionsAdoption, servers []mcpServerAdoption, subs []subagentAdoption, defs adoptDefinitions, registrations []agentRegistrationAdoption) ([]adoptFilePlan, error) {
	var plans []adoptFilePlan

	for _, r := range registrations {
		content, err := registry.BundledAgentTemplateText(r.Agent)
		if err != nil {
			return nil, err
		}
		plans = append(plans, adoptFilePlan{
			Path:    filepath.Join(aikitoDir, "agents", r.Agent+".toml"),
			Desired: content, Kind: "agent", Name: r.Agent, Action: "CREATE",
			Log: fmt.Sprintf("[REGISTER AGENT] %s (%s)", r.Display, strings.Join(r.Sources, ", ")),
		})
	}

	if len(inst.Sources) > 0 && !inst.HasConflict && inst.MergedContent != nil && inst.TargetPath != "" {
		target := inst.TargetPath
		pre, err := readOptional(target)
		if err != nil {
			return nil, err
		}
		var action, log string
		switch {
		case pre == nil:
			action, log = "CREATE", "[WRITE FILE] Created "+target
		case normalizeInstructionsContent(*pre) != normalizeInstructionsContent(*inst.MergedContent):
			action, log = "UPDATE", "[WRITE FILE] Updated "+target
		default:
			action, log = "NOOP", fmt.Sprintf("[NOOP] Instructions at %s already match", target)
		}
		plans = append(plans, adoptFilePlan{
			Path: target, ExpectedPreImage: pre, Desired: *inst.MergedContent,
			Kind: "instructions", Name: "instructions", Action: action, Log: log,
		})
	}

	for _, srv := range servers {
		content, ok, log := renderMCPServerFile(srv)
		if !ok {
			continue
		}
		path := filepath.Join(aikitoDir, "mcps", srv.Name+".toml")
		pre, err := readOptional(path)
		if err != nil {
			return nil, err
		}
		fp := adoptFilePlan{Path: path, ExpectedPreImage: pre, Kind: "mcp", Name: srv.Name}
		if pre == nil {
			fp.Action, fp.Desired, fp.Log = "CREATE", content, log
		} else {
			fp.Action, fp.Desired, fp.Log = "NOOP", *pre, fmt.Sprintf("[SKIP MCP] Server '%s' already present", srv.Name)
		}
		plans = append(plans, fp)
	}

	for _, sub := range subs {
		if workspace.ValidateResourceName(sub.Name, "subagent") != "" {
			continue
		}
		path := filepath.Join(aikitoDir, "subagents", sub.Name+".md")
		pre, err := readOptional(path)
		if err != nil {
			return nil, err
		}
		fp := adoptFilePlan{Path: path, ExpectedPreImage: pre, Kind: "subagent_prompt", Name: sub.Name}
		if pre != nil {
			fp.Action, fp.Desired = "NOOP", *pre
			fp.Log = fmt.Sprintf("[SKIP SUBAGENT] Subagent '%s' already present", sub.Name)
		} else if desired, err := renderAdoptedSubagent(sub, defs); err != nil {
			fp.Action, fp.Desired = "NOOP", ""
			fp.Log = fmt.Sprintf("[SKIP SUBAGENT] Skipping invalid subagent name/config '%s': %v", sub.Name, err)
		} else {
			fp.Action, fp.Desired = "CREATE", desired
			fp.Log = fmt.Sprintf("[ADOPT SUBAGENT] Subagent '%s' from %s", sub.Name, sub.SourceFile)
		}
		plans = append(plans, fp)
	}
	return plans, nil
}

func renderAdoptedSubagent(sub subagentAdoption, defs adoptDefinitions) (string, error) {
	if workspace.PyStrip(sub.Description) == "" || len(sub.TargetAgents) == 0 || workspace.PyStrip(sub.SystemPrompt) == "" {
		return "", fmt.Errorf("Missing description, target Agent, or instructions")
	}
	for _, agent := range sub.PlatformOrder {
		def, ok := defs.Defs[agent]
		if !ok || def.Subagents == nil {
			return "", fmt.Errorf("Subagent '%s' platform '%s' has no defined subagents capability", sub.Name, agent)
		}
		adapter, err := subagent.GetSubagentAdapter(def.Subagents.ConfigFormat)
		if err != nil {
			return "", err
		}
		if _, err := adapter.ValidateOptions(agent, sub.Name, sub.PlatformConfigs[agent]); err != nil {
			return "", err
		}
	}
	agents := make([]any, len(sub.TargetAgents))
	for i, a := range sub.TargetAgents {
		agents[i] = a
	}
	meta := map[string]any{"description": sub.Description, "agents": agents}
	for _, agent := range sub.PlatformOrder {
		meta[agent] = sub.PlatformConfigs[agent]
	}
	return workspace.RenderSubagentText(meta, workspace.PyRStrip(sub.SystemPrompt)+"\n", ""), nil
}

// --- Findings ---

func collectAdoptFindings(aikitoDir string, inst instructionsAdoption, servers []mcpServerAdoption, subs []subagentAdoption, errs []adoptFinding, defs adoptDefinitions, unregistered map[string][]string) ([]adoptFinding, error) {
	findings := append([]adoptFinding{}, errs...)
	agents := make([]string, 0, len(unregistered))
	for a := range unregistered {
		agents = append(agents, a)
	}
	sort.Strings(agents)
	for _, agent := range agents {
		for _, resource := range unregistered[agent] {
			findings = append(findings, adoptFinding{
				Finding: Finding{
					Status: "FAIL", Code: "adopt.agent_unregistered", Resource: resource,
					Source:  "agents/" + agent + ".toml",
					Message: fmt.Sprintf("%s targets unregistered Agent '%s'", resource, agent),
					Reason:  fmt.Sprintf("Registration agent/%s was skipped", agent),
					FixHint: fmt.Sprintf("Adopt agent/%s or also skip %s", agent, resource),
				},
				Actions: []findingAction{{"Skip", "aikito adopt --skip " + resource}},
			})
		}
	}
	if inst.HasConflict {
		var paths []string
		for _, s := range inst.Sources {
			paths = append(paths, s.Path)
		}
		target := inst.TargetPath
		if target == "" {
			target = filepath.Join(aikitoDir, "global", "AGENTS.md")
		}
		findings = append(findings, adoptFinding{
			Finding: Finding{
				Status: "FAIL", Code: "adopt.instructions_conflict", Resource: "instructions",
				Source:  strings.Join(paths, ", "),
				Message: "Global instructions cannot be adopted automatically",
				Reason:  "Detected instruction sources do not match",
				FixHint: "Review and merge the sources into " + target,
			},
			Actions: []findingAction{
				{"Review", "aikito adopt --dry-run --verbose"},
				{"Skip", "aikito adopt --skip instructions"},
			},
		})
	}
	for _, srv := range servers {
		if _, ok, log := renderMCPServerFile(srv); !ok {
			resource := "mcp/" + srv.Name
			source := srv.SourceFile
			if source == "" {
				source = srv.SourceAgent
			}
			findings = append(findings, adoptFinding{
				Finding: Finding{
					Status: "FAIL", Code: "adopt.invalid_mcp", Resource: resource, Source: source,
					Message: fmt.Sprintf("MCP server '%s' cannot be adopted", srv.Name),
					Reason:  log,
					FixHint: "Repair or remove the definition in the source file",
				},
				Actions: []findingAction{{"Skip", "aikito adopt --skip " + resource}},
			})
		}
	}
	plans, err := buildFilePlans(aikitoDir, inst, servers, subs, defs, nil)
	if err != nil {
		return nil, err
	}
	byName := map[string]subagentAdoption{}
	for _, s := range subs {
		byName[s.Name] = s
	}
	for _, fp := range plans {
		if fp.Kind != "subagent_prompt" || !strings.Contains(fp.Log, "Skipping invalid") {
			continue
		}
		resource := "subagent/" + fp.Name
		findings = append(findings, adoptFinding{
			Finding: Finding{
				Status: "FAIL", Code: "adopt.invalid_subagent", Resource: resource,
				Source:  byName[fp.Name].SourceFile,
				Message: fmt.Sprintf("Subagent '%s' cannot be adopted", fp.Name),
				Reason:  fp.Log,
				FixHint: "Repair or remove the definition in the source file",
			},
			Actions: []findingAction{{"Skip", "aikito adopt --skip " + resource}},
		})
	}
	return findings, nil
}

// registrationNeeds maps unregistered built-in Agents to the created
// resources that target them, in agent order.
func registrationNeeds(plans []adoptFilePlan, servers []mcpServerAdoption, subs []subagentAdoption, registered map[string]bool, defs adoptDefinitions) ([]string, map[string][]string) {
	if registered == nil {
		return nil, map[string][]string{}
	}
	created := map[[2]string]bool{}
	for _, fp := range plans {
		if fp.Action == "CREATE" {
			created[[2]string{fp.Kind, fp.Name}] = true
		}
	}
	needs := map[string][]string{}
	for _, s := range servers {
		if !created[[2]string{"mcp", s.Name}] {
			continue
		}
		for _, a := range s.Agents {
			if !registered[a] {
				needs[a] = append(needs[a], "mcp/"+s.Name)
			}
		}
	}
	for _, s := range subs {
		if !created[[2]string{"subagent_prompt", s.Name}] {
			continue
		}
		for _, a := range s.TargetAgents {
			if !registered[a] {
				needs[a] = append(needs[a], "subagent/"+s.Name)
			}
		}
	}
	var order []string
	for a := range needs {
		if _, ok := defs.Defs[a]; ok {
			order = append(order, a)
		} else {
			delete(needs, a)
		}
	}
	sort.Slice(order, func(i, j int) bool { return agentOrderLess(order[i], order[j]) })
	return order, needs
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func createAdoptPlan(workspaceDir, home string, inst instructionsAdoption, servers []mcpServerAdoption, subs []subagentAdoption, errs []adoptFinding, builtin [][2]string, skipped []string, defs adoptDefinitions, registered map[string]bool) (adoptPlan, error) {
	resourcePlans, err := buildFilePlans(workspaceDir, inst, servers, subs, defs, nil)
	if err != nil {
		return adoptPlan{}, err
	}
	skippedSet := map[string]bool{}
	for _, s := range skipped {
		skippedSet[s] = true
	}
	order, needs := registrationNeeds(resourcePlans, servers, subs, registered, defs)
	var registrations []agentRegistrationAdoption
	unregistered := map[string][]string{}
	for _, a := range order {
		if skippedSet["agent/"+a] {
			unregistered[a] = needs[a]
			continue
		}
		registrations = append(registrations, agentRegistrationAdoption{a, defs.Defs[a].DisplayName, needs[a]})
	}
	agentPlans, err := buildFilePlans(workspaceDir, instructionsAdoption{}, nil, nil, defs, registrations)
	if err != nil {
		return adoptPlan{}, err
	}
	findings, err := collectAdoptFindings(workspaceDir, inst, servers, subs, errs, defs, unregistered)
	if err != nil {
		return adoptPlan{}, err
	}
	backup := collectSourcesForBackup(home, inst, subs, defs)
	var fingerprints [][2]string
	for _, src := range sortedUniquePaths(append(append([]string{}, backup...), adoptionSourceFiles(defs, home)...)) {
		if isRegularFile(src) {
			if sum, err := fileSHA256(src); err == nil {
				fingerprints = append(fingerprints, [2]string{src, sum})
			}
		}
	}
	return adoptPlan{
		Workspace: workspaceDir, Home: home,
		Instructions: inst, MCPServers: servers, Subagents: subs,
		FilePlans: append(agentPlans, resourcePlans...),
		Findings:  findings, Errors: errs, Skipped: skipped, BuiltinMCPs: builtin,
		BackupSources: backup, SourceFingerprints: fingerprints,
		CanApply: len(findings) == 0, Registrations: registrations,
		Registered: registered, Definitions: defs,
	}, nil
}

// buildAdoptPlan is build_adopt_plan. It never writes; doctor's Adoption
// check uses it too.
func buildAdoptPlan(workspaceDir, home string, stderr io.Writer) (adoptPlan, error) {
	var errs []adoptFinding
	var builtin [][2]string
	defs, regNames, valid, err := adoptionDefinitions(workspaceDir, home, &errs)
	if err != nil {
		return adoptPlan{}, err
	}
	inst, err := scanInstructions(workspaceDir, home, &errs, defs)
	if err != nil {
		return adoptPlan{}, err
	}
	servers := scanMCPServers(workspaceDir, home, &errs, &builtin, defs, stderr)
	subs := scanSubagents(home, &errs, defs)
	var registered map[string]bool
	if valid && isDirectory(filepath.Join(workspaceDir, "agents")) {
		registered = regNames
	}
	return createAdoptPlan(workspaceDir, home, inst, servers, subs, errs, builtin, nil, defs, registered)
}

// applyAdoptSkips is apply_adopt_skips.
func applyAdoptSkips(plan adoptPlan, requested []string) (adoptPlan, error) {
	if len(requested) == 0 {
		return plan, nil
	}
	req := map[string]bool{}
	for _, r := range requested {
		req[r] = true
	}
	available := map[string]bool{}
	if len(plan.Instructions.Sources) > 0 {
		available["instructions"] = true
	}
	for _, s := range plan.MCPServers {
		available["mcp/"+s.Name] = true
	}
	for _, s := range plan.Subagents {
		available["subagent/"+s.Name] = true
	}
	for _, r := range plan.Registrations {
		available["agent/"+r.Agent] = true
	}
	var unknown []string
	for r := range req {
		if !available[r] {
			unknown = append(unknown, r)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		var avail []string
		for a := range available {
			avail = append(avail, a)
		}
		sort.Strings(avail)
		availText := strings.Join(avail, ", ")
		if availText == "" {
			availText = "none"
		}
		return plan, fmt.Errorf("Unknown adoption skip target(s): %s. Available targets: %s", strings.Join(unknown, ", "), availText)
	}
	inst := plan.Instructions
	if req["instructions"] {
		inst = instructionsAdoption{TargetPath: plan.Instructions.TargetPath}
	}
	var servers []mcpServerAdoption
	for _, s := range plan.MCPServers {
		if !req["mcp/"+s.Name] {
			servers = append(servers, s)
		}
	}
	var subs []subagentAdoption
	for _, s := range plan.Subagents {
		if !req["subagent/"+s.Name] {
			subs = append(subs, s)
		}
	}
	var skipped []string
	for r := range req {
		skipped = append(skipped, r)
	}
	sort.Strings(skipped)
	return createAdoptPlan(plan.Workspace, plan.Home, inst, servers, subs, plan.Errors, plan.BuiltinMCPs, skipped, plan.Definitions, plan.Registered)
}

func summarizeAdoptPlan(plan adoptPlan) adoptSummary {
	var s adoptSummary
	for _, fp := range plan.FilePlans {
		switch fp.Kind {
		case "agent":
			s.AgentRegistrations++
		case "instructions":
			if fp.Action == "CREATE" || fp.Action == "UPDATE" {
				s.InstructionUpdates++
			}
		case "mcp":
			if fp.Action == "CREATE" {
				s.MCPImports++
			}
		case "subagent_prompt":
			if strings.HasPrefix(fp.Log, "[ADOPT SUBAGENT]") {
				s.SubagentImports++
			}
		}
	}
	if plan.hasConflicts() {
		s.Conflicts = 1
	}
	s.Errors = len(plan.Findings) - s.Conflicts
	s.Skipped = len(plan.Skipped)
	return s
}

func renderAdoptFindingLines(f adoptFinding) []string {
	lines := renderFindingLines(f.Finding, "["+f.Status+"]", false, false)
	for _, a := range f.Actions {
		lines = append(lines, fmt.Sprintf("      %s: %s", a.Label, a.Command))
	}
	return lines
}

// --- Execution ---

// writeTextAtomic is _write_text_atomic.
func writeTextAtomic(target, content string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", target, os.Getpid())
	if err := os.WriteFile(tmp, []byte(content), 0o666); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

// copyFile2 is shutil.copy2: content, permission bits and timestamps.
func copyFile2(src, dest string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dest, data, st.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Chmod(dest, st.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(dest, st.ModTime(), st.ModTime())
}

func backupDestination(backupDir, src, home string) string {
	rel, err := filepath.Rel(home, src)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Join(backupDir, filepath.Base(src))
	}
	return filepath.Join(backupDir, rel)
}

func createAdoptBackup(plan adoptPlan, stdout io.Writer) error {
	sources := plan.BackupSources
	if len(sources) == 0 {
		return nil
	}
	backupDir := filepath.Join(plan.Home, ".aikito", "backups", "adopt_"+time.Now().Format("20060102_150405"))
	if err := os.MkdirAll(backupDir, 0o777); err != nil {
		return err
	}
	for _, src := range sources {
		dest := backupDestination(backupDir, src, plan.Home)
		if err := os.MkdirAll(filepath.Dir(dest), 0o777); err != nil {
			return err
		}
		if err := copyFile2(src, dest); err != nil {
			return err
		}
	}
	fmt.Fprintf(stdout, "\n[BACKUP] Saved %d local agent config backup(s) to: %s\n", len(sources), backupDir)
	return nil
}

func staleError(stderr io.Writer, msg, against string) bool {
	fmt.Fprintf(stderr, "[ERROR] Adoption plan is stale: %s. Re-run 'aikito adopt' to plan against %s.\n", msg, against)
	return false
}

// executeAdoption is execute_adoption; it reports success.
func executeAdoption(plan adoptPlan, dryRun, verbose bool, stdout, stderr io.Writer) bool {
	summary := summarizeAdoptPlan(plan)
	fmt.Fprintln(stdout, "Adoption plan")
	fmt.Fprintln(stdout)
	if summary.AgentRegistrations > 0 {
		fmt.Fprintf(stdout, "  Agents:       %d registration(s)\n", summary.AgentRegistrations)
	}
	fmt.Fprintf(stdout, "  Instructions: %d update(s)\n", summary.InstructionUpdates)
	fmt.Fprintf(stdout, "  MCP servers:  %d import(s)\n", summary.MCPImports)
	fmt.Fprintf(stdout, "  Subagents:    %d import(s)\n", summary.SubagentImports)
	fmt.Fprintf(stdout, "  Conflicts:    %d\n", summary.Conflicts)
	fmt.Fprintf(stdout, "  Errors:       %d\n", summary.Errors)
	fmt.Fprintf(stdout, "  Skipped:      %d\n", summary.Skipped)
	for _, r := range plan.Skipped {
		fmt.Fprintf(stdout, "  [SKIP] %s (explicitly requested)\n", r)
	}

	if summary.totalChanges() == 0 && !plan.hasConflicts() && summary.Errors == 0 {
		if verbose && len(plan.BuiltinMCPs) > 0 {
			fmt.Fprintln(stdout, "\n--- MCP Servers Adoption ---")
			for _, b := range plan.BuiltinMCPs {
				fmt.Fprintf(stdout, "[SKIP MCP] Server '%s' is built-in to %s\n", b[0], b[1])
			}
		}
		fmt.Fprintln(stdout, "\n[OK] No adoptable Agent configuration found. No files were modified.")
		return true
	}

	if len(plan.Findings) > 0 {
		fmt.Fprintln(stderr)
		for _, f := range plan.Findings {
			for _, line := range renderAdoptFindingLines(f) {
				fmt.Fprintln(stderr, line)
			}
		}
		fmt.Fprintln(stderr, "[ERROR] Adoption blocked; no files were modified. Resolve the problems above and rerun 'aikito adopt'.")
		return false
	}

	fmt.Fprintln(stdout, "\nSafe to apply")
	if dryRun && !verbose {
		fmt.Fprintln(stdout, "[DRY-RUN] No files were modified.")
		return true
	}

	// Pre-image and source checks (INV-ADOPT-04/06).
	for _, fp := range plan.FilePlans {
		_, statErr := os.Stat(fp.Path)
		exists := statErr == nil
		if fp.ExpectedPreImage == nil {
			if exists {
				return staleError(stderr, fmt.Sprintf("Target file '%s' was created after plan was generated", fp.Path), "the current workspace state")
			}
			continue
		}
		if !exists {
			return staleError(stderr, fmt.Sprintf("Target file '%s' was deleted after plan was generated", fp.Path), "the current workspace state")
		}
		if data, err := os.ReadFile(fp.Path); err != nil || string(data) != *fp.ExpectedPreImage {
			return staleError(stderr, fmt.Sprintf("Target file '%s' was modified after plan was generated", fp.Path), "the current workspace state")
		}
	}
	for _, src := range plan.BackupSources {
		if !isRegularFile(src) {
			return staleError(stderr, fmt.Sprintf("Source configuration file '%s' was removed after plan was generated", src), "current host state")
		}
	}
	for _, sf := range plan.SourceFingerprints {
		if !isRegularFile(sf[0]) {
			return staleError(stderr, fmt.Sprintf("Source configuration file '%s' was removed after plan was generated", sf[0]), "current host state")
		}
		sum, err := fileSHA256(sf[0])
		if err != nil {
			fmt.Fprintf(stderr, "[ERROR] Cannot read source configuration file '%s': %v\n", sf[0], err)
			return false
		}
		if sum != sf[1] {
			return staleError(stderr, fmt.Sprintf("Source configuration file '%s' was modified after plan was generated", sf[0]), "current host state")
		}
	}

	if len(plan.BackupSources) > 0 && !dryRun {
		if err := createAdoptBackup(plan, stdout); err != nil {
			fmt.Fprintf(stderr, "[ERROR] Failed during adoption backup: %v\n", err)
			return false
		}
	}

	write := func(fp adoptFilePlan) bool {
		if err := writeTextAtomic(fp.Path, fp.Desired); err != nil {
			fmt.Fprintf(stderr, "[ERROR] Failed to write '%s': %v\n", fp.Path, err)
			return false
		}
		return true
	}

	// 0. Agent registrations precede the resources that reference them.
	var agentPlans []adoptFilePlan
	for _, fp := range plan.FilePlans {
		if fp.Kind == "agent" {
			agentPlans = append(agentPlans, fp)
		}
	}
	if len(agentPlans) > 0 && verbose {
		fmt.Fprintln(stdout, "\n--- Agent Registration ---")
	}
	for _, fp := range agentPlans {
		if verbose {
			if dryRun {
				fmt.Fprintln(stdout, strings.Replace(fp.Log, "[REGISTER AGENT]", "[DRY-RUN AGENT] Would register", 1))
			} else {
				fmt.Fprintln(stdout, fp.Log)
			}
		}
		if dryRun {
			continue
		}
		if !write(fp) {
			return false
		}
		if verbose {
			fmt.Fprintf(stdout, "[WRITE FILE] Created %s\n", fp.Path)
		}
	}

	// 1. Instructions.
	inst := plan.Instructions
	var instPlan *adoptFilePlan
	for i := range plan.FilePlans {
		if plan.FilePlans[i].Kind == "instructions" {
			instPlan = &plan.FilePlans[i]
			break
		}
	}
	if len(inst.Sources) > 0 {
		if verbose {
			fmt.Fprintln(stdout, "\n--- Global Instructions Adoption ---")
			var names []string
			for _, s := range inst.Sources {
				names = append(names, s.Agent)
			}
			fmt.Fprintf(stdout, "[MERGE] Instructions from %s match perfectly.\n", strings.Join(names, ", "))
			for _, s := range inst.Sources {
				fmt.Fprintf(stdout, "[SOURCE] %s: %s\n", s.Agent, s.Path)
			}
		}
		if instPlan != nil && (instPlan.Action == "CREATE" || instPlan.Action == "UPDATE") {
			if dryRun {
				if verbose {
					fmt.Fprintf(stdout, "[DRY-RUN WRITE] Would write merged instructions to %s\n", instPlan.Path)
				}
			} else {
				if !write(*instPlan) {
					return false
				}
				if verbose {
					fmt.Fprintf(stdout, "[WRITE FILE] Updated %s\n", instPlan.Path)
				}
			}
		}
	}

	// 2. MCP servers.
	if len(plan.MCPServers) > 0 || len(plan.BuiltinMCPs) > 0 {
		if verbose {
			fmt.Fprintln(stdout, "\n--- MCP Servers Adoption ---")
			for _, b := range plan.BuiltinMCPs {
				fmt.Fprintf(stdout, "[SKIP MCP] Server '%s' is built-in to %s\n", b[0], b[1])
			}
		}
		if !dryRun && len(plan.MCPServers) > 0 {
			if err := os.MkdirAll(filepath.Join(plan.Workspace, "mcps"), 0o777); err != nil {
				fmt.Fprintf(stderr, "[ERROR] Failed to adopt local agent configurations: %v\n", err)
				return false
			}
		}
		for _, srv := range plan.MCPServers {
			var fp *adoptFilePlan
			for i := range plan.FilePlans {
				if plan.FilePlans[i].Kind == "mcp" && plan.FilePlans[i].Name == srv.Name {
					fp = &plan.FilePlans[i]
					break
				}
			}
			if fp == nil {
				continue
			}
			if fp.Action == "NOOP" {
				if verbose {
					fmt.Fprintf(stdout, "[SKIP MCP] Server '%s' already present\n", srv.Name)
				}
				continue
			}
			log := fp.Log
			if dryRun && strings.HasPrefix(log, "[ADOPT MCP]") {
				log = strings.Replace(log, "[ADOPT MCP]", "[DRY-RUN MCP] Would import", 1)
			}
			if verbose {
				fmt.Fprintln(stdout, log)
			}
			if !dryRun {
				if !write(*fp) {
					return false
				}
				if verbose {
					fmt.Fprintf(stdout, "[WRITE FILE] Created %s\n", fp.Path)
				}
			}
		}
	}

	// 3. Subagents.
	if len(plan.Subagents) > 0 {
		if verbose {
			fmt.Fprintln(stdout, "\n--- Subagents Adoption ---")
		}
		for _, fp := range plan.FilePlans {
			if fp.Kind != "subagent_prompt" {
				continue
			}
			log := fp.Log
			if dryRun && strings.HasPrefix(log, "[ADOPT SUBAGENT]") {
				log = strings.Replace(log, "[ADOPT SUBAGENT]", "[DRY-RUN SUBAGENT] Would import", 1)
			}
			if verbose {
				fmt.Fprintln(stdout, log)
			}
		}
		if !dryRun {
			for _, fp := range plan.FilePlans {
				if fp.Kind == "subagent_prompt" && fp.Action == "CREATE" {
					if !write(fp) {
						return false
					}
					if verbose {
						fmt.Fprintf(stdout, "[WRITE FILE] Created %s\n", fp.Path)
					}
				}
			}
		}
	}

	if dryRun {
		fmt.Fprintln(stdout, "\n[DRY-RUN] No files were modified.")
	} else {
		fmt.Fprintln(stdout, "\n[SUCCESS] Adoption executed successfully!")
		fmt.Fprintln(stdout, "Next step: Run 'aikito sync' to check and apply runtime changes.")
	}
	return true
}

// --- cmdAdopt ---

const adoptUsage = "usage: aikito adopt [-h] [--dry-run] [--verbose] [--skip RESOURCE] [target]\n"

// parseAdoptArgs mirrors the adopt argparse parser, including unambiguous
// prefixes of long options (argparse's allow_abbrev).
func parseAdoptArgs(args []string, stderr io.Writer) (dryRun, verbose bool, skip []string, target string, code int) {
	var extra []string
	haveTarget := false
	options := []string{"--dry-run", "--verbose", "--skip"}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			for _, rest := range args[i+1:] {
				if !haveTarget {
					target, haveTarget = rest, true
				} else {
					extra = append(extra, rest)
				}
			}
			break
		}
		if strings.HasPrefix(a, "--") && len(a) > 2 {
			name, value, hasValue := strings.Cut(a, "=")
			var matches []string
			for _, o := range options {
				if o == name {
					matches = []string{o}
					break
				}
				if strings.HasPrefix(o, name) {
					matches = append(matches, o)
				}
			}
			if len(matches) > 1 {
				fmt.Fprintf(stderr, "%saikito adopt: error: ambiguous option: %s could match %s\n", adoptUsage, name, strings.Join(matches, ", "))
				return false, false, nil, "", 2
			}
			if len(matches) == 0 {
				extra = append(extra, a)
				continue
			}
			switch matches[0] {
			case "--dry-run", "--verbose":
				if hasValue {
					fmt.Fprintf(stderr, "%saikito adopt: error: argument %s: ignored explicit argument %s\n", adoptUsage, matches[0], pyReprStr(value))
					return false, false, nil, "", 2
				}
				if matches[0] == "--dry-run" {
					dryRun = true
				} else {
					verbose = true
				}
			case "--skip":
				if hasValue {
					skip = append(skip, value)
					continue
				}
				if i+1 >= len(args) || (strings.HasPrefix(args[i+1], "-") && args[i+1] != "-") {
					fmt.Fprintf(stderr, "%saikito adopt: error: argument --skip: expected one argument\n", adoptUsage)
					return false, false, nil, "", 2
				}
				i++
				skip = append(skip, args[i])
			}
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			extra = append(extra, a)
			continue
		}
		if !haveTarget {
			target, haveTarget = a, true
		} else {
			extra = append(extra, a)
		}
	}
	if len(extra) > 0 {
		return false, false, nil, "", argparseUnrecognized(stderr, extra)
	}
	return dryRun, verbose, skip, target, 0
}

// pathlibNorm is pathlib.Path(p)'s normalisation: collapse "//" and "."
// components and drop a trailing slash, but keep "..".
func pathlibNorm(p string) string {
	if p == "" {
		return "."
	}
	abs := strings.HasPrefix(p, "/")
	var parts []string
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." {
			continue
		}
		parts = append(parts, part)
	}
	joined := strings.Join(parts, "/")
	if abs {
		return "/" + joined
	}
	if joined == "" {
		return "."
	}
	return joined
}

func cmdAdopt(args []string, stdout, stderr io.Writer, env Environment) int {
	dryRun, verbose, skip, target, code := parseAdoptArgs(args, stderr)
	if code != 0 {
		return code
	}
	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	// cli.py main()'s gate applies to the active workspace, not the target.
	if err := requireLayoutLikePython(aikitoDir); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	workspaceDir := aikitoDir
	if target != "" {
		// Path(args.target): normalised but not resolved, so a relative
		// target stays relative (to the process cwd) in messages too.
		workspaceDir = pathlibNorm(target)
	}

	fail := func(err error) int {
		fmt.Fprintf(stderr, "[ERROR] Failed to adopt local agent configurations: %v\n", err)
		return 1
	}
	plan, err := buildAdoptPlan(workspaceDir, env.Home, stderr)
	if err != nil {
		return fail(err)
	}
	plan, err = applyAdoptSkips(plan, skip)
	if err != nil {
		return fail(err)
	}
	if !executeAdoption(plan, dryRun, verbose, stdout, stderr) {
		return 1
	}
	return 0
}
