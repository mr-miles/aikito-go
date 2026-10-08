package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/mr-miles/aikito-go/internal/compat"
	"github.com/mr-miles/aikito-go/internal/linkplan"
	"github.com/mr-miles/aikito-go/internal/project"
	"github.com/mr-miles/aikito-go/internal/projectsync"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// projectResourceDetail ports project.py's ProjectResourceDetail.
type projectResourceDetail struct {
	Resource, CanonicalPath, RuntimePath, Status, Detail string
}

// projectSummary ports project.py's ProjectSummary.
type projectSummary struct {
	Name, Path, SyncMode, InstructionsStatus string
	SkillsCount, MemoryNotesCount            int
	RuntimeStatus, ConfigPath, Description   string
	SkillNames, MemoryRefs                   []string
	Details                                  []projectResourceDetail
	InstructionsNotice, SkillsNotice, Error  string
	CandidatePaths                           []project.CandidateView
	ContextTokens                            int
}

func (p projectSummary) hasConflict() bool {
	if p.RuntimeStatus == "CONFLICT" {
		return true
	}
	for _, d := range p.Details {
		if d.Status == "CONFLICT" {
			return true
		}
	}
	return false
}

func (p projectSummary) hasCopiedSkillDrift() bool {
	if p.SyncMode != "copy" {
		return false
	}
	for _, d := range p.Details {
		if strings.HasPrefix(d.Resource, "Skills") && d.Status == "DRIFT" && !strings.HasPrefix(d.Detail, "Deselected managed skill") {
			return true
		}
	}
	return false
}

func (p projectSummary) isSyncFixable() bool {
	if p.RuntimeStatus != "MISSING" && p.RuntimeStatus != "DRIFT" {
		return false
	}
	return !p.hasConflict() && !p.hasCopiedSkillDrift()
}

func (p projectSummary) fixAction() string {
	switch {
	case p.hasConflict():
		return "aikito show project " + p.Name
	case p.hasCopiedSkillDrift():
		return "aikito diff project " + p.Name
	case p.isSyncFixable():
		return "aikito sync project " + p.Name
	}
	return ""
}

func (p projectSummary) fixHint() string {
	switch {
	case p.hasConflict():
		return "Remove unmanaged files from .agents/ or reconcile conflicting resources"
	case p.hasCopiedSkillDrift():
		return fmt.Sprintf("Run 'aikito diff project %s' to review changes, then 'aikito sync project %s --force' after review", p.Name, p.Name)
	case p.isSyncFixable():
		return fmt.Sprintf("Run 'aikito sync project %s' (or 'aikito sync') to reconcile runtime", p.Name)
	}
	return ""
}

// --- context_footprint.py ---

var alwaysLoadedSkills = map[string]bool{"durable-memory": true}

func readTextReplace(p string) (string, bool) {
	data, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return strings.ToValidUTF8(string(data), "�"), true
}

