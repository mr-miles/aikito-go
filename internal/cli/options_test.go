package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// options_vectors.json is generated from the reference CLI by
// testdata/gen_options_vectors.py: options the port used to reject as
// unimplemented (add skill --project/--global/--sync, add mcp --from/--sync,
// add subagent --from, rm skill --project, current-project detection in
// maintain memory, edit instructions and diff project).
func TestOptionsMatchPython(t *testing.T) {
	replayCLIVectors(t, "testdata/options_vectors.json")
}

// workspaceTree mirrors gen_options_vectors.py's wtree(): the workspace
// minus .git, with each file's content (bundled skills listed only).
func workspaceTree(t *testing.T, home string) []string {
	t.Helper()
	root := filepath.Join(home, "aikito")
	var lines []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(home, p)
		rel = filepath.ToSlash(rel)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			lines = append(lines, fmt.Sprintf("L %s -> %s", rel, normalizeHome(target, home)))
		case info.IsDir():
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			lines = append(lines, "D "+rel)
		case strings.HasPrefix(rel, "aikito/skills/aikito/") || strings.HasPrefix(rel, "aikito/skills/durable-memory/"):
			lines = append(lines, "F "+rel)
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			lines = append(lines, fmt.Sprintf("F %s %s", rel, pyJSONString(normalizeHome(string(data), home))))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return lines
}

// pyJSONString is json.dumps(s, ensure_ascii=False) for a string.
func pyJSONString(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	out := strings.TrimSuffix(b.String(), "\n")
	return strings.NewReplacer(` `, " ", ` `, " ").Replace(out)
}
