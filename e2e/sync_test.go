//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestE2ESyncGlobal confirms `sync global` is functionally equivalent
// between the two tools, but NOT structurally identical — a real,
// significant architectural divergence this comparison surfaced (not a
// cosmetic one): Python's global_skills.py routes every agent's skills
// through ONE shared canonical consumer directory (~/.agents/skills/,
// populated with one symlink per selected skill) and then makes each
// agent's own declared skills_path a symlink pointing AT that shared
// directory — even when (as with Claude Code, whose own skills_path is
// literally ".claude/skills", not ".agents/skills") the agent's path
// string doesn't match the shared convention at all. This Go port's
// internal/sync/globalskills.go instead plans one independent symlink per
// (skill x agent) directly inside each agent's own skills_path, so
// ".claude/skills" becomes a real directory containing its own per-skill
// symlinks rather than a single symlink to a shared location. End users
// see the same skill set either way for a single-agent setup, but the two
// approaches diverge for a HOST WITH MULTIPLE AGENTS sharing one physical
// skills_path convention (6 of 8 built-in agents use ".agents/skills"
// literally) — Python writes the shared symlinks once and every such
// agent's path IS that directory; this port currently duplicates the same
// symlinks independently per agent. Confirmed by running the real Python
// CLI and inspecting its output directly (not assumed): see this test's
// skill-resolution check below, which passes because it checks the
// FUNCTIONAL property (what skills end up visible once every symlink hop
// is followed), not the raw directory layout. The raw-layout divergence
// itself is a known, real gap — flag for a follow-up port of the shared
// managed-consumer-directory architecture (internal/registry/targets_todo.go
// already tracks the underlying resolve_targets dependency this needs).
func TestE2ESyncGlobal(t *testing.T) {
	goHome, pyHome, pythonSrc := initBothWorkspaces(t)

	if r := runGo(t, goHome, "add", "skill", "demo", "--description", "Demo skill"); r.ExitCode != 0 {
		t.Fatalf("go add skill: %s", r.Stderr)
	}
	if r := runPython(t, pythonSrc, pyHome, "add", "skill", "demo", "--description", "Demo skill"); r.ExitCode != 0 {
		t.Fatalf("python add skill: %s", r.Stderr)
	}

	goRes := runGo(t, goHome, "sync", "global")
	if goRes.ExitCode != 0 {
		t.Fatalf("go sync global failed (exit %d): %s\n%s", goRes.ExitCode, goRes.Stdout, goRes.Stderr)
	}
	pyRes := runPython(t, pythonSrc, pyHome, "sync", "global")
	if pyRes.ExitCode != 0 {
		t.Fatalf("python sync global failed (exit %d): %s\n%s", pyRes.ExitCode, pyRes.Stdout, pyRes.Stderr)
	}

	// Instructions: a single-hop symlink on both sides, structurally
	// identical once targets are correctly normalized relative to home.
	compareTrees(t, "sync global (CLAUDE.md)", goHome, goHome+"/.claude/CLAUDE.md", pyHome, pyHome+"/.claude/CLAUDE.md")

	// Skills: functional equivalence only (see doc comment above for why
	// structural equivalence is not expected here).
	goSkills := resolvedSkillNames(t, filepath.Join(goHome, ".claude", "skills"))
	pySkills := resolvedSkillNames(t, filepath.Join(pyHome, ".claude", "skills"))
	sort.Strings(goSkills)
	sort.Strings(pySkills)
	if strings.Join(goSkills, ",") != strings.Join(pySkills, ",") {
		t.Errorf("resolved skill set differs: go=%v python=%v", goSkills, pySkills)
	}
}

