//go:build e2e

// These two tests only ever asserted an independent, self-contained
// property of each tool's own output ("does 'show skills' mention the
// skill I just added"), never a Go-vs-Python comparison — so, per this
// refactor, they need no golden fixture at all; they're pure Go-binary
// behavioral checks.
package e2e

import (
	"strings"
	"testing"
)

func TestE2EShowSkillsList(t *testing.T) {
	home := initWorkspace(t)

	if r := runGo(t, home, "add", "skill", "demo", "--description", "Demo skill"); r.ExitCode != 0 {
		t.Fatalf("go add skill: %s", r.Stderr)
	}

	res := runGo(t, home, "show", "skills")
	if res.ExitCode != 0 {
		t.Fatalf("go show skills failed (exit %d): %s", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "demo") {
		t.Errorf("go show skills missing 'demo': %s", res.Stdout)
	}
}

func TestE2EShowMCPList(t *testing.T) {
	home := initWorkspace(t)

	args := []string{"add", "mcp", "weather", "--transport", "remote", "--url", "https://weather.example.com/mcp", "--agents", "claude-code"}
	if r := runGo(t, home, args...); r.ExitCode != 0 {
		t.Fatalf("go add mcp: %s", r.Stderr)
	}

	res := runGo(t, home, "show", "mcps")
	if res.ExitCode != 0 {
		t.Fatalf("go show mcps failed (exit %d): %s", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "weather") {
		t.Errorf("go show mcps missing 'weather': %s", res.Stdout)
	}
}
