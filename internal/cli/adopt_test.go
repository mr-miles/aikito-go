package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupAdoptWorkspace mirrors sync_test.go's setupMCPWorkspace: creates a
// ".claude" marker directory before init so claude-code is detected
// deterministically (this test's restricted PATH has no real agent CLIs),
// then runs `init workspace`.
func setupAdoptWorkspace(t *testing.T) Environment {
	t.Helper()
	env := testEnv(t)
	if err := os.MkdirAll(filepath.Join(env.Home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := Run([]string{"init", "workspace"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("init workspace failed: %s", errOut.String())
	}
	return env
}

const adoptNothingLine = "[OK] No adoptable Agent configuration found. No files were modified."

// Adopting over the pristine post-init template merges: the user's
// instructions are kept and the template's durable-memory section is
// appended (adopt.py _merge_adopted_instructions). Replacing instead lost
// the memory instruction.
func TestAdoptInstructionsMergesMemorySection(t *testing.T) {
	env := setupAdoptWorkspace(t)
	aikitoDir, err := env.AikitoDir()
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := Run([]string{"adopt"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("adopt exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), adoptNothingLine) {
		t.Errorf("a clean post-init workspace has nothing to adopt, got: %s", out.String())
	}

	writeFile(t, filepath.Join(env.Home, ".claude", "CLAUDE.md"), "# My custom instructions\n")
	out.Reset()
	if code := Run([]string{"adopt"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("adopt exit = %d, stderr = %s", code, errOut.String())
	}
	data, err := os.ReadFile(filepath.Join(aikitoDir, "global", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := loadTemplate("global/AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	if want := "# My custom instructions\n\n" + strings.TrimRight(tmpl, "\n"); string(data) != want {
		t.Errorf("global/AGENTS.md = %q, want %q", data, want)
	}

	out.Reset()
	if code := Run([]string{"adopt"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("re-run exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), adoptNothingLine) {
		t.Errorf("re-run should have nothing to adopt, got: %s", out.String())
	}
}

// Regression test for a real bug found during this fork's own hand-testing:
// MCP adoption kept re-proposing to (re)create an mcps/<name>.toml that had
// already been adopted, because the "already canonically represented"
// check was missing from the adoption loop.
func TestAdoptMCPIsIdempotent(t *testing.T) {
	env := setupAdoptWorkspace(t)
	aikitoDir, err := env.AikitoDir()
	if err != nil {
		t.Fatal(err)
	}

	claudeJSON := filepath.Join(env.Home, ".claude.json")
	if err := os.WriteFile(claudeJSON, []byte(`{"mcpServers": {"weather": {"type": "http", "url": "https://weather.example.com/mcp"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := Run([]string{"adopt"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("first adopt exit = %d, stderr = %s", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(aikitoDir, "mcps", "weather.toml")); err != nil {
		t.Fatalf("expected mcps/weather.toml to be adopted: %v", err)
	}

	out.Reset()
	errOut.Reset()
	if code := Run([]string{"adopt"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("second adopt exit = %d, stderr = %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "mcps/weather.toml") {
		t.Errorf("expected re-run to NOT re-propose an already-adopted MCP server, got: %s", out.String())
	}
}

// Regression coverage for the same already-adopted idempotency property,
// for subagent adoption.
func TestAdoptSubagentIsIdempotent(t *testing.T) {
	env := setupAdoptWorkspace(t)
	aikitoDir, err := env.AikitoDir()
	if err != nil {
		t.Fatal(err)
	}

	agentsDir := filepath.Join(env.Home, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	nativeFile := filepath.Join(agentsDir, "reviewer.md")
	if err := os.WriteFile(nativeFile, []byte("---\ndescription: Reviews code\n---\nYou review code.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := Run([]string{"adopt"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("first adopt exit = %d, stderr = %s", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(aikitoDir, "subagents", "reviewer.md")); err != nil {
		t.Fatalf("expected subagents/reviewer.md to be adopted: %v", err)
	}

	out.Reset()
	errOut.Reset()
	if code := Run([]string{"adopt"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("second adopt exit = %d, stderr = %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "subagents/reviewer.md") {
		t.Errorf("expected re-run to NOT re-propose an already-adopted subagent, got: %s", out.String())
	}
}

func TestAdoptDryRunWritesNothing(t *testing.T) {
	env := setupAdoptWorkspace(t)
	aikitoDir, err := env.AikitoDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.Home, ".claude.json"), []byte(`{"mcpServers": {"weather": {"type": "http", "url": "https://weather.example.com/mcp"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := Run([]string{"adopt", "--dry-run"}, nil, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(aikitoDir, "mcps", "weather.toml")); err == nil {
		t.Error("--dry-run must not write mcps/weather.toml")
	}
}

func TestCheckAdoptionDoctorSection(t *testing.T) {
	env := setupAdoptWorkspace(t)
	aikitoDir, err := env.AikitoDir()
	if err != nil {
		t.Fatal(err)
	}

	section := checkAdoption(aikitoDir, env.Home)
	if len(section.Findings) != 1 || section.Findings[0].Status != "OK" {
		t.Errorf("expected a clean OK finding on a fresh workspace, got: %+v", section.Findings)
	}

	if err := os.WriteFile(filepath.Join(env.Home, ".claude.json"), []byte(`{"mcpServers": {"weather": {"type": "http", "url": "https://weather.example.com/mcp"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	section = checkAdoption(aikitoDir, env.Home)
	found := false
	for _, f := range section.Findings {
		if f.Status == "WARN" && strings.Contains(f.Message, "available to adopt") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a WARN finding once an MCP server is adoptable, got: %+v", section.Findings)
	}
	// checkAdoption must be read-only.
	if _, err := os.Stat(filepath.Join(aikitoDir, "mcps", "weather.toml")); err == nil {
		t.Error("checkAdoption must not write anything, but mcps/weather.toml exists")
	}
}
