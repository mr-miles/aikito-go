package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// rm_usage_vectors.json is the reference CLI's argparse output for rm and
// remove (testdata/gen_rm_usage_vectors.py). Usage and error lines name the
// spelling the user typed ("aikito remove skill"), never just "rm".
func TestRmUsageErrorsMatchPython(t *testing.T) {
	data, err := os.ReadFile("testdata/rm_usage_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Args   []string `json:"args"`
		Stdout string   `json:"stdout"`
		Stderr string   `json:"stderr"`
		Exit   int      `json:"exit"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	env := testEnv(t)
	for _, c := range cases {
		var out, errOut bytes.Buffer
		code := Run(c.Args, nil, &out, &errOut, env)
		if code != c.Exit || out.String() != c.Stdout || errOut.String() != c.Stderr {
			t.Errorf("aikito %s: exit %d (want %d)\nstderr:\n%s\nwant:\n%s",
				strings.Join(c.Args, " "), code, c.Exit, errOut.String(), c.Stderr)
		}
	}
}
