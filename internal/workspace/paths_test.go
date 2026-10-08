package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorkspaceWithSourceEnvOverride(t *testing.T) {
	home := t.TempDir()
	override := filepath.Join(t.TempDir(), "my-workspace")
	env := MapEnv{"AIKITO_DIR": override}
	path, source, err := ResolveWorkspaceWithSource(home, env)
	if err != nil {
		t.Fatal(err)
	}
	if source != "AIKITO_DIR" {
		t.Errorf("source = %q, want AIKITO_DIR", source)
	}
	want, _ := ResolvePath(override)
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func TestResolveWorkspaceWithSourceConfigured(t *testing.T) {
	home := t.TempDir()
	env := MapEnv{}
	configured := filepath.Join(t.TempDir(), "configured-workspace")
	pointerPath := WorkspacePointerPath(home, env)
	if err := os.MkdirAll(filepath.Dir(pointerPath), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pointerPath, []byte(configured+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, source, err := ResolveWorkspaceWithSource(home, env)
	if err != nil {
		t.Fatal(err)
	}
	if source != "configured" {
		t.Errorf("source = %q, want configured", source)
	}
	want, _ := ResolvePath(configured)
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func TestResolveWorkspaceWithSourceDefault(t *testing.T) {
	home := t.TempDir()
	env := MapEnv{}
	path, source, err := ResolveWorkspaceWithSource(home, env)
	if err != nil {
		t.Fatal(err)
	}
	if source != "default" {
		t.Errorf("source = %q, want default", source)
	}
	want, _ := ResolvePath(filepath.Join(home, "aikito"))
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func TestPersistWorkspaceRoundTrip(t *testing.T) {
	home := t.TempDir()
	env := MapEnv{}
	workspace := filepath.Join(t.TempDir(), "ws")
	if _, err := PersistWorkspace(workspace, home, env); err != nil {
		t.Fatal(err)
	}
	path, source, err := ResolveWorkspaceWithSource(home, env)
	if err != nil {
		t.Fatal(err)
	}
	if source != "configured" {
		t.Errorf("source = %q, want configured", source)
	}
	want, _ := ResolvePath(workspace)
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func TestExpandUser(t *testing.T) {
	home := "/home/example"
	cases := []struct{ in, want string }{
		{"~", "/home/example"},
		{"~/sub/dir", "/home/example/sub/dir"},
		{"/already/absolute", "/already/absolute"},
		{"relative/path", "relative/path"},
	}
	for _, tc := range cases {
		if got := ExpandUser(home, tc.in); got != tc.want {
			t.Errorf("ExpandUser(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
