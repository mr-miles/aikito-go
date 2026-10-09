package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-go/internal/mcp"
	"github.com/mr-miles/aikito-go/internal/workspace"
	"github.com/mr-miles/aikito-go/internal/writerlock"
)

// cmdRm dispatches `aikito rm|remove <target> ...`, ported from
// remove.py/cli.py's cmd_rm_* family.
// verb is "rm" or "remove", the spelling the user typed: argparse names it
// in every usage and error line.
func cmdRm(verb string, args []string, stdout, stderr io.Writer, env Environment) int {
	choices := "skill, skills, subagent, subagents, mcp, mcps, memory, inbox"
	if len(args) == 0 {
		return argparseRequired(stderr, verb, verb+"_target")
	}
	switch args[0] {
	case "skill", "skills":
		return cmdRmSkill(verb, args[1:], stdout, stderr, env)
	case "subagent", "subagents":
		return cmdRmSubagent(verb, args[1:], stdout, stderr, env)
	case "mcp", "mcps":
		return cmdRmMCP(verb, args[1:], stdout, stderr, env)
	case "memory":
		return cmdRmMemory(verb, args[1:], stdout, stderr, env)
	case "inbox":
		return cmdRmInbox(verb, args[1:], stdout, stderr, env)
	default:
		return argparseSubError(stderr, verb, fmt.Sprintf(
			"argument %s_target: invalid choice: '%s' (choose from %s)", verb, args[0], choices))
	}
}

// --- rm subagent ---

func cmdRmSubagent(verb string, args []string, stdout, stderr io.Writer, env Environment) int {
	path := verb + " subagent"
	parsed, ok := parseArgparseOpts(path, args, []string{"--sync"}, nil, nil, 1, stderr)
	if !ok {
		return 2
	}
	if len(parsed.positionals) == 0 {
		return argparseRequired(stderr, path, "name")
	}
	name := parsed.positionals[0]
	syncFlag := parsed.flags["--sync"]

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

func cmdRmMCP(verb string, args []string, stdout, stderr io.Writer, env Environment) int {
	path := verb + " mcp"
	parsed, ok := parseArgparseOpts(path, args, []string{"--sync", "--force"}, nil, nil, 1, stderr)
	if !ok {
		return 2
	}
	if len(parsed.positionals) == 0 {
		return argparseRequired(stderr, path, "name")
	}
	name := parsed.positionals[0]
	syncFlag, force := parsed.flags["--sync"], parsed.flags["--force"]

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

	// As remove.py's remove_mcp: capture this server's specs, move the
	// workspace file aside, then sync the removal for those specs only (so
	// --force can't touch other servers' hand edits), restoring the file if
	// the sync doesn't succeed.
	var specsToRemove []mcp.AgentSpec
	if syncFlag {
		all, lerr := mcp.LoadAgentSpecs(aikitoDir, env.Home)
		if lerr != nil {
			fmt.Fprintf(stderr, "[ERROR] Failed to inspect MCP configuration: %v\n", lerr)
			return 1
		}
		for _, s := range all {
			if s.Server == nameClean {
				specsToRemove = append(specsToRemove, s)
			}
		}
	}

	backupDir, err := os.MkdirTemp(filepath.Dir(mcpFile), "."+nameClean+".rm_backup.")
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to remove MCP configuration file: %v\n", err)
		return 1
	}
	defer os.RemoveAll(backupDir)
	stagedBackup := filepath.Join(backupDir, filepath.Base(mcpFile))
	if err := os.Rename(mcpFile, stagedBackup); err != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to remove MCP configuration file: %v\n", err)
		return 1
	}
	restore := func() { _ = os.Rename(stagedBackup, mcpFile) }

	if syncFlag && len(specsToRemove) > 0 {
		plan, perr := mcp.BuildMCPPlan(aikitoDir, env.Home, mcp.BuildMCPPlanOptions{
			Specs:                specsToRemove,
			Force:                force,
			DesiredAbsentServers: map[string]bool{nameClean: true},
		})
		if perr != nil {
			restore()
			fmt.Fprintf(stderr, "[ERROR] Failed to synchronize MCP server removal: %v\n", perr)
			return 1
		}
		if !plan.CanApply() {
			for _, op := range plan.Operations {
				if op.Action == "CONFLICT" && !op.IsAuthorized {
					fmt.Fprintf(stdout, "[CONFLICT] %s/%s: existing config was not last written by aikito; review it or rerun with --force\n",
						op.Target.Agent, op.Target.LogicalIdentity)
				}
			}
			restore()
			return 1
		}
		result, eerr := mcp.ExecuteMCPPlan(plan, env.Home, func(line string) { fmt.Fprintln(stdout, line) })
		if eerr != nil {
			restore()
			fmt.Fprintf(stderr, "[ERROR] Failed to synchronize MCP server removal: %v\n", eerr)
			return 1
		}
		if !result.Success {
			restore()
			return 1
		}
	}
	fmt.Fprintf(stdout, "[DELETE FILE] %s\n", displayPathRelativeToHome(mcpFile, env.Home))
	fmt.Fprintf(stdout, "\n[SUCCESS] Removed MCP server '%s'.\n", nameClean)
	return 0
}
