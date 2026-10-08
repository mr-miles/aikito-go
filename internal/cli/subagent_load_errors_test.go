package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// subagent_load_error_vectors.json (testdata/gen_subagent_load_errors.py)
// is the reference CLI's output when subagents/ holds an unsupported entry
// or a malformed subagent file: load_subagent_definitions and
// _parse_subagent_text errors, each naming the offending path.
func TestSubagentLoadErrorsMatchPython(t *testing.T) {
	data, err := os.ReadFile("testdata/subagent_load_error_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases map[string]struct {
		Entry   string  `json:"entry"`
		Content *string `json:"content"`
		Steps   []struct {
			Args   []string `json:"args"`
			Stdout string   `json:"stdout"`
			Stderr string   `json:"stderr"`
			Exit   int      `json:"exit"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		c := cases[name]
		t.Run(name, func(t *testing.T) {
			env := testEnv(t)
			env.Cwd = env.Home
			h := env.Home
			if err := os.MkdirAll(filepath.Join(h, ".claude"), 0o755); err != nil {
				t.Fatal(err)
			}
			var out, errOut bytes.Buffer
			for _, args := range [][]string{{"init", "workspace"}, {"add", "subagent", "good", "--description", "ok"}} {
				if code := Run(args, nil, &out, &errOut, env); code != 0 {
					t.Fatalf("%v: exit %d: %s", args, code, errOut.String())
				}
			}
			target := filepath.Join(h, "aikito", "subagents", strings.TrimSuffix(c.Entry, "/"))
			if c.Content == nil {
				err = os.Mkdir(target, 0o755)
			} else {
				err = os.WriteFile(target, []byte(*c.Content), 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range c.Steps {
				out.Reset()
				errOut.Reset()
				code := Run(s.Args, nil, &out, &errOut, env)
				norm := func(x string) string { return strings.ReplaceAll(x, h, "{H}") }
				if code != s.Exit || norm(out.String()) != s.Stdout || norm(errOut.String()) != s.Stderr {
					t.Errorf("aikito %s: exit %d (want %d)\nstdout:\n%s\nwant:\n%s\nstderr:\n%s\nwant:\n%s",
						strings.Join(s.Args, " "), code, s.Exit, norm(out.String()), s.Stdout, norm(errOut.String()), s.Stderr)
				}
			}
		})
	}
}
