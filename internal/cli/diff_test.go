package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type unifiedDiffVector struct {
	Name string   `json:"name"`
	A    []string `json:"a"`
	B    []string `json:"b"`
	Want string   `json:"want"`
}

// Expected outputs come from Python's difflib.unified_diff, joined and
// rstripped exactly as diff.py's _unified_diff does; regenerate with
// testdata/gen_unified_diff_vectors.py.
func TestUnifiedDiffMatchesPythonDifflib(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "unified_diff_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors []unifiedDiffVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) < 15 {
		t.Fatalf("only %d vectors loaded", len(vectors))
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			got := unifiedDiff(v.A, v.B, "actual: A", "expected: B")
			if got != v.Want {
				t.Errorf("unifiedDiff mismatch vs difflib\n--- got ---\n%s\n--- want (Python) ---\n%s", got, v.Want)
			}
		})
	}
}

func TestFormatRangeUnified(t *testing.T) {
	cases := []struct {
		start, stop int
		want        string
	}{
		{0, 0, "0,0"},
		{3, 3, "3,0"},
		{0, 1, "1"},
		{4, 5, "5"},
		{0, 3, "1,3"},
		{2, 9, "3,7"},
	}
	for _, c := range cases {
		if got := formatRangeUnified(c.start, c.stop); got != c.want {
			t.Errorf("formatRangeUnified(%d,%d) = %q, want %q", c.start, c.stop, got, c.want)
		}
	}
}

func TestSplitKeepEnds(t *testing.T) {
	cases := map[string][]string{
		"":         nil,
		"a":        {"a"},
		"a\n":      {"a\n"},
		"a\nb":     {"a\n", "b"},
		"a\r\nb\n": {"a\r\n", "b\n"},
		"\n\n":     {"\n", "\n"},
	}
	for in, want := range cases {
		got := splitKeepEnds(in)
		if strings.Join(got, "|") != strings.Join(want, "|") || len(got) != len(want) {
			t.Errorf("splitKeepEnds(%q) = %q, want %q", in, got, want)
		}
	}
}

func runCmd(t *testing.T, env Environment, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(args, nil, &out, &errOut, env)
	return out.String(), errOut.String(), code
}

func mustRun(t *testing.T, env Environment, args ...string) string {
	t.Helper()
	out, errOut, code := runCmd(t, env, args...)
	if code != 0 {
		t.Fatalf("%v exited %d: %s", args, code, errOut)
	}
	return out
}

func TestCmdDiffNoDrift(t *testing.T) {
	env := setupMCPWorkspace(t)
	mustRun(t, env, "sync", "mcp")

	if out := mustRun(t, env, "diff"); strings.TrimSpace(out) != "No drift detected." {
		t.Errorf("index view = %q, want \"No drift detected.\"", out)
	}
	if out := mustRun(t, env, "diff", "--all"); strings.TrimSpace(out) != "No drift detected." {
		t.Errorf("--all view = %q", out)
	}
	if out := mustRun(t, env, "diff", "mcp", "claude-code", "weather"); strings.TrimSpace(out) != "No matching drift detected." {
		t.Errorf("mcp view = %q", out)
	}
}

func TestCmdDiffMCPDrift(t *testing.T) {
	env := setupMCPWorkspace(t)
	mustRun(t, env, "sync", "mcp")
	claudeJSON := filepath.Join(env.Home, ".claude.json")
	writeFile(t, claudeJSON, `{"mcpServers": {"weather": {"type": "http", "url": "https://hacked.example.com/mcp"}}}`)

	index := mustRun(t, env, "diff")
	for _, want := range []string{"Drift detected:", "MCP", "  claude-code/weather", "Review details:", "aikito diff mcp <agent> <server>"} {
		if !strings.Contains(index, want) {
			t.Errorf("index view missing %q:\n%s", want, index)
		}
	}
	if strings.Contains(index, "Subagents") {
		t.Errorf("index view lists a Subagents section with no subagent drift:\n%s", index)
	}

	detail := mustRun(t, env, "diff", "mcp", "claude-code", "weather")
	want := "[MCP claude-code/weather]\n" +
		"--- actual: " + claudeJSON + "\n" +
		"+++ expected: mcps/weather.toml\n" +
		"@@ -1,4 +1,4 @@\n" +
		" {\n" +
		"   \"type\": \"http\",\n" +
		"-  \"url\": \"https://hacked.example.com/mcp\"\n" +
		"+  \"url\": \"https://weather.example.com/mcp\"\n" +
		" }\n"
	if detail != want {
		t.Errorf("mcp diff:\n--- got ---\n%s\n--- want ---\n%s", detail, want)
	}

	if out := mustRun(t, env, "diff", "mcp", "claude-code", "other"); strings.TrimSpace(out) != "No matching drift detected." {
		t.Errorf("filter by other server = %q", out)
	}
	if out := mustRun(t, env, "diff", "--all"); !strings.Contains(out, "[MCP claude-code/weather]") {
		t.Errorf("--all missing MCP diff:\n%s", out)
	}
}

