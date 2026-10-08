package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// cmdEdit dispatches `aikito edit <kind> <target>`, resolving the target to
// a concrete canonical workspace file (reusing show.go's name-resolution
// helpers) and launching the user's configured editor on it.
//
// NOT ported: cmd_edit_instructions' cwd-based project auto-detection
// (detect_current_project) for a bare `aikito edit instructions` with no
// target — that's a separate feature (matching the current directory
// against registered project paths) this pass doesn't implement; a missing
// target here defaults straight to "global", with that simplification
// documented on cmdEditInstructions. resolve_executable's Windows .cmd/.bat
// resolution (compat.py) is also not ported (POSIX-first pass) — plain
// exec.Command PATH lookup covers the realistic POSIX case.
func cmdEdit(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "[ERROR] Usage: aikito edit <memory|inbox|instructions|skill|subagent|mcp> <target>")
		return 2
	}
	kind, rest := args[0], args[1:]
	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if err := workspace.RequireCurrentLayout(aikitoDir); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	var target string
	if len(rest) > 0 {
		target = rest[0]
	}

	var path string
	switch kind {
	case "skill":
		path, err = resolveEditSkillPath(aikitoDir, target, stderr)
	case "subagent":
		path, err = resolveEditSubagentPath(aikitoDir, target, stderr)
	case "mcp":
		path, err = resolveEditMCPPath(aikitoDir, target, stderr)
	case "inbox":
		path, err = resolveEditInboxPath(aikitoDir, target, stderr)
	case "memory":
		path, err = resolveEditMemoryPath(aikitoDir, target, stderr)
	case "instructions":
		path, err = resolveEditInstructionsPath(aikitoDir, target, stderr)
	default:
		fmt.Fprintf(stderr, "[ERROR] Unknown edit target: %s\n", kind)
		return 2
	}
	if err != nil {
		if err != errAlreadyReported {
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		}
		return 1
	}

	code, launchErr := launchEditor(path, env)
	if launchErr != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to launch editor: %v\n", launchErr)
		return 1
	}
	return code
}

// errAlreadyReported marks a resolution failure whose message was already
// printed to stderr by a reused show.go helper (resolveByName), so cmdEdit
// doesn't double-print it with an extra "[ERROR]" prefix.
var errAlreadyReported = fmt.Errorf("already reported")

func resolveEditSkillPath(aikitoDir, target string, stderr io.Writer) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", fmt.Errorf("Usage: aikito edit skill <name>")
	}
	skillsDir := filepath.Join(aikitoDir, "skills")
	entries, _ := os.ReadDir(skillsDir)
	var names []string
	for _, e := range entries {
		if !e.IsDir() || workspace.IsBundledSkillName(e.Name()) || workspace.IsIgnoredName(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	matched, ok := resolveByName(names, target, "edit", resolveLabels{
		conflictNoun: "skills", specifyLine: "Please specify the exact skill name, e.g.:",
		cmdName: "skill", notFoundSingular: "Skill",
		notFoundHint: "Run 'aikito show skills' to view available skills.",
		scopeSuffix:  "Global",
	}, stderr)
	if !ok {
		return "", errAlreadyReported
	}
	return filepath.Join(skillsDir, matched, "SKILL.md"), nil
}

func resolveEditSubagentPath(aikitoDir, target string, stderr io.Writer) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", fmt.Errorf("Usage: aikito edit subagent <name>")
	}
	names := subagentNames(aikitoDir)
	matched, ok := resolveByName(names, target, "edit", resolveLabels{
		conflictNoun: "subagents", specifyLine: "Please specify the exact subagent name, e.g.:",
		cmdName: "subagent", notFoundSingular: "Subagent",
		notFoundHint: "Run 'aikito show subagents' to view available subagents.",
	}, stderr)
	if !ok {
		return "", errAlreadyReported
	}
	return filepath.Join(aikitoDir, "subagents", matched+".md"), nil
}

func resolveEditMCPPath(aikitoDir, target string, stderr io.Writer) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", fmt.Errorf("Usage: aikito edit mcp <name>")
	}
	names, err := mcpNames(aikitoDir)
	if err != nil {
		return "", err
	}
	matched, ok := resolveByName(names, target, "edit", resolveLabels{
		conflictNoun: "MCP servers", specifyLine: "Please specify the exact MCP server name, e.g.:",
		cmdName: "mcp", notFoundSingular: "MCP server",
		notFoundHint: "Run 'aikito show mcp' to view available MCP servers.",
	}, stderr)
	if !ok {
		return "", errAlreadyReported
	}
	return filepath.Join(aikitoDir, "mcps", matched+".toml"), nil
}

