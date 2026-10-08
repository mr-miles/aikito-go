// Shell completion support, ported from completion.py/completion_powershell.py.
//
// Design divergence from Python, by deliberate choice: Python reflects the
// completion schema (commands -> flags -> subcommands) live off its
// argparse ArgumentParser at runtime. This Go build has no such
// introspectable parser (commands are a hand-written switch in run.go and
// friends), so the schema below is a hand-maintained table covering only
// the commands THIS build actually implements. It deliberately omits
// `migrate`/`import workspace` (not ported) and, as of this writing,
// `rename`/`maintain`/`doctor` (dispatched in run.go but not yet landed in
// this working tree at the time this file was written — a parallel fork's
// in-progress work). Advertising completions for a command that doesn't
// exist yet would be actively misleading; a follow-up can extend this
// table once those land. Static per-shell script text otherwise mirrors
// Python's generation mechanism closely: completions call back into the
// hidden `aikito completion candidates <category>` subcommand for anything
// that depends on live workspace content.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-rs/internal/project"
)

// --- Schema: the command tree this Go build actually implements ---

type schemaCommand struct {
	flags       []string
	subcommands map[string][]string // subcommand name -> its flags
}

var cliSchema = map[string]schemaCommand{
	"version": {flags: []string{"--json"}},
	"path":    {subcommands: map[string][]string{"workspace": nil}},
	"git":     {},
	"init": {subcommands: map[string][]string{
		"workspace": {"--force"},
		"project":   {"--description"},
	}},
	"add": {subcommands: map[string][]string{
		"skill":    {"--from", "--description", "--project", "--global", "--force", "--sync"},
		"subagent": {"--from", "--description", "--agents", "--force", "--sync"},
		"mcp":      {"--from", "--transport", "--command", "--url", "--agents", "--force", "--sync"},
	}},
	"adopt": {flags: []string{"--dry-run", "--verbose", "--skip"}},
	"show": {subcommands: map[string][]string{
		"project":      nil,
		"skill":        nil,
		"instructions": nil,
		"mcp":          {"--live", "--agent"},
		"subagent":     {"--agent"},
		"inbox":        nil,
		"memory":       {"--project", "--all"},
	}},
	"sync": {subcommands: map[string][]string{
		"mcp":       {"--dry-run", "--force"},
		"global":    {"--dry-run", "--verbose", "--force", "--prune"},
		"subagents": {"--dry-run", "--verbose", "--force", "--prune"},
		"project":   {"--dry-run", "--verbose", "--force", "--prune"},
	}},
	"status": {flags: []string{"--color", "--no-color"}},
	"edit": {subcommands: map[string][]string{
		"skill": nil, "subagent": nil, "mcp": nil, "inbox": nil, "memory": nil, "instructions": nil,
	}},
	"rm": {subcommands: map[string][]string{
		"skill":    {"--project", "--force", "--sync"},
		"subagent": {"--sync"},
		"mcp":      {"--sync", "--force"},
		"memory":   nil,
		"inbox":    nil,
	}},
	"diff": {flags: []string{"--all"}, subcommands: map[string][]string{
		"mcp": nil, "subagent": nil, "project": nil,
	}},
	"auth": {subcommands: map[string][]string{"mcp": nil}},
	"completion": {subcommands: map[string][]string{
		"zsh": nil, "bash": nil, "fish": nil, "powershell": nil, "candidates": nil,
	}},
}

func sortedSchemaNames() []string {
	names := make([]string, 0, len(cliSchema))
	for k := range cliSchema {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// --- Dispatch ---

func cmdCompletion(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "[ERROR] Usage: aikito completion {zsh,bash,fish,powershell,candidates}")
		return 2
	}
	switch args[0] {
	case "zsh":
		fmt.Fprint(stdout, generateZsh())
		return 0
	case "bash":
		fmt.Fprint(stdout, generateBash())
		return 0
	case "fish":
		fmt.Fprint(stdout, generateFish())
		return 0
	case "powershell":
		fmt.Fprint(stdout, generatePowerShell())
		return 0
	case "candidates":
		return cmdCompletionCandidates(args[1:], stdout, stderr, env)
	default:
		fmt.Fprintf(stderr, "[ERROR] Unknown completion target: %s\n", args[0])
		return 2
	}
}

