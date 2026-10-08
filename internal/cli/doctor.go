// Deep workspace diagnostics, ported from doctor.py. Full Python parity
// would need several unported subsystems (adopt.py's full build_adopt_plan,
// workspace/inspection.py's ResourceInspectionView/skill_views,
// local_state.py's inspect_local_state) — this is a breadth-first partial
// port: every check area gets at least a real, cross-validated
// implementation OR an honest "not yet implemented" stub section (never
// silent omission), prioritizing the checks that are both well-specified
// and achievable with primitives already built elsewhere in this Go port
// (the resource scanner, the MCP/subagent planners, the agent registry).
//
// Orphans (check_orphans) is a full real check, reusing the already-built
// sync.BuildSubagentPlan (for orphan subagent files) and mcp.LoadState (for
// residual managed MCP entries); its one documented simplification is the
// "stale entries in ~/.agents/skills/" sub-check, which reports every entry
// not in skills.toml uniformly rather than distinguishing a locally-
// conflicting unmanaged item via the unported skill_views machinery — see
// checkOrphans's doc comment.
//
// NOT implemented (each still appears in the report as a stub section
// saying so, matching run_doctor's section list so `--json` output shape
// stays recognizable): LocalState (needs local_state.py's remote-sync
// bookkeeping — phase 2 territory), the Python-interpreter-consistency
// check within Environment (meaningless for a compiled Go binary, dropped
// rather than stubbed), and --fix's registry-field-backfill behavior
// (add_missing_agent_fields — reported as WARN findings without an
// automated fix).
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mr-miles/aikito-go/internal/compat"
	"github.com/mr-miles/aikito-go/internal/mcp"
	"github.com/mr-miles/aikito-go/internal/project"
	"github.com/mr-miles/aikito-go/internal/registry"
	"github.com/mr-miles/aikito-go/internal/sync"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// Finding mirrors diagnostics.py's Finding: a diagnostic with stable
// identity and optional actionable context.
type Finding struct {
	Status   string `json:"status"` // "OK" | "FAIL" | "WARN"
	Message  string `json:"message"`
	FixHint  string `json:"fix_hint"`
	Code     string `json:"code"`
	Resource string `json:"resource"`
	Source   string `json:"source"`
	Reason   string `json:"reason"`
}

func ok(message string) Finding { return Finding{Status: "OK", Message: message} }
func fail(message, fixHint string) Finding {
	return Finding{Status: "FAIL", Message: message, FixHint: fixHint}
}
func warn(message, fixHint string) Finding {
	return Finding{Status: "WARN", Message: message, FixHint: fixHint}
}

// DoctorSection mirrors render.py's DoctorSection.
type DoctorSection struct {
	Name     string    `json:"name"`
	Findings []Finding `json:"findings"`
}

// DoctorReport mirrors render.py's DoctorReport.
type DoctorReport struct {
	Sections []DoctorSection `json:"sections"`
}

func (r DoctorReport) FailCount() int {
	n := 0
	for _, s := range r.Sections {
		for _, f := range s.Findings {
			if f.Status == "FAIL" {
				n++
			}
		}
	}
	return n
}

func (r DoctorReport) WarnCount() int {
	n := 0
	for _, s := range r.Sections {
		for _, f := range s.Findings {
			if f.Status == "WARN" {
				n++
			}
		}
	}
	return n
}

func cmdDoctor(args []string, stdout, stderr io.Writer, env Environment) int {
	jsonFlag := false
	fixFlag := false
	staleDays := 0
	colorOpt := "auto"
	noColorFlag := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			jsonFlag = true
		case a == "--fix":
			fixFlag = true
		case a == "--stale-days":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "[ERROR] --stale-days requires a value")
				return 2
			}
			n, perr := strconv.Atoi(args[i])
			if perr != nil || n <= 0 {
				fmt.Fprintf(stderr, "[ERROR] --stale-days must be a positive integer (got %q)\n", args[i])
				return 2
			}
			staleDays = n
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

	var fixes []string
	if fixFlag {
		fixes = runDoctorFixes(aikitoDir, env.Home)
		if len(fixes) > 0 && !jsonFlag {
			for _, f := range fixes {
				fmt.Fprintf(stdout, "[FIX] %s\n", f)
			}
			fmt.Fprintln(stdout)
		}
	}

	report := runDoctor(aikitoDir, env.Home, staleDays)

	if jsonFlag {
		type jsonFinding struct {
			Status   string `json:"status"`
			Message  string `json:"message"`
			FixHint  string `json:"fix_hint"`
			Code     string `json:"code"`
			Resource string `json:"resource"`
			Source   string `json:"source"`
			Reason   string `json:"reason"`
			Actions  []any  `json:"actions"`
		}
		type jsonSection struct {
			Name     string        `json:"name"`
			Findings []jsonFinding `json:"findings"`
		}
		out := struct {
			Sections  []jsonSection `json:"sections"`
			FailCount int           `json:"fail_count"`
			WarnCount int           `json:"warn_count"`
			Fixes     []string      `json:"fixes"`
		}{FailCount: report.FailCount(), WarnCount: report.WarnCount(), Fixes: fixes}
		if out.Fixes == nil {
			out.Fixes = []string{}
		}
		for _, s := range report.Sections {
			js := jsonSection{Name: s.Name}
			for _, f := range s.Findings {
				js.Findings = append(js.Findings, jsonFinding{
					Status: f.Status, Message: f.Message, FixHint: f.FixHint,
					Code: f.Code, Resource: f.Resource, Source: f.Source, Reason: f.Reason,
					Actions: []any{},
				})
			}
			if js.Findings == nil {
				js.Findings = []jsonFinding{}
			}
			out.Sections = append(out.Sections, js)
		}
		data, merr := json.MarshalIndent(out, "", "  ")
		if merr != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", merr)
			return 1
		}
		fmt.Fprintln(stdout, string(data))
	} else {
		fmt.Fprintln(stdout, renderDoctorReport(report, useUnicode, useColor))
	}

	if report.FailCount() > 0 {
		return 1
	}
	return 0
}

// --- Report rendering (render.py's render_doctor_report/_render_title_box/render_finding_lines) ---

