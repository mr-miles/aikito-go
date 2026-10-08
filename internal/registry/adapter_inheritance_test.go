package registry

import "testing"

// Cross-validated against real Python _build_agent_definition for the three
// trickiest mcp.adapter inheritance edge cases.
func TestMCPAdapterInheritance(t *testing.T) {
	const home = "/home/testuser"

	t.Run("mismatched_format_no_inherit", func(t *testing.T) {
		// codex's bundled config_format is "toml"; overriding to "jsonc" with
		// no adapter must NOT inherit the bundled "toml" adapter — it should
		// fall back to the new config_format itself.
		doc := map[string]map[string]any{
			"codex": {
				"mcp": map[string]any{
					"config_path":   ".codex/config.toml",
					"config_format": "jsonc",
				},
			},
		}
		reg, err := AgentRegistryFromDocument(doc, home)
		if err != nil {
			t.Fatal(err)
		}
		def, err := buildAgentDefinition(reg.MustGet("codex"), doc["codex"], home, "<test>")
		if err != nil {
			t.Fatal(err)
		}
		if def.MCP.Adapter != "jsonc" || def.MCP.ConfigFormat != "jsonc" {
			t.Errorf("got adapter=%q format=%q, want adapter=jsonc format=jsonc", def.MCP.Adapter, def.MCP.ConfigFormat)
		}
	})

	t.Run("explicit_adapter_wins", func(t *testing.T) {
		doc := map[string]map[string]any{
			"codex": {
				"mcp": map[string]any{
					"config_path":   ".codex/config.toml",
					"config_format": "toml",
					"adapter":       "custom_adapter",
				},
			},
		}
		reg, err := AgentRegistryFromDocument(doc, home)
		if err != nil {
			t.Fatal(err)
		}
		def, err := buildAgentDefinition(reg.MustGet("codex"), doc["codex"], home, "<test>")
		if err != nil {
			t.Fatal(err)
		}
		if def.MCP.Adapter != "custom_adapter" || def.MCP.ConfigFormat != "toml" {
			t.Errorf("got adapter=%q format=%q, want adapter=custom_adapter format=toml", def.MCP.Adapter, def.MCP.ConfigFormat)
		}
	})

	t.Run("custom_agent_no_bundled_spec", func(t *testing.T) {
		// A non-builtin agent name has no bundled spec at all (empty map),
		// so there's nothing to inherit from regardless of format match —
		// adapter must fall back to config_format.
		doc := map[string]map[string]any{
			"myagent": {
				"mcp": map[string]any{
					"config_path":   ".myagent/config.toml",
					"config_format": "toml",
				},
			},
		}
		reg, err := AgentRegistryFromDocument(doc, home)
		if err != nil {
			t.Fatal(err)
		}
		def, err := buildAgentDefinition(reg.MustGet("myagent"), doc["myagent"], home, "<test>")
		if err != nil {
			t.Fatal(err)
		}
		if def.MCP.Adapter != "toml" || def.MCP.ConfigFormat != "toml" {
			t.Errorf("got adapter=%q format=%q, want adapter=toml format=toml", def.MCP.Adapter, def.MCP.ConfigFormat)
		}
	})
}
