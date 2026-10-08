package sync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mr-miles/aikito-go/internal/registry"
)

func writeTestAgentTOML(t *testing.T, aikitoDir, name, body string) {
	t.Helper()
	path := filepath.Join(aikitoDir, "agents", name+".toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setupFixtureWorkspace(t *testing.T) (aikitoDir, home string) {
	t.Helper()
	home = t.TempDir()
	aikitoDir = filepath.Join(home, "aikito")
	// Minimal v2 layout: workspace.RequireCurrentLayout (used by
	// registry.LoadAgentDocument) requires the marker file plus agents/ and
	// subagents/ directories to exist.
	if err := os.MkdirAll(aikitoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(aikitoDir, "layout.toml"), []byte("version = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(aikitoDir, "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(aikitoDir, "skills", "my-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(aikitoDir, "skills", "my-skill", "SKILL.md"), []byte("# My Skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(aikitoDir, "global"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(aikitoDir, "global", "AGENTS.md"), []byte("# Instructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(aikitoDir, "skills.toml"), []byte(`skills = ["my-skill"]`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A minimal agent with distinct skills_path/instruction_path, resolved
	// against home via registry.buildAgentFromSpec's home-join behavior.
	writeTestAgentTOML(t, aikitoDir, "codex", `
[agents.codex]
display_name = "Codex"
instruction_path = ".codex/AGENTS.md"
skills_path = ".agents/skills"
`)
	return aikitoDir, home
}

func TestBuildGlobalSkillsPlanCreatesAndNoops(t *testing.T) {
	aikitoDir, home := setupFixtureWorkspace(t)
	reg, err := registry.LoadStrict(aikitoDir, home)
	if err != nil {
		t.Fatal(err)
	}

	items, err := BuildGlobalSkillsPlan(aikitoDir, home, reg, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 planned skill item, got %d: %+v", len(items), items)
	}
	if items[0].Op.Action != LinkCreate {
		t.Fatalf("expected CREATE, got %+v", items[0].Op)
	}

	if err := ApplySymlink(items[0].Op); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(home, ".agents", "skills", "my-skill")
	data, err := os.ReadFile(filepath.Join(linked, "SKILL.md"))
	if err != nil {
		t.Fatalf("symlinked skill content unreadable: %v", err)
	}
	if string(data) != "# My Skill\n" {
		t.Errorf("content = %q", data)
	}

	// Re-plan: should now be NOOP.
	items2, err := BuildGlobalSkillsPlan(aikitoDir, home, reg, false)
	if err != nil {
		t.Fatal(err)
	}
	if items2[0].Op.Action != LinkNoop {
		t.Fatalf("expected NOOP on second plan, got %+v", items2[0].Op)
	}
}

func TestBuildGlobalInstructionsPlanCreatesAndNoops(t *testing.T) {
	aikitoDir, home := setupFixtureWorkspace(t)
	reg, err := registry.LoadStrict(aikitoDir, home)
	if err != nil {
		t.Fatal(err)
	}

	items, err := BuildGlobalInstructionsPlan(aikitoDir, home, reg, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Op.Action != LinkCreate {
		t.Fatalf("expected 1 CREATE item, got %+v", items)
	}
	if err := ApplySymlink(items[0].Op); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".codex", "AGENTS.md"))
	if err != nil {
		t.Fatalf("symlinked instructions content unreadable: %v", err)
	}
	if string(data) != "# Instructions\n" {
		t.Errorf("content = %q", data)
	}

	items2, err := BuildGlobalInstructionsPlan(aikitoDir, home, reg, false)
	if err != nil {
		t.Fatal(err)
	}
	if items2[0].Op.Action != LinkNoop {
		t.Fatalf("expected NOOP on second plan, got %+v", items2[0].Op)
	}
}

func TestLoadSelectedGlobalSkills(t *testing.T) {
	aikitoDir, _ := setupFixtureWorkspace(t)
	skills := LoadSelectedGlobalSkills(aikitoDir)
	if len(skills) != 1 || skills[0] != "my-skill" {
		t.Errorf("skills = %v, want [my-skill]", skills)
	}

	// Missing file -> empty, not an error.
	empty := LoadSelectedGlobalSkills(t.TempDir())
	if len(empty) != 0 {
		t.Errorf("expected empty for missing skills.toml, got %v", empty)
	}
}
