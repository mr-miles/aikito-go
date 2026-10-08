//go:build e2e

package e2e

import (
	"os"
	"strings"
	"testing"
)

// TestE2ESyncGlobal compares `sync global` structurally against Python:
// selected skills are linked into the shared ~/.agents/skills hub, and an
// agent whose skills_path differs (Claude Code's ~/.claude/skills) gets a
// single symlink to that hub, not a directory of per-skill links. The
// command output must match too.
func TestE2ESyncGlobal(t *testing.T) {
	home := initWorkspace(t)

	if r := runGo(t, home, "add", "skill", "demo", "--description", "Demo skill"); r.ExitCode != 0 {
		t.Fatalf("go add skill: %s", r.Stderr)
	}

	res := runGo(t, home, "sync", "global")
	if res.ExitCode != 0 {
		t.Fatalf("go sync global failed (exit %d): %s\n%s", res.ExitCode, res.Stdout, res.Stderr)
	}

	compareAgainstGolden(t, "sync global (CLAUDE.md)", home+"/.claude/CLAUDE.md", home, "sync_global_claude_md")
	compareAgainstGolden(t, "sync global (.claude/skills)", home+"/.claude/skills", home, "sync_global_claude_skills")
	compareAgainstGolden(t, "sync global (.agents/skills)", home+"/.agents/skills", home, "sync_global_agents_skills")
	compareManifests(t, "sync global (output)", outputGolden(res, home), loadGolden(t, "sync_global_output"))
}

// TestE2ESyncGlobalPrepopulated: a real ~/.claude/skills directory is
// never replaced; Python reports a conflict and aborts before writing.
func TestE2ESyncGlobalPrepopulated(t *testing.T) {
	home := initWorkspace(t)
	writePrepopulatedClaudeSkills(t, home)

	res := runGo(t, home, "sync", "global")
	compareManifests(t, "sync global prepopulated (output)", outputGolden(res, home), loadGolden(t, "sync_global_prepopulated_output"))
	compareAgainstGolden(t, "sync global prepopulated (.claude)", home+"/.claude", home, "sync_global_prepopulated_claude")
}

func TestE2ESyncMCPLifecycle(t *testing.T) {
	home := initWorkspace(t)

	args := []string{"add", "mcp", "weather", "--transport", "remote", "--url", "https://weather.example.com/mcp", "--agents", "claude-code"}
	if r := runGo(t, home, args...); r.ExitCode != 0 {
		t.Fatalf("go add mcp: %s", r.Stderr)
	}

	// 1. Create.
	res := runGo(t, home, "sync", "mcp")
	if res.ExitCode != 0 {
		t.Fatalf("go sync mcp (create) failed: %s\n%s", res.Stdout, res.Stderr)
	}
	compareAgainstGolden(t, "sync mcp create (.claude.json)", home+"/.claude.json", home, "sync_mcp_create_claude_json")

	// 2. Re-run: should report NOOP / already-synchronized and leave the
	// file unchanged. This is a self-referential property of the Go tool
	// (before == after), not something that needs a Python-derived golden.
	before, _ := os.ReadFile(home + "/.claude.json")
	res = runGo(t, home, "sync", "mcp")
	if res.ExitCode != 0 {
		t.Fatalf("re-run sync mcp should succeed: exit %d", res.ExitCode)
	}
	after, _ := os.ReadFile(home + "/.claude.json")
	if string(before) != string(after) {
		t.Errorf("go: re-run of sync mcp changed .claude.json unexpectedly")
	}
	if !strings.Contains(strings.ToLower(res.Stdout), "already synchronized") &&
		!strings.Contains(strings.ToLower(res.Stdout), "noop") {
		t.Errorf("go re-run output doesn't look like a no-op: %s", res.Stdout)
	}

	// 3. Drift: hand-edit the runtime file, confirm the tool refuses to
	// sync without --force (CONFLICT, nonzero exit — again a behavioral
	// property, not something requiring a golden), then confirm --force
	// restores the exact canonical content Python itself would write.
	drifted := `{"mcpServers": {"weather": {"type": "http", "url": "https://hacked.example.com/mcp"}}}`
	if err := os.WriteFile(home+"/.claude.json", []byte(drifted), 0o644); err != nil {
		t.Fatal(err)
	}

	res = runGo(t, home, "sync", "mcp")
	if res.ExitCode == 0 {
		t.Errorf("go: expected a non-zero exit on unresolved conflict, got 0 (stdout=%s)", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "CONFLICT") {
		t.Errorf("go: expected a CONFLICT line, got: %s", res.Stdout)
	}

	res = runGo(t, home, "sync", "mcp", "--force")
	if res.ExitCode != 0 {
		t.Fatalf("go --force should resolve the conflict, got exit %d: %s", res.ExitCode, res.Stdout)
	}
	compareAgainstGolden(t, "sync mcp --force (.claude.json)", home+"/.claude.json", home, "sync_mcp_force_claude_json")
}

func TestE2ESyncSubagentsPerFile(t *testing.T) {
	home := initWorkspace(t)

	args := []string{"add", "subagent", "reviewer", "--description", "Reviews code", "--agents", "claude-code"}
	if r := runGo(t, home, args...); r.ExitCode != 0 {
		t.Fatalf("go add subagent: %s", r.Stderr)
	}

	res := runGo(t, home, "sync", "subagents")
	if res.ExitCode != 0 {
		t.Fatalf("go sync subagents failed: %s\n%s", res.Stdout, res.Stderr)
	}

	compareAgainstGolden(t, "sync subagents (.claude/agents)", home+"/.claude/agents", home, "sync_subagents_per_file")
}
