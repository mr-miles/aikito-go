//go:build e2e

package e2e

import (
	"os"
	"testing"
)

func TestE2ERmSkill(t *testing.T) {
	home := initWorkspace(t)

	if r := runGo(t, home, "add", "skill", "demo", "--description", "Demo skill"); r.ExitCode != 0 {
		t.Fatalf("go add skill: %s", r.Stderr)
	}

	res := runGo(t, home, "rm", "skill", "demo")
	if res.ExitCode != 0 {
		t.Fatalf("go rm skill failed: %s\n%s", res.Stdout, res.Stderr)
	}

	if _, err := os.Stat(home + "/aikito/skills/demo"); !os.IsNotExist(err) {
		t.Errorf("go: expected skills/demo to be gone, stat error = %v", err)
	}
	compareAgainstGolden(t, "rm skill (skills.toml)", home+"/aikito/skills.toml", home, "rm_skill_skills_toml")
}

func TestE2ERmMCPWithSync(t *testing.T) {
	home := initWorkspace(t)

	args := []string{"add", "mcp", "weather", "--transport", "remote", "--url", "https://weather.example.com/mcp", "--agents", "claude-code"}
	if r := runGo(t, home, args...); r.ExitCode != 0 {
		t.Fatalf("go add mcp: %s", r.Stderr)
	}
	if r := runGo(t, home, "sync", "mcp"); r.ExitCode != 0 {
		t.Fatalf("go sync mcp: %s", r.Stdout)
	}

	res := runGo(t, home, "rm", "mcp", "weather", "--sync")
	if res.ExitCode != 0 {
		t.Fatalf("go rm mcp --sync failed: %s\n%s", res.Stdout, res.Stderr)
	}

	if _, err := os.Stat(home + "/aikito/mcps/weather.toml"); !os.IsNotExist(err) {
		t.Errorf("go: expected mcps/weather.toml to be gone")
	}
	compareAgainstGolden(t, "rm mcp --sync (.claude.json)", home+"/.claude.json", home, "rm_mcp_sync_claude_json")
}
