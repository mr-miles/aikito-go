package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/mr-miles/aikito-go/internal/mcp"
	"github.com/mr-miles/aikito-go/internal/project"
	"github.com/mr-miles/aikito-go/internal/registry"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// cmdShow ports cli_show.py.
func cmdShow(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "[ERROR] Usage: aikito show <project|skill|instructions|mcp|subagent|inbox|memory> [target]")
		return 2
	}
	kind, rest := args[0], args[1:]
	switch kind {
	case "project", "projects", "skill", "skills", "instructions", "mcp", "mcps", "subagent", "subagents", "inbox", "memory":
	default:
		fmt.Fprintf(stderr, "[ERROR] Unknown show target: %s\n", kind)
		return 2
	}
	sa, extra, ok := parseShowArgs(kind, rest)
	if !ok {
		fmt.Fprintf(stderr, "[ERROR] --color must be one of: auto, always, never (got %q)\n", sa.colorOpt)
		return 2
	}
	if sa.exclusiveErr != "" {
		fmt.Fprintf(stderr, "%saikito show %s: error: %s\n", subcommandUsage("show "+kind), kind, sa.exclusiveErr)
		return 2
	}
	if len(extra) > 0 {
		return argparseUnrecognized(stderr, extra)
	}
	aikitoDir, _, ok := resolveReportWorkspace(env, stderr)
	if !ok {
		return 1
	}
	sa.useUnicode, sa.useColor = resolveColorFlags(sa.colorOpt, sa.noColor, env)

	switch kind {
	case "project", "projects":
		return cmdShowProject(sa, kind, aikitoDir, env, stdout, stderr)
	case "skill", "skills":
		return cmdShowSkill(sa, aikitoDir, stdout, stderr)
	case "instructions":
		return cmdShowInstructions(sa, aikitoDir, env, stdout, stderr)
	case "mcp", "mcps":
		return cmdShowMCP(sa, aikitoDir, env, stdout, stderr)
	case "subagent", "subagents":
		return cmdShowSubagent(sa, aikitoDir, env, stdout, stderr)
	case "inbox":
		return cmdShowInbox(sa, aikitoDir, env, stdout, stderr)
	default:
		return cmdShowMemory(sa, aikitoDir, env, stdout, stderr)
	}
}

type showArgs struct {
	target               string
	colorOpt             string
	noColor              bool
	agentSet, live       bool
	agent                string
	agentHasValue        bool // --agent VALUE; Python's bare --agent is True, not a string
	projectSet, all      bool
	project              string
	exclusiveErr         string // argparse mutually exclusive group error
	useUnicode, useColor bool
}

// parseShowArgs is the argparse configuration of each show subcommand.
// --agent and --project take an optional value (nargs="?").
func parseShowArgs(kind string, args []string) (showArgs, []string, bool) {
	sa := showArgs{colorOpt: "auto"}
	allowAgent := kind == "mcp" || kind == "mcps" || kind == "subagent" || kind == "subagents"
	allowLive := kind == "mcp" || kind == "mcps"
	allowMemory := kind == "memory"
	var extra []string
	optionalValue := func(i int) (string, int) {
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			return args[i+1], i + 1
		}
		return "", i
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--color" && i+1 < len(args):
			i++
			sa.colorOpt = args[i]
		case strings.HasPrefix(a, "--color="):
			sa.colorOpt = strings.TrimPrefix(a, "--color=")
		case a == "--no-color":
			sa.noColor = true
		case allowAgent && a == "--agent":
			sa.agentSet = true
			j := i
			sa.agent, i = optionalValue(i)
			sa.agentHasValue = i != j
		case allowAgent && strings.HasPrefix(a, "--agent="):
			sa.agentSet, sa.agentHasValue, sa.agent = true, true, strings.TrimPrefix(a, "--agent=")
		case allowLive && a == "--live":
			sa.live = true
		case allowMemory && a == "--project":
			if sa.all && sa.exclusiveErr == "" {
				sa.exclusiveErr = "argument --project: not allowed with argument --all"
			}
			sa.projectSet = true
			sa.project, i = optionalValue(i)
		case allowMemory && strings.HasPrefix(a, "--project="):
			if sa.all && sa.exclusiveErr == "" {
				sa.exclusiveErr = "argument --project: not allowed with argument --all"
			}
			sa.projectSet, sa.project = true, strings.TrimPrefix(a, "--project=")
		case allowMemory && a == "--all":
			if sa.projectSet && sa.exclusiveErr == "" {
				sa.exclusiveErr = "argument --all: not allowed with argument --project"
			}
			sa.all = true
		case strings.HasPrefix(a, "-") && a != "-":
			extra = append(extra, a)
		case sa.target == "":
			sa.target = a
		default:
			extra = append(extra, a)
		}
	}
	valid := sa.colorOpt == "auto" || sa.colorOpt == "always" || sa.colorOpt == "never"
	return sa, extra, valid
}

// --- shared exact-then-unique-prefix-then-conflict resolver ---
// resolve.py's resolve_{subagent,mcp}_target_for_command; also used by
// edit, rm and rename.

type resolveLabels struct {
	conflictNoun     string // "skills", "subagents", "MCP servers", "inbox notes", "memory notes"
	specifyLine      string // e.g. "Please specify the exact skill name, e.g.:"
	cmdName          string // "skill", "subagent", "mcp", "inbox", "memory"
	notFoundSingular string // "Skill", "Subagent", "MCP server", "Inbox note", "Memory note"
	notFoundHint     string // e.g. "Run 'aikito show subagents' to view available subagents."
	scopeSuffix      string // appended as " (<scopeSuffix>)" to each conflict bullet
}

