package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Expected files come from the reference adopt
// (testdata/adopt_mcp_secrets/gen.sh). Adopted MCP servers must never carry
// a plaintext env value or credential header into the workspace.
func TestAdoptSanitizesMCPSecretsLikePython(t *testing.T) {
	env := testEnv(t)
	dir := "testdata/adopt_mcp_secrets"
	if err := os.MkdirAll(filepath.Join(env.Home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	input, err := os.ReadFile(filepath.Join(dir, "input", ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.Home, ".claude.json"), input, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	for _, args := range [][]string{{"init", "workspace"}, {"adopt"}} {
		if code := Run(args, nil, &out, &errOut, env); code != 0 {
			t.Fatalf("%v: exit %d\n%s%s", args, code, out.String(), errOut.String())
		}
	}
	wants, _ := filepath.Glob(filepath.Join(dir, "want", "*.toml"))
	if len(wants) == 0 {
		t.Fatal("no expected files")
	}
	for _, w := range wants {
		want, _ := os.ReadFile(w)
		got, err := os.ReadFile(filepath.Join(env.Home, "aikito", "mcps", filepath.Base(w)))
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(w), err)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("%s:\ngot:\n%s\nwant:\n%s", filepath.Base(w), got, want)
		}
		if strings.Contains(string(got), "fake-value-for-tests") {
			t.Errorf("%s contains the plaintext secret", filepath.Base(w))
		}
	}
}
