package workspace

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// strictjson_vectors.json is CPython's json.loads with layout.py's
// object_pairs_hook (reject duplicates) and parse_constant (reject
// NaN/Infinity), over crafted and fuzzed inputs
// (testdata/gen_strictjson_vectors.py). The first problem in scan order
// decides the outcome, as in Python.
func TestDecodeStrictJSONMatchesCPython(t *testing.T) {
	data, err := os.ReadFile("testdata/strictjson_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Text    string
		Outcome []string
	}
	var raw [][]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, r := range raw {
		var c struct {
			Text    string
			Outcome []string
		}
		_ = json.Unmarshal(r[0], &c.Text)
		_ = json.Unmarshal(r[1], &c.Outcome)
		cases = append(cases, c)
	}
	bad := 0
	for _, c := range cases {
		v, err := DecodeStrictJSON(c.Text)
		var got []string
		var dup *DuplicateKeyError
		var cst *ConstantError
		switch {
		case errors.As(err, &dup):
			got = []string{"dup"}
		case errors.As(err, &cst):
			got = []string{"const", cst.Constant}
		case err != nil:
			got = []string{"error"}
		default:
			got = []string{"ok", CanonicalJSON(v)}
		}
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(c.Outcome)
		if string(g) != string(w) {
			bad++
			if bad <= 15 {
				t.Errorf("%q: got %s, want %s", c.Text, g, w)
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d cases differ", bad, len(cases))
	}
}