func cmdCompletionCandidates(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "[ERROR] Usage: aikito completion candidates <category> [query]")
		return 2
	}
	category := args[0]
	query := ""
	if len(args) > 1 {
		query = args[1]
	}

	aikitoDir, err := env.AikitoDir()
	if err != nil {
		// Candidate listing is best-effort (called from a live shell
		// completion context); never surface a scary error, just no
		// candidates, mirroring Python's "2>/dev/null"-swallowed callers.
		return 0
	}

	var candidates []string
	switch category {
	case "projects":
		candidates = listProjects(aikitoDir)
	case "skills":
		candidates = listSkills(aikitoDir)
	case "subagents":
		candidates = listSubagents(aikitoDir)
	case "mcps", "mcp":
		candidates = listMCPs(aikitoDir)
	case "memories":
		candidates = listMemories(aikitoDir)
	case "memory-completions":
		candidates = listMemoryCompletions(aikitoDir)
	case "inbox", "inbox-completions":
		candidates = listInboxCompletions(aikitoDir)
	case "paths":
		candidates = listPaths(aikitoDir, env.Home, query)
	default:
		fmt.Fprintf(stderr, "[ERROR] Unknown candidate category: %s\n", category)
		return 2
	}
	for _, c := range candidates {
		fmt.Fprintln(stdout, c)
	}
	return 0
}

// --- Dynamic candidate helpers ---

func listProjects(aikitoDir string) []string {
	entries, err := os.ReadDir(filepath.Join(aikitoDir, "projects"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// listSkills mirrors list_skills's intent (skills selectable/known to the
// workspace) with a simplification: it lists skill directories present
// under skills/, not the full cross-project collect_skills_rows
// aggregation Python draws on — a project-local-only skill selection
// (rare) won't appear here. Documented, not hidden.
//
// Confirmed against live Python on an identical fixture (not assumed):
// bundled system skills ("aikito", "durable-memory") ARE included in
// Python's own output — collect_skills_rows doesn't filter them out for
// completion purposes, unlike the resource scanner, which treats them as
// virtual/non-resources. Don't "fix" this by excluding them; that would be
// a real divergence from Python, not a correction of one.
func listSkills(aikitoDir string) []string {
	entries, err := os.ReadDir(filepath.Join(aikitoDir, "skills"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func listSubagents(aikitoDir string) []string {
	return globStems(filepath.Join(aikitoDir, "subagents"), ".md")
}

func listMCPs(aikitoDir string) []string {
	return globStems(filepath.Join(aikitoDir, "mcps"), ".toml")
}

func globStems(dir, suffix string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), suffix)
		if !seen[stem] {
			seen[stem] = true
			out = append(out, stem)
		}
	}
	sort.Strings(out)
	return out
}

type memoryNote struct {
	scope, stem string
}

// memoryNotes mirrors find_memory_files: only <scope>/memory/notes/*.md
// (non-recursive), global plus every project.
func memoryNotes(aikitoDir string) []memoryNote {
	var out []memoryNote
	addFrom := func(scope, dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			out = append(out, memoryNote{scope, strings.TrimSuffix(e.Name(), ".md")})
		}
	}
	addFrom("global", filepath.Join(aikitoDir, "memory", "notes"))
	for _, p := range listProjects(aikitoDir) {
		addFrom(p, filepath.Join(aikitoDir, "projects", p, "memory", "notes"))
	}
	return out
}

func listMemories(aikitoDir string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, n := range memoryNotes(aikitoDir) {
		add(n.stem)
		add(n.scope + "/" + n.stem)
	}
	sort.Strings(out)
	return out
}

// listMemoryCompletions mirrors list_memory_completions: prefer the short
// "scope/stem" form, falling back to nothing extra here since this port
// doesn't track a separate "full path identifier" distinct from scope/stem
// (no nested notes/ subdirectories are scanned, matching find_memory_files).
func listMemoryCompletions(aikitoDir string) []string {
	notes := memoryNotes(aikitoDir)
	seen := map[string]bool{}
	var out []string
	for _, n := range notes {
		key := n.scope + "/" + n.stem
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// listInboxCompletions mirrors list_inbox_completions: every *.md under
// inbox/, recursively, as a slash-separated relative identifier without
// its extension.
func listInboxCompletions(aikitoDir string) []string {
	inboxDir := filepath.Join(aikitoDir, "inbox")
	var out []string
	filepath.Walk(inboxDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, rerr := filepath.Rel(inboxDir, path)
		if rerr != nil {
			return nil
		}
		out = append(out, strings.TrimSuffix(filepath.ToSlash(rel), ".md"))
		return nil
	})
	sort.Strings(out)
	return out
}

var ignoredPathDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, ".tox": true, ".venv": true,
	"__pycache__": true, "node_modules": true, "target": true, "vendor": true,
}

// listPaths mirrors list_paths: basename-prefix matches under the
// workspace root plus every registered project's resolved candidate
// paths, capped at 100.
func listPaths(aikitoDir, home, prefix string) []string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || strings.Contains(prefix, "/") {
		return nil
	}
	roots := []string{aikitoDir}
	for _, p := range listProjects(aikitoDir) {
		cfg, err := project.LoadConfig(aikitoDir, home, p)
		if err != nil {
			continue
		}
		for _, e := range cfg.Binding().ActiveEntries() {
			roots = append(roots, e.ResolvedPath)
		}
	}
	seen := map[string]bool{}
	var matches []string
	for _, root := range roots {
		filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() && path != root && (ignoredPathDirs[info.Name()] || strings.HasPrefix(info.Name(), ".")) {
				return filepath.SkipDir
			}
			if path == root {
				return nil
			}
			if strings.HasPrefix(info.Name(), prefix) {
				abs, aerr := filepath.Abs(path)
				if aerr == nil && !seen[abs] {
					seen[abs] = true
					matches = append(matches, abs)
				}
			}
			if len(matches) >= 100 {
				return filepath.SkipAll
			}
			return nil
		})
		if len(matches) >= 100 {
			break
		}
	}
	sort.Strings(matches)
	return matches
}