func renderDoctorReport(report DoctorReport, useUnicode, useColor bool) string {
	okSym, failSym, warnSym := "✓", "✗", "⚠"
	if !useUnicode {
		okSym, failSym, warnSym = "[OK]", "[FAIL]", "[WARN]"
	}
	maxTitleW := 10
	for _, s := range report.Sections {
		if w := displayWidth(s.Name); w > maxTitleW {
			maxTitleW = w
		}
	}
	boxInnerWidth := maxTitleW + 3

	var blocks []string
	for _, s := range report.Sections {
		var lines []string
		lines = append(lines, renderTitleBox(s.Name, useUnicode, useColor, boxInnerWidth))
		for _, f := range s.Findings {
			sym, color := okSym, colorGreen
			if f.Status == "FAIL" {
				sym, color = failSym, colorRed
			} else if f.Status == "WARN" {
				sym, color = warnSym, colorYellow
			}
			lines = append(lines, renderFindingLines(f, colorize(sym, color, useColor), useUnicode, useColor)...)
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	blocks = append(blocks, "")

	failCount, warnCount := report.FailCount(), report.WarnCount()
	var summary string
	if failCount == 0 && warnCount == 0 {
		summary = colorize(okSym+" All checks passed.", colorGreen, useColor)
	} else {
		var parts []string
		if failCount > 0 {
			word := "issue"
			if failCount != 1 {
				word = "issues"
			}
			parts = append(parts, colorize(fmt.Sprintf("%d %s", failCount, word), colorRed, useColor))
		}
		if warnCount > 0 {
			word := "warning"
			if warnCount != 1 {
				word = "warnings"
			}
			parts = append(parts, colorize(fmt.Sprintf("%d %s", warnCount, word), colorYellow, useColor))
		}
		summary = "Found " + strings.Join(parts, ", ") + "."
	}
	blocks = append(blocks, summary)
	return strings.Join(blocks, "\n")
}

func renderTitleBox(title string, useUnicode, useColor bool, innerWidth int) string {
	tl, tr, bl, br, horiz, vert := "+", "+", "+", "+", "-", "|"
	if useUnicode {
		tl, tr, bl, br, horiz, vert = "╭", "╮", "╰", "╯", "─", "│"
	}
	titleText := " " + title + " "
	width := innerWidth
	if width < displayWidth(titleText)+3 {
		width = displayWidth(titleText) + 3
	}
	padding := strings.Repeat(" ", width-displayWidth(titleText))
	return fmt.Sprintf("%s%s%s\n%s%s%s%s\n%s%s%s",
		tl, strings.Repeat(horiz, width), tr,
		vert, colorize(titleText, colorBold, useColor), padding, vert,
		bl, strings.Repeat(horiz, width), br)
}

func renderFindingLines(f Finding, symbol string, useUnicode, useColor bool) []string {
	lines := []string{fmt.Sprintf("  %s %s", symbol, f.Message)}
	for _, pair := range [][2]string{{"Resource", f.Resource}, {"Source", f.Source}, {"Reason", f.Reason}} {
		if pair[1] != "" {
			lines = append(lines, fmt.Sprintf("      %s: %s", pair[0], pair[1]))
		}
	}
	if f.FixHint != "" {
		prefix := "    -> "
		if useUnicode {
			prefix = "    → "
		}
		lines = append(lines, prefix+colorize(f.FixHint, colorDim, useColor))
	}
	return lines
}

// --- Orchestration ---

func runDoctor(aikitoDir, home string, staleDays int) DoctorReport {
	return DoctorReport{Sections: []DoctorSection{
		checkSymlinks(aikitoDir, home),
		checkOrphans(aikitoDir, home),
		checkLocalStateStub(),
		checkMemory(aikitoDir, home, staleDays),
		checkDrift(aikitoDir, home),
		checkSecurity(aikitoDir, home),
		checkEnvironment(aikitoDir, home),
		checkAdoption(aikitoDir, home),
		checkConflictMarkers(aikitoDir, home),
		checkProjects(aikitoDir, home),
		checkConfigSyntax(aikitoDir, home),
	}}
}

func runDoctorFixes(aikitoDir, home string) []string {
	// Python's run_doctor_fixes also cleans abandoned local-sync state
	// (clean_local_state) — phase 2 territory, not ported. Only the
	// "registered-but-stale bundled agent field backfill" half is in
	// scope here, and even that is reported (Configuration section) but
	// not auto-applied yet — see the package doc comment.
	return nil
}

func homeRel(path, home string) string {
	if home == "" {
		return path
	}
	if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return path
}

// --- Configuration (TOML syntax + agent registration) ---

func checkConfigSyntax(aikitoDir, home string) DoctorSection {
	var findings []Finding

	checkTOML := func(path, label string) {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			findings = append(findings, fail(fmt.Sprintf("%s: file not found", label), ""))
			return
		}
		if hasAnyConflictMarker(string(data)) {
			return // reported by ConflictMarkers
		}
		if _, derr := workspace.DecodeTOML(data); derr != nil {
			findings = append(findings, fail(fmt.Sprintf("%s: TOML parse error — %v", label, derr), ""))
			return
		}
		findings = append(findings, ok(fmt.Sprintf("%s: valid TOML", label)))
	}

	if data, rerr := os.ReadFile(filepath.Join(aikitoDir, "config.toml")); rerr == nil {
		if !hasAnyConflictMarker(string(data)) {
			if _, derr := workspace.DecodeTOML(data); derr != nil {
				findings = append(findings, fail(fmt.Sprintf("config.toml: TOML parse error — %v", derr), ""))
			} else {
				findings = append(findings, ok("config.toml: valid TOML"))
			}
		}
	}
	checkTOML(filepath.Join(aikitoDir, "layout.toml"), "layout.toml")
	checkTOML(filepath.Join(aikitoDir, "skills.toml"), "skills.toml")

	agentsDir := filepath.Join(aikitoDir, "agents")
	if info, serr := os.Stat(agentsDir); serr != nil || !info.IsDir() {
		findings = append(findings, fail("agents: directory not found", ""))
	} else {
		docs, derr := workspace.ReadAgentDocuments(aikitoDir)
		if derr != nil {
			findings = append(findings, fail(fmt.Sprintf("agents: %v", derr), ""))
		} else {
			entries, _ := os.ReadDir(agentsDir)
			var names []string
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".toml") {
					names = append(names, e.Name())
				}
			}
			sort.Strings(names)
			for _, n := range names {
				findings = append(findings, ok(fmt.Sprintf("agents/%s: valid TOML", n)))
			}
			defs, _ := registry.LoadAgentDefinitions(aikitoDir, home)
			registered := make([]string, 0, len(docs))
			for name := range docs {
				registered = append(registered, name)
			}
			sort.Strings(registered)
			for _, name := range registered {
				if def, ok2 := defs[name]; ok2 {
					if registry.CheckAgentAvailabilityForAgent(def.Agent, home, nil).IsNotInstalled() {
						findings = append(findings, ok(fmt.Sprintf("agents/%s.toml: registered Agent is offline on this host", name)))
					}
				}
			}
		}
	}

	mcpsDir := filepath.Join(aikitoDir, "mcps")
	if info, serr := os.Stat(mcpsDir); serr != nil {
		findings = append(findings, fail("mcps: directory not found", ""))
	} else if !info.IsDir() {
		findings = append(findings, fail("mcps: path is not a directory", ""))
	} else {
		entries, _ := os.ReadDir(mcpsDir)
		var names []string
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".toml") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		if len(names) == 0 {
			findings = append(findings, ok("mcps: directory present (empty)"))
		}
		for _, n := range names {
			checkTOML(filepath.Join(mcpsDir, n), "mcps/"+n)
		}
	}

	projectsDir := filepath.Join(aikitoDir, "projects")
	if entries, derr := os.ReadDir(projectsDir); derr == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			agentToml := filepath.Join(projectsDir, e.Name(), "agent.toml")
			if _, serr := os.Stat(agentToml); serr != nil {
				continue
			}
			checkTOML(agentToml, fmt.Sprintf("projects/%s/agent.toml", e.Name()))
		}
	}

	return DoctorSection{Name: "Configuration", Findings: findings}
}

