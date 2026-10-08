package sync

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// subagent_stale_vectors.json comes from the reference execute_subagent_plan
// (testdata/gen_subagent_stale_vectors.py): a target file that changes
// between planning and applying must not be overwritten, and the plan fails
// with Python's "Plan is stale: ..." message. Uses only BuildSubagentPlan
// and ApplySubagentPlan, so it also runs against builds without the check.
func TestApplySubagentPlanRefusesStalePlanLikePython(t *testing.T) {
	data, err := os.ReadFile("testdata/subagent_stale_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases map[string]struct {
		Phase, Mutate, Target, Error string
		Success, Unchanged           bool
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/nonexistent")
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			aikitoDir, home := setupSubagentFixture(t)
			def := filepath.Join(aikitoDir, "subagents", "reviewer.md")
			if c.Phase != "create" {
				ops, err := BuildSubagentPlan(aikitoDir, home, BuildSubagentPlanOptions{GateInstalled: true})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := ApplySubagentPlan(ops, home); err != nil {
					t.Fatal(err)
				}
				if c.Phase == "update" {
					body, _ := os.ReadFile(def)
					writeFileT(t, def, strings.Replace(string(body), "carefully", "very carefully", 1))
				} else if err := os.Remove(def); err != nil {
					t.Fatal(err)
				}
			}
			ops, err := BuildSubagentPlan(aikitoDir, home, BuildSubagentPlanOptions{GateInstalled: true, Prune: c.Phase == "prune"})
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(home, c.Target)
			switch c.Mutate {
			case "write":
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					t.Fatal(err)
				}
				f, err := os.OpenFile(target, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
				if err != nil {
					t.Fatal(err)
				}
				f.WriteString("edited by hand\n")
				f.Close()
			case "remove":
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				body, _ := os.ReadFile(target)
				other := filepath.Join(home, "elsewhere.md")
				writeFileT(t, other, string(body))
				os.Remove(target)
				if err := os.Symlink(other, target); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(target)
			msg, err := ApplySubagentPlan(ops, home)
			after, _ := os.ReadFile(target)
			want := strings.ReplaceAll(c.Error, "{H}", home)
			if (err == nil) != c.Success || msg != want {
				t.Errorf("got success=%v msg %q\nwant success=%v msg %q", err == nil, msg, c.Success, want)
			}
			if c.Unchanged && !bytes.Equal(before, after) {
				t.Errorf("target was modified by a stale plan:\nbefore %q\nafter  %q", before, after)
			}
		})
	}
}