// --- Script generators ---

func joinSorted(items []string) string {
	s := append([]string(nil), items...)
	sort.Strings(s)
	return strings.Join(s, " ")
}

func generateZsh() string {
	cmds := joinSorted(sortedSchemaNames())
	var cmdSubCases, cmdFlagCases, subFlagCases []string
	for _, cmd := range sortedSchemaNames() {
		data := cliSchema[cmd]
		if len(data.subcommands) > 0 {
			var subs []string
			for s := range data.subcommands {
				subs = append(subs, s)
			}
			sort.Strings(subs)
			cmdSubCases = append(cmdSubCases, fmt.Sprintf("                (%s) compadd %s ;;", cmd, strings.Join(subs, " ")))
		}
		if len(data.flags) > 0 {
			cmdFlagCases = append(cmdFlagCases, fmt.Sprintf("                (%s) compadd %s ;;", cmd, joinSorted(data.flags)))
		}
		var subNames []string
		for s := range data.subcommands {
			subNames = append(subNames, s)
		}
		sort.Strings(subNames)
		for _, sub := range subNames {
			flags := data.subcommands[sub]
			if len(flags) > 0 {
				subFlagCases = append(subFlagCases, fmt.Sprintf("                (%s %s) compadd %s ;;", cmd, sub, joinSorted(flags)))
			}
		}
	}

	return "#compdef aikito\n" +
		"# Aikito shell completion for Zsh.\n" +
		"# Installation: eval \"$(aikito completion zsh)\"  (add to ~/.zshrc)\n\n" +
		"_aikito() {\n" +
		"    local -a commands top_flags\n" +
		"    commands=(" + cmds + ")\n" +
		"    top_flags=()\n\n" +
		"    local cur=\"${words[CURRENT]}\"\n\n" +
		"    case $CURRENT in\n" +
		"        2)\n" +
		"            compadd -a commands\n" +
		"            ;;\n" +
		"        3)\n" +
		"            local cmd=\"${words[2]}\"\n" +
		"            if [[ $cur == -* ]]; then\n" +
		"                case $cmd in\n" + strings.Join(cmdFlagCases, "\n") + "\n" +
		"                esac\n" +
		"            else\n" +
		"                case $cmd in\n" + strings.Join(cmdSubCases, "\n") + "\n" +
		"                esac\n" +
		"            fi\n" +
		"            ;;\n" +
		"        *)\n" +
		"            local cmd=\"${words[2]}\" sub=\"${words[3]}\"\n" +
		"            if [[ $cur == -* ]]; then\n" +
		"                case \"$cmd $sub\" in\n" + strings.Join(subFlagCases, "\n") + "\n" +
		"                esac\n" +
		"                return\n" +
		"            fi\n" +
		"            case \"$cmd $sub\" in\n" +
		"                (show\\ memory|edit\\ memory|rm\\ memory|remove\\ memory)\n" +
		"                    local -a cands; cands=(${(f)\"$(aikito completion candidates memory-completions 2>/dev/null)\"}); compadd -a cands ;;\n" +
		"                (show\\ inbox|edit\\ inbox|rm\\ inbox|remove\\ inbox)\n" +
		"                    local -a cands; cands=(${(f)\"$(aikito completion candidates inbox-completions 2>/dev/null)\"}); compadd -a cands ;;\n" +
		"                (show\\ skill|edit\\ skill|rm\\ skill|remove\\ skill|add\\ skill)\n" +
		"                    local -a cands; cands=(${(f)\"$(aikito completion candidates skills 2>/dev/null)\"}); compadd -a cands ;;\n" +
		"                (show\\ subagent|edit\\ subagent|rm\\ subagent|remove\\ subagent|add\\ subagent)\n" +
		"                    local -a cands; cands=(${(f)\"$(aikito completion candidates subagents 2>/dev/null)\"}); compadd -a cands ;;\n" +
		"                (show\\ mcp|edit\\ mcp|rm\\ mcp|remove\\ mcp|add\\ mcp)\n" +
		"                    local -a cands; cands=(${(f)\"$(aikito completion candidates mcps 2>/dev/null)\"}); compadd -a cands ;;\n" +
		"                (show\\ instructions|edit\\ instructions|show\\ project|sync\\ project)\n" +
		"                    local -a cands; cands=(${(f)\"$(aikito completion candidates projects 2>/dev/null)\"}); compadd -a cands ;;\n" +
		"                (init\\ workspace|init\\ project)\n" +
		"                    _files -/ ;;\n" +
		"                (completion\\ candidates)\n" +
		"                    compadd projects skills memories subagents mcps inbox paths ;;\n" +
		"            esac\n" +
		"            ;;\n" +
		"    esac\n" +
		"}\n\n" +
		"compdef _aikito aikito\n"
}

