package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// initworkspace_vectors.json is generated from the reference CLI by
// testdata/gen_initworkspace_vectors.py: refusals (non-empty unknown
// directory, a file, a source checkout, old layouts), next-step hints,
// re-run/--force output and the bundled-skill refresh of an existing
// workspace. Drives only Run, so it also runs against older builds.
func TestInitWorkspaceMatchesPython(t *testing.T) {
	data, err := os.ReadFile("testdata/initworkspace_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenarios map[string]struct {
		Setup [][]string `json:"setup"`
		Steps []struct {
			Args   []string `json:"args"`
			Stdout string   `json:"stdout"`
			Stderr string   `json:"stderr"`
			Exit   int      `json:"exit"`
		} `json:"steps"`
		Tree []string `json:"tree"`
	}
	if err := json.Unmarshal(data, &scenarios); err != nil {
		t.Fatal(err)
	}
	ts := regexp.MustCompile(`bundled-skills_\d{8}_\d{6}_\d{6}`)
	for name, sc := range scenarios {
		t.Run(name, func(t *testing.T) {
			env := testEnv(t)
			h := env.Home
			env.Cwd = h
			var out, errOut bytes.Buffer
			for _, op := range sc.Setup {
				switch op[0] {
				case "mkdir":
					if err := os.MkdirAll(filepath.Join(h, op[1]), 0o755); err != nil {
						t.Fatal(err)
					}
				case "write":
					p := filepath.Join(h, op[1])
					if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte(op[2]), 0o644); err != nil {
						t.Fatal(err)
					}
				case "append":
					f, err := os.OpenFile(filepath.Join(h, op[1]), os.O_APPEND|os.O_WRONLY, 0)
					if err != nil {
						t.Fatal(err)
					}
					f.WriteString(op[2])
					f.Close()
				case "init":
					Run([]string{"init", "workspace"}, nil, &out, &errOut, env)
				}
			}
			norm := func(s string) string {
				return ts.ReplaceAllString(strings.ReplaceAll(s, h, "{H}"), "bundled-skills_{TS}")
			}
			for _, st := range sc.Steps {
				out.Reset()
				errOut.Reset()
				args := make([]string, len(st.Args))
				for i, a := range st.Args {
					args[i] = strings.ReplaceAll(a, "{H}", h)
				}
				code := Run(args, nil, &out, &errOut, env)
				if code != st.Exit || norm(out.String()) != st.Stdout || norm(errOut.String()) != st.Stderr {
					t.Errorf("aikito %s\ngot exit %d\nstdout:\n%s\nstderr:\n%s\nwant exit %d\nstdout:\n%s\nstderr:\n%s",
						strings.Join(st.Args, " "), code, norm(out.String()), norm(errOut.String()), st.Exit, st.Stdout, st.Stderr)
				}
			}
			var tree []string
			filepath.Walk(h, func(p string, info os.FileInfo, err error) error {
				if err != nil || p == h {
					return nil
				}
				rel, _ := filepath.Rel(h, p)
				for _, part := range strings.Split(rel, string(filepath.Separator)) {
					if part == ".git" {
						if info.IsDir() {
							return filepath.SkipDir
						}
						return nil
					}
				}
				tree = append(tree, norm(filepath.ToSlash(rel)))
				return nil
			})
			sort.Strings(tree)
			want := append([]string(nil), sc.Tree...)
			sort.Strings(want)
			if strings.Join(tree, "\n") != strings.Join(want, "\n") {
				t.Errorf("tree:\ngot  %v\nwant %v", tree, want)
			}
		})
	}
}
