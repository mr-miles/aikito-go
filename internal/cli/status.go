package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/mr-miles/aikito-go/internal/workspace"
)

// colorFlags parses the --color/--no-color options shared by the report
// commands. Anything else is collected into extra, for argparse's error.
func colorFlags(args []string, extra *[]string) (colorOpt string, noColor bool, valid bool) {
	colorOpt = "auto"
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--color" && i+1 < len(args):
			i++
			colorOpt = args[i]
		case strings.HasPrefix(a, "--color="):
			colorOpt = strings.TrimPrefix(a, "--color=")
		case a == "--no-color":
			noColor = true
		default:
			*extra = append(*extra, a)
		}
	}
	return colorOpt, noColor, colorOpt == "auto" || colorOpt == "always" || colorOpt == "never"
}

// resolveColorFlags ports cli_parser.py resolve_color_flags.
func resolveColorFlags(colorOpt string, noColorFlag bool, env Environment) (useUnicode, useColor bool) {
	isTTY := isStdoutTTY()
	if colorOpt != "auto" {
		isTTY = colorOpt == "always"
	}
	noColor := noColorFlag || env.Env.Getenv("NO_COLOR") != ""
	return isTTY, isTTY && !noColor
}

// resolveReportWorkspace is the workspace lookup plus cli.py main()'s layout
// gate, shared by the read-only report commands.
func resolveReportWorkspace(env Environment, stderr io.Writer) (string, string, bool) {
	aikitoDir, source, err := workspace.ResolveWorkspaceWithSource(env.Home, env.Env)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return "", "", false
	}
	if err := requireLayoutLikePython(aikitoDir); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return "", "", false
	}
	return aikitoDir, source, true
}

// cmdStatus ports cli.py cmd_status, status.py get_status_report_data and
// render.py render_status_report.
func cmdStatus(args []string, stdout, stderr io.Writer, env Environment) int {
	var extra []string
	colorOpt, noColorFlag, valid := colorFlags(args, &extra)
	if !valid {
		fmt.Fprintf(stderr, "[ERROR] --color must be one of: auto, always, never (got %q)\n", colorOpt)
		return 2
	}
	if len(extra) > 0 {
		return argparseUnrecognized(stderr, extra)
	}
	useUnicode, useColor := resolveColorFlags(colorOpt, noColorFlag, env)
	aikitoDir, source, ok := resolveReportWorkspace(env, stderr)
	if !ok {
		return 1
	}
	warn := func(msg string) { fmt.Fprintln(stderr, msg) }

	ctx := newInspectionContext(aikitoDir, env.Home)
	agentRows, agentIssues, totalSubagents, totalMCP, err := collectAgentStatusRows(ctx, warn)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	memRows, _, _ := collectMemoryStatusRows(ctx, warn)
	skills := globalSkillsList(aikitoDir, warn)

	globalInstr, _ := instructionsLineCountDisplay(filepath.Join(aikitoDir, "AGENTS.md"))
	globalNotes, globalMemStatus := 0, "OK"
	if len(memRows) > 0 && strings.ToLower(memRows[0].Scope) == "global" {
		globalNotes, globalMemStatus = memRows[0].NotesCount, memRows[0].Status
	}
	globalIssues := agentIssues
	switch globalMemStatus {
	case "OK", "EMPTY", "N/A", "SKIP":
	default:
		globalIssues++
	}
	globalStatus := "OK"
	if globalIssues == 1 {
		globalStatus = "! 1 issue"
	} else if globalIssues > 1 {
		globalStatus = fmt.Sprintf("! %d issues", globalIssues)
	}
	mcpStr, subStr := "-", "-"
	if totalMCP > 0 {
		mcpStr = fmt.Sprint(totalMCP)
	}
	if totalSubagents > 0 {
		subStr = fmt.Sprint(totalSubagents)
	}

	projects := collectProjectSummaries(aikitoDir, env.Home)
	consumers := statusConsumers(agentRows)
	consumerStr := "-"
	if len(consumers) > 0 {
		consumerStr = strings.Join(consumers, " · ")
	}

	fmt.Fprintln(stdout, renderProjectsTable(projects, useUnicode, useColor, memRows))
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "Global: %s · Instr %s · Skills %d · Memory %d · MCP %s · Sub %s\n",
		formatScopeStatusBadge(globalStatus, useUnicode, useColor), globalInstr, len(skills), globalNotes, mcpStr, subStr)
	fmt.Fprintf(stdout, "Consumers (%d): %s\n", len(consumers), consumerStr)
	wsLine := "All in one workspace: " + displayPathRelativeToHome(aikitoDir, env.Home)
	if useColor {
		wsLine = colorize(wsLine, colorBold, true)
	}
	if source != "" {
		wsLine += fmt.Sprintf(" (%s)", source)
	}
	fmt.Fprintln(stdout, wsLine)
	printBundledSkillNotice(aikitoDir, stderr)
	return 0
}

// instructionsLineCountDisplay mirrors project.py's
// get_instructions_line_count_display: "<N>L" or "-" if missing/empty.
// Also returns the raw text, "" if absent.
func instructionsLineCountDisplay(path string) (string, string) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "-", ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "-", ""
	}
	text := strings.ToValidUTF8(string(data), "�")
	if strings.TrimSpace(text) == "" {
		return "-", ""
	}
	return fmt.Sprintf("%dL", pythonSplitLinesCount(text)), text
}

// pythonSplitLinesCount mirrors len(text.splitlines()).
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
	last, _ := utf8.DecodeLastRuneInString(s)
	switch last {
	case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
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
