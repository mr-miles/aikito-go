//go:build e2e || e2e_generate

// Shared helpers for both the e2e test suite (tag "e2e": builds and runs
// only the Go binary, comparing it against committed golden fixtures) and
// the golden-fixture generator (tag "e2e_generate": runs the reference
// Python CLI and writes testdata/<name>/ fixtures from its real output).
// See e2e/README.md for how the two fit together.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// testPATH is deterministic: a real git binary (init workspace needs it)
// but no agent CLIs, matching internal/cli/sync_test.go's established
// convention for reproducible agent-detection in tests.
const testPATH = "/usr/bin:/bin"

type runResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
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

// pythonSrcDir resolves the reference Python implementation's src/
// directory: AIKITO_PYTHON_SRC if set, else the sibling checkout this
// sandbox and local dev both have (<module-root>/../aikito/src). Only used
// by the golden generator (tag e2e_generate) — the normal e2e test run
// (tag e2e) never needs Python at all.
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

// requirePython resolves the reference Python checkout, or skips the
// calling test/fails with a clear message. Only called from the generator.
func requirePython(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not found on PATH; cannot regenerate golden fixtures")
	}
	src, ok := pythonSrcDir()
	if !ok {
		t.Skip("reference Python implementation not found (set AIKITO_PYTHON_SRC, or checkout a sibling 'aikito' repo next to this one)")
	}
	return src
}

