package workspace

import (
	"testing"
)

// Cross-validated against the real Python parse_subagent_text /
// render_subagent_text (aikito.workspace.layout), not hand-derived.
func TestParseSubagentTextValidCases(t *testing.T) {
	cases := []struct {
		name            string
		content         string
		wantDescription string
		wantAgents      []string
		wantBody        string
	}{
		{
			"valid_basic",
			"---\ndescription: \"A test subagent\"\nagents: [\"claude-code\", \"codex\"]\n---\nDo the thing.\n",
			"A test subagent", []string{"claude-code", "codex"}, "Do the thing.\n",
		},
		{
			"valid_with_platform_override",
			"---\ndescription: \"A test subagent\"\nagents: [\"claude-code\"]\nclaude-code: {\"model\": \"opus\", \"color\": \"blue\"}\n---\nBody text here.\n",
			"A test subagent", []string{"claude-code"}, "Body text here.\n",
		},
		{
			"valid_with_comment",
			"---\n# a comment\ndescription: \"desc\"\nagents: [\"codex\"]\n---\nBody.\n",
			"desc", []string{"codex"}, "Body.\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata, body, err := ParseSubagentText(tc.content)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
			if metadata["description"] != tc.wantDescription {
				t.Errorf("description = %v, want %v", metadata["description"], tc.wantDescription)
			}
			agents, ok := metadata["agents"].([]any)
			if !ok || len(agents) != len(tc.wantAgents) {
				t.Fatalf("agents = %v, want %v", metadata["agents"], tc.wantAgents)
			}
			for i, a := range tc.wantAgents {
				if agents[i] != a {
					t.Errorf("agents[%d] = %v, want %v", i, agents[i], a)
				}
			}
		})
	}
}

func TestParseSubagentTextErrorCases(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"missing_frontmatter", "description: x\n---\nBody\n"},
		{"incomplete_frontmatter", "---\ndescription: x\n"},
		{"missing_description", "---\nagents: [\"codex\"]\n---\nBody\n"},
		{"empty_description", "---\ndescription: \"   \"\nagents: [\"codex\"]\n---\nBody\n"},
		{"missing_agents", "---\ndescription: \"x\"\n---\nBody\n"},
		{"duplicate_agents", "---\ndescription: \"x\"\nagents: [\"codex\", \"codex\"]\n---\nBody\n"},
		{"invalid_agent_name", "---\ndescription: \"x\"\nagents: [\"Codex!\"]\n---\nBody\n"},
		{"duplicate_key", "---\ndescription: \"x\"\ndescription: \"y\"\nagents: [\"codex\"]\n---\nBody\n"},
		{"bad_platform_value_not_dict", "---\ndescription: \"x\"\nagents: [\"codex\"]\ncodex: \"not a dict\"\n---\nBody\n"},
		{"empty_body", "---\ndescription: \"x\"\nagents: [\"codex\"]\n---\n   \n"},
		{"invalid_json_value", "---\ndescription: x\nagents: [\"codex\"]\n---\nBody\n"},
		{"duplicate_json_object_key", "---\ndescription: \"x\"\nagents: [\"codex\"]\ncodex: {\"model\": \"a\", \"model\": \"b\"}\n---\nBody\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ParseSubagentText(tc.content)
			if err == nil {
				t.Fatalf("expected an error, got none")
			}
		})
	}
}

func TestRenderSubagentText(t *testing.T) {
	metadata := map[string]any{
		"description": "A test",
		"agents":      []any{"claude-code", "codex"},
		"claude-code": map[string]any{"model": "opus"},
	}
	got := RenderSubagentText(metadata, "Body text.\n", "")
	want := "---\ndescription: \"A test\"\nagents: [\"claude-code\", \"codex\"]\nclaude-code: {\"model\": \"opus\"}\n---\nBody text.\n"
	if got != want {
		t.Errorf("RenderSubagentText() = %q, want %q", got, want)
	}
}

func TestValidateResourceName(t *testing.T) {
	cases := []struct {
		name  string
		valid bool
	}{
		{"codex", true},
		{"my-agent-1", true},
		{"a", true},
		{"", false},
		{"-leading", false},
		{"trailing-", false},
		{"Has-Upper", false},
		{"has/slash", false},
		{"has..dots", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateResourceName(tc.name, "agent")
			if (err == "") != tc.valid {
				t.Errorf("ValidateResourceName(%q) error=%q, want valid=%v", tc.name, err, tc.valid)
			}
		})
	}
}
