package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/mr-miles/aikito-rs/internal/sync"
)

// cmdSyncSubagents mirrors subagent.py's cmd_subagent_sync/
// sync_subagent_configs exactly, built on internal/sync.BuildSubagentPlan
// (ported from build_subagent_plan's decision logic — see that file's
// package doc for why no separate state file is needed here, unlike
// `sync mcp`). Flags per cli_parser.py's p_sync_subagent: --dry-run,
// --force (nargs="*": ZERO OR MORE explicit "<agent>/<subagent>" targets —
// bare --force with no targets is a hard error, not "force everything";
// subagent force is always per-target), --prune.
func cmdSyncSubagents(args []string, stdout, stderr io.Writer, env Environment) int {
	dryRun := false
	prune := false
	var forceTargets []string
	forceSeen := false
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--dry-run":
			dryRun = true
			i++
		case a == "--prune":
			prune = true
			i++
		case a == "--force":
			forceSeen = true
			i++
			for i < len(args) && !strings.HasPrefix(args[i], "-") {
				forceTargets = append(forceTargets, args[i])
				i++
			}
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
	if msg := checkWorkspaceInitialized(aikitoDir); msg != "" {
		fmt.Fprintf(stderr, "[ERROR] %s\n", msg)
		return 1
	}

	opts := sync.BuildSubagentPlanOptions{Prune: prune, GateInstalled: true}
	if forceSeen {
		if forceTargets == nil {
			forceTargets = []string{} // present-but-empty: triggers Python's "requires explicit target(s)" error
		}
		opts.ForceTargets = forceTargets
	}

	ops, err := sync.BuildSubagentPlan(aikitoDir, env.Home, opts)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	hasErrors := false
	hasUnforcedConflicts := false
	for _, op := range ops {
		if op.Action == sync.SAError {
			hasErrors = true
		}
		if op.Action == sync.SAConflict && !op.IsAuthorized {
			hasUnforcedConflicts = true
		}
	}

	fmt.Fprintf(stdout, "[INFO] Subagent synchronization plan (dry_run=%s):\n", pyBool(dryRun))
	for _, op := range ops {
		targetKey := op.Agent + "/" + op.Subagent
		switch op.Action {
		case sync.SASkip:
			fmt.Fprintf(stdout, "  [SKIP] %s (%s)\n", op.Agent, op.Reason)
		case sync.SANoop:
			fmt.Fprintf(stdout, "  [OK] %s\n", targetKey)
		case sync.SACreate:
			fmt.Fprintf(stdout, "  [CREATE] %s -> %s\n", targetKey, op.TargetPath)
		case sync.SAUpdate:
			if op.RequiresForce {
				fmt.Fprintf(stdout, "  [FORCE UPDATE] %s -> %s\n", targetKey, op.TargetPath)
			} else {
				fmt.Fprintf(stdout, "  [UPDATE] %s -> %s\n", targetKey, op.TargetPath)
			}
		case sync.SAConflict:
			fmt.Fprintf(stdout, "  [CONFLICT] %s -> %s (%s. Use --force %s to overwrite)\n", targetKey, op.TargetPath, op.Reason, targetKey)
		case sync.SARemove:
			fmt.Fprintf(stdout, "  [PRUNE] %s -> %s\n", targetKey, op.TargetPath)
		case sync.SAOrphan:
			fmt.Fprintf(stdout, "  [ORPHAN] %s -> %s (%s. Use --prune to remove)\n", targetKey, op.TargetPath, op.Reason)
		case sync.SAError:
			fmt.Fprintf(stdout, "  [ERROR] %s: %s\n", targetKey, op.Reason)
		}
	}

	if hasErrors {
		fmt.Fprintln(stderr, "[ERROR] Synchronization aborted due to errors in plan. No changes were made.")
		return 1
	}
	if hasUnforcedConflicts {
		fmt.Fprintln(stderr, "[WARN] Synchronization aborted due to unhandled conflicts. No changes were made.")
		return 1
	}
	if dryRun {
		fmt.Fprintln(stdout, "[SUCCESS] Subagent synchronization plan completed (dry-run).")
		return 0
	}

	for _, op := range ops {
		if op.Action == sync.SACreate || op.Action == sync.SAUpdate || op.Action == sync.SARemove {
			if err := sync.ApplySubagentOperation(op); err != nil {
				fmt.Fprintf(stderr, "[ERROR] Subagent synchronization failed: %v\n", err)
				return 1
			}
		}
	}

	fmt.Fprintln(stdout, "[SUCCESS] Subagent synchronization completed successfully.")
	return 0
}

func pyBool(b bool) string {
	if b {
		return "True"
	}
	return "False"
}
