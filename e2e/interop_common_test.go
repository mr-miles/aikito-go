//go:build e2e || e2e_generate

package e2e

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mr-miles/aikito-go/internal/project"
	"github.com/mr-miles/aikito-go/internal/projectsync"
)

// Interoperability: the Go port and the reference Python CLI must read and
// write the same workspace and runtime state, so a user can switch between
// them at any point. Each scenario builds a home with one implementation,
// snapshots it (HOME made relative, binding hashes and timestamps made
// symbolic), restores the snapshot into a fresh HOME and runs a fixed set
// of read and write commands there. See e2e/README.md.

// ioSnapshot is a home tree made relocatable: file contents and symlink
// targets carry "<HOME>" for the home directory, project-skill binding
// hashes (which hash absolute paths) carry "<BINDING:project:checkout>",
// and backup timestamps are fixed.
type ioSnapshot struct {
	Entries map[string]ioEntry `json:"entries"`
}

type ioEntry struct {
	Kind    string `json:"kind"` // "file" | "dir" | "symlink"
	Mode    string `json:"mode,omitempty"`
	Target  string `json:"target,omitempty"`
	Content string `json:"-"`
}

var (
	// adopt_YYYYMMDD_HHMMSS, and the timestamp prefixes of subagent/bundled
	// backups; normalised so snapshots from different runs compare equal.
	ioStampRe = regexp.MustCompile(`\d{8}[_T-]\d{6}(?:[._-]?\d{1,6})?`)
)

const ioStamp = "20260101_000000"

// ioMtime is every restored file's modification time; commands run with
// TZ=UTC (ioEnv) so listings that print it are stable.
var ioMtime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

var ioEnv = []string{"TZ=UTC"}

var ioBundledSkillRe = regexp.MustCompile(`^aikito/skills/(aikito|durable-memory)(/|$)`)

// ioBindings lists every project's possible binding hash under home, so
// snapshots can replace them with a symbolic name and restores can put the
// hash for the new home back.
func ioBindings(t *testing.T, home string) map[string]string {
	t.Helper()
	ws := filepath.Join(home, "aikito")
	out := map[string]string{}
	entries, _ := os.ReadDir(filepath.Join(ws, "projects"))
	for _, e := range entries {
		cfg, err := project.LoadConfig(ws, home, e.Name())
		if err != nil {
			continue
		}
		for _, pe := range cfg.Binding().Entries {
			rel, err := filepath.Rel(home, pe.ResolvedPath)
			if err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			out[projectsync.BindingHash(ws, e.Name(), pe.ResolvedPath)] =
				fmt.Sprintf("<BINDING:%s:%s>", e.Name(), filepath.ToSlash(rel))
		}
	}
	return out
}

var ioBindingRe = regexp.MustCompile(`<BINDING:([^:>]+):([^>]+)>`)

func ioSymbolic(s, home string, bindings map[string]string) string {
	for hash, sym := range bindings {
		s = strings.ReplaceAll(s, hash, sym)
	}
	s = strings.ReplaceAll(s, home, "<HOME>")
	return ioStampRe.ReplaceAllString(s, ioStamp)
}

func ioConcrete(s, home string) string {
	s = ioBindingRe.ReplaceAllStringFunc(s, func(m string) string {
		p := ioBindingRe.FindStringSubmatch(m)
		return projectsync.BindingHash(filepath.Join(home, "aikito"), p[1], filepath.Join(home, filepath.FromSlash(p[2])))
	})
	return strings.ReplaceAll(s, "<HOME>", home)
}

// ioCapture snapshots home. The workspace's .git internals are left out
// (not byte-stable); restore re-creates an empty repository instead.
func ioCapture(t *testing.T, home string) ioSnapshot {
	t.Helper()
	bindings := ioBindings(t, home)
	snap := ioSnapshot{Entries: map[string]ioEntry{}}
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(home, path)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "aikito/.git" {
			snap.Entries[rel] = ioEntry{Kind: "dir"}
			return filepath.SkipDir
		}
		key := ioSymbolic(rel, home, bindings)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		mode := fmt.Sprintf("%o", info.Mode().Perm())
		if ioBundledSkillRe.MatchString(rel) {
			// Python copies its installed package's modes (664 in a
			// group-writable checkout); Go writes 644. A documented
			// difference, so bundled skill modes aren't compared.
			mode = ""
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			snap.Entries[key] = ioEntry{Kind: "symlink", Target: ioSymbolic(target, home, bindings)}
		case info.IsDir():
			snap.Entries[key] = ioEntry{Kind: "dir", Mode: mode}
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snap.Entries[key] = ioEntry{Kind: "file", Mode: mode,
				Content: ioSymbolic(string(data), home, bindings)}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", home, err)
	}
	return snap
}

