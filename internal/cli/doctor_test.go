package cli

import (
	"os"
	"path/filepath"
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