func resolveByName(names []string, target, operation string, labels resolveLabels, stderr io.Writer) (string, bool) {
	targetNorm := strings.TrimSpace(target)
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)

	var exact []string
	for _, n := range sorted {
		if n == targetNorm {
			exact = append(exact, n)
		}
	}
	if len(exact) == 1 {
		return exact[0], true
	}

	var prefix []string
	for _, n := range sorted {
		if strings.HasPrefix(n, targetNorm) {
			prefix = append(prefix, n)
		}
	}
	if len(prefix) == 1 {
		return prefix[0], true
	}
	if len(prefix) > 1 {
		fmt.Fprintf(stderr, "[CONFLICT] Multiple %s match '%s':\n\n", labels.conflictNoun, target)
		for _, n := range prefix {
			if labels.scopeSuffix != "" {
				fmt.Fprintf(stderr, "  - %s (%s)\n", n, labels.scopeSuffix)
			} else {
				fmt.Fprintf(stderr, "  - %s\n", n)
			}
		}
		fmt.Fprintf(stderr, "\n%s\n", labels.specifyLine)
		for _, n := range prefix {
			fmt.Fprintf(stderr, "  aikito %s %s %s\n", operation, labels.cmdName, n)
		}
		return "", false
	}

	fmt.Fprintf(stderr, "[ERROR] %s '%s' not found.\n", labels.notFoundSingular, target)
	if labels.notFoundHint != "" {
		fmt.Fprintln(stderr, labels.notFoundHint)
	}
	return "", false
}

func printFileVerbatim(path string, stdout, stderr io.Writer) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to read %s: %v\n", path, err)
		return 1
	}
	fmt.Fprint(stdout, string(data))
	return 0
}

// printResourceFile prints a resolved file, with Python's per-kind read
// error message.
func printResourceFile(path, what string, stdout, stderr io.Writer) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to read %s %s: %v\n", what, path, err)
		return 1
	}
	fmt.Fprint(stdout, string(data))
	return 0
}

// detectProjectOrConflict wraps detect_current_project with the CLI's
// conflict message. ok is false after printing a conflict.
func detectProjectOrConflict(aikitoDir string, env Environment, stderr io.Writer) (string, bool) {
	detected, err := project.DetectCurrentProject(aikitoDir, env.Cwd, env.Home)
	var conflict *project.ContextConflictError
	if errors.As(err, &conflict) {
		fmt.Fprintf(stderr, "[CONFLICT] Multiple projects match current directory '%s': %s\n", conflict.Path, strings.Join(conflict.Projects, ", "))
		return "", false
	}
	return detected, true
}

// --- project ---

func formatProjectPaths(p projectSummary, useUnicode bool) string {
	if len(p.CandidatePaths) == 0 {
		return p.Path
	}
	present := "v"
	if useUnicode {
		present = "✓"
	}
	var parts []string
	for _, c := range p.CandidatePaths {
		mark := "-"
		if c.Exists {
			mark = present
		}
		if c.Label != "default" {
			parts = append(parts, fmt.Sprintf("[%s]%s %s", c.Label, mark, c.Display))
		} else {
			parts = append(parts, mark+" "+c.Display)
		}
	}
	return strings.Join(parts, ", ")
}

// renderProjectDetail ports render.py render_project_detail.
func renderProjectDetail(p projectSummary, useUnicode bool) string {
	sep := " | "
	if useUnicode {
		sep = " · "
	}
	skills := "0 selected"
	if len(p.SkillNames) > 0 {
		skills = strings.Join(p.SkillNames, ", ")
	}
	memory := fmt.Sprintf("%d notes%s%d references", p.MemoryNotesCount, sep, len(p.MemoryRefs))
	description := p.Description
	if description == "" {
		description = "-"
	}
	instructions := "not configured"
	if p.InstructionsStatus == "OK" {
		instructions = "configured"
	}
	fields := [][2]string{
		{"Project:", p.Name},
		{"Description:", description},
		{"Canonical path:", filepath.Dir(p.ConfigPath)},
		{"Project paths:", formatProjectPaths(p, useUnicode)},
		{"Sync mode:", p.SyncMode},
		{"Instructions:", instructions},
		{"Selected skills:", skills},
		{"Memory:", memory},
		{"Context:", formatTokenEstimate(p.ContextTokens) + " tokens"},
		{"Sync:", p.RuntimeStatus},
	}
	if p.Error != "" {
		fields = append(fields, [2]string{"Error:", p.Error})
	}
	for _, n := range pythonSplitLines(p.InstructionsNotice) {
		if strings.TrimSpace(n) != "" {
			fields = append(fields, [2]string{"Notice:", strings.TrimSpace(n)})
		}
	}
	for _, n := range pythonSplitLines(p.SkillsNotice) {
		if strings.TrimSpace(n) != "" {
			fields = append(fields, [2]string{"Notice:", strings.TrimSpace(n) + " (not managed by Aikito)"})
		}
	}
	var issues []projectResourceDetail
	for _, d := range p.Details {
		if d.Status != "OK" {
			issues = append(issues, d)
		}
	}
	switch {
	case len(issues) > 0:
		for _, d := range issues {
			messages := []string{""}
			if d.Detail != "" {
				messages = strings.Split(d.Detail, "; ")
			}
			for _, m := range messages {
				fields = append(fields, [2]string{"Issue:", fmt.Sprintf("%s [%s]: %s", d.Resource, d.Status, m)})
			}
		}
		if h := p.fixHint(); h != "" {
			fields = append(fields, [2]string{"Fix:", h})
		}
	case p.RuntimeStatus == "OFFLINE":
		var parts []string
		for _, c := range p.CandidatePaths {
			if c.Exists {
				continue
			}
			if c.Label != "default" {
				parts = append(parts, fmt.Sprintf("[%s] %s", c.Label, c.Display))
			} else {
				parts = append(parts, c.Display)
			}
		}
		cand := strings.Join(parts, ", ")
		if cand == "" {
			cand = p.Path
		}
		fields = append(fields, [2]string{"Notice:", fmt.Sprintf("Project is offline on this host (candidates: %s)", cand)})
	case p.RuntimeStatus == "UNBOUND":
		fields = append(fields, [2]string{"Issue:", "Project: no directory is registered"})
	}
	return renderKeyValueFields(fields)
}

