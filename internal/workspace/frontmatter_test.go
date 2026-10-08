package workspace

import (
	"encoding/json"
	"os"
	"testing"
)

// testdata/frontmatter_vectors.json comes from the reference
// _parse_markdown_frontmatter (testdata/gen_frontmatter_vectors.py).
func TestParseMarkdownFrontmatterMatchesPython(t *testing.T) {
	data, err := os.ReadFile("testdata/frontmatter_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Content   string   `json:"content"`
		Platforms []string `json:"platforms"`
		Meta      string   `json:"meta"`
		Body      string   `json:"body"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		meta, body := ParseMarkdownFrontmatter(c.Content, c.Platforms)
		if got := CanonicalJSON(meta); got != c.Meta {
			t.Errorf("%q meta:\n got %s\nwant %s", c.Content, got, c.Meta)
		}
		if body != c.Body {
			t.Errorf("%q body: got %q, want %q", c.Content, body, c.Body)
		}
	}
}