func isRegularFilePath(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// extractSkillDescription ports context_footprint.extract_skill_description.
func extractSkillDescription(skillDir string) (string, bool) {
	md := filepath.Join(skillDir, "SKILL.md")
	if !isRegularFilePath(md) {
		return "", false
	}
	data, err := os.ReadFile(md)
	if err != nil {
		return "", false
	}
	// read_text(errors="ignore") drops invalid bytes, as ToValidUTF8 with "" does.
	meta, _ := workspace.ParseMarkdownFrontmatter(strings.ToValidUTF8(string(data), ""), nil)
	if d, ok := meta["description"].(string); ok {
		if d = workspace.PyStrip(d); d != "" {
			return d, true
		}
	}
	return "", false
}

func skillContextBytes(name, skillsDir string) int {
	dir := filepath.Join(skillsDir, name)
	md := filepath.Join(dir, "SKILL.md")
	if !isRegularFilePath(md) {
		return 0
	}
	if alwaysLoadedSkills[name] {
		text, ok := readTextReplace(md)
		if !ok || strings.TrimSpace(text) == "" {
			return 0
		}
		return len(text)
	}
	if d, ok := extractSkillDescription(dir); ok {
		return len(name + ": " + d)
	}
	return len(name)
}

type globalContextCache struct {
	instructionBytes int
	skills           []string
	skillBytes       map[string]int
}

func loadGlobalContextCache(aikitoDir string) globalContextCache {
	c := globalContextCache{skillBytes: map[string]int{}}
	if text, ok := readTextReplace(filepath.Join(aikitoDir, "global", "AGENTS.md")); ok && isRegularFilePath(filepath.Join(aikitoDir, "global", "AGENTS.md")) && strings.TrimSpace(text) != "" {
		c.instructionBytes = len(text)
	}
	if isRegularFilePath(filepath.Join(aikitoDir, "skills.toml")) {
		c.skills = globalSkillsList(aikitoDir, nil)
	}
	for _, s := range c.skills {
		c.skillBytes[s] = skillContextBytes(s, filepath.Join(aikitoDir, "skills"))
	}
	return c
}

func estimateProjectContext(aikitoDir, projectDir string, projectSkills []string, cache globalContextCache) int {
	total := cache.instructionBytes
	if text, ok := readTextReplace(filepath.Join(projectDir, "AGENTS.md")); ok && isRegularFilePath(filepath.Join(projectDir, "AGENTS.md")) && strings.TrimSpace(text) != "" {
		total += len(text)
	}
	effective := map[string]bool{}
	for _, s := range cache.skills {
		effective[s] = true
	}
	for _, s := range projectSkills {
		effective[s] = true
	}
	for s := range effective {
		if b, ok := cache.skillBytes[s]; ok {
			total += b
		} else {
			total += skillContextBytes(s, filepath.Join(aikitoDir, "skills"))
		}
	}
	if total <= 0 {
		return 0
	}
	return (total + 3) / 4
}

// --- project.py runtime helpers ---

func linkStatusOf(target, expected string) string {
	if isSymlinkPath(target) {
		if compat.PhysicalPath(target) == compat.PhysicalPath(expected) {
			return "OK"
		}
		return "CONFLICT"
	}
	if _, err := os.Stat(target); err == nil {
		return "CONFLICT"
	}
	return "MISSING"
}

func linkIssueOf(target, expected, status string) string {
	switch status {
	case "MISSING":
		return "Missing " + target
	case "CONFLICT":
		return fmt.Sprintf("Expected %s to link to %s", target, expected)
	}
	return ""
}

func aggregateRuntimeStatus(statuses []string) string {
	for _, s := range []string{"CONFLICT", "DRIFT", "MISSING"} {
		if containsString(statuses, s) {
			return s
		}
	}
	return "OK"
}

func symlinkPointsWithin(path string, expected []string) bool {
	if !isSymlinkPath(path) {
		return false
	}
	target := linkplan.ResolveSymlinkTarget(path)
	fallback := compat.PhysicalPath(path)
	for _, e := range expected {
		re := compat.PhysicalPath(e)
		if target == re || fallback == re {
			return true
		}
	}
	return false
}

// fileInventory ports project._file_inventory.
func fileInventory(root string) (map[string]string, string) {
	files := map[string]string{}
	if !isDirPath(root) {
		return files, "Skill path is not a directory: " + root
	}
	errText := ""
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			errText = "Symbolic links are not supported inside copied skills: " + p
			return filepath.SkipAll
		case info.Mode().IsRegular():
			files[filepath.ToSlash(rel)] = p
		case info.IsDir():
		default:
			errText = "Unsupported filesystem entry: " + p
			return filepath.SkipAll
		}
		return nil
	})
	if errText != "" {
		return map[string]string{}, errText
	}
	return files, ""
}

func directoriesMatch(canonical, runtime string) (bool, string) {
	cf, cerr := fileInventory(canonical)
	if cerr != "" {
		return false, cerr
	}
	rf, rerr := fileInventory(runtime)
	if rerr != "" {
		return false, rerr
	}
	if len(cf) != len(rf) {
		return false, ""
	}
	for k, cp := range cf {
		rp, ok := rf[k]
		if !ok {
			return false, ""
		}
		a, err1 := os.ReadFile(cp)
		b, err2 := os.ReadFile(rp)
		if err1 != nil || err2 != nil {
			if err1 != nil {
				return false, err1.Error()
			}
			return false, err2.Error()
		}
		if string(a) != string(b) {
			return false, ""
		}
	}
	return true, ""
}

