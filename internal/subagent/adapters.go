package subagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/mr-miles/aikito-go/internal/workspace"
)

// jsonValue renders v the way every subagent render function's
// json.dumps(val, ensure_ascii=False) calls do. Empirically confirmed (by
// running the real Python json.dumps) that the bool/list special-casing
// visible in some Python render functions is a no-op: json.dumps(True) is
// already "true" and json.dumps(["a","b"]) is already '["a", "b"]', so a
// single call covers every branch uniformly.
//
// Known limitation: workspace.CanonicalJSON always sorts object keys
// (sort_keys=True), but these Python call sites pass no sort_keys argument
// (insertion order). This only affects a platform option value that is
// itself a multi-key dict (in practice, only Grok's "mcpInheritance"
// table_or_string field) — scalars and lists are unaffected. Accepted as a
// narrow, documented divergence rather than threading an order-preserving
// map type through the whole canonical-parsing pipeline for one rare field.
func jsonValue(v any) string {
	return workspace.CanonicalJSON(v)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// RenderCopilotMarkdown mirrors render_copilot_markdown. Unlike the other
// adapters, "name" (defaulted from the subagent name if absent) and
// "description" (always overridden) are merged into platformOpts itself and
// the whole merged map is emitted sorted — not a fixed name/description
// prefix followed by sorted extras.
func RenderCopilotMarkdown(name, description string, platformOpts map[string]any, instructions string) (string, error) {
	marker := GetMarkerText(name)
	opts := make(map[string]any, len(platformOpts)+2)
	for k, v := range platformOpts {
		opts[k] = v
	}
	if _, ok := opts["name"]; !ok {
		opts["name"] = name
	}
	opts["description"] = description

	var b strings.Builder
	b.WriteString("---\n")
	for _, k := range sortedKeys(opts) {
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(jsonValue(opts[k]))
		b.WriteString("\n")
	}
	b.WriteString("---\n")
	b.WriteString("<!-- " + marker + " -->\n")
	b.WriteString("\n")
	b.WriteString(instructions)
	b.WriteString("\n")
	return b.String(), nil
}

func renderNameDescriptionMarkdown(name, description string, platformOpts map[string]any, instructions string) string {
	marker := GetMarkerText(name)
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + jsonValue(name) + "\n")
	b.WriteString("description: " + jsonValue(description) + "\n")
	for _, k := range sortedKeys(platformOpts) {
		b.WriteString(k + ": " + jsonValue(platformOpts[k]) + "\n")
	}
	b.WriteString("---\n")
	b.WriteString("<!-- " + marker + " -->\n")
	b.WriteString("\n")
	b.WriteString(instructions)
	b.WriteString("\n")
	return b.String()
}

// RenderClaudeMarkdown mirrors render_claude_markdown.
func RenderClaudeMarkdown(name, description string, platformOpts map[string]any, instructions string) (string, error) {
	return renderNameDescriptionMarkdown(name, description, platformOpts, instructions), nil
}

// RenderAgyMarkdown mirrors render_agy_markdown (structurally identical to
// render_claude_markdown in Python).
func RenderAgyMarkdown(name, description string, platformOpts map[string]any, instructions string) (string, error) {
	return renderNameDescriptionMarkdown(name, description, platformOpts, instructions), nil
}

// RenderGrokMarkdown mirrors render_grok_markdown (same shape as
// RenderClaudeMarkdown in Python too).
func RenderGrokMarkdown(name, description string, platformOpts map[string]any, instructions string) (string, error) {
	return renderNameDescriptionMarkdown(name, description, platformOpts, instructions), nil
}

// RenderOpencodeMarkdown mirrors render_opencode_markdown: no fixed "name"
// line, a literal mode: "subagent" line after description.
func RenderOpencodeMarkdown(name, description string, platformOpts map[string]any, instructions string) (string, error) {
	marker := GetMarkerText(name)
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("description: " + jsonValue(description) + "\n")
	b.WriteString("mode: \"subagent\"\n")
	for _, k := range sortedKeys(platformOpts) {
		b.WriteString(k + ": " + jsonValue(platformOpts[k]) + "\n")
	}
	b.WriteString("---\n")
	b.WriteString("<!-- " + marker + " -->\n")
	b.WriteString("\n")
	b.WriteString(instructions)
	b.WriteString("\n")
	return b.String(), nil
}

