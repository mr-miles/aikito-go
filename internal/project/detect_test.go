package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Behaviour checked against resolve.py's detect_current_project with the
// same layouts (see e2e project_sync_init for the CLI-level messages).
func TestDetectCurrentProject(t *testing.T) {
	home := resolvedTempDir(t)
	ws := filepath.Join(home, "aikito")
	write := func(rel, content string) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"code/app/sub", "code/app/nested", "shared", "elsewhere"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("aikito/projects/app/agent.toml", "name = \"app\"\npath = \"~/code/app\"\n")
	write("aikito/projects/inner/agent.toml", "name = \"inner\"\npath = \"~/code/app/nested\"\n")
	write("aikito/projects/one/agent.toml", "name = \"one\"\npath = \"~/shared\"\n")
	write("aikito/projects/two/agent.toml", "name = \"two\"\npath = \"~/shared\"\n")
	write("aikito/projects/.hidden/agent.toml", "name = \"hidden\"\npath = \"~/elsewhere\"\n")

	cases := []struct{ cwd, want string }{
		{"code/app", "app"},
		{"code/app/sub", "app"},
		{"code/app/nested", "inner"}, // longest match wins
		{"aikito/projects/two", "two"},
		{"elsewhere", ""}, // dot-prefixed project folders are skipped
		{"", ""},
	}
	for _, c := range cases {
		got, err := DetectCurrentProject(ws, filepath.Join(home, c.cwd), home)
		if err != nil || got != c.want {
			t.Errorf("cwd %q: got %q, %v; want %q", c.cwd, got, err, c.want)
		}
	}

	_, err := DetectCurrentProject(ws, filepath.Join(home, "shared"), home)
	var conflict *ContextConflictError
	if !errors.As(err, &conflict) || conflict.Path != filepath.Join(home, "shared") ||
		len(conflict.Projects) != 2 || conflict.Projects[0] != "one" || conflict.Projects[1] != "two" {
		t.Errorf("shared checkout: got %v", err)
	}
}
