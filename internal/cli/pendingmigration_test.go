package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mr-miles/aikito-rs/internal/sync"
	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// pmLegacyWorkspace writes a pre-v2 workspace (monolithic agents.toml and
// subagents.toml, no layout.toml) at the env's workspace root.
func pmLegacyWorkspace(t *testing.T) (Environment, string) {
	t.Helper()
	env := testEnv(t)
	root, err := env.AikitoDir()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"agents.toml":           "# Codex agent\n[agents.codex]\ndisplay_name = \"Codex\"\ninstruction_path = \".codex/AGENTS.md\"\n",
		"subagents.toml":        "[subagents.reviewer]\ndescription = \"Reviews code\"\nagents = [\"codex\"]\n",
		"subagents/reviewer.md": "Review the code carefully.\n",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return env, root
}

// pmCrashMidMigration leaves a genuine pending "layout" journal behind: it
// runs a real sync.Apply that writes layout.toml under the migration path
// policy and panics in the verify callback, i.e. after the journal is
// written and the resource renamed into place but before commit. Apply
// only rolls back on a returned error, so the panic models a process that
// died at that point.
func pmCrashMidMigration(t *testing.T, root string) {
	t.Helper()
	stage := filepath.Join(t.TempDir(), "layout.toml")
	if err := os.WriteFile(stage, []byte(workspace.LayoutContent), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := workspace.FingerprintResource(stage, "layout")
	if err != nil {
		t.Fatal(err)
	}
	changes := []sync.Change{{Path: "layout.toml", Kind: "layout", Source: stage, After: &after}}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected the simulated crash")
			}
		}()
		_ = sync.Apply([]string{root}, changes, nil, func() error { panic("simulated crash") }, migrationPathPolicy(), nil)
	}()
	kinds, err := sync.PendingKinds([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := kinds["layout"]; !ok {
		t.Fatalf("expected a pending layout journal, got %v", kinds)
	}
}

func TestInterruptedMigrationBlocksCommandsUntilMigrateRecovers(t *testing.T) {
	env, root := pmLegacyWorkspace(t)
	pmCrashMidMigration(t, root)

	var out, errOut bytes.Buffer
	if code := Run([]string{"show", "skills"}, nil, &out, &errOut, env); code == 0 {
		t.Fatalf("show skills should refuse a workspace with an interrupted migration; stdout=%s", out.String())
	}
	want := "Workspace migration is incomplete: " + root + "\nRun: aikito migrate workspace-resources"
	if !strings.Contains(errOut.String(), want) {
		t.Fatalf("stderr = %q, want it to contain %q", errOut.String(), want)
	}

	// A dry run doesn't recover (matching Python), so the journal survives.
	out.Reset()
	errOut.Reset()
	Run([]string{"migrate", "workspace-resources", "--dry-run"}, nil, &out, &errOut, env)
	if kinds, err := sync.PendingKinds([]string{root}); err != nil || len(kinds) == 0 {
		t.Fatalf("dry run must not recover the journal; kinds=%v err=%v", kinds, err)
	}

	out.Reset()
	errOut.Reset()
	if code := Run([]string{"migrate", "workspace-resources"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("migrate exit %d; stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "[RECOVER] Interrupted workspace migration recovered") {
		t.Errorf("expected a [RECOVER] line, got %q", out.String())
	}
	if kinds, err := sync.PendingKinds([]string{root}); err != nil || len(kinds) != 0 {
		t.Fatalf("journal should be gone after migrate; kinds=%v err=%v", kinds, err)
	}
	if err := workspace.RequireCurrentLayout(root); err != nil {
		t.Fatalf("workspace should be on the current layout after migrate: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "agents", "codex.toml"))
	if err != nil || !strings.Contains(string(data), "# Codex agent") {
		t.Fatalf("migrated agent file missing or lost its comment: %q, %v", data, err)
	}

	out.Reset()
	errOut.Reset()
	if code := Run([]string{"show", "skills"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("show skills after migrate exit %d: %s", code, errOut.String())
	}
}

func TestCorruptedJournalIsUnsafeTransactionState(t *testing.T) {
	env, root := pmLegacyWorkspace(t)
	pmCrashMidMigration(t, root)
	journal := filepath.Join(root, ".local", "state", "aikito", "workspace-transactions", "pending.json")
	if err := os.WriteFile(journal, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := Run([]string{"show", "skills"}, nil, &out, &errOut, env); code == 0 {
		t.Fatal("show skills should refuse a corrupted transaction journal")
	}
	want := "Unsafe workspace transaction state: Invalid workspace journal: " + journal
	if !strings.Contains(errOut.String(), want) {
		t.Fatalf("stderr = %q, want it to contain %q", errOut.String(), want)
	}

	// migrate can't recover a journal it can't read either; it must fail
	// loudly and leave the journal in place for inspection.
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"migrate", "workspace-resources"}, nil, &out, &errOut, env); code == 0 {
		t.Fatalf("migrate should fail on a corrupted journal; stdout=%s", out.String())
	}
	if _, err := os.Stat(journal); err != nil {
		t.Fatalf("corrupted journal must be left in place: %v", err)
	}
}
