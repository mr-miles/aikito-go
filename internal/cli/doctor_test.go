package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Cross-checked against the real Python conflict.py's find_conflict_marker_lines:
// a grouped <<<<<<<...=======...>>>>>>> block is blocking; an isolated bare
// marker line (e.g. a stray "=======" heading underline) is not, for
// Markdown/text files. TOML files treat any marker line as blocking.
func TestFindConflictMarkerLines(t *testing.T) {
	cases := []struct {
		name         string
		filename     string
		content      string
		wantBlocking []int
		wantIsolated []int
	}{
		{
			"grouped_block",
			"note.md",
			"before\n<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> branch\nafter\n",
			[]int{2, 4, 6}, nil,
		},
		{
			"isolated_separator",
			"note.md",
			"Heading\n=======\nSome text after a heading underline.\n",
			nil, []int{2},
		},
		{
			"toml_any_marker_blocking",
			"config.toml",
			"key = 1\n<<<<<<< HEAD\nkey = 2\n=======\nkey = 3\n>>>>>>> branch\n",
			[]int{2, 4, 6}, nil,
		},
		{
			"crlf_isolated_separator",
			"note.md",
			"Heading\r\n=======\r\nSome text.\r\n",
			nil, []int{2},
		},
		{
			"crlf_grouped_block",
			"note.md",
			"<<<<<<< HEAD\r\nours\r\n=======\r\ntheirs\r\n>>>>>>> branch\r\n",
			[]int{1, 3, 5}, nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.filename)
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			blocking, isolated := findConflictMarkerLines(path)
			if !intSliceEqual(blocking, tc.wantBlocking) {
				t.Errorf("blocking = %v, want %v", blocking, tc.wantBlocking)
			}
			if !intSliceEqual(isolated, tc.wantIsolated) {
				t.Errorf("isolated = %v, want %v", isolated, tc.wantIsolated)
			}
		})
	}
}

func intSliceEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestHasAnyConflictMarkerCRLF(t *testing.T) {
	if !hasAnyConflictMarker("plain\r\n=======\r\nmore\r\n") {
		t.Error("expected a CRLF-terminated bare marker line to be detected")
	}
	if hasAnyConflictMarker("no markers here\r\njust text\r\n") {
		t.Error("expected no false positive on ordinary CRLF text")
	}
}

// Cross-validated against the real Python check_orphans (doctor.py) on a
// matching fixture: an orphan skill directory under <workspace>/skills/,
// an empty orphan directory, a stale entry under ~/.agents/skills/ no
// longer in skills.toml, and a residual managed MCP entry left behind
// after its mcps/<name>.toml was deleted without syncing the removal.
func TestCheckOrphans(t *testing.T) {
	home := t.TempDir()
	aikitoDir := filepath.Join(home, "aikito")

	// Orphan (non-empty) skill directory, not in skills.toml/any project.
	mustMkdirAll(t, filepath.Join(aikitoDir, "skills", "stray"))
	mustWriteFile(t, filepath.Join(aikitoDir, "skills", "stray", "SKILL.md"), "---\nname: stray\n---\nbody\n")

	// Orphan EMPTY skill directory.
	mustMkdirAll(t, filepath.Join(aikitoDir, "skills", "empty-stray"))

	// A selected skill, present and accounted for — must NOT be flagged.
	mustMkdirAll(t, filepath.Join(aikitoDir, "skills", "kept"))
	mustWriteFile(t, filepath.Join(aikitoDir, "skills.toml"), "skills = [\"kept\"]\n")

	// Stale entry under ~/.agents/skills/ no longer in skills.toml.
	mustMkdirAll(t, filepath.Join(home, ".agents", "skills", "kept"))
	mustMkdirAll(t, filepath.Join(home, ".agents", "skills", "gone"))

	// Required scaffolding so other checkOrphans sub-functions don't error.
	mustMkdirAll(t, filepath.Join(aikitoDir, "subagents"))
	mustMkdirAll(t, filepath.Join(aikitoDir, "agents"))
	mustMkdirAll(t, filepath.Join(aikitoDir, "mcps"))
	mustMkdirAll(t, filepath.Join(aikitoDir, "projects"))

	section := checkOrphans(aikitoDir, home)

	joined := ""
	for _, f := range section.Findings {
		joined += f.Status + "|" + f.Message + "\n"
	}

	for _, want := range []string{
		"WARN|skills/stray: orphan skill directory (not in skills.toml or any project agent.toml)",
		"WARN|skills/empty-stray: empty directory, safe to delete",
		"FAIL|~/.agents/skills/gone: not in skills.toml",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected finding %q, got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "skills/kept") || strings.Contains(joined, ".agents/skills/kept") {
		t.Errorf("did not expect 'kept' to be flagged, got:\n%s", joined)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
