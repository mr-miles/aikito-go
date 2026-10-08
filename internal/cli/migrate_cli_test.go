package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mr-miles/aikito-go/internal/workspace"
)

// mgLegacyWorkspace builds a pre-v2 workspace: monolithic agents.toml and
// subagents.toml (with comments attached to their table headers) and a
// legacy subagent body file. Migrating this exact fixture with the real
// Python `aikito migrate workspace-resources` produced a tree byte-for-byte
// identical to this port's output (the expected contents asserted below).
func mgLegacyWorkspace(t *testing.T) (Environment, string) {
	t.Helper()
	env := testEnv(t)
	root := filepath.Join(env.Home, "aikito")
	for _, d := range []string{"global", "memory", "projects", "skills", "subagents", "mcps"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "agents.toml"),
		"# Codex agent\n# second line\n[agents.codex]\ndisplay_name = \"Codex\"\ninstruction_path = \".codex/AGENTS.md\"\n\n"+
			"# Claude\n[agents.claude-code]\ndisplay_name = \"Claude Code\"\ninstruction_path = \".claude/CLAUDE.md\"\n")
	writeFile(t, filepath.Join(root, "subagents.toml"),
		"# reviewer comment\n[subagents.reviewer]\ndescription = \"Reviews code\"\nagents = [\"codex\"]\n")
	writeFile(t, filepath.Join(root, "subagents", "reviewer.md"), "Review the code carefully.\n")
	return env, root
}

func mgRun(t *testing.T, env Environment, want int, args ...string) (string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := Run(append([]string{"migrate"}, args...), nil, &out, &errOut, env); code != want {
		t.Fatalf("migrate %v: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", args, code, want, out.String(), errOut.String())
	}
	return out.String(), errOut.String()
}

// mgAssertNoTransactionState confirms the transaction engine left nothing
// behind: no pending journal and no per-transaction staging directory.
func mgAssertNoTransactionState(t *testing.T, root string) {
	t.Helper()
	state := filepath.Join(root, ".local", "state", "aikito", "workspace-transactions")
	if _, err := os.Stat(filepath.Join(state, "pending.json")); err == nil {
		t.Errorf("pending transaction journal left behind")
	}
	if entries, err := os.ReadDir(filepath.Join(state, "tx")); err == nil && len(entries) > 0 {
		t.Errorf("transaction staging left behind: %d entries", len(entries))
	}
}

