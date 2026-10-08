package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Commands whose options are all implemented print Python's help verbatim,
// with no "(not implemented in this build)" notes. Drives only Run, so it
// also runs against older builds that still annotated these.
func TestRunHelpHasNoNotesForImplementedCommands(t *testing.T) {
	data, err := os.ReadFile("helptext/help.json")
	if err != nil {
		t.Fatal(err)
	}
	var texts map[string]string
	if err := json.Unmarshal(data, &texts); err != nil {
		t.Fatal(err)
	}
	env := testEnv(t)
	for _, path := range []string{"doctor", "show mcp", "show mcps", "show subagents", "show subagent"} {
		var out, errOut bytes.Buffer
		code := Run(append(strings.Fields(path), "--help"), nil, &out, &errOut, env)
		if code != 0 || out.String() != texts[path] {
			t.Errorf("aikito %s --help: exit %d\ngot:\n%s\nwant:\n%s", path, code, out.String(), texts[path])
		}
	}
}
