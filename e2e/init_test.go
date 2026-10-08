//go:build e2e

package e2e

import "testing"

func TestE2EInitWorkspace(t *testing.T) {
	home := resolvedTempDir(t)
	withMarkerDir(t, home, ".claude")

	res := runGo(t, home, "init", "workspace")
	if res.ExitCode != 0 {
		t.Fatalf("go init workspace failed (exit %d): %s", res.ExitCode, res.Stderr)
	}

	compareAgainstGolden(t, "init workspace", home+"/aikito", home, "init_workspace")
}

func TestE2EInitProject(t *testing.T) {
	home := resolvedTempDir(t)
	withMarkerDir(t, home, ".claude")
	proj := resolvedTempDir(t)

	if r := runGo(t, home, "init", "workspace"); r.ExitCode != 0 {
		t.Fatalf("go init workspace: %s", r.Stderr)
	}

	res := runGo(t, home, "init", "project", "myproj", proj)
	if res.ExitCode != 0 {
		t.Fatalf("go init project failed (exit %d): %s", res.ExitCode, res.Stderr)
	}

	compareAgainstGoldenRedacting(t, "init project (workspace side)",
		home+"/aikito/projects/myproj", home, "init_project", []string{proj})
}
