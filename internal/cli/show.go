package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/project"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// cmdShow dispatches `aikito show <kind> [target] [flags]`.
//
// Scope note: this is a read-only port of cli_show.py. Faithfully ported:
// the shared exact-then-unique-prefix-then-conflict target resolution
// (resolve.py's resolve_*_target_for_command family, which this file
// generalizes into one resolveByName helper since all five callers use the
// identical algorithm with only label text differing) and raw-file-content
// display for skill/instructions/mcp/subagent/inbox/memory targets.
//
// NOT ported (documented, not silently approximated): the no-target list
// views here use a simple Go-rendered table, NOT render.py's
// render_*_table functions (which this Go port hasn't built — they depend
// on a presentation layer, render.py, that's 1316 lines and out of scope
// for this pass). `--live` MCP probing and `--agent` cross-agent detail
// views (collect_mcp_details/collect_subagent_details and their table
// renderers) are not implemented; both flags print a clear error instead
// of silently no-op'ing.
func cmdShow(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "[ERROR] Usage: aikito show <project|skill|instructions|mcp|subagent|inbox|memory> [target]")
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

	switch kind {
	case "project", "projects":
		return cmdShowProject(rest, aikitoDir, env, stdout, stderr)
	case "skill", "skills":
		return cmdShowSkill(rest, aikitoDir, stdout, stderr)
	case "instructions":
		return cmdShowInstructions(rest, aikitoDir, env, stdout, stderr)
	case "mcp", "mcps":
		return cmdShowMCP(rest, aikitoDir, stdout, stderr)
	case "subagent", "subagents":
		return cmdShowSubagent(rest, aikitoDir, stdout, stderr)
	case "inbox":
		return cmdShowInbox(rest, aikitoDir, stdout, stderr)
	case "memory":
		return cmdShowMemory(rest, aikitoDir, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "[ERROR] Unknown show target: %s\n", kind)
		return 2
	}
}

// --- shared exact-then-unique-prefix-then-conflict resolver ---
// Mirrors resolve.py's resolve_{skill,subagent,mcp,inbox,memory}_target_for_command,
// which all share this exact algorithm (confirmed against source) modulo
// per-kind label text.

// resolveLabels carries each resolve_*_target_for_command's literal text
// verbatim rather than trying to derive it from a shared noun — confirmed
// against source that the conflict-message noun, the "please specify" line,
// and the not-found singular noun all vary independently per kind (e.g.
// inbox's specify-line is generic "the exact name", memory's is "the full
// identifier", neither following the "the exact <noun> name" pattern the
// others share), so a single pluralization field cannot represent all five
// correctly — an earlier version of this code tried that and was caught
// producing "Multiple skill match" (wrong) against live Python's "Multiple
// skills match" during cross-validation.
type resolveLabels struct {
	conflictNoun     string // "skills", "subagents", "MCP servers", "inbox notes", "memory notes"
	specifyLine      string // e.g. "Please specify the exact skill name, e.g.:"
	cmdName          string // "skill", "subagent", "mcp", "inbox", "memory" (the subcommand word in the hint)
	notFoundSingular string // "Skill", "Subagent", "MCP server", "Inbox note", "Memory note"
	notFoundHint     string // e.g. "Run 'aikito show subagents' to view available subagents."
	scopeSuffix      string // if non-empty, appended as " (<scopeSuffix>)" to each conflict bullet (skill's "(Global)")
}