func sortedDirEntries(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func planRuntimeCleanup(runtimeDir string, selected map[string]bool, roots []string, allowMatchingCopies bool) (cleanup, conflicts []string) {
	if !isDirPath(runtimeDir) {
		return nil, nil
	}
	for _, name := range sortedDirEntries(runtimeDir) {
		if selected[name] {
			continue
		}
		item := filepath.Join(runtimeDir, name)
		var expected []string
		for _, r := range roots {
			expected = append(expected, filepath.Join(r, name))
		}
		owned := symlinkPointsWithin(item, expected)
		if !owned && allowMatchingCopies && isDirPath(item) {
			canonical := filepath.Join(roots[0], name)
			if isDirPath(canonical) {
				m, e := directoriesMatch(canonical, item)
				owned = e == "" && m
			}
		}
		if owned {
			cleanup = append(cleanup, item)
		} else {
			conflicts = append(conflicts, item)
		}
	}
	return cleanup, conflicts
}

func findSelectedRuntimeConflicts(runtimeDir string, selected []string, canonicalRoot string, allowDriftedCopies bool) []string {
	var out []string
	for _, name := range selected {
		target := filepath.Join(runtimeDir, name)
		if _, err := os.Lstat(target); err != nil {
			continue
		}
		if symlinkPointsWithin(target, []string{filepath.Join(canonicalRoot, name)}) {
			continue
		}
		if isDirPath(target) {
			if allowDriftedCopies {
				continue
			}
			canonical := filepath.Join(canonicalRoot, name)
			if isDirPath(canonical) {
				if m, e := directoriesMatch(canonical, target); e == "" && m {
					continue
				}
			}
		}
		out = append(out, target)
	}
	return out
}

// copiedSkillState ports classify_project_skill_state (status, reason).
func copiedSkillState(aikitoDir, home, projectName, checkout, skill string) (string, string) {
	if filepath.Base(skill) != skill || skill == "" || skill == "." || skill == ".." {
		return "CONFLICT", "Skill name must be a single path component"
	}
	runtime := filepath.Join(checkout, ".agents", "skills", skill)
	target := projectsync.SkillTarget{
		WorkspaceRoot: aikitoDir, WorkspaceID: filepath.Base(aikitoDir), ProjectName: projectName,
		PhysicalCheckout: checkout, SkillName: skill, TargetPath: runtime,
	}
	observed, desired := projectsync.InspectSkillTarget(target, "copy", home)
	op := projectsync.PlanSingleSkill(target, desired, observed, false, false)
	switch {
	case op.Action == "NOOP" || op.Action == "RECONCILE_STATE":
		return "OK", ""
	case op.Action == "CREATE":
		return "MISSING", "Runtime skill is missing"
	case op.RuleID == "INV-TR-08":
		return "UPDATE", "Canonical skill updated upstream; safe to sync without --force"
	case op.RuleID == "INV-TR-20" && op.Action == "UPDATE":
		return "UPDATE", op.Reason
	case op.RuleID == "INV-TR-09" || op.RuleID == "INV-TR-12" || op.RuleID == "INV-TR-19":
		return "DRIFT", "Copied project skill drifted from workspace skill"
	case op.RuleID == "INV-TR-14":
		if observed.CanonicalError != "" && (strings.Contains(observed.CanonicalError, "does not exist") || strings.Contains(strings.ToLower(observed.CanonicalError), "missing")) {
			return "MISSING", "Canonical skill is missing"
		}
		return "CONFLICT", op.Reason
	case op.RuleID == "INV-TR-13":
		if observed.EntryType == "unsupported" {
			return "CONFLICT", "Runtime skill is not a directory"
		}
		return "CONFLICT", op.Reason
	case op.Action == "CONFLICT":
		return "CONFLICT", op.Reason
	}
	return op.Action, op.Reason
}

func lowerStr(v any, fallback string) string {
	if v == nil {
		return fallback
	}
	return strings.ToLower(pyStr(v))
}

func listStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		out = append(out, pyStr(x))
	}
	return out
}

