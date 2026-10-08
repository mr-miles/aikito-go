package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mr-miles/aikito-rs/internal/registry"
)

// writeFile writes content to path, creating parent directories.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setupMCPWorkspace(t *testing.T) Environment {
	t.Helper()
	env := testEnv(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"init", "workspace"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("init workspace failed: %s", errOut.String())
	}
	aikitoDir, err := env.AikitoDir()
	if err != nil {
		t.Fatal(err)
	}
	// The restricted test PATH means init workspace detects no agent CLIs,
	// so it writes no agents/*.toml templates; add claude-code's bundled
	// definition explicitly so the MCP loader can resolve the "claude-code"
	// agent referenced below.
	claudeCodeTemplate, err := registry.BundledAgentTemplateText("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(aikitoDir, "agents", "claude-code.toml"), claudeCodeTemplate)
	// claude-code's detect table requires EITHER the "claude" binary on PATH
	// OR a ".claude" marker directory under home (agents.py's
	// check_agent_availability never falls back to "config file's parent
	// directory exists" once a detect table is declared — see
	// mcp/loader.py's _agent_detected, which only uses that fallback when
	// is_agent_installed returns None, and claude-code's detect table means
	// it never does). Create the marker directory so detection is
	// deterministic regardless of PATH restrictions above.
	if err := os.MkdirAll(filepath.Join(env.Home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(aikitoDir, "mcps", "weather.toml"), "transport = \"remote\"\nurl = \"https://weather.example.com/mcp\"\nagents = [\"claude-code\"]\n")
	return env
}

// Cross-validated against the real Python `aikito sync mcp` (dry-run,
// apply, re-run NOOP, conflict, and --force) via subprocess+file diff
// during manual testing; this test locks in the same scenarios in-process.
func TestCmdSyncMCPDryRunThenApply(t *testing.T) {
	env := setupMCPWorkspace(t)

	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "mcp", "--dry-run"}, nil, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("dry-run exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "[DRY-RUN] claude-code/weather: would create entry") {
		t.Errorf("dry-run output missing expected line: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(env.Home, ".claude.json")); err == nil {
		t.Fatal("dry-run must not write the target file")
	}

	out.Reset()
	errOut.Reset()
	code = Run([]string{"sync", "mcp"}, nil, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("apply exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "[SYNC] claude-code/weather: created") {
		t.Errorf("apply output missing created line: %s", out.String())
	}
	data, err := os.ReadFile(filepath.Join(env.Home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "weather.example.com") {
		t.Errorf("claude.json missing expected server: %s", data)
	}

	// Re-run: NOOP.
	out.Reset()
	errOut.Reset()
	code = Run([]string{"sync", "mcp"}, nil, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("re-run exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "[OK] claude-code/weather: already synchronized") {
		t.Errorf("re-run output missing NOOP line: %s", out.String())
	}
}

func TestCmdSyncMCPConflictRequiresForce(t *testing.T) {
	env := setupMCPWorkspace(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"sync", "mcp"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("initial apply failed: %s", errOut.String())
	}

	// Hand-edit the runtime file to simulate drift.
	claudeJSON := filepath.Join(env.Home, ".claude.json")
	writeFile(t, claudeJSON, `{"mcpServers": {"weather": {"type": "http", "url": "https://hacked.example.com/mcp"}}}`)

	out.Reset()
	errOut.Reset()
	code := Run([]string{"sync", "mcp"}, nil, &out, &errOut, env)
	if code != 1 {
		t.Fatalf("conflict exit = %d, want 1; stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "[CONFLICT] claude-code/weather") {
		t.Errorf("expected CONFLICT line, got: %s", out.String())
	}

	out.Reset()
	errOut.Reset()
	code = Run([]string{"sync", "mcp", "--force"}, nil, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("forced apply exit = %d, stderr = %s", code, errOut.String())
	}
	data, _ := os.ReadFile(claudeJSON)
	if strings.Contains(string(data), "hacked.example.com") {
		t.Errorf("--force did not overwrite drifted entry: %s", data)
	}
	if !strings.Contains(string(data), "weather.example.com") {
		t.Errorf("--force did not restore canonical URL: %s", data)
	}
}

func TestCmdSyncUnknownTarget(t *testing.T) {
	env := testEnv(t)
	var out, errOut bytes.Buffer
	code := Run([]string{"sync", "bogus"}, nil, &out, &errOut, env)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

// sync global became real functionality (internal/sync/globalskills.go,
// globalinstructions.go, link.go) after this test was first written as a
// stub-detection check; it now locks in the actual end-to-end behavior
// instead; the unit-level CREATE/NOOP/CONFLICT/--force matrix for the
// underlying plan builders already lives in internal/sync's own tests, so
// this just exercises the CLI wiring on a realistic fixture.
func TestCmdSyncGlobal(t *testing.T) {
	env := testEnv(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"init", "workspace"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("init workspace failed: %s", errOut.String())
	}
	aikitoDir, err := env.AikitoDir()
	if err != nil {
		t.Fatal(err)
	}
	claudeCodeTemplate, err := registry.BundledAgentTemplateText("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(aikitoDir, "agents", "claude-code.toml"), claudeCodeTemplate)
	if err := os.MkdirAll(filepath.Join(env.Home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	errOut.Reset()
	if code := Run([]string{"add", "skill", "my-skill", "--description", "Test skill"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("add skill failed: %s", errOut.String())
	}

	out.Reset()
	errOut.Reset()
	code := Run([]string{"sync", "global", "--dry-run"}, nil, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("dry-run exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "[CREATE]") {
		t.Errorf("dry-run output missing a CREATE line: %s", out.String())
	}
	skillLink := filepath.Join(env.Home, ".claude", "skills", "my-skill")
	if _, err := os.Lstat(skillLink); err == nil {
		t.Fatal("dry-run must not create the symlink")
	}

	out.Reset()
	errOut.Reset()
	code = Run([]string{"sync", "global"}, nil, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("apply exit = %d, stderr = %s", code, errOut.String())
	}
	target, err := os.Readlink(skillLink)
	if err != nil {
		t.Fatalf("expected a symlink at %s: %v", skillLink, err)
	}
	if !strings.HasSuffix(target, filepath.Join("skills", "my-skill")) {
		t.Errorf("symlink target = %q, want it to end with skills/my-skill", target)
	}
	instrLink := filepath.Join(env.Home, ".claude", "CLAUDE.md")
	if _, err := os.Lstat(instrLink); err != nil {
		t.Fatalf("expected global instructions symlink at %s: %v", instrLink, err)
	}

	// Re-run: NOOP (no conflicts, nothing left to create).
	out.Reset()
	errOut.Reset()
	code = Run([]string{"sync", "global"}, nil, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("re-run exit = %d, stderr = %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "[CREATE]") {
		t.Errorf("re-run should be a no-op, got: %s", out.String())
	}
}

func TestCmdSyncGlobalPruneNotYetImplementedNotice(t *testing.T) {
	env := testEnv(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"init", "workspace"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("init workspace failed: %s", errOut.String())
	}
	out.Reset()
	errOut.Reset()
	code := Run([]string{"sync", "global", "--prune", "--dry-run"}, nil, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "--prune is not yet implemented") {
		t.Errorf("expected a --prune notice, got: %s", out.String())
	}
}
