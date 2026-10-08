package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpAnnotationMarkersMatchExactlyOneLine(t *testing.T) {
	for path, markers := range helpAnnotations {
		text, ok := helpTexts[path]
		if !ok {
			t.Errorf("annotation for unknown help path %q", path)
			continue
		}
		for _, m := range markers {
			n := 0
			for _, line := range strings.Split(text, "\n") {
				if strings.HasPrefix(line, m) {
					n++
				}
			}
			if n != 1 {
				t.Errorf("help %q: marker %q matches %d lines, want 1", path, m, n)
			}
		}
	}
}

func TestEveryCompletionCommandHasHelp(t *testing.T) {
	for path := range helpTexts {
		if strings.HasPrefix(path, "__") {
			continue
		}
		if _, ok := helpFor(path); !ok {
			t.Errorf("no help for %q", path)
		}
	}
	for _, path := range []string{"", "sync", "sync global", "init project", "rm skill", "remove subagents", "git"} {
		if _, ok := helpTexts[path]; !ok {
			t.Errorf("help.json missing %q", path)
		}
	}
}

func TestHelpRouting(t *testing.T) {
	cases := []struct {
		args     []string
		wantPath string // "" root; "-" means not handled
	}{
		{[]string{"--help"}, ""},
		{[]string{"-h"}, ""},
		{[]string{"sync", "--help"}, "sync"},
		{[]string{"sync", "global", "--dry-run", "--help"}, "sync global"},
		{[]string{"show", "mcp", "foo", "-h"}, "show mcp"},
		{[]string{"show", "mcp", "--agent", "codex", "--help"}, "show mcp"},
		{[]string{"remove", "skills", "--help"}, "remove skills"},
		{[]string{"git", "--help"}, "git"},
		{[]string{"git", "log", "--help"}, "-"},
		{[]string{"nosuch", "--help"}, "-"},
		{[]string{"sync", "global"}, "-"},
		{[]string{"sync", "--", "--help"}, "-"},
	}
	for _, c := range cases {
		var out bytes.Buffer
		handled := handleHelp(c.args, &out)
		if c.wantPath == "-" {
			if handled {
				t.Errorf("%v: handled as help, want passthrough", c.args)
			}
			continue
		}
		want, _ := helpFor(c.wantPath)
		if !handled || out.String() != want {
			t.Errorf("%v: got handled=%v output:\n%s\nwant help for %q", c.args, handled, out.String(), c.wantPath)
		}
	}
}

func TestRunHelpAndNoArgs(t *testing.T) {
	env := testEnv(t)
	var out, errb bytes.Buffer
	if code := Run([]string{"--debug", "version", "--help"}, nil, &out, &errb, env); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "usage: aikito version") ||
		!strings.Contains(out.String(), "  -c, --check  Check remote repository for latest available release"+notImplementedNote) {
		t.Errorf("version help:\n%s", out.String())
	}

	out.Reset()
	errb.Reset()
	if code := Run(nil, nil, &out, &errb, env); code != 2 {
		t.Fatalf("no args: exit %d, want 2", code)
	}
	if errb.String() != helpTexts["__noargs__"] || helpTexts["__noargs_exit__"] != "2" {
		t.Errorf("no args stderr:\n%s", errb.String())
	}
}