func resolveByName(names []string, target, operation string, labels resolveLabels, stderr io.Writer) (string, bool) {
	targetNorm := strings.TrimSpace(target)
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)

	var exact []string
	for _, n := range sorted {
		if n == targetNorm {
			exact = append(exact, n)
		}
	}
	if len(exact) == 1 {
		return exact[0], true
	}

	var prefix []string
	for _, n := range sorted {
		if strings.HasPrefix(n, targetNorm) {
			prefix = append(prefix, n)
		}
	}
	if len(prefix) == 1 {
		return prefix[0], true
	}
	if len(prefix) > 1 {
		fmt.Fprintf(stderr, "[CONFLICT] Multiple %s match '%s':\n\n", labels.conflictNoun, target)
		for _, n := range prefix {
			if labels.scopeSuffix != "" {
				fmt.Fprintf(stderr, "  - %s (%s)\n", n, labels.scopeSuffix)
			} else {
				fmt.Fprintf(stderr, "  - %s\n", n)
			}
		}
		fmt.Fprintf(stderr, "\n%s\n", labels.specifyLine)
		for _, n := range prefix {
			fmt.Fprintf(stderr, "  aikito %s %s %s\n", operation, labels.cmdName, n)
		}
		return "", false
	}

	fmt.Fprintf(stderr, "[ERROR] %s '%s' not found.\n", labels.notFoundSingular, target)
	if labels.notFoundHint != "" {
		fmt.Fprintln(stderr, labels.notFoundHint)
	}
	return "", false
}

func printFileVerbatim(path string, stdout, stderr io.Writer) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to read %s: %v\n", path, err)
		return 1
	}
	fmt.Fprint(stdout, string(data))
	return 0
}

func resourceNamesOfKind(snap *workspace.WorkspaceSnapshot, kind string) []string {
	var names []string
	for _, r := range snap.Resources {
		if r.Kind == kind {
			names = append(names, r.Name)
		}
	}
	sort.Strings(names)
	return names
}

// --- project ---

func cmdShowProject(args []string, aikitoDir string, env Environment, stdout, stderr io.Writer) int {
	var target string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target = args[0]
	}

	snap, err := workspace.SnapshotWorkspace(aikitoDir, env.Home)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	names := resourceNamesOfKind(snap, "project")

	if target == "" {
		if len(names) == 0 {
			fmt.Fprintln(stdout, "No projects registered.")
			return 0
		}
		fmt.Fprintln(stdout, "Projects:")
		for _, n := range names {
			fmt.Fprintf(stdout, "  - %s\n", n)
		}
		fmt.Fprintln(stdout, "\n(List formatting is simplified in this Go build; use 'aikito show project <name>' for detail.)")
		return 0
	}

	var exact, prefix []string
	for _, n := range names {
		if n == target {
			exact = append(exact, n)
		} else if strings.HasPrefix(n, target) {
			prefix = append(prefix, n)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = prefix
	}
	if len(matches) == 0 {
		fmt.Fprintf(stderr, "[ERROR] Project '%s' not found.\n", target)
		return 1
	}
	if len(matches) > 1 {
		fmt.Fprintf(stderr, "[CONFLICT] Multiple projects match '%s': %s\n", target, strings.Join(matches, ", "))
		return 1
	}

	cfg, err := project.LoadConfig(aikitoDir, env.Home, matches[0])
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	binding := cfg.Binding()
	views := project.CandidatePathViews(binding, env.Home)

	fmt.Fprintf(stdout, "Project: %s\n", cfg.Name)
	fmt.Fprintf(stdout, "Config:  %s\n", cfg.Path)
	fmt.Fprintf(stdout, "Paths:   %s\n", project.JoinedCandidatePaths(views))
	if skills, ok := cfg.Raw["skills"].([]any); ok && len(skills) > 0 {
		var skillNames []string
		for _, s := range skills {
			skillNames = append(skillNames, fmt.Sprint(s))
		}
		fmt.Fprintf(stdout, "Skills:  %s\n", strings.Join(skillNames, ", "))
	} else {
		fmt.Fprintln(stdout, "Skills:  (none selected)")
	}
	syncMode := "link"
	if sm, ok := cfg.Raw["sync_mode"].(string); ok && sm != "" {
		syncMode = sm
	}
	fmt.Fprintf(stdout, "Sync mode: %s\n", syncMode)
	return 0
}

// --- skill ---

