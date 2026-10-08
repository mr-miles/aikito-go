package registry

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// MCPCapability mirrors MCPCapability: the MCP capability declared in an
// agent's [agents.<name>.mcp] section.
type MCPCapability struct {
	ConfigPath     string // home-joined (NOT resolved — matches Python, which only .joins, never .resolve()s this)
	ConfigFormat   string // default "unsupported" (sentinel meaning "not supported")
	NameStyle      string // default "verbatim"
	Reason         string
	LiveCommand    []string
	AuthCommand    []string
	BuiltinServers []string
	// Adapter is the semantic adapter key; differs from ConfigFormat only
	// when one file format carries different MCP semantics (e.g. Codex vs
	// Grok TOML). Defaults to ConfigFormat when neither declared nor
	// inherited from the bundled template (mirrors MCPCapability.__post_init__).
	Adapter string
}

func (m MCPCapability) IsSupported() bool { return m.ConfigFormat != "unsupported" }

// SubagentCapability mirrors SubagentCapability: a native subagent target
// declared in an agent's subagents section. Unlike MCPCapability.ConfigPath,
// these paths ARE resolved (home-joined then symlink-resolved) — this
// asymmetry is intentional and present in the Python source.
type SubagentCapability struct {
	ConfigPath   string
	ConfigFormat string
	RequiresPath *string
}

// RunnerCapability mirrors RunnerCapability: the command and environment
// used to launch an agent for maintenance.
type RunnerCapability struct {
	Command []string
	Env     map[string]string
}

// AgentDefinition mirrors AgentDefinition: the base Agent plus its
// capabilities. A nil capability field means the agent declares no
// corresponding section (not an empty/zero capability).
type AgentDefinition struct {
	Agent
	MCP       *MCPCapability
	Subagents *SubagentCapability
	Runner    *RunnerCapability
}

// stringify approximates Python's str(value) for the handful of scalar
// kinds that can legally appear here (TOML strings/bools/numbers). This
// exists only to replicate a deliberately-preserved legacy coercion
// (agents.py: "Legacy coercion (str()/tuple()) is preserved from v1.50.0 on
// purpose") for fields whose schema expects a string but whose loader
// doesn't strictly enforce it.
func stringify(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number:
		return string(x)
	case nil:
		return "None"
	default:
		return fmt.Sprintf("%v", x)
	}
}

func stringListFieldStrict(m map[string]any, key, name, field string) ([]string, error) {
	list, ok := asStringList(valueOrEmptyList(m[key]))
	if !ok {
		return nil, regErrorf("Agent '%s' has invalid %s", name, field)
	}
	return list, nil
}

// loadMCPCapability mirrors _load_mcp_capability, including the
// adapter-inheritance rule: an explicit adapter in the workspace spec is
// used verbatim (must be non-empty); an ABSENT adapter inherits the bundled
// template's adapter only when the declared config_format still matches the
// bundled template's config_format for this same agent name (conditioned on
// format match — unlike detect's unconditional fallback-to-bundled-template
// when the key is merely absent). If adapter is still empty after that, it
// defaults to config_format (MCPCapability.__post_init__).
func loadMCPCapability(spec map[string]any, name, home string) (*MCPCapability, error) {
	mcpAny, present := spec["mcp"]
	if !present {
		return nil, nil
	}
	mcpMap, ok := mcpAny.(map[string]any)
	if !ok {
		return nil, regErrorf("Agent '%s' mcp section must be a table", name)
	}

	configPath, err := resolveHomePath(home, mcpMap["config_path"], "mcp.config_path", name)
	if err != nil {
		return nil, err
	}

	builtinServers, ok := asStringList(valueOrEmptyList(mcpMap["builtin_mcps"]))
	if !ok {
		return nil, regErrorf("Agent '%s' mcp.builtin_mcps must be a list of strings", name)
	}

	configFormat := "unsupported"
	if v, present := mcpMap["config_format"]; present {
		configFormat = stringify(v)
	}

	var adapter string
	if adapterAny, present := mcpMap["adapter"]; present {
		a, ok := adapterAny.(string)
		if !ok || a == "" {
			return nil, regErrorf("Agent '%s' mcp.adapter must be a non-empty string", name)
		}
		adapter = a
	} else {
		if bundledMCP, ok := BundledAgentSpec(name)["mcp"].(map[string]any); ok {
			bundledFormat, _ := bundledMCP["config_format"].(string)
			if bundledFormat == configFormat {
				if a, ok := bundledMCP["adapter"].(string); ok {
					adapter = a
				}
			}
		}
	}
	if adapter == "" {
		adapter = configFormat
	}

	nameStyle := "verbatim"
	if v, present := mcpMap["name_style"]; present {
		nameStyle = stringify(v)
	}
	reason := ""
	if v, present := mcpMap["reason"]; present {
		reason = stringify(v)
	}
	liveCommand, err := stringListFieldStrict(mcpMap, "live_command", name, "mcp.live_command")
	if err != nil {
		return nil, err
	}
	authCommand, err := stringListFieldStrict(mcpMap, "auth_command", name, "mcp.auth_command")
	if err != nil {
		return nil, err
	}

	return &MCPCapability{
		ConfigPath:     configPath,
		ConfigFormat:   configFormat,
		Adapter:        adapter,
		NameStyle:      nameStyle,
		Reason:         reason,
		LiveCommand:    liveCommand,
		AuthCommand:    authCommand,
		BuiltinServers: builtinServers,
	}, nil
}