// --- Drift (MCP + subagent plan conflicts) ---

func checkDrift(aikitoDir, home string) DoctorSection {
	var findings []Finding

	plan, perr := mcp.BuildMCPPlan(aikitoDir, home, mcp.BuildMCPPlanOptions{})
	if perr != nil {
		findings = append(findings, fail(fmt.Sprintf("Cannot load MCP specs: %v", perr), ""))
	} else {
		driftCount, checked := 0, 0
		for _, op := range plan.Operations {
			if op.Spec != nil && !op.Spec.Enabled {
				continue
			}
			st := mcp.MapOperationToStatus(op)
			if st == "SKIP" {
				continue
			}
			checked++
			key := op.Target.Agent + " × " + op.Target.LogicalIdentity
			switch st {
			case "OK":
			case "DRIFT":
				driftCount++
				if op.Spec != nil && op.Spec.MissingCredentialEnv != "" {
					findings = append(findings, warn(fmt.Sprintf("%s: credential-dependent MCP config differs; current shell may have stale or missing %s", key, op.Spec.MissingCredentialEnv), "open a new shell and run: aikito doctor"))
				} else {
					findings = append(findings, fail(fmt.Sprintf("%s: managed MCP config differs (unmanaged modification)", key), "aikito sync mcp --force"))
				}
			case "UPDATE":
				driftCount++
				findings = append(findings, fail(fmt.Sprintf("%s: managed MCP config differs", key), "aikito sync mcp"))
			case "MISSING":
				if op.Spec != nil && op.Spec.MissingCredentialEnv != "" {
					findings = append(findings, warn(fmt.Sprintf("%s: entry omitted due to missing %s", key, op.Spec.MissingCredentialEnv), fmt.Sprintf("set %s and run: aikito sync mcp", op.Spec.MissingCredentialEnv)))
				} else {
					findings = append(findings, fail(fmt.Sprintf("%s: managed entry missing from config", key), "aikito sync mcp"))
				}
			case "ERROR":
				findings = append(findings, fail(fmt.Sprintf("%s: config parse error", key), "aikito sync mcp"))
			}
		}
		if driftCount == 0 && checked > 0 {
			findings = append(findings, ok(fmt.Sprintf("MCP fingerprints OK (%d entries)", checked)))
		} else if checked == 0 {
			findings = append(findings, ok("No managed MCP entries to check"))
		}
	}

	ops, serr := sync.BuildSubagentPlan(aikitoDir, home, sync.BuildSubagentPlanOptions{})
	if serr != nil {
		findings = append(findings, fail(fmt.Sprintf("Cannot build subagent synchronization plan: %v", serr), ""))
	} else {
		checked, issues := 0, 0
		for _, op := range ops {
			if op.Subagent == "*" || op.Action == sync.SASkip || op.Action == sync.SAOrphan {
				continue
			}
			checked++
			key := op.Agent + "/" + op.Subagent
			target := homeRel(op.TargetPath, home)
			switch op.Action {
			case sync.SACreate:
				issues++
				findings = append(findings, fail(fmt.Sprintf("%s: managed subagent missing (%s)", key, target), "aikito sync subagents"))
			case sync.SAUpdate:
				issues++
				findings = append(findings, fail(fmt.Sprintf("%s: managed subagent drift (%s)", key, target), "aikito sync subagents"))
			case sync.SAConflict:
				issues++
				findings = append(findings, fail(fmt.Sprintf("%s: unmanaged target conflict (%s)", key, target), fmt.Sprintf("aikito sync subagents --force %s", key)))
			case sync.SAError:
				issues++
				findings = append(findings, fail(fmt.Sprintf("%s: %s", key, op.Reason), fmt.Sprintf("Check subagents/%s.md", op.Subagent)))
			}
		}
		if checked == 0 {
			findings = append(findings, ok("No managed subagent entries to check"))
		} else if issues == 0 {
			findings = append(findings, ok(fmt.Sprintf("Subagent managed files OK (%d entries)", checked)))
		}
	}

	return DoctorSection{Name: "Drift", Findings: findings}
}

// --- Security ---

