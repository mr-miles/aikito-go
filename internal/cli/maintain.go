package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-go/internal/project"
	"github.com/mr-miles/aikito-go/internal/registry"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// cmdMaintain dispatches `aikito maintain <kind> ...`. Only "memory" is
// ported (the only maintain target cli_parser.py actually defines).
func cmdMaintain(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 || args[0] != "memory" {
		fmt.Fprintln(stderr, "[ERROR] Usage: aikito maintain memory [target] [--agent AGENT]")
		return 2
	}
	return cmdMaintainMemory(args[1:], stdout, stderr, env)
}

// cmdMaintainMemory ports maintain.py's run_memory_maintenance: resolve a
// memory scope (global or a named project), build the confirmation-gated
// maintenance prompt, resolve the configured agent's runner command, and
// launch it as a real interactive subprocess (stdio inherited from the
// real process, like edit.go's editor launch — this spawns a coding agent
// the user interacts with directly, not something the Environment's
// injected writers can meaningfully capture).
//
// NOT ported: target "." (the default) resolving via resolve.py's
// detect_current_project, which matches the process's current working
// directory against every registered project's active path candidates.
// That function isn't built anywhere in this Go port yet. A bare `aikito
// maintain memory` (or an explicit ".") prints a clear error asking for an
// explicit scope ("global" or a project name) instead of silently guessing
// or crashing on a nil lookup.
func cmdMaintainMemory(args []string, stdout, stderr io.Writer, env Environment) int {
	target := "."
	agentName := "codex"
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--agent":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "[ERROR] --agent requires a value")
				return 2
			}
			agentName = args[i]
		case strings.HasPrefix(a, "--agent="):
			agentName = strings.TrimPrefix(a, "--agent=")
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "[ERROR] Unknown flag: %s\n", a)
			return 2
		default:
			target = a
		}
	}

	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if msg := checkWorkspaceInitialized(aikitoDir); msg != "" {
		fmt.Fprintf(stderr, "[ERROR] %s\n", msg)
		return 1
	}

	if target == "." {
		fmt.Fprintln(stderr, "[ERROR] Current-directory project detection (resolve.py's detect_current_project) "+
			"is not yet implemented in this Go build; pass an explicit scope: 'global' or a registered project name.")
		return 1
	}

	scopeName, memoryDir, workdir, err := resolveMemoryMaintenanceScope(aikitoDir, env.Home, target, env.Cwd)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	agentDef, err := registry.LoadAgentDefinition(aikitoDir, env.Home, agentName)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if agentDef.Runner == nil {
		fmt.Fprintf(stderr, "[ERROR] Agent '%s' has no runner configuration in %s\n",
			agentName, filepath.Join(aikitoDir, "agents", agentName+".toml"))
		return 1
	}

	prompt := buildMemoryMaintenancePrompt(scopeName, memoryDir)
	values := map[string]string{
		"prompt":     prompt,
		"scope":      scopeName,
		"workdir":    workdir,
		"memory_dir": memoryDir,
	}

	command := make([]string, len(agentDef.Runner.Command))
	for i, part := range agentDef.Runner.Command {
		command[i] = substitutePlaceholders(part, values)
	}
	if len(command) == 0 {
		fmt.Fprintf(stderr, "[ERROR] Agent '%s' has an empty runner command\n", agentName)
		return 1
	}

	processEnv := os.Environ()
	for k, v := range agentDef.Runner.Env {
		processEnv = append(processEnv, k+"="+substitutePlaceholders(v, values))
	}

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = workdir
	cmd.Env = processEnv
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "[ERROR] Failed to launch Agent '%s': %v\n", agentName, err)
		return 1
	}
	return 0
}

