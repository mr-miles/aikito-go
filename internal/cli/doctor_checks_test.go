package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mr-miles/aikito-go/internal/registry"
)

// doctorWorkspace initializes a workspace under a fresh testEnv and returns
// the env plus its resolved workspace directory.
func doctorWorkspace(t *testing.T) (Environment, string) {
	t.Helper()
	env := testEnv(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"init", "workspace"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("init workspace failed: %s", errOut.String())
	}
	dir, err := env.AikitoDir()
	if err != nil {
		t.Fatal(err)
	}
	return env, dir
}

func addBundledAgent(t *testing.T, aikitoDir, name string) {
	t.Helper()
	text, err := registry.BundledAgentTemplateText(name)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(aikitoDir, "agents", name+".toml"), text)
}

// findingsText renders a section's findings one per line as
// "STATUS|message|fixhint" for substring assertions.
func findingsText(s DoctorSection) string {
	var b strings.Builder
	for _, f := range s.Findings {
		b.WriteString(f.Status + "|" + f.Message + "|" + f.FixHint + "\n")
	}
	return b.String()
}

func assertContains(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("expected %q in:\n%s", w, got)
		}
	}
}

// --- cmdDoctor flag parsing and exit codes ---

func TestCmdDoctorFlagErrors(t *testing.T) {
	env, _ := doctorWorkspace(t)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"stale_days_missing_value", []string{"--stale-days"}, "--stale-days requires a value"},
		{"stale_days_not_int", []string{"--stale-days", "abc"}, "--stale-days must be a positive integer"},
		{"stale_days_zero", []string{"--stale-days", "0"}, "--stale-days must be a positive integer"},
		{"color_missing_value", []string{"--color"}, "--color requires a value"},
		{"color_bad_value", []string{"--color", "purple"}, "--color must be one of: auto, always, never"},
		{"unknown_flag", []string{"--bogus"}, "Unknown flag: --bogus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := Run(append([]string{"doctor"}, tc.args...), nil, &out, &errOut, env)
			if code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			assertContains(t, errOut.String(), tc.want)
		})
	}
}

func TestCmdDoctorUninitializedWorkspace(t *testing.T) {
	env := testEnv(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"doctor"}, nil, &out, &errOut, env); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	assertContains(t, errOut.String(), "Aikito workspace directory not found")
}

