//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestE2ESyncGlobal confirms `sync global` is functionally equivalent to
// Python, but NOT structurally identical — a real, significant
// architectural divergence this comparison surfaced (not a cosmetic one):
// Python's global_skills.py routes every agent's skills through ONE shared
// canonical consumer directory (~/.agents/skills/, populated with one
// symlink per selected skill) and then makes each agent's own declared
// skills_path a symlink pointing AT that shared directory — even when (as
// with Claude Code, whose own skills_path is literally ".claude/skills",
// not ".agents/skills") the agent's path string doesn't match the shared
// convention at all. This Go port's internal/sync/globalskills.go instead
// plans one independent symlink per (skill x agent) directly inside each
// agent's own skills_path, so ".claude/skills" becomes a real directory
// containing its own per-skill symlinks rather than a single symlink to a
// shared location. End users see the same skill set either way for a
// single-agent setup, but the two approaches diverge for a HOST WITH
// MULTIPLE AGENTS sharing one physical skills_path convention (6 of 8
// built-in agents use ".agents/skills" literally) — Python writes the
// shared symlinks once and every such agent's path IS that directory; this
// port currently duplicates the same symlinks independently per agent.
// Confirmed by running the real Python CLI and inspecting its output
// directly (not assumed) when this golden fixture was captured — see
// e2e/testdata/sync_global_skills/ (a captured, functional skill-name
// list, not a raw directory layout) and the skill-resolution check below,
// which passes because it checks the FUNCTIONAL property (what skills end
// up visible once every symlink hop is followed), not the raw layout. The
// raw-layout divergence itself is a known, real gap — flag for a follow-up
// port of the shared managed-consumer-directory architecture
// (internal/registry/targets_todo.go already tracks the underlying
// resolve_targets dependency this needs).
func TestE2ESyncGlobal(t *testing.T) {
	home := initWorkspace(t)

	if r := runGo(t, home, "add", "skill", "demo", "--description", "Demo skill"); r.ExitCode != 0 {
		t.Fatalf("go add skill: %s", r.Stderr)
	}

	res := runGo(t, home, "sync", "global")
	if res.ExitCode != 0 {
		t.Fatalf("go sync global failed (exit %d): %s\n%s", res.ExitCode, res.Stdout, res.Stderr)
	}

	// Instructions: a single-hop symlink, structurally identical once the
	// target is normalized relative to home.
	compareAgainstGolden(t, "sync global (CLAUDE.md)", home+"/.claude/CLAUDE.md", home, "sync_global_claude_md")

	// Skills: functional equivalence only (see doc comment above for why
	// structural equivalence is not expected here).
	goSkills := resolvedSkillNames(t, filepath.Join(home, ".claude", "skills"))
	sort.Strings(goSkills)

	wantData := loadGoldenSingleFile(t, "sync_global_skills", "resolved_skills.json")
	var wantSkills []string
	if err := json.Unmarshal(wantData, &wantSkills); err != nil {
		t.Fatalf("parsing golden resolved_skills.json: %v", err)
	}
	sort.Strings(wantSkills)

	if strings.Join(goSkills, ",") != strings.Join(wantSkills, ",") {
		t.Errorf("resolved skill set differs: go=%v golden(python)=%v", goSkills, wantSkills)
	}
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