// A drift hidden entirely by redaction (only a secret header value
// changed) must still be reported, without ever printing either secret.
func TestCmdDiffMCPRedactedOnlyDrift(t *testing.T) {
	env := setupMCPWorkspace(t)
	aikitoDir, err := env.AikitoDir()
	if err != nil {
		t.Fatal(err)
	}
	const original = "sk-live-ORIGINAL-0123456789"
	const changed = "sk-live-CHANGED-9876543210"
	writeFile(t, filepath.Join(aikitoDir, "mcps", "weather.toml"),
		"transport = \"remote\"\nurl = \"https://weather.example.com/mcp\"\nagents = [\"claude-code\"]\n"+
			"headers = { Authorization = \"Bearer "+original+"\" }\n")
	mustRun(t, env, "sync", "mcp")

	claudeJSON := filepath.Join(env.Home, ".claude.json")
	data, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), original) {
		t.Fatalf("fixture assumption broken: synced config doesn't contain the header value:\n%s", data)
	}
	writeFile(t, claudeJSON, strings.ReplaceAll(string(data), original, changed))

	detail := mustRun(t, env, "diff", "mcp", "claude-code", "weather")
	for _, secret := range []string{original, changed, "sk-live"} {
		if strings.Contains(detail, secret) {
			t.Fatalf("diff output leaked a secret (%q):\n%s", secret, detail)
		}
	}
	for _, want := range []string{"-<redacted value differs>", "+<expected redacted value>", "[MCP claude-code/weather]"} {
		if !strings.Contains(detail, want) {
			t.Errorf("redaction-only diff missing %q:\n%s", want, detail)
		}
	}
	all := mustRun(t, env, "diff", "--all")
	if strings.Contains(all, original) || strings.Contains(all, changed) {
		t.Fatalf("--all leaked a secret:\n%s", all)
	}
}

func TestCmdDiffSubagentDrift(t *testing.T) {
	env := setupMCPWorkspace(t)
	mustRun(t, env, "add", "subagent", "reviewer", "--description", "Reviews code", "--agents", "claude-code")
	mustRun(t, env, "sync", "subagents")

	target := filepath.Join(env.Home, ".claude", "agents", "reviewer.md")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("subagent not synced: %v", err)
	}
	if out := mustRun(t, env, "diff", "subagent", "claude-code", "reviewer"); strings.TrimSpace(out) != "No matching drift detected." {
		t.Errorf("freshly synced subagent shows drift:\n%s", out)
	}

	writeFile(t, target, string(data)+"\nHand-added line.\n")
	detail := mustRun(t, env, "diff", "subagent", "claude-code", "reviewer")
	for _, want := range []string{
		"[Subagent claude-code/reviewer]",
		"--- " + target,
		"+++ subagents/reviewer.md (claude-code)",
		"-Hand-added line.",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("subagent diff missing %q:\n%s", want, detail)
		}
	}
	index := mustRun(t, env, "diff")
	if !strings.Contains(index, "Subagents\n  claude-code/reviewer") || !strings.Contains(index, "aikito diff subagent <agent> <name>") {
		t.Errorf("index view missing subagent entry:\n%s", index)
	}
}

// Argument errors (argparse wording) are checked against Python by
// options_vectors.json (diff_project scenario).

func TestCmdDiffWithoutWorkspace(t *testing.T) {
	env := testEnv(t)
	_, errOut, code := runCmd(t, env, "diff")
	if code != 1 || !strings.Contains(errOut, "[ERROR]") {
		t.Errorf("diff without a workspace: exit %d, stderr %q", code, errOut)
	}
}