func TestCmdDoctorJSONShape(t *testing.T) {
	env, _ := doctorWorkspace(t)
	var out, errOut bytes.Buffer
	code := Run([]string{"doctor", "--json"}, nil, &out, &errOut, env)

	if strings.Contains(out.String(), "null") {
		t.Errorf("--json output must use [] not null for empty lists:\n%s", out.String())
	}
	var parsed struct {
		Sections []struct {
			Name     string `json:"name"`
			Findings []struct {
				Status  string `json:"status"`
				Actions []any  `json:"actions"`
			} `json:"findings"`
		} `json:"sections"`
		FailCount int      `json:"fail_count"`
		WarnCount int      `json:"warn_count"`
		Fixes     []string `json:"fixes"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, out.String())
	}
	wantSections := []string{"Symlinks", "Orphans", "LocalState", "Memory", "Drift", "Security",
		"Environment", "Adoption", "ConflictMarkers", "Projects", "Configuration"}
	if len(parsed.Sections) != len(wantSections) {
		t.Fatalf("got %d sections, want %d", len(parsed.Sections), len(wantSections))
	}
	fails, warns := 0, 0
	for i, s := range parsed.Sections {
		if s.Name != wantSections[i] {
			t.Errorf("section %d = %q, want %q", i, s.Name, wantSections[i])
		}
		if s.Findings == nil {
			t.Errorf("section %q has null findings", s.Name)
		}
		for _, f := range s.Findings {
			switch f.Status {
			case "FAIL":
				fails++
			case "WARN":
				warns++
			}
			if f.Actions == nil {
				t.Errorf("section %q finding has null actions", s.Name)
			}
		}
	}
	if parsed.FailCount != fails || parsed.WarnCount != warns {
		t.Errorf("fail_count/warn_count = %d/%d, but findings contain %d/%d", parsed.FailCount, parsed.WarnCount, fails, warns)
	}
	if parsed.Fixes == nil {
		t.Error("fixes must be [] not null")
	}
	if (code == 1) != (fails > 0) {
		t.Errorf("exit = %d with %d FAIL findings; want 1 iff any FAIL", code, fails)
	}
}

func TestCmdDoctorExitCodeTracksFailures(t *testing.T) {
	env := setupMCPWorkspace(t)
	var out, errOut bytes.Buffer
	if code := Run([]string{"sync", "mcp"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("sync mcp failed: %s", errOut.String())
	}
	if code := Run([]string{"sync", "global"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("sync global failed: %s", errOut.String())
	}

	out.Reset()
	errOut.Reset()
	if code := Run([]string{"doctor", "--color", "never"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("clean synced workspace: exit = %d, want 0\n%s", code, out.String())
	}
	assertContains(t, out.String(), "MCP fingerprints OK (1 entries)")

	writeFile(t, filepath.Join(env.Home, ".claude.json"),
		`{"mcpServers": {"weather": {"type": "http", "url": "https://hacked.example.com/mcp"}}}`)
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"doctor", "--color", "never"}, nil, &out, &errOut, env); code != 1 {
		t.Fatalf("drifted workspace: exit = %d, want 1\n%s", code, out.String())
	}
	// render_doctor_report always uses unicode symbols.
	assertContains(t, out.String(),
		"✗ claude-code × weather: managed MCP config differs (unmanaged modification)",
		"→ aikito sync mcp --force")
}

// --- rendering ---

func sampleReport() DoctorReport {
	return DoctorReport{Sections: []DoctorSection{
		{Name: "Memory", Findings: []Finding{ok("all good")}},
		{Name: "ConflictMarkers", Findings: []Finding{
			fail("bad thing", "fix it"),
			{Status: "WARN", Message: "meh", Resource: "skill:x", Source: "src", Reason: "why"},
			warn("other", ""),
		}},
	}}
}

func TestRenderDoctorReportASCII(t *testing.T) {
	got := renderDoctorReport(sampleReport(), false, false)
	assertContains(t, got,
		"+------------------+\n| Memory           |\n+------------------+",
		"  [OK] all good",
		"  [FAIL] bad thing\n    -> fix it",
		"  [WARN] meh\n      Resource: skill:x\n      Source: src\n      Reason: why",
		"Found 1 issue, 2 warnings.",
	)
	if strings.ContainsAny(got, "✓✗⚠╭→\x1b") {
		t.Errorf("ASCII mode must not contain unicode symbols or ANSI escapes:\n%s", got)
	}
}

func TestRenderDoctorReportUnicode(t *testing.T) {
	got := renderDoctorReport(sampleReport(), true, false)
	assertContains(t, got,
		"╭──────────────────╮\n│ Memory           │\n╰──────────────────╯",
		"  ✓ all good",
		"  ✗ bad thing\n    → fix it",
		"  ⚠ meh",
	)
}

func TestRenderDoctorReportSummaryWording(t *testing.T) {
	clean := DoctorReport{Sections: []DoctorSection{{Name: "X", Findings: []Finding{ok("fine")}}}}
	if got := renderDoctorReport(clean, false, false); !strings.HasSuffix(got, "[OK] All checks passed.") {
		t.Errorf("clean summary wrong:\n%s", got)
	}
	twoFails := DoctorReport{Sections: []DoctorSection{{Name: "X", Findings: []Finding{fail("a", ""), fail("b", "")}}}}
	if got := renderDoctorReport(twoFails, false, false); !strings.HasSuffix(got, "Found 2 issues.") {
		t.Errorf("plural fail summary wrong:\n%s", got)
	}
	oneWarn := DoctorReport{Sections: []DoctorSection{{Name: "X", Findings: []Finding{warn("a", "")}}}}
	if got := renderDoctorReport(oneWarn, false, false); !strings.HasSuffix(got, "Found 1 warning.") {
		t.Errorf("single warn summary wrong:\n%s", got)
	}
	if got := renderDoctorReport(twoFails, false, true); !strings.Contains(got, "\x1b[") {
		t.Errorf("color mode should emit ANSI escapes:\n%s", got)
	}
}

// --- checkConfigSyntax ---

func TestCheckConfigSyntaxReportsEachManagedFileKind(t *testing.T) {
	env, dir := doctorWorkspace(t)
	writeFile(t, filepath.Join(dir, "config.toml"), "[memory\n")
	writeFile(t, filepath.Join(dir, "skills.toml"), "skills = [\n")
	if err := os.Remove(filepath.Join(dir, "layout.toml")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "mcps", "bad.toml"), "url = \n")
	writeFile(t, filepath.Join(dir, "projects", "p", "agent.toml"), "path = [\n")

	got := findingsText(checkConfigSyntax(dir, env.Home))
	assertContains(t, got,
		"FAIL|config.toml: TOML parse error — ",
		"FAIL|skills.toml: TOML parse error — ",
		"FAIL|layout.toml: file not found|",
		"FAIL|mcps/bad.toml: TOML parse error — ",
		"FAIL|projects/p/agent.toml: TOML parse error — ",
	)
}

func TestCheckConfigSyntaxValidAndStructuralCases(t *testing.T) {
	env, dir := doctorWorkspace(t)
	addBundledAgent(t, dir, "claude-code")
	got := findingsText(checkConfigSyntax(dir, env.Home))
	assertContains(t, got,
		"OK|config.toml: valid TOML",
		"OK|layout.toml: valid TOML",
		"OK|skills.toml: valid TOML",
		"OK|agents/claude-code.toml: valid TOML",
		// PATH is restricted and no ~/.claude marker exists.
		"OK|agents/claude-code.toml: registered Agent is offline on this host",
		"OK|mcps: directory present (empty)",
	)

	// A file with conflict markers is left to the ConflictMarkers section.
	writeFile(t, filepath.Join(dir, "skills.toml"), "<<<<<<< HEAD\nskills = []\n=======\nskills = [\"x\"]\n>>>>>>> b\n")
	if got := findingsText(checkConfigSyntax(dir, env.Home)); strings.Contains(got, "skills.toml") {
		t.Errorf("conflicted skills.toml should not be reported here:\n%s", got)
	}

	// agents/ missing, mcps/ a file.
	if err := os.RemoveAll(filepath.Join(dir, "agents")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "mcps")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "mcps"), "not a dir")
	assertContains(t, findingsText(checkConfigSyntax(dir, env.Home)),
		"FAIL|agents: directory not found|",
		"FAIL|mcps: path is not a directory|",
	)
}

func TestCheckConfigSyntaxInvalidAgentFile(t *testing.T) {
	env, dir := doctorWorkspace(t)
	writeFile(t, filepath.Join(dir, "agents", "codex.toml"), "[agents.other]\nx = 1\n")
	assertContains(t, findingsText(checkConfigSyntax(dir, env.Home)), "FAIL|agents: ")
}

// --- checkSecurity ---

// secretMCPWorkspace registers an agy server whose headers reference an env
// var: agy_json materializes secrets, so the spec is ContainsSecret and its
// native config file is subject to the credential-permission check.
func secretMCPWorkspace(t *testing.T) (Environment, string, string) {
	t.Helper()
	env, dir := doctorWorkspace(t)
	addBundledAgent(t, dir, "agy")
	writeFile(t, filepath.Join(dir, "mcps", "secret.toml"),
		"transport = \"remote\"\nurl = \"https://s.example.com/mcp\"\nagents = [\"agy\"]\nheaders = { Authorization = \"${DOCTOR_TEST_TOKEN}\" }\n")
	cfg := filepath.Join(env.Home, ".gemini", "config", "mcp_config.json")
	writeFile(t, cfg, "{}\n")
	return env, dir, cfg
}

func TestCheckSecurityCredentialPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	cases := []struct {
		mode   os.FileMode
		secure bool
	}{
		{0o600, true},
		{0o644, false},
		{0o640, false},
		// compat.py's check_credential_permissions requires exactly 0600, so
		// owner-only modes other than 0600 are also insecure. The previous Go
		// check only looked at group/other bits and passed these.
		{0o700, false},
		{0o400, false},
	}
	for _, tc := range cases {
		t.Run(tc.mode.String(), func(t *testing.T) {
			env, dir, cfg := secretMCPWorkspace(t)
			if err := os.Chmod(cfg, tc.mode); err != nil {
				t.Fatal(err)
			}
			got := findingsText(checkSecurity(dir, env.Home))
			if tc.secure {
				assertContains(t, got, "OK|Credential file permissions OK (1 files)|")
				return
			}
			want := fmt.Sprintf("FAIL|Credential file has insecure permissions (0o%o): ~/.gemini/config/mcp_config.json|chmod 600 %q", tc.mode.Perm(), cfg)
			assertContains(t, got, want)
		})
	}
}

func TestCheckSecurityNoSecretFiles(t *testing.T) {
	env, dir := doctorWorkspace(t)
	assertContains(t, findingsText(checkSecurity(dir, env.Home)),
		"OK|No secret-bearing credential config files detected|")
}

func TestCheckSecurityGitignoreCoverage(t *testing.T) {
	env, dir := doctorWorkspace(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "*.swp\n")
	assertContains(t, findingsText(checkSecurity(dir, env.Home)),
		"WARN|Workspace .gitignore may not cover .local/state/ (MCP state & backups)|Add '/.local/' to .gitignore")

	writeFile(t, filepath.Join(dir, ".gitignore"), "/.local/\n")
	assertContains(t, findingsText(checkSecurity(dir, env.Home)), "OK|.gitignore covers .local/state/|")

	if err := os.Remove(filepath.Join(dir, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	if got := findingsText(checkSecurity(dir, env.Home)); strings.Contains(got, ".gitignore") {
		t.Errorf("no .gitignore should produce no .gitignore finding:\n%s", got)
	}
}

// --- checkEnvironment ---

func TestCheckEnvironmentAikitoDir(t *testing.T) {
	env, dir := doctorWorkspace(t)

	t.Setenv("AIKITO_DIR", "")
	assertContains(t, findingsText(checkEnvironment(dir, env.Home)),
		"OK|AIKITO_DIR not set; using configured workspace: ~/aikito|")

	t.Setenv("AIKITO_DIR", dir)
	assertContains(t, findingsText(checkEnvironment(dir, env.Home)), "OK|$AIKITO_DIR → ~/aikito|")

	other := resolvedTempDir(t)
	t.Setenv("AIKITO_DIR", other)
	assertContains(t, findingsText(checkEnvironment(dir, env.Home)),
		"WARN|$AIKITO_DIR ("+other+") resolved to "+other+", but aikito_dir="+dir+"|")
}

func TestCheckEnvironmentAgentCLIs(t *testing.T) {
	env, dir := doctorWorkspace(t)
	addBundledAgent(t, dir, "claude-code")
	addBundledAgent(t, dir, "codex")

	assertContains(t, findingsText(checkEnvironment(dir, env.Home)),
		"WARN|No supported agent CLI found in $PATH (install at least one: claude, codex)|")

	if runtime.GOOS == "windows" {
		return
	}
	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "claude"), "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(bin, "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	got := findingsText(checkEnvironment(dir, env.Home))
	assertContains(t, got, "OK|claude CLI found (claude)|")
	if strings.Contains(got, "No supported agent CLI") {
		t.Errorf("should not warn once a CLI is found:\n%s", got)
	}
}

// --- checkMemory ---

func TestCheckMemoryNamesSubdirsAndWikilinks(t *testing.T) {
	env, dir := doctorWorkspace(t)
	notes := filepath.Join(dir, "memory", "notes")
	writeFile(t, filepath.Join(notes, "Bad_Name.md"), "x\n")
	writeFile(t, filepath.Join(notes, "linker.md"), "see [[target-note]] and [[missing-note|alias]]\n")
	writeFile(t, filepath.Join(notes, "target-note.md"), "here\n")
	mustMkdirAll(t, filepath.Join(notes, "nested"))
	writeFile(t, filepath.Join(dir, "projects", "proj", "memory", "notes", "p-note.md"), "[[nowhere]]\n")

	got := findingsText(checkMemory(dir, env.Home, 0))
	assertContains(t, got,
		"FAIL|Global note 'Bad_Name' has invalid filename: ",
		"|Rename note using 'aikito rename memory Bad_Name <valid-name>'",
		"WARN|Global memory notes subdirectory 'nested/' is not scanned|",
		"FAIL|Global note 'linker' links to [[missing-note]] but that note does not exist|Write notes/missing-note.md if the topic is still worth keeping, or edit linker.md to drop the [[missing-note]] link",
		"FAIL|Project:proj note 'p-note' links to [[nowhere]] but that note does not exist|",
	)
	if strings.Contains(got, "[[target-note]] but") {
		t.Errorf("link to an existing note must not be flagged:\n%s", got)
	}
	// No git history: each note counts as checked but none is stale (doctor.py sets checked_any before the git lookup).
	assertContains(t, got, "OK|No memory notes older than 30 days|")
}

// gitCommitAt commits every file under dir with the given committer date.
func gitCommitAt(t *testing.T, dir, date string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		run("init", "-q")
	}
	run("add", "-A")
	run("commit", "-q", "-m", "c")
}

func TestCheckMemoryStalenessUsesGitHistory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	env, dir := doctorWorkspace(t)
	writeFile(t, filepath.Join(dir, "memory", "notes", "old-note.md"), "old\n")
	gitCommitAt(t, dir, "2020-01-01T00:00:00Z")

	got := findingsText(checkMemory(dir, env.Home, 0))
	assertContains(t, got,
		"WARN|Global note 'old-note' has not been updated in ",
		" days (threshold: 30d) — worth a re-read to confirm it still holds|Re-read notes/old-note.md; rewrite if it drifted, or leave it if it's still accurate",
	)

	// --stale-days override wide enough to cover the note.
	got = findingsText(checkMemory(dir, env.Home, 1000000))
	assertContains(t, got, "OK|No memory notes older than 1000000 days|")

	// Workspace config.toml threshold applies when there's no override.
	writeFile(t, filepath.Join(dir, "config.toml"), "[memory]\nstale_days = 1000000\n")
	assertContains(t, findingsText(checkMemory(dir, env.Home, 0)), "OK|No memory notes older than 1000000 days|")
}

func TestCheckMemoryPerProjectThreshold(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	env, dir := doctorWorkspace(t)
	writeFile(t, filepath.Join(dir, "config.toml"), "[memory]\nstale_days = 1000000\n")
	writeFile(t, filepath.Join(dir, "memory", "notes", "g.md"), "g\n")
	writeFile(t, filepath.Join(dir, "projects", "proj", "agent.toml"), "[memory]\nstale_days = 999999\n")
	writeFile(t, filepath.Join(dir, "projects", "proj", "memory", "notes", "p.md"), "p\n")
	gitCommitAt(t, dir, "2020-01-01T00:00:00Z")

	assertContains(t, findingsText(checkMemory(dir, env.Home, 0)),
		"OK|No memory notes older than configured thresholds (999999, 1000000 days)|")

	writeFile(t, filepath.Join(dir, "projects", "proj", "agent.toml"), "[memory]\nstale_days = 7\n")
	gitCommitAt(t, dir, "2020-01-01T00:00:00Z")
	got := findingsText(checkMemory(dir, env.Home, 0))
	assertContains(t, got, "WARN|Project:proj note 'p' has not been updated in ")
	assertContains(t, got, "(threshold: 7d)")
	if strings.Contains(got, "note 'g'") {
		t.Errorf("global note is within its own threshold:\n%s", got)
	}
}

// --- checkProjects ---

// --- checkSymlinks ---

// --- checkAdoption ---

func TestCheckAdoptionReportsAdoptableMCPEntry(t *testing.T) {
	env := setupAdoptWorkspace(t)
	dir, err := env.AikitoDir()
	if err != nil {
		t.Fatal(err)
	}
	// Messages from doctor.py's check_adoption.
	if got := findingsText(checkAdoption(dir, env.Home)); got != "OK|No external Agent configuration needs adoption|\n" {
		t.Fatalf("clean workspace should report only the OK line, got:\n%s", got)
	}
	writeFile(t, filepath.Join(env.Home, ".claude.json"),
		`{"mcpServers": {"preexisting": {"type": "http", "url": "https://pre.example.com/mcp"}}}`)
	got := findingsText(checkAdoption(dir, env.Home))
	// Python's adopt.pending finding carries actions, not a fix hint.
	if got != "WARN|1 local Agent resource(s) are available to adopt|\n" {
		t.Errorf("got:\n%s", got)
	}
	if strings.Contains(got, "OK|") {
		t.Errorf("OK line must only appear when nothing is reported:\n%s", got)
	}
}

// check_adoption reports adopt's blocking findings as warnings, with adopt's
// own wording.
func TestCheckAdoptionReportsInstructionConflict(t *testing.T) {
	env := setupAdoptWorkspace(t)
	dir, err := env.AikitoDir()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(env.Home, ".claude", "CLAUDE.md"), "# A\n")
	mustMkdirAll(t, filepath.Join(env.Home, ".codex"))
	writeFile(t, filepath.Join(env.Home, ".codex", "AGENTS.md"), "# B\n")
	want := "WARN|Global instructions cannot be adopted automatically|Review and merge the sources into " +
		filepath.Join(dir, "global", "AGENTS.md") + "\n"
	if got := findingsText(checkAdoption(dir, env.Home)); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// --- small helpers ---

func TestHomeRelAndToInt(t *testing.T) {
	// compat.safe_relative_path: "~/" prefix under home.
	if got := homeRel("/h/a/b", "/h"); got != "~/a/b" {
		t.Errorf("homeRel inside = %q", got)
	}
	if got := homeRel("/other/x", "/h"); got != "/other/x" {
		t.Errorf("homeRel outside = %q", got)
	}
	if got := homeRel("/x", ""); got != "/x" {
		t.Errorf("homeRel empty home = %q", got)
	}
	if n, ok2 := toInt(int64(5)); !ok2 || n != 5 {
		t.Error("toInt int64")
	}
	if n, ok2 := toInt(7); !ok2 || n != 7 {
		t.Error("toInt int")
	}
	if _, ok2 := toInt("5"); ok2 {
		t.Error("toInt string must fail")
	}
}
