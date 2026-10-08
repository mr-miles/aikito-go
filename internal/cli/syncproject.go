package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-rs/internal/project"
	"github.com/mr-miles/aikito-rs/internal/registry"
	"github.com/mr-miles/aikito-rs/internal/sync"
	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// cmdSyncProject implements `aikito sync project [name] [--dry-run]
// [--verbose] [--force] [--prune]`, built on internal/sync's deliberately
// simplified project-skill/memory/instructions plan builders (see
// projectskills.go's package doc for what's NOT a full port of
// project_sync.py here, and why). --prune is accepted but not yet
// implemented (no stale-entry tracking), matching this build's existing
// `sync global`/`sync subagents` convention for that flag.
func cmdSyncProject(args []string, stdout, stderr io.Writer, env Environment) int {
	dryRun, verbose, force, prune := false, false, false, false
	var name string
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
			if name == "" {
				name = a
			}
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

	var names []string
	if name != "" {
		names = []string{name}
	} else {
		entries, _ := os.ReadDir(filepath.Join(aikitoDir, "projects"))
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
	}
	if len(names) == 0 {
		fmt.Fprintln(stdout, "[INFO] No registered projects to synchronize.")
		return 0
	}

	reg := registry.Load(aikitoDir, env.Home)

	var conflicts, changes int

	type planned struct {
		projectName, checkout string
		skillOps              []sync.ProjectSkillOperation
		instrOps, memOps      []sync.LinkOperation
	}
	var plans []planned

	for _, pname := range names {
		cfg, err := project.LoadConfig(aikitoDir, env.Home, pname)
		if err != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return 1
		}
		binding := cfg.Binding()
		active := binding.ActiveEntries()
		if len(active) == 0 {
			fmt.Fprintf(stdout, "[SKIP] %-20s no active checkout found on this host\n", pname)
			continue
		}

		var skillsSel []string
		if raw, ok := cfg.Raw["skills"].([]any); ok {
			for _, s := range raw {
				skillsSel = append(skillsSel, fmt.Sprint(s))
			}
		}
		syncMode := "link"
		if sm, ok := cfg.Raw["sync_mode"].(string); ok && sm != "" {
			syncMode = sm
		}

		for _, entry := range active {
			p := planned{projectName: pname, checkout: entry.ResolvedPath}

			skillOps, err := sync.BuildProjectSkillsPlan(aikitoDir, env.Home, pname, entry.ResolvedPath, skillsSel, syncMode, force)
			if err != nil {
				fmt.Fprintf(stderr, "[ERROR] %s: %v\n", pname, err)
				return 1
			}
			p.skillOps = skillOps

			if _, serr := os.Stat(filepath.Join(aikitoDir, "projects", pname, "AGENTS.md")); serr == nil {
				instrOps, err := sync.BuildProjectInstructionsPlan(aikitoDir, pname, entry.ResolvedPath, reg, force)
				if err != nil {
					fmt.Fprintf(stderr, "[ERROR] %s: %v\n", pname, err)
					return 1
				}
				p.instrOps = instrOps
			}

			memOps, err := sync.BuildProjectMemoryPlan(aikitoDir, pname, entry.ResolvedPath, force)
			if err != nil {
				fmt.Fprintf(stderr, "[ERROR] %s: %v\n", pname, err)
				return 1
			}
			p.memOps = memOps

			plans = append(plans, p)
		}
	}

	for _, p := range plans {
		for _, op := range p.skillOps {
			switch op.Action {
			case "CREATE", "UPDATE":
				changes++
				tag := "[" + op.Action + "]"
				if op.RequiresForce {
					tag = "[FORCE " + op.Action + "]"
				}
				fmt.Fprintf(stdout, "%s skill    %-20s %s\n", tag, p.projectName+"/"+op.Skill, op.TargetPath)
			case "NOOP":
				if verbose {
					fmt.Fprintf(stdout, "[OK]     skill    %-20s %s\n", p.projectName+"/"+op.Skill, op.TargetPath)
				}
			case "CONFLICT":
				conflicts++
				fmt.Fprintf(stdout, "[CONFLICT] skill  %-20s %s\n", p.projectName+"/"+op.Skill, op.Reason)
			}
		}
		for _, op := range p.instrOps {
			reportLink(stdout, "instr", p.projectName, "", op, verbose, &conflicts, &changes)
		}
		for _, op := range p.memOps {
			reportLink(stdout, "memory", p.projectName, op.ResourceName, op, verbose, &conflicts, &changes)
		}
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
	for _, p := range plans {
		for _, op := range p.skillOps {
			if op.Action == "CREATE" || op.Action == "UPDATE" {
				if err := sync.ApplyProjectSkillOperation(env.Home, p.projectName, op); err != nil {
					fmt.Fprintf(stderr, "[ERROR] %s/%s: %v\n", p.projectName, op.Skill, err)
					return 1
				}
				applied++
			}
		}
		for _, op := range p.instrOps {
			if op.Action == sync.LinkCreate {
				if err := sync.ApplySymlink(op); err != nil {
					fmt.Fprintf(stderr, "[ERROR] %s: %v\n", p.projectName, err)
					return 1
				}
				applied++
			}
		}
		for _, op := range p.memOps {
			if op.Action == sync.LinkCreate {
				if err := sync.ApplySymlink(op); err != nil {
					fmt.Fprintf(stderr, "[ERROR] %s/%s: %v\n", p.projectName, op.ResourceName, err)
					return 1
				}
				applied++
			}
		}
	}

	fmt.Fprintf(stdout, "\n[SUCCESS] Synced project resources: %d change(s) applied.\n", applied)
	return 0
}

func reportLink(stdout io.Writer, label, project, resource string, op sync.LinkOperation, verbose bool, conflicts, changes *int) {
	name := project
	if resource != "" {
		name = project + "/" + resource
	}
	switch op.Action {
	case sync.LinkCreate:
		*changes++
		tag := "[CREATE]"
		if op.RequiresForce {
			tag = "[FORCE CREATE]"
		}
		fmt.Fprintf(stdout, "%s %-9s%-20s %s\n", tag, label, name, op.TargetPath)
	case sync.LinkNoop:
		if verbose {
			fmt.Fprintf(stdout, "[OK]     %-9s%-20s %s\n", label, name, op.TargetPath)
		}
	case sync.LinkConflict:
		*conflicts++
		fmt.Fprintf(stdout, "[CONFLICT] %-9s%-20s %s\n", label, name, op.Reason)
	}
}