// RenderPiMarkdown mirrors render_pi_markdown: only "model" and "tools" are
// ever emitted, in that fixed order (not sorted — there are only ever these
// two possible keys here).
func RenderPiMarkdown(name, description string, platformOpts map[string]any, instructions string) (string, error) {
	marker := GetMarkerText(name)
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + jsonValue(name) + "\n")
	b.WriteString("description: " + jsonValue(description) + "\n")
	if v, ok := platformOpts["model"]; ok {
		b.WriteString("model: " + jsonValue(v) + "\n")
	}
	if v, ok := platformOpts["tools"]; ok {
		b.WriteString("tools: " + jsonValue(v) + "\n")
	}
	b.WriteString("---\n")
	b.WriteString("<!-- " + marker + " -->\n")
	b.WriteString("\n")
	b.WriteString(instructions)
	b.WriteString("\n")
	return b.String(), nil
}

// RenderCodexTOML mirrors render_codex_toml: a marker comment, name/
// description/sorted-opts as TOML key = value assignments, then the
// instructions as a TOML literal multi-line string
// (developer_instructions = ”'...”'), round-trip-validated as TOML before
// returning.
func RenderCodexTOML(name, description string, platformOpts map[string]any, instructions string) (string, error) {
	if strings.Contains(instructions, "'''") {
		return "", configErrorf(
			"Instructions for subagent '%s' contain triple single-quotes ('''), which is invalid for codex_toml literal string.",
			name,
		)
	}
	marker := GetMarkerText(name)
	var b strings.Builder
	b.WriteString("# " + marker + "\n")
	b.WriteString("name = " + jsonValue(name) + "\n")
	b.WriteString("description = " + jsonValue(description) + "\n")
	for _, k := range sortedKeys(platformOpts) {
		b.WriteString(k + " = " + jsonValue(platformOpts[k]) + "\n")
	}
	b.WriteString("developer_instructions = '''\n")
	b.WriteString(instructions)
	b.WriteString("\n'''\n")
	rendered := b.String()

	var doc map[string]any
	if err := toml.Unmarshal([]byte(rendered), &doc); err != nil {
		return "", configErrorf("Rendered Codex TOML for '%s' is invalid TOML: %v", name, err)
	}
	return rendered, nil
}

// RenderDSHCordisSubagent mirrors render_dsh_cordis_subagent: a single
// Cordis YAML list item for the "@deepseek-ai/dsh-tool-subagent" plugin.
//
// Known limitation: instructions are split on "\n" only, not Python's full
// str.splitlines() (which also splits on \r, \v, \f, and a few Unicode
// line-boundary code points) — acceptable for real-world subagent
// instructions, which are authored as plain \n-terminated Markdown.
func RenderDSHCordisSubagent(name, description string, platformOpts map[string]any, instructions string) (string, error) {
	marker := GetMarkerText(name)
	provider := "spawn"
	if v, ok := platformOpts["provider"]; ok {
		provider = toPyStr(v)
	}
	var b strings.Builder
	b.WriteString("- id: aikito-subagent-" + name + "\n")
	b.WriteString("  # " + marker + "\n")
	b.WriteString("  name: '@deepseek-ai/dsh-tool-subagent'\n")
	b.WriteString("  config:\n")
	b.WriteString("    provider: " + provider + "\n")
	b.WriteString("    toolName: " + name + "\n")

	_, hasModel := platformOpts["model"]
	_, hasMaxTokens := platformOpts["maxTokens"]
	if hasModel || hasMaxTokens {
		b.WriteString("    agentOptions:\n")
		if hasModel {
			b.WriteString("      model: " + jsonValue(platformOpts["model"]) + "\n")
		}
		if hasMaxTokens {
			b.WriteString("      maxTokens: " + toPyStr(platformOpts["maxTokens"]) + "\n")
		}
	}
	if v, ok := platformOpts["backgroundMode"]; ok {
		b.WriteString("    backgroundMode: " + toPyStr(v) + "\n")
	}
	if toolsAny, ok := platformOpts["tools"]; ok {
		if tools, ok := toolsAny.([]any); ok {
			b.WriteString("    toolFilter:\n")
			b.WriteString("      allow:\n")
			for _, t := range tools {
				b.WriteString("        - " + toPyStr(t) + "\n")
			}
		}
	}

	var indented []string
	for _, line := range splitInstructionLines(instructions) {
		if line == "" {
			indented = append(indented, "")
		} else {
			indented = append(indented, "      "+line)
		}
	}
	b.WriteString("    persona: |-\n")
	b.WriteString(strings.Join(indented, "\n"))
	return b.String(), nil
}

