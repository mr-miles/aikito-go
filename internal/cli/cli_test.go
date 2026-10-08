package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/mr-miles/aikito-go/internal/workspace"
)

func testEnv(t *testing.T) Environment {
	t.Helper()
	// Deterministic agent detection: PATH restricted to just wherever git
	// lives (init workspace's own git-init step needs a real git binary),
	// with no agent CLIs (claude/codex/...) and no marker dirs under this
	// fake home.
	t.Setenv("PATH", "/usr/bin:/bin")
	return Environment{Home: resolvedTempDir(t), Env: workspace.MapEnv{}, Cwd: resolvedTempDir(t)}
}

func TestCmdVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"version"}, nil, &out, &errOut, Environment{})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut.String())
	}
	want := "aikito " + Version + "\n"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

func TestCmdPathWorkspaceDefault(t *testing.T) {
	env := testEnv(t)
	var out, errOut bytes.Buffer
	code := Run([]string{"path", "workspace"}, nil, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut.String())
	}
	want := filepath.Join(env.Home, "aikito") + "\n"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

func TestCmdInitWorkspaceDefault(t *testing.T) {
	env := testEnv(t)
	var out, errOut bytes.Buffer
	code := Run([]string{"init", "workspace"}, nil, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, out.String(), errOut.String())
	}

	target := filepath.Join(env.Home, "aikito")

	// Self-check against the already-built, independently-tested scanner:
	// a freshly initialized workspace must satisfy RequireCurrentLayout and
	// scan with zero error-level findings.
	if err := workspace.RequireCurrentLayout(target); err != nil {
		t.Fatalf("RequireCurrentLayout rejected freshly-initialized workspace: %v", err)
	}
	snap, err := workspace.SnapshotWorkspace(target, env.Home)
	if err != nil {
		t.Fatalf("SnapshotWorkspace error: %v", err)
	}
	for _, f := range snap.Findings {
		if f.Status == "error" {
			t.Errorf("unexpected scanner finding on fresh init: %+v", f)
		}
	}

	for _, rel := range []string{
		"layout.toml", "config.toml", "skills.toml", ".gitignore",
		"global/AGENTS.md", "skills/aikito/SKILL.md", "skills/durable-memory/SKILL.md",
		".git/HEAD",
	} {
		if _, err := os.Stat(filepath.Join(target, rel)); err != nil {
			t.Errorf("expected %s to exist: %v", rel, err)
		}
	}

	data, err := os.ReadFile(filepath.Join(target, "layout.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != workspace.LayoutContent {
		t.Errorf("layout.toml = %q, want %q", data, workspace.LayoutContent)
	}

	// Re-running without --force should be idempotent (no error, files kept).
	var out2, errOut2 bytes.Buffer
	code = Run([]string{"init", "workspace"}, nil, &out2, &errOut2, env)
	if code != 0 {
		t.Fatalf("second init exit code = %d, stderr = %s", code, errOut2.String())
	}
}

func TestCmdInitWorkspaceExplicitPathPersistsPointer(t *testing.T) {
	env := testEnv(t)
	target := filepath.Join(env.Home, "custom-ws")
	var out, errOut bytes.Buffer
	code := Run([]string{"init", "workspace", target}, nil, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut.String())
	}
	resolved, source, err := workspace.ResolveWorkspaceWithSource(env.Home, env.Env)
	if err != nil {
		t.Fatal(err)
	}
	if source != "configured" {
		t.Errorf("source = %q, want configured", source)
	}
	wantResolved, _ := workspace.ResolvePath(target)
	if resolved != wantResolved {
		t.Errorf("resolved workspace = %q, want %q", resolved, wantResolved)
	}
}

func TestCmdInitProject(t *testing.T) {
	env := testEnv(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"init", "workspace"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("init workspace failed: %d %s", code, errOut.String())
	}

	projectDir := filepath.Join(env.Home, "myproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	errOut.Reset()
	code := Run([]string{"init", "project", "myproj", projectDir, "--description", "A test project"}, nil, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%s stderr=%s", code, out.String(), errOut.String())
	}

	aikitoDir := filepath.Join(env.Home, "aikito")
	configPath := filepath.Join(aikitoDir, "projects", "myproj", "agent.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("agent.toml not written: %v", err)
	}
	doc, err := workspace.DecodeTOML(data)
	if err != nil {
		t.Fatalf("agent.toml is not valid TOML: %v\ncontent:\n%s", err, data)
	}
	if doc["name"] != "myproj" {
		t.Errorf("name = %v, want myproj", doc["name"])
	}
	if doc["description"] != "A test project" {
		t.Errorf("description = %v, want 'A test project'", doc["description"])
	}
	if doc["sync_mode"] != "link" {
		t.Errorf("sync_mode = %v, want link", doc["sync_mode"])
	}

	agentsMD := filepath.Join(aikitoDir, "projects", "myproj", "AGENTS.md")
	if _, err := os.Stat(agentsMD); err != nil {
		t.Errorf("AGENTS.md not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(aikitoDir, "projects", "myproj", "memory", "notes")); err != nil {
		t.Errorf("memory/notes dir not created: %v", err)
	}
}

func TestCmdInitProjectInvalidName(t *testing.T) {
	env := testEnv(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"init", "workspace"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("init workspace failed: %d", code)
	}
	out.Reset()
	errOut.Reset()
	code := Run([]string{"init", "project", "Bad Name!", env.Cwd}, nil, &out, &errOut, env)
	if code == 0 {
		t.Fatal("expected a non-zero exit code for an invalid project name")
	}
}

func TestCmdUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"bogus"}, nil, &out, &errOut, Environment{})
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}
