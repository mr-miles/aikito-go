//go:build e2e_generate

package e2e

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGenerateInteropGoldens captures, for every interop scenario:
//
//	python_state/    the home Python builds
//	python_on_python.txt + python_on_python_after/
//	                 Python's commands on that state (restored elsewhere)
//	go_state/        the home the Go binary builds from the same steps
//	python_on_go.txt + python_on_go_after/
//	                 Python's commands on the Go-built state
//
//	go test -tags e2e_generate -run TestGenerateInteropGoldens ./e2e/... -v
func TestGenerateInteropGoldens(t *testing.T) {
	pythonSrc := requirePython(t)
	py := func(t *testing.T, home, dir string, args ...string) runResult {
		t.Helper()
		return runBinaryIn(t, "python3", home, dir, append([]string{"PYTHONPATH=" + pythonSrc}, ioEnv...),
			append([]string{"-m", "aikito"}, args...)...)
	}
	goBin := filepath.Join(t.TempDir(), "aikito")
	build := exec.Command("go", "build", "-o", goBin, "./cmd/aikito")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	goCLI := func(t *testing.T, home, dir string, args ...string) runResult {
		t.Helper()
		return runBinaryIn(t, goBin, home, dir, ioEnv, args...)
	}
	for _, sc := range ioScenarios {
		t.Run(sc.name, func(t *testing.T) {
			pyState, pyBuild := ioRunScenarioBuild(t, sc, py)
			ioSave(t, pyState, sc.name, "python_state")
			ioSaveText(t, sc.name, "python_build.txt", pyBuild)
			text, after := ioRunCommands(t, sc, pyState, py)
			ioSaveText(t, sc.name, "python_on_python.txt", text)
			ioSave(t, after, sc.name, "python_on_python_after")

			goState, goBuild := ioRunScenarioBuild(t, sc, goCLI)
			ioSave(t, goState, sc.name, "go_state")
			ioSaveText(t, sc.name, "go_build.txt", goBuild)
			text, after = ioRunCommands(t, sc, goState, py)
			ioSaveText(t, sc.name, "python_on_go.txt", text)
			ioSave(t, after, sc.name, "python_on_go_after")
		})
	}
}