func generateBash() string {
	var cmdSubCases, cmdFlagCases, subFlagCases []string
	for _, cmd := range sortedSchemaNames() {
		data := cliSchema[cmd]
		if len(data.subcommands) > 0 {
			var subs []string
			for s := range data.subcommands {
				subs = append(subs, s)
			}
			sort.Strings(subs)
			cmdSubCases = append(cmdSubCases, fmt.Sprintf("        %s)\n            COMPREPLY=( $(compgen -W \"%s\" -- \"$cur\") )\n            return 0 ;;", cmd, strings.Join(subs, " ")))
		}
		if len(data.flags) > 0 {
			cmdFlagCases = append(cmdFlagCases, fmt.Sprintf("        %s)\n            COMPREPLY=( $(compgen -W \"%s\" -- \"$cur\") )\n            return 0 ;;", cmd, joinSorted(data.flags)))
		}
		var subNames []string
		for s := range data.subcommands {
			subNames = append(subNames, s)
		}
		sort.Strings(subNames)
		for _, sub := range subNames {
			flags := data.subcommands[sub]
			if len(flags) > 0 {
				subFlagCases = append(subFlagCases, fmt.Sprintf("        \"%s %s\")\n            COMPREPLY=( $(compgen -W \"%s\" -- \"$cur\") )\n            return 0 ;;", cmd, sub, joinSorted(flags)))
			}
		}
	}
	cmds := joinSorted(sortedSchemaNames())

	return "# Aikito shell completion for Bash.\n" +
		"# Add to ~/.bashrc:  eval \"$(aikito completion bash)\"\n\n" +
		"_aikito_completion() {\n" +
		"    local cur=\"${COMP_WORDS[COMP_CWORD]}\"\n" +
		"    local commands=\"" + cmds + "\"\n\n" +
		"    if [[ $COMP_CWORD -eq 1 ]]; then\n" +
		"        COMPREPLY=( $(compgen -W \"$commands\" -- \"$cur\") )\n" +
		"        return 0\n" +
		"    fi\n\n" +
		"    local cmd=\"${COMP_WORDS[1]}\"\n" +
		"    local sub=\"${COMP_WORDS[2]}\"\n\n" +
		"    if [[ $COMP_CWORD -eq 2 ]]; then\n" +
		"        if [[ $cur == -* ]]; then\n" +
		"            case $cmd in\n" + strings.Join(cmdFlagCases, "\n") + "\n            esac\n" +
		"        else\n" +
		"            case $cmd in\n" + strings.Join(cmdSubCases, "\n") + "\n            esac\n" +
		"        fi\n" +
		"        return 0\n" +
		"    fi\n\n" +
		"    if [[ $cur == -* ]]; then\n" +
		"        case \"$cmd $sub\" in\n" + strings.Join(subFlagCases, "\n") + "\n        esac\n" +
		"        return 0\n" +
		"    fi\n\n" +
		"    case \"$cmd $sub\" in\n" +
		"        show\\ memory|edit\\ memory|rm\\ memory|remove\\ memory)\n" +
		"            COMPREPLY=( $(compgen -W \"$(aikito completion candidates memory-completions 2>/dev/null)\" -- \"$cur\") ) ;;\n" +
		"        show\\ inbox|edit\\ inbox|rm\\ inbox|remove\\ inbox)\n" +
		"            COMPREPLY=( $(compgen -W \"$(aikito completion candidates inbox-completions 2>/dev/null)\" -- \"$cur\") ) ;;\n" +
		"        show\\ skill|edit\\ skill|rm\\ skill|remove\\ skill|add\\ skill)\n" +
		"            COMPREPLY=( $(compgen -W \"$(aikito completion candidates skills 2>/dev/null)\" -- \"$cur\") ) ;;\n" +
		"        show\\ subagent|edit\\ subagent|rm\\ subagent|remove\\ subagent|add\\ subagent)\n" +
		"            COMPREPLY=( $(compgen -W \"$(aikito completion candidates subagents 2>/dev/null)\" -- \"$cur\") ) ;;\n" +
		"        show\\ mcp|edit\\ mcp|rm\\ mcp|remove\\ mcp|add\\ mcp)\n" +
		"            COMPREPLY=( $(compgen -W \"$(aikito completion candidates mcps 2>/dev/null)\" -- \"$cur\") ) ;;\n" +
		"        show\\ instructions|edit\\ instructions|show\\ project|sync\\ project)\n" +
		"            COMPREPLY=( $(compgen -W \"$(aikito completion candidates projects 2>/dev/null)\" -- \"$cur\") ) ;;\n" +
		"        init\\ workspace|init\\ project)\n" +
		"            COMPREPLY=( $(compgen -d -- \"$cur\") ) ;;\n" +
		"        completion\\ candidates)\n" +
		"            COMPREPLY=( $(compgen -W \"projects skills memories subagents mcps inbox paths\" -- \"$cur\") ) ;;\n" +
		"    esac\n" +
		"    return 0\n" +
		"}\n\n" +
		"complete -F _aikito_completion aikito\n"
}