// ioRestore materialises snap under home (which must be empty).
func ioRestore(t *testing.T, snap ioSnapshot, home string) {
	t.Helper()
	keys := make([]string, 0, len(snap.Entries))
	for k := range snap.Entries {
		keys = append(keys, k)
	}
	sort.Strings(keys) // parents before children
	var dirModes []string
	for _, k := range keys {
		e := snap.Entries[k]
		p := filepath.Join(home, filepath.FromSlash(ioConcrete(k, home)))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		switch e.Kind {
		case "dir":
			if k == "aikito/.git" {
				cmd := exec.Command("git", "init", "-q", filepath.Dir(p))
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git init: %v\n%s", err, out)
				}
				continue
			}
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			dirModes = append(dirModes, k)
		case "symlink":
			if err := os.Symlink(filepath.FromSlash(ioConcrete(e.Target, home)), p); err != nil {
				t.Fatal(err)
			}
		case "file":
			var mode uint64 = 0o644
			fmt.Sscanf(e.Mode, "%o", &mode)
			if err := os.WriteFile(p, []byte(ioConcrete(e.Content, home)), os.FileMode(mode)); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, os.FileMode(mode)); err != nil {
				t.Fatal(err)
			}
			// Listings (show inbox, memory) print modification times.
			if err := os.Chtimes(p, ioMtime, ioMtime); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Directory modes last, deepest first, so a 0700 parent doesn't stop
	// its children being created.
	for i := len(dirModes) - 1; i >= 0; i-- {
		k := dirModes[i]
		var mode uint64 = 0o755
		fmt.Sscanf(snap.Entries[k].Mode, "%o", &mode)
		_ = os.Chmod(filepath.Join(home, filepath.FromSlash(ioConcrete(k, home))), os.FileMode(mode))
	}
}

// --- persistence: testdata/interop/<scenario>/<name>/{snapshot.json,files/} ---

func ioDir(scenario, name string) string {
	return filepath.Join("testdata", "interop", scenario, name)
}