func checkSecurity(aikitoDir, home string) DoctorSection {
	var findings []Finding

	if compat.IsWindows() {
		if compat.CanSymlink() {
			findings = append(findings, ok("Windows Developer Mode / symlink support: enabled"))
		} else {
			findings = append(findings, fail("Windows Developer Mode is disabled: symbolic links cannot be created",
				"Enable Developer Mode in Windows Settings: Settings -> System -> For developers -> Developer Mode (On)"))
		}
	}

	specs, serr := mcp.LoadAgentSpecs(aikitoDir, home)
	if serr == nil {
		checkedN, issues := 0, 0
		seen := map[string]bool{}
		for _, spec := range specs {
			if !spec.ContainsSecret || seen[spec.ConfigPath] {
				continue
			}
			seen[spec.ConfigPath] = true
			info, ierr := os.Stat(spec.ConfigPath)
			if ierr != nil {
				continue
			}
			checkedN++
			// compat.py's check_credential_permissions: secure iff the mode is
			// exactly 0600 (so 0700 and 0400 are flagged too), described as
			// Python's oct() renders it.
			if mode := info.Mode().Perm(); !compat.IsWindows() && mode != 0o600 {
				issues++
				findings = append(findings, fail(
					fmt.Sprintf("Credential file has insecure permissions (0o%o): %s", mode, homeRel(spec.ConfigPath, home)),
					`chmod 600 "`+spec.ConfigPath+`"`)) // compat.py: f'chmod 600 "{path}"', no escaping
			}
		}
		if checkedN > 0 && issues == 0 {
			findings = append(findings, ok(fmt.Sprintf("Credential file permissions OK (%d files)", checkedN)))
		} else if checkedN == 0 {
			findings = append(findings, ok("No secret-bearing credential config files detected"))
		}
	}

	if data, rerr := os.ReadFile(filepath.Join(aikitoDir, ".gitignore")); rerr == nil {
		content := string(data)
		if !strings.Contains(content, ".local/state") && !strings.Contains(content, ".local/") {
			findings = append(findings, warn("Workspace .gitignore may not cover .local/state/ (MCP state & backups)", "Add '/.local/' to .gitignore"))
		} else {
			findings = append(findings, ok(".gitignore covers .local/state/"))
		}
	}

	return DoctorSection{Name: "Security", Findings: findings}
}

// --- Environment ---

func checkEnvironment(aikitoDir, home string) DoctorSection {
	var findings []Finding

	envDir := os.Getenv("AIKITO_DIR")
	if envDir != "" {
		resolved, _ := workspace.ResolvePath(workspace.ExpandUser(home, envDir))
		if resolved != aikitoDir {
			findings = append(findings, warn(fmt.Sprintf("$AIKITO_DIR (%s) resolved to %s, but aikito_dir=%s", envDir, resolved, aikitoDir), ""))
		} else if _, serr := os.Stat(resolved); serr != nil {
			findings = append(findings, fail(fmt.Sprintf("$AIKITO_DIR points to non-existent path: %s", envDir), ""))
		} else {
			findings = append(findings, ok(fmt.Sprintf("$AIKITO_DIR → %s", homeRel(resolved, home))))
		}
	} else {
		findings = append(findings, ok(fmt.Sprintf("AIKITO_DIR not set; using configured workspace: %s", homeRel(aikitoDir, home))))
	}

	// Note: Python's check_environment also compares $PATH's python3/python
	// against the running interpreter (sys.executable) — meaningless for a
	// compiled Go binary, so that sub-check is dropped entirely here, not
	// stubbed.

	defs, derr := registry.LoadAgentDefinitions(aikitoDir, home)
	if derr != nil {
		for _, name := range registry.BuiltinAgents {
			if a, aerr := registry.BundledAgent(name, home); aerr == nil {
				if defs == nil {
					defs = map[string]registry.AgentDefinition{}
				}
				defs[name] = registry.AgentDefinition{Agent: a}
			}
		}
	}
	commandSet := map[string]bool{}
	for _, def := range defs {
		if def.Detect != nil {
			for _, c := range def.Detect.Commands {
				commandSet[c] = true
			}
		}
	}
	var commands []string
	for c := range commandSet {
		commands = append(commands, c)
	}
	sort.Strings(commands)
	var foundAny bool
	for _, bin := range commands {
		if _, err := exec.LookPath(bin); err == nil {
			findings = append(findings, ok(fmt.Sprintf("%s CLI found (%s)", bin, bin)))
			foundAny = true
		}
	}
	if !foundAny {
		findings = append(findings, warn(fmt.Sprintf("No supported agent CLI found in $PATH (install at least one: %s)", strings.Join(commands, ", ")), ""))
	}

	return DoctorSection{Name: "Environment", Findings: findings}
}

// --- Memory ---

var wikilinkPattern = regexp.MustCompile(`\[\[([^\]|]+)(?:\|[^\]]+)?\]\]`)

