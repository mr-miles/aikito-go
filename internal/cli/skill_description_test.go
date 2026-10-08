package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// skill_description_vectors.json is generated from the reference
// context_footprint.extract_skill_description
// (testdata/gen_skill_description_vectors.py). The description feeds the
// Context column of `aikito status` and `show project`.
func TestExtractSkillDescriptionMatchesPython(t *testing.T) {
	data, err := os.ReadFile("testdata/skill_description_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		SkillMD     string  `json:"skill_md"`
		Description *string `json:"description"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(c.SkillMD), 0o644); err != nil {
			t.Fatal(err)
		}
		got, ok := extractSkillDescription(dir)
		switch {
		case c.Description == nil && ok:
			t.Errorf("%q: got %q, want none", c.SkillMD, got)
		case c.Description != nil && (!ok || got != *c.Description):
			t.Errorf("%q: got %q (%v), want %q", c.SkillMD, got, ok, *c.Description)
		}
	}
}
