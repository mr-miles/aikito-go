//go:build e2e || e2e_generate

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Project-sync scenarios: scripted command sequences whose complete
// transcript (stdout, stderr, exit status, with $HOME redacted) and final
// home tree are captured from Python by generate_projectsync_test.go and
// replayed against the Go binary by projectsync_test.go.

// psCLI runs one aikito invocation in dir (relative to home) and returns
// its result.
type psCLI func(t *testing.T, home, dir string, args ...string) runResult

func runBinaryIn(t *testing.T, binary, home, dir string, extraEnv []string, args ...string) runResult {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = filepath.Join(home, dir)
	cmd.Env = append([]string{"HOME=" + home, "PATH=" + testPATH}, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running %s %v: %v", binary, args, err)
		}
		code = ee.ExitCode()
	}
	return runResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: code}
}

// psEnv is what a scenario step can touch.
type psEnv struct {
	t          *testing.T
	home       string
	cli        psCLI
	transcript strings.Builder
}

func (e *psEnv) path(rel string) string { return filepath.Join(e.home, filepath.FromSlash(rel)) }

// run executes aikito in cwd (relative to home, "" for home) and records
// the result. Arguments may use "{H}" for the home directory.
func (e *psEnv) run(cwd string, args ...string) {
	e.t.Helper()
	expanded := make([]string, len(args))
	for i, a := range args {
		expanded[i] = strings.ReplaceAll(a, "{H}", e.home)
	}
	r := e.cli(e.t, e.home, cwd, expanded...)
	fmt.Fprintf(&e.transcript, "$ (%s) aikito %s\n", cwd, strings.Join(args, " "))
	fmt.Fprintf(&e.transcript, "--- stdout\n%s--- stderr\n%s--- exit %d\n", r.Stdout, r.Stderr, r.ExitCode)
}

func (e *psEnv) write(rel, content string) {
	e.t.Helper()
	p := e.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *psEnv) appendTo(rel, content string) {
	e.t.Helper()
	f, err := os.OpenFile(e.path(rel), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		e.t.Fatal(err)
	}
}