func cmdShowSkill(args []string, aikitoDir string, stdout, stderr io.Writer) int {
	var target string
	if len(args) > 0 {
		target = args[0]
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
	sort.Strings(names)

	if target == "" {
		if len(names) == 0 {
			fmt.Fprintln(stdout, "No skills found.")
			return 0
		}
		fmt.Fprintln(stdout, "Skills:")
		for _, n := range names {
			fmt.Fprintf(stdout, "  - %s\n", n)
		}
		return 0
	}

	matched, ok := resolveByName(names, target, "show", resolveLabels{
		conflictNoun: "skills", specifyLine: "Please specify the exact skill name, e.g.:",
		cmdName: "skill", notFoundSingular: "Skill",
		notFoundHint: "Run 'aikito show skills' to view available skills.",
		scopeSuffix:  "Global",
	}, stderr)
	if !ok {
		return 1
	}
	return printFileVerbatim(filepath.Join(skillsDir, matched, "SKILL.md"), stdout, stderr)
}

// --- instructions ---

func cmdShowInstructions(args []string, aikitoDir string, env Environment, stdout, stderr io.Writer) int {
	var target string
	if len(args) > 0 {
		target = args[0]
	}
	globalPath := filepath.Join(aikitoDir, "global", "AGENTS.md")

	if target == "" {
		fmt.Fprintf(stdout, "Global instructions: %s\n", displayPathRelativeToHome(globalPath, env.Home))
		if _, err := os.Stat(globalPath); err != nil {
			fmt.Fprintln(stdout, "  (not yet materialized)")
		}
		snap, err := workspace.SnapshotWorkspace(aikitoDir, env.Home)
		if err == nil {
			projNames := resourceNamesOfKind(snap, "project")
			if len(projNames) > 0 {
				fmt.Fprintln(stdout, "\nProject instructions:")
				for _, p := range projNames {
					path := filepath.Join(aikitoDir, "projects", p, "AGENTS.md")
					status := "present"
					if _, err := os.Stat(path); err != nil {
						status = "not set"
					}
					fmt.Fprintf(stdout, "  %-20s %s\n", p, status)
				}
			}
		}
		fmt.Fprintln(stdout, "\n(This Go build shows presence only; it does not yet port the per-agent link/drift status table.)")
		return 0
	}

	if target == "global" {
		if _, err := os.Stat(globalPath); err != nil {
			fmt.Fprintf(stderr, "[ERROR] Instructions file not found: %s\n", globalPath)
			return 1
		}
		return printFileVerbatim(globalPath, stdout, stderr)
	}

	projectPath := filepath.Join(aikitoDir, "projects", target, "AGENTS.md")
	if _, err := os.Stat(filepath.Join(aikitoDir, "projects", target, "agent.toml")); err != nil {
		fmt.Fprintf(stderr, "[ERROR] Instructions target '%s' not found.\n", target)
		return 1
	}
	if _, err := os.Stat(projectPath); err != nil {
		fmt.Fprintf(stderr, "[ERROR] Instructions file not found: %s\n", projectPath)
		return 1
	}
	rc := printFileVerbatim(projectPath, stdout, stderr)
	if rc == 0 {
		fmt.Fprintln(stderr, "\nAlso active: global instructions")
		fmt.Fprintln(stderr, "View with: aikito show instructions global")
	}
	return rc
}

// --- mcp ---

func mcpNames(aikitoDir string) ([]string, error) {
	mcpsDir := filepath.Join(aikitoDir, "mcps")
	entries, err := os.ReadDir(mcpsDir)
	if err != nil {
		return nil, fmt.Errorf("MCP directory not found at %s", mcpsDir)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".toml"))
	}
	sort.Strings(names)
	return names, nil
}

