package project

import (
	"os"
	"path/filepath"
	"testing"
)

// Cross-validated against the real Python resolve_project_path.
func TestResolveProjectPath(t *testing.T) {
	home := resolvedTempDir(t)
	cases := []struct {
		raw  string
		want string
	}{
		{"~", home},
		{"~/sub/dir", filepath.Join(home, "sub", "dir")},
		{"/abs/path", "/abs/path"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, ok := ResolveProjectPath(tc.raw, home)
			if !ok {
				t.Fatalf("expected ok=true")
			}
			if got != tc.want {
				t.Errorf("ResolveProjectPath(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// Confirms the CWD-relative fallback behavior (a bare relative path resolves
// against the process's current working directory at call time, not home or
// any workspace root) — cross-validated: running the equivalent Python
// resolve_project_path("relative/path", home) from a given CWD resolves to
// <that CWD>/relative/path, exactly like this test expects of the Go port.
func TestResolveProjectPathRelativeUsesCWD(t *testing.T) {
	dir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldwd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	got, ok := ResolveProjectPath("relative/path", resolvedTempDir(t))
	if !ok {
		t.Fatalf("expected ok=true")
	}
	resolvedDir, _ := filepath.EvalSymlinks(dir)
	want := filepath.Join(resolvedDir, "relative/path")
	if got != want {
		t.Errorf("ResolveProjectPath(relative) = %q, want %q", got, want)
	}
}

func TestResolveProjectPathNonString(t *testing.T) {
	if _, ok := ResolveProjectPath(42, "/home"); ok {
		t.Errorf("expected ok=false for non-string input")
	}
	if _, ok := ResolveProjectPath(nil, "/home"); ok {
		t.Errorf("expected ok=false for nil input")
	}
	if _, ok := ResolveProjectPath("", "/home"); ok {
		t.Errorf("expected ok=false for empty string input")
	}
}

// Cross-validated against the real Python get_project_candidate_paths.
func TestGetProjectCandidatePaths(t *testing.T) {
	cases := []struct {
		name   string
		config map[string]any
		want   [][2]string
	}{
		{
			"paths_list",
			map[string]any{"paths": []any{"~/a", "~/b"}},
			[][2]string{{"1", "~/a"}, {"2", "~/b"}},
		},
		{
			"path_single_string",
			map[string]any{"path": "~/single"},
			[][2]string{{"default", "~/single"}},
		},
		{
			"path_list",
			map[string]any{"path": []any{"~/one", "~/two"}},
			[][2]string{{"1", "~/one"}, {"2", "~/two"}},
		},
		{
			"empty_paths_table_falls_back_to_path",
			map[string]any{"paths": map[string]any{}, "path": "~/fallback"},
			[][2]string{{"default", "~/fallback"}},
		},
		{
			"empty_config",
			map[string]any{},
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GetProjectCandidatePaths(tc.config)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i, w := range tc.want {
				if got[i].label != w[0] || got[i].raw != w[1] {
					t.Errorf("entry %d = (%q,%q), want (%q,%q)", i, got[i].label, got[i].raw, w[0], w[1])
				}
			}
		})
	}
}

func TestGetProjectCandidatePathsTableForm(t *testing.T) {
	// Table form key ORDER is a documented, flagged divergence (sorted here
	// vs TOML-source order in Python) — just check both labels/raws are
	// present, not their relative order.
	got := GetProjectCandidatePaths(map[string]any{
		"paths": map[string]any{"work": "~/work", "laptop": "/x"},
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %v", got)
	}
	found := map[string]string{}
	for _, c := range got {
		found[c.label] = c.raw
	}
	if found["work"] != "~/work" || found["laptop"] != "/x" {
		t.Errorf("unexpected candidates: %v", found)
	}
}

// Cross-validated against the real Python add_candidate_path_to_content for
// each of its branches.
func TestAddCandidatePathToContent(t *testing.T) {
	home := "/home/example"
	cases := []struct {
		name    string
		content string
		newPath string
		want    string
	}{
		{
			"no_existing_paths_field",
			"name = \"x\"\n",
			"~/newpath",
			"name = \"x\"\npaths = [\n    \"~/newpath\",\n]\n",
		},
		{
			"existing_paths_list",
			"name = \"x\"\npaths = [\"~/a\"]\n",
			"~/b",
			"name = \"x\"\npaths = [\"~/a\", \"~/b\"]\n",
		},
		{
			"existing_paths_table_section",
			"name = \"x\"\n\n[paths]\nwork = \"~/work\"\n",
			"~/laptop",
			"name = \"x\"\n\n[paths]\nwork = \"~/work\"\npath_1 = \"~/laptop\"\n",
		},
		{
			"existing_paths_inline_table",
			"name = \"x\"\npaths = { work = \"~/work\" }\n",
			"~/laptop",
			"name = \"x\"\npaths = { work = \"~/work\", path_1 = \"~/laptop\" }\n",
		},
		{
			"legacy_path_field_becomes_paths_list",
			"path = \"~/old\"\n",
			"~/new",
			"paths = [\n    \"~/old\",\n    \"~/new\",\n]\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, err := AddCandidatePathToContent(tc.content, tc.newPath, home, true)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !changed {
				t.Fatalf("expected changed=true")
			}
			if got != tc.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

func TestAddCandidatePathToContentAlreadyPresent(t *testing.T) {
	_, changed, err := AddCandidatePathToContent("name = \"x\"\npaths = [\"~/a\"]\n", "~/a", "/home/example", true)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Errorf("expected changed=false when the path is already present")
	}
}

func TestValidateProjectConfig(t *testing.T) {
	cases := []struct {
		name    string
		config  map[string]any
		wantErr bool
	}{
		{"empty_valid", map[string]any{}, false},
		{"valid_skills", map[string]any{"skills": []any{"a", "b"}}, false},
		{"invalid_skills_not_list", map[string]any{"skills": "a"}, true},
		{"invalid_skills_empty_string", map[string]any{"skills": []any{""}}, true},
		{"valid_sync_mode_copy", map[string]any{"sync_mode": "copy"}, false},
		{"invalid_sync_mode", map[string]any{"sync_mode": "bogus"}, true},
		{"valid_paths_table", map[string]any{"paths": map[string]any{"a": "~/x"}}, false},
		{"invalid_paths_table_empty_value", map[string]any{"paths": map[string]any{"a": ""}}, true},
		{"valid_path_list", map[string]any{"path": []any{"~/x"}}, false},
		{"invalid_path_empty_string", map[string]any{"path": ""}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProjectConfig("agent.toml", tc.config)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateProjectConfig(%v) error=%v, wantErr=%v", tc.config, err, tc.wantErr)
			}
		})
	}
}

func TestValidProjectName(t *testing.T) {
	cases := []struct {
		name  string
		valid bool
	}{
		{"my-project", true},
		{"My.Project_1", true},
		{"1project", true},
		{"", false},
		{".leading-dot", true}, // starts with '.' is NOT allowed by the regex (must start alnum) -- verify below
		{"-leading-dash", false},
	}
	for _, tc := range cases {
		got := ValidProjectName(tc.name)
		// ".leading-dot" actually starts with '.', which the pattern
		// [A-Za-z0-9] requires as the FIRST character -- so it must be invalid.
		want := tc.valid
		if tc.name == ".leading-dot" {
			want = false
		}
		if got != want {
			t.Errorf("ValidProjectName(%q) = %v, want %v", tc.name, got, want)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	projDir := filepath.Join(ws, "projects", "myproj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "agent.toml"), []byte(
		"skills = [\"a\"]\nsync_mode = \"link\"\npath = \"~/code/myproj\"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(ws, home, "myproj")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "myproj" {
		t.Errorf("Name = %q", cfg.Name)
	}
	binding := cfg.Binding()
	if len(binding.Entries) != 1 || binding.Entries[0].RawPath != "~/code/myproj" {
		t.Errorf("unexpected binding: %+v", binding)
	}
}

func TestLoadConfigNameMismatch(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	projDir := filepath.Join(ws, "projects", "myproj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "agent.toml"), []byte(
		"name = \"other\"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(ws, home, "myproj"); err == nil {
		t.Fatal("expected an error for mismatched name field")
	}
}
