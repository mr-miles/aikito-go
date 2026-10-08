package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFixtureWorkspace(t *testing.T, home string) string {
	t.Helper()
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
	return aikitoDir
}

// Cross-validated against a live Python execute_mcp_plan run on an
// identical fixture: first apply creates config.toml with
// "[mcp_servers.my_server]\nurl = ...\n"; a second plan build is a NOOP
// ("Already synchronized"); a third plan with the server marked absent is a
// REMOVE that leaves config.toml empty and creates exactly one backup.
func TestExecuteMCPPlanCrossValidatedLifecycle(t *testing.T) {
	home := t.TempDir()
	aikitoDir := writeFixtureWorkspace(t, home)
	configPath := filepath.Join(home, ".codex", "config.toml")

	plan, err := BuildMCPPlan(aikitoDir, home, BuildMCPPlanOptions{})
	if err != nil {
		t.Fatalf("build plan 1: %v", err)
	}
	result, err := ExecuteMCPPlan(plan, home, func(string) {})
	if err != nil {
		t.Fatalf("execute plan 1: %v", err)
	}
	if !result.Success || result.AppliedCount != 1 {
		t.Fatalf("plan 1 result = %+v", result)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	wantCreated := "[mcp_servers.my_server]\nurl = \"https://example.com/mcp\"\n"
	if string(data) != wantCreated {
		t.Errorf("config.toml after create = %q, want %q", string(data), wantCreated)
	}

	plan2, err := BuildMCPPlan(aikitoDir, home, BuildMCPPlanOptions{})
	if err != nil {
		t.Fatalf("build plan 2: %v", err)
	}
	if len(plan2.Operations) != 1 || plan2.Operations[0].Action != "NOOP" {
		t.Fatalf("plan 2 operations = %+v", plan2.Operations)
	}
	if plan2.Operations[0].Reason != "Already synchronized" {
		t.Errorf("plan 2 reason = %q", plan2.Operations[0].Reason)
	}

	plan3, err := BuildMCPPlan(aikitoDir, home, BuildMCPPlanOptions{DesiredAbsentServers: map[string]bool{"my-server": true}})
	if err != nil {
		t.Fatalf("build plan 3: %v", err)
	}
	if len(plan3.Operations) != 1 || plan3.Operations[0].Action != "REMOVE" {
		t.Fatalf("plan 3 operations = %+v", plan3.Operations)
	}
	if plan3.Operations[0].RequiresForce {
		t.Errorf("plan 3 should not require force (managed fingerprint matches)")
	}
	result3, err := ExecuteMCPPlan(plan3, home, func(string) {})
	if err != nil {
		t.Fatalf("execute plan 3: %v", err)
	}
	if !result3.Success || result3.AppliedCount != 1 {
		t.Fatalf("plan 3 result = %+v", result3)
	}
	if len(result3.BackupsCreated) != 1 {
		t.Fatalf("expected exactly 1 backup, got %d: %v", len(result3.BackupsCreated), result3.BackupsCreated)
	}
	data3, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data3) != "" {
		t.Errorf("config.toml after remove = %q, want empty", string(data3))
	}
}

// Rollback-path coverage: inject a write failure on the Nth of M mutating
// files by making the target file's parent directory read-only after the
// first file succeeds, and confirm the first file is rolled back to its
// pre-image byte-for-byte.
func TestExecuteMCPPlanRollsBackOnPartialWriteFailure(t *testing.T) {
	home := t.TempDir()
	aikitoDir := filepath.Join(home, "aikito")
	for _, dir := range []string{"agents", "subagents", "mcps"} {
		if err := os.MkdirAll(filepath.Join(aikitoDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(aikitoDir, "layout.toml"), []byte("version = 2\n"), 0o644)
	os.WriteFile(filepath.Join(aikitoDir, "agents", "codex.toml"), []byte(`[agents.codex]
display_name = "Codex"
[agents.codex.mcp]
config_path = ".codex/config.toml"
config_format = "toml"
name_style = "underscore"
`), 0o644)
	os.WriteFile(filepath.Join(aikitoDir, "agents", "claude-code.toml"), []byte(`[agents.claude-code]
display_name = "Claude Code"
[agents.claude-code.mcp]
config_path = ".claude.json"
config_format = "claude_json"
`), 0o644)
	os.WriteFile(filepath.Join(aikitoDir, "mcps", "server-a.toml"), []byte(`transport = "remote"
url = "https://a.example.com/mcp"
agents = ["codex"]
`), 0o644)
	os.WriteFile(filepath.Join(aikitoDir, "mcps", "server-b.toml"), []byte(`transport = "remote"
url = "https://b.example.com/mcp"
agents = ["claude-code"]
`), 0o644)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755) // claude-code's detect.paths marker

	plan, err := BuildMCPPlan(aikitoDir, home, BuildMCPPlanOptions{})
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	if len(plan.FilePlans) != 2 {
		t.Fatalf("expected 2 file plans, got %d", len(plan.FilePlans))
	}

	// Make the second file's directory unwritable so os.Rename into it fails,
	// forcing the executor to roll back the first (already-written) file.
	// The first file plan in map iteration order isn't guaranteed, so make
	// BOTH target dirs' parents read-only isn't viable (both would fail);
	// instead, pre-create the claude.json path as a directory so writing a
	// file there fails deterministically regardless of order.
	for _, fp := range plan.FilePlans {
		if filepath.Base(fp.Path) == ".claude.json" {
			if err := os.MkdirAll(fp.Path, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}

	result, err := ExecuteMCPPlan(plan, home, func(string) {})
	if err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure, got success: %+v", result)
	}
	// The codex config.toml file must not exist (rolled back / never
	// committed) since it had no pre-image (PreImage.Exists == false).
	if _, err := os.Stat(filepath.Join(home, ".codex", "config.toml")); err == nil {
		t.Errorf("expected .codex/config.toml to be rolled back (removed), but it exists")
	}
}
