package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// Cross-validated against the real Python build_migration_plan/
// apply_migration on an identical fixture: byte-identical resulting tree
// (agents/codex.toml with its comment preserved verbatim, subagents/
// reviewer.md round-tripped, agents.toml/subagents.toml removed,
// layout.toml containing exactly "version = 2\n"), and idempotent on re-run.
func buildLegacyFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "subagents", "reviewer.md"), []byte(
		"---\ndescription: \"Reviews code\"\nagents: [\"codex\", \"claude-code\"]\n---\nReview the diff carefully.\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agents.toml"), []byte(
		"# Codex agent\n[agents.codex]\ndisplay_name = \"Codex\"\ninstruction_path = \".codex/AGENTS.md\"\n\n"+
			"# Claude Code agent\n[agents.claude-code]\ndisplay_name = \"Claude Code\"\ninstruction_path = \".claude/CLAUDE.md\"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "subagents.toml"), []byte(
		"[subagents.reviewer]\ndescription = \"Reviews code\"\nagents = [\"codex\", \"claude-code\"]\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestBuildMigrationPlan(t *testing.T) {
	root := buildLegacyFixture(t)
	plan, err := BuildMigrationPlan(root, root)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Blocked() {
		t.Fatalf("unexpected findings: %v", plan.Findings)
	}
	if len(plan.Creates) != 2 {
		t.Fatalf("creates = %v, want 2 entries", plan.Creates)
	}
	wantCodexFragment := "# Codex agent\n[agents.codex]\ndisplay_name = \"Codex\"\ninstruction_path = \".codex/AGENTS.md\"\n"
	found := false
	for _, c := range plan.Creates {
		if c.Path == "agents/codex.toml" {
			found = true
			if c.Content != wantCodexFragment {
				t.Errorf("codex.toml fragment = %q, want %q", c.Content, wantCodexFragment)
			}
		}
	}
	if !found {
		t.Fatal("expected agents/codex.toml in creates")
	}
	if len(plan.Updates) != 1 || plan.Updates[0].Path != "subagents/reviewer.md" {
		t.Errorf("updates = %v, want one subagents/reviewer.md entry", plan.Updates)
	}
	wantRemoves := map[string]bool{"agents.toml": true, "subagents.toml": true}
	if len(plan.Removes) != 2 || !wantRemoves[plan.Removes[0]] || !wantRemoves[plan.Removes[1]] {
		t.Errorf("removes = %v, want agents.toml + subagents.toml", plan.Removes)
	}
	if plan.MarkerContent != LayoutContent {
		t.Errorf("marker content = %q, want %q", plan.MarkerContent, LayoutContent)
	}
}

func TestMigrationPlanAlreadyV2IsCleanNoOp(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"agents", "subagents"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "layout.toml"), []byte(LayoutContent), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildMigrationPlan(root, root)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Blocked() {
		t.Fatalf("unexpected findings on a clean v2 workspace: %v", plan.Findings)
	}
	if len(plan.Removes) != 0 || len(plan.Creates) != 0 || len(plan.Updates) != 0 {
		t.Errorf("expected a no-op plan, got %+v", plan)
	}
}

func TestMigrationPlanBlockedOnPartialLegacy(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "agents.toml"), []byte("[agents]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildMigrationPlan(root, root)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Blocked() {
		t.Fatal("expected a blocked plan when only one legacy file is present")
	}
}
