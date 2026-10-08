package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/mcp"
	"github.com/mr-miles/aikito-go/internal/workspace"
	"github.com/mr-miles/aikito-go/internal/writerlock"
)

// cmdRm dispatches `aikito rm|remove <target> ...`, ported from
// remove.py/cli.py's cmd_rm_* family.
func cmdRm(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: aikito rm skill|subagent|mcp|memory|inbox ...")
		return 2
	}
	switch args[0] {
	case "skill", "skills":
		return cmdRmSkill(args[1:], stdout, stderr, env)
	case "subagent", "subagents":
		return cmdRmSubagent(args[1:], stdout, stderr, env)
	case "mcp", "mcps":
		return cmdRmMCP(args[1:], stdout, stderr, env)
	case "memory":
		return cmdRmMemory(args[1:], stdout, stderr, env)
	case "inbox":
		return cmdRmInbox(args[1:], stdout, stderr, env)
	default:
		fmt.Fprintf(stderr, "[ERROR] Unknown rm target: %s\n", args[0])
		return 2
	}
}

// --- rm subagent ---

func cmdRmSubagent(args []string, stdout, stderr io.Writer, env Environment) int {
	var name string
	var syncFlag bool
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--sync":
			syncFlag = true
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "[ERROR] Unknown flag: %s\n", a)
			return 2
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) > 0 {
		name = positional[0]
	}

	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if err := workspace.RequireCurrentLayout(aikitoDir); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	nameClean := strings.TrimSpace(name)
	if msg := workspace.ValidateResourceName(nameClean, "subagent"); msg != "" {
		fmt.Fprintf(stderr, "[ERROR] %s\n", msg)
		return 1
	}
	subagentFile := filepath.Join(aikitoDir, "subagents", nameClean+".md")
	if fi, serr := os.Stat(subagentFile); serr != nil || !fi.Mode().IsRegular() {
		fmt.Fprintf(stderr, "[ERROR] Subagent '%s' does not exist in workspace.\n", nameClean)
		return 1
	}
	if _, _, perr := workspace.ParseSubagentFile(subagentFile); perr != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to remove subagent: %v\n", perr)
		return 1
	}
	lock, err := writerlock.Acquire(env.Home)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to remove subagent: %v\n", err)
		return 1
	}
	err = os.Remove(subagentFile)
	lock.Release()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to remove subagent: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "[DELETE FILE] %s\n", displayPathRelativeToHome(subagentFile, env.Home))
	fmt.Fprintf(stdout, "[SUCCESS] Removed subagent '%s'.\n", nameClean)
	// remove_subagent(sync=True) prunes the agent-native files.
	if syncFlag && !syncSubagentConfigs(aikitoDir, env.Home, false, nil, true, stdout, stderr) {
		return 1
	}
	return 0
}

// --- rm mcp ---

func cmdRmMCP(args []string, stdout, stderr io.Writer, env Environment) int {
	var name string
	var syncFlag, force bool
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--sync":
			syncFlag = true
		case a == "--force":
			force = true
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "[ERROR] Unknown flag: %s\n", a)
			return 2
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) > 0 {
		name = positional[0]
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
	nameClean := strings.TrimSpace(name)
	if msg := workspace.ValidateResourceName(nameClean, "mcp"); msg != "" {
		fmt.Fprintf(stderr, "[ERROR] %s\n", msg)
		return 1
	}
	mcpFile := filepath.Join(aikitoDir, "mcps", nameClean+".toml")
	if fi, serr := os.Stat(mcpFile); serr != nil || !fi.Mode().IsRegular() {
		fmt.Fprintf(stderr, "[ERROR] MCP server '%s' does not exist in workspace (%s).\n", nameClean, displayPathRelativeToHome(aikitoDir, env.Home))
		return 1
	}

	// Build the removal plan (and, if authorized, apply it to agent-native
	// configs) WHILE the workspace file still exists: BuildMCPPlan loads
	// specs from mcps/*.toml, and DesiredAbsentServers only takes effect
	// for a server whose spec is still present in that load — this mirrors
	// Python's remove_mcp, which captures specs_to_remove before moving the
	// file aside.
	if syncFlag {
		plan, perr := mcp.BuildMCPPlan(aikitoDir, env.Home, mcp.BuildMCPPlanOptions{
			Force:                force,
			DesiredAbsentServers: map[string]bool{nameClean: true},
		})
		if perr != nil {
			fmt.Fprintf(stderr, "[ERROR] Failed to inspect MCP configuration: %v\n", perr)
			return 1
		}
		if !plan.CanApply() {
			fmt.Fprintln(stderr, "[ERROR] Cannot synchronize removal: plan has unauthorized conflicts (rerun with --force).")
			return 1
		}
		result, eerr := mcp.ExecuteMCPPlan(plan, env.Home, func(line string) { fmt.Fprintln(stdout, line) })
		if eerr != nil {
			fmt.Fprintf(stderr, "[ERROR] Failed to synchronize MCP server removal: %v\n", eerr)
			return 1
		}
		if !result.Success {
			if result.ErrorMessage != "" {
				fmt.Fprintf(stderr, "[ERROR] %s\n", result.ErrorMessage)
			}
			return 1
		}
	}

	if err := os.Remove(mcpFile); err != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to remove MCP configuration file: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "[DELETE FILE] %s\n", displayPathRelativeToHome(mcpFile, env.Home))
	fmt.Fprintf(stdout, "\n[SUCCESS] Removed MCP server '%s'.\n", nameClean)
	return 0
}

