//go:build e2e

package e2e

import "testing"

func TestE2EInitWorkspace(t *testing.T) {
	pythonSrc := requirePython(t)

	goHome := t.TempDir()
	pyHome := t.TempDir()
	withMarkerDir(t, goHome, ".claude")
	withMarkerDir(t, pyHome, ".claude")

	goRes := runGo(t, goHome, "init", "workspace")
	if goRes.ExitCode != 0 {
		t.Fatalf("go init workspace failed (exit %d): %s", goRes.ExitCode, goRes.Stderr)
	}
	pyRes := runPython(t, pythonSrc, pyHome, "init", "workspace")
	if pyRes.ExitCode != 0 {
		t.Fatalf("python init workspace failed (exit %d): %s", pyRes.ExitCode, pyRes.Stderr)
	}

	compareTrees(t, "init workspace", goHome, goHome+"/aikito", pyHome, pyHome+"/aikito")
}

func TestE2EInitProject(t *testing.T) {
	pythonSrc := requirePython(t)

	goHome := t.TempDir()
	pyHome := t.TempDir()
	goProj := t.TempDir()
	pyProj := t.TempDir()

	if r := runGo(t, goHome, "init", "workspace"); r.ExitCode != 0 {
		t.Fatalf("go init workspace: %s", r.Stderr)
	}
	if r := runPython(t, pythonSrc, pyHome, "init", "workspace"); r.ExitCode != 0 {
		t.Fatalf("python init workspace: %s", r.Stderr)
	}

	goRes := runGo(t, goHome, "init", "project", "myproj", goProj)
	if goRes.ExitCode != 0 {
		t.Fatalf("go init project failed (exit %d): %s", goRes.ExitCode, goRes.Stderr)
	}
	pyRes := runPython(t, pythonSrc, pyHome, "init", "project", "myproj", pyProj)
	if pyRes.ExitCode != 0 {
		t.Fatalf("python init project failed (exit %d): %s", pyRes.ExitCode, pyRes.Stderr)
	}

	compareTreesRedacting(t, "init project (workspace side)",
		goHome, goHome+"/aikito/projects/myproj", pyHome, pyHome+"/aikito/projects/myproj",
		[]string{goProj}, []string{pyProj})
}
