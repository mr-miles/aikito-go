package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mr-miles/aikito-rs/internal/mcp"
	"github.com/mr-miles/aikito-rs/internal/project"
	"github.com/mr-miles/aikito-rs/internal/registry"
	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// cmdStatus is a deliberately reduced port of status.py/render.py's
// aikito status (status.py is ~1000 lines on top of inspection.py,
// workspace/inspection.py, and several render.py Row dataclasses — none of
// which exist in this Go port yet). This implementation ports the parts
// that are both well-specified AND achievable with what's already built:
//
//   - The generic box-table renderer (table.go) — byte-faithful, since it's
//     reused by every future table command, not just status.
//   - The "Global:" / "Consumers (N):" / "All in one workspace:" summary
//     lines — ported exactly, including Python's own documented FALLBACK
//     global-summary formula (_build_fallback_global_summary in render.py,
//     used whenever status.py doesn't supply the full per-agent-issue-
//     counted global_summary) — this is not an invented simplification,
//     it's a real code path Python itself falls back to.
//   - A projects table using the generic table renderer for exact visual
//     shape, but with REDUCED per-project fidelity: no per-agent
//     instructions/skills/mcp/subagent layered status aggregation (that
//     needs inspection.py's ResourceInspectionView machinery, not ported),
//     no live MCP-tool probing, and project health is a simple
//     path-availability check rather than evaluate_project_health's full
//     priority-ordered multi-signal logic.
//
// --color/--no-color/NO_COLOR and TTY-based use_unicode/use_color
// resolution ARE ported (cli_parser.py's resolve_color_flags, exactly),
// using a zero-dependency os.ModeCharDevice check for isatty (see
// table.go's isStdoutTTY) rather than skipping TTY detection entirely.
//
// NOT ported: per-agent table (render_agents_table — needs the same
// unported inspection machinery) and the bundled-skill drift notice
// (print_bundled_skill_notice).
func cmdStatus(args []string, stdout, stderr io.Writer, env Environment) int {
	colorOpt := "auto"
	noColorFlag := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--color":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "[ERROR] --color requires a value")
				return 2
			}
			colorOpt = args[i]
			if colorOpt != "auto" && colorOpt != "always" && colorOpt != "never" {
				fmt.Fprintf(stderr, "[ERROR] --color must be one of: auto, always, never (got %q)\n", colorOpt)
				return 2
			}
		case a == "--no-color":
			noColorFlag = true
		default:
			fmt.Fprintf(stderr, "[ERROR] Unknown flag: %s\n", a)
			return 2
		}
	}
	// Mirrors cli_parser.py's resolve_color_flags exactly.
	isTTY := isStdoutTTY()
	if colorOpt != "auto" {
		isTTY = colorOpt == "always"
	}
	noColor := noColorFlag || (env.Env.Getenv("NO_COLOR") != "")
	useUnicode := isTTY
	useColor := isTTY && !noColor

	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if msg := checkWorkspaceInitialized(aikitoDir); msg != "" {
		fmt.Fprintf(stderr, "[ERROR] %s\n", msg)
		return 1
	}

	snap, err := workspace.SnapshotWorkspace(aikitoDir, env.Home)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	// --- Projects table ---
	var projectNames []string
	for _, r := range snap.Resources {
		if r.Kind == "project" {
			projectNames = append(projectNames, r.Name)
		}
	}
	sort.Strings(projectNames)

	var rows [][]string
	for _, name := range projectNames {
		rows = append(rows, buildProjectRow(aikitoDir, env.Home, name, snap, useUnicode, useColor))
	}
	projectsTable := buildGenericTable(
		[]string{"Project", "Instr", "Skills", "Memory", "Context", "Paths", "Mode", "Status"},
		rows, useUnicode, useColor, []int{0},
	)

	// --- Global summary (fallback formula, render.py _build_fallback_global_summary) ---
	// total_skills_count mirrors status.py's _get_skills_list: the length of
	// skills.toml's `skills = [...]` SELECTION list (which includes bundled
	// skill names like "aikito"/"durable-memory" by default), not a count of
	// "skill" resources from the tree scan — the scanner deliberately
	// excludes bundled skills from "skill" kind resources, which is a
	// different count than what status displays here. Verified against live
	// Python (a fresh init workspace + one added skill showed "Skills 3":
	// the 2 bundled skills plus the 1 added one).
	totalSkills, totalMCP, totalSubagents, globalMemoryNotes := 0, 0, 0, 0
	for _, r := range snap.Resources {
		switch r.Kind {
		case "skill-selection":
			totalSkills++
		case "mcp":
			totalMCP++
		case "subagent":
			totalSubagents++
		case "memory":
			if strings.HasPrefix(r.Name, "notes/") {
				globalMemoryNotes++
			}
		}
	}

	issuesCount := 0
	for _, f := range snap.Findings {
		if f.Status == "error" {
			issuesCount++
		}
	}
	if plan, perr := mcp.BuildMCPPlan(aikitoDir, env.Home, mcp.BuildMCPPlanOptions{}); perr == nil {
		for _, op := range plan.Operations {
			if op.Action == "CONFLICT" || op.Action == "ERROR" {
				issuesCount++
			}
		}
	}
	// NOTE: this issues_count only reflects scanner findings + MCP plan
	// drift/conflict/error, NOT instructions/skills/subagent status (not
	// computed in this build) — a real but narrower signal than Python's.

	globalStatus := "OK"
	if issuesCount == 1 {
		globalStatus = "! 1 issue"
	} else if issuesCount > 1 {
		globalStatus = fmt.Sprintf("! %d issues", issuesCount)
	}
	mcpCountStr := "-"
	if totalMCP > 0 {
		mcpCountStr = fmt.Sprintf("%d", totalMCP)
	}
	subagentCountStr := "-"
	if totalSubagents > 0 {
		subagentCountStr = fmt.Sprintf("%d", totalSubagents)
	}
	globalLine := fmt.Sprintf(
		"Global: %s · Instr - · Skills %d · Memory %d · MCP %s · Sub %s",
		formatScopeStatusBadge(globalStatus, useUnicode, useColor),
		totalSkills, globalMemoryNotes, mcpCountStr, subagentCountStr,
	)

	// --- Consumers line ---
	var consumers []string
	seen := map[string]bool{}
	for _, name := range registry.BuiltinAgents {
		agent, aerr := registry.BundledAgent(name, env.Home)
		if aerr != nil {
			continue
		}
		avail := registry.CheckAgentAvailabilityForAgent(agent, env.Home, nil)
		if avail.Status != "installed" {
			continue
		}
		consumer := agent.Name
		if agent.Detect != nil && len(agent.Detect.Commands) > 0 {
			consumer = agent.Detect.Commands[0]
		}
		if !seen[consumer] {
			seen[consumer] = true
			consumers = append(consumers, consumer)
		}
	}
	sort.Strings(consumers)
	consumerStr := "-"
	if len(consumers) > 0 {
		consumerStr = strings.Join(consumers, " · ")
	}
	consumersLine := fmt.Sprintf("Consumers (%d): %s", len(consumers), consumerStr)

	// --- Workspace line ---
	_, source, _ := workspace.ResolveWorkspaceWithSource(env.Home, env.Env)
	wsLine := "All in one workspace: " + displayPathRelativeToHome(aikitoDir, env.Home)
	if source != "" {
		wsLine += fmt.Sprintf(" (%s)", source)
	}

	fmt.Fprintln(stdout, projectsTable)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, globalLine)
	fmt.Fprintln(stdout, consumersLine)
	fmt.Fprintln(stdout, wsLine)
	return 0
}

