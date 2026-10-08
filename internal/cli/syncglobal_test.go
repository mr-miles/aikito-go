package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// syncglobal_vectors.json is generated from the reference CLI by
// testdata/gen_syncglobal_vectors.py: each scenario's setup steps, plus the
// stdout/stderr/exit of every `sync global` run and snapshots of the home
// tree. This replays the same steps against the Go CLI.

type syncGlobalStep struct {
	Op      string   `json:"op"`
	Path    string   `json:"path"`
	Content string   `json:"content"`
	Target  string   `json:"target"`
	Args    []string `json:"args"`
}

type syncGlobalExpect struct {
	Args    []string `json:"args"`
	Exit    *int     `json:"exit"`
	Stdout  string   `json:"stdout"`
	Stderr  string   `json:"stderr"`
	Tree    []string `json:"tree"`
	Read    string   `json:"read"`
	Content string   `json:"content"`
}

var backupTimestampRe = regexp.MustCompile(`bundled-skills_\d{8}_\d{6}_\d{6}`)

func normalizeHome(s, home string) string {
	return backupTimestampRe.ReplaceAllString(strings.ReplaceAll(s, home, "H"), "bundled-skills_TS")
}

// homeTree mirrors the generator's tree(): everything under home except the
// workspace, symlinks with their raw targets, modes only under ~/.local.
func homeTree(t *testing.T, home string) []string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == home {
			return nil
		}
		rel, _ := filepath.Rel(home, p)
		rel = backupTimestampRe.ReplaceAllString(filepath.ToSlash(rel), "bundled-skills_TS")
		if rel == "aikito" {
			return filepath.SkipDir
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		mode := ""
		if strings.HasPrefix(rel, ".local") {
			mode = fmt.Sprintf(" %o", info.Mode().Perm())
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			lines = append(lines, fmt.Sprintf("L %s -> %s", rel, normalizeHome(target, home)))
		case info.IsDir():
			lines = append(lines, fmt.Sprintf("D %s%s", rel, mode))
		default:
			lines = append(lines, fmt.Sprintf("F %s%s", rel, mode))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return lines
}

func TestSyncGlobalMatchesPython(t *testing.T) {
	data, err := os.ReadFile("testdata/syncglobal_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenarios map[string]struct {
		Steps  []syncGlobalStep   `json:"steps"`
		Expect []syncGlobalExpect `json:"expect"`
	}
	if err := json.Unmarshal(data, &scenarios); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(scenarios))
	for name := range scenarios {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sc := scenarios[name]
		t.Run(name, func(t *testing.T) {
			env := testEnv(t)
			home := env.Home
			env.Cwd = home
			var got []syncGlobalExpect
			for _, st := range sc.Steps {
				path := filepath.Join(home, filepath.FromSlash(st.Path))
				switch st.Op {
				case "mkdir":
					if err := os.MkdirAll(path, 0o755); err != nil {
						t.Fatal(err)
					}
				case "write", "append":
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
					if st.Op == "append" {
						flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
					}
					f, err := os.OpenFile(path, flags, 0o644)
					if err != nil {
						t.Fatal(err)
					}
					f.WriteString(st.Content)
					f.Close()
				case "symlink":
					target := st.Target
					if strings.HasPrefix(target, "H/") {
						target = home + target[1:]
					}
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				case "rm":
					if err := os.RemoveAll(path); err != nil {
						t.Fatal(err)
					}
				case "init":
					var out, errb bytes.Buffer
					if code := Run([]string{"init", "workspace"}, nil, &out, &errb, env); code != 0 {
						t.Fatalf("init workspace: %d %s", code, errb.String())
					}
				case "run":
					var out, errb bytes.Buffer
					code := Run(st.Args, nil, &out, &errb, env)
					got = append(got, syncGlobalExpect{Args: st.Args, Exit: &code,
						Stdout: normalizeHome(out.String(), home), Stderr: normalizeHome(errb.String(), home)})
				case "tree":
					got = append(got, syncGlobalExpect{Tree: homeTree(t, home)})
				case "read":
					content, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					got = append(got, syncGlobalExpect{Read: st.Path, Content: string(content)})
				default:
					t.Fatalf("unknown step %q", st.Op)
				}
			}
			if len(got) != len(sc.Expect) {
				t.Fatalf("got %d results, want %d", len(got), len(sc.Expect))
			}
			for i, want := range sc.Expect {
				g := got[i]
				switch {
				case want.Exit != nil:
					if *g.Exit != *want.Exit || g.Stdout != want.Stdout || g.Stderr != want.Stderr {
						t.Errorf("step %d `aikito %s`:\n--- got exit %d\nstdout:\n%s\nstderr:\n%s\n--- want exit %d\nstdout:\n%s\nstderr:\n%s",
							i, strings.Join(want.Args, " "), *g.Exit, g.Stdout, g.Stderr, *want.Exit, want.Stdout, want.Stderr)
					}
				case want.Tree != nil:
					if strings.Join(g.Tree, "\n") != strings.Join(want.Tree, "\n") {
						t.Errorf("step %d tree:\n--- got\n%s\n--- want\n%s", i, strings.Join(g.Tree, "\n"), strings.Join(want.Tree, "\n"))
					}
				default:
					if g.Content != want.Content {
						t.Errorf("step %d %s:\n got %q\nwant %q", i, want.Read, g.Content, want.Content)
					}
				}
			}
		})
	}
}