// pythonSplitLines is str.splitlines() for the common line endings.
func pythonSplitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func cmdShowProject(sa showArgs, kind, aikitoDir string, env Environment, stdout, stderr io.Writer) int {
	projects := collectProjectSummaries(aikitoDir, env.Home)
	target := sa.target
	if target == "." {
		detected, ok := detectProjectOrConflict(aikitoDir, env, stderr)
		if !ok {
			return 1
		}
		if detected == "" {
			cwd, _ := workspace.ResolvePath(env.Cwd)
			fmt.Fprintf(stderr, "[ERROR] Current directory is not inside a registered project: %s\n", cwd)
			return 1
		}
		target = detected
	}
	if target == "" && kind != "projects" {
		detected, ok := detectProjectOrConflict(aikitoDir, env, stderr)
		if !ok {
			return 1
		}
		if detected != "" {
			fmt.Fprintf(stdout, "[aikito] Target project: '%s' (detected from cwd)\n", detected)
			target = detected
		}
	}
	if target == "" {
		memRows, _, _ := collectMemoryStatusRows(newInspectionContext(aikitoDir, env.Home), func(m string) { fmt.Fprintln(stderr, m) })
		fmt.Fprintln(stdout, renderProjectsTable(projects, sa.useUnicode, sa.useColor, memRows))
		return 0
	}
	var matches []projectSummary
	for _, p := range projects {
		if p.Name == target {
			matches = append(matches, p)
		}
	}
	if len(matches) == 0 {
		for _, p := range projects {
			if strings.HasPrefix(p.Name, target) {
				matches = append(matches, p)
			}
		}
	}
	switch len(matches) {
	case 1:
		fmt.Fprintln(stdout, renderProjectDetail(matches[0], sa.useUnicode))
		return 0
	case 0:
		fmt.Fprintf(stderr, "[ERROR] Project '%s' not found.\n", target)
	default:
		var names []string
		for _, p := range matches {
			names = append(names, p.Name)
		}
		fmt.Fprintf(stderr, "[CONFLICT] Multiple projects match '%s': %s\n", target, strings.Join(names, ", "))
	}
	return 1
}

// --- skill ---

type skillRow struct {
	Name, Scope, SourceStatus, Description string
}