// collectProjectSummaries ports project.py collect_project_summaries.
func collectProjectSummaries(aikitoDir, home string) []projectSummary {
	var out []projectSummary
	projectsDir := filepath.Join(aikitoDir, "projects")
	if !isDirPath(projectsDir) {
		return out
	}
	cache := loadGlobalContextCache(aikitoDir)
	for _, name := range sortedDirEntries(projectsDir) {
		projectDir := filepath.Join(projectsDir, name)
		if !isDirPath(projectDir) || strings.HasPrefix(name, ".") {
			continue
		}
		configPath := filepath.Join(projectDir, "agent.toml")
		var cfg map[string]any
		data, err := os.ReadFile(configPath)
		if err == nil {
			err = toml.Unmarshal(data, &cfg)
		}
		if err != nil {
			out = append(out, projectSummary{
				Name: name, Path: "-", SyncMode: "-", InstructionsStatus: "MISSING",
				RuntimeStatus: "INVALID CONFIG", ConfigPath: configPath, Error: pyIOError(err, configPath),
			})
			continue
		}
		binding := project.ResolveProjectBinding(cfg, home)
		candidates := project.CandidatePathViews(binding, home)
		joined := project.JoinedCandidatePaths(candidates)
		syncMode := lowerStr(cfg["sync_mode"], "link")
		if _, ok := cfg["sync_mode"]; !ok {
			syncMode = "link"
		}
		descRaw, hasDesc := cfg["description"]
		description := ""
		if hasDesc {
			s, ok := descRaw.(string)
			if !ok {
				out = append(out, projectSummary{
					Name: name, Path: joined, SyncMode: syncMode, InstructionsStatus: "MISSING",
					RuntimeStatus: "INVALID CONFIG", ConfigPath: configPath,
					Error: "Project description must be a string", CandidatePaths: candidates,
				})
				continue
			}
			description = strings.TrimSpace(s)
		}
		skillNames := listStrings(cfg["skills"])
		sort.Strings(skillNames)
		memoryRefs := listStrings(cfg["memory"])
		sort.Strings(memoryRefs)
		instructions := filepath.Join(projectDir, "AGENTS.md")
		instrStatus := "MISSING"
		if isRegularFilePath(instructions) {
			text, _ := readTextReplace(instructions)
			instrStatus = "EMPTY"
			if strings.TrimSpace(text) != "" {
				instrStatus = "OK"
			}
		}
		notesCount := 0
		notesDir := filepath.Join(projectDir, "memory", "notes")
		if isDirPath(notesDir) {
			_ = filepath.Walk(notesDir, func(p string, info os.FileInfo, err error) error {
				if err == nil && info.Mode().IsRegular() && strings.HasSuffix(p, ".md") {
					notesCount++
				}
				return nil
			})
		}

		var details []projectResourceDetail
		var instrNotices, skillsNotices []string
		runtime := ""
		active := binding.ActiveEntries()
		switch {
		case len(binding.Entries) == 0:
			runtime = "UNBOUND"
		case len(active) == 0:
			runtime = "OFFLINE"
		default:
			multi := len(active) > 1
			var activeStatuses []string
			for _, entry := range active {
				checkout := entry.ResolvedPath
				tag := ""
				if multi {
					tag = " [" + entry.Label + "]"
				}
				agentsDir := filepath.Join(checkout, ".agents")
				var statuses []string
				batch, berr := linkplan.BuildProjectInstructionBatch(aikitoDir, name, []string{checkout}, home, nil, nil)
				if berr == nil {
					plan := linkplan.PlanInstructions(batch, home, false)
					switch instrStatus {
					case "OK":
						for _, op := range plan.Operations {
							st, issue := "", ""
							switch op.Action {
							case linkplan.ActNoop, linkplan.ActSharedPath:
								st = "OK"
							case linkplan.ActCreate:
								st, issue = "MISSING", "Missing "+op.TargetPath
							case linkplan.ActConflict:
								st = "CONFLICT"
								issue = op.Finding
								if issue == "" {
									issue = fmt.Sprintf("Expected %s to link to %s", op.TargetPath, instructions)
								}
							case linkplan.ActSkip:
								st, issue = "SKIP", op.Reason
							default:
								st, issue = op.Action, op.Reason
							}
							statuses = append(statuses, st)
							details = append(details, projectResourceDetail{
								fmt.Sprintf("Instructions (%s)%s", op.ResourceName, tag), instructions, op.TargetPath, st, issue,
							})
						}
					case "EMPTY":
						for _, op := range plan.Operations {
							if op.Action == linkplan.ActUnlink {
								statuses = append(statuses, "DRIFT")
								details = append(details, projectResourceDetail{
									fmt.Sprintf("Instructions (%s)%s", op.ResourceName, tag), instructions, op.TargetPath, "DRIFT",
									fmt.Sprintf("Empty canonical instructions no longer require %s", op.TargetPath),
								})
							} else if op.Action == linkplan.ActNoop && op.ExpectedRepresentation == "file" && filepath.Base(op.TargetPath) == "AGENTS.md" {
								in := ""
								if multi {
									in = " in " + entry.Label
								}
								instrNotices = append(instrNotices, fmt.Sprintf("Project-owned AGENTS.md detected%s: %s (not managed because canonical instructions are empty)", in, op.TargetPath))
							}
						}
					}
				}

				skillsRuntime := filepath.Join(agentsDir, "skills")
				selected := map[string]bool{}
				for _, s := range skillNames {
					selected[s] = true
				}
				skillsRoot := filepath.Join(aikitoDir, "skills")
				selConflicts := findSelectedRuntimeConflicts(skillsRuntime, sortedKeys(selected), skillsRoot, syncMode == "copy")
				cleanup, cleanupConflicts := planRuntimeCleanup(skillsRuntime, selected, []string{skillsRoot}, false)
				var skillIssues []string
				if len(cleanupConflicts) > 0 {
					var names []string
					for _, p := range cleanupConflicts {
						names = append(names, filepath.Base(p))
					}
					skillsNotices = append(skillsNotices, fmt.Sprintf("Project-owned skills detected%s: %s", tag, strings.Join(names, ", ")))
				}
				skillsStatus := ""
				switch {
				case len(selConflicts) > 0:
					skillsStatus = "CONFLICT"
					for _, p := range selConflicts {
						skillIssues = append(skillIssues, "Selected skill conflicts with project-owned entry: "+p)
					}
				case len(cleanup) > 0:
					skillsStatus = "DRIFT"
					for _, p := range cleanup {
						skillIssues = append(skillIssues, "Deselected managed skill: "+p)
					}
				default:
					var skillStatuses []string
					for _, s := range skillNames {
						canonical := filepath.Join(skillsRoot, s)
						rt := filepath.Join(skillsRuntime, s)
						if syncMode == "copy" {
							st, reason := copiedSkillState(aikitoDir, home, name, checkout, s)
							skillStatuses = append(skillStatuses, st)
							if st != "OK" {
								if reason == "" {
									reason = st
								}
								skillIssues = append(skillIssues, s+": "+reason)
							}
						} else {
							st := linkStatusOf(rt, canonical)
							skillStatuses = append(skillStatuses, st)
							if issue := linkIssueOf(rt, canonical, st); issue != "" {
								skillIssues = append(skillIssues, s+": "+issue)
							}
						}
					}
					skillsStatus = aggregateRuntimeStatus(skillStatuses)
				}
				statuses = append(statuses, skillsStatus)
				details = append(details, projectResourceDetail{"Skills" + tag, skillsRoot, skillsRuntime, skillsStatus, strings.Join(skillIssues, "; ")})

				memPlan := projectMemoryPlan(aikitoDir, name, map[string]any{"memory": toAnySlice(memoryRefs)}, checkout)
				var memStatuses, memIssues []string
				for _, op := range memPlan.Operations {
					prefix := ""
					if op.ResourceName != "" {
						prefix = op.ResourceName + ": "
					}
					switch op.Action {
					case linkplan.ActNoop, linkplan.ActSharedPath:
						memStatuses = append(memStatuses, "OK")
					case linkplan.ActCreate:
						memStatuses = append(memStatuses, "MISSING")
						memIssues = append(memIssues, prefix+"target is missing")
					case linkplan.ActUnlink:
						memStatuses = append(memStatuses, "DRIFT")
						memIssues = append(memIssues, "Stale managed memory: "+op.TargetPath)
					case linkplan.ActConflict:
						memStatuses = append(memStatuses, "CONFLICT")
						f := op.Finding
						if f == "" {
							f = op.Reason
						}
						memIssues = append(memIssues, prefix+f)
					case linkplan.ActSkip:
						memStatuses = append(memStatuses, "SKIP")
					}
				}
				memStatus := "OK"
				if len(memStatuses) > 0 {
					memStatus = aggregateRuntimeStatus(memStatuses)
				}
				statuses = append(statuses, memStatus)
				details = append(details, projectResourceDetail{"Memory" + tag, filepath.Join(projectDir, "memory"), filepath.Join(agentsDir, "memory"), memStatus, strings.Join(memIssues, "; ")})
				activeStatuses = append(activeStatuses, aggregateRuntimeStatus(statuses))
			}
			runtime = aggregateRuntimeStatus(activeStatuses)
		}

		out = append(out, projectSummary{
			Name: name, Path: joined, SyncMode: syncMode, InstructionsStatus: instrStatus,
			SkillsCount: len(skillNames), MemoryNotesCount: notesCount, RuntimeStatus: runtime,
			ConfigPath: configPath, Description: description, SkillNames: skillNames, MemoryRefs: memoryRefs,
			Details: details, InstructionsNotice: strings.Join(instrNotices, "\n"),
			SkillsNotice: strings.Join(skillsNotices, "\n"), CandidatePaths: candidates,
			ContextTokens: estimateProjectContext(aikitoDir, projectDir, skillNames, cache),
		})
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// pyIOError approximates str(exc) for the OSError / TOMLDecodeError a
// project config read can raise.
func pyIOError(err error, path string) string {
	if os.IsNotExist(err) {
		return fmt.Sprintf("[Errno 2] No such file or directory: '%s'", path)
	}
	return err.Error()
}

// evaluateProjectHealth ports project.py evaluate_project_health.
func evaluateProjectHealth(p projectSummary, memStatus *string) string {
	if len(p.CandidatePaths) > 0 {
		exists := 0
		for _, c := range p.CandidatePaths {
			if c.Exists {
				exists++
			}
		}
		if exists == 0 {
			return "-"
		}
	} else if p.RuntimeStatus == "OFFLINE" {
		return "-"
	}
	statuses := []string{p.RuntimeStatus, p.InstructionsStatus}
	if memStatus != nil {
		statuses = append(statuses, *memStatus)
	}
	for _, kr := range [][2]string{{"INVALID CONFIG", "invalid config"}, {"CONFLICT", "conflict"}, {"DRIFT", "drift"}, {"MISSING", "missing"}, {"UNBOUND", "unbound"}} {
		for _, s := range statuses {
			if s == kr[0] || (s != "" && strings.HasPrefix(s, kr[0]+" ")) {
				return "! " + kr[1]
			}
		}
	}
	return "OK"
}

// formatProjectPathCounts ports format_project_path_counts.
func formatProjectPathCounts(p projectSummary) string {
	if len(p.CandidatePaths) > 0 {
		seen := map[string]bool{}
		var order []string
		for _, c := range p.CandidatePaths {
			if _, ok := seen[c.Display]; !ok {
				order = append(order, c.Display)
				seen[c.Display] = c.Exists
			} else {
				seen[c.Display] = seen[c.Display] || c.Exists
			}
		}
		active := 0
		for _, k := range order {
			if seen[k] {
				active++
			}
		}
		return fmt.Sprintf("%d/%d", active, len(order))
	}
	if p.Path != "" && p.Path != "-" {
		if _, err := os.Stat(p.Path); err == nil {
			return "1/1"
		}
		return "0/1"
	}
	return "0/0"
}

// renderProjectsTable ports render.py render_projects_table.
func renderProjectsTable(projects []projectSummary, useUnicode, useColor bool, memRows []memoryStatusRow) string {
	var rows [][]string
	for _, p := range projects {
		var memStatus *string
		for i := range memRows {
			if memRows[i].Name == p.Name {
				memStatus = &memRows[i].Status
				break
			}
		}
		badge := formatScopeStatusBadge(evaluateProjectHealth(p, memStatus), useUnicode, useColor)
		instr := "-"
		if p.ConfigPath != "" {
			instr, _ = instructionsLineCountDisplay(filepath.Join(filepath.Dir(p.ConfigPath), "AGENTS.md"))
		}
		rows = append(rows, []string{
			p.Name, instr, fmt.Sprint(p.SkillsCount), fmt.Sprint(p.MemoryNotesCount),
			formatTokenEstimate(p.ContextTokens), formatProjectPathCounts(p), p.SyncMode, badge,
		})
	}
	return buildGenericTable([]string{"Project", "Instr", "Skills", "Memory", "Context", "Paths", "Mode", "Status"}, rows, useUnicode, useColor, []int{0})
}
