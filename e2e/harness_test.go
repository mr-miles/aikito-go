//go:build e2e

// Package e2e runs real scenarios against BOTH the compiled Go aikito
// binary and the reference Python implementation, and asserts they agree.
// This replaces the ad-hoc "run both by hand once, diff, throw the diff
// away" validation every prior fork in this port's history did during
// development: those confirmations are real, but they vanish the moment
// the shell history scrolls past them. This package keeps them.
//
// Excluded from the default `go test ./...` via the e2e build tag: these
// tests shell out to a real python3 interpreter and a reference Python
// checkout, are slower than unit tests, and must not fail a normal build
// when Python isn't available. Run explicitly with `go test -tags e2e ./e2e/...`.
package e2e

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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

// pythonSrcDir resolves the reference Python implementation's src/
// directory: AIKITO_PYTHON_SRC if set, else the sibling checkout this
// sandbox and local dev both have (<module-root>/../aikito/src).
func pythonSrcDir() (string, bool) {
	if v := os.Getenv("AIKITO_PYTHON_SRC"); v != "" {
		if _, err := os.Stat(filepath.Join(v, "aikito", "__main__.py")); err == nil {
			return v, true
		}
	}
	moduleRoot, err := filepath.Abs("..")
	if err != nil {
		return "", false
	}
	candidate := filepath.Join(moduleRoot, "..", "aikito", "src")
	if _, err := os.Stat(filepath.Join(candidate, "aikito", "__main__.py")); err == nil {
		return candidate, true
	}
	return "", false
}

// requirePython skips the calling test (not the whole suite) when python3
// or the reference checkout isn't available, rather than failing.
func requirePython(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not found on PATH; skipping e2e-vs-Python comparison")
	}
	src, ok := pythonSrcDir()
	if !ok {
		t.Skip("reference Python implementation not found (set AIKITO_PYTHON_SRC, or checkout a sibling 'aikito' repo next to this one); skipping e2e-vs-Python comparison")
	}
	return src
}

type runResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// testPATH is deterministic: a real git binary (init workspace needs it)
// plus python3's own location, but no agent CLIs — matching
// internal/cli/sync_test.go's established convention for reproducible
// agent-detection in tests.
const testPATH = "/usr/bin:/bin"

func runGo(t *testing.T, home string, args ...string) runResult {
	t.Helper()
	return runBinary(t, binPath, home, nil, args...)
}

func runPython(t *testing.T, pythonSrc, home string, args ...string) runResult {
	t.Helper()
	pyArgs := append([]string{"-m", "aikito"}, args...)
	return runBinary(t, "python3", home, []string{"PYTHONPATH=" + pythonSrc}, pyArgs...)
}

func runBinary(t *testing.T, binary, home string, extraEnv []string, args ...string) runResult {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = append([]string{
		"HOME=" + home,
		"PATH=" + testPATH,
	}, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("failed to run %s %v: %v", binary, args, err)
		}
	}
	return runResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: code}
}

// withMarkerDir creates <home>/<dir>, mirroring the detect.paths marker
// directory tests rely on for deterministic agent availability (see
// internal/cli/sync_test.go's identical convention).
func withMarkerDir(t *testing.T, home, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
		t.Fatal(err)
	}
}

// --- directory tree comparison ---

type treeEntry struct {
	isDir     bool
	isSymlink bool
	target    string // normalized, relative to its own home root
	content   []byte
}

// treeManifest walks root and returns a relpath -> entry map, normalizing
// any symlink target relative to home (NOT root — a symlink inside the
// walked subtree routinely points outside it, e.g. a skill symlink under
// .claude/skills/ points back into <home>/aikito/skills/, so the "home"
// used for normalization must be the whole fixture root, not whatever
// subtree a particular comparison happens to be walking). The .git
// directory (created by `init workspace`) is excluded: its internal
// object/index content is not expected to match byte-for-byte between two
// independently-run git-init calls (timestamps, object compression), and
// comparing its presence alone isn't a meaningful cross-tool assertion.
func treeManifest(t *testing.T, root, home string) map[string]treeEntry {
	t.Helper()
	manifest := map[string]treeEntry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" || strings.HasPrefix(rel, ".git/") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, lerr := os.Readlink(path)
			if lerr != nil {
				return lerr
			}
			manifest[rel] = treeEntry{isSymlink: true, target: normalizeTarget(target, home)}
			return nil
		}
		if d.IsDir() {
			manifest[rel] = treeEntry{isDir: true}
			return nil
		}
		data, rerr2 := os.ReadFile(path)
		if rerr2 != nil {
			return rerr2
		}
		manifest[rel] = treeEntry{content: data}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return manifest
}