func cmdShowMCP(args []string, aikitoDir string, stdout, stderr io.Writer) int {
	var target string
	var agentFlag, liveFlag bool
	for _, a := range args {
		switch {
		case a == "--live":
			liveFlag = true
		case a == "--agent" || strings.HasPrefix(a, "--agent="):
			agentFlag = true
		case strings.HasPrefix(a, "-"):
			// ignore unrecognized flags rather than hard-fail on them
		default:
			if target == "" {
				target = a
			}
		}
	}
	if liveFlag {
		fmt.Fprintln(stderr, "[ERROR] --live is not yet implemented in this Go build (needs internal/mcp/probe.go wired in here).")
		return 2
	}
	if agentFlag {
		fmt.Fprintln(stderr, "[ERROR] --agent cross-agent detail views are not yet implemented in this Go build.")
		return 2
	}

	names, err := mcpNames(aikitoDir)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	if target == "" {
		if len(names) == 0 {
			fmt.Fprintln(stdout, "No MCP servers configured.")
			return 0
		}
		fmt.Fprintln(stdout, "MCP servers:")
		for _, n := range names {
			fmt.Fprintf(stdout, "  - %s\n", n)
		}
		return 0
	}

	matched, ok := resolveByName(names, target, "show", resolveLabels{
		conflictNoun: "MCP servers", specifyLine: "Please specify the exact MCP server name, e.g.:",
		cmdName: "mcp", notFoundSingular: "MCP server",
		notFoundHint: "Run 'aikito show mcp' to view available MCP servers.",
	}, stderr)
	if !ok {
		return 1
	}
	return printFileVerbatim(filepath.Join(aikitoDir, "mcps", matched+".toml"), stdout, stderr)
}

// --- subagent ---

func subagentNames(aikitoDir string) []string {
	subDir := filepath.Join(aikitoDir, "subagents")
	entries, _ := os.ReadDir(subDir)
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(names)
	return names
}

func cmdShowSubagent(args []string, aikitoDir string, stdout, stderr io.Writer) int {
	var target string
	var agentFlag bool
	for _, a := range args {
		switch {
		case a == "--agent" || strings.HasPrefix(a, "--agent="):
			agentFlag = true
		case strings.HasPrefix(a, "-"):
		default:
			if target == "" {
				target = a
			}
		}
	}
	if agentFlag {
		fmt.Fprintln(stderr, "[ERROR] --agent cross-agent detail views are not yet implemented in this Go build.")
		return 2
	}

	names := subagentNames(aikitoDir)
	if target == "" {
		if len(names) == 0 {
			fmt.Fprintln(stdout, "No subagents configured.")
			return 0
		}
		fmt.Fprintln(stdout, "Subagents:")
		for _, n := range names {
			fmt.Fprintf(stdout, "  - %s\n", n)
		}
		return 0
	}

	matched, ok := resolveByName(names, target, "show", resolveLabels{
		conflictNoun: "subagents", specifyLine: "Please specify the exact subagent name, e.g.:",
		cmdName: "subagent", notFoundSingular: "Subagent",
		notFoundHint: "Run 'aikito show subagents' to view available subagents.",
	}, stderr)
	if !ok {
		return 1
	}
	return printFileVerbatim(filepath.Join(aikitoDir, "subagents", matched+".md"), stdout, stderr)
}

// --- inbox ---

func inboxDir(aikitoDir string) string {
	// Simplified: this Go build does not yet port config.toml's
	// configurable inbox.path (internal/workspace's scanner does); default
	// convention only.
	return filepath.Join(aikitoDir, "inbox")
}