// resolvedSkillNames fully dereferences path (which may itself be a
// symlink, or contain entries that are symlinks, or both — Python chains
// two hops here, this Go port only one) and returns the names of whatever
// skill entries are ultimately visible.
func resolvedSkillNames(t *testing.T, path string) []string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolving %s: %v", path, err)
	}
	entries, err := os.ReadDir(real)
	if err != nil {
		t.Fatalf("reading %s: %v", real, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestE2ESyncMCPLifecycle(t *testing.T) {
	goHome, pyHome, pythonSrc := initBothWorkspaces(t)

	args := []string{"add", "mcp", "weather", "--transport", "remote", "--url", "https://weather.example.com/mcp", "--agents", "claude-code"}
	if r := runGo(t, goHome, args...); r.ExitCode != 0 {
		t.Fatalf("go add mcp: %s", r.Stderr)
	}
	if r := runPython(t, pythonSrc, pyHome, args...); r.ExitCode != 0 {
		t.Fatalf("python add mcp: %s", r.Stderr)
	}

	// 1. Create.
	goRes := runGo(t, goHome, "sync", "mcp")
	if goRes.ExitCode != 0 {
		t.Fatalf("go sync mcp (create) failed: %s\n%s", goRes.Stdout, goRes.Stderr)
	}
	pyRes := runPython(t, pythonSrc, pyHome, "sync", "mcp")
	if pyRes.ExitCode != 0 {
		t.Fatalf("python sync mcp (create) failed: %s\n%s", pyRes.Stdout, pyRes.Stderr)
	}
	compareTrees(t, "sync mcp create (.claude.json)", goHome, goHome+"/.claude.json", pyHome, pyHome+"/.claude.json")

	// 2. Re-run: both should report NOOP / already-synchronized and leave
	// the file unchanged.
	goBefore, _ := os.ReadFile(goHome + "/.claude.json")
	pyBefore, _ := os.ReadFile(pyHome + "/.claude.json")
	goRes = runGo(t, goHome, "sync", "mcp")
	pyRes = runPython(t, pythonSrc, pyHome, "sync", "mcp")
	if goRes.ExitCode != 0 || pyRes.ExitCode != 0 {
		t.Fatalf("re-run sync mcp should succeed: go=%d py=%d", goRes.ExitCode, pyRes.ExitCode)
	}
	goAfter, _ := os.ReadFile(goHome + "/.claude.json")
	pyAfter, _ := os.ReadFile(pyHome + "/.claude.json")
	if string(goBefore) != string(goAfter) {
		t.Errorf("go: re-run of sync mcp changed .claude.json unexpectedly")
	}
	if string(pyBefore) != string(pyAfter) {
		t.Errorf("python: re-run of sync mcp changed .claude.json unexpectedly")
	}
	if !strings.Contains(strings.ToLower(goRes.Stdout), "already synchronized") &&
		!strings.Contains(strings.ToLower(goRes.Stdout), "noop") {
		t.Errorf("go re-run output doesn't look like a no-op: %s", goRes.Stdout)
	}

	// 3. Drift: hand-edit the runtime file identically on both sides, then
	// confirm BOTH tools refuse to sync without --force (CONFLICT), and
	// both succeed and restore canonical content with --force.
	drifted := `{"mcpServers": {"weather": {"type": "http", "url": "https://hacked.example.com/mcp"}}}`
	if err := os.WriteFile(goHome+"/.claude.json", []byte(drifted), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pyHome+"/.claude.json", []byte(drifted), 0o644); err != nil {
		t.Fatal(err)
	}

	goRes = runGo(t, goHome, "sync", "mcp")
	pyRes = runPython(t, pythonSrc, pyHome, "sync", "mcp")
	if goRes.ExitCode == 0 {
		t.Errorf("go: expected a non-zero exit on unresolved conflict, got 0 (stdout=%s)", goRes.Stdout)
	}
	if pyRes.ExitCode == 0 {
		t.Errorf("python: expected a non-zero exit on unresolved conflict, got 0 (stdout=%s)", pyRes.Stdout)
	}
	if !strings.Contains(goRes.Stdout, "CONFLICT") {
		t.Errorf("go: expected a CONFLICT line, got: %s", goRes.Stdout)
	}
	if !strings.Contains(pyRes.Stdout, "CONFLICT") {
		t.Errorf("python: expected a CONFLICT line, got: %s", pyRes.Stdout)
	}

	goRes = runGo(t, goHome, "sync", "mcp", "--force")
	pyRes = runPython(t, pythonSrc, pyHome, "sync", "mcp", "--force")
	if goRes.ExitCode != 0 {
		t.Fatalf("go --force should resolve the conflict, got exit %d: %s", goRes.ExitCode, goRes.Stdout)
	}
	if pyRes.ExitCode != 0 {
		t.Fatalf("python --force should resolve the conflict, got exit %d: %s", pyRes.ExitCode, pyRes.Stdout)
	}
	compareTrees(t, "sync mcp --force (.claude.json)", goHome, goHome+"/.claude.json", pyHome, pyHome+"/.claude.json")
}

func TestE2ESyncSubagentsPerFile(t *testing.T) {
	goHome, pyHome, pythonSrc := initBothWorkspaces(t)

	args := []string{"add", "subagent", "reviewer", "--description", "Reviews code", "--agents", "claude-code"}
	if r := runGo(t, goHome, args...); r.ExitCode != 0 {
		t.Fatalf("go add subagent: %s", r.Stderr)
	}
	if r := runPython(t, pythonSrc, pyHome, args...); r.ExitCode != 0 {
		t.Fatalf("python add subagent: %s", r.Stderr)
	}

	goRes := runGo(t, goHome, "sync", "subagents")
	if goRes.ExitCode != 0 {
		t.Fatalf("go sync subagents failed: %s\n%s", goRes.Stdout, goRes.Stderr)
	}
	pyRes := runPython(t, pythonSrc, pyHome, "sync", "subagents")
	if pyRes.ExitCode != 0 {
		t.Fatalf("python sync subagents failed: %s\n%s", pyRes.Stdout, pyRes.Stderr)
	}

	compareTrees(t, "sync subagents (.claude/agents)", goHome, goHome+"/.claude/agents", pyHome, pyHome+"/.claude/agents")
}