func (e *psEnv) mkdir(rels ...string) {
	e.t.Helper()
	for _, rel := range rels {
		if err := os.MkdirAll(e.path(rel), 0o755); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *psEnv) remove(rel string) {
	e.t.Helper()
	if err := os.RemoveAll(e.path(rel)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *psEnv) skill(name string) {
	e.write("aikito/skills/"+name+"/SKILL.md", fmt.Sprintf("---\nname: %s\ndescription: test skill %s\n---\n\n# %s\n", name, name, name))
}

var (
	syncModeLine = regexp.MustCompile(`(?m)^sync_mode = .*$`)
	skillsLine   = regexp.MustCompile(`(?m)^skills = .*$`)
)

// setSkills rewrites a project's sync_mode and skills lines.
func (e *psEnv) setSkills(project, mode string, skills ...string) {
	e.t.Helper()
	p := e.path("aikito/projects/" + project + "/agent.toml")
	data, err := os.ReadFile(p)
	if err != nil {
		e.t.Fatal(err)
	}
	quoted := make([]string, len(skills))
	for i, s := range skills {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	s := syncModeLine.ReplaceAllString(string(data), fmt.Sprintf("sync_mode = %q", mode))
	s = skillsLine.ReplaceAllString(s, "skills = ["+strings.Join(quoted, ", ")+"]")
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

type psScenario struct {
	name  string
	steps func(e *psEnv)
}

var projectSyncScenarios = []psScenario{
	{"init", func(e *psEnv) {
		e.mkdir("p1", "p2")
		e.run("", "init", "project", "p1", "{H}/p1")
		e.run("", "init", "project", "p1", "{H}/p2")
		e.run("", "init", "project", "p1", "{H}/p1")
		e.run("", "sync", "project", "p1", "--dry-run")
		e.run("p1", "sync", "project")
		e.run("p1", "sync", "project", ".")
		e.run("", "sync", "project")
		e.run("", "sync", "project", ".")
		e.run("", "sync", "project", "nosuch")
		e.run("", "sync", "project", "p1,p1", "{H}/p1")
		e.run("", "sync", "project", "--prune")
		e.run("", "sync", "project", "--force=1", "p1")
	}},
	{"link", func(e *psEnv) {
		e.mkdir("p1")
		e.run("", "init", "project", "p1", "{H}/p1")
		e.skill("alpha")
		e.skill("beta")
		e.setSkills("p1", "link", "alpha", "beta")
		e.run("", "sync", "project", "p1", "--dry-run")
		e.run("", "sync", "project", "p1")
		e.run("", "sync", "project", "p1")
		e.setSkills("p1", "link", "alpha")
		e.run("", "sync", "project", "p1", "--dry-run")
		e.run("", "sync", "project", "p1")
		e.setSkills("p1", "link", "alpha", "ghost")
		e.run("", "sync", "project", "p1")
	}},
	{"copy", func(e *psEnv) {
		e.mkdir("p1")
		e.run("", "init", "project", "p1", "{H}/p1")
		e.skill("alpha")
		e.skill("beta")
		e.setSkills("p1", "copy", "alpha", "beta")
		e.run("", "sync", "project", "p1")
		e.run("", "sync", "project", "p1")
		e.appendTo("p1/.agents/skills/alpha/SKILL.md", "hand edit\n")
		e.run("", "sync", "project", "p1")
		e.run("", "sync", "project", "p1", "--dry-run")
		e.run("", "sync", "project", "p1", "--force")
		e.appendTo("aikito/skills/beta/SKILL.md", "canon edit\n")
		e.run("", "sync", "project", "p1")
		e.setSkills("p1", "copy", "alpha")
		e.run("", "sync", "project", "p1")
		e.setSkills("p1", "copy", "alpha", "beta")
		e.run("", "sync", "project", "p1")
		e.run("", "sync", "project", "p1", "--force")
		e.setSkills("p1", "link", "alpha", "beta")
		e.run("", "sync", "project", "p1")
	}},
	{"unmanaged", func(e *psEnv) {
		e.skill("alpha")
		e.write("p1/.agents/skills/alpha/SKILL.md", "---\nname: alpha\ndescription: test skill alpha\n---\n\n# alpha\n")
		e.write("p1/.agents/skills/own/SKILL.md", "mine\n")
		e.run("", "init", "project", "p1", "{H}/p1")
		e.setSkills("p1", "copy", "alpha")
		e.run("", "sync", "project", "p1")
		e.run("", "sync", "project", "p1", "--force")
		e.write("p1/.agents/skills/alpha/extra.md", "different\n")
		e.setSkills("p1", "link", "alpha")
		e.run("", "sync", "project", "p1")
	}},
	{"instructions", func(e *psEnv) {
		e.write("p1/.claude/CLAUDE.md", "theirs\n")
		e.run("", "init", "project", "p1", "{H}/p1")
		e.write("aikito/projects/p1/AGENTS.md", "# Real instructions\n")
		e.run("", "sync", "project", "p1")
		e.remove("p1/.claude/CLAUDE.md")
		e.run("", "sync", "project", "p1", "--dry-run")
		e.run("", "sync", "project", "p1")
		e.write("aikito/projects/p1/AGENTS.md", "")
		e.run("", "sync", "project", "p1")
		e.write("aikito/projects/p2/agent.toml", "name = \"p2\"\npath = \"~/p2\"\nsync_mode = \"link\"\nskills = []\n")
		e.write("aikito/projects/p2/AGENTS.md", "# p2 instructions\n")
		e.write("p2/.claude/CLAUDE.md", "theirs\n")
		e.run("", "init", "project", "p2", "{H}/p2")
	}},
	{"paths_and_memory", func(e *psEnv) {
		e.mkdir("p1", "p1b", "p1/.agents/memory")
		e.run("", "init", "project", "p1", "{H}/p1")
		e.skill("alpha")
		e.setSkills("p1", "link", "alpha")
		e.run("p1", "sync", "project", "p1", "../p1b")
		e.run("", "sync", "project", "p1")
		e.write("aikito/memory/shared.md", "shared\n")
		e.appendTo("aikito/projects/p1/agent.toml", "memory = [\"shared.md\", \"missing.md\"]\n")
		e.run("", "sync", "project", "p1")
		e.write("p1/.agents/memory/stray.md", "stray\n")
		e.run("", "sync", "project", "p1", "--dry-run")
		e.remove("p1b")
		e.remove("p1")
		e.run("", "sync", "project", "p1")
	}},
}

var stateFileRe = regexp.MustCompile(`^\.local/state/aikito/project-skills/[0-9a-f]{64}\.json$`)

// runProjectSyncScenario runs one scenario in a fresh home (with a Claude
// Code marker and an initialized workspace) and returns its transcript and
// home tree. State file names are binding hashes of absolute paths, so
// they are re-keyed by the checkout they describe.
func runProjectSyncScenario(t *testing.T, sc psScenario, cli psCLI) map[string]treeEntry {
	t.Helper()
	home := resolvedTempDir(t)
	withMarkerDir(t, home, ".claude")
	if r := cli(t, home, "", "init", "workspace"); r.ExitCode != 0 {
		t.Fatalf("init workspace: %s", r.Stderr)
	}
	e := &psEnv{t: t, home: home, cli: cli}
	sc.steps(e)

	tree := treeManifest(t, home, home)
	out := map[string]treeEntry{}
	for k, v := range tree {
		switch {
		case k == "aikito/.git" || strings.HasPrefix(k, "aikito/.git/"),
			// Bundled skills are covered by the init_workspace golden.
			strings.HasPrefix(k, "aikito/skills/aikito"), strings.HasPrefix(k, "aikito/skills/durable-memory"):
			continue
		case stateFileRe.MatchString(k):
			var doc map[string]any
			if err := json.Unmarshal(v.content, &doc); err != nil {
				t.Fatalf("state file %s: %v", k, err)
			}
			co, _ := doc["physical_checkout"].(string)
			rel, _ := filepath.Rel(home, co)
			k = ".local/state/aikito/project-skills/state-of-" + strings.ReplaceAll(filepath.ToSlash(rel), "/", "_") + ".json"
		}
		if !v.isDir && !v.isSymlink {
			v.content = []byte(strings.ReplaceAll(string(v.content), home, "<HOME>"))
		}
		out[k] = v
	}
	out["transcript.txt"] = treeEntry{content: []byte(strings.ReplaceAll(e.transcript.String(), home, "<HOME>"))}
	return out
}