func buildProjectRow(aikitoDir, home, name string, snap *workspace.WorkspaceSnapshot, useUnicode, useColor bool) []string {
	cfg, err := project.LoadConfig(aikitoDir, home, name)
	if err != nil {
		return []string{name, "-", "-", "-", "~0", "0/0", "-", formatScopeStatusBadge("!", useUnicode, useColor)}
	}

	agentsMD := filepath.Join(aikitoDir, "projects", name, "AGENTS.md")
	instrDisplay, instrText := instructionsLineCountDisplay(agentsMD)

	skillsCount := 0
	if skills, ok := cfg.Raw["skills"].([]any); ok {
		skillsCount = len(skills)
	}

	memoryCount := 0
	prefix := name + "/"
	for _, r := range snap.Resources {
		if r.Kind == "project-memory" && strings.HasPrefix(r.Name, prefix) {
			memoryCount++
		}
	}

	contextTokens := estimateTokens(instrText)
	contextDisplay := formatTokenEstimate(contextTokens)

	binding := cfg.Binding()
	views := project.CandidatePathViews(binding, home)
	activeTotal := len(views)
	activeCount := 0
	for _, v := range views {
		if v.Exists {
			activeCount++
		}
	}
	pathsDisplay := fmt.Sprintf("%d/%d", activeCount, activeTotal)

	syncMode := "link"
	if sm, ok := cfg.Raw["sync_mode"].(string); ok && sm != "" {
		syncMode = sm
	}

	status := "OK"
	if activeTotal > 0 && activeCount == 0 {
		status = "-"
	}
	statusBadge := formatScopeStatusBadge(status, useUnicode, useColor)

	return []string{name, instrDisplay, fmt.Sprintf("%d", skillsCount), fmt.Sprintf("%d", memoryCount), contextDisplay, pathsDisplay, syncMode, statusBadge}
}

