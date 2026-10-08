package cli

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// fakeEditorInvocation records what runEditorProcess was called with,
// without spawning a real interactive process.
func stubEditor(t *testing.T) *[]string {
	t.Helper()
	var captured []string
	orig := runEditorProcess
	runEditorProcess = func(cmdArgs []string, _, _ io.Writer) (int, error) {
		captured = append([]string(nil), cmdArgs...)
		return 0, nil
	}
	t.Cleanup(func() { runEditorProcess = orig })
	return &captured
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"vi", []string{"vi"}},
		{"code --wait", []string{"code", "--wait"}},
		{"emacs -nw", []string{"emacs", "-nw"}},
		{`"my editor" --flag`, []string{"my editor", "--flag"}},
		{"", nil},
		{"  ", nil},
	}
	for _, tc := range cases {
		got := splitCommand(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("splitCommand(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitCommand(%q) = %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}

func TestCmdEditSkillResolvesPathAndInvokesEditor(t *testing.T) {
	env := testEnv(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"init", "workspace"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("init workspace failed: %s", errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"add", "skill", "my-skill", "--description", "Test"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("add skill failed: %s", errOut.String())
	}
	env.Env = testEditorEnv{"EDITOR": "code --wait"}

	captured := stubEditor(t)
	out.Reset()
	errOut.Reset()
	code := Run([]string{"edit", "skill", "my-skill"}, nil, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	aikitoDir, _ := env.AikitoDir()
	wantPath := filepath.Join(aikitoDir, "skills", "my-skill", "SKILL.md")
	want := []string{"code", "--wait", wantPath}
	if len(*captured) != len(want) {
		t.Fatalf("captured = %v, want %v", *captured, want)
	}
	for i := range want {
		if (*captured)[i] != want[i] {
			t.Errorf("captured[%d] = %q, want %q", i, (*captured)[i], want[i])
		}
	}
}

func TestCmdEditInstructionsDefaultsToGlobal(t *testing.T) {
	env := testEnv(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"init", "workspace"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("init workspace failed: %s", errOut.String())
	}
	env.Env = testEditorEnv{"EDITOR": "vi"}
	captured := stubEditor(t)
	out.Reset()
	errOut.Reset()
	code := Run([]string{"edit", "instructions"}, nil, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	aikitoDir, _ := env.AikitoDir()
	want := filepath.Join(aikitoDir, "global", "AGENTS.md")
	if len(*captured) != 2 || (*captured)[1] != want {
		t.Errorf("captured = %v, want [vi %s]", *captured, want)
	}
}

func TestCmdEditMCPNotFound(t *testing.T) {
	env := testEnv(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"init", "workspace"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("init workspace failed: %s", errOut.String())
	}
	stubEditor(t)
	out.Reset()
	errOut.Reset()
	code := Run([]string{"edit", "mcp", "does-not-exist"}, nil, &out, &errOut, env)
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "not found") {
		t.Errorf("expected a not-found error, got: %s", errOut.String())
	}
}

func TestCmdEditDefaultEditorFallback(t *testing.T) {
	env := testEnv(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"init", "workspace"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("init workspace failed: %s", errOut.String())
	}
	env.Env = testEditorEnv{} // no VISUAL/EDITOR set
	captured := stubEditor(t)
	out.Reset()
	errOut.Reset()
	code := Run([]string{"edit", "instructions", "global"}, nil, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	if len(*captured) == 0 || (*captured)[0] != "vi" {
		t.Errorf("expected fallback to vi, got: %v", *captured)
	}
}

// testEditorEnv is a minimal workspace.Env fake that also answers
// VISUAL/EDITOR lookups (testEnv's workspace.MapEnv{} works too, but this
// local alias keeps the intent obvious at each call site).
type testEditorEnv map[string]string

func (m testEditorEnv) Getenv(key string) string { return m[key] }
