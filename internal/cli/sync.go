package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/mr-miles/aikito-go/internal/mcp"
)

// cmdSync dispatches `aikito sync [--dry-run] [--verbose] [<target> ...]`
// as cli_parser.py's p_sync does. With no target it runs the whole-workspace
// sync (cmd_sync_all). The parent --dry-run carries into the subcommand,
// as argparse leaves it set on the shared namespace; --verbose only affects
// the whole-workspace form.
func cmdSync(args []string, stdout, stderr io.Writer, env Environment) int {
	parentFlags := []string{"--dry-run", "--verbose"}
	set := map[string]bool{}
	var extras []string
	i := 0
loop:
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			break loop
		case a == "-" || !strings.HasPrefix(a, "-"):
			break loop
		}
		var matches []string
		for _, f := range parentFlags {
			if f == a {
				matches = []string{f}
				break
			}
			if strings.HasPrefix(a, "--") && strings.HasPrefix(f, a) {
				matches = append(matches, f)
			}
		}
		switch len(matches) {
		case 0:
			extras = append(extras, a)
		case 1:
			set[matches[0]] = true
		default:
			fmt.Fprintf(stderr, "%saikito sync: error: ambiguous option: %s could match %s\n", subcommandUsage("sync"), a, strings.Join(matches, ", "))
			return 2
		}
	}
	if len(extras) > 0 {
		fmt.Fprintf(stderr, "%saikito: error: unrecognized arguments: %s\n", rootUsage(), strings.Join(extras, " "))
		return 2
	}
	rest := args[i:]
	if len(rest) == 0 {
		return cmdSyncAll(set["--dry-run"], set["--verbose"], stdout, stderr, env)
	}
	sub := rest[1:]
	if set["--dry-run"] {
		sub = append([]string{"--dry-run"}, sub...)
	}
	switch rest[0] {
	case "mcp":
		return cmdSyncMCP(sub, stdout, stderr, env)
	case "global":
		return cmdSyncGlobal(sub, stdout, stderr, env)
	case "subagents", "subagent":
		return cmdSyncSubagents(sub, stdout, stderr, env)
	case "project":
		return cmdSyncProject(sub, stdout, stderr, env)
	default:
		fmt.Fprintf(stderr, "%saikito sync: error: argument sync_target: invalid choice: '%s' (choose from global, project, mcp, subagents, subagent)\n",
			subcommandUsage("sync"), rest[0])
		return 2
	}
}

// cmdSyncMCP mirrors cli.py's cmd_mcp_sync + mcp/__init__.py's
// sync_mcp_configs: build the single shared MCP plan (mcp.BuildMCPPlan),
// report every operation's inspection status, optionally preview without
// writing (--dry-run), or apply it (mcp.ExecuteMCPPlan) and report the
// outcome. Flags per cli_parser.py's p_sync_mcp: only --dry-run and
// --force (a plain boolean — no per-target force_targets at the CLI layer
// for mcp, unlike subagents' sync).
func cmdSyncMCP(args []string, stdout, stderr io.Writer, env Environment) int {
	dryRun := false
	force := false
	for _, a := range args {
		switch {
		case a == "--dry-run":
			dryRun = true
		case a == "--force":
			force = true
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

	plan, err := mcp.BuildMCPPlan(aikitoDir, env.Home, mcp.BuildMCPPlanOptions{Force: force})
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	// Inspection output: one line per operation, exact wording/ordering
	// from sync_mcp_configs (SKIP/NOOP/CONFLICT only — CREATE/UPDATE/REMOVE
	// produce no inspection-phase line; they're reported by the executor
	// during apply, or as [DRY-RUN] lines below during a dry run).
	for _, op := range plan.Operations {
		targetKey := op.Target.Agent + "/" + op.Target.LogicalIdentity
		switch op.Action {
		case "SKIP":
			switch {
			case op.Spec != nil && op.Spec.MissingCredentialEnv != "":
				fmt.Fprintf(stdout, "[WARN] %s: skipped due to missing credential environment variable: %s\n", targetKey, op.Spec.MissingCredentialEnv)
			case op.Spec != nil && !op.Spec.Enabled:
				fmt.Fprintf(stdout, "[SKIP] %s: %s\n", targetKey, op.Reason)
			default:
				fmt.Fprintf(stdout, "[SKIP] %s not detected: %s\n", op.Target.Agent, parentDir(op.Target.Path))
			}
		case "NOOP":
			fmt.Fprintf(stdout, "[OK] %s: already synchronized\n", targetKey)
		case "CONFLICT":
			fmt.Fprintf(stdout, "[CONFLICT] %s: existing config was not last written by aikito; review it or rerun with --force\n", targetKey)
		}
	}

	if dryRun {
		for _, op := range plan.Operations {
			if (op.Action == "CREATE" || op.Action == "UPDATE") && op.IsAuthorized {
				actionName := "create"
				if op.Action == "UPDATE" {
					actionName = "update"
				}
				fmt.Fprintf(stdout, "[DRY-RUN] %s: would %s entry\n", op.Target.Agent+"/"+op.Target.LogicalIdentity, actionName)
			}
		}
		// NOTE: Python's dry-run branch loads the state file, computes a
		// "converged" in-memory entries map for already-OK operations, and
		// then... never saves it. Confirmed by reading mcp/__init__.py
		// directly: there is no _save_state call in this branch. This is
		// dead code in the original, not a side effect to replicate — a
		// dry run here genuinely writes nothing, matching that reality
		// rather than the comment's stated intent.
		if !plan.CanApply() {
			return 1
		}
		return 0
	}

	if !plan.CanApply() {
		return 1
	}

	result, err := mcp.ExecuteMCPPlan(plan, env.Home, func(line string) {
		fmt.Fprintln(stdout, line)
	})
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if !result.Success {
		if result.ErrorMessage != "" {
			fmt.Fprintf(stderr, "[ERROR] %s\n", result.ErrorMessage)
		}
		return 1
	}
	return 0
}

func parentDir(path string) string {
	i := strings.LastIndexByte(path, '/')
	if i < 0 {
		return path
	}
	return path[:i]
}