// normalizeTarget expresses an absolute symlink target as a path relative
// to home, so two trees built under different temp directories (one per
// tool invocation) can still be compared structurally.
func normalizeTarget(target, home string) string {
	if filepath.IsAbs(target) {
		if rel, err := filepath.Rel(home, target); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(target)
}

// compareTrees asserts goRoot and pyRoot (subtrees under goHome/pyHome
// respectively — pass the same value for root and home to compare a whole
// fixture root) contain the same relative paths, of the same kind
// (dir/file/symlink), with identical file content and identical
// (home-relative) symlink targets.
func compareTrees(t *testing.T, label, goHome, goRoot, pyHome, pyRoot string) {
	t.Helper()
	compareTreesRedacting(t, label, goHome, goRoot, pyHome, pyRoot, nil, nil)
}

// compareTreesRedacting is compareTrees, but first replaces every
// occurrence of goExtraPaths[i]/pyExtraPaths[i] in FILE CONTENT (not path
// names) with a shared placeholder before comparing. Use this when a
// scenario necessarily records an absolute path that legitimately differs
// between the two tool invocations for reasons unrelated to tool behavior
// — e.g. a registered project's on-disk checkout directory, which is a
// distinct t.TempDir() per side purely because the test harness runs each
// tool against its own fixture, not because the tools disagree about
// anything.
func compareTreesRedacting(t *testing.T, label, goHome, goRoot, pyHome, pyRoot string, goExtraPaths, pyExtraPaths []string) {
	t.Helper()
	redact := func(content []byte, extraPaths []string) []byte {
		s := string(content)
		for i, p := range extraPaths {
			s = strings.ReplaceAll(s, p, fmt.Sprintf("<REDACTED_PATH_%d>", i))
		}
		return []byte(s)
	}
	goTree := treeManifest(t, goRoot, goHome)
	pyTree := treeManifest(t, pyRoot, pyHome)
	for k, e := range goTree {
		if !e.isDir && !e.isSymlink {
			e.content = redact(e.content, goExtraPaths)
			goTree[k] = e
		}
	}
	for k, e := range pyTree {
		if !e.isDir && !e.isSymlink {
			e.content = redact(e.content, pyExtraPaths)
			pyTree[k] = e
		}
	}
	compareManifests(t, label, goTree, pyTree)
}

func compareManifests(t *testing.T, label string, goTree, pyTree map[string]treeEntry) {
	t.Helper()

	var paths []string
	seen := map[string]bool{}
	for p := range goTree {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	for p := range pyTree {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)

	for _, p := range paths {
		ge, gok := goTree[p]
		pe, pok := pyTree[p]
		if gok != pok {
			t.Errorf("%s: path %q present in Go=%v, Python=%v", label, p, gok, pok)
			continue
		}
		if ge.isDir != pe.isDir || ge.isSymlink != pe.isSymlink {
			t.Errorf("%s: path %q kind mismatch: go(dir=%v,symlink=%v) vs python(dir=%v,symlink=%v)",
				label, p, ge.isDir, ge.isSymlink, pe.isDir, pe.isSymlink)
			continue
		}
		if ge.isSymlink && ge.target != pe.target {
			t.Errorf("%s: path %q symlink target mismatch: go=%q python=%q", label, p, ge.target, pe.target)
		}
		if !ge.isDir && !ge.isSymlink && !bytes.Equal(ge.content, pe.content) {
			t.Errorf("%s: path %q content mismatch:\n--- go ---\n%s\n--- python ---\n%s", label, p, ge.content, pe.content)
		}
	}
}
