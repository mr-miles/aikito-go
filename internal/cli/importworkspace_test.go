package cli

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mr-miles/aikito-go/internal/registry"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// iwPair builds a target workspace (the Environment returned, as cmdImport
// always imports INTO env's workspace) and a separate source workspace,
// both freshly initialized and both carrying claude-code's agent
// definition so subagents/mcps referencing it resolve on either side.
func iwPair(t *testing.T) (target Environment, sourceEnv Environment, sourceRoot string) {
	t.Helper()
	target = testEnv(t)
	sourceEnv = Environment{Home: t.TempDir(), Env: workspace.MapEnv{}, Cwd: t.TempDir()}
	tmpl, err := registry.BundledAgentTemplateText("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range []Environment{target, sourceEnv} {
		iwRun(t, env, 0, "init", "workspace")
		dir, err := env.AikitoDir()
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "agents", "claude-code.toml"), tmpl)
	}
	sourceRoot, err = sourceEnv.AikitoDir()
	if err != nil {
		t.Fatal(err)
	}
	return target, sourceEnv, sourceRoot
}

// iwRun runs a command and fails the test unless it exits with want.
func iwRun(t *testing.T, env Environment, want int, args ...string) (string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := Run(args, nil, &out, &errOut, env); code != want {
		t.Fatalf("%v: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", args, code, want, out.String(), errOut.String())
	}
	return out.String(), errOut.String()
}

// iwTree snapshots every file (content), symlink (target) and directory
// under root, excluding host-local .git/.local, so a test can prove a
// command wrote nothing.
func iwTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" || rel == ".local" {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(path)
			tree[rel] = "-> " + target
		case d.IsDir():
			tree[rel] = "<dir>"
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			tree[rel] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func iwAssertTreesEqual(t *testing.T, label string, want, got map[string]string) {
	t.Helper()
	for k, v := range want {
		if g, ok := got[k]; !ok {
			t.Errorf("%s: %s disappeared", label, k)
		} else if g != v {
			t.Errorf("%s: %s changed:\n--- before ---\n%s\n--- after ---\n%s", label, k, v, g)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: unexpected new path %s", label, k)
		}
	}
}

func iwRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// iwConflictFixture gives source and target a differing memory note (a
// CONFLICT) plus a source-only note (a CREATE).
func iwConflictFixture(t *testing.T) (Environment, string, string) {
	t.Helper()
	target, _, src := iwPair(t)
	dst, _ := target.AikitoDir()
	writeFile(t, filepath.Join(src, "memory", "notes", "decision.md"), "# decision from source\n")
	writeFile(t, filepath.Join(dst, "memory", "notes", "decision.md"), "# decision from target\n")
	writeFile(t, filepath.Join(src, "memory", "notes", "fresh.md"), "# only in source\n")
	return target, src, dst
}

func TestImportWorkspaceCreatesNewResources(t *testing.T) {
	target, sourceEnv, src := iwPair(t)
	dst, _ := target.AikitoDir()
	iwRun(t, sourceEnv, 0, "add", "skill", "src-skill", "--description", "From source")
	iwRun(t, sourceEnv, 0, "add", "subagent", "reviewer", "--description", "Reviews code", "--agents", "claude-code")
	iwRun(t, sourceEnv, 0, "add", "mcp", "weather", "--transport", "remote", "--url", "https://w.example/mcp", "--agents", "claude-code")
	writeFile(t, filepath.Join(src, "memory", "notes", "fresh.md"), "# only in source\n")

	out, _ := iwRun(t, target, 0, "import", "workspace", src)
	if !strings.Contains(out, "[SUCCESS] Imported 5 resource(s)") {
		t.Errorf("expected 5 imports, got:\n%s", out)
	}
	for _, rel := range []string{
		"skills/src-skill/SKILL.md", "subagents/reviewer.md", "mcps/weather.toml", "memory/notes/fresh.md",
	} {
		if got, want := iwRead(t, filepath.Join(dst, rel)), iwRead(t, filepath.Join(src, rel)); got != want {
			t.Errorf("%s: imported content differs from source:\n%s\nvs\n%s", rel, got, want)
		}
	}
	if !strings.Contains(iwRead(t, filepath.Join(dst, "skills.toml")), `"src-skill"`) {
		t.Errorf("skills.toml did not gain src-skill selection")
	}

	before := iwTree(t, dst)
	out, _ = iwRun(t, target, 0, "import", "workspace", src)
	if !strings.Contains(out, "Nothing to import") {
		t.Errorf("re-run should be a no-op, got:\n%s", out)
	}
	iwAssertTreesEqual(t, "re-run", before, iwTree(t, dst))
}

