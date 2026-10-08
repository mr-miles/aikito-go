package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

// cmdMaintainMemory ports cli.py cmd_maintain_memory and maintain.py
// run_memory_maintenance: resolve a memory scope (global, a project name, or
// "." for the project containing the current directory), then launch the
// configured Agent runner with the maintenance prompt. The runner inherits
// the command's stdin/stdout/stderr (the terminal when run from main).
func cmdMaintainMemory(args []string, stdout, stderr io.Writer, env Environment) int {
	parsed, ok := parseArgparseOpts("maintain memory", args, nil, []string{"--agent"}, nil, 1, stderr)
	if !ok {
		return 2
	}
	target := "."
	if len(parsed.positionals) > 0 {
		target = parsed.positionals[0]
	}
	agentName := "codex"
	if a, ok := parsed.values["--agent"]; ok {
		agentName = a
	}
	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if err := requireLayoutLikePython(aikitoDir); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	code, err := runMemoryMaintenance(aikitoDir, target, agentName, env, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	return code
}

func runMemoryMaintenance(aikitoDir, target, agentName string, env Environment, stdout, stderr io.Writer) (int, error) {
	scopeName, memoryDir, workdir, err := resolveMemoryMaintenanceScope(aikitoDir, env.Home, target, env.Cwd)
	if err != nil {
		return 0, err
	}
	prompt := buildMemoryMaintenancePrompt(scopeName, memoryDir)

	configPath := filepath.Join(aikitoDir, "agents", agentName+".toml")
	agentDef, err := registry.LoadAgentDefinition(aikitoDir, env.Home, agentName)
	if err != nil {
		return 0, err
	}
	if agentDef.Runner == nil {
		return 0, fmt.Errorf("Agent '%s' has no runner configuration in %s", agentName, configPath)
	}
	values := map[string]string{"prompt": prompt, "scope": scopeName, "workdir": workdir, "memory_dir": memoryDir}
	command := make([]string, len(agentDef.Runner.Command))
	for i, part := range agentDef.Runner.Command {
		if command[i], err = pyFormatMap(part, values); err != nil {
			return 0, fmt.Errorf("Invalid runner placeholder: %v", err)
		}
	}
	processEnv := os.Environ()
	envKeys := make([]string, 0, len(agentDef.Runner.Env))
	for k := range agentDef.Runner.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		v, ferr := pyFormatMap(agentDef.Runner.Env[k], values)
		if ferr != nil {
			return 0, fmt.Errorf("Invalid runner placeholder: %v", ferr)
		}
		processEnv = append(processEnv, k+"="+v)
	}
	if len(command) == 0 {
		return 0, fmt.Errorf("Failed to launch Agent '%s': list index out of range", agentName)
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = workdir
	cmd.Env = processEnv
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return 0, fmt.Errorf("Failed to launch Agent '%s': %s", agentName, pyLaunchError(err, command[0]))
	}
	return 0, nil
}

// pyFormatMap is str.format_map for plain {name} fields, with "{{" and
// "}}" escapes. Errors read like Python's KeyError / ValueError text.
func pyFormatMap(s string, values map[string]string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '{' && i+1 < len(s) && s[i+1] == '{':
			b.WriteByte('{')
			i++
		case c == '}' && i+1 < len(s) && s[i+1] == '}':
			b.WriteByte('}')
			i++
		case c == '}':
			return "", fmt.Errorf("Single '}' encountered in format string")
		case c == '{':
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				return "", fmt.Errorf("expected '}' before end of string")
			}
			field := s[i+1 : i+end]
			v, ok := values[field]
			if !ok {
				return "", fmt.Errorf("'%s'", field)
			}
			b.WriteString(v)
			i += end
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}

type loadedMaintenanceProject struct {
	name, memoryDir, configPath string
	activePaths                 []string
	hasCandidates               bool
}

// resolveMemoryMaintenanceScope ports maintain.py's
// resolve_memory_maintenance_scope.
func resolveMemoryMaintenanceScope(aikitoDir, home, target, cwd string) (scopeName, memoryDir, workdir string, err error) {
	resolve := func(p string) string {
		if r, rerr := workspace.ResolvePath(p); rerr == nil {
			return r
		}
		return p
	}
	if target == "global" {
		dir := filepath.Join(aikitoDir, "memory")
		if !isDir(dir) {
			return "", "", "", fmt.Errorf("Global memory scope not found: %s", dir)
		}
		return "global", resolve(dir), resolve(aikitoDir), nil
	}
	projectsDir := filepath.Join(aikitoDir, "projects")
	if !isDir(projectsDir) {
		return "", "", "", fmt.Errorf("No registered projects found")
	}
	var projects []loadedMaintenanceProject
	entries, _ := os.ReadDir(projectsDir)
	for _, e := range entries {
		dir := filepath.Join(projectsDir, e.Name())
		cfgPath := filepath.Join(dir, "agent.toml")
		if !isDir(dir) || !isRegularFile(cfgPath) {
			continue
		}
		raw, rerr := os.ReadFile(cfgPath)
		var cfg map[string]any
		if rerr == nil {
			cfg, rerr = workspace.DecodeTOML(raw)
		}
		if rerr != nil {
			return "", "", "", fmt.Errorf("Failed to read %s: %v", cfgPath, rerr)
		}
		binding := project.ResolveProjectBinding(cfg, home)
		lp := loadedMaintenanceProject{name: e.Name(), memoryDir: resolve(filepath.Join(dir, "memory")),
			configPath: cfgPath, hasCandidates: len(binding.Entries) > 0}
		for _, a := range binding.ActiveEntries() {
			lp.activePaths = append(lp.activePaths, a.ResolvedPath)
		}
		projects = append(projects, lp)
	}

	if target == "." {
		detected, derr := project.DetectCurrentProject(aikitoDir, cwd, home)
		var conflict *project.ContextConflictError
		if errors.As(derr, &conflict) {
			return "", "", "", fmt.Errorf("Current directory '%s' belongs to multiple projects: %s", conflict.Path, strings.Join(conflict.Projects, ", "))
		}
		if detected == "" {
			return "", "", "", fmt.Errorf("Current directory is not inside a registered project: %s", resolve(cwd))
		}
		target = detected
	}

	for _, p := range projects {
		if p.name != target {
			continue
		}
		if !p.hasCandidates {
			return "", "", "", fmt.Errorf("Project path is missing in %s", p.configPath)
		}
		if len(p.activePaths) == 0 {
			return "", "", "", fmt.Errorf("Project '%s' is offline on this host", p.name)
		}
		work := bestCwdMatch(resolve(cwd), p.activePaths)
		if work == "" {
			if len(p.activePaths) != 1 {
				return "", "", "", fmt.Errorf("Project '%s' has multiple local paths; run this command from one of them: %s", p.name, strings.Join(p.activePaths, ", "))
			}
			work = p.activePaths[0]
		}
		if !isDir(p.memoryDir) {
			return "", "", "", fmt.Errorf("Project '%s' is registered but has no memory scope: %s", p.name, p.memoryDir)
		}
		return p.name, p.memoryDir, work, nil
	}
	return "", "", "", fmt.Errorf("Memory scope '%s' not found", target)
}

// bestCwdMatch is maintain.py's _best_cwd_match: the deepest root that is
// cwd or contains it.
func bestCwdMatch(cwd string, roots []string) string {
	best, bestDepth := "", -1
	for _, rp := range roots {
		if cwd != rp && !isWithinDir(cwd, rp) {
			continue
		}
		if depth := len(strings.Split(filepath.Clean(rp), string(filepath.Separator))); depth > bestDepth {
			bestDepth, best = depth, rp
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
