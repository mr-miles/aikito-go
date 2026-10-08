//go:build e2e

package e2e

import "testing"

func initBothWorkspaces(t *testing.T) (goHome, pyHome, pythonSrc string) {
	t.Helper()
	pythonSrc = requirePython(t)
	goHome = t.TempDir()
	pyHome = t.TempDir()
	withMarkerDir(t, goHome, ".claude")
	withMarkerDir(t, pyHome, ".claude")
	if r := runGo(t, goHome, "init", "workspace"); r.ExitCode != 0 {
		t.Fatalf("go init workspace: %s", r.Stderr)
	}
	if r := runPython(t, pythonSrc, pyHome, "init", "workspace"); r.ExitCode != 0 {
		t.Fatalf("python init workspace: %s", r.Stderr)
	}
	return goHome, pyHome, pythonSrc
}

func TestE2EAddSkill(t *testing.T) {
	goHome, pyHome, pythonSrc := initBothWorkspaces(t)

	goRes := runGo(t, goHome, "add", "skill", "demo", "--description", "Demo skill")
	if goRes.ExitCode != 0 {
		t.Fatalf("go add skill: %s", goRes.Stderr)
	}
	pyRes := runPython(t, pythonSrc, pyHome, "add", "skill", "demo", "--description", "Demo skill")
	if pyRes.ExitCode != 0 {
		t.Fatalf("python add skill: %s", pyRes.Stderr)
	}

	compareTrees(t, "add skill", goHome, goHome+"/aikito/skills/demo", pyHome, pyHome+"/aikito/skills/demo")
	compareTrees(t, "add skill (skills.toml)", goHome, goHome+"/aikito/skills.toml", pyHome, pyHome+"/aikito/skills.toml")
}

func TestE2EAddSubagent(t *testing.T) {
	goHome, pyHome, pythonSrc := initBothWorkspaces(t)

	goRes := runGo(t, goHome, "add", "subagent", "reviewer", "--description", "Reviews code", "--agents", "claude-code")
	if goRes.ExitCode != 0 {
		t.Fatalf("go add subagent: %s", goRes.Stderr)
	}
	pyRes := runPython(t, pythonSrc, pyHome, "add", "subagent", "reviewer", "--description", "Reviews code", "--agents", "claude-code")
	if pyRes.ExitCode != 0 {
		t.Fatalf("python add subagent: %s", pyRes.Stderr)
	}

	compareTrees(t, "add subagent", goHome, goHome+"/aikito/subagents", pyHome, pyHome+"/aikito/subagents")
}

func TestE2EAddMCPRemote(t *testing.T) {
	goHome, pyHome, pythonSrc := initBothWorkspaces(t)

	args := []string{"add", "mcp", "weather", "--transport", "remote", "--url", "https://weather.example.com/mcp", "--agents", "claude-code"}
	goRes := runGo(t, goHome, args...)
	if goRes.ExitCode != 0 {
		t.Fatalf("go add mcp: %s", goRes.Stderr)
	}
	pyRes := runPython(t, pythonSrc, pyHome, args...)
	if pyRes.ExitCode != 0 {
		t.Fatalf("python add mcp: %s", pyRes.Stderr)
	}

	compareTrees(t, "add mcp (remote)", goHome, goHome+"/aikito/mcps", pyHome, pyHome+"/aikito/mcps")
}

func TestE2EAddMCPStdio(t *testing.T) {
	goHome, pyHome, pythonSrc := initBothWorkspaces(t)

	args := []string{"add", "mcp", "files", "--transport", "stdio", "--command", "npx", "--agents", "claude-code"}
	goRes := runGo(t, goHome, args...)
	if goRes.ExitCode != 0 {
		t.Fatalf("go add mcp: %s", goRes.Stderr)
	}
	pyRes := runPython(t, pythonSrc, pyHome, args...)
	if pyRes.ExitCode != 0 {
		t.Fatalf("python add mcp: %s", pyRes.Stderr)
	}

	compareTrees(t, "add mcp (stdio)", goHome, goHome+"/aikito/mcps", pyHome, pyHome+"/aikito/mcps")
}