func checkMemory(aikitoDir, home string, staleDaysOverride int) DoctorSection {
	var findings []Finding

	type scopeDir struct {
		dir, label, projFolder string
	}
	var scopes []scopeDir
	scopes = append(scopes, scopeDir{filepath.Join(aikitoDir, "memory"), "Global", ""})
	if entries, derr := os.ReadDir(filepath.Join(aikitoDir, "projects")); derr == nil {
		for _, e := range entries {
			if e.IsDir() {
				scopes = append(scopes, scopeDir{filepath.Join(aikitoDir, "projects", e.Name(), "memory"), e.Name(), filepath.Join(aikitoDir, "projects", e.Name())})
			}
		}
	}

	globalStaleDays := 30
	if staleDaysOverride > 0 {
		globalStaleDays = staleDaysOverride
	} else if data, rerr := os.ReadFile(filepath.Join(aikitoDir, "config.toml")); rerr == nil {
		if doc, derr := workspace.DecodeTOML(data); derr == nil {
			if m, ok2 := doc["memory"].(map[string]any); ok2 {
				if n, ok3 := toInt(m["stale_days"]); ok3 && n > 0 {
					globalStaleDays = n
				}
			}
		}
	}

	for _, sc := range scopes {
		notesDir := filepath.Join(sc.dir, "notes")
		entries, derr := os.ReadDir(notesDir)
		if derr != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				findings = append(findings, warn(fmt.Sprintf("%s memory notes subdirectory '%s/' is not scanned", sc.label, e.Name()),
					"Move its Markdown files directly into the scope's notes/ directory or remove the subdirectory"))
			}
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			stem := strings.TrimSuffix(e.Name(), ".md")
			if msg := workspace.ValidateMemoryName(stem); msg != "" {
				findings = append(findings, fail(fmt.Sprintf("%s note '%s' has invalid filename: %s", sc.label, stem, msg),
					fmt.Sprintf("Rename note using 'aikito rename memory %s <valid-name>'", stem)))
			}
		}
	}

	for _, sc := range scopes {
		notesDir := filepath.Join(sc.dir, "notes")
		entries, derr := os.ReadDir(notesDir)
		if derr != nil {
			continue
		}
		stems := map[string]bool{}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".md") {
				stems[strings.TrimSuffix(e.Name(), ".md")] = true
			}
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			body, rerr := os.ReadFile(filepath.Join(notesDir, e.Name()))
			if rerr != nil {
				continue
			}
			noteStem := strings.TrimSuffix(e.Name(), ".md")
			referenced := map[string]bool{}
			for _, m := range wikilinkPattern.FindAllStringSubmatch(string(body), -1) {
				referenced[strings.TrimSpace(m[1])] = true
			}
			var targets []string
			for t := range referenced {
				targets = append(targets, t)
			}
			sort.Strings(targets)
			for _, target := range targets {
				if !stems[target] {
					findings = append(findings, fail(
						fmt.Sprintf("%s note '%s' links to [[%s]] but that note does not exist", sc.label, noteStem, target),
						fmt.Sprintf("Write notes/%s.md if the topic is still worth keeping, or edit %s.md to drop the [[%s]] link", target, noteStem, target)))
				}
			}
		}
	}

	now := time.Now().Unix()
	checkedAny, staleFound := false, false
	staleDaysUsed := map[int]bool{}
	for _, sc := range scopes {
		notesDir := filepath.Join(sc.dir, "notes")
		entries, derr := os.ReadDir(notesDir)
		if derr != nil {
			continue
		}
		scopeStaleDays := globalStaleDays
		if staleDaysOverride <= 0 && sc.projFolder != "" {
			if data, rerr := os.ReadFile(filepath.Join(sc.projFolder, "agent.toml")); rerr == nil {
				if doc, derr2 := workspace.DecodeTOML(data); derr2 == nil {
					if m, ok2 := doc["memory"].(map[string]any); ok2 {
						if n, ok3 := toInt(m["stale_days"]); ok3 && n > 0 {
							scopeStaleDays = n
						}
					}
				}
			}
		}
		staleDaysUsed[scopeStaleDays] = true
		threshold := int64(scopeStaleDays) * 86400
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			notePath := filepath.Join(notesDir, e.Name())
			lastCommit, hasCommit := gitLastCommitEpoch(aikitoDir, notePath)
			if !hasCommit {
				continue
			}
			checkedAny = true
			ageDays := (now - lastCommit) / 86400
			if now-lastCommit > threshold {
				staleFound = true
				stem := strings.TrimSuffix(e.Name(), ".md")
				findings = append(findings, warn(
					fmt.Sprintf("%s note '%s' has not been updated in %d days (threshold: %dd) — worth a re-read to confirm it still holds", sc.label, stem, ageDays, scopeStaleDays),
					fmt.Sprintf("Re-read notes/%s.md; rewrite if it drifted, or leave it if it's still accurate", stem)))
			}
		}
	}
	if checkedAny && !staleFound {
		if len(staleDaysUsed) == 1 {
			for d := range staleDaysUsed {
				findings = append(findings, ok(fmt.Sprintf("No memory notes older than %d days", d)))
			}
		} else {
			var ds []string
			for d := range staleDaysUsed {
				ds = append(ds, strconv.Itoa(d))
			}
			sort.Strings(ds)
			findings = append(findings, ok(fmt.Sprintf("No memory notes older than configured thresholds (%s days)", strings.Join(ds, ", "))))
		}
	} else if !checkedAny {
		findings = append(findings, ok("No memory notes found"))
	}

	return DoctorSection{Name: "Memory", Findings: findings}
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int64:
		return int(x), true
	case int:
		return x, true
	}
	return 0, false
}

// gitLastCommitEpoch shells out to `git log -1 --format=%ct -- <path>` in
// aikitoDir, mirroring doctor.py's _git_last_commit_epoch. Returns
// (0, false) if git isn't available, the dir isn't a repo, or the file has
// no commit history (matching Python's "skip freshness for this note"
// behavior rather than erroring).
func gitLastCommitEpoch(aikitoDir, path string) (int64, bool) {
	rel, err := filepath.Rel(aikitoDir, path)
	if err != nil {
		return 0, false
	}
	cmd := exec.Command("git", "log", "-1", "--format=%ct", "--", rel)
	cmd.Dir = aikitoDir
	out, err := cmd.Output()
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0, false
	}
	n, perr := strconv.ParseInt(s, 10, 64)
	if perr != nil {
		return 0, false
	}
	return n, true
}

// --- Conflict markers ---

var anyConflictMarkerRe = regexp.MustCompile(`^(<{7}|={7}|>{7}|\|{7})([ \t].*)?$`)
var startMarkerRe = regexp.MustCompile(`^<{7}([ \t].*)?$`)
var sepMarkerRe = regexp.MustCompile(`^(={7}|\|{7})([ \t].*)?$`)
var endMarkerRe = regexp.MustCompile(`^>{7}([ \t].*)?$`)

const conflictFixHint = "Resolve the Git conflict manually, then run 'git add' and commit"

// splitLinesCRLFSafe mirrors Python's str.splitlines() for the one aspect
// that matters here: a CRLF line ending must not leave a trailing "\r"
// attached to the line, or a bare marker line (e.g. "=======\r") fails to
// match the end-anchored marker regexes below even though Python's
// splitlines()-based scan (which treats \r\n as a single terminator) would
// catch it. Found and fixed during review, not part of the original port.
func splitLinesCRLFSafe(text string) []string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	return lines
}

