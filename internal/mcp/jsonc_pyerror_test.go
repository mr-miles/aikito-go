package mcp

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Agent JSON configs report parse errors with json.loads' wording, which
// adopt and sync show to the user (mcp/adapters/jsonc.py). Uses only
// LoadDocument, with CPython's messages from testdata/pyjson_errors.json
// (testdata/gen_pyjson_errors.py), so it also runs against builds that
// predate the Python-style messages.
func TestLoadDocumentReportsJSONErrorsLikePython(t *testing.T) {
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
		// Valid JSON, or blank text (which Python reads as an empty config).
		if c[1] == "" || strings.TrimSpace(c[0]) == "" {
			continue
		}
		_, err := LoadDocument("claude_json", c[0])
		want := "Invalid Claude Code JSON config: " + c[1]
		if err == nil || err.Error() != want {
			bad++
			if bad <= 10 {
				t.Errorf("%q:\n got %v\nwant %q", c[0], err, want)
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d cases differ", bad, len(cases))
	}
}