// loadSubagentCapability mirrors _load_subagent_capability.
func loadSubagentCapability(spec map[string]any, name, home string) (*SubagentCapability, error) {
	sectionAny, present := spec["subagents"]
	if !present {
		return nil, nil
	}
	section, ok := sectionAny.(map[string]any)
	if !ok {
		return nil, regErrorf("Agent '%s' subagents section must be a table", name)
	}
	configPath, cpOK := section["config_path"].(string)
	configFormat, cfOK := section["config_format"].(string)
	if !cpOK || configPath == "" || !cfOK || configFormat == "" {
		return nil, regErrorf("Agent '%s' subagents section missing 'config_path' or 'config_format'", name)
	}
	var requiresPath *string
	if rpAny, present := section["requires_path"]; present {
		rp, ok := rpAny.(string)
		if !ok || rp == "" {
			return nil, regErrorf("Agent '%s' subagents 'requires_path' must be a non-empty string", name)
		}
		resolved, err := workspace.ResolvePath(filepath.Join(home, filepath.FromSlash(rp)))
		if err != nil {
			return nil, err
		}
		requiresPath = &resolved
	}
	resolvedConfigPath, err := workspace.ResolvePath(filepath.Join(home, filepath.FromSlash(configPath)))
	if err != nil {
		return nil, err
	}
	return &SubagentCapability{
		ConfigPath:   resolvedConfigPath,
		ConfigFormat: configFormat,
		RequiresPath: requiresPath,
	}, nil
}

// loadRunnerCapability mirrors _load_runner_capability.
func loadRunnerCapability(spec map[string]any, name, configPath string) (*RunnerCapability, error) {
	runnerAny, present := spec["runner"]
	if !present {
		return nil, nil
	}
	runner, ok := runnerAny.(map[string]any)
	if !ok {
		return nil, regErrorf("Agent '%s' has no runner configuration in %s", name, configPath)
	}
	command, ok := asStringList(valueOrEmptyList(runner["command"]))
	if !ok || len(command) == 0 {
		return nil, regErrorf("Agent '%s' has invalid runner.command in %s", name, configPath)
	}
	env := map[string]string{}
	if envAny, present := runner["env"]; present {
		envMap, ok := envAny.(map[string]any)
		if !ok {
			return nil, regErrorf("Agent '%s' has invalid runner.env in %s", name, configPath)
		}
		for k, v := range envMap {
			s, ok := v.(string)
			if k == "" || !ok {
				return nil, regErrorf("Agent '%s' has invalid runner.env in %s", name, configPath)
			}
			env[k] = s
		}
	}
	return &RunnerCapability{Command: command, Env: env}, nil
}

// buildAgentDefinition mirrors _build_agent_definition: combine an
// already-built base Agent with its mcp/subagents/runner capabilities.
func buildAgentDefinition(baseAgent Agent, spec map[string]any, home, configPath string) (AgentDefinition, error) {
	mcp, err := loadMCPCapability(spec, baseAgent.Name, home)
	if err != nil {
		return AgentDefinition{}, err
	}
	subagents, err := loadSubagentCapability(spec, baseAgent.Name, home)
	if err != nil {
		return AgentDefinition{}, err
	}
	runner, err := loadRunnerCapability(spec, baseAgent.Name, configPath)
	if err != nil {
		return AgentDefinition{}, err
	}
	return AgentDefinition{Agent: baseAgent, MCP: mcp, Subagents: subagents, Runner: runner}, nil
}

// BundledAgentDefinition mirrors bundled_agent_definition: build a built-in
// definition from its bundled template only. Errors for a name with no
// bundled spec at all (unlike BundledAgent, which tolerates that case).
func BundledAgentDefinition(name, home string) (AgentDefinition, error) {
	spec := BundledAgentSpec(name)
	if len(spec) == 0 {
		return AgentDefinition{}, regErrorf("Agent '%s' has no bundled definition", name)
	}
	baseAgent, err := buildAgentFromSpec(name, spec, home)
	if err != nil {
		return AgentDefinition{}, err
	}
	configPath := "templates/agents/" + name + ".toml"
	return buildAgentDefinition(baseAgent, spec, home, configPath)
}

// LoadAgentDefinition mirrors load_agent_definition: load one agent's full
// definition without validating unrelated agent declarations in the same
// workspace.
func LoadAgentDefinition(aikitoDir, home, name string) (AgentDefinition, error) {
	document, err := LoadAgentDocument(aikitoDir)
	if err != nil {
		return AgentDefinition{}, err
	}
	configPath := filepath.Join(aikitoDir, "agents", name+".toml")
	spec, ok := document[name]
	if !ok {
		return AgentDefinition{}, regErrorf("Agent '%s' not found in %s", name, configPath)
	}
	baseAgent, err := buildAgentFromSpec(name, spec, home)
	if err != nil {
		return AgentDefinition{}, err
	}
	return buildAgentDefinition(baseAgent, spec, home, configPath)
}

// LoadAgentDefinitions mirrors load_agent_definitions: strictly load every
// agent in agents/*.toml, raising on the first invalid definition.
func LoadAgentDefinitions(aikitoDir, home string) (map[string]AgentDefinition, error) {
	document, err := LoadAgentDocument(aikitoDir)
	if err != nil {
		return nil, err
	}
	reg, err := AgentRegistryFromDocument(document, home)
	if err != nil {
		return nil, err
	}
	out := map[string]AgentDefinition{}
	for name, spec := range document {
		configPath := filepath.Join(aikitoDir, "agents", name+".toml")
		def, err := buildAgentDefinition(reg.MustGet(name), spec, home, configPath)
		if err != nil {
			return nil, err
		}
		out[name] = def
	}
	return out, nil
}