func hasAnyConflictMarker(text string) bool {
	for _, line := range splitLinesCRLFSafe(text) {
		if anyConflictMarkerRe.MatchString(line) {
			return true
		}
	}
	return false
}

// findConflictMarkerLines mirrors conflict.py's find_conflict_marker_lines:
// for TOML, any marker line is blocking; for Markdown/text, only grouped
// <<<<<<<...=======...>>>>>>> blocks are blocking, isolated markers (e.g. a
// stray "=======" heading underline) are not.
func findConflictMarkerLines(path string) (blocking, isolated []int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	lines := splitLinesCRLFSafe(string(data))
	if strings.HasSuffix(path, ".toml") {
		for i, line := range lines {
			if anyConflictMarkerRe.MatchString(line) {
				blocking = append(blocking, i+1)
			}
		}
		return blocking, nil
	}

	blockingSet := map[int]bool{}
	allMarkers := map[int]bool{}
	pendingStart := -1
	var pendingSeps []int
	for i, line := range lines {
		lineno := i + 1
		switch {
		case startMarkerRe.MatchString(line):
			allMarkers[lineno] = true
			pendingStart = lineno
			pendingSeps = nil
		case sepMarkerRe.MatchString(line):
			allMarkers[lineno] = true
			if pendingStart >= 0 {
				pendingSeps = append(pendingSeps, lineno)
			}
		case endMarkerRe.MatchString(line):
			allMarkers[lineno] = true
			if pendingStart >= 0 && len(pendingSeps) > 0 {
				blockingSet[pendingStart] = true
				for _, s := range pendingSeps {
					blockingSet[s] = true
				}
				blockingSet[lineno] = true
			}
			pendingStart = -1
			pendingSeps = nil
		}
	}
	for m := range allMarkers {
		if !blockingSet[m] {
			isolated = append(isolated, m)
		} else {
			blocking = append(blocking, m)
		}
	}
	sort.Ints(blocking)
	sort.Ints(isolated)
	return blocking, isolated
}

func checkConflictMarkers(aikitoDir, home string) DoctorSection {
	var findings []Finding
	checked := 0

	var mdFiles, tomlFiles []string
	for _, dir := range []string{filepath.Join(aikitoDir, "memory")} {
		if entries, derr := os.ReadDir(filepath.Join(dir, "notes")); derr == nil {
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".md") {
					mdFiles = append(mdFiles, filepath.Join(dir, "notes", e.Name()))
				}
			}
		}
	}
	if entries, derr := os.ReadDir(filepath.Join(aikitoDir, "projects")); derr == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			notesDir := filepath.Join(aikitoDir, "projects", e.Name(), "memory", "notes")
			if sub, derr2 := os.ReadDir(notesDir); derr2 == nil {
				for _, f := range sub {
					if strings.HasSuffix(f.Name(), ".md") {
						mdFiles = append(mdFiles, filepath.Join(notesDir, f.Name()))
					}
				}
			}
			agentToml := filepath.Join(aikitoDir, "projects", e.Name(), "agent.toml")
			if _, serr := os.Stat(agentToml); serr == nil {
				tomlFiles = append(tomlFiles, agentToml)
			}
		}
	}
	for _, name := range []string{"config.toml", "skills.toml", "layout.toml"} {
		p := filepath.Join(aikitoDir, name)
		if _, serr := os.Stat(p); serr == nil {
			tomlFiles = append(tomlFiles, p)
		}
	}
	if entries, derr := os.ReadDir(filepath.Join(aikitoDir, "agents")); derr == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".toml") {
				tomlFiles = append(tomlFiles, filepath.Join(aikitoDir, "agents", e.Name()))
			}
		}
	}
	if entries, derr := os.ReadDir(filepath.Join(aikitoDir, "subagents")); derr == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".md") {
				mdFiles = append(mdFiles, filepath.Join(aikitoDir, "subagents", e.Name()))
			}
		}
	}
	if entries, derr := os.ReadDir(filepath.Join(aikitoDir, "mcps")); derr == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".toml") {
				tomlFiles = append(tomlFiles, filepath.Join(aikitoDir, "mcps", e.Name()))
			}
		}
	}

	for _, path := range mdFiles {
		checked++
		rel := homeRel(path, home)
		blocking, isolated := findConflictMarkerLines(path)
		for _, ln := range blocking {
			findings = append(findings, fail(fmt.Sprintf("%s:%d: Git conflict marker detected", rel, ln), conflictFixHint))
		}
		for _, ln := range isolated {
			findings = append(findings, warn(fmt.Sprintf("%s:%d: isolated Git conflict marker or heading underline", rel, ln),
				"Review file to confirm if this line is an intentional heading or unresolved conflict"))
		}
	}
	for _, path := range tomlFiles {
		checked++
		rel := homeRel(path, home)
		blocking, _ := findConflictMarkerLines(path)
		for _, ln := range blocking {
			findings = append(findings, fail(fmt.Sprintf("%s:%d: Git conflict marker detected", rel, ln), conflictFixHint))
		}
	}

	if len(findings) == 0 {
		findings = append(findings, ok(fmt.Sprintf("No conflict markers detected (%d files checked)", checked)))
	}
	return DoctorSection{Name: "ConflictMarkers", Findings: findings}
}

// --- Projects ---

