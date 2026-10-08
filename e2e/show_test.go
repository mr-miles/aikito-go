//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

func TestE2EShowSkillsList(t *testing.T) {
	goHome, pyHome, pythonSrc := initBothWorkspaces(t)

	if r := runGo(t, goHome, "add", "skill", "demo", "--description", "Demo skill"); r.ExitCode != 0 {
		t.Fatalf("go add skill: %s", r.Stderr)
	}
	if r := runPython(t, pythonSrc, pyHome, "add", "skill", "demo", "--description", "Demo skill"); r.ExitCode != 0 {
		t.Fatalf("python add skill: %s", r.Stderr)
	}

	goRes := runGo(t, goHome, "show", "skills")
	pyRes := runPython(t, pythonSrc, pyHome, "show", "skills")
	if goRes.ExitCode != 0 || pyRes.ExitCode != 0 {
		t.Fatalf("show skills failed: go=%d py=%d", goRes.ExitCode, pyRes.ExitCode)
	}
	if !strings.Contains(goRes.Stdout, "demo") {
		t.Errorf("go show skills missing 'demo': %s", goRes.Stdout)
	}
	if !strings.Contains(pyRes.Stdout, "demo") {
		t.Errorf("python show skills missing 'demo': %s", pyRes.Stdout)
	}
}

func TestE2EShowMCPList(t *testing.T) {
	goHome, pyHome, pythonSrc := initBothWorkspaces(t)

	args := []string{"add", "mcp", "weather", "--transport", "remote", "--url", "https://weather.example.com/mcp", "--agents", "claude-code"}
	if r := runGo(t, goHome, args...); r.ExitCode != 0 {
		t.Fatalf("go add mcp: %s", r.Stderr)
	}
	if r := runPython(t, pythonSrc, pyHome, args...); r.ExitCode != 0 {
		t.Fatalf("python add mcp: %s", r.Stderr)
	}

	goRes := runGo(t, goHome, "show", "mcps")
	pyRes := runPython(t, pythonSrc, pyHome, "show", "mcps")
	if goRes.ExitCode != 0 || pyRes.ExitCode != 0 {
		t.Fatalf("show mcps failed: go=%d py=%d", goRes.ExitCode, pyRes.ExitCode)
	}
	if !strings.Contains(goRes.Stdout, "weather") {
		t.Errorf("go show mcps missing 'weather': %s", goRes.Stdout)
	}
	if !strings.Contains(pyRes.Stdout, "weather") {
		t.Errorf("python show mcps missing 'weather': %s", pyRes.Stdout)
	}
}