func runPython(t *testing.T, pythonSrc, home string, args ...string) runResult {
	t.Helper()
	pyArgs := append([]string{"-m", "aikito"}, args...)
	return runBinary(t, "python3", home, []string{"PYTHONPATH=" + pythonSrc}, pyArgs...)
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

// --- directory tree snapshot ---

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
// object/index content is not expected to be byte-stable across
// independent git-init calls (timestamps, object compression).
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
		// A directory root isn't itself a meaningful tree entry (its
		// presence is implied by walking into its contents, same as every
		// non-root directory below). A FILE or SYMLINK root, however, must
		// still be recorded — treeManifest is routinely called with root
		// pointing directly at a single file/symlink (e.g. comparing just
		// ".claude/CLAUDE.md"), and unconditionally skipping "." would make
		// that whole comparison vacuously empty. Found and fixed during
		// this refactor: the previous version unconditionally skipped ".",
		// which meant every single-file compareTrees call in the old
		// dual-process suite was comparing two empty manifests — a latent,
		// always-passing no-op that never actually checked anything.
		if rel == "." && d.IsDir() {
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
// to home, so trees built under different temp directories (an e2e run's
// live $HOME vs. whatever $HOME the golden was captured under) can still
// be compared structurally.
func normalizeTarget(target, home string) string {
	if filepath.IsAbs(target) {
		if rel, err := filepath.Rel(home, target); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(target)
}

func redactContent(content []byte, extraPaths []string) []byte {
	if len(extraPaths) == 0 {
		return content
	}
	s := string(content)
	for i, p := range extraPaths {
		s = strings.ReplaceAll(s, p, fmt.Sprintf("<REDACTED_PATH_%d>", i))
	}
	return []byte(s)
}

func redactManifest(tree map[string]treeEntry, extraPaths []string) map[string]treeEntry {
	if len(extraPaths) == 0 {
		return tree
	}
	out := make(map[string]treeEntry, len(tree))
	for k, e := range tree {
		if !e.isDir && !e.isSymlink {
			e.content = redactContent(e.content, extraPaths)
		}
		out[k] = e
	}
	return out
}

func compareManifests(t *testing.T, label string, liveTree, goldenTree map[string]treeEntry) {
	t.Helper()

	var paths []string
	seen := map[string]bool{}
	for p := range liveTree {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	for p := range goldenTree {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)

	for _, p := range paths {
		le, lok := liveTree[p]
		ge, gok := goldenTree[p]
		if lok != gok {
			t.Errorf("%s: path %q present in Go=%v, golden(Python)=%v", label, p, lok, gok)
			continue
		}
		if le.isDir != ge.isDir || le.isSymlink != ge.isSymlink {
			t.Errorf("%s: path %q kind mismatch: go(dir=%v,symlink=%v) vs golden(dir=%v,symlink=%v)",
				label, p, le.isDir, le.isSymlink, ge.isDir, ge.isSymlink)
			continue
		}
		if le.isSymlink && le.target != ge.target {
			t.Errorf("%s: path %q symlink target mismatch: go=%q golden=%q", label, p, le.target, ge.target)
		}
		if !le.isDir && !le.isSymlink && !bytes.Equal(le.content, ge.content) {
			t.Errorf("%s: path %q content mismatch:\n--- go ---\n%s\n--- golden (python) ---\n%s", label, p, le.content, ge.content)
		}
	}
}

// --- golden fixture persistence (testdata/<name>/manifest.json + files/) ---

type goldenEntryJSON struct {
	Kind   string `json:"kind"` // "file" | "dir" | "symlink"
	Target string `json:"target,omitempty"`
}

func goldenPath(name string) string {
	return filepath.Join("testdata", name)
}

// goldenFileStoragePath maps a manifest path to where its content lives
// under testdata/<name>/files/. "." (the manifest key used when the
// compared root is itself a single file/symlink, not a directory — see
// treeManifest) can't be passed through filepath.Join as-is: Join cleans
// a trailing "." component away, so filepath.Join(dir, "files", ".")
// silently collapses to dir/files itself, turning the files/ directory
// into a plain file on disk instead of a directory containing one. Use an
// explicit, reviewable filename for that case instead.
func goldenFileStoragePath(dir, relPath string) string {
	if relPath == "." {
		return filepath.Join(dir, "files", "_root_")
	}
	return filepath.Join(dir, "files", filepath.FromSlash(relPath))
}

// saveGolden writes tree as a golden fixture under testdata/<name>/,
// replacing any existing fixture of the same name. Only used by the
// generator (tag e2e_generate).
func saveGolden(t *testing.T, name string, tree map[string]treeEntry) {
	t.Helper()
	dir := goldenPath(name)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]goldenEntryJSON{}
	for path, e := range tree {
		switch {
		case e.isDir:
			manifest[path] = goldenEntryJSON{Kind: "dir"}
		case e.isSymlink:
			manifest[path] = goldenEntryJSON{Kind: "symlink", Target: e.target}
		default:
			manifest[path] = goldenEntryJSON{Kind: "file"}
			full := goldenFileStoragePath(dir, path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, e.content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// loadGolden reads a previously-saved golden fixture back into the same
// map[string]treeEntry shape treeManifest produces, so compareManifests
// can compare a live Go-produced tree against it directly.
func loadGolden(t *testing.T, name string) map[string]treeEntry {
	t.Helper()
	dir := goldenPath(name)
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("loading golden %q: %v (did you run the generator? see e2e/README.md)", name, err)
	}
	var manifest map[string]goldenEntryJSON
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parsing golden %q manifest: %v", name, err)
	}
	tree := map[string]treeEntry{}
	for path, e := range manifest {
		switch e.Kind {
		case "dir":
			tree[path] = treeEntry{isDir: true}
		case "symlink":
			tree[path] = treeEntry{isSymlink: true, target: e.Target}
		case "file":
			content, rerr := os.ReadFile(goldenFileStoragePath(dir, path))
			if rerr != nil {
				t.Fatalf("golden %q: reading file %q: %v", name, path, rerr)
			}
			tree[path] = treeEntry{content: content}
		default:
			t.Fatalf("golden %q: path %q has unknown kind %q", name, path, e.Kind)
		}
	}
	return tree
}

// loadGoldenSingleFile reads one file's raw bytes out of a golden fixture
// saved as a single-entry tree (saveGolden(t, name, map[string]treeEntry{key: {content: ...}})),
// for scenarios that compare decoded/parsed content rather than a whole
// directory tree (see adopt_test.go).
func loadGoldenSingleFile(t *testing.T, name, key string) []byte {
	t.Helper()
	tree := loadGolden(t, name)
	entry, ok := tree[key]
	if !ok {
		t.Fatalf("golden %q has no entry %q", name, key)
	}
	return entry.content
}

// compareAgainstGolden walks liveRoot (a live Go-produced directory, with
// symlink targets normalized relative to liveHome) and asserts it matches
// the committed golden fixture goldenName byte-for-byte/target-for-target.
func compareAgainstGolden(t *testing.T, label, liveRoot, liveHome, goldenName string) {
	t.Helper()
	compareAgainstGoldenRedacting(t, label, liveRoot, liveHome, goldenName, nil)
}

// compareAgainstGoldenRedacting is compareAgainstGolden, but first replaces
// every occurrence of liveExtraPaths[i] in FILE CONTENT (not path names)
// with the same "<REDACTED_PATH_i>" placeholder the generator already
// baked into the golden fixture, for content that legitimately contains an
// absolute path that differs run-to-run for reasons unrelated to tool
// behavior (e.g. a registered project's on-disk checkout directory).
func compareAgainstGoldenRedacting(t *testing.T, label, liveRoot, liveHome, goldenName string, liveExtraPaths []string) {
	t.Helper()
	liveTree := redactManifest(treeManifest(t, liveRoot, liveHome), liveExtraPaths)
	goldenTree := loadGolden(t, goldenName)
	compareManifests(t, label, liveTree, goldenTree)
}

// resolvedTempDir is t.TempDir() with symlinks resolved, so expected paths
// match what aikito writes on macOS, where temp dirs live under
// /var -> /private/var and the binary resolves paths as Python's
// Path.resolve() does.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// outputGolden captures a command's exit code, stdout and stderr as a
// single-file golden ("output.txt"), with home replaced by "H" so captures
// from different temp homes compare equal.
func outputGolden(r runResult, home string) map[string]treeEntry {
	text := fmt.Sprintf("exit %d\n--- stdout\n%s--- stderr\n%s", r.ExitCode,
		strings.ReplaceAll(r.Stdout, home, "H"), strings.ReplaceAll(r.Stderr, home, "H"))
	return map[string]treeEntry{"output.txt": {content: []byte(text)}}
}

// writePrepopulatedClaudeSkills leaves a hand-made skill in a real
// ~/.claude/skills directory, which `sync global` must refuse to replace.
func writePrepopulatedClaudeSkills(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "skills", "mine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