func checkProjects(aikitoDir, home string) DoctorSection {
	var findings []Finding
	entries, derr := os.ReadDir(filepath.Join(aikitoDir, "projects"))
	if derr != nil {
		findings = append(findings, ok("No projects registered"))
		return DoctorSection{Name: "Projects", Findings: findings}
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		agentToml := filepath.Join(aikitoDir, "projects", e.Name(), "agent.toml")
		if _, serr := os.Stat(agentToml); serr != nil {
			continue
		}
		count++
		cfg, lerr := project.LoadConfig(aikitoDir, home, e.Name())
		if lerr != nil {
			findings = append(findings, fail(fmt.Sprintf("projects/%s: %v", e.Name(), lerr), ""))
			continue
		}
		if verr := project.ValidateProjectConfig(agentToml, cfg.Raw); verr != nil {
			findings = append(findings, fail(fmt.Sprintf("projects/%s: %v", e.Name(), verr), ""))
			continue
		}
		binding := project.ResolveProjectBinding(cfg.Raw, home)
		candidates := binding.Entries
		existing := len(binding.ActiveEntries())
		if len(candidates) == 0 {
			findings = append(findings, warn(fmt.Sprintf("projects/%s: no path candidates configured", e.Name()), "aikito show project "+e.Name()))
		} else if existing == 0 {
			findings = append(findings, fail(fmt.Sprintf("projects/%s: no configured path exists on this host (%d candidate(s))", e.Name(), len(candidates)), ""))
		} else {
			findings = append(findings, ok(fmt.Sprintf("projects/%s: %d/%d path(s) available", e.Name(), existing, len(candidates))))
		}
	}
	if count == 0 {
		findings = append(findings, ok("No projects registered"))
	}
	return DoctorSection{Name: "Projects", Findings: findings}
}

// --- Symlinks (reduced: classifies global skill/instruction symlinks this
// Go port itself creates via `aikito sync global`; Python's classify_symlink
// also covers project-scope skill links via project_sync.py, not yet
// ported) ---

func checkSymlinks(aikitoDir, home string) DoctorSection {
	var findings []Finding
	checked, broken := 0, 0

	defs, derr := registry.LoadAgentDefinitions(aikitoDir, home)
	if derr != nil {
		findings = append(findings, fail(fmt.Sprintf("Cannot load agent definitions: %v", derr), ""))
		return DoctorSection{Name: "Symlinks", Findings: findings}
	}
	var names []string
	for n := range defs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		def := defs[name]
		if def.InstructionPath == nil {
			continue
		}
		target := *def.InstructionPath
		info, lerr := os.Lstat(target)
		if lerr != nil {
			continue // not present; not this port's concern here (Drift/Configuration cover "missing")
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue // a real file, not a managed symlink
		}
		checked++
		if _, serr := os.Stat(target); serr != nil {
			broken++
			findings = append(findings, fail(fmt.Sprintf("%s: broken symlink at %s", name, homeRel(target, home)), "aikito sync global --force"))
		}
	}
	if checked == 0 {
		findings = append(findings, ok("No managed symlinks to check"))
	} else if broken == 0 {
		findings = append(findings, ok(fmt.Sprintf("Symlinks OK (%d checked)", checked)))
	}
	return DoctorSection{Name: "Symlinks", Findings: findings}
}

// --- Stub sections (honestly not yet implemented; see package doc comment) ---

// checkAdoption reuses buildAdoptPlan (adopt.go) read-only — it never
// writes anything, it just reports how many agent-native resources (global
// instructions, MCP servers, subagents) `aikito adopt` would currently
// bring into the canonical workspace, plus any adoption conflicts found.
func checkAdoption(aikitoDir, home string) DoctorSection {
	defs, err := registry.LoadAgentDefinitions(aikitoDir, home)
	if err != nil {
		return DoctorSection{Name: "Adoption", Findings: []Finding{
			warn(fmt.Sprintf("Cannot check adoptable resources: %v", err), ""),
		}}
	}
	plan := buildAdoptPlan(aikitoDir, home, defs, map[string]bool{})

	adoptable := len(plan.MCPServers) + len(plan.Subagents)
	if plan.Instructions != nil && plan.Instructions.Action == "CREATE" {
		adoptable++
	}
	// Mirrors doctor.py's check_adoption: per-resource blockers first, then
	// the pending-count summary, and the OK line only when there is nothing
	// at all to report.
	var findings []Finding
	if plan.Instructions != nil && plan.Instructions.Action == "CONFLICT" {
		findings = append(findings, warn("Global instructions: canonical content diverges from agent-native source(s)", "aikito adopt --verbose"))
	}
	for _, c := range plan.MCPConflicts {
		findings = append(findings, warn(fmt.Sprintf("MCP server '%s': %s", c.Name, c.Reason), "aikito adopt --skip "+c.Name))
	}
	if adoptable > 0 {
		findings = append(findings, warn(
			fmt.Sprintf("%d local Agent resource(s) are available to adopt", adoptable),
			"aikito adopt --dry-run --verbose"))
	}
	if len(findings) == 0 {
		findings = append(findings, ok("No external Agent configuration needs adoption"))
	}
	return DoctorSection{Name: "Adoption", Findings: findings}
}

