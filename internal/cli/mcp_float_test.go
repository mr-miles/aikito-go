package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// mcp_float_vectors.json (testdata/gen_mcp_float_vectors.py) is the OpenCode
// config the reference CLI writes for float timeout overrides: numbers are
// printed with Python's repr (12500000000.0, not Go's 1.25e+10).
func TestSyncMCPWritesFloatsLikePython(t *testing.T) {
	data, err := os.ReadFile("testdata/mcp_float_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		TOML    string `json:"toml"`
		Exit    int    `json:"exit"`
		File    string `json:"file"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		env := testEnv(t)
		env.Cwd = env.Home
		h := env.Home
		if err := os.MkdirAll(filepath.Join(h, ".config", "opencode"), 0o755); err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		if code := Run([]string{"init", "workspace"}, nil, &out, &errOut, env); code != 0 {
			t.Fatalf("init workspace: %d", code)
		}
		if err := os.WriteFile(filepath.Join(h, "aikito", "mcps", "num.toml"), []byte(c.TOML), 0o644); err != nil {
			t.Fatal(err)
		}
		code := Run([]string{"sync", "mcp"}, nil, &out, &errOut, env)
		got, _ := os.ReadFile(filepath.Join(h, ".config", "opencode", c.File))
		if code != c.Exit || string(got) != c.Content {
			t.Errorf("%s: exit %d (want %d)\ngot:\n%s\nwant:\n%s", c.TOML, code, c.Exit, got, c.Content)
		}
	}
}
