//go:build !windows

package linkplan

import "testing"

// Path(dir) / raw keeps ".." (pathlib never collapses it); a symlink's
// destination is shown that way in conflict messages.
func TestPathlibJoinKeepsDotDot(t *testing.T) {
	cases := map[[2]string]string{
		{"/h/.agents/skills", "../../aikito/skills/x"}: "/h/.agents/skills/../../aikito/skills/x",
		{"/h/a", "./b//c/."}:                           "/h/a/b/c",
		{"/h/a", "/abs//x"}:                            "/abs/x",
	}
	for in, want := range cases {
		if got := pathlibJoin(in[0], in[1]); got != want {
			t.Errorf("pathlibJoin(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}
