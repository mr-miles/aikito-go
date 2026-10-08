// Package registry ports aikito's agents.py: agent platform identities,
// registry loading from a workspace's agents/*.toml files, the 8 bundled
// agent templates, and availability (installed/not_installed/unknown)
// inspection.
package registry

import (
	"fmt"
	"os"
	stdpath "path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/workspace"
)

// BuiltinAgents is the default registry order. This order is product
// policy (what `aikito init` offers, display ordering, etc.), independent
// of host installation signals — confirmed verbatim against agents.py's
// BUILTIN_AGENTS tuple.
var BuiltinAgents = []string{
	"codex",
	"claude-code",
	"agy",
	"opencode",
	"github-copilot",
	"dsh",
	"grok",
	"pi",
}

// AgentRegistryError mirrors Python's AgentRegistryError (raised when
// agents/*.toml cannot be loaded or validated).
type AgentRegistryError struct{ Message string }

func (e *AgentRegistryError) Error() string { return e.Message }

func regErrorf(format string, args ...any) error {
	return &AgentRegistryError{Message: fmt.Sprintf(format, args...)}
}

// AgentAvailability is the tri-state availability of an agent platform with
// supporting evidence. Deliberately not collapsed to a bool: "unknown" is a
// distinct, common outcome for custom/undeclared agents.
type AgentAvailability struct {
	Status   string // "installed" | "not_installed" | "unknown"
	Evidence string
}

func (a AgentAvailability) IsInstalled() bool    { return a.Status == "installed" }
func (a AgentAvailability) IsNotInstalled() bool { return a.Status == "not_installed" }
func (a AgentAvailability) IsUnknown() bool      { return a.Status == "unknown" }

// DetectionCapability mirrors DetectionCapability: a portable install-check
// policy. Paths are stored home-relative (joined with home only when
// checked), matching the Python dataclass.
type DetectionCapability struct {
	Commands []string
	Paths    []string
}

// Agent mirrors the base Agent dataclass: identity and declarative resource
// paths, with no capability (mcp/subagents/runner) information.
//
// Path-safety note (intentional, preserved from the Python source, not an
// oversight): InstructionPath and SkillsPath are only required to be
// non-empty strings before being joined with home — unlike
// ProjectInstructionPath, they get no "must be a safe relative path" check
// at this layer. Don't tighten this without confirming with the user first;
// it would change acceptance behavior for existing workspaces.
type Agent struct {
	Name                   string
	DisplayName            string
	InstructionPath        *string // home-joined; nil if agent declares none
	ProjectInstructionPath *string // project-relative (NOT home-joined); safe-relative-validated
	SkillsPath             *string // home-joined
	Detect                 *DetectionCapability
}

// AgentRegistry is the set of agents loaded from a workspace's agents/*.toml
// (base identity only — no mcp/subagents/runner capabilities; see
// AgentDefinition for the capability-bearing superset).
type AgentRegistry struct {
	agents map[string]Agent
	order  []string // insertion order, for deterministic iteration
}

func (r *AgentRegistry) Get(name string) (Agent, bool) {
	a, ok := r.agents[name]
	return a, ok
}

func (r *AgentRegistry) MustGet(name string) Agent { return r.agents[name] }

func (r *AgentRegistry) Contains(name string) bool {
	_, ok := r.agents[name]
	return ok
}

func (r *AgentRegistry) Len() int { return len(r.agents) }

// InFileOrder returns the same agents ordered as Python's load_agent_document
// reads them: sorted agents/<name>.toml file names. Iteration order decides
// consumer order in grouped targets (e.g. "Codex/DeepSeek Harness/..."),
// which surfaces in `sync global` output.
func (r *AgentRegistry) InFileOrder() *AgentRegistry {
	order := make([]string, len(r.order))
	copy(order, r.order)
	sort.Slice(order, func(i, j int) bool { return order[i]+".toml" < order[j]+".toml" })
	return &AgentRegistry{agents: r.agents, order: order}
}