// collectSkillsRows ports status.py collect_skills_rows.
func collectSkillsRows(aikitoDir string, warn func(string)) []skillRow {
	global := map[string]bool{}
	for _, s := range globalSkillsList(aikitoDir, warn) {
		global[s] = true
	}
	projectSkills := map[string]map[string]bool{}
	var projectNames []string
	for _, name := range sortedDirEntries(filepath.Join(aikitoDir, "projects")) {
		dir := filepath.Join(aikitoDir, "projects", name)
		agentToml := filepath.Join(dir, "agent.toml")
		if !isDirPath(dir) || !isRegularFilePath(agentToml) {
			continue
		}
		data, err := os.ReadFile(agentToml)
		var cfg map[string]any
		if err == nil {
			err = toml.Unmarshal(data, &cfg)
		}
		if err != nil {
			warn(fmt.Sprintf("[WARN] Failed to read configuration for project '%s': %v", name, err))
			continue
		}
		if list, ok := cfg["skills"].([]any); ok {
			set := map[string]bool{}
			for _, s := range list {
				set[pyStr(s)] = true
			}
			projectSkills[name] = set
			projectNames = append(projectNames, name)
		}
	}
	skillsDir := filepath.Join(aikitoDir, "skills")
	all := map[string]bool{}
	for s := range global {
		all[s] = true
	}
	for _, name := range sortedDirEntries(skillsDir) {
		if isDirPath(filepath.Join(skillsDir, name)) {
			all[name] = true
		}
	}
	for _, set := range projectSkills {
		for s := range set {
			all[s] = true
		}
	}
	var rows []skillRow
	for _, name := range sortedKeys(all) {
		var projMatches []string
		for _, p := range projectNames {
			if projectSkills[p][name] {
				projMatches = append(projMatches, p)
			}
		}
		scope := "Orphan"
		if global[name] {
			scope = "Global"
		} else if len(projMatches) > 0 {
			scope = strings.Join(projMatches, ", ")
		}
		source, desc := "MISSING", "-"
		if isDirPath(filepath.Join(skillsDir, name)) {
			source = "OK"
			if d, ok := extractSkillDescription(filepath.Join(skillsDir, name)); ok {
				desc = d
			}
		}
		rows = append(rows, skillRow{name, scope, source, desc})
	}
	rank := func(r skillRow) int {
		switch r.Scope {
		case "Global":
			return 0
		case "Orphan":
			return 2
		}
		return 1
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ri, rj := rank(rows[i]), rank(rows[j])
		if ri != rj {
			return ri < rj
		}
		if ri == 1 && rows[i].Scope != rows[j].Scope {
			return rows[i].Scope < rows[j].Scope
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}

// renderSkillsTable ports render.py render_skills_table.
func renderSkillsTable(rows []skillRow, useUnicode, useColor bool) string {
	var table [][]string
	last := ""
	issue := false
	for i, s := range rows {
		if i > 0 && s.Scope != last {
			table = append(table, []string{tableSeparator})
		}
		last = s.Scope
		desc := s.Description
		if desc == "" {
			desc = "-"
		}
		table = append(table, []string{s.Name, s.Scope, formatStatusBadge(s.SourceStatus, useUnicode, useColor), desc})
		issue = issue || badgeIsIssue(s.SourceStatus, useUnicode)
	}
	out := buildGenericTable([]string{"Skill", "Scope", "Source", "Description"}, table, useUnicode, useColor, []int{3})
	if issue {
		out += "\n\n" + renderLegend(useUnicode, useColor)
	}
	return out
}

func cmdShowSkill(sa showArgs, aikitoDir string, stdout, stderr io.Writer) int {
	warn := func(m string) { fmt.Fprintln(stderr, m) }
	if sa.target == "" {
		fmt.Fprintln(stdout, renderSkillsTable(collectSkillsRows(aikitoDir, warn), sa.useUnicode, sa.useColor))
		printBundledSkillNotice(aikitoDir, stderr)
		return 0
	}
	rows := collectSkillsRows(aikitoDir, warn)
	target := strings.TrimSpace(sa.target)
	var exact, prefix []skillRow
	for _, r := range rows {
		if r.Name == target {
			exact = append(exact, r)
		}
		if strings.HasPrefix(r.Name, target) {
			prefix = append(prefix, r)
		}
	}
	var row skillRow
	switch {
	case len(exact) == 1:
		row = exact[0]
	case len(prefix) == 1:
		row = prefix[0]
	case len(prefix) > 1:
		fmt.Fprintf(stderr, "[CONFLICT] Multiple skills match '%s':\n\n", sa.target)
		for _, r := range prefix {
			fmt.Fprintf(stderr, "  - %s (%s)\n", r.Name, r.Scope)
		}
		fmt.Fprintln(stderr, "\nPlease specify the exact skill name, e.g.:")
		for _, r := range prefix {
			fmt.Fprintf(stderr, "  aikito show skill %s\n", r.Name)
		}
		return 1
	default:
		fmt.Fprintf(stderr, "[ERROR] Skill '%s' not found.\n", sa.target)
		fmt.Fprintln(stderr, "Run 'aikito show skills' to view available skills.")
		return 1
	}
	skillFile := filepath.Join(aikitoDir, "skills", row.Name, "SKILL.md")
	if !isRegularFilePath(skillFile) {
		fmt.Fprintf(stderr, "[ERROR] SKILL.md not found for skill '%s' at %s\n", row.Name, skillFile)
		return 1
	}
	if rc := printResourceFile(skillFile, "skill file", stdout, stderr); rc != 0 {
		return rc
	}
	printBundledSkillNotice(aikitoDir, stderr, row.Name)
	return 0
}

// --- instructions ---

type instrSource struct {
	name, instructions, checkout string // checkout "" = None
}

// findInstructionSources ports resolve.py find_instruction_sources.
func findInstructionSources(aikitoDir, home string, stderr io.Writer) []instrSource {
	sources := []instrSource{{"global", filepath.Join(aikitoDir, "global", "AGENTS.md"), ""}}
	for _, name := range sortedDirEntries(filepath.Join(aikitoDir, "projects")) {
		dir := filepath.Join(aikitoDir, "projects", name)
		configPath := filepath.Join(dir, "agent.toml")
		instructions := filepath.Join(dir, "AGENTS.md")
		if !isDirPath(dir) || !isRegularFilePath(configPath) {
			continue
		}
		data, err := os.ReadFile(configPath)
		var cfg map[string]any
		if err == nil {
			err = toml.Unmarshal(data, &cfg)
		}
		if err != nil {
			fmt.Fprintf(stderr, "[WARN] Failed to read %s: %v\n", configPath, err)
			continue
		}
		binding := project.ResolveProjectBinding(cfg, home)
		switch active := binding.ActiveEntries(); {
		case len(active) > 0:
			for _, e := range active {
				sources = append(sources, instrSource{name, instructions, e.ResolvedPath})
			}
		case len(binding.Entries) > 0:
			sources = append(sources, instrSource{name, instructions, binding.Entries[0].ResolvedPath})
		default:
			sources = append(sources, instrSource{name, instructions, ""})
		}
	}
	return sources
}

// classifySymlinkStatus is link.py classify_symlink followed by
// symlink_verdict_to_status.
func classifySymlinkStatus(path, expected string) string {
	if isSymlinkPath(path) {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "CONFLICT"
		}
		want, _ := workspace.ResolvePath(expected)
		if resolved == want {
			return "OK"
		}
		return "CONFLICT"
	}
	if _, err := os.Stat(path); err == nil {
		return "CONFLICT"
	}
	return "MISSING"
}

func cmdShowInstructions(sa showArgs, aikitoDir string, env Environment, stdout, stderr io.Writer) int {
	if sa.target == "" {
		reg, err := registry.LoadStrict(aikitoDir, env.Home)
		if err != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return 1
		}
		source := filepath.Join(aikitoDir, "global", "AGENTS.md")
		names := map[string]string{"OK": "linked", "MISSING": "missing", "CONFLICT": "conflict", "SKIP": "skipped"}
		blocks := []string{"Instructions"}
		for _, a := range reg.InFileOrder().Values() {
			status, target := "SKIP", "-"
			if a.InstructionPath != nil {
				target = safeRelativePath(*a.InstructionPath, env.Home)
				if isDirPath(filepath.Dir(*a.InstructionPath)) || pathExists(filepath.Dir(*a.InstructionPath)) {
					status = classifySymlinkStatus(*a.InstructionPath, source)
				}
			}
			blocks = append(blocks, renderKeyValueFields([][2]string{{"Agent:", a.DisplayName}, {"Status:", names[status]}, {"Target:", target}}))
		}
		var projectRows [][2]string
		projNames := map[string]string{"OK": "linked", "MISSING": "missing", "CONFLICT": "conflict"}
		for _, s := range findInstructionSources(aikitoDir, env.Home, stderr)[1:] {
			text, ok := readTextReplace(s.instructions)
			if !ok || !isRegularFilePath(s.instructions) || strings.TrimSpace(text) == "" || s.checkout == "" {
				projectRows = append(projectRows, [2]string{s.name + ":", "-"})
				continue
			}
			st := classifySymlinkStatus(filepath.Join(s.checkout, ".agents", "AGENTS.md"), s.instructions)
			projectRows = append(projectRows, [2]string{s.name + ":", projNames[st]})
		}
		if len(projectRows) > 0 {
			blocks = append(blocks, "Projects\n"+renderKeyValueFields(projectRows))
		}
		fmt.Fprintln(stdout, strings.Join(blocks, "\n\n"))
		return 0
	}

	sources := findInstructionSources(aikitoDir, env.Home, stderr)
	name, path := "", ""
	if sa.target == "." {
		detected, ok := detectProjectOrConflict(aikitoDir, env, stderr)
		if !ok {
			return 1
		}
		if detected == "" {
			cwd, _ := workspace.ResolvePath(env.Cwd)
			fmt.Fprintf(stderr, "[ERROR] Current directory is not inside a registered project: %s\n", cwd)
			return 1
		}
		name, path = detected, filepath.Join(aikitoDir, "projects", detected, "AGENTS.md")
		for _, s := range sources {
			if s.name == detected {
				path = s.instructions
				break
			}
		}
	} else {
		for _, s := range sources {
			if s.name == sa.target {
				name, path = s.name, s.instructions
				break
			}
		}
		if name == "" {
			fmt.Fprintf(stderr, "[ERROR] Instructions target '%s' not found.\n", sa.target)
			fmt.Fprintln(stderr, "Run 'aikito show instructions' to view available targets.")
			return 1
		}
	}
	if !isRegularFilePath(path) {
		fmt.Fprintf(stderr, "[ERROR] Instructions file not found: %s\n", path)
		return 1
	}
	if rc := printFileVerbatim(path, stdout, stderr); rc != 0 {
		return rc
	}
	if sa.target == "." && name != "global" {
		fmt.Fprintln(stderr, "\nAlso active: global instructions")
		fmt.Fprintln(stderr, "View with: aikito show instructions global")
	}
	return 0
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// --- mcp ---

func mcpNames(aikitoDir string) ([]string, error) {
	mcpsDir := filepath.Join(aikitoDir, "mcps")
	if !isDirPath(mcpsDir) {
		return nil, fmt.Errorf("MCP directory not found at %s", mcpsDir)
	}
	var names []string
	for _, n := range sortedDirEntries(mcpsDir) {
		if strings.HasSuffix(n, ".toml") && !strings.HasPrefix(n, ".") && isRegularFilePath(filepath.Join(mcpsDir, n)) {
			names = append(names, strings.TrimSuffix(n, ".toml"))
		}
	}
	return names, nil
}

// collectMCPMatrix ports status.py collect_mcp_matrix. With live, servers
// already in sync are probed and shown as "OK (<tools>)" or "ERROR".
func collectMCPMatrix(c *inspectionContext, live bool) ([]subagentRow, []string, error) {
	reg, _, err := c.agents()
	if err != nil {
		return nil, nil, err
	}
	specs, merr := c.mcp()
	if merr != nil {
		specs = nil
	}
	var agentNames []string
	display := map[string]string{}
	for _, a := range reg.Values() {
		agentNames = append(agentNames, a.DisplayName)
		display[a.Name] = a.DisplayName
	}
	servers := map[string]map[string]string{}
	for _, spec := range specs {
		d, known := display[spec.Agent]
		if !known {
			d = spec.Agent
		}
		if servers[spec.Server] == nil {
			servers[spec.Server] = map[string]string{}
		}
		st := "SKIP"
		if known {
			st = c.mcpStatus(spec)
		}
		servers[spec.Server][d] = st
	}
	if live {
		var liveSpecs []mcp.AgentSpec
		for _, spec := range specs {
			d, known := display[spec.Agent]
			if spec.Enabled && known && servers[spec.Server][d] == "OK" {
				liveSpecs = append(liveSpecs, spec)
			}
		}
		for i, r := range mcp.ProbeMCPToolsForSpecs(liveSpecs, mcpProbeTimeout) {
			d := display[liveSpecs[i].Agent]
			switch r.Status {
			case "OK":
				servers[liveSpecs[i].Server][d] = fmt.Sprintf("OK (%d)", len(r.ToolNames))
			case "ERROR":
				servers[liveSpecs[i].Server][d] = "ERROR"
			}
		}
	}
	var rows []subagentRow
	for name, st := range servers {
		for _, d := range agentNames {
			if _, ok := st[d]; !ok {
				st[d] = "SKIP"
			}
		}
		rows = append(rows, subagentRow{name, st})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, agentNames, nil
}

// renderStatusMatrix ports render_mcp_status_table and the matrix part of
// render_subagents_status_table.
func renderStatusMatrix(firstHeader string, rows []subagentRow, agentNames []string, useUnicode, useColor bool) (string, bool) {
	var table [][]string
	issue := false
	for _, r := range rows {
		line := []string{r.Name}
		for _, ag := range agentNames {
			st, ok := r.Statuses[ag]
			if !ok {
				st = "SKIP"
			}
			line = append(line, formatStatusBadge(st, useUnicode, useColor))
			issue = issue || badgeIsIssue(st, useUnicode)
		}
		table = append(table, line)
	}
	return buildGenericTable(append([]string{firstHeader}, agentNames...), table, useUnicode, useColor, nil), issue
}

func cmdShowMCP(sa showArgs, aikitoDir string, env Environment, stdout, stderr io.Writer) int {
	if sa.live && sa.target == "" && sa.agentSet {
		fmt.Fprintln(stderr, "[ERROR] --agent with --live requires an MCP server target")
		return 2
	}
	if sa.live && sa.target != "" {
		return showMCPLive(sa, aikitoDir, env, stdout, stderr)
	}
	if sa.agentHasValue || (sa.target != "" && sa.agentSet) {
		return showMCPAgentDetails(sa, aikitoDir, env, stdout, stderr)
	}
	if sa.target != "" {
		names, err := mcpNames(aikitoDir)
		if err != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return 1
		}
		matched, ok := resolveByName(names, sa.target, "show", resolveLabels{
			conflictNoun: "MCP servers", specifyLine: "Please specify the exact MCP server name, e.g.:",
			cmdName: "mcp", notFoundSingular: "MCP server",
			notFoundHint: "Run 'aikito show mcp' to view available MCP servers.",
		}, stderr)
		if !ok {
			return 1
		}
		return printResourceFile(filepath.Join(aikitoDir, "mcps", matched+".toml"), "MCP config file", stdout, stderr)
	}
	rows, agentNames, err := collectMCPMatrix(newInspectionContext(aikitoDir, env.Home), sa.live)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	table, issue := renderStatusMatrix("MCP Server", rows, agentNames, sa.useUnicode, sa.useColor)
	if issue {
		table += "\n\n" + renderLegend(sa.useUnicode, sa.useColor)
	}
	fmt.Fprintln(stdout, table)
	return 0
}

// --- subagent ---

func subagentNames(aikitoDir string) []string {
	subDir := filepath.Join(aikitoDir, "subagents")
	var names []string
	for _, n := range sortedDirEntries(subDir) {
		if strings.HasSuffix(n, ".md") && !strings.HasPrefix(n, ".") && !isDirPath(filepath.Join(subDir, n)) {
			names = append(names, strings.TrimSuffix(n, ".md"))
		}
	}
	return names
}

func cmdShowSubagent(sa showArgs, aikitoDir string, env Environment, stdout, stderr io.Writer) int {
	if sa.agentHasValue || (sa.target != "" && sa.agentSet) {
		return showSubagentAgentDetails(sa, aikitoDir, env, stdout, stderr)
	}
	if sa.target != "" {
		matched, ok := resolveByName(subagentNames(aikitoDir), sa.target, "show", resolveLabels{
			conflictNoun: "subagents", specifyLine: "Please specify the exact subagent name, e.g.:",
			cmdName: "subagent", notFoundSingular: "Subagent",
			notFoundHint: "Run 'aikito show subagents' to view available subagents.",
		}, stderr)
		if !ok {
			return 1
		}
		return printResourceFile(filepath.Join(aikitoDir, "subagents", matched+".md"), "subagent file", stdout, stderr)
	}
	rows, orphans, agentNames, err := collectSubagentsMatrix(newInspectionContext(aikitoDir, env.Home))
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	table, issue := renderStatusMatrix("Subagent", rows, agentNames, sa.useUnicode, sa.useColor)
	out := []string{table}
	if len(orphans) > 0 {
		title := "Orphan Subagent Files (Managed files no longer defined):"
		if sa.useColor {
			title = colorize(title, colorYellow, true)
		}
		var orows [][]string
		for _, o := range orphans {
			orows = append(orows, []string{o.AgentDisplayName, o.FilePath})
		}
		out = append(out, "", title, buildGenericTable([]string{"Agent", "Orphan File Path"}, orows, sa.useUnicode, sa.useColor, nil))
		issue = true
	}
	if issue {
		out = append(out, "", renderLegend(sa.useUnicode, sa.useColor))
	}
	fmt.Fprintln(stdout, strings.Join(out, "\n"))
	return 0
}

// --- inbox ---

func inboxDir(aikitoDir string) string {
	home, _ := os.UserHomeDir()
	return workspace.GetInboxPath(aikitoDir, home)
}

// findInboxFiles ports inbox.py find_inbox_files.
func findInboxFiles(dir string) []string {
	if !isDirPath(dir) {
		return nil
	}
	var files []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || p == dir {
			return nil
		}
		if info.IsDir() && strings.HasPrefix(info.Name(), ".") {
			return filepath.SkipDir
		}
		if !info.IsDir() && strings.HasSuffix(p, ".md") && !strings.HasPrefix(info.Name(), ".") && info.Mode().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

func cmdShowInbox(sa showArgs, aikitoDir string, env Environment, stdout, stderr io.Writer) int {
	dir := workspace.GetInboxPath(aikitoDir, env.Home)
	files := findInboxFiles(dir)
	if sa.target == "" {
		if !isDirPath(dir) {
			fmt.Fprintf(stdout, "Inbox directory does not exist: %s\n", dir)
			return 0
		}
		if len(files) == 0 {
			fmt.Fprintf(stdout, "Inbox is empty (%s).\n", dir)
			return 0
		}
		type row struct {
			name, modified string
			mtime          int64
		}
		var rows []row
		for _, f := range files {
			rel, _ := filepath.Rel(dir, f)
			r := row{name: strings.TrimSuffix(filepath.ToSlash(rel), ".md"), modified: "-"}
			if st, err := os.Stat(f); err == nil {
				r.modified = st.ModTime().Format("2006-01-02 15:04")
				r.mtime = st.ModTime().UnixNano()
			}
			rows = append(rows, r)
		}
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].mtime != rows[j].mtime {
				return rows[i].mtime > rows[j].mtime
			}
			return rows[i].name < rows[j].name
		})
		var table [][]string
		for _, r := range rows {
			table = append(table, []string{r.name, r.modified})
		}
		fmt.Fprintln(stdout, buildGenericTable([]string{"Name", "Modified"}, table, sa.useUnicode, sa.useColor, []int{0}))
		return 0
	}

	target, ok := resolveInboxTarget(dir, sa.target, "show", stderr)
	if !ok {
		return 1
	}
	return printResourceFile(target, "inbox note", stdout, stderr)
}