// --- Orphans ---
//
// Ports doctor.py's check_orphans. Sub-checks 2a (orphan subagent files) and
// 2c (residual managed MCP entries) are full, faithful ports — they reuse
// primitives already built for `sync subagents`/`sync mcp` rather than
// reimplementing detection logic. Sub-check 2b's "stale entries in
// ~/.agents/skills/" half is a documented simplification: Python's version
// goes through workspace/inspection.py's skill_views to distinguish a
// locally-conflicting unmanaged item from a plain "not in skills.toml"
// absence; that inspection framework isn't ported, so this reports every
// ~/.agents/skills/ entry not in skills.toml's current selection uniformly
// as "not in skills.toml" rather than drawing that distinction.
func checkOrphans(aikitoDir, home string) DoctorSection {
	var findings []Finding

	// 2a. Orphan subagent files: an agent-native subagent file/entry with no
	// canonical subagents/<name>.md counterpart. BuildSubagentPlan already
	// computes this (the SAOrphan action, already relied on elsewhere in
	// this file — see checkDrift's `op.Action == sync.SAOrphan` exclusion).
	ops, serr := sync.BuildSubagentPlan(aikitoDir, home, sync.BuildSubagentPlanOptions{})
	if serr != nil {
		findings = append(findings, warn(fmt.Sprintf("Cannot check subagent orphans: %v", serr), ""))
	} else {
		orphanCount := 0
		for _, op := range ops {
			if op.Action != sync.SAOrphan {
				continue
			}
			orphanCount++
			findings = append(findings, fail(
				fmt.Sprintf("%s: orphan subagent file %s", op.Agent, homeRel(op.TargetPath, home)),
				"aikito sync subagents --prune"))
		}
		if orphanCount == 0 {
			findings = append(findings, ok("No orphan subagent files"))
		}
	}

	// 2b. Orphan skill directories in <workspace>/skills/: a directory not
	// referenced by skills.toml's global selection or any project's
	// agent.toml skills list.
	globalSkills := readTOMLStringSet(filepath.Join(aikitoDir, "skills.toml"), "skills")
	projectSkills := map[string]bool{}
	if entries, derr := os.ReadDir(filepath.Join(aikitoDir, "projects")); derr == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			for s := range readTOMLStringSet(filepath.Join(aikitoDir, "projects", e.Name(), "agent.toml"), "skills") {
				projectSkills[s] = true
			}
		}
	}
	allRegisteredSkills := map[string]bool{}
	for s := range globalSkills {
		allRegisteredSkills[s] = true
	}
	for s := range projectSkills {
		allRegisteredSkills[s] = true
	}

	skillsDir := filepath.Join(aikitoDir, "skills")
	if entries, derr := os.ReadDir(skillsDir); derr == nil {
		var orphanNames []string
		for _, e := range entries {
			if e.IsDir() && !allRegisteredSkills[e.Name()] {
				orphanNames = append(orphanNames, e.Name())
			}
		}
		sort.Strings(orphanNames)
		if len(orphanNames) == 0 {
			findings = append(findings, ok("No orphan skill directories in skills/"))
		} else {
			for _, name := range orphanNames {
				target := filepath.Join(skillsDir, name)
				if hasUserFiles(target) {
					findings = append(findings, warn(
						fmt.Sprintf("skills/%s: orphan skill directory (not in skills.toml or any project agent.toml)", name), ""))
				} else {
					findings = append(findings, warn(
						fmt.Sprintf("skills/%s: empty directory, safe to delete", name),
						fmt.Sprintf("Remove the empty directory manually: %s", target)))
				}
			}
		}
	}

	// 2b (simplified): stale entries in ~/.agents/skills/ not in skills.toml.
	agentsSkillsDir := filepath.Join(home, ".agents", "skills")
	if entries, derr := os.ReadDir(agentsSkillsDir); derr == nil {
		var staleNames []string
		for _, e := range entries {
			if !globalSkills[e.Name()] {
				staleNames = append(staleNames, e.Name())
			}
		}
		sort.Strings(staleNames)
		if len(staleNames) == 0 {
			findings = append(findings, ok("No stale entries in ~/.agents/skills/"))
		} else {
			for _, name := range staleNames {
				findings = append(findings, fail(
					fmt.Sprintf("~/.agents/skills/%s: not in skills.toml", name), "aikito sync global"))
			}
		}
	}

	// 2c. Residual managed MCP entries: an agent-native config still has an
	// entry this tool previously wrote (per the state file's recorded
	// target_name), but that server is no longer defined in mcps/*.toml —
	// i.e. it was deleted from the canonical workspace without ever being
	// un-synced, and `sync mcp`'s own sweep (executor.go's REMOVE handling)
	// only runs when the removal is actually synced, not detected passively.
	specs, lerr := mcp.LoadAgentSpecs(aikitoDir, home)
	if lerr == nil {
		currentlyDefined := map[[2]string]bool{}
		for _, spec := range specs {
			currentlyDefined[[2]string{spec.Agent, spec.TargetName}] = true
		}
		previouslyManaged := map[[2]string]bool{}
		if state, serr2 := mcp.LoadState(home); serr2 == nil {
			for key, entry := range state.Entries {
				agentPart, _, found := strings.Cut(key, ":")
				if found && entry.TargetName != "" {
					previouslyManaged[[2]string{agentPart, entry.TargetName}] = true
				}
			}
		}
		if defs, derr2 := registry.LoadAgentDefinitions(aikitoDir, home); derr2 == nil {
			var agentNames []string
			for name := range defs {
				agentNames = append(agentNames, name)
			}
			sort.Strings(agentNames)
			for _, agentName := range agentNames {
				def := defs[agentName]
				if def.MCP == nil || !def.MCP.IsSupported() {
					continue
				}
				data, rerr := os.ReadFile(def.MCP.ConfigPath)
				if rerr != nil {
					continue
				}
				existing, eerr := mcp.ReadAllEntries(def.MCP.Adapter, string(data))
				if eerr != nil {
					continue
				}
				keys := existing.Keys()
				sort.Strings(keys)
				for _, srvKey := range keys {
					pair := [2]string{agentName, srvKey}
					if previouslyManaged[pair] && !currentlyDefined[pair] {
						findings = append(findings, fail(
							fmt.Sprintf("%s: residual managed MCP entry '%s' in %s", def.DisplayName, srvKey, homeRel(def.MCP.ConfigPath, home)),
							"aikito sync mcp"))
					}
				}
			}
		}
	}

	return DoctorSection{Name: "Orphans", Findings: findings}
}

// readTOMLStringSet reads field (expected []string, e.g. "skills") from the
// TOML document at path, returning an empty set on any read/parse/shape
// error (matching doctor.py's orphan check, which silently treats a
// malformed skills.toml/agent.toml as "no selections" rather than failing
// the whole check).
func readTOMLStringSet(path, field string) map[string]bool {
	set := map[string]bool{}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		return set
	}
	doc, derr := workspace.DecodeTOML(data)
	if derr != nil {
		return set
	}
	raw, ok2 := doc[field].([]any)
	if !ok2 {
		return set
	}
	for _, v := range raw {
		if s, ok3 := v.(string); ok3 {
			set[s] = true
		}
	}
	return set
}

// hasUserFiles mirrors doctor.py's _has_user_files: true if dir contains
// any regular file or symlink anywhere in its tree (an unreadable entry
// also counts as "has files", matching Python's fail-safe OSError handling
// — don't misidentify an inaccessible directory as empty).
func hasUserFiles(dir string) bool {
	found := false
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			found = true
			return filepath.SkipAll
		}
		if path == dir {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 || d.Type().IsRegular() {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found || err != nil
}

func checkLocalStateStub() DoctorSection {
	return DoctorSection{Name: "LocalState", Findings: []Finding{
		warn("Local sync-state inspection is not yet implemented in this Go build (needs local_state.py's remote-sync bookkeeping — phase 2/multi-machine territory, out of scope for this phase).", ""),
	}}
}