// splitInstructionLines approximates Python's str.splitlines() for the
// common \n-only case: unlike strings.Split(s, "\n"), it does not produce a
// trailing empty element when s ends with "\n" (splitlines splits BETWEEN
// lines; a trailing newline is a terminator, not the start of a further
// empty line). This does not handle the full set of Unicode line-boundary
// code points Python's splitlines() also recognizes (\r, \v, \f, etc.) —
// acceptable for subagent instructions, which are authored as plain
// \n-terminated Markdown; see RenderDSHCordisSubagent's doc comment.
func splitInstructionLines(s string) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// toPyStr mirrors Python's bare str(value) used for raw (non-JSON-quoted)
// interpolation of maxTokens/backgroundMode/tools-items/provider, which in
// practice only ever receive the JSON-decoded scalar types this package's
// callers produce: string, bool, json.Number, or nil.
func toPyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return x
	default:
		return jsonValueBare(x)
	}
}

func jsonValueBare(v any) string {
	// Numbers (json.Number/int64/float64) render the same whether or not
	// they go through JSON encoding, since Python's bare str(int)/str(float)
	// and json.dumps(int)/json.dumps(float) agree for ordinary values.
	return workspace.CanonicalJSON(v)
}

// --- Layout / allowed-fields / option-type model (subagent_adapters.py SubagentAdapter) ---

// Layout distinguishes a per-subagent-file native target (most agents) from
// DSH's single shared cordis.patch.yml file holding every subagent as one
// list item.
type Layout string

const (
	LayoutPerFile     Layout = "per_file"
	LayoutSharedPatch Layout = "shared_patch"
)

// OptionKind is the validation kind for one platform-option field, mirroring
// subagent_adapters.py's option_types values.
type OptionKind string

const (
	OptString          OptionKind = "string"
	OptTools           OptionKind = "tools"
	OptStrings         OptionKind = "strings"
	OptBoolean         OptionKind = "boolean"
	OptTableOrString   OptionKind = "table_or_string"
	OptPositiveInteger OptionKind = "positive_integer"
	OptBackground      OptionKind = "background"
)

// RenderFunc is one platform's subagent_adapters.py render_* function.
type RenderFunc func(name, description string, platformOpts map[string]any, instructions string) (string, error)

// Adapter mirrors subagent_adapters.py's SubagentAdapter dataclass. Fields
// tied to config_runtime.py's ConfigOperation (the Python "merge" field) are
// intentionally omitted — that generic precondition framework isn't ported
// yet; ReadItem/ManagedEntries/TargetPath/AvailabilityCheck, which operate
// on plain filesystem paths, are kept since they don't need it.
type Adapter struct {
	Render            RenderFunc
	Extension         string
	Layout            Layout
	AllowedFields     map[string]struct{}
	OptionTypes       map[string]OptionKind
	TargetPath        func(root, name string) string
	ManagedEntries    func(root string) (map[string]string, error)
	ReadItem          func(text, name string) (string, bool)
	AvailabilityCheck func(root string) (bool, string, error)
	// ImportFields is nil when the platform's native frontmatter is not
	// adoptable at all; a non-nil (possibly empty) set when it is.
	ImportFields map[string]struct{}
}

func fieldSet(fields ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		m[f] = struct{}{}
	}
	return m
}

// ResolveTargetPath mirrors SubagentAdapter.resolve_target_path.
func (a Adapter) ResolveTargetPath(root, name string) string {
	if a.TargetPath != nil {
		return a.TargetPath(root, name)
	}
	return filepath.Join(root, name+a.Extension)
}

// ListManaged mirrors SubagentAdapter.list_managed.
func (a Adapter) ListManaged(root string) (map[string]string, error) {
	if a.ManagedEntries != nil {
		return a.ManagedEntries(root)
	}
	return scanExtensionDir(root, a.Extension, true)
}

// ListUnmanaged mirrors SubagentAdapter.list_unmanaged.
func (a Adapter) ListUnmanaged(root string) (map[string]string, error) {
	if a.Layout != LayoutPerFile || a.TargetPath != nil {
		return map[string]string{}, nil
	}
	return scanExtensionDir(root, a.Extension, false)
}