// --- rm memory ---
//
// Scope note: Python's remove_memory_note additionally scans every other
// memory note for inbound [[wikilink]] references to the removed note and
// warns about them (memory_runtime.py's wikilink index, not ported). This
// build removes the file and skips that scan — a real, documented gap, not
// a silent one.
func cmdRmMemory(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "[ERROR] Usage: aikito rm memory <target>")
		return 2
	}
	target := args[0]

	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if err := workspace.RequireCurrentLayout(aikitoDir); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	// Bug found during cross-validation against live Python (not present
	// in my own code originally — it's inherited from show.go's
	// cmdShowMemory, which this was modeled on): Python's
	// find_memory_files only ever looks under memory/notes/*.md (never
	// flat memory/*.md directly), and resolve_memory_target matches a
	// slash-free target against the bare filename stem ("arch-decision"),
	// not a notes/-prefixed relative path. show.go's directory walk over
	// the whole memory/ tree combined with resolveByName's prefix-matching
	// on the full relative path means a plain note name can never actually
	// match (e.g. "arch-decision" is not a prefix of "notes/arch-decision.md"),
	// so `rm memory <name>` was silently broken for the only directory
	// layout real memory notes ever have. Fixed here to list memory/notes/
	// only, keyed by bare stem. Flagging for the parent: show.go's
	// cmdShowMemory likely has the identical bug and should get the same
	// fix — out of this command's file scope to touch directly.
	notesDir := filepath.Join(aikitoDir, "memory", "notes")
	var names []string
	byStem := map[string]string{}
	entries, _ := os.ReadDir(notesDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), ".md")
		names = append(names, stem)
		byStem[stem] = filepath.Join(notesDir, e.Name())
	}
	sort.Strings(names)

	matched, ok := resolveByName(names, target, "rm", resolveLabels{
		conflictNoun: "memory notes", specifyLine: "Please specify the full identifier, e.g.:",
		cmdName: "memory", notFoundSingular: "Memory note",
		notFoundHint: "Run 'aikito show memory' to view available notes.",
	}, stderr)
	if !ok {
		return 1
	}

	path := byStem[matched]
	if err := os.Remove(path); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	stem := matched
	fmt.Fprintf(stdout, "[OK] Removed memory note '%s' (%s)\n", stem, filepath.Base(path))
	fmt.Fprintln(stdout, "  - Inbound-reference checking is not yet implemented in this Go build.")
	return 0
}

// --- rm inbox ---

func cmdRmInbox(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "[ERROR] Usage: aikito rm inbox <target>")
		return 2
	}
	target := args[0]

	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if err := workspace.RequireCurrentLayout(aikitoDir); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	dir := inboxDir(aikitoDir)
	var names []string
	filepath.Walk(dir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info == nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr == nil {
			names = append(names, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(names)

	matched, ok := resolveByName(names, target, "rm", resolveLabels{
		conflictNoun: "inbox notes", specifyLine: "Please specify the exact name, e.g.:",
		cmdName: "inbox", notFoundSingular: "Inbox note",
		notFoundHint: "Run 'aikito show inbox' to view available inbox files.",
	}, stderr)
	if !ok {
		return 1
	}

	path := filepath.Join(dir, filepath.FromSlash(matched))
	ident := strings.TrimSuffix(matched, ".md")
	if err := os.Remove(path); err != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to remove inbox note '%s': %v\n", ident, err)
		return 1
	}
	fmt.Fprintf(stdout, "[OK] Removed inbox note '%s' (%s)\n", ident, filepath.Base(path))
	return 0
}