func TestMigrateDryRunWritesNothing(t *testing.T) {
	env, root := mgLegacyWorkspace(t)
	before := iwTree(t, root)
	out, _ := mgRun(t, env, 0, "workspace-resources", "--dry-run")
	for _, want := range []string{
		"[CREATE] agents/claude-code.toml", "[CREATE] agents/codex.toml", "[UPDATE] subagents/reviewer.md",
		"[REMOVE] agents.toml", "[REMOVE] subagents.toml", "[CREATE] layout.toml", "[DRY RUN]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	iwAssertTreesEqual(t, "dry run", before, iwTree(t, root))
	if _, err := os.Stat(filepath.Join(root, ".local")); err == nil {
		t.Errorf("dry run should not create transaction state")
	}
}

func TestMigrateAppliesAndIsIdempotent(t *testing.T) {
	env, root := mgLegacyWorkspace(t)
	mgRun(t, env, 0, "workspace-resources")

	want := map[string]string{
		"agents/codex.toml":       "# Codex agent\n# second line\n[agents.codex]\ndisplay_name = \"Codex\"\ninstruction_path = \".codex/AGENTS.md\"\n",
		"agents/claude-code.toml": "# Claude\n[agents.claude-code]\ndisplay_name = \"Claude Code\"\ninstruction_path = \".claude/CLAUDE.md\"\n",
		"subagents/reviewer.md":   "---\n# reviewer comment\ndescription: \"Reviews code\"\nagents: [\"codex\"]\n---\nReview the code carefully.\n",
		"layout.toml":             workspace.LayoutContent,
	}
	for rel, content := range want {
		if got := iwRead(t, filepath.Join(root, rel)); got != content {
			t.Errorf("%s:\n got %q\nwant %q", rel, got, content)
		}
	}
	for _, legacy := range workspace.LegacyFiles {
		if _, err := os.Lstat(filepath.Join(root, legacy)); err == nil {
			t.Errorf("legacy file %s should have been removed", legacy)
		}
	}
	if err := workspace.RequireCurrentLayout(root); err != nil {
		t.Errorf("migrated workspace should satisfy the current layout: %v", err)
	}
	mgAssertNoTransactionState(t, root)

	before := iwTree(t, root)
	out, _ := mgRun(t, env, 0, "workspace-resources")
	if !strings.Contains(out, "already on the current layout") {
		t.Errorf("re-run should be a no-op, got:\n%s", out)
	}
	iwAssertTreesEqual(t, "re-run", before, iwTree(t, root))
}

// The cases below are all refused at planning time, before any write; each
// must leave the workspace exactly as it was.
func TestMigrateBlockedCasesWriteNothing(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(t *testing.T, root string)
		wantErr string
	}{
		{
			// Python's build_migration_plan: a v2 marker alongside legacy
			// files is an incomplete/unsupported state, not something to
			// migrate over.
			"layout marker and legacy files both present",
			func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "layout.toml"), workspace.LayoutContent)
			},
			"Unsupported or incomplete layout marker: 2",
		},
		{
			"invalid legacy agents.toml",
			func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "agents.toml"), "[agents.codex\n") },
			"agents.toml:",
		},
		{
			"only one legacy file",
			func(t *testing.T, root string) { os.Remove(filepath.Join(root, "subagents.toml")) },
			"Both legacy configuration files are required",
		},
		{
			"target agent file already exists",
			func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "agents", "codex.toml"), "[agents.codex]\n")
			},
			"Target already exists: agents/codex.toml",
		},
		{
			"unregistered subagent entry",
			func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "subagents", "README.txt"), "junk\n")
			},
			"Unregistered subagent entry",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, root := mgLegacyWorkspace(t)
			tc.mutate(t, root)
			before := iwTree(t, root)
			_, errOut := mgRun(t, env, 1, "workspace-resources")
			if !strings.Contains(errOut, tc.wantErr) {
				t.Errorf("stderr missing %q:\n%s", tc.wantErr, errOut)
			}
			iwAssertTreesEqual(t, tc.name, before, iwTree(t, root))
			mgAssertNoTransactionState(t, root)
		})
	}
}

// applyMigration re-plans before writing and refuses a plan that no longer
// matches the workspace — the guard against an edit landing between the
// preview and the commit. Nothing may be written in that case.
func TestApplyMigrationRefusesStalePlan(t *testing.T) {
	env, root := mgLegacyWorkspace(t)
	plan, err := workspace.BuildMigrationPlan(root, env.Home)
	if err != nil || plan.Blocked() {
		t.Fatalf("plan: %v %v", err, plan.Findings)
	}
	agents := filepath.Join(root, "agents.toml")
	writeFile(t, agents, iwRead(t, agents)+"\n[agents.extra]\ndisplay_name = \"Extra\"\n")
	before := iwTree(t, root)

	err = applyMigration(plan, root)
	if err == nil || !strings.Contains(err.Error(), "workspace changed after migration planning") {
		t.Fatalf("expected a stale-plan refusal, got %v", err)
	}
	iwAssertTreesEqual(t, "stale plan", before, iwTree(t, root))
	mgAssertNoTransactionState(t, root)
}

func TestMigrateUsageErrors(t *testing.T) {
	env := testEnv(t)
	for _, args := range [][]string{{}, {"other"}, {"workspace-resources", "--bogus"}} {
		var out, errOut bytes.Buffer
		if code := Run(append([]string{"migrate"}, args...), nil, &out, &errOut, env); code != 2 {
			t.Errorf("migrate %v: exit %d, want 2", args, code)
		}
	}
}
