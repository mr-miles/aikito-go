package cli

import "testing"

// Cross-validated against the real Python render.py's _build_generic_table
// via a direct stdout diff of `aikito status` output (see status_test.go /
// the implementation review); this is a focused unit test of the table
// primitive itself.
func TestBuildGenericTableASCII(t *testing.T) {
	headers := []string{"Project", "Instr", "Skills", "Memory", "Context", "Paths", "Mode", "Status"}
	rows := [][]string{
		{"demo-proj", "-", "0", "0", "~0", "1/1", "link", "v"},
	}
	got := buildGenericTable(headers, rows, false, false, []int{0})
	want := "+-----------+-------+--------+--------+---------+-------+------+--------+\n" +
		"| Project   | Instr | Skills | Memory | Context | Paths | Mode | Status |\n" +
		"+-----------+-------+--------+--------+---------+-------+------+--------+\n" +
		"| demo-proj | -     | 0      | 0      | ~0      | 1/1   | link | v      |\n" +
		"+-----------+-------+--------+--------+---------+-------+------+--------+"
	if got != want {
		t.Errorf("buildGenericTable mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestFormatScopeStatusBadge(t *testing.T) {
	cases := []struct {
		status string
		want   string
	}{
		{"OK", "v"},
		{"-", "-"},
		{"OFFLINE", "-"},
		{"! 2 issues", "! 2 issues"},
	}
	for _, tc := range cases {
		if got := formatScopeStatusBadge(tc.status, false, false); got != tc.want {
			t.Errorf("formatScopeStatusBadge(%q) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestFormatTokenEstimate(t *testing.T) {
	cases := []struct {
		tokens int
		want   string
	}{
		{0, "~0"},
		{-5, "~0"},
		{500, "~500"},
		{1500, "~1.5k"},
		{2000, "~2.0k"},
	}
	for _, tc := range cases {
		if got := formatTokenEstimate(tc.tokens); got != tc.want {
			t.Errorf("formatTokenEstimate(%d) = %q, want %q", tc.tokens, got, tc.want)
		}
	}
}

func TestPythonSplitLinesCount(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"one_line_no_newline", "hello", 1},
		{"one_line_with_newline", "hello\n", 1},
		{"two_lines", "a\nb", 2},
		{"two_lines_trailing_newline", "a\nb\n", 2},
		{"crlf", "a\r\nb\r\n", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pythonSplitLinesCount(tc.text); got != tc.want {
				t.Errorf("pythonSplitLinesCount(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}