func cmdShowInbox(args []string, aikitoDir string, stdout, stderr io.Writer) int {
	var target string
	if len(args) > 0 {
		target = args[0]
	}
	dir := inboxDir(aikitoDir)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(stdout, "Inbox directory does not exist: %s\n", dir)
		return 0
	}

	var names []string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr == nil {
			names = append(names, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(names)

	if target == "" {
		if len(names) == 0 {
			fmt.Fprintf(stdout, "Inbox is empty (%s).\n", dir)
			return 0
		}
		fmt.Fprintln(stdout, "Inbox notes:")
		for _, n := range names {
			fmt.Fprintf(stdout, "  - %s\n", n)
		}
		return 0
	}

	matched, ok := resolveByName(names, target, "show", resolveLabels{
		conflictNoun: "inbox notes", specifyLine: "Please specify the exact name, e.g.:",
		cmdName: "inbox", notFoundSingular: "Inbox note",
		notFoundHint: "Run 'aikito show inbox' to view available inbox files.",
	}, stderr)
	if !ok {
		return 1
	}
	return printFileVerbatim(filepath.Join(dir, filepath.FromSlash(matched)), stdout, stderr)
}

// --- memory ---

func cmdShowMemory(args []string, aikitoDir string, stdout, stderr io.Writer) int {
	var target, projectFlag string
	var allFlag bool
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--project":
			i++
			if i < len(args) {
				projectFlag = args[i]
			}
		case a == "--all":
			allFlag = true
		case strings.HasPrefix(a, "-"):
		default:
			if target == "" {
				target = a
			}
		}
	}
	if projectFlag != "" && allFlag {
		fmt.Fprintln(stderr, "[ERROR] Cannot specify both --project and --all.")
		return 1
	}

	var dirs []string
	var labels []string
	if projectFlag != "" && projectFlag != "global" {
		dirs = append(dirs, filepath.Join(aikitoDir, "projects", projectFlag, "memory"))
		labels = append(labels, projectFlag)
	} else if allFlag {
		dirs = append(dirs, filepath.Join(aikitoDir, "memory"))
		labels = append(labels, "global")
		projEntries, _ := os.ReadDir(filepath.Join(aikitoDir, "projects"))
		for _, e := range projEntries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(aikitoDir, "projects", e.Name(), "memory"))
				labels = append(labels, e.Name())
			}
		}
	} else {
		dirs = append(dirs, filepath.Join(aikitoDir, "memory"))
		labels = append(labels, "global")
	}

	// find_memory_files (memory.py) only ever looks under <scope>/memory/notes/*.md
	// (a non-recursive glob("*.md"), never flat memory/*.md and never a
	// deeper nested subdirectory under notes/), matched by bare filename
	// stem. An earlier version of this function walked the whole memory/
	// tree and keyed by notes/-prefixed relative path, which a bare stem
	// can never prefix-match — the same bug internal/cli/rm.go's memory
	// removal hit and fixed, cross-validated against live Python there;
	// mirrored here so `show memory <name>` can actually resolve a note.
	type note struct{ label, stem, path string }
	var notes []note
	for i, dir := range dirs {
		notesDir := filepath.Join(dir, "notes")
		entries, _ := os.ReadDir(notesDir)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			stem := strings.TrimSuffix(e.Name(), ".md")
			notes = append(notes, note{labels[i], stem, filepath.Join(notesDir, e.Name())})
		}
	}
	sort.Slice(notes, func(i, j int) bool {
		if notes[i].label != notes[j].label {
			return notes[i].label < notes[j].label
		}
		return notes[i].stem < notes[j].stem
	})

	if target == "" {
		if len(notes) == 0 {
			fmt.Fprintln(stdout, "No memory notes found.")
			return 0
		}
		fmt.Fprintln(stdout, "Memory notes:")
		for _, n := range notes {
			fmt.Fprintf(stdout, "  - [%s] %s\n", n.label, n.stem)
		}
		return 0
	}

	// Matching mirrors memory.py's match_keys: a bare stem (no "/") is
	// always a valid match key regardless of scope count — two notes with
	// the same stem in different scopes correctly fall through to
	// resolveByName's conflict path rather than one silently shadowing the
	// other — and a "label/stem" qualified form is also offered whenever
	// more than one scope is in play, for disambiguation.
	var names []string
	byName := map[string]string{}
	for _, n := range notes {
		names = append(names, n.stem)
		byName[n.stem] = n.path
		if len(dirs) > 1 {
			qualified := n.label + "/" + n.stem
			names = append(names, qualified)
			byName[qualified] = n.path
		}
	}
	// notFoundSingular/notFoundHint here are a best-effort pattern match,
	// not source-confirmed the way the conflict path above is (this
	// session's time budget covered memory.py's confirmed
	// MemoryTargetConflictError text but not its no-match fallthrough).
	matched, ok := resolveByName(names, target, "show", resolveLabels{
		conflictNoun: "memory notes", specifyLine: "Please specify the full identifier, e.g.:",
		cmdName: "memory", notFoundSingular: "Memory note",
		notFoundHint: "Run 'aikito show memory' to view available notes.",
	}, stderr)
	if !ok {
		return 1
	}
	return printFileVerbatim(byName[matched], stdout, stderr)
}