// instructionsLineCountDisplay mirrors project.py's
// get_instructions_line_count_display: "<N>L" or "-" if missing/empty.
// Also returns the raw text (for the context-token estimate), "" if absent.
func instructionsLineCountDisplay(path string) (string, string) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "-", ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "-", ""
	}
	text := string(data)
	if strings.TrimSpace(text) == "" {
		return "-", ""
	}
	return fmt.Sprintf("%dL", pythonSplitLinesCount(text)), text
}

// pythonSplitLinesCount mirrors len(text.splitlines()): counts line
// terminators the same way Python's str.splitlines() does (\n, \r, \r\n,
// plus \v\f\x1c-\x1e\x85  ), without allocating the split slice
// (internal/workspace has an equivalent unexported pythonSplitLines, but
// this command must not edit that package per its review scope, so this is
// a small local duplicate of the same counting logic).
func pythonSplitLinesCount(s string) int {
	count := 0
	i := 0
	sawContent := false
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		sawContent = true
		switch r {
		case '\r':
			count++
			next := i + size
			if next < len(s) && s[next] == '\n' {
				i = next + 1
				continue
			}
			i += size
			continue
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			count++
			i += size
			continue
		}
		i += size
	}
	if sawContent && !endsInLineBoundary(s) {
		count++
	}
	return count
}

func endsInLineBoundary(s string) bool {
	if s == "" {
		return true
	}
	last, size := utf8.DecodeLastRuneInString(s)
	if last == '\n' {
		return true
	}
	if last == '\r' {
		return true
	}
	switch last {
	case '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return true
	}
	_ = size
	return false
}

// estimateTokens mirrors context_footprint.py's estimate_tokens: a
// zero-dependency ceil(utf8_bytes/4) approximation. This Go build only
// estimates from the project's own instructions text (NOT the full
// Python model, which also sums global instructions + per-skill discovery
// metadata + always-loaded skills' full content) — a narrower, smaller
// number than Python's Context column, not a wrong one.
func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len([]byte(text)) + 3) / 4
}

// formatTokenEstimate mirrors context_footprint.py's format_token_estimate.
func formatTokenEstimate(tokens int) string {
	if tokens <= 0 {
		return "~0"
	}
	if tokens < 1000 {
		return fmt.Sprintf("~%d", tokens)
	}
	return fmt.Sprintf("~%.1fk", float64(tokens)/1000.0)
}