func scanExtensionDir(root, extension string, wantMarked bool) (map[string]string, error) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return map[string]string{}, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	byName := map[string]os.DirEntry{}
	for _, e := range entries {
		names = append(names, e.Name())
		byName[e.Name()] = e
	}
	sort.Strings(names)
	result := map[string]string{}
	for _, name := range names {
		e := byName[name]
		if e.IsDir() || !strings.HasSuffix(name, extension) {
			continue
		}
		path := filepath.Join(root, name)
		// Path.is_file() follows symlinks, so a symlinked file counts.
		fi, err := os.Stat(path)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		marked := HasAikitoMarker(path)
		if marked != wantMarked {
			continue
		}
		result[strings.TrimSuffix(name, extension)] = path
	}
	return result, nil
}

func agyManaged(root string) (map[string]string, error) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return map[string]string{}, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	result := map[string]string{}
	for _, name := range names {
		dir := filepath.Join(root, name)
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		agentMD := filepath.Join(dir, "agent.md")
		if HasAikitoMarker(agentMD) {
			result[name] = agentMD
		}
	}
	return result, nil
}

func cordisManaged(root string) (map[string]string, error) {
	text := ""
	if info, err := os.Stat(root); err == nil && info.Mode().IsRegular() {
		data, err := os.ReadFile(root)
		if err != nil {
			return nil, err
		}
		text = string(data)
	}
	result := map[string]string{}
	for _, name := range GetAllDSHCordisSubagents(text) {
		result[name] = root
	}
	return result, nil
}

// CheckCodexEnabled mirrors check_codex_enabled: reads
// <home>/.codex/config.toml and refuses only when it explicitly contains
// [agents] enabled = false. Any read/parse error, or a missing file, is
// treated as "enabled" (best-effort check, not a security boundary).
func CheckCodexEnabled(home string) (bool, string, error) {
	return checkCodexConfig(filepath.Join(home, ".codex", "config.toml"))
}

func codexAvailable(root string) (bool, string, error) {
	// root is the subagents config_path (".codex/agents"); its parent holds
	// config.toml, mirroring _codex_available's root.parent / "config.toml".
	return checkCodexConfig(filepath.Join(filepath.Dir(root), "config.toml"))
}

func checkCodexConfig(path string) (bool, string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return true, "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return true, "", nil
	}
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return true, "", nil
	}
	agentsSec, ok := doc["agents"].(map[string]any)
	if ok {
		if enabled, ok := agentsSec["enabled"].(bool); ok && !enabled {
			return false, "Codex agents capability is explicitly disabled in ~/.codex/config.toml ([agents] enabled = false)", nil
		}
	}
	return true, "", nil
}

// SUBAGENT_ADAPTERS mirrors subagent_adapters.py's SUBAGENT_ADAPTERS table.
var SUBAGENT_ADAPTERS = map[string]Adapter{
	"codex_toml": {
		Render:            RenderCodexTOML,
		Extension:         ".toml",
		Layout:            LayoutPerFile,
		AllowedFields:     fieldSet("model", "model_reasoning_effort"),
		AvailabilityCheck: codexAvailable,
	},
	"claude_markdown": {
		Render:        RenderClaudeMarkdown,
		Extension:     ".md",
		Layout:        LayoutPerFile,
		AllowedFields: fieldSet("model", "effort"),
		ImportFields:  fieldSet(),
	},
	"agy_markdown": {
		Render:         RenderAgyMarkdown,
		Extension:      ".md",
		Layout:         LayoutPerFile,
		AllowedFields:  fieldSet("tools", "model"),
		OptionTypes:    map[string]OptionKind{"tools": OptTools},
		TargetPath:     func(root, name string) string { return filepath.Join(root, name, "agent.md") },
		ManagedEntries: agyManaged,
	},
	"copilot_markdown": {
		Render:    RenderCopilotMarkdown,
		Extension: ".agent.md",
		Layout:    LayoutPerFile,
		AllowedFields: fieldSet(
			"name", "description", "model", "tools", "target",
			"disable-model-invocation", "user-invocable",
		),
		OptionTypes: map[string]OptionKind{
			"tools":                    OptTools,
			"disable-model-invocation": OptBoolean,
			"user-invocable":           OptBoolean,
		},
		ImportFields: fieldSet(
			"name", "model", "tools", "target",
			"disable-model-invocation", "user-invocable",
		),
	},
	"opencode_markdown": {
		Render:        RenderOpencodeMarkdown,
		Extension:     ".md",
		Layout:        LayoutPerFile,
		AllowedFields: fieldSet("model"),
	},
	"dsh_cordis_subagent": {
		Render:        RenderDSHCordisSubagent,
		Extension:     "",
		Layout:        LayoutSharedPatch,
		AllowedFields: fieldSet("provider", "tools", "model", "maxTokens", "backgroundMode"),
		OptionTypes: map[string]OptionKind{
			"tools":          OptTools,
			"maxTokens":      OptPositiveInteger,
			"backgroundMode": OptBackground,
		},
		TargetPath:     func(root, _ string) string { return root },
		ManagedEntries: cordisManaged,
		ReadItem:       GetDSHCordisSubagentItem,
	},
	"grok_markdown": {
		Render:    RenderGrokMarkdown,
		Extension: ".md",
		Layout:    LayoutPerFile,
		AllowedFields: fieldSet(
			"model", "reasoning_effort", "prompt_mode", "permission_mode",
			"agents_md", "tools", "skills", "mcpInheritance",
		),
		OptionTypes: map[string]OptionKind{
			"agents_md":      OptBoolean,
			"tools":          OptTools,
			"skills":         OptStrings,
			"mcpInheritance": OptTableOrString,
		},
	},
	"pi_markdown": {
		Render:        RenderPiMarkdown,
		Extension:     ".md",
		Layout:        LayoutPerFile,
		AllowedFields: fieldSet("model", "tools"),
		OptionTypes:   map[string]OptionKind{"tools": OptTools},
	},
}

