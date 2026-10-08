package compat

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"testing"
)

// float_repr_vectors.json is CPython repr(float) over crafted and random
// values (testdata/gen_float_repr_vectors.py).
func TestPyFloatReprMatchesCPython(t *testing.T) {
	data, err := os.ReadFile("testdata/float_repr_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases [][2]string
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, c := range cases {
		raw, _ := hex.DecodeString(c[0])
		f := math.Float64frombits(binary.BigEndian.Uint64(raw))
		if got := PyFloatRepr(f); got != c[1] {
			bad++
			if bad <= 10 {
				t.Errorf("%v: got %q, want %q", f, got, c[1])
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d cases differ", bad, len(cases))
	}
}
