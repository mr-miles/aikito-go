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

// testdata/diff_invalid_utf8 (gen.py) is the reference CLI's `aikito diff`
// of a copied skill edited to hold invalid UTF-8: Python decodes with
// errors="replace", one U+FFFD per maximal invalid subpart, not one per run.
func TestDiffInvalidUTF8MatchesPython(t *testing.T) {
	dir := "testdata/diff_invalid_utf8"
	data, err := os.ReadFile(filepath.Join(dir, "want.json"))
	if err != nil {
		t.Fatal(err)
	}
	edited, err := os.ReadFile(filepath.Join(dir, "edited.bin"))
	if err != nil {
		t.Fatal(err)
	}
	var steps []struct {
		Args   []string `json:"args"`
		Stdout string   `json:"stdout"`
		Stderr string   `json:"stderr"`
		Exit   int      `json:"exit"`
	}
	if err := json.Unmarshal(data, &steps); err != nil {
		t.Fatal(err)
	}

	env := testEnv(t)
	env.Cwd = env.Home
	h := env.Home
	if err := os.MkdirAll(filepath.Join(h, "p1"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	run := func(args ...string) {
		out.Reset()
		errOut.Reset()
		if code := Run(args, nil, &out, &errOut, env); code != 0 {
			t.Fatalf("%v: exit %d\n%s%s", args, code, out.String(), errOut.String())
		}
	}
	run("init", "workspace")
	run("init", "project", "p1", filepath.Join(h, "p1"))
	run("add", "skill", "s1", "--description", "s", "--project", "p1")
	cfg := filepath.Join(h, "aikito", "projects", "p1", "agent.toml")
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw = regexp.MustCompile(`sync_mode = "link"`).ReplaceAll(raw, []byte(`sync_mode = "copy"`))
	if err := os.WriteFile(cfg, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	run("sync", "project", "p1")
	if err := os.WriteFile(filepath.Join(h, "p1", ".agents", "skills", "s1", "SKILL.md"), edited, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, s := range steps {
		out.Reset()
		errOut.Reset()
		code := Run(s.Args, nil, &out, &errOut, env)
		norm := func(x string) string { return strings.ReplaceAll(x, h, "{H}") }
		if code != s.Exit || norm(out.String()) != s.Stdout || norm(errOut.String()) != s.Stderr {
			t.Errorf("aikito %s: exit %d (want %d)\nstdout:\n%s\nwant:\n%s\nstderr:\n%s",
				strings.Join(s.Args, " "), code, s.Exit, norm(out.String()), s.Stdout, errOut.String())
		}
	}
}
