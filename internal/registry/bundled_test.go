package registry

import "testing"

// Cross-validated against the real Python bundled_agent_definition for all
// 8 built-in agents with home="/home/testuser", re-rooted here at a temp dir (see the generation script
// run against /home/miles/aikito-rs/aikito during development).
func TestBundledAgentDefinitionsMatchPython(t *testing.T) {
	home := resolvedTempDir(t)

	type want struct {
		displayName            string
		instructionPath        *string
		projectInstructionPath *string
		skillsPath             *string
		detectCommands         []string
		detectPaths            []string
		mcpConfigPath          string
		mcpConfigFormat        string
		mcpAdapter             string
		mcpNameStyle           string
		mcpBuiltinServers      []string
		hasMCP                 bool
		subagentsConfigPath    string
		subagentsConfigFormat  string
		subagentsRequiresPath  *string
		runnerCommand          []string
	}
	s := func(v string) *string { return &v }

	cases := map[string]want{
		"codex": {
			displayName: "Codex", instructionPath: s(home + "/.codex/AGENTS.md"),
			projectInstructionPath: s("AGENTS.md"), skillsPath: s(home + "/.agents/skills"),
			detectCommands: []string{"codex"}, detectPaths: []string{".codex"},
			hasMCP: true, mcpConfigPath: home + "/.codex/config.toml", mcpConfigFormat: "toml",
			mcpAdapter: "toml", mcpNameStyle: "underscore", mcpBuiltinServers: []string{"openaiDeveloperDocs"},
			subagentsConfigPath: home + "/.codex/agents", subagentsConfigFormat: "codex_toml",
			runnerCommand: []string{"codex", "-C", "{workdir}", "{prompt}"},
		},
		"claude-code": {
			displayName: "Claude Code", instructionPath: s(home + "/.claude/CLAUDE.md"),
			projectInstructionPath: s(".claude/CLAUDE.md"), skillsPath: s(home + "/.claude/skills"),
			detectCommands: []string{"claude"}, detectPaths: []string{".claude"},
			hasMCP: true, mcpConfigPath: home + "/.claude.json", mcpConfigFormat: "claude_json",
			mcpAdapter: "claude_json", mcpNameStyle: "verbatim", mcpBuiltinServers: []string{},
			subagentsConfigPath: home + "/.claude/agents", subagentsConfigFormat: "claude_markdown",
			runnerCommand: []string{"claude", "{prompt}"},
		},
		"agy": {
			displayName: "Antigravity CLI", instructionPath: s(home + "/.gemini/GEMINI.md"),
			projectInstructionPath: s("AGENTS.md"), skillsPath: s(home + "/.gemini/antigravity-cli/skills"),
			detectCommands: []string{"agy"}, detectPaths: []string{".gemini/config"},
			hasMCP: true, mcpConfigPath: home + "/.gemini/config/mcp_config.json", mcpConfigFormat: "agy_json",
			mcpAdapter: "agy_json", mcpNameStyle: "verbatim", mcpBuiltinServers: []string{},
			subagentsConfigPath: home + "/.gemini/config/agents", subagentsConfigFormat: "agy_markdown",
			runnerCommand: []string{"agy", "--prompt-interactive", "{prompt}"},
		},
		"opencode": {
			displayName: "OpenCode", instructionPath: s(home + "/.config/opencode/AGENTS.md"),
			projectInstructionPath: s("AGENTS.md"), skillsPath: s(home + "/.agents/skills"),
			detectCommands: []string{"opencode"}, detectPaths: []string{".config/opencode"},
			hasMCP: true, mcpConfigPath: home + "/.config/opencode/opencode.jsonc", mcpConfigFormat: "jsonc",
			mcpAdapter: "jsonc", mcpNameStyle: "verbatim", mcpBuiltinServers: []string{},
			subagentsConfigPath: home + "/.config/opencode/agents", subagentsConfigFormat: "opencode_markdown",
			runnerCommand: []string{"opencode", "{workdir}", "--prompt", "{prompt}"},
		},
		"github-copilot": {
			displayName: "GitHub Copilot CLI", instructionPath: s(home + "/.copilot/copilot-instructions.md"),
			projectInstructionPath: s("AGENTS.md"), skillsPath: s(home + "/.agents/skills"),
			detectCommands: []string{"copilot"}, detectPaths: []string{".copilot"},
			hasMCP: true, mcpConfigPath: home + "/.copilot/mcp-config.json", mcpConfigFormat: "copilot_json",
			mcpAdapter: "copilot_json", mcpNameStyle: "verbatim", mcpBuiltinServers: []string{},
			subagentsConfigPath: home + "/.copilot/agents", subagentsConfigFormat: "copilot_markdown",
			runnerCommand: []string{"copilot", "-C", "{workdir}", "-i", "{prompt}"},
		},
		"dsh": {
			displayName: "DeepSeek Harness", instructionPath: s(home + "/.dsh/AGENTS.md"),
			projectInstructionPath: s("AGENTS.md"), skillsPath: s(home + "/.agents/skills"),
			detectCommands: []string{"dsh"}, detectPaths: []string{".dsh"},
			hasMCP: true, mcpConfigPath: home + "/.dsh/cordis.patch.yml", mcpConfigFormat: "dsh_cordis",
			mcpAdapter: "dsh_cordis", mcpNameStyle: "verbatim", mcpBuiltinServers: []string{},
			subagentsConfigPath: home + "/.dsh/cordis.patch.yml", subagentsConfigFormat: "dsh_cordis_subagent",
			runnerCommand: []string{"dsh", "--profile", "headless", "{prompt}"},
		},
		"grok": {
			displayName: "Grok Build", instructionPath: s(home + "/.grok/rules/aikito.md"),
			projectInstructionPath: s("AGENTS.md"), skillsPath: s(home + "/.agents/skills"),
			detectCommands: []string{"grok"}, detectPaths: []string{".grok"},
			hasMCP: true, mcpConfigPath: home + "/.grok/config.toml", mcpConfigFormat: "toml",
			mcpAdapter: "grok_toml", mcpNameStyle: "verbatim", mcpBuiltinServers: []string{},
			subagentsConfigPath: home + "/.grok/agents", subagentsConfigFormat: "grok_markdown",
			runnerCommand: []string{"grok", "--cwd", "{workdir}", "-p", "{prompt}"},
		},
		"pi": {
			displayName: "Pi", instructionPath: s(home + "/.pi/agent/AGENTS.md"),
			projectInstructionPath: s("AGENTS.md"), skillsPath: s(home + "/.agents/skills"),
			detectCommands: []string{"pi"}, detectPaths: []string{".pi"},
			hasMCP:                false,
			subagentsConfigPath:   home + "/.pi/agent/agents",
			subagentsConfigFormat: "pi_markdown",
			subagentsRequiresPath: s(home + "/.pi/agent/extensions/subagent/index.ts"),
			runnerCommand:         []string{"pi", "-p", "{prompt}"},
		},
	}

	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			def, err := BundledAgentDefinition(name, home)
			if err != nil {
				t.Fatalf("BundledAgentDefinition(%q) error: %v", name, err)
			}
			if def.DisplayName != w.displayName {
				t.Errorf("DisplayName = %q, want %q", def.DisplayName, w.displayName)
			}
			assertStrPtr(t, "InstructionPath", def.InstructionPath, w.instructionPath)
			assertStrPtr(t, "ProjectInstructionPath", def.ProjectInstructionPath, w.projectInstructionPath)
			assertStrPtr(t, "SkillsPath", def.SkillsPath, w.skillsPath)
			if def.Detect == nil {
				t.Fatalf("Detect = nil, want non-nil")
			}
			assertStrSlice(t, "Detect.Commands", def.Detect.Commands, w.detectCommands)
			assertStrSlice(t, "Detect.Paths", def.Detect.Paths, w.detectPaths)

			if w.hasMCP {
				if def.MCP == nil {
					t.Fatalf("MCP = nil, want non-nil")
				}
				if def.MCP.ConfigPath != w.mcpConfigPath {
					t.Errorf("MCP.ConfigPath = %q, want %q", def.MCP.ConfigPath, w.mcpConfigPath)
				}
				if def.MCP.ConfigFormat != w.mcpConfigFormat {
					t.Errorf("MCP.ConfigFormat = %q, want %q", def.MCP.ConfigFormat, w.mcpConfigFormat)
				}
				if def.MCP.Adapter != w.mcpAdapter {
					t.Errorf("MCP.Adapter = %q, want %q", def.MCP.Adapter, w.mcpAdapter)
				}
				if def.MCP.NameStyle != w.mcpNameStyle {
					t.Errorf("MCP.NameStyle = %q, want %q", def.MCP.NameStyle, w.mcpNameStyle)
				}
				assertStrSlice(t, "MCP.BuiltinServers", def.MCP.BuiltinServers, w.mcpBuiltinServers)
			} else if def.MCP != nil {
				t.Errorf("MCP = %+v, want nil", def.MCP)
			}

			if def.Subagents == nil {
				t.Fatalf("Subagents = nil, want non-nil")
			}
			if def.Subagents.ConfigPath != w.subagentsConfigPath {
				t.Errorf("Subagents.ConfigPath = %q, want %q", def.Subagents.ConfigPath, w.subagentsConfigPath)
			}
			if def.Subagents.ConfigFormat != w.subagentsConfigFormat {
				t.Errorf("Subagents.ConfigFormat = %q, want %q", def.Subagents.ConfigFormat, w.subagentsConfigFormat)
			}
			assertStrPtr(t, "Subagents.RequiresPath", def.Subagents.RequiresPath, w.subagentsRequiresPath)

			if def.Runner == nil {
				t.Fatalf("Runner = nil, want non-nil")
			}
			assertStrSlice(t, "Runner.Command", def.Runner.Command, w.runnerCommand)
		})
	}
}

func assertStrPtr(t *testing.T, field string, got, want *string) {
	t.Helper()
	if (got == nil) != (want == nil) {
		t.Errorf("%s presence mismatch: got=%v want=%v", field, got, want)
		return
	}
	if got != nil && *got != *want {
		t.Errorf("%s = %q, want %q", field, *got, *want)
	}
}

func assertStrSlice(t *testing.T, field string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s = %v, want %v", field, got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %q, want %q", field, i, got[i], want[i])
		}
	}
}

func TestBuiltinAgentsOrder(t *testing.T) {
	want := []string{"codex", "claude-code", "agy", "opencode", "github-copilot", "dsh", "grok", "pi"}
	assertStrSlice(t, "BuiltinAgents", BuiltinAgents, want)
}
