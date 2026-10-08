package workspace

import (
	"encoding/json"
	"os"
	"testing"
)

// frontmatter_update_vectors.json is generated from the reference
// implementation by testdata/gen_frontmatter_update_vectors.py.
func TestUpdateMarkdownFrontmatterMatchesPython(t *testing.T) {
	data, err := os.ReadFile("testdata/frontmatter_update_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Updates []struct {
			Content string      `json:"content"`
			Updates [][2]string `json:"updates"`
			Want    string      `json:"want"`
		} `json:"updates"`
		Scalars [][2]string `json:"scalars"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Updates {
		var ups []FrontmatterUpdate
		for _, u := range c.Updates {
			ups = append(ups, FrontmatterUpdate{u[0], u[1]})
		}
		if got := UpdateMarkdownFrontmatter(c.Content, ups); got != c.Want {
			t.Errorf("%q %v:\n got %q\nwant %q", c.Content, c.Updates, got, c.Want)
		}
	}
	for _, s := range v.Scalars {
		if got := FormatYAMLScalar(s[0]); got != s[1] {
			t.Errorf("FormatYAMLScalar(%q) = %q, want %q", s[0], got, s[1])
		}
	}
}
