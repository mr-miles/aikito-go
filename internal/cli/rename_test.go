package cli

import "testing"

// Cross-validated against the real Python rename_memory_note directly: the
// wikilink boundary (?=[|#\]]) lookahead must reject "old-namex"/"old-names"
// while accepting the three legal forms ([[x]], [[x|alias]], [[x#heading]]).
func TestRewriteWikilinks(t *testing.T) {
	content := "See [[old-name]] and [[old-name|Aliased]] and [[old-name#Section]] " +
		"but not [[old-namex]] or [[old-names]].\n"
	want := "See [[new-name]] and [[new-name|Aliased]] and [[new-name#Section]] " +
		"but not [[old-namex]] or [[old-names]].\n"

	got, n := rewriteWikilinks(content, "old-name", "new-name")
	if got != want {
		t.Errorf("rewriteWikilinks() = %q, want %q", got, want)
	}
	if n != 3 {
		t.Errorf("rewriteWikilinks() substitutions = %d, want 3", n)
	}
}

func TestRewriteWikilinksNoMatch(t *testing.T) {
	content := "No wikilinks here.\n"
	got, n := rewriteWikilinks(content, "old-name", "new-name")
	if got != content || n != 0 {
		t.Errorf("rewriteWikilinks() = %q, %d, want unchanged, 0", got, n)
	}
}
