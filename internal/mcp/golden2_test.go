package mcp

import (
	"os"
	"testing"
)

func loadGolden2(t *testing.T) *OrderedObject {
	t.Helper()
	data, err := os.ReadFile("testdata/mcp_golden2.json")
	if err != nil {
		t.Fatal(err)
	}
	v, err := ParseJSONOrdered(string(data))
	if err != nil {
		t.Fatal(err)
	}
	obj, ok := v.(*OrderedObject)
	if !ok {
		t.Fatal("expected top-level object")
	}
	return obj
}

func TestEntryMatchesDesiredAgainstPython(t *testing.T) {
	golden := loadGolden2(t)
	cases := mustArray(t, golden.GetOr("entry_matches_desired", []any{}))
	for _, c := range cases {
		entry := mustObj(t, c)
		name := getStr(t, entry, "name")
		t.Run(name, func(t *testing.T) {
			currentVal, _ := entry.Get("current")
			var current *OrderedObject
			if currentVal != nil {
				current = mustObj(t, currentVal)
			}
			desired := mustObj(t, entry.GetOr("spec_desired", NewOrderedObject()))
			missingEnv := getStr(t, entry, "missing_env")
			wantResult, _ := entry.GetOr("result", false).(bool)

			spec := AgentSpec{Desired: desired, MissingCredentialEnv: missingEnv}
			got := EntryMatchesDesired(spec, current)
			if got != wantResult {
				t.Errorf("EntryMatchesDesired() = %v, want %v", got, wantResult)
			}
		})
	}
}

func TestRedactMCPEntryAgainstPython(t *testing.T) {
	golden := loadGolden2(t)
	cases := mustArray(t, golden.GetOr("redact", []any{}))
	for i, c := range cases {
		entry := mustObj(t, c)
		input := mustObj(t, entry.GetOr("entry", NewOrderedObject()))
		want := mustObj(t, entry.GetOr("redacted", NewOrderedObject()))
		got := RedactMCPEntry(input)
		if !JSONEqual(got, want) {
			t.Errorf("case %d: RedactMCPEntry mismatch:\n got=%s\nwant=%s", i, DumpIndented(got), DumpIndented(want))
		}
	}
}
