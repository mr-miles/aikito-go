package mcp

import (
	"encoding/json"
	"os"
	"testing"
)

// testdata/pyjson_errors.json holds CPython json.loads messages, generated
// by testdata/gen_pyjson_errors.py.
func TestPythonJSONDecodeErrorMatchesCPython(t *testing.T) {
	data, err := os.ReadFile("testdata/pyjson_errors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases [][2]string
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, c := range cases {
		if got := PythonJSONDecodeError(c[0]); got != c[1] {
			bad++
			if bad <= 20 {
				t.Errorf("%q:\n got %q\nwant %q", c[0], got, c[1])
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d cases differ", bad, len(cases))
	}
}
