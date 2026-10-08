//go:build e2e

package e2e

import (
	"os"
	"testing"
)

func TestE2ERmSkill(t *testing.T) {
	goHome, pyHome, pythonSrc := initBothWorkspaces(t)

	if r := runGo(t, goHome, "add", "skill", "demo", "--description", "Demo skill"); r.ExitCode != 0 {
		t.Fatalf("go add skill: %s", r.Stderr)
	}
	if r := runPython(t, pythonSrc, pyHome, "add", "skill", "demo", "--description", "Demo skill"); r.ExitCode != 0 {
		t.Fatalf("python add skill: %s", r.Stderr)
	}

	goRes := runGo(t, goHome, "rm", "skill", "demo")
	if goRes.ExitCode != 0 {
		t.Fatalf("go rm skill failed: %s\n%s", goRes.Stdout, goRes.Stderr)
	}
	pyRes := runPython(t, pythonSrc, pyHome, "rm", "skill", "demo")
	if pyRes.ExitCode != 0 {
		t.Fatalf("python rm skill failed: %s\n%s", pyRes.Stdout, pyRes.Stderr)
	}

	if _, err := os.Stat(goHome + "/aikito/skills/demo"); !os.IsNotExist(err) {
		t.Errorf("go: expected skills/demo to be gone, stat error = %v", err)
	}
	if _, err := os.Stat(pyHome + "/aikito/skills/demo"); !os.IsNotExist(err) {
		t.Errorf("python: expected skills/demo to be gone, stat error = %v", err)
	}
	compareTrees(t, "rm skill (skills.toml)", goHome, goHome+"/aikito/skills.toml", pyHome, pyHome+"/aikito/skills.toml")
}

func TestE2ERmMCPWithSync(t *testing.T) {
	goHome, pyHome, pythonSrc := initBothWorkspaces(t)

	args := []string{"add", "mcp", "weather", "--transport", "remote", "--url", "https://weather.example.com/mcp", "--agents", "claude-code"}
	if r := runGo(t, goHome, args...); r.ExitCode != 0 {
		t.Fatalf("go add mcp: %s", r.Stderr)
	}
	if r := runPython(t, pythonSrc, pyHome, args...); r.ExitCode != 0 {
		t.Fatalf("python add mcp: %s", r.Stderr)
	}
	if r := runGo(t, goHome, "sync", "mcp"); r.ExitCode != 0 {
		t.Fatalf("go sync mcp: %s", r.Stdout)
	}
	if r := runPython(t, pythonSrc, pyHome, "sync", "mcp"); r.ExitCode != 0 {
		t.Fatalf("python sync mcp: %s", r.Stdout)
	}

	goRes := runGo(t, goHome, "rm", "mcp", "weather", "--sync")
	if goRes.ExitCode != 0 {
		t.Fatalf("go rm mcp --sync failed: %s\n%s", goRes.Stdout, goRes.Stderr)
	}
	pyRes := runPython(t, pythonSrc, pyHome, "rm", "mcp", "weather", "--sync")
	if pyRes.ExitCode != 0 {
		t.Fatalf("python rm mcp --sync failed: %s\n%s", pyRes.Stdout, pyRes.Stderr)
	}

	if _, err := os.Stat(goHome + "/aikito/mcps/weather.toml"); !os.IsNotExist(err) {
		t.Errorf("go: expected mcps/weather.toml to be gone")
	}
	if _, err := os.Stat(pyHome + "/aikito/mcps/weather.toml"); !os.IsNotExist(err) {
		t.Errorf("python: expected mcps/weather.toml to be gone")
	}
	compareTrees(t, "rm mcp --sync (.claude.json)", goHome, goHome+"/.claude.json", pyHome, pyHome+"/.claude.json")
}