func generateFish() string {
	cmds := joinSorted(sortedSchemaNames())
	var lines []string
	lines = append(lines,
		"# Aikito shell completion for Fish.",
		"# Install: aikito completion fish > ~/.config/fish/completions/aikito.fish",
		"",
		fmt.Sprintf("complete -c aikito -f -n '__fish_use_subcommand' -a '%s'", cmds),
		"",
	)
	for _, cmd := range sortedSchemaNames() {
		data := cliSchema[cmd]
		var subs []string
		for s := range data.subcommands {
			subs = append(subs, s)
		}
		sort.Strings(subs)
		if len(subs) > 0 {
			lines = append(lines, fmt.Sprintf("complete -c aikito -f -n '__fish_seen_subcommand_from %s' -a '%s'", cmd, strings.Join(subs, " ")))
		}
		for _, flag := range data.flags {
			lines = append(lines, fmt.Sprintf("complete -c aikito -f -n '__fish_seen_subcommand_from %s' -l %s", cmd, strings.TrimPrefix(flag, "--")))
		}
		for _, sub := range subs {
			for _, flag := range data.subcommands[sub] {
				lines = append(lines, fmt.Sprintf("complete -c aikito -f -n '__fish_seen_subcommand_from %s; and __fish_seen_subcommand_from %s' -l %s", cmd, sub, strings.TrimPrefix(flag, "--")))
			}
		}
	}
	lines = append(lines,
		"",
		"# Dynamic candidates",
		"complete -c aikito -f -n '__fish_seen_subcommand_from show edit rm remove; and __fish_seen_subcommand_from memory' -a '(aikito completion candidates memory-completions 2>/dev/null)'",
		"complete -c aikito -f -n '__fish_seen_subcommand_from show edit rm remove; and __fish_seen_subcommand_from inbox' -a '(aikito completion candidates inbox-completions 2>/dev/null)'",
		"complete -c aikito -f -n '__fish_seen_subcommand_from show edit rm remove add; and __fish_seen_subcommand_from skill' -a '(aikito completion candidates skills 2>/dev/null)'",
		"complete -c aikito -f -n '__fish_seen_subcommand_from show edit rm remove add; and __fish_seen_subcommand_from subagent' -a '(aikito completion candidates subagents 2>/dev/null)'",
		"complete -c aikito -f -n '__fish_seen_subcommand_from show edit rm remove add; and __fish_seen_subcommand_from mcp' -a '(aikito completion candidates mcps 2>/dev/null)'",
		"complete -c aikito -f -n '__fish_seen_subcommand_from show edit; and __fish_seen_subcommand_from instructions' -a '(aikito completion candidates projects 2>/dev/null)'",
		"complete -c aikito -f -n '__fish_seen_subcommand_from show sync; and __fish_seen_subcommand_from project' -a '(aikito completion candidates projects 2>/dev/null)'",
		"complete -c aikito -F -n '__fish_seen_subcommand_from init'",
	)
	return strings.Join(lines, "\n") + "\n"
}