// Names returns agent names in the order they were loaded (not sorted).
func (r *AgentRegistry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

func (r *AgentRegistry) Values() []Agent {
	out := make([]Agent, 0, len(r.agents))
	for _, name := range r.order {
		out = append(out, r.agents[name])
	}
	return out
}

// isUnsafeRelative replicates the three-part "safe relative path" check
// applied to project_instruction_path and detect.paths entries: not
// absolute (POSIX "/" or a Windows drive anchor like "C:" / "C:foo"), not
// literally "." , and no ".." path component anywhere. Implemented against
// the posix-style "path" package (not "path/filepath") so the check's
// result does not depend on the build OS.
func isUnsafeRelative(raw string) bool {
	norm := strings.ReplaceAll(raw, `\`, "/")
	if strings.HasPrefix(norm, "/") {
		return true
	}
	if len(raw) >= 2 && raw[1] == ':' && isASCIILetter(raw[0]) {
		return true // drive anchor: "C:", "C:foo", "C:\foo"
	}
	if stdpath.Clean(norm) == "." {
		return true
	}
	for _, part := range strings.Split(norm, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func isASCIILetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func resolveHomePath(home string, value any, field, agent string) (string, error) {
	s, ok := value.(string)
	if !ok || s == "" {
		return "", regErrorf("Agent '%s' requires a string '%s'", agent, field)
	}
	return filepath.Join(home, filepath.FromSlash(s)), nil
}

func resolveProjectPath(value any, field, agent string) (string, error) {
	s, ok := value.(string)
	if !ok || s == "" {
		return "", regErrorf("Agent '%s' requires a string '%s'", agent, field)
	}
	if isUnsafeRelative(s) {
		return "", regErrorf("Agent '%s' requires a safe relative '%s', got: %s", agent, field, s)
	}
	return s, nil
}

func asStringList(value any) ([]string, bool) {
	list, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok || s == "" {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func loadDetection(spec map[string]any, name string) (*DetectionCapability, error) {
	section, present := spec["detect"]
	if !present {
		section, present = BundledAgentSpec(name)["detect"]
		if !present {
			return nil, nil
		}
	}
	sectionMap, ok := section.(map[string]any)
	if !ok {
		return nil, regErrorf("Agent '%s' detect section must be a table", name)
	}
	commands, ok := asStringList(valueOrEmptyList(sectionMap["commands"]))
	if !ok {
		return nil, regErrorf("Agent '%s' detect.commands must be a list of non-empty strings", name)
	}
	paths, ok := asStringList(valueOrEmptyList(sectionMap["paths"]))
	if !ok {
		return nil, regErrorf("Agent '%s' detect.paths must be a list of non-empty strings", name)
	}
	for _, p := range paths {
		if isUnsafeRelative(p) {
			return nil, regErrorf("Agent '%s' detect.paths must be safe home-relative paths", name)
		}
	}
	return &DetectionCapability{Commands: commands, Paths: paths}, nil
}

// valueOrEmptyList returns v itself, or an empty []any when v is nil
// (TOML-decoded absent key), matching Python's dict.get(key, []) default.
func valueOrEmptyList(v any) any {
	if v == nil {
		return []any{}
	}
	return v
}

// buildAgentFromSpec builds the base Agent (identity + paths + detect) from
// one raw agent spec table, mirroring the per-agent body of
// AgentRegistry.from_document's loop. Shared by both the full-registry
// loader and the single-agent definition builders (bundled_agent,
// load_agent_definition), exactly as agents.py shares it by constructing a
// one-item registry document in those call sites.
func buildAgentFromSpec(name string, spec map[string]any, home string) (Agent, error) {
	var instrPath *string
	if v, present := spec["instruction_path"]; present {
		p, err := resolveHomePath(home, v, "instruction_path", name)
		if err != nil {
			return Agent{}, err
		}
		instrPath = &p
	}
	var projInstrPath *string
	if v, present := spec["project_instruction_path"]; present {
		p, err := resolveProjectPath(v, "project_instruction_path", name)
		if err != nil {
			return Agent{}, err
		}
		projInstrPath = &p
	}
	var skillsPath *string
	if v, present := spec["skills_path"]; present {
		p, err := resolveHomePath(home, v, "skills_path", name)
		if err != nil {
			return Agent{}, err
		}
		skillsPath = &p
	}
	detect, err := loadDetection(spec, name)
	if err != nil {
		return Agent{}, err
	}
	displayName := name
	if v, present := spec["display_name"]; present {
		if s, ok := v.(string); ok {
			displayName = s
		} else {
			displayName = fmt.Sprintf("%v", v)
		}
	}
	return Agent{
		Name:                   name,
		DisplayName:            displayName,
		InstructionPath:        instrPath,
		ProjectInstructionPath: projInstrPath,
		SkillsPath:             skillsPath,
		Detect:                 detect,
	}, nil
}

// AgentRegistryFromDocument mirrors AgentRegistry.from_document: builds the
// base registry from one parsed agents/*.toml-shaped document (name -> raw
// spec table).
func AgentRegistryFromDocument(document map[string]map[string]any, home string) (*AgentRegistry, error) {
	reg := &AgentRegistry{agents: map[string]Agent{}}
	// Deterministic order: BuiltinAgents first (in policy order), then any
	// remaining custom agent names sorted, matching typical registry
	// construction order without depending on Go's random map iteration.
	seen := map[string]bool{}
	var order []string
	for _, name := range BuiltinAgents {
		if _, ok := document[name]; ok {
			order = append(order, name)
			seen[name] = true
		}
	}
	var custom []string
	for name := range document {
		if !seen[name] {
			custom = append(custom, name)
		}
	}
	sortStrings(custom)
	order = append(order, custom...)

	for _, name := range order {
		spec := document[name]
		agent, err := buildAgentFromSpec(name, spec, home)
		if err != nil {
			return nil, err
		}
		reg.agents[name] = agent
	}
	reg.order = order
	return reg, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// LoadAgentDocument mirrors agents.py's load_agent_document: a friendly
// "workspace not found" check, then the workspace-layout checks
// (RequireCurrentLayout, which load_agent_document in workspace/layout.py
// runs before _read_agent_files) and the raw per-agent file read.
func LoadAgentDocument(aikitoDir string) (map[string]map[string]any, error) {
	info, err := os.Stat(aikitoDir)
	if err != nil || !info.IsDir() {
		return nil, regErrorf(
			"Aikito workspace directory not found: %s. Run 'aikito init workspace' to initialize.",
			aikitoDir,
		)
	}
	if err := workspace.RequireCurrentLayout(aikitoDir); err != nil {
		return nil, regErrorf("%s", err.Error())
	}
	doc, err := workspace.ReadAgentDocuments(aikitoDir)
	if err != nil {
		return nil, regErrorf("%s", err.Error())
	}
	return doc, nil
}

// LoadStrict loads agents/*.toml and returns an error on invalid input.
func LoadStrict(aikitoDir, home string) (*AgentRegistry, error) {
	doc, err := LoadAgentDocument(aikitoDir)
	if err != nil {
		return nil, err
	}
	return AgentRegistryFromDocument(doc, home)
}

// Load loads agents/*.toml, returning an empty registry for invalid input
// (mirrors AgentRegistry.load's broad except-and-fall-back-to-empty).
func Load(aikitoDir, home string) *AgentRegistry {
	reg, err := LoadStrict(aikitoDir, home)
	if err != nil {
		return &AgentRegistry{agents: map[string]Agent{}}
	}
	return reg
}
