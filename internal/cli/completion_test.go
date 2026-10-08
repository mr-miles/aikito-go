package cli

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// dispatchedCommands parses run.go and returns every top-level command
// string Run's switch dispatches on (excluding flag-style cases such as
// "--version"), so a command added to run.go but not to cliSchema fails
// TestCompletionSchemaCoversEveryDispatchedCommand.
func dispatchedCommands(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "run.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var cmds []string
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Run" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				lit, ok := e.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(s, "-") {
					cmds = append(cmds, s)
				}
			}
			return true
		})
		return false
	})
	if len(cmds) < 10 {
		t.Fatalf("parsed only %d commands from run.go (%v); parser assumption broken?", len(cmds), cmds)
	}
	sort.Strings(cmds)
	return cmds
}

func TestCompletionSchemaCoversEveryDispatchedCommand(t *testing.T) {
	for _, cmd := range dispatchedCommands(t) {
		if _, ok := cliSchema[cmd]; !ok {
			t.Errorf("command %q is dispatched in run.go but missing from completion.go's cliSchema", cmd)
		}
	}
	dispatched := map[string]bool{}
	for _, c := range dispatchedCommands(t) {
		dispatched[c] = true
	}
	for cmd := range cliSchema {
		if !dispatched[cmd] {
			t.Errorf("cliSchema advertises %q, which run.go does not dispatch", cmd)
		}
	}
}

func TestCompletionScriptsMentionEveryCommand(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := Run([]string{"completion", shell}, nil, &out, &errOut, Environment{}); code != 0 {
				t.Fatalf("exit %d: %s", code, errOut.String())
			}
			script := out.String()
			if strings.TrimSpace(script) == "" {
				t.Fatal("empty script")
			}
			for _, cmd := range dispatchedCommands(t) {
				if !strings.Contains(script, cmd) {
					t.Errorf("%s script does not mention command %q", shell, cmd)
				}
			}
		})
	}
}

func TestCompletionBashScriptListsFlagsAndSubcommands(t *testing.T) {
	var out bytes.Buffer
	Run([]string{"completion", "bash"}, nil, &out, &bytes.Buffer{}, Environment{})
	script := out.String()
	for _, want := range []string{
		`complete -F _aikito_completion aikito`,
		`"import workspace")`,
		`--keep-target`,
		`"migrate workspace-resources")`,
		`doctor)`,
		`--stale-days`,
		`"maintain memory")`,
		`completion candidates memory-completions`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("bash script missing %q", want)
		}
	}
}

func TestCompletionErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no_target", []string{"completion"}, "Usage: aikito completion"},
		{"unknown_shell", []string{"completion", "tcsh"}, "Unknown completion target: tcsh"},
		{"candidates_no_category", []string{"completion", "candidates"}, "Usage: aikito completion candidates"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := Run(tc.args, nil, &out, &errOut, testEnv(t)); code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			if !strings.Contains(errOut.String(), tc.want) {
				t.Errorf("stderr = %q, want it to contain %q", errOut.String(), tc.want)
			}
		})
	}

	env := testEnv(t)
	initTestWorkspace(t, env)
	var out, errOut bytes.Buffer
	if code := Run([]string{"completion", "candidates", "bogus"}, nil, &out, &errOut, env); code != 2 {
		t.Errorf("unknown category exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "Unknown candidate category: bogus") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func initTestWorkspace(t *testing.T, env Environment) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := Run([]string{"init", "workspace"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("init workspace: %s", errOut.String())
	}
	dir, err := env.AikitoDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

type completionVectors struct {
	ExtraFiles map[string]string   `json:"extra_files"`
	Expected   map[string][]string `json:"expected"`
}

// Expected values come from the real Python completion list functions on an
// identical fixture; regenerate with testdata/gen_completion_vectors.py.
func TestCompletionCandidatesMatchPython(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "completion_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v completionVectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}

	env := testEnv(t)
	if err := os.MkdirAll(filepath.Join(env.Home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := initTestWorkspace(t, env)
	for rel, content := range v.ExtraFiles {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	categories := make([]string, 0, len(v.Expected))
	for k := range v.Expected {
		categories = append(categories, k)
	}
	sort.Strings(categories)
	for _, category := range categories {
		want := v.Expected[category]
		t.Run(category, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := Run([]string{"completion", "candidates", category}, nil, &out, &errOut, env); code != 0 {
				t.Fatalf("exit %d: %s", code, errOut.String())
			}
			got := strings.Fields(out.String())
			if len(got) == 0 {
				got = []string{}
			}
			if want == nil {
				want = []string{}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("candidates %s:\n got  %v\n want %v (Python)", category, got, want)
			}
		})
	}
}

func TestCompletionCandidatesAliasesAndPaths(t *testing.T) {
	env := testEnv(t)
	root := initTestWorkspace(t, env)
	if err := os.MkdirAll(filepath.Join(root, "mcps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mcps", "weather.toml"), []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inbox-note-marker.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) string {
		var out, errOut bytes.Buffer
		if code := Run(append([]string{"completion", "candidates"}, args...), nil, &out, &errOut, env); code != 0 {
			t.Fatalf("%v exit %d: %s", args, code, errOut.String())
		}
		return out.String()
	}

	if got := run("mcp"); strings.TrimSpace(got) != "weather" {
		t.Errorf(`"mcp" alias = %q, want "weather"`, got)
	}
	if got := run("paths", "inbox-note"); !strings.Contains(got, "inbox-note-marker.md") {
		t.Errorf("paths prefix match missing marker file: %q", got)
	}
	if got := run("paths", ""); got != "" {
		t.Errorf("empty paths prefix should list nothing, got %q", got)
	}
	if got := run("paths", "a/b"); got != "" {
		t.Errorf("paths prefix containing / should list nothing, got %q", got)
	}
}

func TestCompletionCandidatesWithoutWorkspaceIsEmpty(t *testing.T) {
	env := testEnv(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"completion", "candidates", "skills"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("expected silent empty output, got stdout=%q stderr=%q", out.String(), errOut.String())
	}
}