func ioSave(t *testing.T, snap ioSnapshot, scenario, name string) {
	t.Helper()
	dir := ioDir(scenario, name)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	for k, e := range snap.Entries {
		if e.Kind != "file" {
			continue
		}
		p := filepath.Join(dir, "files", filepath.FromSlash(k))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(e.Content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := json.MarshalIndent(snap, "", " ")
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ioLoad(t *testing.T, scenario, name string) ioSnapshot {
	t.Helper()
	dir := ioDir(scenario, name)
	data, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		t.Fatalf("loading interop fixture %s/%s: %v (regenerate: see e2e/README.md)", scenario, name, err)
	}
	var snap ioSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	for k, e := range snap.Entries {
		if e.Kind == "file" {
			content, err := os.ReadFile(filepath.Join(dir, "files", filepath.FromSlash(k)))
			if err != nil {
				t.Fatal(err)
			}
			e.Content = string(content)
			snap.Entries[k] = e
		}
	}
	return snap
}

func ioSaveText(t *testing.T, scenario, name, text string) {
	t.Helper()
	p := filepath.Join(ioDir(scenario, ""), name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ioLoadText(t *testing.T, scenario, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(ioDir(scenario, ""), name))
	if err != nil {
		t.Fatalf("loading interop fixture %s/%s: %v", scenario, name, err)
	}
	return string(data)
}

// ioDiff reports the differences between two snapshots (nil if equal).
func ioDiff(a, b ioSnapshot) []string {
	var out []string
	keys := map[string]bool{}
	for k := range a.Entries {
		keys[k] = true
	}
	for k := range b.Entries {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		ea, oka := a.Entries[k]
		eb, okb := b.Entries[k]
		switch {
		case !oka:
			out = append(out, "only in second: "+k)
		case !okb:
			out = append(out, "only in first: "+k)
		case ea.Kind != eb.Kind || ea.Target != eb.Target || ea.Mode != eb.Mode:
			out = append(out, fmt.Sprintf("%s: %s %s %s vs %s %s %s", k, ea.Kind, ea.Mode, ea.Target, eb.Kind, eb.Mode, eb.Target))
		case ea.Content != eb.Content:
			out = append(out, fmt.Sprintf("%s: content differs:\n--- first\n%s\n--- second\n%s", k, ea.Content, eb.Content))
		}
	}
	return out
}

// --- scenarios ---

type ioScenario struct {
	name string
	// build creates the state with one implementation.
	build func(e *psEnv)
	// commands run on the restored state, in order; each is cwd + args.
	commands [][]string
	// clean scenarios must leave the restored tree untouched: nothing to
	// sync, no drift.
	clean bool
}

// ioRead is the read-only command set; ioWrite adds the syncs.
var ioRead = [][]string{
	{"", "status"},
	{"", "diff", "--all"},
	{"", "show", "skills"},
	{"", "show", "projects"},
	{"", "show", "mcps"},
	{"", "show", "subagents"},
	{"", "show", "inbox"},
	{"", "sync", "--dry-run"},
	{"", "sync", "global", "--dry-run"},
	{"", "sync", "mcp", "--dry-run"},
	{"", "sync", "subagents", "--dry-run"},
}

func ioCommands(extra ...[]string) [][]string {
	return append(append([][]string{}, ioRead...), extra...)
}

func ioBuildRich(e *psEnv) {
	// Remote only: v1.57.7's sync mcp rejects adopted stdio servers ("must
	// use remote transport"), which would block every later sync.
	e.write(".claude/CLAUDE.md", "# My rules\n\nBe nice.\n")
	e.write(".claude.json", `{"mcpServers": {"http-s": {"type": "http", "url": "https://example.com/mcp", `+
		`"headers": {"Authorization": "Bearer fake-value-for-tests-0000000000", "X-Plain": "y"}}}}`)
	e.write(".claude/agents/reviewer.md", "---\nname: reviewer\ndescription: Reviews changes\n---\n\nReview the diff carefully.\n")
	e.run("", "init", "workspace")
	e.run("", "adopt")
	// adopt merged and backed up the hand-written instructions; the user
	// moves the original aside so sync can link the managed copy.
	e.remove(".claude/CLAUDE.md")
	e.remove(".claude/agents/reviewer.md")
	e.run("", "add", "skill", "alpha", "--description", "Alpha skill", "--global")
	e.run("", "add", "skill", "beta", "--description", "Beta skill", "--global")
	e.run("", "add", "mcp", "docs", "--transport", "remote", "--url", "https://docs.example.com/mcp", "--agents", "claude-code,codex")
	e.run("", "add", "subagent", "helper", "--description", "Helps out", "--agents", "claude-code,codex")
	e.write("aikito/memory/notes/global-note.md", "# Global note\n\nRemember this.\n")
	e.write("aikito/inbox/idea.md", "# Idea\n\nTry this.\n")
	e.mkdir("p1", "p2", "p3")
	e.run("", "init", "project", "p1", "{H}/p1")
	e.run("", "init", "project", "p2", "{H}/p2")
	e.run("", "init", "project", "p3", "{H}/p3")
	e.setSkills("p1", "link", "alpha")
	e.setSkills("p2", "copy", "alpha", "beta")
	e.write("aikito/projects/p2/AGENTS.md", "# p2 instructions\n")
	e.write("aikito/projects/p1/memory/notes/p1-note.md", "# p1 note\n")
	// Take ownership of the adopted server's live entry, as adopt advises.
	e.run("", "sync", "mcp", "--force")
	e.run("", "sync")
}

var ioScenarios = []ioScenario{
	{
		name:  "synced",
		build: ioBuildRich,
		commands: ioCommands(
			[]string{"p2", "sync", "project"},
			[]string{"", "sync"},
			[]string{"", "status"},
		),
		clean: true,
	},
	{
		name: "drift",
		build: func(e *psEnv) {
			ioBuildRich(e)
			e.appendTo("p2/.agents/skills/alpha/SKILL.md", "hand edit\n")
			e.appendTo("aikito/skills/beta/SKILL.md", "canonical edit\n")
			e.remove("p3")
		},
		commands: ioCommands(
			[]string{"", "sync", "project", "p2", "--dry-run"},
			[]string{"", "sync", "project", "p2"},
			[]string{"", "sync"},
			[]string{"", "sync", "project", "p2", "--force"},
			[]string{"", "sync"},
			[]string{"", "status"},
			[]string{"", "diff", "--all"},
		),
	},
}

// ioRunScenarioBuild builds sc with cli in a fresh home and snapshots it.
func ioRunScenarioBuild(t *testing.T, sc ioScenario, cli psCLI) (ioSnapshot, string) {
	t.Helper()
	home := resolvedTempDir(t)
	withMarkerDir(t, home, ".claude")
	withMarkerDir(t, home, ".codex")
	e := &psEnv{t: t, home: home, cli: cli}
	sc.build(e)
	return ioCapture(t, home), ioNormalizeTranscript(e.transcript.String(), home)
}

// ioRunCommands restores snap into a fresh home, runs sc's commands with
// cli and returns the transcript and the resulting snapshot.
func ioRunCommands(t *testing.T, sc ioScenario, snap ioSnapshot, cli psCLI) (string, ioSnapshot) {
	t.Helper()
	home := resolvedTempDir(t)
	ioRestore(t, snap, home)
	e := &psEnv{t: t, home: home, cli: cli}
	for _, c := range sc.commands {
		e.run(c[0], c[1:]...)
	}
	return ioNormalizeTranscript(e.transcript.String(), home), ioCapture(t, home)
}

func ioNormalizeTranscript(s, home string) string {
	return ioStampRe.ReplaceAllString(strings.ReplaceAll(s, home, "<HOME>"), ioStamp)
}