func resolveEditInboxPath(aikitoDir, target string, stderr io.Writer) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", fmt.Errorf("Usage: aikito edit inbox <name>")
	}
	dir := inboxDir(aikitoDir)
	var names []string
	filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		if rel, rerr := filepath.Rel(dir, path); rerr == nil {
			names = append(names, filepath.ToSlash(rel))
		}
		return nil
	})
	matched, ok := resolveByName(names, target, "edit", resolveLabels{
		conflictNoun: "inbox notes", specifyLine: "Please specify the exact name, e.g.:",
		cmdName: "inbox", notFoundSingular: "Inbox note",
		notFoundHint: "Run 'aikito show inbox' to view available inbox files.",
	}, stderr)
	if !ok {
		return "", errAlreadyReported
	}
	return filepath.Join(dir, filepath.FromSlash(matched)), nil
}

func resolveEditMemoryPath(aikitoDir, target string, stderr io.Writer) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", fmt.Errorf("Usage: aikito edit memory <name>")
	}
	dir := filepath.Join(aikitoDir, "memory")
	var names []string
	byName := map[string]string{}
	filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		if rel, rerr := filepath.Rel(dir, path); rerr == nil {
			key := filepath.ToSlash(rel)
			names = append(names, key)
			byName[key] = path
		}
		return nil
	})
	matched, ok := resolveByName(names, target, "edit", resolveLabels{
		conflictNoun: "memory notes", specifyLine: "Please specify the full identifier, e.g.:",
		cmdName: "memory", notFoundSingular: "Memory note",
		notFoundHint: "Run 'aikito show memory' to view available notes.",
	}, stderr)
	if !ok {
		return "", errAlreadyReported
	}
	return byName[matched], nil
}

// resolveEditInstructionsPath mirrors cmd_edit_instructions, simplified: a
// missing target defaults straight to "global" rather than first trying
// detect_current_project(cwd) — see the package-level doc comment.
func resolveEditInstructionsPath(aikitoDir, target string, stderr io.Writer) (string, error) {
	if target == "" || target == "global" {
		return filepath.Join(aikitoDir, "global", "AGENTS.md"), nil
	}
	if _, err := os.Stat(filepath.Join(aikitoDir, "projects", target, "agent.toml")); err != nil {
		return "", fmt.Errorf("Instructions target '%s' not found.", target)
	}
	return filepath.Join(aikitoDir, "projects", target, "AGENTS.md"), nil
}

// launchEditor resolves $VISUAL/$EDITOR (falling back to "vi" on POSIX,
// "notepad" on Windows — compat.py's get_default_editor) via env.Env
// rather than a direct os.Getenv, splits it the way a shell would
// (splitCommand, a simplified shlex), and runs it with the REAL process
// stdio (not env's injected stdin/stdout/stderr, which are for capturing
// this CLI's own textual output) so an interactive editor can actually
// drive the terminal — matching Python's subprocess.run(cmd_args), which
// inherits file descriptors by default. The actual subprocess launch is a
// package variable (runEditorProcess) so tests can substitute a fake
// without spawning a real interactive process.
func launchEditor(path string, env Environment) (int, error) {
	editor := strings.TrimSpace(env.Env.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(env.Env.Getenv("EDITOR"))
	}
	if editor == "" {
		editor = defaultEditorName()
	}
	parts := splitCommand(editor)
	if len(parts) == 0 {
		parts = []string{editor}
	}
	cmdArgs := append(append([]string(nil), parts...), path)
	return runEditorProcess(cmdArgs)
}

func defaultEditorName() string {
	if isWindowsRuntime() {
		return "notepad"
	}
	return "vi"
}

// runEditorProcess is the real subprocess launch; overridable in tests.
var runEditorProcess = func(cmdArgs []string) (int, error) {
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), nil
	}
	return 1, err
}

// splitCommand is a simplified POSIX shlex.split: handles single/double
// quotes and backslash escapes, enough for realistic $EDITOR/$VISUAL
// values ("vi", "code --wait", "emacs -nw"). It does not replicate every
// shlex edge case (e.g. nested quoting quirks) — a deliberate
// simplification for a command string that is almost always one or two
// plain words in practice.
func splitCommand(cmd string) []string {
	var parts []string
	var cur strings.Builder
	hasCur := false
	inSingle, inDouble := false, false
	runes := []rune(cmd)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				cur.WriteRune(c)
			}
		case inDouble:
			if c == '"' {
				inDouble = false
			} else if c == '\\' && i+1 < len(runes) && (runes[i+1] == '"' || runes[i+1] == '\\') {
				cur.WriteRune(runes[i+1])
				i++
			} else {
				cur.WriteRune(c)
			}
		case c == '\'':
			inSingle, hasCur = true, true
		case c == '"':
			inDouble, hasCur = true, true
		case c == '\\' && i+1 < len(runes):
			cur.WriteRune(runes[i+1])
			i++
			hasCur = true
		case c == ' ' || c == '\t':
			if hasCur {
				parts = append(parts, cur.String())
				cur.Reset()
				hasCur = false
			}
		default:
			cur.WriteRune(c)
			hasCur = true
		}
	}
	if hasCur {
		parts = append(parts, cur.String())
	}
	return parts
}

func isWindowsRuntime() bool {
	return os.PathSeparator == '\\'
}
