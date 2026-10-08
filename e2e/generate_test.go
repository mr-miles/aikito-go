//go:build e2e_generate

// The golden-fixture generator. Run manually/occasionally (never by CI, and
// never by the normal `go test -tags e2e ./e2e/...` run) when adding a new
// e2e scenario or intentionally updating one after a confirmed Python
// behavior change. See e2e/README.md.
//
//	go test -tags e2e_generate -run TestGenerateGoldens ./e2e/... -v
package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// pyInitWorkspace runs `init workspace` against a fresh Python-only home
// and returns that home.
func pyInitWorkspace(t *testing.T, pythonSrc string) string {
	t.Helper()
	home := resolvedTempDir(t)
	withMarkerDir(t, home, ".claude")
	if r := runPython(t, pythonSrc, home, "init", "workspace"); r.ExitCode != 0 {
		t.Fatalf("python init workspace: %s", r.Stderr)
	}
	return home
}

func TestGenerateGoldens(t *testing.T) {
	pythonSrc := requirePython(t)

	t.Run("init_workspace", func(t *testing.T) {
		home := resolvedTempDir(t)
		withMarkerDir(t, home, ".claude")
		if r := runPython(t, pythonSrc, home, "init", "workspace"); r.ExitCode != 0 {
			t.Fatalf("python init workspace: %s", r.Stderr)
		}
		saveGolden(t, "init_workspace", treeManifest(t, home+"/aikito", home))
	})

	t.Run("init_project", func(t *testing.T) {
		home := pyInitWorkspace(t, pythonSrc)
		proj := resolvedTempDir(t)
		if r := runPython(t, pythonSrc, home, "init", "project", "myproj", proj); r.ExitCode != 0 {
			t.Fatalf("python init project: %s", r.Stderr)
		}
		tree := redactManifest(treeManifest(t, home+"/aikito/projects/myproj", home), []string{proj})
		saveGolden(t, "init_project", tree)
	})

	t.Run("add_skill", func(t *testing.T) {
		home := pyInitWorkspace(t, pythonSrc)
		if r := runPython(t, pythonSrc, home, "add", "skill", "demo", "--description", "Demo skill"); r.ExitCode != 0 {
			t.Fatalf("python add skill: %s", r.Stderr)
		}
		saveGolden(t, "add_skill_dir", treeManifest(t, home+"/aikito/skills/demo", home))
		saveGolden(t, "add_skill_toml", treeManifest(t, home+"/aikito/skills.toml", home))
	})

	t.Run("add_subagent", func(t *testing.T) {
		home := pyInitWorkspace(t, pythonSrc)
		if r := runPython(t, pythonSrc, home, "add", "subagent", "reviewer", "--description", "Reviews code", "--agents", "claude-code"); r.ExitCode != 0 {
			t.Fatalf("python add subagent: %s", r.Stderr)
		}
		saveGolden(t, "add_subagent", treeManifest(t, home+"/aikito/subagents", home))
	})

	t.Run("add_mcp_remote", func(t *testing.T) {
		home := pyInitWorkspace(t, pythonSrc)
		args := []string{"add", "mcp", "weather", "--transport", "remote", "--url", "https://weather.example.com/mcp", "--agents", "claude-code"}
		if r := runPython(t, pythonSrc, home, args...); r.ExitCode != 0 {
			t.Fatalf("python add mcp: %s", r.Stderr)
		}
		saveGolden(t, "add_mcp_remote", treeManifest(t, home+"/aikito/mcps", home))
	})

	t.Run("add_mcp_stdio", func(t *testing.T) {
		home := pyInitWorkspace(t, pythonSrc)
		args := []string{"add", "mcp", "files", "--transport", "stdio", "--command", "npx", "--agents", "claude-code"}
		if r := runPython(t, pythonSrc, home, args...); r.ExitCode != 0 {
			t.Fatalf("python add mcp: %s", r.Stderr)
		}
		saveGolden(t, "add_mcp_stdio", treeManifest(t, home+"/aikito/mcps", home))
	})

	t.Run("adopt_mcp", func(t *testing.T) {
		home := pyInitWorkspace(t, pythonSrc)
		existingConfig := `{"mcpServers": {"existing-server": {"type": "http", "url": "https://existing.example.com/mcp"}}}`
		if err := os.WriteFile(home+"/.claude.json", []byte(existingConfig), 0o644); err != nil {
			t.Fatal(err)
		}
		if r := runPython(t, pythonSrc, home, "adopt"); r.ExitCode != 0 {
			t.Fatalf("python adopt: %s\n%s", r.Stdout, r.Stderr)
		}
		data, err := os.ReadFile(home + "/aikito/mcps/existing-server.toml")
		if err != nil {
			t.Fatalf("expected mcps/existing-server.toml to be adopted: %v", err)
		}
		saveGolden(t, "adopt_mcp", map[string]treeEntry{"adopted.toml": {content: data}})
	})

	t.Run("rm_skill", func(t *testing.T) {
		home := pyInitWorkspace(t, pythonSrc)
		if r := runPython(t, pythonSrc, home, "add", "skill", "demo", "--description", "Demo skill"); r.ExitCode != 0 {
			t.Fatalf("python add skill: %s", r.Stderr)
		}
		if r := runPython(t, pythonSrc, home, "rm", "skill", "demo"); r.ExitCode != 0 {
			t.Fatalf("python rm skill: %s\n%s", r.Stdout, r.Stderr)
		}
		if _, err := os.Stat(home + "/aikito/skills/demo"); !os.IsNotExist(err) {
			t.Fatalf("python: expected skills/demo to be gone, stat error = %v", err)
		}
		saveGolden(t, "rm_skill_skills_toml", treeManifest(t, home+"/aikito/skills.toml", home))
	})

	t.Run("rm_mcp_sync", func(t *testing.T) {
		home := pyInitWorkspace(t, pythonSrc)
		args := []string{"add", "mcp", "weather", "--transport", "remote", "--url", "https://weather.example.com/mcp", "--agents", "claude-code"}
		if r := runPython(t, pythonSrc, home, args...); r.ExitCode != 0 {
			t.Fatalf("python add mcp: %s", r.Stderr)
		}
		if r := runPython(t, pythonSrc, home, "sync", "mcp"); r.ExitCode != 0 {
			t.Fatalf("python sync mcp: %s", r.Stdout)
		}
		if r := runPython(t, pythonSrc, home, "rm", "mcp", "weather", "--sync"); r.ExitCode != 0 {
			t.Fatalf("python rm mcp --sync: %s\n%s", r.Stdout, r.Stderr)
		}
		if _, err := os.Stat(home + "/aikito/mcps/weather.toml"); !os.IsNotExist(err) {
			t.Fatalf("python: expected mcps/weather.toml to be gone")
		}
		saveGolden(t, "rm_mcp_sync_claude_json", treeManifest(t, home+"/.claude.json", home))
	})

	t.Run("sync_global", func(t *testing.T) {
		home := pyInitWorkspace(t, pythonSrc)
		if r := runPython(t, pythonSrc, home, "add", "skill", "demo", "--description", "Demo skill"); r.ExitCode != 0 {
			t.Fatalf("python add skill: %s", r.Stderr)
		}
		if r := runPython(t, pythonSrc, home, "sync", "global"); r.ExitCode != 0 {
			t.Fatalf("python sync global: %s\n%s", r.Stdout, r.Stderr)
		}
		saveGolden(t, "sync_global_claude_md", treeManifest(t, home+"/.claude/CLAUDE.md", home))

		names := resolvedSkillNames(t, filepath.Join(home, ".claude", "skills"))
		sort.Strings(names)
		data, err := json.Marshal(names)
		if err != nil {
			t.Fatal(err)
		}
		saveGolden(t, "sync_global_skills", map[string]treeEntry{"resolved_skills.json": {content: data}})
	})

	t.Run("sync_mcp_lifecycle", func(t *testing.T) {
		home := pyInitWorkspace(t, pythonSrc)
		args := []string{"add", "mcp", "weather", "--transport", "remote", "--url", "https://weather.example.com/mcp", "--agents", "claude-code"}
		if r := runPython(t, pythonSrc, home, args...); r.ExitCode != 0 {
			t.Fatalf("python add mcp: %s", r.Stderr)
		}
		if r := runPython(t, pythonSrc, home, "sync", "mcp"); r.ExitCode != 0 {
			t.Fatalf("python sync mcp (create): %s\n%s", r.Stdout, r.Stderr)
		}
		saveGolden(t, "sync_mcp_create_claude_json", treeManifest(t, home+"/.claude.json", home))

		drifted := `{"mcpServers": {"weather": {"type": "http", "url": "https://hacked.example.com/mcp"}}}`
		if err := os.WriteFile(home+"/.claude.json", []byte(drifted), 0o644); err != nil {
			t.Fatal(err)
		}
		if r := runPython(t, pythonSrc, home, "sync", "mcp", "--force"); r.ExitCode != 0 {
			t.Fatalf("python sync mcp --force: %s", r.Stdout)
		}
		saveGolden(t, "sync_mcp_force_claude_json", treeManifest(t, home+"/.claude.json", home))
	})

	t.Run("sync_subagents_per_file", func(t *testing.T) {
		home := pyInitWorkspace(t, pythonSrc)
		args := []string{"add", "subagent", "reviewer", "--description", "Reviews code", "--agents", "claude-code"}
		if r := runPython(t, pythonSrc, home, args...); r.ExitCode != 0 {
			t.Fatalf("python add subagent: %s", r.Stderr)
		}
		if r := runPython(t, pythonSrc, home, "sync", "subagents"); r.ExitCode != 0 {
			t.Fatalf("python sync subagents: %s\n%s", r.Stdout, r.Stderr)
		}
		saveGolden(t, "sync_subagents_per_file", treeManifest(t, home+"/.claude/agents", home))
	})
}
