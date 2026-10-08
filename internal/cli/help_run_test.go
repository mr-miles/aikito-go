package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Black-box: drives only Run, reading the expected text straight from the
// Python-captured helptext/help.json, so it also runs against builds that
// predate help support.
func TestRunPrintsPythonHelpForEveryCommand(t *testing.T) {
	data, err := os.ReadFile("helptext/help.json")
	if err != nil {
		t.Fatal(err)
	}
	var texts map[string]string
	if err := json.Unmarshal(data, &texts); err != nil {
		t.Fatal(err)
	}
	env := testEnv(t)
	const note = "  (not implemented in this build)"
	for path, want := range texts {
		if strings.HasPrefix(path, "__") {
			continue
		}
		for _, flag := range []string{"--help", "-h"} {
			args := append(strings.Fields(path), flag)
			var out, errOut bytes.Buffer
			code := Run(args, nil, &out, &errOut, env)
			// Annotated lines carry a suffix; strip it to compare with Python.
			got := strings.ReplaceAll(out.String(), note, "")
			if code != 0 || got != want {
				t.Errorf("aikito %s: exit %d\ngot:\n%s%s\nwant:\n%s", strings.Join(args, " "), code, out.String(), errOut.String(), want)
			}
		}
	}
	var out, errOut bytes.Buffer
	if code := Run(nil, nil, &out, &errOut, env); code != 2 || errOut.String() != texts["__noargs__"] {
		t.Errorf("no args: exit %d, stderr %q", code, errOut.String())
	}
	out.Reset()
	if code := Run([]string{"--debug", "version", "--help"}, nil, &out, &errOut, env); code != 0 ||
		!strings.Contains(out.String(), "usage: aikito version") {
		t.Errorf("--debug version --help: exit %d, out %q", code, out.String())
	}
}
