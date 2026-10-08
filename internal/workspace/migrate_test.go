package workspace

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// Each case's expected findings come from running the real Python
// build_migration_plan on an identical fixture. Two Python findings are
// deliberately not asserted: the TOML parser's own error wording (library-
// specific), and Python's secondary "subagents.toml: 'agents' must be a
// table" finding, produced by platform-option validation this port
// documents as not performed during migration (see buildSubagentUpdates).
// Every case is blocked on both sides either way.
func TestBuildMigrationPlanEdgeCasesMatchPython(t *testing.T) {
	const agents = "[agents.codex]\ndisplay_name = \"Codex\"\n"
	const subagents = "[subagents.reviewer]\ndescription = \"R\"\nagents = [\"codex\"]\n"
	cases := []struct {
		name        string
		agents      string
		subagents   string
		bodies      map[string]string
		layout      string // written as layout.toml (and legacy files removed) when non-empty
		wantFinding string
		wantCreates []string
		wantUpdates []string
	}{
		{"subagent registry with only a comment", agents, "# just a note\n", nil, "",
			"subagents.toml: Invalid legacy subagent registry", []string{"agents/codex.toml"}, nil},
		{"subagent name not a valid header", agents, "[subagents.\"Bad_Name\"]\ndescription = \"R\"\nagents = [\"codex\"]\n", nil, "",
			"subagents.toml: Unsupported subagent table header", []string{"agents/codex.toml"}, nil},
		{"subagent entry not a table", agents, "subagents = { reviewer = 3 }\n", nil, "",
			"subagents.toml: Unsupported subagent table header", []string{"agents/codex.toml"}, nil},
		{"missing subagent body", agents, subagents, nil, "",
			"Missing or unsafe subagent instructions: <ROOT>/subagents/reviewer.md", []string{"agents/codex.toml"}, nil},
		{"invalid subagents.toml", agents, "[subagents.reviewer\n", nil, "",
			"subagents.toml: ", []string{"agents/codex.toml"}, nil},
		{"agent name not a valid header", "[agents.\"Bad Name\"]\ndisplay_name = \"X\"\n", subagents, map[string]string{"reviewer.md": "Body.\n"}, "",
			"agents.toml: Unsupported Agent name in agents.toml", nil, []string{"subagents/reviewer.md"}},
		{"empty agents.toml with a comment", "# nothing here\n", subagents, map[string]string{"reviewer.md": "Body.\n"}, "",
			"agents.toml: Invalid legacy Agent registry", nil, []string{"subagents/reviewer.md"}},
		{"agents.toml not a registry", "foo = 1\n", subagents, map[string]string{"reviewer.md": "Body.\n"}, "",
			"agents.toml: Invalid legacy Agent registry", nil, []string{"subagents/reviewer.md"}},
		{"unsupported layout version", agents, subagents, nil, "version = 3\n",
			"Unsupported or incomplete layout marker: 3", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, d := range []string{"global", "memory", "projects", "skills", "subagents", "mcps"} {
				if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			mustWrite := func(rel, content string) {
				if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.layout != "" {
				mustWrite("layout.toml", tc.layout)
			} else {
				mustWrite("agents.toml", tc.agents)
				mustWrite("subagents.toml", tc.subagents)
			}
			for name, body := range tc.bodies {
				mustWrite(filepath.Join("subagents", name), body)
			}

			plan, err := BuildMigrationPlan(root, "")
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Blocked() {
				t.Fatalf("expected a blocked plan, got none: %+v", plan)
			}
			want := strings.ReplaceAll(tc.wantFinding, "<ROOT>", root)
			found := false
			for _, f := range plan.Findings {
				if strings.HasPrefix(f, want) {
					found = true
				}
			}
			if !found {
				t.Errorf("findings %q missing %q", plan.Findings, want)
			}
			var creates, updates []string
			for _, c := range plan.Creates {
				creates = append(creates, c.Path)
			}
			for _, u := range plan.Updates {
				updates = append(updates, u.Path)
			}
			sort.Strings(creates)
			if strings.Join(creates, ",") != strings.Join(tc.wantCreates, ",") {
				t.Errorf("creates = %v, want %v", creates, tc.wantCreates)
			}
			if strings.Join(updates, ",") != strings.Join(tc.wantUpdates, ",") {
				t.Errorf("updates = %v, want %v", updates, tc.wantUpdates)
			}
		})
	}
}
