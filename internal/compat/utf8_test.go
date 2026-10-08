package compat

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// utf8_replace_vectors.json is CPython's bytes.decode("utf-8", "replace")
// over crafted and fuzzed inputs (testdata/gen_utf8_replace_vectors.py).
func TestDecodeUTF8ReplaceMatchesCPython(t *testing.T) {
	data, err := os.ReadFile("testdata/utf8_replace_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases [][2]string
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, c := range cases {
		in, _ := hex.DecodeString(c[0])
		if got := DecodeUTF8Replace(in); got != c[1] {
			bad++
			if bad <= 10 {
				t.Errorf("%s: got %q, want %q", c[0], got, c[1])
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d cases differ", bad, len(cases))
	}
}
