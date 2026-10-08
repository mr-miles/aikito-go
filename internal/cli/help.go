package cli

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// helptext/help.json is the reference Python CLI's --help output for every
// command path, captured verbatim by helptext/gen_help.py (see UPSTREAM.md).
//
//go:embed helptext/help.json
var helpJSON []byte

var helpTexts = func() map[string]string {
	m := map[string]string{}
	if err := json.Unmarshal(helpJSON, &m); err != nil {
		panic("cli: invalid embedded help.json: " + err.Error())
	}
	return m
}()

const notImplementedNote = "  (not implemented in this build)"

// helpAnnotations marks help lines for things the reference CLI offers but
// this port doesn't implement yet. Each marker must match exactly one line
// of that path's help text (help_test.go enforces it, so an upstream
// wording change can't silently drop a note).
var helpAnnotations = map[string][]string{
	"":                  {"    web                 Start the read-only local Web Console"},
	"web":               {"usage: aikito web "},
	"version":           {"  -c, --check  ", "  --force      "},
	"add subagent":      {"  --from FROM_SOURCE    ", "  --sync                "},
	"add subagents":     {"  --from FROM_SOURCE    ", "  --sync                "},
	"add mcp":           {"  --from FROM_SOURCE    ", "  --sync                "},
	"rm skill":          {"  --project PROJECT  "},
	"rm skills":         {"  --project PROJECT  "},
	"remove skill":      {"  --project PROJECT  "},
	"remove skills":     {"  --project PROJECT  "},
	"rm subagent":       {"  --sync      "},
	"rm subagents":      {"  --sync      "},
	"remove subagent":   {"  --sync      "},
	"remove subagents":  {"  --sync      "},
	"show mcp":          {"  --agent [AGENT]       ", "  --live                "},
	"show mcps":         {"  --agent [AGENT]       ", "  --live                "},
	"show subagents":    {"  --agent [AGENT]       "},
	"show subagent":     {"  --agent [AGENT]       "},
	"diff":              {"    project             "},
	"diff project":      {"usage: aikito diff project"},
	"doctor":            {"  --fix                 "},
	"edit instructions": {"  target      global, a project name, or . for the current project"},
	"maintain memory":   {"  target         global, a project name, or . for the current project"},
}

// helpFor returns the help text for a command path ("" for the root), with
// not-implemented notes appended to the affected lines.
func helpFor(path string) (string, bool) {
	text, ok := helpTexts[path]
	if !ok {
		return "", false
	}
	markers := helpAnnotations[path]
	if len(markers) == 0 {
		return text, true
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		for _, m := range markers {
			if strings.HasPrefix(line, m) {
				lines[i] = line + notImplementedNote
			}
		}
	}
	return strings.Join(lines, "\n"), true
}

// handleHelp mirrors argparse: -h/--help anywhere in args prints the help of
// the deepest command reached before it, and exits 0. Positional values and
// option values never extend the path unless they name a real subcommand.
// After `git <something>`, arguments belong to git, as in Python.
func handleHelp(args []string, stdout io.Writer) bool {
	path := ""
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" {
			text, _ := helpFor(path)
			fmt.Fprint(stdout, text)
			return true
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		next := a
		if path != "" {
			next = path + " " + a
		}
		if _, ok := helpTexts[next]; ok {
			path = next
			continue
		}
		if path == "" || path == "git" {
			// Unknown command (left to the normal dispatcher), or arguments
			// that belong to git.
			return false
		}
	}
	return false
}

// noArgsUsage is what the reference CLI prints (to stderr, exit 2) when run
// with no command.
func noArgsUsage() string { return helpTexts["__noargs__"] }
