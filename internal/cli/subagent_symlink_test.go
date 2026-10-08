package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// subagent_symlink_vectors.json comes from the reference CLI
// (testdata/gen_subagent_symlink_vectors.py): agent subagent files that are
// symlinks are listed like regular files, so an unmanaged one is adopted and
// a managed one is reported as an orphan and pruned.
func TestSubagentSymlinkedFilesMatchPython(t *testing.T) {
	data, err := os.ReadFile("testdata/subagent_symlink_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenarios map[string]struct {
		Steps   [][]string `json:"steps"`
		Results []struct {
			Step    []string `json:"step"`
			Stdout  string   `json:"stdout"`
			Stderr  string   `json:"stderr"`
			Exit    int      `json:"exit"`
			Content *string  `json:"content"`
			Exists  *bool    `json:"exists"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &scenarios); err != nil {
		t.Fatal(err)
	}
	stamp := regexp.MustCompile(`\d{8}T\d{12}Z`)
	adoptStamp := regexp.MustCompile(`adopt_\d{8}_\d{6}`)
	for name, sc := range scenarios {
		t.Run(name, func(t *testing.T) {
			env := testEnv(t)
			h := env.Home
			env.Cwd = h
			norm := func(s string) string {
				s = strings.ReplaceAll(s, h, "{H}")
				s = stamp.ReplaceAllString(s, "{TS}")
				return adoptStamp.ReplaceAllString(s, "adopt_{TS}")
			}
			ri := 0
			for _, st := range sc.Steps {
				p := func(i int) string { return filepath.Join(h, st[i]) }
				switch st[0] {
				case "mkdir":
					os.MkdirAll(p(1), 0o755)
				case "write":
					os.MkdirAll(filepath.Dir(p(1)), 0o755)
					os.WriteFile(p(1), []byte(st[2]), 0o644)
				case "symlink":
					if err := os.Symlink(p(2), p(1)); err != nil {
						t.Fatal(err)
					}
				case "move":
					os.MkdirAll(filepath.Dir(p(2)), 0o755)
					if err := os.Rename(p(1), p(2)); err != nil {
						t.Fatal(err)
					}
				case "remove":
					os.Remove(p(1))
				case "run":
					want := sc.Results[ri]
					ri++
					var out, errOut bytes.Buffer
					code := Run(st[1:], nil, &out, &errOut, env)
					if code != want.Exit || norm(out.String()) != want.Stdout || norm(errOut.String()) != want.Stderr {
						t.Errorf("aikito %s\ngot exit %d\nstdout:\n%s\nstderr:\n%s\nwant exit %d\nstdout:\n%s\nstderr:\n%s",
							strings.Join(st[1:], " "), code, norm(out.String()), norm(errOut.String()), want.Exit, want.Stdout, want.Stderr)
					}
				case "read":
					want := sc.Results[ri]
					ri++
					got, err := os.ReadFile(p(1))
					if want.Content == nil {
						if err == nil {
							t.Errorf("%s exists, want missing", st[1])
						}
					} else if string(got) != *want.Content {
						t.Errorf("%s:\ngot  %q\nwant %q", st[1], got, *want.Content)
					}
				case "exists":
					want := sc.Results[ri]
					ri++
					_, err := os.Lstat(p(1))
					if (err == nil) != *want.Exists {
						t.Errorf("%s exists=%v, want %v", st[1], err == nil, *want.Exists)
					}
				}
			}
		})
	}
}