// generatePowerShell is a minimal, functionally-complete analogue of
// completion_powershell.py's Register-ArgumentCompleter-based script
// (static-command-tree completion only — no dynamic candidate callback for
// PowerShell yet; Python's own implementation is comparatively small and
// this covers the same top-level/subcommand completion depth).
func generatePowerShell() string {
	var sb strings.Builder
	sb.WriteString("# Aikito shell completion for PowerShell.\n")
	sb.WriteString("# Install: aikito completion powershell | Out-String | Invoke-Expression\n")
	sb.WriteString("# (add that line to your $PROFILE to load it in every session)\n\n")
	sb.WriteString("Register-ArgumentCompleter -Native -CommandName aikito -ScriptBlock {\n")
	sb.WriteString("    param($wordToComplete, $commandAst, $cursorPosition)\n")
	sb.WriteString("    $tokens = $commandAst.CommandElements | ForEach-Object { $_.ToString() }\n")
	sb.WriteString("    $commands = @(" + psQuotedList(sortedSchemaNames()) + ")\n")
	sb.WriteString("    if ($tokens.Count -le 2) {\n")
	sb.WriteString("        $commands | Where-Object { $_ -like \"$wordToComplete*\" } | ForEach-Object { [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_) }\n")
	sb.WriteString("        return\n")
	sb.WriteString("    }\n")
	sb.WriteString("    $cmd = $tokens[1]\n")
	sb.WriteString("    switch ($cmd) {\n")
	for _, cmd := range sortedSchemaNames() {
		data := cliSchema[cmd]
		if len(data.subcommands) == 0 {
			continue
		}
		var subs []string
		for s := range data.subcommands {
			subs = append(subs, s)
		}
		sort.Strings(subs)
		fmt.Fprintf(&sb, "        '%s' { @(%s) | Where-Object { $_ -like \"$wordToComplete*\" } | ForEach-Object { [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_) } }\n", cmd, psQuotedList(subs))
	}
	sb.WriteString("    }\n")
	sb.WriteString("}\n")
	return sb.String()
}

func psQuotedList(items []string) string {
	quoted := make([]string, len(items))
	for i, it := range items {
		quoted[i] = "'" + it + "'"
	}
	return strings.Join(quoted, ", ")
}
