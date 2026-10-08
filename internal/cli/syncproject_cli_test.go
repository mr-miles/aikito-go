package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Black-box replay of testdata/syncproject_cli/want.json, generated from the
// reference CLI by gen.sh: cwd detection (no name outside any project is an
// error, not "sync everything"), "." outside a project, detection from a
// subdirectory, and a conflict blocking both --dry-run and a real run with
// exit 1.
func TestSyncProjectCLIMatchesPython(t *testing.T) {
	data, err := os.ReadFile("testdata/syncproject_cli/want.json")
	if err != nil {
		t.Fatal(err)
	}
	var steps []struct {
		Cwd    string   `json:"cwd"`
		Args   []string `json:"args"`
		Stdout string   `json:"stdout"`
		Stderr string   `json:"stderr"`
		Exit   int      `json:"exit"`
	}
	if err := json.Unmarshal(data, &steps); err != nil {
		t.Fatal(err)
	}

	env := testEnv(t)
	h := env.Home
	for _, d := range []string{"p1", "p2/sub", "elsewhere"} {
		if err := os.MkdirAll(filepath.Join(h, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut bytes.Buffer
	for _, args := range [][]string{
		{"init", "workspace"},
		{"init", "project", "p1", filepath.Join(h, "p1")},
		{"init", "project", "p2", filepath.Join(h, "p2")},
	} {
		if code := Run(args, nil, &out, &errOut, env); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errOut.String())
		}
	}
	// init project links the checkout's memory notes into the workspace.
	notes := filepath.Join(h, "p1", ".agents", "memory", "notes")
	if fi, err := os.Lstat(notes); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("init project did not link %s: %v", notes, err)
	}
	if err := os.RemoveAll(notes); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(notes, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notes, "a.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, s := range steps {
		out.Reset()
		errOut.Reset()
		stepEnv := env
		stepEnv.Cwd = filepath.Join(h, s.Cwd)
		code := Run(s.Args, nil, &out, &errOut, stepEnv)
		norm := func(x string) string { return strings.ReplaceAll(x, h, "{H}") }
		if code != s.Exit || norm(out.String()) != s.Stdout || norm(errOut.String()) != s.Stderr {
			t.Errorf("(cd %s; aikito %s)\ngot exit %d\nstdout:\n%s\nstderr:\n%s\nwant exit %d\nstdout:\n%s\nstderr:\n%s",
				s.Cwd, strings.Join(s.Args, " "), code, norm(out.String()), norm(errOut.String()), s.Exit, s.Stdout, s.Stderr)
		}
	}
}
