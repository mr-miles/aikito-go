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

	"github.com/mr-miles/aikito-go/internal/workspace"
)

// syncglobal_vectors.json is generated from the reference CLI by
// testdata/gen_syncglobal_vectors.py: each scenario's setup steps, plus the
// stdout/stderr/exit of every `sync global` run and snapshots of the home
// tree. This replays the same steps against the Go CLI.

type syncGlobalStep struct {
	Op      string            `json:"op"`
	Path    string            `json:"path"`
	Content string            `json:"content"`
	Target  string            `json:"target"`
	Args    []string          `json:"args"`
	Old     string            `json:"old"`
	New     string            `json:"new"`
	Env     map[string]string `json:"env"`
	Cwd     string            `json:"cwd"`
}

type syncGlobalExpect struct {
	Args    []string `json:"args"`
	Exit    *int     `json:"exit"`
	Stdout  string   `json:"stdout"`
	Stderr  string   `json:"stderr"`
	Tree    []string `json:"tree"`
	Read    string   `json:"read"`
	Content string   `json:"content"`
	WTree   []string `json:"wtree"`
}

var backupTimestampRe = regexp.MustCompile(`bundled-skills_\d{8}_\d{6}_\d{6}`)

var (
	stateHashRe      = regexp.MustCompile(`[0-9a-f]{64}`)
	subagentBackupRe = regexp.MustCompile(`\d{8}T\d{12}Z-`)
)

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
	replayCLIVectors(t, "testdata/syncglobal_vectors.json")
}

// replayCLIVectors replays a generator's scenarios (step format in
// testdata/gen_syncglobal_vectors.py and gen_syncall_vectors.py) against
// the Go CLI and compares every recorded result.
func replayCLIVectors(t *testing.T, vectorsPath string) {
	data, err := os.ReadFile(vectorsPath)
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
				case "replace":
					content, err := os.ReadFile(path)
					if err != nil || !strings.Contains(string(content), st.Old) {
						t.Fatalf("replace in %s: %q not found (%v)", st.Path, st.Old, err)
					}
					if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(content), st.Old, st.New)), 0o644); err != nil {
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
					runEnv := env
					if len(st.Env) > 0 {
						vars := workspace.MapEnv{}
						for k, v := range st.Env {
							if strings.HasPrefix(v, "H/") {
								v = home + v[1:]
							}
							vars[k] = v
						}
						runEnv.Env = vars
					}
					if st.Cwd != "" {
						runEnv.Cwd = filepath.Join(home, filepath.FromSlash(st.Cwd))
					}
					var out, errb bytes.Buffer
					code := Run(st.Args, nil, &out, &errb, runEnv)
					got = append(got, syncGlobalExpect{Args: st.Args, Exit: &code,
						Stdout: normalizeHome(out.String(), home), Stderr: normalizeHome(errb.String(), home)})
				case "tree":
					got = append(got, syncGlobalExpect{Tree: homeTree(t, home)})
				case "wtree":
					got = append(got, syncGlobalExpect{WTree: workspaceTree(t, home)})
				case "read":
					content, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					got = append(got, syncGlobalExpect{Read: st.Path, Content: normalizeHome(string(content), home)})
				case "readglob":
					files, _ := filepath.Glob(path)
					sort.Strings(files)
					var parts []string
					for _, f := range files {
						content, err := os.ReadFile(f)
						if err != nil {
							t.Fatal(err)
						}
						parts = append(parts, normalizeHome(string(content), home))
					}
					got = append(got, syncGlobalExpect{Read: st.Path, Content: strings.Join(parts, "\n---\n")})
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
					// Project skill state files are named by a hash of
					// absolute paths, which differ between generator and
					// replay homes.
					// Subagent backups are named by UTC timestamp.
					norm := func(tree []string) string {
						s := stateHashRe.ReplaceAllString(strings.Join(tree, "\n"), "HASH")
						return subagentBackupRe.ReplaceAllString(s, "TS-")
					}
					gotTree, wantTree := norm(g.Tree), norm(want.Tree)
					if gotTree != wantTree {
						t.Errorf("step %d tree:\n--- got\n%s\n--- want\n%s", i, gotTree, wantTree)
					}
				case want.WTree != nil:
					gotTree, wantTree := strings.Join(g.WTree, "\n"), strings.Join(want.WTree, "\n")
					if gotTree != wantTree {
						t.Errorf("step %d workspace tree:\n--- got\n%s\n--- want\n%s", i, gotTree, wantTree)
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
