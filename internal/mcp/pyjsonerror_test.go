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

// Agent JSON configs report parse errors with json.loads' wording, which
// adopt and sync show to the user (mcp/adapters/jsonc.py).
func TestLoadDocumentReportsJSONErrorsLikePython(t *testing.T) {
	_, err := LoadDocument("claude_json", "{not json")
	want := "Invalid Claude Code JSON config: Expecting property name enclosed in double quotes: line 1 column 2 (char 1)"
	if err == nil || err.Error() != want {
		t.Errorf("got %v, want %q", err, want)
	}
}
