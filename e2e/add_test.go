//go:build e2e

package e2e

import "testing"

// initWorkspace inits a fresh Go-only fixture workspace under a deterministic
// PATH/marker-dir setup (see common_test.go's testPATH/withMarkerDir).
func initWorkspace(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	withMarkerDir(t, home, ".claude")
	if r := runGo(t, home, "init", "workspace"); r.ExitCode != 0 {
		t.Fatalf("go init workspace: %s", r.Stderr)
	}
	return home
}

func TestE2EAddSkill(t *testing.T) {
	home := initWorkspace(t)

	res := runGo(t, home, "add", "skill", "demo", "--description", "Demo skill")
	if res.ExitCode != 0 {
		t.Fatalf("go add skill: %s", res.Stderr)
	}

	compareAgainstGolden(t, "add skill", home+"/aikito/skills/demo", home, "add_skill_dir")
	compareAgainstGolden(t, "add skill (skills.toml)", home+"/aikito/skills.toml", home, "add_skill_toml")
}

func TestE2EAddSubagent(t *testing.T) {
	home := initWorkspace(t)

	res := runGo(t, home, "add", "subagent", "reviewer", "--description", "Reviews code", "--agents", "claude-code")
	if res.ExitCode != 0 {
		t.Fatalf("go add subagent: %s", res.Stderr)
	}

	compareAgainstGolden(t, "add subagent", home+"/aikito/subagents", home, "add_subagent")
}

func TestE2EAddMCPRemote(t *testing.T) {
	home := initWorkspace(t)

	args := []string{"add", "mcp", "weather", "--transport", "remote", "--url", "https://weather.example.com/mcp", "--agents", "claude-code"}
	res := runGo(t, home, args...)
	if res.ExitCode != 0 {
		t.Fatalf("go add mcp: %s", res.Stderr)
	}

	compareAgainstGolden(t, "add mcp (remote)", home+"/aikito/mcps", home, "add_mcp_remote")
}

func TestE2EAddMCPStdio(t *testing.T) {
	home := initWorkspace(t)

	args := []string{"add", "mcp", "files", "--transport", "stdio", "--command", "npx", "--agents", "claude-code"}
	res := runGo(t, home, args...)
	if res.ExitCode != 0 {
		t.Fatalf("go add mcp: %s", res.Stderr)
	}

	compareAgainstGolden(t, "add mcp (stdio)", home+"/aikito/mcps", home, "add_mcp_stdio")
}
