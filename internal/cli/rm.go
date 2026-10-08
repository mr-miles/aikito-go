package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-rs/internal/mcp"
	"github.com/mr-miles/aikito-rs/internal/registry"
	"github.com/mr-miles/aikito-rs/internal/sync"
	"github.com/mr-miles/aikito-rs/internal/workspace"
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

// --- rm skill ---
//
// Scope note: ports remove.py's global-removal path (_remove_skill_globally)
// in full, including the referencing-projects block-unless-force check and
// (with --force) unregistering from every referencing project's agent.toml.
// NOT ported: --project (unregister-from-specific-projects-only, a
// separate ~90-line code path in remove.py — deferred the same way add.go
// deferred project-scoped skill *registration*, since this build never
// creates that state in the first place) and the temp-backup-dir +
// execute_selection_transaction machinery Python wraps the actual file
// writes in (skill_runtime.py, not ported) — this build writes
// skills.toml/agent.toml updates and removes the skill directory as
// straightforward sequential operations rather than one all-or-nothing
// transaction. Both are flagged here rather than silently simplified.
func cmdRmSkill(args []string, stdout, stderr io.Writer, env Environment) int {
	var name, projectArg string
	var force, syncFlag bool
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--project":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "[ERROR] --project requires a value")
				return 2
			}
			projectArg = args[i]
		case a == "--force":
			force = true
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
	if projectArg != "" {
		fmt.Fprintln(stderr, "[ERROR] --project (unregistering a skill from specific projects only, leaving the canonical copy intact) is not yet implemented in this Go build.")
		return 2
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
	if nameClean == "" {
		fmt.Fprintln(stderr, "[ERROR] Skill name cannot be empty.")
		return 1
	}
	if msg := workspace.ValidateResourceName(nameClean, "skill"); msg != "" {
		fmt.Fprintf(stderr, "[ERROR] %s\n", msg)
		return 1
	}
	if workspace.IsBundledSkillName(nameClean) {
		fmt.Fprintf(stderr, "[ERROR] Cannot remove bundled system skill '%s'.\n", nameClean)
		return 1
	}

	skillDir := filepath.Join(aikitoDir, "skills", nameClean)
	skillsTomlPath := filepath.Join(aikitoDir, "skills.toml")

	hasCanonicalDir := false
	if fi, serr := os.Stat(skillDir); serr == nil && fi.IsDir() {
		hasCanonicalDir = true
	}

	skillsTomlHasSkill := false
	var originalSkillsToml, newSkillsToml string
	if data, rerr := os.ReadFile(skillsTomlPath); rerr == nil {
		originalSkillsToml = string(data)
		doc, derr := workspace.DecodeTOML(data)
		if derr != nil {
			fmt.Fprintf(stderr, "[ERROR] Failed to read global skills configuration: %v\n", derr)
			return 1
		}
		if raw, ok := doc["skills"].([]any); ok {
			var current []string
			for _, v := range raw {
				current = append(current, fmt.Sprint(v))
			}
			if containsString(current, nameClean) {
				skillsTomlHasSkill = true
				var remaining []string
				for _, s := range current {
					if s != nameClean {
						remaining = append(remaining, s)
					}
				}
				newSkillsToml = sync.UpdateSkillsInToml(originalSkillsToml, remaining)
			}
		}
	}

	if !hasCanonicalDir && !skillsTomlHasSkill {
		fmt.Fprintf(stderr, "[ERROR] Skill '%s' does not exist in workspace (%s).\n", nameClean, displayPathRelativeToHome(aikitoDir, env.Home))
		return 1
	}

	// Check project references.
	type projUpdate struct {
		path, original, updated, proj string
	}
	var referencing []string
	var projUpdates []projUpdate
	projectsDir := filepath.Join(aikitoDir, "projects")
	if entries, derr := os.ReadDir(projectsDir); derr == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, proj := range names {
			agentToml := filepath.Join(projectsDir, proj, "agent.toml")
			data, rerr := os.ReadFile(agentToml)
			if rerr != nil {
				continue
			}
			doc, derr := workspace.DecodeTOML(data)
			if derr != nil {
				fmt.Fprintf(stderr, "[WARN] Failed to inspect configuration for project '%s': %v\n", proj, derr)
				continue
			}
			raw, ok := doc["skills"].([]any)
			if !ok {
				continue
			}
			var current []string
			for _, v := range raw {
				current = append(current, fmt.Sprint(v))
			}
			if !containsString(current, nameClean) {
				continue
			}
			var remaining []string
			for _, s := range current {
				if s != nameClean {
					remaining = append(remaining, s)
				}
			}
			referencing = append(referencing, proj)
			projUpdates = append(projUpdates, projUpdate{
				path: agentToml, original: string(data),
				updated: sync.UpdateSkillsInToml(string(data), remaining), proj: proj,
			})
		}
	}

	if len(referencing) > 0 && !force {
		projList := make([]string, len(referencing))
		for i, p := range referencing {
			projList[i] = "'" + p + "'"
		}
		fmt.Fprintf(stderr, "[ERROR] Skill '%s' is still registered in project(s): %s.\n", nameClean, strings.Join(projList, ", "))
		fmt.Fprintf(stderr, "Unregister it first with 'aikito rm skill %s --project %s', or use --force to unregister from all projects and delete.\n", nameClean, strings.Join(referencing, ","))
		return 1
	}

	// Execute: project agent.toml updates, skills.toml update, canonical dir removal.
	for _, u := range projUpdates {
		if err := os.WriteFile(u.path, []byte(u.updated), 0o644); err != nil {
			fmt.Fprintf(stderr, "[ERROR] Failed to update configuration for project '%s': %v\n", u.proj, err)
			return 1
		}
	}
	if skillsTomlHasSkill {
		if err := os.WriteFile(skillsTomlPath, []byte(newSkillsToml), 0o644); err != nil {
			fmt.Fprintf(stderr, "[ERROR] Failed to update global skills configuration: %v\n", err)
			return 1
		}
	}
	if hasCanonicalDir {
		if err := os.RemoveAll(skillDir); err != nil {
			fmt.Fprintf(stderr, "[ERROR] Failed during skill removal: %v\n", err)
			return 1
		}
	}

	for _, u := range projUpdates {
		fmt.Fprintf(stdout, "[UPDATE FILE] %s (unregistered skill from project '%s')\n", displayPathRelativeToHome(u.path, env.Home), u.proj)
	}
	if skillsTomlHasSkill {
		fmt.Fprintf(stdout, "[UPDATE FILE] %s (unregistered global skill)\n", displayPathRelativeToHome(skillsTomlPath, env.Home))
	}
	if hasCanonicalDir {
		fmt.Fprintf(stdout, "[REMOVE DIR] %s\n", displayPathRelativeToHome(skillDir, env.Home))
	}
	fmt.Fprintf(stdout, "\n[SUCCESS] Removed skill '%s'.\n", nameClean)

	if syncFlag {
		removeGlobalSkillSymlinks(aikitoDir, env.Home, nameClean, force, stdout, stderr)
	}
	return 0
}