// resolveInboxTarget ports inbox.py resolve_inbox_target_for_command: exact
// match on stem, relative path or file name, else a unique prefix of any of
// them. On failure it prints Python's not-found or conflict text.
func resolveInboxTarget(dir, target, operation string, stderr io.Writer) (string, bool) {
	files := findInboxFiles(dir)
	notFound := func() (string, bool) {
		fmt.Fprintf(stderr, "[ERROR] Inbox note '%s' not found.\n", target)
		fmt.Fprintln(stderr, "Run 'aikito show inbox' to view available inbox files.")
		return "", false
	}
	raw := strings.ReplaceAll(strings.TrimSpace(target), `\`, "/")
	raw = strings.TrimSuffix(raw, "…")
	norm := strings.TrimSuffix(raw, ".md")
	if len(files) == 0 {
		return notFound()
	}
	keys := func(f string) [4]string {
		rel, _ := filepath.Rel(dir, f)
		rel = filepath.ToSlash(rel)
		base := filepath.Base(f)
		return [4]string{strings.TrimSuffix(base, ".md"), strings.TrimSuffix(rel, ".md"), base, rel}
	}
	var candidates []string
	for _, f := range files {
		k := keys(f)
		if norm == k[0] || norm == k[1] || raw == k[2] || raw == k[3] {
			candidates = append(candidates, f)
		}
	}
	if len(candidates) == 0 {
		for _, f := range files {
			for _, k := range keys(f) {
				if strings.HasPrefix(k, norm) || strings.HasPrefix(k, raw) {
					candidates = append(candidates, f)
					break
				}
			}
		}
	}
	switch len(candidates) {
	case 0:
		return notFound()
	case 1:
		return candidates[0], true
	}
	fmt.Fprintf(stderr, "[CONFLICT] Multiple inbox notes match '%s':\n\n", target)
	for _, f := range candidates {
		fmt.Fprintf(stderr, "  - %s\n", keys(f)[1])
	}
	fmt.Fprintln(stderr, "\nPlease specify the exact name, e.g.:")
	for _, f := range candidates {
		fmt.Fprintf(stderr, "  aikito %s inbox %s\n", operation, keys(f)[1])
	}
	return "", false
}

// --- memory ---

// extractNoteTitle ports memory.py extract_note_title.
func extractNoteTitle(path string) string {
	stem := strings.TrimSuffix(filepath.Base(path), ".md")
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range pythonSplitLines(strings.ToValidUTF8(string(data), "")) {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "# ") && !strings.HasPrefix(t, "## ") {
				if title := strings.TrimSpace(t[2:]); title != "" {
					return title
				}
			}
		}
	}
	words := strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(stem))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
	}
	return strings.Join(words, " ")
}

type memoryNoteRow struct {
	scope, note, title, link string
	updated                  *string
}

// collectMemoryNotesRows ports status.py collect_memory_notes_rows.
func collectMemoryNotesRows(aikitoDir, home, proj string, includeGlobal bool) [][5]string {
	var rows [][5]string
	add := func(scope, path, link string) {
		updated := "-"
		if st, err := os.Stat(path); err == nil {
			updated = formatMemoryUpdatedDate(localDate(st.ModTime()))
		}
		rows = append(rows, [5]string{scope, strings.TrimSuffix(filepath.Base(path), ".md"), extractNoteTitle(path), link, updated})
	}
	if proj == "" || strings.ToLower(proj) == "global" || includeGlobal {
		for _, p := range notesGlob(filepath.Join(aikitoDir, "memory", "notes")) {
			add("Global", p, "SKIP")
		}
	}
	if proj == "" || strings.ToLower(proj) != "global" {
		for _, name := range sortedDirEntries(filepath.Join(aikitoDir, "projects")) {
			dir := filepath.Join(aikitoDir, "projects", name)
			if !isDirPath(dir) || (proj != "" && name != proj) {
				continue
			}
			link := "MISSING"
			if data, err := os.ReadFile(filepath.Join(dir, "agent.toml")); err == nil {
				var cfg map[string]any
				if toml.Unmarshal(data, &cfg) == nil {
					binding := project.ResolveProjectBinding(cfg, home)
					if active := binding.ActiveEntries(); len(active) > 0 {
						link = "OK"
						for _, e := range active {
							if !pathExists(filepath.Join(e.ResolvedPath, ".agents", "memory")) {
								link = "MISSING"
							}
						}
					} else if len(binding.Entries) > 0 {
						link = "OFFLINE"
					}
				}
			}
			for _, p := range notesGlob(filepath.Join(dir, "memory", "notes")) {
				add(name, p, link)
			}
		}
	}
	return rows
}

func cmdShowMemory(sa showArgs, aikitoDir string, env Environment, stdout, stderr io.Writer) int {
	if sa.projectSet && sa.all {
		fmt.Fprintln(stderr, "[ERROR] Cannot specify both --project and --all.")
		return 1
	}
	resolved := ""
	if sa.projectSet {
		var ok bool
		if resolved, ok = resolveProjectFilter(aikitoDir, sa.project, env, stderr); !ok {
			return 1
		}
	}
	if sa.target == "" {
		includeGlobal := false
		scoped := resolved
		if resolved == "" && !sa.all {
			detected, ok := detectProjectOrConflict(aikitoDir, env, stderr)
			if !ok {
				return 1
			}
			if detected != "" {
				fmt.Fprintf(stdout, "[aikito] Target project: '%s' (detected from cwd)\n", detected)
				scoped, includeGlobal = detected, true
			}
		}
		notes := collectMemoryNotesRows(aikitoDir, env.Home, scoped, includeGlobal)
		var table [][]string
		issue := false
		for i, n := range notes {
			if i > 0 && n[0] != notes[i-1][0] {
				table = append(table, []string{tableSeparator})
			}
			title := n[2]
			if title == "" {
				title = "-"
			}
			table = append(table, []string{n[0], n[1], title, formatStatusBadge(n[3], sa.useUnicode, sa.useColor), n[4]})
			issue = issue || badgeIsIssue(n[3], sa.useUnicode)
		}
		out := buildGenericTable([]string{"Scope", "Note File", "Title", "Link", "Updated"}, table, sa.useUnicode, sa.useColor, []int{2, 1})
		if issue {
			out += "\n\n" + renderLegend(sa.useUnicode, sa.useColor)
		}
		fmt.Fprintln(stdout, out)
		return 0
	}
	path, ok := resolveMemoryTarget(aikitoDir, sa.target, "show", resolved, stderr)
	if !ok {
		return 1
	}
	return printResourceFile(path, "memory note", stdout, stderr)
}

// resolveProjectFilter ports resolve.py resolve_project_filter.
func resolveProjectFilter(aikitoDir, target string, env Environment, stderr io.Writer) (string, bool) {
	clean := strings.TrimSpace(target)
	if strings.ToLower(clean) == "global" {
		return "global", true
	}
	if clean == "." {
		detected, ok := detectProjectOrConflict(aikitoDir, env, stderr)
		if !ok {
			return "", false
		}
		if detected != "" {
			return detected, true
		}
		cwd, _ := workspace.ResolvePath(env.Cwd)
		fmt.Fprintf(stderr, "[ERROR] Current directory is not inside a registered project: %s\n", cwd)
		return "", false
	}
	var registered []string
	for _, n := range sortedDirEntries(filepath.Join(aikitoDir, "projects")) {
		if isDirPath(filepath.Join(aikitoDir, "projects", n)) && !strings.HasPrefix(n, ".") {
			registered = append(registered, n)
		}
	}
	var prefix []string
	for _, n := range registered {
		if n == clean {
			return n, true
		}
		if strings.HasPrefix(n, clean) {
			prefix = append(prefix, n)
		}
	}
	switch len(prefix) {
	case 1:
		return prefix[0], true
	case 0:
		fmt.Fprintf(stderr, "[ERROR] Project '%s' not found.\n", clean)
	default:
		fmt.Fprintf(stderr, "[CONFLICT] Multiple projects match '%s': %s\n", clean, strings.Join(prefix, ", "))
	}
	return "", false
}

type memoryFileItem struct {
	scope, relNoExt, full string
}

func (m memoryFileItem) stem() string { return strings.TrimSuffix(filepath.Base(m.full), ".md") }
func (m memoryFileItem) shortID() string {
	return m.scope + "/" + m.stem()
}
func (m memoryFileItem) fullID() string { return m.scope + "/" + m.relNoExt }

// findMemoryFiles ports memory.py find_memory_files.
func findMemoryFiles(aikitoDir, proj string) []memoryFileItem {
	var items []memoryFileItem
	if proj == "" || strings.ToLower(proj) == "global" {
		for _, p := range notesGlob(filepath.Join(aikitoDir, "memory", "notes")) {
			if isRegularFilePath(p) {
				items = append(items, memoryFileItem{"global", "notes/" + strings.TrimSuffix(filepath.Base(p), ".md"), p})
			}
		}
	}
	if proj == "" || strings.ToLower(proj) != "global" {
		for _, name := range sortedDirEntries(filepath.Join(aikitoDir, "projects")) {
			dir := filepath.Join(aikitoDir, "projects", name)
			if !isDirPath(dir) || (proj != "" && name != proj) {
				continue
			}
			for _, p := range notesGlob(filepath.Join(dir, "memory", "notes")) {
				if isRegularFilePath(p) {
					items = append(items, memoryFileItem{name, "notes/" + strings.TrimSuffix(filepath.Base(p), ".md"), p})
				}
			}
		}
	}
	return items
}

// resolveMemoryTarget ports memory.py resolve_memory_target_for_command.
func resolveMemoryTarget(aikitoDir, target, operation, proj string, stderr io.Writer) (string, bool) {
	raw := strings.TrimSuffix(strings.TrimSpace(target), "…")
	norm := strings.TrimSuffix(raw, ".md")
	items := findMemoryFiles(aikitoDir, proj)
	keys := func(m memoryFileItem) []string {
		if strings.Contains(norm, "/") {
			return []string{m.shortID(), m.fullID()}
		}
		return []string{m.stem()}
	}
	var matched []memoryFileItem
	for _, m := range items {
		if containsString(keys(m), norm) {
			matched = append(matched, m)
		}
	}
	if len(matched) == 0 {
		for _, m := range items {
			for _, k := range keys(m) {
				if strings.HasPrefix(k, norm) {
					matched = append(matched, m)
					break
				}
			}
		}
	}
	switch len(matched) {
	case 1:
		return matched[0].full, true
	case 0:
		if proj != "" {
			fmt.Fprintf(stderr, "[ERROR] Memory note '%s' not found in project '%s'.\n", target, proj)
			fmt.Fprintf(stderr, "Run 'aikito show memory --project %s' to view available memory files.\n", proj)
		} else {
			fmt.Fprintf(stderr, "[ERROR] Memory note '%s' not found.\n", target)
			fmt.Fprintln(stderr, "Run 'aikito show memory' to view available memory files.")
		}
		return "", false
	}
	fmt.Fprintf(stderr, "[CONFLICT] Multiple memory notes match '%s':\n\n", target)
	for _, m := range matched {
		fmt.Fprintf(stderr, "  - %s\n", m.fullID())
	}
	fmt.Fprintln(stderr, "\nPlease specify the full identifier, e.g.:")
	for _, m := range matched {
		fmt.Fprintf(stderr, "  aikito %s memory %s\n", operation, m.fullID())
	}
	return "", false
}