// GetSubagentAdapter mirrors get_subagent_adapter.
func GetSubagentAdapter(configFormat string) (Adapter, error) {
	a, ok := SUBAGENT_ADAPTERS[configFormat]
	if !ok {
		return Adapter{}, configErrorf("Unsupported subagent config_format '%s'", configFormat)
	}
	return a, nil
}

// ValidateOptions mirrors SubagentAdapter.validate_options: rejects unknown
// fields and type-checks each known field's value per its OptionKind,
// returning the validated (possibly auto-wrapped) option map. The
// kind=="tools" + bare-string-value auto-wrap-into-a-one-element-list
// quirk is preserved exactly, including that the *wrapped* value is what
// ends up in the returned map.
func (a Adapter) ValidateOptions(agentName, subagentName string, options map[string]any) (map[string]any, error) {
	validated := make(map[string]any, len(options))
	for _, key := range sortedKeys(options) {
		value := options[key]
		if _, ok := a.AllowedFields[key]; !ok {
			allowed := make([]string, 0, len(a.AllowedFields))
			for f := range a.AllowedFields {
				allowed = append(allowed, f)
			}
			sort.Strings(allowed)
			return nil, configErrorf(
				"Subagent '%s' contains unknown field '%s' for platform '%s'. Allowed: %v",
				subagentName, key, agentName, allowed,
			)
		}
		kind := OptString
		if k, ok := a.OptionTypes[key]; ok {
			kind = k
		}
		if kind == OptTools {
			if s, ok := value.(string); ok {
				value = []any{s}
			}
		}
		if !validOptionValue(kind, value) {
			return nil, configErrorf(
				"Subagent '%s' field '%s' for platform '%s' has invalid value (%s)",
				subagentName, key, agentName, kind,
			)
		}
		validated[key] = value
	}
	return validated, nil
}

func validOptionValue(kind OptionKind, value any) bool {
	switch kind {
	case OptString:
		s, ok := value.(string)
		return ok && strings.TrimSpace(s) != ""
	case OptTools, OptStrings:
		items, ok := value.([]any)
		if !ok {
			return false
		}
		for _, item := range items {
			s, ok := item.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return false
			}
		}
		return true
	case OptBoolean:
		_, ok := value.(bool)
		return ok
	case OptTableOrString:
		switch value.(type) {
		case string, map[string]any:
			return true
		default:
			return false
		}
	case OptPositiveInteger:
		return isPositiveInteger(value)
	case OptBackground:
		s, ok := value.(string)
		return ok && (s == "one-shot" || s == "continuable")
	default:
		return false
	}
}

// isPositiveInteger mirrors Python's
// isinstance(value, int) and not isinstance(value, bool) and value > 0.
// Values decoded by workspace.DecodeStrictJSON (the canonical subagent
// frontmatter parser) carry integers as json.Number; int64/int are accepted
// too for values constructed directly in Go (e.g. tests).
func isPositiveInteger(value any) bool {
	switch v := value.(type) {
	case json.Number:
		if strings.ContainsAny(string(v), ".eE") {
			return false
		}
		n, err := v.Int64()
		return err == nil && n > 0
	case int64:
		return v > 0
	case int:
		return v > 0
	default:
		return false
	}
}
