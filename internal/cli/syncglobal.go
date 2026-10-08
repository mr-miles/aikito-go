package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/mr-miles/aikito-rs/internal/registry"
	"github.com/mr-miles/aikito-rs/internal/sync"
	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// cmdSyncGlobal implements `aikito sync global [--dry-run] [--verbose]
// [--force] [--prune]`. See internal/sync/link.go's package doc: this is a
// deliberately simplified per-agent symlink model, not a full port of
// global_skills.py/instructions.py/link.py's shared-container +
// resolve_targets architecture (which depends on agents.resolve_targets,
// already flagged as deferred in internal/registry/targets_todo.go).
//
// --prune is accepted but not yet implemented: Python's prune removes
// deselected/stale entries from an agent's managed skills directory; this
// build only ever plans CREATE/NOOP/CONFLICT for currently-selected skills
// and configured agents, so there is nothing to prune yet. A clear notice
// is printed rather than silently ignoring the flag.
func cmdSyncGlobal(args []string, stdout, stderr io.Writer, env Environment) int {
	dryRun := false
	verbose := false
	force := false
	prune := false
	for _, a := range args {
		switch {
		case a == "--dry-run":
			dryRun = true
		case a == "--verbose":
			verbose = true
		case a == "--force":
			force = true
		case a == "--prune":
			prune = true
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "[ERROR] Unknown flag: %s\n", a)
			return 2
		default:
			fmt.Fprintf(stderr, "[ERROR] Unexpected argument: %s\n", a)
			return 2
		}
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
	if prune {
		fmt.Fprintln(stdout, "[INFO] --prune is not yet implemented in this Go build (no stale-entry tracking yet); continuing without it.")
	}

	reg := registry.Load(aikitoDir, env.Home)

	skillItems, err := sync.BuildGlobalSkillsPlan(aikitoDir, env.Home, reg, force)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	instrItems, err := sync.BuildGlobalInstructionsPlan(aikitoDir, env.Home, reg, force)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	var conflicts int
	var changes int
	report := func(label, agent, resource string, op sync.LinkOperation) {
		switch op.Action {
		case sync.LinkCreate:
			changes++
			tag := "[CREATE]"
			if op.RequiresForce {
				tag = "[FORCE CREATE]"
			}
			name := agent
			if resource != "" {
				name = agent + "/" + resource
			}
			fmt.Fprintf(stdout, "%s %-9s %-30s %s\n", tag, label, name, op.TargetPath)
		case sync.LinkNoop:
			if verbose {
				name := agent
				if resource != "" {
					name = agent + "/" + resource
				}
				fmt.Fprintf(stdout, "[OK]     %-9s %-30s %s\n", label, name, op.TargetPath)
			}
		case sync.LinkConflict:
			conflicts++
			name := agent
			if resource != "" {
				name = agent + "/" + resource
			}
			fmt.Fprintf(stdout, "[CONFLICT] %-9s %-30s %s\n", label, name, op.Reason)
		}
	}

	for _, item := range skillItems {
		report("skill", item.Op.Agent, item.Skill, item.Op)
	}
	for _, item := range instrItems {
		report("instr", item.Op.Agent, "", item.Op)
	}

	if conflicts > 0 {
		fmt.Fprintf(stdout, "\n[BLOCKED] %d conflict(s) found; rerun with --force to overwrite, or resolve manually.\n", conflicts)
		if dryRun {
			return 0
		}
		return 1
	}

	if dryRun {
		fmt.Fprintf(stdout, "\n[DRY RUN] %d change(s) would be made.\n", changes)
		return 0
	}

	applied := 0
	for _, item := range skillItems {
		if item.Op.Action == sync.LinkCreate {
			if err := sync.ApplySymlink(item.Op); err != nil {
				fmt.Fprintf(stderr, "[ERROR] %s/%s: %v\n", item.Op.Agent, item.Skill, err)
				return 1
			}
			applied++
		}
	}
	for _, item := range instrItems {
		if item.Op.Action == sync.LinkCreate {
			if err := sync.ApplySymlink(item.Op); err != nil {
				fmt.Fprintf(stderr, "[ERROR] %s: %v\n", item.Op.Agent, err)
				return 1
			}
			applied++
		}
	}

	fmt.Fprintf(stdout, "\n[SUCCESS] Synced global skills/instructions: %d change(s) applied.\n", applied)
	return 0
}
