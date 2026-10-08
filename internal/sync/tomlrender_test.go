package sync

import (
	"testing"

	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// Cross-validated against the real Python _adopt_field
// (aikito.workspace.toml_render), not hand-derived.
func TestAdoptFieldAgainstPython(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		sections []string
		key      string
		value    any
		want     string
	}{
		{"no_section_new_key", "", nil, "stale_days", int64(30), "stale_days = 30\n"},
		{"existing_key_with_comment", "[memory]\nstale_days = 10 # old value\nother = 1\n", []string{"memory"}, "stale_days", int64(30), "[memory]\nstale_days = 30 # old value\nother = 1\n"},
		{"new_key_in_existing_section", "[memory]\nother = 1\n", []string{"memory"}, "stale_days", int64(30), "[memory]\nother = 1\nstale_days = 30\n"},
		{"new_section", "[other]\nx = 1\n", []string{"memory"}, "stale_days", int64(30), "[other]\nx = 1\n\n[memory]\nstale_days = 30\n"},
		{"top_level_existing", "foo = 1\nbar = 2\n", nil, "foo", int64(99), "foo = 99\nbar = 2\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AdoptField(tc.text, tc.sections, tc.key, tc.value)
			if got != tc.want {
				t.Errorf("AdoptField() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Cross-validated against the real Python update_skills_in_toml.
func TestUpdateSkillsInTomlAgainstPython(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		skills []string
		want   string
	}{
		{"no_skills_field", "", []string{"a", "b"}, "skills = [\n    \"a\",\n    \"b\"\n]\n"},
		{"existing_bracket_list", "skills = [\n    \"x\"\n]\nother = 1\n", []string{"a", "b"}, "skills = [\n    \"a\",\n    \"b\"\n]\nother = 1\n"},
		{"existing_single_line", "skills = [\"x\"]\n", []string{"a", "b"}, "skills = [\n    \"a\",\n    \"b\"\n]\n"},
		{"with_table_header", "[memory]\nstale_days = 5\n", []string{"a", "b"}, "skills = [\n    \"a\",\n    \"b\"\n]\n\n[memory]\nstale_days = 5\n"},
		{"empty_skills", "", nil, "skills = []\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UpdateSkillsInToml(tc.text, tc.skills)
			if got != tc.want {
				t.Errorf("UpdateSkillsInToml() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatTomlKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"stale_days", "stale_days"},
		{"my-key", "my-key"},
		{"has space", `"has space"`},
		{"", `""`},
	}
	for _, tc := range cases {
		if got := FormatTomlKey(tc.in); got != tc.want {
			t.Errorf("FormatTomlKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDocumentValuesFlattenAndRemove(t *testing.T) {
	doc, err := workspace.DecodeTOML([]byte("[memory]\nstale_days = 30\n\n[inbox]\npath = \"inbox\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	values := DocumentValues(doc, nil)
	if len(values) != 2 {
		t.Fatalf("expected 2 flattened values, got %d: %v", len(values), values)
	}
	v, ok := values["memory.stale_days"]
	if !ok {
		t.Fatalf("expected memory.stale_days in %v", values)
	}
	if v.Value != int64(30) {
		t.Errorf("memory.stale_days = %v, want 30", v.Value)
	}

	removeTomlValue(doc, []string{"memory", "stale_days"})
	if _, ok := doc["memory"]; ok {
		t.Errorf("expected empty 'memory' table to be pruned, got %v", doc)
	}
	if _, ok := doc["inbox"]; !ok {
		t.Errorf("expected 'inbox' table to survive pruning, got %v", doc)
	}
}

func TestRenderSyncFileConfigUpdateAndDelete(t *testing.T) {
	expected := map[string]workspace.Resource{}
	text := "[memory]\nstale_days = 10\n\n[inbox]\npath = \"inbox\"\n"

	fp := workspace.ValueFingerprint(int64(30))
	items := []ResourceWrite{
		{RelativePath: "config.toml", Kind: "config", Name: "memory.stale_days", Fingerprint: &fp},
	}
	values := map[string]TomlValue{
		"config:memory.stale_days": {Path: []string{"memory", "stale_days"}, Value: int64(30)},
	}

	got, err := RenderSyncFile(text, items, values, expected)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	want := "[memory]\nstale_days = 30\n\n[inbox]\npath = \"inbox\"\n"
	// Re-parse both and compare semantically (key order reconstruction is
	// best-effort; the important thing is the document round-trips to the
	// same values).
	gotDoc, err := workspace.DecodeTOML([]byte(*got))
	if err != nil {
		t.Fatalf("result is not valid TOML: %v\n%s", err, *got)
	}
	wantDoc, _ := workspace.DecodeTOML([]byte(want))
	if workspace.ValueFingerprint(gotDoc) != workspace.ValueFingerprint(wantDoc) {
		t.Errorf("RenderSyncFile() = %q (parsed %v), want semantically %q (parsed %v)", *got, gotDoc, want, wantDoc)
	}
}

func TestRenderSyncFileDeletesField(t *testing.T) {
	expected := map[string]workspace.Resource{}
	text := "[memory]\nstale_days = 10\n"
	items := []ResourceWrite{
		{RelativePath: "config.toml", Kind: "config", Name: "memory.stale_days", Fingerprint: nil},
	}
	got, err := RenderSyncFile(text, items, nil, expected)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	doc, err := workspace.DecodeTOML([]byte(*got))
	if err != nil {
		t.Fatalf("result is not valid TOML: %v\n%s", err, *got)
	}
	if len(doc) != 0 {
		t.Errorf("expected empty document after deleting the only field, got %v (text %q)", doc, *got)
	}
}

func TestRenderSyncFileSkillsSelection(t *testing.T) {
	expected := map[string]workspace.Resource{
		"skill-selection:alpha": {Kind: "skill-selection", Name: "alpha", Parts: []workspace.ResourcePart{{Path: "skills.toml"}}},
		"skill-selection:beta":  {Kind: "skill-selection", Name: "beta", Parts: []workspace.ResourcePart{{Path: "skills.toml"}}},
	}
	items := []ResourceWrite{
		{RelativePath: "skills.toml", Kind: "skill-selection", Name: "beta", Fingerprint: strp("")},
	}
	got, err := RenderSyncFile("skills = [\"alpha\"]\n", items, nil, expected)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := workspace.DecodeTOML([]byte(*got))
	if err != nil {
		t.Fatalf("result not valid TOML: %v\n%s", err, *got)
	}
	skills, _ := doc["skills"].([]any)
	if len(skills) != 2 || skills[0] != "alpha" || skills[1] != "beta" {
		t.Errorf("skills = %v, want [alpha beta]", skills)
	}
}
