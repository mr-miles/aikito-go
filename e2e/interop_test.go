//go:build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func goCLIIn(t *testing.T, home, dir string, args ...string) runResult {
	t.Helper()
	return runBinaryIn(t, binPath, home, dir, ioEnv, args...)
}

// TestInteropGoOnPythonState restores each home Python built and runs the
// scenario's commands with the Go binary: every command's output and exit
// status, and the resulting home, must match what Python itself did on the
// same state.
func TestInteropGoOnPythonState(t *testing.T) {
	skipInteropOnWindows(t)
	for _, sc := range ioScenarios {
		t.Run(sc.name, func(t *testing.T) {
			state := ioLoad(t, sc.name, "python_state")
			text, after := ioRunCommands(t, sc, state, goCLIIn)
			ioCompareText(t, "Go on Python-built state", text, ioLoadText(t, sc.name, "python_on_python.txt"))
			ioCompareSnapshots(t, "home after Go's commands vs after Python's", after, ioLoad(t, sc.name, "python_on_python_after"))
			if sc.clean {
				ioCompareSnapshots(t, "clean state changed by Go's syncs", after, state)
			}
		})
	}
}

// TestInteropGoWritesPythonState builds each scenario with the Go binary and
// requires the same transcript and the same home, byte for byte, as Python
// built. It also checks the committed record of Python running on a
// Go-built home: Python must see exactly what it sees on its own state.
func TestInteropGoWritesPythonState(t *testing.T) {
	skipInteropOnWindows(t)
	for _, sc := range ioScenarios {
		t.Run(sc.name, func(t *testing.T) {
			state, build := ioRunScenarioBuild(t, sc, goCLIIn, goFaultCLI(sc))
			ioCompareText(t, "Go build transcript vs Python's", build, ioLoadText(t, sc.name, "python_build.txt"))
			ioCompareSnapshots(t, "Go-built home vs Python-built home", state, ioLoad(t, sc.name, "python_state"))
			// The recorded Python-on-Go run is only meaningful while it was
			// captured from what Go builds today.
			ioCompareSnapshots(t, "Go-built home vs recorded go_state (regenerate the interop fixtures)", state, ioLoad(t, sc.name, "go_state"))
			ioCompareText(t, "Python on Go-built state vs Python on its own", ioLoadText(t, sc.name, "python_on_go.txt"), ioLoadText(t, sc.name, "python_on_python.txt"))
			ioCompareSnapshots(t, "home after Python on Go state vs on its own", ioLoad(t, sc.name, "python_on_go_after"), ioLoad(t, sc.name, "python_on_python_after"))
		})
	}
}

// skipInteropOnWindows: the snapshots were captured on POSIX and compare
// file modes and symlinks literally.
func skipInteropOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("interop snapshots compare POSIX modes and symlinks")
	}
}

var (
	faultBinOnce sync.Once
	faultBin     string
	faultBinErr  error
)

// goFaultCLI runs a binary built with the aikito_faultinject tag, which
// exits at sc.fault (see internal/faultinject).
func goFaultCLI(sc ioScenario) psCLI {
	return func(t *testing.T, home, dir string, args ...string) runResult {
		t.Helper()
		faultBinOnce.Do(func() {
			faultBin = filepath.Join(filepath.Dir(binPath), "aikito-fault")
			cmd := exec.Command("go", "build", "-tags", "aikito_faultinject", "-o", faultBin, "./cmd/aikito")
			cmd.Dir = ".."
			if out, err := cmd.CombinedOutput(); err != nil {
				faultBinErr = fmt.Errorf("%v\n%s", err, out)
			}
		})
		if faultBinErr != nil {
			t.Fatalf("building fault-injection binary: %v", faultBinErr)
		}
		return runBinaryIn(t, faultBin, home, dir, ioFaultEnv(sc), args...)
	}
}

func ioCompareText(t *testing.T, label, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl {
			lo := i - 8
			if lo < 0 {
				lo = 0
			}
			hi := i + 8
			t.Errorf("%s: first difference at line %d\n got: %q\nwant: %q\ncontext (want):\n%s", label, i+1, gl, wl,
				strings.Join(w[lo:min(hi, len(w))], "\n"))
			return
		}
	}
}

func ioCompareSnapshots(t *testing.T, label string, got, want ioSnapshot) {
	t.Helper()
	if d := ioDiff(got, want); len(d) > 0 {
		if len(d) > 20 {
			d = append(d[:20], "...")
		}
		t.Errorf("%s:\n%s", label, strings.Join(d, "\n"))
	}
}