// resolveMemoryMaintenanceScope mirrors resolve_memory_maintenance_scope
// for the "global" and named-project cases (the "." case is handled, and
// rejected, by the caller before this is reached).
func resolveMemoryMaintenanceScope(aikitoDir, home, target, cwd string) (scopeName, memoryDir, workdir string, err error) {
	if target == "global" {
		dir := filepath.Join(aikitoDir, "memory")
		info, statErr := os.Stat(dir)
		if statErr != nil || !info.IsDir() {
			return "", "", "", fmt.Errorf("global memory scope not found: %s", dir)
		}
		return "global", dir, aikitoDir, nil
	}

	projectDir := filepath.Join(aikitoDir, "projects", target)
	configPath := filepath.Join(projectDir, "agent.toml")
	if info, statErr := os.Stat(configPath); statErr != nil || !info.Mode().IsRegular() {
		return "", "", "", fmt.Errorf("memory scope '%s' not found", target)
	}
	data, rerr := os.ReadFile(configPath)
	if rerr != nil {
		return "", "", "", fmt.Errorf("failed to read %s: %w", configPath, rerr)
	}
	config, derr := workspace.DecodeTOML(data)
	if derr != nil {
		return "", "", "", fmt.Errorf("failed to read %s: %w", configPath, derr)
	}

	memDir := filepath.Join(projectDir, "memory")
	info, statErr := os.Stat(memDir)
	if statErr != nil || !info.IsDir() {
		return "", "", "", fmt.Errorf("project '%s' is registered but has no memory scope: %s", target, memDir)
	}

	binding := project.ResolveProjectBinding(config, home)
	active := binding.ActiveEntries()
	if len(binding.Entries) == 0 {
		return "", "", "", fmt.Errorf("project path is missing in %s", configPath)
	}
	if len(active) == 0 {
		return "", "", "", fmt.Errorf("project '%s' is offline on this host", target)
	}

	matched := bestCwdMatch(cwd, active)
	if matched != "" {
		return target, memDir, matched, nil
	}
	if len(active) == 1 {
		return target, memDir, active[0].ResolvedPath, nil
	}
	var listed []string
	for _, e := range active {
		listed = append(listed, e.ResolvedPath)
	}
	return "", "", "", fmt.Errorf("project '%s' has multiple local paths; run this command from one of them: %s",
		target, strings.Join(listed, ", "))
}

// bestCwdMatch returns the active path entry whose resolved path is cwd or
// an ancestor of it, preferring the deepest (most specific) match — mirrors
// maintain.py's _best_cwd_match.
func bestCwdMatch(cwd string, active []project.PathEntry) string {
	best := ""
	bestDepth := -1
	for _, e := range active {
		rp := e.ResolvedPath
		if cwd != rp && !isWithinDir(cwd, rp) {
			continue
		}
		depth := strings.Count(filepath.Clean(rp), string(filepath.Separator))
		if depth > bestDepth {
			bestDepth = depth
			best = rp
		}
	}
	return best
}

func isWithinDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func buildMemoryMaintenancePrompt(scopeName, memoryDir string) string {
	return fmt.Sprintf(`Use the durable-memory skill to perform proactive maintenance of the selected Aikito memory scope.

Selected scope: %s
Canonical memory directory: %s

For this task, review the complete selected scope rather than maintaining memory only opportunistically.

Inspect every memory note in the scope and relevant inbound wikilinks. Evaluate each note for accuracy, durability, duplication, scope ownership, naming, and continued decision value.

Use current code, configuration, documentation, and Git history when needed to verify claims. Do not treat age alone as evidence that a note is obsolete. Preserve unrelated user changes.

Also compare notes with relevant canonical skills and instructions. Treat duplicated or conflicting operational guidance as a maintenance issue. Keep reusable procedures in skills, binding rules in instructions, and retain in memory only durable decisions, rationale, or constraints not readily available from those sources. Resolve conflicts using current code, configuration, documentation, tests, or Git history when they provide sufficient evidence. When a conflict cannot be verified objectively, present the alternatives and ask the user to decide. Do not modify skills or instructions as part of this workflow; report any required upstream correction separately.

Propose the smallest set of meaningful changes. Group the proposal into update, merge, move, retire, and wikilink repair. For every proposed change, explain the reason and identify the affected files. Explicitly report when no meaningful maintenance is needed.

Do not modify files, stage changes, or create commits until the user confirms the proposal.

After confirmation, apply only the approved changes, repair affected indices and wikilinks, verify memory integrity, and follow the durable-memory skill's Git commit rules. Do not push.
`, scopeName, memoryDir)
}

// substitutePlaceholders replaces {prompt}/{scope}/{workdir}/{memory_dir}
// literally, mirroring Python's str.format_map(values) for this fixed set
// of keys. Unlike Python, an unrecognized {placeholder} is left as-is
// rather than raising — runner command templates are curated agent-config
// content, not untrusted input, so silently passing through an unknown
// brace sequence is an acceptable simplification here.
func substitutePlaceholders(s string, values map[string]string) string {
	for k, v := range values {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}
