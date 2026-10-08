package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

// Cross-validated against the real Python build_mcp_plan on an identical
// fixture tree (see this session's scratch invocation): a single codex
// agent with no live binary but a ~/.codex marker directory (so detection
// resolves to "installed" via marker_directory), and one mcps/my-server.toml
// remote server targeting it. Python's plan produced exactly one CREATE
// operation and a final config.toml body of
// `[mcp_servers.my_server]\nurl = "https://example.com/mcp"\n`.
func TestBuildMCPPlanCrossValidatedCreate(t *testing.T) {
	home := t.TempDir()
	aikitoDir := filepath.Join(home, "aikito")
	for _, dir := range []string{"agents", "subagents", "mcps"} {
		if err := os.MkdirAll(filepath.Join(aikitoDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(aikitoDir, "layout.toml"), []byte("version = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(aikitoDir, "agents", "codex.toml"), []byte(`[agents.codex]
display_name = "Codex"
instruction_path = ".codex/AGENTS.md"
project_instruction_path = "AGENTS.md"
skills_path = ".agents/skills"

[agents.codex.detect]
commands = ["codex-nonexistent-binary-xyz"]
paths = [".codex"]

[agents.codex.mcp]
config_path = ".codex/config.toml"
config_format = "toml"
name_style = "underscore"
live_command = ["codex", "mcp", "list"]
auth_command = ["codex", "mcp", "login", "{target}"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(aikitoDir, "mcps", "my-server.toml"), []byte(`transport = "remote"
url = "https://example.com/mcp"
agents = ["codex"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}

	plan, err := BuildMCPPlan(aikitoDir, home, BuildMCPPlanOptions{})
	if err != nil {
		t.Fatalf("BuildMCPPlan error: %v", err)
	}
	if len(plan.Operations) != 1 {
		t.Fatalf("expected 1 operation, got %d: %+v", len(plan.Operations), plan.Operations)
	}
	op := plan.Operations[0]
	if op.Target.Agent != "codex" || op.Target.LogicalIdentity != "my-server" {
		t.Errorf("unexpected target: %+v", op.Target)
	}
	if op.Action != "CREATE" {
		t.Errorf("Action = %q, want CREATE", op.Action)
	}
	if op.Reason != "New server entry" {
		t.Errorf("Reason = %q, want %q", op.Reason, "New server entry")
	}
	if !op.IsAuthorized {
		t.Errorf("expected IsAuthorized=true")
	}
	if op.RequiresForce {
		t.Errorf("expected RequiresForce=false")
	}
	if len(plan.FilePlans) != 1 {
		t.Fatalf("expected 1 file plan, got %d", len(plan.FilePlans))
	}
	fp := plan.FilePlans[0]
	if !fp.WillMutate() {
		t.Errorf("expected WillMutate()=true")
	}
	wantFinal := "[mcp_servers.my_server]\nurl = \"https://example.com/mcp\"\n"
	if fp.FinalContent == nil || *fp.FinalContent != wantFinal {
		got := "<nil>"
		if fp.FinalContent != nil {
			got = *fp.FinalContent
		}
		t.Errorf("FinalContent = %q, want %q", got, wantFinal)
	}
}
