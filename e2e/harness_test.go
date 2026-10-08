//go:build e2e

// Package e2e runs real scenarios against the compiled Go aikito binary and
// asserts its output/resulting file trees match committed golden fixtures
// captured from the reference Python implementation (see e2e/README.md).
//
// Excluded from the default `go test ./...` via the e2e build tag: these
// tests build and run a real binary and are slower than unit tests. Run
// explicitly with `go test -tags e2e ./e2e/...`. Unlike an earlier version
// of this suite, running these tests does NOT require python3 or a
// reference checkout at all — only regenerating the golden fixtures does
// (tag e2e_generate, a separate, manual/occasional step).
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

var binPath string

// TestMain builds the real aikito Go binary once for the whole package.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "aikito-e2e-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: mkdtemp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	binPath = filepath.Join(dir, "aikito")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	// e2e/ sits directly under the module root.
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/aikito")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: go build ./cmd/aikito failed: %v\n%s\n", err, out)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func runGo(t *testing.T, home string, args ...string) runResult {
	t.Helper()
	return runBinary(t, binPath, home, nil, args...)
}