// The per-resource decisions below were cross-validated against the real
// Python `aikito import workspace --dry-run --verbose` on an identical
// fixture pair. One cosmetic difference: Python groups shared-file changes
// (skills.toml) into a single file-level line labelled UPDATE when the file
// already exists, while its per-resource decision (decide_resource) is
// CREATE, which is what this port prints.
func TestImportWorkspaceDecisionsMatchPython(t *testing.T) {
	target, sourceEnv, src := iwPair(t)
	dst, _ := target.AikitoDir()
	iwRun(t, sourceEnv, 0, "add", "skill", "src-skill", "--description", "From source")
	writeFile(t, filepath.Join(src, "memory", "notes", "decision.md"), "# decision src\n")
	writeFile(t, filepath.Join(dst, "memory", "notes", "decision.md"), "# decision tgt\n")
	writeFile(t, filepath.Join(src, "memory", "notes", "fresh.md"), "# only src\n")

	out, _ := iwRun(t, target, 2, "import", "workspace", src, "--dry-run", "--verbose")
	for _, want := range []string{
		"[NOOP] agents/claude-code.toml (agent:claude-code)",
		"[NOOP] config.toml (config:memory.stale_days)",
		"[NOOP] config.toml (config:update.check)",
		"[NOOP] global/AGENTS.md (global-instructions:AGENTS.md)",
		"[CONFLICT] memory/notes/decision.md (memory:notes/decision.md): Contents differ",
		"[CREATE] memory/notes/fresh.md (memory:notes/fresh.md)",
		"[NOOP] skills.toml (skill-selection:aikito)",
		"[NOOP] skills.toml (skill-selection:durable-memory)",
		"[CREATE] skills.toml (skill-selection:src-skill)",
		"[CREATE] skills/src-skill (skill:src-skill)",
		"Summary: 3 create, 0 update, 1 conflict, 6 unchanged",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestImportWorkspaceIdenticalIsNoop(t *testing.T) {
	target, _, src := iwPair(t)
	dst, _ := target.AikitoDir()
	before := iwTree(t, dst)
	out, _ := iwRun(t, target, 0, "import", "workspace", src, "--verbose")
	if !strings.Contains(out, "Nothing to import") || !strings.Contains(out, "[NOOP] global/AGENTS.md") {
		t.Errorf("unexpected output:\n%s", out)
	}
	if strings.Contains(out, "[CREATE]") || strings.Contains(out, "[CONFLICT]") {
		t.Errorf("identical workspaces should produce only NOOPs:\n%s", out)
	}
	iwAssertTreesEqual(t, "identical import", before, iwTree(t, dst))
}

// Python's cmd_import_workspace imports every non-conflicting resource even
// when conflicts exist, leaves the conflicting ones unchanged, and exits 2.
// This port previously refused the whole import and exited 1; cross-
// validated against live Python: identical resulting tree, same exit code.
func TestImportWorkspaceConflictImportsOthersAndKeepsTarget(t *testing.T) {
	target, src, dst := iwConflictFixture(t)
	out, errOut := iwRun(t, target, 2, "import", "workspace", src)
	if !strings.Contains(out, "[CONFLICT] memory/notes/decision.md (memory:notes/decision.md): Contents differ") {
		t.Errorf("missing CONFLICT line:\n%s", out)
	}
	if !strings.Contains(out, "[PARTIAL]") || !strings.Contains(errOut, "--keep-target RESOURCE_ID or --take-source RESOURCE_ID") {
		t.Errorf("expected PARTIAL result and resolution hint:\nstdout:\n%s\nstderr:\n%s", out, errOut)
	}
	if got := iwRead(t, filepath.Join(dst, "memory", "notes", "decision.md")); got != "# decision from target\n" {
		t.Errorf("conflicting target note was overwritten: %q", got)
	}
	if got := iwRead(t, filepath.Join(dst, "memory", "notes", "fresh.md")); got != "# only in source\n" {
		t.Errorf("non-conflicting note not imported: %q", got)
	}
}

func TestImportWorkspaceOnlyConflictsChangesNothing(t *testing.T) {
	target, _, src := iwPair(t)
	dst, _ := target.AikitoDir()
	writeFile(t, filepath.Join(src, "memory", "notes", "decision.md"), "# src\n")
	writeFile(t, filepath.Join(dst, "memory", "notes", "decision.md"), "# tgt\n")
	before := iwTree(t, dst)
	out, _ := iwRun(t, target, 2, "import", "workspace", src)
	if !strings.Contains(out, "No resources changed") {
		t.Errorf("unexpected output:\n%s", out)
	}
	iwAssertTreesEqual(t, "conflict-only import", before, iwTree(t, dst))
}

func TestImportWorkspaceTakeSourceResolvesConflict(t *testing.T) {
	target, src, dst := iwConflictFixture(t)
	out, _ := iwRun(t, target, 0, "import", "workspace", src, "--take-source", "memory:notes/decision.md")
	if !strings.Contains(out, "[UPDATE] memory/notes/decision.md") {
		t.Errorf("expected UPDATE for the resolved conflict:\n%s", out)
	}
	if got := iwRead(t, filepath.Join(dst, "memory", "notes", "decision.md")); got != "# decision from source\n" {
		t.Errorf("--take-source did not apply source content: %q", got)
	}
}

func TestImportWorkspaceKeepTargetLeavesTargetUntouched(t *testing.T) {
	target, src, dst := iwConflictFixture(t)
	iwRun(t, target, 0, "import", "workspace", src, "--keep-target", "memory:notes/decision.md")
	if got := iwRead(t, filepath.Join(dst, "memory", "notes", "decision.md")); got != "# decision from target\n" {
		t.Errorf("--keep-target changed the target: %q", got)
	}
	if got := iwRead(t, filepath.Join(dst, "memory", "notes", "fresh.md")); got != "# only in source\n" {
		t.Errorf("other resources should still import: %q", got)
	}
}

func TestImportWorkspaceRejectsConflictingResolutions(t *testing.T) {
	target, src, dst := iwConflictFixture(t)
	before := iwTree(t, dst)
	_, errOut := iwRun(t, target, 1, "import", "workspace", src,
		"--keep-target", "memory:notes/decision.md", "--take-source", "memory:notes/decision.md")
	if !strings.Contains(errOut, "Conflicting resolutions for: memory:notes/decision.md") {
		t.Errorf("unexpected stderr: %s", errOut)
	}
	iwAssertTreesEqual(t, "conflicting resolutions", before, iwTree(t, dst))
}

func TestImportWorkspaceDryRunWritesNothing(t *testing.T) {
	t.Run("with conflicts", func(t *testing.T) {
		target, src, dst := iwConflictFixture(t)
		before := iwTree(t, dst)
		out, _ := iwRun(t, target, 2, "import", "workspace", src, "--dry-run")
		if !strings.Contains(out, "[CREATE] memory/notes/fresh.md") || !strings.Contains(out, "[DRY RUN]") {
			t.Errorf("dry run should preview the create:\n%s", out)
		}
		iwAssertTreesEqual(t, "dry run", before, iwTree(t, dst))
	})
	t.Run("without conflicts", func(t *testing.T) {
		target, sourceEnv, src := iwPair(t)
		dst, _ := target.AikitoDir()
		iwRun(t, sourceEnv, 0, "add", "skill", "src-skill", "--description", "From source")
		before := iwTree(t, dst)
		out, _ := iwRun(t, target, 0, "import", "workspace", src, "--dry-run")
		if !strings.Contains(out, "[CREATE] skills/src-skill") {
			t.Errorf("dry run should preview the skill:\n%s", out)
		}
		iwAssertTreesEqual(t, "dry run", before, iwTree(t, dst))
	})
}

func TestImportWorkspaceSourceNotAWorkspace(t *testing.T) {
	target, _, _ := iwPair(t)
	_, errOut := iwRun(t, target, 1, "import", "workspace", t.TempDir())
	if !strings.Contains(errOut, "Source is not an Aikito workspace") {
		t.Errorf("unexpected stderr: %s", errOut)
	}
}

func TestImportWorkspaceTargetNotAWorkspace(t *testing.T) {
	_, _, src := iwPair(t)
	_, errOut := iwRun(t, testEnv(t), 1, "import", "workspace", src)
	if !strings.Contains(errOut, "Target is not an Aikito workspace") {
		t.Errorf("unexpected stderr: %s", errOut)
	}
}

// fixtureCredentialValue is long enough to match aikito's own plaintext-
// credential heuristic but deliberately matches no real provider's key
// format, so repository secret scanners don't flag it.
const fixtureCredentialValue = "fake-value-for-tests-0000000000"
const fixtureCredentialLine = "api_key = " + fixtureCredentialValue

func TestImportWorkspaceWarnsOnPlausibleSecret(t *testing.T) {
	target, _, src := iwPair(t)
	writeFile(t, filepath.Join(src, "memory", "notes", "creds.md"), fixtureCredentialLine+"\n")
	_, errOut := iwRun(t, target, 0, "import", "workspace", src)
	if !strings.Contains(errOut, "[WARNING]") || !strings.Contains(errOut, "creds.md") {
		t.Errorf("expected a credential warning naming creds.md, got: %s", errOut)
	}
	if strings.Contains(errOut, fixtureCredentialValue) {
		t.Errorf("warning must not echo the secret value: %s", errOut)
	}
}

// inbox.path is host-local (LOCAL_CONFIG): a source workspace's inbox
// location must never be imported over the target's.
func TestImportWorkspaceNeverImportsInboxPath(t *testing.T) {
	target, _, src := iwPair(t)
	dst, _ := target.AikitoDir()
	cfg := filepath.Join(src, "config.toml")
	text := iwRead(t, cfg)
	if !strings.Contains(text, `path = "inbox"`) {
		t.Fatalf("fixture assumption broken, config.toml:\n%s", text)
	}
	writeFile(t, cfg, strings.Replace(text, `path = "inbox"`, `path = "elsewhere"`, 1))
	if err := os.MkdirAll(filepath.Join(src, "elsewhere"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := iwRead(t, filepath.Join(dst, "config.toml"))
	out, _ := iwRun(t, target, 0, "import", "workspace", src, "--verbose")
	if strings.Contains(out, "config:inbox.path") {
		t.Errorf("config:inbox.path must not appear in the import plan:\n%s", out)
	}
	if after := iwRead(t, filepath.Join(dst, "config.toml")); after != before {
		t.Errorf("target config.toml changed:\n%s", after)
	}
}

func TestImportWorkspaceUsageErrors(t *testing.T) {
	env := testEnv(t)
	for _, args := range [][]string{
		{"import"},
		{"import", "other"},
		{"import", "workspace"},
		{"import", "workspace", "x", "--bogus"},
		{"import", "workspace", "x", "--keep-target"},
		{"import", "workspace", "x", "--take-source"},
	} {
		var out, errOut bytes.Buffer
		if code := Run(args, nil, &out, &errOut, env); code != 2 {
			t.Errorf("%v: exit %d, want 2 (stderr %q)", args, code, errOut.String())
		}
	}
}

// importing.py rejects a resolution ID that isn't a resource in the source
// workspace ("Unknown import resource ID: ...") rather than ignoring it.
func TestCmdImportUnknownResolutionID(t *testing.T) {
	for _, flag := range []string{"--keep-target", "--take-source"} {
		env := testEnv(t)
		var out, errOut bytes.Buffer
		if code := Run([]string{"init", "workspace"}, nil, &out, &errOut, env); code != 0 {
			t.Fatalf("init target: %s", errOut.String())
		}
		srcEnv := testEnv(t)
		if code := Run([]string{"init", "workspace"}, nil, &out, &errOut, srcEnv); code != 0 {
			t.Fatalf("init source: %s", errOut.String())
		}
		srcRoot, _ := srcEnv.AikitoDir()
		out.Reset()
		errOut.Reset()
		code := Run([]string{"import", "workspace", srcRoot, flag, "skill:does-not-exist"}, nil, &out, &errOut, env)
		if code != 1 {
			t.Errorf("%s: exit = %d, want 1", flag, code)
		}
		if !strings.Contains(errOut.String(), "Unknown import resource ID: skill:does-not-exist") {
			t.Errorf("%s: stderr = %q", flag, errOut.String())
		}
	}
}