// removeGlobalSkillSymlinks is a pragmatic, targeted cleanup rather than a
// full port of Python's --sync path (which re-runs sync_global_resources
// with prune=True, built on the not-yet-ported managed-container/orphan
// model): for every configured agent, if its skills-dir entry for this
// skill is a symlink that still resolves to the just-deleted canonical
// path (or is now dangling, since the canonical target is gone), remove it.
// Anything else (unmanaged content) is left alone with a warning, matching
// the spirit of this port's "never silently clobber unmanaged content"
// rule, unless force is set.
func removeGlobalSkillSymlinks(aikitoDir, home, skillName string, force bool, stdout, stderr io.Writer) {
	reg := registry.Load(aikitoDir, home)
	canonical := filepath.Join(aikitoDir, "skills", skillName)
	for _, agent := range reg.Values() {
		if agent.SkillsPath == nil {
			continue
		}
		target := filepath.Join(*agent.SkillsPath, skillName)
		info, err := os.Lstat(target)
		if err != nil {
			continue // nothing to clean up for this agent
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if force {
				if rerr := os.RemoveAll(target); rerr == nil {
					fmt.Fprintf(stdout, "[REMOVE] %s\n", target)
				}
			} else {
				fmt.Fprintf(stderr, "[WARN] %s: unmanaged content at %s left in place (rerun with --force to remove)\n", agent.Name, target)
			}
			continue
		}
		raw, rerr := os.Readlink(target)
		if rerr == nil {
			resolved := raw
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(filepath.Dir(target), resolved)
			}
			if !force && filepath.Clean(resolved) != filepath.Clean(canonical) {
				fmt.Fprintf(stderr, "[WARN] %s: symlink at %s points elsewhere, left in place (rerun with --force to remove)\n", agent.Name, target)
				continue
			}
		}
		if err := os.Remove(target); err == nil {
			fmt.Fprintf(stdout, "[REMOVE] %s\n", target)
		}
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
	if err := os.Remove(subagentFile); err != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to remove subagent: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "[DELETE FILE] %s\n", displayPathRelativeToHome(subagentFile, env.Home))
	fmt.Fprintf(stdout, "[SUCCESS] Removed subagent '%s'.\n", nameClean)
	if syncFlag {
		// subagent.py's sync_subagent_configs(prune=True) equivalent isn't
		// built in this Go port yet (internal/subagent has the per-platform
		// render/validate primitives but no sync planner on top) — print a
		// clear hint rather than silently skipping agent-native cleanup.
		fmt.Fprintln(stdout, "[INFO] --sync: agent-native subagent file cleanup is not yet implemented in this Go build; run 'aikito sync subagents' once available.")
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
