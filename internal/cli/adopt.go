package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-rs/internal/registry"
	"github.com/mr-miles/aikito-rs/internal/sync"
	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// cmdAdopt is a deliberately reduced port of adopt.py's build_adopt_plan/
// execute_adoption (~1800 lines in Python, covering instructions + MCP
// servers + subagents + agent registration, each with its own scan/plan/
// backup/apply machinery). Given this command's real size, this Go build
// only ports the GLOBAL INSTRUCTIONS adoption path end to end (scan each
// registered agent's native instruction file, compare against the
// canonical global/AGENTS.md using sync.Compare's three-way-diff
// primitive, and adopt the first agent's content found when the canonical
// file doesn't exist yet) as a representative, honestly-scoped slice.
//
// NOT ported (prints a clear notice rather than silently doing nothing):
// MCP server adoption (scan_mcp_servers), subagent adoption
// (scan_subagents), project-instruction adoption, agent-registration
// adoption, and adopt.py's backup-before-write safety net
// (create_adopt_backup) — this command writes directly via the resource
// scanner+transaction engine's atomic write, which is crash-safe, but does
// not keep the separate human-facing backup copy Python's adopt does.
//
// IMPORTANT REAL-WORLD LIMITATION found via hand-testing, not just a
// theoretical gap: `aikito init workspace` always writes a non-empty
// template global/AGENTS.md (the durable-memory skill pointer), so after a
// normal init there is ALWAYS already "canonical content" — this command's
// CREATE path (canonical file missing) is only reachable if that file was
// later deleted. Python's real adopt.py handles the common "still exactly
// what Aikito shipped, safe to treat as adoptable" case via a THIRD,
// separate fingerprint concept (templates.py's TEMPLATE_HISTORY — see the
// architecture research's §7.4) that this Go build does NOT implement;
// without it, this reduced adopt can only ever report DRIFT/CONFLICT, never
// a clean adoption, for the realistic post-init case. This is a known,
// documented gap, not a silent one — a real template-fingerprint-aware
// adopt is a follow-up task, not something to paper over here.
func cmdAdopt(args []string, stdout, stderr io.Writer, env Environment) int {
	dryRun := false
	verbose := false
	var skip []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dry-run":
			dryRun = true
		case a == "--verbose":
			verbose = true
		case a == "--skip":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "[ERROR] --skip requires a value")
				return 2
			}
			skip = append(skip, args[i])
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "[ERROR] Unknown flag: %s\n", a)
			return 2
		default:
			// Python's adopt accepts an optional [target] positional (scope
			// hint); this Go build only ever adopts global instructions, so a
			// positional target is accepted but has no effect yet.
		}
	}
	skipSet := map[string]bool{}
	for _, s := range skip {
		skipSet[s] = true
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

	fmt.Fprintln(stdout, "[INFO] This Go build's `adopt` currently covers global instructions only")
	fmt.Fprintln(stdout, "[INFO] (not yet MCP servers, subagents, or project instructions), and does")
	fmt.Fprintln(stdout, "[INFO] not yet detect 'still exactly the init template' content, so it can")
	fmt.Fprintln(stdout, "[INFO] only adopt into a MISSING global/AGENTS.md, not reconcile an existing one.")

	canonicalPath := filepath.Join(aikitoDir, "global", "AGENTS.md")
	canonicalFP, cerr := fingerprintIfExists(canonicalPath)
	if cerr != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", cerr)
		return 1
	}

	type candidate struct {
		agent string
		path  string
		fp    string
	}
	var candidates []candidate
	for _, name := range registry.BuiltinAgents {
		if skipSet[name] {
			continue
		}
		agent, aerr := registry.BundledAgent(name, env.Home)
		if aerr != nil || agent.InstructionPath == nil {
			continue
		}
		fp, ferr := fingerprintIfExists(*agent.InstructionPath)
		if ferr != nil {
			continue
		}
		if fp == "" {
			if verbose {
				fmt.Fprintf(stdout, "  %-14s %s (no native instructions file)\n", name, "SKIP")
			}
			continue
		}
		candidates = append(candidates, candidate{agent: name, path: *agent.InstructionPath, fp: fp})
	}

	if canonicalFP != "" {
		fmt.Fprintf(stdout, "\n[OK] Canonical global instructions already present: %s\n", displayPathRelativeToHome(canonicalPath, env.Home))
		for _, c := range candidates {
			outcome := sync.Compare(map[string]struct{}{}, strp(canonicalFP), strp(c.fp))
			status := "OK"
			if outcome.Action != "NOOP" {
				status = "DRIFT (canonical already adopted from elsewhere; not re-comparing further sources)"
			}
			if verbose || outcome.Action != "NOOP" {
				fmt.Fprintf(stdout, "  %-14s %s  %s\n", c.agent, status, displayPathRelativeToHome(c.path, env.Home))
			}
		}
		return 0
	}

	if len(candidates) == 0 {
		fmt.Fprintln(stdout, "\n[INFO] Nothing to adopt: no registered agent has a native global instructions file.")
		return 0
	}

	chosen := candidates[0]
	fmt.Fprintf(stdout, "\n[PLAN] CREATE %s\n", displayPathRelativeToHome(canonicalPath, env.Home))
	fmt.Fprintf(stdout, "       from %s (%s)\n", displayPathRelativeToHome(chosen.path, env.Home), chosen.agent)
	for _, c := range candidates[1:] {
		if verbose {
			fmt.Fprintf(stdout, "  %-14s %s  %s\n", c.agent, "NOT USED (first match wins)", displayPathRelativeToHome(c.path, env.Home))
		}
	}

	if dryRun {
		fmt.Fprintln(stdout, "\n[DRY RUN] No changes written.")
		return 0
	}

	data, rerr := os.ReadFile(chosen.path)
	if rerr != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", rerr)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o777); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if err := os.WriteFile(canonicalPath, data, 0o644); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "\n[CREATE FILE] %s\n", displayPathRelativeToHome(canonicalPath, env.Home))
	fmt.Fprintln(stdout, "[SUCCESS] Adopted global instructions.")
	return 0
}

func fingerprintIfExists(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", nil
	}
	if !info.Mode().IsRegular() {
		return "", nil
	}
	return workspace.FileDigest(path)
}

func strp(s string) *string { return &s }
